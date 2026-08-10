package adb

import (
	"image"
	"math"
	"testing"
)

// TestSwipePath_StraightLine: with the control point at the segment
// midpoint the quadratic bezier degenerates to the straight line
// p0→p2, and the endpoints are exact.
func TestSwipePath_StraightLine(t *testing.T) {
	p0 := image.Point{X: 100, Y: 200}
	p2 := image.Point{X: 700, Y: 500}
	mid := image.Point{X: (p0.X + p2.X) / 2, Y: (p0.Y + p2.Y) / 2}

	pts := swipePath(p0, mid, p2, 16)

	if len(pts) != 16 {
		t.Fatalf("expected 16 points, got %d", len(pts))
	}
	if pts[0] != p0 || pts[len(pts)-1] != p2 {
		t.Fatalf("endpoints wrong: first=%v last=%v", pts[0], pts[len(pts)-1])
	}
	// Every interior point must lie on the p0→p2 line (same slope).
	dx, dy := float64(p2.X-p0.X), float64(p2.Y-p0.Y)
	for i, pt := range pts[1 : len(pts)-1] {
		fx := float64(pt.X-p0.X) / dx
		fy := float64(pt.Y-p0.Y) / dy
		if math.Abs(fx-fy) > 0.02 {
			t.Errorf("point %d (%v) off the straight line: fx=%.4f fy=%.4f", i+1, pt, fx, fy)
		}
	}
}

// TestSwipePath_Curved: with the control point pulled perpendicular to
// the axis, the midpoint of the path must deviate from the straight
// line (the arc is real, not cosmetic).
func TestSwipePath_Curved(t *testing.T) {
	p0 := image.Point{X: 100, Y: 200}
	p2 := image.Point{X: 700, Y: 200} // horizontal gesture
	// Control point 60px above the line.
	p1 := image.Point{X: 400, Y: 140}

	pts := swipePath(p0, p1, p2, 33)
	mid := pts[len(pts)/2]

	if mid.X != 400 {
		t.Errorf("path midpoint X = %d, want 400 (symmetric bezier)", mid.X)
	}
	if mid.Y >= 200 {
		t.Errorf("path midpoint Y = %d, want < 200 (curved above the line)", mid.Y)
	}
	// The apex of a quadratic bezier is exactly 1/2 the control-point
	// height above the baseline (mid.Y should sit at p1.Y + (lineY - p1.Y)/2 = 170).
	if math.Abs(float64(mid.Y)-170) > 2 {
		t.Errorf("path midpoint Y = %d, want ~170 (half the control height)", mid.Y)
	}
}

// TestSwipePath_MinSteps: a degenerate step count must not panic and
// must still return usable endpoints.
func TestSwipePath_MinSteps(t *testing.T) {
	p0 := image.Point{X: 0, Y: 0}
	p2 := image.Point{X: 10, Y: 10}
	pts := swipePath(p0, p2, p2, 1)
	if len(pts) != 2 {
		t.Fatalf("expected floor of 2 points, got %d", len(pts))
	}
}

// TestEasedStepSleeps: sleeps must be non-negative, sum to ~totalMs,
// and follow the ease-in-out profile — the slowest pauses at the
// start and end of the gesture, the fastest in the middle.
func TestEasedStepSleeps(t *testing.T) {
	const steps = 16
	sleeps := easedStepSleeps(400, steps)

	if len(sleeps) != steps {
		t.Fatalf("expected %d sleeps, got %d", steps, len(sleeps))
	}
	var sum int
	for i, s := range sleeps {
		if s < 4 {
			t.Errorf("sleep %d = %dms below 4ms floor", i, s)
		}
		sum += s
	}
	if sum < 320 || sum > 480 {
		t.Errorf("sleep total %dms, want ~400ms (±20%%)", sum)
	}
	mid := sleeps[steps/2]
	if sleeps[0] <= mid || sleeps[steps-1] <= mid {
		t.Errorf("expected slow start/end, fast middle: start=%d mid=%d end=%d",
			sleeps[0], mid, sleeps[steps-1])
	}
}

// TestEasedStepSleeps_ZeroTotal: a non-positive total must default to
// 300ms instead of panicking or returning all-zero sleeps.
func TestEasedStepSleeps_ZeroTotal(t *testing.T) {
	sleeps := easedStepSleeps(0, 8)
	if len(sleeps) != 8 {
		t.Fatalf("expected 8 sleeps, got %d", len(sleeps))
	}
	var sum int
	for _, s := range sleeps {
		sum += s
	}
	if sum == 0 {
		t.Error("expected non-zero sleep total after defaulting")
	}
}

// TestClampInt: boundary + out-of-range behavior.
func TestClampInt(t *testing.T) {
	cases := []struct {
		v, min, max, want int
	}{
		{5, 0, 10, 5},
		{-3, 0, 10, 0},
		{42, 0, 10, 10},
		{0, 0, 32767, 0},
		{32767, 0, 32767, 32767},
	}
	for _, tc := range cases {
		if got := clampInt(tc.v, tc.min, tc.max); got != tc.want {
			t.Errorf("clampInt(%d,%d,%d) = %d, want %d", tc.v, tc.min, tc.max, got, tc.want)
		}
	}
}
