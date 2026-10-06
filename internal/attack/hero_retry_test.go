package attack

import (
	"image"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Ducky705/ClashGO/internal/attacklog"
	"github.com/Ducky705/ClashGO/pkg/formula"
	"github.com/Ducky705/ClashGO/pkg/strategy"
	"github.com/rs/zerolog"
	"gocv.io/x/gocv"
)

// ---------------------------------------------------------------------------
// Hero card tap ceiling — the "it keeps tapping the Archer Queen" defect
// (2026-09-26)
//
// A hero is placed at most twice per battle (the drop plus one rescue) and
// activated exactly once. The old main-phase "displaced retry" spent the second
// tap on placement (its pinned-tile verify kept false-failing), which left the
// ability pass to fire a THIRD tap, and the sweep/verifier a fourth. The
// placement ceiling is enforced in one place — TapExecutor.consumeHeroSpotTap —
// and the activation in exactly one other, TrackedSlot.AbilityFired.
// ---------------------------------------------------------------------------

// newHeroTapHarness builds a HeroManager over the closed-client tap harness.
// Card taps are recorded in tl.slotTaps() (StdDev <= 4) and field taps in
// tl.fieldTaps() (StdDev > 4), so placement vs selection is fully observable.
func newHeroTapHarness(t *testing.T, line DeployLine, slots []*TrackedSlot) (*HeroManager, *tapLog) {
	t.Helper()
	sd, tl := newSpellTestHarness(t, PrecisionConfig{}, &formula.Formula{Screen: formula.ScreenSize{W: 860, H: 732}}, nil, slots)
	hm := NewHeroManager(sd.executor, nil, PrecisionConfig{}, "BottomLeft", 860, 732, nil, nil, line, zerolog.Nop())
	return hm, tl
}

func heroSlotNamed(name string, x int) *TrackedSlot {
	return &TrackedSlot{TroopSlot: TroopSlot{X: x, Y: 682, Category: "Hero"}, UnitName: name, Confidence: 0.95}
}

// scriptedRatios returns a ratioHook that yields the given readings in order,
// repeating the last one once the sequence is exhausted (so a stray extra
// read cannot flip a verdict).
func scriptedRatios(vals ...float64) func() (float64, bool) {
	i := 0
	return func() (float64, bool) {
		v := vals[len(vals)-1]
		if i < len(vals) {
			v = vals[i]
		}
		i++
		return v, true
	}
}

func nearDeployLine(pt image.Point, line DeployLine) bool {
	for _, lp := range line.Points {
		if dist(pt, lp) <= 12 {
			return true
		}
	}
	return false
}

// TestHeroDeploy_OnePlacementTapThenAbility is the contract: placing a hero
// uses exactly one card tap and one field tap, the ability then uses the
// second (and last) card tap, and nothing can tap the card a third time.
func TestHeroDeploy_OnePlacementTapThenAbility(t *testing.T) {
	line := DeployLine{Points: []image.Point{{X: 200, Y: 300}, {X: 500, Y: 450}}}
	slot := heroSlotNamed("archer queen", 287)
	hm, tl := newHeroTapHarness(t, line, []*TrackedSlot{slot})
	hm.ratioHook = scriptedRatios(0.74, 0.11) // place succeeds (delta 0.63)

	d := HeroDeployment{Unit: strategy.Unit{Name: "Archer Queen"}, Slot: slot}
	pre := gocv.NewMat()
	defer pre.Close()
	if !hm.deploySingleHero(d, pre) {
		t.Fatal("deploySingleHero failed on a drop whose slot transitioned to cooldown")
	}

	if got := len(tl.slotTaps()); got != 1 {
		t.Fatalf("placement used %d card taps, want exactly 1", got)
	}
	if got := len(tl.fieldTaps()); got != 1 {
		t.Fatalf("placement used %d field taps, want exactly 1", got)
	}

	// Ability is card tap #2.
	if !hm.executor.TapHeroAbility(slot) {
		t.Fatal("the ability tap (card tap #2) must be allowed")
	}
	// The slot manager marks a hero deployed on confirmation; this harness has
	// no slot manager, so stand in for it — the second-placement refusal below
	// is gated on exactly that state.
	slot.State = SlotDeployed

	// Anything beyond two is refused, and fires nothing: the ability is a
	// one-shot, and a hero the slot manager already confirmed deployed has no
	// placement tap left either.
	if hm.executor.TapHeroAbility(slot) {
		t.Error("a repeated ability tap must be refused")
	}
	if hm.executor.TapSlot(slot, 8) {
		t.Error("a second placement tap on an already-deployed hero must be refused")
	}
	if got := len(tl.slotTaps()); got != 2 {
		t.Fatalf("total hero card taps = %d, want 2 (place + ability)", got)
	}
}

// TestHeroDeploy_FieldTapUsesDeployLine pins WHERE the drop lands: the
// battle's validated deploy line, not a pinned/exact formula point (live
// 2026-09-26: the hero formula pins sat inside the red line and the game
// refused every tap aimed at them, so no hero placed on tap #1).
func TestHeroDeploy_FieldTapUsesDeployLine(t *testing.T) {
	line := DeployLine{Points: []image.Point{{X: 200, Y: 300}, {X: 350, Y: 380}, {X: 500, Y: 450}}}
	slot := heroSlotNamed("barbarian king", 287)
	hm, tl := newHeroTapHarness(t, line, []*TrackedSlot{slot})
	hm.ratioHook = scriptedRatios(0.74, 0.11)

	d := HeroDeployment{Unit: strategy.Unit{Name: "Barbarian King"}, Slot: slot}
	pre := gocv.NewMat()
	defer pre.Close()
	if !hm.deploySingleHero(d, pre) {
		t.Fatal("deploy failed")
	}

	pts := tl.fieldTaps()
	if len(pts) != 1 {
		t.Fatalf("got %d field taps, want 1", len(pts))
	}
	if !nearDeployLine(pts[0], line) {
		t.Errorf("hero field tap %v is not on the deploy line %+v — the drop must aim at validated ground", pts[0], line.Points)
	}
}

// TestHeroDeploy_NoRetryTapWhenVerifyFails: an unconfirmed drop must not become
// a second PLACEMENT tap — and when there is NO new ground to re-aim at, no
// re-arm either: a card re-tap that cannot be followed by a field tap is exactly
// the stray "random tap" this bot must never fire. The placement budget and the
// ability's tap stay intact for the sweep/ability pass.
func TestHeroDeploy_NoRetryTapWhenVerifyFails(t *testing.T) {
	line := DeployLine{Points: []image.Point{{X: 200, Y: 300}, {X: 500, Y: 450}}}
	slot := heroSlotNamed("grand warden", 650)
	hm, tl := newHeroTapHarness(t, line, []*TrackedSlot{slot})
	hm.ratioHook = scriptedRatios(0.74, 0.74) // delta 0 → unconfirmed

	d := HeroDeployment{Unit: strategy.Unit{Name: "Grand Warden"}, Slot: slot}
	pre := gocv.NewMat()
	defer pre.Close()
	if hm.deploySingleHero(d, pre) {
		t.Fatal("deploySingleHero must not claim success on an unconfirmed drop")
	}

	if got := len(tl.slotTaps()); got != 1 {
		t.Errorf("an unconfirmed drop with no new ground must fire exactly 1 card tap (the placement), got %d", got)
	}
	if slot.SpotTaps != 1 {
		t.Errorf("placement budget spent = %d, want 1", slot.SpotTaps)
	}
	if slot.SpotRearms != 0 {
		t.Errorf("re-arms = %d, want 0: a re-arm that leads to no field tap is a stray tap", slot.SpotRearms)
	}
	if got := len(tl.fieldTaps()); got != 1 {
		t.Errorf("an unconfirmed drop must fire exactly 1 field tap, got %d", got)
	}
	if !hm.executor.HeroSpotTapBudgetLeft(slot) {
		t.Error("one card tap must remain so the sweep/verifier can recover the hero, and for its ability")
	}
}

// TestHeroSpotTapCeiling_SharedAcrossPaths proves both caps are per-slot and
// independent: placement stops at two (the drop plus one rescue) and the
// ability fires exactly once, whichever path asks first.
//
// This is the fix for the live defect the user reported as "it doesn't activate
// heroes properly": when the sweep rescued a hero it spent both placement taps,
// so the deferred ability pass found no budget and skipped the activation. The
// caps being separate is what lets a rescued hero still fire its ability.
func TestHeroSpotTapCeiling_SharedAcrossPaths(t *testing.T) {
	slot := heroSlotNamed("dragon duke", 400)
	hm, tl := newHeroTapHarness(t, DeployLine{}, []*TrackedSlot{slot})

	if !hm.executor.TapSlot(slot, 4) {
		t.Fatal("placement tap #1 must be allowed")
	}
	if !hm.executor.TapSlot(slot, 4) {
		t.Fatal("placement tap #2 (the one rescue) must be allowed while the hero is not confirmed deployed")
	}
	if hm.executor.TapSlot(slot, 4) {
		t.Fatal("a third placement tap must be refused")
	}
	if !hm.executor.TapHeroAbility(slot) {
		t.Fatal("the ability must still fire after two placement taps; it has its own one-shot flag")
	}
	if hm.executor.TapHeroAbility(slot) {
		t.Fatal("a second ability tap must be refused")
	}
	if got := len(tl.slotTaps()); got != 3 {
		t.Fatalf("recorded card taps = %d, want 3 (two placements + one ability)", got)
	}
}

// ---------------------------------------------------------------------------
// attacklog verdicts for the hero lines
// ---------------------------------------------------------------------------

func heroVerdictFile(t *testing.T, messages []string) string {
	t.Helper()
	var b strings.Builder
	for _, m := range messages {
		b.WriteString("18:56:26 | INF | " + m + " component=hero_manager delta=0.56 unit=\"Barbarian King\"\n")
	}
	b.WriteString("18:56:27 | INF | all units successfully deployed component=verifier\n")
	dir := t.TempDir()
	path := filepath.Join(dir, "battle.log")
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestAttackLog_HeroDeployedVerdict: the single-tap success line must confirm
// the hero as deployed.
func TestAttackLog_HeroDeployedVerdict(t *testing.T) {
	path := heroVerdictFile(t, []string{
		"hero deployed (single card tap + single field tap)",
	})
	rep, err := attacklog.Parse(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Units) != 1 {
		t.Fatalf("parsed %d units, want 1", len(rep.Units))
	}
	u := rep.Units[0]
	if u.Verdict != attacklog.ConfirmedDeploy {
		t.Errorf("verdict = %s, want deployed; reason=%s", u.Verdict, u.Reason)
	}
}

// TestAttackLog_SweepHeroSuccessVerdict: the sweep's proven hero success line
// must confirm the hero (run17's Warden deployed via the sweep yet the report
// still called it UNVERIFIED because attacklog never matched the line).
func TestAttackLog_SweepHeroSuccessVerdict(t *testing.T) {
	path := heroVerdictFile(t, []string{"hero sweep deploy succeeded"})
	rep, err := attacklog.Parse(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Units) != 1 {
		t.Fatalf("parsed %d units, want 1", len(rep.Units))
	}
	u := rep.Units[0]
	if u.Verdict != attacklog.ConfirmedDeploy {
		t.Errorf("verdict = %s, want deployed; reason=%s", u.Verdict, u.Reason)
	}
}

// TestAttackLog_HeroRetryFailureStaysUnverified: a hero whose retry ALSO
// failed must not read as deployed.
func TestAttackLog_HeroRetryFailureStaysUnverified(t *testing.T) {
	path := heroVerdictFile(t, []string{
		"hero drop not confirmed by the slot transition; leaving it for the sweep's one remaining tap (no re-tap)",
		"hero sweep retry did not visibly transition slot; will be marked failed",
	})
	rep, err := attacklog.Parse(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Units) != 1 {
		t.Fatalf("parsed %d units, want 1", len(rep.Units))
	}
	u := rep.Units[0]
	if u.Verdict == attacklog.ConfirmedDeploy {
		t.Errorf("verdict = %s, want not-deployed; reason=%s", u.Verdict, u.Reason)
	}
}

// TestRearmDoesNotStealTheAbility is the outcome the user actually asked for,
// end to end: the pinned point is refused, the bot re-arms the card and places
// the hero on the next ground, and the hero's ability still fires on its second
// card tap. Before the re-arm existed, the refused drop left the card
// deselected, every retry tap hit nothing, and the sweep — which arms the card
// itself — placed the hero seconds AFTER the ability pass had already skipped it.
func TestRearmDoesNotStealTheAbility(t *testing.T) {
	line := []image.Point{
		{X: 935, Y: 300}, {X: 935, Y: 450}, {X: 935, Y: 600},
		{X: 1088, Y: 300}, {X: 1088, Y: 450}, {X: 1088, Y: 600},
	}
	slot := heroSlotNamed("dragon duke", 550)
	hm, tl := newHeroTapHarness(t, DeployLine{Points: line}, []*TrackedSlot{slot})
	hm.SetRedZone(rectZone(400, 100, 1010, 650))
	// The drop is refused (0.74 -> 0.74), the retry lands (0.74 -> 0.11).
	hm.ratioHook = scriptedRatios(0.74, 0.74, 0.11)

	d := HeroDeployment{Unit: strategy.Unit{Name: "Dragon Duke"}, Slot: slot}
	pre := gocv.NewMat()
	defer pre.Close()
	if !hm.deploySingleHero(d, pre) {
		t.Fatal("deploySingleHero must confirm the placement the re-armed retry achieved")
	}
	if slot.SpotRearms != 1 {
		t.Fatalf("re-arms = %d, want 1", slot.SpotRearms)
	}
	// Checked BEFORE the ability tap: the re-arm must not have counted as a
	// placement, so only the placement is on the shared counter here.
	if slot.SpotTaps != 1 {
		t.Fatalf("placement budget spent = %d, want 1 (the re-arm must not be counted as placement)", slot.SpotTaps)
	}
	if !hm.executor.TapHeroAbility(slot) {
		t.Fatal("the ability tap must still be available after a re-arm: the re-arm replaces a tap the game never registered as a placement")
	}
	// Three physical card taps, and that is the documented ceiling: the placement,
	// the single re-arm that a refusal makes necessary, and the ability. What is
	// preserved is the CONTRACT the 2-tap rule protects — exactly one placement and
	// exactly one ability, never a second placement — which is why the re-arm is
	// accounted separately from the placement budget.
	if got := len(tl.slotTaps()); got != 2+maxHeroRearms {
		t.Fatalf("total card taps = %d, want %d (placement + re-arm + ability)", got, 2+maxHeroRearms)
	}
	// After the ability: the placement counter still holds exactly the one
	// placement (the re-arm and the ability are accounted elsewhere), and the
	// ability's own one-shot flag is set — the contract the 2-tap rule protects
	// is "exactly one placement and exactly one ability".
	if slot.SpotTaps != 1 {
		t.Fatalf("placement taps = %d, want 1", slot.SpotTaps)
	}
	if !slot.AbilityFired {
		t.Fatal("AbilityFired must be set after the ability tap")
	}
	if hm.executor.TapHeroAbility(slot) {
		t.Fatal("a second ability tap must be refused: one activation per hero per battle")
	}
}

// TestConfirmHeroWave_EarlyExitWhenAllPlaced pins the deploy-window saving: a
// wave whose drops register on the first beat must not pay for the follow-up
// beat (heroConfirmFollowMs plus a capture) re-reading cards that already
// transitioned — the 400ms lives inside the seven-second deploy window.
func TestConfirmHeroWave_EarlyExitWhenAllPlaced(t *testing.T) {
	t.Setenv("CLASHGO_HERO_DEBUG", "") // production beat count; the exit must beat it
	line := DeployLine{Points: []image.Point{{X: 200, Y: 300}, {X: 500, Y: 450}}}
	slot := heroSlotNamed("archer queen", 287)
	hm, _ := newHeroTapHarness(t, line, []*TrackedSlot{slot})
	reads := 0
	hm.ratioHook = func() (float64, bool) {
		reads++
		return 0.11, true // consumed at once: the wave's placement registers on beat 0
	}
	d := HeroDeployment{Unit: strategy.Unit{Name: "Archer Queen"}, Slot: slot}
	pre := gocv.NewMat()
	defer pre.Close()
	if !hm.deploySingleHero(d, pre) {
		t.Fatal("a hero whose card reads consumed on the first beat must confirm")
	}
	if reads != 1 {
		t.Fatalf("confirm reads = %d, want 1 (the wave must exit once every fired hero is placed)", reads)
	}
}

// TestConfirmHeroWave_DebugEnvAddsLateBeat pins the diagnostic seam: without
// CLASHGO_HERO_DEBUG the confirmation wave keeps its normal two beats
// (production timing untouched); with it, a third, late beat reads every
// pending card so a live run can show when a refused drop registers on its own
// versus never (batch-drop diagnosis, 2026-10-02).
func TestConfirmHeroWave_DebugEnvAddsLateBeat(t *testing.T) {
	countReads := func(t *testing.T, dbg string) int {
		t.Helper()
		t.Setenv("CLASHGO_HERO_DEBUG", dbg)
		line := DeployLine{Points: []image.Point{{X: 200, Y: 300}, {X: 500, Y: 450}}}
		slot := heroSlotNamed("grand warden", 650)
		hm, _ := newHeroTapHarness(t, line, []*TrackedSlot{slot})
		reads := 0
		hm.ratioHook = func() (float64, bool) {
			reads++
			return 0.74, true // never transitions: every beat reads the card still full
		}
		d := HeroDeployment{Unit: strategy.Unit{Name: "Grand Warden"}, Slot: slot}
		pre := gocv.NewMat()
		defer pre.Close()
		hm.deploySingleHero(d, pre) // unconfirmed; no new ground, so no rescue reads
		return reads
	}

	if got := countReads(t, ""); got != 2 {
		t.Errorf("confirm reads without CLASHGO_HERO_DEBUG = %d, want 2 (production beat count)", got)
	}
	if got := countReads(t, t.TempDir()); got != 3 {
		t.Errorf("confirm reads with CLASHGO_HERO_DEBUG = %d, want 3 (the late diagnostic beat)", got)
	}
}
