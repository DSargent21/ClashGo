package attack

import (
	"image"
	"image/color"
	"math"
	"testing"

	"github.com/Ducky705/ClashGO/pkg/formula"
	"github.com/Ducky705/ClashGO/pkg/strategy"
	"gocv.io/x/gocv"
)

// ---------------------------------------------------------------------------
// Hero ground selection (hero_ground.go) — the "heroes were dropped inside the
// red line" defect, run10 2026-09-27.
//
// In that run the pinned reference-geometry edge line put both heroes at
// (935,485) while every troop and spell had deployed along the formula line at
// x≈1088. The game refused the hero taps (the slots never left their resting
// ratio), the ability pass correctly refused to fire, and the sweep had to
// re-place both heroes seconds later. These tests pin the rule that fixes it:
// a candidate INSIDE the red outline is never the first choice.
// ---------------------------------------------------------------------------

// rectZone builds a RedZone whose outline is an axis-aligned rectangle. Real
// zones are contours; a rectangle is enough to answer the only question these
// tests ask (inside vs outside).
func rectZone(x0, y0, x1, y1 int) RedZone {
	return RedZone{
		Valid: true,
		BBox:  image.Rect(x0, y0, x1, y1),
		Polygon: []image.Point{
			{X: x0, Y: y0}, {X: x1, Y: y0}, {X: x1, Y: y1}, {X: x0, Y: y1},
		},
	}
}

// TestHeroGroundCandidates_AvoidsBlockedPinnedLine: when the whole pinned line
// is inside the red line (a stale pin on a base that moved), the picker must
// still hand back ground outside it — pushed radially out, the same rule the
// formula path uses — and keep the blocked points only as last-resort
// fallbacks.
func TestHeroGroundCandidates_AvoidsBlockedPinnedLine(t *testing.T) {
	line := []image.Point{{X: 935, Y: 300}, {X: 935, Y: 450}, {X: 935, Y: 600}}
	zone := rectZone(400, 100, 1010, 650)

	cands := heroGroundCandidates(line, 720, zone)
	if len(cands) == 0 {
		t.Fatal("no candidates for a fully blocked line")
	}
	if zone.Inside(cands[0].X, cands[0].Y) {
		t.Fatalf("first candidate %v is inside the red line; the drop would be refused", cands[0])
	}
	if cands[0].X <= 1010 {
		t.Fatalf("pushed candidate %v did not clear the zone's right edge (x>1010)", cands[0])
	}
	for _, c := range cands[1:] {
		if !zone.Inside(c.X, c.Y) {
			t.Errorf("candidate %v should be a blocked fallback, not free ground", c)
		}
	}
}

// TestHeroGroundCandidates_PrefersClearGroundOutsideLine: with the line
// straddling the red line's edge, the outside points come first and every
// blocked point is demoted below them.
func TestHeroGroundCandidates_PrefersClearGroundOutsideLine(t *testing.T) {
	line := []image.Point{
		{X: 935, Y: 300}, {X: 935, Y: 450}, {X: 935, Y: 600},
		{X: 1088, Y: 300}, {X: 1088, Y: 450}, {X: 1088, Y: 600},
	}
	zone := rectZone(400, 100, 1010, 650)

	cands := heroGroundCandidates(line, 720, zone)
	if len(cands) != len(line) {
		t.Fatalf("got %d candidates, want %d (one per line point)", len(cands), len(line))
	}
	if cands[0].X != 1088 {
		t.Fatalf("first candidate = %v, want an x=1088 point (the clear ground the troops used)", cands[0])
	}
	seenBlocked := false
	for _, c := range cands {
		if zone.Inside(c.X, c.Y) {
			seenBlocked = true
			continue
		}
		if seenBlocked {
			t.Errorf("clear point %v ranked below a blocked one", c)
		}
	}
}

// TestHeroGroundCandidates_NoZoneKeepsLegacyChoice: without a usable outline
// the picker must behave exactly as before (the band point nearest the band
// centroid), because "unknown" must never move a point the user pinned.
func TestHeroGroundCandidates_NoZoneKeepsLegacyChoice(t *testing.T) {
	line := []image.Point{{X: 200, Y: 300}, {X: 500, Y: 450}, {X: 800, Y: 100}}
	cands := heroGroundCandidates(line, 720, RedZone{})
	if len(cands) != 1 {
		t.Fatalf("got %d candidates, want 1 (no alternates without a zone)", len(cands))
	}
	// (800,100) is above yTopMin and must be filtered out; of the rest the
	// centroid is (350,375) and (200,300)/(500,450) tie, so the first wins.
	if cands[0] != (image.Point{X: 200, Y: 300}) {
		t.Errorf("legacy choice = %v, want (200,300)", cands[0])
	}
}

// TestHeroDeploy_UnconfirmedDropLeavesNoPlacementBudget pins the user-requested
// cap: when the single field tap does not confirm, the hero is reported, not
// re-armed and re-dropped. One card tap, one field tap, nothing more.
func TestHeroDeploy_UnconfirmedDropLeavesNoPlacementBudget(t *testing.T) {
	line := []image.Point{
		{X: 935, Y: 300}, {X: 935, Y: 450}, {X: 935, Y: 600},
		{X: 1088, Y: 300}, {X: 1088, Y: 450}, {X: 1088, Y: 600},
	}
	slot := heroSlotNamed("archer queen", 287)
	hm, tl := newHeroTapHarness(t, DeployLine{Points: line}, []*TrackedSlot{slot})
	hm.SetRedZone(rectZone(400, 100, 1010, 650))
	hm.ratioHook = scriptedRatios(0.74, 0.74, 0.74)

	d := HeroDeployment{Unit: strategy.Unit{Name: "Archer Queen"}, Slot: slot}
	pre := gocv.NewMat()
	defer pre.Close()
	if hm.deploySingleHero(d, pre) {
		t.Fatal("deploySingleHero must not claim success when the drop never confirms")
	}
	if got := len(tl.slotTaps()); got != 1 {
		t.Errorf("card taps = %d, want 1 (selection only)", got)
	}
	if slot.SpotTaps != 1 {
		t.Errorf("placement budget spent = %d, want 1", slot.SpotTaps)
	}
	if slot.SpotRearms != 0 {
		t.Errorf("re-arms = %d, want 0: the rescue pass is gone", slot.SpotRearms)
	}
	if got := len(tl.fieldTaps()); got != 1 {
		t.Errorf("field taps = %d, want 1 (drop only)", got)
	}
	if hm.executor.HeroSpotTapBudgetLeft(slot) {
		t.Error("the one placement tap is spent; no later path may tap the hero again")
	}
}

// TestHeroDeploy_FormulaGroundBeatsStalePinnedEdge is the run12 fix. In that run
// the strategy's formula pinned every hero at x=1088 (the right edge, where the
// troop pass had just deployed cleanly), but the hero path aimed at the pinned
// reference-geometry edge instead — a diagonal cutting into the base — and the
// game refused all four. The formula ground must win.
func TestHeroDeploy_FormulaGroundBeatsStalePinnedEdge(t *testing.T) {
	// The stale pinned edge: two points that sit inside the red line.
	line := DeployLine{Points: []image.Point{{X: 838, Y: 143}, {X: 989, Y: 216}}}
	slot := heroSlotNamed("archer queen", 287)
	hm, tl := newHeroTapHarness(t, line, []*TrackedSlot{slot})
	hm.SetRedZone(rectZone(400, 100, 1010, 650))
	hm.formula = &formula.Formula{Units: map[string]formula.UnitEntry{
		"archer queen": {Type: "point", P: &formula.Point{X: 1088, Y: 288}},
	}}
	hm.ratioHook = scriptedRatios(0.74, 0.11)

	d := HeroDeployment{Unit: strategy.Unit{Name: "Archer Queen"}, Slot: slot}
	pre := gocv.NewMat()
	defer pre.Close()
	if !hm.deploySingleHero(d, pre) {
		t.Fatal("deploySingleHero failed on a drop whose slot transitioned to cooldown")
	}

	pts := tl.fieldTaps()
	if len(pts) != 1 {
		t.Fatalf("got %d field taps, want 1 (the first ground must be accepted, no rescue)", len(pts))
	}
	// The formula point (1088,288) is off the 860-wide field. It is clamped into
	// the deployable band rather than tapped off-grid, and the stale edge line
	// (inside the measured red zone) never leads.
	if pts[0].Y < BandTop() || pts[0].Y > UICutoff(hm.h) {
		t.Fatalf("first hero field tap %v is outside the deployable band", pts[0])
	}
	for _, lp := range line.Points {
		if dist(pts[0], lp) <= 12 {
			t.Fatalf("first tap %v fell back to the stale edge line point %v", pts[0], lp)
		}
	}
	if slot.SpotRearms != 0 || slot.SpotTaps != 1 {
		t.Fatalf("first tap used the rescue: spot_taps=%d rearms=%d", slot.SpotTaps, slot.SpotRearms)
	}
}

// TestHeroDeploy_TroopGroundAxisBeatsPushedFormulaPoint: the orchestrator's
// radial red-zone push can move a hero pin far sideways onto ground the game
// refuses (run13: Duke pushed to x=45 and Warden to x=118 while the troop line
// at x=192 had just been accepted). The hero drop must snap back onto the axis
// the troop pass used and keep only the hero's own perpendicular coordinate.
func TestHeroDeploy_TroopGroundAxisBeatsPushedFormulaPoint(t *testing.T) {
	line := DeployLine{Points: []image.Point{{X: 838, Y: 143}, {X: 989, Y: 216}}}
	slot := heroSlotNamed("dragon duke", 751)
	hm, tl := newHeroTapHarness(t, line, []*TrackedSlot{slot})
	hm.SetRedZone(rectZone(400, 100, 1010, 650))
	// The pushed (unusable) pin, plus the line the troop pass deployed on.
	hm.formula = &formula.Formula{Units: map[string]formula.UnitEntry{
		"dragon duke": {Type: "point", P: &formula.Point{X: 45, Y: 187}},
	}}
	hm.recordGround(image.Pt(192, 144), image.Pt(192, 576))
	hm.ratioHook = scriptedRatios(0.74, 0.11)

	d := HeroDeployment{Unit: strategy.Unit{Name: "Dragon Duke"}, Slot: slot}
	pre := gocv.NewMat()
	defer pre.Close()
	if !hm.deploySingleHero(d, pre) {
		t.Fatal("deploySingleHero failed on a drop whose slot transitioned to cooldown")
	}

	pts := tl.fieldTaps()
	if len(pts) != 1 {
		t.Fatalf("got %d field taps, want 1", len(pts))
	}
	if math.Abs(float64(pts[0].X-192)) > 3 || math.Abs(float64(pts[0].Y-187)) > 3 {
		t.Errorf("hero drop %v must snap onto the troop line's axis (x=192) at the hero's own y, not the pushed pin (45,187)", pts[0])
	}
}

// TestHeroGround_ChosenGroundIsTheClearOne pins what the deploy path actually
// asks for: heroDropCandidates hands back the clear ground first, with the
// blocked points demoted behind it as fallbacks.
func TestHeroGround_ChosenGroundIsTheClearOne(t *testing.T) {
	line := []image.Point{
		{X: 935, Y: 300}, {X: 935, Y: 450}, {X: 935, Y: 600},
		{X: 1088, Y: 300}, {X: 1088, Y: 450}, {X: 1088, Y: 600},
	}
	slot := heroSlotNamed("barbarian king", 450)
	hm, _ := newHeroTapHarness(t, DeployLine{Points: line}, []*TrackedSlot{slot})
	hm.SetRedZone(rectZone(400, 100, 1010, 650))

	cands := hm.heroDropCandidates(slot, "Barbarian King")
	if len(cands) < 2 {
		t.Fatalf("got %d candidates, want the blocked points demoted plus clear ground", len(cands))
	}
	if cands[0].Y < BandTop() || cands[0].Y > UICutoff(hm.h) {
		t.Fatalf("chosen ground %v is outside the deployable band", cands[0])
	}
	if hm.zone.Inside(cands[0].X, cands[0].Y) {
		t.Errorf("chosen ground %v is inside the red line", cands[0])
	}
}

// TestHeroDropCandidatesDoNotSampleDiagonalTroopLineInterior is the 2026-11-11
// fix. The TopLeft pin ran diagonally across the village, the hero path took
// fractions along it, the game refused all four heroes there, and the sweep's
// authored point beside the line placed every one of them on its first tap.
// A diagonal troop line is not a set of proven hero tiles: only its ENDPOINTS
// are the anchors the pass fired first.
func TestHeroDropCandidatesDoNotSampleDiagonalTroopLineInterior(t *testing.T) {
	line := lineFromPoints(image.Pt(461, 130), image.Pt(198, 305), "TopLeft", linePoints)
	hm := &HeroManager{
		targetEdge:   "TopLeft",
		pinnedGround: true,
		pCfg: PrecisionConfig{
			Width:  1024,
			Height: 768,
			HeroTargets: map[string]image.Point{
				"TopLeft": {X: 321, Y: 207},
			},
		},
		deployLine:  line,
		troopGround: []image.Point{{X: 461, Y: 130}, {X: 198, Y: 305}},
		w:           1024,
		h:           768,
	}

	cands := hm.heroDropCandidates(&TrackedSlot{UnitName: "archer queen"}, "archer queen")
	if len(cands) < 2 {
		t.Fatalf("got %d candidates, want authored ground plus retries", len(cands))
	}
	for _, p := range []image.Point{{X: 395, Y: 174}, {X: 264, Y: 261}, {X: 422, Y: 156}, {X: 238, Y: 278}} {
		if containsPoint(cands, p) {
			t.Fatalf("candidate %v is an interior fraction of the diagonal troop line; the game refused every hero dropped there live", p)
		}
	}
	for i, c := range cands {
		if c.Y < BandTop() || c.Y > UICutoff(hm.h) {
			t.Fatalf("candidate %d = %v sits outside the deploy band", i, c)
		}
	}
}

// TestRunHeroFieldRetriesRearmsBeforeEveryRetry: every refused field tap
// deselects the card, so a retry that does not re-arm fires at nothing. The
// live 2026-11-11 run measured exactly that (delta 0.0003 and 0.0000 on the
// second and third retries) before this rule.
func TestRunHeroFieldRetriesRearmsBeforeEveryRetry(t *testing.T) {
	cands := []image.Point{{X: 10, Y: 10}, {X: 20, Y: 10}, {X: 30, Y: 10}, {X: 40, Y: 10}}
	rearms, fired := 0, 0
	won := runHeroFieldRetries(
		cands, 0.7, true,
		func() bool { rearms++; return true },
		func(image.Point) { fired++ },
		func() (float64, bool) { return 0.7, true }, // never transitions
		nil,
		func() bool { return false },
		func(int, image.Point, float64, float64, float64, bool) {},
	)
	if won {
		t.Fatal("no retry transitions the card, so the run must not report a placement")
	}
	if rearms != heroFieldRetries || fired != heroFieldRetries {
		t.Fatalf("re-arms = %d, field taps = %d, want %d of each (one re-arm per retry)", rearms, fired, heroFieldRetries)
	}
}

// TestHeroDeploy_FirstGroundIsFormulaPoint pins the ground the first tap uses
// for a Point-pattern hero: the strategy formula point, the same ground the
// sweep lands on its first tap. The troop-line lead was refused for every hero
// live on 2026-11-19, which cost the ability pass.
func TestHeroDeploy_FirstGroundIsFormulaPoint(t *testing.T) {
	line := DeployLine{Points: []image.Point{{X: 200, Y: 300}, {X: 200, Y: 600}}}
	slot := heroSlotNamed("archer queen", 287)
	hm, tl := newHeroTapHarness(t, line, []*TrackedSlot{slot})
	hm.formula = &formula.Formula{Units: map[string]formula.UnitEntry{
		"archer queen": {Type: "point", P: &formula.Point{X: 220, Y: 450}},
	}}
	hm.ratioHook = scriptedRatios(0.74, 0.11)

	cands := hm.heroDropCandidates(slot, "archer queen")
	if len(cands) == 0 {
		t.Fatal("no hero candidates")
	}
	want := hm.executor.safeFieldPoint(ClampToDeployBand(image.Pt(220, 450), UICutoff(hm.h)))
	_ = cands

	d := HeroDeployment{Unit: strategy.Unit{Name: "Archer Queen", Pattern: "Point"}, Slot: slot}
	pre := gocv.NewMat()
	defer pre.Close()
	if !hm.deploySingleHero(d, pre) {
		t.Fatal("deploy failed on a drop whose card transitioned")
	}
	pts := tl.fieldTaps()
	if len(pts) != 1 {
		t.Fatalf("field taps = %d, want 1", len(pts))
	}
	if dist(pts[0], want) > 6 {
		t.Fatalf("first ground = %v, want formula point %v", pts[0], want)
	}
}

// TestHeroHpBarVisible pins the deploy signal that replaced the card-activity
// delta: the green health bar above the hero card. The old delta read 0.59 ->
// 0.60 on a landed hero (live 2026-11-19), so it called every deployment a
// failure and skipped the ability pass.
func TestHeroHpBarVisible(t *testing.T) {
	screen := gocv.NewMatWithSize(768, 1024, gocv.MatTypeCV8UC3)
	defer screen.Close()
	gocv.Rectangle(&screen, image.Rect(0, 0, 1024, 768), color.RGBA{R: 30, G: 30, B: 30, A: 255}, -1)
	slot := heroSlotNamed("archer queen", 325)
	slot.Y = 705 // measured hero bar position at 1024 px

	if heroHpBarVisible(screen, slot, 1024) {
		t.Fatal("plain card background reported a deployed health bar")
	}
	// 70 px above the slot, 60x12: the measured bar position at 1024 px.
	gocv.Rectangle(&screen, image.Rect(295, 635, 355, 647), color.RGBA{R: 0, G: 255, B: 0, A: 255}, -1)
	if !heroHpBarVisible(screen, slot, 1024) {
		t.Fatal("green health bar above the card was not detected")
	}
}
