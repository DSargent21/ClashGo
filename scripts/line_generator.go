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

	fmt.Println("Capturing screen...")
	screen, err := client.CaptureToMat()
	if err != nil {
		fmt.Printf("Capture Error: %v\n", err)
		return
	}
	defer screen.Close()

	var pCfg PrecisionConfig
	pData, err := os.ReadFile("assets/precision_config.json")
	if err == nil {
		json.Unmarshal(pData, &pCfg)
	} else {
		pCfg = PrecisionConfig{
			Edges:       make(map[string]ManualEdge),
			SpellEdgesA: make(map[string]ManualEdge),
			SpellEdgesB: make(map[string]ManualEdge),
			HeroTargets: make(map[string]image.Point),
			Width:       screen.Cols(),
			Height:      screen.Rows(),
		}
	}

	window := gocv.NewWindow("LINE GENERATOR")
	defer window.Close()

	fmt.Println("\n--- LINE GENERATOR ---")
	fmt.Println("1. Click START of line")
	fmt.Println("2. Click END of line")
	fmt.Println("3. Press 't' (Troop), 'a' (Spell A), or 'b' (Spell B) to assign to current edge")
	fmt.Println("4. Press '1'-'4' to switch Edge: 1:TR, 2:BR, 3:BL, 4:TL")
	fmt.Println("\nControls: 's' to save, 'u' to undo, 'q' to quit.")

	edgeNames := []string{"TopRight", "BottomRight", "BottomLeft", "TopLeft"}
	currentEdgeIdx := 0
	
	var points []image.Point

	window.SetMouseHandler(func(event int, x, y int, flags int, userdata interface{}) {
		if event == 1 { // LBUTTONDOWN
			refX, refY := cal.Unscale(x, y)
			refP := image.Pt(refX, refY)
			points = append(points, refP)
			fmt.Printf("Click at REF: %v\n", refP)
			if len(points) > 2 {
				points = points[len(points)-2:] // Keep last 2
			}
		}
	}, nil)

	for {
		display := screen.Clone()
		name := edgeNames[currentEdgeIdx]

		// Draw Existing Lines
		drawLines(&display, pCfg.Edges, color.RGBA{0, 255, 0, 255}, cal)
		drawLines(&display, pCfg.SpellEdgesA, color.RGBA{255, 0, 255, 255}, cal)
		drawLines(&display, pCfg.SpellEdgesB, color.RGBA{200, 0, 200, 255}, cal)

		// Draw Pending Line
		if len(points) == 1 {
			p1X, p1Y := cal.ScaleRef(points[0].X, points[0].Y)
			gocv.Circle(&display, image.Pt(p1X, p1Y), 5, color.RGBA{255, 255, 0, 255}, -1)
		} else if len(points) == 2 {
			p1X, p1Y := cal.ScaleRef(points[0].X, points[0].Y)
			p2X, p2Y := cal.ScaleRef(points[1].X, points[1].Y)
			gocv.Line(&display, image.Pt(p1X, p1Y), image.Pt(p2X, p2Y), color.RGBA{255, 255, 0, 255}, 2)
		}

		msg := fmt.Sprintf("EDGE: %s | CLICKS: %d", name, len(points))
		gocv.PutText(&display, msg, image.Pt(20, 40), gocv.FontHersheySimplex, 0.8, color.RGBA{0, 255, 255, 255}, 2)

		window.IMShow(display)
		key := window.WaitKey(10)
		display.Close()

		if key == 'q' || key == 27 { break }
		if key >= '1' && key <= '4' {
			currentEdgeIdx = int(key - '1')
			fmt.Printf("Switched to %s\n", edgeNames[currentEdgeIdx])
		}
		if key == 'u' && len(points) > 0 {
			points = points[:len(points)-1]
		}
		if len(points) == 2 {
			edge := ManualEdge{P1: points[0], P2: points[1]}
			if key == 't' {
				pCfg.Edges[name] = edge
				fmt.Printf("Assigned TROOP line to %s\n", name)
				points = nil
			} else if key == 'a' {
				pCfg.SpellEdgesA[name] = edge
				fmt.Printf("Assigned SPELL A line to %s\n", name)
				points = nil
			} else if key == 'b' {
				pCfg.SpellEdgesB[name] = edge
				fmt.Printf("Assigned SPELL B line to %s\n", name)
				points = nil
			}
		}
		if key == 's' {
			pCfg.Width = 860   // Ref Width
			pCfg.Height = 732  // Ref Height
			data, _ := json.MarshalIndent(pCfg, "", "  ")
			os.WriteFile("assets/precision_config.json", data, 0644)
			fmt.Println("\n✅ SAVED assets/precision_config.json")
		}
	}
}

func drawLines(img *gocv.Mat, edges map[string]ManualEdge, c color.RGBA, cal *game.Calibration) {
	for _, e := range edges {
		p1X, p1Y := cal.ScaleRef(e.P1.X, e.P1.Y)
		p2X, p2Y := cal.ScaleRef(e.P2.X, e.P2.Y)
		gocv.Line(img, image.Pt(p1X, p1Y), image.Pt(p2X, p2Y), c, 2)
	}
}
