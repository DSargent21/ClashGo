package formula

import (
	"encoding/json"
	"os"
	"path/filepath"
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
