package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/Ducky705/ClashGo/internal/adb"
	"github.com/Ducky705/ClashGo/internal/bot"
	"github.com/Ducky705/ClashGo/internal/config"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"github.com/rs/zerolog"
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
	}
}

// startup is called when the app starts. The context is saved
// so we can call the runtime methods
func (a *App) startup(ctx context.Context) {
	a.ctx = ctx

	// Load previous stats from disk
	if data, err := os.ReadFile("stats.json"); err == nil {
		if err := json.Unmarshal(data, &a.lastStats); err != nil {
			log.Error().Err(err).Msg("failed to load stats.json")
		}
	}

	// Sync stats from history if stats.json was missing or empty but history exists
	if a.lastStats.AttacksCompleted == 0 {
		a.rebuildStatsFromHistory()
	}

	// Setup log bridge
	wailsWriter := &WailsLogWriter{app: a}
	consoleWriter := zerolog.ConsoleWriter{Out: os.Stdout, TimeFormat: "15:04:05"}
	multi := zerolog.MultiLevelWriter(consoleWriter, wailsWriter)
	log.Logger = zerolog.New(multi).With().Timestamp().Logger()

	// Start Web Server for Remote Access
	go a.startWebServer()
}

func (a *App) rebuildStatsFromHistory() {
	history := a.GetAttackHistory()
	if len(history) == 0 {
		return
	}

	var gold, elixir, de int64
	var s0, s1, s2, s3 int32
	for _, rep := range history {
		gold += int64(rep.GoldStolen)
		elixir += int64(rep.ElixirStolen)
		de += int64(rep.DarkElixirStolen)
		switch rep.Stars {
		case 0:
			s0++
		case 1:
			s1++
		case 2:
			s2++
		case 3:
			s3++
		}
	}

	a.mu.Lock()
	a.lastStats = bot.BotStats{
		AttacksCompleted: int32(len(history)),
		TotalGold:        gold,
		TotalElixir:      elixir,
		TotalDE:          de,
		Stars0:           s0,
		Stars1:           s1,
		Stars2:           s2,
		Stars3:           s3,
	}
	a.mu.Unlock()
	a.saveStats()
}

func (a *App) shutdown(ctx context.Context) {
	a.saveStats()
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

	// Persist stats to disk
	bytes, err := json.MarshalIndent(stats, "", "  ")
	if err != nil {
		log.Error().Err(err).Msg("failed to marshal stats")
		return
	}

	if err := os.WriteFile("stats.json", bytes, 0644); err != nil {
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
	_ = os.Remove("stats.json")
	_ = os.Remove("attack_history.json")
}

func (a *App) startWebServer() {
	e := echo.New()
	e.HideBanner = true
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

	log.Info().Msg("Web Dashboard available at http://0.0.0.0:8080")
	if err := e.Start(":8080"); err != nil {
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

	b, err := bot.NewBot(cfg)
	if err != nil {
		return BotStatus{Running: false, Message: fmt.Sprintf("Error: %v", err)}
	}

	b.OnFrame = func(frame string) {
		runtime.EventsEmit(a.ctx, "live_feed", frame)
	}

	b.OnStatsUpdate = func() {
		a.saveStats()
	}

	a.bot = b
	a.botCtx, a.cancel = context.WithCancel(context.Background())

	go func() {
		if err := a.bot.Start(); err != nil {
			runtime.EventsEmit(a.ctx, "bot_error", err.Error())
		}
	}()

	return BotStatus{Running: true, Message: "Bot started"}
}

// StopBot stops the bot
func (a *App) StopBot() BotStatus {
	a.mu.Lock()
	defer a.mu.Unlock()

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
	data, err := os.ReadFile("attack_history.json")
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
		cfg := config.LoadOrDefault("config.json")
		client = adb.NewClient(
			adb.WithHost(cfg.Device.ADBHost),
			adb.WithPort(cfg.Device.ADBPort),
		)
		client.DeviceID = cfg.Device.DeviceID
		if err := client.Connect(); err != nil {
			return "", err
		}
		defer client.Close()
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
func (a *App) SaveConfig(minGold, minElixir, minDE int, upgradeWalls bool, strategyFile string, searchEnabled bool) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	cfg := config.LoadOrDefault("config.json")
	cfg.Search.MinLootGold = minGold
	cfg.Search.MinLootElixir = minElixir
	cfg.Search.MinLootDarkElixir = minDE
	cfg.Upgrade.UpgradeWalls = upgradeWalls
	cfg.Search.Enabled = searchEnabled
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
	return os.WriteFile("config.json", bytes, 0644)
}

// GetStrategies lists available strategy files
func (a *App) GetStrategies() []string {
	var files []string
	matches, err := filepath.Glob("assets/strategies/*.yaml")
	if err == nil {
		for _, m := range matches {
			files = append(files, filepath.Base(filepath.ToSlash(m)))
		}
	}
	csvMatches, err := filepath.Glob("assets/strategies/*.csv")
	if err == nil {
		for _, m := range csvMatches {
			files = append(files, filepath.Base(filepath.ToSlash(m)))
		}
	}
	return files
}

