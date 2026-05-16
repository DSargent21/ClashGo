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

	pts := []struct{x, y int; name string}{
		{64, 705, "Attack Button Center"},
		{35, 705, "Attack Button Left"},
		{255, 10, "Top Left Sky"},
		{560, 20, "Top Right Area"},
		{579, 458, "Gold Icon Candidate"},
		{712, 653, "Elixir Icon Candidate"},
	}

	fmt.Printf("🎨 Sampling colors in %dx%d screen...\n", screen.Cols(), screen.Rows())

	for _, p := range pts {
		if p.x < 0 || p.x >= screen.Cols() || p.y < 0 || p.y >= screen.Rows() {
			fmt.Printf("❌ %-25s: Out of bounds (%d, %d)\n", p.name, p.x, p.y)
			continue
		}
		b := screen.GetUCharAt(p.y, p.x*3)
		g := screen.GetUCharAt(p.y, p.x*3+1)
		r := screen.GetUCharAt(p.y, p.x*3+2)
		fmt.Printf("✅ %-25s: Pos(%d, %d) RGB(%d, %d, %d)\n", p.name, p.x, p.y, r, g, b)
	}
}
