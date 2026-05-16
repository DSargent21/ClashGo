package main

import (
	"os"
	"time"

	"github.com/Ducky705/ClashGo/internal/adb"
	"github.com/Ducky705/ClashGo/internal/attack"
	"github.com/Ducky705/ClashGo/internal/config"
	"github.com/Ducky705/ClashGo/internal/game"
	"github.com/Ducky705/ClashGo/pkg/strategy"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

func main() {
	// Professional logging setup
	zerolog.TimeFieldFormat = time.RFC3339
	log.Logger = log.Output(zerolog.ConsoleWriter{
		Out:        os.Stderr,
		TimeFormat: "15:04:05",
	})
	zerolog.SetGlobalLevel(zerolog.DebugLevel)

	// 1. Load Config
	botCfg := config.DefaultConfig()
	// Set the device ID based on your adb devices output
	botCfg.Device.DeviceID = "127.0.0.1:5555" 

	// 2. Initialize ADB
	client := adb.NewClient(func(c *adb.Client) {
		c.DeviceID = botCfg.Device.DeviceID
	})
	if err := client.Connect(); err != nil {
		log.Fatal().Err(err).Msg("failed to connect to ADB")
	}
	defer client.Close()

	// 3. Load Calibration
	calibrator := game.NewCalibrator(client)
	cal, err := calibrator.Calibrate()
	if err != nil {
		log.Fatal().Err(err).Msg("failed to calibrate")
	}

	// 4. Setup Attack Executor
	executor := attack.NewExecutor(client, cal, &botCfg.Attack, log.Logger)

	// 4. Load Strategy
	stratPath := "assets/strategies/auto_edrag_rush.yaml"
	s, err := strategy.ParseYAML(stratPath)
	if err != nil {
		log.Fatal().Err(err).Str("path", stratPath).Msg("failed to load strategy")
	}

	log.Info().Str("strategy", s.Name).Msg("starting attack")

	// 5. ZOOM OUT COMPLETELY
	log.Info().Msg("zooming out for maximum deployment area...")
	for i := 0; i < 5; i++ {
		client.ZoomOut()
		time.Sleep(200 * time.Millisecond)
	}
	time.Sleep(1000 * time.Millisecond) // Increased for stability

	log.Info().Msg("capturing screen for base analysis...")
	// 6. Capture Live Screen
	screen, err := client.CaptureToMat()
	if err != nil {
		log.Fatal().Err(err).Msg("failed to capture screen")
	}
	defer screen.Close()

	// 6. EXECUTE ATTACK
	log.Info().Msg("executing dynamic deployment sequence...")
	if err := executor.DeployDynamic(s, screen); err != nil {
		log.Fatal().Err(err).Msg("attack failed")
	}

	log.Info().Msg("attack sequence complete")
}
