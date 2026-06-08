package main

import (
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"os"

	"gocv.io/x/gocv"
)

type ManualSlotConfig struct {
	CardWidth  int   `json:"card_width"`
	CardHeight int   `json:"card_height"`
	SlotXs     []int `json:"slot_xs"`
	SlotY      int   `json:"slot_y"`
}

func main() {
	if len(os.Args) < 2 {
		fmt.Println("Usage: go run scripts/calibrate_slots_manual.go <attack_screenshot.png>")
		return
	}

	path := os.Args[1]
	img := gocv.IMRead(path, gocv.IMReadColor)
	if img.Empty() {
		fmt.Printf("Error: Could not read %s\n", path)
		return
	}
	defer img.Close()

	win := gocv.NewWindow("MANUAL SLOT CALIBRATION")
	defer win.Close()

	fmt.Println("\n--- ULTIMATE MANUAL SLOT CALIBRATION ---")
	fmt.Println("1. CLICK TOP-LEFT of the FIRST card.")
	fmt.Println("2. CLICK BOTTOM-RIGHT of the FIRST card.")
	fmt.Println("3. CLICK THE CENTER OF EVERY OTHER CARD IN ORDER (Left to Right).")
	fmt.Println("\nControls: 'u' to undo, 'r' to reset, 's' to save, 'q' to quit.")

	var points []image.Point
	var conf ManualSlotConfig

	win.SetMouseHandler(func(event int, x, y int, flags int, userdata interface{}) {
		if event == 1 { // LBUTTONDOWN
			points = append(points, image.Pt(x, y))
			step := len(points)
			
			if step == 1 {
				fmt.Printf("✓ Top-Left set: %v\n", points[0])
			} else if step == 2 {
				conf.CardWidth = points[1].X - points[0].X
				conf.CardHeight = points[1].Y - points[0].Y
				conf.SlotY = points[0].Y + conf.CardHeight/2
				firstX := points[0].X + conf.CardWidth/2
				conf.SlotXs = append(conf.SlotXs, firstX)
				fmt.Printf("✓ Card Geometry set: %dx%d\n", conf.CardWidth, conf.CardHeight)
				fmt.Printf("✓ First Slot Center: %d\n", firstX)
			} else {
				conf.SlotXs = append(conf.SlotXs, x)
				fmt.Printf("✓ Added Slot %d at X=%d\n", len(conf.SlotXs), x)
			}
		}
	}, nil)

	for {
		display := img.Clone()
		
		msg := ""
		if len(points) == 0 {
			msg = "CLICK TOP-LEFT OF FIRST CARD"
		} else if len(points) == 1 {
			msg = "CLICK BOTTOM-RIGHT OF FIRST CARD"
		} else {
			msg = fmt.Sprintf("CLICK CENTER OF SLOT %d (or 'S' to Save)", len(conf.SlotXs)+1)
		}
		
		gocv.PutText(&display, msg, image.Pt(20, 40), gocv.FontHersheySimplex, 0.7, color.RGBA{0, 255, 255, 255}, 2)

		// Draw detected cards
		for i, x := range conf.SlotXs {
			rect := image.Rect(x-conf.CardWidth/2, conf.SlotY-conf.CardHeight/2, x+conf.CardWidth/2, conf.SlotY+conf.CardHeight/2)
			gocv.Rectangle(&display, rect, color.RGBA{0, 255, 0, 255}, 2)
			gocv.Circle(&display, image.Pt(x, conf.SlotY), 3, color.RGBA{0, 255, 0, 255}, -1)
			gocv.PutText(&display, fmt.Sprintf("%d", i+1), image.Pt(x-5, conf.SlotY-10), gocv.FontHersheySimplex, 0.5, color.RGBA{255, 255, 255, 255}, 1)
		}

		win.IMShow(display)
		key := win.WaitKey(10)
		display.Close()

		if key == 'q' || key == 27 {
			break
		} else if key == 'r' {
			points = nil
			conf = ManualSlotConfig{}
		} else if key == 'u' && len(points) > 0 {
			points = points[:len(points)-1]
			if len(conf.SlotXs) > 0 {
				conf.SlotXs = conf.SlotXs[:len(conf.SlotXs)-1]
			}
			fmt.Println("↶ Undid last click")
		} else if key == 's' && len(conf.SlotXs) > 0 {
			data, _ := json.MarshalIndent(conf, "", "  ")
			os.WriteFile("assets/manual_slots.json", data, 0644)
			fmt.Println("\n✅ SAVED assets/manual_slots.json")
			break
		}
	}
}
