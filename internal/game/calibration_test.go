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

// ---------------------------------------------------------------------------
// The inverse of the HUD law
// ---------------------------------------------------------------------------

// The pickers record a reference rect for a box a human dragged on the live
// device. Two properties are being pinned here, at three geometries:
//
//   - whatever HudPointReference reports ok for must round-trip EXACTLY (a near
//     miss would move a recorded box under the user's hands), and
//   - the law is not surjective at k > 1, so the great majority of physical
//     points that DO have a preimage must be found. A regression that dropped
//     reachability to a narrow slice of the frame would still pass a
//     round-trip-only test.
func TestHudReferenceInvertsHudExactly(t *testing.T) {
	geometries := []struct {
		name string
		w, h int
	}{
		{"reference 860x732", RefWidth, RefHeight},
		{"720p 1280x720", 1280, 720},
		{"900p 1600x900", 1600, 900},
	}
	for _, g := range geometries {
		t.Run(g.name, func(t *testing.T) {
			cal := NewCalibration(g.w, g.h)
			cal.SetDisplayScale(cal.DisplayScale())

			points, unreachable := 0, 0
			for y := 0; y < g.h; y += 7 {
				for x := 0; x < g.w; x += 7 {
					rx, ry, ok := cal.HudPointReference(x, y)
					if !ok {
						unreachable++
						continue
					}
					if gx, gy := cal.Hud(rx, ry); gx != x || gy != y {
						t.Fatalf("Hud(HudPointReference(%d,%d)) = (%d,%d), want the original point", x, y, gx, gy)
					}
					points++
				}
			}
			// Every anchor scales by k > 1, so a reference step of one skips
			// physical pixels. One axis therefore reaches about 1/k of its values,
			// and a point needs both axes, so the expected coverage is ~1/k² — 59%
			// at 720p, 38% at 900p. That is why the rect inverse snaps rather than
			// requiring an exact fit. The bound is a floor with a 20% margin, not an
			// expectation of completeness.
			sampled := len(frameSizes(g.w, g.h))
			floor := int(float64(sampled) * 0.8 / (cal.DisplayScale() * cal.DisplayScale()))
			if points < floor {
				t.Fatalf("only %d of %d sampled points were invertible (floor %d)", points, sampled, floor)
			}
			if g.w == RefWidth && g.h == RefHeight && unreachable != 0 {
				t.Fatalf("reference geometry must invert every point, %d were unreachable", unreachable)
			}
		})
	}
}

// frameSizes is the sample count TestHudReferenceInvertsHudExactly walks, so the
// unreachable allowance is expressed against the same denominator.
func frameSizes(w, h int) []image.Point {
	var pts []image.Point
	for y := 0; y < h; y += 7 {
		for x := 0; x < w; x += 7 {
			pts = append(pts, image.Pt(x, y))
		}
	}
	return pts
}

// A rect is only invertible as a whole: HudRect anchors BOTH edges off the
// rectangle's centre, so an edge that inverts fine on its own can still produce a
// rect that moves when it is mapped back. The shipped wall-upgrade chip boxes are
// the regression case, because they are the ones the live run proved wrong.
func TestHudRectReferenceSnapsAndRoundTrips(t *testing.T) {
	cal := NewCalibration(1280, 720)
	cal.SetDisplayScale(1.325)

	// The boxes the shipped assets map to on the live frame (see
	// cmd/test_wall_upgrade's live runs).
	live := []image.Rectangle{
		image.Rect(591, 503, 692, 601),   // gold chip
		image.Rect(717, 503, 816, 601),   // elixir chip
		image.Rect(800, 466, 1003, 549),  // confirm button
		image.Rect(640, 30, 660, 50),     // small square near the top
		image.Rect(0, 0, 40, 40),         // frame corner
		image.Rect(1209, 174, 1224, 189), // the modal X over the de readout
	}
	for _, target := range live {
		ref, mapped, ok := cal.HudRectReferenceSnap(target, 1)
		if !ok {
			t.Errorf("HudRectReferenceSnap(%v) found nothing within 1 px", target)
			continue
		}
		if got := cal.HudRect(ref); got != mapped {
			t.Errorf("HudRect(%v) = %v but the inverse reported %v", ref, got, mapped)
		}
		if d := RectDelta(mapped, target); d > 1 {
			t.Errorf("%v recorded as %v (%d px off, want <= 1)", target, mapped, d)
		}
		if ref.Min.X < 0 || ref.Min.Y < 0 || ref.Max.X > RefWidth || ref.Max.Y > RefHeight {
			t.Errorf("recovered reference rect %v falls outside the 860x732 frame", ref)
		}
	}

	// At the reference geometry the inverse is the identity, which is what keeps
	// the pickers exact when the user runs them against an 860x732 device.
	refCal := NewCalibration(RefWidth, RefHeight)
	r := image.Rect(393, 568, 469, 642)
	got, mapped, ok := refCal.HudRectReferenceSnap(r, 0)
	if !ok || got != r || mapped != r {
		t.Errorf("reference-geometry inverse = %v -> %v (ok=%v), want identity", got, mapped, ok)
	}
}

// Snapping is what the pickers rely on, so it has to hold across the frame, not
// just on the boxes above: every rect a human could plausibly drag must come back
// within a pixel, or the tool has to be able to say it cannot.
// Snapping is what the picking tools rely on, so its two behaviours both need
// pinning across the frame rather than on a handful of rects:
//
//   - a tight tolerance reproduces almost every drag within a pixel, and
//   - the drags it cannot reproduce are the BAND SEAMS, which no reference rect
//     can express at any tolerance. HudRect chooses one anchor per axis from the
//     rectangle's centre, so a box whose centre falls between two bands has no
//     preimage: at 1280x720 the seam is physical x 134..215 and 1065..1146, about
//     13% of the frame, and the smallest recorded box there lands ~81 px away.
//     The tools therefore report the distance instead of pretending, and a wide
//     tolerance always finds an answer so no drag is ever left un-recordable.
func TestHudRectReferenceSnapCoversTheFrame(t *testing.T) {
	const tightTol = 1
	const anyTol = 128
	for _, g := range []struct{ w, h int }{{1280, 720}, {1600, 900}} {
		cal := NewCalibration(g.w, g.h)
		snapped, missed, worst := 0, 0, 0
		hist := map[int]int{}
		var samples []image.Rectangle
		for y := 0; y < g.h-40; y += 53 {
			for x := 0; x < g.w-40; x += 61 {
				samples = append(samples, image.Rect(x, y, x+40, y+40))
			}
		}
		for _, target := range samples {
			_, mapped, ok := cal.HudRectReferenceSnap(target, tightTol)
			if !ok {
				missed++
				continue
			}
			d := RectDelta(mapped, target)
			hist[d]++
			if d > worst {
				worst = d
			}
			snapped++
		}
		t.Logf("%dx%d: snapped=%d/%d within %dpx, worst=%dpx, histogram=%v",
			g.w, g.h, snapped, len(samples), tightTol, worst, hist)

		if snapped == 0 {
			t.Fatalf("%dx%d: nothing snapped at all", g.w, g.h)
		}
		if worst > tightTol {
			t.Errorf("%dx%d: worst tight snap = %dpx, want <= %d", g.w, g.h, worst, tightTol)
		}
		// Seams are a minority of the frame; if most of it stopped snapping, the
		// inverse would be broken rather than merely piecewise.
		if missed > len(samples)/4 {
			t.Errorf("%dx%d: %d of %d samples unsnappable within %dpx", g.w, g.h, missed, len(samples), tightTol)
		}

		// The wide tolerance has to be total: a picker that cannot record a box at
		// all leaves the user with nothing to act on.
		for _, target := range samples {
			if _, _, ok := cal.HudRectReferenceSnap(target, anyTol); !ok {
				t.Fatalf("%dx%d: %v is unrecordable even within %dpx", g.w, g.h, target, anyTol)
			}
		}
	}
}

// A degenerate rect has no centre to anchor from and must be refused rather than
// silently inverted into a zero-area asset.
func TestHudRectReferenceRefusesEmpty(t *testing.T) {
	cal := NewCalibration(1280, 720)
	if _, _, ok := cal.HudRectReferenceSnap(image.Rectangle{}, 1); ok {
		t.Error("an empty rect was inverted")
	}
	if _, _, ok := cal.HudRectReferenceSnap(image.Rect(10, 10, 10, 10), 1); ok {
		t.Error("a zero-area rect was inverted")
	}
}

// hudCorners maps a reference rect the way the builder-menu ROI loader reads one:
// each corner through Hud, then min/max-ordered into a frame rect.
func hudCorners(cal *Calibration, r image.Rectangle) image.Rectangle {
	x1, y1 := cal.Hud(r.Min.X, r.Min.Y)
	x2, y2 := cal.Hud(r.Max.X, r.Max.Y)
	return image.Rect(min(x1, x2), min(y1, y2), max(x1, x2), max(y1, y2))
}

// The two inverses are not interchangeable, and the difference is invisible on
// the device a box was dragged on. The builder-menu panel spans a band boundary
// (its top edge is pinned under the top bar, its bottom edge is mid-screen), so
// HudRect — which anchors both edges from the rect's centre — cannot express the
// coordinates the asset file actually holds. Inverting the wrong law writes an
// asset whose numbers look plausible and whose box drifts on the next geometry,
// so this pins both the right answer and the divergence.
func TestHudCornersReferenceSnapReproducesTheMenuROI(t *testing.T) {
	cal := NewCalibration(1280, 720)
	cal.SetDisplayScale(1.325)

	// The live rect the shipped assets/builder_menu_roi.json maps to, measured on
	// the device by the live wall-upgrade run.
	target := image.Rect(480, 98, 831, 422)

	ref, mapped, ok := cal.HudCornersReferenceSnap(target, 1)
	if !ok {
		t.Fatalf("the shipped menu ROI %v did not invert", target)
	}
	wantRef := image.Rect(309, 74, 574, 413)
	if ref != wantRef {
		t.Errorf("ref = %v, want %v (the reference block the shipped file already holds)", ref, wantRef)
	}
	if mapped != target {
		t.Errorf("the recorded ref maps to %v via Hud, want %v", hudCorners(cal, ref), target)
	}
	if got := hudCorners(cal, ref); got != target {
		t.Errorf("corner-by-corner mapping of %v = %v, want %v", ref, got, target)
	}

	// The rect-centre inverse lands on a different reference rect that happens to
	// produce the same live box here: that is the trap this test exists for.
	centreRef, _, ok := cal.HudRectReferenceSnap(target, 1)
	if !ok {
		t.Fatal("HudRectReferenceSnap refused a rect it should express")
	}
	if centreRef == ref {
		t.Errorf("HudRect and corner-by-corner inverses agree on %v; the menu loader would read the rect-centre "+
			"numbers corner by corner, so if these ever collapse the distinction is gone", target)
	}
	if got := hudCorners(cal, centreRef); got == target {
		t.Errorf("corner-by-corner mapping of the rect-centre ref %v unexpectedly = %v", centreRef, target)
	}
}

// The corner inverse is the one a picker must use for the menu ROI at any
// geometry, so it has to reproduce drags and stay reportable where it cannot.
func TestHudCornersReferenceSnapCoversTheFrame(t *testing.T) {
	for _, g := range []struct{ w, h int }{{1280, 720}, {1600, 900}} {
		cal := NewCalibration(g.w, g.h)
		snapped, worst := 0, 0
		for y := 0; y < g.h-40; y += 53 {
			for x := 0; x < g.w-40; x += 61 {
				target := image.Rect(x, y, x+40, y+40)
				ref, _, ok := cal.HudCornersReferenceSnap(target, 1)
				if !ok {
					continue
				}
				snapped++
				if d := RectDelta(hudCorners(cal, ref), target); d > worst {
					worst = d
				}
				// Whatever the tolerance found, re-reading it the way the loader
				// does must land within the tolerance the caller asked for.
				if _, _, ok := cal.HudCornersReferenceSnap(hudCorners(cal, ref), 1); !ok {
					t.Fatalf("%dx%d: %v did not round-trip", g.w, g.h, target)
				}
			}
		}
		t.Logf("%dx%d: corner-snapped %d samples, worst reassembly %dpx", g.w, g.h, snapped, worst)
		if snapped == 0 {
			t.Fatalf("%dx%d: nothing snapped at all", g.w, g.h)
		}
		if worst > 1 {
			t.Errorf("%dx%d: worst corner fit = %dpx, want <= 1", g.w, g.h, worst)
		}
	}
}

func TestHudCornersReferenceRefusesEmpty(t *testing.T) {
	cal := NewCalibration(1280, 720)
	if _, _, ok := cal.HudCornersReferenceSnap(image.Rectangle{}, 1); ok {
		t.Error("an empty rect was inverted")
	}
	if _, _, ok := cal.HudCornersReferenceSnap(image.Rect(10, 10, 10, 10), 1); ok {
		t.Error("a zero-area rect was inverted")
	}
}
