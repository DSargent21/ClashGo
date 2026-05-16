package main

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Ducky705/ClashGo/internal/adb"
	"github.com/Ducky705/ClashGo/internal/game"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

func main() {
	// 1. Professional logging setup
	zerolog.TimeFieldFormat = time.RFC3339
	log.Logger = log.Output(zerolog.ConsoleWriter{
		Out:        os.Stderr,
		TimeFormat: "15:04:05",
	})
	zerolog.SetGlobalLevel(zerolog.InfoLevel)

	fmt.Println("=== Live Loot Recognition Monitor ===")
	fmt.Println("Press Ctrl+C to stop")
	fmt.Println()

	// 2. ADB Connection
	client := adb.NewClient(
		adb.WithHost("127.0.0.1"),
		adb.WithPort(5037),
		adb.WithTimeout(10*time.Second),
	)

	// Auto-select first device
	devs, err := client.Devices()
	if err != nil || len(devs) == 0 {
		log.Fatal().Err(err).Msg("no ADB devices found")
	}
	client.DeviceID = devs[0]
	if err := client.Connect(); err != nil {
		log.Fatal().Err(err).Msg("ADB connect error")
	}
	defer client.Close()
	log.Info().Str("device", client.DeviceID).Msg("connected")

	// 3. Initialize Recognition Engine
	ts, err := game.NewTemplateStore("assets/templates")
	if err != nil {
		log.Fatal().Err(err).Msg("TemplateStore error")
	}
	if err := ts.LoadTemplates(); err != nil {
		log.Fatal().Err(err).Msg("LoadTemplates error")
	}
	defer ts.Close()

	// 4. Handle Interruption
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	// 5. Main Capture Loop
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-sigChan:
			fmt.Println("\nStopping monitor...")
			return
		case <-ticker.C:
			screen, err := client.CaptureToMat()
			if err != nil {
				log.Error().Err(err).Msg("capture error")
				continue
			}

			// Calibrate based on current screen
			cal := &game.Calibration{
				PhysicalW: screen.Cols(),
				PhysicalH: screen.Rows(),
				ScaleX:    float64(screen.Cols()) / game.RefWidth,
				ScaleY:    float64(screen.Rows()) / game.RefHeight,
			}

			lr := game.NewLootRecognizer(cal, ts, log.Logger)
			
			start := time.Now()
			loot, err := lr.ReadAvailableLoot(screen)
			elapsed := time.Since(start)

			if err != nil {
				log.Error().Err(err).Msg("recognition error")
			} else {
				fmt.Printf("[%s] Gold: %-10d Elixir: %-10d DE: %-10d (%v)\n",
					time.Now().Format("15:04:05"),
					loot.Gold,
					loot.Elixir,
					loot.DarkElixir,
					elapsed.Truncate(time.Millisecond),
				)
			}

			lr.Close()
			screen.Close()
		}
	}
}
