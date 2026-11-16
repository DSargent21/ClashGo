package attack

import (
	"bytes"
	"image"
	"strings"
	"testing"

	"github.com/Ducky705/ClashGO/pkg/formula"
	"github.com/Ducky705/ClashGO/pkg/strategy"
	"github.com/rs/zerolog"
	"gocv.io/x/gocv"
)

// ---------------------------------------------------------------------------
// The hero's FIRST tap must land.
//
// Live runs 10 and 11 refused every authored lead — run 10's formula points
// (MP (240,535), RC (283,169)) and run 11's pin (321,207) — while the very next
// tap on the troop line landed in every case. The contract tested here: the
// first field tap comes from the troop line (clamped into the band), the game
// accepts it on that tap, and neither the rescue re-arm nor the ability budget
// is touched. The no-acceptable-ground case must report FAILED.
// ---------------------------------------------------------------------------

// heroFirstTap deploys one hero through the real batch path with a card ratio
// that transitions on the first post-drop beat (0.74 -> 0.11), and returns the
// field taps, the hero manager and its slot.
func heroFirstTap(t *testing.T, hm *HeroManager, tl *tapLog, name string) (*TrackedSlot, []image.Point) {
	t.Helper()
	slot := heroSlotNamed(name, 287)
	// The slot must be in the manager's index for the confirmation/mark paths.
	hm.slotManager = newTestSlotManager([]*TrackedSlot{slot})
	hm.ratioHook = scriptedRatios(0.74, 0.11)

	d := HeroDeployment{Unit: strategy.Unit{Name: name}, Slot: slot}
	pre := gocv.NewMat()
	defer pre.Close()
	if !hm.deploySingleHero(d, pre) {
		t.Fatalf("%s: first tap must deploy the hero", name)
	}
	return slot, tl.fieldTaps()
}

func heroHarnessWithLine(t *testing.T, line DeployLine) (*HeroManager, *tapLog) {
	t.Helper()
	return newHeroTapHarness(t, line, nil)
}

func assertOnLine(t *testing.T, pt image.Point, line DeployLine, max float64) {
	t.Helper()
	_, d, ok := nearestDeployLinePoint(pt, line)
	if !ok || d > max {
		t.Fatalf("first tap %v is %.1f px from the troop line (max %.0f): it must use ground the troops already used", pt, d, max)
	}
}

// (a) run 11: the authored pin was refused for the siege and both heroes; both
// landed on the line. The first hero tap must already be on the line.
func TestHeroFirstTap_Run11RefusedPin(t *testing.T) {
	line := DeployLine{Points: []image.Point{{X: 461, Y: 130}, {X: 198, Y: 305}}}
	hm, tl := heroHarnessWithLine(t, line)
	hm.targetEdge = "TopLeft"
	hm.pinnedGround = true
	hm.pCfg.HeroTargets = map[string]image.Point{"TopLeft": {X: 321, Y: 207}}

	slot, pts := heroFirstTap(t, hm, tl, "archer queen")
	if len(pts) != 1 {
		t.Fatalf("field taps = %d, want 1 (first tap must land)", len(pts))
	}
	assertOnLine(t, pts[0], line, 65)
	if dist(pts[0], image.Pt(321, 207)) > 80 {
		t.Fatalf("first tap %v wandered from the pinned side", pts[0])
	}
	if slot.SpotRearms != 0 || slot.SpotTaps != 1 {
		t.Fatalf("rescue consumed: spot_taps=%d rearms=%d", slot.SpotTaps, slot.SpotRearms)
	}
}

// (b) run 10: no pin; the formula lead was refused and the line retry landed.
func TestHeroFirstTap_Run10RefusedFormula(t *testing.T) {
	line := DeployLine{Points: []image.Point{{X: 144, Y: 394}, {X: 359, Y: 558}}}
	hm, tl := heroHarnessWithLine(t, line)
	hm.formula = &formula.Formula{Units: map[string]formula.UnitEntry{
		"archer queen": {Type: "point", P: &formula.Point{X: 283, Y: 169}},
	}}

	slot, pts := heroFirstTap(t, hm, tl, "archer queen")
	if len(pts) != 1 {
		t.Fatalf("field taps = %d, want 1 (formula lead must not be retried)", len(pts))
	}
	assertOnLine(t, pts[0], line, 65)
	if slot.SpotRearms != 0 || slot.SpotTaps != 1 {
		t.Fatalf("rescue consumed: spot_taps=%d rearms=%d", slot.SpotTaps, slot.SpotRearms)
	}
}

// (c) A line/author point that falls under the HUD must be clamped into the
// deployable band, not tapped into the UI.
func TestHeroFirstTap_ClampedOutOfHUD(t *testing.T) {
	line := DeployLine{Points: []image.Point{{X: 200, Y: 40}, {X: 500, Y: 90}}}
	hm, tl := heroHarnessWithLine(t, line)
	hm.targetEdge = "TopLeft"
	hm.pinnedGround = true
	hm.pCfg.HeroTargets = map[string]image.Point{"TopLeft": {X: 350, Y: 10}}

	_, pts := heroFirstTap(t, hm, tl, "archer queen")
	if len(pts) != 1 {
		t.Fatalf("field taps = %d, want 1", len(pts))
	}
	if pts[0].Y < BandTop() || pts[0].Y > UICutoff(hm.h) {
		t.Fatalf("first tap %v is not inside the band [%d,%d]", pts[0], BandTop(), UICutoff(hm.h))
	}
}

// (d) A hero with one placement tap left (reduced budget) still lands on the
// first tap: the rescue must not be the thing that makes it work.
func TestHeroFirstTap_ReducedTapBudget(t *testing.T) {
	line := DeployLine{Points: []image.Point{{X: 144, Y: 394}, {X: 359, Y: 558}}}
	hm, tl := heroHarnessWithLine(t, line)
	slot := heroSlotNamed("archer queen", 287)
	slot.SpotTaps = maxHeroSpotTaps - 1 // exactly one placement tap left
	hm.slotManager = newTestSlotManager([]*TrackedSlot{slot})
	hm.ratioHook = scriptedRatios(0.74, 0.11)

	pre := gocv.NewMat()
	defer pre.Close()
	if !hm.deploySingleHero(HeroDeployment{Unit: strategy.Unit{Name: "archer queen"}, Slot: slot}, pre) {
		t.Fatal("the last placement tap must land on the first ground")
	}
	if got := len(tl.fieldTaps()); got != 1 {
		t.Fatalf("field taps = %d, want 1", got)
	}
	if slot.SpotTaps != maxHeroSpotTaps || slot.SpotRearms != 0 {
		t.Fatalf("budget violated: spot_taps=%d rearms=%d", slot.SpotTaps, slot.SpotRearms)
	}
}

// (e) No ground survives selection: the hero must be reported FAILED, not left
// silently on the bar and not tapped at a guess.
func TestHeroFirstTap_NoAcceptableGroundReportsFailed(t *testing.T) {
	slot := heroSlotNamed("archer queen", 287)
	hm, tl := newHeroTapHarness(t, DeployLine{}, []*TrackedSlot{slot})
	var buf bytes.Buffer
	hm.logger = zerolog.New(&buf)

	if got := hm.deployHeroBatch([]HeroDeployment{{Unit: strategy.Unit{Name: "archer queen"}, Slot: slot}}); len(got) != 0 {
		t.Fatalf("deployed = %d, want 0 with no acceptable ground", len(got))
	}
	if slot.State != SlotFailed {
		t.Fatalf("slot state = %v, want FAILED", slot.State)
	}
	if n := len(tl.fieldTaps()); n != 0 {
		t.Fatalf("field taps = %d, want 0: no ground existed to tap", n)
	}
	if !strings.Contains(buf.String(), "hero has no acceptable ground in the deployable band") {
		t.Fatalf("failure was not logged; log=%q", buf.String())
	}
}

// twoSelectionArmHook models what the live game did in runs 10-12: a field tap
// only lands once the hero card has been selected TWICE. With the prime tap the
// first field tap already follows two selections; without it the first drop is
// refused, the rescue re-arms, and SpotRearms becomes 1 — which these tests
// reject.
func twoSelectionArmHook(tl *tapLog) func() (float64, bool) {
	return func() (float64, bool) {
		if len(tl.slotTaps()) >= 2 {
			return 0.11, true
		}
		return 0.74, true
	}
}

func deployWithFirstSelectionRefused(t *testing.T, hm *HeroManager, tl *tapLog, name string, line DeployLine) *TrackedSlot {
	t.Helper()
	slot := heroSlotNamed(name, 287)
	hm.slotManager = newTestSlotManager([]*TrackedSlot{slot})
	hm.ratioHook = twoSelectionArmHook(tl)

	pre := gocv.NewMat()
	defer pre.Close()
	if !hm.deploySingleHero(HeroDeployment{Unit: strategy.Unit{Name: name}, Slot: slot}, pre) {
		t.Fatalf("%s: first field tap must land after the prime selection", name)
	}
	pts := tl.fieldTaps()
	if len(pts) != 1 {
		t.Fatalf("%s: field taps = %d, want 1", name, len(pts))
	}
	assertOnLine(t, pts[0], line, 65)
	if slot.SpotRearms != 0 || slot.SpotTaps != 1 {
		t.Fatalf("%s: rescue consumed: spot_taps=%d rearms=%d", name, slot.SpotTaps, slot.SpotRearms)
	}
	return slot
}

// (a) first selection refused at an authored PIN (run 11).
func TestHeroFirstTap_FirstSelectionRefusedAtPin(t *testing.T) {
	line := DeployLine{Points: []image.Point{{X: 461, Y: 130}, {X: 198, Y: 305}}}
	hm, tl := heroHarnessWithLine(t, line)
	hm.targetEdge = "TopLeft"
	hm.pinnedGround = true
	hm.pCfg.HeroTargets = map[string]image.Point{"TopLeft": {X: 321, Y: 207}}
	deployWithFirstSelectionRefused(t, hm, tl, "archer queen", line)
}

// (b) first selection refused at a FORMULA point (run 10).
func TestHeroFirstTap_FirstSelectionRefusedAtFormula(t *testing.T) {
	line := DeployLine{Points: []image.Point{{X: 144, Y: 394}, {X: 359, Y: 558}}}
	hm, tl := heroHarnessWithLine(t, line)
	hm.formula = &formula.Formula{Units: map[string]formula.UnitEntry{
		"archer queen": {Type: "point", P: &formula.Point{X: 283, Y: 169}},
	}}
	deployWithFirstSelectionRefused(t, hm, tl, "archer queen", line)
}

// (c) first selection refused even though the lead is already TROOP-LINE ground
// (run 12): the prime tap, not the ground, is what makes the first drop land.
func TestHeroFirstTap_FirstSelectionRefusedAtTroopLine(t *testing.T) {
	line := DeployLine{Points: []image.Point{{X: 144, Y: 394}, {X: 359, Y: 558}}}
	hm, tl := heroHarnessWithLine(t, line)
	deployWithFirstSelectionRefused(t, hm, tl, "archer queen", line)
}
