package attack

import (
	"testing"

	"github.com/Ducky705/ClashGO/pkg/formula"
	"github.com/rs/zerolog"
)

// A formula may carry its own `tap_band` — the y-range a tap reaches on the
// screen it was drawn on (tools/line_picker.html). It exists because the
// built-in yTopMin..uiCutoff is one guess at where the game's chrome ends,
// applied to every device and zoom; on a live 720p frame the field is clear well
// above yTopMin in the centre, so a line drawn near the top of the base was
// silently clamped ~100px down onto it.
//
// The rules these pin are the two ways honouring a band goes wrong: applying a
// band written for a different frame, and applying one that is not a range at
// all. Both must fall back to the built-in band rather than clamp live points to
// rows nobody measured.

func bandFormula(w, h int, band *formula.TapBand) *formula.Formula {
	return &formula.Formula{
		Screen:  formula.ScreenSize{W: w, H: h},
		TapBand: band,
		Units:   map[string]formula.UnitEntry{},
	}
}

func TestFormulaBand_NoBandUsesTheBuiltInRange(t *testing.T) {
	for _, f := range []*formula.Formula{nil, bandFormula(1280, 720, nil)} {
		top, bottom := FormulaBand(f, 1280, 720, 612, zerolog.Nop())
		if top != yTopMin || bottom != 612 {
			t.Errorf("no tap_band must use the built-in band %d..%d, got %d..%d", yTopMin, 612, top, bottom)
		}
	}
}

func TestFormulaBand_HonoursABandDrawnOnTheLiveFrame(t *testing.T) {
	f := bandFormula(1280, 720, &formula.TapBand{YTop: 61, YBottom: 599})
	top, bottom := FormulaBand(f, 1280, 720, 612, zerolog.Nop())
	if top != 61 || bottom != 599 {
		t.Fatalf("band = %d..%d, want the authored 61..599 — the user measured this screen", top, bottom)
	}
}

// The chrome a band describes is edge-anchored; ProjectUniform moves world
// coordinates about the frame centre. So a band from another geometry has no
// correct projection, and using it would clamp live points to unmeasured rows.
func TestFormulaBand_IgnoresABandDrawnOnAnotherFrame(t *testing.T) {
	f := bandFormula(860, 732, &formula.TapBand{YTop: 20, YBottom: 500})
	top, bottom := FormulaBand(f, 1280, 720, 612, zerolog.Nop())
	if top != yTopMin || bottom != 612 {
		t.Fatalf("band = %d..%d, want the built-in %d..612: the band describes 860x732, not the live frame",
			top, bottom, yTopMin)
	}
}

func TestFormulaBand_RejectsUnusableRanges(t *testing.T) {
	cases := []struct {
		name string
		band formula.TapBand
	}{
		{"empty", formula.TapBand{YTop: 300, YBottom: 300}},
		{"inverted", formula.TapBand{YTop: 500, YBottom: 200}},
		{"too thin to spread a line", formula.TapBand{YTop: 300, YBottom: 320}},
		{"negative top", formula.TapBand{YTop: -5, YBottom: 500}},
		{"past the bottom of the frame", formula.TapBand{YTop: 100, YBottom: 720}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := bandFormula(1280, 720, &tc.band)
			top, bottom := FormulaBand(f, 1280, 720, 612, zerolog.Nop())
			if top != yTopMin || bottom != 612 {
				t.Errorf("band = %d..%d, want the built-in %d..612", top, bottom, yTopMin)
			}
		})
	}
}

// A band reaching under the troop bar is the user's call, not ours: they picked
// it looking at the screenshot. Reverting it silently would deploy somewhere
// neither of you chose, so it is used and logged.
func TestFormulaBand_UsesABandThatReachesUnderTheTroopBar(t *testing.T) {
	f := bandFormula(1280, 720, &formula.TapBand{YTop: 90, YBottom: 700})
	top, bottom := FormulaBand(f, 1280, 720, 612, zerolog.Nop())
	if top != 90 || bottom != 700 {
		t.Fatalf("band = %d..%d, want the authored 90..700 even though it passes the troop bar at 612", top, bottom)
	}
}

// End to end through the clamp the orchestrator applies: with a band that
// reaches higher than the built-in one, a point the built-in clamp would have
// dragged down to yTopMin now stays where it was drawn.
func TestFormulaBand_LetsATopLineStayWhereItWasDrawn(t *testing.T) {
	const drawnY = 13
	f := bandFormula(1280, 720, &formula.TapBand{YTop: 10, YBottom: 612})
	f.Units["valkyrie"] = formula.UnitEntry{
		Type:  "sides",
		Lines: []formula.LinePoint{{P1: formula.Point{X: 493, Y: drawnY}, P2: formula.Point{X: 64, Y: 340}}},
	}

	top, bottom := FormulaBand(f, 1280, 720, 612, zerolog.Nop())
	if moved := f.ClampY(top, bottom); moved != 0 {
		t.Fatalf("ClampY moved %d point(s); a band the user drew must not pull a drawn line anywhere", moved)
	}
	if got := f.Units["valkyrie"].Lines[0].P1.Y; got != drawnY {
		t.Fatalf("line top = %d, want %d", got, drawnY)
	}

	// The same line against the built-in band is what this change exists to
	// stop: it is dragged down to yTopMin, ~100px onto the base.
	builtin := bandFormula(1280, 720, nil)
	builtin.Units["valkyrie"] = f.Units["valkyrie"]
	if moved := builtin.ClampY(yTopMin, 612); moved != 1 {
		t.Fatalf("expected the built-in band to move exactly the top point, moved %d", moved)
	}
	if got := builtin.Units["valkyrie"].Lines[0].P1.Y; got != yTopMin {
		t.Fatalf("built-in clamp put the top at %d, want %d", got, yTopMin)
	}
}
