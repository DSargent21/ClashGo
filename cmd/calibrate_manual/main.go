package main

import (
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"os"

	"gocv.io/x/gocv"
)

type ManualEdge struct {
	P1 image.Point `json:"p1"`
	P2 image.Point `json:"p2"`
}

type ManualCalibration struct {
	TopRight    ManualEdge `json:"top_right"`
	BottomRight ManualEdge `json:"bottom_right"`
	BottomLeft  ManualEdge `json:"bottom_left"`
	TopLeft     ManualEdge `json:"top_left"`
	BarY        int        `json:"bar_y"`
	Width       int        `json:"width"`
	Height      int        `json:"height"`
}

const Padding = 100

var (
	points []image.Point
	labels = []string{
		"Top-Right START", "Top-Right END",
		"Bottom-Right START", "Bottom-Right END",
		"Bottom-Left START", "Bottom-Left END",
		"Top-Left START", "Top-Left END",
		"Hero Bar TOP",
	}
)

func main() {
	if len(os.Args) < 2 {
		fmt.Println("Usage: go run main.go <screenshot.png>")
		return
	}

	path := os.Args[1]
	originalImg := gocv.IMRead(path, gocv.IMReadColor)
	if originalImg.Empty() {
		fmt.Printf("Error: Could not read %s\n", path)
		return
	}
	defer originalImg.Close()

	win := gocv.NewWindow("Precision Edge Calibrator")
	defer win.Close()

	fmt.Println("\n--- PRECISION EDGE CALIBRATOR (9 CLICKS) ---")
	fmt.Println("Click the START and END of each deployment line in order:")
	for i, l := range labels {
		fmt.Printf("%d. %s\n", i+1, l)
	}
	fmt.Println("\nControls: 'r' to reset, 's' to save, 'q' to quit.")

	win.SetMouseHandler(func(event int, x, y int, flags int, userdata interface{}) {
		if event == 1 { // LBUTTONDOWN
			if len(points) < len(labels) {
				points = append(points, image.Pt(x, y))
				fmt.Printf("Point %d added: %s at %v\n", len(points), labels[len(points)-1], points[len(points)-1])
			}
		}
	}, nil)

	for {
		display := originalImg.Clone()
		
		// Draw points and lines
		for i, p := range points {
			gocv.Circle(&display, p, 5, color.RGBA{0, 255, 255, 255}, -1)
			gocv.PutText(&display, fmt.Sprintf("%d", i+1), p.Add(image.Pt(10, 10)), gocv.FontHersheySimplex, 0.5, color.RGBA{0, 255, 255, 255}, 1)
		}

		// Draw lines for completed pairs
		for i := 0; i < len(points)-1; i += 2 {
			if i+1 < len(points) && i < 8 {
				gocv.Line(&display, points[i], points[i+1], color.RGBA{0, 255, 0, 255}, 2)
			}
		}

		// Draw Hero Bar if clicked
		if len(points) == 9 {
			y := points[8].Y
			gocv.Line(&display, image.Pt(0, y), image.Pt(originalImg.Cols(), y), color.RGBA{0, 0, 255, 255}, 2)
		}

		// Show instructions on screen
		nextIdx := len(points)
		if nextIdx < len(labels) {
			gocv.PutText(&display, "NEXT: "+labels[nextIdx], image.Pt(20, 30), gocv.FontHersheySimplex, 0.8, color.RGBA{255, 255, 255, 255}, 2)
		} else {
			gocv.PutText(&display, "ALL POINTS SET! Press 's' to save.", image.Pt(20, 30), gocv.FontHersheySimplex, 0.8, color.RGBA{0, 255, 0, 255}, 2)
		}

		win.IMShow(display)
		key := win.WaitKey(10)
		display.Close()

		if key == 'q' {
			break
		} else if key == 'r' {
			points = nil
		} else if key == 's' && len(points) == 9 {
			saveCalibration(points, originalImg.Cols(), originalImg.Rows())
			break
		}
	}
}

func saveCalibration(pts []image.Point, w, h int) {
	cal := ManualCalibration{
		TopRight:    ManualEdge{P1: pts[0], P2: pts[1]},
		BottomRight: ManualEdge{P1: pts[2], P2: pts[3]},
		BottomLeft:  ManualEdge{P1: pts[4], P2: pts[5]},
		TopLeft:     ManualEdge{P1: pts[6], P2: pts[7]},
		BarY:        pts[8].Y,
		Width:       w,
		Height:      h,
	}
	data, _ := json.MarshalIndent(cal, "", "  ")
	os.WriteFile("assets/manual_calibration.json", data, 0644)
	fmt.Println("\n✅ Precision Manual Calibration Saved to assets/manual_calibration.json")
}
