package attack

import (
	"image"
	"testing"

	"github.com/Ducky705/ClashGO/pkg/formula"
)

// ---------------------------------------------------------------------------
// A hand-pinned side must own the hero ground too.
//
// Live TopLeft run 2026-09-27: the troop lines correctly used the user's pin
// (log: p1={148,283} p2={443,130} src=pin) and the siege went to the user's hero
// point, but BOTH heroes were dropped at the formula's mirrored point
// ({192,288} / {192,216}, log: ground_source=formula). The user had pinned
// (281,181) for that side.
//
// Two separate causes, one rule: heroDropCandidates took its "intent" from the
// formula whenever a formula entry existed, so the pinned hero point was only
// reachable through the axis-snap fallback — and a DIAGONAL pinned line has no
// shared axis, so that fallback never fired. resolveHeroTarget now also gives a
// pinned hero point priority over the formula's per-unit entry.
// ---------------------------------------------------------------------------

// pinnedHM builds a HeroManager for a side the user pinned by hand: the troop
// line is the real diagonal TopLeft pin, and precision_config carries the real
// hero point for that side.
func pinnedHM() *HeroManager {
	return &HeroManager{
		targetEdge:   "TopLeft",
		pinnedGround: true,
		pCfg: PrecisionConfig{
			Width:  860,
			Height: 732,
			HeroTargets: map[string]image.Point{
				"TopLeft": {X: 281, Y: 181},
			},
		},
		// The pinned diagonal troop line, as the orchestrator scaled it.
		deployLine: DeployLine{
			Points: []image.Point{{X: 148, Y: 283}, {X: 443, Y: 130}},
			Side:   "TopLeft",
		},
		troopGround: []image.Point{{X: 148, Y: 283}, {X: 250, Y: 227}, {X: 443, Y: 130}},
		w:           1280,
		h:           720,
	}
}

func TestPinnedHeroPointFoundForPinnedSide(t *testing.T) {
	hm := pinnedHM()

	got, ok := hm.pinnedHeroPoint()
	if !ok {
		t.Fatal("pinnedHeroPoint reported no pin for a side the user pinned")
	}
	if got != (image.Point{X: 281, Y: 181}) {
		t.Fatalf("pinnedHeroPoint = %v, want the user's (281,181)", got)
	}

	// An unpinned battle must keep whatever the strategy authored.
	hm.pinnedGround = false
	if _, ok := hm.pinnedHeroPoint(); ok {
		t.Fatal("pinnedHeroPoint reported a pin for an UNPINNED battle")
	}
}

// TestHeroDropCandidatesDemotesRefusedPin pins the 2026-10-05 ordering fix.
//
// The pin used to lead every candidate list (ground_source=pinned in the log),
// and live run1 + probe3 then refused that first tap 11/12 while the outward
// points beside it landed on the rescue taps. With no axis to prove (this pin's
// troop line is diagonal, so groundAxis never matches), the outward order is
// the only evidence left — it must beat both the pin and the formula mirror.
func TestHeroDropCandidatesDemotesRefusedPin(t *testing.T) {
	hm := pinnedHM()

	// A formula that carries its own hero entry, exactly like
	// auto_edrag_rush_formula.json does (mirrored to this side's x=192).
	hm.formula = &formula.Formula{
		Units: map[string]formula.UnitEntry{
			"dragon duke": {Type: "point", P: &formula.Point{X: 192, Y: 216}, Jitter: 6},
		},
	}

	slot := &TrackedSlot{UnitName: "Dragon Duke"}
	cands := hm.heroDropCandidates(slot, "dragon duke")
	if len(cands) == 0 {
		t.Fatal("no hero drop candidates")
	}
	pin := image.Point{X: 281, Y: 181}
	if cands[0] == pin {
		t.Fatalf("first candidate = the pin %v; a pin-led first tap was refused 11/12 live — outward ground must lead", cands[0])
	}
	if cands[0] == (image.Point{X: 192, Y: 216}) {
		t.Fatalf("first candidate = the formula mirror %v; the pin's side must not take the formula's point", cands[0])
	}
	mid := image.Pt(hm.w/2, hm.h/2)
	if dist2(cands[0], mid) <= dist2(pin, mid) {
		t.Fatalf("first candidate %v is not further from the field centre than the pin %v; the first tap must be the outward ground",
			cands[0], pin)
	}
}

func TestResolveHeroTargetPrefersPinOverFormula(t *testing.T) {
	hm := pinnedHM()
	hm.formula = &formula.Formula{
		Units: map[string]formula.UnitEntry{
			"barbarian king": {Type: "point", P: &formula.Point{X: 192, Y: 216}, Jitter: 6},
		},
	}

	got, _ := hm.resolveHeroTarget(&TrackedSlot{UnitName: "Barbarian King"})
	if got != (image.Point{X: 281, Y: 181}) {
		t.Fatalf("resolveHeroTarget = %v, want the pinned (281,181)", got)
	}

	// With no pin for the side, the formula still wins — an unpinned battle must
	// not lose the formula's per-unit hero points.
	hm.pinnedGround = false
	got, _ = hm.resolveHeroTarget(&TrackedSlot{UnitName: "Barbarian King"})
	if got != (image.Point{X: 192, Y: 216}) {
		t.Fatalf("unpinned resolveHeroTarget = %v, want the formula's (192,216)", got)
	}
}
