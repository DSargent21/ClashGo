package main

import (
	"fmt"
	"os"

	"github.com/Ducky705/ClashGo/internal/adb"
	"github.com/Ducky705/ClashGo/internal/config"
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

	// Scan around (64, 704)
	centerX, centerY := 64, 704
	radius := 40
	
	fmt.Printf("🔍 Scanning 80x80 area around (%d, %d)...\n", centerX, centerY)

	bestR, bestG, bestB := 0, 0, 0
	bestX, bestY := 0, 0
	maxOrangeScore := -1.0

	for y := centerY - radius; y <= centerY+radius; y++ {
		for x := centerX - radius; x <= centerX+radius; x++ {
			if x < 0 || x >= screen.Cols() || y < 0 || y >= screen.Rows() {
				continue
			}
			b := int(screen.GetUCharAt(y, x*3))
			g := int(screen.GetUCharAt(y, x*3+1))
			r := int(screen.GetUCharAt(y, x*3+2))

			// Orange score: high R, medium G, low B
			score := float64(r) - float64(b) - float64(absDiff(g, 150))
			if score > maxOrangeScore {
				maxOrangeScore = score
				bestR, bestG, bestB = r, g, b
				bestX, bestY = x, y
			}
		}
	}

	fmt.Printf("Best Orange Pixel: Pos(%d, %d) RGB(%d, %d, %d) Score=%.1f\n", 
		bestX, bestY, bestR, bestG, bestB, maxOrangeScore)
}

func absDiff(a, b int) int {
	if a > b { return a - b }
	return b - a
}
