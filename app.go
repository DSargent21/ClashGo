package main

import (
	"context"
	"fmt"
	"os"
	"sync"

	"github.com/Ducky705/ClashGo/internal/bot"
	"github.com/Ducky705/ClashGo/internal/config"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// App struct
type App struct {
	ctx    context.Context
	bot    *bot.Bot
	botCtx context.Context
	cancel context.CancelFunc
	mu     sync.Mutex
	echo   *echo.Echo
}

type WailsLogWriter struct {
	ctx context.Context
}

func (w *WailsLogWriter) Write(p []byte) (n int, err error) {
	if w.ctx != nil {
		runtime.EventsEmit(w.ctx, "bot_log", string(p))
	}
	return len(p), nil
}

// NewApp creates a new App application struct
func NewApp() *App {
	return &App{}
}

// startup is called when the app starts. The context is saved
// so we can call the runtime methods
func (a *App) startup(ctx context.Context) {
	a.ctx = ctx

	// Setup log bridge
	wailsWriter := &WailsLogWriter{ctx: ctx}
	consoleWriter := zerolog.ConsoleWriter{Out: os.Stdout, TimeFormat: "15:04:05"}
	multi := zerolog.MultiLevelWriter(consoleWriter, wailsWriter)
	log.Logger = zerolog.New(multi).With().Timestamp().Logger()

	// Start Web Server for Remote Access
	go a.startWebServer()
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
func (a *App) StartBot(gold, elixir, dark int, upgradeWalls bool) BotStatus {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.bot != nil {
		return BotStatus{Running: true, Message: "Bot already running"}
	}

	cfg := config.DefaultConfig()
	cfg.Search.MinLootGold = gold
	cfg.Search.MinLootElixir = elixir
	cfg.Search.MinLootDarkElixir = dark
	cfg.Upgrade.UpgradeWalls = upgradeWalls

	b, err := bot.NewBot(cfg)
	if err != nil {
		return BotStatus{Running: false, Message: fmt.Sprintf("Error: %v", err)}
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

	a.cancel()
	a.bot.Stop()
	a.bot = nil

	return BotStatus{Running: false, Message: "Bot stopped"}
}

// GetConfig returns the current default config
func (a *App) GetConfig() *config.BotConfig {
	return config.DefaultConfig()
}
