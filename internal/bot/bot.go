package bot

import (
	"context"
	"fmt"
	"image"
	"os/exec"
	"runtime"
	"sync/atomic"
	"time"

	"gocv.io/x/gocv"

	"github.com/Ducky705/ClashGo/internal/adb"
	"github.com/Ducky705/ClashGo/internal/attack"
	"github.com/Ducky705/ClashGo/internal/config"
	"github.com/Ducky705/ClashGo/internal/game"
	"github.com/Ducky705/ClashGo/internal/training"
	"github.com/Ducky705/ClashGo/internal/vision"
	"github.com/Ducky705/ClashGo/pkg/strategy"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

type Bot struct {
	client    *adb.Client
	cal       *game.Calibration
	classifier *game.Classifier
	navigator *game.Navigator
	graph     *game.StateGraph
	templates *game.TemplateStore
	recognizer *game.Recognizer
	cfg       *config.BotConfig

	classify func(gocv.Mat) (game.GameState, int)

	attackExec  *attack.Executor
	trainer    *training.Trainer

	ctx        context.Context
	cancel     context.CancelFunc
	logger     zerolog.Logger

	attackCount atomic.Int32
	totalGold   atomic.Int64
	totalElixir atomic.Int64
	totalDE     atomic.Int64
	totalStars  atomic.Int32
	stars0      atomic.Int32
	stars1      atomic.Int32
	stars2      atomic.Int32
	stars3      atomic.Int32
	seqRunning  atomic.Bool
	zoomedOut   atomic.Bool
	startedAt   time.Time
}

func NewBot(cfg *config.BotConfig) (*Bot, error) {
	zl := &adbLogAdapter{log: log.Logger}

	client := adb.NewClient(
		adb.WithHost(cfg.Device.ADBHost),
		adb.WithPort(cfg.Device.ADBPort),
		adb.WithLogger(zl),
		adb.WithTimeout(30*time.Second),
	)
	client.DeviceID = cfg.Device.DeviceID

	log.Info().Msg("initializing bot startup sequence...")

	// Try to connect first, launch if fails
	if err := client.Connect(); err != nil {
		log.Warn().Err(err).Msg("initial ADB connection failed")

		if runtime.GOOS == "darwin" {
			log.Info().Msg("attempting to launch BlueStacks...")
			// Use 'open -a BlueStacks' as it's the standard way to launch apps on macOS
			if err := exec.Command("open", "-a", "BlueStacks").Run(); err != nil {
				log.Error().Err(err).Msg("failed to launch BlueStacks via 'open' command")
			}

			// Wait for ADB server and device to become available
			log.Info().Msg("waiting for ADB connection (up to 90s)...")
			deadline := time.Now().Add(90 * time.Second)
			connected := false
			for time.Now().Before(deadline) {
				if err := client.Reconnect(); err == nil {
					connected = true
					break
				}
				time.Sleep(3 * time.Second)
			}
			if !connected {
				return nil, fmt.Errorf("timeout waiting for ADB connection (localhost:5037 -> localhost:5555)")
			}
			log.Info().Msg("ADB connected successfully")
		} else {
			return nil, fmt.Errorf("ADB connect failed and automatic launch only supported on macOS: %w", err)
		}
	} else {
		log.Info().Msg("ADB connected successfully (already running)")
	}

	// Ensure system is booted
	log.Info().Msg("waiting for Android system to report 'boot_completed'...")
	if err := client.WaitForBoot(90 * time.Second); err != nil {
		return nil, fmt.Errorf("android boot timeout: %w", err)
	}
	log.Info().Msg("Android system is ready")

	// Ensure game is started
	log.Info().Msg("launching Clash of Clans...")
	packageName := cfg.Device.PackageName
	if packageName == "" {
		packageName = "com.supercell.clashofclans"
	}
	
	// Professional approach: Restart the game on launch to clear state
	log.Info().Str("package", packageName).Msg("restarting game for clean state...")
	
	// Try multiple times to ensure it stops
	for i := 0; i < 3; i++ {
		if err := client.ForceStop(packageName); err != nil {
			log.Warn().Err(err).Int("attempt", i).Msg("failed to force stop game")
		}
		time.Sleep(2 * time.Second)
	}
	
	activity := packageName + "/com.supercell.titan.GameApp"
	log.Info().Str("activity", activity).Msg("starting game activity...")
	if err := client.StartActivity(activity); err != nil {
		log.Error().Err(err).Str("activity", activity).Msg("failed to start game activity")
		return nil, fmt.Errorf("failed to start game activity: %w", err)
	}
	log.Info().Str("package", packageName).Msg("game launch intent sent successfully")

	// Brief wait for game to start rendering, then rely on template polling
	log.Info().Msg("waiting 5s for game to initialize...")
	time.Sleep(5 * time.Second)

	log.Info().Msg("starting calibration...")
	calibrator := game.NewCalibrator(client)
	cal, err := calibrator.Calibrate()
	if err != nil {
		return nil, fmt.Errorf("calibrate: %w", err)
	}

	classifier := game.NewClassifier(cal, game.DefaultClassifierConfig(), log.Logger)
	classify := func(mat gocv.Mat) (game.GameState, int) {
		return classifier.ClassifyState(mat)
	}

	graph := game.NewStateGraph()
	graph.AddNode(game.StateMainVillage)

	navigator := game.NewNavigator(client, cal, graph, classify, log.Logger)

	attackExec := attack.NewExecutor(client, cal, &cfg.Attack, log.Logger)
	attackExec.SetClassifier(classify)

	trainer := training.NewTrainer(client, cal, &cfg.Training, log.Logger)
	trainer.SetClassifier(classify)

	var templates *game.TemplateStore
	templates, err = game.NewTemplateStore("assets/templates")
	if err != nil {
		log.Warn().Err(err).Msg("template store init failed, continuing without templates")
		templates = nil
	}

	if templates != nil {
		templates.LoadTemplates()
		log.Info().Int("templates", templates.Count()).Msg("templates loaded")
		classifier.SetTemplates(templates)
		trainer.SetTemplates(templates)
		navigator.SetTemplates(templates)
	}

	// Professional Zoom Out on first launch - moved to main loop to ensure village is loaded
	// navigator.ZoomOut()

	recognizer := game.NewRecognizer()
	ctx, cancel := context.WithCancel(context.Background())

	b := &Bot{
		client:     client,
		cal:        cal,
		graph:      graph,
		templates:  templates,
		recognizer: recognizer,
		cfg:        cfg,
		attackExec: attackExec,
		trainer:    trainer,
		ctx:        ctx,
		cancel:     cancel,
		logger:     log.With().Str("bot", "orchestrator").Logger(),
		startedAt:  time.Now(),
	}

	b.classifier = game.NewClassifier(cal, game.DefaultClassifierConfig(), b.logger)
	b.classify = func(mat gocv.Mat) (game.GameState, int) {
		return b.classifier.ClassifyState(mat)
	}
	b.navigator = game.NewNavigator(client, cal, graph, b.classify, b.logger)
	b.attackExec.SetClassifier(b.classify)
	b.trainer.SetClassifier(b.classify)

	return b, nil
}

func (b *Bot) Start() error {
	if err := b.client.EnsureConnected(); err != nil {
		return fmt.Errorf("ensure connect: %w", err)
	}

	sw, sh, err := b.client.ScreenSize()
	if err != nil {
		b.logger.Warn().Err(err).Msg("could not get screen size")
	} else {
		b.logger.Info().
			Str("device", b.cfg.Device.DeviceID).
			Str("resolution", fmt.Sprintf("%dx%d", sw, sh)).
			Str("scale", fmt.Sprintf("%.3fx%.3f", b.cal.ScaleX, b.cal.ScaleY)).
			Msg("connected")
	}

	go b.captureLoop()
	return nil
}

func (b *Bot) Stop() {
	b.cancel()
	b.client.Close()
}

func (b *Bot) captureLoop() {
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()

	gc := game.NewGameContext()

	for {
		select {
		case <-b.ctx.Done():
			return
		case <-ticker.C:
			b.processFrame(gc)
		}
	}
}

func (b *Bot) processFrame(gc *game.GameContext) {
	start := time.Now()

	screen, err := b.client.CaptureToMat()
	if err != nil {
		gc.RecordCaptureError()
		b.logger.Debug().Err(err).Msg("capture failed")
		return
	}

	state, score := b.classify(screen)
	captureMs := time.Since(start)

	gc.UpdateScreen(screen, captureMs)

	if gc.ConfirmState(state) {
		now := time.Now()
		gc.UpdateState(state, now)

		select {
		case gc.StateChange <- game.StateChange{From: gc.PrevState(), To: state, At: now}:
		default:
		}

		b.logger.Debug().
			Str("state", state.String()).
			Int("score", score).
			Msg("state detected")
	}

	// Professional Zoom Out on first detection of Main Village or Attack button
	if (gc.State == game.StateMainVillage || b.templateMatch(screen, "btn_attack", 0.6)) && !b.zoomedOut.Load() {
		if b.zoomedOut.CompareAndSwap(false, true) {
			b.logger.Info().Msg("main village elements detected, performing initial zoom out...")
			b.navigator.ZoomOut()
			// Wait for zoom animation to settle
			time.Sleep(2000 * time.Millisecond)
			// Return here so the next loop iteration captures a fresh screen after zoom
			return
		}
	}

	if b.seqRunning.Load() {
		return
	}

	// Primary detection: try to find the attack button via template matching
	if b.findAttackButton(screen, 0.45) {
		b.logger.Info().Msg("attack button detected, starting sequence")
		go b.executeAttackSequence(gc)
		return
	}

	// Classifier-based fallback: only with cooldown to prevent spamming
	if gc.State == game.StateArmyCamp && time.Since(lastNav) > 3*time.Second {
		lastNav = time.Now()
		b.logger.Info().Msg("in ArmyCamp, returning to main village...")
		go b.navigator.NavigateToMainVillage(gc)
		return
	}
}

func (b *Bot) findAttackButton(screen gocv.Mat, threshold float32) bool {
	// Step 1: Check a precise pinpoint location first for speed and accuracy.
	// In the Main Village, the Attack button center is remarkably consistent.
	// Ref (860x732) -> (60, 695) is the sweet spot (center-top of the orange area).
	pinX, pinY := b.cal.ScaleRef(60, 695)
	if b.isOrange(screen, pinX, pinY) {
		b.logger.Debug().Msg("attack button confirmed via pinpoint color check")
		return true
	}

	// Step 2: Fallback to template matching in the bottom-left ROI if pinpoint fails.
	tpl, ok := b.templates.Get("btn_attack")
	if !ok {
		return false
	}

	// ROI: Bottom-left quadrant
	roi := image.Rect(0, 500, 300, 732)
	physROI := image.Rect(
		int(float64(roi.Min.X)*b.cal.ScaleX),
		int(float64(roi.Min.Y)*b.cal.ScaleY),
		int(float64(roi.Max.X)*b.cal.ScaleX),
		int(float64(roi.Max.Y)*b.cal.ScaleY),
	)

	matches, err := vision.MatchMultiScaleROI(screen, tpl, 0.2, 2.0, 30, threshold, physROI)
	if err != nil || len(matches) == 0 {
		if err != nil {
			b.logger.Debug().Err(err).Msg("btn_attack template match error")
		}
		return false
	}

	best := matches[0]
	isOrange := b.isOrange(screen, best.Point.X, best.Point.Y)
	
	b.logger.Debug().
		Float64("conf", best.Confidence).
		Int("x", best.Point.X).
		Int("y", best.Point.Y).
		Bool("is_orange", isOrange).
		Msg("attack button detection check")

	if !isOrange {
		return false
	}

	return true
}

func (b *Bot) isOrange(screen gocv.Mat, x, y int) bool {
	if x < 0 || y < 0 || x >= screen.Cols() || y >= screen.Rows() {
		return false
	}
	// Sample a small area around the point for color robustness
	region := image.Rect(x-10, y-10, x+11, y+11)
	if region.Min.X < 0 { region.Min.X = 0 }
	if region.Min.Y < 0 { region.Min.Y = 0 }
	if region.Max.X > screen.Cols() { region.Max.X = screen.Cols() }
	if region.Max.Y > screen.Rows() { region.Max.Y = screen.Rows() }

	sub := screen.Region(region)
	defer sub.Close()

	// Broad Attack button orange range (BGR)
	// CoC orange: R=255, G=175, B=0
	// We allow a wide range for emulator differences
	lower := gocv.NewScalar(0, 100, 150, 0)
	upper := gocv.NewScalar(150, 255, 255, 0)

	mask := gocv.NewMat()
	defer mask.Close()
	gocv.InRangeWithScalar(sub, lower, upper, &mask)

	return gocv.CountNonZero(mask) > 20 // At least 20 pixels in the 21x21 area match
}

var lastNav time.Time

func (b *Bot) templateMatch(screen gocv.Mat, name string, threshold float32) bool {
	tpl, ok := b.templates.Get(name)
	if !ok {
		return false
	}
	// Use wider scale range for all bot-level template matching
	matches, err := vision.MatchMultiScale(screen, tpl, 0.2, 2.0, 30, threshold)
	if err != nil {
		return false
	}
	return len(matches) > 0
}

func (b *Bot) executeAttackSequence(gc *game.GameContext) {
	if !b.seqRunning.CompareAndSwap(false, true) {
		return
	}
	defer b.seqRunning.Store(false)

	if b.attackCount.Load() >= int32(b.cfg.Attack.MaxAttackPerSession) {
		return
	}

	if !b.clickSequence() {
		b.logger.Warn().Msg("attack click sequence failed, recovering to main village")
		b.navigator.NavigateToMainVillage(gc)
		time.Sleep(2 * time.Second)
		return
	}

	b.logger.Info().Msg("waiting for base to be found...")

	lootRec := game.NewLootRecognizer(b.cal, b.templates, b.logger)

	for {
		// High-Speed Loop: reduced sleep for faster cycling
		time.Sleep(1200 * time.Millisecond)

		screen, err := b.client.CaptureToMat()
		if err != nil {
			return
		}

		state, _ := b.classify(screen)
		if state != game.StateBattle {
			if state == game.StateSearchMap || state == game.StateLoading {
				b.logger.Debug().Str("state", state.String()).Msg("still searching (clouds)...")
				screen.Close()
				continue
			}
			b.logger.Debug().Str("state", state.String()).Msg("unexpected state during search, checking interruptions")
			// Unexpected state, check interruptions but keep moving
			b.dismissInterruptions()
			screen.Close()
			continue
		}

		b.logger.Info().Msg("base found, reading loot...")
		loot, err := lootRec.ReadAvailableLoot(screen)
		if err != nil {
			b.logger.Warn().Err(err).Msg("failed to read loot")
		}

		b.logger.Info().
			Int("gold", loot.Gold).
			Int("elixir", loot.Elixir).
			Int("de", loot.DarkElixir).
			Msg("loot detected")

		meetsReq := loot.Gold >= b.cfg.Search.MinLootGold &&
			loot.Elixir >= b.cfg.Search.MinLootElixir &&
			loot.DarkElixir >= b.cfg.Search.MinLootDarkElixir

		if meetsReq {
			b.logger.Info().Msg("loot requirements met, starting attack!")
			b.deployTroops(screen)
			screen.Close()
			break
		}

		b.logger.Info().
			Msg("loot too low, skipping base...")

		screen.Close() // Close before findAndClick which does its own capture

		// Professional High-Speed Click: Use pinpoint with fallback
		if !b.findAndClick("btn_next", "Next Match", 2) {
			b.logger.Warn().Msg("template match failed, forcing skip via pinpoint")
			nextX, nextY := b.cal.ScaleRef(810, 660)
			b.client.Tap(nextX, nextY)
		}

		// Wait briefly for the "Clouds" to appear (transition start)
		time.Sleep(1500 * time.Millisecond)
	}

	b.logger.Info().Msg("battle complete, ending...")
	b.attackExec.EndBattle()
	
	if b.attackExec.WaitForBattleEnd(60 * time.Second) {
		// Capture screen to read results
		resultScreen, err := b.client.CaptureToMat()
		if err == nil {
			lootRec := game.NewLootRecognizer(b.cal, b.templates, b.logger)
			res, err := lootRec.ReadBattleResult(resultScreen)
			if err == nil {
				b.totalGold.Add(int64(res.Loot.Gold + res.Bonus.Gold))
				b.totalElixir.Add(int64(res.Loot.Elixir + res.Bonus.Elixir))
				b.totalDE.Add(int64(res.Loot.DarkElixir + res.Bonus.DarkElixir))
				b.totalStars.Add(int32(res.Stars))
				
				// Track specific star result
				switch res.Stars {
				case 0: b.stars0.Add(1)
				case 1: b.stars1.Add(1)
				case 2: b.stars2.Add(1)
				case 3: b.stars3.Add(1)
				}
				
				b.logger.Info().
					Int("stars", res.Stars).
					Int("gold", res.Loot.Gold).
					Int("bonus_gold", res.Bonus.Gold).
					Msg("battle result processed")
			}
			resultScreen.Close()
			lootRec.Close()
		}
	}

	b.attackExec.ReturnHome()

	b.attackCount.Add(1)
	
	// Professional Session Summary
	b.logger.Info().
		Int32("attacks", b.attackCount.Load()).
		Str("stars", fmt.Sprintf("3⭐:%d | 2⭐:%d | 1⭐:%d | 0⭐:%d", b.stars3.Load(), b.stars2.Load(), b.stars1.Load(), b.stars0.Load())).
		Str("loot", fmt.Sprintf("Gold: %d | Elixir: %d | DE: %d", b.totalGold.Load(), b.totalElixir.Load(), b.totalDE.Load())).
		Dur("uptime", time.Since(b.startedAt)).
		Msg("=== SESSION SUMMARY ===")
}

func (b *Bot) clickSequence() bool {
	// Step 1: find and click the orange Attack button
	if !b.findAndClick("btn_attack", "Attack", 3) {
		return false
	}
	time.Sleep(1200 * time.Millisecond) // Professional delay for menu slide-in

	// Wait for the attack menu to open
	if !b.waitForButton("btn_find_match", 10*time.Second) {
		b.logger.Warn().Msg("find match button did not appear")
		return false
	}

	// Step 2: click the yellow Find Match button
	if !b.findAndClick("btn_find_match", "Find Match", 3) {
		return false
	}
	time.Sleep(1000 * time.Millisecond) // Wait for search screen/army bar

	// Wait for army selector to appear
	if !b.waitForButton("btn_army_arrow", 5*time.Second) {
		b.logger.Warn().Msg("army arrow did not appear")
		return false
	}

	// Step 3: click the white army arrow to expand army selection
	b.findAndClick("btn_army_arrow", "Army Arrow", 2)
	time.Sleep(800 * time.Millisecond) // Wait for expansion animation

	// Wait for army 1 preset button
	if !b.waitForButton("btn_army_1", 3*time.Second) {
		b.logger.Warn().Msg("army 1 button did not appear, continuing anyway")
	}

	// Step 4: click army composition 1
	b.findAndClick("btn_army_1", "Army 1", 2)
	time.Sleep(800 * time.Millisecond)

	// Wait for the green battle button to become available
	if !b.waitForButton("btn_battle", 10*time.Second) {
		b.logger.Warn().Msg("battle button did not appear")
		return false
	}

	// Step 5: click the green Battle button
	if !b.findAndClick("btn_battle", "Battle", 3) {
		return false
	}

	// Wait for the actual battle to start
	b.logger.Info().Msg("waiting for battle state (searching)...")
	return b.waitForBattleState(60 * time.Second)
}

func (b *Bot) waitForButton(templateName string, timeout time.Duration) bool {
	tpl, ok := b.templates.Get(templateName)
	if !ok {
		b.logger.Error().Str("template", templateName).Msg("template not loaded")
		return false
	}

	// Define specialized ROIs for known buttons
	var roi image.Rectangle
	switch templateName {
	case "btn_attack":
		roi = image.Rect(0, 500, 300, 732)
	case "btn_find_match":
		roi = image.Rect(50, 400, 400, 600) // left-middle
	case "btn_battle":
		roi = image.Rect(400, 450, 860, 732)
	case "btn_army_arrow":
		roi = image.Rect(300, 100, 700, 300) // top-center
	case "btn_army_1":
		roi = image.Rect(200, 150, 600, 400) // top-center/left
	case "btn_next":
		roi = image.Rect(600, 450, 860, 732)
	default:
		roi = image.Rect(0, 0, 860, 732)
	}

	physROI := image.Rect(
		int(float64(roi.Min.X)*b.cal.ScaleX),
		int(float64(roi.Min.Y)*b.cal.ScaleY),
		int(float64(roi.Max.X)*b.cal.ScaleX),
		int(float64(roi.Max.Y)*b.cal.ScaleY),
	)

	b.logger.Debug().Str("template", templateName).Msg("waiting for button")
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		screen, err := b.client.CaptureToMat()
		if err != nil {
			time.Sleep(200 * time.Millisecond)
			continue
		}
		// Expand scale range for UI buttons and use ROI
		matches, _ := vision.MatchMultiScaleROI(screen, tpl, 0.2, 1.8, 30, 0.5, physROI)
		screen.Close()
		if len(matches) > 0 {
			return true
		}
		b.dismissInterruptions()
		time.Sleep(200 * time.Millisecond)
	}
	return false
}

// Pinpoint defines a precise location on the reference screen (860x732)
// and a color check to verify it before clicking.
type Pinpoint struct {
	X, Y int
	Name string
}

var villagePinpoints = map[string]Pinpoint{
	"btn_attack":     {X: 64, Y: 700, Name: "Attack"},
	"btn_find_match": {X: 165, Y: 495, Name: "Find Match"},
	"btn_battle":     {X: 525, Y: 615, Name: "Battle"},
	"btn_army_arrow": {X: 512, Y: 189, Name: "Army Arrow"},
	"btn_army_1":     {X: 402, Y: 247, Name: "Army 1"},
	"btn_next":       {X: 778, Y: 574, Name: "Next Match"},
}

func (b *Bot) findAndClick(templateName, stepName string, maxRetries int) bool {
	// Professional High-Speed Path: Check Pinpoint first
	if pp, ok := villagePinpoints[templateName]; ok {
		// Take a quick capture to verify pinpoint
		screen, err := b.client.CaptureToMat()
		if err == nil {
			px, py := b.cal.ScaleRef(pp.X, pp.Y)
			// Bypass color check for menu elements with inconsistent backgrounds
			isMenuElement := templateName == "btn_army_arrow" || templateName == "btn_army_1"
			
			// Professional Resilience: Check multiple colors for the button
			matched := isMenuElement || b.isOrange(screen, px, py) || b.isYellow(screen, px, py) || b.isGreen(screen, px, py) || b.isWhite(screen, px, py) || b.isSilver(screen, px, py)
			
			if !matched && templateName == "btn_next" {
				// Secondary check for Next button: test slightly to the left (hitting the silver text)
				altX, altY := b.cal.ScaleRef(pp.X-60, pp.Y)
				matched = b.isSilver(screen, altX, altY) || b.isWhite(screen, altX, altY)
				if matched {
					px, py = altX, altY // Use the confirmed point
				}
			}

			if matched {
				screen.Close()
				b.logger.Info().Str("step", stepName).Msg("pinpoint match, clicking...")
				if err := b.client.Tap(px, py); err == nil {
					return true
				}
			}
			screen.Close()
		}
	}

	// Fallback Path: Template Matching (Robust but slower)
	tpl, ok := b.templates.Get(templateName)
	if !ok {
		b.logger.Error().Str("template", templateName).Msg("template not loaded")
		return false
	}

	// Define specialized ROIs for known buttons
	var roi image.Rectangle
	switch templateName {
	case "btn_attack":
		roi = image.Rect(0, 500, 300, 732)
	case "btn_find_match":
		roi = image.Rect(50, 400, 400, 600) // left-middle
	case "btn_battle":
		roi = image.Rect(400, 450, 860, 732)
	case "btn_army_arrow":
		roi = image.Rect(300, 100, 700, 300) // top-center
	case "btn_army_1":
		roi = image.Rect(200, 150, 600, 400) // top-center/left
	case "btn_next":
		roi = image.Rect(600, 450, 860, 732)
	default:
		roi = image.Rect(0, 0, 860, 732)
	}

	physROI := image.Rect(
		int(float64(roi.Min.X)*b.cal.ScaleX),
		int(float64(roi.Min.Y)*b.cal.ScaleY),
		int(float64(roi.Max.X)*b.cal.ScaleX),
		int(float64(roi.Max.Y)*b.cal.ScaleY),
	)

	for retry := 0; retry < maxRetries; retry++ {
		screen, err := b.client.CaptureToMat()
		if err != nil {
			b.logger.Warn().Err(err).Str("step", stepName).Msg("capture failed")
			time.Sleep(500 * time.Millisecond)
			continue
		}

		if screen.Empty() {
			screen.Close()
			time.Sleep(500 * time.Millisecond)
			continue
		}

		// Use specialized ROI for matching
		matches, err := vision.MatchMultiScaleROI(screen, tpl, 0.2, 2.0, 30, 0.45, physROI)
		screen.Close()

		if err != nil {
			b.logger.Warn().Err(err).Str("step", stepName).Msg("match error")
			time.Sleep(500 * time.Millisecond)
			continue
		}

		if len(matches) == 0 {
			if retry == 0 {
				b.logger.Debug().Str("step", stepName).Msg("not found, retrying...")
			}
			b.dismissInterruptions()
			time.Sleep(800 * time.Millisecond)
			continue
		}

		best := matches[0]
		px, py := best.Point.X, best.Point.Y

		b.logger.Info().
			Str("step", stepName).
			Float64("conf", best.Confidence).
			Int("x", px).Int("y", py).
			Msg("clicking (fallback match)")

		if err := b.client.Tap(px, py); err != nil {
			b.logger.Error().Err(err).Msg("tap failed")
			return false
		}

		return true
	}

	b.logger.Error().Str("step", stepName).Int("retries", maxRetries).Msg("failed after retries")
	return false
}

func (b *Bot) isWhite(screen gocv.Mat, x, y int) bool {
	return b.colorCheck(screen, x, y, 
		gocv.NewScalar(220, 220, 220, 0), // Lower White
		gocv.NewScalar(255, 255, 255, 0), // Upper White
		10)
}

func (b *Bot) isSilver(screen gocv.Mat, x, y int) bool {
	return b.colorCheck(screen, x, y, 
		gocv.NewScalar(170, 170, 170, 0), // Lower Silver
		gocv.NewScalar(235, 235, 235, 0), // Upper Silver
		10)
}

func (b *Bot) isYellow(screen gocv.Mat, x, y int) bool {
	return b.colorCheck(screen, x, y, 
		gocv.NewScalar(0, 180, 200, 0), // Lower Yellow (BGR)
		gocv.NewScalar(100, 255, 255, 0), // Upper Yellow
		15)
}

func (b *Bot) isGreen(screen gocv.Mat, x, y int) bool {
	return b.colorCheck(screen, x, y, 
		gocv.NewScalar(0, 150, 0, 0),   // Lower Green
		gocv.NewScalar(120, 255, 120, 0), // Upper Green
		15)
}

func (b *Bot) colorCheck(screen gocv.Mat, x, y int, lower, upper gocv.Scalar, minPixels int) bool {
	if x < 0 || y < 0 || x >= screen.Cols() || y >= screen.Rows() {
		return false
	}
	region := image.Rect(x-10, y-10, x+11, y+11)
	if region.Min.X < 0 { region.Min.X = 0 }
	if region.Min.Y < 0 { region.Min.Y = 0 }
	if region.Max.X > screen.Cols() { region.Max.X = screen.Cols() }
	if region.Max.Y > screen.Rows() { region.Max.Y = screen.Rows() }

	sub := screen.Region(region)
	defer sub.Close()

	mask := gocv.NewMat()
	defer mask.Close()
	gocv.InRangeWithScalar(sub, lower, upper, &mask)

	return gocv.CountNonZero(mask) > minPixels
}

func (b *Bot) dismissInterruptions() {
	screen, err := b.client.CaptureToMat()
	if err != nil {
		return
	}
	state, _ := b.classify(screen)
	screen.Close()

	switch state {
	case game.StateObstacleDialog:
		b.client.TapRandomized(400, 300)
		time.Sleep(400 * time.Millisecond)
		b.client.Back()
	case game.StateGemDialog, game.StateShieldInfo:
		b.client.TapRandomized(175, 30)
	case game.StateChatOpen:
		b.client.Back()
	}
}

func (b *Bot) waitForBattleState(timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		screen, err := b.client.CaptureToMat()
		if err != nil {
			time.Sleep(500 * time.Millisecond)
			continue
		}

		state, _ := b.classify(screen)
		screen.Close()

		switch {
		case state == game.StateBattle:
			b.logger.Info().Msg("battle state detected, entering search loop")
			return true
		case state == game.StateSearchMap || state == game.StateLoading:
			b.logger.Debug().Msg("in clouds/loading...")
			time.Sleep(1 * time.Second)
			continue
		default:
			b.logger.Debug().Str("state", state.String()).Msg("waiting for battle state (unknown/other)...")
			b.dismissInterruptions()
			time.Sleep(500 * time.Millisecond)
		}
	}

	b.logger.Warn().Dur("timeout", timeout).Msg("timed out waiting for battle")
	return false
}

func (b *Bot) deployTroops(screen gocv.Mat) {
	strat, err := strategy.ParseYAML(b.cfg.Attack.StrategyFile)
	if err != nil {
		b.logger.Warn().Err(err).Str("path", b.cfg.Attack.StrategyFile).Msg("could not load strategy")
		return
	}

	b.logger.Info().
		Str("strategy", strat.Name).
		Int("phases", len(strat.Phases)).
		Msg("executing dynamic attack plan")

	if err := b.attackExec.DeployDynamic(strat, screen); err != nil {
		b.logger.Error().Err(err).Msg("dynamic deploy failed")
	}
}

func (b *Bot) Health() game.SystemHealth {
	return game.SystemHealth{
		ADBConnected:     b.client.IsConnected(),
		LastCapture:      time.Now(),
		AvgCaptureMs:     b.client.Health().AvgCaptureMs,
		ConsecutiveFails: b.client.Health().ConsecutiveFails,
	}
}

func (b *Bot) Stats() BotStats {
	return BotStats{
		AttacksCompleted: b.attackCount.Load(),
		Uptime:            time.Since(b.startedAt),
		AdbHealth:         b.client.Health(),
	}
}

type BotStats struct {
	AttacksCompleted int32
	Uptime           time.Duration
	AdbHealth        adb.Health
}

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


func init() {
	runtime.GOMAXPROCS(0)
}