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

	if screen.Empty() {
		fmt.Println("Empty screen")
		return
	}
	gocv.IMWrite("debug_next_sample.png", screen)

	x, y := 770, 560 // Fallback coords in bot.go
	if x < screen.Cols() && y < screen.Rows() {
		b := screen.GetUCharAt(y, x*3)
		g := screen.GetUCharAt(y, x*3+1)
		r := screen.GetUCharAt(y, x*3+2)
		fmt.Printf("Color at (770, 560): R=%d, G=%d, B=%d\n", r, g, b)
	}

	x2, y2 := 730, 615 // Pinpoint coords in bot.go
	if x2 < screen.Cols() && y2 < screen.Rows() {
		b := screen.GetUCharAt(y2, x2*3)
		g := screen.GetUCharAt(y2, x2*3+1)
		r := screen.GetUCharAt(y2, x2*3+2)
		fmt.Printf("Color at (730, 615): R=%d, G=%d, B=%d\n", r, g, b)
	}
}
