package main

import (
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"os"
	"runtime"

	"github.com/Ducky705/ClashGO/internal/adb"
	"github.com/Ducky705/ClashGO/internal/config"
	"github.com/Ducky705/ClashGO/internal/game"
	"gocv.io/x/gocv"
)

type PrecisionConfig struct {
	Edges        map[string]ManualEdge   `json:"edges"`
	SpellEdgesA  map[string]ManualEdge   `json:"spell_edges_a"`
	SpellEdgesB  map[string]ManualEdge   `json:"spell_edges_b"`
	HeroTargets  map[string]image.Point  `json:"hero_targets"`
	BarY         int                    `json:"bar_y"`
	Width        int                    `json:"width"`
	Height       int                    `json:"height"`
}

type ManualEdge struct {
	P1 image.Point `json:"p1"`
	P2 image.Point `json:"p2"`
}

func main() {
	runtime.LockOSThread()

	cfg := config.DefaultConfig()
	client := adb.NewClient(
		adb.WithHost(cfg.Device.ADBHost),
		adb.WithPort(cfg.Device.ADBPort),
	)
	client.DeviceID = cfg.Device.DeviceID

	if err := client.Connect(); err != nil {
		fmt.Printf("ADB Error: %v\n", err)
		return
	}
	defer client.Close()

	calibrator := game.NewCalibrator(client)
	cal, err := calibrator.Calibrate()
	if err != nil {
		fmt.Printf("Calibration Error: %v\n", err)
		return
	}

	screen, err := client.CaptureToMat()
	if err != nil {
		fmt.Printf("Capture Error: %v\n", err)
		return
	}
	defer screen.Close()

	var pCfg PrecisionConfig
	pData, err := os.ReadFile("assets/precision_config.json")
	if err != nil {
		fmt.Println("Error: assets/precision_config.json not found. Run precision_setup first.")
		return
	}
	if err := json.Unmarshal(pData, &pCfg); err != nil {
		fmt.Printf("Error parsing config: %v\n", err)
		return
	}

	window := gocv.NewWindow("Valkyrie Attack Designer")
	defer window.Close()

	fmt.Println("\n--- VALKYRIE ATTACK DESIGNER ---")
	fmt.Println("Visualize Four-Side deployment and find REF coordinates.")
	fmt.Println("Controls: 's' to save screenshot, 'r' to refresh, 'q' to quit.")

	var lastX, lastY int
	var clicked bool

	window.SetMouseHandler(func(event int, x, y int, flags int, userdata interface{}) {
		if event == 1 { // Left Button Down
			lastX, lastY = x, y
			clicked = true
		}
	}, nil)

	for {
		display := screen.Clone()
		
		// 1. Draw Troop Lines (Green)
		for _, e := range pCfg.Edges {
			p1, p2 := scalePoint(e.P1, pCfg.Width, pCfg.Height, screen.Cols(), screen.Rows()), scalePoint(e.P2, pCfg.Width, pCfg.Height, screen.Cols(), screen.Rows())
			gocv.Line(&display, p1, p2, color.RGBA{0, 255, 0, 255}, 2)
		}
		
		// 2. Draw Spell B Lines (Purple - Inner)
		for _, e := range pCfg.SpellEdgesB {
			p1, p2 := scalePoint(e.P1, pCfg.Width, pCfg.Height, screen.Cols(), screen.Rows()), scalePoint(e.P2, pCfg.Width, pCfg.Height, screen.Cols(), screen.Rows())
			gocv.Line(&display, p1, p2, color.RGBA{255, 0, 255, 255}, 2)
		}

		if clicked {
			refX, refY := cal.Unscale(lastX, lastY)
			msg := fmt.Sprintf("REF: (%d, %d) | PHYS: (%d, %d)", refX, refY, lastX, lastY)
			
			// Draw crosshair
			gocv.Line(&display, image.Pt(0, lastY), image.Pt(screen.Cols(), lastY), color.RGBA{0, 255, 255, 255}, 1)
			gocv.Line(&display, image.Pt(lastX, 0), image.Pt(lastX, screen.Rows()), color.RGBA{0, 255, 255, 255}, 1)
			gocv.PutText(&display, msg, image.Pt(10, 30), gocv.FontHersheySimplex, 0.7, color.RGBA{0, 255, 255, 255}, 2)
		}

		window.IMShow(display)
		display.Close()

		key := window.WaitKey(10)
		if key == 'q' || key == 27 { break }
		if key == 's' {
			gocv.IMWrite("attack_designer_preview.png", screen)
			fmt.Println("Saved preview to attack_designer_preview.png")
		}
		if key == 'r' {
			newScreen, err := client.CaptureToMat()
			if err == nil {
				screen.Close()
				screen = newScreen
			}
		}
	}
}

func scalePoint(p image.Point, oldW, oldH, newW, newH int) image.Point {
	return image.Pt(
		int(float64(p.X)*float64(newW)/float64(oldW)),
		int(float64(p.Y)*float64(newH)/float64(oldH)),
	)
}
