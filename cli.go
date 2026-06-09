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

var (
	version = "dev"
	commit  = "none"
)

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

	flag.Parse()

	cfg.Upgrade.UpgradeWalls = *upgradeWalls
	cfg.Search.MinLootGold = *minGold
	cfg.Search.MinLootElixir = *minElixir
	cfg.Search.MinLootDarkElixir = *minDE
	cfg.Attack.StrategyFile = *strategy
	cfg.Device.DeviceID = *deviceID
}