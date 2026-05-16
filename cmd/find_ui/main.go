package main

import (
	"fmt"
	"os"
	"sort"

	"github.com/Ducky705/ClashGo/internal/adb"
	"github.com/Ducky705/ClashGo/internal/config"
	"github.com/Ducky705/ClashGo/internal/game"
	"github.com/Ducky705/ClashGo/internal/vision"
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

	targets := []string{"btn_attack", "btn_battle", "btn_find_match", "icon_gold", "icon_elixir", "icon_de"}

	fmt.Printf("🔍 Searching UI elements in %dx%d screen...\n", screen.Cols(), screen.Rows())

	for _, name := range targets {
		tpl, ok := ts.Get(name)
		if !ok {
			fmt.Printf("⚠️  Template %s not found in store\n", name)
			continue
		}

		// Use wide multi-scale search to find where it is
		matches, err := vision.MatchMultiScale(screen, tpl, 0.2, 2.0, 50, 0.4)
		if err != nil || len(matches) == 0 {
			fmt.Printf("❌ %-15s: NOT FOUND\n", name)
			continue
		}

		sort.Slice(matches, func(i, j int) bool {
			return matches[i].Confidence > matches[j].Confidence
		})

		m := matches[0]
		// Calculate "reference" coordinates (scaled to 860x732)
		refX := int(float64(m.Point.X) / (float64(screen.Cols()) / 860.0))
		refY := int(float64(m.Point.Y) / (float64(screen.Rows()) / 732.0))

		status := "✅"
		if m.Confidence < 0.6 {
			status = "❓"
		}

		fmt.Printf("%s %-15s: Conf=%.3f  Phys=(%d, %d)  Ref=(%d, %d)  Scale=%.2f\n",
			status, name, m.Confidence, m.Point.X, m.Point.Y, refX, refY, m.Scale)
	}
}
