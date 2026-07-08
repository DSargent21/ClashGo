// cmd/design_attack is the one-stop tool for authoring + verifying the
// per-unit `formula.json` consumed by the deploy pipeline. It walks the
// user through every unit in the strategy YAML — for each unit the user
// clicks 1 (point) or 2 (line) screen coordinates that the bot will then
// tap on its own.
//
// ONE SIDE COVERS ALL FOUR. The user authors the formula for ONE corner
// (e.g. BottomRight). Combined with `target_edge: "Rotate"` in the
// strategy YAML and `formula.MirrorForCorner` in the orchestrator, the
// bot reflects the same authored attack onto all 4 corners (BR / BL /
// TR / TL) automatically. Use `-verify` to see the 2x2 mirror grid
// before running the bot.
//
// Goal: replace the legacy corner-based pCfg.Edges / red-zone-detection /
// Duke-adjacent-override chain (which kept attacking in the corner and
// stacking heroes on the same pixel) with deterministic, user-pinned
// per-unit coordinates authored once and mirrored to all 4 sides.
//
// Usage (author — click 1 or 2 points per unit, save):
//
//	go run ./cmd/design_attack \
//	    -live -strategy assets/strategies/auto_edrag_rush.yaml \
//	    -out assets/strategies/auto_edrag_rush_formula.json
//
// Usage (verify — 2x2 grid of the formula mirrored to all 4 corners):
//
//	go run ./cmd/design_attack -live -verify auto_edrag_rush_formula.json
//
// Usage (auto-pick from heuristics, no clicks):
//
//	go run ./cmd/design_attack -auto -live -strategy ... -out ...
//
// Flags:
//
//	-screen <png>  : pre-saved battle PNG. Mutually exclusive with -live.
//	-live          : capture live from adb (adb exec-out screencap -p).
//	-device <s>    : adb device serial for -live. Empty = the only
//	                 connected device.
//	-strategy <y>  : strategy YAML (drives the unit list).
//	-out <f.json>  : output formula path. Required for author/auto modes.
//	-verify <f>    : load existing formula, show 2x2 4-corner mirror grid,
//	                 exit on keypress. Implies -screen or -live.
//	-auto          : skip clicks, derive coords from per-class heuristics.
//	-include-event : append _event_troop + _event_spell planned rows.
//	-target-edge   : override auto-pick target edge (right/left/top/bottom).
//
// Author-mode keys: ENTER=commit, BACKSPACE/u=undo, c=clear, m=sub-line,
//
//	s=save+quit, q/ESC=quit-without-save.
package main

import (
	"flag"
	"fmt"
	"image"
	"image/color"
	"os"
	"os/exec"
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
	idx      int            // current unit being annotated
	clicks   []image.Point  // clicks for current unit
	subLines []subLineEntry // multi-sub-line accumulator (Line + m)
	saved    map[string]formula.UnitEntry
	done     bool // true → break main loop and save
}

func main() {
	zerolog.TimeFieldFormat = "15:04:05"
	log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stderr, TimeFormat: "15:04:05"})

	screenPath := flag.String("screen", "", "path to pre-attack PNG. Mutually exclusive with -live.")
	strategyPath := flag.String("strategy", "", "path to strategy YAML (required for author/auto modes)")
	outPath := flag.String("out", "", "output formula JSON (required for author/auto modes)")
	autoFlag := flag.Bool("auto", false, "auto-pick every unit's coordinates from the strategy + screen size; skips manual clicks")
	targetEdgeFlag := flag.String("target-edge", "", "override target_edge for auto-pick: right|left|top|bottom. Empty = use strategy YAML's target_edge.")
	includeEventFlag := flag.Bool("include-event", false, "append _event_troop (point) and _event_spell (line) planned rows after the strategy units, so the user can pin where extra event troops / event spells land on the bar.")
	liveFlag := flag.Bool("live", false, "capture the screen live from adb (no -screen PNG required). Internally runs `adb exec-out screencap -p > tmp.png`.")
	deviceFlag := flag.String("device", "", "adb device serial for -live. Empty = the only connected device. Use `adb devices` to list serials.")
	verifyFlag := flag.String("verify", "", "verify an existing formula.json: load it + the screen, then show a 2x2 grid of the formula mirrored to all 4 corners (BR / BL / TR / TL via MirrorForCorner). Exits on keypress. -screen or -live required for the base image.")
	cornerFlag := flag.String("corner", "BottomRight", "which corner is being authored. BottomRight (default) saves to formula.units and is the BR reference for the mirror fallback. BottomLeft/TopLeft/TopRight save to formula.corner_overrides[<CORNER>] (a per-corner partial override the orchestrator uses INSTEAD of mirroring). Run 4 times (once per corner) for full per-side coverage. Accepts abbreviations: BR/BL/TR/TL.")
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

	// -verify: load an existing formula, show 2x2 4-corner mirror grid.
	// Exits on keypress. Doesn't write anything.
	if *verifyFlag != "" {
		runVerify(*verifyFlag, *screenPath, *liveFlag, *deviceFlag)
		return
	}

	// Author/auto modes: require -strategy and -out. Check BEFORE
	// the -live capture so a missing flag fails fast (no wasted
	// adb screencap before showing the usage). The previous order
	// captured first then failed, which was confusing — the user
	// saw a successful screencap log line followed by `exit status 2`.
	if *strategyPath == "" || *outPath == "" {
		flag.Usage()
		os.Exit(2)
	}

	// -live: capture the screen from adb. Replaces the -screen PNG.
	if *liveFlag {
		if *screenPath != "" {
			log.Fatal().Msg("-live and -screen are mutually exclusive; pick one")
		}
		captured := captureFromAdb(*deviceFlag)
		defer os.Remove(captured)
		screenPath = &captured
	}

	// Final check: need a screen source by now (-screen or -live).
	if *screenPath == "" {
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

	corner := normalizeCorner(*cornerFlag)
	if corner == "BottomRight" {
		// Default path: save to formula.units. If the main file
		// already exists (from a prior -corner BL/TR/TL run), preserve
		// its CornerOverrides so re-authoring the BR default doesn't
		// wipe the per-corner overrides the user already authored.
		var mainF *formula.Formula
		if existing, err := formula.LoadFile(*outPath); err == nil && existing != nil {
			mainF = existing
			if mainF.Units == nil {
				mainF.Units = map[string]formula.UnitEntry{}
			}
		} else {
			mainF = st.toFormula(screen.Cols(), screen.Rows())
		}
		mainF.Units = st.saved
		mainF.Screen.W = screen.Cols()
		mainF.Screen.H = screen.Rows()
		if err := mainF.Save(*outPath); err != nil {
			log.Fatal().Err(err).Str("path", *outPath).Msg("failed to save formula")
		}
		log.Info().Str("path", *outPath).Str("corner", corner).Int("units", len(st.saved)).Msg("formula saved (BR default)")
		fmt.Printf("\nSaved %d units to %s (BR default)\n", len(st.saved), *outPath)
		return
	}

	// Per-corner path: save to formula.corner_overrides[<CORNER>].
	// Load the main formula (create one if it doesn't exist yet),
	// merge our new clicks into CornerOverrides[corner], and save
	// the main formula back. The orchestrator's per-corner override
	// path (see internal/attack/orchestrator.go) uses these in
	// place of the mirror when the active targetEdge has an
	// override, so the user can pin coords that better match the
	// base's actual red-line position on each side.
	var mainF *formula.Formula
	if existing, err := formula.LoadFile(*outPath); err == nil && existing != nil {
		mainF = existing
	} else {
		mainF = st.toFormula(screen.Cols(), screen.Rows())
	}
	if mainF.CornerOverrides == nil {
		mainF.CornerOverrides = make(map[string]map[string]formula.UnitEntry)
	}
	mainF.CornerOverrides[corner] = st.saved
	// Make sure Units is initialized even if the user hasn't done
	// a BR run yet — the orchestrator's merge step needs a non-nil
	// Units map.
	if mainF.Units == nil {
		mainF.Units = map[string]formula.UnitEntry{}
	}
	if err := mainF.Save(*outPath); err != nil {
		log.Fatal().Err(err).Str("path", *outPath).Str("corner", corner).Msg("failed to save per-corner formula override")
	}
	log.Info().
		Str("path", *outPath).
		Str("corner", corner).
		Int("units", len(st.saved)).
		Msg("formula saved (per-corner override)")
	fmt.Printf("\nSaved %d units to %s under corner_overrides.%s\n", len(st.saved), *outPath, corner)
}

// buildPlan walks the strategy YAML and emits a deduplicated, ordered list
// of plannedUnit rows for the user to annotate. Pattern decides point-vs-line:
//
//	"Line" (and missing pattern default) → 2 clicks  (sub-line mode via `m`)
//	"Point"                              → 1 click
//	"FourSides"                          → 1 click (bot fans taps around it)
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
//  1. subLines populated → emit "lines" with one LinePoint per sub-line.
//     This is the 3+2 rage path.
//  2. Needs == 2 AND len(subLines) == 0 → emit "line".
//  3. Needs == 1 → emit "point".
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

// captureFromAdb shells out to `adb exec-out screencap -p` and writes the
// PNG to a temp file. Returns the temp path; caller is responsible for
// os.Remove() after use. `device` is the adb serial (or "" for default).
//
// Modern adb supports `exec-out` which streams binary stdout to our
// process directly — no need for the older "shell screencap -p /sdcard/x
// && adb pull && adb shell rm" dance that leaves files on the device.
func captureFromAdb(device string) string {
	args := []string{"exec-out", "screencap", "-p"}
	if device != "" {
		args = append([]string{"-s", device}, args...)
	}
	cmd := exec.Command("adb", args...)
	out, err := cmd.Output()
	if err != nil {
		log.Fatal().Err(err).Strs("adb_args", args).
			Msg("-live: `adb exec-out screencap -p` failed. Is the device connected (`adb devices`)? Is adb on $PATH?")
	}
	f, err := os.CreateTemp("", "design_attack_capture_*.png")
	if err != nil {
		log.Fatal().Err(err).Msg("-live: failed to create temp PNG file")
	}
	defer f.Close()
	if _, err := f.Write(out); err != nil {
		log.Fatal().Err(err).Msg("-live: failed to write captured PNG to temp file")
	}
	log.Info().Str("path", f.Name()).Int("bytes", len(out)).Msg("-live: adb screencap captured")
	return f.Name()
}

// runVerify loads an existing formula.json, captures/loads the screen,
// and shows a 2x2 grid where each quadrant is the formula mirrored to
// one of the 4 corners (BR / BL / TR / TL). The user can visually
// confirm deploy positions on all sides before running the bot. Exits
// on any keypress. Also saves the grid as verify_grid.png next to the
// working directory for offline reference.
//
// This is the visual counterpart to formula.MirrorForCorner — the user
// can verify the same math the orchestrator applies at runtime.
func runVerify(formulaPath, screenPath string, live bool, device string) {
	if live {
		if screenPath != "" {
			log.Fatal().Msg("-verify -live and -screen are mutually exclusive; pick one")
		}
		screenPath = captureFromAdb(device)
		defer os.Remove(screenPath)
	}
	if screenPath == "" {
		log.Fatal().Msg("-verify requires -screen <png> or -live")
	}

	f, err := formula.LoadFile(formulaPath)
	if err != nil {
		log.Fatal().Err(err).Str("path", formulaPath).Msg("verify: failed to load formula")
	}
	log.Info().Str("path", formulaPath).Int("units", len(f.Units)).Msg("verify: formula loaded")

	screen := gocv.IMRead(screenPath, gocv.IMReadColor)
	if screen.Empty() {
		log.Fatal().Str("path", screenPath).Msg("verify: failed to read screen image")
	}
	defer screen.Close()
	w, h := screen.Cols(), screen.Rows()
	log.Info().Int("w", w).Int("h", h).Msg("verify: screen loaded")

	// Build 2x2 grid. Each quadrant is the screen resized to (w/2, h/2)
	// with the formula mirrored for that corner + lines/points drawn
	// in a corner-distinguishing color + corner label in the top-left
	// of the quadrant.
	halfW, halfH := w/2, h/2
	half := gocv.NewMat()
	defer half.Close()
	gocv.Resize(screen, &half, image.Pt(halfW, halfH), 0, 0, gocv.InterpolationLinear)

	corners := []struct {
		name  string
		color color.RGBA
	}{
		// 2x2 grid layout (matches how the user reads the screen):
		//   [TL][TR]
		//   [BL][BR]
		{"BR", color.RGBA{0, 255, 0, 255}},   // bottom-right → green
		{"BL", color.RGBA{0, 200, 255, 255}}, // bottom-left  → cyan
		{"TR", color.RGBA{255, 255, 0, 255}}, // top-right    → yellow
		{"TL", color.RGBA{255, 0, 255, 255}}, // top-left     → magenta
	}

	// quads: [0]=BR, [1]=BL, [2]=TR, [3]=TL — matches the corners slice.
	quads := make([]gocv.Mat, 4)
	for i, c := range corners {
		q := half.Clone()
		// Deep-clone the formula for this corner. MirrorForCorner
		// mutates P / P1 / P2 *Point in place; without the deep copy,
		// the 4 iterations alias each other (all share the same
		// *Point values via the shallow map copy) and the LAST mirror
		// (TL) would win — all 4 quadrants would render TL geometry.
		// The orchestrator's call site is safe because it loads a
		// fresh throwaway formula per attack; runVerify reuses one
		// *f across 4 mirrors, so we must deep-copy here.
		mirrored := *f
		mirrored.Units = make(map[string]formula.UnitEntry, len(f.Units))
		for k, v := range f.Units {
			v2 := v
			if v2.P != nil {
				p := *v2.P
				v2.P = &p
			}
			if v2.P1 != nil {
				p := *v2.P1
				v2.P1 = &p
			}
			if v2.P2 != nil {
				p := *v2.P2
				v2.P2 = &p
			}
			// Lines slice also aliases its backing array after the
			// shallow `v2 := v` copy, so MirrorForCorner's in-place
			// mutation of e.Lines[i].P1 / P2 would alias back to f.
			// LinePoint is a value type, so a slice copy is enough —
			// no nested pointer aliasing inside it. This matters for
			// the user's 3+2 rage spell ("lines" type with multiple
			// sub-lines) which would otherwise show TL geometry in
			// all 4 quadrants.
			if len(v2.Lines) > 0 {
				v2.Lines = append([]formula.LinePoint(nil), v2.Lines...)
			}
			mirrored.Units[k] = v2
		}
		mirrored.MirrorForCorner(c.name)
		// Scale the mirrored formula (still in its authored 860x732
		// frame) to the quadrant's halfW x halfH frame. Without this,
		// a BL mirror of (60, 110) lands at (800, 622) on a ~430x366
		// quadrant — off the right edge, invisible. ApplyScreenScale
		// is the same helper the orchestrator uses, so the verify
		// grid uses the same scale math the runtime deploy uses.
		mirrored.ApplyScreenScale(mirrored.Screen.W, mirrored.Screen.H, halfW, halfH)
		// Draw every unit's deploy line / point with the corner's color.
		for unitName, entry := range mirrored.Units {
			drawFormulaEntry(&q, entry, c.color, unitName)
		}
		// Corner label, top-left of the quadrant.
		gocv.PutText(&q, c.name+" (mirrored)",
			image.Pt(10, 26), gocv.FontHersheySimplex, 0.7, c.color, 2)
		quads[i] = q
	}
	defer func() {
		for _, q := range quads {
			q.Close()
		}
	}()

	// Compose 2x2 grid: [TL, TR] on top, [BL, BR] on bottom.
	top := gocv.NewMat()
	defer top.Close()
	gocv.Hconcat(quads[3], quads[2], &top)
	bot := gocv.NewMat()
	defer bot.Close()
	gocv.Hconcat(quads[1], quads[0], &bot)
	grid := gocv.NewMat()
	defer grid.Close()
	gocv.Vconcat(top, bot, &grid)

	// Save the grid next to the formula for offline reference.
	gridPath := "verify_grid.png"
	if ok := gocv.IMWrite(gridPath, grid); !ok {
		log.Warn().Str("path", gridPath).Msg("verify: failed to write grid PNG")
	} else {
		log.Info().Str("path", gridPath).Msg("verify: grid PNG saved")
	}

	// Display in a window. Any keypress exits.
	win := gocv.NewWindow("VERIFY FORMULA — 4-corner mirror grid (BR green / BL cyan / TR yellow / TL magenta)")
	defer win.Close()
	win.IMShow(grid)
	fmt.Println("=== VERIFY FORMULA ===")
	fmt.Println("2x2 grid shows the formula mirrored to all 4 corners.")
	fmt.Println("Press any key in the window to exit.")
	log.Info().Msg("verify: grid displayed, awaiting keypress")
	win.WaitKey(0)
}

// normalizeCorner accepts the user-friendly aliases for the 4 corners
// (BR/BL/TR/TL or BottomRight/etc.) and returns the canonical name
// the orchestrator's per-corner override lookup uses. Exits the
// process on unknown input so a typo never silently writes to a
// wrong key.
func normalizeCorner(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	switch s {
	case "br", "bottomright", "bottom_right":
		return "BottomRight"
	case "bl", "bottomleft", "bottom_left":
		return "BottomLeft"
	case "tr", "topright", "top_right":
		return "TopRight"
	case "tl", "topleft", "top_left":
		return "TopLeft"
	}
	log.Fatal().Str("corner", s).Msg("unknown -corner value; use BR/BL/TR/TL or BottomRight/BottomLeft/TopRight/TopLeft")
	return ""
}

// drawFormulaEntry paints a single UnitEntry (point / line / multi-line)
// on the overlay with the given color. Used by runVerify to overlay
// every unit's deploy geometry on each corner's quadrant.
func drawFormulaEntry(m *gocv.Mat, entry formula.UnitEntry, c color.RGBA, name string) {
	switch {
	case entry.IsPoint() && entry.P != nil:
		p := entry.P.Image()
		gocv.Circle(m, p, 8, c, 2)
		gocv.PutText(m, name, image.Pt(p.X+12, p.Y-6),
			gocv.FontHersheySimplex, 0.4, c, 1)
	case entry.IsLine() && entry.P1 != nil && entry.P2 != nil:
		p1 := entry.P1.Image()
		p2 := entry.P2.Image()
		gocv.Line(m, p1, p2, c, 2)
		gocv.Circle(m, p1, 5, c, -1)
		gocv.Circle(m, p2, 5, c, -1)
		gocv.PutText(m, name, image.Pt(p1.X+10, p1.Y-6),
			gocv.FontHersheySimplex, 0.4, c, 1)
	case entry.IsLines() && len(entry.Lines) > 0:
		for i, sl := range entry.Lines {
			p1 := sl.P1.Image()
			p2 := sl.P2.Image()
			gocv.Line(m, p1, p2, c, 2)
			gocv.Circle(m, p1, 5, c, -1)
			gocv.Circle(m, p2, 5, c, -1)
			gocv.PutText(m, fmt.Sprintf("%s[%d]", name, i),
				image.Pt(p1.X+10, p1.Y-6),
				gocv.FontHersheySimplex, 0.4, c, 1)
		}
	}
}
