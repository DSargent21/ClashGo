package main

import (
	"flag"
	"fmt"
	"image"
	"image/color"
	"os"
	"sort"

	"github.com/Ducky705/ClashGO/internal/adb"
	"github.com/Ducky705/ClashGO/internal/config"
	"github.com/Ducky705/ClashGO/internal/game"
	"github.com/Ducky705/ClashGO/internal/vision"
	"gocv.io/x/gocv"
)

func main() {
	tplName := flag.String("tpl", "btn_army_1", "Template name to match")
	minScale := flag.Float64("min", 0.5, "Min scale")
	maxScale := flag.Float64("max", 1.5, "Max scale")
	steps := flag.Int("steps", 10, "Scale steps")
	thresh := flag.Float64("thresh", 0.5, "Confidence threshold")
	flag.Parse()

	cfg := config.DefaultConfig()
	client := adb.NewClient(
		adb.WithHost(cfg.Device.ADBHost),
		adb.WithPort(cfg.Device.ADBPort),
	)
	client.DeviceID = cfg.Device.DeviceID

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

	tpl, ok := ts.Get(*tplName)
	if !ok {
		// Try loading as file if not in store
		if _, err := os.Stat(*tplName); err == nil {
			tpl = gocv.IMRead(*tplName, gocv.IMReadColor)
		} else {
			fmt.Printf("❌ Template %s not found\n", *tplName)
			os.Exit(1)
		}
	}
	defer tpl.Close()

	cal := game.NewCalibrator(client)
	c, _ := cal.Calibrate()

	fmt.Printf("🔍 Matching %s...\n", *tplName)
	matches, _ := vision.MatchMultiScale(screen, tpl, *minScale, *maxScale, *steps, float32(*thresh))
	
	sort.Slice(matches, func(i, j int) bool {
		return matches[i].Confidence > matches[j].Confidence
	})

	for i, m := range matches {
		if i >= 10 { break }
		refX, refY := c.Unscale(m.Point.X, m.Point.Y)
		fmt.Printf("[%d] Conf=%.4f at PHYS=(%d,%d) REF=(%d,%d) Scale=%.2f\n", 
			i, m.Confidence, m.Point.X, m.Point.Y, refX, refY, m.Scale)
		
		w := int(float64(tpl.Cols()) * m.Scale)
		h := int(float64(tpl.Rows()) * m.Scale)
		rect := image.Rect(m.Point.X - w/2, m.Point.Y - h/2, m.Point.X + w/2, m.Point.Y + h/2)
		gocv.Rectangle(&screen, rect, color.RGBA{0, 255, 0, 0}, 2)
		gocv.PutText(&screen, fmt.Sprintf("%.2f (%d,%d)", m.Confidence, refX, refY), 
			image.Pt(rect.Min.X, rect.Min.Y-5), gocv.FontHersheyPlain, 1.0, color.RGBA{0, 255, 0, 0}, 1)
	}

	out := "match_results.png"
	gocv.IMWrite(out, screen)
	fmt.Printf("💾 Saved top matches to %s\n", out)
}
