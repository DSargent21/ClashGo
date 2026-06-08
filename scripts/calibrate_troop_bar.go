package main

import (
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"os"

	"gocv.io/x/gocv"
)

type BarConfig struct {
	BarY        int `json:"bar_y"`
	CardWidth   int `json:"card_width"`
	CardHeight  int `json:"card_height"`
	CardSpacing int `json:"card_spacing"`
	FirstCardX  int `json:"first_card_x"`
	FirstCardY  int `json:"first_card_y"`
}

func main() {
	if len(os.Args) < 2 {
		fmt.Println("Usage: go run scripts/calibrate_troop_bar.go <attack_screenshot.png>")
		return
	}

	path := os.Args[1]
	img := gocv.IMRead(path, gocv.IMReadColor)
	if img.Empty() {
		fmt.Printf("Error: Could not read %s\n", path)
		return
	}
	defer img.Close()

	win := gocv.NewWindow("Troop Bar Calibration")
	defer win.Close()

	fmt.Println("\n--- TROOP BAR CALIBRATION ---")
	fmt.Println("1. CLICK TOP-LEFT of the FIRST troop card.")
	fmt.Println("2. CLICK BOTTOM-RIGHT of the FIRST troop card.")
	fmt.Println("3. CLICK CENTER of the SECOND troop card.")
	fmt.Println("\nControls: 'r' to reset, 's' to save, 'q' to quit.")

	var points []image.Point
	var conf BarConfig

	win.SetMouseHandler(func(event int, x, y int, flags int, userdata interface{}) {
		if event == 1 { // LBUTTONDOWN
			points = append(points, image.Pt(x, y))
			step := len(points)
			
			switch step {
			case 1:
				fmt.Printf("✓ Top-Left set: %v\n", points[0])
			case 2:
				fmt.Printf("✓ Bottom-Right set: %v\n", points[1])
				conf.CardWidth = points[1].X - points[0].X
				conf.CardHeight = points[1].Y - points[0].Y
				conf.FirstCardX = points[0].X + conf.CardWidth/2
				conf.FirstCardY = points[0].Y + conf.CardHeight/2
				conf.BarY = points[0].Y
			case 3:
				fmt.Printf("✓ Second Center set: %v\n", points[2])
				conf.CardSpacing = points[2].X - conf.FirstCardX
				fmt.Println("\nCALIBRATION READY! Press 's' to save or 'r' to reset.")
			}
		}
	}, nil)

	for {
		display := img.Clone()
		
		msg := ""
		switch len(points) {
		case 0: msg = "CLICK TOP-LEFT OF FIRST CARD"
		case 1: msg = "CLICK BOTTOM-RIGHT OF FIRST CARD"
		case 2: msg = "CLICK CENTER OF SECOND CARD"
		default: msg = "PRESS 'S' TO SAVE"
		}
		
		gocv.PutText(&display, msg, image.Pt(20, 40), gocv.FontHersheySimplex, 0.7, color.RGBA{0, 255, 255, 255}, 2)

		// Draw points
		for _, p := range points {
			gocv.Circle(&display, p, 5, color.RGBA{255, 0, 0, 255}, -1)
		}

		// Draw preview grid
		if len(points) >= 3 {
			for i := 0; i < 15; i++ {
				centerX := conf.FirstCardX + (i * conf.CardSpacing)
				if centerX > img.Cols() { break }
				
				rect := image.Rect(
					centerX - conf.CardWidth/2,
					conf.FirstCardY - conf.CardHeight/2,
					centerX + conf.CardWidth/2,
					conf.FirstCardY + conf.CardHeight/2,
				)
				gocv.Rectangle(&display, rect, color.RGBA{0, 255, 0, 255}, 2)
				gocv.Circle(&display, image.Pt(centerX, conf.FirstCardY), 3, color.RGBA{0, 255, 0, 255}, -1)
			}
		}

		win.IMShow(display)
		key := win.WaitKey(10)
		display.Close()

		if key == 'q' || key == 27 {
			break
		} else if key == 'r' {
			points = nil
			conf = BarConfig{}
		} else if key == 's' && len(points) >= 3 {
			data, _ := json.MarshalIndent(conf, "", "  ")
			os.WriteFile("assets/troop_bar_config.json", data, 0644)
			fmt.Println("\n✅ SAVED assets/troop_bar_config.json")
			break
		}
	}
}
