package main

import (
	"fmt"
	"os"

	"github.com/Ducky705/ClashGo/internal/adb"
	"github.com/Ducky705/ClashGo/internal/config"
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

	// Find orange pixels (Attack button color: 255, 175, 0)
	// We'll use a range
	lower := gocv.NewScalar(0, 100, 150, 0) // BGR
	upper := gocv.NewScalar(100, 200, 255, 0)

	mask := gocv.NewMat()
	defer mask.Close()
	gocv.InRangeWithScalar(screen, lower, upper, &mask)

	count := gocv.CountNonZero(mask)
	fmt.Printf("Found %d orange-ish pixels\n", count)

	if count > 0 {
		// Find centroid of orange pixels
		var xSum, ySum int64
		pts := 0
		for y := 0; y < mask.Rows(); y++ {
			for x := 0; x < mask.Cols(); x++ {
				if mask.GetUCharAt(y, x) > 0 {
					xSum += int64(x)
					ySum += int64(y)
					pts++
					if pts > 10000 { break } // Limit
				}
			}
			if pts > 10000 { break }
		}
		if pts > 0 {
			fmt.Printf("Centroid of first 10k orange pixels: (%d, %d)\n", xSum/int64(pts), ySum/int64(pts))
		}
	}

	// Also find white pixels (Text/Icons)
	lowerWhite := gocv.NewScalar(200, 200, 200, 0)
	upperWhite := gocv.NewScalar(255, 255, 255, 0)
	gocv.InRangeWithScalar(screen, lowerWhite, upperWhite, &mask)
	fmt.Printf("Found %d white-ish pixels\n", gocv.CountNonZero(mask))
}
