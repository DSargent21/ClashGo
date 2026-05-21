package main

import (
	"fmt"
	"os"

	"github.com/Ducky705/ClashGo/internal/adb"
	"github.com/Ducky705/ClashGo/internal/game"
	"github.com/rs/zerolog"
)

type adbLogAdapter struct {
	log zerolog.Logger
}

func (a *adbLogAdapter) Debug() bool { return a.log.GetLevel() <= zerolog.DebugLevel }
func (a *adbLogAdapter) Debugf(format string, v ...any) {
	a.log.Debug().Msgf(format, v...)
}
func (a *adbLogAdapter) Info(msg string)  { a.log.Info().Msg(msg) }
func (a *adbLogAdapter) Warn(msg string)  { a.log.Warn().Msg(msg) }
func (a *adbLogAdapter) Error(msg string) { a.log.Error().Msg(msg) }
func (a *adbLogAdapter) WithFields(fields map[string]any) adb.Logger {
	return &adbLogAdapter{log: a.log.With().Fields(fields).Logger()}
}

func main() {
	logger := zerolog.New(os.Stdout).With().Timestamp().Logger()
	logger.Info().Msg("🚀 Starting Live End-Battle Detection Test")

	// 1. Connect to ADB
	client := adb.NewClient(
		adb.WithHost("127.0.0.1"),
		adb.WithPort(5037),
		adb.WithDeviceID("emulator-5554"),
		adb.WithLogger(&adbLogAdapter{log: logger}),
	)
	if err := client.Connect(); err != nil {
		fmt.Printf("❌ ADB Connection Error: %v\n", err)
		return
	}

	// 2. Capture Current Screen
	fmt.Println("📸 Capturing screen...")
	img, err := client.CaptureToMat()
	if err != nil {
		fmt.Printf("❌ Capture Error: %v\n", err)
		return
	}
	defer img.Close()

	// 3. Setup Calibration & Templates
	// We use 860x732 as the reference resolution for the internal logic
	cal := &game.Calibration{
		ScaleX: float64(img.Cols()) / 860.0,
		ScaleY: float64(img.Rows()) / 732.0,
	}
	ts, err := game.NewTemplateStore("assets/templates")
	if err != nil {
		fmt.Printf("❌ Template Store Error: %v\n", err)
		return
	}
	if err := ts.LoadTemplates(); err != nil {
		fmt.Printf("❌ Load Templates Error: %v\n", err)
		return
	}

	// 4. Run Detection
	lr := game.NewLootRecognizer(cal, ts, logger)
	defer lr.Close()

	fmt.Println("\n🔍 Analyzing Current Screen...")
	result, err := lr.ReadBattleResult(img)
	if err != nil {
		fmt.Printf("❌ Error: %v\n", err)
		return
	}

	// 5. Output Results
	fmt.Println("\n" + "========================================")
	fmt.Printf("⭐ STARS DETECTED: %d\n", result.Stars)
	fmt.Println("----------------------------------------")
	fmt.Println("💰 BATTLE LOOT:")
	fmt.Printf("   Gold:   %d\n", result.Loot.Gold)
	fmt.Printf("   Elixir: %d\n", result.Loot.Elixir)
	fmt.Printf("   DE:     %d\n", result.Loot.DarkElixir)
	fmt.Println("----------------------------------------")
	fmt.Println("🎁 BONUS LOOT:")
	fmt.Printf("   Gold:   %d\n", result.Bonus.Gold)
	fmt.Printf("   Elixir: %d\n", result.Bonus.Elixir)
	fmt.Printf("   DE:     %d\n", result.Bonus.DarkElixir)
	fmt.Println("========================================")
	
	totalGold := result.Loot.Gold + result.Bonus.Gold
	totalElixir := result.Loot.Elixir + result.Bonus.Elixir
	totalDE := result.Loot.DarkElixir + result.Bonus.DarkElixir
	fmt.Printf("💎 GRAND TOTAL: G:%d E:%d DE:%d\n", totalGold, totalElixir, totalDE)
}
