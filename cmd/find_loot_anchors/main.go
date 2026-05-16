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

	if screen.Empty() {
		fmt.Println("❌ Capture is empty")
		return
	}

	ts, _ := game.NewTemplateStore("assets/templates")
	ts.LoadTemplates()

	anchors := []string{"icon_gold", "icon_elixir", "icon_de"}

	for _, name := range anchors {
		tpl, ok := ts.Get(name)
		if !ok {
			fmt.Printf("⚠️  Template %s not found\n", name)
			continue
		}

		fmt.Printf("🔍 Searching for anchor %s...\n", name)
		matches, err := vision.MatchMultiScale(screen, tpl, 0.2, 2.0, 50, 0.4)
		if err == nil && len(matches) > 0 {
			best := matches[0]
			fmt.Printf("✅ FOUND %s: Conf=%.4f at (%d, %d) Scale=%.2f\n", name, best.Confidence, best.Point.X, best.Point.Y, best.Scale)
		} else {
			fmt.Printf("❌ NOT FOUND: %s\n", name)
		}
	}
}
