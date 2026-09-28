package attack

import (
	"image"
	"math"
	"testing"
)

// ---------------------------------------------------------------------------
// A refused hero drop must move OUTWARD.
//
// Live 2026-09-27, one battle on each of the four pinned sides: the game refused
// the hero tap on the user's pinned point (the card stayed armed and full), the
// only alternative candidate was the line's MIDPOINT — equally far inside the
// base's no-deploy pocket — so all eight hero drops failed. The sweep then
// placed both heroes ~40 px further OUT, seconds after the ability pass had
// already skipped them, which is why no ability fired in any of the four
// battles.
//
// These tests pin the ladder that fixes it, using the real pins from
// assets/precision_config.json scaled to the live 1280x720 frame.
// ---------------------------------------------------------------------------

func pinnedLine(p1, p2 image.Point, side string) []image.Point {
	return lineFromPoints(p1, p2, side, linePoints).Points
}

func distFromFieldCentre(p image.Point, w, h int) float64 {
	return math.Hypot(float64(p.X-w/2), float64(p.Y-h/2))
}

// TestOutwardHeroLadderWalksOutAlongThePinnedLine: from the user's BottomLeft
// hero point, the ladder must step along their own troop line toward the screen
// border — and must reach the ground the sweep proved deployable, so the hero
// lands on it without the rescue.
func TestOutwardHeroLadderWalksOutAlongThePinnedLine(t *testing.T) {
	const w, h = 1280, 720
	// The user's BottomLeft pin, as the orchestrator scaled it.
	line := pinnedLine(image.Pt(122, 386), image.Pt(352, 555), "BottomLeft")
	pin := image.Pt(230, 469)

	ladder := outwardHeroLadder(pin, line, w, h, maxHeroGroundCandidates-1)
	if len(ladder) == 0 {
		t.Fatal("no outward ladder for a pinned point inside the base")
	}

	onLine := func(p image.Point) bool {
		for _, lp := range line {
			if lp == p {
				return true
			}
		}
		return false
	}
	pinDist := distFromFieldCentre(pin, w, h)
	for i, p := range ladder {
		if !onLine(p) {
			t.Fatalf("ladder[%d] = %v is not on the user's line; the hero must stay on the ground they drew", i, p)
		}
		if d := distFromFieldCentre(p, w, h); d <= pinDist {
			t.Fatalf("ladder[%d] = %v is %0.0f from the field centre, not further out than the pin (%0.0f); it would be refused too",
				i, p, d, pinDist)
		}
	}

	// The first step has to be far enough out to matter: the sweep succeeded at
	// (192,432) in that battle, and a step of one line index (~20 px) was
	// already known to be short.
	if got := math.Hypot(float64(ladder[0].X-192), float64(ladder[0].Y-432)); got > 60 {
		t.Fatalf("first ladder step %v is %0.0f px from the proven-deployable ground (192,432); too far to be the same pocket edge", ladder[0], got)
	}
}

// TestOutwardHeroLadderStepsAwayFromCentreWhenTheLineIsUnusable: with no usable
// line the ladder still has to move away from the base, which is what a radial
// step from the middle of the field does. Also covers the border clamp.
func TestOutwardHeroLadderStepsAwayFromCentreWhenTheLineIsUnusable(t *testing.T) {
	const w, h = 1280, 720
	pin := image.Pt(1000, 200)

	ladder := outwardHeroLadder(pin, nil, w, h, maxHeroGroundCandidates-1)
	if len(ladder) == 0 {
		t.Fatal("no radial ladder without a line")
	}
	pinDist := distFromFieldCentre(pin, w, h)
	for i, p := range ladder {
		if d := distFromFieldCentre(p, w, h); d <= pinDist {
			t.Fatalf("radial ladder[%d] = %v is not further from the centre than the pin", i, p)
		}
		if p.Y < BandTop() || p.Y > UICutoff(h) {
			t.Fatalf("radial ladder[%d] = %v is outside the deploy band [%d,%d]", i, p, BandTop(), UICutoff(h))
		}
		if p.X < fieldEdgePad || p.X > w-fieldEdgePad {
			t.Fatalf("radial ladder[%d] = %v is on the screen edge", i, p)
		}
	}

	// A pin in the corner must not push steps off the screen.
	corner := outwardHeroLadder(image.Pt(30, 300), nil, w, h, maxHeroGroundCandidates-1)
	for i, p := range corner {
		if p.X < 0 || p.X > w || p.Y < BandTop() || p.Y > UICutoff(h) {
			t.Fatalf("corner ladder[%d] = %v left the field", i, p)
		}
	}
}

// TestHeroDropCandidatesRetriesMoveOutward is the end-to-end property: whatever
// the leading candidate is, the retry after it must be further from the middle
// of the field — that is the difference between recovering a hero on a field tap
// (keeping the ability) and losing it to the sweep.
func TestHeroDropCandidatesRetriesMoveOutward(t *testing.T) {
	const w, h = 1280, 720
	hm := &HeroManager{
		targetEdge:   "BottomLeft",
		pinnedGround: true,
		pCfg: PrecisionConfig{
			Width:  860,
			Height: 732,
			HeroTargets: map[string]image.Point{
				"BottomLeft": {X: 230, Y: 469},
			},
		},
		deployLine: DeployLine{Points: pinnedLine(image.Pt(122, 386), image.Pt(352, 555), "BottomLeft")},
		w:          w,
		h:          h,
	}

	cands := hm.heroDropCandidates(&TrackedSlot{UnitName: "Dragon Duke"}, "dragon duke")
	if len(cands) < 2 {
		t.Fatalf("expected the pin plus outward retries, got %d candidates", len(cands))
	}
	if cands[0] != (image.Point{X: 230, Y: 469}) {
		t.Fatalf("first candidate = %v, want the user's pin (230,469)", cands[0])
	}
	pinDist := distFromFieldCentre(cands[0], w, h)
	if d := distFromFieldCentre(cands[1], w, h); d <= pinDist {
		t.Fatalf("first retry %v is not further out than the pin %v (%0.0f vs %0.0f); a refusal is regional, so this would be refused too",
			cands[1], cands[0], d, pinDist)
	}
	for i, c := range cands {
		if c.Y < BandTop() || c.Y > UICutoff(h) {
			t.Fatalf("candidate %d = %v is outside the deploy band", i, c)
		}
	}
}
