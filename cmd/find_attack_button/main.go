package main

import (
	"fmt"
	"os"

	"github.com/Ducky705/ClashGo/internal/adb"
	"github.com/Ducky705/ClashGo/internal/config"
	"github.com/Ducky705/ClashGo/internal/game"
	"github.com/Ducky705/ClashGo/internal/vision"
	"gocv.io/x/gocv"
)

func main() {
	cfg := config.LoadOrDefault("config.json")
	client := adb.NewClient(func(c *adb.Client) {
		c.DeviceID = cfg.Device.DeviceID
	})
	if err := client.Connect(); err != nil {
		fmt.Printf("❌ Connection failed: %v\n", err)
		os.Exit(1)
	}

	screen, err := client.CaptureToMat()
	if err != nil {
		fmt.Printf("❌ Capture failed: %v\n", err)
		os.Exit(1)
	}
	defer screen.Close()

	ts, _ := game.NewTemplateStore("assets/templates")
	if err := ts.LoadTemplates(); err != nil {
		fmt.Printf("❌ Failed to load templates: %v\n", err)
		os.Exit(1)
	}
	tpl, ok := ts.Get("btn_attack")
	if !ok {
		fmt.Println("❌ Template btn_attack not found")
		os.Exit(1)
	}

	fmt.Printf("🔍 Searching for 'Attack' button in %dx%d screen...\n", screen.Cols(), screen.Rows())

	// Exhaustive search
	bestConf := -1.0
	var bestMatch vision.Match

	for scale := 0.3; scale <= 2.0; scale += 0.05 {
		matches, err := vision.MatchMultiScale(screen, tpl, scale, scale, 1, 0.1)
		if err == nil && len(matches) > 0 {
			if matches[0].Confidence > bestConf {
				bestConf = matches[0].Confidence
				bestMatch = matches[0]
			}
		}
	}

	if bestConf > 0.1 {
		fmt.Printf("Best match: Conf=%.4f at Phys=(%d, %d) Scale=%.2f\n", 
			bestConf, bestMatch.Point.X, bestMatch.Point.Y, bestMatch.Scale)
		
		if bestConf < 0.5 {
			fmt.Println("⚠️  Match confidence is very low. Button might be hidden, different, or resolution is too low.")
		}
	} else {
		fmt.Println("❌ No match found even at low confidence.")
	}
	
	// Save the capture for manual verification if possible
	gocv.IMWrite("last_capture.png", screen)
	fmt.Println("💾 Saved current screen to last_capture.png")
}
