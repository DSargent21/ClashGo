package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/Ducky705/ClashGO/internal/adb"
	"github.com/Ducky705/ClashGO/internal/bot"
	"github.com/Ducky705/ClashGO/internal/config"
	"github.com/Ducky705/ClashGO/internal/logger"
	"github.com/Ducky705/ClashGO/internal/paths"
	"github.com/Ducky705/ClashGO/internal/updater"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"github.com/rs/zerolog/log"
	"github.com/wailsapp/wails/v2/pkg/runtime"
	"gocv.io/x/gocv"
)

// App struct
type App struct {
	ctx       context.Context
	bot       *bot.Bot
	botCtx    context.Context
	cancel    context.CancelFunc
	mu        sync.Mutex
	echo      *echo.Echo
	lastStats bot.BotStats
	logBuffer []string

	// Updater wiring
	updater       *updater.Service
	updaterBgCtx  context.Context
	updaterBgStop context.CancelFunc
}

type WailsLogWriter struct {
	app *App
}

func (w *WailsLogWriter) Write(p []byte) (n int, err error) {
	msg := string(p)
	if w.app.ctx != nil {
		runtime.EventsEmit(w.app.ctx, "bot_log", msg)
	}

	w.app.mu.Lock()
	w.app.logBuffer = append(w.app.logBuffer, msg)
	if len(w.app.logBuffer) > 100 {
		w.app.logBuffer = w.app.logBuffer[len(w.app.logBuffer)-100:]
	}
	w.app.mu.Unlock()

	return len(p), nil
}

// NewApp creates a new App application struct
func NewApp() *App {
	return &App{
		logBuffer: make([]string, 0, 100),
		updater:   updater.New(updater.DefaultConfig(version)),
	}
}

// startup is called when the app starts. The context is saved
// so we can call the runtime methods
func (a *App) startup(ctx context.Context) {
	a.ctx = ctx

	// Ensure fresh stats and history every boot
	_ = os.Remove(paths.ResolveConfig("stats.json"))
	_ = os.Remove(paths.ResolveConfig("attack_history.json"))

	// Setup log bridge
	wailsWriter := &WailsLogWriter{app: a}
	logger.Init(os.Getenv("DEBUG") != "", wailsWriter)

	// Bring up the updater service. If NewApp wasn't used (rare
	// test scaffold), construct lazily.
	if a.updater == nil {
		a.updater = updater.New(updater.DefaultConfig(version))
	}
	a.updater.CleanupOrphanDownloads()
	bgCtx, bgCancel := context.WithCancel(context.Background())
	a.updaterBgCtx = bgCtx
	a.updaterBgStop = bgCancel
	a.updater.StartBackgroundPoller(bgCtx)
	go a.forwardUpdaterStatus(bgCtx)

	// Skip the standalone web dashboard on `wails dev`. Wails injects
	// its own dev proxy at :34115 → Vite at :5173 by parsing stdout
	// for `http://host:port` patterns. If we start Echo (which prints
	// its own listen URL), `wails dev` mis-reads it as Vite re-pointing
	// to :8080 and re-aims the WkWebView there — where Echo serves the
	// (stale or empty) `web/dist` instead of Vite's HMR graph. The user
	// sees a half-mounted layout on the transparent webview, presenting
	// as the dark-zinc frame color through the transparent layers, i.e.
	// a "black screen after 1 second".
	//
	// IMPORTANT: detect dev mode via Wails' canonical runtime API rather
	// than an env-var check — the V2 CLI does NOT set WAILS_DEV (or any
	// equivalent) on the spawned GUI process, so `os.Getenv("WAILS_DEV")`
	// was always empty and would have started Echo in dev too.
	if runtime.Environment(ctx).BuildType != "dev" {
		// Start Web Server for Remote Access (production-only).
		go a.startWebServer()
	}
}

func (a *App) shutdown(ctx context.Context) {
	if a.updaterBgStop != nil {
		a.updaterBgStop()
	}
	a.saveStats()
}

// forwardUpdaterStatus pushes the updater's status to the React side
// on every meaningful change. We use a 2s ticker with equality check
// to avoid spamming the UI with identical payloads; React renders
// only when the status struct actually changes.
func (a *App) forwardUpdaterStatus(ctx context.Context) {
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	var lastJSON string
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if a.ctx == nil || a.updater == nil {
				continue
			}
			st := a.updater.GetStatus()
			b, _ := json.Marshal(st)
			s := string(b)
			if s == lastJSON {
				continue
			}
			lastJSON = s
			runtime.EventsEmit(a.ctx, "updater_status", st)
		}
	}
}

func (a *App) saveStats() {
	a.mu.Lock()
	stats := a.lastStats
	if a.bot != nil {
		current := a.bot.Stats()
		stats = bot.BotStats{
			AttacksCompleted: a.lastStats.AttacksCompleted + current.AttacksCompleted,
			SearchSkips:      a.lastStats.SearchSkips + current.SearchSkips,
			TotalGold:        a.lastStats.TotalGold + current.TotalGold,
			TotalElixir:      a.lastStats.TotalElixir + current.TotalElixir,
			TotalDE:          a.lastStats.TotalDE + current.TotalDE,
			Stars0:           a.lastStats.Stars0 + current.Stars0,
			Stars1:           a.lastStats.Stars1 + current.Stars1,
			Stars2:           a.lastStats.Stars2 + current.Stars2,
			Stars3:           a.lastStats.Stars3 + current.Stars3,
			Uptime:           a.lastStats.Uptime + current.Uptime,
		}
	}
	a.mu.Unlock()

	bytes, err := json.MarshalIndent(stats, "", "  ")
	if err != nil {
		log.Error().Err(err).Msg("failed to marshal stats")
		return
	}

	if err := bot.AsyncWriteFile(paths.ResolveConfig("stats.json"), bytes, 0644); err != nil {
		log.Error().Err(err).Msg("failed to write stats.json")
	}
}

func (a *App) ResetStats() {
	a.mu.Lock()
	a.lastStats = bot.BotStats{}
	if a.bot != nil {
		// We can't easily reset atomic counters in a running bot without adding a Reset method there too.
		// For now, stopping the bot might be required for a full reset, or we just clear the persistent part.
	}
	a.mu.Unlock()
	_ = os.Remove(paths.ResolveConfig("stats.json"))
	_ = os.Remove(paths.ResolveConfig("attack_history.json"))
}

func (a *App) startWebServer() {
	e := echo.New()
	e.HideBanner = true
	// Defense in depth: even if a future startup path accidentally enables
	// startWebServer under wails dev (e.g. removing the WAILS_DEV gate),
	// suppressing the listen-port banner removes the `http://host:port`
	// pattern that `wails dev` parses for proxy-target discovery.
	e.HidePort = true
	e.Use(middleware.CORS())

	// Basic API for remote control
	e.GET("/status", func(c echo.Context) error {
		a.mu.Lock()
		defer a.mu.Unlock()
		running := a.bot != nil
		return c.JSON(200, map[string]interface{}{"running": running})
	})

	e.GET("/stats", func(c echo.Context) error {
		return c.JSON(200, a.GetStats())
	})

	e.GET("/history", func(c echo.Context) error {
		return c.JSON(200, a.GetAttackHistory())
	})

	// Static assets from embed would be ideal, but for now just API
	// Or we can serve the built dist folder if it exists
	e.Static("/", "web/dist")

	// NOTE: do NOT print "http://127.0.0.1:8080" here — `wails dev` watches
	// stdout for `http://host:port` patterns to discover the Vite dev
	// server, and it would mis-read this line as Vite's URL flipping to
	// :8080. Once that happens the WkWebView gets re-pointed to the Echo
	// server, which serves an empty / stale `web/dist/` in dev (Vite
	// never writes to disk in dev), leaving the window painted as the
	// WkWebView's transparent background — i.e. the `bg-zinc-950` frame
	// color, which presents as a solid black screen. Keep this as a plain
	// "port NNN" string so the regex in `wails dev` ignores it.
	log.Info().Msg("Web Dashboard available on port 8080")
	if err := e.Start("127.0.0.1:8080"); err != nil {
		log.Error().Err(err).Msg("failed to start web server")
	}
}

type BotStatus struct {
	Running bool   `json:"running"`
	Message string `json:"message"`
}

// StartBot starts the bot with the given thresholds
func (a *App) StartBot(gold, elixir, dark int, upgradeWalls bool, searchEnabled bool) BotStatus {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.bot != nil {
		return BotStatus{Running: true, Message: "Bot already running"}
	}

	cfg := config.LoadOrDefault("config.json")
	cfg.Search.MinLootGold = gold
	cfg.Search.MinLootElixir = elixir
	cfg.Search.MinLootDarkElixir = dark
	cfg.Upgrade.UpgradeWalls = upgradeWalls
	cfg.Search.Enabled = searchEnabled

	// Create a placeholder to indicate the bot is starting
	a.botCtx, a.cancel = context.WithCancel(context.Background())

	go func() {
		b, err := bot.NewBot(cfg)
		if err != nil {
			// The orchestrator wraps the error with a Summary(); the
			// underlying cause is still reachable via errors.Unwrap
			// for programmatic consumers. The console logger now
			// surfaces `error="..."` so the user no longer has to
			// grep app.log to see what failed.
			log.Error().Err(err).Msg("failed to initialize bot")
			runtime.EventsEmit(a.ctx, "bot_error", fmt.Sprintf("Initialization Error: %v", err))
			runtime.EventsEmit(a.ctx, "bot_init_failed", map[string]interface{}{
				"message": err.Error(),
			})

			a.mu.Lock()
			a.cancel()
			a.bot = nil
			a.mu.Unlock()
			return
		}

		b.OnFrame = func(frame string) {
			if a.ctx != nil {
				runtime.EventsEmit(a.ctx, "live_feed", frame)
			}
		}

		b.OnStatsUpdate = func() {
			a.saveStats()
		}

		a.mu.Lock()
		a.bot = b
		a.mu.Unlock()

		if err := a.bot.Start(); err != nil {
			log.Error().Err(err).Msg("failed to start bot")
			runtime.EventsEmit(a.ctx, "bot_error", fmt.Sprintf("Start Error: %v", err))

			a.mu.Lock()
			a.bot = nil
			a.mu.Unlock()
		}
	}()

	return BotStatus{Running: true, Message: "Bot initialization started in background"}
}

// StopBot stops the bot
func (a *App) StopBot() BotStatus {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.cancel != nil {
		a.cancel()
	}

	if a.bot == nil {
		return BotStatus{Running: false, Message: "Bot not running"}
	}

	// Capture and accumulate final stats before stopping
	current := a.bot.Stats()
	a.lastStats = bot.BotStats{
		AttacksCompleted: a.lastStats.AttacksCompleted + current.AttacksCompleted,
		SearchSkips:      a.lastStats.SearchSkips + current.SearchSkips,
		TotalGold:        a.lastStats.TotalGold + current.TotalGold,
		TotalElixir:      a.lastStats.TotalElixir + current.TotalElixir,
		TotalDE:          a.lastStats.TotalDE + current.TotalDE,
		Stars0:           a.lastStats.Stars0 + current.Stars0,
		Stars1:           a.lastStats.Stars1 + current.Stars1,
		Stars2:           a.lastStats.Stars2 + current.Stars2,
		Stars3:           a.lastStats.Stars3 + current.Stars3,
		Uptime:           a.lastStats.Uptime + current.Uptime,
		AdbHealth:        current.AdbHealth,
	}

	a.cancel()
	a.bot.Stop()
	a.bot = nil

	a.mu.Unlock()
	a.saveStats()
	a.mu.Lock()

	return BotStatus{Running: false, Message: "Bot stopped"}
}

// IsRunning returns if the bot is currently running
func (a *App) IsRunning() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.bot != nil
}

// GetConfig returns the current config.json settings
func (a *App) GetConfig() *config.BotConfig {
	return config.LoadOrDefault("config.json")
}

// GetStats returns the bot's live runtime statistics
func (a *App) GetStats() bot.BotStats {
	a.mu.Lock()
	defer a.mu.Unlock()

	res := a.lastStats
	if a.bot != nil {
		current := a.bot.Stats()
		res = bot.BotStats{
			AttacksCompleted: a.lastStats.AttacksCompleted + current.AttacksCompleted,
			SearchSkips:      a.lastStats.SearchSkips + current.SearchSkips,
			TotalGold:        a.lastStats.TotalGold + current.TotalGold,
			TotalElixir:      a.lastStats.TotalElixir + current.TotalElixir,
			TotalDE:          a.lastStats.TotalDE + current.TotalDE,
			Stars0:           a.lastStats.Stars0 + current.Stars0,
			Stars1:           a.lastStats.Stars1 + current.Stars1,
			Stars2:           a.lastStats.Stars2 + current.Stars2,
			Stars3:           a.lastStats.Stars3 + current.Stars3,
			Uptime:           a.lastStats.Uptime + current.Uptime,
			AdbHealth:        current.AdbHealth,
		}
	}
	return res
}

// GetLogs returns the buffered logs
func (a *App) GetLogs() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	// Return a copy to avoid race conditions
	res := make([]string, len(a.logBuffer))
	copy(res, a.logBuffer)
	return res
}

// GetAttackHistory returns persistent log of attacks
func (a *App) GetAttackHistory() []bot.AttackReport {
	data, err := os.ReadFile(paths.ResolveConfig("attack_history.json"))
	if err != nil {
		return []bot.AttackReport{}
	}
	var history []bot.AttackReport
	if err := json.Unmarshal(data, &history); err != nil {
		return []bot.AttackReport{}
	}
	return history
}

// GetLiveScreenshot captures the current frame via ADB and encodes it to base64
func (a *App) GetLiveScreenshot() (string, error) {
	a.mu.Lock()
	var client *adb.Client
	if a.bot != nil {
		// Optimization: If the bot is running, it's already capturing frames.
		// Return the latest processed frame instead of triggering a new ADB capture.
		frame := a.bot.GetLastFrame()
		if frame != "" {
			a.mu.Unlock()
			return frame, nil
		}
		client = a.bot.GetClient()
	}
	a.mu.Unlock()

	if client == nil {
		// Bot isn't running and we have no client to fall back to.
		// React's setInterval(updateScreenshot, 1000) calls this IPC
		// method once per second. When ADB is offline, an unbounded
		// adb.Connect() here stacks a goroutine per tick and floods
		// Wails' IPC bridge until the webview hangs on a black frame.
		// Returning empty is the correct placeholder while the bot
		// isn't started; once the user clicks Start, the running-bot
		// branch above (GetLastFrame / a.bot.GetClient()) takes over.
		//
		// IMPORTANT: if you ever re-add client.Connect() below, it
		// MUST be wrapped in a finite context.WithTimeout(<=2*time.Second)
		// so a single stalled connect cannot pin a goroutine — see
		// the IPC-bridge-storm comment above. Without a timeout, this
		// method will re-black-screen wails dev on adb-unreachable launch.
		return "", nil
	}

	mat, err := client.CaptureToMat()
	if err != nil {
		return "", err
	}
	defer mat.Close()

	if mat.Empty() {
		return "", fmt.Errorf("empty mat captured")
	}

	buf, err := gocv.IMEncode(".jpg", mat)
	if err != nil {
		return "", err
	}
	defer buf.Close()

	return base64.StdEncoding.EncodeToString(buf.GetBytes()), nil
}

// SaveConfig updates config.json settings
func (a *App) SaveConfig(minGold, minElixir, minDE int, upgradeWalls bool, strategyFile string, searchEnabled bool, stall int) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	cfg := config.LoadOrDefault("config.json")
	cfg.Search.MinLootGold = minGold
	cfg.Search.MinLootElixir = minElixir
	cfg.Search.MinLootDarkElixir = minDE
	cfg.Upgrade.UpgradeWalls = upgradeWalls
	cfg.Search.Enabled = searchEnabled
	cfg.Attack.StallTimerSeconds = stall
	if strategyFile != "" {
		cfg.Attack.StrategyFile = strategyFile
	}

	// Update running bot in real-time if it exists
	if a.bot != nil {
		a.bot.UpdateConfig(cfg)
	}

	bytes, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(paths.ResolveConfig("config.json"), bytes, 0644)
}

// GetStrategies lists available strategy files
func (a *App) GetStrategies() []string {
	var files []string
	matches, err := filepath.Glob(paths.Resolve("strategies/*.yaml"))
	if err == nil {
		for _, m := range matches {
			files = append(files, filepath.Base(filepath.ToSlash(m)))
		}
	}
	csvMatches, err := filepath.Glob(paths.Resolve("strategies/*.csv"))
	if err == nil {
		for _, m := range csvMatches {
			files = append(files, filepath.Base(filepath.ToSlash(m)))
		}
	}
	return files
}

// ---- Updater bindings ----
//
// The following methods are exposed to the React side via Wails. The
// names are stable; renaming requires regenerating wailsjs/ bindings
// AND matching the UpdateBanner.tsx imports.

// GetAppVersion returns the embedded build version (ldflags-injected
// by the Makefile — see version.go).
func (a *App) GetAppVersion() string { return version }

// GetUpdateStatus returns the current updater status snapshot.
func (a *App) GetUpdateStatus() updater.Status {
	if a.updater == nil {
		return updater.Status{
			CurrentVersion: version,
			State:          updater.StateError,
			Error:          "updater not initialized",
		}
	}
	return a.updater.GetStatus()
}

// CheckForUpdate triggers an immediate GitHub-Releases check. Returns
// the fresh status; the `updater_status` event is also emitted.
func (a *App) CheckForUpdate() (updater.Status, error) {
	if a.updater == nil {
		return updater.Status{State: updater.StateError}, fmt.Errorf("updater not initialized")
	}
	return a.updater.Check(a.ctx)
}

// DownloadUpdate downloads + SHA256-verifies the matched asset. The
// result is the absolute path of the verified file.
func (a *App) DownloadUpdate() (string, error) {
	if a.updater == nil {
		return "", fmt.Errorf("updater not initialized")
	}
	return a.updater.Download(a.ctx)
}

// ApplyUpdate opens the downloaded zip in Finder so the user can
// drag-replace the running app (Phase-2 fast / always-works path).
// Use InstallAndRestart for an in-place auto-replace.
func (a *App) ApplyUpdate() error {
	if a.updater == nil {
		return fmt.Errorf("updater not initialized")
	}
	return a.updater.Apply()
}

// InstallAndRestart is the one-click auto-install path.
// Sequence (intentional ordering):
//  1. Stop the bot synchronously so ADB + stats flush cleanly.
//  2. Save persisted stats via the existing path.
//  3. Mark Status=StateRestarting so React covers the Wails ↔ helper
//     transition with a non-dismissible splash.
//  4. Spawn updater.ApplyAuto() which detaches install_update.sh.
//  5. Schedule os.Exit(0) AFTER a short delay so the IPC reply has
//     time to land at React before the process dies (about 1s is
//     safe on the local socket). Using runtime.Quit is tempting but
//     races with the helper script's `kill -0 $PPID` loop.
//
// Returns nil iff the helper was successfully started. If the helper
// fails afterwards, it's the helper's responsibility to surface a
// native macOS notification (see install_update.sh).
func (a *App) InstallAndRestart() error {
	if a.updater == nil {
		return fmt.Errorf("updater not initialized")
	}

	// Step 1: stop the bot synchronously if running.
	if a.IsRunning() {
		log.Info().Msg("InstallAndRestart: stopping bot to drain ADB before exit")
		_ = a.StopBot()
	}

	// Step 2: flush persistent stats. saveStats is idempotent + safe
	// even when no bot is running.
	a.saveStats()

	// Step 3: cover the Wails exit + helper wait window.
	// We deliberately do NOT emit "updater_status" here — the 2s
	// ticker in forwardUpdaterStatus emits within ~2s and we don't
	// want React to receive two close-in-time events (the IPC emit
	// + the ticker race). SetState alone is enough.
	a.updater.SetState(updater.StateRestarting)

	// Step 4: detach the helper script. Returns (started, error).
	// If false, the helper is missing (e.g. dev build); fall back to
	// Finder-open and don't exit.
	started, err := a.updater.ApplyAuto()
	if err != nil || !started {
		log.Warn().Err(err).Msg("InstallAndRestart: helper unavailable, falling back to Finder")
		_ = a.updater.Apply()
		// Revert state so the UI comes back to "ready" instead of
		// staying on the restart splash.
		a.updater.SetState(updater.StateReady)
		return err
	}

	// Step 5: exit cleanly so the helper script's PID wait resolves.
	// 1s delay gives the Wails JS bridge time to flush our success
	// response + the splash render before the process vanishes.
	go func() {
		time.Sleep(1 * time.Second)
		log.Info().Msg("InstallAndRestart: helper detached, exiting for bundle swap")
		os.Exit(0)
	}()

	return nil
}

// SkipCurrentVersion marks the current latest version as "skip this".
// Persisted to ~/Library/Application Support/ClashGO/skip_version.txt.
func (a *App) SkipCurrentVersion() error {
	if a.updater == nil {
		return fmt.Errorf("updater not initialized")
	}
	st := a.updater.GetStatus()
	if st.LatestVersion == "" {
		return fmt.Errorf("no version available to skip")
	}
	a.updater.SetSkipVersion(st.LatestVersion)
	return nil
}

// ClearSkippedVersion is the inverse of SkipCurrentVersion.
func (a *App) ClearSkippedVersion() error {
	if a.updater == nil {
		return fmt.Errorf("updater not initialized")
	}
	a.updater.SetSkipVersion("")
	return nil
}
