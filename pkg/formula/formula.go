// Package formula defines the per-unit deploy coordinate schema produced
// by the `design_attack` interactive tool and consumed by the deploy
// pipeline in `internal/attack`.
//
// The schema is intentionally side-based (single endpoints along a chosen
// SIDE of the screen — top, bottom, left, right) rather than the legacy
// corner-based system in `precision_config.json`. The user can pinpoint
// exact tap coordinates for every unit type so the bot no longer has to
// guess based on red zone detection or random interpolation.
//
// Schema (1 line per field, see examples in cmd/design_attack README):
//
//	{
//	  "name":  "EDrag side formula",
//	  "screen": {"w": 860, "h": 732},
//	  "units": {
//	    "balloon":          {"type":"line", "p1":{"x":60,"y":110}, "p2":{"x":60,"y":542}, "count":10, "jitter":3},
//	    "stone slammer":    {"type":"point", "p":{"x":400,"y":326}, "jitter":6},
//	    "rage spell":       {"type":"lines", "lines":[
//	      {"p1":{"x":80,"y":200},"p2":{"x":130,"y":280},"count":3,"jitter":3},
//	      {"p1":{"x":80,"y":280},"p2":{"x":130,"y":380},"count":2,"jitter":3}
//	    ]}
//	  }
//	}
package formula

import (
	"encoding/json"
	"fmt"
	"image"
	"os"
	"path/filepath"
	"strings"

	"github.com/Ducky705/ClashGO/internal/paths"
)

// Point is a tap-able 2D screen coordinate.
type Point struct {
	X int `json:"x"`
	Y int `json:"y"`
}

// Image converts to image.Point for the deploy path.
func (p Point) Image() image.Point { return image.Pt(p.X, p.Y) }

// LinePoint is one segment of a multi-line deploy (e.g. rage spell 3+2 split).
// Count = number of taps distributed along P1->P2 for this segment.
type LinePoint struct {
	P1     Point `json:"p1"`
	P2     Point `json:"p2"`
	Count  int   `json:"count"`
	Jitter int   `json:"jitter"`
}

// UnitEntry is the per-unit deploy instruction.
//
// Type discriminator (inferred from populated fields):
//
//	"point" - single tap target (heroes, siege)
//	"line"  - taps evenly distributed from P1 to P2 (balloons, EDrag)
//	"lines" - rage-style split (each LinePoint is one sub-line)
//	empty   - no entry; caller falls back to pCfg.Edges
type UnitEntry struct {
	Type string `json:"type,omitempty"`

	// point
	P      *Point `json:"p,omitempty"`
	Jitter int    `json:"jitter,omitempty"`

	// line
	P1    *Point `json:"p1,omitempty"`
	P2    *Point `json:"p2,omitempty"`
	Count int    `json:"count,omitempty"`

	// lines (rage-style)
	Lines []LinePoint `json:"lines,omitempty"`
}

// IsPoint returns true if the entry is "point".
func (e UnitEntry) IsPoint() bool {
	return e.Type == "point" || (e.Type == "" && e.P != nil && e.P1 == nil && len(e.Lines) == 0)
}

// IsLine returns true if the entry is "line".
func (e UnitEntry) IsLine() bool {
	return e.Type == "line" || (e.Type == "" && e.P1 != nil && e.P2 != nil && len(e.Lines) == 0)
}

// IsLines returns true if the entry is "lines".
func (e UnitEntry) IsLines() bool {
	return e.Type == "lines" || len(e.Lines) > 0
}

// Formula is the top-level deploy plan.
//
// `Units` is the per-unit deploy geometry authored for one canonical
// corner (BottomRight by convention). The orchestrator mirrors Units
// to the active target edge at runtime (see MirrorForCorner).
//
// `CornerOverrides` is an optional map of per-corner partial overrides.
// When present, the orchestrator uses the override for that corner
// INSTEAD OF mirroring Units — because the user has authored explicit
// coordinates for that side (e.g. with `cmd/design_attack -corner BL`)
// that better match the base's actual red-line position on that side.
//
// Keys are the canonical corner names produced by NextEdgeIndex:
// "TopLeft" / "TopRight" / "BottomRight" / "BottomLeft" (case
// sensitive — the orchestrator's switch uses the raw value). The
// per-corner value is a per-unit map with the SAME schema as Units;
// typically a PARTIAL formula (only the units that differ from the
// mirrored BR default). The orchestrator merges the override with
// Units per-unit, with the override winning.
//
// `corner_overrides` is omitted from JSON when nil, so old formulas
// (with only Units) continue to round-trip cleanly.
type Formula struct {
	Name   string `json:"name"`
	Screen struct {
		W int `json:"w"`
		H int `json:"h"`
	} `json:"screen"`
	Units           map[string]UnitEntry            `json:"units"`
	CornerOverrides map[string]map[string]UnitEntry `json:"corner_overrides,omitempty"`
}

// Load finds and reads a formula file. Strategy path is the YAML file
// the bot is loading — we look for `<stem>_formula.json` next to it,
// also probing `assets/strategies/<basename>` via paths.Resolve.
func Load(strategyPath string) (*Formula, bool, error) {
	if strategyPath == "" {
		return nil, false, nil
	}
	candidates := candidatePaths(strategyPath)
	for _, p := range candidates {
		raw, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var f Formula
		if err := json.Unmarshal(raw, &f); err != nil {
			return nil, false, fmt.Errorf("formula %s: %w", p, err)
		}
		if f.Units == nil {
			f.Units = map[string]UnitEntry{}
		}
		return &f, true, nil
	}
	return nil, false, nil
}

// LookUp returns the entry for a unit name, normalizing underscores to
// spaces (Strategy stores "stone_slammer"; formula stores "stone slammer").
func (f *Formula) LookUp(unitName string) (UnitEntry, bool) {
	if f == nil {
		return UnitEntry{}, false
	}
	key := strings.ToLower(strings.TrimSpace(unitName))
	if e, ok := f.Units[key]; ok {
		return e, true
	}
	if e, ok := f.Units[strings.ReplaceAll(key, "_", " ")]; ok {
		return e, true
	}
	return UnitEntry{}, false
}

// LoadFile reads a formula from a direct path. Use Load() instead when
// the path is a strategy YAML (Load auto-resolves <stem>_formula.json).
// Used by cmd/design_attack -verify to load a previously-saved formula
// for visual inspection of the 4-corner mirror.
func LoadFile(path string) (*Formula, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var f Formula
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("formula %s: %w", path, err)
	}
	if f.Units == nil {
		f.Units = map[string]UnitEntry{}
	}
	return &f, nil
}

// candidatePaths returns all plausible locations for a formula file
// given the strategy YAML path.
func candidatePaths(strategyPath string) []string {
	var out []string
	if strategyPath != "" {
		base := strings.TrimSuffix(strategyPath, filepath.Ext(strategyPath))
		out = append(out, base+"_formula.json")
		nameLower := strings.ToLower(filepath.Base(base)) + "_formula.json"
		out = append(out, paths.Resolve(filepath.Join("strategies", nameLower)))
	}
	out = append(out, paths.Resolve("strategies/custom_formula.json"))
	return out
}

// Save writes the formula as pretty JSON.
func (f *Formula) Save(path string) error {
	if f == nil {
		return fmt.Errorf("formula: nil")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, raw, 0o644)
}

// MirrorForCorner reflects every P / P1 / P2 / Lines[i].P1 / Lines[i].P2
// across the screen axes as needed so a formula authored for ONE corner
// (BottomRight by convention) applies to any of the 4 corners. Used by
// the orchestrator with `target_edge: "Rotate"` so the same authored
// attack lands on a different side each run without the user having to
// pin all 4 corners in `precision_config.json`.
//
// Reflection rules (mirror around the formula's authored screen center):
//
//	BottomRight: identity (formula as-authored)        — no-op
//	BottomLeft:  reflect X (newX = W - X)              — flip horizontally
//	TopRight:    reflect Y (newY = H - Y)              — flip vertically
//	TopLeft:     reflect both (newX = W - X; newY = H - Y)
//
// Accepts BOTH the full canonical names ("BottomLeft", "TopRight", etc.
// — used by the orchestrator via NextEdgeIndex) AND the abbreviated
// 2-letter forms ("BL", "TR", etc. — used by cmd/design_attack -verify
// for its 2x2 grid labels). Freeform values like "left" or "right"
// also work via the substring fallback so a user-authored strategy
// with `target_edge: "left"` still gets a reasonable mirror.
//
// Designed to be called BEFORE ApplyScreenScale so the mirror uses the
// formula's authored 860x732 reference frame. After the mirror,
// ApplyScreenScale does the live-screen projection. The Grand Warden
// "always at screen center" hardcode in HeroManager is NOT mirrored —
// center stays center — so a pin authored at the formula's (430, 366)
// center mirrors to itself, which is the right behavior either way.
func (f *Formula) MirrorForCorner(targetEdge string) {
	if f == nil {
		return
	}
	corner := strings.ToLower(strings.TrimSpace(targetEdge))
	var mirrorX, mirrorY bool
	switch corner {
	case "bottomright", "br", "bottom_right":
		// identity
	case "bottomleft", "bl", "bottom_left":
		mirrorX = true
	case "topright", "tr", "top_right":
		mirrorY = true
	case "topleft", "tl", "top_left":
		mirrorX = true
		mirrorY = true
	default:
		// Freeform fallback: any value containing "left" mirrors X,
		// any containing "top" mirrors Y. Lets `target_edge: "left"`
		// or similar user inputs still produce a reasonable mirror.
		mirrorX = strings.Contains(corner, "left")
		mirrorY = strings.Contains(corner, "top")
	}
	if !mirrorX && !mirrorY {
		return // identity (BR or unknown) — formula used as-authored.
	}

	w, h := f.Screen.W, f.Screen.H
	if w == 0 {
		w = 1
	}
	if h == 0 {
		h = 1
	}

	for name, e := range f.Units {
		if mirrorX {
			if e.P != nil {
				e.P.X = w - e.P.X
			}
			if e.P1 != nil {
				e.P1.X = w - e.P1.X
			}
			if e.P2 != nil {
				e.P2.X = w - e.P2.X
			}
			for i := range e.Lines {
				e.Lines[i].P1.X = w - e.Lines[i].P1.X
				e.Lines[i].P2.X = w - e.Lines[i].P2.X
			}
		}
		if mirrorY {
			if e.P != nil {
				e.P.Y = h - e.P.Y
			}
			if e.P1 != nil {
				e.P1.Y = h - e.P1.Y
			}
			if e.P2 != nil {
				e.P2.Y = h - e.P2.Y
			}
			for i := range e.Lines {
				e.Lines[i].P1.Y = h - e.Lines[i].P1.Y
				e.Lines[i].P2.Y = h - e.Lines[i].P2.Y
			}
		}
		f.Units[name] = e
	}
}
