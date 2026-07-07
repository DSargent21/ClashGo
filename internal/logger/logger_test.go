package logger

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// makeZerologLine emits a single newline-terminated JSON line resembling what
// zerolog would write in production. Used by tests that need a realistic
// payload (e.g. day-rotation that derives day from ts).
func makeZerologLine(level, msg string) string {
	b, _ := json.Marshal(map[string]any{
		"level":   level,
		"message": msg,
		"time":    time.Now().UTC().Format(time.RFC3339Nano),
	})
	return string(b) + "\n"
}

// TestNDJSONMirror_WritesValidJSONL exercises the ndjsonMirror with its
// drain goroutine running: 5 lines pushed, file contains 5 valid JSON lines.
//
// Close() closes the buffer channel, which causes run() to drain everything
// and exit, but its return is not synchronised with run()'s actual exit.
// We therefore poll the on-disk file with a deadline until all 5 lines appear.
func TestNDJSONMirror_WritesValidJSONL(t *testing.T) {
	dir := t.TempDir()
	m := &ndjsonMirror{dir: dir, buf: make(chan jsonLogEntry, 16)}
	go m.run()

	for i := 0; i < 5; i++ {
		if _, err := m.Write([]byte(makeZerologLine("info", "hello world"))); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}
	if err := m.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	const want = 5
	deadline := time.Now().Add(2 * time.Second)
	var body []byte
	var count int
	for time.Now().Before(deadline) {
		entries, _ := os.ReadDir(dir)
		if len(entries) == 0 {
			time.Sleep(10 * time.Millisecond)
			continue
		}
		body, _ = os.ReadFile(filepath.Join(dir, entries[0].Name()))
		s := bufio.NewScanner(strings.NewReader(string(body)))
		count = 0
		for s.Scan() {
			var msg map[string]any
			if err := json.Unmarshal(s.Bytes(), &msg); err != nil {
				t.Errorf("line %d not valid JSON: %v", count, err)
			}
			count++
		}
		if count >= want {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if count != want {
		t.Errorf("expected %d lines, got %d (file=%q)", want, count, string(body))
	}
}

// TestNDJSONMirror_DropsOnFullChannel verifies that when the channel is full
// (no drainer running) every Write past the buffer cap increments Dropped()
// and never blocks. This is the producer-side backpressure invariant.
func TestNDJSONMirror_DropsOnFullChannel(t *testing.T) {
	dir := t.TempDir()
	m := &ndjsonMirror{dir: dir, buf: make(chan jsonLogEntry, 4)}
	// Deliberately do not call run() — the channel stays full, drops are
	// guaranteed by construction (no goroutine-scheduling dependence).
	t.Cleanup(func() { _ = m.Close() })

	start := time.Now()
	for i := 0; i < 2000; i++ {
		_, _ = m.Write([]byte(`{"level":"info","message":"x"}` + "\n"))
	}
	elapsed := time.Since(start)
	if elapsed > 150*time.Millisecond {
		t.Errorf("Write blocked under backpressure: %v", elapsed)
	}
	if got := m.Dropped(); got == 0 {
		t.Errorf("expected dropped count > 0 with channel cap=4 and 2000 messages; drops=%d", got)
	}
}

// TestNDJSONMirror_RotatesByDay injects two entries with explicit
// timestamps (yesterday + today) and asserts two .ndjson files exist after
// drain.
func TestNDJSONMirror_RotatesByDay(t *testing.T) {
	dir := t.TempDir()
	m := &ndjsonMirror{dir: dir, buf: make(chan jsonLogEntry, 16)}
	go m.run()
	t.Cleanup(func() { _ = m.Close() })

	m.buf <- jsonLogEntry{
		ts:    time.Now().AddDate(0, 0, -1).UnixMilli(),
		bytes: []byte(`{"message":"yesterday"}` + "\n"),
	}
	m.buf <- jsonLogEntry{
		ts:    time.Now().UnixMilli(),
		bytes: []byte(`{"message":"today"}` + "\n"),
	}
	time.Sleep(50 * time.Millisecond)

	entries, _ := os.ReadDir(dir)
	var ndjsonFiles int
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".ndjson") {
			ndjsonFiles++
		}
	}
	if ndjsonFiles < 2 {
		t.Errorf("expected two .ndjson files (today + yesterday), got %d", ndjsonFiles)
	}
}

// TestNDJSONMirror_NonJSONEnqueuesUnchanged ensures non-JSON bytes pass
// through unchanged. After the simplification that removed unmarshal+remarshal,
// we explicitly verify the round-trip preserves bytes.
func TestNDJSONMirror_NonJSONEnqueuesUnchanged(t *testing.T) {
	dir := t.TempDir()
	m := &ndjsonMirror{dir: dir, buf: make(chan jsonLogEntry, 4)}
	t.Cleanup(func() { _ = m.Close() })

	line := "this is not json\n"
	start := time.Now()
	n, err := m.Write([]byte(line))
	if err != nil || n != len(line) {
		t.Errorf("non-json write should pass through: n=%d err=%v", n, err)
	}
	if time.Since(start) > 50*time.Millisecond {
		t.Errorf("non-json write path took too long")
	}
	select {
	case entry := <-m.buf:
		got := strings.TrimRight(string(entry.bytes), "\n")
		want := strings.TrimRight(line, "\n")
		if got != want {
			t.Errorf("bytes modified: got %q, want %q", got, want)
		}
	case <-time.After(time.Second):
		t.Fatal("entry never arrived in buf")
	}
}

// silence unused-import warning: atomic kept for the atomic.Int64 in Dropped().
var _ atomic.Int64
