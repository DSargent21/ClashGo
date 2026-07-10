// cmd/test_wall_upgrade is a manual verification tool for the wall-upgrade
// system. It drives the same RunWallUpgradeLoop function that the main
// Bot uses, but exposes three progressively deeper verification steps
// so a developer can confirm that everything from device connectivity
// through template recognition to the full loop works on their machine.
//
// Modes:
//
//	check      (default)  Verifies prerequisites only — ADB connected,
//	                        device resolvable, calibration sane, required
//	                        templates loaded. NO clicks. Safe to run on
//	                        any game state.
//
//	dry-run    Verifies prerequisites PLUS captures the current screen
//	                        and runs template-matching for each of the
//	                        three required templates (text_wall,
//	                        btn_upgrade_wall, btn_confirm_upgrade). Saves
//	                        an annotated screenshot with the highest-
//	                        confidence match overlaid per template. NO
//	                        clicks. Best run while the game is parked at
//	                        a representative point of each phase (e.g.
//	                        open the builder menu before running).
//
//	run        Verifies prerequisites, dry-run, THEN prompts for
//	                        confirmation (skippable with -yes) and invokes
//	                        RunWallUpgradeLoop for real. Each phase
//	                        boundary emits a screenshot
//	                        (output/wall_upgrade_tests/<ts>/<step>.png)
//	                        and a JSONL phase log. Use this to confirm
//	                        the FULL sequence works end-to-end — start
//	                        with the game on MainVillage.
//
// All screenshots + JSONL phases are saved under
// ./output/wall_upgrade_tests/<timestamp>/ relative to the working dir.
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"image"
	"image/color"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gocv.io/x/gocv"

	"github.com/Ducky705/ClashGO/internal/adb"
	"github.com/Ducky705/ClashGO/internal/bot"
	"github.com/Ducky705/ClashGO/internal/config"
	"github.com/Ducky705/ClashGO/internal/game"
	"github.com/Ducky705/ClashGO/internal/paths"
	"github.com/Ducky705/ClashGO/internal/vision"
	"github.com/rs/zerolog"
)

// requiredTemplates are the keys the wall-upgrade strictly needs. Listed
// here so preflight + dry-run both cover them.
var requiredTemplates = []string{
	"text_wall",
	"btn_upgrade_wall",
	"btn_confirm_upgrade",
}

func main() {
	var (
		mode           string
		autoYes        bool
		deviceOverride string
		outDir         string
		rmLogs         bool
	)
	flag.StringVar(&mode, "mode", "check", "check|dry-run|run")
	flag.BoolVar(&autoYes, "yes", false, "skip confirmation prompt before live run")
	flag.StringVar(&deviceOverride, "device", "", "override device ID (otherwise uses config)")
	flag.StringVar(&outDir, "out", "", "output directory for screenshots (default ./output/wall_upgrade_tests/<ts>)")
	flag.BoolVar(&rmLogs, "rm-logs", false, "remove output directory before running")
	flag.Usage = usage
	flag.Parse()

	logger := newLogger()

	if rmLogs && outDir != "" {
		_ = os.RemoveAll(outDir)
	}

	// 1. Load config (default if file missing).
	cfgPath := paths.ResolveConfig("config.json")
	usedDefault := false
	cfg, err := config.Load(cfgPath)
	if err != nil {
		logger.Warn().Str("path", cfgPath).Err(err).Msg("config load failed, using built-in defaults")
		cfg = config.DefaultConfig()
		usedDefault = true
	}

	// 2. Build adb.Client + connect + autodetect.
	zl := adbLogAdapter{log: logger}
	client := adb.NewClient(
		adb.WithHost(cfg.Device.ADBHost),
		adb.WithPort(cfg.Device.ADBPort),
		adb.WithTimeout(30*time.Second),
		adb.WithLogger(&zl),
		adb.WithZoomKeys(cfg.Device.ZoomOutKey, cfg.Device.ZoomInKey),
	)
	if deviceOverride != "" {
		client.DeviceID = deviceOverride
	} else {
		client.DeviceID = cfg.Device.DeviceID
	}

	fmt.Printf("\n=== Test Wall Upgrade — mode=%s ===\n\n", mode)

	preflightOK := runPreflight(client, logger, cfg)
	if !preflightOK {
		fmt.Println("\n❌ Preflight failed. Fix the issues above and re-run.")
		os.Exit(1)
	}
	fmt.Println("\n✅ Preflight OK.")
	if usedDefault {
		fmt.Printf("   (note: %s was missing/invalid, used defaults)\n", cfgPath)
	}

	if mode == "check" {
		printFinalCheckSummary()
		return
	}

	// 4. Calibrate + load templates.
	w, h, err := client.ScreenSize()
	if err != nil {
		fmt.Printf("\n❌ ScreenSize: %v\n", err)
		os.Exit(1)
	}
	cal := &game.Calibration{
		PhysicalW:  w,
		PhysicalH:  h,
		ScaleX:     float64(w) / float64(game.RefWidth),
		ScaleY:     float64(h) / float64(game.RefHeight),
		MidOffsetY: (h - game.RefHeight) / 2,
		BottomOffY: h - game.RefHeight,
		Verified:   true,
	}
	ts, err := game.NewTemplateStore(paths.Resolve("templates"))
	if err != nil {
		fmt.Printf("\n❌ NewTemplateStore: %v\n", err)
		os.Exit(1)
	}
	if err := ts.LoadTemplates(); err != nil {
		fmt.Printf("\n❌ LoadTemplates: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("   Templates loaded: %d total (need: %v)\n", ts.Count(), requiredTemplates)

	// 5. Set up output directory.
	if outDir == "" {
		outDir = filepath.Join("output", "wall_upgrade_tests", time.Now().Format("20060102_150405"))
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		fmt.Printf("\n❌ mkdir outDir: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("   Output dir: %s\n", outDir)

	// 6. Dry-run if requested: capture screen + match required templates.
	if mode == "dry-run" || mode == "run" {
		runDryRun(client, ts, cal, logger, outDir)
	}
	if mode != "run" {
		printFinalDryRunSummary(outDir)
		return
	}

	// 7. Prompt for confirmation before live run.
	if !autoYes {
		fmt.Printf("\n⚠️  About to invoke the LIVE wall-upgrade loop against %s.\n", client.DeviceID)
		fmt.Printf("    Make sure the game is on MainVillage (zoom out, then town hall visible).\n")
		fmt.Printf("    Screenshots will be saved to: %s\n", outDir)
		fmt.Print("    Continue? [y/N]: ")
		reader := bufio.NewReader(os.Stdin)
		ans, _ := reader.ReadString('\n')
		ans = strings.TrimSpace(strings.ToLower(ans))
		if ans != "y" && ans != "yes" {
			fmt.Println("Aborted.")
			return
		}
	}

	// 8. Wire up the OnStep instrumentation hook.
	phaseLogPath := filepath.Join(outDir, "phase_log.jsonl")
	phaseLogF, _ := os.OpenFile(phaseLogPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	defer phaseLogF.Close()

	instr := newInstrumentation(outDir, phaseLogF, client, cal, ts, logger)

	// Classify is intentionally left nil — the diagnostic tool's
	// attackButtonClassifier uses btn_attack + orange-color heuristics
	// whose current band is too narrow against the actual Attack!
	// button colour, returning StateUnknown even when the screen IS
	// MainVillage. With Classify wired, waitForMainVillage runs the
	// classifier loop for the full 30s timeout before breaking with
	// no taps fired. Leaving Classify nil hits the existing fast
	// path in waitForMainVillage: returns true after the first
	// successful capture. The user has already verified the screen
	// themselves, so the loop should drive the menu immediately.
	// To re-enable the safety check, tighten the orange-check band
	// in isOrangeSpot (and/or add a top-bar loot-counter check) so
	// it produces a stable StateMainVillage, then wire Classify
	// back to attackButtonClassifier.
	hooks := &bot.WallUpgradeHooks{
		Logger:    logger,
		Client:    client,
		Cal:       cal,
		Templates: ts,
		Classify:  nil,
		Dismiss:   func() { dismissHelper(client, cal, logger) },
		OnStep:    instr.onStep,
	}

	// 9. Run.
	fmt.Println("\n🎬 Starting wall upgrade loop…")
	startTime := time.Now()
	bot.RunWallUpgradeLoop(hooks)
	elapsed := time.Since(startTime)

	// 10. Persist summary.
	summary := map[string]any{
		"mode":             mode,
		"device":           client.DeviceID,
		"screen":           fmt.Sprintf("%dx%d", w, h),
		"templates_loaded": ts.Count(),
		"elapsed_sec":      elapsed.Seconds(),
		"phase_count":      instr.count,
		"output_dir":       outDir,
		"first_phase":      instr.firstStep,
		"last_phase":       instr.lastStep,
	}
	if data, err := json.MarshalIndent(summary, "", "  "); err == nil {
		_ = os.WriteFile(filepath.Join(outDir, "summary.json"), data, 0o644)
	}

	fmt.Printf("\n🏁 Loop finished in %s — %d phase transitions logged.\n", elapsed.Round(time.Millisecond), instr.count)
	fmt.Printf("   Output: %s\n", outDir)
	fmt.Println("   Open phase_log.jsonl + summary.json there for diagnostics.")
}

// usage is the -h output for the tool.
func usage() {
	fmt.Fprintf(os.Stderr, `Usage: test_wall_upgrade [flags]

Mode:
  -mode=check    (default) prerequisites only — ADB, calibration, templates
  -mode=dry-run  capture current screen + match required templates
  -mode=run      prompt before invoking the full wall-upgrade loop

Flags:
`)
	flag.PrintDefaults()
	fmt.Fprintf(os.Stderr, `

Output:
  -out=DIR                 screenshot dir (default ./output/wall_upgrade_tests/<ts>)
  -rm-logs                 remove -out dir before running
`)
}

// instrumentation captures phase transitions emitted by RunWallUpgradeLoop
// via the OnStep hook. The contract for any `screen` payload item is:
// it is a gocv.Mat CLONE owned by this receiver — we must Close it.
type instrumentation struct {
	outDir    string
	phaseF    *os.File
	client    *adb.Client
	cal       *game.Calibration
	templates *game.TemplateStore
	logger    zerolog.Logger
	count     int
	firstStep string
	lastStep  string
}

func newInstrumentation(outDir string, phaseF *os.File, client *adb.Client, cal *game.Calibration, ts *game.TemplateStore, logger zerolog.Logger) *instrumentation {
	return &instrumentation{
		outDir:    outDir,
		phaseF:    phaseF,
		client:    client,
		cal:       cal,
		templates: ts,
		logger:    logger,
	}
}

// onStep is the OnStep hook called by RunWallUpgradeLoop. Each OnStep
// call may carry a `screen` Mat; per the wall_upgrade.go contract that
// Mat is a clone owned by us, so this method closes it.
func (i *instrumentation) onStep(name string, data map[string]any) {
	i.count++
	if i.firstStep == "" {
		i.firstStep = name
	}
	i.lastStep = name

	stepNum := fmt.Sprintf("%02d", i.count)
	rawPath := filepath.Join(i.outDir, stepNum+"_"+name+".png")
	annoPath := filepath.Join(i.outDir, stepNum+"_"+name+"_overlay.png")

	// Honour payload contract: the screen Mat (if present) is a clone
	// owned by us — we close it after writing.
	hookScreen, haveScreen := data["screen"].(gocv.Mat)
	if haveScreen && !hookScreen.Empty() {
		gocv.IMWrite(rawPath, hookScreen)
		if matches, ok := data["matches"].([]vision.Match); ok && len(matches) > 0 {
			overlay := annotateOverlay(hookScreen, matches)
			gocv.IMWrite(annoPath, overlay)
			overlay.Close()
		}
		hookScreen.Close()
	} else if i.client.IsConnected() {
		// Fall back to a fresh capture if the payload didn't carry one.
		if fresh, err := i.client.CaptureToMat(); err == nil && !fresh.Empty() {
			gocv.IMWrite(rawPath, fresh)
			fresh.Close()
		}
	}

	// JSONL log entry — strip non-JSON-able values (gocv.Mat etc.).
	rec := map[string]any{
		"step":     name,
		"index":    i.count,
		"ts":       time.Now().Format(time.RFC3339Nano),
		"raw_png":  filepath.Base(rawPath),
		"anno_png": filepath.Base(annoPath),
	}
	cleanData := make(map[string]any, len(data))
	for k, v := range data {
		if _, isMat := v.(gocv.Mat); isMat {
			continue
		}
		cleanData[k] = v
	}
	if len(cleanData) > 0 {
		rec["data"] = cleanData
	}
	if data, err := json.Marshal(rec); err == nil {
		_, _ = i.phaseF.Write(append(data, '\n'))
	}

	fmt.Printf("   %s %-32s %s\n", stepNum, "["+name+"]", summarizePayload(cleanData))
}

// summarizePayload picks a few stable, summary-friendly fields out of a
// phase data map so the stdout trace stays compact.
func summarizePayload(d map[string]any) string {
	parts := []string{}
	for _, k := range []string{"count", "btn_idx", "attempt", "conf", "x", "y", "err", "is_red", "red_pixels", "matches"} {
		v, ok := d[k]
		if !ok {
			continue
		}
		switch k {
		case "err":
			parts = append(parts, fmt.Sprintf("err=%q", v))
		case "conf":
			if f, ok := v.(float64); ok {
				parts = append(parts, fmt.Sprintf("conf=%.2f", f))
			} else {
				parts = append(parts, fmt.Sprintf("conf=%v", v))
			}
		default:
			parts = append(parts, fmt.Sprintf("%s=%v", k, v))
		}
	}
	return strings.Join(parts, " ")
}

// annotateOverlay stamps each match as a colored bounding box on a clone
// of the input. Caller owns both input and output Mats.
func annotateOverlay(src gocv.Mat, matches []vision.Match) gocv.Mat {
	dst := src.Clone()
	for idx, m := range matches {
		col := color.RGBA{0, 255, 0, 255}
		if m.Confidence < 0.8 {
			col = color.RGBA{255, 165, 0, 255}
		}
		box := image.Rect(m.Point.X-30, m.Point.Y-30, m.Point.X+30, m.Point.Y+30)
		gocv.Rectangle(&dst, box, col, 2)
		gocv.PutText(&dst, fmt.Sprintf("#%d %.2f", idx+1, m.Confidence),
			image.Pt(m.Point.X+34, m.Point.Y),
			gocv.FontHersheyPlain, 1.0, col, 1)
	}
	return dst
}

// runPreflight verifies the prerequisites without touching game state.
// Returns true when everything checks out.
func runPreflight(client *adb.Client, logger zerolog.Logger, cfg *config.BotConfig) bool {
	ok := true

	// 1. adb reachable.
	if err := client.AutoDetectDevice(); err != nil {
		fmt.Printf("   ✗ adb device detection: %v\n", err)
		ok = false
	} else {
		fmt.Printf("   ✓ adb device: %s\n", client.DeviceID)
		if err := client.EnsureConnected(); err != nil {
			fmt.Printf("   ✗ adb connect: %v\n", err)
			ok = false
		} else {
			fmt.Printf("   ✓ adb connected\n")
		}
	}

	// 2. Screen size sane.
	if w, h, err := client.ScreenSize(); err != nil {
		fmt.Printf("   ✗ screen size: %v\n", err)
		ok = false
	} else {
		fmt.Printf("   ✓ screen size: %dx%d (scale %.2fx%.2f → ref %dx%d)\n",
			w, h,
			float64(w)/float64(game.RefWidth),
			float64(h)/float64(game.RefHeight),
			game.RefWidth, game.RefHeight)
		if w < 100 || h < 100 {
			fmt.Printf("   ✗ screen size too small — refusing\n")
			ok = false
		}
	}

	// 3. Templates: load and verify the required ones.
	ts, err := game.NewTemplateStore(paths.Resolve("templates"))
	if err != nil {
		fmt.Printf("   ✗ template store: %v\n", err)
		return false
	}
	if err := ts.LoadTemplates(); err != nil {
		fmt.Printf("   ✗ LoadTemplates: %v\n", err)
		return false
	}
	totalLoaded := ts.Count()
	loaded := 0
	var missing []string
	for _, name := range requiredTemplates {
		if _, ok := ts.Get(name); ok {
			loaded++
		} else {
			missing = append(missing, name)
		}
	}
	fmt.Printf("   %s templates: %d/%d of required loaded (%d total in store)\n",
		statusSymbol(loaded == len(requiredTemplates)), loaded, len(requiredTemplates), totalLoaded)
	if len(missing) > 0 {
		fmt.Printf("   ✗ missing templates: %v\n", missing)
		ok = false
	}

	// 4. builder_menu_roi.json optional but recommended.
	roiPath := paths.Resolve("builder_menu_roi.json")
	if data, err := os.ReadFile(roiPath); err == nil {
		var roi struct {
			Physical map[string]any `json:"physical"`
		}
		if json.Unmarshal(data, &roi) == nil {
			if x1, ok := roi.Physical["x1"].(float64); ok {
				if x2, ok := roi.Physical["x2"].(float64); ok {
					fmt.Printf("   ✓ builder menu ROI: x1=%.0f..x2=%.0f loaded (%d bytes)\n", x1, x2, len(data))
				}
			}
		} else {
			fmt.Printf("   ℹ builder menu ROI present but unparseable (%d bytes)\n", len(data))
		}
	} else {
		fmt.Printf("   ℹ builder menu ROI config absent (default ROI applies — fine)\n")
	}

	// 5. ADB recent capture health (best-effort; AvgCaptureMs is the running
	// avg of captured-screencap latency from any prior CaptureToMat calls
	// since the client opened — 0 on a fresh connection, that's expected).
	if hint := client.Health(); hint.AvgCaptureMs > 0 {
		fmt.Printf("   ✓ recent avg capture: %.1fms (consecutive_fails=%d)\n", hint.AvgCaptureMs, hint.ConsecutiveFails)
	}

	// 6. Config: Upgrade.UpgradeWalls flag.
	if cfg.Upgrade.UpgradeWalls {
		fmt.Printf("   ✓ cfg.upgrade.upgrade_walls = true\n")
	} else {
		fmt.Printf("   ℹ cfg.upgrade.upgrade_walls = false (this tool runs either way)\n")
	}

	return ok
}

// runDryRun captures the current device screen and tries template-matching
// against each required template on the whole screen. Saves annotated
// screenshots so the developer can see exactly what got matched.
func runDryRun(client *adb.Client, ts *game.TemplateStore, cal *game.Calibration, logger zerolog.Logger, outDir string) {
	fmt.Println("\n--- Dry Run ---")

	screen, err := client.CaptureToMat()
	if err != nil {
		fmt.Printf("   ✗ capture: %v\n", err)
		return
	}
	defer screen.Close()
	fmt.Printf("   captured: %dx%d\n", screen.Cols(), screen.Rows())

	gocv.IMWrite(filepath.Join(outDir, "00_dryrun_initial.png"), screen)

	fullROI := image.Rect(0, 0, screen.Cols(), screen.Rows())
	for _, name := range requiredTemplates {
		tpl, ok := ts.Get(name)
		if !ok {
			fmt.Printf("   ✗ %s — template missing\n", name)
			continue
		}
		matches, _ := vision.MatchMultiScaleROICached(screen, tpl, name, 0.3, 1.5, 60, 0.6, fullROI)
		if len(matches) == 0 {
			fmt.Printf("   ✗ %s — no match on current screen\n", name)
			continue
		}
		best := matches[0]
		fmt.Printf("   ✓ %s — best match conf=%.2f at (%d,%d) scale=%.2f\n",
			name, best.Confidence, best.Point.X, best.Point.Y, best.Scale)

		dst := screen.Clone()
		box := image.Rect(best.Point.X-30, best.Point.Y-30, best.Point.X+30, best.Point.Y+30)
		gocv.Rectangle(&dst, box, color.RGBA{0, 255, 0, 255}, 2)
		gocv.PutText(&dst, fmt.Sprintf("%s %.2f", name, best.Confidence),
			image.Pt(best.Point.X+34, best.Point.Y),
			gocv.FontHersheyPlain, 1.2, color.RGBA{0, 255, 0, 255}, 2)
		gocv.IMWrite(filepath.Join(outDir, fmt.Sprintf("dryrun_match_%s.png", name)), dst)
		dst.Close()
	}
}

// statusSymbol returns ✓ when ok else ✗.
func statusSymbol(ok bool) string {
	if ok {
		return "✓"
	}
	return "✗"
}

// attackButtonClassifier is a lightweight MainVillage guess keyed off
// btn_attack template presence + the orange pinpoint color check. It is
// NOT a real game-state classifier — the only state we need to be sure
// of before the wall loop runs is MainVillage. If it returns the wrong
// state the loop will just retry or fail-fast. The name is deliberately
// longer/descriptive so JSONL phase logs don't mislead.
func attackButtonClassifier(client *adb.Client, cal *game.Calibration, ts *game.TemplateStore, logger zerolog.Logger) func(gocv.Mat) (game.GameState, int) {
	return func(screen gocv.Mat) (game.GameState, int) {
		if tpl, ok := ts.Get("btn_attack"); ok {
			roi := image.Rect(0, int(500*cal.ScaleY), int(300*cal.ScaleX), screen.Rows())
			matches, err := vision.MatchMultiScaleROICached(screen, tpl, "btn_attack", 0.3, 1.5, 5, 0.5, roi)
			if err == nil && len(matches) > 0 {
				// Bonus: confirm the attack button's signature orange.
				if isOrangeSpot(screen, matches[0].Point.X, matches[0].Point.Y) {
					return game.StateMainVillage, int(matches[0].Confidence * 100)
				}
				// Template matched but color is wrong (false positive).
				return game.StateUnknown, 0
			}
		}
		return game.StateUnknown, 0
	}
}

// isOrangeSpot is the same broad-range orange check Bot.isOrange uses
// against the Attack button. Implemented inline so the diagnostic tool
// has no dependency on the Bot struct.
func isOrangeSpot(screen gocv.Mat, x, y int) bool {
	if x < 0 || y < 0 || x+10 >= screen.Cols() || y+10 >= screen.Rows() {
		return false
	}
	region := image.Rect(x-10, y-10, x+11, y+11)
	region.Min.X = maxInt(region.Min.X, 0)
	region.Min.Y = maxInt(region.Min.Y, 0)
	region.Max.X = minInt(region.Max.X, screen.Cols())
	region.Max.Y = minInt(region.Max.Y, screen.Rows())
	sub := screen.Region(region)
	defer sub.Close()

	// CoC attack-button orange: BGR ~ (0, 175, 255). Broad band for safety.
	lower := gocv.NewScalar(0, 100, 150, 0)
	upper := gocv.NewScalar(150, 255, 255, 0)
	mask := gocv.NewMat()
	defer mask.Close()
	gocv.InRangeWithScalar(sub, lower, upper, &mask)
	return gocv.CountNonZero(mask) > 20
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// dismissHelper mirrors Bot.dismissSelection — taps a neutral
// bottom-left area to clear any active selection menus.
func dismissHelper(client *adb.Client, cal *game.Calibration, logger zerolog.Logger) {
	x, y := cal.ScaleRef(50, 450)
	_ = client.Tap(x, y)
	time.Sleep(500 * time.Millisecond)
	logger.Debug().Int("x", x).Int("y", y).Msg("dismiss tap")
}

// adbLogAdapter adapts zerolog to adb.Logger.
type adbLogAdapter struct {
	log zerolog.Logger
}

func (a *adbLogAdapter) Debug() bool { return a.log.GetLevel() <= zerolog.DebugLevel }
func (a *adbLogAdapter) Debugf(format string, v ...any) {
	a.log.Debug().Msgf(format, v...)
}
func (a *adbLogAdapter) Info(msg string)  { a.log.Info().Msg(msg) }
func (a *adbLogAdapter) Warn(msg string)  { a.log.Warn().Msg(msg) }
func (a *adbLogAdapter) Error(msg string) { a.log.Error().Msg(msg) }
func (a *adbLogAdapter) WithFields(fields map[string]any) adb.Logger {
	return &adbLogAdapter{log: a.log.With().Fields(fields).Logger()}
}

// newLogger returns a zerolog logger writing to stderr at Info level.
func newLogger() zerolog.Logger {
	zerolog.TimeFieldFormat = time.RFC3339Nano
	return zerolog.New(os.Stderr).With().Timestamp().Logger()
}

// printFinalCheckSummary prints a one-liner after the check-only path.
func printFinalCheckSummary() {
	fmt.Println("\n--- Summary ---")
	fmt.Println("  Re-run with -mode=dry-run to verify templates on a representative screen,")
	fmt.Println("  or -mode=run to actually invoke the wall-upgrade loop.")
}

// printFinalDryRunSummary prints a one-liner after dry-run path.
func printFinalDryRunSummary(outDir string) {
	fmt.Println("\n--- Summary ---")
	fmt.Printf("  Screenshots saved to: %s\n", outDir)
	fmt.Println("  Re-run with -mode=run -yes to invoke the live loop.")
}
