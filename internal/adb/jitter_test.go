package adb

import (
	"math"
	"testing"
)

// The jitter offsets are drawn on every tap, several times a second, for the
// length of a session — so the draw path must not allocate. It used to:
// rand.New(rand.NewSource(now)) builds a 607-word generator (~4.9 KB) per call
// for four numbers nobody keeps.
func TestGaussianOffsetDoesNotAllocate(t *testing.T) {
	if allocs := testing.AllocsPerRun(1000, func() {
		ox, oy := gaussianOffset(2.5)
		_, _ = ox, oy
	}); allocs != 0 {
		t.Errorf("gaussianOffset allocates %.2f objects per call, want 0", allocs)
	}
}

// Zero (or negative) standard deviation means "no jitter", and must draw
// nothing at all: callers use a zero stdDev to mean "place the tap exactly
// where I asked", and the previous code multiplied a draw by zero instead.
func TestGaussianOffsetIsZeroWithoutJitter(t *testing.T) {
	for _, stdDev := range []float64{0, -1, -0.001} {
		for i := 0; i < 100; i++ {
			if ox, oy := gaussianOffset(stdDev); ox != 0 || oy != 0 {
				t.Fatalf("gaussianOffset(%v) = (%d, %d), want (0, 0)", stdDev, ox, oy)
			}
		}
	}
}

// The offsets must stay a Gaussian centred on zero: both signs appear, the
// spread stays inside a few standard deviations, and the sample mean sits near
// zero. A regression to a uniform or one-sided offset would still "jitter" but
// would walk every tap in one direction.
func TestGaussianOffsetStaysCentredAndBounded(t *testing.T) {
	const (
		stdDev = 2.5
		n      = 20000
	)
	sumX, sumY := 0, 0
	posX, negX := 0, 0
	maxAbs := 0
	for i := 0; i < n; i++ {
		ox, oy := gaussianOffset(stdDev)
		sumX += ox
		sumY += oy
		switch {
		case ox > 0:
			posX++
		case ox < 0:
			negX++
		}
		if a := int(math.Abs(float64(ox))); a > maxAbs {
			maxAbs = a
		}
		if a := int(math.Abs(float64(oy))); a > maxAbs {
			maxAbs = a
		}
	}

	if mean := float64(sumX) / n; math.Abs(mean) > 0.15 {
		t.Errorf("mean x offset = %.3f, want it near 0 for a centred Gaussian", mean)
	}
	if mean := float64(sumY) / n; math.Abs(mean) > 0.15 {
		t.Errorf("mean y offset = %.3f, want it near 0 for a centred Gaussian", mean)
	}
	if posX == 0 || negX == 0 {
		t.Errorf("x offsets never changed sign (positive %d, negative %d); the jitter is one-sided", posX, negX)
	}
	// 6 sigma is a generous ceiling: with 20k draws an excursion beyond it is
	// ~2e-6 likely, so this catches a scale bug without flaking.
	if limit := int(6 * stdDev); maxAbs > limit {
		t.Errorf("|offset| reached %d, beyond the %d (6 sigma) ceiling", maxAbs, limit)
	}
	// Sanity: a Gaussian with sigma 2.5 is rarely all zeros, so the offsets
	// must actually vary rather than collapsing to a constant.
	if maxAbs < 2 {
		t.Errorf("offsets never exceeded %d px; the distribution is not spread", maxAbs)
	}
}
