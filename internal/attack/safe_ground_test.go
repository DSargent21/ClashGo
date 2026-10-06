package attack

import (
	"image"
	"testing"
)

// Live failure these tests pin down. 2026-09-30, `Auto EDrag Rush`, target
// BottomRight. Red detection rejected its only candidate and reported no
// boundary, so the run deployed onto the user's pinned line
// (897,557)->(1154,367) — the right 12% of a 1280px frame — and every tap came
// back "You cannot deploy troops on the Red area!". Five slots undeployed,
// 17.5s of deploy phase against a 7s goal, and no log line said why.
//
// The no-deploy ring is a property of the MAP, so with the boundary unknown the
// frame's central box is the only ground whose legality does not depend on
// having solved detection. That is what these tests hold.

func TestSafeGroundRectIsCentralAndNonEmpty(t *testing.T) {
	const (
		w, h     = 1280, 720
		uiCutoff = 612
	)
	safe := SafeGroundRect(w, h, uiCutoff)

	if safe.Empty() {
		t.Fatalf("SafeGroundRect(1280,720,612) = %v, which is empty: every deploy point would be refused", safe)
	}

	// Central on both axes: it must not touch the view's edges, which is where
	// the map's no-deploy ring is.
	if safe.Min.X <= 0 || safe.Max.X >= w {
		t.Errorf("x span %v..%v runs to the frame edge (0..%d); the outer band is unverified ground", safe.Min.X, safe.Max.X, w)
	}
	if safe.Min.Y <= BandTop() || safe.Max.Y >= uiCutoff {
		t.Errorf("y span %v..%v escapes the deploy band %d..%d", safe.Min.Y, safe.Max.Y, BandTop(), uiCutoff)
	}

	// Roughly centred: the box is the middle of the view, not a corner.
	cx := safe.Min.X + (safe.Dx() / 2)
	cy := safe.Min.Y + (safe.Dy() / 2)
	if abs(cx-w/2) > w/10 {
		t.Errorf("box centre x=%d, want within %d of the frame centre %d", cx, w/10, w/2)
	}
	if abs(cy-(BandTop()+uiCutoff)/2) > (uiCutoff-BandTop())/10 {
		t.Errorf("box centre y=%d, want within a tenth of the band %d..%d", cy, BandTop(), uiCutoff)
	}
}

func TestSafeGroundRectSurvivesNarrowAndShortFrames(t *testing.T) {
	// A frame too small for the inset must still yield a legal middle rather
	// than a degenerate rectangle, which would collapse every tap onto one spot.
	for _, tc := range []struct{ w, h, cutoff int }{
		{w: 40, h: 30, cutoff: 25},
		{w: 1280, h: 720, cutoff: 0}, // no band: falls back to full height
		{w: 1280, h: 720, cutoff: BandTop()},
	} {
		safe := SafeGroundRect(tc.w, tc.h, tc.cutoff)
		if safe.Empty() {
			t.Errorf("SafeGroundRect(%d,%d,%d) = %v, empty", tc.w, tc.h, tc.cutoff, safe)
		}
	}
}

func TestTranslateIntoSafeGroundPullsEdgePinInward(t *testing.T) {
	const (
		w, h     = 1280, 720
		uiCutoff = 612
	)
	safe := SafeGroundRect(w, h, uiCutoff)

	// The real BottomRight pin, as the run deployed on it: 15 points from
	// (897,557) to (1154,367).
	start, end := image.Pt(897, 557), image.Pt(1154, 367)
	points := make([]image.Point, linePoints)
	for i := range points {
		t2 := float64(i) / float64(linePoints-1)
		points[i] = image.Pt(
			start.X+int(float64(end.X-start.X)*t2),
			start.Y+int(float64(end.Y-start.Y)*t2),
		)
	}

	dx, _, moved := TranslateIntoSafeGround(points, safe)
	if !moved {
		t.Fatal("the pinned BottomRight line was reported already-safe, but it spans the right edge of the frame")
	}
	if dx >= 0 {
		t.Errorf("dx = %d, want a leftward shift: the pin's right end was at x=%d in a %d wide frame", dx, end.X, w)
	}
	for i, p := range points {
		if !p.In(safe) {
			t.Fatalf("point %d = %v is still outside the safe ground %v", i, p, safe)
		}
	}

	// Direction and length must survive: this is the pinned line moved, not a
	// different line, and not every unit collapsed onto one tile.
	wantDX, wantDY := end.X-start.X, end.Y-start.Y
	gotDX := points[len(points)-1].X - points[0].X
	gotDY := points[len(points)-1].Y - points[0].Y
	if gotDX != wantDX || gotDY != wantDY {
		t.Errorf("line vector changed: got (%d,%d), want (%d,%d)", gotDX, gotDY, wantDX, wantDY)
	}
}

func TestTranslateIntoSafeGroundLeavesCentralLineUntouched(t *testing.T) {
	const (
		w, h     = 1280, 720
		uiCutoff = 612
	)
	safe := SafeGroundRect(w, h, uiCutoff)

	// A diagonal through the middle of the base — the geometry the log calls the
	// last thing that should stand in for real ground, but which is legal. It
	// must come out byte-identical, or the fix silently re-aims every good plan.
	original := []image.Point{
		image.Pt(500, 300), image.Pt(560, 330), image.Pt(620, 360),
		image.Pt(680, 390), image.Pt(740, 420),
	}
	points := append([]image.Point(nil), original...)

	dx, dy, moved := TranslateIntoSafeGround(points, safe)
	if moved {
		t.Errorf("moved = true (dx=%d, dy=%d) for a line already inside %v", dx, dy, safe)
	}
	for i := range points {
		if points[i] != original[i] {
			t.Errorf("point %d changed from %v to %v with no move reported", i, original[i], points[i])
		}
	}
}

func TestTranslateIntoSafeGroundCentresAnOversizedLine(t *testing.T) {
	const (
		w, h     = 1280, 720
		uiCutoff = 612
	)
	safe := SafeGroundRect(w, h, uiCutoff)

	// A line longer than the box cannot be moved in without losing length, so no
	// translation makes it fit. The honest answer is to centre it: the midpoint
	// lands on the middle of the safe ground even though the ends still
	// overhang. That is a partial rescue, not a silent one — the caller logs the
	// offsets, and the battle still reports what it deployed.
	points := []image.Point{image.Pt(-200, 250), image.Pt(1500, 480)}
	dx, dy, moved := TranslateIntoSafeGround(points, safe)
	if !moved {
		t.Fatal("moved = false for a line spanning -200..1500 in a 1280 wide frame")
	}
	if dx == 0 {
		t.Errorf("dx = 0: an off-frame line was reported already-centred")
	}
	if dy != 0 {
		t.Errorf("dy = %d: y spans 250..480 and already fits inside %v, so it must not move", dy, safe)
	}
	midX := (points[0].X + points[1].X) / 2
	wantMidX := (safe.Min.X + safe.Max.X) / 2
	if diff := midX - wantMidX; diff > 1 || diff < -1 {
		t.Errorf("midpoint x = %d, want the middle of the safe ground %d (within 1px)", midX, wantMidX)
	}
}

func TestTranslateIntoSafeGroundIgnoresEmptyInput(t *testing.T) {
	safe := SafeGroundRect(1280, 720, 612)
	if _, _, moved := TranslateIntoSafeGround(nil, safe); moved {
		t.Error("an empty line reported a move")
	}
	if _, _, moved := TranslateIntoSafeGround([]image.Point{image.Pt(5, 5)}, image.Rectangle{}); moved {
		t.Error("a move was reported against an empty safe rect")
	}
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
