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
	lastStats bot.BotStats
	logBuffer []string

	// cachedHistory is the in-memory mirror of attack_history.json
	// so React's 2 s poll for GetAttackHistory doesn't hit the
	// filesystem on every tick. Refreshed lazily on first call
	// (cold start) and eagerly on each bot.statsUpdate callback
	// (end of every attack — bounded to ~once per attack, well
	// below the 0.5 Hz React poll). RWMutex because read dominates
	// on the hot IPC path.
	cachedHistory   []bot.AttackReport
	cachedHistoryMu sync.RWMutex

	// Updater wiring
	updater       *updater.Service
	updaterBgCtx  context.Context
	updaterBgStop context.CancelFunc
}

type WailsLogWriter struct {
	app *App
}

// Write bridges zerolog to a bounded in-memory ring buffer read by
// App.GetLogs() on the React poll cadence. A prior revision also
// emitted `"bot_log"` Events here for a future streaming-log
// viewer, but the bridge emit had no React subscriber (verified:
// no EventsOn("bot_log", ...) anywhere in web/src/components) and
// cost a WailsIPC round-trip per zerolog line. Removed.
func (w *WailsLogWriter) Write(p []byte) (n int, err error) {
	msg := string(p)

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
		stats = mergeStats(a.lastStats, a.bot.Stats())
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

// mergeStats accumulates the live bot's session counters into acc.
// Bot counters are zeroed on every NewBot, so persisted/returned totals
// are acc + current. AdbHealth and CPU metrics are live values and are
// always taken from current.
func mergeStats(acc, current bot.BotStats) bot.BotStats {
	return bot.BotStats{
		AttacksCompleted: acc.AttacksCompleted + current.AttacksCompleted,
		SearchSkips:      acc.SearchSkips + current.SearchSkips,
		TotalGold:        acc.TotalGold + current.TotalGold,
		TotalElixir:      acc.TotalElixir + current.TotalElixir,
		TotalDE:          acc.TotalDE + current.TotalDE,
		Stars0:           acc.Stars0 + current.Stars0,
		Stars1:           acc.Stars1 + current.Stars1,
		Stars2:           acc.Stars2 + current.Stars2,
		Stars3:           acc.Stars3 + current.Stars3,
		Uptime:           acc.Uptime + current.Uptime,
		AdbHealth:        current.AdbHealth,
		CPUTimeSec:       current.CPUTimeSec,
		CPUCores:         current.CPUCores,
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
	// Drop the cached history so the next GetAttackHistory re-reads
	// (correct empty) state from disk instead of serving stale rows
	// we'd just deleted from disk but still keep in memory.
	a.cachedHistoryMu.Lock()
	a.cachedHistory = nil
	a.cachedHistoryMu.Unlock()
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

		// Note: b.OnFrame used to be wired to a `"live_feed"`
		// EventsEmit that pushed a 50–150 KB base64 JPEG over the
		// WailsIPC bridge at capture-loop frequency (up to 10 FPS).
		// The React UI never subscribed to "live_feed" (verified:
		// web/src/components/* has no EventsOn matching that name)
		// so each emit was a wasted IPC round-trip. The live
		// screenshot now flows exclusively through GetLiveScreenshot()
		// — it returns the same b.lastFrame string from atomic.Value
		// without burning the bridge. See App.GetLiveScreenshot.

		b.OnStatsUpdate = func() {
			// Refresh the in-memory history cache once per attack
			// so React's 2 s GetAttackHistory poll doesn't re-read
			// attack_history.json from disk every tick. Bounded
			// to roughly the bot's attack cadence (a few minutes)
			// — well below the 0.5 Hz poll rate.
			a.refreshHistory()
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

// StopBot stops the bot instantly.
//
// Behavior change vs. the previous implementation: the heavy teardown
// is detached to a goroutine so the IPC returns to React as fast as
// possible. Previously the IPC blocked on the entire graceful-shutdown
// chain — bot.Stop() (cancel + ADB client Close + globalAsyncWriter.Close
// which itself `wg.Wait()`s the in-flight stats write drain, plus
// template cache close + NDJSON file close) followed by saveStats()
// (synchronous JSON marshal + file write to disk). On an active attack
// that could run 1–3s before the IPC reply reached React, and the user
// perceived the Stop button as broken while the UI stayed on "Running".
//
// Now: we synchronously cancel the bot's internal context (so the
// captureLoop and any in-flight executeAttackSequence see the stop on
// their next `b.ctx.Done()` check — sub-millisecond) and detach the
// heavy teardown. The bot stops issuing new taps, captures, and state
// transitions immediately, which is what "stop right where we are"
// means in practice. React's `setIsRunning(false)` flips on the next
// React tick (within the 2s poll), so the UI feels instant.
//
// Concurrency: the heavy teardown runs in a detached goroutine that
// captures the local `bot` reference, so a subsequent StartBot can't
// observe a half-torn-down bot. The async-writer's global singleton
// still gets closed, which means a quick Stop → Start sequence could
// see AsyncWriteFile fall through to a synchronous os.WriteFile until
// the next NewAsyncWriter — acceptable, since the previous code path
// had the same constraint and the new behaviour is strictly an
// improvement on the slow path.
func (a *App) StopBot() BotStatus {
	a.mu.Lock()

	if a.cancel != nil {
		a.cancel()
	}

	if a.bot == nil {
		a.mu.Unlock()
		return BotStatus{Running: false, Message: "Bot not running"}
	}

	// Capture and accumulate final stats before stopping. All counters
	// are atomic.Int* loads, so this is O(1) and non-blocking.
	current := a.bot.Stats()
	a.lastStats = mergeStats(a.lastStats, current)

	// Snapshot the bot pointer + synchronously cancel its context so
	// the captureLoop and any in-flight executeAttackSequence see the
	// stop on their next `b.ctx.Done()` check. This is the
	// user-visible "stop right where we are" — no more taps, captures,
	// or state transitions issued from this point on.
	bot := a.bot
	bot.Cancel()

	// Detach references under the lock so:
	//   1. `IsRunning()` returns false the moment the lock is released
	//      (drives the React `setIsRunning(false)` flip in <1 React
	//      tick).
	//   2. A concurrent StartBot sees `a.bot == nil` and proceeds to
	//      construct a new bot without observing a half-torn-down one.
	a.bot = nil
	a.cancel = nil
	a.mu.Unlock()

	// Detach the slow teardown. The captureLoop will exit on its own
	// now that b.ctx is cancelled; this goroutine just releases OS
	// handles (ADB pipe, async-writer drain, template cache, NDJSON
	// file) and flushes the final stats snapshot to disk. None of that
	// is required for correctness of the user-visible stop signal.
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Error().Interface("panic", r).Msg("recovered panic during async bot stop")
			}
		}()
		bot.Stop()
		a.saveStats()
		// Re-seed attack-history cache after teardown. If the user
		// manually edited attack_history.json while the bot was
		// stopped, the next React poll re-reads from disk instead
		// of serving the stale pre-edit snapshot.
		a.cachedHistoryMu.Lock()
		a.cachedHistory = nil
		a.cachedHistoryMu.Unlock()
	}()

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
		res = mergeStats(a.lastStats, a.bot.Stats())
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

// GetAttackHistory returns the persistent log of attacks.
//
// Reads from an in-memory cache that is refreshed:
//   - lazily on the first call after process start (cold boot, before
//     any OnStatsUpdate has fired), via double-check locking so only
//     ONE goroutine ever performs the disk read; and
//   - eagerly in OnStatsUpdate at the end of every attack.
//
// React polls this at 0.5 Hz; without the cache that translates to
// a filesystem read + JSON unmarshal + re-marshal on every tick.
// With the cache, the per-tick cost is an atomic read + slice copy
// — no syscalls, no JSON.
func (a *App) GetAttackHistory() []bot.AttackReport {
	// Fast path: cache hit. RLock allows concurrent IPC polls to
	// all read in parallel.
	a.cachedHistoryMu.RLock()
	if a.cachedHistory != nil {
		out := make([]bot.AttackReport, len(a.cachedHistory))
		copy(out, a.cachedHistory)
		a.cachedHistoryMu.RUnlock()
		return out
	}
	a.cachedHistoryMu.RUnlock()

	// Cache miss: take the write lock and lazy-load via the shared
	// helper. Double-check inside the helper means only ONE
	// goroutine in the entire process performs the disk read on
	// cold start, even if React fires multiple polls back-to-back.
	a.cachedHistoryMu.Lock()
	a.ensureHistoryLoadedLocked()
	out := make([]bot.AttackReport, len(a.cachedHistory))
	copy(out, a.cachedHistory)
	a.cachedHistoryMu.Unlock()
	return out
}

// ensureHistoryLoadedLocked populates the cache from disk IF the
// cache is still nil. Caller MUST hold cachedHistoryMu.Lock()
// (write lock) on entry. Centralising the read+parse+assign here
// keeps the two refresh paths (cold-miss GetAttackHistory + per-
// attack OnStatsUpdate) behaviourally identical — and means a fix
// to the parse logic only needs to be made once.
//
// Failure modes are intentionally non-fatal: a missing or malformed
// file leaves the previous cache untouched (nil on cold-start).
// This matches the legacy behaviour where a bad file silently
// returned []string{}.
func (a *App) ensureHistoryLoadedLocked() {
	if a.cachedHistory != nil {
		return
	}
	data, err := os.ReadFile(paths.ResolveConfig("attack_history.json"))
	if err != nil {
		return
	}
	var hist []bot.AttackReport
	if err := json.Unmarshal(data, &hist); err != nil {
		return
	}
	a.cachedHistory = hist
}

// refreshHistory is the eager (per-attack-end) refresh path. It
// runs from inside the bot's OnStatsUpdate callback, so the cadence
// is bounded by attack frequency (~one refresh every few minutes of
// normal play) — well below the 0.5 Hz React poll.
//
// Holds cachedHistoryMu through the disk read so concurrent React
// polls wait on the writer rather than racing the assign. The
// per-attack write-lock window is ~50 ms (read+parse) which is
// imperceptible at 0.5 Hz polling.
func (a *App) refreshHistory() {
	a.cachedHistoryMu.Lock()
	a.ensureHistoryLoadedLocked()
	a.cachedHistoryMu.Unlock()
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
