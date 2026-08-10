package bot

import (
	"context"
	"encoding/json"
	"fmt"
	"image"
	"os"
	"path/filepath"
	"runtime"
	"strings"
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
	client     *adb.Client
	cal        *game.Calibration
	classifier *game.Classifier
	navigator  *game.Navigator
	graph      *game.StateGraph
	templates  *game.TemplateStore
	recognizer *game.Recognizer
	cfg        *config.BotConfig

	classify func(gocv.Mat) (game.GameState, int)

	attackExec *attack.Executor
	trainer    *training.Trainer

	ctx    context.Context
	cancel context.CancelFunc
	logger zerolog.Logger

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

	chestDismissInFlight  atomic.Bool
	splashDismissInFlight atomic.Bool
	startedAt             time.Time
	lastAction            time.Time
	lastSequenceStart     time.Time
	lastNav               time.Time
	lastCapture           time.Time
	lastIdlePan           time.Time
	stuckTimeout          time.Duration
	cpuSampler            *cpuSampler

	dukePicksFile *os.File

	historyCache []AttackReport

	OnStatsUpdate func()
}

// NewBot builds a fully-booted Bot using a background context (no
// external cancellation). CLI and tests use this; the Wails app uses
// NewBotWithContext so a Stop click can abort a boot in progress.
func NewBot(cfg *config.BotConfig) (*Bot, error) {
	return NewBotWithContext(context.Background(), cfg)
}

// NewBotWithContext boots the bot under the caller's context. The
// context is threaded through the boot orchestrator AND becomes the
// parent of the bot's runtime context, so cancelling it (the app's
// Stop click) aborts an in-progress boot and stops a running bot.
//
// On any error the freshly-constructed adb.Client is closed — a
// failed boot must not leak a half-open transport (visible as a
// lingering localhost:5555 ghost that breaks the next Start).
func NewBotWithContext(bootCtx context.Context, cfg *config.BotConfig) (b *Bot, err error) {
	zl := &adbLogAdapter{log: log.Logger}

	client := adb.NewClient(
		adb.WithHost(cfg.Device.ADBHost),
		adb.WithPort(cfg.Device.ADBPort),
		adb.WithLogger(zl),
		adb.WithTimeout(30*time.Second),
		adb.WithZoomKeys(cfg.Device.ZoomOutKey, cfg.Device.ZoomInKey),
		adb.WithJitterTaps(cfg.Debug.JitterTaps),
		adb.WithJitterDelays(cfg.Debug.JitterDelays),
		adb.WithMaxJitterPixels(cfg.Debug.MaxJitterPixels),
		adb.WithJitterFraction(cfg.Debug.JitterFraction),
	)
	client.DeviceID = cfg.Device.DeviceID

	// If any step below fails before the client is handed to the Bot,
	// release the transport so a subsequent StartBot starts clean.
	defer func() {
		if err != nil && b == nil {
			_ = client.Close()
		}
	}()

	log.Info().Msg("initializing bot startup sequence...")

	bootCfg := NewBootConfigFromBotConfig(cfg)
	if devFastFail() {
		bootCfg = bootCfg.WithDevFastFail()
	}
	orchestrator := NewBootOrchestrator(bootCfg, client, log.Logger)
	bctx, err := orchestrator.Boot(bootCtx)
	if err != nil {

		wrapped := fmt.Errorf("%s: %w", orchestrator.Report().Summary(), err)
		log.Error().Err(wrapped).
			Str("suggested_action", orchestrator.Report().Snapshot().SuggestedAction).
			Str("steps", orchestrator.Report().JoinedStepSummary(200)).
			Msg("boot orchestrator failed; structured report at logs/last_boot_report.json")
		return nil, wrapped
	}

	log.Info().
		Int("screen_w", bctx.ScreenW).
		Int("screen_h", bctx.ScreenH).
		Str("recovery", strings.Join(bctx.RecoveryUsed, ",")).
		Dur("boot_duration", bctx.BootDuration).
		Msg("boot complete; calibrating...")

	w, h := bctx.ScreenW, bctx.ScreenH
	if w <= 0 || h <= 0 {
		return nil, fmt.Errorf("boot returned invalid screen size %dx%d; cannot calibrate", w, h)
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

	packageName := cfg.Device.PackageName
	if packageName == "" {
		packageName = "com.supercell.clashofclans"
	}
	if cfg.Device.RestartOnStartup {
		log.Info().Str("package", packageName).Msg("ensuring clean state by restarting game...")
		if err := client.ForceStop(packageName); err != nil {
			log.Warn().Err(err).Msg("failed to force stop game during startup")
		}
		client.JitteredSleep(2 * time.Second)

		log.Info().Str("package", packageName).Msg("launching game...")
		if err := client.StartApp(packageName); err != nil {
			return nil, fmt.Errorf("failed to start game: %w", err)
		}
		log.Info().Msg("waiting for game to settle...")
		client.JitteredSleep(bootCfg.WaitForGameSettle)
	} else {
		log.Info().Msg("skipping game restart on startup (restart_on_startup=false)")
	}

	if profile, perr := LoadBootProfile(paths.ResolveConfig("boot_profile.json")); perr == nil {
		profile.AddSample(BootProfileSample{
			StartedAt: bctx.Report.StartedAt,
			Duration:  bctx.BootDuration.Milliseconds(),
			Outcome:   "ok",
		})
		if perr := profile.Save(paths.ResolveConfig("boot_profile.json")); perr != nil {
			log.Debug().Err(perr).Msg("failed to persist boot profile")
		}
	}

	graph := game.NewStateGraph()
	graph.AddNode(game.StateMainVillage)

	startedWall := time.Now()

	dukePicksDir := paths.ResolveConfig("output/duke_picks")
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
		// NEVER leave templates nil: every consumer (classifier,
		// navigator, loot recognizer, attack executor) dereferences it
		// without a nil check, and the first such dereference panics
		// (observed live: SIGSEGV in LootRecognizer.prepareDigitTemplates
		// killing the bot the moment it entered an attack). The empty
		// store degrades every lookup to "not found" so the bot keeps
		// running on its color/pinpoint heuristics.
		log.Warn().Err(err).Msg("template store init failed; continuing WITHOUT templates (color/pinpoint fallbacks only)")
		templates = game.NewEmptyTemplateStore()
	}

	if templates != nil {
		templates.LoadTemplates()
		log.Info().Int("templates", templates.Count()).Msg("templates loaded")
	}

	recognizer := game.NewRecognizer()
	// Derive the bot's runtime context from the caller's context so a
	// Stop that cancels the boot also tears down a successfully-booted
	// bot without waiting for the App-level bot.Cancel() to be called.
	ctx, cancel := context.WithCancel(bootCtx)

	b = &Bot{
		client:            client,
		cal:               cal,
		graph:             graph,
		templates:         templates,
		recognizer:        recognizer,
		cfg:               cfg,
		attackExec:        attackExec,
		trainer:           trainer,
		ctx:               ctx,
		cancel:            cancel,
		logger:            log.With().Str("bot", "orchestrator").Logger(),
		startedAt:         startedWall,
		lastAction:        time.Now(),
		lastSequenceStart: time.Now(),
		lastIdlePan:       time.Now(),
		stuckTimeout:      35 * time.Second,
		cpuSampler:        newCPUSampler(),
		dukePicksFile:     dukePicksFile,
	}

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

	if histData, err := os.ReadFile(paths.ResolveConfig("attack_history.json")); err == nil {
		var seeded []AttackReport
		if jsonErr := json.Unmarshal(histData, &seeded); jsonErr == nil {
			b.historyCache = seeded
		} else {
			b.logger.Warn().Err(jsonErr).Msg("failed to parse attack history, starting fresh")
		}
	}

	b.classifier = game.NewClassifier(cal, game.DefaultClassifierConfig(), b.logger)
	if b.templates != nil {
		b.classifier.SetTemplates(b.templates)
	}

	b.classify = func(mat gocv.Mat) (game.GameState, int) {
		return b.classifier.ClassifyState(mat)
	}

	b.navigator = game.NewNavigator(client, cal, graph, b.classify, b.logger)
	if b.templates != nil {
		b.navigator.SetTemplates(b.templates)
	}

	b.navigator.SetDisableChestDismissal(b.cfg.Device.DisableChestDismissal)

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

	focusX, focusY := b.cal.ScaleRef(842, 345)
	b.logger.Info().Int("x", focusX).Int("y", focusY).Msg("performing initial focus click")
	b.client.Tap(focusX, focusY)
	b.client.JitteredSleep(1 * time.Second)

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

// Cancel signals the bot's context to stop. The captureLoop and any
// in-flight attack sequence see the cancellation on their next
// `b.ctx.Done()` check (sub-millisecond), which is what makes the
// Stop button feel "instant" to the user — no more taps, captures,
// state transitions, or attack progression. The full `Stop()` path
// (ADB close, async-writer drain, file flush) is intentionally kept
// out of this method so callers can split the work: `Cancel()` for
// the synchronous "stop what you're doing" signal, and `Stop()` for
// the heavier teardown that App.StopBot detaches into a goroutine.
func (b *Bot) Cancel() {
	b.cancel()
}
func (b *Bot) captureLoop() {
	gc := game.NewGameContext()

	type frame struct {
		mat gocv.Mat
		err error
		dur time.Duration
	}
	frames := make(chan frame, 1)

	getCaptureInterval := func() time.Duration {
		switch gc.State {
		case game.StateBattle, game.StateSearchMap, game.StateLoading:
			return 100 * time.Millisecond
		case game.StateMainVillage, game.StateArmySelection, game.StateArmyCamp:
			return 300 * time.Millisecond
		default:
			return 1000 * time.Millisecond
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
			b.lastCapture = lastCapture

			if err != nil || screen.Empty() || screen.Cols() < 2 || screen.Rows() < 2 {
				screen.Close()
				b.logger.Debug().Err(err).Msg("empty/degenerate capture dropped")
				continue
			}

			select {
			case frames <- frame{mat: screen, err: err, dur: dur}:
			default:
				screen.Close()
			}
		}
	}()

	for {
		select {
		case <-b.ctx.Done():
			select {
			case f := <-frames:
				if f.mat.Cols() >= 1 && f.mat.Rows() >= 1 {
					f.mat.Close()
				}
			default:
			}
			return
		case f := <-frames:
			// Panic guard: one bad frame (degenerate mat, classifier
			// edge case, cgo hiccup) must never kill the whole bot
			// process — an unattended farm would stay dead until a
			// human notices. Recover, release the frame, and keep
			// the loop alive; the stuck-watchdog handles the rest.
			func() {
				defer func() {
					if r := recover(); r != nil {
						b.logger.Error().Interface("panic", r).Msg("recovered panic in frame processing; continuing capture loop")
						if !f.mat.Empty() {
							f.mat.Close()
						}
						b.recordActivity()
					}
				}()
				b.checkStuck(gc)
				b.processFrame(gc, f.mat, f.err, f.dur)
			}()
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

	if gc.ReadHealth().ConsecutiveFails >= 10 {
		b.logger.Error().
			Int("consecutive_fails", gc.ReadHealth().ConsecutiveFails).
			Str("state", gc.State.String()).
			Msg("capture pipeline appears dead, beginning device recovery ladder...")
		b.recoverEmulator()
		b.lastSequenceStart = time.Now()
		return
	}

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

	// Post-boot splash states (ТАР! collect splash, castle logo, news)
	// legitimately sit static for 1-3 minutes while the game connects — the
	// castle logo has no progress indicator at all. The generic stuck timeout
	// below (35s) would force-restart mid-boot, which previously caused an
	// endless force-stop/relaunch loop on the collect splash. Give the whole
	// boot-splash chain a generous window; the dismiss taps in processFrame
	// advance through it.
	if state == game.StateLogo || state == game.StateTapToContinue || state == game.StateNewsSplash {
		bootStuck := time.Since(b.lastAction)
		const bootSplashTimeout = 5 * time.Minute
		if bootStuck > bootSplashTimeout {
			b.logger.Warn().
				Str("state", state.String()).
				Time("last_action", b.lastAction).
				Dur("stuck_time", bootStuck).
				Dur("timeout", bootSplashTimeout).
				Msg("boot splash stuck too long, triggering emergency restart...")
			b.restartGame()
			b.lastSequenceStart = time.Now()
		}
		return
	}

	if state == game.StateBattle ||
		state == game.StateSearchMap ||
		state == game.StateLoading {
		attackPhaseStuck := time.Since(b.lastAction)
		const attackPhaseTimeout = 30 * time.Second
		if attackPhaseStuck > attackPhaseTimeout {
			b.logger.Warn().
				Str("state", state.String()).
				Time("last_action", b.lastAction).
				Dur("stuck_time", attackPhaseStuck).
				Dur("timeout", attackPhaseTimeout).
				Msg("attack-phase state without active sequence, triggering emergency restart...")
			b.restartGame()
			b.lastSequenceStart = time.Now()
		}
		return
	}

	timeout := b.stuckTimeout

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

	b.client.JitteredSleep(2 * time.Second)

	if err := b.client.StartApp(pkg); err != nil {
		b.logger.Error().Err(err).Msg("failed to start app")
	}

	b.client.JitteredSleep(15 * time.Second)
	b.zoomedOut.Store(false)

	b.lastAction = time.Now()
	b.lastNav = time.Now()
	b.lastSequenceStart = time.Now()
}

// recoverEmulator is the mid-run escalation for a dead capture
// pipeline. restartGame only force-stops CoC — when the EMULATOR
// itself is gone (BlueStacks crashed, adb-server wedged, transport
// socket stale) that just fails silently and the bot spins at high
// CPU against a dead device forever. recoverEmulator walks a
// cheap→destructive ladder, re-probing liveness (wm size) after
// each step, and only relaunches BlueStacks as a last resort:
//
//	1. screen-size probe     — device alive? just restart the game
//	2. transport Reconnect   — stale socket after a BlueStacks blip
//	3. ResetAdbServer        — stale adb-server registration
//	   (note: drops ALL adb connections on this host — logged)
//	4. EnsureBlueStacksMac   — emulator really gone; relaunch at the
//	   configured resolution, then poll up to 2 min for adb
func (b *Bot) recoverEmulator() {
	b.logger.Warn().Msg("capture pipeline dead; beginning device recovery ladder")

	deviceOK := func() bool {
		_, _, err := b.client.ScreenSize()
		return err == nil
	}

	if deviceOK() {
		b.logger.Info().Msg("device still responsive; restarting game only")
		b.restartGame()
		return
	}

	b.logger.Warn().Msg("device unresponsive to wm size; reconnecting ADB transport")
	if err := b.client.Reconnect(); err != nil {
		b.logger.Warn().Err(err).Msg("transport reconnect failed")
	}
	if deviceOK() {
		b.restartGame()
		return
	}

	b.logger.Warn().Msg("device still unreachable; resetting adb server (drops ALL adb connections on this host)")
	if err := b.client.ResetAdbServer(); err != nil {
		b.logger.Warn().Err(err).Msg("adb server reset failed")
	}
	time.Sleep(2 * time.Second)
	_ = b.client.Reconnect()
	if deviceOK() {
		b.restartGame()
		return
	}

	b.logger.Error().Msg("device unreachable after transport + adb-server recovery; relaunching BlueStacks")
	if err := b.client.EnsureBlueStacksMac(b.cfg.Device.Width, b.cfg.Device.Height, b.cfg.Device.DPI); err != nil {
		b.logger.Error().Err(err).Msg("BlueStacks relaunch failed; will retry on next stuck check")
	}
	// Give the freshly-relaunched emulator up to 2 minutes to expose
	// its adb daemon (cold VM boot can take 45-70s on this hardware).
	for i := 0; i < 60; i++ {
		if deviceOK() {
			break
		}
		time.Sleep(2 * time.Second)
	}
	b.restartGame()
}

func (b *Bot) processFrame(gc *game.GameContext, screen gocv.Mat, err error, captureMs time.Duration) {
	if err != nil {
		gc.RecordCaptureError()
		b.logger.Debug().Err(err).Msg("capture failed")
		screen.Close()
		return
	}
	if screen.Empty() || screen.Cols() < 2 || screen.Rows() < 2 {
		screen.Close()
		return
	}

	state, score := b.classify(screen)

	gc.UpdateScreen(screen, captureMs)

	if !b.zoomedOut.Load() {

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

				time.Sleep(1800 * time.Millisecond)

				return
			}
		}
	}

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

	if state == game.StateChestReward {
		if b.cfg.Device.DisableChestDismissal {

			b.logger.Debug().Msg("chest detected but dismissal disabled by config; deferring to stuck-watchdog")
			return
		}
		if b.chestDismissInFlight.CompareAndSwap(false, true) {
			b.logger.Info().Msg("chest reward screen detected; dispatching dismiss goroutine")
			go func() {
				defer b.chestDismissInFlight.Store(false)
				start := time.Now()
				if err := b.navigator.DismissChestReward(); err != nil {
					b.logger.Warn().Err(err).
						Dur("elapsed", time.Since(start)).
						Msg("chest dismiss failed; will retry on next detection if still on screen")
				} else {
					b.logger.Info().
						Dur("elapsed", time.Since(start)).
						Msg("chest dismissed")
				}
			}()
		}
		return
	}

	// Post-boot splash screens. The game shows a short chain after every
	// relaunch: the "ТАР!" tap-to-continue / collect splash, then the CoC
	// castle logo (which sits static 1-3 min while the session connects),
	// then an optional news/announcement splash with a Continue button
	// before the village appears. The bot must tap each prompt to advance;
	// previously these read as Battle/Unknown and the stuck-watchdog
	// force-restarted the game in an endless loop.
	if state == game.StateTapToContinue || state == game.StateNewsSplash {
		if b.splashDismissInFlight.CompareAndSwap(false, true) {
			b.logger.Info().Str("state", state.String()).Msg("boot splash detected; dispatching dismiss tap")
			go func(st game.GameState) {
				defer b.splashDismissInFlight.Store(false)
				time.Sleep(1200 * time.Millisecond)

				var x, y int
				switch st {
				case game.StateTapToContinue:
					// Tap the "ТАР!" prompt text (ref 450,195). Verified live:
					// this dismisses the collect splash into the game.
					x, y = b.cal.ScaleRef(450, 195)
				case game.StateNewsSplash:
					// Tap the green Continue button (ref 403,535).
					x, y = b.cal.ScaleRef(403, 535)
				}
				if err := b.client.TapRandomized(x, y); err != nil {
					b.logger.Warn().Err(err).Msg("boot splash dismiss tap failed; will retry on next detection")
					return
				}
				// Deliberately NO recordActivity here: the adb tap reports
				// success even when the splash did not actually dismiss, so
				// recording activity would keep resetting lastAction and
				// the stuck-watchdog (including the boot-splash grace in
				// checkStuck) could never fire on a genuinely stuck splash
				// variant. The real progress signal is the resulting state
				// transition out of the splash, which processFrame records
				// via its own state-change activity call.
				b.logger.Info().Str("state", st.String()).Msg("boot splash dismissed")
			}(state)
		}
		return
	}

	if b.seqRunning.Load() {
		return
	}

	if gc.State == game.StateBattleEnd || gc.State == game.StateReturnHome {
		b.logger.Info().Str("state", gc.State.String()).Msg("detected terminal state without active sequence, returning home...")
		go b.attackExec.ReturnHome()
		b.recordActivity()
		return
	}

	if b.zoomedOut.Load() && (gc.State == game.StateMainVillage || gc.State == game.StateUnknown) && b.findAttackButton(screen, 0.45) {
		b.logger.Info().Msg("attack button detected, starting sequence")
		b.lastSequenceStart = time.Now()
		go b.executeAttackSequence(gc)
		return
	}

	// Idle humanization: while confirmed on the main village with no
	// attack button in sight (army still training / waiting), drift the
	// camera the way a waiting player would. Throttled so the wander
	// never overlaps an attack sequence, and deliberately NOT
	// recordActivity — a genuinely stuck bot must still trip the
	// stuck-watchdog and cycle the game.
	//
	// Dispatched in a goroutine (mirroring the chest-dismiss pattern)
	// so the ~3s sendevent gesture can't freeze the capture loop's UI
	// frame stream; the seqRunning re-check keeps it from colliding
	// with a freshly-started attack sequence. The pan swipes the map
	// center, never the fixed HUD chrome, so a mid-pan capture still
	// sees the attack button.
	if gc.State == game.StateMainVillage && time.Since(b.lastIdlePan) > 18*time.Second {
		b.lastIdlePan = time.Now()
		b.logger.Debug().Msg("idle in village, wandering camera")
		go func() {
			if b.seqRunning.Load() {
				return
			}
			b.navigator.IdlePan()
		}()
	}

	if gc.State == game.StateArmyCamp && time.Since(b.lastNav) > 3*time.Second {
		b.lastNav = time.Now()
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

	return b.colorCheck(screen, x, y,
		gocv.NewScalar(0, 100, 150, 0),
		gocv.NewScalar(150, 255, 255, 0),
		20)
}

// buttonROI returns the normalized (reference-resolution) region of interest
// for a known UI button template. Centralized here so the wait-for-button and
// find-and-click paths share one definition and cannot drift apart.
func (b *Bot) buttonROI(templateName string) image.Rectangle {
	switch templateName {
	case "btn_attack":
		return image.Rect(0, 500, 300, 732)
	case "btn_find_match":
		return image.Rect(50, 400, 400, 600)
	case "btn_battle":
		return image.Rect(300, 150, 860, 732)
	case "btn_army_arrow":
		return image.Rect(350, 100, 700, 300)
	case "btn_army_1":
		return image.Rect(400, 150, 650, 350)
	case "btn_next":
		return image.Rect(600, 450, 860, 732)
	default:
		return image.Rect(0, 0, 860, 732)
	}
}

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
	// Panic guard for the long-running attack goroutine (spawned from
	// processFrame outside the frame-loop recover). A panic anywhere in
	// the deploy/search/battle-parse pipeline must release seqRunning
	// (via the pre-existing defer below, which LIFO-runs first) and log
	// instead of killing the whole bot process. The stuck-watchdog then
	// decides the next move.
	defer func() {
		if r := recover(); r != nil {
			b.logger.Error().Interface("panic", r).Msg("recovered panic in attack sequence; abandoning sequence and continuing")
			b.recordActivity()
		}
	}()
	if !b.seqRunning.CompareAndSwap(false, true) {
		return
	}
	defer b.seqRunning.Store(false)

	if b.cfg.Debug.UseShellPipe {
		b.client.EnablePersistentShell(b.cfg.Debug.ShellPipeSyncFlush)
		defer b.client.ClosePersistentShell()
	}

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
		// Stop check: a user Stop must abort the search loop even
		// though CaptureToMat below would silently reconnect a closed
		// transport and keep searching forever.
		select {
		case <-b.ctx.Done():
			b.logger.Info().Msg("search loop cancelled by stop, abandoning attack sequence")
			lootRec.Close()
			return
		default:
		}

		if time.Since(searchStart) > 5*time.Minute {
			b.logger.Error().Msg("searching/skipping bases took too long (stuck in clouds?), restarting game...")
			b.restartGame()
			lootRec.Close()
			return
		}

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

		meetsReq := !b.cfg.Search.Enabled || (loot.Gold >= b.cfg.Search.MinLootGold &&
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

		screen.Close()

		if !b.findAndClick("btn_next", "Next Match", 2) {
			b.logger.Warn().Msg("template match failed, forcing skip via color/pinpoint")

			searchROI := image.Rect(b.cal.PhysicalW/2, b.cal.PhysicalH/2, b.cal.PhysicalW, b.cal.PhysicalH)
			orangePt, err := vision.PixelSearch(screen, searchROI, 252, 186, 54, 50)
			if err == nil {
				b.logger.Info().Msg("clicking Next via orange color fallback")
				b.client.TapRandomized(orangePt.X, orangePt.Y)
				b.recordActivity()
			} else {

				b.DumpDiagnostics("next_button_not_found", screen, map[string]interface{}{
					"message": "forcing skip via hardcoded coordinates",
				})
				nextX, nextY := b.cal.ScaleRef(796, 565)
				b.client.TapRandomized(nextX, nextY)
				b.recordActivity()
			}
		}

		time.Sleep(600 * time.Millisecond)
	}

	b.logger.Info().Msg("battle deployment complete, waiting for battle to end naturally...")

	var battleStars int = 0
	var battleGold int = 0
	var battleElixir int = 0
	var battleDE int = 0
	var bonusGold, bonusElixir, bonusDE int = 0, 0, 0
	var parsedResults bool = false

	if b.attackExec.WaitForBattleEndCtx(b.ctx, 4*time.Minute) {

		// WaitForBattleEnd returns the moment the result overlay's Return
		// Home button is detected, but the overlay is still animating in:
		// the star counter lights up one-by-one and the loot numbers count
		// up over ~1.5-2s. Capturing right then freezes the animation at
		// frame 0, and the OCR reads it as 0 stars / 0 loot (observed live:
		// a 50%-destruction battle parsed as 0 stars / 0 loot, while the
		// SAME screenshot parsed minutes later as 1 star / 444k gold).
		//
		// Settle first, then parse. If the read comes back all-zero we
		// cannot assume the animation is still running — a genuine 0%
		// destruction loss reads exactly the same. The discriminator is
		// frame stability: once two captures ~1s apart are pixel-identical
		// in the result panel, the count-up finished and the (possibly
		// zero) value is the true result. Every attempt overwrites
		// last_battle_result.png with the freshest frame so the saved
		// artifact matches the final parse.
		b.client.JitteredSleep(1800 * time.Millisecond)

		var parsedResult game.BattleResult
		parsedOK := false
		prevHash := uint64(0)
		for attempt := 0; attempt < 3 && !parsedOK; attempt++ {
			resultScreen, err := b.client.CaptureToMat()
			if err != nil {
				b.logger.Warn().Err(err).Msg("battle result capture failed; retrying")
				time.Sleep(500 * time.Millisecond)
				continue
			}
			gocv.IMWrite(paths.ResolveConfig("last_battle_result.png"), resultScreen)
			b.logger.Info().Msg("saved battle result screenshot to last_battle_result.png")

			lootRec := game.NewLootRecognizer(b.cal, b.templates, b.logger)
			res, rerr := lootRec.ReadBattleResult(resultScreen)
			hash := resultPanelHash(resultScreen, b.cal)
			resultScreen.Close()
			lootRec.Close()

			if rerr != nil {
				b.logger.Warn().Err(rerr).Msg("battle result parse error; retrying")
				time.Sleep(800 * time.Millisecond)
				continue
			}

			settled := hash != 0 && hash == prevHash
			if res.Stars > 0 || res.Loot.Gold > 0 || res.Loot.Elixir > 0 || res.Loot.DarkElixir > 0 || settled {
				parsedResult = res
				parsedOK = true
				if settled {
					b.logger.Debug().Msg("battle result accepted: result panel stable across captures")
				} else {
					b.logger.Debug().Msg("battle result accepted (non-empty read)")
				}
				break
			}

			prevHash = hash
			b.logger.Warn().Int("attempt", attempt).Msg("battle result read empty and panel still changing; overlay animating, retrying...")
			time.Sleep(1000 * time.Millisecond)
		}

		if parsedOK {
			battleStars = parsedResult.Stars
			battleGold = parsedResult.Loot.Gold
			battleElixir = parsedResult.Loot.Elixir
			battleDE = parsedResult.Loot.DarkElixir
			bonusGold = parsedResult.Bonus.Gold
			bonusElixir = parsedResult.Bonus.Elixir
			bonusDE = parsedResult.Bonus.DarkElixir
			parsedResults = true

			b.totalGold.Add(int64(parsedResult.Loot.Gold + parsedResult.Bonus.Gold))
			b.totalElixir.Add(int64(parsedResult.Loot.Elixir + parsedResult.Bonus.Elixir))
			b.totalDE.Add(int64(parsedResult.Loot.DarkElixir + parsedResult.Bonus.DarkElixir))
			b.totalStars.Add(int32(parsedResult.Stars))

			switch parsedResult.Stars {
			case 0:
				b.stars0.Add(1)
			case 1:
				b.stars1.Add(1)
			case 2:
				b.stars2.Add(1)
			case 3:
				b.stars3.Add(1)
			}

			b.logger.Info().
				Int("stars", parsedResult.Stars).
				Int("gold", parsedResult.Loot.Gold).
				Int("bonus_gold", parsedResult.Bonus.Gold).
				Msg("battle result processed")
		} else {
			b.logger.Error().Msg("battle result OCR failed after retries; recording unparsed attack")
		}
	} else if b.ctx.Err() != nil {
		// The bot was stopped mid-battle. Exit cleanly — no forced
		// restart (the ADB client is already being torn down by the
		// detached Stop, and restartGame would just log a stream of
		// transport errors against a closed client).
		b.logger.Info().Msg("battle ended by user stop, abandoning sequence")
		return
	} else {
		b.logger.Error().Msg("battle end timeout (stuck in battle?), restarting game...")
		b.restartGame()
		return
	}

	// Record the attack in history IMMEDIATELY after the result is
	// parsed, so the Attack History row appears in the UI at the same
	// moment the loot totals tick up. Previously this block ran after
	// ReturnHome + wall upgrades (which can take minutes), so the
	// dashboard showed fresh gold/elixir totals for minutes before the
	// history row appeared — and the entry was dropped entirely if
	// ReturnHome failed.
	b.attackCount.Add(1)

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

	history := b.historyCache
	if history == nil {
		if histData, err := os.ReadFile(paths.ResolveConfig("attack_history.json")); err == nil {
			_ = json.Unmarshal(histData, &history)
		}
	}
	history = append([]AttackReport{rep}, history...)
	if len(history) > 500 {
		history = history[:500]
	}
	b.historyCache = history
	if histBytes, err := json.MarshalIndent(history, "", "  "); err == nil {
		_ = AsyncWriteFile(paths.ResolveConfig("attack_history.json"), histBytes, 0644)
	}

	// Notify the UI AFTER the report is in historyCache and
	// attack_history.json is flushed to disk. Firing this earlier
	// (right after ReadBattleResult) raced the App's refreshHistory
	// cache re-read with the report write, so the UI stayed a full
	// attack behind even though the loot totals (live atomics) moved
	// instantly. AsyncWriteFile blocks until the worker flushes, so
	// by the time we get here the file on disk contains this report.
	if b.OnStatsUpdate != nil {
		b.OnStatsUpdate()
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

	sideX := int(537 * b.cal.ScaleX)
	sideY := int(693 * b.cal.ScaleY)
	b.logger.Info().Msg("Tapping side area to dismiss potential post-attack popups...")
	_ = b.client.Tap(sideX, sideY)
	time.Sleep(1000 * time.Millisecond)

	if b.cfg.Upgrade.UpgradeWalls {
		b.UpgradeWalls(gc)
	}

	// Cap check stays after wall upgrades so the graceful shutdown (2s
	// grace then cancel) never interrupts an in-progress wall loop; the
	// count itself was already incremented when the report was recorded.
	if int(b.attackCount.Load()) >= b.cfg.Attack.MaxAttackPerSession {
		b.logger.Info().
			Int32("attacks", b.attackCount.Load()).
			Int("cap", b.cfg.Attack.MaxAttackPerSession).
			Msg("attack cap reached, scheduling graceful shutdown...")
		go func() {

			time.Sleep(2 * time.Second)
			b.cancel()
		}()
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

	b.logger.Info().
		Int32("attacks", b.attackCount.Load()).
		Str("stars", fmt.Sprintf("3⭐:%d | 2⭐:%d | 1⭐:%d | 0⭐:%d", b.stars3.Load(), b.stars2.Load(), b.stars1.Load(), b.stars0.Load())).
		Str("loot", fmt.Sprintf("Gold: %d | Elixir: %d | DE: %d", b.totalGold.Load(), b.totalElixir.Load(), b.totalDE.Load())).
		Dur("uptime", time.Since(b.startedAt)).
		Msg("=== SESSION SUMMARY ===")

	b.zoomedOut.Store(false)
}

func (b *Bot) clickSequence() bool {

	attackClicked := false
	for attempt := 0; attempt < 3; attempt++ {
		if b.findAndClick("btn_attack", "Attack", 1) {
			attackClicked = true
			break
		}
		b.client.JitteredSleep(500 * time.Millisecond)
	}
	if !attackClicked {
		b.logger.Warn().Msg("could not find or click Attack button")
		if screen, err := b.client.CaptureToMat(); err == nil {
			b.DumpDiagnostics("click_attack_failed", screen, nil)
			screen.Close()
		}
		return false
	}
	b.client.JitteredSleep(500 * time.Millisecond)

	findMatchClicked := false
	for attempt := 0; attempt < 3; attempt++ {
		if b.findAndClick("btn_find_match", "Find Match", 1) {
			findMatchClicked = true
			break
		}
		b.client.JitteredSleep(500 * time.Millisecond)
	}
	if !findMatchClicked {
		b.logger.Warn().Msg("could not find or click Find Match button")
		if screen, err := b.client.CaptureToMat(); err == nil {
			b.DumpDiagnostics("click_find_match_failed", screen, nil)
			screen.Close()
		}
		return false
	}
	b.client.JitteredSleep(500 * time.Millisecond)

	armyArrowClicked := false
	for attempt := 0; attempt < 3; attempt++ {
		if b.findAndClick("btn_army_arrow", "Army Arrow", 1) {
			armyArrowClicked = true
			break
		}
		b.client.JitteredSleep(500 * time.Millisecond)
	}
	if !armyArrowClicked {
		b.logger.Warn().Msg("could not find or click Army Arrow button")
		if screen, err := b.client.CaptureToMat(); err == nil {
			b.DumpDiagnostics("click_army_arrow_failed", screen, nil)
			screen.Close()
		}
		return false
	}
	b.client.JitteredSleep(500 * time.Millisecond)

	army1Clicked := false
	for attempt := 0; attempt < 3; attempt++ {
		if b.findAndClick("btn_army_1", "Army 1", 1) {
			army1Clicked = true
			break
		}
		b.client.JitteredSleep(500 * time.Millisecond)
	}
	if !army1Clicked {
		b.logger.Warn().Msg("army 1 button did not appear, continuing anyway")
		if screen, err := b.client.CaptureToMat(); err == nil {
			b.DumpDiagnostics("click_army_1_not_found", screen, nil)
			screen.Close()
		}
	}
	b.client.JitteredSleep(500 * time.Millisecond)

	battleClicked := false
	for attempt := 0; attempt < 3; attempt++ {
		if b.findAndClick("btn_battle", "Battle", 1) {
			battleClicked = true
			break
		}
		b.client.JitteredSleep(500 * time.Millisecond)
	}
	if !battleClicked {
		b.logger.Warn().Msg("could not find or click Battle button")
		return false
	}

	b.logger.Info().Msg("waiting for battle state (searching)...")
	return b.waitForBattleState(60 * time.Second)
}

// Pinpoint defines a precise location on the reference screen (860x732)
// and a color check to verify it before clicking.
type Pinpoint struct {
	X, Y int
	Name string
}

var villagePinpoints = map[string]Pinpoint{
	"btn_attack":      {X: 64, Y: 666, Name: "Attack"},
	"btn_find_match":  {X: 158, Y: 494, Name: "Find Match"},
	"btn_battle":      {X: 731, Y: 537, Name: "Battle"},
	"btn_army_arrow":  {X: 514, Y: 192, Name: "Army Arrow"},
	"btn_army_1":      {X: 513, Y: 230, Name: "Army 1"},
	"btn_next":        {X: 794, Y: 577, Name: "Next Match"},
	"btn_return_home": {X: 431, Y: 581, Name: "Return Home"},
	"btn_okay":        {X: 430, Y: 520, Name: "Okay"},
}

func (b *Bot) findAndClick(templateName, stepName string, maxRetries int) bool {

	if pp, ok := villagePinpoints[templateName]; ok {
		px, py := b.cal.ScaleRef(pp.X, pp.Y)
		b.logger.Info().Str("step", stepName).Msg("pinpoint match, clicking...")
		// TapRandomized = Gaussian jitter + the 180-450ms human reaction
		// delay, so the bot visibly hesitates before committing to each
		// decision tap the way a player would.
		if err := b.client.TapRandomized(px, py); err == nil {
			time.Sleep(1000 * time.Millisecond)
			b.recordActivity()
			return true
		}
	}

	tpl, ok := b.templates.Get(templateName)
	if !ok {
		b.logger.Error().Str("template", templateName).Msg("template not loaded")
		return false
	}

	roi := b.buttonROI(templateName)

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

		if templateName == "btn_battle" && retry == 0 {
			altX, altY := b.cal.ScaleRef(525, 247)
			if b.isGreen(screen, altX, altY) {
				screen.Close()
				b.logger.Info().Str("step", stepName).Msg("secondary pinpoint match (upper battle), clicking...")
				if err := b.client.TapRandomized(altX, altY); err == nil {
					b.recordActivity()
					return true
				}
				screen, _ = b.client.CaptureToMat()
			}
		}

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

		if err := b.client.TapRandomized(px, py); err != nil {
			b.logger.Error().Err(err).Msg("tap failed")
			return false
		}
		b.recordActivity()

		return true
	}

	if pp, ok := villagePinpoints[templateName]; ok {
		px, py := b.cal.ScaleRef(pp.X, pp.Y)
		b.logger.Warn().Str("step", pp.Name).Msg("pinpoint color check and template match failed; executing blind tap fallback")
		if err := b.client.TapRandomized(px, py); err == nil {
			b.recordActivity()
			return true
		}
	}

	b.logger.Error().Str("step", stepName).Int("retries", maxRetries).Msg("failed after retries")
	return false
}

// resultPanelHash returns a cheap content hash of the end-of-battle
// result panel region (star row + battle-loot and bonus columns). Two
// captures with an equal nonzero hash mean the panel has finished
// rendering — used to distinguish a still-counting-up overlay from a
// genuine 0-star/0-loot result.
func resultPanelHash(screen gocv.Mat, cal *game.Calibration) uint64 {
	// Reference (860x732) region covering the stars and both loot
	// columns; generous bounds tolerate small theme shifts.
	ref := image.Rect(300, 180, 690, 470)
	x0 := int(float64(ref.Min.X) * cal.ScaleX)
	y0 := int(float64(ref.Min.Y) * cal.ScaleY)
	x1 := int(float64(ref.Max.X) * cal.ScaleX)
	y1 := int(float64(ref.Max.Y) * cal.ScaleY)
	if x0 < 0 {
		x0 = 0
	}
	if y0 < 0 {
		y0 = 0
	}
	if x1 > screen.Cols() {
		x1 = screen.Cols()
	}
	if y1 > screen.Rows() {
		y1 = screen.Rows()
	}
	if x1-x0 < 2 || y1-y0 < 2 {
		return 0
	}

	var h uint64 = 14695981039346656037 // FNV-1a offset basis
	stride := 4                         // sample every 4th pixel — plenty for frame-diff detection
	for y := y0; y < y1; y += stride {
		for x := x0; x < x1; x += stride {
			b := uint64(screen.GetUCharAt(y, x*3))
			g := uint64(screen.GetUCharAt(y, x*3+1))
			r := uint64(screen.GetUCharAt(y, x*3+2))
			h ^= (r << 16) | (g << 8) | b
			h *= 1099511628211
		}
	}
	return h
}

func (b *Bot) isGreen(screen gocv.Mat, x, y int) bool {
	return b.colorCheck(screen, x, y,
		gocv.NewScalar(0, 150, 0, 0),
		gocv.NewScalar(120, 255, 120, 0),
		15)
}

func (b *Bot) colorCheck(screen gocv.Mat, x, y int, lower, upper gocv.Scalar, minPixels int) bool {
	if x < 0 || y < 0 || x >= screen.Cols() || y >= screen.Rows() {
		return false
	}
	region := image.Rect(x-10, y-10, x+11, y+11)
	if region.Min.X < 0 {
		region.Min.X = 0
	}
	if region.Min.Y < 0 {
		region.Min.Y = 0
	}
	if region.Max.X > screen.Cols() {
		region.Max.X = screen.Cols()
	}
	if region.Max.Y > screen.Rows() {
		region.Max.Y = screen.Rows()
	}

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

		ox, oy := b.cal.ScaleRef(430, 520)
		b.client.TapRandomized(ox, oy)
	case game.StateChatOpen:
		b.client.Back()
	case game.StateTapToContinue:
		// Post-boot "ТАР!" collect splash — tap the prompt text.
		px, py := b.cal.ScaleRef(450, 195)
		b.client.TapRandomized(px, py)
	case game.StateNewsSplash:
		// Post-boot news splash — tap the green Continue button.
		px, py := b.cal.ScaleRef(403, 535)
		b.client.TapRandomized(px, py)
	}
}

// dismissSelection taps in the background/empty space to close any active
// selection menus. Used as the production Dismiss hook for the wall-upgrade
// loop in RunWallUpgradeLoop. The 500ms settle waits for the menu
// close-animation; without it the next capture can race the menu's
// fade-out and confuse the next template match.
func (b *Bot) dismissSelection() {
	tx, ty := b.cal.ScaleRef(50, 450)
	_ = b.client.Tap(tx, ty)
	time.Sleep(500 * time.Millisecond)
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

	if state != game.StateBattle {
		b.logger.Error().
			Str("state", state.String()).
			Msg("refusing to deploy: screen is not in Battle state; wait for clouds/loading to clear or navigate to a base on the attack screen, then re-run")
		return fmt.Errorf("screen is in state %s; expected Battle — wait for the attack screen to be ready, then re-run", state.String())
	}

	remaining, deployErr := b.deployTroops(screen)
	if deployErr != nil {

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
		LastCapture:      b.lastCapture,
		AvgCaptureMs:     b.client.Health().AvgCaptureMs,
		ConsecutiveFails: b.client.Health().ConsecutiveFails,
		CPUTimeSec:       CPUTime().Seconds(),
		CPUCores:         b.cpuSampler.Usage(),
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

	if b.navigator != nil {
		b.navigator.SetDisableChestDismissal(cfg.Device.DisableChestDismissal)
	}
	b.logger.Info().Msg("bot configuration updated in real-time")
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
		CPUTimeSec:       CPUTime().Seconds(),
		CPUCores:         b.cpuSampler.Usage(),
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

	CPUTimeSec float64 `json:"cpu_time_sec"`

	CPUCores float64 `json:"cpu_cores"`
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
