// cmd/design_attack is an interactive pinpoint tool for authoring a
// per-unit `formula.json` consumed by the deploy pipeline. It walks the
// user through every unit in the strategy YAML — for each unit the user
// clicks 1 (point) or 2 (line) screen coordinates that the bot will then
// tap on its own.
//
// Goal: replace the legacy corner-based pCfg.Edges / red-zone-detection /
// Duke-adjacent-override chain (which kept attacking in the corner and
// stacking heroes on the same pixel) with deterministic, user-pinned side
// coordinates.
//
// Usage:
//
//	go run ./cmd/design_attack \
//	    -screen debug_attacks/<date>/00_pre_attack.png \
//	    -strategy assets/strategies/auto_edrag_rush.yaml \
//	    -out assets/strategies/auto_edrag_rush_formula.json
//
// Keys: ENTER=commit current unit, BACKSPACE/u=undo, c=clear current,
//       s=save+quit, q/ESC=quit-without-save.
package main

import (
	"flag"
	"fmt"
	"image"
	"image/color"
	"os"
	"strings"
	"sync"

	"github.com/Ducky705/ClashGO/pkg/formula"
	"github.com/Ducky705/ClashGO/pkg/strategy"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"gocv.io/x/gocv"
)

// plannedUnit is one row in the guided walk.
type plannedUnit struct {
	Name    string // "balloon"
	Phase   string // "Balloons"
	Pattern string // YAML phase pattern
	Needs   int    // 1=point, 2=line, 2=sub-lines (Line + user opts in)
	IsEvent bool   // true for _event_troop / _event_spell planned rows
}

// subLineEntry is one row inside a multi-sub-line picker session
// (e.g. a 3+2 rage spell). Clicks is the (P1, P2) pair; Count is the
// number of taps the bot fires along that sub-line.
type subLineEntry struct {
	Clicks []image.Point
	Count  int
}

// state is the live tool state. Modified under mu.
type state struct {
	planned  []plannedUnit
	idx      int                // current unit being annotated
	clicks   []image.Point      // clicks for current unit
	subLines []subLineEntry     // multi-sub-line accumulator (Line + m)
	saved    map[string]formula.UnitEntry
	done     bool               // true → break main loop and save
}

func main() {
	zerolog.TimeFieldFormat = "15:04:05"
	log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stderr, TimeFormat: "15:04:05"})

	screenPath := flag.String("screen", "", "path to pre-attack PNG (required)")
	strategyPath := flag.String("strategy", "", "path to strategy YAML (required)")
	outPath := flag.String("out", "", "output formula JSON (required)")
	autoFlag := flag.Bool("auto", false, "auto-pick every unit's coordinates from the strategy + screen size; skips manual clicks")
	targetEdgeFlag := flag.String("target-edge", "", "override target_edge for auto-pick: right|left|top|bottom. Empty = use strategy YAML's target_edge.")
	includeEventFlag := flag.Bool("include-event", false, "append _event_troop (point) and _event_spell (line) planned rows after the strategy units, so the user can pin where extra event troops / event spells land on the bar.")
	flag.Parse()

	if *autoFlag {
		if *strategyPath == "" || *outPath == "" || *screenPath == "" {
			log.Fatal().Msg("--auto requires -strategy, -screen, and -out flags")
		}
		// If the user pinned --target-edge, pass it through; else the auto
		// picker will fall back to the strategy's target_edge (Left for
		// Random). This is the easy-button for "attack only from the right".
		runAuto(*strategyPath, *screenPath, *outPath, *targetEdgeFlag)
		return
	}

	if *screenPath == "" || *strategyPath == "" || *outPath == "" {
		flag.Usage()
		os.Exit(2)
	}

	strat, err := strategy.ParseYAML(*strategyPath)
	if err != nil {
		log.Fatal().Err(err).Str("path", *strategyPath).Msg("failed to parse strategy")
	}

	screen := gocv.IMRead(*screenPath, gocv.IMReadColor)
	if screen.Empty() {
		log.Fatal().Str("path", *screenPath).Msg("failed to read image")
	}
	defer screen.Close()

	st := &state{
		planned: buildPlan(strat, *includeEventFlag),
		saved:   map[string]formula.UnitEntry{},
	}
	if len(st.planned) == 0 {
		log.Fatal().Str("strategy", *strategyPath).Msg("strategy has no deployable units")
	}

	overlay := screen.Clone()
	defer overlay.Close()

	win := gocv.NewWindow("DESIGN ATTACK FORMULA")
	defer win.Close()

	var mu sync.Mutex
	render := func() { // mu required by caller
		drawOverlay(overlay, st)
		win.IMShow(overlay)
	}
	render()

	win.SetMouseHandler(func(event, x, y, flags int, _ interface{}) {
		const lButtonDown = 1 // OpenCV EVENT_LBUTTONDOWN = 1
		if event != lButtonDown {
			return
		}
		mu.Lock()
		if st.done || st.idx >= len(st.planned) {
			mu.Unlock()
			return
		}
		st.clicks = append(st.clicks, image.Pt(x, y))
		mu.Unlock()
		render()
		log.Info().Int("x", x).Int("y", y).Int("unit_idx", st.idx).Msg("click")
	}, nil)

	log.Info().Int("units", len(st.planned)).Msg("start")
	fmt.Println("=== DESIGN ATTACK FORMULA ===")
	fmt.Println("Left-click: drop point. ENTER: commit & next.")
	fmt.Println("u/Backspace: undo click. c: clear current. m: (line units) finalize sub-line & start next, or bump count on last sub-line.")
	fmt.Println("s: save. q/ESC: quit (no save unless s).")
	fmt.Println()

	for {
		key := win.WaitKey(30)
		if key < 0 {
			if st.done {
				break
			}
			continue
		}
		mu.Lock()
		switch key {
		case '\r', '\n': // ENTER
			if st.idx < len(st.planned) {
				if err := commit(st); err != nil {
					log.Warn().Err(err).Msg("commit failed; need more clicks")
				} else {
					st.clicks = st.clicks[:0]
					st.subLines = nil
					st.idx++
					if st.idx >= len(st.planned) {
						st.done = true
					}
				}
			}
		case 'm', 'M':
			// Sub-line controls. Two modes:
			//   (a) len(clicks) == 2 (we just finished a 2-click
			//       line) — finalize that pair as a new sub-line,
			//       count=1, and start the next sub-line. UX: "m
			//       after two clicks = next sub-line".
			//   (b) len(clicks) == 0   (in between sub-lines) —
			//       bump count on the most recent sub-line. UX:
			//       "m with empty clicks = +1 count on last". For
			//       a 3+2 rage spell: P1,P2 m m m, P1b,P2b m m,
			//       ENTER.
			if st.idx < len(st.planned) && st.planned[st.idx].Needs == 2 {
				if len(st.clicks) == 2 {
					st.subLines = append(st.subLines, subLineEntry{Clicks: append([]image.Point(nil), st.clicks...), Count: 1})
					st.clicks = st.clicks[:0]
					log.Info().Int("sub_idx", len(st.subLines)).Msg("sub-line added; click next 2 points OR press m to bump its count")
				} else if len(st.clicks) == 0 && len(st.subLines) > 0 {
					st.subLines[len(st.subLines)-1].Count++
					log.Info().Int("sub_idx", len(st.subLines)).Int("count", st.subLines[len(st.subLines)-1].Count).Msg("last sub-line count bumped")
				}
			}
		case 'u', 'U', 8: // backspace
			if len(st.clicks) > 0 {
				st.clicks = st.clicks[:len(st.clicks)-1]
			}
		case 'c', 'C':
			st.clicks = st.clicks[:0]
			st.subLines = nil
		case 's', 'S':
			if st.idx < len(st.planned) {
				_ = commit(st)
				st.clicks = st.clicks[:0]
				st.subLines = nil
				st.idx = len(st.planned)
			}
			st.done = true
		case 'q', 'Q', 27: // ESC
			log.Warn().Bool("done", st.done).Int("saved_units", len(st.saved)).Msg("quit without saving")
			mu.Unlock()
			return
		}
		mu.Unlock()
		render()
		if st.done {
			break
		}
	}

	f := st.toFormula(screen.Cols(), screen.Rows())
	if err := f.Save(*outPath); err != nil {
		log.Fatal().Err(err).Str("path", *outPath).Msg("failed to save formula")
	}
	log.Info().Str("path", *outPath).Int("units", len(st.saved)).Msg("formula saved")
	fmt.Printf("\nSaved %d units to %s\n", len(st.saved), *outPath)
}

// buildPlan walks the strategy YAML and emits a deduplicated, ordered list
// of plannedUnit rows for the user to annotate. Pattern decides point-vs-line:
//   "Line" (and missing pattern default) → 2 clicks  (sub-line mode via `m`)
//   "Point"                              → 1 click
//   "FourSides"                          → 1 click (bot fans taps around it)
//
// includeEvent appends _event_troop (point, 1 click) and _event_spell
// (line, 2 clicks) rows at the very end so the user can pin where
// extra event troops / seasonal spells drop on the bar. Without these
// rows, event troops fall through to the dynamic red-zone line, which
// is almost never where the user actually wants them.
func buildPlan(s *strategy.DynamicStrategy, includeEvent bool) []plannedUnit {
	var out []plannedUnit
	seen := map[string]bool{}
	for _, ph := range s.Phases {
		for _, u := range ph.Units {
			if strings.EqualFold(u.Pattern, "Ability") {
				continue
			}
			name := strings.ToLower(strings.TrimSpace(u.Name))
			if name == "" || seen[name] {
				continue
			}
			seen[name] = true
			needs := 1
			switch strings.ToLower(ph.Pattern) {
			case "line", "":
				needs = 2
			case "point", "foursides":
				needs = 1
			default:
				needs = 2
			}
			out = append(out, plannedUnit{
				Name:    name,
				Phase:   ph.Name,
				Pattern: ph.Pattern,
				Needs:   needs,
			})
		}
	}
	if includeEvent {
		out = append(out,
			plannedUnit{Name: "_event_troop", Phase: "Event Troops", Pattern: "Point", Needs: 1, IsEvent: true},
			plannedUnit{Name: "_event_spell", Phase: "Event Spells", Pattern: "Line", Needs: 2, IsEvent: true},
		)
	}

	// Rage second-line picker step. When the strategy has a Rage spell
	// in any phase, append a `_rage_inner` row that asks the user to
	// pin the second, deeper-in line for the canonical 3+2 rage split.
	// The deployer reads this key BEFORE auto-derive (so the user's
	// explicit pin wins), and falls back to `deriveInwardLine` only
	// when this row was never authored.
	//
	// For an edrag-rush strategy (10 strategy units) + -include-event
	// (2 rows) + this row, the picker runs 13 steps total.
	for _, ph := range s.Phases {
		for _, u := range ph.Units {
			if strings.Contains(strings.ToLower(u.Name), "rage") {
				out = append(out, plannedUnit{
					Name:    "_rage_inner",
					Phase:   "Rage Inner Line",
					Pattern: "Line",
					Needs:   2,
				})
				break
			}
		}
	}
	return out
}

// commit validates the current unit's click count and writes a UnitEntry
// into st.saved. Path precedence:
//   1. subLines populated → emit "lines" with one LinePoint per sub-line.
//      This is the 3+2 rage path.
//   2. Needs == 2 AND len(subLines) == 0 → emit "line".
//   3. Needs == 1 → emit "point".
func commit(st *state) error {
	if st.idx >= len(st.planned) {
		return fmt.Errorf("no current unit")
	}
	u := st.planned[st.idx]
	if len(st.clicks) < u.Needs {
		return fmt.Errorf("unit %q needs %d clicks, have %d", u.Name, u.Needs, len(st.clicks))
	}
	e := formula.UnitEntry{}

	if u.Needs == 2 && len(st.subLines) > 0 {
		// Multi-sub-line path. Each sub-line contributes one LinePoint
		// to the entry. Count starts at the user's m-bump total so a
		// "3+2" rage spell becomes {LineA count=3, LineB count=2}.
		//
		// If the user pressed `m` after the very LAST click set but
		// never bumped the count, the final set is sitting in st.clicks
		// with count=0. Push it as sub-line count=1 so it isn't lost.
		if len(st.clicks) >= 2 {
			st.subLines = append(st.subLines, subLineEntry{Clicks: append([]image.Point(nil), st.clicks[:2]...), Count: 1})
			st.clicks = st.clicks[:0]
		}
		e.Type = "lines"
		e.Lines = make([]formula.LinePoint, 0, len(st.subLines))
		for _, sl := range st.subLines {
			if len(sl.Clicks) < 2 || sl.Count <= 0 {
				continue
			}
			e.Lines = append(e.Lines, formula.LinePoint{
				P1:     formula.Point{X: sl.Clicks[0].X, Y: sl.Clicks[0].Y},
				P2:     formula.Point{X: sl.Clicks[1].X, Y: sl.Clicks[1].Y},
				Count:  sl.Count,
				Jitter: 5,
			})
		}
	} else if u.Needs == 2 {
		e.Type = "line"
		e.P1 = &formula.Point{X: st.clicks[0].X, Y: st.clicks[0].Y}
		e.P2 = &formula.Point{X: st.clicks[1].X, Y: st.clicks[1].Y}
		e.Jitter = 3
	} else {
		e.Type = "point"
		e.P = &formula.Point{X: st.clicks[0].X, Y: st.clicks[0].Y}
		e.Jitter = 5
	}
	st.saved[u.Name] = e
	log.Info().Str("unit", u.Name).Int("sub_lines", len(st.subLines)).Str("type", e.Type).Msg("committed")
	return nil
}

// toFormula packages st.saved + screen dims into a formula.
func (st *state) toFormula(w, h int) *formula.Formula {
	f := &formula.Formula{
		Name: "design_attack_formula",
	}
	f.Screen.W = w
	f.Screen.H = h
	f.Units = st.saved
	return f
}

// drawOverlay paints the screen with current-state annotations: title,
// instructions, click pebbles, current-unit prompt, and a tiled grid for
// pixel-level precision. The overlay mutates the caller-provided Mat.
func drawOverlay(m gocv.Mat, st *state) {
	w, h := m.Cols(), m.Rows()

	// 50-px tile grid for easy pixel-peeping.
	for x := 0; x < w; x += 50 {
		gocv.Line(&m, image.Pt(x, 0), image.Pt(x, h), color.RGBA{100, 100, 100, 80}, 1)
	}
	for y := 0; y < h; y += 50 {
		gocv.Line(&m, image.Pt(0, y), image.Pt(w, y), color.RGBA{100, 100, 100, 80}, 1)
	}

	// Current-unit prompt panel (top-left).
	panelW, panelH := 460, 130
	rect := image.Rect(8, 8, 8+panelW, 8+panelH)
	gocv.Rectangle(&m, rect, color.RGBA{0, 0, 0, 200}, -1)
	lines := []string{}
	if st.idx < len(st.planned) {
		u := st.planned[st.idx]
		kind := "line (2 clicks: P1=start, P2=end; press m to add sub-line)"
		if u.Needs == 1 {
			kind = "point (1 click)"
		}
		lines = []string{
			fmt.Sprintf("Unit %d/%d: %s  [%s]", st.idx+1, len(st.planned), u.Name, u.Phase),
			fmt.Sprintf("Pattern: %s  Kind: %s", u.Pattern, kind),
			fmt.Sprintf("Clicks so far: %d / %d", len(st.clicks), u.Needs),
			fmt.Sprintf("Sub-lines so far: %d  (m adds another; m on empty bumps last count)", len(st.subLines)),
			"ENTER=commit  m=sub-line  u/BSp=undo  c=clear  s=save",
		}
	} else {
		lines = []string{"All units annotated. Save with s or press ENTER on last."}
	}
	for i, ln := range lines {
		gocv.PutText(&m, ln, image.Pt(18, 28+i*22),
			gocv.FontHersheySimplex, 0.42, color.RGBA{255, 255, 255, 255}, 1)
	}

	// Saved-unit list (right-side).
	panelX := w - 8 - 280
	gocv.Rectangle(&m, image.Rect(panelX, 8, panelX+280, 8+22*(len(st.saved)+2)),
		color.RGBA{0, 0, 0, 180}, -1)
	gocv.PutText(&m, fmt.Sprintf("SAVED %d", len(st.saved)), image.Pt(panelX+10, 28),
		gocv.FontHersheySimplex, 0.42, color.RGBA{255, 255, 255, 255}, 1)
	i := 0
	for name := range st.saved {
		gocv.PutText(&m, "  - "+name, image.Pt(panelX+10, 50+i*16),
			gocv.FontHersheySimplex, 0.32, color.RGBA{180, 255, 180, 255}, 1)
		i++
	}

	// Pebbles for the current unit's clicks + labeled markers.
	for i, pt := range st.clicks {
		if i == 0 {
			gocv.Circle(&m, pt, 8, color.RGBA{0, 255, 0, 255}, -1)
		} else {
			gocv.Circle(&m, pt, 8, color.RGBA{255, 200, 0, 255}, -1)
		}
		gocv.PutText(&m, fmt.Sprintf("%d", i+1), image.Pt(pt.X+10, pt.Y-6),
			gocv.FontHersheySimplex, 0.38, color.RGBA{255, 255, 255, 255}, 2)
		if i == 1 && len(st.clicks) >= 2 {
			gocv.Line(&m, st.clicks[0], pt, color.RGBA{255, 200, 0, 200}, 2)
		}
	}
}

// runAuto is the one-shot auto-pick path. No gocv window, no mouse
// callback - it just reads the strategy, asks formula.AutoPickFor to
// fill every unit's geometry via per-class heuristics, then saves
// to outPath.
//
// targetEdge is a CLI override (right/left/top/bottom/Random/empty).
// Empty means "use the strategy YAML's target_edge". Random collapses
// to Left for determinism so the formula is reproducible across runs.
//
// Output naming: writes to outPath verbatim. Convention is
// <yaml_stem>_formula.json — the same path the orchestrator probes
// via formula.candidatePaths.
func runAuto(strategyPath, screenPath, outPath, targetEdge string) {
	strat, err := strategy.ParseYAML(strategyPath)
	if err != nil {
		log.Fatal().Err(err).Str("path", strategyPath).Msg("auto: failed to parse strategy")
	}

	// Read the screen for calibration frame size. We do NOT analyze the
	// bitmap for auto-pick (heuristics are screen-size based, not
	// pixel-based) - but we still need w/h to write into formula.Screen
	// so ApplyScreenScale in the orchestrator is a no-op.
	screen := gocv.IMRead(screenPath, gocv.IMReadColor)
	if screen.Empty() {
		log.Fatal().Str("path", screenPath).Msg("auto: failed to read image")
	}
	defer screen.Close()

	// CLI override wins. Falls back to strategy YAML, then Left as the
	// deterministic no-op for Random.
	if targetEdge == "" {
		targetEdge = strat.TargetEdge
	}
	if targetEdge == "" || strings.EqualFold(targetEdge, "Random") {
		targetEdge = "Left"
	}

	f := formula.AutoPickFor(strat, screen.Cols(), screen.Rows(), targetEdge)
	if f == nil {
		log.Fatal().Msg("auto: AutoPickFor returned nil (bad strategy or screen dims)")
	}
	if err := f.Save(outPath); err != nil {
		log.Fatal().Err(err).Str("path", outPath).Msg("auto: failed to save formula")
	}

	log.Info().
		Str("path", outPath).
		Int("units", len(f.Units)).
		Int("screen_w", screen.Cols()).
		Int("screen_h", screen.Rows()).
		Str("target_edge", targetEdge).
		Msg("auto formula saved; bot will pick every spawn point from this JSON")
}

// (no trailing cargo-cult sentinel — `strings` is used in buildPlan)
