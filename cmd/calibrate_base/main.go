package main

import (
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"os"
	"sort"

	"gocv.io/x/gocv"
)

type BaseCalibration struct {
	BaseTop      image.Point `json:"base_top"`
	BaseRight    image.Point `json:"base_right"`
	BaseBottom   image.Point `json:"base_bottom"`
	BaseLeft     image.Point `json:"base_left"`
	FieldTop     image.Point `json:"field_top"`
	FieldRight   image.Point `json:"field_right"`
	FieldBottom  image.Point `json:"field_bottom"`
	FieldLeft    image.Point `json:"field_left"`
	BarY         int         `json:"bar_y"`
	Width        int         `json:"width"`
	Height       int         `json:"height"`
}

const Padding = 200

var (
	basePoints  []image.Point
	fieldPoints []image.Point
	barY        = -1
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

	canvasW := originalImg.Cols() + (Padding * 2)
	canvasH := originalImg.Rows() + Padding
	canvas := gocv.NewMatWithSize(canvasH, canvasW, gocv.MatTypeCV8UC3)
	defer canvas.Close()
	gocv.Rectangle(&canvas, image.Rect(0, 0, canvasW, canvasH), color.RGBA{50, 50, 50, 255}, -1)
	roi := image.Rect(Padding, Padding, Padding+originalImg.Cols(), Padding+originalImg.Rows())
	originalROI := canvas.Region(roi)
	originalImg.CopyTo(&originalROI)
	originalROI.Close()

	win := gocv.NewWindow("Double Diamond Calibrator")
	defer win.Close()

	fmt.Println("\n--- DOUBLE DIAMOND CALIBRATOR (9 CLICKS) ---")
	fmt.Println("1-4. Click the 4 corners of the BASE boundary (Turns BLUE)")
	fmt.Println("5-8. Click the 4 corners of the FIELD boundary (Turns YELLOW)")
	fmt.Println("9.   Click the TOP of the Hero Bar (Turns RED)")
	fmt.Println("\nControls: 'r' to reset, 's' to save, 'q' to quit.")

	win.SetMouseHandler(func(event int, x, y int, flags int, userdata interface{}) {
		if event == 1 { // LBUTTONDOWN
			if len(basePoints) < 4 {
				basePoints = append(basePoints, image.Pt(x, y))
				fmt.Printf("Base Point %d added.\n", len(basePoints))
			} else if len(fieldPoints) < 4 {
				fieldPoints = append(fieldPoints, image.Pt(x, y))
				fmt.Printf("Field Point %d added.\n", len(fieldPoints))
			} else if barY == -1 {
				barY = y
				fmt.Printf("Hero Bar Limit added: Y=%d\n", y)
			}
		}
	}, nil)

	for {
		display := canvas.Clone()
		
		for _, p := range basePoints { gocv.Circle(&display, p, 5, color.RGBA{255, 0, 0, 255}, -1) }
		if len(basePoints) == 4 {
			t, b, l, r := sortPoints(basePoints)
			drawDiamond(&display, t, r, b, l, color.RGBA{255, 0, 0, 255}, "BLUE (BASE)") // OpenCV is BGR, so 255,0,0 is Blue
		}

		for _, p := range fieldPoints { gocv.Circle(&display, p, 5, color.RGBA{0, 255, 255, 255}, -1) }
		if len(fieldPoints) == 4 {
			t, b, l, r := sortPoints(fieldPoints)
			drawDiamond(&display, t, r, b, l, color.RGBA{0, 255, 255, 255}, "YELLOW (FIELD)") // 0,255,255 is Yellow
		}

		if barY != -1 {
			gocv.Line(&display, image.Pt(0, barY), image.Pt(canvasW, barY), color.RGBA{0, 0, 255, 255}, 2) // 0,0,255 is Red
		}

		win.IMShow(display)
		key := win.WaitKey(10)
		display.Close()

		if key == 'q' {
			break
		} else if key == 'r' {
			basePoints, fieldPoints, barY = nil, nil, -1
		} else if key == 's' && len(basePoints) == 4 && len(fieldPoints) == 4 && barY != -1 {
			t1, b1, l1, r1 := sortPoints(basePoints)
			t2, b2, l2, r2 := sortPoints(fieldPoints)
			saveCalibration(t1, b1, l1, r1, t2, b2, l2, r2, barY-Padding, originalImg.Cols(), originalImg.Rows())
			break
		}
	}
}

func drawDiamond(img *gocv.Mat, t, r, b, l image.Point, c color.RGBA, label string) {
	gocv.Line(img, t, r, c, 2); gocv.Line(img, r, b, c, 2); gocv.Line(img, b, l, c, 2); gocv.Line(img, l, t, c, 2)
	gocv.PutText(img, label, t.Add(image.Pt(-20, -10)), gocv.FontHersheySimplex, 0.7, c, 2)
}

func sortPoints(pts []image.Point) (top, bottom, left, right image.Point) {
	tempY := make([]image.Point, 4); copy(tempY, pts)
	sort.Slice(tempY, func(i, j int) bool { return tempY[i].Y < tempY[j].Y })
	top, bottom = tempY[0], tempY[3]
	tempX := make([]image.Point, 4); copy(tempX, pts)
	sort.Slice(tempX, func(i, j int) bool { return tempX[i].X < tempX[j].X })
	left, right = tempX[0], tempX[3]
	return
}

func saveCalibration(t1, b1, l1, r1, t2, b2, l2, r2 image.Point, bY, w, h int) {
	cal := BaseCalibration{
		BaseTop: t1.Sub(image.Pt(Padding, Padding)), BaseBottom: b1.Sub(image.Pt(Padding, Padding)), 
		BaseLeft: l1.Sub(image.Pt(Padding, Padding)), BaseRight: r1.Sub(image.Pt(Padding, Padding)),
		FieldTop: t2.Sub(image.Pt(Padding, Padding)), FieldBottom: b2.Sub(image.Pt(Padding, Padding)), 
		FieldLeft: l2.Sub(image.Pt(Padding, Padding)), FieldRight: r2.Sub(image.Pt(Padding, Padding)),
		BarY: bY, Width: w, Height: h,
	}
	data, _ := json.MarshalIndent(cal, "", "  ")
	os.WriteFile("assets/base_calibration.json", data, 0644)
	fmt.Println("\n✅ Ultimate Calibration Saved (Base + Field + Bar).")
}
