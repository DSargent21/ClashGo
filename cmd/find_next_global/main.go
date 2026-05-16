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
	gocv.IMWrite("debug_search_screen.png", screen)

	ts, _ := game.NewTemplateStore("assets/templates")
	ts.LoadTemplates()
	tpl, _ := ts.Get("btn_next")

	fmt.Printf("🔍 Searching for 'btn_next' across the WHOLE screen (%dx%d)...\n", screen.Cols(), screen.Rows())

	matches, err := vision.MatchMultiScale(screen, tpl, 0.2, 2.0, 60, 0.1)
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		return
	}

	if len(matches) > 0 {
		for i, m := range matches {
			fmt.Printf("Match %d: Conf=%.4f at (%d, %d) Scale=%.2f\n", i, m.Confidence, m.Point.X, m.Point.Y, m.Scale)
		}
	} else {
		fmt.Println("❌ No matches found for btn_next anywhere.")
	}
}
