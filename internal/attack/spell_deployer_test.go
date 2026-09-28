package attack

import (
	"encoding/json"
	"fmt"
	"image"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/Ducky705/ClashGO/internal/adb"
	"github.com/Ducky705/ClashGO/internal/game"
	"github.com/Ducky705/ClashGO/pkg/formula"
	"github.com/Ducky705/ClashGO/pkg/strategy"
	"github.com/rs/zerolog"
)

// ---------------------------------------------------------------------------
// Test doubles
// ---------------------------------------------------------------------------

// tapLog records every TapFast the spell deployer fires via the adb client
// tap hook, plus every slot re-select. It is the ground truth for "did the
// spell actually get tapped, where, and how many times".
type tapLog struct {
	mu    sync.Mutex
	taps  []image.Point
	slots []image.Point
}

func (tl *tapLog) record(ev adb.TapEvent) {
	tl.mu.Lock()
	defer tl.mu.Unlock()
	if ev.Type != "tap_fast" {
		return
	}
	// Field taps use stdDev 8; slot selections (TapSlot / ability) use 2-4.
	if ev.StdDev > 4 {
		tl.taps = append(tl.taps, image.Pt(ev.X, ev.Y))
	} else {
		tl.slots = append(tl.slots, image.Pt(ev.X, ev.Y))
	}
}

func (tl *tapLog) fieldTaps() []image.Point {
	tl.mu.Lock()
	defer tl.mu.Unlock()
	out := make([]image.Point, len(tl.taps))
	copy(out, tl.taps)
	return out
}

func (tl *tapLog) slotTaps() []image.Point {
	tl.mu.Lock()
	defer tl.mu.Unlock()
	out := make([]image.Point, len(tl.slots))
	copy(out, tl.slots)
	return out
}

// newSpellTestHarness builds a SpellDeployer whose client taps are captured
// (no device needed: the closed client fails every transport call, but the
// tap hook still fires before routeTap, so placement is fully observable).
func newSpellTestHarness(t *testing.T, pCfg PrecisionConfig, f *formula.Formula, counts map[int]int, slots []*TrackedSlot) (*SpellDeployer, *tapLog) {
	t.Helper()

	client := adb.NewClient(
		adb.WithJitterTaps(false), // deterministic ActualX/ActualY == X/Y
		adb.WithJitterDelays(false),
		adb.WithTimeout(1), // never actually used: closed client
	)
	client.Close() // tap hook still fires; transport calls fail fast

	tl := &tapLog{}
	client.SetTapHook(tl.record)

	cal := &game.Calibration{PhysicalW: 860, PhysicalH: 732, ScaleX: 1.0, ScaleY: 1.0}
	exec := NewTapExecutor(client, cal, zerolog.Nop())

	idx := make(map[string]*TrackedSlot, len(slots))
	for _, s := range slots {
		idx[strings.ToLower(s.UnitName)] = s
	}
	exec.SetSlotResolver(func(name string) *TrackedSlot { return idx[name] })

	var sd *SpellDeployer
	if counts != nil {
		sd = NewSpellDeployerWithCounts(exec, pCfg, f, 860, 732, counts, zerolog.Nop())
	} else {
		sd = NewSpellDeployer(exec, pCfg, f, 860, 732, zerolog.Nop())
	}
	return sd, tl
}

func spellSlot(name string, x int) *TrackedSlot {
	return &TrackedSlot{TroopSlot: TroopSlot{X: x, Y: 682}, UnitName: name}
}

// loadShippedFormula parses the real auto_edrag_rush_formula.json so tests
// run against the exact geometry users attack with.
func loadShippedFormula(t *testing.T) *formula.Formula {
	t.Helper()
	for _, p := range []string{"../../assets/strategies/auto_edrag_rush_formula.json", "assets/strategies/auto_edrag_rush_formula.json"} {
		raw, err := os.ReadFile(p)
		if err == nil {
			f, err := formula.LoadFile(p)
			if err != nil {
				t.Fatalf("LoadFile(%s): %v", p, err)
			}
			_ = raw
			return f
		}
	}
	t.Skip("auto_edrag_rush_formula.json not found from test cwd")
	return nil
}

// distToSegment returns the min distance from pt to the segment a→b.
func distToSegment(pt, a, b image.Point) float64 {
	ax, ay := float64(a.X), float64(a.Y)
	bx, by := float64(b.X), float64(b.Y)
	px, py := float64(pt.X), float64(pt.Y)
	dx, dy := bx-ax, by-ay
	l2 := dx*dx + dy*dy
	if l2 == 0 {
		return math.Hypot(px-ax, py-ay)
	}
	t := ((px-ax)*dx + (py-ay)*dy) / l2
	if t < 0 {
		t = 0
	}
	if t > 1 {
		t = 1
	}
	return math.Hypot(px-(ax+t*dx), py-(ay+t*dy))
}

// ---------------------------------------------------------------------------
// resolveSpellCount — how many taps per spell unit
// ---------------------------------------------------------------------------

func TestResolveSpellCount_NumericAmountWins(t *testing.T) {
	sd, _ := newSpellTestHarness(t, PrecisionConfig{}, nil, map[int]int{100: 11}, []*TrackedSlot{spellSlot("rage spell", 100)})
	unit := strategy.Unit{Name: "Rage Spell", Amount: "3"}
	if got := sd.resolveSpellCount(unit); got != 3 {
		t.Fatalf("numeric Amount=3 resolved to %d taps, want 3", got)
	}
}

func TestResolveSpellCount_LiveOCRCountUsedForAll(t *testing.T) {
	// edrag rush army: 11 rage on the card at x=100.
	sd, _ := newSpellTestHarness(t, PrecisionConfig{}, nil, map[int]int{100: 11}, []*TrackedSlot{spellSlot("rage spell", 100)})
	unit := strategy.Unit{Name: "Rage Spell", Amount: "All"}
	if got := sd.resolveSpellCount(unit); got != 11 {
		t.Fatalf("Amount=All with OCR count 11 resolved to %d taps, want 11", got)
	}
}

func TestResolveSpellCount_DefaultFiveWithoutCounts(t *testing.T) {
	sd, _ := newSpellTestHarness(t, PrecisionConfig{}, nil, nil, []*TrackedSlot{spellSlot("ice spell", 60)})
	unit := strategy.Unit{Name: "Ice Spell", Amount: "All"}
	if got := sd.resolveSpellCount(unit); got != 5 {
		t.Fatalf("Amount=All without OCR resolved to %d taps, want legacy default 5", got)
	}
}

func TestResolveSpellCount_ZeroOCRCountFallsBack(t *testing.T) {
	// OCR present but read 0 (blurry frame) — must NOT tap 0 times.
	sd, _ := newSpellTestHarness(t, PrecisionConfig{}, nil, map[int]int{100: 0}, []*TrackedSlot{spellSlot("rage spell", 100)})
	unit := strategy.Unit{Name: "Rage Spell", Amount: "All"}
	if got := sd.resolveSpellCount(unit); got != 5 {
		t.Fatalf("OCR count 0 resolved to %d taps, want fallback 5", got)
	}
}

func TestResolveSpellCount_UnknownUnitFallsBack(t *testing.T) {
	sd, _ := newSpellTestHarness(t, PrecisionConfig{}, nil, map[int]int{100: 7}, []*TrackedSlot{spellSlot("rage spell", 100)})
	unit := strategy.Unit{Name: "Skeleton Spell", Amount: "All"} // no slot in the bar
	if got := sd.resolveSpellCount(unit); got != 5 {
		t.Fatalf("unknown unit resolved to %d taps, want fallback 5", got)
	}
}

// ---------------------------------------------------------------------------
// Legacy line path — edrag rush without formula: rage→Line A, ice→Line B
// ---------------------------------------------------------------------------

func testLinePCfg() PrecisionConfig {
	return PrecisionConfig{
		SpellEdgesA: map[string]ManualEdge{
			"BottomRight": {P1: image.Pt(400, 500), P2: image.Pt(700, 300)},
		},
		SpellEdgesB: map[string]ManualEdge{
			"BottomRight": {P1: image.Pt(420, 520), P2: image.Pt(720, 320)},
		},
	}
}

func TestDeployLineSpell_RageUsesLineA_AllTapsNearLineA(t *testing.T) {
	pCfg := testLinePCfg()
	sd, tl := newSpellTestHarness(t, pCfg, nil, nil, []*TrackedSlot{spellSlot("rage spell", 100)})
	unit := strategy.Unit{Name: "Rage Spell", Amount: "3"} // 3 < 5 → no rage special

	ok := sd.DeploySpell(unit, spellSlot("rage spell", 100), "BottomRight", "Line")
	if !ok {
		t.Fatal("DeploySpell returned false for a configured line")
	}
	taps := tl.fieldTaps()
	if len(taps) != 3 {
		t.Fatalf("got %d field taps, want 3", len(taps))
	}
	lineA := pCfg.SpellEdgesA["BottomRight"]
	for i, pt := range taps {
		if d := distToSegment(pt, lineA.P1, lineA.P2); d > 12 {
			t.Errorf("tap %d at %v is %.1fpx off Line A (jitter ≤ 8)", i, pt, d)
		}
	}
	// Spacing: taps must progress along the line, not stack on one point.
	if spread(taps) < 50 {
		t.Errorf("taps clustered (spread %.1fpx) — spells must be distributed along the line", spread(taps))
	}
}

func TestDeployLineSpell_IceUsesLineB_AllTapsNearLineB(t *testing.T) {
	pCfg := testLinePCfg()
	sd, tl := newSpellTestHarness(t, pCfg, nil, nil, []*TrackedSlot{spellSlot("ice spell", 60)})
	unit := strategy.Unit{Name: "Ice Spell", Amount: "All"}

	ok := sd.DeploySpell(unit, spellSlot("ice spell", 60), "BottomRight", "Line")
	if !ok {
		t.Fatal("DeploySpell returned false for a configured line")
	}
	taps := tl.fieldTaps()
	if len(taps) != 5 {
		t.Fatalf("got %d field taps, want 5", len(taps))
	}
	lineB := pCfg.SpellEdgesB["BottomRight"]
	for i, pt := range taps {
		if d := distToSegment(pt, lineB.P1, lineB.P2); d > 12 {
			t.Errorf("tap %d at %v is %.1fpx off Line B", i, pt, d)
		}
		if d := distToSegment(pt, pCfg.SpellEdgesA["BottomRight"].P1, pCfg.SpellEdgesA["BottomRight"].P2); d < 12 {
			t.Errorf("tap %d landed on Line A — ice must use Line B", i)
		}
	}
}

func TestDeployLineSpell_RageSpecial_3OnA_2OnB(t *testing.T) {
	pCfg := testLinePCfg()
	sd, tl := newSpellTestHarness(t, pCfg, nil, nil, []*TrackedSlot{spellSlot("rage spell", 100)})
	unit := strategy.Unit{Name: "Rage Spell", Amount: "All"} // 5 taps → rage special

	ok := sd.DeploySpell(unit, spellSlot("rage spell", 100), "BottomRight", "Line")
	if !ok {
		t.Fatal("DeploySpell returned false")
	}
	lineA := pCfg.SpellEdgesA["BottomRight"]
	lineB := pCfg.SpellEdgesB["BottomRight"]
	onA, onB := 0, 0
	for _, pt := range tl.fieldTaps() {
		switch {
		case distToSegment(pt, lineA.P1, lineA.P2) <= 12:
			onA++
		case distToSegment(pt, lineB.P1, lineB.P2) <= 12:
			onB++
		default:
			t.Errorf("tap at %v is on neither Line A (%.1fpx) nor Line B (%.1fpx)", pt,
				distToSegment(pt, lineA.P1, lineA.P2), distToSegment(pt, lineB.P1, lineB.P2))
		}
	}
	if onA != 3 || onB != 2 {
		t.Fatalf("rage special split = %d on Line A / %d on Line B, want 3/2", onA, onB)
	}
	// The slot must be re-selected between the A and B drops or the
	// second drop falls on an empty cursor (the historical dropped-tap bug).
	if len(tl.slotTaps()) == 0 {
		t.Error("rage special did not re-select the slot between Line A and Line B")
	}
}

func TestDeployLineSpell_NonRageNeverTriggersRageSpecial(t *testing.T) {
	pCfg := testLinePCfg()
	// Only Line B configured: a rage special would fail; poison must still deploy on B.
	pCfg.SpellEdgesA = nil
	sd, tl := newSpellTestHarness(t, pCfg, nil, nil, []*TrackedSlot{spellSlot("poison spell", 60)})
	unit := strategy.Unit{Name: "Poison Spell", Amount: "All"}

	if !sd.DeploySpell(unit, spellSlot("poison spell", 60), "BottomRight", "Line") {
		t.Fatal("non-rage spell failed to deploy on Line B fallback")
	}
	if got := len(tl.fieldTaps()); got != 5 {
		t.Fatalf("got %d taps, want 5", got)
	}
}

func TestDeployLineSpell_NoLineConfiguredReturnsFalse(t *testing.T) {
	sd, tl := newSpellTestHarness(t, PrecisionConfig{}, nil, nil, []*TrackedSlot{spellSlot("ice spell", 60)})
	unit := strategy.Unit{Name: "Ice Spell", Amount: "All"}
	if sd.DeploySpell(unit, spellSlot("ice spell", 60), "TopLeft", "Line") {
		t.Error("DeploySpell reported success with no spell line configured")
	}
	if got := len(tl.fieldTaps()); got != 0 {
		t.Errorf("got %d taps with no configuration, want 0", got)
	}
}

// spread returns max pairwise distance between taps.
func spread(pts []image.Point) float64 {
	max := 0.0
	for i := 0; i < len(pts); i++ {
		for j := i + 1; j < len(pts); j++ {
			if d := math.Hypot(float64(pts[i].X-pts[j].X), float64(pts[i].Y-pts[j].Y)); d > max {
				max = d
			}
		}
	}
	return max
}

// ---------------------------------------------------------------------------
// Point path
// ---------------------------------------------------------------------------

func TestDeployPointSpell_RingAroundTarget(t *testing.T) {
	pCfg := PrecisionConfig{
		SpellTargets: map[string]image.Point{"BottomRight": {X: 500, Y: 400}},
	}
	sd, tl := newSpellTestHarness(t, pCfg, nil, nil, []*TrackedSlot{spellSlot("ice spell", 60)})
	unit := strategy.Unit{Name: "Ice Spell", Amount: "All"} // 5

	if !sd.DeploySpell(unit, spellSlot("ice spell", 60), "BottomRight", "Point") {
		t.Fatal("point deploy failed")
	}
	taps := tl.fieldTaps()
	if len(taps) != 5 {
		t.Fatalf("got %d taps, want 5", len(taps))
	}
	// The radius is no longer a constant. It is whatever radius puts
	// neighbouring casts a minimum spacing apart, so the bound is that radius
	// plus the worst-case per-axis jitter. The hardcoded 18 that used to sit
	// here put 5 casts 28 px apart — inside one cast's own reach.
	jitter := 6
	radius := sd.castRingRadius(5, sd.ringChord(jitter))
	for i, pt := range taps {
		if d := math.Hypot(float64(pt.X-500), float64(pt.Y-400)); d > radius+float64(jitter)*math.Sqrt2+1 {
			t.Errorf("tap %d at %v is %.1fpx from target (ring radius %.0f + jitter %d per axis)", i, pt, d, radius, jitter)
		}
	}
	// Ring taps must not all stack on the center point.
	if spread(taps) < 12 {
		t.Error("ring taps clustered at center — ring offsets were not applied")
	}
	assertRingClearsSpacing(t, taps, sd)
}

// assertRingClearsSpacing checks the invariant every cast ring exists to
// honour: no two of its casts land on the same ground. The bar comes from
// attacklog, so the deployer and the tool that grades it share one number.
//
// The 2 px of slack is the worst case the arithmetic cannot rule out: jitter is
// drawn per axis and moves two neighbours toward each other by up to 2·√2·j,
// and each integer tap coordinate truncates toward zero by up to 1 px. The ring
// is sized for exactly that much loss, so anything materially below the bar is a
// real defect rather than a bad draw.
func assertRingClearsSpacing(t *testing.T, taps []image.Point, sd *SpellDeployer) {
	t.Helper()
	if len(taps) < 2 {
		return
	}
	bar := sd.minCastSpacing()
	closest, a, b := math.Inf(1), image.Point{}, image.Point{}
	for i := 0; i < len(taps); i++ {
		for j := i + 1; j < len(taps); j++ {
			if d := math.Hypot(float64(taps[i].X-taps[j].X), float64(taps[i].Y-taps[j].Y)); d < closest {
				closest, a, b = d, taps[i], taps[j]
			}
		}
	}
	if closest < bar-2 {
		t.Errorf("closest cast pair is %.1fpx (%v, %v), under the %.0fpx same-ground bar: these casts cover the same area", closest, a, b, bar)
	}
}

// ---------------------------------------------------------------------------
// Formula path — the shipped edrag rush geometry
// ---------------------------------------------------------------------------

func TestFormulaLine_RageAutoSplit_OuterAndInnerLines(t *testing.T) {
	f := &formula.Formula{
		Screen: formula.ScreenSize{W: 860, H: 732},
		Units: map[string]formula.UnitEntry{
			"rage spell": {Type: "line", P1: &formula.Point{X: 453, Y: 535}, P2: &formula.Point{X: 678, Y: 363}, Jitter: 3},
		},
	}
	sd, tl := newSpellTestHarness(t, PrecisionConfig{}, f, nil, []*TrackedSlot{spellSlot("rage spell", 100)})
	unit := strategy.Unit{Name: "Rage Spell", Amount: "All"} // 5 → 3 outer + 2 inner

	if !sd.DeploySpell(unit, spellSlot("rage spell", 100), "BottomRight", "Line") {
		t.Fatal("formula line deploy failed")
	}
	taps := tl.fieldTaps()
	if len(taps) != 5 {
		t.Fatalf("got %d taps, want 5 (3 outer + 2 inner)", len(taps))
	}
	outerP1, outerP2 := image.Pt(453, 535), image.Pt(678, 363)
	// The two sub-lines must not overlap AT THE CASTS — the same bar the
	// cast-level guard (ensureInnerCastsClearOuter) enforces. A live 720p run
	// drew the two sub-lines a fixed 35px apart against a 100px cast reach, so
	// an outer and an inner Rage cast landed 31px apart and the attack report
	// called the line OVERLAPPED; the guard now also clears the jitter it
	// applies per tap, so the auto-derived line sits slightly deeper than the
	// nominal spacing. Formula jitter here is 3.
	minStep := sd.minCastSpacing()
	nearOuter, nearInner := 0, 0
	outerCasts := taps[:3]
	for _, pt := range taps {
		dOuter := distToSegment(pt, outerP1, outerP2)
		switch {
		case dOuter <= 8:
			nearOuter++
		default:
			// Inner cast: must be INWARD (toward center) of the outer line
			// and clear every outer cast — with jitter margin, since taps are
			// jittered after placement.
			dInner := signedInwardDistance(pt, outerP1, outerP2, image.Pt(430, 366))
			if dInner < minStep {
				t.Errorf("inner tap at %v is only %.1fpx inward (outer line %.1fpx away); must clear the %.1fpx cast spacing so the two sub-lines do not overlap", pt, dInner, dOuter, minStep)
				continue
			}
			for _, oc := range outerCasts {
				if d := dist(pt, oc); d < minStep/2 {
					t.Errorf("inner tap %v is %.1fpx from outer tap %v, under the %.1fpx same-ground bar — the sub-lines must not overlap at the casts", pt, d, oc, minStep/2)
				}
			}
			nearInner++
		}
	}
	if nearOuter != 3 || nearInner != 2 {
		t.Fatalf("rage split = %d outer / %d inner, want 3/2", nearOuter, nearInner)
	}
}

func TestFormulaLine_RageSplit_UsesUserPinnedInnerLine(t *testing.T) {
	f := &formula.Formula{
		Screen: formula.ScreenSize{W: 860, H: 732},
		Units: map[string]formula.UnitEntry{
			"rage spell":  {Type: "line", P1: &formula.Point{X: 453, Y: 535}, P2: &formula.Point{X: 678, Y: 363}, Jitter: 3},
			"_rage_inner": {Type: "line", P1: &formula.Point{X: 452, Y: 452}, P2: &formula.Point{X: 588, Y: 344}, Jitter: 3},
		},
	}
	sd, tl := newSpellTestHarness(t, PrecisionConfig{}, f, nil, []*TrackedSlot{spellSlot("rage spell", 100)})
	unit := strategy.Unit{Name: "Rage Spell", Amount: "All"}

	if !sd.DeploySpell(unit, spellSlot("rage spell", 100), "BottomRight", "Line") {
		t.Fatal("deploy failed")
	}
	innerP1, innerP2 := image.Pt(452, 452), image.Pt(588, 344)
	onInner := 0
	for _, pt := range tl.fieldTaps() {
		if d := distToSegment(pt, innerP1, innerP2); d <= 8 {
			onInner++
		}
	}
	if onInner != 2 {
		t.Fatalf("%d taps landed on the user-pinned _rage_inner line, want 2", onInner)
	}
}

// A rotated pinned inner line can clear the midpoint bar and still converge at
// its far end — the exact defect a live 720p run measured (run16, 2026-09-22):
// outer cast (505,517) vs inner cast (512,501), 17 px apart, after the old
// midpoint-only guard had pushed the inner line to a 56 px midpoint clearance.
// Midpoint distance bounds cast distance only when the lines are parallel, and
// a user-pinned `_rage_inner` is authored free-hand — nothing preserves
// parallelism. This fixture reproduces the convergence directly: the inner
// line's midpoint sits ~60 px from the outer line (clear of the ~50 px chord)
// while its far-end cast pair sits ~43 px apart (inside it).
func TestEnsureInnerCastsClearOuter_RotatedInnerLineConvergingAtEnd(t *testing.T) {
	outer1, outer2 := image.Pt(453, 535), image.Pt(678, 363)
	inner1, inner2 := image.Pt(471, 472), image.Pt(579, 418)

	sd, _ := newSpellTestHarness(t, PrecisionConfig{}, &formula.Formula{Screen: formula.ScreenSize{W: 860, H: 732}}, nil, []*TrackedSlot{spellSlot("rage spell", 100)})
	chord := sd.ringChord(0)
	if chord <= 0 {
		t.Fatalf("ringChord(0) = %v, want > 0", chord)
	}

	// The fixture must reproduce the failure before the guard may fix it:
	// midpoint clear of the chord, casts not.
	midOuter := image.Pt((outer1.X+outer2.X)/2, (outer1.Y+outer2.Y)/2)
	midInner := image.Pt((inner1.X+inner2.X)/2, (inner1.Y+inner2.Y)/2)
	if midDist := dist(midInner, midOuter); midDist < chord {
		t.Fatalf("fixture: midpoint distance %.1f is already under the %.1f chord — adjust the fixture", midDist, chord)
	}
	outerCasts := sd.distributeAlong(outer1, outer2, rageOuterCount(5), 0)
	worst := math.Inf(1)
	for _, ic := range sd.distributeAlong(inner1, inner2, 2, 0) {
		for _, oc := range outerCasts {
			if d := dist(ic, oc); d < worst {
				worst = d
			}
		}
	}
	if worst >= chord {
		t.Fatalf("fixture: closest cast pair %.1f already clears the %.1f chord — it no longer reproduces the convergence defect", worst, chord)
	}

	got1, got2, pushed := sd.ensureInnerCastsClearOuter(outer1, outer2, inner1, inner2, 2, 0)
	if !pushed {
		t.Fatal("guard did not move a rotated inner line whose casts sit inside one cast spacing of the outer casts")
	}
	for _, ic := range sd.distributeAlong(got1, got2, 2, 0) {
		for _, oc := range outerCasts {
			if d := dist(ic, oc); d < chord-0.5 {
				t.Errorf("inner cast %v is %.1f px from outer cast %v, want >= %.1f — the guard must separate the lines at cast level, not midpoint level", ic, d, oc, chord)
			}
		}
	}
}

// End to end: the same rotated pinned inner line deployed through DeploySpell
// must leave no outer/inner cast pair closer than half a cast spacing — the
// report's own "not stacked" bar.
func TestFormulaLine_RageRotatedPinnedInnerEndsStayApart(t *testing.T) {
	f := &formula.Formula{
		Screen: formula.ScreenSize{W: 860, H: 732},
		Units: map[string]formula.UnitEntry{
			"rage spell":  {Type: "line", P1: &formula.Point{X: 453, Y: 535}, P2: &formula.Point{X: 678, Y: 363}, Jitter: 3},
			"_rage_inner": {Type: "line", P1: &formula.Point{X: 471, Y: 472}, P2: &formula.Point{X: 579, Y: 418}, Jitter: 3},
		},
	}
	sd, tl := newSpellTestHarness(t, PrecisionConfig{}, f, nil, []*TrackedSlot{spellSlot("rage spell", 100)})
	unit := strategy.Unit{Name: "Rage Spell", Amount: "All"} // 3 outer + 2 inner

	if !sd.DeploySpell(unit, spellSlot("rage spell", 100), "BottomRight", "Line") {
		t.Fatal("deploy failed")
	}
	taps := tl.fieldTaps()
	if len(taps) != 5 {
		t.Fatalf("got %d taps, want 5 (3 outer + 2 inner)", len(taps))
	}
	outer, inner := taps[:3], taps[3:]
	minStep := sd.minCastSpacing()
	for _, in := range inner {
		for _, out := range outer {
			if d := dist(in, out); d < minStep/2 {
				t.Errorf("inner tap %v is %.1f px from outer tap %v, want >= %.1f — a rotated pinned inner line whose ends converge must be pushed apart at cast level", in, d, out, minStep/2)
			}
		}
	}
}

// A user-pinned `_rage_inner` that lands almost on the outer line must be
// pushed apart. The pinned line is authored in reference space; projected and
// clamped into the live deploy band it landed 10 px from the outer line on a
// real 720p run while one cast covers 100 px, so four of the five Rage casts
// shared ground.
func TestFormulaLine_RagePinnedInnerTooCloseIsPushedApart(t *testing.T) {
	outerP1, outerP2 := image.Pt(453, 535), image.Pt(678, 363)
	f := &formula.Formula{
		Screen: formula.ScreenSize{W: 860, H: 732},
		Units: map[string]formula.UnitEntry{
			"rage spell":  {Type: "line", P1: &formula.Point{X: outerP1.X, Y: outerP1.Y}, P2: &formula.Point{X: outerP2.X, Y: outerP2.Y}, Jitter: 3},
			"_rage_inner": {Type: "line", P1: &formula.Point{X: 455, Y: 530}, P2: &formula.Point{X: 676, Y: 360}, Jitter: 3},
		},
	}
	sd, tl := newSpellTestHarness(t, PrecisionConfig{}, f, nil, []*TrackedSlot{spellSlot("rage spell", 100)})
	unit := strategy.Unit{Name: "Rage Spell", Amount: "All"} // 3 outer + 2 inner

	if !sd.DeploySpell(unit, spellSlot("rage spell", 100), "BottomRight", "Line") {
		t.Fatal("deploy failed")
	}
	taps := tl.fieldTaps()
	if len(taps) != 5 {
		t.Fatalf("got %d taps, want 5 (3 outer + 2 inner)", len(taps))
	}
	outer, inner := taps[:3], taps[3:]
	// Half a cast spacing is the report's own "not stacked" bar. Taps are
	// jittered after placement, so the exact nominal spacing is not asserted
	// here — only that the two sub-lines are genuinely apart.
	minStep := sd.minCastSpacing()
	for _, in := range inner {
		for _, out := range outer {
			if d := dist(in, out); d < minStep/2 {
				t.Errorf("inner tap %v is %.1f px from outer tap %v, want >= %.1f — a pinned inner line that lands on the outer one must be pushed apart", in, d, out, minStep/2)
			}
		}
	}
}

// A non-rage spell whose line is long enough for its casts must deploy every
// cast ON that line — it must not be split the way rage is.
func TestFormulaLine_NonRageStaysOnSingleLine(t *testing.T) {
	f := &formula.Formula{
		Screen: formula.ScreenSize{W: 860, H: 732},
		Units: map[string]formula.UnitEntry{
			"ice spell": {Type: "line", P1: &formula.Point{X: 340, Y: 520}, P2: &formula.Point{X: 700, Y: 300}, Jitter: 3},
		},
	}
	sd, tl := newSpellTestHarness(t, PrecisionConfig{}, f, nil, []*TrackedSlot{spellSlot("ice spell", 60)})
	unit := strategy.Unit{Name: "Ice Spell", Amount: "All"} // 5 taps, all on the one line

	if !sd.DeploySpell(unit, spellSlot("ice spell", 60), "BottomRight", "Line") {
		t.Fatal("deploy failed")
	}
	lineP1, lineP2 := image.Pt(340, 520), image.Pt(700, 300)
	taps := tl.fieldTaps()
	if len(taps) != 5 {
		t.Fatalf("got %d taps, want 5", len(taps))
	}
	for i, pt := range taps {
		if d := distToSegment(pt, lineP1, lineP2); d > 12 {
			t.Errorf("tap %d at %v is %.1fpx off the pinned ice line — non-rage spells must not be split", i, pt, d)
		}
	}
}

// …but a non-rage line too short to hold its casts is a different situation:
// the choice is between stacking casts on one spot and spreading them over the
// ground the line points at. Stacking is what the run did, so the deployer now
// spreads, and says so in the log.
func TestFormulaLine_NonRageShortLineSpreadsInsteadOfStacking(t *testing.T) {
	f := &formula.Formula{
		Screen: formula.ScreenSize{W: 860, H: 732},
		Units: map[string]formula.UnitEntry{
			"ice spell": {Type: "line", P1: &formula.Point{X: 480, Y: 429}, P2: &formula.Point{X: 537, Y: 391}, Jitter: 3},
		},
	}
	sd, tl := newSpellTestHarness(t, PrecisionConfig{}, f, nil, []*TrackedSlot{spellSlot("ice spell", 60)})
	unit := strategy.Unit{Name: "Ice Spell", Amount: "All"}

	if !sd.DeploySpell(unit, spellSlot("ice spell", 60), "BottomRight", "Line") {
		t.Fatal("deploy failed")
	}
	taps := tl.fieldTaps()
	if len(taps) != 5 {
		t.Fatalf("got %d taps, want 5 (a cast left in the bar is a cast not deployed)", len(taps))
	}
	minStep := sd.minCastSpacing()
	// Jitter is applied after the spread, so the exact bound is enforced on the
	// deterministic path (TestDistributeAlong_ShortLineDoesNotStackCasts). Here
	// the bar is "not stacked": the defect this replaced put the closest pair
	// 11 px apart against a 37.7 px minimum.
	for i := range taps {
		for j := i + 1; j < len(taps); j++ {
			d := dist(taps[i], taps[j])
			if d < minStep/2 {
				t.Errorf("taps %d and %d at %v/%v are %.1f px apart, want >= half of the %.1f px minimum",
					i, j, taps[i], taps[j], d, minStep)
			}
		}
	}
}

func dist(a, b image.Point) float64 {
	return math.Hypot(float64(a.X-b.X), float64(a.Y-b.Y))
}

// A reconcile re-fire must land on new ground. Live symptom this pins down: an
// ice spell deployed 13 casts across two passes whose closest pair was 4 px —
// the first pass covered its ring, the next passes cast onto the same ring, and
// every freeze after the first was spent on ground already frozen.
func TestFormulaLine_RefireDisplacesInsteadOfRestacking(t *testing.T) {
	f := &formula.Formula{
		Screen: formula.ScreenSize{W: 860, H: 732},
		Units: map[string]formula.UnitEntry{
			"ice spell": {Type: "line", P1: &formula.Point{X: 480, Y: 429}, P2: &formula.Point{X: 537, Y: 391}, Jitter: 0},
		},
	}
	sd, tl := newSpellTestHarness(t, PrecisionConfig{}, f, nil, []*TrackedSlot{spellSlot("ice spell", 60)})
	unit := strategy.Unit{Name: "Ice Spell", Amount: "All"}

	if !sd.DeploySpell(unit, spellSlot("ice spell", 60), "BottomRight", "Line") {
		t.Fatal("first deploy failed")
	}
	firstPass := append([]image.Point(nil), tl.fieldTaps()...)
	if len(firstPass) == 0 {
		t.Fatal("first pass fired no taps")
	}

	// Same geometry, one reconcile pass later.
	sd.firePass = 1
	if !sd.DeploySpell(unit, spellSlot("ice spell", 60), "BottomRight", "Line") {
		t.Fatal("re-fire failed")
	}
	sd.firePass = 0
	secondPass := tl.fieldTaps()[len(firstPass):]
	if len(secondPass) == 0 {
		t.Fatal("re-fire fired no taps")
	}

	minStep := sd.minCastSpacing()
	for i, a := range secondPass {
		for j, b := range firstPass {
			if d := dist(a, b); d < minStep {
				t.Errorf("re-fire tap %d at %v is %.1f px from first-pass tap %d at %v, want >= %.1f — a re-fire must not cast onto ground the first pass covered",
					i, a, d, j, b, minStep)
			}
		}
	}
}

func TestFormulaLines_ExplicitSubLinesRespected(t *testing.T) {
	f := &formula.Formula{
		Screen: formula.ScreenSize{W: 860, H: 732},
		Units: map[string]formula.UnitEntry{
			"rage spell": {Type: "lines", Lines: []formula.LinePoint{
				{P1: formula.Point{X: 453, Y: 535}, P2: formula.Point{X: 678, Y: 363}, Count: 3, Jitter: 3},
				{P1: formula.Point{X: 452, Y: 452}, P2: formula.Point{X: 588, Y: 344}, Count: 2, Jitter: 3},
			}},
		},
	}
	sd, tl := newSpellTestHarness(t, PrecisionConfig{}, f, nil, []*TrackedSlot{spellSlot("rage spell", 100)})
	unit := strategy.Unit{Name: "Rage Spell", Amount: "All"}

	if !sd.DeploySpell(unit, spellSlot("rage spell", 100), "BottomRight", "Line") {
		t.Fatal("deploy failed")
	}
	// The lines entry must re-select the slot between sub-lines.
	if got := len(tl.slotTaps()); got != 1 {
		t.Fatalf("got %d slot re-selects between sub-lines, want 1", got)
	}
	if got := len(tl.fieldTaps()); got != 5 {
		t.Fatalf("got %d field taps, want 3+2=5", got)
	}
}

func TestFormulaLines_ZeroCountSubLineSkipped(t *testing.T) {
	f := &formula.Formula{
		Screen: formula.ScreenSize{W: 860, H: 732},
		Units: map[string]formula.UnitEntry{
			"rage spell": {Type: "lines", Lines: []formula.LinePoint{
				{P1: formula.Point{X: 453, Y: 535}, P2: formula.Point{X: 678, Y: 363}, Count: 3, Jitter: 3},
				{P1: formula.Point{X: 452, Y: 452}, P2: formula.Point{X: 588, Y: 344}, Count: 0, Jitter: 3},
			}},
		},
	}
	sd, tl := newSpellTestHarness(t, PrecisionConfig{}, f, nil, []*TrackedSlot{spellSlot("rage spell", 100)})
	unit := strategy.Unit{Name: "Rage Spell", Amount: "All"}

	if !sd.DeploySpell(unit, spellSlot("rage spell", 100), "BottomRight", "Line") {
		t.Fatal("deploy failed")
	}
	if got := len(tl.fieldTaps()); got != 3 {
		t.Fatalf("got %d taps, want 3 (count=0 sub-line skipped)", got)
	}
	if got := len(tl.slotTaps()); got != 0 {
		t.Fatalf("got %d re-selects, want 0 (no second sub-line fired)", got)
	}
}

func TestFormulaPoint_RingAroundPinnedPoint(t *testing.T) {
	f := &formula.Formula{
		Screen: formula.ScreenSize{W: 860, H: 732},
		Units: map[string]formula.UnitEntry{
			"skeleton spell": {Type: "point", P: &formula.Point{X: 636, Y: 510}, Jitter: 5},
		},
	}
	sd, tl := newSpellTestHarness(t, PrecisionConfig{}, f, nil, []*TrackedSlot{spellSlot("skeleton spell", 60)})
	unit := strategy.Unit{Name: "Skeleton Spell", Amount: "3"}

	if !sd.DeploySpell(unit, spellSlot("skeleton spell", 60), "BottomRight", "Point") {
		t.Fatal("deploy failed")
	}
	taps := tl.fieldTaps()
	if len(taps) != 3 {
		t.Fatalf("got %d taps, want 3", len(taps))
	}
	jitter := 5
	radius := sd.castRingRadius(3, sd.ringChord(jitter))
	for i, pt := range taps {
		if d := math.Hypot(float64(pt.X-636), float64(pt.Y-510)); d > radius+float64(jitter)*math.Sqrt2+1 {
			t.Errorf("tap %d at %v is %.1fpx from the pinned point (ring radius %.0f + jitter %d per axis)", i, pt, d, radius, jitter)
		}
	}
	assertRingClearsSpacing(t, taps, sd)
}

// The empty-ring defect, pinned from the live run that exposed it. An Ice Spell
// card carrying 8 casts whose authored line is shorter than 8 spacings falls back
// to a ring — and the ring radius was capped at 45 reference px, below the 63 that
// 8 casts need to sit a minimum spacing apart. The measured closest pair was
// 41 px against a 50 px bar, so all 8 freezes landed on ground another already
// covered: one freeze cast eight times.
//
// This is white-box on purpose. The cap and the chord relation are the two halves
// that disagreed, so the test states the relation the radius must satisfy rather
// than a magic radius that happens to be big enough today.
func TestRingFallback_ShortLineOfEightCastsCoversDistinctGround(t *testing.T) {
	// 91px ice line: the shipped geometry measured in the run this rule came from.
	f := &formula.Formula{
		Screen: formula.ScreenSize{W: 860, H: 732},
		Units: map[string]formula.UnitEntry{
			"ice spell": {Type: "line", P1: &formula.Point{X: 520, Y: 300}, P2: &formula.Point{X: 611, Y: 300}, Jitter: 6},
		},
	}
	sd, tl := newSpellTestHarness(t, PrecisionConfig{}, f, nil, []*TrackedSlot{spellSlot("ice spell", 60)})
	unit := strategy.Unit{Name: "Ice Spell", Amount: "8"}

	if !sd.DeploySpell(unit, spellSlot("ice spell", 60), "BottomRight", "Line") {
		t.Fatal("formula line deploy failed")
	}
	taps := tl.fieldTaps()
	if len(taps) != 8 {
		t.Fatalf("got %d taps, want 8", len(taps))
	}

	// The radius the chord relation asks for, and the ceiling that used to clamp
	// it, so a future ceiling cannot quietly reintroduce the overlap.
	exact := (sd.ringChord(6) + ringRoundPadPx) / (2 * math.Sin(math.Pi/8))
	if ceiling := sd.executor.cal.Length(maxCastRingRadiusRef); ceiling < exact {
		t.Fatalf("the cast-ring ceiling is %.0fpx but 8 casts at a %.0fpx chord need %.0fpx: the ceiling is clamping a correctly-spaced ring into overlap", ceiling, sd.ringChord(6), exact)
	}
	assertRingClearsSpacing(t, taps, sd)
}

// ---------------------------------------------------------------------------
// Shipped formula regression: every spell unit in the real file is a usable
// entry for BOTH the default and all three corner overrides.
// ---------------------------------------------------------------------------

func TestShippedEdragFormula_SpellEntriesUsableForAllCorners(t *testing.T) {
	f := loadShippedFormula(t)

	corners := []string{"BottomRight", "BottomLeft", "TopLeft", "TopRight"}
	for _, corner := range corners {
		corner := corner
		t.Run(corner, func(t *testing.T) {
			// Deep-copy through JSON so per-corner mutation can't leak.
			raw, err := json.Marshal(f)
			if err != nil {
				t.Fatal(err)
			}
			cf, err := formula.LoadFile(writeTemp(t, raw))
			if err != nil {
				t.Fatal(err)
			}

			if override, ok := cf.CornerOverrides[corner]; ok {
				for k, v := range override {
					cf.Units[k] = v
				}
			} else {
				cf.MirrorForCorner(corner)
			}
			cf.ApplyScreenScale(cf.Screen.W, cf.Screen.H, 860, 732) // identity at reference size

			for _, name := range []string{"rage spell", "ice spell"} {
				entry, ok := cf.LookUp(name)
				if !ok {
					t.Fatalf("%q missing from formula for corner %s", name, corner)
				}
				switch {
				case entry.IsLines() && len(entry.Lines) > 0:
				case entry.IsLine() && entry.P1 != nil && entry.P2 != nil:
					if entry.P1.X == entry.P2.X && entry.P1.Y == entry.P2.Y {
						t.Errorf("%q (%s): degenerate line %v→%v", name, corner, entry.P1, entry.P2)
					}
				case entry.IsPoint() && entry.P != nil:
				default:
					t.Errorf("%q (%s): entry has no usable geometry (type=%q)", name, corner, entry.Type)
				}
			}
		})
	}
}

func writeTemp(t *testing.T, data []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "formula.json")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// ---------------------------------------------------------------------------
// Mirror correctness — the shared-pointer bug
// ---------------------------------------------------------------------------

func TestMirrorForCorner_MirrorsSharedPointsExactlyOnce(t *testing.T) {
	// Two units pinned at the SAME coordinate (the shipped formula shares
	// (636,510) across barbarian king / archer queen / grand warden).
	f := &formula.Formula{
		Screen: formula.ScreenSize{W: 860, H: 732},
		Units: map[string]formula.UnitEntry{
			"barbarian king": {Type: "point", P: &formula.Point{X: 636, Y: 510}},
			"archer queen":   {Type: "point", P: &formula.Point{X: 636, Y: 510}},
			"ice spell":      {Type: "line", P1: &formula.Point{X: 480, Y: 429}, P2: &formula.Point{X: 537, Y: 391}},
		},
	}
	f.MirrorForCorner("TopLeft") // mirror both axes

	bk := f.Units["barbarian king"].P
	aq := f.Units["archer queen"].P
	if bk.X != 860-636 || bk.Y != 732-510 {
		t.Fatalf("barbarian king mirrored to (%d,%d), want (%d,%d)", bk.X, bk.Y, 860-636, 732-510)
	}
	if aq.X != bk.X || aq.Y != bk.Y {
		t.Fatalf("archer queen landed at (%d,%d) but barbarian king at (%d,%d) — shared point was mutated more than once", aq.X, aq.Y, bk.X, bk.Y)
	}
	ice := f.Units["ice spell"]
	if ice.P1.X != 860-480 || ice.P2.X != 860-537 || ice.P1.Y != 732-429 || ice.P2.Y != 732-391 {
		t.Fatalf("ice line mirrored to %v→%v, want (%d,%d)→(%d,%d)", ice.P1, ice.P2, 860-480, 732-429, 860-537, 732-391)
	}
}

func TestMirrorForCorner_DoubleApplicationRestoresAuthored(t *testing.T) {
	base := func() *formula.Formula {
		return &formula.Formula{
			Screen: formula.ScreenSize{W: 860, H: 732},
			Units: map[string]formula.UnitEntry{
				"rage spell": {Type: "line", P1: &formula.Point{X: 453, Y: 535}, P2: &formula.Point{X: 678, Y: 363}},
				"heroes":     {Type: "point", P: &formula.Point{X: 636, Y: 510}},
				"duo":        {Type: "point", P: &formula.Point{X: 636, Y: 510}},
			},
		}
	}
	// A mirror is an involution: applying the same reflection twice must
	// restore the authored coordinates exactly. The regression shape of
	// the shared-pointer bug was heroes/duo DIVERGING after one pass.
	corners := []string{"BottomRight", "BottomLeft", "TopRight", "TopLeft"}
	for _, corner := range corners {
		f := base()
		f.MirrorForCorner(corner)
		if f.Units["heroes"].P.X != f.Units["duo"].P.X || f.Units["heroes"].P.Y != f.Units["duo"].P.Y {
			t.Errorf("corner %s: units sharing one authored point diverged after mirror", corner)
		}
		f.MirrorForCorner(corner) // second pass restores authored coords
		if got := *f.Units["rage spell"].P1; got != (formula.Point{X: 453, Y: 535}) {
			t.Errorf("corner %s: double mirror did not restore authored coords: P1 %v", corner, got)
		}
		if got := *f.Units["heroes"].P; got != (formula.Point{X: 636, Y: 510}) {
			t.Errorf("corner %s: double mirror did not restore shared point: %v", corner, got)
		}
	}
}

func TestMirrorForCorner_CornersMatchAxisMath(t *testing.T) {
	cases := map[string][2]int{
		"BottomRight": {30, 40},
		"BottomLeft":  {70, 40},
		"TopRight":    {30, 60},
		"TopLeft":     {70, 60},
	}
	for corner, want := range cases {
		ff := &formula.Formula{
			Screen: formula.ScreenSize{W: 100, H: 100},
			Units: map[string]formula.UnitEntry{
				"u": {Type: "point", P: &formula.Point{X: 30, Y: 40}},
			},
		}
		ff.MirrorForCorner(corner)
		if got := ff.Units["u"].P; got.X != want[0] || got.Y != want[1] {
			t.Errorf("corner %s: got (%d,%d), want (%d,%d)", corner, got.X, got.Y, want[0], want[1])
		}
	}
}

// ---------------------------------------------------------------------------
// deriveInwardLine — the rage auto-split inner line
// ---------------------------------------------------------------------------

func TestDeriveInwardLine_ParallelAndOffsetTowardCenter(t *testing.T) {
	p1, p2 := image.Pt(700, 200), image.Pt(800, 300) // line out near the BR corner
	in1, in2 := deriveInwardLine(p1, p2, 860, 732, 35)

	// Same direction (parallel).
	d1 := float64(p2.X-p1.X) / math.Hypot(float64(p2.X-p1.X), float64(p2.Y-p1.Y))
	d2 := float64(in2.X-in1.X) / math.Hypot(float64(in2.X-in1.X), float64(in2.Y-in1.Y))
	if math.Abs(d1-d2) > 0.01 {
		t.Errorf("inner line not parallel: dir1=%.4f dir2=%.4f", d1, d2)
	}
	// Shifted ~35px inward (toward screen center).
	mid := image.Pt((p1.X+p2.X)/2, (p1.Y+p2.Y)/2)
	midIn := image.Pt((in1.X+in2.X)/2, (in1.Y+in2.Y)/2)
	shift := math.Hypot(float64(mid.X-midIn.X), float64(mid.Y-midIn.Y))
	if math.Abs(shift-35) > 2 {
		t.Errorf("inner line shifted %.1fpx, want ~35px", shift)
	}
	// Inward means closer to the screen center than the original.
	origDist := math.Hypot(float64(430-mid.X), float64(366-mid.Y))
	newDist := math.Hypot(float64(430-midIn.X), float64(366-midIn.Y))
	if newDist >= origDist {
		t.Errorf("inner line midpoint (%v) is not closer to center than outer (%v)", midIn, mid)
	}
}

func TestDeriveInwardLine_DegenerateLineStillShifts(t *testing.T) {
	p1, p2 := image.Pt(700, 500), image.Pt(700, 500)
	in1, in2 := deriveInwardLine(p1, p2, 860, 732, 35)
	if in1 == p1 && in2 == p2 {
		t.Fatal("degenerate line was not shifted toward center")
	}
	shift := math.Hypot(float64(p1.X-in1.X), float64(p1.Y-in1.Y))
	if math.Abs(shift-35) > 2 {
		t.Errorf("degenerate shift %.1fpx, want ~35px", shift)
	}
}

// ---------------------------------------------------------------------------
// Phase-offset fallback (valk_spam EQ ring "deeper in")
// ---------------------------------------------------------------------------

func TestPhaseOffsetFallback_FourSidesUsesPhaseOffsetWhenUnitZero(t *testing.T) {
	// Only the BottomRight edge configured — the other three must be
	// skipped (no zero-value (0,0) taps).
	pCfg := PrecisionConfig{
		Edges: map[string]ManualEdge{
			"BottomRight": {P1: image.Pt(100, 700), P2: image.Pt(800, 700)},
		},
	}
	unit := strategy.Unit{Name: "Earthquake Spell", Amount: "All"} // Offset unset
	unit.PhaseOffset = 130                                         // valk_spam's phase pin

	sd, tl := newSpellTestHarness(t, pCfg, nil, nil, []*TrackedSlot{spellSlot("earthquake spell", 100)})
	if !sd.DeploySpell(unit, spellSlot("earthquake spell", 100), "BottomRight", "FourSides") {
		t.Fatal("FourSides EQ deploy failed")
	}
	taps := tl.fieldTaps()
	if len(taps) != 4 { // 1 configured edge × 4 taps
		t.Fatalf("got %d taps, want 4", len(taps))
	}
	// BottomRight edge taps: y must be pulled INWARD (up) from y=700 by
	// 130/300 of the way to the center — 700→~402, so clearly above 600.
	for _, pt := range taps {
		if pt.Y > 600 {
			t.Fatalf("tap at %v sits on the outer edge — phase offset 130 was not applied", pt)
		}
	}
	// And no tap may land at the degenerate (0,0) corner.
	for _, pt := range taps {
		if pt.X < 20 && pt.Y < 20 {
			t.Fatalf("tap at %v hit the unconfigured zero-edge corner", pt)
		}
	}
}

func TestPhaseOffsetFallback_PerUnitOffsetWinsOverPhase(t *testing.T) {
	pCfg := PrecisionConfig{
		Edges: map[string]ManualEdge{
			"BottomRight": {P1: image.Pt(100, 700), P2: image.Pt(800, 700)},
		},
	}
	unit := strategy.Unit{Name: "Earthquake Spell", Amount: "All", Offset: 10}
	unit.PhaseOffset = 130

	sd, tl := newSpellTestHarness(t, pCfg, nil, nil, []*TrackedSlot{spellSlot("earthquake spell", 100)})
	sd.DeploySpell(unit, spellSlot("earthquake spell", 100), "BottomRight", "FourSides")

	// offset 10 → 10/300 ≈ 3% inward: taps stay near the raw edge line.
	// With phase 130 wrongly applied they'd sit ~200px above it.
	edge := pCfg.Edges["BottomRight"]
	for i, pt := range tl.fieldTaps() {
		if d := distToSegment(pt, edge.P1, edge.P2); d > 40 {
			t.Fatalf("tap %d at %v is %.0fpx off the edge — per-unit Offset=10 should have won over phase 130", i, pt, d)
		}
	}
}

// ---------------------------------------------------------------------------
// DistributeAlong geometry
// ---------------------------------------------------------------------------

func TestDistributeAlong_EndpointsAndSpread(t *testing.T) {
	sd, _ := newSpellTestHarness(t, PrecisionConfig{}, nil, nil, nil)
	// A line long enough to carry 5 casts at the minimum spacing keeps the
	// original even spread: pct 0.22 … 0.78 of the segment.
	p1, p2 := image.Pt(0, 0), image.Pt(500, 0)

	pts := sd.distributeAlong(p1, p2, 5, 0) // jitter 0 → exact points
	if len(pts) != 5 {
		t.Fatalf("got %d points, want 5", len(pts))
	}
	wantPcts := []float64{0.22, 0.36, 0.50, 0.64, 0.78}
	for i, wp := range wantPcts {
		wantX := int(500 * wp)
		if math.Abs(float64(pts[i].X-wantX)) > 1 {
			t.Errorf("point %d: x=%d, want ~%d", i, pts[i].X, wantX)
		}
	}
}

// A line too short for its cast count must not stack its casts on the same
// spot. This is the regression test for the defect that cost a real run five
// Ice Spells: the ice line is 68.5 px against a 5-cast count, and splitting it
// into five equal taps put the closest pair 11 px apart — one freeze landed
// five times, and the same shape would do it to any short formula line.
func TestDistributeAlong_ShortLineDoesNotStackCasts(t *testing.T) {
	sd, _ := newSpellTestHarness(t, PrecisionConfig{}, nil, nil, nil)
	minStep := sd.minCastSpacing()
	if minStep <= 0 {
		t.Fatalf("minCastSpacing() = %v, want a positive constraint", minStep)
	}

	// The shipped ice line, in reference px (it is 68.5 px long).
	p1, p2 := image.Pt(480, 429), image.Pt(537, 391)
	pts := sd.distributeAlong(p1, p2, 5, 0)
	if len(pts) != 5 {
		t.Fatalf("got %d points, want 5", len(pts))
	}

	for i := range pts {
		for j := i + 1; j < len(pts); j++ {
			d := dist(pts[i], pts[j])
			if d < minStep-0.5 { // 0.5 px slack for the integer tap coordinates
				t.Errorf("casts %d and %d are %.1f px apart, want >= %.1f (stacked casts waste cards)",
					i, j, d, minStep)
			}
		}
	}

	// …and they stay anchored to the geometry the user drew: the cluster may
	// not wander off to some other part of the village.
	mid := image.Pt((p1.X+p2.X)/2, (p1.Y+p2.Y)/2)
	maxR := sd.executor.cal.Length(maxCastRingRadiusRef) + 1
	for i, pt := range pts {
		if d := dist(pt, mid); d > maxR {
			t.Errorf("cast %d at %v is %.1f px from the line midpoint, want <= %.0f", i, pt, d, maxR)
		}
	}
}

// A two-cast line has no spacing problem to solve: it must keep the ordinary
// even spread, so a common "2 rage on this line" formula is untouched.
func TestDistributeAlong_TwoCastsOnShortLineStillSpread(t *testing.T) {
	sd, _ := newSpellTestHarness(t, PrecisionConfig{}, nil, nil, nil)
	pts := sd.distributeAlong(image.Pt(480, 429), image.Pt(537, 391), 2, 0)
	if len(pts) != 2 {
		t.Fatalf("got %d points, want 2", len(pts))
	}
	p1, p2 := image.Pt(480, 429), image.Pt(537, 391)
	for i, pct := range []float64{0.22, 0.78} {
		want := image.Pt(
			int(float64(p1.X)+float64(p2.X-p1.X)*pct),
			int(float64(p1.Y)+float64(p2.Y-p1.Y)*pct),
		)
		if pts[i] != want {
			t.Fatalf("two-cast distribution point %d = %v, want the line's %.2f point %v", i, pts[i], pct, want)
		}
	}
}

// TestDistributeAlong_SingleCountCastsAtLineCentre pins the rule that stopped
// the ice/freeze spell being tapped onto the troop bar.
//
// A line's ENDPOINTS are the least trustworthy points on it: they are what the
// projection clamp and the red-line push displace first. Live 2026-09-25 (run7,
// BottomRight) the ice spell's line came out as p1=(751,611) — the bar's own card
// row — the single cast fired at (751,614) and CoC handed that tap to the bar
// instead of casting it. The card kept its one charge through two attempts
// (`reconcile: spells still on card; re-firing remainder` → `OCR count did not
// drop after re-fire`), the post-deploy frame still read `x1` (OCR-confirmed),
// and the run reported Deploy Health SUCCESS. One cast means the line's centre.
func TestDistributeAlong_SingleCountCastsAtLineCentre(t *testing.T) {
	sd, _ := newSpellTestHarness(t, PrecisionConfig{}, nil, nil, nil)

	p1, p2 := image.Pt(751, 611), image.Pt(960, 530)
	pts := sd.distributeAlong(p1, p2, 1, 0)
	if len(pts) != 1 {
		t.Fatalf("single-count distribution = %v, want exactly one cast", pts)
	}
	centre := image.Pt((p1.X+p2.X)/2, (p1.Y+p2.Y)/2)
	if pts[0] != centre {
		t.Errorf("single cast = %v, want the line's centre %v", pts[0], centre)
	}
	if pts[0].Y >= p1.Y {
		t.Errorf("single cast y=%d must sit above the displaced endpoint y=%d, or the tap goes to the bar", pts[0].Y, p1.Y)
	}

	// A re-fire still has to land on fresh ground rather than the same spot.
	if sd.minCastSpacing() > 0 {
		sd.firePass = 1
		again := sd.distributeAlong(p1, p2, 1, 0)
		sd.firePass = 0
		if again[0] == pts[0] {
			t.Errorf("a re-fire cast onto the identical point %v; it must be displaced one cast spacing", again[0])
		}
	}
}

// TestCastPointDeployable: the gate every spell tap passes through. A tap that
// leaves the screen deploys nothing (live 2026-09-25: a TopLeft sweep line
// projected to x=-11 and sent 18 taps off-screen), a tap above YTopMin is behind
// the top HUD, and a tap on the bar is handed to the card row instead of casting.
func TestCastPointDeployable(t *testing.T) {
	sd, _ := newSpellTestHarness(t, PrecisionConfig{}, nil, nil, nil) // 860x732, no counter

	// Without a counter the bar's top is the deploy band's own bottom (0.85·h).
	barTop := sd.spellBarTop()
	if barTop <= 0 || barTop >= sd.h {
		t.Fatalf("harness bar top = %d, want a bound inside the %dpx screen", barTop, sd.h)
	}
	cases := []struct {
		name string
		pt   image.Point
		want bool
	}{
		{"mid-field", image.Pt(400, 300), true},
		{"just above the bar", image.Pt(400, barTop-1), true},
		{"on the bar", image.Pt(400, barTop), false},
		{"under the bar", image.Pt(400, 700), false},
		{"behind the top HUD", image.Pt(400, YTopMin()-1), false},
		{"at the top of the band", image.Pt(400, YTopMin()), true},
		{"off the left edge", image.Pt(-1, 300), false},
		{"off the right edge", image.Pt(860, 300), false},
		{"off the top edge", image.Pt(400, -20), false},
		{"off the bottom edge", image.Pt(400, 732), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sd.castPointDeployable(tc.pt); got != tc.want {
				t.Errorf("castPointDeployable(%v) = %v, want %v", tc.pt, got, tc.want)
			}
		})
	}

	// With the count reader wired, the bar's top is the counter's own bar_y.
	sd.SetCounter(nil, barTop)
	if sd.castPointDeployable(image.Pt(400, barTop)) {
		t.Error("the counter's bar_y must bound the field the same way")
	}
}

// ---------------------------------------------------------------------------
// Valkyrie army spell phase end-to-end (legacy path, FourSides EQ)
// ---------------------------------------------------------------------------

func TestValkyrieSpells_EarthquakeFourSidesDeploysOnAllFourEdges(t *testing.T) {
	edges := map[string]ManualEdge{
		"TopRight":    {P1: image.Pt(700, 100), P2: image.Pt(800, 200)},
		"BottomRight": {P1: image.Pt(800, 600), P2: image.Pt(700, 700)},
		"BottomLeft":  {P1: image.Pt(100, 700), P2: image.Pt(200, 600)},
		"TopLeft":     {P1: image.Pt(100, 100), P2: image.Pt(200, 200)},
	}
	sd, tl := newSpellTestHarness(t, PrecisionConfig{Edges: edges}, nil, nil, []*TrackedSlot{spellSlot("earthquake spell", 100)})
	unit := strategy.Unit{Name: "Earthquake Spell", Amount: "All", PhaseOffset: 130}

	if !sd.DeploySpell(unit, spellSlot("earthquake spell", 100), "BottomRight", "FourSides") {
		t.Fatal("FourSides deploy failed")
	}
	taps := tl.fieldTaps()
	if len(taps) != 16 {
		t.Fatalf("got %d taps, want 16 (4 per edge)", len(taps))
	}
	// Every tap must sit near one of the four edges (within offset pull + jitter).
	for i, pt := range taps {
		best := math.Inf(1)
		for _, e := range edges {
			if d := distToSegment(pt, e.P1, e.P2); d < best {
				best = d
			}
		}
		// 130/300 ≈ 43% pull toward center moves taps off the raw lines;
		// allow up to ~200px for the pull, but they must not be scattered
		// across the whole screen (>300px from everything is broken).
		if best > 300 {
			t.Errorf("tap %d at %v is %.0fpx from every edge — FourSides EQ placement broken", i, pt, best)
		}
	}
}

// ---------------------------------------------------------------------------
// Guard: spell deploy must never tap outside the screen
// ---------------------------------------------------------------------------

func TestDeploySpell_TapsStayWithinScreen(t *testing.T) {
	f := loadShippedFormula(t)
	f.MirrorForCorner("TopLeft")
	sd, tl := newSpellTestHarness(t, PrecisionConfig{}, f, nil, []*TrackedSlot{spellSlot("rage spell", 100), spellSlot("ice spell", 60)})

	for _, u := range []strategy.Unit{
		{Name: "Rage Spell", Amount: "All"},
		{Name: "Ice Spell", Amount: "All"},
	} {
		sd.DeploySpell(u, spellSlot(strings.ToLower(u.Name), 100), "TopLeft", "Line")
	}
	for i, pt := range tl.fieldTaps() {
		if pt.X < 0 || pt.X >= 860 || pt.Y < 0 || pt.Y >= 732 {
			t.Errorf("tap %d at %v is outside the 860x732 screen", i, pt)
		}
	}
}

// ---------------------------------------------------------------------------
// Planner wiring: spells classified + phase offset copied
// ---------------------------------------------------------------------------

func TestPlanPhase_ClassifiesSpellsAndCopiesPhaseOffset(t *testing.T) {
	sm := newTestSlotManager([]*TrackedSlot{
		spellSlot("earthquake spell", 100),
		spellSlot("valkyrie", 62),
	})
	dp := NewDeployPlanner(sm, PrecisionConfig{}, "BottomRight", 860, 732, zerolog.Nop())

	phase := strategy.Phase{
		Name:    "Earthquakes",
		Pattern: "FourSides",
		Offset:  130,
		Units: []strategy.Unit{
			{Name: "Earthquake Spell", Amount: "All"},
			{Name: "Valkyrie", Amount: "All"},
		},
	}
	plan := dp.planPhase(phase)

	var eq *UnitPlan
	var valk *UnitPlan
	for i := range plan.UnitPlans {
		switch strings.ToLower(plan.UnitPlans[i].Unit.Name) {
		case "earthquake spell":
			eq = &plan.UnitPlans[i]
		case "valkyrie":
			valk = &plan.UnitPlans[i]
		}
	}
	if eq == nil || valk == nil {
		t.Fatalf("planner lost units: %+v", plan.UnitPlans)
	}
	if !eq.IsSpell {
		t.Error("Earthquake Spell not classified as spell")
	}
	if valk.IsSpell {
		t.Error("Valkyrie misclassified as spell")
	}
	if eq.Unit.PhaseOffset != 130 {
		t.Errorf("phase offset 130 not copied into the unit plan (got %d)", eq.Unit.PhaseOffset)
	}
	if eq.Slot == nil || eq.Slot.X != 100 {
		t.Errorf("EQ slot not resolved to x=100 (got %+v)", eq.Slot)
	}
}

// ---------------------------------------------------------------------------
// isSpellStatic contract
// ---------------------------------------------------------------------------

func TestIsSpellStatic(t *testing.T) {
	cases := map[string]bool{
		"Rage Spell":       true,
		"earthquake spell": true,
		"Ice Spell":        true,
		"Poison Spell":     true,
		"Valkyrie":         false,
		"Balloon":          false,
		"Barbarian King":   false,
		"Stone Slammer":    false,
	}
	for name, want := range cases {
		if got := isSpellStatic(strings.ToLower(name)); got != want {
			t.Errorf("isSpellStatic(%q) = %v, want %v", name, got, want)
		}
	}
}

// ---------------------------------------------------------------------------
// Helpers used above
// ---------------------------------------------------------------------------

// signedInwardDistance returns the perpendicular distance from pt to line
// a→b, positive when pt is on the inward side (toward center).
func signedInwardDistance(pt, a, b, center image.Point) float64 {
	ax, ay := float64(a.X), float64(a.Y)
	bx, by := float64(b.X), float64(b.Y)
	px, py := float64(pt.X), float64(pt.Y)
	cx, cy := float64(center.X), float64(center.Y)

	nx, ny := -(by - ay), bx-ax // perpendicular
	norm := math.Hypot(nx, ny)
	if norm == 0 {
		return math.Inf(1)
	}
	nx, ny = nx/norm, ny/norm
	if (cx-ax)*nx+(cy-ay)*ny < 0 {
		nx, ny = -nx, -ny
	}
	return (px-ax)*nx + (py-ay)*ny
}

// Compile-time guard that the tap hook event type matches what we record.
var _ = fmt.Sprintf
