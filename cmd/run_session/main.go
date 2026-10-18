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

func main() {
	durationFlag := flag.Duration("duration", 60*time.Minute, "Session run duration (default: 60m)")
	flag.Parse()

	logger.Init(false, os.Stdout)
	cfg := config.LoadOrDefault("config.json")

	log.Info().
		Dur("target_duration", *durationFlag).
		Str("strategy", cfg.Attack.StrategyFile).
		Bool("walls", cfg.Upgrade.UpgradeWalls).
		Bool("builders", cfg.Upgrade.AutoBuilders).
		Bool("lab", cfg.Upgrade.AutoLab).
		Bool("pets", cfg.Upgrade.AutoPets).
		Msg("starting autonomous ClashGO session")

	ctx, cancel := context.WithTimeout(context.Background(), *durationFlag)
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigCh
		log.Warn().Msg("received termination signal, shutting down cleanly...")
		cancel()
	}()

	b, err := bot.NewBotWithContext(ctx, cfg)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to initialize bot")
	}

	if err := b.Start(); err != nil {
		log.Fatal().Err(err).Msg("failed to start bot loop")
	}

	ticker := time.NewTicker(2 * time.Minute)
	defer ticker.Stop()

	startTime := time.Now()
	for {
		select {
		case <-ctx.Done():
			log.Info().Dur("elapsed", time.Since(startTime).Round(time.Second)).Msg("session duration elapsed; stopping bot...")
			b.Stop()
			finalStats := b.Stats()
			fmt.Printf("\n=========================================\n")
			fmt.Printf("   CLASHGO 1-HOUR AUTONOMOUS RUN REPORT  \n")
			fmt.Printf("=========================================\n")
			fmt.Printf("Elapsed Time:      %v\n", time.Since(startTime).Round(time.Second))
			fmt.Printf("Attacks Completed: %d\n", finalStats.AttacksCompleted)
			fmt.Printf("Search Skips:      %d\n", finalStats.SearchSkips)
			fmt.Printf("Total Gold Loot:   %d\n", finalStats.TotalGold)
			fmt.Printf("Total Elixir Loot: %d\n", finalStats.TotalElixir)
			fmt.Printf("Total DE Loot:     %d\n", finalStats.TotalDE)
			fmt.Printf("Stars:             3★: %d | 2★: %d | 1★: %d | 0★: %d\n", finalStats.Stars3, finalStats.Stars2, finalStats.Stars1, finalStats.Stars0)
			fmt.Printf("ADB Capture Ms:    %.1f ms (Errors: %d)\n", finalStats.AdbHealth.AvgCaptureMs, finalStats.AdbHealth.ErrorsTotal)
			fmt.Printf("=========================================\n")
			return
		case <-ticker.C:
			st := b.Stats()
			log.Info().
				Dur("elapsed", time.Since(startTime).Round(time.Second)).
				Int32("attacks", st.AttacksCompleted).
				Int32("skips", st.SearchSkips).
				Int64("gold", st.TotalGold).
				Int64("elixir", st.TotalElixir).
				Int64("de", st.TotalDE).
				Float64("adb_ms", st.AdbHealth.AvgCaptureMs).
				Uint64("adb_errs", st.AdbHealth.ErrorsTotal).
				Msg("telemetry pulse")
		}
	}
}
