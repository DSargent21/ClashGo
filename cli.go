//go:build cli
// +build cli

package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Ducky705/ClashGO/internal/bot"
	"github.com/Ducky705/ClashGO/internal/config"
	"github.com/Ducky705/ClashGO/internal/logger"
	"github.com/rs/zerolog/log"
)

// version + commit vars live in version.go (no build tag) so they're
// shared by both the `cli` build (this file) and the Wails GUI build
// (main.go). The Makefile overrides them via `-ldflags`.

// deployOnly is hoisted to package scope so main() can branch on it.
// flag.BoolVar binds the flag declaration to this variable.
var deployOnly bool

func main() {
	// Professional logging setup
	logger.Init(os.Getenv("DEBUG") != "")

	fmt.Printf("ClashGO v%s (%.7s)\n", version, commit)

	// Remove execution timeout for full pipeline test
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		select {
		case <-sigCh:
			log.Info().Msg("shutdown signal received")
		case <-ctx.Done():
			if ctx.Err() == context.DeadlineExceeded {
				log.Info().Msg("test execution timeout reached")
			}
		}
		cancel()
	}()

	cfg := loadConfig()
	parseFlags(cfg)

	b, err := bot.NewBot(cfg)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to initialize bot")
	}

	if deployOnly {
		log.Info().Msg("deploy-only mode: capturing current screen and deploying once.")
		qdErr := b.QuickDeploy()
		// Always cleanup before any exit. log.Fatal below calls os.Exit, so
		// calling b.Stop() AFTER the error check would skip closing the adb
		// client + the per-session duke-picks NDJSON (last lines may not
		// flush). Run Stop() unconditionally first.
		b.Stop()
		if qdErr != nil {
			log.Fatal().Err(qdErr).Msg("deploy-only failed")
		}
		return
	}

	go func() {
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				stats := b.Stats()
				health := b.Health()
				log.Info().
					Int32("attacks", stats.AttacksCompleted).
					Str("uptime", stats.Uptime.Round(time.Second).String()).
					Float64("avg_ms", health.AvgCaptureMs).
					Msg("bot stats")
			}
		}
	}()

	if err := b.Start(); err != nil {
		log.Fatal().Err(err).Msg("failed to start bot")
	}

	<-ctx.Done()

	log.Info().Msg("shutting down...")
	b.Stop()
	log.Info().Msg("shutdown complete")
}

func loadConfig() *config.BotConfig {
	cfg := config.LoadOrDefault("config.json")
	if cfg != nil {
		return cfg
	}
	return config.DefaultConfig()
}

func parseFlags(cfg *config.BotConfig) {
	upgradeWalls := flag.Bool("upgrade-walls", cfg.Upgrade.UpgradeWalls, "Enable/disable automatic wall upgrades")
	minGold := flag.Int("gold", cfg.Search.MinLootGold, "Minimum gold to attack")
	minElixir := flag.Int("elixir", cfg.Search.MinLootElixir, "Minimum elixir to attack")
	minDE := flag.Int("de", cfg.Search.MinLootDarkElixir, "Minimum dark elixir to attack")
	strategy := flag.String("strategy", cfg.Attack.StrategyFile, "Path to strategy YAML file")
	deviceID := flag.String("device", cfg.Device.DeviceID, "ADB device ID")
	once := flag.Bool("once", false, "Run a single attack, then exit cleanly (sets MaxAttackPerSession=1 and triggers graceful shutdown when the attack finishes)")
	flag.BoolVar(&deployOnly, "deploy-only", false, "Skip the search/attack-button pipeline and deploy immediately on the current screen. Assumes you're already on the attack screen with troops loaded. Pairs with --once for a single manual deploy. Disables game restart on startup so your deploy screen isn't force-stopped.")

	flag.Parse()

	cfg.Upgrade.UpgradeWalls = *upgradeWalls
	cfg.Search.MinLootGold = *minGold
	cfg.Search.MinLootElixir = *minElixir
	cfg.Search.MinLootDarkElixir = *minDE
	cfg.Attack.StrategyFile = *strategy
	cfg.Device.DeviceID = *deviceID

	if *once {
		original := cfg.Attack.MaxAttackPerSession
		cfg.Attack.MaxAttackPerSession = 1
		fmt.Printf("ClashGO: --once set; capping attacks at 1 (was %d) and exiting cleanly after the first battle.\n", original)
	}

	if deployOnly {
		// Critical: do NOT force-stop CoC on startup. The user is already
		// on the deploy screen (or fresh in-match) — kicking them out would
		// resurrect the home screen and lose their base.
		cfg.Device.RestartOnStartup = false
		// Cap implicitly — deploy-only has no concept of "many".
		originalCap := cfg.Attack.MaxAttackPerSession
		cfg.Attack.MaxAttackPerSession = 1
		fmt.Printf("ClashGO: --deploy-only set; skipping game restart, deploying on current screen once (cap was %d).\n", originalCap)
	}
}