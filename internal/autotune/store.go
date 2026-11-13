package autotune

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// The log is a plain NDJSON file beside config.json: append-only, one sample
// per line, so a crash mid-write costs at most the last line and the file can
// be inspected (or trimmed) with any text tool.
const (
	// MaxSamples caps the file. At one line per searched base that is several
	// sessions of history, far past the point where more data changes the
	// recommendation.
	MaxSamples = 5000
	// KeepOnRotate is how much history survives a rotation. Keeping the tail
	// preserves recency, which is what the recommendation should follow: loot
	// availability moves as the player's trophy range and town hall change.
	KeepOnRotate = 2500
)

// Store is the on-disk search sample log plus an in-memory copy.
//
// One process writes it (the bot) and one reads it (the UI), so the store is
// mutex-guarded rather than file-locked. A reader that opens the file
// concurrently sees whole lines only, because every write is a single line and
// every rotation is an atomic rename.
type Store struct {
	mu        sync.Mutex
	path      string
	samples   []Sample
	loaded    bool
	malformed int
}

// Open returns a store rooted at path. It does not touch the filesystem; the
// first Samples or Append call loads the file.
func Open(path string) *Store {
	return &Store{path: path}
}

// Path reports the file the store reads and writes.
func (s *Store) Path() string { return s.path }

// Append records one sample in memory and on disk.
//
// Disk errors are returned but deliberately not fatal to the caller: losing a
// tuning sample must never interrupt an attack session.
func (s *Store) Append(sample Sample) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.loadLocked()
	s.samples = append(s.samples, sample)

	if s.path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return fmt.Errorf("autotune: create dir: %w", err)
	}
	line, err := json.Marshal(sample)
	if err != nil {
		return fmt.Errorf("autotune: marshal sample: %w", err)
	}
	f, err := os.OpenFile(s.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("autotune: open log: %w", err)
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		f.Close()
		return fmt.Errorf("autotune: append sample: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("autotune: close log: %w", err)
	}

	if len(s.samples) > MaxSamples {
		return s.rotateLocked()
	}
	return nil
}

// Samples returns a copy of the recorded samples. A missing file is an empty
// history, not an error: the first run has nothing to say yet.
func (s *Store) Samples() []Sample {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loadLocked()
	out := make([]Sample, len(s.samples))
	copy(out, s.samples)
	return out
}

// Malformed reports how many unreadable lines the last load skipped. Non-zero
// means the file was edited or truncated mid-write; the good lines still count.
func (s *Store) Malformed() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loadLocked()
	return s.malformed
}

// Reset deletes the history. Used by the UI when a player changes town hall and
// wants the recommendation to relearn from the new range instead of averaging
// it with the old.
func (s *Store) Reset() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.samples = nil
	s.loaded = true
	s.malformed = 0
	if s.path == "" {
		return nil
	}
	if err := os.Remove(s.path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("autotune: reset log: %w", err)
	}
	return nil
}

func (s *Store) loadLocked() {
	if s.loaded {
		return
	}
	s.loaded = true
	s.samples = nil
	s.malformed = 0
	if s.path == "" {
		return
	}
	f, err := os.Open(s.path)
	if err != nil {
		return
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var sample Sample
		if err := json.Unmarshal([]byte(line), &sample); err != nil {
			s.malformed++
			continue
		}
		s.samples = append(s.samples, sample)
	}
}

// rotateLocked rewrites the log with the newest KeepOnRotate samples. The
// rename is atomic, so a reader never observes a partially written history.
func (s *Store) rotateLocked() error {
	keep := s.samples
	if len(keep) > KeepOnRotate {
		keep = keep[len(keep)-KeepOnRotate:]
	}
	s.samples = append([]Sample(nil), keep...)

	tmp := s.path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return fmt.Errorf("autotune: rotate: %w", err)
	}
	w := bufio.NewWriter(f)
	for _, sample := range keep {
		line, merr := json.Marshal(sample)
		if merr != nil {
			continue
		}
		if _, werr := w.WriteString(string(line) + "\n"); werr != nil {
			f.Close()
			os.Remove(tmp)
			return fmt.Errorf("autotune: rotate write: %w", werr)
		}
	}
	if err := w.Flush(); err != nil {
		f.Close()
		os.Remove(tmp)
		return fmt.Errorf("autotune: rotate flush: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("autotune: rotate close: %w", err)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("autotune: rotate rename: %w", err)
	}
	return nil
}
