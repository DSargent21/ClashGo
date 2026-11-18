package bot

import (
	"errors"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Ducky705/ClashGO/internal/paths"
	"github.com/rs/zerolog/log"
)

// asyncQueueSize bounds how many best-effort writes may sit between the
// caller and the worker. These are small JSON snapshots; a backlog this large
// means the disk stalled, not that the bot is generating work faster than it
// can be written.
const asyncQueueSize = 256

// DefaultStopWait bounds Close: a stop must never hang the app on a disk that
// stopped answering. Anything still queued at the deadline is reported.
const DefaultStopWait = 5 * time.Second

var (
	// ErrWriterStopping means the request arrived while the writer was
	// draining; the write was not queued.
	ErrWriterStopping = errors.New("async writer stopping")
	// ErrWriteQueueFull means the backlog is full; the write was not queued.
	ErrWriteQueueFull = errors.New("async write queue full")
	// ErrFlushTimeout means WriteSync gave up waiting for the async backlog
	// (a stalled disk); it did not write, so it cannot interleave with it.
	ErrFlushTimeout = errors.New("timed out waiting for queued writes")
)

type writeRequest struct {
	path  string
	data  []byte
	perms os.FileMode
}

// AsyncWriter serialises best-effort state writes on one background goroutine.
//
//	Write      queues and returns; it never waits for the disk.
//	WriteSync  is the explicit on-disk variant for callers that must block.
//	Close      drains, then joins the worker (bounded), and is re-armable.
//
// Ordering: requests are written in the order they were queued. Data is not
// copied, so a caller must not mutate the slice after Write returns.
type AsyncWriter struct {
	mu       sync.Mutex
	requests chan writeRequest
	done     chan struct{}
	running  bool
	closed   bool
	pending  atomic.Int64

	// write is the atomic primitive the worker uses. A field so tests can
	// observe, block or fail the disk path without touching the real one.
	write    func(path string, data []byte, perm os.FileMode) error
	stopWait time.Duration
}

// NewAsyncWriter builds an idle writer. The worker starts on the first Write
// or Start, so a process that never saves state never carries the goroutine.
func NewAsyncWriter() *AsyncWriter {
	return &AsyncWriter{
		write:    paths.WriteFileAtomic,
		stopWait: DefaultStopWait,
	}
}

// Start arms the worker if it is not running. Safe to call repeatedly.
func (aw *AsyncWriter) Start() {
	aw.mu.Lock()
	aw.startLocked()
	aw.mu.Unlock()
}

func (aw *AsyncWriter) startLocked() {
	if aw.running {
		return
	}
	aw.running = true
	aw.closed = false
	aw.requests = make(chan writeRequest, asyncQueueSize)
	aw.done = make(chan struct{})
	go aw.worker(aw.requests, aw.done)
}

func (aw *AsyncWriter) worker(requests <-chan writeRequest, done chan struct{}) {
	defer close(done)
	for req := range requests {
		if err := aw.atomicWrite(req.path, req.data, req.perms); err != nil {
			log.Error().
				Err(err).
				Str("path", req.path).
				Msg("async write failed; state on disk is unchanged")
		}
		aw.pending.Add(-1)
	}
	aw.mu.Lock()
	aw.running = false
	aw.mu.Unlock()
}

func (aw *AsyncWriter) atomicWrite(path string, data []byte, perm os.FileMode) error {
	fn := aw.write
	if fn == nil {
		fn = paths.WriteFileAtomic
	}
	return fn(path, data, perm)
}

// Write queues a write and returns once it is queued. The error is about
// queueing only (writer stopping, or backlog full); a disk failure is logged by
// the worker because the caller has already moved on. Writers are re-armed on
// demand, so a write after Close starts a fresh session instead of silently
// degrading to a direct synchronous write.
//
// Race with Close: the mutex makes the outcome one of two, never a send on a
// closed queue. A Write that reaches the lock before Close is queued and Close
// drains it; a Write that arrives while Close is draining is rejected with
// ErrWriterStopping (logged with its path) and never touches the disk.
func (aw *AsyncWriter) Write(path string, data []byte, perms os.FileMode) error {
	aw.mu.Lock()
	aw.startLocked()
	if aw.closed {
		aw.mu.Unlock()
		log.Error().Str("path", path).Msg("async write dropped: writer is stopping")
		return ErrWriterStopping
	}
	aw.pending.Add(1)
	select {
	case aw.requests <- writeRequest{path: path, data: data, perms: perms}:
		aw.mu.Unlock()
		return nil
	default:
		aw.pending.Add(-1)
		aw.mu.Unlock()
		log.Error().Str("path", path).Msg("async write dropped: queue full")
		return ErrWriteQueueFull
	}
}

// WriteSync writes through the same atomic primitive on the caller's goroutine.
// Use it when the bytes must be on disk before the next step; Write is the
// normal, non-blocking path. Any writes already queued are drained first, so a
// snapshot flushed at shutdown cannot be overwritten by an older queued one.
//
// The wait is bounded like Close. If it times out the write is NOT performed:
// a queued request may still be in flight, and writing now would let that
// older payload land after this newer one. The caller gets ErrFlushTimeout and
// can decide; ordering is never inverted.
func (aw *AsyncWriter) WriteSync(path string, data []byte, perms os.FileMode) error {
	deadline := time.Now().Add(aw.stopWait)
	for aw.pending.Load() > 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if pending := aw.pending.Load(); pending > 0 {
		log.Error().
			Int64("pending", pending).
			Dur("waited", aw.stopWait).
			Str("path", path).
			Msg("sync write abandoned: queued writes are still landing; refusing to reorder them")
		return ErrFlushTimeout
	}
	return aw.atomicWrite(path, data, perms)
}

// Close stops accepting writes, drains what is already queued, and waits for
// the worker to exit. The wait is bounded: if the disk stops answering it logs
// how many writes were abandoned and returns instead of hanging shutdown. A
// later Write (or Start) re-arms a fresh worker.
//
// Idempotent: a second Close while the first is draining joins the same
// bounded wait instead of closing the queue again; Start during that window is
// a no-op, so nothing reopens or writes through the closed queue.
//
// After a bounded abandon the worker may still be alive. Until it exits, Start
// is a no-op and Write is rejected, so a second worker cannot drain the same
// queue and a late abandoned write cannot land after a newer one.
func (aw *AsyncWriter) Close() {
	aw.mu.Lock()
	if !aw.running {
		aw.mu.Unlock()
		return
	}
	done := aw.done
	if !aw.closed {
		aw.closed = true
		close(aw.requests)
	}
	aw.mu.Unlock()

	select {
	case <-done:
	case <-time.After(aw.stopWait):
		log.Error().
			Int64("pending", aw.pending.Load()).
			Dur("waited", aw.stopWait).
			Msg("async writer stop timed out; abandoning queued writes because the disk did not finish")
	}
}

var globalAsyncWriter = NewAsyncWriter()

// AsyncWriteFile queues a state write. It returns once the request is queued,
// not once it is on disk.
func AsyncWriteFile(path string, data []byte, perms os.FileMode) error {
	return globalAsyncWriter.Write(path, data, perms)
}

// WriteFileSync writes a state file on the caller's goroutine. Explicit on-disk
// path for callers that must not proceed before the bytes land.
func WriteFileSync(path string, data []byte, perms os.FileMode) error {
	return globalAsyncWriter.WriteSync(path, data, perms)
}
