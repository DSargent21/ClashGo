// Command attack_verify is the 720p review harness: it proves the attack
// plan is correct BEFORE a battle is spent on it, by rendering exactly what
// the bot would tap onto a live (or saved) 720p battle screenshot.
//
// What it checks:
//   - formula.json loads, mirrors and projects to the live screen the same way
//     the orchestrator does (formula.ProjectUniform with the calibration's own
//     k, mirror per corner), and reports any point that projects under the
//     troop bar where the tap would hit the HUD instead of deploying
//   - the red zone / pinned deploy line detection works at this resolution
//   - army-bar slots are detected and matched to the strategy's units
//     (reports units the plan expects but the bar doesn't carry — the
//     "unit not found in bar" problem)
//   - troop-count OCR reads plausible numbers per slot (the count=0 problem)
//   - every planned tap point lands inside the deployable area (above the
//     troop bar, below the HUD top) — catches "spells placed in the corner"
//
// It never taps. Live capture goes through internal/adb, same as the bot.
//
// Usage:
//
//	go run ./cmd/attack_verify                        # live capture at 720p
//	go run ./cmd/attack_verify -img shot.png          # analyze a saved frame
//	go run ./cmd/attack_verify -edge BottomRight      # force a corner
//	go run ./cmd/attack_verify -save overlay.png      # write annotated PNG
//	go run ./cmd/attack_verify -strategy assets/strategies/auto_edrag_rush.yaml
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"image"
	"image/color"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Ducky705/ClashGO/internal/adb"
	"github.com/Ducky705/ClashGO/internal/attack"
	"github.com/Ducky705/ClashGO/internal/config"
	"github.com/Ducky705/ClashGO/internal/game"
	"github.com/Ducky705/ClashGO/internal/paths"
	"github.com/Ducky705/ClashGO/pkg/formula"
	"github.com/Ducky705/ClashGO/pkg/strategy"
	"github.com/rs/zerolog"
	"gocv.io/x/gocv"
)

func main() {
	imgPath := flag.String("img", "", "battle screenshot to analyze (default: live adb capture)")
	savePath := flag.String("save", "", "write the annotated overlay PNG here (default: <config>/attack_verify.png)")
	strategyPath := flag.String("strategy", "assets/strategies/auto_edrag_rush.yaml", "strategy YAML; formula is <stem>_formula.json")
	edge := flag.String("edge", "BottomRight", "target corner (BottomRight|BottomLeft|TopRight|TopLeft)")
	device := flag.String("device", "localhost:5555", "ADB device for live capture")
	showScale := flag.Bool("scale", true, "print the formula-vs-live scale report")
	k := flag.Float64("k", 0, "override the display scale (0 = use the bot's pinned device.display_scale)")
	flag.Parse()

	logger := zerolog.New(zerolog.ConsoleWriter{Out: os.Stderr, NoColor: true}).Level(zerolog.WarnLevel)

	// ---- capture ----
	img := gocv.Mat{}
	if *imgPath != "" {
		img = gocv.IMRead(*imgPath, gocv.IMReadColor)
		if img.Empty() {
			fatal("cannot read %s", *imgPath)
		}
	} else {
		client := adb.NewClient(
			adb.WithHost("127.0.0.1"),
			adb.WithPort(5037),
			adb.WithTimeout(30*time.Second),
		)
		client.DeviceID = *device
		defer client.Close()
		var err error
		img, err = client.CaptureToMat()
		if err != nil {
			fatal("live capture: %v", err)
		}
	}
	defer img.Close()
	w, h := img.Cols(), img.Rows()
	fmt.Printf("=== attack_verify: screen %dx%d, edge %s ===\n", w, h, *edge)

	// ---- load strategy ----
	s, err := strategy.ParseYAML(*strategyPath)
	if err != nil {
		fatal("strategy %s: %v", *strategyPath, err)
	}
	liveEdge := *edge
	if strings.EqualFold(s.TargetEdge, "random") {
		liveEdge = *edge // verifier pins a fixed edge for reproducible review
	} else if s.TargetEdge != "" && !strings.EqualFold(s.TargetEdge, "rotate") {
		liveEdge = s.TargetEdge
	}

	// ---- load formula exactly as the orchestrator does ----
	formulaPtr, hasFormula, ferr := formula.Load(*strategyPath)
	if ferr != nil {
		warn("formula parse error: %v", ferr)
	}
	if hasFormula {
		usedOverride := false
		if formulaPtr.CornerOverrides != nil {
			if cornerUnits, ok := formulaPtr.CornerOverrides[*edge]; ok {
				merged := make(map[string]formula.UnitEntry, len(formulaPtr.Units)+len(cornerUnits))
				for k, v := range formulaPtr.Units {
					merged[k] = v
				}
				for k, v := range cornerUnits {
					merged[k] = v
				}
				formulaPtr.Units = merged
				usedOverride = true
			}
		}
		if !usedOverride {
			formulaPtr.MirrorForCorner(*edge)
		}
		// Mirror the orchestrator exactly: the world law is one uniform display
		// scale k about the viewport centre, not a per-axis stretch to fill the
		// framebuffer. Drawing the review overlay under a different law than the
		// bot taps under would approve plans the bot cannot execute.
		scale, _, _ := config.ResolveDisplayScale(*k)
		if scale <= 0 {
			scale = game.NewCalibration(w, h).DisplayScale()
		}
		formulaPtr.ProjectUniform(scale, formulaPtr.Screen.W, formulaPtr.Screen.H, w, h)
		fmt.Printf("formula loaded: %s (authored %dx%d -> live %dx%d, %d units, projected with uniform k=%.4f about the centre)\n",
			formulaPtr.Name, formulaPtr.Screen.W, formulaPtr.Screen.H, w, h, len(formulaPtr.Units), scale)
	} else {
		warn("no formula.json found next to %s — bot would fall back to dynamic red-zone lines", *strategyPath)
	}

	// ---- calibration: the SAME display scale the bot runs ----
	//
	// Deriving the scale from the screen diagonal is not good enough here. The
	// derived ratio at 1280x720 is 1.3004 while the bot pins 1.325, and this
	// overlay exists to show where the bot would tap: at the derived value every
	// rendered point is up to 30px from the live one, so the picture would
	// approve plans the bot cannot execute, and the outside-the-band count would
	// be computed in a geometry that never happens.
	cal := game.NewCalibration(w, h)
	cal.Verified = true
	scale, scaleSource, departsFromBot := config.ResolveDisplayScale(*k)
	cal.SetDisplayScale(scale)
	if departsFromBot {
		fmt.Printf("WARNING: the bot does not run this geometry — %s\n", scaleSource)
	}
	fmt.Printf("display scale k=%.4f (%s; ref %dx%d)\n", cal.DisplayScale(), scaleSource, game.RefWidth, game.RefHeight)
	if *showScale && hasFormula {
		sx := float64(w) / float64(formulaPtr.Screen.W)
		sy := float64(h) / float64(formulaPtr.Screen.H)
		fmt.Printf("formula per-axis scale: x=%.4f y=%.4f (aspect drift %.1f%% — the game scales UNIFORMLY, so authored Y coords drift)\n",
			sx, sy, (sy/sx-1)*100)
	}

	// ---- precision config: pin + red zone ----
	// Same file the executor reads via readConfigJSON (paths.Resolve = assets).
	var pCfg attack.PrecisionConfig
	mBarY := int(float64(h) * 0.78)
	if pData, err := os.ReadFile(paths.Resolve("precision_config.json")); err == nil {
		if err := json.Unmarshal(pData, &pCfg); err == nil && pCfg.BarY > 0 && pCfg.Height > 0 {
			mBarY = int(float64(pCfg.BarY) * float64(h) / float64(pCfg.Height))
		}
	}

	redDetector := attack.NewRedLineDetector(logger)
	uiCutoff := int(float64(h) * 0.85)
	redZone := redDetector.Detect(img, uiCutoff)
	// The mask is what makes this report honest. Detect answers "is there a
	// plausible no-deploy boundary", and on live 720p battle frames the answer is
	// usually NO: the HSV windows catch the gold/elixir top HUD and the warm
	// terrain (measured at 9% of the frame) and the closes fuse that noise into
	// one contour spanning the whole playfield. That blob used to be reported as a
	// valid zone, and "valid=true bbox 63,0 1217x612" reads exactly like a
	// measurement. Showing the mask that produced it, in magenta, is what lets a
	// human see the difference at a glance.
	redMask := redDetector.Mask(img, uiCutoff)
	defer redMask.Close()
	maskPx := gocv.CountNonZero(redMask)
	maskPct := float64(maskPx) * 100 / float64(w*uiCutoff)
	fmt.Printf("red zone: valid=%v", redZone.Valid)
	if redZone.Valid {
		fmt.Printf(" bbox (%d,%d)-(%d,%d)", redZone.BBox.Min.X, redZone.BBox.Min.Y, redZone.BBox.Max.X, redZone.BBox.Max.Y)
	}
	// Coverage guard: outside a battle the "red line" heuristic latches onto village
	// terrain, so a village screenshot must never be read as a real failure.
	redCoverage := 0.0
	if redZone.Valid {
		b := redZone.BBox
		redCoverage = float64((b.Max.X-b.Min.X)*(b.Max.Y-b.Min.Y)) / float64(w*h)
		fmt.Printf(" (covers %.0f%% of the screen)", redCoverage*100)
	}
	fmt.Println()
	fmt.Printf("red mask: %d px (%.1f%% of the playfield is 'red' to the detector), %d outline points, %d channels — magenta in the overlay is all of it\n",
		maskPx, maskPct, len(redZone.Polygon), redMask.Channels())
	// The detector is a threshold, not an oracle. A dashed boundary is a thin
	// curve a few percent of the frame; a fill is the base. Say which one this
	// frame looks like, because that is the difference between a zone worth
	// honoring and one that must be ignored.
	zoneVerdict := "REJECTED — no plausible boundary, so the red-line push is skipped"
	if redZone.Valid {
		zoneVerdict = "usable boundary found"
	}
	fmt.Printf("boundary verdict: %s (mask %.1f%% of frame)\n", zoneVerdict, maskPct)

	// Deploy line exactly as the orchestrator derives it (dynamic or pinned).
	deployCalc := attack.NewDeployLineCalculator(logger)
	deployLine := deployCalc.Calculate(redZone, w, h, uiCutoff, edgeToSide(liveEdge), 15)

	pinned := hasPinnedForTarget(pCfg, liveEdge)
	fmt.Printf("edge %s: pinned=%v, deploy line %s (%d pts)\n", liveEdge, pinned, deployLine.Side, len(deployLine.Points))

	// DeployLine.Outside is set to true by most constructors in internal/attack
	// and never read by the bot, so it says nothing about real geometry. When the
	// edge is pinned the bot ignores the dynamic line entirely ("using user-pinned
	// deploy line; skipping dynamic override"), so check the coordinates that will
	// actually be tapped.
	activeLine := deployLine
	if pinned {
		if pl, ok := pinnedDeployLine(pCfg, liveEdge, 15); ok {
			activeLine = pl
		}
	}

	// …and the same fallback the orchestrator applies (orchestrator.go: when the
	// red boundary is NOT established, the deploy line is translated into the
	// frame's central ground). Without this the harness would keep reporting the
	// PRE-fix plan — an edge-hugging pin that the bot no longer taps — and a
	// reviewer would read a fixed bug as a live one.
	safeGround := attack.SafeGroundRect(w, h, uiCutoff)
	safeShrunk := 0
	if !redZone.Valid && len(activeLine.Points) >= 2 {
		if dx, dy, moved := attack.TranslateIntoSafeGround(activeLine.Points, safeGround); moved {
			safeShrunk = len(activeLine.Points)
			fmt.Printf("deploy line: red boundary not established, so it was TRANSLATED into the central ground (dx=%d dy=%d, box %v) — the pre-fix line is the one shown as \"pinned\" above\n", dx, dy, safeGround)
		}
	}
	pointsOffScreen, pointsUnderBar := 0, 0
	for _, pt := range activeLine.Points {
		if pt.X < 0 || pt.X >= w || pt.Y < 0 || pt.Y >= h {
			pointsOffScreen++
		}
		if pt.Y >= mBarY {
			pointsUnderBar++
		}
	}
	lineKind := "dynamic"
	if pinned {
		lineKind = "PINNED (what the bot taps)"
	}
	fmt.Printf("deploy line [%s]: %d pts, %d off-screen, %d under the troop bar\n",
		lineKind, len(activeLine.Points), pointsOffScreen, pointsUnderBar)

	// ---- slots ----
	// Templates come from the executor's own loader (<assets>/templates/attack),
	// i.e. the exact mats the bot matches bar cards against. Running this half
	// with a nil template map (which is what it did) leaves every card to
	// manual_labels.json — a file authored for one OLD army — so the review
	// relabelled the cards with the wrong units and warned that the strategy's
	// own troops were "NOT in the bar". A pre-flight tool that cannot identify
	// the cards cannot answer the question it exists for.
	verifyExec := attack.NewExecutor(nil, cal, &config.AttackConfig{}, logger)
	slotMgr := attack.NewSlotManager(img, cal, pCfg, w, h, mBarY, verifyExec.GetTemplates(), nil, logger)
	slots := slotMgr.GetAllSlots()
	// Zero slots is normal outside a battle (no army bar on screen). Keep going so the
	// formula/spell/red-zone half of the report still renders instead of dying.
	if len(slots) == 0 {
		warn("no army-bar slots detected — expected outside a battle; set manual_slots.json if this happens mid-attack")
	}

	// Context verdict: both halves of the report are only meaningful in a battle.
	// The army bar is the battle signal, not the red zone: keying off the zone
	// made a real battle frame read as "not a battle" the moment the zone was
	// correctly rejected — i.e. exactly when the reader most needs the report.
	inBattle := len(slots) > 0
	if pointsOffScreen > 0 && inBattle {
		warn("deploy line has %d point(s) off-screen — deploy geometry suspect at this resolution", pointsOffScreen)
	}
	if pointsUnderBar > 0 && inBattle {
		warn("deploy line has %d point(s) under the troop bar (y>=%d) — those taps hit the deck, not the base", pointsUnderBar, mBarY)
	}
	if !inBattle {
		warn("frame does not look like a live battle (zone=%.0f%% of screen, slots=%d) — treating report as plan-only", redCoverage*100, len(slots))
	}

	// ---- troop counts ----
	var countMap map[int]int
	if len(slots) > 0 {
		troopCounter := attack.NewTroopCounter(cal, pCfg.Width, pCfg.Height, logger)
		troopCounts := troopCounter.DetectCounts(img, slots, mBarY)
		countMap = attack.GetAllCounts(troopCounts)
	} else {
		countMap = map[int]int{}
	}

	// ---- plan vs bar ----
	planner := attack.NewDeployPlanner(slotMgr, pCfg, *edge, w, h, logger)
	plans := planner.PlanDeployment(s)

	// Cross-check every NON-ABILITY unit the strategy asks for against the bar.
	// Deriving this from the returned plans would report "none missing" forever:
	// DeployPlanner.planPhase silently drops units whose card it did not match
	// (`if unitPlan.Slot != nil`), which is precisely the "it didn't use all the
	// troops" failure — so ask the slot manager directly.
	found := map[string]bool{}
	var missing []string
	seenMissing := map[string]bool{}
	for _, plan := range plans {
		for _, up := range plan.UnitPlans {
			if up.Slot != nil {
				found[strings.ToLower(up.Unit.Name)] = true
			}
		}
	}
	strategyUnits := 0
	for _, phase := range s.Phases {
		for _, unit := range phase.Units {
			if unit.Pattern == "Ability" || phase.Pattern == "Ability" {
				continue // abilities ride on a hero card, they have no slot of their own
			}
			strategyUnits++
			name := strings.ToLower(strings.TrimSpace(unit.Name))
			if slotMgr.GetSlot(name) != nil {
				found[name] = true
				continue
			}
			if !seenMissing[name] {
				seenMissing[name] = true
				missing = append(missing, unit.Name)
			}
		}
	}

	// ---- render overlay ----
	out := img.Clone()
	defer out.Close()
	// The band the bot is willing to tap: it starts where the deploy line's own
	// top bound does, not at 6% of the screen, so the picture cannot show a tap
	// as "in band" that the deploy code refuses to make.
	uiTop := attack.YTopMin()
	gocv.Rectangle(&out, image.Rect(0, uiTop, w, mBarY), color.RGBA{40, 40, 40, 255}, 1)

	// Order matters: the mask goes down first (magenta fill), then the outline the
	// detector derived from that mask (white), then the box.
	tintMasked(&out, redMask)
	if len(redZone.Polygon) >= 3 {
		for i := 0; i < len(redZone.Polygon); i++ {
			gocv.Line(&out, redZone.Polygon[i], redZone.Polygon[(i+1)%len(redZone.Polygon)], color.RGBA{255, 255, 255, 255}, 1)
		}
	}
	if redZone.Valid {
		gocv.Rectangle(&out, redZone.BBox, color.RGBA{0, 0, 255, 255}, 1)
	}
	for i := 0; i+1 < len(activeLine.Points); i++ {
		gocv.Line(&out, activeLine.Points[i], activeLine.Points[i+1], color.RGBA{0, 255, 255, 255}, 1)
	}
	for _, pt := range activeLine.Points {
		gocv.Circle(&out, pt, 2, color.RGBA{0, 255, 255, 255}, -1)
	}

	// Planned tap points from the (mirrored+projected) formula.
	//
	// The bot clamps projected points into its deployable band before tapping,
	// at both edges: a reference-authored y of 581 projects to 645 on a 720px
	// screen — under the troop bar — and y 176 projects to 108, behind the top
	// resource HUD. Either way the tap hits chrome and the unit never deploys.
	// The overlay must show the clamped plan (what the bot will really do) while
	// still telling the reader that the formula is mis-authored for this
	// geometry, so the fix lands in the formula, not in the clamp.
	tapCount := 0
	cornerHits := 0
	clampedOutsideBand := 0
	if hasFormula {
		// Same band the orchestrator uses: yTopMin (the deploy line's own top
		// bound) up to the uiCutoff the bot computes for this screen.
		clampedOutsideBand = formulaPtr.ClampY(attack.YTopMin(), int(float64(h)*0.85))
		// …and the central-ground clamp, for the same reason: without it this
		// harness renders the pre-fix formula ground and the overlay contradicts
		// what the bot now does.
		if !redZone.Valid {
			formulaPtr.TranslateIntoRect(safeGround.Min.X, safeGround.Max.X, safeGround.Min.Y, safeGround.Max.Y)
		}
		for name, entry := range formulaPtr.Units {
			if strings.HasPrefix(name, "_") {
				continue // helper entries (e.g. _rage_inner) are not directly tapped
			}
			c := colorFor(name)
			points := collectPoints(entry)
			for _, pt := range points {
				tapCount++
				inBand := pt.Y >= attack.YTopMin() && pt.Y < mBarY
				if !inBand {
					cornerHits++
					// draw offenders in solid red with a warning ring
					gocv.Circle(&out, pt, 14, color.RGBA{0, 0, 255, 255}, 3)
				}
				gocv.Circle(&out, pt, 5, c, 2)
				gocv.PutText(&out, short(name), image.Pt(pt.X+6, pt.Y-6), gocv.FontHersheySimplex, 0.35, c, 1)
			}
			if len(points) > 1 {
				for i := 0; i+1 < len(points); i++ {
					gocv.Line(&out, points[i], points[i+1], c, 1)
				}
			}
		}
	}

	// slots: green when matched by the strategy, orange when not planned, red when OCR says 0
	for _, slot := range slots {
		c := color.RGBA{0, 255, 0, 255}
		lbl := slot.UnitName
		if lbl == "" {
			lbl = "?"
			c = color.RGBA{0, 165, 255, 255}
		}
		if !found[strings.ToLower(slot.UnitName)] {
			c = color.RGBA{0, 200, 255, 255} // in bar but not planned
		}
		if countMap[slot.X] == 0 && !slot.IsEmpty {
			c = color.RGBA{0, 0, 255, 255} // OCR read 0 — count detection broken
		}
		gocv.Circle(&out, image.Pt(slot.X, slot.Y), 16, c, 2)
		gocv.PutText(&out, fmt.Sprintf("%s:%d", short(lbl), countMap[slot.X]), image.Pt(slot.X-20, slot.Y-22), gocv.FontHersheySimplex, 0.4, c, 1)
	}

	// legend + verdict text. Collected first, then drawn on a dark panel: thin
	// Hershey text over village terrain is unreadable in a screenshot, which
	// defeats the point of saving evidence.
	type legendLine struct {
		text string
		col  color.RGBA
	}
	var legend []legendLine
	line := func(s string, c color.RGBA) {
		legend = append(legend, legendLine{s, c})
	}
	line(fmt.Sprintf("attack_verify %s | %dx%d edge=%s", time.Now().Format("15:04:05"), w, h, *edge), white)
	if inBattle {
		line(fmt.Sprintf("CONTEXT: in battle (%d army-bar slots) — boundary %s", len(slots), zoneVerdict), green)
	} else {
		line(fmt.Sprintf("CONTEXT: NOT a battle frame (no army bar, %d slots) — deploy-line verdict suppressed", len(slots)), orange)
	}
	if hasFormula {
		line(fmt.Sprintf("formula taps: %d total, %d OUTSIDE deploy band (spells-in-corner check)", tapCount, cornerHits), pick(cornerHits == 0, green, red))
	} else {
		line("no formula loaded — nothing to tap-verify", orange)
	}
	missingNote := ""
	if !inBattle && len(missing) > 0 {
		missingNote = " (fallback labels — rerun mid-attack to confirm)"
	} else if strategyUnits > 0 && len(slots)*2 < strategyUnits {
		// Far fewer cards on screen than the strategy lists: the army has already
		// been thrown, so "missing from bar" is spent cards, not a broken roster.
		missingNote = " (bar looks post-deploy — spent cards read as missing)"
	}
	line(fmt.Sprintf("slots: %d detected, %d planned by strategy, missing from bar: %s%s", len(slots), len(found), orNone(missing), missingNote), pick(len(missing) == 0, green, orange))
	zeroCounts := 0
	for _, slot := range slots {
		if countMap[slot.X] == 0 && !slot.IsEmpty {
			zeroCounts++
		}
	}
	if len(slots) > 0 {
		line(fmt.Sprintf("count OCR: %d/%d slots read 0 (should be ~0)", zeroCounts, len(slots)), pick(zeroCounts == 0, green, red))
	} else {
		line("count OCR: n/a (no army bar visible — not in a battle)", orange)
	}
	line(fmt.Sprintf("red zone: %s | edge %s pinned: %v | mBarY %d", zoneVerdict, liveEdge, pinned, mBarY),
		pick(redZone.Valid, green, orange))
	line(fmt.Sprintf("red mask: %.1f%% of the frame reads as 'red' (magenta) — a real dashed boundary is a thin curve, not a fill", maskPct),
		pick(maskPct < 8, green, red))
	line(fmt.Sprintf("deploy line [%s]: %d pts, %d off-screen, %d under bar", lineKind, len(activeLine.Points), pointsOffScreen, pointsUnderBar),
		pick(pointsOffScreen == 0 && pointsUnderBar == 0, green, red))

	// Dark panel behind the legend so the verdict survives a busy frame. Width
	// follows the longest line (Hershey simplex at scale 0.5 is ~11px/char).
	panelH := len(legend)*18 + 10
	panelW := 0
	for _, l := range legend {
		if need := len(l.text)*11 + 20; need > panelW {
			panelW = need
		}
	}
	if panelW > w {
		panelW = w
	}
	gocv.Rectangle(&out, image.Rect(0, 0, panelW, panelH), color.RGBA{12, 14, 18, 255}, -1)
	y := 20
	for _, l := range legend {
		gocv.PutText(&out, l.text, image.Pt(10, y), gocv.FontHersheySimplex, 0.5, l.col, 1)
		y += 18
	}

	outPath := *savePath
	if outPath == "" {
		outPath = filepath.Join(paths.GetConfigDir(), "attack_verify.png")
	}
	if ok := gocv.IMWrite(outPath, out); ok {
		fmt.Printf("annotated overlay -> %s\n", outPath)
	}

	// ---- the plan, point by point ----
	// This is the "lines" view: every tap the bot would fire from the formula,
	// with the two facts that decide whether the game accepts it — is it inside
	// the deployable band, and does the detected boundary consider it blocked.
	// Text rather than a picture because it can be diffed between runs, which is
	// how a change in the line becomes visible without anyone re-learning a
	// screenshot.
	if hasFormula {
		fmt.Println("--- planned taps (what the bot would actually fire) ---")
		for name, entry := range formulaPtr.Units {
			if strings.HasPrefix(name, "_") {
				continue
			}
			for _, pt := range collectPoints(entry) {
				inBand := pt.Y >= attack.YTopMin() && pt.Y < mBarY
				blocked := redZone.Inside(pt.X, pt.Y)
				verdict := "ok"
				switch {
				case !inBand:
					verdict = "OUT OF BAND (hits the HUD or the troop bar)"
				case blocked:
					verdict = "INSIDE the detected boundary (the game would refuse it)"
				}
				fmt.Printf("  %-16s (%4d,%4d)  %s\n", short(name), pt.X, pt.Y, verdict)
			}
		}
	}

	// ---- text verdict ----
	fmt.Println("--- slots ---")
	for _, slot := range slots {
		fmt.Printf("  x=%-4d y=%-4d unit=%-18s conf=%.2f count=%d empty=%v fallback=%v\n",
			slot.X, slot.Y, orDash(slot.UnitName), slot.Confidence, countMap[slot.X], slot.IsEmpty, slot.FallbackLabeled)
	}
	if len(missing) > 0 {
		warn("strategy units NOT in the bar (bot will skip them): %s", strings.Join(missing, ", "))
	}
	if clampedOutsideBand > 0 {
		warn("%d formula tap points project outside the live deployable band (y %d..%d: behind the top HUD or under the troop bar) and are clamped into it — re-author them for this geometry",
			clampedOutsideBand, attack.YTopMin(), int(float64(h)*0.85))
	}
	if cornerHits > 0 {
		warn("%d formula tap points fall OUTSIDE the deployable band even after clamping — these land on the HUD/corners in a real battle", cornerHits)
	}
	if safeShrunk > 0 {
		warn("no red boundary in this frame, so all %d deploy-line points sat in the view's outer band (where the map's no-deploy ring lives) and were TRANSLATED into the central ground %v — without this the bot taps there and the game answers \"You cannot deploy troops on the Red area!\"", safeShrunk, safeGround)
	}
	if bbox, ok := formulaPtr.BBox(); ok && !bbox.In(safeGround) {
		warn("the formula's span %v is LARGER than the central ground %v, so no translation brings it fully inside — it is centred and its ends are still in unverified ground (taps at y<%d or y>%d)", bbox, safeGround, safeGround.Min.Y, safeGround.Max.Y-1)
	}
	if zeroCounts > len(slots)/2 {
		warn("most slot counts OCR'd as 0 — count-driven deploy will misfire; sweep phase is compensating")
	}
}

// tintMasked blends the detector's binary mask into the screenshot as magenta.
//
// In place, because the frame is already the annotated output. 55/45 rather than
// opaque so the terrain underneath stays readable: the point of the picture is
// to see what the detector picked, and an opaque fill hides the shape it picked
// it from — the base.
func tintMasked(dst *gocv.Mat, mask gocv.Mat) {
	if dst == nil || dst.Empty() || mask.Empty() || dst.Channels() != 3 {
		return
	}
	rows, cols := mask.Rows(), mask.Cols()
	if rows > dst.Rows() {
		rows = dst.Rows()
	}
	if cols > dst.Cols() {
		cols = dst.Cols()
	}
	for y := 0; y < rows; y++ {
		for x := 0; x < cols; x++ {
			if mask.GetUCharAt(y, x) == 0 {
				continue
			}
			// Three bytes per pixel: the byte column is x*3 (B,G,R).
			i := x * 3
			dst.SetUCharAt(y, i, uint8(int(dst.GetUCharAt(y, i))*55/100+115))
			dst.SetUCharAt(y, i+1, uint8(int(dst.GetUCharAt(y, i+1))*55/100))
			dst.SetUCharAt(y, i+2, uint8(int(dst.GetUCharAt(y, i+2))*55/100+115))
		}
	}
}

// edgeToSide maps a corner name the way attack.cornerToSide does.
func edgeToSide(edge string) string {
	switch strings.ToLower(edge) {
	case "topleft", "topright":
		return "top"
	case "bottomleft", "bottomright":
		return "bottom"
	case "left":
		return "left"
	case "right":
		return "right"
	}
	return ""
}

// pinnedDeployLine mirrors attack.manualEdgeToDeployLine for the verifier: it
// interpolates the pinned P1->P2 for the target edge (or its physical side)
// into n points — the coordinates the orchestrator actually taps when pinned.
func pinnedDeployLine(pCfg attack.PrecisionConfig, targetEdge string, n int) (attack.DeployLine, bool) {
	edge, ok := pCfg.Edges[targetEdge]
	if !ok || edge.P1.X == 0 && edge.P1.Y == 0 && edge.P2.X == 0 && edge.P2.Y == 0 {
		side := edgeToSide(targetEdge)
		if side == "" {
			return attack.DeployLine{}, false
		}
		edge, ok = pCfg.Sides[side]
		if !ok || edge.P1.X == 0 && edge.P1.Y == 0 && edge.P2.X == 0 && edge.P2.Y == 0 {
			return attack.DeployLine{}, false
		}
	}
	if n < 2 {
		n = 15
	}
	pts := make([]image.Point, n)
	for i := 0; i < n; i++ {
		t := float64(i) / float64(n-1)
		pts[i] = image.Pt(
			edge.P1.X+int(t*float64(edge.P2.X-edge.P1.X)),
			edge.P1.Y+int(t*float64(edge.P2.Y-edge.P1.Y)),
		)
	}
	return attack.DeployLine{Points: pts, Side: targetEdge, Anchor: pts[len(pts)/2]}, true
}

// hasPinnedForTarget mirrors attack.hasPinnedForTarget (unexported there).
func hasPinnedForTarget(pCfg attack.PrecisionConfig, targetEdge string) bool {
	zero := func(e attack.ManualEdge) bool { return e.P1.X == 0 && e.P1.Y == 0 && e.P2.X == 0 && e.P2.Y == 0 }
	if pCfg.Edges != nil {
		if e, ok := pCfg.Edges[targetEdge]; ok && !zero(e) {
			return true
		}
	}
	if side := edgeToSide(targetEdge); side != "" && pCfg.Sides != nil {
		if s, ok := pCfg.Sides[side]; ok && !zero(s) {
			return true
		}
	}
	return false
}

// collectPoints flattens a (scaled) formula entry into tap points for rendering.
func collectPoints(e formula.UnitEntry) []image.Point {
	var pts []image.Point
	switch {
	case e.IsPoint() && e.P != nil:
		pts = append(pts, e.P.Image())
	case e.IsLine() && e.P1 != nil && e.P2 != nil:
		pts = append(pts, e.P1.Image(), e.P2.Image())
	case e.IsLines():
		for _, lp := range e.Lines {
			pts = append(pts, lp.P1.Image(), lp.P2.Image())
		}
	}
	return pts
}

func colorFor(name string) color.RGBA {
	n := strings.ToLower(name)
	switch {
	case strings.Contains(n, "rage"):
		return color.RGBA{0, 0, 255, 255}
	case strings.Contains(n, "ice") || strings.Contains(n, "freeze"):
		return color.RGBA{255, 255, 0, 255}
	case strings.Contains(n, "spell"):
		return color.RGBA{255, 0, 255, 255}
	case strings.Contains(n, "king") || strings.Contains(n, "queen") || strings.Contains(n, "warden") || strings.Contains(n, "prince"):
		return color.RGBA{0, 255, 0, 255}
	default:
		return color.RGBA{255, 255, 255, 255}
	}
}

func short(s string) string {
	fields := strings.Fields(s)
	if len(fields) == 0 {
		return "?"
	}
	if len(fields[0]) > 7 {
		return fields[0][:7]
	}
	return fields[0]
}

func orNone(ss []string) string {
	if len(ss) == 0 {
		return "none"
	}
	return strings.Join(ss, ",")
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func pick(cond bool, a, b color.RGBA) color.RGBA {
	if cond {
		return a
	}
	return b
}

var (
	white  = color.RGBA{255, 255, 255, 255}
	green  = color.RGBA{0, 255, 0, 255}
	orange = color.RGBA{0, 165, 255, 255}
	red    = color.RGBA{0, 0, 255, 255}
)

func fatal(format string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, "attack_verify: "+format+"\n", args...)
	os.Exit(1)
}

func warn(format string, args ...interface{}) {
	fmt.Printf("  !! "+format+"\n", args...)
}
