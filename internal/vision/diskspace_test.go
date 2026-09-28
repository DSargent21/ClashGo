package vision

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// probeFrame is a minimal non-empty frame for the disk-guard tests.
func probeFrame(w, h int) Frame {
	return Frame{W: w, H: h, Stride: w * 3, Pix: make([]byte, w*h*3)}
}

// TestDiskFreeBytesReportsSpace pins the platform probe: a normal directory
// must report a plausible, non-trivial amount of free space. This is the number
// the recorder uses to decide whether it may write another frame.
func TestDiskFreeBytesReportsSpace(t *testing.T) {
	dir := t.TempDir()
	free, err := DiskFreeBytes(dir)
	if err != nil {
		t.Fatalf("DiskFreeBytes(%s): %v", dir, err)
	}
	if free < 16<<20 {
		t.Fatalf("free space on a temp dir looks wrong: %d bytes", free)
	}
}

// TestFrameStoreRefusesLowDisk is the regression test for the runaway frame
// dumps that filled this machine's disk: once the volume is below the floor,
// Add must fail with ErrLowDisk and write nothing at all.
func TestFrameStoreRefusesLowDisk(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenFrameStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	store.freeFn = func(string) (uint64, error) { return 1 << 20, nil } // 1 MiB free
	store.SetMinFreeBytes(DefaultMinFreeBytes)

	entry, err := store.Add(probeFrame(8, 8), FrameMeta{State: "Battle"})
	if err == nil {
		t.Fatalf("Add succeeded with 1 MiB free and a 2 GiB floor; entry=%+v", entry)
	}
	if !errors.Is(err, ErrLowDisk) {
		t.Fatalf("error does not wrap ErrLowDisk: %v", err)
	}

	// Nothing may have been written: neither a frame nor an index line.
	frames, _ := filepath.Glob(filepath.Join(dir, "frames", "*.png"))
	if len(frames) != 0 {
		t.Fatalf("a refused write still left %d frame files: %v", len(frames), frames)
	}
	if _, err := os.Stat(filepath.Join(dir, "index.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("a refused write still created the index: %v", err)
	}
}

// TestFrameStoreWritesWhenHeadroomIsMet keeps the guard from being a
// blanket refusal: with headroom (or the floor disabled) recording still works.
func TestFrameStoreWritesWhenHeadroomIsMet(t *testing.T) {
	for _, tc := range []struct {
		name string
		free uint64
		ceil uint64
	}{
		{"above floor", 8 << 30, DefaultMinFreeBytes},
		{"floor disabled", 1 << 20, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			store, err := OpenFrameStore(dir)
			if err != nil {
				t.Fatal(err)
			}
			store.freeFn = func(string) (uint64, error) { return tc.free, nil }
			store.SetMinFreeBytes(tc.ceil)

			if _, err := store.Add(probeFrame(8, 8), FrameMeta{State: "Battle"}); err != nil {
				t.Fatalf("Add refused a frame it should have written: %v", err)
			}
			entries, err := store.Entries()
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 1 {
				t.Fatalf("want 1 recorded entry, got %d", len(entries))
			}
		})
	}
}
