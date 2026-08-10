package vision

import (
	"testing"

	"gocv.io/x/gocv"
)

func TestMatPoolKeyUnique(t *testing.T) {
	p := NewMatPool()

	// Distinct dimensions that previously collided under rune() encoding
	// (e.g. 100 and 256 both produced a 1-char string key) must now be
	// independent pool entries.
	keys := map[string]bool{}
	for _, dims := range [][2]int{{100, 100}, {256, 256}, {70000, 200}, {200, 70000}, {100, 256}} {
		k := p.getPoolKey(dims[0], dims[1], gocv.MatTypeCV8UC3)
		if keys[k] {
			t.Fatalf("collision for dims %v: key %q reused", dims, k)
		}
		keys[k] = true
	}
}

func TestMatPoolRoundTrip(t *testing.T) {
	p := NewMatPool()

	const rows, cols = 480, 640
	got := p.Get(rows, cols, gocv.MatTypeCV8UC3)
	if got.Empty() || got.Rows() != rows || got.Cols() != cols {
		t.Fatalf("Get returned invalid mat: rows=%d cols=%d empty=%v", got.Rows(), got.Cols(), got.Empty())
	}

	// Write a marker pixel, return to pool, reacquire, and verify it was
	// reset (SetTo clears) so a reused mat cannot leak stale data.
	got.SetUCharAt(0, 0, 255)
	p.Put(got)

	reused := p.Get(rows, cols, gocv.MatTypeCV8UC3)
	if reused.Empty() {
		t.Fatal("reacquired mat is empty")
	}
	if reused.GetUCharAt(0, 0) != 0 {
		t.Errorf("pooled mat was not reset: pixel[0,0]=%d, want 0", reused.GetUCharAt(0, 0))
	}
	reused.Close()
}

func TestMatPoolLifecycleBalance(t *testing.T) {
	p := NewMatPool()

	// Simulate an encode-error path: every acquired mat must be returned,
	// even when the consumer short-circuits on error. Leaks show up as
	// growing pool maps / unreclaimed mats.
	mats := make([]gocv.Mat, 0, 50)
	for i := 0; i < 50; i++ {
		m := p.Get(320, 240, gocv.MatTypeCV8UC3)
		mats = append(mats, m)
	}
	for _, m := range mats {
		p.Put(m)
	}

	// Reacquire the same size; all should come from the pool (non-empty).
	for i := 0; i < 50; i++ {
		m := p.Get(320, 240, gocv.MatTypeCV8UC3)
		if m.Empty() {
			t.Fatal("unexpected empty mat from pool")
		}
		p.Put(m)
	}
}
