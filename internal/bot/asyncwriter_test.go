package bot

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Ducky705/ClashGO/internal/paths"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

// drainGlobalWrites must be registered after t.TempDir: cleanups run LIFO, so
// the queued state writes finish before the temporary tree is removed.
func drainGlobalWrites(t *testing.T) {
	t.Helper()
	t.Cleanup(func() { globalAsyncWriter.Close() })
}

func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := log.Logger
	log.Logger = zerolog.New(&buf)
	t.Cleanup(func() { log.Logger = prev })
	return &buf
}

func waitUntil(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func waitForFile(t *testing.T, path, want string) {
	t.Helper()
	waitUntil(t, "file "+path, func() bool {
		data, err := os.ReadFile(path)
		return err == nil && string(data) == want
	})
}

func waitForPendingZero(t *testing.T, aw *AsyncWriter) {
	t.Helper()
	waitUntil(t, "pending writes to drain", func() bool { return aw.pending.Load() == 0 })
}

// A caller must return once the request is queued, not once the disk has it.
func TestAsyncWriterWriteQueuesWithoutWaitingForDisk(t *testing.T) {
	aw := NewAsyncWriter()
	defer aw.Close()

	release := make(chan struct{})
	started := make(chan struct{}, 1)
	aw.write = func(path string, data []byte, perm os.FileMode) error {
		started <- struct{}{}
		<-release
		return paths.WriteFileAtomic(path, data, perm)
	}

	path := filepath.Join(t.TempDir(), "stats.json")
	returned := make(chan error, 1)
	go func() { returned <- aw.Write(path, []byte(`{"attacks":1}`), 0o644) }()

	select {
	case err := <-returned:
		if err != nil {
			t.Fatalf("Write returned error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Write blocked instead of returning once queued")
	}

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("worker never started on the queued write")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("file landed before the worker was released (stat err = %v)", err)
	}
	if got := aw.pending.Load(); got != 1 {
		t.Fatalf("pending = %d while the write is in flight, want 1", got)
	}

	close(release)
	waitForFile(t, path, `{"attacks":1}`)
	waitForPendingZero(t, aw)
}

// Close drains in order, joins, nothing lands afterwards, and the next write
// uses a re-armed worker rather than a direct fallback.
func TestAsyncWriterCloseDrainsInOrderAndReArms(t *testing.T) {
	aw := NewAsyncWriter()

	var mu sync.Mutex
	var written []string
	aw.write = func(path string, data []byte, perm os.FileMode) error {
		mu.Lock()
		written = append(written, filepath.Base(path)+"="+string(data))
		mu.Unlock()
		return paths.WriteFileAtomic(path, data, perm)
	}

	dir := t.TempDir()
	for i, name := range []string{"a.json", "b.json", "c.json"} {
		if err := aw.Write(filepath.Join(dir, name), []byte{byte('0' + i)}, 0o644); err != nil {
			t.Fatalf("Write %s: %v", name, err)
		}
	}

	aw.Close()
	waitForPendingZero(t, aw)

	mu.Lock()
	got := append([]string(nil), written...)
	mu.Unlock()
	want := []string{"a.json=0", "b.json=1", "c.json=2"}
	if len(got) != len(want) {
		t.Fatalf("writes = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("writes = %v, want %v (order changed)", got, want)
		}
	}
	for i, name := range []string{"a.json", "b.json", "c.json"} {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || string(data) != string([]byte{byte('0' + i)}) {
			t.Fatalf("%s = %q (err %v), want %q", name, data, err, string([]byte{byte('0' + i)}))
		}
	}

	// Nothing may land after Close returned.
	mu.Lock()
	countAfterClose := len(written)
	mu.Unlock()
	time.Sleep(50 * time.Millisecond)
	mu.Lock()
	if len(written) != countAfterClose {
		t.Fatalf("%d writes landed after Close returned", len(written)-countAfterClose)
	}
	mu.Unlock()

	// A fresh session after Stop must enqueue again (through the worker), not
	// take the old direct-write path.
	late := filepath.Join(dir, "late.json")
	if err := aw.Write(late, []byte("late"), 0o644); err != nil {
		t.Fatalf("Write after Close returned error: %v", err)
	}
	waitForFile(t, late, "late")
	mu.Lock()
	lateSeen := len(written) == countAfterClose+1
	mu.Unlock()
	if !lateSeen {
		t.Fatal("write after Close did not go through the re-armed worker")
	}
	aw.Close()
}

// WriteSync must not let an older queued snapshot overwrite the newer one it
// was asked to persist, so it drains the queue first.
func TestAsyncWriterWriteSyncWaitsForQueuedWrites(t *testing.T) {
	aw := NewAsyncWriter()
	defer aw.Close()

	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	var mu sync.Mutex
	var order []string
	aw.write = func(path string, data []byte, perm os.FileMode) error {
		mu.Lock()
		order = append(order, string(data))
		mu.Unlock()
		if string(data) == "queued" {
			entered <- struct{}{}
			<-release
		}
		return paths.WriteFileAtomic(path, data, perm)
	}

	path := filepath.Join(t.TempDir(), "stats.json")
	if err := aw.Write(path, []byte("queued"), 0o644); err != nil {
		t.Fatalf("Write: %v", err)
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("worker never started the queued write")
	}

	syncDone := make(chan error, 1)
	go func() { syncDone <- aw.WriteSync(path, []byte("sync"), 0o644) }()
	select {
	case <-syncDone:
		t.Fatal("WriteSync returned while the queued write was still pending")
	case <-time.After(50 * time.Millisecond):
	}

	close(release)
	if err := <-syncDone; err != nil {
		t.Fatalf("WriteSync: %v", err)
	}
	waitForFile(t, path, "sync")

	mu.Lock()
	got := append([]string(nil), order...)
	mu.Unlock()
	if len(got) != 2 || got[0] != "queued" || got[1] != "sync" {
		t.Fatalf("write order = %v, want [queued sync]", got)
	}
}

// WriteSync is the explicit on-disk path and must be on disk when it returns.
func TestAsyncWriterWriteSyncIsImmediate(t *testing.T) {
	aw := NewAsyncWriter()
	defer aw.Close()

	path := filepath.Join(t.TempDir(), "now.json")
	if err := aw.WriteSync(path, []byte("now"), 0o644); err != nil {
		t.Fatalf("WriteSync: %v", err)
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != "now" {
		t.Fatalf("WriteSync did not write immediately: %q (err %v)", data, err)
	}
}

// A failed flush is logged with the target path and the cause.
func TestAsyncWriterFailedFlushIsLogged(t *testing.T) {
	buf := captureLog(t)
	aw := NewAsyncWriter()
	aw.write = func(string, []byte, os.FileMode) error { return errors.New("disk full") }

	path := filepath.Join(t.TempDir(), "stats.json")
	if err := aw.Write(path, []byte("x"), 0o644); err != nil {
		t.Fatalf("Write: %v", err)
	}
	aw.Close()
	waitForPendingZero(t, aw)

	out := buf.String()
	if !strings.Contains(out, path) || !strings.Contains(out, "disk full") {
		t.Fatalf("failed flush not logged with path and cause: %q", out)
	}
}

// A stuck disk must not hang shutdown: Close returns within its bound and says
// how many writes it abandoned.
func TestAsyncWriterStopIsBoundedAndReportsAbandoned(t *testing.T) {
	buf := captureLog(t)
	aw := NewAsyncWriter()
	aw.stopWait = 100 * time.Millisecond

	release := make(chan struct{})
	entered := make(chan struct{}, 1)
	aw.write = func(string, []byte, os.FileMode) error {
		entered <- struct{}{}
		<-release
		return nil
	}

	if err := aw.Write(filepath.Join(t.TempDir(), "stuck.json"), []byte("x"), 0o644); err != nil {
		t.Fatalf("Write: %v", err)
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("worker never entered the write")
	}

	start := time.Now()
	aw.Close()
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("Close blocked for %v, want a bounded wait", elapsed)
	}
	if !strings.Contains(buf.String(), "abandoning queued writes") {
		t.Fatalf("abandoned writes were not reported: %q", buf.String())
	}

	close(release)
	waitForPendingZero(t, aw)
}

// Repeated sessions must not leave a worker goroutine behind.
func TestAsyncWriterSessionsDoNotLeakGoroutines(t *testing.T) {
	aw := NewAsyncWriter()
	base := runtime.NumGoroutine()

	dir := t.TempDir()
	for i := 0; i < 20; i++ {
		aw.Start()
		path := filepath.Join(dir, "state.json")
		if err := aw.Write(path, []byte("x"), 0o644); err != nil {
			t.Fatalf("cycle %d: Write: %v", i, err)
		}
		aw.Close()
	}

	waitUntil(t, "worker goroutines to exit", func() bool {
		return runtime.NumGoroutine() <= base
	})
}

func writerState(aw *AsyncWriter) (running, closed bool) {
	aw.mu.Lock()
	defer aw.mu.Unlock()
	return aw.running, aw.closed
}

// A Write that races Close is either drained (if it queued first) or rejected
// with ErrWriterStopping and a logged path; it never panics, deadlocks, or
// touches the disk after the rejection.
func TestAsyncWriterWriteRacingCloseIsRejectedOrDrained(t *testing.T) {
	buf := captureLog(t)
	aw := NewAsyncWriter()

	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	aw.write = func(path string, data []byte, perm os.FileMode) error {
		entered <- struct{}{}
		<-release
		return paths.WriteFileAtomic(path, data, perm)
	}

	dir := t.TempDir()
	drained := filepath.Join(dir, "drained.json")
	rejected := filepath.Join(dir, "rejected.json")
	if err := aw.Write(drained, []byte("drained"), 0o644); err != nil {
		t.Fatalf("queued Write: %v", err)
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("worker never started")
	}

	closeDone := make(chan struct{})
	go func() { defer close(closeDone); aw.Close() }()
	waitUntil(t, "Close to start draining", func() bool {
		_, closed := writerState(aw)
		return closed
	})

	if err := aw.Write(rejected, []byte("rejected"), 0o644); !errors.Is(err, ErrWriterStopping) {
		t.Fatalf("Write during drain = %v, want ErrWriterStopping", err)
	}
	if !strings.Contains(buf.String(), rejected) || !strings.Contains(buf.String(), "stopping") {
		t.Fatalf("rejected write not logged with its path: %q", buf.String())
	}

	close(release)
	select {
	case <-closeDone:
	case <-time.After(2 * time.Second):
		t.Fatal("Close deadlocked with a racing Write")
	}

	waitForFile(t, drained, "drained")
	if _, err := os.Stat(rejected); !os.IsNotExist(err) {
		t.Fatalf("rejected write touched the disk (stat err = %v)", err)
	}
}

// Double Close is idempotent, Start during the drain does not reopen the queue,
// and the drain still completes with every queued byte.
func TestAsyncWriterDoubleCloseAndStartWhileDraining(t *testing.T) {
	aw := NewAsyncWriter()

	var mu sync.Mutex
	var active, maxActive int
	var payloads []string
	release := make(chan struct{})
	entered := make(chan struct{}, 1)
	aw.write = func(path string, data []byte, perm os.FileMode) error {
		mu.Lock()
		active++
		if active > maxActive {
			maxActive = active
		}
		payloads = append(payloads, string(data))
		mu.Unlock()
		select {
		case entered <- struct{}{}:
		default:
		}
		<-release
		err := paths.WriteFileAtomic(path, data, perm)
		mu.Lock()
		active--
		mu.Unlock()
		return err
	}

	dir := t.TempDir()
	a, b := filepath.Join(dir, "a.json"), filepath.Join(dir, "b.json")
	if err := aw.Write(a, []byte("a"), 0o644); err != nil {
		t.Fatalf("Write a: %v", err)
	}
	if err := aw.Write(b, []byte("b"), 0o644); err != nil {
		t.Fatalf("Write b: %v", err)
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("worker never started")
	}

	firstClose := make(chan struct{})
	go func() { defer close(firstClose); aw.Close() }()
	waitUntil(t, "first Close to start draining", func() bool {
		_, closed := writerState(aw)
		return closed
	})
	queue := aw.requests

	// A second Close joins the same drain: it must not close the queue again
	// (panic) and must not return while bytes are still pending.
	secondClose := make(chan struct{})
	go func() { defer close(secondClose); aw.Close() }()
	select {
	case <-secondClose:
		t.Fatal("second Close returned while the drain was still pending")
	case <-time.After(50 * time.Millisecond):
	}

	// Start during the drain must not reopen the closed queue.
	aw.Start()
	running, closed := writerState(aw)
	if !running || !closed || aw.requests != queue {
		t.Fatalf("Start during drain changed the writer state: running=%v closed=%v reopened=%v",
			running, closed, aw.requests != queue)
	}

	close(release)
	select {
	case <-firstClose:
	case <-time.After(2 * time.Second):
		t.Fatal("first Close did not finish")
	}
	select {
	case <-secondClose:
	case <-time.After(2 * time.Second):
		t.Fatal("second Close did not return after the drain finished")
	}
	waitForPendingZero(t, aw)
	waitForFile(t, a, "a")
	waitForFile(t, b, "b")

	mu.Lock()
	if len(payloads) != 2 || payloads[0] != "a" || payloads[1] != "b" {
		t.Fatalf("payload order = %v, want [a b]", payloads)
	}
	if maxActive != 1 {
		t.Fatalf("max concurrent workers = %d, want 1", maxActive)
	}
	before := len(payloads)
	mu.Unlock()

	// Lifecycle calls after Close must not write through the queue.
	aw.Start()
	aw.Close()
	aw.Close()
	mu.Lock()
	after := len(payloads)
	mu.Unlock()
	if after != before {
		t.Fatalf("%d writes happened through Close/Start lifecycle calls", after-before)
	}

	// The stopped writer is still usable: a fresh write re-arms one worker.
	c := filepath.Join(dir, "c.json")
	if err := aw.Write(c, []byte("c"), 0o644); err != nil {
		t.Fatalf("Write after Close: %v", err)
	}
	waitForFile(t, c, "c")
	aw.Close()
}

// After Close abandons a stuck worker, Start/Write cannot create a second
// worker or reorder a newer flush ahead of the abandoned payload; the queue
// stays single and ordered, and the writer re-arms only after the old worker
// exits.
func TestAsyncWriterStartAfterAbandonedCloseStaysSingleAndOrdered(t *testing.T) {
	buf := captureLog(t)
	aw := NewAsyncWriter()
	aw.stopWait = 50 * time.Millisecond

	var mu sync.Mutex
	var active, maxActive int
	var payloads []string
	release := make(chan struct{})
	entered := make(chan struct{}, 1)
	blocked := true
	aw.write = func(path string, data []byte, perm os.FileMode) error {
		mu.Lock()
		active++
		if active > maxActive {
			maxActive = active
		}
		payloads = append(payloads, string(data))
		shouldBlock := blocked
		mu.Unlock()
		if shouldBlock {
			select {
			case entered <- struct{}{}:
			default:
			}
			<-release
		}
		err := paths.WriteFileAtomic(path, data, perm)
		mu.Lock()
		active--
		mu.Unlock()
		return err
	}

	dir := t.TempDir()
	old := filepath.Join(dir, "stats.json")
	if err := aw.Write(old, []byte("old"), 0o644); err != nil {
		t.Fatalf("Write: %v", err)
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("worker never entered the write")
	}

	aw.Close() // times out and abandons
	if !strings.Contains(buf.String(), "abandoning queued writes") {
		t.Fatalf("abandon not reported: %q", buf.String())
	}
	running, closed := writerState(aw)
	if !running || !closed {
		t.Fatalf("abandoned writer state running=%v closed=%v, want running+draining", running, closed)
	}

	queue := aw.requests
	aw.Start()
	if running, closed := writerState(aw); !running || !closed || aw.requests != queue {
		t.Fatalf("Start after abandon reopened the queue or dropped the drain state")
	}

	if err := aw.Write(filepath.Join(dir, "rejected.json"), []byte("new"), 0o644); !errors.Is(err, ErrWriterStopping) {
		t.Fatalf("Write while abandoned = %v, want ErrWriterStopping", err)
	}
	if err := aw.WriteSync(filepath.Join(dir, "sync.json"), []byte("sync"), 0o644); !errors.Is(err, ErrFlushTimeout) {
		t.Fatalf("WriteSync with an abandoned worker = %v, want ErrFlushTimeout", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "sync.json")); !os.IsNotExist(err) {
		t.Fatalf("timed-out WriteSync wrote a newer payload that could be overwritten")
	}

	close(release)
	waitForPendingZero(t, aw)
	waitUntil(t, "abandoned worker to exit", func() bool {
		running, _ := writerState(aw)
		return !running
	})
	waitForFile(t, old, "old")

	newer := filepath.Join(dir, "stats.json")
	if err := aw.Write(newer, []byte("new"), 0o644); err != nil {
		t.Fatalf("Write after abandoned worker exited: %v", err)
	}
	waitForFile(t, newer, "new")
	aw.Close()

	mu.Lock()
	defer mu.Unlock()
	if len(payloads) != 2 || payloads[0] != "old" || payloads[1] != "new" {
		t.Fatalf("payload order = %v, want [old new]", payloads)
	}
	if maxActive != 1 {
		t.Fatalf("max concurrent workers = %d, want 1", maxActive)
	}
}

// After a normal Close returns, no hook call may touch a file, including from
// repeated Close/Start calls.
func TestAsyncWriterNoFileWritesAfterClose(t *testing.T) {
	aw := NewAsyncWriter()

	var mu sync.Mutex
	var calls []string
	aw.write = func(path string, data []byte, perm os.FileMode) error {
		mu.Lock()
		calls = append(calls, string(data))
		mu.Unlock()
		return paths.WriteFileAtomic(path, data, perm)
	}

	path := filepath.Join(t.TempDir(), "state.json")
	if err := aw.Write(path, []byte("one"), 0o644); err != nil {
		t.Fatalf("Write: %v", err)
	}
	aw.Close()
	waitForFile(t, path, "one")

	mu.Lock()
	before := len(calls)
	mu.Unlock()

	// These lifecycle calls must not enqueue or write anything.
	aw.Start()
	aw.Close()
	aw.Close()

	mu.Lock()
	after := len(calls)
	mu.Unlock()
	if after != before {
		t.Fatalf("%d writes occurred after Close returned", after-before)
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != "one" {
		t.Fatalf("file changed after Close: %q (err %v)", data, err)
	}
}
