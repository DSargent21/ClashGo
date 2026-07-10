package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"image"
	"image/color"
	"math"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/Ducky705/ClashGO/internal/adb"
	"github.com/Ducky705/ClashGO/internal/attack"
	"github.com/Ducky705/ClashGO/internal/config"
	"github.com/Ducky705/ClashGO/internal/game"
	"github.com/Ducky705/ClashGO/internal/paths"
	"github.com/Ducky705/ClashGO/internal/vision"
	"github.com/Ducky705/ClashGO/pkg/strategy"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"gocv.io/x/gocv"
)

// --- Data Structures ---

type TapLog struct {
	Seq          int64   `json:"seq"`
	Type         string  `json:"type"`
	X            int     `json:"x"`
	Y            int     `json:"y"`
	ActualX      int     `json:"actual_x"`
	ActualY      int     `json:"actual_y"`
	StdDev       float64 `json:"std_dev,omitempty"`
	Error        string  `json:"error,omitempty"`
	Ts           int64   `json:"ts_ms"`
	Unit         string  `json:"unit,omitempty"`
	Phase        string  `json:"phase,omitempty"`
	Edge         string  `json:"edge,omitempty"`
	DistFromEdge float64 `json:"dist_from_edge,omitempty"`
	HitTarget    bool    `json:"hit_target"`
}

type PhaseLog struct {
	Name       string       `json:"name"`
	Edge       string       `json:"edge"`
	Units      []string     `json:"units"`
	Taps       []TapLog     `json:"taps"`
	SlotHealth []SlotHealth `json:"slot_health_before"`
}

type SlotHealth struct {
	X        int     `json:"x"`
	Category string  `json:"category"`
	Ratio    float64 `json:"activity_ratio"`
	IsEmpty  bool    `json:"is_empty"`
}

type SlotDiagnostics struct {
	X            int      `json:"x"`
	Category     string   `json:"category"`
	Template     string   `json:"template,omitempty"`
	TotalTaps    int      `json:"total_taps"`
	PhasesTapped []string `json:"phases_tapped"`
	FinalEmpty   bool     `json:"final_empty"`
	FinalRatio   float64  `json:"final_ratio"`
	Deployed     bool     `json:"deployed"`
	MissedReason string   `json:"missed_reason,omitempty"`
}

type DebugManifest struct {
	Timestamp       string            `json:"timestamp"`
	Strategy        string            `json:"strategy"`
	TargetEdge      string            `json:"target_edge"`
	ScreenW         int               `json:"screen_w"`
	ScreenH         int               `json:"screen_h"`
	BarY            int               `json:"bar_y"`
	Slots           []SlotInfo        `json:"slots"`
	Phases          []PhaseLog        `json:"phases"`
	AllTaps         []TapLog          `json:"all_taps"`
	SlotDiagnostics []SlotDiagnostics `json:"slot_diagnostics"`
	Errors          []string          `json:"errors,omitempty"`
	TapCount        int               `json:"tap_count"`
	TapAreas        map[string]int    `json:"tap_areas"`
	Summary         AttackSummary     `json:"summary"`
}

type SlotInfo struct {
	X        int    `json:"x"`
	Y        int    `json:"y"`
	Category string `json:"category"`
}

type TemplateLog struct {
	Unit string  `json:"unit"`
	X    int     `json:"x"`
	Y    int     `json:"y"`
	Conf float64 `json:"confidence"`
}

type AttackSummary struct {
	TotalSlots      int     `json:"total_slots"`
	DeployedSlots   int     `json:"deployed_slots"`
	FailedSlots     int     `json:"failed_slots"`
	TotalTaps       int     `json:"total_taps"`
	TapsOnTarget    int     `json:"taps_on_target"`
	TapsOffTarget   int     `json:"taps_off_target"`
	AvgDistFromEdge float64 `json:"avg_dist_from_edge"`
	BarTaps         int     `json:"bar_taps"`
	CenterTaps      int     `json:"center_taps"`
	FalsePositives  int     `json:"false_positives"`
}

// --- Main ---

func main() {
	zerolog.TimeFieldFormat = time.RFC3339
	log.Logger = log.Output(zerolog.ConsoleWriter{
		Out:        os.Stderr,
		TimeFormat: "15:04:05",
	})
	zerolog.SetGlobalLevel(zerolog.DebugLevel)

	strategyPath := flag.String("strategy", paths.Resolve("strategies/auto_edrag_rush.yaml"), "path to strategy YAML")
	outDir := flag.String("out", "", "output directory (auto-generated if empty)")
	dryRun := flag.Bool("dry-run", false, "only capture and analyze, no actual taps")
	edge := flag.String("edge", "", "override target edge (TopLeft/TopRight/BottomLeft/BottomRight/Random)")
	flag.Parse()

	if flag.NArg() > 0 {
		switch flag.Arg(0) {
		case "off", "disable":
			fmt.Println("debug mode off")
			return
		}
	}

	// Output directory
	if *outDir == "" {
		ts := time.Now().Format("20060102_150405")
		*outDir = filepath.Join("debug_attacks", ts)
	}
	if err := os.MkdirAll(*outDir, 0755); err != nil {
		log.Fatal().Err(err).Msg("failed to create output directory")
	}
	log.Info().Str("dir", *outDir).Msg("debug output directory")

	// Connect ADB
	client := adb.NewClient()
	if err := client.AutoDetectDevice(); err != nil {
		log.Warn().Err(err).Msg("auto-detect failed, using default ID")
	}
	if err := client.Connect(); err != nil {
		log.Fatal().Err(err).Msg("failed to connect to ADB")
	}
	defer client.Close()

	// Calibrate
	calibrator := game.NewCalibrator(client)
	cal, err := calibrator.Calibrate()
	if err != nil {
		log.Fatal().Err(err).Msg("failed to calibrate")
	}

	botCfg := config.DefaultConfig()
	executor := attack.NewExecutor(client, cal, &botCfg.Attack, log.Logger)

	// Parse strategy
	strat, err := strategy.ParseYAML(*strategyPath)
	if err != nil {
		log.Fatal().Err(err).Str("path", *strategyPath).Msg("failed to parse strategy")
	}
	if *edge != "" {
		strat.TargetEdge = *edge
	}
	log.Info().Str("strategy", strat.Name).Str("edge", strat.TargetEdge).Msg("loaded strategy")

	// Capture pre-attack screen
	preScreen, err := client.CaptureToMat()
	if err != nil {
		log.Fatal().Err(err).Msg("failed to capture pre-attack screen")
	}
	defer preScreen.Close()

	w, h := preScreen.Cols(), preScreen.Rows()

	// Load precision config
	var pCfg attack.PrecisionConfig
	mBarY := int(float64(h) * 0.78)
	pData, err := os.ReadFile(paths.Resolve("precision_config.json"))
	if err == nil && json.Unmarshal(pData, &pCfg) == nil {
		scaleY := float64(h) / float64(pCfg.Height)
		mBarY = int(float64(pCfg.BarY) * scaleY)
		if mBarY > int(float64(h)*0.92) {
			mBarY = int(float64(h) * 0.92)
		}
	}
	log.Info().Int("bar_y", mBarY).Msg("bar Y position")

	// Save pre-attack screen
	gocv.IMWrite(filepath.Join(*outDir, "00_pre_attack.png"), preScreen)
	log.Info().Msg("saved pre-attack screen")

	// Parse slots
	slots := executor.ParseLayout(preScreen, pCfg, w, h, mBarY)
	slotInfos := make([]SlotInfo, 0, len(slots))
	for _, s := range slots {
		slotInfos = append(slotInfos, SlotInfo{X: s.X, Y: s.Y, Category: s.Category})
	}
	slotY := executor.GetSlotY(h, mBarY)

	// --- Draw slot diagnostics image ---
	debugImg := preScreen.Clone()
	defer debugImg.Close()

	// Bar limit
	gocv.Line(&debugImg, image.Pt(0, mBarY), image.Pt(w, mBarY), color.RGBA{0, 0, 255, 255}, 2)
	gocv.PutText(&debugImg, fmt.Sprintf("BAR_Y=%d", mBarY), image.Pt(10, mBarY-10), gocv.FontHersheySimplex, 0.4, color.RGBA{0, 0, 255, 255}, 1)

	// Deployment edges
	for edgeName, e := range pCfg.Edges {
		scaled := attack.ScaleEdge(e, pCfg.Width, pCfg.Height, w, h)
		c := color.RGBA{0, 255, 0, 255}
		if edgeName == strat.TargetEdge {
			c = color.RGBA{0, 255, 255, 255}
		}
		gocv.Line(&debugImg, scaled.P1, scaled.P2, c, 2)
		gocv.PutText(&debugImg, edgeName, scaled.P1, gocv.FontHersheySimplex, 0.35, c, 1)
	}

	// Spell edges
	for name, e := range pCfg.SpellEdgesA {
		scaled := attack.ScaleEdge(e, pCfg.Width, pCfg.Height, w, h)
		gocv.Line(&debugImg, scaled.P1, scaled.P2, color.RGBA{255, 0, 255, 255}, 1)
		gocv.PutText(&debugImg, "SA:"+name, scaled.P1, gocv.FontHersheySimplex, 0.25, color.RGBA{255, 0, 255, 255}, 1)
	}
	for name, e := range pCfg.SpellEdgesB {
		scaled := attack.ScaleEdge(e, pCfg.Width, pCfg.Height, w, h)
		gocv.Line(&debugImg, scaled.P1, scaled.P2, color.RGBA{200, 0, 200, 255}, 1)
		gocv.PutText(&debugImg, "SB:"+name, scaled.P1, gocv.FontHersheySimplex, 0.25, color.RGBA{200, 0, 200, 255}, 1)
	}

	// Hero/spell targets
	for name, pt := range pCfg.HeroTargets {
		scaled := image.Pt(int(float64(pt.X)*float64(w)/float64(pCfg.Width)), int(float64(pt.Y)*float64(h)/float64(pCfg.Height)))
		gocv.Circle(&debugImg, scaled, 8, color.RGBA{255, 0, 0, 255}, 2)
		gocv.PutText(&debugImg, "H:"+name, image.Pt(scaled.X+10, scaled.Y-5), gocv.FontHersheySimplex, 0.3, color.RGBA{255, 0, 0, 255}, 1)
	}
	for name, pt := range pCfg.SpellTargets {
		scaled := image.Pt(int(float64(pt.X)*float64(w)/float64(pCfg.Width)), int(float64(pt.Y)*float64(h)/float64(pCfg.Height)))
		gocv.Circle(&debugImg, scaled, 8, color.RGBA{255, 0, 255, 255}, 2)
		gocv.PutText(&debugImg, "S:"+name, image.Pt(scaled.X+10, scaled.Y-5), gocv.FontHersheySimplex, 0.3, color.RGBA{255, 0, 255, 255}, 1)
	}

	// Slots with categories
	for _, slot := range slots {
		c := color.RGBA{255, 255, 255, 255}
		switch slot.Category {
		case "Troop":
			c = color.RGBA{255, 0, 0, 255}
		case "Siege":
			c = color.RGBA{0, 255, 255, 255}
		case "Hero":
			c = color.RGBA{0, 255, 0, 255}
		case "Spell":
			c = color.RGBA{255, 0, 255, 255}
		case "CC":
			c = color.RGBA{0, 165, 255, 255}
		}
		gocv.Circle(&debugImg, image.Pt(slot.X, slot.Y), 18, c, 2)
		gocv.PutText(&debugImg, slot.Category, image.Pt(slot.X-15, slot.Y-25), gocv.FontHersheySimplex, 0.4, c, 1)
		gocv.PutText(&debugImg, fmt.Sprintf("%d", slot.X), image.Pt(slot.X-10, slot.Y+30), gocv.FontHersheySimplex, 0.3, color.RGBA{200, 200, 200, 255}, 1)
	}

	gocv.IMWrite(filepath.Join(*outDir, "01_slots_and_edges.png"), debugImg)
	log.Info().Int("slots", len(slots)).Msg("saved slot diagnostics")

	// --- Red zone detection visualization ---
	redDetector := attack.NewRedLineDetector(log.Logger)
	uiCutoff := int(float64(h) * 0.85)
	redZone := redDetector.Detect(preScreen, uiCutoff)

	redImg := preScreen.Clone()
	if redZone.Valid {
		// Draw red zone bounding box with thick outline
		gocv.Rectangle(&redImg, redZone.BBox, color.RGBA{255, 0, 0, 255}, 4)
		// Label
		gocv.PutText(&redImg, fmt.Sprintf("RED ZONE: %dx%d at (%d,%d)", redZone.BBox.Dx(), redZone.BBox.Dy(), redZone.BBox.Min.X, redZone.BBox.Min.Y),
			image.Pt(redZone.BBox.Min.X, redZone.BBox.Min.Y-15),
			gocv.FontHersheySimplex, 0.5, color.RGBA{255, 0, 0, 255}, 2)

		// Calculate deployment lines for ALL sides
		deployCalc := attack.NewDeployLineCalculator(log.Logger)
		sides := []string{"left", "top", "right", "bottom"}
		bestLine := deployCalc.Calculate(redZone, w, h, uiCutoff, "", 15)

		// Draw inactive sides dimmer
		for _, side := range sides {
			if side == bestLine.Side {
				continue
			}
			line := deployCalc.Calculate(redZone, w, h, uiCutoff, side, 15)
			for i, pt := range line.Points {
				gocv.Circle(&redImg, pt, 4, color.RGBA{100, 100, 100, 128}, -1)
				if i > 0 {
					gocv.Line(&redImg, line.Points[i-1], pt, color.RGBA{100, 100, 100, 128}, 1)
				}
			}
		}

		// Draw best deployment line (bright green)
		for i, pt := range bestLine.Points {
			gocv.Circle(&redImg, pt, 8, color.RGBA{0, 255, 0, 255}, -1)
			gocv.Circle(&redImg, pt, 8, color.RGBA{0, 200, 0, 255}, 2)
			if i > 0 {
				gocv.Line(&redImg, bestLine.Points[i-1], pt, color.RGBA{0, 255, 0, 255}, 3)
			}
			// Number each point
			gocv.PutText(&redImg, fmt.Sprintf("%d", i+1), image.Pt(pt.X-4, pt.Y-12),
				gocv.FontHersheySimplex, 0.35, color.RGBA{255, 255, 255, 255}, 1)
		}

		// Draw anchor point (blue diamond)
		gocv.Circle(&redImg, bestLine.Anchor, 12, color.RGBA{0, 100, 255, 255}, -1)
		gocv.Circle(&redImg, bestLine.Anchor, 12, color.RGBA{0, 50, 200, 255}, 3)
		gocv.PutText(&redImg, fmt.Sprintf("ANCHOR (%s)", bestLine.Side),
			image.Pt(bestLine.Anchor.X+15, bestLine.Anchor.Y+5),
			gocv.FontHersheySimplex, 0.45, color.RGBA{0, 100, 255, 255}, 2)

		// Draw standoff distance line from red zone to deploy line
		standoffPt := image.Pt((redZone.BBox.Min.X+redZone.BBox.Max.X)/2, redZone.BBox.Min.Y)
		gocv.Line(&redImg, standoffPt, image.Pt(standoffPt.X, bestLine.Points[0].Y), color.RGBA{255, 255, 0, 180}, 1)
		gocv.PutText(&redImg, fmt.Sprintf("standoff=%dpx", bestLine.Points[0].Y-standoffPt.Y),
			image.Pt(standoffPt.X+5, (standoffPt.Y+bestLine.Points[0].Y)/2),
			gocv.FontHersheySimplex, 0.3, color.RGBA{255, 255, 0, 200}, 1)

		log.Info().
			Str("side", bestLine.Side).
			Int("points", len(bestLine.Points)).
			Int("redZoneW", redZone.BBox.Dx()).
			Int("redZoneH", redZone.BBox.Dy()).
			Msg("red zone and deploy line detected")
	} else {
		gocv.PutText(&redImg, "NO RED ZONE DETECTED - using fallback",
			image.Pt(10, 30), gocv.FontHersheySimplex, 0.6, color.RGBA{255, 255, 0, 255}, 2)
		log.Warn().Msg("no red zone detected, using fallback deployment line")
	}
	gocv.IMWrite(filepath.Join(*outDir, "00_red_zone.png"), redImg)
	log.Info().Bool("valid", redZone.Valid).Msg("saved red zone detection")

	// --- Template match log ---
	tplImg := preScreen.Clone()
	barROI := image.Rect(0, mBarY, w, h)
	templates := executor.GetTemplates()
	var tplLogs []TemplateLog
	for tplName, tpl := range templates {
		if tpl.Empty() {
			continue
		}
		matches, _ := vision.MatchMultiScaleROICached(preScreen, tpl, tplName, 0.2, 1.2, 20, 0.50, barROI)
		for _, m := range matches {
			tplLogs = append(tplLogs, TemplateLog{
				Unit: tplName, X: m.Point.X, Y: m.Point.Y, Conf: m.Confidence,
			})
			gocv.Circle(&tplImg, m.Point, 10, color.RGBA{0, 255, 255, 255}, 2)
			gocv.PutText(&tplImg, fmt.Sprintf("%s %.2f", tplName, m.Confidence), image.Pt(m.Point.X+12, m.Point.Y+5), gocv.FontHersheySimplex, 0.28, color.RGBA{0, 255, 255, 255}, 1)
		}
	}
	gocv.IMWrite(filepath.Join(*outDir, "02_template_matches.png"), tplImg)
	log.Info().Int("matches", len(tplLogs)).Msg("saved template matches")

	// --- Capture pre-phase slot health ---
	prePhaseHealth := captureSlotHealth(executor, preScreen, slots, slotY)
	log.Info().Int("slots", len(prePhaseHealth)).Msg("pre-phase slot health captured")

	// --- Set up tap logging with phase tracking ---
	var allTaps []TapLog
	phaseTaps := make(map[string][]TapLog)
	currentPhase := ""
	currentEdge := ""
	phaseCounter := 0

	// Phase health snapshots: phaseName -> screen -> health
	type phaseSnapshot struct {
		Name   string
		Health []SlotHealth
		Screen gocv.Mat
	}
	var phaseSnapshots []phaseSnapshot

	client.SetTapHook(func(ev adb.TapEvent) {
		tl := TapLog{
			Seq:     ev.Seq,
			Type:    ev.Type,
			X:       ev.X,
			Y:       ev.Y,
			ActualX: ev.ActualX,
			ActualY: ev.ActualY,
			StdDev:  ev.StdDev,
			Error:   ev.Error,
			Ts:      ev.Ts,
			Phase:   currentPhase,
			Edge:    currentEdge,
		}
		// Calculate distance from target edge
		if currentEdge != "" {
			tl.DistFromEdge = distFromEdgeLine(ev.ActualX, ev.ActualY, currentEdge, pCfg, w, h)
			tl.HitTarget = tl.DistFromEdge < float64(h)*0.15
		}
		allTaps = append(allTaps, tl)
		if currentPhase != "" {
			phaseTaps[currentPhase] = append(phaseTaps[currentPhase], tl)
		}
	})

	// Phase start callback — capture slot health at phase boundary
	executor.OnPhaseStart = func(phaseName, edge string) {
		currentPhase = phaseName
		currentEdge = edge
		phaseCounter++

		// Capture screen for this phase's pre-health
		scr, err := client.CaptureToMat()
		if err == nil {
			health := captureSlotHealth(executor, scr, slots, slotY)
			phaseSnapshots = append(phaseSnapshots, phaseSnapshot{
				Name:   phaseName,
				Health: health,
				Screen: scr,
			})
			log.Info().Str("phase", phaseName).Int("active_slots", countActive(health)).Msg("phase start slot health")
		}
	}

	// Unit deploy callback — log which slot was tapped
	executor.OnUnitDeploy = func(unit string, slotX, slotYCoord int) {
		log.Info().Str("unit", unit).Int("slot_x", slotX).Msg("unit deploy callback")
	}

	// --- Run attack (or dry-run) ---
	if *dryRun {
		log.Info().Msg("dry-run mode: skipping actual attack")
	} else {
		log.Info().Msg("starting attack deployment")
		remaining, err := executor.DeployDynamicV2(strat, preScreen, *strategyPath)
		if err != nil {
			log.Error().Err(err).Msg("attack deployment failed")
		}
		log.Info().Int("remaining", remaining).Msg("attack deployment complete")
	}

	// --- Capture post-attack screen ---
	postScreen, err := client.CaptureToMat()
	if err == nil {
		gocv.IMWrite(filepath.Join(*outDir, "03_post_attack.png"), postScreen)
		log.Info().Msg("saved post-attack screen")
	}

	// --- Capture post-phase slot health ---
	var postPhaseHealth []SlotHealth
	if !postScreen.Closed() {
		postPhaseHealth = captureSlotHealth(executor, postScreen, slots, slotY)
		log.Info().Int("slots", len(postPhaseHealth)).Msg("post-phase slot health captured")
		postScreen.Close()
	}

	// Close phase snapshot screens
	for i := range phaseSnapshots {
		if !phaseSnapshots[i].Screen.Closed() {
			phaseSnapshots[i].Screen.Close()
		}
	}

	// --- Save tap log JSON ---
	tapJSON, _ := json.MarshalIndent(allTaps, "", "  ")
	os.WriteFile(filepath.Join(*outDir, "tap_log.json"), tapJSON, 0644)
	log.Info().Int("taps", len(allTaps)).Msg("saved tap log")

	// --- Save template matches JSON ---
	tplJSON, _ := json.MarshalIndent(tplLogs, "", "  ")
	os.WriteFile(filepath.Join(*outDir, "template_matches.json"), tplJSON, 0644)

	// --- Classify tap areas ---
	tapAreas := map[string]int{
		"bar": 0, "edge_top": 0, "edge_bot": 0,
		"edge_left": 0, "edge_right": 0, "center": 0, "unknown": 0,
	}
	for _, tl := range allTaps {
		area := classifyTapArea(tl.ActualX, tl.ActualY, w, h, mBarY)
		tapAreas[area]++
	}

	// --- Build slot diagnostics ---
	slotDiags := buildSlotDiagnostics(slots, allTaps, prePhaseHealth, postPhaseHealth, tplLogs, w)

	// --- Build phase logs ---
	var phaseLogs []PhaseLog
	for phaseName, taps := range phaseTaps {
		pl := PhaseLog{
			Name: phaseName,
			Edge: currentEdge,
			Taps: taps,
		}
		for _, u := range taps {
			if u.Unit != "" {
				pl.Units = append(pl.Units, u.Unit)
			}
		}
		// Find pre-health for this phase
		for _, ps := range phaseSnapshots {
			if ps.Name == phaseName {
				pl.SlotHealth = ps.Health
				break
			}
		}
		phaseLogs = append(phaseLogs, pl)
	}
	sort.Slice(phaseLogs, func(i, j int) bool { return phaseLogs[i].Name < phaseLogs[j].Name })

	// --- Build error list ---
	var errs []string
	for _, tl := range allTaps {
		if tl.Error != "" {
			errs = append(errs, fmt.Sprintf("tap %d: %s", tl.Seq, tl.Error))
		}
	}

	// --- Build summary ---
	summary := buildSummary(slots, slotDiags, allTaps, tapAreas)

	// --- Save manifest ---
	manifest := DebugManifest{
		Timestamp:       time.Now().Format(time.RFC3339),
		Strategy:        strat.Name,
		TargetEdge:      strat.TargetEdge,
		ScreenW:         w,
		ScreenH:         h,
		BarY:            mBarY,
		Slots:           slotInfos,
		Phases:          phaseLogs,
		AllTaps:         allTaps,
		SlotDiagnostics: slotDiags,
		Errors:          errs,
		TapCount:        len(allTaps),
		TapAreas:        tapAreas,
		Summary:         summary,
	}
	manifestJSON, _ := json.MarshalIndent(manifest, "", "  ")
	os.WriteFile(filepath.Join(*outDir, "manifest.json"), manifestJSON, 0644)
	log.Info().Msg("saved manifest")

	// --- Draw edge hit map ---
	hitMap := preScreen.Clone()
	defer hitMap.Close()

	// Draw target edge line (thick)
	if edge, ok := pCfg.Edges[strat.TargetEdge]; ok {
		scaled := attack.ScaleEdge(edge, pCfg.Width, pCfg.Height, w, h)
		gocv.Line(&hitMap, scaled.P1, scaled.P2, color.RGBA{0, 255, 255, 255}, 3)
		gocv.PutText(&hitMap, "TARGET: "+strat.TargetEdge, scaled.P1, gocv.FontHersheySimplex, 0.5, color.RGBA{0, 255, 255, 255}, 2)
	}

	// Draw taps with hit/miss coloring
	for i, tl := range allTaps {
		pt := image.Pt(tl.ActualX, tl.ActualY)
		var c color.RGBA
		radius := 5
		if tl.HitTarget {
			c = color.RGBA{0, 255, 0, 255} // green = on target
		} else if tl.ActualY > mBarY {
			c = color.RGBA{255, 128, 0, 255} // orange = bar tap
			radius = 4
		} else {
			c = color.RGBA{255, 0, 0, 255} // red = off target
		}
		gocv.Circle(&hitMap, pt, radius, c, 2)
		if i%10 == 0 || i == len(allTaps)-1 {
			gocv.PutText(&hitMap, fmt.Sprintf("#%d", tl.Seq), image.Pt(pt.X+8, pt.Y-8), gocv.FontHersheySimplex, 0.25, c, 1)
		}
	}

	// Draw slot positions
	for _, slot := range slots {
		gocv.Circle(&hitMap, image.Pt(slot.X, slotY), 12, color.RGBA{255, 255, 255, 128}, 1)
		gocv.PutText(&hitMap, fmt.Sprintf("%d", slot.X), image.Pt(slot.X-8, slotY+25), gocv.FontHersheySimplex, 0.25, color.RGBA{200, 200, 200, 200}, 1)
	}

	gocv.IMWrite(filepath.Join(*outDir, "05_edge_hit_map.png"), hitMap)
	log.Info().Msg("saved edge hit map")

	// --- Draw tap scatter grouped by phase ---
	phaseColors := map[int]color.RGBA{
		0: {255, 255, 0, 255}, // yellow
		1: {0, 255, 0, 255},   // green
		2: {0, 0, 255, 255},   // blue
		3: {255, 0, 255, 255}, // purple
		4: {255, 128, 0, 255}, // orange
		5: {0, 255, 255, 255}, // cyan
		6: {255, 0, 0, 255},   // red
		7: {128, 128, 0, 255}, // olive
	}
	scatterImg := preScreen.Clone()
	defer scatterImg.Close()

	phaseIdx := 0
	lastPhase := ""
	for _, tl := range allTaps {
		if tl.Phase != lastPhase {
			lastPhase = tl.Phase
			phaseIdx++
		}
		c := phaseColors[phaseIdx%len(phaseColors)]
		pt := image.Pt(tl.ActualX, tl.ActualY)
		gocv.Circle(&scatterImg, pt, 4, c, 2)
	}

	// Legend
	phaseIdx = 0
	lastPhase = ""
	legendY := 20
	for _, tl := range allTaps {
		if tl.Phase != lastPhase {
			lastPhase = tl.Phase
			phaseIdx++
			c := phaseColors[phaseIdx%len(phaseColors)]
			gocv.PutText(&scatterImg, fmt.Sprintf("Phase %d: %s", phaseIdx, tl.Phase), image.Pt(10, legendY), gocv.FontHersheySimplex, 0.35, c, 1)
			legendY += 15
		}
	}
	gocv.IMWrite(filepath.Join(*outDir, "06_phase_scatter.png"), scatterImg)
	log.Info().Msg("saved phase scatter")

	// --- Print summary to stdout ---
	printSummary(summary, slotDiags, strat, w, h, mBarY, *outDir)
}

// --- Helper Functions ---

func captureSlotHealth(executor *attack.Executor, screen gocv.Mat, slots []attack.TroopSlot, slotY int) []SlotHealth {
	var health []SlotHealth
	for _, slot := range slots {
		ratio := executor.GetSlotActivityRatio(screen, slot.X, slotY)
		health = append(health, SlotHealth{
			X:        slot.X,
			Category: slot.Category,
			Ratio:    ratio,
			IsEmpty:  ratio < 0.08,
		})
	}
	return health
}

func countActive(health []SlotHealth) int {
	count := 0
	for _, h := range health {
		if !h.IsEmpty {
			count++
		}
	}
	return count
}

func distFromEdgeLine(x, y int, edgeName string, pCfg attack.PrecisionConfig, w, h int) float64 {
	edge, ok := pCfg.Edges[edgeName]
	if !ok {
		return math.MaxFloat64
	}
	scaled := attack.ScaleEdge(edge, pCfg.Width, pCfg.Height, w, h)
	// Distance from point to line segment P1-P2
	return pointToSegmentDist(float64(x), float64(y), float64(scaled.P1.X), float64(scaled.P1.Y), float64(scaled.P2.X), float64(scaled.P2.Y))
}

func pointToSegmentDist(px, py, x1, y1, x2, y2 float64) float64 {
	dx := x2 - x1
	dy := y2 - y1
	if dx == 0 && dy == 0 {
		return math.Sqrt((px-x1)*(px-x1) + (py-y1)*(py-y1))
	}
	t := ((px-x1)*dx + (py-y1)*dy) / (dx*dx + dy*dy)
	if t < 0 {
		t = 0
	} else if t > 1 {
		t = 1
	}
	closestX := x1 + t*dx
	closestY := y1 + t*dy
	return math.Sqrt((px-closestX)*(px-closestX) + (py-closestY)*(py-closestY))
}

func buildSlotDiagnostics(slots []attack.TroopSlot, allTaps []TapLog, preHealth, postHealth []SlotHealth, tplLogs []TemplateLog, w int) []SlotDiagnostics {
	var diags []SlotDiagnostics
	for _, slot := range slots {
		d := SlotDiagnostics{
			X:        slot.X,
			Category: slot.Category,
		}

		// Find nearest template match for this slot
		bestDist := 999
		for _, tpl := range tplLogs {
			dist := int(math.Abs(float64(tpl.X - slot.X)))
			if dist < bestDist && dist < 30 { // Within 30px of slot
				bestDist = dist
				d.Template = tpl.Unit
			}
		}

		// Count taps on this slot
		tapCount := 0
		phases := make(map[string]bool)
		for _, tl := range allTaps {
			if math.Abs(float64(tl.X-slot.X)) < float64(w)*0.04 {
				tapCount++
				if tl.Phase != "" {
					phases[tl.Phase] = true
				}
			}
		}
		d.TotalTaps = tapCount
		for p := range phases {
			d.PhasesTapped = append(d.PhasesTapped, p)
		}
		sort.Strings(d.PhasesTapped)

		// Find final state from postHealth and preHealth ratio for this slot
		var preRatio float64
		for _, ph := range preHealth {
			if ph.X == slot.X {
				preRatio = ph.Ratio
				break
			}
		}
		for _, ph := range postHealth {
			if ph.X == slot.X {
				d.FinalEmpty = ph.IsEmpty
				d.FinalRatio = ph.Ratio
				break
			}
		}

		// Determine if deployed
		// Troop/Spell/CC: slot empties after deployment
		// Hero: always stays on bar after deploy (cooldown), so tapped = deployed
		// Siege: slot shows smaller icon (cage) after deployment
		d.Deployed = d.FinalEmpty && tapCount > 0
		if !d.Deployed && tapCount > 0 && slot.Category == "Hero" {
			// Heroes ALWAYS stay on the bar - if tapped, it was deployed
			d.Deployed = true
		}
		if !d.Deployed && tapCount > 0 && slot.Category == "Siege" {
			// For siege: deployed if ratio decreased significantly from pre-attack
			ratioDrop := preRatio - d.FinalRatio
			if ratioDrop > 0.15 {
				d.Deployed = true
			}
		}

		// Determine miss reason
		if !d.Deployed {
			if tapCount == 0 {
				d.MissedReason = "never_tapped"
			} else if !d.FinalEmpty {
				d.MissedReason = "still_active_after_taps"
			}
		}

		diags = append(diags, d)
	}
	return diags
}

func buildSummary(slots []attack.TroopSlot, diags []SlotDiagnostics, allTaps []TapLog, tapAreas map[string]int) AttackSummary {
	s := AttackSummary{
		TotalSlots: len(slots),
		TotalTaps:  len(allTaps),
		BarTaps:    tapAreas["bar"],
		CenterTaps: tapAreas["center"],
	}

	for _, d := range diags {
		if d.Deployed {
			s.DeployedSlots++
		} else {
			s.FailedSlots++
		}
	}

	totalDist := 0.0
	distCount := 0
	for _, tl := range allTaps {
		if tl.HitTarget {
			s.TapsOnTarget++
		} else {
			s.TapsOffTarget++
		}
		if tl.DistFromEdge > 0 && tl.DistFromEdge < 10000 {
			totalDist += tl.DistFromEdge
			distCount++
		}
	}
	if distCount > 0 {
		s.AvgDistFromEdge = totalDist / float64(distCount)
	}

	s.FalsePositives = s.BarTaps + s.CenterTaps

	return s
}

func printSummary(s AttackSummary, diags []SlotDiagnostics, strat *strategy.DynamicStrategy, w, h, mBarY int, outDir string) {
	fmt.Println("\n╔══════════════════════════════════════════════════════════════╗")
	fmt.Println("║               DEBUG ATTACK ANALYSIS SUMMARY                ║")
	fmt.Println("╚══════════════════════════════════════════════════════════════╝")
	fmt.Printf("Strategy:    %s\n", strat.Name)
	fmt.Printf("Target Edge: %s\n", strat.TargetEdge)
	fmt.Printf("Screen:      %dx%d  BarY: %d\n", w, h, mBarY)
	fmt.Println()

	// Slot diagnostics
	fmt.Println("── SLOT DIAGNOSTICS ──────────────────────────────────────────")
	fmt.Printf("%-6s %-8s %-12s %-6s %-8s %-10s %s\n", "X", "Category", "Template", "Taps", "Empty?", "Ratio", "Status")
	fmt.Println("──────────────────────────────────────────────────────────────")
	for _, d := range diags {
		status := "OK"
		if !d.Deployed {
			status = "FAIL: " + d.MissedReason
		}
		tpl := "-"
		if d.Template != "" {
			tpl = d.Template
		}
		fmt.Printf("%-6d %-8s %-12s %-6d %-8t %-8.4f %s\n",
			d.X, d.Category, tpl, d.TotalTaps, d.FinalEmpty, d.FinalRatio, status)
	}
	fmt.Println()

	// Tap accuracy
	fmt.Println("── TAP ACCURACY ─────────────────────────────────────────────")
	fmt.Printf("Total taps:       %d\n", s.TotalTaps)
	fmt.Printf("On target:        %d (%.1f%%)\n", s.TapsOnTarget, pct(s.TapsOnTarget, s.TotalTaps))
	fmt.Printf("Off target:       %d (%.1f%%)\n", s.TapsOffTarget, pct(s.TapsOffTarget, s.TotalTaps))
	fmt.Printf("Avg dist to edge: %.1f px\n", s.AvgDistFromEdge)
	fmt.Printf("Bar taps:         %d (false positives)\n", s.BarTaps)
	fmt.Printf("Center taps:      %d (false positives)\n", s.CenterTaps)
	fmt.Println()

	// Deployment results
	fmt.Println("── DEPLOYMENT RESULTS ───────────────────────────────────────")
	fmt.Printf("Total slots:    %d\n", s.TotalSlots)
	fmt.Printf("Deployed:       %d (%.1f%%)\n", s.DeployedSlots, pct(s.DeployedSlots, s.TotalSlots))
	fmt.Printf("Failed:         %d (%.1f%%)\n", s.FailedSlots, pct(s.FailedSlots, s.TotalSlots))
	fmt.Println()

	// Failed slots detail
	if s.FailedSlots > 0 {
		fmt.Println("── FAILED SLOTS ─────────────────────────────────────────────")
		for _, d := range diags {
			if !d.Deployed {
				fmt.Printf("  X=%-4d %-8s taps=%-3d reason=%s\n", d.X, d.Category, d.TotalTaps, d.MissedReason)
			}
		}
		fmt.Println()
	}

	// Diagnosis
	fmt.Println("── DIAGNOSIS ────────────────────────────────────────────────")
	issues := []string{}
	if s.BarTaps > 5 {
		issues = append(issues, fmt.Sprintf("HIGH bar taps (%d) — verification is tapping into bar area", s.BarTaps))
	}
	if s.CenterTaps > 5 {
		issues = append(issues, fmt.Sprintf("HIGH center taps (%d) — deployment target is wrong edge or too deep", s.CenterTaps))
	}
	if s.AvgDistFromEdge > float64(h)*0.1 {
		issues = append(issues, fmt.Sprintf("HIGH avg edge distance (%.1f px) — taps landing far from target edge", s.AvgDistFromEdge))
	}
	for _, d := range diags {
		if d.MissedReason == "never_tapped" && d.Category != "Hero" && d.Category != "Siege" {
			issues = append(issues, fmt.Sprintf("Slot X=%d (%s) NEVER TAPPED — not matched in any phase", d.X, d.Category))
		}
		if d.MissedReason == "still_active_after_taps" {
			issues = append(issues, fmt.Sprintf("Slot X=%d (%s) STILL ACTIVE after %d taps — deployment failed or too early to verify", d.X, d.Category, d.TotalTaps))
		}
	}
	if len(issues) == 0 {
		fmt.Println("  No issues detected.")
	} else {
		for _, issue := range issues {
			fmt.Printf("  ⚠ %s\n", issue)
		}
	}
	fmt.Println()

	// Files
	fmt.Println("── OUTPUT FILES ─────────────────────────────────────────────")
	fmt.Printf("  Dir: %s\n\n", outDir)
	fmt.Println("  00_pre_attack.png         - screen before attack")
	fmt.Println("  01_slots_and_edges.png    - detected slots + deployment lines")
	fmt.Println("  02_template_matches.png   - template match positions")
	fmt.Println("  03_post_attack.png        - screen after attack")
	fmt.Println("  05_edge_hit_map.png       - tap accuracy vs target edge")
	fmt.Println("  06_phase_scatter.png      - taps colored by phase")
	fmt.Println("  tap_log.json              - every tap with distance/hit data")
	fmt.Println("  template_matches.json     - template match data")
	fmt.Println("  manifest.json             - full debug manifest")
	fmt.Println()
}

func pct(a, b int) float64 {
	if b == 0 {
		return 0
	}
	return float64(a) / float64(b) * 100
}

func classifyTapArea(x, y, w, h, barY int) string {
	if y > barY {
		return "bar"
	}
	if y < h/6 {
		return "edge_top"
	}
	if y > h*5/6 {
		return "edge_bot"
	}
	if x < w/6 {
		return "edge_left"
	}
	if x > w*5/6 {
		return "edge_right"
	}
	if x > w/3 && x < w*2/3 && y > h/3 && y < h*2/3 {
		return "center"
	}
	return "unknown"
}
