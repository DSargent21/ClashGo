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

	// classifyGate decides whether the capture loop's frame needs the classifier
	// at all (see classifygate.go). nil means "classify every frame".
	classifyGate *classifyGate

	attackExec *attack.Executor

	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}
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

	chestDismissInFlight    atomic.Bool
	splashDismissInFlight   atomic.Bool
	connLostDismissInFlight atomic.Bool
	lastArmyCampGuardLog    time.Time
	startedAt               time.Time
	lastAction              time.Time
	lastSequenceStart       time.Time
	lastNav                 time.Time
	lastCapture             time.Time
	lastIdlePan             time.Time
	// lastAttackEnd is stamped when a battle fully returns home; the
	// inter-attack cooldown (cfg.Attack.MinSecondsBetweenAttacks) is
	// measured from it. Written by the attack goroutine only.
	lastAttackEnd time.Time
	stuckTimeout  time.Duration
	cpuSampler    *cpuSampler

	dukePicksFile *os.File
	// tapTrace, when non-nil, is the CLASHGO_TAP_TRACE sink: every tap the adb
	// client fires is appended to its file as NDJSON for post-run tap-count
	// audits. nil in every normal run.
	tapTrace *adb.TapTrace
	// armySlot is the 1-based saved-army recipe the strategy wants armed
	// before attacking (strategy YAML army_slot; default 1). Loaded once
	// at construction so clickSequence can select it before the strategy
	// phases run.
	armySlot int

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
		// Share one device capture between consumers reading the same screen. A
		// capture is a guest `screencap` process, i.e. emulator work, and during
		// a battle the frame loop, the deploy verifier and the battle-end watcher
		// all read the same screen; see internal/adb/capturecache.go for the input
		// barrier that keeps a post-tap check from seeing a pre-tap frame.
		adb.WithCaptureCache(cfg.Performance.CaptureCacheTTL()),
	)
	client.DeviceID = cfg.Device.DeviceID

	// CLASHGO_TAP_TRACE=<path> records every tap the client fires to an NDJSON
	// file (one adb.TapEvent per line). The structured attack logs say which
	// unit the bot meant to drop and whether its own verification passed; this
	// says what actually reached the device, in order with timestamps — the
	// only way to answer "is it spam-tapping the hero/siege slot?". Off unless
	// the env var names a path, so normal runs install no hook at all.
	var tapTrace *adb.TapTrace
	if tracePath := os.Getenv("CLASHGO_TAP_TRACE"); tracePath != "" {
		if tr, terr := adb.NewTapTrace(tracePath); terr != nil {
			log.Warn().Err(terr).Str("path", tracePath).
				Msg("tap trace requested but the file could not be opened; continuing without a tap trace")
		} else {
			tapTrace = tr
			client.SetTapHook(tr.Record)
			log.Info().Str("path", tracePath).
				Msg("tap trace enabled: every tap is appended as NDJSON (CLASHGO_TAP_TRACE)")
		}
	}

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
	// NewCalibration derives the display scale (K) from the live geometry as
	// well as the per-axis ratios; at 860x732 every mapping stays the
	// identity, at other geometries the HUD/world anchors follow the layout
	// the game actually renders (see game.Calibration and docs/RESOLUTION.md).
	cal := game.NewCalibration(w, h)
	cal.Verified = true
	// A pinned scale from config wins over the diagonal-ratio derivation
	// (docs/RESOLUTION.md): the derivation measured 1.9% off on 1280x720, and a
	// 1-pixel HUD probe notices 2% at the far edge of the frame.
	if cfg.Device.DisplayScale > 0 {
		cal.SetDisplayScale(cfg.Device.DisplayScale)
	}
	// Name the file the calibration settings came from. A pin that lives in a
	// different tree than the running binary reads is invisible but looks
	// identical in the old log line, and that ambiguity is what hid the split
	// config trees; the path makes the next occurrence self-diagnosing.
	configPath := paths.ResolveConfig("config.json")
	log.Info().
		Int("screen_w", w).
		Int("screen_h", h).
		Float64("display_scale", cal.DisplayScale()).
		Bool("display_scale_pinned", cfg.Device.DisplayScale > 0).
		Str("config_path", configPath).
		Msg("calibration built")

	// Name the asset tree at boot, for the same reason the line above names the
	// config file: `make pick-coords` writes assets/precision_config.json, and a
	// packaged app that read a COPY of assets/ carried inside its bundle was a
	// second, invisible source of geometry. See paths.GetAssetsDir.
	//
	// Two trees at once is worth a warning, not just a log field: the whole cost
	// of the split is that both files look right and only one is obeyed.
	if a := paths.DescribeAssets(); a.Dir != "" {
		ev := log.Info().Str("assets_dir", a.Dir).Str("assets_source", a.Source)
		pinsAge, pinsOK := paths.FileAge(filepath.Join(a.Dir, "precision_config.json"))
		if pinsOK {
			ev = ev.Float64("pins_age_hours", pinsAge.Hours())
		}
		if a.IgnoredDir != "" {
			ev = ev.Str("ignored_assets_dir", a.IgnoredDir)
			if age, ok := paths.FileAge(filepath.Join(a.IgnoredDir, "precision_config.json")); ok {
				ev = ev.Float64("ignored_pins_age_hours", age.Hours())
			}
			ev.Msg("asset tree resolved")
			log.Warn().
				Str("using", a.Dir).
				Str("ignoring", a.IgnoredDir).
				Str("assets_source", a.Source).
				Msg("two asset trees are present; the pins in the one named 'ignoring' are NOT read by this run")
		} else {
			ev.Msg("asset tree resolved")
		}
	}
	// Off the reference geometry the whole dataset is a mapped prediction, so
	// say so loudly: at 1280x720 an unpinned scale left the village with 1 of 7
	// probes passing, and some states misrank (docs/RESOLUTION.md). Behaviour
	// is safe either way — an unrecognised screen is reported as unknown rather
	// than acted on — but the log is where a stall gets explained.
	if !cal.IsReferenceGeometry() {
		if cfg.Device.DisplayScale > 0 {
			log.Warn().
				Int("screen_w", w).
				Int("screen_h", h).
				Float64("display_scale", cfg.Device.DisplayScale).
				Msg("non-reference display geometry with a pinned scale: the classifier and attack geometry are calibrated for " +
					"860x732 at the reference aspect. Validate states live with cmd/screendump and docs/RESOLUTION.md before trusting an attack run.")
		} else {
			log.Warn().
				Int("screen_w", w).
				Int("screen_h", h).
				Float64("derived_scale", cal.DisplayScale()).
				Str("config_path", configPath).
				Msg("non-reference display geometry without a pinned display scale: the derived scale measured 1.9% off at " +
					"1280x720, which loses single-pixel probes. Measure the live scale with cmd/resprobe and set device.display_scale in " +
					"" + configPath + ", or run at 860x732. See docs/RESOLUTION.md.")
		}
	}

	// Zero the guest's Android animation scales before the game starts, so the
	// app picks them up on this launch. This is the one emulator lever in
	// docs/PERFORMANCE.md the bot can pull itself: animated transitions are
	// per-frame guest GPU and CPU work that the bot — reading the screen at 4 Hz
	// and tapping at human pace — can never use, and they are also the reason a
	// frame can be caught half-drawn. Best effort in every direction: a device
	// that refuses the writes boots and runs exactly as it would have, and a
	// device that already reads zero skips the round trips.
	if cfg.Performance.TuneGuestAnimations {
		switch {
		case client.AnimationsAlreadyZeroed():
			log.Info().Msg("guest animation scales already zeroed; skipping")
		default:
			applied, terr := client.TuneGuestAnimations()
			if terr != nil {
				log.Warn().Err(terr).Int("applied", applied).
					Msg("could not zero every guest animation scale; continuing (this is an optimisation, not a precondition)")
			} else {
				log.Info().Int("applied", applied).Msg("zeroed guest animation scales (less guest GPU/CPU work per transition)")
			}
		}
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
		ctx:               ctx,
		cancel:            cancel,
		done:              make(chan struct{}),
		logger:            log.With().Str("bot", "orchestrator").Logger(),
		startedAt:         startedWall,
		lastAction:        time.Now(),
		lastSequenceStart: time.Now(),
		lastIdlePan:       time.Now(),
		stuckTimeout:      35 * time.Second,
		cpuSampler:        newCPUSampler(),
		dukePicksFile:     dukePicksFile,
		tapTrace:          tapTrace,
		classifyGate:      newClassifyGate(cfg.Performance),
	}

	// Resolve the strategy's declared army slot once at boot so the
	// pre-battle click sequence can arm the right saved recipe. The
	// strategy is parsed again later for the deploy phases; this early
	// read only needs army_slot (falls back to slot 1 on any error).
	if strat, err := strategy.ParseYAML(cfg.Attack.StrategyFile); err == nil {
		b.armySlot = strat.SelectedArmySlot()
		log.Info().Int("army_slot", b.armySlot).Str("strategy", strat.Name).Msg("resolved strategy army slot")
	} else {
		b.armySlot = 1
		log.Warn().Err(err).Str("path", cfg.Attack.StrategyFile).Msg("could not pre-read strategy for army slot; defaulting to slot 1")
	}

	// Close `done` the moment the bot's runtime context is cancelled
	// (Stop click in the GUI, or the attack-cap graceful shutdown after
	// a --once CLI run) so external owners — the CLI's main() — can
	// wait on b.Done() instead of polling stats or blocking on a
	// signal that never arrives.
	go func() {
		<-ctx.Done()
		close(b.done)
	}()

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
	// DismissOverlay verifies an overlay against this classifier's own rules before
	// it moves anything (see internal/game/dismiss.go); without it the navigator
	// refuses to dismiss rather than tapping a predicted point.
	b.navigator.SetClassifier(b.classifier)
	if b.templates != nil {
		b.navigator.SetTemplates(b.templates)
	}

	b.navigator.SetDisableChestDismissal(b.cfg.Device.DisableChestDismissal)

	b.attackExec.SetClassifier(b.classify)

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

	// NO startup tap. The bot used to open every session with a "focus click" on
	// the middle of the village; it was the first thing a run did and the first
	// thing it had no evidence for. ADB input goes to the guest, so the click was
	// never what gave the emulator focus, and on a session that starts on a
	// dialog it landed behind the dialog. The capture loop classifies the first
	// frame and dismisses whatever is actually there (see dismissInterruptions),
	// which is the same job with evidence behind it.
	b.logger.Info().Msg("startup: no input fired; the capture loop dismisses what the first frame shows")

	go b.captureLoop()
	return nil
}

func (b *Bot) Stop() {
	b.cancel()
	b.client.Close()
	globalAsyncWriter.Close()
	vision.CloseTemplateCache()
	b.classifyGate.close()
	if b.dukePicksFile != nil {
		_ = b.dukePicksFile.Close()
	}
	if b.tapTrace != nil {
		_ = b.tapTrace.Close()
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

// Done returns a channel that closes when the bot's runtime context
// is cancelled — i.e. after a Stop, or after the attack-cap graceful
// shutdown (a --once CLI run). External owners (the CLI main) wait on
// it so they can exit once the bot finishes its session instead of
// blocking on a signal that never arrives.
func (b *Bot) Done() <-chan struct{} { return b.done }
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
			// 250ms (4Hz) instead of the old 10Hz: during an attack the
			// deploy/battle-end goroutines run their own captures at their
			// own cadence, so the frame loop's full-screen classify was
			// mostly redundant (observed: ~55ms captures + classify at 10Hz
			// pinned one core during every battle). 4Hz still catches the
			// result overlay fast enough for ReturnHome and the stuck
			// watchdog.
			return 250 * time.Millisecond
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

// classifyFrame returns the state for a captured frame, letting the classify
// gate reuse the previous verdict when the screen has not moved enough to
// matter (see classifygate.go). Only the capture loop goes through it: the
// attack sequence classifies the specific frames it captured, where reusing a
// cached verdict would be answering a question nobody asked.
func (b *Bot) classifyFrame(screen gocv.Mat) (game.GameState, int) {
	if b.classifyGate == nil {
		return b.classify(screen)
	}
	state, score, reused := b.classifyGate.classifyFor(screen, b.classify)
	if reused {
		b.logger.Trace().
			Str("state", state.String()).
			Float64("changed_pct", b.classifyGate.stats().LastChangePct).
			Msg("frame unchanged; reusing previous classification")
	}
	return state, score
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
//  1. screen-size probe     — device alive? just restart the game
//  2. transport Reconnect   — stale socket after a BlueStacks blip
//  3. ResetAdbServer        — stale adb-server registration
//     (note: drops ALL adb connections on this host — logged)
//  4. EnsureBlueStacksMac   — emulator really gone; relaunch at the
//     configured resolution, then poll up to 2 min for adb
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

	state, score := b.classifyFrame(screen)

	gc.UpdateScreen(screen, captureMs)

	if !b.zoomedOut.Load() {

		pinX, pinY := b.cal.Hud(60, 695)
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

				// Evidence-gated dismissal (game.DismissOverlay): the splash must
				// still be on the frame this captures, and the tap lands on the
				// classifier's own probe for it rather than on a predicted point.
				if !game.DismissOverlay(b.client, b.cal, b.classifier, st, b.templates, b.logger) {
					b.logger.Warn().Str("state", st.String()).
						Msg("boot splash not dismissed from verified evidence; will retry on next detection")
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

	// Connection-lost dialog. CoC shows this whenever the game's own
	// server link drops (emulator network blip, server restart); the
	// classifier used to misread it as StateBattleEnd — the dialog's
	// RETURN HOME button satisfied that rule's template-only match —
	// and the bot tapped result-screen coordinates forever (observed
	// live: 18:26 boot → 18:28 ReturnHome fallback → 18:33 emergency
	// restart loop). Tapping TRY AGAIN makes the game reconnect in place; the
	// point comes from the dialog's own classifier probes (game.DismissOverlay),
	// because the panel is drawn at a fixed pixel size and the reference probe
	// maps 34 px below the label at 720p. Like the splash dismissal below,
	// activity is deliberately NOT recorded: the adb tap reports success even
	// when the dialog survives, and the stuck-watchdog must still fire if
	// reconnect keeps failing.
	if state == game.StateConnectionLost {
		if b.connLostDismissInFlight.CompareAndSwap(false, true) {
			b.logger.Warn().Msg("connection lost dialog detected; tapping TRY AGAIN...")
			go func() {
				defer b.connLostDismissInFlight.Store(false)
				time.Sleep(800 * time.Millisecond)
				if !game.DismissOverlay(b.client, b.cal, b.classifier, game.StateConnectionLost, b.templates, b.logger) {
					b.logger.Warn().Msg("connection-lost dialog not dismissed from verified evidence; will retry on next detection")
					return
				}
				b.logger.Info().Msg("connection-lost TRY AGAIN tapped; waiting for reconnect")
			}()
		}
		return
	}

	// Quit-confirm dialog ("Do you want to quit the game?", Cancel /
	// Okay). Defense in depth for the ArmyCamp misclassification guard
	// above: if a Back press still lands on the real main village, this
	// dialog appears and must be dismissed with CANCEL — otherwise the
	// bot sits on it for the boot-splash grace (5 min) then force-
	// restarts. Same no-recordActivity reasoning as the splash handler.
	if state == game.StateConfirmExit {
		if b.connLostDismissInFlight.CompareAndSwap(false, true) {
			b.logger.Warn().Msg("quit-confirm dialog detected; tapping Cancel...")
			go func() {
				defer b.connLostDismissInFlight.Store(false)
				time.Sleep(800 * time.Millisecond)
				// The Cancel FACE is one of the rule's own probes; the tap lands on
				// it only while it still reads as the orange Cancel button, never on
				// the green Okay face beside it (which quits the game).
				if !game.DismissOverlay(b.client, b.cal, b.classifier, game.StateConfirmExit, b.templates, b.logger) {
					b.logger.Warn().Msg("quit-confirm dialog not dismissed from verified evidence; will retry on next detection")
					return
				}
				b.logger.Info().Msg("quit-confirm Cancel tapped")
			}()
		}
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
		// Guard against a misclassification, not a real camp: a dim or
		// zoomed village frame can pass the ArmyCamp rule's single loose
		// brown pixel check. Pressing Back on the actual main village opens
		// CoC's "Do you want to quit the game?" confirm dialog, which no
		// rule detects — the bot then sat on that dialog for the full
		// boot-splash grace (5 min) and force-restarted, every cycle
		// (observed live 11:32–11:43). A frame that still shows the real
		// Attack! button IS the main village; skip the Back press.
		if b.findAttackButton(screen, 0.45) {
			// Throttle the log: the guard can fire every frame while the
			// misclassification persists, which would spam 10 lines/sec.
			if time.Since(b.lastArmyCampGuardLog) > 10*time.Second {
				b.lastArmyCampGuardLog = time.Now()
				b.logger.Info().Msg("ArmyCamp state but attack button visible; treating as main village (misclassification guard)")
			}
			b.recordActivity()
			return
		}
		b.lastNav = time.Now()
		b.logger.Info().Msg("in ArmyCamp, returning to main village...")
		go b.navigator.NavigateToMainVillage(gc)
		return
	}
}

func (b *Bot) findAttackButton(screen gocv.Mat, threshold float32) bool {
	pinX, pinY := b.cal.Hud(60, 695)
	if b.isOrange(screen, pinX, pinY) {
		b.logger.Debug().Msg("attack button confirmed via pinpoint color check")
		return true
	}

	tpl, ok := b.templates.Get("btn_attack")
	if !ok {
		return false
	}

	roi := image.Rect(0, 500, 300, 732)
	// The Attack! button is bottom-left HUD chrome, so the search window keeps
	// its distance to the left and bottom edges.
	physROI := b.cal.HudRect(roi)

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

	return isOrange
}

func (b *Bot) isOrange(screen gocv.Mat, x, y int) bool {

	return b.colorCheck(screen, x, y,
		gocv.NewScalar(0, 100, 150, 0),
		gocv.NewScalar(150, 255, 255, 0),
		20)
}

// buttonROI returns the normalized (reference-resolution) region of interest
// for a known UI button template. The table lives in the game package so the
// classifier's template search and this click path share one definition and
// cannot drift apart; a template with no authored window searches the whole
// reference frame, which is the historical behaviour.
func (b *Bot) buttonROI(templateName string) image.Rectangle {
	if r, ok := game.ButtonROIBounds(templateName); ok {
		return r
	}
	return game.ReferenceFrame()
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

	// Inter-attack cooldown. Armies need real time to retrain; without a
	// gate the bot re-attacked ~8s after every Return Home with whatever
	// the camps held (observed live: three near-identical defeats in <4
	// minutes). min_seconds_between_attacks is the human-real pause;
	// waiting inside the sequence goroutine (seqRunning is already held)
	// keeps the capture loop from starting a second sequence meanwhile.
	gap := time.Duration(b.cfg.Attack.MinSecondsBetweenAttacks) * time.Second
	if gap > 0 && !b.lastAttackEnd.IsZero() {
		if wait := gap - time.Since(b.lastAttackEnd); wait > 0 {
			b.logger.Info().
				Dur("wait", wait).
				Int("min_gap_s", b.cfg.Attack.MinSecondsBetweenAttacks).
				Msg("waiting out inter-attack cooldown before next search")
			select {
			case <-time.After(wait):
			case <-b.ctx.Done():
				return
			}
		}
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

			// The orange Next button is LOCATED (PixelSearch), never predicted. The
			// hardcoded reference coordinate this used to fall back to was a tap on
			// whatever happened to be there; a base that cannot be skipped this way
			// is left to the search loop's own 5-minute bound, which restarts the
			// game rather than clicking blind.
			searchROI := image.Rect(b.cal.PhysicalW/2, b.cal.PhysicalH/2, b.cal.PhysicalW, b.cal.PhysicalH)
			orangePt, err := vision.PixelSearch(screen, searchROI, 252, 186, 54, 50)
			if err == nil {
				b.logger.Info().Msg("clicking Next via orange color fallback")
				b.client.TapRandomized(orangePt.X, orangePt.Y)
				b.recordActivity()
			} else {
				b.logger.Warn().Msg("Next button not located and no orange fallback found; not clicking blind")
				b.DumpDiagnostics("next_button_not_found", screen, map[string]interface{}{
					"message": "no located Next control on this frame; no blind tap fired",
				})
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
		//
		// Which reads qualify is decided by resultSettle
		// (internal/bot/result_settle.go): the panel must STOP CHANGING,
		// not merely show a number. The old "any field non-zero" shortcut
		// accepted the star emblem's paint frame — seconds before the loot
		// and bonus counters ran — and locked in a 2-star victory as 0
		// loot / 0 bonus.
		b.client.JitteredSleep(1800 * time.Millisecond)

		// Authoritative star signal: the destruction percentage the battle
		// wait sampled from the stall ROI (proven live: valk runs tracked
		// 23% -> 50% tick by tick). CoC scores stars from the outcome,
		// not from the result-screen art: >=50% = 1 star, TH destroyed =
		// +1, 100% = 3 (see game.StarsFromOutcome). When the wait had no
		// percent ROI to sample (no stall_config, no end_at_percent) this
		// stays 0 and the visual parse below is the fallback.
		finalPct := b.attackExec.LastDestructionPercent()

		var parsedResult game.BattleResult
		parsedOK := false
		for settle, attempt := newResultSettle(b.logger), 0; attempt < resultSettleAttempts && !parsedOK; attempt++ {
			resultScreen, err := b.client.CaptureToMat()
			if err != nil {
				b.logger.Warn().Err(err).Msg("battle result capture failed; retrying")
				time.Sleep(resultSettleCapturePause)
				continue
			}

			lootRec := game.NewLootRecognizer(b.cal, b.templates, b.logger)
			res, rerr := lootRec.ReadBattleResult(resultScreen)
			hash := resultPanelHash(resultScreen, b.cal)

			var accepted game.BattleResult
			settled := false
			if rerr == nil {
				accepted, settled = settle.observe(hash, res, time.Now())
			}

			// Save the artifact only for a frame worth keeping: the read that was
			// accepted, the frame that explains a parse failure, or the last
			// attempt (so a panel that never settled still leaves evidence).
			// Writing every attempt meant a full-frame PNG encode plus a disk
			// write on each of up to resultSettleAttempts passes, seconds apart,
			// for a file each one overwrote.
			if keepResultFrame(attempt, resultSettleAttempts-1, settled, rerr != nil) {
				b.saveResultFrame(resultScreen)
			}

			resultScreen.Close()
			lootRec.Close()

			if rerr != nil {
				b.logger.Warn().Err(rerr).Msg("battle result parse error; retrying")
				time.Sleep(resultSettleParsePause)
				continue
			}

			if settled {
				parsedResult = accepted
				parsedOK = true
				break
			}

			b.logger.Warn().
				Int("attempt", attempt).
				Int("stars", res.Stars).
				Int("gold", res.Loot.Gold).
				Int("bonus_gold", res.Bonus.Gold).
				Msg("battle result panel still animating; waiting for the loot and bonus counters to stop, retrying...")
			time.Sleep(resultSettleAnimatingPause)
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

			// The result panel's star art is the ground truth: the two-pass
			// defeat-safe read correctly returned 0 stars on the live 32%
			// defeat (the OCR'd "Defeat" screen). The destruction-rule read
			// (stall ROI) is only a fallback when the panel parse fails
			// entirely — see the !parsedOK branch below. This block used to
			// OVERRIDE the visual stars with the rule whenever any percent
			// was measured, and a garbage stall-ROI read (381%, later
			// clamped + per-battle-reset in WaitForBattleEndCtx) turned live
			// defeats into fabricated 3-star victories. Visual wins now;
			// the rule comparison is logged for observability only.
			if finalPct > 0 {
				ruleStars := game.StarsFromOutcome(finalPct, b.attackExec.ThDestroyed())
				if ruleStars != battleStars {
					b.logger.Info().
						Int("visual_stars", battleStars).
						Int("rule_stars", ruleStars).
						Int("destruction_pct", finalPct).
						Bool("th_destroyed", b.attackExec.ThDestroyed()).
						Msg("battle stars: keeping visual parse over destruction-rule read")
				}
			}

			b.totalGold.Add(int64(parsedResult.Loot.Gold + parsedResult.Bonus.Gold))
			b.totalElixir.Add(int64(parsedResult.Loot.Elixir + parsedResult.Bonus.Elixir))
			b.totalDE.Add(int64(parsedResult.Loot.DarkElixir + parsedResult.Bonus.DarkElixir))
			b.totalStars.Add(int32(battleStars))

			switch battleStars {
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
				Int("stars", battleStars).
				Int("gold", parsedResult.Loot.Gold).
				Int("bonus_gold", parsedResult.Bonus.Gold).
				Int("destruction_pct", finalPct).
				Msg("battle result processed")
		} else {
			// OCR failed after retries — still record the rules-derived
			// star count when the wait measured destruction, so a battle
			// is never reported as a 0-star defeat merely because the
			// result panel misparsed.
			if finalPct > 0 {
				battleStars = game.StarsFromOutcome(finalPct, b.attackExec.ThDestroyed())
				b.logger.Error().
					Int("stars", battleStars).
					Int("destruction_pct", finalPct).
					Msg("battle result OCR failed after retries; stars recorded from destruction rules")
			} else {
				b.logger.Error().Msg("battle result OCR failed after retries; recording unparsed attack")
			}
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

	// Stamp the attack boundary so the inter-attack cooldown has a clean
	// reference point (set only on a real return home, not on a restart).
	b.lastAttackEnd = time.Now()

	// Leftover post-attack popups are dismissed only when one is actually on the
	// screen. This used to be an unconditional tap on empty village space, which
	// is a tap with no evidence behind it: the bot clicked the village after every
	// battle whether or not anything was up. The classification says whether there
	// is anything to dismiss, and dismissInterruptions only fires on an overlay it
	// can verify (game.DismissOverlay).
	if screen, err := b.client.CaptureToMat(); err == nil {
		state, _ := b.classify(screen)
		screen.Close()
		if state != game.StateMainVillage && state != game.StateUnknown {
			b.logger.Info().Str("state", state.String()).Msg("post-attack overlay detected; dismissing it")
			b.dismissInterruptions()
			time.Sleep(1000 * time.Millisecond)
		} else {
			b.logger.Debug().Msg("returned home with nothing to dismiss; no tap fired")
		}
	}

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

	// Find a Match. Off the reference geometry the attack menu reflows, so the
	// button is located in the live frame rather than predicted — and that also
	// removes the reason the old blind tap failed live at 1280x720: it fired one
	// second after the Attack tap, while the menu was still animating in, landed
	// on the village behind it, and closed the menu again. Waiting for the
	// button to *exist* is what makes the step safe.
	if !b.cal.IsReferenceGeometry() {
		// The Attack tap is *verified*, not assumed: tapping a predicted point and
		// trusting it is what killed the 1280x720 runs. See clearSwallowedTap for
		// the measured reason a tap aimed at the village can land somewhere else.
		_, menuUp := b.pressPanelAction("Find Match", attackMenuActionRegion, 8*time.Second)
		if !menuUp {
			b.logger.Warn().Msg("attack menu did not open after tapping Attack; clearing whatever swallowed " +
				"the tap and retrying once")
			b.clearSwallowedTap()
			b.findAndClick("btn_attack", "Attack (retry)", 1)
			b.client.JitteredSleep(700 * time.Millisecond)
			_, menuUp = b.pressPanelAction("Find Match", attackMenuActionRegion, 8*time.Second)
		}
		if !menuUp {
			b.logger.Warn().Msg("could not locate the attack menu's Find a Match button in the live frame")
			if screen, err := b.client.CaptureToMat(); err == nil {
				b.DumpDiagnostics("click_find_match_failed", screen, nil)
				screen.Close()
			}
			return false
		}
		// The army sheet is already open on MY ARMY (the active army), so when no
		// saved recipe has to be picked, its own action button starts the search.
		if b.armySlot <= 1 {
			if _, ok := b.pressPanelAction("Army Sheet Attack", sheetActionRegion, 8*time.Second); ok {
				b.logger.Info().Msg("waiting for battle state (searching)...")
				return b.waitForBattleState(60 * time.Second)
			}
			b.logger.Warn().
				Int("screen_w", b.cal.PhysicalW).Int("screen_h", b.cal.PhysicalH).
				Msg("could not locate the army sheet's action button in the live frame; not falling back " +
					"to the authored pinpoints, which describe the reference layout (see docs/RESOLUTION.md)")
			if screen, err := b.client.CaptureToMat(); err == nil {
				b.DumpDiagnostics("sheet_action_not_found", screen, nil)
				screen.Close()
			}
			return false
		}
		b.logger.Warn().Int("army_slot", b.armySlot).
			Msg("selecting a saved recipe needs the sheet's recipe list, which is not mapped at this " +
				"geometry; falling back to the authored pinpoints")
	}

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

	armyClicked := false
	for attempt := 0; attempt < 3; attempt++ {
		if b.selectArmySlot() {
			armyClicked = true
			break
		}
		b.client.JitteredSleep(500 * time.Millisecond)
	}
	if !armyClicked {
		b.logger.Warn().Int("army_slot", b.armySlot).Msg("army recipe card did not appear, continuing anyway")
		if screen, err := b.client.CaptureToMat(); err == nil {
			b.DumpDiagnostics("click_army_slot_not_found", screen, map[string]interface{}{"army_slot": b.armySlot})
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

// pressPanelAction waits for a panel's primary action button to appear inside a
// frame-fraction window and presses it, returning the button it pressed.
//
// This is the verified-step primitive the pre-battle flow uses away from the
// reference geometry. Two measured facts force it. First, panels reflow: the
// army sheet's Attack! face is at (728,536) on the 860x732 reference and
// (1131,641) at 1280x720 — 180 px apart in y, with no per-axis anchor that
// reproduces both — while the authored pinpoint maps to (1039,462). Second, a
// panel animates in: tapping a predicted point one second after opening the
// attack menu landed on the village behind it and closed the menu again, which
// is what sank the 720p runs. Waiting for the button to exist and tapping where
// it actually is fixes both.
//
// The window, not the button's width, is what selects the right control: the
// attack menu puts Find a Match (290 px, green, x 17% of the frame) and Join
// Tournament (286 px, gold, x 44%) side by side at the same height, so
// widest-wins cannot separate them — see attackMenuActionRegion.
func (b *Bot) pressPanelAction(step string, region [4]float64, timeout time.Duration) (vision.ActionButton, bool) {
	deadline := time.Now().Add(timeout)
	for {
		screen, err := b.client.CaptureToMat()
		if err == nil && !screen.Empty() {
			win := vision.FractionRect(screen.Cols(), screen.Rows(), region[0], region[1], region[2], region[3])
			btn, ok := vision.FindActionButton(screen, win, vision.DefaultActionButtonConfig())
			screen.Close()
			if ok {
				b.logger.Info().
					Str("step", step).
					Str("button", btn.Describe()).
					Msg("pressing the action button located in the live frame")
				if err := b.client.TapRandomized(btn.Centre().X, btn.Centre().Y); err == nil {
					time.Sleep(1200 * time.Millisecond)
					return btn, true
				}
			}
		} else if err == nil {
			screen.Close()
		}
		if time.Now().After(deadline) {
			return vision.ActionButton{}, false
		}
		b.client.JitteredSleep(300 * time.Millisecond)
	}
}

// clearSwallowedTap removes whatever is covering the village after a tap that
// did not do what it was aimed at.
//
// Measured live at 1280x720: the mandatory zoom-out pinch puts its two fingers a
// quarter and three quarters of the way across the frame, and one of those
// points lands on the Builder's Hut. CoC opens the hut's "Work for Hire!" dialog
// roughly two seconds later, dimming the whole screen — the Attack! button face
// drops from RGB(208,114,66) to RGB(104,57,33), an exact halving. A tap aimed at
// the Attack button then goes into that dialog's scrim, the attack menu never
// opens, and every later tap in the chain is swallowed with it, which is how the
// 720p runs died before reaching a battle. Back closes it (verified live at both
// dialogs the pinch can open); on an already-clear village Back instead opens
// CoC's quit-confirm dialog, so dismissInterruptions runs afterwards to cancel
// it. Safe either way — which is what lets this step exist without a scrim
// detector. A brightness test could not replace it anyway: the attack menu dims
// the village to almost the same mean as the popup (84.8 vs 82.5).
func (b *Bot) clearSwallowedTap() {
	if err := b.client.Back(); err != nil {
		b.logger.Warn().Err(err).Msg("back to clear a dialog covering the village failed")
	}
	time.Sleep(900 * time.Millisecond)
	b.dismissInterruptions()
	time.Sleep(400 * time.Millisecond)
}

// attackMenuActionRegion is the window holding the attack menu's Find a Match
// button, as frame fractions: the left column, below the mode tabs. It is a
// fraction of the frame rather than a reference coordinate because the menu
// reflows, and it is narrow deliberately — the Join Tournament button beside it
// is only 4 px narrower at 1280x720, so width cannot choose between them. The
// menu's other controls (Battle, Ranked Battle) sit in the same column but at
// y <= 0.5, above the window.
var attackMenuActionRegion = [4]float64{0.0, 0.55, 0.38, 0.90}

// sheetActionRegion is the window the bot searches for the army sheet's action
// button, as frame fractions: the lower-right half, where that button sits at
// both measured geometries — (728,536) on the 860x732 reference, (1131,641) at
// 1280x720 — even though the panel around it reflows. Verified on live frames:
// it finds exactly that button on the sheet and reports no button at all on
// village, army-camp, search and battle frames, in either geometry.
var sheetActionRegion = [4]float64{0.5, 0.5, 1.0, 1.0}

// selectArmySlot clicks the saved-recipe card for b.armySlot in the
// army-selection list opened by the Army Arrow. Each recipe card is a
// ~146px-tall row in a vertically scrolling list; the first card sits at
// ref-y 227 and cards stack every ~54px once the list is expanded
// (measured on the live 860x732 BlueStacks layout). Slot 1 keeps the
// legacy template path (btn_army_1) for backward compatibility with
// older game layouts; slots 2+ use the measured card geometry.
func (b *Bot) selectArmySlot() bool {
	slot := b.armySlot
	if slot <= 0 {
		slot = 1
	}

	if slot == 1 {
		return b.findAndClick("btn_army_1", "Army 1", 1)
	}

	// Card rows in the saved-recipes list (reference resolution).
	// cardY is the vertical center of the Nth card body; tapping the
	// card body sets it as the active army (verified live: slot 4 at
	// y≈403 fired the "Recipe ... set as active army!" toast).
	cardY := 227 + (slot-1)*54
	tapX, tapY := b.cal.Hud(430, cardY)

	b.logger.Info().Int("army_slot", slot).Int("x", tapX).Int("y", tapY).Msg("selecting saved army recipe card")
	if err := b.client.TapRandomized(tapX, tapY); err != nil {
		b.logger.Warn().Err(err).Msg("army recipe card tap failed")
		return false
	}
	time.Sleep(1000 * time.Millisecond)
	b.recordActivity()
	return true
}

// Pinpoint defines a precise location on the reference screen (860x732)
// and a color check to verify it before clicking.
type Pinpoint struct {
	X, Y int
	Name string
	// XA and YA declare which screen edge — or the axis centre — the point
	// keeps its distance to, one anchor per axis. Measured, not inferred: the
	// game anchors each widget per axis, so a single Anchor for both axes (or
	// the classifier's band heuristic, which reads the widget's reference y)
	// cannot express the layout. The attack menu is the case that broke 720p
	// live: its buttons keep their left edge offset (ref x 158 -> 209) but are
	// centred vertically (ref y 494 -> 530, where the bottom-edge model put
	// them at 405 — 125 px above the button, so Find a Match was never
	// clicked and the search never started). See docs/RESOLUTION.md.
	//
	// Verified live at 1280x720 with cmd/tplprobe and Apple Vision OCR on the
	// same screen at both geometries.
	XA, YA game.AxisAnchor
}

// villagePinpoints maps a button name to its reference-frame tap point and the
// anchoring of each axis. Anchors marked "measured" were read off the live
// 1280x720 layout; the rest keep the mapping that was verified at the reference
// geometry (the identity) and are the mapping candidates to check first if a
// non-reference run mis-taps.
var villagePinpoints = map[string]Pinpoint{
	// measured: village bottom-left button, keeps its left and bottom offsets
	"btn_attack": {X: 64, Y: 666, Name: "Attack", XA: game.AnchorLow, YA: game.AnchorHigh},
	// measured: attack-menu button, left-anchored, vertically centred
	"btn_find_match": {X: 158, Y: 494, Name: "Find Match", XA: game.AnchorLow, YA: game.AnchorMid},
	// not yet measured at a non-reference geometry: the army bar's buttons are
	// centre/centre (see the army-bar measurement in docs/RESOLUTION.md) and
	// the two bottom-right buttons keep their bottom/right offsets.
	"btn_battle":      {X: 731, Y: 537, Name: "Battle", XA: game.AnchorMid, YA: game.AnchorHigh},
	"btn_army_arrow":  {X: 514, Y: 192, Name: "Army Arrow", XA: game.AnchorMid, YA: game.AnchorMid},
	"btn_army_1":      {X: 513, Y: 230, Name: "Army 1", XA: game.AnchorMid, YA: game.AnchorMid},
	"btn_next":        {X: 794, Y: 577, Name: "Next Match", XA: game.AnchorHigh, YA: game.AnchorHigh},
	"btn_return_home": {X: 431, Y: 581, Name: "Return Home", XA: game.AnchorMid, YA: game.AnchorMid},
	"btn_okay":        {X: 430, Y: 520, Name: "Okay", XA: game.AnchorMid, YA: game.AnchorMid},
}

// pinpointPoint maps a pinpoint's reference point onto the live frame using the
// anchoring the pinpoint declares for each axis.
func (b *Bot) pinpointPoint(pp Pinpoint) (int, int) {
	return b.cal.Point2(pp.X, pp.Y, pp.XA, pp.YA)
}

func (b *Bot) findAndClick(templateName, stepName string, maxRetries int) bool {

	if pp, ok := villagePinpoints[templateName]; ok {
		px, py := b.pinpointPoint(pp)
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

	// A pinpoint's or template's reference ROI is HUD chrome (every entry in
	// buttonROI is a screen-anchored button), so the search window follows the
	// same anchors the tap does.
	physROI := b.cal.HudRect(roi)

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
			altX, altY := b.cal.Hud(525, 247)
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

	// NO blind-tap fallback. This is where the "clicking" happened even after
	// every located path had failed: the pinpoint's coordinate was tapped and the
	// step was logged as a success, because a predicted coordinate had been tapped
	// rather than a widget. That is the race docs/RESOLUTION.md measured on the
	// 1280x720 runs — the tap landed on whatever was behind the panel that failed
	// to match, closed it, and every later tap in the chain went to the village.
	// Failing here instead lets each caller decide: they retry, dump diagnostics,
	// skip the base, or restart the game, and all four are better than a tap
	// nobody could aim.
	b.logger.Error().Str("step", stepName).Int("retries", maxRetries).
		Msg("no control located on this frame; not tapping a predicted coordinate")
	return false
}

// resultPanelHash returns a cheap content hash of the end-of-battle
// result panel region (star row + battle-loot and bonus columns). Two
// captures with an equal nonzero hash mean the panel has finished
// rendering — used to distinguish a still-counting-up overlay from a
// genuine 0-star/0-loot result.
func resultPanelHash(screen gocv.Mat, cal *game.Calibration) uint64 {
	// Reference (860x732) region covering the stars and both loot
	// columns; generous bounds tolerate small theme shifts. The result
	// panel is a centred overlay, so its region is mapped around the
	// viewport centre.
	live := cal.CentreRect(image.Rect(300, 180, 690, 470))
	x0, y0, x1, y1 := live.Min.X, live.Min.Y, live.Max.X, live.Max.Y
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

	mask := vision.GetMat(sub.Rows(), sub.Cols(), gocv.MatTypeCV8UC1)
	defer vision.PutMat(mask)
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
// dismissInterruptions clears a transient overlay the capture loop just saw.
//
// Every action goes through game.DismissOverlay, which captures its own frame and
// refuses to fire unless (a) the state is still on that frame and (b) the control
// it moves — a probe the classifier verified, a located button, or Back — is where
// the action expects it. The per-state coordinate table this used to hold (blind
// taps at (400,300) for the obstacle sheet, predicted X / Welcome-Back / splash /
// TRY AGAIN coordinates) is gone with it; see internal/game/dismiss.go for why a
// predicted tap is worse than no tap.
//
// Like the old direct taps, these deliberately DO NOT call recordActivity() — if
// an overlay is truly stuck and refusing to dismiss, the global stuck watchdog
// must fire and cycle the game rather than have the hang masked as forward
// progress. If the dismissal is actually working, the resulting state transition
// (caught by the captureLoop's next frame) resets lastAction via recordActivity()
// on its own.
func (b *Bot) dismissInterruptions() {
	screen, err := b.client.CaptureToMat()
	if err != nil {
		return
	}
	state, _ := b.classify(screen)
	screen.Close()

	if !game.DismissOverlay(b.client, b.cal, b.classifier, state, b.templates, b.logger) {
		b.logger.Debug().Str("state", state.String()).
			Msg("no overlay to dismiss from verified evidence; no input fired")
	}
}

// wallMenuActionWindow is the frame-fraction window a wall-selection menu's own
// action button sits in: the body of the centred modal. It exists so
// dismissSelection can tell "a wall menu is open" from "the bot is looking at an
// ordinary village", which is the whole difference between closing a menu and
// clicking empty ground.
var wallMenuActionWindow = [4]float64{0.10, 0.20, 0.90, 0.80}

// dismissSelection closes an active wall-selection menu by tapping the empty
// village space beside it. Used as the production Dismiss hook for the
// wall-upgrade loop in RunWallUpgradeLoop.
//
// The tap is gated on evidence that a menu is actually open: the menu's own
// Upgrade button, located in the live frame. Without it this ran after every
// failed template match and tapped empty ground on a plain village — a click with
// nothing behind it. The tap point itself is the one thing here that is not a
// located widget, because the action IS "touch nothing": the documented way to
// close a CoC selection menu is to tap the ground beside it, and tapping a
// located widget there (Back included) would open the quit-confirm dialog.
//
// The 500ms settle waits for the menu close-animation; without it the next capture
// can race the menu's fade-out and confuse the next template match.
func (b *Bot) dismissSelection() {
	screen, err := b.client.CaptureToMat()
	if err != nil {
		return
	}
	win := vision.FractionRect(screen.Cols(), screen.Rows(),
		wallMenuActionWindow[0], wallMenuActionWindow[1],
		wallMenuActionWindow[2], wallMenuActionWindow[3])
	_, menuOpen := vision.FindActionButton(screen, win, vision.DefaultActionButtonConfig())
	screen.Close()

	if !menuOpen {
		b.logger.Debug().Msg("no selection menu located; not tapping empty ground")
		return
	}
	tx, ty := b.cal.Centre(50, 450)
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

	if b.navigator != nil {
		b.navigator.SetDisableChestDismissal(cfg.Device.DisableChestDismissal)
	}
	b.logger.Info().Msg("bot configuration updated in real-time")
}

func (b *Bot) Stats() BotStats {
	s := BotStats{
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
	// The gate's numbers are how a user confirms the optimisation is working on
	// their machine: classify_reused climbing while last_frame_change_pct sits
	// under the threshold means it is; classify_reused stuck at 0 means the
	// threshold is below this device's animation floor and can be raised.
	if g := b.classifyGate; g != nil {
		gs := g.stats()
		s.ClassifyRan = gs.Classifies
		s.ClassifyReused = gs.Reused
		s.LastFrameChangePct = gs.LastChangePct
		s.ClassifyGateEnabled = gs.Enabled
		s.ClassifyThresholdPct = gs.Threshold
	}
	s.CaptureCache = b.client.CaptureCache()
	return s
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

	// Classify-gate observability (see classifygate.go): how many frames were
	// classified, how many reused the previous verdict, and how much the last
	// frame changed. These are how a user verifies the setting on their device.
	ClassifyRan          int64   `json:"classify_ran"`
	ClassifyReused       int64   `json:"classify_reused"`
	LastFrameChangePct   float64 `json:"last_frame_change_pct"`
	ClassifyGateEnabled  bool    `json:"classify_gate_enabled"`
	ClassifyThresholdPct float64 `json:"classify_threshold_pct"`

	// Capture-cache observability (see internal/adb/capturecache.go): captures
	// the emulator was not asked to take because another consumer had just read
	// the same screen, and how many requests the input barrier refused (a frame
	// older than the bot's last tap must never answer a post-tap check). Hits
	// climbing means the emulator is doing less; hits at zero with the cache
	// enabled means nothing captures concurrently on this workload.
	CaptureCache adb.CaptureCacheStats `json:"capture_cache"`
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
