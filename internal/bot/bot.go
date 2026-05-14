package bot

import (
	"context"
	"fmt"
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
	seqRunning  atomic.Bool
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

	classifier := game.NewClassifier(cal, game.DefaultClassifierConfig())
	classify := func(mat gocv.Mat) (game.GameState, int) {
		return classifier.ClassifyState(mat)
	}

	graph := game.NewStateGraph()
	graph.AddNode(game.StateMainVillage)

	navigator := game.NewNavigator(client, cal, graph, classify)

	attackExec := attack.NewExecutor(client, cal, &cfg.Attack)
	attackExec.SetClassifier(classify)

	trainer := training.NewTrainer(client, cal, &cfg.Training)
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

	recognizer := game.NewRecognizer()
	ctx, cancel := context.WithCancel(context.Background())

	return &Bot{
		client:     client,
		cal:        cal,
		classifier: classifier,
		navigator:  navigator,
		graph:      graph,
		templates:  templates,
		recognizer: recognizer,
		cfg:        cfg,
		classify:   classify,
		attackExec: attackExec,
		trainer:    trainer,
		ctx:        ctx,
		cancel:     cancel,
		logger:     log.With().Str("bot", "orchestrator").Logger(),
		startedAt:  time.Now(),
	}, nil
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

	if b.seqRunning.Load() {
		return
	}

	// Primary detection: try to find the attack button via template matching
	// This works at any resolution and doesn't rely on the classifier
	if b.templateMatch(screen, "btn_attack", 0.5) {
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

var lastNav time.Time

func (b *Bot) templateMatch(screen gocv.Mat, name string, threshold float32) bool {
	tpl, ok := b.templates.Get(name)
	if !ok {
		return false
	}
	matches, err := vision.MatchMultiScale(screen, tpl, 0.4, 2.0, 25, threshold)
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

	lootRec := game.NewLootRecognizer(b.cal, b.templates)

	for {
		time.Sleep(2 * time.Second)

		screen, err := b.client.CaptureToMat()
		if err != nil {
			return
		}

		state, _ := b.classify(screen)
		if state != game.StateBattle {
			b.logger.Debug().Str("state", state.String()).Msg("waiting for battle state...")

			if state == game.StateSearchMap || state == game.StateLoading {
				screen.Close()
				continue
			}

			if state == game.StateArmyCamp {
			} else {
				screen.Close()
				continue
			}
		}

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
			Int("req_gold", b.cfg.Search.MinLootGold).
			Int("req_elixir", b.cfg.Search.MinLootElixir).
			Int("req_de", b.cfg.Search.MinLootDarkElixir).
			Msg("loot too low, skipping base...")

		screen.Close() // Close before findAndClick which does its own capture

		if !b.findAndClick("btn_next", "Next Match", 3) {
			b.logger.Warn().Msg("could not find 'Next' button, using fallback coordinates")
			nextX, nextY := b.cal.ScaleRef(770, 560)
			b.client.Tap(nextX, nextY)
		}

		// Wait for next base to load
		time.Sleep(3 * time.Second)
	}

	b.logger.Info().Msg("battle complete, ending...")
	b.attackExec.EndBattle()
	b.attackExec.WaitForBattleEnd(60 * time.Second)
	b.attackExec.ReturnHome()

	b.attackCount.Add(1)
	b.logger.Info().
		Int32("total", b.attackCount.Load()).
		Dur("runtime", time.Since(b.startedAt)).
		Msg("attack session stats")
}

func (b *Bot) clickSequence() bool {
	// Step 1: find and click the orange Attack button on the main village
	if !b.findAndClick("btn_attack", "Attack", 3) {
		return false
	}
	// Wait for the attack menu to open (find match button appears)
	if !b.waitForButton("btn_find_match", 10*time.Second) {
		b.logger.Warn().Msg("find match button did not appear")
		return false
	}
	// Step 2: click the yellow Find Match button
	if !b.findAndClick("btn_find_match", "Find Match", 3) {
		return false
	}
	// Wait for army selector to appear
	if !b.waitForButton("btn_army_arrow", 5*time.Second) {
		b.logger.Warn().Msg("army arrow did not appear")
		return false
	}
	// Step 3: click the white army arrow to expand army selection
	b.findAndClick("btn_army_arrow", "Army Arrow", 2)
	// Wait for army 1 preset button
	if !b.waitForButton("btn_army_1", 3*time.Second) {
		b.logger.Warn().Msg("army 1 button did not appear, continuing anyway")
	}
	// Step 4: click army composition 1
	b.findAndClick("btn_army_1", "Army 1", 2)
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
	b.logger.Info().Msg("waiting for battle state...")
	return b.waitForBattleState(30 * time.Second)
}

func (b *Bot) waitForButton(templateName string, timeout time.Duration) bool {
	tpl, ok := b.templates.Get(templateName)
	if !ok {
		b.logger.Error().Str("template", templateName).Msg("template not loaded")
		return false
	}
	b.logger.Debug().Str("template", templateName).Msg("waiting for button")
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		screen, err := b.client.CaptureToMat()
		if err != nil {
			time.Sleep(200 * time.Millisecond)
			continue
		}
		matches, _ := vision.MatchMultiScale(screen, tpl, 0.4, 2.0, 25, 0.5)
		screen.Close()
		if len(matches) > 0 {
			return true
		}
		b.dismissInterruptions()
		time.Sleep(200 * time.Millisecond)
	}
	return false
}

func (b *Bot) findAndClick(templateName, stepName string, maxRetries int) bool {
	tpl, ok := b.templates.Get(templateName)
	if !ok {
		b.logger.Error().Str("template", templateName).Msg("template not loaded")
		return false
	}

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

		matches, err := vision.MatchMultiScale(screen, tpl, 0.4, 2.0, 25, 0.55)
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
		for _, m := range matches[1:] {
			if m.Confidence > best.Confidence {
				best = m
			}
		}

		px, py := best.Point.X, best.Point.Y

		b.logger.Info().
			Str("step", stepName).
			Float64("conf", best.Confidence).
			Int("x", px).Int("y", py).
			Msg("clicking")

		if err := b.client.Tap(px, py); err != nil {
			b.logger.Error().Err(err).Msg("tap failed")
			return false
		}

		return true
	}

	b.logger.Error().Str("step", stepName).Int("retries", maxRetries).Msg("failed after retries")
	return false
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
			b.logger.Info().Msg("battle state detected")
			return true
		case state == game.StateSearchMap || state == game.StateLoading:
			time.Sleep(1 * time.Second)
			continue
		default:
			b.dismissInterruptions()
			time.Sleep(500 * time.Millisecond)
		}
	}

	b.logger.Warn().Dur("timeout", timeout).Msg("timed out waiting for battle")
	return false
}

func (b *Bot) deployTroops(screen gocv.Mat) {
	plan, err := b.attackExec.LoadStrategy(b.cfg.Attack.StrategyFile)
	if err != nil {
		b.logger.Warn().Err(err).Msg("could not load strategy, using default drops")
		return
	}

	redPts, err := b.attackExec.AnalyzeRedArea(screen)
	if err != nil {
		b.logger.Warn().Err(err).Msg("red area analysis failed")
	}

	b.logger.Info().
		Str("strategy", plan.Strategy.Name).
		Int("drops", len(plan.DropOrder)).
		Int("red_pts", len(redPts)).
		Msg("executing attack plan")

	if err := b.attackExec.DeployAll(plan, screen, redPts); err != nil {
		b.logger.Error().Err(err).Msg("deploy failed")
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

func init() {
	runtime.GOMAXPROCS(0)
}