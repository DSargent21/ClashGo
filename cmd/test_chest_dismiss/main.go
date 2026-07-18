// Command test_chest_dismiss is a one-shot driver for the chest-reward
// recovery loop. It connects to ADB, verifies the current screen is
// in StateChestReward, runs Navigator.DismissChestReward, and verifies
// the screen has cleared. Prints tap count, capture count, wall-clock
// time, and the final state for fast iteration when tuning tap zones
// in assets/chest_dismiss_roi.json.
//
// Typical workflow:
//
//	# 1. capture + drag two tap zones (chest box + hammers).
//	go run cmd/pick_chest_roi/main.go -also-alt
//	# 2. position the emulator on the chest screen.
//	# 3. verify recovery works in isolation.
//	go run cmd/test_chest_dismiss/main.go
//
// Flags:
//
//	-adb-host    ADB host (default 127.0.0.1)
//	-adb-port    ADB port (default 5037)
//	-device      ADB device id (default emulator-5554)
//	-timeout     ADB operation timeout (default 30s)
//	-fast-anim   shrink ChestAnimSettle to 200ms for fast iteration
//	             (DO NOT use in production)
//	-dry-run     verify classifier sees chest, do not tap
//	-skip-config load a hand-built config from this path instead of
//	             assets/chest_dismiss_roi.json (test-only)
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math"
	"os"
	"time"

	"github.com/Ducky705/ClashGO/internal/adb"
	"github.com/Ducky705/ClashGO/internal/game"
	"github.com/Ducky705/ClashGO/internal/paths"
	"github.com/rs/zerolog"
	"gocv.io/x/gocv"
)

func main() {
	var (
		adbHost    = flag.String("adb-host", "127.0.0.1", "ADB host")
		adbPort    = flag.Int("adb-port", 5037, "ADB port")
		deviceID   = flag.String("device", "emulator-5554", "ADB device id")
		timeout    = flag.Duration("timeout", 30*time.Second, "ADB operation timeout")
		fastAnim   = flag.Bool("fast-anim", false, "shrink ChestAnimSettle to 200ms for fast iteration (test-only)")
		dryRun     = flag.Bool("dry-run", false, "verify classifier sees StateChestReward, do not tap")
		skipConfig = flag.String("skip-config", "", "load a hand-built ChestROISchema from this JSON path instead of the default")
		probe      = flag.Bool("probe", false, "dump screen + print StateChestReward PixelCheck diagnostics, no taps fired")
		dumpDir    = flag.String("dump-dir", "/tmp", "directory to write the screen dump PNG into (used by -probe)")
		force      = flag.Bool("force", false, "bypass the StateChestReward pre-flight check; run DismissChestReward regardless. Dangerous: taps blindly if the chest isn't actually on screen.")
	)
	flag.Parse()

	zl := zerolog.New(zerolog.ConsoleWriter{Out: os.Stderr, TimeFormat: time.RFC3339}).
		Level(zerolog.InfoLevel)

	// 1. Load the chest dismiss config (default or override).
	cfg := mustLoadConfig(*skipConfig, zl)
	printConfig(cfg)

	// 2. Connect to ADB and read the screen size for calibration.
	client := adb.NewClient(
		adb.WithHost(*adbHost),
		adb.WithPort(*adbPort),
		adb.WithTimeout(*timeout),
	)
	client.DeviceID = *deviceID
	if err := client.Connect(); err != nil {
		log.Fatalf("adb connect: %v", err)
	}
	// dev.Close() (set up below) forwards to client.Close() AND
	// releases the captured Mats tracked in dev.allMats. We MUST
	// Close via the wrapper, not the bare client, otherwise the
	// Mats leak — gocv doesn't finalize them and a leak here floors
	// the RSS over many test runs.

	sw, sh, err := client.ScreenSize()
	if err != nil {
		log.Fatalf("screen size: %v", err)
	}
	cal := &game.Calibration{
		PhysicalW:  sw,
		PhysicalH:  sh,
		ScaleX:     float64(sw) / float64(game.RefWidth),
		ScaleY:     float64(sh) / float64(game.RefHeight),
		MidOffsetY: (sh - game.RefHeight) / 2,
		BottomOffY: sh - game.RefHeight,
		Verified:   true,
	}
	fmt.Printf("→ Calibration: %dx%d physical, scale=(%.3f, %.3f)\n",
		sw, sh, cal.ScaleX, cal.ScaleY)

	// 3. Build the classifier. Templates are best-effort — if they're
	//    missing the classifier still has color/pixel rules and the
	//    chest-reward StateRule will fire on its own merits.
	var templates *game.TemplateStore
	if ts, terr := game.NewTemplateStore(paths.Resolve("templates")); terr == nil {
		_ = ts.LoadTemplates()
		templates = ts
		zl.Info().Int("templates", templates.Count()).Msg("template store loaded")
	} else {
		zl.Warn().Err(terr).Msg("template store unavailable; classifier will rely on pixel rules")
	}

	classifier := game.NewClassifier(cal, game.DefaultClassifierConfig(), zl)
	if templates != nil {
		classifier.SetTemplates(templates)
	}
	classify := func(mat gocv.Mat) (game.GameState, int) {
		return classifier.ClassifyState(mat)
	}

	// 4. Pre-flight: capture + classify to confirm we're on the chest
	//    screen. Bail with a clear message if not, so the user can
	//    re-stage the emulator instead of burning the budget on a
	//    misconfigured device.
	fmt.Println("→ Capturing pre-flight screen...")
	preScreen, err := client.CaptureToMat()
	if err != nil {
		log.Fatalf("pre-flight capture: %v", err)
	}
	preState, preScore := classify(preScreen)
	fmt.Printf("→ Pre-flight state: %s (score=%d)\n", preState, preScore)

	// -probe path: dump screen + walk the StateChestReward rule's
	// PixelCheck coords, printing actual RGB vs expected RGB and
	// pass/fail per check. Exits before any taps so the user can
	// iterate on the rule without burning the chest budget.
	if *probe {
		dumpPath := fmt.Sprintf("%s/chest_preflight_%s.png", *dumpDir, time.Now().Format("20060102_150405"))
		if gocv.IMWrite(dumpPath, preScreen) {
			fmt.Printf("→ Screen dumped to: %s\n", dumpPath)
		} else {
			fmt.Fprintf(os.Stderr, "warning: failed to write screen dump to %s\n", dumpPath)
		}
		probeChestRewardRule(preScreen, classifier, cal)
		preScreen.Close()
		return
	}
	preScreen.Close()

	if preState != game.StateChestReward {
		if *force {
			fmt.Fprintf(os.Stderr, "⚠ -force set: classifier said %s (score=%d) but running DismissChestReward anyway.\n", preState, preScore)
			fmt.Fprintln(os.Stderr, "  Taps will fire blindly. Verify on-screen behavior before relying on this.")
		} else {
			log.Fatalf("expected StateChestReward, got %s — re-run with -probe to dump the screen + rule diagnostics, or -force to bypass this check", preState)
		}
	}

	if *dryRun {
		fmt.Println("✓ Dry-run OK: classifier identifies chest screen. (No taps fired.)")
		return
	}

	// 5. Optionally shrink the chest settle window for fast iteration.
	//    Restored on return so a misclicked `-fast-anim` doesn't
	//    poison subsequent production runs of the bot in the same
	//    process (this CLI exits anyway, but keep the pattern tidy).
	if *fastAnim {
		origAnim := game.ChestAnimSettle
		origSkip := game.ChestSkipConfirmSettle
		game.ChestAnimSettle = 200 * time.Millisecond
		game.ChestSkipConfirmSettle = 200 * time.Millisecond
		defer func() {
			game.ChestAnimSettle = origAnim
			game.ChestSkipConfirmSettle = origSkip
		}()
		fmt.Println("→ Fast-anim mode: ChestAnimSettle=200ms (test-only)")
	}

	// 6. Wrap the adb client in a countingDevice so we can report
	//    diagnostic counts after the run.
	dev := &countingDevice{inner: client}
	defer dev.Close()

	// 7. Build the navigator and run DismissChestReward.
	graph := game.NewStateGraph()
	graph.AddNode(game.StateMainVillage)
	nav := game.NewNavigator(dev, cal, graph, classify, zl)
	if templates != nil {
		nav.SetTemplates(templates)
	}

	fmt.Println("→ Running DismissChestReward...")
	start := time.Now()
	dismissErr := nav.DismissChestReward()
	elapsed := time.Since(start)

	fmt.Printf("→ Wall-clock: %s\n", elapsed.Round(time.Millisecond))
	fmt.Printf("→ Taps fired: %d\n", dev.tapCount)
	fmt.Printf("→ Captures:   %d\n", dev.capCount)

	if dismissErr != nil {
		fmt.Fprintf(os.Stderr, "✗ DismissChestReward FAILED: %v\n", dismissErr)
		fmt.Fprintln(os.Stderr, "  Hint: re-run cmd/pick_chest_roi/main.go -also-alt and re-drag the tap zones,")
		fmt.Fprintln(os.Stderr, "  or check the classifier logs for repeated StateChestReward mis-detections.")
		os.Exit(1)
	}
	fmt.Println("✓ DismissChestReward returned nil")

	// 8. Post-flight verify: re-capture and confirm the classifier
	//    no longer sees the chest screen. The DismissChestReward
	//    loop already does this, but a second sample after the call
	//    returns protects against transient flicker (the screen
	//    could be animating the dismissal out as we return).
	time.Sleep(500 * time.Millisecond)
	postScreen, err := client.CaptureToMat()
	if err != nil {
		log.Fatalf("post-flight capture: %v", err)
	}
	postState, postScore := classify(postScreen)
	postScreen.Close()
	fmt.Printf("→ Post-flight state: %s (score=%d)\n", postState, postScore)

	if postState == game.StateChestReward {
		fmt.Fprintln(os.Stderr, "✗ FAIL: classifier still sees StateChestReward after DismissChestReward returned nil.")
		fmt.Fprintln(os.Stderr, "  This usually means the screen is mid-transition — re-capture in 1s.")
		os.Exit(2)
	}
	fmt.Printf("✓ Confirmed: state changed from %s → %s\n", preState, postState)
}

// countingDevice wraps a Device and tallies tap + capture calls so the
// test script can report diagnostic counts after DismissChestReward
// returns. Each captured Mat is tracked for Close on shutdown — gocv
// doesn't finalize Mats and a leak here would floor the RSS over many
// test runs.
type countingDevice struct {
	inner    game.Device
	tapCount int
	capCount int
	allMats  []gocv.Mat
}

func (c *countingDevice) Tap(x, y int) error { c.tapCount++; return c.inner.Tap(x, y) }
func (c *countingDevice) TapRandomized(x, y int) error {
	c.tapCount++
	return c.inner.TapRandomized(x, y)
}
func (c *countingDevice) Swipe(x1, y1, x2, y2, ms int) error {
	return c.inner.Swipe(x1, y1, x2, y2, ms)
}
func (c *countingDevice) Pinch(x1, y1, x2, y2, x3, y3, x4, y4, ms int) error {
	return c.inner.Pinch(x1, y1, x2, y2, x3, y3, x4, y4, ms)
}
func (c *countingDevice) PinchZoom(zoomOut bool) error { return c.inner.PinchZoom(zoomOut) }
func (c *countingDevice) ZoomOut() error               { return c.inner.ZoomOut() }
func (c *countingDevice) ZoomIn() error                { return c.inner.ZoomIn() }
func (c *countingDevice) Hold(x, y, ms int) error      { return c.inner.Hold(x, y, ms) }
func (c *countingDevice) KeyEvent(code int) error      { return c.inner.KeyEvent(code) }
func (c *countingDevice) Text(text string) error       { return c.inner.Text(text) }
func (c *countingDevice) Back() error                  { return c.inner.Back() }
func (c *countingDevice) CaptureToMat() (gocv.Mat, error) {
	m, err := c.inner.CaptureToMat()
	if err == nil {
		c.capCount++
		c.allMats = append(c.allMats, m)
	}
	return m, err
}
func (c *countingDevice) Close() error {
	for _, m := range c.allMats {
		if !m.Empty() {
			m.Close()
		}
	}
	c.allMats = nil
	return c.inner.Close()
}

// mustLoadConfig loads ChestROISchema from `-skip-config` if provided,
// else from the default assets/chest_dismiss_roi.json. Empty result
// from the default path means the file is absent and we fall back to
// the center-screen default (matching production behavior).
func mustLoadConfig(skipConfig string, zl zerolog.Logger) *game.ChestROISchema {
	if skipConfig != "" {
		raw, err := os.ReadFile(skipConfig)
		if err != nil {
			log.Fatalf("read -skip-config %s: %v", skipConfig, err)
		}
		cfg := &game.ChestROISchema{}
		if err := json.Unmarshal(raw, cfg); err != nil {
			log.Fatalf("parse -skip-config %s: %v", skipConfig, err)
		}
		zl.Info().Str("path", skipConfig).Msg("loaded override chest config")
		return cfg
	}
	cfg, err := game.LoadChestDismissConfig()
	if err != nil {
		log.Fatalf("load chest dismiss config: %v", err)
	}
	if cfg == nil {
		// Mirror production: silently fall back to the center-screen
		// default rather than failing the test.
		zl.Warn().Msg("no chest_dismiss_roi.json found; using center-screen default")
		cfg = &game.ChestROISchema{
			TapROI: &game.Rectangle{X1: 330, Y1: 280, X2: 530, Y2: 480},
		}
	}
	return cfg
}

func printConfig(cfg *game.ChestROISchema) {
	fmt.Println("→ Chest ROI config:")
	if cfg.TapROI != nil {
		fmt.Printf("    tap_roi            = %s\n", formatRect(cfg.TapROI))
	}
	if cfg.TapROIAlt != nil {
		fmt.Printf("    tap_roi_alt        = %s\n", formatRect(cfg.TapROIAlt))
	}
	if cfg.SkipButton != nil {
		fmt.Printf("    skip_button        = %s (Skip+Confirm fast path ENABLED if confirm also set)\n", formatRect(cfg.SkipButton))
	}
	if cfg.ConfirmYesButton != nil {
		fmt.Printf("    confirm_yes_button = %s\n", formatRect(cfg.ConfirmYesButton))
	}
	if cfg.SkipButton != nil && cfg.ConfirmYesButton != nil {
		fmt.Println("    path: Skip+Confirm fast → tap-scan fallback")
	} else {
		fmt.Println("    path: tap-scan only (no Skip+Confirm fast path)")
	}
}

func formatRect(r *game.Rectangle) string {
	return fmt.Sprintf("(%d,%d)-(%d,%d)", r.X1, r.Y1, r.X2, r.Y2)
}

// probeChestRewardRule walks the StateChestReward rule's PixelChecks
// against a captured screen and prints each check's actual vs expected
// RGB + euclidean distance + pass/fail. Mirrors the read logic in
// Classifier.ClassifyState (cal.ScaleRef → GetUCharAt) so the output
// exactly reflects what the classifier saw when it returned Unknown.
//
// Output format (one line per check):
//
//	[idx] ref(x,y) phys(x,y) expected=0xRR,0xGG,0xBB tol=N  actual=0xRR,0xGG,0xBB  dist=N.N  PASS|FAIL
//
// At the end prints a summary: passed K / N (MinPass=M) plus the
// classifier's verdict (will detect vs will NOT detect chest).
func probeChestRewardRule(screen gocv.Mat, classifier *game.Classifier, cal *game.Calibration) {
	fmt.Println("→ StateChestReward rule diagnostics:")
	rules := classifier.GetRules()
	found := false
	for _, rule := range rules {
		if rule.State != game.StateChestReward {
			continue
		}
		found = true
		fmt.Printf("  MinPass=%d, %d checks, weight=%d, priority=%d\n",
			rule.MinPass, len(rule.Checks), rule.Weight, rule.Priority)
		passed := 0
		for i, chk := range rule.Checks {
			sx, sy := cal.ScaleRef(chk.X, chk.Y)
			if sx < 0 || sy < 0 || sx >= screen.Cols() || sy >= screen.Rows() {
				fmt.Printf("    [%d] ref(%d,%d) phys=OFF-SCREEN  expected=0x%02X,0x%02X,0x%02X tol=%d  SKIP\n",
					i, chk.X, chk.Y, chk.R, chk.G, chk.B, chk.Tolerance)
				continue
			}
			b := screen.GetUCharAt(sy, sx*3)
			g := screen.GetUCharAt(sy, sx*3+1)
			r := screen.GetUCharAt(sy, sx*3+2)
			dr := absDiffInt(int(r), int(chk.R))
			dg := absDiffInt(int(g), int(chk.G))
			db := absDiffInt(int(b), int(chk.B))
			dist := math.Sqrt(float64(dr*dr + dg*dg + db*db))
			status := "FAIL"
			if dist <= float64(chk.Tolerance) {
				status = "PASS"
				passed++
			}
			fmt.Printf("    [%d] ref(%d,%d) phys(%d,%d)  expected=0x%02X,0x%02X,0x%02X tol=%d  actual=0x%02X,0x%02X,0x%02X  dist=%.1f  %s\n",
				i, chk.X, chk.Y, sx, sy,
				chk.R, chk.G, chk.B, chk.Tolerance,
				r, g, b, dist, status)
		}
		fmt.Printf("  → passed %d / %d (MinPass=%d)\n", passed, len(rule.Checks), rule.MinPass)
		if passed >= rule.MinPass {
			fmt.Printf("  ✓ Rule PASSES — classifier will detect chest.\n")
		} else {
			fmt.Printf("  ✗ Rule FAILS — classifier returns Unknown until the rule is re-tuned.\n")
			fmt.Printf("  → To fix: edit the PixelChecks in internal/game/classifier.go (StateChestReward rule).\n")
			fmt.Printf("    Replace the failing checks' expected RGB values with the actual values above.\n")
			fmt.Printf("    A tolerance bump (e.g. 30→60) can also help if the icon color varies slightly.\n")
		}
	}
	if !found {
		fmt.Println("  ✗ No StateChestReward rule found in classifier!")
	}
}

func absDiffInt(a, b int) int {
	if a > b {
		return a - b
	}
	return b - a
}
