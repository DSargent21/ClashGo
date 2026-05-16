package main

import (
	"fmt"
	"os"
	"time"

	"github.com/Ducky705/ClashGo/internal/adb"
	"github.com/Ducky705/ClashGo/internal/config"
	"github.com/Ducky705/ClashGo/internal/game"
	"github.com/Ducky705/ClashGo/internal/vision"
	"gocv.io/x/gocv"
	"image"
	"github.com/rs/zerolog"
)

func main() {
	fmt.Println("💰 Starting Automated Loot Scout Simulation...")

	logger := zerolog.New(os.Stdout).With().Timestamp().Logger()
	cfg := config.LoadOrDefault("config.json")
	
	fmt.Printf("🎯 Target Requirements: Gold=%d, Elixir=%d, DE=%d\n", 
		cfg.Search.MinLootGold, cfg.Search.MinLootElixir, cfg.Search.MinLootDarkElixir)

	client := adb.NewClient(func(c *adb.Client) {
		c.DeviceID = cfg.Device.DeviceID
	})

	if err := client.Connect(); err != nil {
		fmt.Printf("❌ ADB Connection failed: %v\n", err)
		os.Exit(1)
	}

	cal, _ := game.NewCalibrator(client).Calibrate()
	ts, _ := game.NewTemplateStore("assets/templates")
	ts.LoadTemplates()

	lr := game.NewLootRecognizer(cal, ts, logger)
	
	for i := 1; ; i++ {
		fmt.Printf("\n--- Scouting Base #%d ---\n", i)
		
		screen, err := client.CaptureToMat()
		if err != nil {
			fmt.Printf("❌ Capture failed: %v, retrying...\n", err)
			time.Sleep(1 * time.Second)
			continue
		}

		resources, err := lr.ReadAvailableLoot(screen)
		if err != nil {
			fmt.Printf("❌ Failed to read loot: %v\n", err)
		} else {
			fmt.Printf("Gold:   %d\n", resources.Gold)
			fmt.Printf("Elixir: %d\n", resources.Elixir)
			fmt.Printf("DE:     %d\n", resources.DarkElixir)

			meetsReq := resources.Gold >= cfg.Search.MinLootGold &&
				resources.Elixir >= cfg.Search.MinLootElixir &&
				resources.DarkElixir >= cfg.Search.MinLootDarkElixir

			if meetsReq {
				fmt.Println("✅ REQUIREMENTS MET! Stopping scout.")
				screen.Close()
				break
			}
			fmt.Println("❌ Loot too low, looking for Next button...")
		}

		// Check for Next button (Silver/Grey or Yellow/Orange)
		// 1. Check for Silver/Grey (text/highlights)
		lowerSilver := gocv.NewScalar(160, 160, 160, 0)
		upperSilver := gocv.NewScalar(245, 245, 245, 0)
		
		maskSilver := gocv.NewMat()
		gocv.InRangeWithScalar(screen, lowerSilver, upperSilver, &maskSilver)

		// Focus on bottom-right ROI for Next button
		searchROI := image.Rect(
			int(600*cal.ScaleX), 
			int(450*cal.ScaleY), 
			int(860*cal.ScaleX), 
			int(732*cal.ScaleY),
		)
		
		subSilver := maskSilver.Region(searchROI)
		countSilver := gocv.CountNonZero(subSilver)
		
		subSilver.Close()
		maskSilver.Close()

		if countSilver > 500 {
			tx, ty := cal.ScaleRef(778, 574)
			fmt.Printf("🚀 Clicking Next Match at (%d, %d)...\n", tx, ty)
			client.Tap(tx, ty)
		} else {
			// Fallback: Template matching check
			fmt.Println("Searching for Next button via template matching...")
			tpl, _ := ts.Get("btn_next")
			if !tpl.Empty() {
				matches, _ := vision.MatchMultiScaleROI(screen, tpl, 0.5, 1.5, 10, 0.45, searchROI)
				if len(matches) > 0 {
					best := matches[0]
					fmt.Printf("✅ Found Next button via TEMPLATE. Clicking...\n")
					client.Tap(best.Point.X, best.Point.Y)
				} else {
					fmt.Println("⚠️ Next button NOT found. Waiting for clouds/loading...")
				}
			}
		}

		screen.Close()
		// Wait for next base to load (Clouds)
		time.Sleep(3 * time.Second)
	}
}
