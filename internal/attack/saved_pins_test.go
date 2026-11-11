package attack

import (
	"encoding/json"
	"image"
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/Ducky705/ClashGO/internal/paths"
)

// minPinnedLineLength is the shortest troop line worth deploying an army along.
// Below it every unit lands on a handful of tiles instead of a line; the picker
// warns at the same number (cmd/pick_coords INLAND/short-line check).
const minPinnedLineLength = 120

// TestSavedPinsDeployInsideBand proves the lines actually stored in
// assets/precision_config.json survive the path the bot runs:
//
//   - every corner carries a pin, so the deploy path takes the pinned branch for
//     all four targets instead of refusing or falling back to the formula
//   - after the loader's scaling and ClampEdgeToDeployBand, every tap the bot
//     would fire is on the field: x inside the frame, y inside
//     [BandTop, UICutoff]. A point above BandTop is under the resource HUD, and
//     one past UICutoff is under the troop bar — the game gives those taps to
//     its own chrome and the unit is never placed
//   - each line is long enough to spread a group of troops
//   - the hero point, spell line A and spell line B exist for the same corner
func TestSavedPinsDeployInsideBand(t *testing.T) {
	raw, src := readSavedPins(t)
	var cfg PrecisionConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("%s: %v", src, err)
	}
	if cfg.Width <= 0 || cfg.Height <= 0 {
		t.Fatalf("%s declares %dx%d reference geometry; the pins cannot be scaled", src, cfg.Width, cfg.Height)
	}

	// The live frame the pins were drawn on.
	const w, h = 1024, 768
	sx, sy := float64(w)/float64(cfg.Width), float64(h)/float64(cfg.Height)
	scaleEdge := func(e ManualEdge) ManualEdge {
		return ManualEdge{
			P1: image.Pt(int(float64(e.P1.X)*sx), int(float64(e.P1.Y)*sy)),
			P2: image.Pt(int(float64(e.P2.X)*sx), int(float64(e.P2.Y)*sy)),
		}
	}
	for k, v := range cfg.Edges {
		cfg.Edges[k] = scaleEdge(v)
	}
	for k, v := range cfg.SpellEdgesA {
		cfg.SpellEdgesA[k] = scaleEdge(v)
	}
	for k, v := range cfg.SpellEdgesB {
		cfg.SpellEdgesB[k] = scaleEdge(v)
	}
	for k, v := range cfg.HeroTargets {
		cfg.HeroTargets[k] = image.Pt(int(float64(v.X)*sx), int(float64(v.Y)*sy))
	}

	ui := UICutoff(h)
	top := BandTop()
	inBand := func(p image.Point) bool { return p.Y >= top && p.Y <= ui }

	for _, corner := range []string{"TopLeft", "TopRight", "BottomLeft", "BottomRight"} {
		if !hasPinnedForTarget(cfg, corner) {
			t.Errorf("%s: no usable pin, so this target does not take the pinned deploy path", corner)
			continue
		}
		edge, ok := cfg.Edges[corner]
		if !ok || isZeroManualEdge(edge) {
			t.Errorf("%s: pin missing from edges", corner)
			continue
		}

		// Exactly what the orchestrator does with a pin: clamp onto the field,
		// then interpolate the taps.
		line := manualEdgeToDeployLine(ClampEdgeToDeployBand(edge, ui), corner, linePoints)
		if len(line.Points) != linePoints {
			t.Errorf("%s: deploy line has %d points, want %d", corner, len(line.Points), linePoints)
		}
		for i, p := range line.Points {
			if p.X < 0 || p.X >= w || p.Y < 0 || p.Y >= h {
				t.Errorf("%s tap %d at (%d,%d) is off the %dx%d frame", corner, i, p.X, p.Y, w, h)
			}
			if !inBand(p) {
				where := "under the top HUD"
				if p.Y > ui {
					where = "under the troop bar"
				}
				t.Errorf("%s tap %d at (%d,%d) is %s (band is y %d..%d); the game gives that tap to its chrome, so the unit is never placed", corner, i, p.X, p.Y, where, top, ui)
			}
		}
		length := math.Hypot(float64(line.Points[len(line.Points)-1].X-line.Points[0].X),
			float64(line.Points[len(line.Points)-1].Y-line.Points[0].Y))
		if length < minPinnedLineLength {
			t.Errorf("%s line is %.0fpx long, under the %dpx a deploy line needs to spread troops", corner, length, minPinnedLineLength)
		}

		hero, ok := cfg.HeroTargets[corner]
		if !ok || (hero.X == 0 && hero.Y == 0) {
			t.Errorf("%s: no hero point", corner)
		} else if c := ClampToDeployBand(hero, ui); !inBand(c) {
			t.Errorf("%s hero point at (%d,%d) clamps to (%d,%d), outside the deployable band", corner, hero.X, hero.Y, c.X, c.Y)
		}
		for name, m := range map[string]map[string]ManualEdge{"spell_edges_a": cfg.SpellEdgesA, "spell_edges_b": cfg.SpellEdgesB} {
			if e, ok := m[corner]; !ok || isZeroManualEdge(e) {
				t.Errorf("%s: %s has no line for this corner", corner, name)
			}
		}
	}
}

// readSavedPins loads the pin file the bot reads, preferring the asset tree the
// running binary resolves and falling back to the in-tree file so the test also
// works from `go test ./internal/attack`.
func readSavedPins(t *testing.T) ([]byte, string) {
	t.Helper()
	var candidates []string
	if p := paths.Resolve("precision_config.json"); p != "" {
		candidates = append(candidates, p)
	}
	candidates = append(candidates, filepath.Join("..", "..", "assets", "precision_config.json"))
	var lastErr error
	for _, p := range candidates {
		raw, err := os.ReadFile(p)
		if err == nil {
			return raw, p
		}
		lastErr = err
	}
	t.Fatalf("cannot read precision_config.json (%v); the bot has no deploy geometry", lastErr)
	return nil, ""
}
