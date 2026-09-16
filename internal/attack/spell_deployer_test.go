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
	for i, pt := range taps {
		if d := math.Hypot(float64(pt.X-500), float64(pt.Y-400)); d > 18+6+1 {
			t.Errorf("tap %d at %v is %.1fpx from target (ring radius 18 + jitter 6)", i, pt, d)
		}
	}
	// Ring taps must not all stack on the center point.
	if spread(taps) < 12 {
		t.Error("ring taps clustered at center — ring offsets were not applied")
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
	nearOuter, nearInner := 0, 0
	for _, pt := range taps {
		dOuter := distToSegment(pt, outerP1, outerP2)
		if dOuter <= 8 {
			nearOuter++
		} else {
			// Inner line: must be INWARD (toward center) of the outer line.
			dInner := signedInwardDistance(pt, outerP1, outerP2, image.Pt(430, 366))
			if dInner > 45 {
				t.Errorf("tap at %v is neither on the outer line (%.1fpx) nor near the inner offset (~35px: %.1fpx)", pt, dOuter, dInner)
			} else {
				nearInner++
			}
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

func TestFormulaLine_NonRageStaysOnSingleLine(t *testing.T) {
	f := &formula.Formula{
		Screen: formula.ScreenSize{W: 860, H: 732},
		Units: map[string]formula.UnitEntry{
			"ice spell": {Type: "line", P1: &formula.Point{X: 480, Y: 429}, P2: &formula.Point{X: 537, Y: 391}, Jitter: 3},
		},
	}
	sd, tl := newSpellTestHarness(t, PrecisionConfig{}, f, nil, []*TrackedSlot{spellSlot("ice spell", 60)})
	unit := strategy.Unit{Name: "Ice Spell", Amount: "All"} // 5 taps, all on the one line

	if !sd.DeploySpell(unit, spellSlot("ice spell", 60), "BottomRight", "Line") {
		t.Fatal("deploy failed")
	}
	lineP1, lineP2 := image.Pt(480, 429), image.Pt(537, 391)
	for i, pt := range tl.fieldTaps() {
		if d := distToSegment(pt, lineP1, lineP2); d > 12 {
			t.Errorf("tap %d at %v is %.1fpx off the pinned ice line — non-rage spells must not be split", i, pt, d)
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
	for i, pt := range taps {
		if d := math.Hypot(float64(pt.X-636), float64(pt.Y-510)); d > 18+6+1 {
			t.Errorf("tap %d at %v is %.1fpx from the pinned point", i, pt, d)
		}
	}
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
	p1, p2 := image.Pt(0, 0), image.Pt(100, 0)

	pts := sd.distributeAlong(p1, p2, 5, 0) // jitter 0 → exact points
	if len(pts) != 5 {
		t.Fatalf("got %d points, want 5", len(pts))
	}
	// pct = 0.22 … 0.78 evenly spaced.
	wantPcts := []float64{0.22, 0.36, 0.50, 0.64, 0.78}
	for i, wp := range wantPcts {
		wantX := int(100 * wp)
		if math.Abs(float64(pts[i].X-wantX)) > 1 {
			t.Errorf("point %d: x=%d, want ~%d", i, pts[i].X, wantX)
		}
	}
}

func TestDistributeAlong_SingleCountReturnsP1(t *testing.T) {
	sd, _ := newSpellTestHarness(t, PrecisionConfig{}, nil, nil, nil)
	pts := sd.distributeAlong(image.Pt(12, 34), image.Pt(99, 99), 1, 0)
	if len(pts) != 1 || pts[0] != (image.Point{X: 12, Y: 34}) {
		t.Fatalf("single-count distribution = %v, want [(12,34)]", pts)
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
