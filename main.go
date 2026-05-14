package main

import (
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Ducky705/ClashGo/internal/bot"
	"github.com/Ducky705/ClashGo/internal/config"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

var (
	version = "dev"
	commit  = "none"
)

func main() {
	zerolog.TimeFieldFormat = zerolog.TimeFormatUnix
	zerolog.SetGlobalLevel(zerolog.DebugLevel)
	log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stderr, TimeFormat: time.RFC3339})

	fmt.Printf("coc-bot v%s (%.7s)\n", version, commit)

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