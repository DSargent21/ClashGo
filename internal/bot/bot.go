package bot

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"time"

	"gocv.io/x/gocv"

	"github.com/Ducky705/ClashGO/internal/adb"
	"github.com/Ducky705/ClashGO/internal/attack"
	"github.com/Ducky705/ClashGO/internal/config"
	"github.com/Ducky705/ClashGO/internal/game"
	"github.com/Ducky705/ClashGO/internal/paths"
	"github.com/Ducky705/ClashGO/internal/training"
	"github.com/Ducky705/ClashGO/internal/vision"
	"github.com/Ducky705/ClashGO/pkg/strategy"
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
	skipsCount  atomic.Int32
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
	lastAction  time.Time
	lastSequenceStart time.Time
	stuckTimeout      time.Duration

	lastFrame      atomic.Value // Stores the latest base64 encoded frame
	lastFrameTime  time.Time

	// dukePicksFile records every Dragon Duke random pick across all
	// live attacks. Opened once at NewBot time and written from the
	// attackExec.OnDukePick callback (legacy path) via the orchestrator
	// bridge that funnels HeroManager.resolveHeroTarget picks through
	// OnDukePick as well. One NDJSON line per pick; jq-friendly.
	dukePicksFile *os.File

	OnFrame       func(string)
	OnStatsUpdate func()
}

func NewBot(cfg *config.BotConfig) (*Bot, error) {
	zl := &adbLogAdapter{log: log.Logger}

	client := adb.NewClient(
		adb.WithHost(cfg.Device.ADBHost),
		adb.WithPort(cfg.Device.ADBPort),
		adb.WithLogger(zl),
		adb.WithTimeout(30*time.Second),
		adb.WithZoomKeys(cfg.Device.ZoomOutKey, cfg.Device.ZoomInKey),
	)
	client.DeviceID = cfg.Device.DeviceID

	log.Info().Msg("initializing bot startup sequence...")

	if runtime.GOOS == "darwin" {
		log.Info().Msg("verifying BlueStacks configuration...")
		restartNeeded := true
		if err := client.Connect(); err == nil {
			w, h, _ := client.ScreenSize()
			if w == cfg.Device.Width && h == cfg.Device.Height {
				log.Info().Msg("BlueStacks already running with correct resolution")
				restartNeeded = false
			}
		}

		if restartNeeded {
			if err := client.EnsureBlueStacksMac(cfg.Device.Width, cfg.Device.Height, cfg.Device.DPI); err != nil {
				log.Error().Err(err).Msg("failed to enforce BlueStacks configuration")
			}
		}
	}

	// Wait for ADB server and device to become available
	log.Info().Msg("waiting for ADB connection (up to 90s)...")
	deadline := time.Now().Add(90 * time.Second)
	connected := false
	for time.Now().Before(deadline) {
		_ = client.AutoDetectDevice()
		if err := client.Reconnect(); err == nil {
			connected = true
			break
		}
		time.Sleep(3 * time.Second)
	}

	if !connected {
		return nil, fmt.Errorf("timeout waiting for ADB connection")
	}
	log.Info().Msg("ADB connected successfully")

	// Ensure system is booted
	log.Info().Msg("waiting for Android system to report 'boot_completed'...")
	if err := client.WaitForBoot(90 * time.Second); err != nil {
		return nil, fmt.Errorf("android boot timeout: %w", err)
	}
	log.Info().Msg("Android system is ready")

	// Ensure game is started
	log.Info().Msg("checking game status...")
	packageName := cfg.Device.PackageName
	if packageName == "" {
		packageName = "com.supercell.clashofclans"
	}
	
	if cfg.Device.RestartOnStartup {
		// Always close the app to start out for a clean state
		log.Info().Str("package", packageName).Msg("ensuring clean state by restarting game...")
		if err := client.ForceStop(packageName); err != nil {
			log.Warn().Err(err).Msg("failed to force stop game during startup")
		}
		time.Sleep(2 * time.Second)

		log.Info().Str("package", packageName).Msg("launching game...")
		if err := client.StartApp(packageName); err != nil {
			return nil, fmt.Errorf("failed to start game: %w", err)
		}

		// Brief wait for game to start rendering, then rely on template polling
		log.Info().Msg("waiting for game to settle...")
		time.Sleep(15 * time.Second)
	} else {
		log.Info().Msg("skipping game restart on startup (restart_on_startup=false)")
	}

	log.Info().Msg("starting calibration...")
	calibrator := game.NewCalibrator(client)
	cal, err := calibrator.Calibrate()
	if err != nil {
		return nil, fmt.Errorf("calibrate: %w", err)
	}

	graph := game.NewStateGraph()
	graph.AddNode(game.StateMainVillage)

	// Open the Duke-pick NDJSON writer BEFORE constructing the
	// attackExec so the callback closure captures the file handle.
	startedWall := time.Now()
	dukePicksDir := "output/duke_picks"
	if err := os.MkdirAll(dukePicksDir, 0o755); err != nil {
		log.Warn().Err(err).Str("dir", dukePicksDir).Msg("failed to create duke_picks dir")
	}
	dukePicksPath := filepath.Join(dukePicksDir, startedWall.Format("20060102_150405")+".ndjson")
	dukePicksFile, dpErr := os.OpenFile(dukePicksPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if dpErr != nil {
		log.Warn().Err(dpErr).Str("path", dukePicksPath).Msg("failed to open duke_picks NDJSON; will skip")
	}

	attackExec := attack.NewExecutor(client, cal, &cfg.Attack, log.Logger)
	trainer := training.NewTrainer(client, cal, &cfg.Training, log.Logger)

	var templates *game.TemplateStore
	templates, err = game.NewTemplateStore(paths.Resolve("templates"))
	if err != nil {
		log.Warn().Err(err).Msg("template store init failed, continuing without templates")
		templates = nil
	}

	if templates != nil {
		templates.LoadTemplates()
		log.Info().Int("templates", templates.Count()).Msg("templates loaded")
	}

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
		startedAt:         startedWall,
		lastAction:        time.Now(),
		lastSequenceStart: time.Now(),
		stuckTimeout:      35 * time.Second,
		dukePicksFile:     dukePicksFile,
	}

	// Wire the OnDukePick observer so every Duke random pick (legacy
	// adjacent-corner OR new chosen-edge fallback) gets append-recorded
	// to the per-session NDJSON. No-op if the file failed to open.
	if dukePicksFile != nil {
		writePick := func(target, chosen string) {
			line := fmt.Sprintf(`{"timestamp":%q,"target_edge":%q,"chosen_edge":%q}`+"\n",
				time.Now().Format(time.RFC3339Nano), target, chosen)
			if _, err := dukePicksFile.WriteString(line); err != nil {
				log.Warn().Err(err).Msg("failed to write duke pick to NDJSON")
			}
		}
		attackExec.OnDukePick = writePick
	}

	b.lastFrame.Store("")

	b.classifier = game.NewClassifier(cal, game.DefaultClassifierConfig(), b.logger)
	if b.templates != nil {
		b.classifier.SetTemplates(b.templates)
	}

	b.classify = func(mat gocv.Mat) (game.GameState, int) {
		return b.classifier.ClassifyState(mat)
	}

	// Update dependents with the final classifier/classify function
	b.navigator = game.NewNavigator(client, cal, graph, b.classify, b.logger)
	if b.templates != nil {
		b.navigator.SetTemplates(b.templates)
	}

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

	// Initial focus click to ensure emulator/window is active
	focusX, focusY := b.cal.ScaleRef(842, 345)
	b.logger.Info().Int("x", focusX).Int("y", focusY).Msg("performing initial focus click")
	b.client.Tap(focusX, focusY)
	time.Sleep(1 * time.Second)

	go b.captureLoop()
	return nil
}

func (b *Bot) Stop() {
	b.cancel()
	b.client.Close()
	globalAsyncWriter.Close()
	vision.CloseTemplateCache()
	if b.dukePicksFile != nil {
		_ = b.dukePicksFile.Close()
	}
}
func (b *Bot) captureLoop() {
	gc := game.NewGameContext()

	type frame struct {
		mat gocv.Mat
		err error
		dur time.Duration
	}
	frames := make(chan frame, 1)

	// Adaptive FPS based on game state
	getCaptureInterval := func() time.Duration {
		switch gc.State {
		case game.StateBattle, game.StateSearchMap, game.StateLoading:
			return 100 * time.Millisecond // 10 FPS for active gameplay
		case game.StateMainVillage, game.StateArmySelection, game.StateArmyCamp:
			return 300 * time.Millisecond // ~3 FPS for UI navigation
		default:
			return 1000 * time.Millisecond // 1 FPS for unknown/idle states
		}
	}

	go func() {
		var lastCapture time.Time
		for {
			interval := getCaptureInterval()
			nextCapture := lastCapture.Add(interval)
			sleepTime := time.Until(nextCapture)
			if sleepTime > 0 {
				select {
				case <-b.ctx.Done():
					return
				case <-time.After(sleepTime):
				}
			}

			select {
			case <-b.ctx.Done():
				return
			default:
			}

			start := time.Now()
			screen, err := b.client.CaptureToMat()
			dur := time.Since(start)
			lastCapture = time.Now()

			if err == nil && !screen.Empty() {
				if b.OnFrame != nil {
					small := vision.GetMat(screen.Rows()/2, screen.Cols()/2, screen.Type())
					gocv.Resize(screen, &small, image.Point{}, 0.5, 0.5, gocv.InterpolationLinear)

					go func(m gocv.Mat) {
						defer vision.PutMat(m)
						buf, err := gocv.IMEncodeWithParams(".jpg", m, []int{gocv.IMWriteJpegQuality, 60})
						if err == nil {
							encoded := base64.StdEncoding.EncodeToString(buf.GetBytes())
							b.lastFrame.Store(encoded)
							b.OnFrame(encoded)
							buf.Close()
						}
					}(small)
				}
			}

			select {
			case frames <- frame{mat: screen, err: err, dur: dur}:
			default:
				if err == nil && !screen.Empty() {
					screen.Close()
				}
			}
		}
	}()

	for {
		select {
		case <-b.ctx.Done():
			select {
			case f := <-frames:
				if !f.mat.Empty() {
					f.mat.Close()
				}
			default:
			}
			return
		case f := <-frames:
			b.checkStuck(gc)
			b.processFrame(gc, f.mat, f.err, f.dur)
		}
	}
}

// recordActivity marks the bot as having taken a meaningful action.
// Called after real forward progress (successful clicks/state transitions)
// so the stuck-check distinguishes "spinning" from "working".
func (b *Bot) recordActivity() {
	b.lastAction = time.Now()
}

// checkStuck enforces a global watchdog: if the capture pipeline is dead,
// or if the bot is in-progress for absurdly long, or has been sitting in
// one place doing nothing for too long, we cycle the game to recover from
// hangs / dialogs / out-of-game screens without requiring user intervention.
func (b *Bot) checkStuck(gc *game.GameContext) {
	// 1. Capture pipeline watchdog: catches dead ADB / emulator before the
	// slower time-based watchdogs below can fire (and ensures restarts reset
	// the zoom state, which trusts that captures are returning frames).
	if gc.ReadHealth().ConsecutiveFails >= 10 {
		b.logger.Error().
			Int("consecutive_fails", gc.ReadHealth().ConsecutiveFails).
			Str("state", gc.State.String()).
			Msg("capture pipeline appears dead, triggering emergency restart...")
		b.restartGame()
		b.lastSequenceStart = time.Now()
		return
	}

	// 2. Per-attack absolute ceiling: a single attack cycle (clicks → search →
	// deploy → battle end → return home) is bounded by lastSequenceStart.
	// WaitForBattleEnd alone can take ~4 min, so we keep a generous 15-min
	// window here. The activity-based watchdog below is bypassed for the
	// duration of a sequence because long inner phases (deploy) intentionally
	// have quiet stretches where no recordActivity() tick fires.
	if b.seqRunning.Load() {
		if time.Since(b.lastSequenceStart) > 15*time.Minute {
			b.logger.Warn().
				Dur("seq_time", time.Since(b.lastSequenceStart)).
				Msg("attack sequence exceeded maximum duration, triggering emergency restart...")
			b.restartGame()
			b.lastSequenceStart = time.Now()
		}
		return
	}

	state, _, _ := gc.ReadState()

	// 3. Activity-based watchdog for non-sequence flows. Any successful
	// click / state transition / post-zoom resets lastAction via
	// b.recordActivity(), so this only fires when the bot is genuinely stuck
	// (no clicks landing, no state advance).
	timeout := b.stuckTimeout
	if state == game.StateBattle {
		// Battle outside an active sequence is unusual; allow a longer
		// window before force-cycling.
		timeout = 3 * time.Minute
	}

	stuckTime := time.Since(b.lastAction)
	if stuckTime > timeout {
		b.logger.Warn().
			Str("state", state.String()).
			Time("last_action", b.lastAction).
			Dur("stuck_time", stuckTime).
			Dur("timeout", timeout).
			Msg("bot appears stuck without meaningful action, triggering emergency restart...")

		b.restartGame()
		b.lastSequenceStart = time.Now()
	}
}

func (b *Bot) restartGame() {
	pkg := b.cfg.Device.PackageName
	if pkg == "" {
		pkg = "com.supercell.clashofclans"
	}

	b.logger.Info().Str("package", pkg).Msg("restarting game...")

	if err := b.client.ForceStop(pkg); err != nil {
		b.logger.Error().Err(err).Msg("failed to force stop game")
	}

	time.Sleep(2 * time.Second)

	if err := b.client.StartApp(pkg); err != nil {
		b.logger.Error().Err(err).Msg("failed to start app")
	}

	// Wait for game to launch and settle
	time.Sleep(15 * time.Second)
	b.zoomedOut.Store(false) // Reset zoom state on restart
}

func (b *Bot) processFrame(gc *game.GameContext, screen gocv.Mat, err error, captureMs time.Duration) {
	if err != nil {
		gc.RecordCaptureError()
		b.logger.Debug().Err(err).Msg("capture failed")
		if !screen.Empty() {
			screen.Close()
		}
		return
	}
	if screen.Empty() {
		screen.Close()
		return
	}

	state, score := b.classify(screen)

	// UpdateScreen takes ownership and will handle closing previous mat
	gc.UpdateScreen(screen, captureMs)

	// Professional Zoom Out: Mandatory first action upon entering any village state.
	// We MUST zoom out before starting ANY sequence or clicking ANY buttons.
	if !b.zoomedOut.Load() {
		// Include pinpoint attack button check in village detection to avoid race with sequence starter
		pinX, pinY := b.cal.ScaleRef(60, 695)
		isVillage := state == game.StateMainVillage || 
					state == game.StateArmyCamp || 
					b.isOrange(screen, pinX, pinY) ||
					b.templateMatch(screen, "btn_attack", 0.45) || 
					b.templateMatch(screen, "btn_settings", 0.6)

		if isVillage {
			if b.zoomedOut.CompareAndSwap(false, true) {
				b.logger.Info().Msg("village detected, performing MANDATORY initial zoom out...")
				b.navigator.ZoomOut()
				b.recordActivity()
				// Wait for zoom animation to settle (Clash has long momentum)
				time.Sleep(1800 * time.Millisecond)
				// Return here so the next loop iteration captures a fresh screen AFTER zoom
				return
			}
		}
	}

	// Update lastAction only on meaningful state transitions. Unknown / Loading
	// are excluded so a transient flicker doesn't reset the stuck timer.
	if state != gc.State && state != game.StateUnknown && state != game.StateLoading {
		b.recordActivity()
	}

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

	if b.seqRunning.Load() {
		return
	}

	// Stuck State Recovery: if we are in a terminal state but no sequence is running,
	// it means we finished an attack but failed to return home, or a manual action left us here.
	if gc.State == game.StateBattleEnd || gc.State == game.StateReturnHome {
		b.logger.Info().Str("state", gc.State.String()).Msg("detected terminal state without active sequence, returning home...")
		go b.attackExec.ReturnHome()
		b.recordActivity()
		return
	}

	// Primary detection: try to find the attack button via template matching
	// Only start attack if we are reasonably sure we're in the Main Village
	if b.zoomedOut.Load() && (gc.State == game.StateMainVillage || gc.State == game.StateUnknown) && b.findAttackButton(screen, 0.45) {
		b.logger.Info().Msg("attack button detected, starting sequence")
		b.lastSequenceStart = time.Now()
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
	pinX, pinY := b.cal.ScaleRef(60, 695)
	if b.isOrange(screen, pinX, pinY) {
		b.logger.Debug().Msg("attack button confirmed via pinpoint color check")
		return true
	}

	tpl, ok := b.templates.Get("btn_attack")
	if !ok {
		return false
	}

	roi := image.Rect(0, 500, 300, 732)
	physROI := image.Rect(
		int(float64(roi.Min.X)*b.cal.ScaleX),
		int(float64(roi.Min.Y)*b.cal.ScaleY),
		int(float64(roi.Max.X)*b.cal.ScaleX),
		int(float64(roi.Max.Y)*b.cal.ScaleY),
	)

	matches, err := vision.MatchMultiScaleROICached(screen, tpl, "btn_attack", 0.2, 2.0, 5, threshold, physROI)
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
	// Broad Attack button orange range (BGR)
	// CoC orange: R=255, G=175, B=0
	return b.colorCheck(screen, x, y,
		gocv.NewScalar(0, 100, 150, 0),   // Lower Orange
		gocv.NewScalar(150, 255, 255, 0), // Upper Orange
		20)
}

var lastNav time.Time

func (b *Bot) templateMatch(screen gocv.Mat, name string, threshold float32) bool {
	tpl, ok := b.templates.Get(name)
	if !ok {
		return false
	}
	matches, err := vision.MatchMultiScaleROICached(screen, tpl, name, 0.2, 2.0, 5, threshold, image.Rect(0, 0, screen.Cols(), screen.Rows()))
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
		b.logger.Warn().Msg("attack click sequence failed, restarting game to recover...")
		b.restartGame()
		return
	}

	b.logger.Info().Msg("waiting for base to be found...")

	lootRec := game.NewLootRecognizer(b.cal, b.templates, b.logger)

	var remainingUndeployed int
	var deployErr error
	var stratName string = "Unknown"
	var targetEdge string = "Unknown"

	searchStart := time.Now()
	for {
		if time.Since(searchStart) > 5*time.Minute {
			b.logger.Error().Msg("searching/skipping bases took too long (stuck in clouds?), restarting game...")
			b.restartGame()
			lootRec.Close()
			return
		}

		// High-Speed Loop: reduced sleep for faster cycling
		time.Sleep(500 * time.Millisecond)

		screen, err := b.client.CaptureToMat()
		if err != nil {
			return
		}

		state, _ := b.classify(screen)
		if state != game.StateBattle {
			if state == game.StateSearchMap || state == game.StateLoading {
				b.logger.Info().Str("state", state.String()).Msg("still searching (clouds)...")
				screen.Close()
				continue
			}
			b.logger.Info().Str("state", state.String()).Msg("searching area (wait)...")
			// Unexpected state, check interruptions but keep moving
			b.dismissInterruptions()
			screen.Close()
			continue
		}

		b.logger.Info().Msg("base found, reading loot...")
		loot, err := lootRec.ReadAvailableLoot(screen)
		if err != nil {
			b.logger.Warn().Err(err).Msg("failed to read loot")
			b.DumpDiagnostics("loot_read_failed", screen, map[string]interface{}{
				"error": err.Error(),
			})
		}

		b.logger.Info().
			Int("gold", loot.Gold).
			Int("elixir", loot.Elixir).
			Int("de", loot.DarkElixir).
			Msg("loot detected")

		meetsReq := !b.cfg.Search.Enabled || (
			loot.Gold >= b.cfg.Search.MinLootGold &&
			loot.Elixir >= b.cfg.Search.MinLootElixir &&
			loot.DarkElixir >= b.cfg.Search.MinLootDarkElixir)

		if meetsReq {
			b.logger.Info().Msg("loot requirements met, starting attack!")
			if strat, err := strategy.ParseYAML(b.cfg.Attack.StrategyFile); err == nil {
				stratName = strat.Name
				targetEdge = strat.TargetEdge
			}
			remainingUndeployed, deployErr = b.deployTroops(screen)
			if deployErr != nil || remainingUndeployed > 0 {
				failScreen, err := b.client.CaptureToMat()
				if err == nil {
					b.DumpDiagnostics("deployment_failed", failScreen, map[string]interface{}{
						"error":      fmt.Sprintf("%v", deployErr),
						"remaining":  remainingUndeployed,
						"stratName":  stratName,
						"targetEdge": targetEdge,
					})
					failScreen.Close()
				}
			}
			screen.Close()
			break
		}

		b.logger.Info().
			Msg("loot too low, skipping base...")
		b.skipsCount.Add(1)
		if b.OnStatsUpdate != nil {
			b.OnStatsUpdate()
		}

		screen.Close() // Close before findAndClick which does its own capture

		// Professional High-Speed Click: Use pinpoint with fallback
		if !b.findAndClick("btn_next", "Next Match", 2) {
			b.logger.Warn().Msg("template match failed, forcing skip via color/pinpoint")

			// Try color-based fallback before hardcoded coordinates
			searchROI := image.Rect(b.cal.PhysicalW/2, b.cal.PhysicalH/2, b.cal.PhysicalW, b.cal.PhysicalH)
			orangePt, err := vision.PixelSearch(screen, searchROI, 252, 186, 54, 50)
			if err == nil {
				b.logger.Info().Msg("clicking Next via orange color fallback")
				b.client.Tap(orangePt.X, orangePt.Y)
				b.recordActivity()
			} else {
				// Final hardcoded fallback
				b.DumpDiagnostics("next_button_not_found", screen, map[string]interface{}{
					"message": "forcing skip via hardcoded coordinates",
				})
				nextX, nextY := b.cal.ScaleRef(796, 565)
				b.client.Tap(nextX, nextY)
				b.recordActivity()
			}
		}

		// Wait briefly for the "Clouds" to appear (transition start)
		time.Sleep(600 * time.Millisecond)
	}

	b.logger.Info().Msg("battle deployment complete, waiting for battle to end naturally...")
	
	var battleStars int = 0
	var battleGold int = 0
	var battleElixir int = 0
	var battleDE int = 0
	var bonusGold, bonusElixir, bonusDE int = 0, 0, 0
	var parsedResults bool = false

	if b.attackExec.WaitForBattleEnd(4 * time.Minute) {
		// Capture screen to read results
		resultScreen, err := b.client.CaptureToMat()
		if err == nil {
			gocv.IMWrite(paths.ResolveConfig("last_battle_result.png"), resultScreen)
			b.logger.Info().Msg("saved battle result screenshot to last_battle_result.png")

			lootRec := game.NewLootRecognizer(b.cal, b.templates, b.logger)
			res, err := lootRec.ReadBattleResult(resultScreen)
			if err == nil {
				battleStars = res.Stars
				battleGold = res.Loot.Gold
				battleElixir = res.Loot.Elixir
				battleDE = res.Loot.DarkElixir
				bonusGold = res.Bonus.Gold
				bonusElixir = res.Bonus.Elixir
				bonusDE = res.Bonus.DarkElixir
				parsedResults = true

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
				
				if b.OnStatsUpdate != nil {
					b.OnStatsUpdate()
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
	} else {
		b.logger.Error().Msg("battle end timeout (stuck in battle?), restarting game...")
		b.restartGame()
		return
	}

	returnedHome := false
	if err := b.attackExec.ReturnHome(); err == nil {
		returnedHome = true
	} else {
		b.logger.Warn().Err(err).Msg("ReturnHome failed, attempting template fallback")
		for i := 0; i < 3; i++ {
			if b.findAndClick("btn_return_home", "Return Home", 1) {
				returnedHome = true
				break
			}
			time.Sleep(1 * time.Second)
		}
	}

	if !returnedHome {
		b.logger.Error().Msg("failed to return home after battle, restarting game...")
		b.restartGame()
		return
	}

	// Dismiss potential popup menus by tapping a neutral side area (Ref: 537, 693)
	sideX := int(537 * b.cal.ScaleX)
	sideY := int(693 * b.cal.ScaleY)
	b.logger.Info().Msg("Tapping side area to dismiss potential post-attack popups...")
	_ = b.client.Tap(sideX, sideY)
	time.Sleep(1000 * time.Millisecond)

	if b.cfg.Upgrade.UpgradeWalls {
		b.UpgradeWalls(gc)
	}

	b.attackCount.Add(1)

	// If we've reached the configured attack cap (e.g. --once), shut the bot
	// down gracefully instead of silently idling in the capture loop.
	if int(b.attackCount.Load()) >= b.cfg.Attack.MaxAttackPerSession {
		b.logger.Info().
			Int32("attacks", b.attackCount.Load()).
			Int("cap", b.cfg.Attack.MaxAttackPerSession).
			Msg("attack cap reached, scheduling graceful shutdown...")
		go func() {
			// Tiny delay so the summary flushes and any post-attack writes finish.
			time.Sleep(2 * time.Second)
			b.cancel()
		}()
	}

	depErrStr := ""
	if deployErr != nil {
		depErrStr = deployErr.Error()
	}

	rep := AttackReport{
		Timestamp:        time.Now().Format(time.RFC3339),
		Strategy:         stratName,
		TargetEdge:       targetEdge,
		DeploySuccess:    deployErr == nil && remainingUndeployed == 0,
		UndeployedSlots:  remainingUndeployed,
		DeployError:      depErrStr,
		ParsedResults:    parsedResults,
		Stars:            battleStars,
		GoldStolen:       battleGold,
		ElixirStolen:     battleElixir,
		DarkElixirStolen: battleDE,
		BonusGold:        bonusGold,
		BonusElixir:      bonusElixir,
		BonusDE:          bonusDE,
		TotalAttacks:     b.attackCount.Load(),
	}

	if repBytes, err := json.MarshalIndent(rep, "", "  "); err == nil {
		_ = AsyncWriteFile(paths.ResolveConfig("last_attack_report.json"), repBytes, 0644)
	}

	// Update persistent history file
	var history []AttackReport
	if histData, err := os.ReadFile(paths.ResolveConfig("attack_history.json")); err == nil {
		_ = json.Unmarshal(histData, &history)
	}
	history = append([]AttackReport{rep}, history...)
	if len(history) > 500 {
		history = history[:500]
	}
	if histBytes, err := json.MarshalIndent(history, "", "  "); err == nil {
		_ = AsyncWriteFile(paths.ResolveConfig("attack_history.json"), histBytes, 0644)
	}

	deployStatus := "SUCCESS (100% Deployed)"
	if !rep.DeploySuccess {
		if remainingUndeployed > 0 {
			deployStatus = fmt.Sprintf("FAILED (%d Slots Undeployed)", remainingUndeployed)
		} else {
			deployStatus = fmt.Sprintf("FAILED (%s)", depErrStr)
		}
	}

	fmt.Println()
	fmt.Println("=========================================")
	fmt.Println("          BATTLE REPORT SUMMARY          ")
	fmt.Println("=========================================")
	fmt.Printf("Strategy:      %s\n", rep.Strategy)
	fmt.Printf("Target Edge:   %s\n", rep.TargetEdge)
	fmt.Printf("Deploy Health: %s\n", deployStatus)
	fmt.Printf("Stars Earned:  %d ⭐\n", rep.Stars)
	fmt.Println("Loot Collected:")
	fmt.Printf("  - Gold:      %d\n", rep.GoldStolen)
	fmt.Printf("  - Elixir:    %d\n", rep.ElixirStolen)
	fmt.Printf("  - DE:        %d\n", rep.DarkElixirStolen)
	fmt.Println("=========================================")
	fmt.Println()
	
	// Professional Session Summary
	b.logger.Info().
		Int32("attacks", b.attackCount.Load()).
		Str("stars", fmt.Sprintf("3⭐:%d | 2⭐:%d | 1⭐:%d | 0⭐:%d", b.stars3.Load(), b.stars2.Load(), b.stars1.Load(), b.stars0.Load())).
		Str("loot", fmt.Sprintf("Gold: %d | Elixir: %d | DE: %d", b.totalGold.Load(), b.totalElixir.Load(), b.totalDE.Load())).
		Dur("uptime", time.Since(b.startedAt)).
		Msg("=== SESSION SUMMARY ===")

	// Reset zoom state so we zoom out again after returning home
	b.zoomedOut.Store(false)
}

func (b *Bot) clickSequence() bool {
	// Step 1: Click the orange Attack button (retry up to 3 times)
	attackClicked := false
	for attempt := 0; attempt < 3; attempt++ {
		if b.findAndClick("btn_attack", "Attack", 1) {
			attackClicked = true
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if !attackClicked {
		b.logger.Warn().Msg("could not find or click Attack button")
		if screen, err := b.client.CaptureToMat(); err == nil {
			b.DumpDiagnostics("click_attack_failed", screen, nil)
			screen.Close()
		}
		return false
	}
	time.Sleep(500 * time.Millisecond) // menu slide-in (tightened)

	// Step 2: Click the yellow Find Match button (retry up to 3 times)
	findMatchClicked := false
	for attempt := 0; attempt < 3; attempt++ {
		if b.findAndClick("btn_find_match", "Find Match", 1) {
			findMatchClicked = true
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if !findMatchClicked {
		b.logger.Warn().Msg("could not find or click Find Match button")
		if screen, err := b.client.CaptureToMat(); err == nil {
			b.DumpDiagnostics("click_find_match_failed", screen, nil)
			screen.Close()
		}
		return false
	}
	time.Sleep(500 * time.Millisecond) // search screen (tightened)

	// Step 3: Click the white army arrow to expand army selection (retry up to 3 times)
	armyArrowClicked := false
	for attempt := 0; attempt < 3; attempt++ {
		if b.findAndClick("btn_army_arrow", "Army Arrow", 1) {
			armyArrowClicked = true
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if !armyArrowClicked {
		b.logger.Warn().Msg("could not find or click Army Arrow button")
		if screen, err := b.client.CaptureToMat(); err == nil {
			b.DumpDiagnostics("click_army_arrow_failed", screen, nil)
			screen.Close()
		}
		return false
	}
	time.Sleep(500 * time.Millisecond) // army expansion (tightened)

	// Step 4: Click army composition 1 (retry up to 3 times)
	army1Clicked := false
	for attempt := 0; attempt < 3; attempt++ {
		if b.findAndClick("btn_army_1", "Army 1", 1) {
			army1Clicked = true
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if !army1Clicked {
		b.logger.Warn().Msg("army 1 button did not appear, continuing anyway")
		if screen, err := b.client.CaptureToMat(); err == nil {
			b.DumpDiagnostics("click_army_1_not_found", screen, nil)
			screen.Close()
		}
	}
	time.Sleep(500 * time.Millisecond)

	// Step 5: Click the green Battle button (retry up to 3 times)
	battleClicked := false
	for attempt := 0; attempt < 3; attempt++ {
		if b.findAndClick("btn_battle", "Battle", 1) {
			battleClicked = true
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if !battleClicked {
		b.logger.Warn().Msg("could not find or click Battle button")
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
		roi = image.Rect(300, 150, 860, 732) // Expanded to catch battle button next to army slots
	case "btn_army_arrow":
		roi = image.Rect(350, 100, 700, 300) // top-center
	case "btn_army_1":
		roi = image.Rect(400, 150, 650, 350) // Tight ROI around army 1 spot
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
		matches, _ := vision.MatchMultiScaleROICached(screen, tpl, templateName, 0.2, 1.8, 5, 0.5, physROI)
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
	"btn_attack":     {X: 64, Y: 666, Name: "Attack"},
	"btn_find_match": {X: 158, Y: 494, Name: "Find Match"},
	"btn_battle":     {X: 731, Y: 537, Name: "Battle"},
	"btn_army_arrow": {X: 514, Y: 192, Name: "Army Arrow"},
	"btn_army_1":     {X: 513, Y: 230, Name: "Army 1"},
	"btn_next":       {X: 794, Y: 577, Name: "Next Match"},
	"btn_return_home":{X: 431, Y: 581, Name: "Return Home"},
	"btn_okay":       {X: 430, Y: 520, Name: "Okay"},
}

func (b *Bot) findAndClick(templateName, stepName string, maxRetries int) bool {
	// Step 1: Pinpoint Match (Fast Path - Trust Coordinates)
	if pp, ok := villagePinpoints[templateName]; ok {
		px, py := b.cal.ScaleRef(pp.X, pp.Y)
		b.logger.Info().Str("step", stepName).Msg("pinpoint match, clicking...")
		if err := b.client.Tap(px, py); err == nil {
			time.Sleep(1000 * time.Millisecond) // Wait for UI transition
			b.recordActivity() // successful click = real progress
			return true
		}
	}

	// Step 2: Fallback Path: Template Matching (Robust but slower)
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
		roi = image.Rect(300, 150, 860, 732) // Expanded to catch battle button next to army slots
	case "btn_army_arrow":
		roi = image.Rect(350, 100, 700, 300) // top-center
	case "btn_army_1":
		roi = image.Rect(400, 150, 650, 350) // Tight ROI around army 1 spot
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

		// Tier 1.5 Fallback: Try a secondary pinpoint for Battle button if it's next to Army 1
		if templateName == "btn_battle" && retry == 0 {
			altX, altY := b.cal.ScaleRef(525, 247) // Higher Y coordinate, same X as Slot 1 Battle
			if b.isGreen(screen, altX, altY) {
				screen.Close()
				b.logger.Info().Str("step", stepName).Msg("secondary pinpoint match (upper battle), clicking...")
				if err := b.client.Tap(altX, altY); err == nil {
					b.recordActivity()
					return true
				}
				screen, _ = b.client.CaptureToMat() // Re-capture if tap failed somehow
			}
		}

		// Use specialized ROI for matching (Optimized: 5 steps)
		matches, err := vision.MatchMultiScaleROICached(screen, tpl, templateName, 0.2, 2.0, 5, 0.45, physROI)
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
		
		if b.cfg.Debug.SaveScreenshots {
			gocv.IMWrite(paths.ResolveConfig(fmt.Sprintf("diag_fallback_%s.png", templateName)), screen)
		}

		if err := b.client.Tap(px, py); err != nil {
			b.logger.Error().Err(err).Msg("tap failed")
			return false
		}
		b.recordActivity() // successful template-based click = real progress

		return true
	}

	// Tier 3 Fallback: Blind tap calibrated pinpoint coordinates
	if pp, ok := villagePinpoints[templateName]; ok {
		px, py := b.cal.ScaleRef(pp.X, pp.Y)
		b.logger.Warn().Str("step", pp.Name).Msg("pinpoint color check and template match failed; executing blind tap fallback")
		if err := b.client.Tap(px, py); err == nil {
			b.recordActivity() // successful blind tap = real progress
			return true
		}
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

// dismissInterruptions taps its way through transient overlays. Note that
// these direct taps intentionally DO NOT call recordActivity() — if a dialog
// is truly stuck and refusing to dismiss, we want the global stuck watchdog
// to fire and cycle the game rather than mask the hang as forward progress.
//
// If the dismiss is actually working, the resulting state transition (caught
// by the captureLoop's next frame) will reset lastAction via recordActivity()
// on its own.
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
	case game.StateWelcomeBack:
		// Okay button center-ish tap
		ox, oy := b.cal.ScaleRef(430, 520)
		b.client.TapRandomized(ox, oy)
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
			b.logger.Info().Msg("in clouds/loading...")
			time.Sleep(1 * time.Second)
			continue
		case state == game.StateArmySelection || state == game.StateArmyCamp:
			b.logger.Info().Msg("in army menu, retrying battle click...")
			b.findAndClick("btn_battle", "Battle Retry", 1)
			time.Sleep(1 * time.Second)
		default:
			b.logger.Info().Str("state", state.String()).Msg("waiting for battle state (searching)...")
			b.dismissInterruptions()
			time.Sleep(500 * time.Millisecond)
		}
	}

	b.logger.Warn().Dur("timeout", timeout).Msg("timed out waiting for battle")
	return false
}

func (b *Bot) deployTroops(screen gocv.Mat) (int, error) {
	strat, err := strategy.ParseYAML(b.cfg.Attack.StrategyFile)
	if err != nil {
		b.logger.Warn().Err(err).Str("path", b.cfg.Attack.StrategyFile).Msg("could not load strategy")
		return 0, err
	}

	b.logger.Info().
		Str("strategy", strat.Name).
		Int("phases", len(strat.Phases)).
		Msg("executing dynamic attack plan")

	// Wait for clouds/battle transition to settle and troop bar to become responsive
	time.Sleep(600 * time.Millisecond)

	remaining, err := b.attackExec.DeployDynamicV2(strat, screen, b.cfg.Attack.StrategyFile)
	if err != nil {
		b.logger.Error().Err(err).Msg("dynamic deploy failed")
		return remaining, err
	}
	return remaining, nil
}

// QuickDeploy is the manual single-shot deploy path for run_designed_attack.sh.
// The user is assumed to already be on the attack screen with a base loaded —
// there's no search loop, no attack-button discovery, no home → finds-match
// pipeline. We capture the current screen once, run deployTroops (which honors
// formula.json overrides if design_attack wrote one next to the strategy), and
// return.
//
// The captureLoop is intentionally NOT started; cli.go's --deploy-only branch
// calls this directly and bypasses b.Start() to avoid the Find-Attack-Button
// race that would otherwise kick the bot into the next-base search cycle.
//
// On non-Battle screens (e.g. user is still on home, or in clouds), we log a
// WARN and proceed anyway — the deployTroops path will see the absence of a
// troop bar and either error out cleanly or succeed-by-luck if the screen does
// actually contain a deployable base. Caller should surface the error.
func (b *Bot) QuickDeploy() error {
	b.logger.Info().Msg("deploy-only mode: capturing current screen once")

	screen, err := b.client.CaptureToMat()
	if err != nil {
		return fmt.Errorf("capture: %w", err)
	}
	defer screen.Close()

	if screen.Empty() {
		return fmt.Errorf("captured screen is empty; device disconnected?")
	}

	state, _ := b.classify(screen)
	b.logger.Info().
		Str("state", state.String()).
		Int("w", screen.Cols()).
		Int("h", screen.Rows()).
		Msg("starting deploy from current screen")

	// Hard gate on StateBattle: deployTroops assumes a Battle-state screen
	// with a visible troop bar and a deployable base. Running it against
	// home / army-menu / battle-end / clouds could mis-fire taps on buttons
	// mistaken for slot positions. Surface a clear actionable error so the
	// user can wait for the clouds to clear or finish navigating to the
	// attack screen before re-running.
	if state != game.StateBattle {
		b.logger.Error().
			Str("state", state.String()).
			Msg("refusing to deploy: screen is not in Battle state; wait for clouds/loading to clear or navigate to a base on the attack screen, then re-run")
		return fmt.Errorf("screen is in state %s; expected Battle — wait for the attack screen to be ready, then re-run", state.String())
	}

	remaining, deployErr := b.deployTroops(screen)
	if deployErr != nil {
		// Surface partial-success so callers know how far it got.
		b.logger.Error().Err(deployErr).Int("undeployed", remaining).Msg("deploy failed")
		return fmt.Errorf("deployTroops: %w (undeployed=%d)", deployErr, remaining)
	}
	if remaining > 0 {
		b.logger.Warn().Int("undeployed", remaining).Msg("deploy finished with undeployed slots")
		return fmt.Errorf("deploy completed with %d undeployed slots", remaining)
	}
	b.logger.Info().Msg("deploy completed cleanly (0 undeployed)")
	return nil
}

func (b *Bot) Health() game.SystemHealth {
	return game.SystemHealth{
		ADBConnected:     b.client.IsConnected(),
		LastCapture:      time.Now(),
		AvgCaptureMs:     b.client.Health().AvgCaptureMs,
		ConsecutiveFails: b.client.Health().ConsecutiveFails,
	}
}

func (b *Bot) UpdateConfig(cfg *config.BotConfig) {
	b.cfg = cfg
	if b.attackExec != nil {
		b.attackExec.UpdateConfig(&cfg.Attack)
	}
	if b.trainer != nil {
		b.trainer.UpdateConfig(&cfg.Training)
	}
	b.logger.Info().Msg("bot configuration updated in real-time")
}

func (b *Bot) GetClient() *adb.Client {
	return b.client
}

func (b *Bot) GetLastFrame() string {
	val := b.lastFrame.Load()
	if val == nil {
		return ""
	}
	return val.(string)
}

func (b *Bot) Stats() BotStats {
	return BotStats{
		AttacksCompleted: b.attackCount.Load(),
		SearchSkips:      b.skipsCount.Load(),
		TotalGold:        b.totalGold.Load(),
		TotalElixir:      b.totalElixir.Load(),
		TotalDE:          b.totalDE.Load(),
		Stars0:           b.stars0.Load(),
		Stars1:           b.stars1.Load(),
		Stars2:           b.stars2.Load(),
		Stars3:           b.stars3.Load(),
		Uptime:           time.Since(b.startedAt),
		AdbHealth:        b.client.Health(),
	}
}

type BotStats struct {
	AttacksCompleted int32         `json:"attacks_completed"`
	SearchSkips      int32         `json:"search_skips"`
	TotalGold        int64         `json:"total_gold"`
	TotalElixir      int64         `json:"total_elixir"`
	TotalDE          int64         `json:"total_de"`
	Stars0           int32         `json:"stars_0"`
	Stars1           int32         `json:"stars_1"`
	Stars2           int32         `json:"stars_2"`
	Stars3           int32         `json:"stars_3"`
	Uptime           time.Duration `json:"uptime"`
	AdbHealth        adb.Health    `json:"adb_health"`
}

type AttackReport struct {
	Timestamp        string `json:"timestamp"`
	Strategy         string `json:"strategy"`
	TargetEdge       string `json:"target_edge"`
	DeploySuccess    bool   `json:"deploy_success"`
	UndeployedSlots  int    `json:"undeployed_slots"`
	DeployError      string `json:"deploy_error,omitempty"`
	ParsedResults    bool   `json:"parsed_results"`
	Stars            int    `json:"stars"`
	GoldStolen       int    `json:"gold_stolen"`
	ElixirStolen     int    `json:"elixir_stolen"`
	DarkElixirStolen int    `json:"dark_elixir_stolen"`
	BonusGold        int    `json:"bonus_gold"`
	BonusElixir      int    `json:"bonus_elixir"`
	BonusDE          int    `json:"bonus_de"`
	TotalAttacks     int32  `json:"total_attacks_session"`
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