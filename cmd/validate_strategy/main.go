// cmd/validate_strategy runs the attack Planner against a single
// battle screenshot without touching ADB, the live bot, or the
// network. It diff-prints every tap point that lands off the chosen
// target edge so a user can see "attacks on 2 sides in 1 corner"
// regressions in seconds.
//
// Usage:
//
//	validate_strategy -screen path/to/battle.png \
//	                  -strategy assets/strategies/auto_edrag_rush.yaml \
//	                  -precision assets/precision_config.json \
//	                  -edge BottomLeft \
//	                  -out debug_attacks/run-42
//
// Artifacts written to -out:
//
//	00_pre.png        — red zone (red box), all 4 corner lines from
//	                    the (overridden) precision config, and the
//	                    chosen deploy line (green).
//	phase_<n>_<name>.png — every planned tap on the phase, colored
//	                    green if it lands on the target edge's side,
//	                    red otherwise. Yellow corner lines for
//	                    side reference.
//	plan.json         — full structured PlanReport dump.
//	side_classification.ndjson — one line per planned tap, sortable
//	                    + jq-friendly for downstream analysis.
//
// The command INTENTIONALLY does not seed rand — Duke's planned
// location is the chosen-edge midpoint (matches HeroManager's current
// behavior). If you re-introduce a Duke adjacent-corner random pick
// in hero_manager.go, add a -seed flag and seed rand.Intn with it.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"image"
	"image/color"
	"math/rand"
	"os"
	"path/filepath"
	"strings"

	"github.com/Ducky705/ClashGO/internal/attack"
	"github.com/Ducky705/ClashGO/pkg/strategy"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"gocv.io/x/gocv"
)

var (
	screenPath    = flag.String("screen", "", "Path to a battle screenshot PNG (post-deploy-button, pre-bar-tap)")
	strategyPath  = flag.String("strategy", "assets/strategies/auto_edrag_rush.yaml", "Path to strategy YAML")
	precisionPath = flag.String("precision", "assets/precision_config.json", "Path to precision config JSON")
	outDir        = flag.String("out", "validate_out", "Output directory for overlays + plan dump")
	targetEdge    = flag.String("edge", "", "Override target edge (empty = use YAML, 'BottomLeft', 'TopRight', etc.)")
)

func main() {
	flag.Parse()

	if *screenPath == "" {
		log.Fatal().Msg("-screen is required (path to a battle screenshot PNG)")
	}

	// Quiet log to stderr; the summary is what the user reads on stdout.
	log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stderr, TimeFormat: "15:04:05"}).
		With().Timestamp().Logger()

	screen := gocv.IMRead(*screenPath, gocv.IMReadColor)
	if screen.Empty() {
		log.Fatal().Str("path", *screenPath).Msg("failed to load screen image")
	}
	defer screen.Close()
	w, h := screen.Cols(), screen.Rows()
	log.Info().Int("w", w).Int("h", h).Str("path", *screenPath).Msg("loaded screen")

	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		log.Fatal().Err(err).Str("out", *outDir).Msg("failed to create out dir")
	}

	// Resolve strategy + override target edge BEFORE planning.
	strat, err := strategy.ParseYAML(*strategyPath)
	if err != nil {
		log.Fatal().Err(err).Str("path", *strategyPath).Msg("failed to parse strategy")
	}
	if *targetEdge != "" {
		strat.TargetEdge = *targetEdge
	}
	// Resolve "Random" to a deterministic edge so the per-tap
	// MatchSide check has a concrete expected envelope. Without this,
	// SidesForCorner("Random") returns [] and EVERY tap is flagged as
	// a mismatch — drowning the real bug in noise.
	if strat.TargetEdge == "" {
		strat.TargetEdge = "BottomLeft"
	}
	if strings.EqualFold(strat.TargetEdge, "Random") {
		edges := []string{"TopLeft", "TopRight", "BottomLeft", "BottomRight"}
		rand.Seed(42) // deterministic so validator output is reproducible.
		strat.TargetEdge = edges[rand.Intn(len(edges))]
		log.Info().Str("picked", strat.TargetEdge).Msg("resolved Random target edge")
	}
	log.Info().Str("strategy", strat.Name).Int("phases", len(strat.Phases)).Str("target_edge", strat.TargetEdge).Msg("loaded strategy")

	// 1. Detect red zone.
	redDetector := attack.NewRedLineDetector(log.Logger)
	uiCutoff := int(float64(h) * 0.85)
	redZone := redDetector.Detect(screen, uiCutoff)
	log.Info().Bool("valid", redZone.Valid).Interface("bbox", redZone.BBox).Msg("red zone")

	// 2. Compute dynamic deploy line.
	deployCalc := attack.NewDeployLineCalculator(log.Logger)
	deployLine := deployCalc.Calculate(redZone, w, h, uiCutoff, strat.TargetEdge, 15)
	log.Info().Str("side", deployLine.Side).Bool("outside", deployLine.Outside).Msg("deploy line")

	// 3. Load + scale precision config.
	pCfg, err := loadPrecisionConfig(*precisionPath, w, h)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to load precision config")
	}

	// 4. Mirror orchestrator's corner-override (DeployDynamicV2).
	// When red zone is valid, ALL 4 corners get the same deploy line
	// endpoints — this is the fix that prevents troops + Duke from
	// scattering across 2 sides. We apply it here so the validator
	// matches the live behavior exactly.
	if redZone.Valid && len(deployLine.Points) >= 2 {
		ls := deployLine.Points[0]
		le := deployLine.Points[len(deployLine.Points)-1]
		for _, k := range []string{"TopLeft", "TopRight", "BottomLeft", "BottomRight"} {
			pCfg.Edges[k] = attack.ManualEdge{P1: ls, P2: le}
		}
		if pCfg.Sides == nil {
			pCfg.Sides = make(map[string]attack.ManualEdge)
		}
		for _, k := range []string{"top", "bottom", "left", "right"} {
			pCfg.Sides[k] = attack.ManualEdge{P1: ls, P2: le}
		}
		log.Info().Msg("mirrored deploy line into all 4 corners (red zone valid path)")
	} else {
		log.Warn().Msg("red zone invalid; planner will use pinned corners (legacy path)")
	}

	// 5. Run Planner.
	planner := attack.NewPlanner(pCfg, strat, redZone, deployLine, strat.TargetEdge, w, h)
	report := planner.Plan()

	// 6. Render overlays.
	renderPre(screen, *outDir, redZone, deployLine, pCfg, w, h)
	for i, ph := range report.Phases {
		renderPhase(fmt.Sprintf("%02d_%s", i+1, safeName(ph.Name)), screen, *outDir, ph, pCfg, w, h)
	}

	// 7. Dump plan.json (replace image.Rectangle field with a struct
	//    that survives JSON encoding — gocv's image.Rectangle marshal
	//    output is braces/key pairs that confuse jq).
	jcfg, _ := json.MarshalIndent(simplifyReport(report), "", "  ")
	jpath := filepath.Join(*outDir, "plan.json")
	if err := os.WriteFile(jpath, jcfg, 0o644); err != nil {
		log.Error().Err(err).Msg("failed to write plan.json")
	}
	log.Info().Str("path", jpath).Msg("plan dump")

	// 8. NDJSON per-tap (one line per planned tap).
	ndpath := filepath.Join(*outDir, "side_classification.ndjson")
	if f, err := os.Create(ndpath); err == nil {
		for _, ph := range report.Phases {
			for _, tap := range ph.Taps {
				line, _ := json.Marshal(tap)
				f.Write(line)
				f.Write([]byte("\n"))
			}
		}
		f.Close()
		log.Info().Str("path", ndpath).Msg("ndjson per-tap dump")
	}

	// 9. Summary on stdout.
	totalTaps := 0
	for _, ph := range report.Phases {
		totalTaps += len(ph.Taps)
	}
	diagonals := 0
	for _, d := range report.DiagonalCorners {
		if d.AngleReason == "diagonal" {
			diagonals++
		}
	}
	fmt.Println()
	fmt.Println("==== VALIDATION SUMMARY ====")
	fmt.Printf("Screen:           %dx%d\n", w, h)
	fmt.Printf("Strategy:         %s (%d phases)\n", strat.Name, len(strat.Phases))
	fmt.Printf("Target edge:      %s\n", strat.TargetEdge)
	fmt.Printf("Red zone valid:   %t\n", redZone.Valid)
	fmt.Printf("Deploy side:      %s\n", deployLine.Side)
	fmt.Printf("Total taps:       %d\n", totalTaps)
	fmt.Printf("Mismatches:       %d\n", len(report.Mismatches))
	fmt.Printf("Diagonal corners: %d\n", diagonals)
	if len(report.Mismatches) > 0 {
		fmt.Println()
		fmt.Println("MISMATCHED TAPS (landed off-target):")
		for _, m := range report.Mismatches {
			fmt.Printf("  [phase=%s unit=%s target=%s] dropped at (%d,%d) on '%s' (expected %v) note=%s\n",
				m.Phase, m.Unit, m.TargetEdge, m.X, m.Y, m.TapSide, m.ExpectedSides, m.Note)
		}
	}
	if diagonals > 0 {
		fmt.Println()
		fmt.Println("DIAGONAL CORNERS (your line spans multiple screen sides):")
		for _, d := range report.DiagonalCorners {
			if d.AngleReason != "diagonal" {
				continue
			}
			fmt.Printf("  [%s] P1=(%d,%d)-side=%s -> P2=(%d,%d)-side=%s angle=%d° reason=%s\n",
				d.Key, d.P1X, d.P1Y, d.P1Side, d.P2X, d.P2Y, d.P2Side, d.AngleDeg, d.AngleReason)
		}
	}
	fmt.Println()
	fmt.Printf("Output written to: %s\n", *outDir)
}

// loadPrecisionConfig reads the JSON and scales every coordinate
// field to live screen dims. Mirrors the orchestration's
// precision-loader math (DeployDynamic + DeployDynamicV2 do this).
func loadPrecisionConfig(path string, w, h int) (attack.PrecisionConfig, error) {
	var cfg attack.PrecisionConfig
	data, err := os.ReadFile(path)
	if err != nil {
		return cfg, err
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return cfg, err
	}
	scaleX, scaleY := float64(w)/float64(cfg.Width), float64(h)/float64(cfg.Height)
	for k, v := range cfg.Edges {
		cfg.Edges[k] = attack.ManualEdge{
			P1: image.Pt(int(float64(v.P1.X)*scaleX), int(float64(v.P1.Y)*scaleY)),
			P2: image.Pt(int(float64(v.P2.X)*scaleX), int(float64(v.P2.Y)*scaleY)),
		}
	}
	for k, v := range cfg.SpellEdgesA {
		cfg.SpellEdgesA[k] = attack.ManualEdge{
			P1: image.Pt(int(float64(v.P1.X)*scaleX), int(float64(v.P1.Y)*scaleY)),
			P2: image.Pt(int(float64(v.P2.X)*scaleX), int(float64(v.P2.Y)*scaleY)),
		}
	}
	for k, v := range cfg.SpellEdgesB {
		cfg.SpellEdgesB[k] = attack.ManualEdge{
			P1: image.Pt(int(float64(v.P1.X)*scaleX), int(float64(v.P1.Y)*scaleY)),
			P2: image.Pt(int(float64(v.P2.X)*scaleX), int(float64(v.P2.Y)*scaleY)),
		}
	}
	for k, v := range cfg.Sides {
		cfg.Sides[k] = attack.ManualEdge{
			P1: image.Pt(int(float64(v.P1.X)*scaleX), int(float64(v.P1.Y)*scaleY)),
			P2: image.Pt(int(float64(v.P2.X)*scaleX), int(float64(v.P2.Y)*scaleY)),
		}
	}
	return cfg, nil
}

// simplifyReport flattens image.Rectangle to a json-friendly shape
// ({x,y,w,h,nil}) so jq consumers can filter on coordinates.
func simplifyReport(rep attack.PlanReport) map[string]interface{} {
	out := map[string]interface{}{
		"screen":              rep.Screen,
		"red_zone_valid":      rep.RedZoneValid,
		"deploy_side":         rep.DeploySide,
		"decided_target_edge": rep.DecidedTargetEdge,
		"phases":              rep.Phases,
		"mismatches":          rep.Mismatches,
		"diagonal_corners":    rep.DiagonalCorners,
	}
	corners := map[string]map[string]int{}
	for k, r := range rep.Corners {
		corners[k] = map[string]int{
			"minX": r.Min.X, "minY": r.Min.Y,
			"maxX": r.Max.X, "maxY": r.Max.Y,
		}
	}
	out["corners_after_override"] = corners
	if rep.RedZoneValid {
		out["red_zone_bbox"] = map[string]int{
			"minX": rep.RedZoneBBox.Min.X, "minY": rep.RedZoneBBox.Min.Y,
			"maxX": rep.RedZoneBBox.Max.X, "maxY": rep.RedZoneBBox.Max.Y,
		}
	}
	return out
}

// renderPre draws the red zone + corner lines + chosen deploy line.
func renderPre(screen gocv.Mat, outDir string, redZone attack.RedZone, deployLine attack.DeployLine, pCfg attack.PrecisionConfig, w, h int) {
	img := screen.Clone()
	defer img.Close()
	if redZone.Valid {
		gocv.Rectangle(&img, redZone.BBox, color.RGBA{255, 0, 0, 255}, 4)
		label := fmt.Sprintf("RED ZONE %dx%d", redZone.BBox.Dx(), redZone.BBox.Dy())
		gocv.PutText(&img, label, image.Pt(redZone.BBox.Min.X, redZone.BBox.Min.Y-10),
			gocv.FontHersheySimplex, 0.5, color.RGBA{255, 0, 0, 255}, 2)
	}
	for _, name := range []string{"TopLeft", "TopRight", "BottomLeft", "BottomRight"} {
		e, ok := pCfg.Edges[name]
		if !ok {
			continue
		}
		c := color.RGBA{255, 200, 0, 255} // amber for corners
		gocv.Line(&img, e.P1, e.P2, c, 2)
		mid := image.Pt((e.P1.X+e.P2.X)/2, (e.P1.Y+e.P2.Y)/2)
		gocv.PutText(&img, name, image.Pt(mid.X-30, mid.Y-8),
			gocv.FontHersheySimplex, 0.4, c, 2)
	}
	for i, pt := range deployLine.Points {
		if i > 0 {
			gocv.Line(&img, deployLine.Points[i-1], pt, color.RGBA{0, 255, 0, 255}, 3)
		}
	}
	gocv.IMWrite(filepath.Join(outDir, "00_pre.png"), img)
}

// renderPhase draws the (dim gray) corner reference lines and the
// planned taps color-coded by MatchSide. Green = on-side; red =
// off-side (smoking gun).
func renderPhase(name string, screen gocv.Mat, outDir string, ps attack.PlanPhaseSummary, pCfg attack.PrecisionConfig, w, h int) {
	img := screen.Clone()
	defer img.Close()
	for _, edgeName := range []string{"TopLeft", "TopRight", "BottomLeft", "BottomRight"} {
		if e, ok := pCfg.Edges[edgeName]; ok {
			gocv.Line(&img, e.P1, e.P2, color.RGBA{120, 120, 120, 100}, 1)
		}
	}
	mismatches := 0
	for _, tap := range ps.Taps {
		c := color.RGBA{0, 200, 0, 255}
		if !tap.MatchSide {
			c = color.RGBA{255, 0, 0, 255}
			mismatches++
		}
		gocv.Circle(&img, image.Pt(tap.X, tap.Y), 8, c, -1)
		gocv.Circle(&img, image.Pt(tap.X, tap.Y), 8, color.RGBA{255, 255, 255, 200}, 2)
	}
	header := fmt.Sprintf("Phase: %s   taps=%d   mismatches=%d", name, len(ps.Taps), mismatches)
	gocv.PutText(&img, header, image.Pt(20, 60),
		gocv.FontHersheySimplex, 0.7, color.RGBA{255, 255, 0, 255}, 2)
	gocv.PutText(&img, "green=on-side  red=off-side  gray=config-corners",
		image.Pt(20, h-20), gocv.FontHersheySimplex, 0.4, color.RGBA{255, 255, 255, 180}, 1)
	gocv.IMWrite(filepath.Join(outDir, "phase_"+name+".png"), img)
}

// safeName sanitizes a phase name for filesystem use (the YAML might
// contain spaces, emojis, or other tricky chars). We replace
// unsupported bytes with '_' rather than dropping them so a phase
// name like "🏠 Heroes" becomes "_Heroes.png" rather than silently
// hiding the "_Heroes" intent.
func safeName(s string) string {
	out := make([]rune, 0, len(s))
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			out = append(out, r)
		} else {
			out = append(out, '_')
		}
	}
	if len(out) == 0 {
		return "phase"
	}
	return string(out)
}
