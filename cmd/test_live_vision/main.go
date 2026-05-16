package main

import (
	"fmt"
	"log"
	"strings"

	"gocv.io/x/gocv"
	"github.com/Ducky705/ClashGo/internal/adb"
	"github.com/Ducky705/ClashGo/internal/vision"
)

func main() {
	// 1. Initialize ADB
	client := adb.NewClient(func(c *adb.Client) {
		c.DeviceID = "127.0.0.1:5555"
	})
	if err := client.Connect(); err != nil {
		log.Fatalf("ADB Connect: %v", err)
	}
	defer client.Close()

	// 2. Capture Live Screen
	fmt.Println("📸 Capturing live screen...")
	screen, err := client.CaptureToMat()
	if err != nil {
		log.Fatalf("Capture: %v", err)
	}
	defer screen.Close()

	// Save the screen for manual inspection
	gocv.IMWrite("assets/verify/live_capture_debug.png", screen)
	fmt.Println("✅ Live capture saved to assets/verify/live_capture_debug.png")

	// 3. Test every template in the attack folder
	units := []string{"Balloon", "Electro Dragon", "Archer Queen", "Grand Warden", "Minion Prince", "Rage Spell"}
	
	fmt.Println("\n🔍 Testing Template Matching (Confidence Threshold: 0.1 to see raw values):")
	
	for _, name := range units {
		fileName := strings.ToLower(strings.ReplaceAll(name, " ", "_"))
		tplPath := fmt.Sprintf("assets/templates/attack/%s.png", fileName)
		
		tpl := gocv.IMRead(tplPath, gocv.IMReadColor)
		if tpl.Empty() {
			fmt.Printf("❌ [%s] Template file not found: %s\n", name, tplPath)
			continue
		}
		defer tpl.Close()

		// Match with a very low threshold to see what the BEST match is
		matches, err := vision.MatchTemplate(screen, tpl, 0.1)
		if err != nil {
			fmt.Printf("❌ [%s] Error during matching: %v\n", name, err)
			continue
		}

		if len(matches) > 0 {
			best := matches[0]
			status := "❌ FAIL"
			if best.Confidence >= 0.7 {
				status = "✅ PASS"
			}
			fmt.Printf("%s [%s] Max Confidence: %.4f at (%d,%d)\n", status, name, best.Confidence, best.Point.X, best.Point.Y)
		} else {
			fmt.Printf("❌ [%s] No match found at all (even at 0.1 threshold)\n", name)
		}
	}
}
