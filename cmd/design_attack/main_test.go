package main

import (
	"image"
	"os"
	"testing"

	"github.com/Ducky705/ClashGO/pkg/formula"
	"github.com/Ducky705/ClashGO/pkg/strategy"
)

// The picker is a click tool, but everything that decides what lands in the
// formula file is pure: buildPlan says what to click for each unit and commit
// turns the clicks into a UnitEntry. Those two functions are what a window
// cannot be tested through, and they are where the ring contract lives.
//
// The regression these pin: a FourSides phase saved as a plain "line" (or a
// Point phase saved as a line) produces a formula the deploy path reads
// differently from what the user drew, and nothing fails until the troops land
// on one flank instead of around the base.

func valkStrategyPath(t *testing.T) string {
	t.Helper()
	for _, p := range []string{"../../assets/strategies/valk_spam.yaml", "assets/strategies/valk_spam.yaml"} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	t.Skip("valk_spam.yaml not found from test cwd")
	return ""
}

func planRow(t *testing.T, plan []plannedUnit, name string) plannedUnit {
	t.Helper()
	for _, u := range plan {
		if u.Name == name {
			return u
		}
	}
	t.Fatalf("plan has no row for %q (rows: %v)", name, plan)
	return plannedUnit{}
}

// A ring unit takes a P1/P2 pair per side, so it needs 2 clicks — not 1. A
// 1-click row would save a "point" for the Valkyries and drop the whole card on
// one tile.
func TestBuildPlan_FourSidesUnitTakesTwoClicksPerSide(t *testing.T) {
	s, err := strategy.ParseYAML(valkStrategyPath(t))
	if err != nil {
		t.Fatalf("parse strategy: %v", err)
	}
	plan := buildPlan(s, false)

	valk := planRow(t, plan, "valkyrie")
	if valk.Needs != 2 {
		t.Errorf("valkyrie Needs = %d, want 2 (one P1/P2 pair per side)", valk.Needs)
	}
	if valk.Pattern != "FourSides" {
		t.Errorf("valkyrie pattern = %q, want FourSides", valk.Pattern)
	}

	// The spell phase is a Point phase: exactly one click, the one spot the
	// whole spell card drops on.
	eq := planRow(t, plan, "earthquake spell")
	if eq.Needs != 1 {
		t.Errorf("earthquake spell Needs = %d, want 1 (a single point)", eq.Needs)
	}

	// No rage in this army, so no _rage_inner row should be appended — a stray
	// row is an extra click the user is asked for on a spell that is not here.
	for _, u := range plan {
		if u.Name == "_rage_inner" {
			t.Fatal("plan appended a _rage_inner row for a strategy with no Rage spell")
		}
	}
}

func TestCommit_FourSidesUnitWritesASidesRing(t *testing.T) {
	st := &state{
		planned: []plannedUnit{{Name: "valkyrie", Phase: "Valkyrie Spam", Pattern: "FourSides", Needs: 2}},
		saved:   map[string]formula.UnitEntry{},
		subLines: []subLineEntry{
			{Clicks: []image.Point{image.Pt(200, 150), image.Pt(1080, 150)}, Count: 1},
			{Clicks: []image.Point{image.Pt(1080, 150), image.Pt(1080, 560)}, Count: 1},
			{Clicks: []image.Point{image.Pt(1080, 560), image.Pt(200, 560)}, Count: 1},
			{Clicks: []image.Point{image.Pt(200, 560), image.Pt(200, 150)}, Count: 1},
		},
	}
	if err := commit(st); err != nil {
		t.Fatalf("commit: %v", err)
	}
	e, ok := st.saved["valkyrie"]
	if !ok {
		t.Fatal("commit saved no entry for valkyrie")
	}
	if e.Type != "sides" {
		t.Fatalf("committed type = %q, want \"sides\" so the deploy spreads the card across the ring", e.Type)
	}
	if !e.IsSides() || e.IsLines() {
		t.Fatalf("committed entry disagrees with itself: IsSides=%v IsLines=%v", e.IsSides(), e.IsLines())
	}
	if len(e.Lines) != 4 {
		t.Fatalf("ring carries %d sides, want 4", len(e.Lines))
	}
	if got := e.Lines[1].P2; got != (formula.Point{X: 1080, Y: 560}) {
		t.Errorf("ring side 2 ends at %v, want the right-side end of the second pair", got)
	}
}

// A ring of one line is not a ring. The commit must refuse rather than write a
// single-sided "sides" entry the deploy would spread one side's worth of units
// over.
func TestCommit_RingNeedsAtLeastTwoSides(t *testing.T) {
	st := &state{
		planned: []plannedUnit{{Name: "valkyrie", Phase: "Valkyrie Spam", Pattern: "FourSides", Needs: 2}},
		saved:   map[string]formula.UnitEntry{},
		// Two clicks in hand but never finalized with `m`, so nothing is in
		// subLines: the user has drawn one side and is pressing ENTER.
		clicks: []image.Point{image.Pt(200, 150), image.Pt(1080, 150)},
	}
	if err := commit(st); err == nil {
		t.Fatal("committing a FourSides unit with no finalized sides must fail; a plain \"line\" would be written and the ring silently lost")
	}
	if _, saved := st.saved["valkyrie"]; saved {
		t.Fatal("a rejected commit must not save an entry")
	}
}

func TestCommit_PointPhaseUnitWritesASinglePoint(t *testing.T) {
	st := &state{
		planned: []plannedUnit{{Name: "earthquake spell", Phase: "Earthquakes", Pattern: "Point", Needs: 1}},
		saved:   map[string]formula.UnitEntry{},
		clicks:  []image.Point{image.Pt(900, 400)},
	}
	if err := commit(st); err != nil {
		t.Fatalf("commit: %v", err)
	}
	e := st.saved["earthquake spell"]
	if !e.IsPoint() || e.P == nil {
		t.Fatalf("committed entry is not a point: %+v", e)
	}
	if *e.P != (formula.Point{X: 900, Y: 400}) {
		t.Errorf("point = %v, want the clicked spot", *e.P)
	}
}

// The rage 3+2 split must keep writing "lines": it is a spell's sub-lines, not a
// ring, and the deploy path reflects it per corner.
func TestCommit_LinePhaseRageStillWritesLines(t *testing.T) {
	st := &state{
		planned: []plannedUnit{{Name: "rage spell", Phase: "Spells", Pattern: "Line", Needs: 2}},
		saved:   map[string]formula.UnitEntry{},
		subLines: []subLineEntry{
			{Clicks: []image.Point{image.Pt(100, 200), image.Pt(120, 300)}, Count: 3},
			{Clicks: []image.Point{image.Pt(140, 300), image.Pt(160, 400)}, Count: 2},
		},
	}
	if err := commit(st); err != nil {
		t.Fatalf("commit: %v", err)
	}
	e := st.saved["rage spell"]
	if e.Type != "lines" {
		t.Fatalf("committed type = %q, want \"lines\" for a spell sub-line split", e.Type)
	}
	if len(e.Lines) != 2 || e.Lines[0].Count != 3 || e.Lines[1].Count != 2 {
		t.Fatalf("rage split = %+v, want counts 3 and 2", e.Lines)
	}
}
