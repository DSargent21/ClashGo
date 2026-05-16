package main

import (
	"fmt"
	"image"
	"image/color"
	"os"
	"sort"

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
	ts.LoadTemplates()

	tpl, _ := ts.Get("btn_attack")
	
	fmt.Println("🔍 Matching btn_attack...")
	matches, _ := vision.MatchMultiScale(screen, tpl, 0.2, 1.0, 40, 0.4)
	
	if len(matches) > 0 {
		sort.Slice(matches, func(i, j int) bool {
			return matches[i].Confidence > matches[j].Confidence
		})
		m := matches[0]
		fmt.Printf("Best match: Conf=%.4f at %v Scale=%.2f\n", m.Confidence, m.Point, m.Scale)
		
		// Draw on screen
		w := int(float64(tpl.Cols()) * m.Scale)
		h := int(float64(tpl.Rows()) * m.Scale)
		rect := image.Rect(m.Point.X - w/2, m.Point.Y - h/2, m.Point.X + w/2, m.Point.Y + h/2)
		gocv.Rectangle(&screen, rect, color.RGBA{255, 0, 0, 0}, 3)
		gocv.PutText(&screen, fmt.Sprintf("%.2f", m.Confidence), image.Pt(rect.Min.X, rect.Min.Y-10), gocv.FontHersheySimplex, 1.0, color.RGBA{255, 0, 0, 0}, 2)
	} else {
		fmt.Println("❌ No matches found")
	}

	gocv.IMWrite("match_debug.png", screen)
	fmt.Println("💾 Saved to match_debug.png")
}
