package main

import (
	"os"
	"time"

	"github.com/Ducky705/ClashGO/internal/adb"
	"github.com/Ducky705/ClashGO/internal/attack"
	"github.com/Ducky705/ClashGO/internal/config"
	"github.com/Ducky705/ClashGO/internal/game"
	"github.com/Ducky705/ClashGO/pkg/strategy"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

func main() {
	// 1. Setup Logging
	zerolog.TimeFieldFormat = time.RFC3339
	log.Logger = log.Output(zerolog.ConsoleWriter{
		Out:        os.Stderr,
		TimeFormat: "15:04:05",
	})
	zerolog.SetGlobalLevel(zerolog.InfoLevel)

	// 2. Initialize Hardware/Client
	botCfg := config.DefaultConfig()
	client := adb.NewClient()
	if err := client.AutoDetectDevice(); err != nil {
		log.Warn().Err(err).Msg("auto-detect failed, using default ID")
	}
	if err := client.Connect(); err != nil {
		log.Fatal().Err(err).Msg("failed to connect to ADB")
	}
	defer client.Close()

	// 3. Calibration
	calibrator := game.NewCalibrator(client)
	cal, err := calibrator.Calibrate()
	if err != nil {
		log.Fatal().Err(err).Msg("failed to calibrate screen")
	}

	// 4. Load EDrag Strategy
	stratPath := "assets/strategies/auto_edrag_rush.yaml"
	s, err := strategy.ParseYAML(stratPath)
	if err != nil {
		log.Fatal().Err(err).Str("path", stratPath).Msg("failed to load edrag strategy")
	}

	log.Info().Str("strategy", s.Name).Msg("Starting Edrag Test Attack")

	// 5. Preparation: Zoom out completely
	log.Info().Msg("Preparing screen: Zooming out...")
	for i := 0; i < 5; i++ {
		client.ZoomOut()
		time.Sleep(200 * time.Millisecond)
	}
	time.Sleep(1000 * time.Millisecond)

	// 6. Execute Attack
	executor := attack.NewExecutor(client, cal, &botCfg.Attack, log.Logger)
	
	screen, err := client.CaptureToMat()
	if err != nil || screen.Empty() {
		log.Fatal().Err(err).Msg("failed to capture valid screen for deployment")
	}
	defer screen.Close()

	log.Info().Msg("Deploying units...")
	if _, err := executor.DeployDynamic(s, screen); err != nil {
		log.Fatal().Err(err).Msg("attack execution failed")
	}

	log.Info().Msg("Edrag test attack complete.")
}
