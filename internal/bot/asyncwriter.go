package bot

import (
	"os"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
)

type writeRequest struct {
	path  string
	data  []byte
	perms os.FileMode
	done  chan error
}

type AsyncWriter struct {
	requests chan writeRequest
	wg       sync.WaitGroup
	closed   bool
	mu       sync.Mutex
}

func NewAsyncWriter() *AsyncWriter {
	aw := &AsyncWriter{
		requests: make(chan writeRequest, 100),
	}
	aw.wg.Add(1)
	go aw.worker()
	return aw
}

func (aw *AsyncWriter) worker() {
	defer aw.wg.Done()
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	pending := make([]writeRequest, 0, 10)

	flush := func() {
		for _, req := range pending {
			if err := os.WriteFile(req.path, req.data, req.perms); err != nil {
				log.Error().Err(err).Str("path", req.path).Msg("async write failed")
				if req.done != nil {
					req.done <- err
				}
			} else if req.done != nil {
				req.done <- nil
			}
		}
		pending = pending[:0]
	}

	for {
		select {
		case req, ok := <-aw.requests:
			if !ok {
				flush()
				return
			}
			pending = append(pending, req)
			if len(pending) >= 10 {
				flush()
			}
		case <-ticker.C:
			flush()
		}
	}
}

func (aw *AsyncWriter) Write(path string, data []byte, perms os.FileMode) error {
	aw.mu.Lock()
	if aw.closed {
		aw.mu.Unlock()
		return os.WriteFile(path, data, perms)
	}
	aw.mu.Unlock()

	done := make(chan error, 1)
	select {
	case aw.requests <- writeRequest{path: path, data: data, perms: perms, done: done}:
		return <-done
	default:
		return os.WriteFile(path, data, perms)
	}
}

func (aw *AsyncWriter) Close() {
	aw.mu.Lock()
	if aw.closed {
		aw.mu.Unlock()
		return
	}
	aw.closed = true
	close(aw.requests)
	aw.mu.Unlock()
	aw.wg.Wait()
}

var globalAsyncWriter = NewAsyncWriter()

func AsyncWriteFile(path string, data []byte, perms os.FileMode) error {
	return globalAsyncWriter.Write(path, data, perms)
}
