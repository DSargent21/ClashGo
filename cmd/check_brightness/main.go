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

	mean := screen.Mean()
	fmt.Printf("Screen Average Color: BGR(%.1f, %.1f, %.1f)\n", mean.Val1, mean.Val2, mean.Val3)
	
	gray := gocv.NewMat()
	defer gray.Close()
	gocv.CvtColor(screen, &gray, gocv.ColorBGRToGray)
	
	_, maxVal, _, _ := gocv.MinMaxLoc(gray)
	fmt.Printf("Max Brightness: %.1f\n", maxVal)
}
