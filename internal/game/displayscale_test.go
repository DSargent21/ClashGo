package game

import (
	"math"
	"testing"
)

// A pinned display scale is how a non-reference geometry gets exact probes:
// 1280x720 measured 1.325 live (cmd/resprobe, docs/RESOLUTION.md) against a
// 1.3006 diagonal-ratio derivation, and that 1.9% is a 2 px miss on a HUD
// anchor — enough to lose a 1-pixel probe. The pin therefore has to reach the
// mapping, not just the reported number.
func TestDisplayScalePinOverridesTheDerivation(t *testing.T) {
	cal := NewCalibration(1280, 720)
	derived := cal.DisplayScale()
	if _, ok := cal.DisplayScaleOverride(); ok {
		t.Fatal("a fresh calibration must not report a pinned scale")
	}
	if math.Abs(derived-math.Hypot(1280, 720)/math.Hypot(RefWidth, RefHeight)) > 1e-9 {
		t.Fatalf("derived scale = %v, want the diagonal ratio", derived)
	}

	const measured = 1.325
	cal.SetDisplayScale(measured)
	if got, ok := cal.DisplayScaleOverride(); !ok || got != measured {
		t.Fatalf("DisplayScaleOverride = %v (ok=%v), want %v", got, ok, measured)
	}
	if got := cal.DisplayScale(); got != measured {
		t.Fatalf("DisplayScale = %v, want the pinned %v", got, measured)
	}

	// The pin has to move the mapping: a point 400 px right of the reference
	// centre shifts by 400*(pin-derived) — about 10 px, i.e. three times the
	// tolerance of a 1-pixel probe.
	ref := Point{X: 830, Y: 366}
	pinned := cal.MapPoint(ref, AnchorCenter)
	wantPinned := 1280/2 + int(math.Round(400*measured))
	wantDerived := 1280/2 + int(math.Round(400*derived))
	if pinned.X != wantPinned {
		t.Errorf("pinned MapX = %d, want %d", pinned.X, wantPinned)
	}
	if pinned.X == wantDerived {
		t.Errorf("pinned MapX = %d, identical to the derived mapping: the pin never reached MapPoint", pinned.X)
	}

	// Edge-anchored HUD chrome moves too, in the other direction (its distance
	// to the nearest edge, not the centre, sets the error).
	if got, want := cal.MapPoint(Point{X: 25, Y: 695}, AnchorEdge).X, int(math.Round(25*measured)); got != want {
		t.Errorf("pinned edge MapX = %d, want %d", got, want)
	}
}

// A zero DeviceConfig.DisplayScale means "derive it", so SetDisplayScale has to
// ignore values that would silently place every probe at the frame origin.
func TestDisplayScalePinIgnoresInvalidValues(t *testing.T) {
	cal := NewCalibration(1280, 720)
	const good = 1.325
	cal.SetDisplayScale(good)
	for _, bad := range []float64{0, -good, math.NaN(), math.Inf(1), math.Inf(-1)} {
		cal.SetDisplayScale(bad)
		if got := cal.DisplayScale(); got != good {
			t.Errorf("SetDisplayScale(%v) replaced a good pin with %v", bad, got)
		}
	}
}

// The reference geometry is the calibrated dataset's home: every authored
// coordinate is exact there, so a stray pin must not be able to disturb it. A
// user who sets display_scale on a 860x732 device gets the identity, not a
// mapping nobody measured.
func TestReferenceGeometryIgnoresAPinnedScale(t *testing.T) {
	cal := NewCalibration(RefWidth, RefHeight)
	cal.SetDisplayScale(1.325) // nonsense on purpose
	if !cal.IsReferenceGeometry() {
		t.Fatal("a pinned scale must not change what counts as the reference geometry")
	}
	for _, a := range []Anchor{AnchorEdge, AnchorCenter} {
		for _, p := range []Point{{25, 695}, {430, 366}, {830, 35}} {
			if got := cal.MapPoint(p, a); got != p {
				t.Errorf("reference MapPoint(%v, anchor=%d) = %v with a pin set, want the identity", p, a, got)
			}
		}
	}
}
