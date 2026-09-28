package attack

import (
	"image"
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

// TestHeroDeploy_FieldRetryOnNewGroundKeepsCardBudget: a refused drop is
// recovered by re-aiming the FIELD, and the field can only be re-aimed after the
// card is re-selected — the game DESELECTS a hero's card when it refuses the
// ground (measured live 2026-09-27, see maxHeroRearms).
//
// The re-arm is not a second placement tap: the game never registered the first
// one as a selection, so the placement budget is untouched and the ability still
// has its tap. That is the difference between this and the old "displaced retry"
// that spent the hero's second tap on placement and left the ability nothing.
func TestHeroDeploy_FieldRetryOnNewGroundKeepsCardBudget(t *testing.T) {
	line := []image.Point{
		{X: 935, Y: 300}, {X: 935, Y: 450}, {X: 935, Y: 600},
		{X: 1088, Y: 300}, {X: 1088, Y: 450}, {X: 1088, Y: 600},
	}
	slot := heroSlotNamed("archer queen", 287)
	hm, tl := newHeroTapHarness(t, DeployLine{Points: line}, []*TrackedSlot{slot})
	hm.SetRedZone(rectZone(400, 100, 1010, 650))
	// pre 0.74, first drop unconfirmed (delta 0), then the field retry lands
	// (0.74 -> 0.11).
	hm.ratioHook = scriptedRatios(0.74, 0.74, 0.11)

	d := HeroDeployment{Unit: strategy.Unit{Name: "Archer Queen"}, Slot: slot}
	pre := gocv.NewMat()
	defer pre.Close()
	if !hm.deploySingleHero(d, pre) {
		t.Fatal("deploySingleHero must confirm a placement the field retry achieved")
	}

	if got := len(tl.slotTaps()); got != 2 {
		t.Errorf("card taps = %d, want 2 (the placement plus one re-arm)", got)
	}
	if slot.SpotTaps != 1 {
		t.Errorf("placement budget spent = %d, want 1: the re-arm must not eat the ability's tap", slot.SpotTaps)
	}
	if slot.SpotRearms != 1 {
		t.Errorf("re-arms = %d, want exactly 1", slot.SpotRearms)
	}
	pts := tl.fieldTaps()
	if len(pts) != 2 {
		t.Fatalf("field taps = %d, want 2 (the drop plus one field retry)", len(pts))
	}
	if pts[0] == pts[1] {
		t.Errorf("the field retry must aim at new ground, both taps were %v", pts[0])
	}
	if !hm.executor.HeroSpotTapBudgetLeft(slot) {
		t.Error("the ability tap (card tap #2) must still be available")
	}
}

// TestHeroDeploy_ExhaustedFieldRetriesCostNoCardTap: even when every candidate
// fails, the retries themselves are field taps only, and the single re-arm is
// not renewable — the hero keeps its ability tap, which is what the 2-tap
// ceiling exists to protect.
func TestHeroDeploy_ExhaustedFieldRetriesCostNoCardTap(t *testing.T) {
	line := []image.Point{
		{X: 935, Y: 300}, {X: 935, Y: 450}, {X: 935, Y: 600},
		{X: 1088, Y: 300}, {X: 1088, Y: 450}, {X: 1088, Y: 600},
	}
	slot := heroSlotNamed("grand warden", 650)
	hm, tl := newHeroTapHarness(t, DeployLine{Points: line}, []*TrackedSlot{slot})
	hm.SetRedZone(rectZone(400, 100, 1010, 650))
	hm.ratioHook = scriptedRatios(0.74, 0.74, 0.74, 0.74, 0.74)

	d := HeroDeployment{Unit: strategy.Unit{Name: "Grand Warden"}, Slot: slot}
	pre := gocv.NewMat()
	defer pre.Close()
	if hm.deploySingleHero(d, pre) {
		t.Fatal("deploySingleHero must not claim success when nothing confirms")
	}
	if got := len(tl.slotTaps()); got != 2 {
		t.Errorf("card taps = %d, want exactly 2 (the placement plus one re-arm)", got)
	}
	if slot.SpotTaps != 1 {
		t.Errorf("placement budget spent = %d, want 1: retries must never cost the ability's tap", slot.SpotTaps)
	}
	if got := len(tl.fieldTaps()); got != 1+heroFieldRetries {
		t.Errorf("field taps = %d, want %d (the drop plus %d retries)", got, 1+heroFieldRetries, heroFieldRetries)
	}
	if hm.executor.RearmSlot(slot) {
		t.Error("a second re-arm must be refused: maxHeroRearms bounds how often the card may be re-tapped")
	}
	if !hm.executor.HeroSpotTapBudgetLeft(slot) {
		t.Error("a card tap must remain for the sweep/verifier to recover the hero, and for its ability")
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
		t.Fatalf("got %d field taps, want 1 (the formula point is accepted on tap #1)", len(pts))
	}
	want := image.Pt(1088, 288)
	if math.Abs(float64(pts[0].X-want.X)) > 3 || math.Abs(float64(pts[0].Y-want.Y)) > 3 {
		t.Errorf("first hero field tap %v is not the strategy's pinned ground %v (it must not fall back to the stale edge line)", pts[0], want)
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
	if cands[0].X != 1088 {
		t.Fatalf("chosen ground = %v, want an x=1088 point", cands[0])
	}
	if hm.zone.Inside(cands[0].X, cands[0].Y) {
		t.Errorf("chosen ground %v is inside the red line", cands[0])
	}
}
