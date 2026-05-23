package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/Ducky705/ClashGo/internal/adb"
	"github.com/Ducky705/ClashGo/internal/game"
	"github.com/Ducky705/ClashGo/internal/vision"
	"github.com/rs/zerolog"
	"gocv.io/x/gocv"
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
	filePath := flag.String("file", "", "Path to image file to test instead of live ADB")
	flag.Parse()

	logger := zerolog.New(os.Stdout).With().Timestamp().Logger()
	logger.Info().Msg("🚀 Starting End-Battle Detection Test")

	var img gocv.Mat
	if *filePath != "" {
		fmt.Printf("📂 Loading image: %s\n", *filePath)
		img = vision.LoadImage(*filePath)
	} else {
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
		var err error
		img, err = client.CaptureToMat()
		if err != nil {
			fmt.Printf("❌ Capture Error: %v\n", err)
			return
		}
		
		// Save captured screen for manual inspection
		vision.SaveImage(img, "battle_end_capture.png")
		fmt.Println("💾 Saved screen to battle_end_capture.png")
	}
	defer img.Close()

	if img.Empty() {
		fmt.Println("❌ Error: Image is empty")
		return
	}

	// 3. Setup Calibration & Templates
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
