package formula

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

func TestMirrorForCorner_NilFormulaNoPanic(t *testing.T) {
	var f *Formula
	f.MirrorForCorner("BottomLeft") // must not panic
}

func TestMirrorForCorner_ZeroScreenDoesNotPanic(t *testing.T) {
	f := &Formula{
		Units: map[string]UnitEntry{
			"u": {Type: "point", P: &Point{X: 10, Y: 20}},
		},
	}
	f.MirrorForCorner("TopLeft") // w=h=0 clamps to 1
	if got := f.Units["u"].P; got.X != -10 || got.Y != -20 {
		t.Logf("zero-screen mirror produced %v (degenerate but must not panic)", got)
	}
}

func TestMirrorForCorner_LinesMirroredAllCorners(t *testing.T) {
	cases := map[string]Point{
		"BottomRight": {X: 453, Y: 535},
		"BottomLeft":  {X: 407, Y: 535},
		"TopRight":    {X: 453, Y: 197},
		"TopLeft":     {X: 407, Y: 197},
	}
	for corner, wantP1 := range cases {
		f := &Formula{
			Screen: ScreenSize{W: 860, H: 732},
			Units: map[string]UnitEntry{
				"rage spell": {
					Type: "lines",
					Lines: []LinePoint{
						{P1: Point{X: 453, Y: 535}, P2: Point{X: 678, Y: 363}, Count: 3},
						{P1: Point{X: 452, Y: 452}, P2: Point{X: 588, Y: 344}, Count: 2},
					},
				},
			},
		}
		f.MirrorForCorner(corner)
		got := f.Units["rage spell"].Lines[0].P1
		if got != wantP1 {
			t.Errorf("corner %s: lines[0].p1 = %v, want %v", corner, got, wantP1)
		}
		// Counts must survive mirroring untouched.
		if f.Units["rage spell"].Lines[0].Count != 3 || f.Units["rage spell"].Lines[1].Count != 2 {
			t.Errorf("corner %s: line counts changed by mirror", corner)
		}
	}
}

func TestMirrorForCorner_AbbreviatedAndFreeformCorners(t *testing.T) {
	mk := func() *Formula {
		return &Formula{
			Screen: ScreenSize{W: 100, H: 100},
			Units:  map[string]UnitEntry{"u": {Type: "point", P: &Point{X: 30, Y: 40}}},
		}
	}
	// Abbreviated forms behave like canonical.
	f := mk()
	f.MirrorForCorner("BL")
	if got := f.Units["u"].P; got.X != 70 || got.Y != 40 {
		t.Errorf("BL: got %v", got)
	}
	// Freeform "left" mirrors X only.
	f = mk()
	f.MirrorForCorner("left")
	if got := f.Units["u"].P; got.X != 70 || got.Y != 40 {
		t.Errorf("left: got %v", got)
	}
	// Freeform "top" mirrors Y only.
	f = mk()
	f.MirrorForCorner("top")
	if got := f.Units["u"].P; got.X != 30 || got.Y != 60 {
		t.Errorf("top: got %v", got)
	}
}

func TestApplyScreenScale_ScalesAllGeometry(t *testing.T) {
	f := &Formula{
		Screen: ScreenSize{W: 100, H: 100},
		Units: map[string]UnitEntry{
			"point": {Type: "point", P: &Point{X: 50, Y: 60}},
			"line":  {Type: "line", P1: &Point{X: 10, Y: 20}, P2: &Point{X: 30, Y: 40}},
			"lines": {Type: "lines", Lines: []LinePoint{{P1: Point{X: 5, Y: 5}, P2: Point{X: 15, Y: 15}}}},
		},
	}
	f.ApplyScreenScale(100, 100, 200, 300) // 2x, 3x

	if got := f.Units["point"].P; got.X != 100 || got.Y != 180 {
		t.Errorf("point scaled to %v, want (100,180)", got)
	}
	if got := f.Units["line"].P1; got.X != 20 || got.Y != 60 {
		t.Errorf("line.p1 scaled to %v, want (20,60)", got)
	}
	if got := f.Units["lines"].Lines[0].P2; got.X != 30 || got.Y != 45 {
		t.Errorf("lines[0].p2 scaled to %v, want (30,45)", got)
	}
}

// The deploy path projects authored coordinates onto the live screen with the
// device's display scale about the frame centre. These tests pin the two
// properties that make it the right model: the centre is invariant, and the
// scale is the same on both axes (which is what the framebuffer-fill factors
// this replaced got wrong, drifting authored taps 19-76 px at 1280x720).
func TestProjectUniform_CentreIsInvariantAndScaleIsUniform(t *testing.T) {
	f := &Formula{
		Screen: ScreenSize{W: 860, H: 732},
		Units: map[string]UnitEntry{
			"centre": {Type: "point", P: &Point{X: 430, Y: 366}},
			"right":  {Type: "point", P: &Point{X: 630, Y: 366}},
			"below":  {Type: "point", P: &Point{X: 430, Y: 566}},
		},
	}
	f.ProjectUniform(1.325, 860, 732, 1280, 720)

	if got := f.Units["centre"].P; got.X != 640 || got.Y != 360 {
		t.Errorf("the authored centre must land on the live centre, got %v", got)
	}
	// 200 px to the right of the authored centre, at k=1.325, is 265 px to the
	// right of the live centre.
	if got := f.Units["right"].P; got.X != 905 || got.Y != 360 {
		t.Errorf("right probe projected to %v, want (905,360)", got)
	}
	if got := f.Units["below"].P; got.X != 640 || got.Y != 625 {
		t.Errorf("below probe projected to %v, want (640,625)", got)
	}

	// Uniformity: equal authored offsets must stay equal after projection.
	dx := f.Units["right"].P.X - f.Units["centre"].P.X
	dy := f.Units["below"].P.Y - f.Units["centre"].P.Y
	if dx != dy {
		t.Errorf("projection is anisotropic: dx=%d dy=%d for equal authored offsets", dx, dy)
	}
}

func TestProjectUniform_LeavesLiveSizedFormulasAlone(t *testing.T) {
	// cmd/design_attack stamps formulas with the live screen size. Re-scaling
	// those by the device's k would multiply them a second time.
	f := &Formula{
		Screen: ScreenSize{W: 1280, H: 720},
		Units:  map[string]UnitEntry{"u": {Type: "point", P: &Point{X: 900, Y: 500}}},
	}
	f.ProjectUniform(1.325, 1280, 720, 1280, 720)
	if got := f.Units["u"].P; got.X != 900 || got.Y != 500 {
		t.Errorf("a live-sized formula must not be projected again: %v", got)
	}
}

func TestProjectUniform_ScalesEveryGeometryAndRejectsDegenerateInput(t *testing.T) {
	f := &Formula{
		Screen: ScreenSize{W: 860, H: 732},
		Units: map[string]UnitEntry{
			"point": {Type: "point", P: &Point{X: 430, Y: 366}},
			"line":  {Type: "line", P1: &Point{X: 430, Y: 366}, P2: &Point{X: 530, Y: 466}},
			"lines": {Type: "lines", Lines: []LinePoint{{P1: Point{X: 430, Y: 366}, P2: Point{X: 330, Y: 266}}}},
		},
	}
	f.ProjectUniform(2, 860, 732, 1280, 720)
	if got := f.Units["line"].P2; got.X != 840 || got.Y != 560 {
		t.Errorf("line.p2 projected to %v, want (840,560)", got)
	}
	if got := f.Units["lines"].Lines[0].P2; got.X != 440 || got.Y != 160 {
		t.Errorf("lines[0].p2 projected to %v, want (440,160)", got)
	}

	var nilFormula *Formula
	nilFormula.ProjectUniform(1.325, 860, 732, 1280, 720) // must not panic

	degenerate := []struct {
		name           string
		k              float64
		sw, sh, dw, dh int
	}{
		{"no scale", 0, 860, 732, 1280, 720},
		{"negative scale", -1, 860, 732, 1280, 720},
		{"no source width", 1.325, 0, 732, 1280, 720},
		{"no destination width", 1.325, 860, 732, 0, 720},
	}
	for _, bad := range degenerate {
		fresh := &Formula{Screen: ScreenSize{W: 860, H: 732},
			Units: map[string]UnitEntry{"u": {Type: "point", P: &Point{X: 1, Y: 1}}}}
		fresh.ProjectUniform(bad.k, bad.sw, bad.sh, bad.dw, bad.dh)
		if got := fresh.Units["u"].P; got.X != 1 || got.Y != 1 {
			t.Errorf("%s: degenerate projection mutated units: %v", bad.name, got)
		}
	}
}

// A correctly projected reference point can still land under the troop bar at
// 720p: an authored y of 580 projects to 645 on a 720px screen, past the bar at
// ~624, where a tap hits the HUD and the unit never deploys. Clamping keeps the
// unit in play and reports the count so the caller can tell the reader to
// re-author the point.
func TestClampY_PullsPointsIntoTheBand(t *testing.T) {
	f := &Formula{
		Screen: ScreenSize{W: 860, H: 732},
		Units: map[string]UnitEntry{
			"below":  {Type: "point", P: &Point{X: 10, Y: 645}},
			"inside": {Type: "point", P: &Point{X: 10, Y: 300}},
			"line":   {Type: "line", P1: &Point{X: 0, Y: 700}, P2: &Point{X: 0, Y: 400}},
			"lines":  {Type: "lines", Lines: []LinePoint{{P1: Point{X: 0, Y: 650}, P2: Point{X: 0, Y: 100}}}},
		},
	}
	if got := f.ClampY(0, 612); got != 3 {
		t.Errorf("ClampY reported %d clamped points, want 3", got)
	}
	if got := f.Units["below"].P; got.Y != 612 {
		t.Errorf("below band: y=%d, want 612", got.Y)
	}
	if got := f.Units["inside"].P; got.Y != 300 {
		t.Errorf("in band must not move: y=%d, want 300", got.Y)
	}
	if got := f.Units["line"].P1; got.Y != 612 {
		t.Errorf("line.p1: y=%d, want 612", got.Y)
	}
	if got := f.Units["lines"].Lines[0].P1; got.Y != 612 {
		t.Errorf("lines[0].p1: y=%d, want 612", got.Y)
	}
	// X must be untouched: the band is a horizontal cut, not a crop.
	if got := f.Units["below"].P; got.X != 10 {
		t.Errorf("clamping moved x: %d", got.X)
	}

	// A degenerate band must not collapse every point onto one row.
	if got := f.ClampY(700, 600); got != 0 {
		t.Errorf("degenerate band clamped %d points, want 0", got)
	}
	if got := f.Units["line"].P1; got.Y != 612 {
		t.Errorf("degenerate band moved a point: y=%d", got.Y)
	}

	var nilFormula *Formula
	nilFormula.ClampY(0, 100) // must not panic
}

// The red no-deploy ring is a property of the map, so a formula point in the
// outer band of the view is a tap the game refuses with "You cannot deploy
// troops on the Red area!" — and the bot's tap call still reports success, so
// the unit silently stays in the bar. Live 2026-09-30: red detection reported no
// boundary, so nothing pulled the formula's own ground inward and every tap was
// refused. TranslateIntoRect is the fallback that does not need the boundary.
//
// attack.SafeGroundRect(1280, 720, 612) at the shipped 20% inset is
// x 256..1024 and y 226..516, both half-open, i.e. a legal x of 256..1023.
func TestTranslateIntoRect_PullsFormulaIntoCentralGround(t *testing.T) {
	const (
		minX, maxX = 256, 1024
		minY, maxY = 226, 516
	)
	// Every coordinate sits in the view's outer band — x from 60 to 1200 against
	// a legal 256..1023, y from 150 to 600 against a legal 226..515 — but the
	// span itself FITS the box, so one offset per axis is enough to bring the
	// whole formula in. (A span too wide is the separate centring case below.)
	f := &Formula{
		Screen: ScreenSize{W: 1280, H: 720},
		Units: map[string]UnitEntry{
			"right_edge": {Type: "point", P: &Point{X: 1200, Y: 400}},
			"left_edge":  {Type: "point", P: &Point{X: 450, Y: 300}},
			"under_bar":  {Type: "point", P: &Point{X: 600, Y: 600}},
			"line":       {Type: "line", P1: &Point{X: 1150, Y: 350}, P2: &Point{X: 900, Y: 550}},
			"lines":      {Type: "lines", Lines: []LinePoint{{P1: Point{X: 500, Y: 200}, P2: Point{X: 1150, Y: 480}}}},
		},
	}
	// Snapshot every coordinate, so the assertion below is about a shared OFFSET
	// and not merely about the bounds — a per-point clamp passes every
	// inside-the-box check while destroying the shape.
	type coord struct {
		x, y int
	}
	// Sorted unit names: map iteration order is randomized, so the snapshot and
	// the walk below have to agree on an order or the comparison is noise.
	names := make([]string, 0, len(f.Units))
	for name := range f.Units {
		names = append(names, name)
	}
	sort.Strings(names)

	var before []coord
	for _, name := range names {
		u := f.Units[name]
		for _, p := range []*Point{u.P, u.P1, u.P2} {
			if p != nil {
				before = append(before, coord{p.X, p.Y})
			}
		}
		for i := range u.Lines {
			for _, p := range []*Point{&u.Lines[i].P1, &u.Lines[i].P2} {
				before = append(before, coord{p.X, p.Y})
			}
		}
	}

	if got := f.TranslateIntoRect(minX, maxX, minY, maxY); got != len(before) {
		t.Errorf("TranslateIntoRect reported %d moved coordinates, want %d", got, len(before))
	}

	// One offset per axis, independently. x spans 450..1200 (750 wide) against a
	// legal 256..1023 (767 wide), so it FITS and takes the smallest offset that
	// brings the far end in: 1023-1200 = -177. y spans 200..600 (400 wide)
	// against a legal 226..515 (290 wide), so it cannot fit and is centred
	// instead. Both are correct; a clamp would silently collapse each point onto
	// the box edge and satisfy neither.
	const (
		wantDX = 1023 - 1200
		wantDY = (226+515)/2 - (200+600)/2
	)
	i := 0
	for _, name := range names {
		u := f.Units[name]
		all := []*Point{u.P, u.P1, u.P2}
		for j := range u.Lines {
			all = append(all, &u.Lines[j].P1, &u.Lines[j].P2)
		}
		for _, p := range all {
			if p == nil {
				continue
			}
			if p.X != before[i].x+wantDX || p.Y != before[i].y+wantDY {
				t.Errorf("%s (%d,%d) -> (%d,%d), want one shared offset (dx=%d dy=%d)",
					name, before[i].x, before[i].y, p.X, p.Y, wantDX, wantDY)
			}
			i++
		}
	}
	if i != len(before) {
		t.Errorf("walked %d coordinates but snapshotted %d", i, len(before))
	}

	// A degenerate rect must not collapse every coordinate onto one spot.
	beforeDegenerate := *f.Units["line"].P1
	if got := f.TranslateIntoRect(0, -1, 0, 100); got != 0 {
		t.Errorf("degenerate rect moved %d coordinates, want 0", got)
	}
	if got := *f.Units["line"].P1; got != beforeDegenerate {
		t.Errorf("degenerate rect moved a coordinate: %v -> %v", beforeDegenerate, got)
	}

	var nilFormula *Formula
	nilFormula.TranslateIntoRect(0, 10, 0, 10) // must not panic
	empty := &Formula{Units: map[string]UnitEntry{}}
	if got := empty.TranslateIntoRect(0, 10, 0, 10); got != 0 {
		t.Errorf("a formula with no coordinates reported %d moves", got)
	}
}

// The exact live regression. `Auto EDrag Rush` at 1280x720 puts every line unit
// and hero on the 85% column x=1088, and the central ground's last legal column
// is 1023. A per-point clamp folded thirteen taps onto the single column x=1024
// — a plan that is legal on paper and deploys the whole army on one tile line.
// The translation must move that column to 1023 and leave it a column.
func TestTranslateIntoRect_KeepsAFormulaColumnAColumn(t *testing.T) {
	// The live auto-picked ground for target BottomRight: line troops and heroes
	// as a vertical column of taps at x=1088 spanning y 144..576.
	f := &Formula{
		Screen: ScreenSize{W: 1280, H: 720},
		Units: map[string]UnitEntry{
			"balloon": {Type: "line", P1: &Point{X: 1088, Y: 144}, P2: &Point{X: 1088, Y: 576}, Count: 10},
			"dragon":  {Type: "point", P: &Point{X: 1088, Y: 504}},
			"archer":  {Type: "point", P: &Point{X: 1088, Y: 288}},
		},
	}
	// x span 1088..1088 fits; y span 144..576 is 432 wide against a legal
	// 226..515, so it is centred — see the note below on why that is the honest
	// outcome rather than a clamp.
	const (
		minX, maxX = 256, 1024
		minY, maxY = 226, 516
	)
	if got := f.TranslateIntoRect(minX, maxX, minY, maxY); got != 4 {
		t.Errorf("moved %d coordinates, want 4", got)
	}

	// Every tap that was on the 85% column is now on the last LEGAL column. A
	// per-point clamp would report 1024 here, which fails image.Rectangle.In.
	for name, u := range f.Units {
		for _, p := range []*Point{u.P, u.P1, u.P2} {
			if p != nil && p.X != 1023 {
				t.Errorf("%s x=%d, want 1023: the column must move onto the last legal column", name, p.X)
			}
		}
	}

	// The column is still a column: every point shares one x, and the line's
	// length and direction survive. A clamp folds points onto one x too, so the
	// load-bearing assertion is the length.
	if got := f.Units["balloon"].P2.X - f.Units["balloon"].P1.X; got != 0 {
		t.Errorf("the column lost its vertical alignment: dx=%d, want 0", got)
	}
	if got, want := f.Units["balloon"].P2.Y-f.Units["balloon"].P1.Y, 576-144; got != want {
		t.Errorf("balloon line length changed: %d, want %d", got, want)
	}
	// y is centred: the 144..576 span is 432 wide against a legal 226..515
	// (290 wide), so it cannot fit and is recentred on the box middle — dy=+10.
	if got, want := f.Units["balloon"].P1.Y, 144+((226+515)/2-(144+576)/2); got != want {
		t.Errorf("balloon P1 y=%d, want %d (the over-long span recentred)", got, want)
	}
}

// A formula whose span is WIDER than the central ground cannot be made to fit by
// any offset. The honest outcome is to centre it — and to report the offsets, so
// the caller can say the line no longer fits rather than let a silently broken
// plan pass for the authored one.
func TestTranslateIntoRect_CentresAnOversizedFormula(t *testing.T) {
	const (
		minX, maxX = 256, 1024
		minY, maxY = 226, 516
	)
	f := &Formula{
		Screen: ScreenSize{W: 1280, H: 720},
		Units: map[string]UnitEntry{
			"wide": {Type: "line", P1: &Point{X: -200, Y: 250}, P2: &Point{X: 1500, Y: 480}},
		},
	}
	if got := f.TranslateIntoRect(minX, maxX, minY, maxY); got != 2 {
		t.Errorf("moved %d coordinates, want 2", got)
	}
	// The MIDPOINT lands in the middle of the box even though the ends still
	// overhang — that is the part that helps, and the rest is why the caller logs.
	midX := (f.Units["wide"].P1.X + f.Units["wide"].P2.X) / 2
	wantMidX := (minX + maxX - 1) / 2
	if diff := midX - wantMidX; diff > 1 || diff < -1 {
		t.Errorf("midpoint x=%d, want the middle of the central ground %d (within 1px)", midX, wantMidX)
	}
	// Length survives, which clamping would destroy outright.
	if got, want := f.Units["wide"].P2.X-f.Units["wide"].P1.X, 1500-(-200); got != want {
		t.Errorf("line length changed: %d, want %d", got, want)
	}
}

// ClampY runs first and TranslateIntoRect second, so the stricter bound wins: a
// point the band clamp rescued must still be pulled out of the outer band.
func TestTranslateIntoRect_AfterClampYIsTheStricterBound(t *testing.T) {
	f := &Formula{
		Screen: ScreenSize{W: 1280, H: 720},
		Units:  map[string]UnitEntry{"u": {Type: "point", P: &Point{X: 1200, Y: 700}}},
	}
	f.ClampY(130, 612) // band only: x is not this clamp's business
	if got := f.Units["u"].P; got.Y != 612 {
		t.Fatalf("ClampY put y=%d, want 612", got.Y)
	}
	f.TranslateIntoRect(256, 1024, 226, 516)
	if got := f.Units["u"].P; got.X != 1023 || got.Y != 515 {
		t.Errorf("after both, (%d,%d), want (1023,515): one offset onto the last legal coordinate", got.X, got.Y)
	}
}

// A formula already inside the box must come out byte-identical: the fallback
// exists for the outer band, and re-aiming every good plan would be a worse bug
// than the one being fixed.
func TestTranslateIntoRect_LeavesACentralFormulaUntouched(t *testing.T) {
	f := &Formula{
		Screen: ScreenSize{W: 1280, H: 720},
		Units: map[string]UnitEntry{
			"a": {Type: "line", P1: &Point{X: 500, Y: 300}, P2: &Point{X: 740, Y: 420}},
			"b": {Type: "point", P: &Point{X: 640, Y: 360}},
		},
	}
	if got := f.TranslateIntoRect(256, 1024, 226, 516); got != 0 {
		t.Errorf("moved %d coordinates for a formula already inside the box, want 0", got)
	}
	if got := *f.Units["a"].P1; got != (Point{X: 500, Y: 300}) {
		t.Errorf("a.P1 = %v, want (500,300)", got)
	}
	if got := *f.Units["b"].P; got != (Point{X: 640, Y: 360}) {
		t.Errorf("b.P = %v, want (640,360)", got)
	}
}

func TestApplyScreenScale_DegenerateParamsNoPanic(t *testing.T) {
	var f *Formula
	f.ApplyScreenScale(0, 0, 0, 0) // nil receiver must not panic

	f2 := &Formula{Units: map[string]UnitEntry{"u": {Type: "point", P: &Point{X: 1, Y: 1}}}}
	f2.ApplyScreenScale(0, 100, 200, 300) // srcW=0 → no-op
	if got := f2.Units["u"].P; got.X != 1 || got.Y != 1 {
		t.Errorf("degenerate scale mutated units: %v", got)
	}
}

func TestApplyScreenScale_MirrorThenScaleMatchesDirectAuthoring(t *testing.T) {
	// Authoring on BR then mirroring to TL then scaling to the live screen
	// must equal authoring the TL coordinates directly and scaling.
	scale := func(x, y int) (int, int) { return x * 2, y * 2 }

	br := &Formula{
		Screen: ScreenSize{W: 860, H: 732},
		Units: map[string]UnitEntry{
			"ice spell": {Type: "line", P1: &Point{X: 480, Y: 429}, P2: &Point{X: 537, Y: 391}},
		},
	}
	br.MirrorForCorner("TopLeft")
	br.ApplyScreenScale(860, 732, 1720, 1464)

	direct := &Formula{
		Screen: ScreenSize{W: 860, H: 732},
		Units: map[string]UnitEntry{
			"ice spell": {Type: "line", P1: &Point{X: 380, Y: 303}, P2: &Point{X: 323, Y: 341}},
		},
	}
	direct.ApplyScreenScale(860, 732, 1720, 1464)

	if *br.Units["ice spell"].P1 != *direct.Units["ice spell"].P1 {
		t.Errorf("mirror+scale p1 = %v, direct = %v", *br.Units["ice spell"].P1, *direct.Units["ice spell"].P1)
	}
	if wantX, wantY := scale(380, 303); direct.Units["ice spell"].P1.X != wantX || direct.Units["ice spell"].P1.Y != wantY {
		t.Errorf("scale sanity: got (%d,%d), want (%d,%d)", direct.Units["ice spell"].P1.X, direct.Units["ice spell"].P1.Y, wantX, wantY)
	}
}

func TestLookUp_CaseAndUnderscoreNormalization(t *testing.T) {
	f := &Formula{
		Units: map[string]UnitEntry{
			"rage spell": {Type: "line", P1: &Point{X: 1, Y: 2}, P2: &Point{X: 3, Y: 4}},
		},
	}
	if _, ok := f.LookUp("Rage Spell"); !ok {
		t.Error("LookUp should be case-insensitive")
	}
	if _, ok := f.LookUp("  rage spell  "); !ok {
		t.Error("LookUp should trim whitespace")
	}
	if _, ok := f.LookUp("rage_spell"); !ok {
		t.Error("LookUp should normalize underscores to spaces")
	}
	if _, ok := f.LookUp("ice spell"); ok {
		t.Error("LookUp should not match absent units")
	}
	var nilF *Formula
	if _, ok := nilF.LookUp("rage spell"); ok {
		t.Error("nil formula LookUp must return false")
	}
}

func TestTypeInference(t *testing.T) {
	// Type discriminator inferred from populated fields when Type is empty.
	cases := []struct {
		entry UnitEntry
		point bool
		line  bool
		lines bool
	}{
		{UnitEntry{Type: "point", P: &Point{}}, true, false, false},
		{UnitEntry{Type: "line", P1: &Point{}, P2: &Point{}}, false, true, false},
		{UnitEntry{Lines: []LinePoint{{Count: 1}}}, false, false, true},
		{UnitEntry{P: &Point{}, P1: &Point{}}, false, false, false}, // ambiguous: P + P1 → not inferred
		{UnitEntry{}, false, false, false},
	}
	for i, tc := range cases {
		if got := tc.entry.IsPoint(); got != tc.point {
			t.Errorf("case %d: IsPoint()=%v, want %v", i, got, tc.point)
		}
		if got := tc.entry.IsLine(); got != tc.line {
			t.Errorf("case %d: IsLine()=%v, want %v", i, got, tc.line)
		}
		if got := tc.entry.IsLines(); got != tc.lines {
			t.Errorf("case %d: IsLines()=%v, want %v", i, got, tc.lines)
		}
	}
}

func TestLoadFile_RoundTripPreservesGeometry(t *testing.T) {
	original := Formula{
		Name:   "roundtrip",
		Screen: ScreenSize{W: 860, H: 732},
		Units: map[string]UnitEntry{
			"rage spell": {Type: "line", P1: &Point{X: 453, Y: 535}, P2: &Point{X: 678, Y: 363}, Jitter: 3},
			"heroes":     {Type: "point", P: &Point{X: 636, Y: 510}, Jitter: 5},
		},
		CornerOverrides: map[string]map[string]UnitEntry{
			"TopLeft": {"rage spell": {Type: "line", P1: &Point{X: 125, Y: 337}, P2: &Point{X: 420, Y: 132}}},
		},
	}

	path := filepath.Join(t.TempDir(), "formula.json")
	if err := original.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	loaded, err := LoadFile(path)
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}

	rawWant, _ := json.Marshal(original)
	rawGot, _ := json.Marshal(*loaded)
	if string(rawWant) != string(rawGot) {
		t.Errorf("round trip changed the formula:\nwant %s\ngot  %s", rawWant, rawGot)
	}
}

func TestLoadFile_EmptyUnitsNormalizedToMap(t *testing.T) {
	path := filepath.Join(t.TempDir(), "formula.json")
	if err := os.WriteFile(path, []byte(`{"name":"x","screen":{"w":860,"h":732}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := LoadFile(path)
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	if f.Units == nil {
		t.Fatal("LoadFile must normalize nil units to an empty map")
	}
}

func TestSave_NilFormulaErrors(t *testing.T) {
	var f *Formula
	if err := f.Save(filepath.Join(t.TempDir(), "x.json")); err == nil {
		t.Fatal("Save on nil formula should error")
	}
}

func TestShippedFormulaFile_MirrorRoundTripsEveryUnit(t *testing.T) {
	// The real shipped file: mirror to every corner and back must restore
	// the exact original JSON — catches any aliasing/mutation bug in the
	// wild data, not just synthetic fixtures.
	for _, p := range []string{"../../assets/strategies/auto_edrag_rush_formula.json", "assets/strategies/auto_edrag_rush_formula.json"} {
		raw, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var original Formula
		if err := json.Unmarshal(raw, &original); err != nil {
			t.Fatalf("parse shipped formula: %v", err)
		}
		for _, corner := range []string{"BottomRight", "BottomLeft", "TopRight", "TopLeft"} {
			f := original // struct copy; shared *Point fields exercise aliasing
			f.MirrorForCorner(corner)
			f.MirrorForCorner(corner) // back
			got, _ := json.Marshal(f.Units)
			want, _ := json.Marshal(original.Units)
			if string(got) != string(want) {
				t.Errorf("corner %s: double mirror did not restore shipped units", corner)
			}
		}
		return
	}
	t.Skip("shipped formula not found from test cwd")
}

// checkShift asserts a coordinate landed exactly where one shared offset put it.
func checkShift(t *testing.T, name string, p *Point, dx, dy int) {
	t.Helper()
	if p == nil {
		return
	}
	if p.X != p.X-dx || p.Y != p.Y-dy {
		t.Errorf("%s (%d,%d): every coordinate must share the one offset (dx=%d dy=%d)", name, p.X, p.Y, dx, dy)
	}
}
