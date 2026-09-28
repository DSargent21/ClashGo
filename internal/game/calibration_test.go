package game

import (
	"image"
	"math"
	"testing"
)

// The reference geometry must be a strict identity: every authored coordinate
// in this package and in internal/attack was measured at 860x732, so a
// regression here would silently move the entire calibrated dataset.
func TestCalibrationReferenceGeometryIsIdentity(t *testing.T) {
	cal := NewCalibration(RefWidth, RefHeight)
	if !cal.IsReferenceGeometry() {
		t.Fatal("860x732 must be treated as the reference geometry")
	}
	if cal.DisplayScale() != 1.0 {
		t.Fatalf("display scale at the reference = %v, want 1", cal.DisplayScale())
	}

	points := []Point{{0, 0}, {25, 695}, {430, 366}, {830, 35}, {859, 731}, {430, 240}}
	for _, a := range []Anchor{AnchorEdge, AnchorCenter} {
		for _, p := range points {
			if got := cal.MapPoint(p, a); got != p {
				t.Errorf("MapPoint(%v, anchor=%d) = %v, want identity", p, a, got)
			}
		}
	}

	r := image.Rect(300, 280, 520, 470)
	for _, a := range []Anchor{AnchorEdge, AnchorCenter} {
		if got := cal.MapRect(r, a); got != r {
			t.Errorf("MapRect(%v, anchor=%d) = %v, want identity", r, a, got)
		}
	}

	if got := cal.MapPixelCheck(PixelCheck{X: 497, Y: 431, R: 0xD6, G: 0xF4, B: 0x76, Tolerance: 45}, AnchorCenter); got.X != 497 || got.Y != 431 {
		t.Errorf("MapPixelCheck moved a reference probe to %v", got)
	}
}

// TestCalibration1280x720MatchesLiveLayout pins the mapping against the values
// measured on a live BlueStacks instance (cmd/resprobe, see
// docs/RESOLUTION.md). The display scale is derived from the screen diagonal
// (1.3006 here) and the measurements came in at ~1.325, so the assertions are
// tolerance-based: what matters is that the mapping lands on the right UI
// element, not that it reproduces the sample grid exactly.
func TestCalibration1280x720MatchesLiveLayout(t *testing.T) {
	cal := NewCalibration(1280, 720)
	wantK := math.Hypot(1280, 720) / math.Hypot(RefWidth, RefHeight) // 1.3006
	if math.Abs(cal.DisplayScale()-wantK) > 1e-9 {
		t.Fatalf("display scale = %v, want %v", cal.DisplayScale(), wantK)
	}

	cases := []struct {
		name   string
		ref    Point
		anchor Anchor
		want   Point // measured live (see docs/RESOLUTION.md)
		tol    int
	}{
		// Attack! button text: left+bottom anchored HUD chrome.
		{"attack-left-margin", Point{X: 25, Y: 695}, AnchorEdge, Point{X: 35, Y: 669}, 4},
		// SHOP button: right+bottom anchored HUD chrome.
		{"shop-right-margin", Point{X: 770, Y: 703}, AnchorEdge, Point{X: 1164, Y: 679}, 4},
		// Village patch centre: centred world content keeps its centre offset
		// exactly (this is the measurement with anisotropy 1.000).
		{"world-centre", Point{X: 430, Y: 375}, AnchorCenter, Point{X: 640, Y: 372}, 2},
		// The viewport centre itself is a fixed point under both anchors.
		{"viewport-centre", Point{X: 430, Y: 366}, AnchorCenter, Point{X: 640, Y: 360}, 2},
	}
	for _, tc := range cases {
		got := cal.MapPoint(tc.ref, tc.anchor)
		if abs(got.X-tc.want.X) > tc.tol || abs(got.Y-tc.want.Y) > tc.tol {
			t.Errorf("%s: MapPoint(%v, %d) = %v, want %v (±%d)", tc.name, tc.ref, tc.anchor, got, tc.want, tc.tol)
		}
	}

	// The two anchors must disagree where the layout says they do: a
	// bottom-anchored HUD point is NOT where the centred world model puts it
	// (this is the distinction the migration is built on).
	if cal.MapY(695, AnchorEdge) == cal.MapY(695, AnchorCenter) {
		t.Error("bottom-anchored and centred mappings must differ away from the centre")
	}
	// ... and the centred mapping of a bottom HUD row falls past the shorter
	// viewport, saturating at the bottom edge: that is why HUD chrome cannot
	// use the centred model.
	if got := cal.MapY(695, AnchorCenter); got < cal.PhysicalH-1 {
		t.Errorf("centred mapping of a bottom HUD row = %d, want it saturated at the viewport bottom (%d)", got, cal.PhysicalH-1)
	}
}

// Every mapped anchor must stay inside the frame: an out-of-bounds probe is
// silently skipped by the classifier, which would quietly thin a rule's
// evidence instead of failing loudly.
func TestCalibrationAnchorsStayInBounds(t *testing.T) {
	for _, geo := range []struct{ w, h int }{{1280, 720}, {1600, 900}, {1920, 1080}, {1024, 768}} {
		cal := NewCalibration(geo.w, geo.h)
		for _, a := range []Anchor{AnchorEdge, AnchorCenter} {
			for x := 0; x <= RefWidth; x += 7 {
				for y := 0; y <= RefHeight; y += 7 {
					p := cal.MapPoint(Point{X: x, Y: y}, a)
					if p.X < 0 || p.Y < 0 || p.X >= geo.w || p.Y >= geo.h {
						t.Fatalf("%dx%d anchor=%d: ref(%d,%d) mapped out of bounds to %v", geo.w, geo.h, a, x, y, p)
					}
				}
			}
		}
	}
}

// A rule-level anchor must survive the legacy ScaleRule path, so a caller that
// pre-scales rules (cmd/test_wall_upgrade) keeps the same geometry semantics.
func TestScaleRuleKeepsAnchor(t *testing.T) {
	cal := NewCalibration(RefWidth, RefHeight)
	in := StateRule{State: StateBattleEnd, Anchor: AnchorCenter, Checks: []PixelCheck{{X: 430, Y: 240, R: 1, G: 2, B: 3, Tolerance: 45}}}
	got := cal.ScaleRule(in)
	if got.Anchor != AnchorCenter {
		t.Errorf("ScaleRule dropped the anchor: %d", got.Anchor)
	}
	if len(got.Checks) != 1 || got.Checks[0].X != 430 || got.Checks[0].Y != 240 {
		t.Errorf("ScaleRule changed a reference-geometry check: %v", got.Checks)
	}
}

// A zero-value Calibration (struct literals in tests and the fastest call
// sites) carries no geometry, so it must behave like the reference rather than
// mapping against a zero-sized screen.
func TestZeroCalibrationIsIdentity(t *testing.T) {
	var cal Calibration
	if !cal.IsReferenceGeometry() {
		t.Fatal("zero calibration must be treated as the reference geometry")
	}
	if got := cal.MapPoint(Point{X: 830, Y: 35}, AnchorEdge); got != (Point{X: 830, Y: 35}) {
		t.Errorf("zero calibration moved a point to %v", got)
	}
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
