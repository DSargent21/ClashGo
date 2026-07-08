package attack

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/Ducky705/ClashGO/internal/paths"
)

// resetRotationStateForTest clears the rotation state file under the
// current CLASHGO_CONFIG_DIR AND resets the in-memory lastIndexInMem
// sentinel so each test starts from a known state. It returns the
// file path so the test can write specific fixtures.
//
// The in-memory reset is critical: `lastIndexInMem` is package-level,
// so without this reset, Test A leaving it at value 2 would silently
// make Test B's "first call returns TopLeft" assertion fail (it would
// actually get BottomLeft). See Reviewer Finding #1 in the
// 4-side-rotation change set for the regression story.
func resetRotationStateForTest(t *testing.T) string {
	t.Helper()
	lastIndexInMem = -1
	path := filepath.Join(paths.GetConfigDir(), rotationStateFile)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		t.Fatalf("reset rotation state: %v", err)
	}
	return path
}

func writeRotationStateForTest(t *testing.T, st RotationState) {
	t.Helper()
	// writeRotationStateForTest is a peer of resetRotationStateForTest:
	// it sets the file to a specific value AND must reset the
	// in-memory lastIndexInMem sentinel, otherwise a previous test's
	// in-memory state would make the next NextEdgeIndex() call
	// advance from that value rather than from the fixture we just
	// wrote. Without this reset, OutOfRangeIndex / NegativeIndex
	// silently depend on whatever value the prior test left in
	// lastIndexInMem — and pass/fail based on test execution order.
	lastIndexInMem = -1
	path := filepath.Join(paths.GetConfigDir(), rotationStateFile)
	data, err := json.Marshal(st)
	if err != nil {
		t.Fatalf("marshal rotation state: %v", err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatalf("write rotation state: %v", err)
	}
}

// writeRawRotationStateForTest writes arbitrary raw bytes to the
// rotation state file. Used by tests that need to seed edge-case
// on-disk content (empty, corrupted, truncated JSON) which the
// structured writeRotationStateForTest helper can't express. Like
// its peers it MUST reset lastIndexInMem so the next NextEdgeIndex
// call hydrates from the file fixture, not from leftover in-memory
// state.
func writeRawRotationStateForTest(t *testing.T, raw []byte) {
	t.Helper()
	t.Setenv("CLASHGO_CONFIG_DIR", t.TempDir())
	lastIndexInMem = -1
	path := filepath.Join(paths.GetConfigDir(), rotationStateFile)
	if err := os.WriteFile(path, raw, 0644); err != nil {
		t.Fatalf("write raw rotation state: %v", err)
	}
}

// Test 1: cycle from a clean state — 4 calls cover all corners, 5th wraps.
func TestNextEdgeIndex_CycleFromCleanState(t *testing.T) {
	t.Setenv("CLASHGO_CONFIG_DIR", t.TempDir())
	resetRotationStateForTest(t)

	want := []string{"TopLeft", "TopRight", "BottomRight", "BottomLeft"}
	for i, expected := range want {
		got := NextEdgeIndex()
		if got != expected {
			t.Errorf("Call %d: got %q, want %q", i+1, got, expected)
		}
	}
	// 5th call: wraps to TopLeft.
	if got := NextEdgeIndex(); got != "TopLeft" {
		t.Errorf("5th call (wrap): got %q, want TopLeft", got)
	}
}

// Test 2: file exists but empty → first call still returns TopLeft.
func TestNextEdgeIndex_EmptyFile(t *testing.T) {
	writeRawRotationStateForTest(t, []byte(""))
	if got := NextEdgeIndex(); got != "TopLeft" {
		t.Errorf("Empty file: got %q, want TopLeft", got)
	}
}

// Test 3: corrupted JSON → first call returns TopLeft and overwrites.
func TestNextEdgeIndex_CorruptedJSON(t *testing.T) {
	writeRawRotationStateForTest(t, []byte("{not valid json"))
	if got := NextEdgeIndex(); got != "TopLeft" {
		t.Errorf("Corrupted JSON: got %q, want TopLeft (defaults gracefully)", got)
	}
	// Subsequent call should advance to TopRight.
	if got := NextEdgeIndex(); got != "TopRight" {
		t.Errorf("Post-corruption 2nd call: got %q, want TopRight", got)
	}
}

// Test 4: out-of-range LastIndex → defensively reset to 0 then advance.
func TestNextEdgeIndex_OutOfRangeIndex(t *testing.T) {
	t.Setenv("CLASHGO_CONFIG_DIR", t.TempDir())
	writeRotationStateForTest(t, RotationState{LastIndex: 99})
	// 99 is out of [0,3]; the function should treat it as missing and
	// start at 0 (TopLeft), then advance to TopRight on the next call.
	if got := NextEdgeIndex(); got != "TopLeft" {
		t.Errorf("Out-of-range 99: got %q, want TopLeft (defensive reset)", got)
	}
	if got := NextEdgeIndex(); got != "TopRight" {
		t.Errorf("Post-reset 2nd call: got %q, want TopRight", got)
	}
}

// Test 5: negative LastIndex (-1) — also rejected as out of range.
func TestNextEdgeIndex_NegativeIndex(t *testing.T) {
	t.Setenv("CLASHGO_CONFIG_DIR", t.TempDir())
	writeRotationStateForTest(t, RotationState{LastIndex: -1})
	if got := NextEdgeIndex(); got != "TopLeft" {
		t.Errorf("Negative -1: got %q, want TopLeft (defensive reset)", got)
	}
}

// Test 6: 100 concurrent calls produce only valid corners, no panics.
func TestNextEdgeIndex_ConcurrentNoPanic(t *testing.T) {
	t.Setenv("CLASHGO_CONFIG_DIR", t.TempDir())
	resetRotationStateForTest(t)

	const N = 100
	var wg sync.WaitGroup
	results := make([]string, N)
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			results[idx] = NextEdgeIndex()
		}(i)
	}
	wg.Wait()

	// Every result must be one of the 4 valid corners.
	for i, r := range results {
		if !isValidCorner(r) {
			t.Errorf("Call %d returned invalid corner: %q", i, r)
		}
	}
}

// Test 7: after N concurrent calls the persisted LastIndex must be in
// [0, len-1] (no out-of-range state escapes to disk).
func TestNextEdgeIndex_ConcurrentPersistedStateValid(t *testing.T) {
	t.Setenv("CLASHGO_CONFIG_DIR", t.TempDir())
	resetRotationStateForTest(t)

	const N = 50
	var wg sync.WaitGroup
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = NextEdgeIndex()
		}()
	}
	wg.Wait()

	data, err := os.ReadFile(filepath.Join(paths.GetConfigDir(), rotationStateFile))
	if err != nil {
		t.Fatalf("read persisted state: %v", err)
	}
	var st RotationState
	if jerr := json.Unmarshal(data, &st); jerr != nil {
		t.Fatalf("unmarshal persisted state: %v", jerr)
	}
	if st.LastIndex < 0 || st.LastIndex >= len(rotationOrder) {
		t.Errorf("Persisted LastIndex out of range after %d concurrent calls: %d", N, st.LastIndex)
	}
}

// Test 8: truncated JSON on disk (process killed mid-write) is treated
// as missing — first call returns TopLeft, then advances to TopRight.
// Cheap to add; proves the recovery path the os.WriteFile non-atomicity
// depends on.
func TestNextEdgeIndex_TruncatedJSON(t *testing.T) {
	writeRawRotationStateForTest(t, []byte(`{"last_index":1`))
	if got := NextEdgeIndex(); got != "TopLeft" {
		t.Errorf("Truncated JSON: got %q, want TopLeft (defaults gracefully)", got)
	}
	if got := NextEdgeIndex(); got != "TopRight" {
		t.Errorf("Post-truncation 2nd call: got %q, want TopRight", got)
	}
}

// Test 9: read-only config dir (write fails). The cycle must still
// progress in-memory; otherwise a frozen sandbox / CI mount would
// stick the bot on TopLeft forever. This is the regression test for
// the in-memory lastIndexInMem fallback.
//
// Note: writeRawRotationStateForTest seeds a 0-byte file at 0644.
// We then downgrade to 0444 so the next NextEdgeIndex call can read
// (file exists) but the internal WriteFile fails (read-only), which
// is exactly the production failure mode the in-memory fallback
// exists to handle.
func TestNextEdgeIndex_ReadOnlyDirStillCycles(t *testing.T) {
	writeRawRotationStateForTest(t, []byte(""))
	path := filepath.Join(paths.GetConfigDir(), rotationStateFile)
	if err := os.Chmod(path, 0444); err != nil {
		t.Skipf("chmod read-only not supported on this platform: %v", err)
	}
	// Best-effort restore on test exit so t.TempDir cleanup works
	// (and so an unrelated sub-test in the same process doesn't
	// inherit a read-only file).
	t.Cleanup(func() { _ = os.Chmod(path, 0644) })

	seen := map[string]bool{}
	for i := 0; i < len(rotationOrder); i++ {
		got := NextEdgeIndex()
		if !isValidCorner(got) {
			t.Fatalf("Call %d returned invalid corner: %q", i+1, got)
		}
		seen[got] = true
	}
	if len(seen) != len(rotationOrder) {
		t.Errorf("Read-only dir: only saw %d unique corners across %d calls, want %d. In-memory fallback broken? seen=%v",
			len(seen), len(rotationOrder), len(rotationOrder), seen)
	}
}

func isValidCorner(c string) bool {
	for _, v := range rotationOrder {
		if v == c {
			return true
		}
	}
	return false
}
