package game

import (
	"image"
	"math"
	"testing"
)

// The per-axis anchor model is pinned to measurements taken on a live
// BlueStacks instance at 1280x720 with the display scale pinned to 1.325
// (cmd/resprobe searched each reference patch in the 720p capture of the same
// village; docs/RESOLUTION.md). Each row says which mapping the game actually
// uses for that widget, and the tolerance leaves room for the fact that the
// pinned scale is 1.9% below the per-element measurement.
func TestHudAndCentreMatchLiveMeasurements(t *testing.T) {
	cal := NewCalibration(1280, 720)
	cal.SetDisplayScale(1.325)

	cases := []struct {
		name     string
		ref      Point
		want     Point
		tol      int
		useHudFn bool
	}{
		// Builder head (wall-upgrade menu): x sits at the reference centre, y on
		// the top edge. The centre model would put it at y = -85 and clamp.
		{"builder-head", Point{430, 30}, Point{640, 40}, 4, true},
		// Attack! button: left edge and bottom edge margins.
		{"attack-button", Point{60, 695}, Point{79, 669}, 4, true},
		// Army-bar battle button and the saved-army recipe cards: centre on both
		// axes even though they are HUD, well away from every edge.
		{"army-bar-battle", Point{525, 247}, Point{767, 200}, 4, true},
		{"recipe-card-1", Point{430, 227}, Point{640, 173}, 5, true},
		// Settings gear: right edge and top edge.
		{"settings-gear", Point{830, 16}, Point{1240, 21}, 4, true},
		{"army-arrow", Point{514, 192}, Point{752, 127}, 4, true},
		// The village top bar's resource column: members further down it are
		// still top-anchored (y 95 lands at 124, not at the centre's y 1), which
		// is why the top band is wider than a symmetric edge band.
		{"elixir-icon", Point{830, 95}, Point{1241, 124}, 4, true},
		{"gold-icon", Point{830, 35}, Point{1240, 46}, 4, true},
		// Bottom bar text/icons keep their bottom margin (Attack! label, SHOP).
		{"attack-label", Point{25, 695}, Point{35, 669}, 4, true},
		{"shop-label", Point{770, 703}, Point{1164, 679}, 4, true},
		// Village (world) content: centred, even when the reference coordinate
		// sits near an edge — this is the row that rules out inferring the
		// anchor from the coordinate alone.
		{"village-tap", Point{50, 450}, Point{131, 472}, 7, false},
	}
	for _, tc := range cases {
		var gotX, gotY int
		if tc.useHudFn {
			gotX, gotY = cal.Hud(tc.ref.X, tc.ref.Y)
		} else {
			gotX, gotY = cal.Centre(tc.ref.X, tc.ref.Y)
		}
		if abs(gotX-tc.want.X) > tc.tol || abs(gotY-tc.want.Y) > tc.tol {
			t.Errorf("%s: %v ref %v, want %v (±%d)", tc.name, tc.ref, Point{gotX, gotY}, tc.want, tc.tol)
		}
	}
}

// Hud must never let a bottom-stack widget collapse towards the centre: the
// bottom HUD bar (troop bar, Next, End Battle, the army/attack buttons) keeps
// its distance to the bottom edge, so a reference y in the bottom band maps
// near the bottom of a shorter viewport rather than off it.
func TestHudBottomStackFollowsTheBottomEdge(t *testing.T) {
	cal := NewCalibration(1280, 720)
	cal.SetDisplayScale(1.325)

	// ref (34,588) is the End Battle button: 144 px above the reference bottom,
	// so it belongs 144*1.325 = 191 px above the 720p bottom, not 654 (centre)
	// or 588 (identity).
	if _, got := cal.Hud(34, 588); got != 720-int(math.Round(144*1.325)) {
		t.Errorf("Hud(34,588) y = %d, want %d", got, 720-int(math.Round(144*1.325)))
	}
	if got := cal.Y(588, AnchorMid); got == 720-int(math.Round(144*1.325)) {
		t.Error("the centre anchor must not agree with the bottom anchor this far from the centre")
	}
	// The troop-counter ROI row sits on the same stack.
	roi := cal.Rect2(image.Rect(300, 660, 480, 700), AnchorMid, AnchorHigh)
	if roi.Max.Y > 720 || roi.Min.Y < 600 {
		t.Errorf("troop counter ROI = %v, want it inside the bottom stack", roi)
	}
}

// The reference geometry is the authored dataset's home: every model must be
// the identity there, including the new per-axis anchors, or the calibrated
// thresholds would silently move.
func TestAxisAnchorsAreIdentityAtTheReference(t *testing.T) {
	cal := NewCalibration(RefWidth, RefHeight)
	for _, a := range []AxisAnchor{AnchorLow, AnchorHigh, AnchorMid} {
		for p := 0; p <= RefWidth; p += 17 {
			if got := cal.X(p, a); got != p {
				t.Fatalf("reference X(%d, anchor=%d) = %d, want identity", p, a, got)
			}
		}
		for p := 0; p <= RefHeight; p += 17 {
			if got := cal.Y(p, a); got != p {
				t.Fatalf("reference Y(%d, anchor=%d) = %d, want identity", p, a, got)
			}
		}
	}
	pts := []Point{{430, 30}, {60, 695}, {525, 247}, {50, 450}, {830, 16}, {0, 0}, {859, 731}}
	for _, p := range pts {
		if x, y := cal.Hud(p.X, p.Y); x != p.X || y != p.Y {
			t.Errorf("reference Hud(%v) = (%d,%d), want identity", p, x, y)
		}
		if x, y := cal.Centre(p.X, p.Y); x != p.X || y != p.Y {
			t.Errorf("reference Centre(%v) = (%d,%d), want identity", p, x, y)
		}
	}
	// A zero-value calibration behaves like the reference, so test doubles and
	// struct literals keep working.
	var zero Calibration
	if x, y := zero.Hud(60, 695); x != 60 || y != 695 {
		t.Errorf("zero calibration Hud = (%d,%d), want identity", x, y)
	}
}

// Mapping must stay inside the frame for every geometry: a tap that lands out
// of bounds is silently skipped by the adb layer, which looks like a button
// that does nothing.
func TestAxisAnchorsStayInBounds(t *testing.T) {
	for _, geo := range []struct{ w, h int }{{1280, 720}, {1600, 900}, {1920, 1080}, {1024, 768}, {900, 760}, {3840, 2160}} {
		cal := NewCalibration(geo.w, geo.h)
		for x := 0; x <= RefWidth; x += 13 {
			for y := 0; y <= RefHeight; y += 13 {
				hudX, hudY := cal.Hud(x, y)
				midX, midY := cal.Centre(x, y)
				mapped := [][2]int{
					{hudX, hudY},
					{midX, midY},
					{cal.X(x, AnchorLow), cal.Y(y, AnchorLow)},
					{cal.X(x, AnchorHigh), cal.Y(y, AnchorHigh)},
					{cal.X(x, AnchorMid), cal.Y(y, AnchorMid)},
				}
				for _, m := range mapped {
					if m[0] < 0 || m[1] < 0 || m[0] >= geo.w || m[1] >= geo.h {
						t.Fatalf("%dx%d: ref(%d,%d) mapped out of bounds to (%d,%d)", geo.w, geo.h, x, y, m[0], m[1])
					}
				}
			}
		}
	}
}

// Length scales distances (radii, spacings, jitter) by the display scale, not
// by a per-axis ratio, and is the identity at the reference geometry.
func TestLengthScalesByDisplayScale(t *testing.T) {
	ref := NewCalibration(RefWidth, RefHeight)
	if got := ref.Length(18); got != 18 {
		t.Errorf("reference Length(18) = %v, want 18", got)
	}
	cal := NewCalibration(1280, 720)
	cal.SetDisplayScale(1.325)
	if got := cal.Length(18); math.Abs(got-18*1.325) > 1e-9 {
		t.Errorf("Length(18) = %v, want %v", got, 18*1.325)
	}
	var zero *Calibration
	if got := zero.Length(25); got != 25 {
		t.Errorf("nil Length(25) = %v, want 25", got)
	}
}
