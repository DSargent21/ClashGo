package attack

import (
	"image"
	"testing"

	"github.com/Ducky705/ClashGO/pkg/formula"
	"github.com/Ducky705/ClashGO/pkg/strategy"
	"github.com/rs/zerolog"
	"gocv.io/x/gocv"
)

// A Valkyrie ring is the one deploy shape a single line cannot express: the same
// card lands on all four sides of the base in the same battle. It arrives as a
// formula entry of `"type": "sides"` (drawn one line per side with
// cmd/design_attack), and the corner mirror deliberately leaves it alone because
// the user already drew every side.
//
// These tests pin the two things that made the ring a no-op before it existed:
// the tap pass must reach EVERY authored side (not sweep the first one), and the
// authored geometry must be what is tapped (not a fallback edge line).
func TestDeployTroops_SidesEntryDeploysOnAllFourSides(t *testing.T) {
	exec, probe := newTapProbe(t)

	ring := ringEntry()
	f := &formula.Formula{
		Screen: formula.ScreenSize{W: 1280, H: 720},
		Units:  map[string]formula.UnitEntry{"valkyrie": ring},
	}
	hm := NewHeroManager(exec, &SlotManager{}, PrecisionConfig{}, "BottomRight", 1280, 720,
		f, nil, DeployLine{}, zerolog.Nop())
	slot := &TrackedSlot{TroopSlot: TroopSlot{X: 200, Y: 682, Category: "Troop"}, UnitName: "valkyrie"}

	// detectedTrusted=true is the orchestrator's settled battle-start read: the
	// card holds 8, so the main pass fires a measured 8 (+ the tail pad) and no
	// tap is a blind guess.
	hm.DeployTroops(strategy.Unit{Name: "Valkyrie", Amount: "All"}, slot, "FourSides", 0, "FourSides",
		gocv.NewMat(), 8, true)

	pts := probe.fields()
	if len(pts) == 0 {
		t.Fatal("a sides entry fired no field taps at all — the ring never deployed")
	}

	sides := []struct {
		name string
		hit  func(image.Point) bool
	}{
		{"top", func(p image.Point) bool { return nearAxis(p.Y, 150, 30) && p.X >= 170 && p.X <= 1110 }},
		{"right", func(p image.Point) bool { return nearAxis(p.X, 1080, 30) && p.Y >= 120 && p.Y <= 590 }},
		{"bottom", func(p image.Point) bool { return nearAxis(p.Y, 560, 30) && p.X >= 170 && p.X <= 1110 }},
		{"left", func(p image.Point) bool { return nearAxis(p.X, 200, 30) && p.Y >= 120 && p.Y <= 590 }},
	}
	for _, side := range sides {
		if !anyPoint(pts, side.hit) {
			t.Errorf("no tap landed on the %s side of the ring — the card was not spread around the base (taps: %v)", side.name, pts)
		}
	}

	// Every tap must sit on the ring the user drew. A tap far off the ring means
	// the deploy fell back to a legacy edge line and the authored geometry was
	// ignored — the report this whole change set exists to answer.
	for _, p := range pts {
		if d := distToRing(ring, p); d > 30 {
			t.Errorf("tap %v is %.0fpx from the authored ring; the four sides are x=200, x=1080, y=150, y=560 so this tap is off-plan", p, d)
		}
	}
}

// The ring must win over the legacy FourSides edge spam. That path fires
// countPerSide taps at every corner's pCfg edge without ever looking at the
// formula, so a regression there silently replaces a drawn ring with a
// red-zone-derived one — the exact "my lines did nothing" report.
func TestDeployTroops_SidesEntryBeatsLegacyFourSidesEdges(t *testing.T) {
	exec, probe := newTapProbe(t)

	// A legacy edge nowhere near the ring, so a tap on it is unmistakable.
	legacy := ManualEdge{P1: image.Pt(640, 640), P2: image.Pt(660, 640)}
	pCfg := PrecisionConfig{
		Edges: map[string]ManualEdge{
			"TopLeft": legacy, "TopRight": legacy,
			"BottomLeft": legacy, "BottomRight": legacy,
		},
		Width: 1280, Height: 720,
	}
	f := &formula.Formula{
		Screen: formula.ScreenSize{W: 1280, H: 720},
		Units:  map[string]formula.UnitEntry{"valkyrie": ringEntry()},
	}
	hm := NewHeroManager(exec, &SlotManager{}, pCfg, "BottomRight", 1280, 720,
		f, nil, DeployLine{}, zerolog.Nop())
	slot := &TrackedSlot{TroopSlot: TroopSlot{X: 200, Y: 682, Category: "Troop"}, UnitName: "valkyrie"}

	hm.DeployTroops(strategy.Unit{Name: "Valkyrie", Amount: "All"}, slot, "FourSides", 0, "FourSides",
		gocv.NewMat(), 8, true)

	for _, p := range probe.fields() {
		if nearAxis(p.Y, 640, 20) && p.X > 600 && p.X < 700 {
			t.Fatalf("a tap landed on the legacy FourSides edge (640,640) instead of the authored ring: %v", p)
		}
	}
}

// A legacy precision_config pin must not swallow a drawn ring. The pins are a
// per-corner LINE placed with cmd/pick_coords; a ring is four sides, which one
// pin cannot express, so letting the pin win made the ring unreachable for any
// user who had ever pinned a corner — with no error to explain it.
func TestDeployTroops_SidesEntryBeatsAPinnedGround(t *testing.T) {
	exec, probe := newTapProbe(t)
	f := &formula.Formula{
		Screen: formula.ScreenSize{W: 1280, H: 720},
		Units:  map[string]formula.UnitEntry{"valkyrie": ringEntry()},
	}
	// The old pinned ground: one line, nowhere near the ring, so a tap on it is
	// unmistakable.
	pin := DeployLine{Points: []image.Point{image.Pt(640, 640), image.Pt(660, 640)}, Side: "BottomRight"}
	hm := NewHeroManager(exec, &SlotManager{}, PrecisionConfig{Width: 1280, Height: 720}, "BottomRight",
		1280, 720, f, nil, pin, zerolog.Nop())
	hm.SetPinnedGround(true)
	slot := &TrackedSlot{TroopSlot: TroopSlot{X: 200, Y: 682, Category: "Troop"}, UnitName: "valkyrie"}

	hm.DeployTroops(strategy.Unit{Name: "Valkyrie", Amount: "All"}, slot, "FourSides", 0, "FourSides",
		gocv.NewMat(), 8, true)

	pts := probe.fields()
	if len(pts) == 0 {
		t.Fatal("the pinned ground suppressed the ring entirely; no field taps fired")
	}
	for _, p := range pts {
		if nearAxis(p.Y, 640, 20) && p.X > 600 && p.X < 700 {
			t.Fatalf("tap %v landed on the legacy pinned line instead of the authored ring", p)
		}
		if d := distToRing(ringEntry(), p); d > 30 {
			t.Errorf("tap %v is %.0fpx from the authored ring", p, d)
		}
	}
}

// ringEntry is the shipped valk ring's shape (assets/strategies/valk_spam_formula.json).
func ringEntry() formula.UnitEntry {
	pt := func(x, y int) formula.Point { return formula.Point{X: x, Y: y} }
	return formula.UnitEntry{
		Type: "sides",
		Lines: []formula.LinePoint{
			{P1: pt(200, 150), P2: pt(1080, 150)}, // top
			{P1: pt(1080, 150), P2: pt(1080, 560)}, // right
			{P1: pt(1080, 560), P2: pt(200, 560)}, // bottom
			{P1: pt(200, 560), P2: pt(200, 150)},  // left
		},
	}
}

func nearAxis(got, want, tol int) bool {
	d := got - want
	return d <= tol && d >= -tol
}

func anyPoint(pts []image.Point, pred func(image.Point) bool) bool {
	for _, p := range pts {
		if pred(p) {
			return true
		}
	}
	return false
}

func distToRing(entry formula.UnitEntry, p image.Point) float64 {
	best := -1.0
	for _, lp := range entry.Lines {
		d := distToSegment(p, lp.P1.Image(), lp.P2.Image())
		if best < 0 || d < best {
			best = d
		}
	}
	if best < 0 {
		return 0
	}
	return best
}
