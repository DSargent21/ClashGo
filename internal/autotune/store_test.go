package autotune

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestStore_AppendAndReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "search_samples.jsonl")
	store := Open(path)

	for i := 0; i < 5; i++ {
		if err := store.Append(Sample{At: int64(i), Gold: 1000 * (i + 1)}); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}

	reopened := Open(path)
	got := reopened.Samples()
	if len(got) != 5 {
		t.Fatalf("reloaded %d samples, want 5", len(got))
	}
	if got[4].Gold != 5000 {
		t.Errorf("last sample gold = %d, want 5000", got[4].Gold)
	}
	if reopened.Malformed() != 0 {
		t.Errorf("malformed = %d, want 0", reopened.Malformed())
	}
}

func TestStore_MissingFileIsEmptyHistory(t *testing.T) {
	store := Open(filepath.Join(t.TempDir(), "nope", "search_samples.jsonl"))
	if got := store.Samples(); len(got) != 0 {
		t.Fatalf("got %d samples from a missing file, want 0", len(got))
	}
}

func TestStore_SkipsMalformedLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "search_samples.jsonl")
	body := strings.Join([]string{
		`{"at":1,"gold":100,"elixir":50,"de":5}`,
		`not json at all`,
		`{"at":2,"gold":200,"elixir":100,"de":10}`,
		`{"at":3,"gold":`,
		``,
	}, "\n")
	if err := os.WriteFile(path, []byte(body+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	store := Open(path)
	if got := len(store.Samples()); got != 2 {
		t.Errorf("parsed %d samples, want the 2 well-formed lines", got)
	}
	if got := store.Malformed(); got != 2 {
		t.Errorf("malformed = %d, want 2", got)
	}
}

func TestStore_RotatesKeepingNewest(t *testing.T) {
	path := filepath.Join(t.TempDir(), "search_samples.jsonl")
	store := Open(path)

	for i := 0; i < MaxSamples+10; i++ {
		if err := store.Append(Sample{At: int64(i), Gold: 1000}); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}

	got := store.Samples()
	// Rotation fires once, when the log crosses MaxSamples, and keeps the tail;
	// the appends after it are still there.
	if len(got) < KeepOnRotate || len(got) > KeepOnRotate+10 {
		t.Fatalf("in-memory samples after rotation = %d, want %d..%d", len(got), KeepOnRotate, KeepOnRotate+10)
	}
	if got[len(got)-1].At != int64(MaxSamples+9) {
		t.Errorf("newest sample lost in rotation: last At = %d", got[len(got)-1].At)
	}
	if got[0].At != int64(MaxSamples+10-len(got)) {
		t.Errorf("oldest kept sample = %d, want the tail of the stream", got[0].At)
	}

	lines := readLines(t, path)
	if len(lines) != len(got) {
		t.Fatalf("file has %d lines, memory holds %d", len(lines), len(got))
	}
	for _, line := range lines {
		var s Sample
		if err := json.Unmarshal([]byte(line), &s); err != nil {
			t.Fatalf("rotation wrote a corrupt line: %v", err)
		}
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Error("rotation left its temp file behind")
	}
}

func TestStore_ConcurrentAppendsLoseNothing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "search_samples.jsonl")
	store := Open(path)

	const writers, perWriter = 8, 100
	var wg sync.WaitGroup
	errs := make(chan error, writers)
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < perWriter; i++ {
				if err := store.Append(Sample{At: int64(w*perWriter + i), Gold: 1000}); err != nil {
					errs <- err
					return
				}
			}
		}(w)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("append failed: %v", err)
	}

	if got := len(store.Samples()); got != writers*perWriter {
		t.Errorf("in-memory samples = %d, want %d", got, writers*perWriter)
	}
	lines := readLines(t, path)
	if len(lines) != writers*perWriter {
		t.Errorf("file lines = %d, want %d (interleaved writes must not corrupt or drop lines)", len(lines), writers*perWriter)
	}
	for _, line := range lines {
		var s Sample
		if err := json.Unmarshal([]byte(line), &s); err != nil {
			t.Fatalf("corrupt line after concurrent appends: %v (%q)", err, line)
		}
	}
}

func TestStore_ResetClearsMemoryAndDisk(t *testing.T) {
	path := filepath.Join(t.TempDir(), "search_samples.jsonl")
	store := Open(path)
	if err := store.Append(Sample{Gold: 1000}); err != nil {
		t.Fatal(err)
	}

	if err := store.Reset(); err != nil {
		t.Fatalf("reset: %v", err)
	}
	if got := len(store.Samples()); got != 0 {
		t.Errorf("samples after reset = %d, want 0", got)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("reset left the log file on disk: %v", err)
	}
	// Reset on an already-clean store is a no-op, not an error.
	if err := store.Reset(); err != nil {
		t.Errorf("second reset: %v", err)
	}
}

func TestStore_AppendSurvivesMissingDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data", "deeper", "search_samples.jsonl")
	store := Open(path)
	if err := store.Append(Sample{Gold: 1}); err != nil {
		t.Fatalf("append into a new directory: %v", err)
	}
	if got := len(Open(path).Samples()); got != 1 {
		t.Errorf("reloaded %d samples, want 1", got)
	}
}

func readLines(t *testing.T, path string) []string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer f.Close()
	var out []string
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		if line := strings.TrimSpace(scanner.Text()); line != "" {
			out = append(out, line)
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan %s: %v", path, err)
	}
	return out
}

// Guard the format contract the UI and any script rely on: one JSON object per
// line, lowercase keys, no trailing commas.
func TestStore_LineFormat(t *testing.T) {
	path := filepath.Join(t.TempDir(), "search_samples.jsonl")
	store := Open(path)
	if err := store.Append(NewSample(time.Unix(0, 0), 1234, 5678, 90, true, 2*time.Second)); err != nil {
		t.Fatal(err)
	}
	lines := readLines(t, path)
	if len(lines) != 1 {
		t.Fatalf("got %d lines, want 1", len(lines))
	}
	for _, key := range []string{`"gold":1234`, `"elixir":5678`, `"de":90`, `"taken":true`, `"search_ms":2000`} {
		if !strings.Contains(lines[0], key) {
			t.Errorf("line missing %s: %s", key, lines[0])
		}
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &decoded); err != nil {
		t.Fatalf("line is not JSON: %v", err)
	}
	if got := len(decoded); got != 6 {
		t.Errorf("got %d fields, want 6: %v", got, decoded)
	}
}
