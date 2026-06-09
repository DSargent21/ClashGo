package main

import (
	"fmt"
	"image"
	"image/color"
	"os"

	"github.com/Ducky705/ClashGO/internal/game"
	"github.com/rs/zerolog"
	"gocv.io/x/gocv"
)

type MockExecutor struct {
	logger zerolog.Logger
	cal    *game.Calibration
}

func (e *MockExecutor) isSlotEmpty(screen gocv.Mat, x, y int) (bool, float64) {
	if screen.Empty() || x < 0 || y < 0 || x >= screen.Cols() || y >= screen.Rows() {
		return true, 0
	}

	size := int(25.0 * e.cal.ScaleX)
	region := image.Rect(x-size, y-size, x+size, y+size)
	if region.Min.X < 0 { region.Min.X = 0 }
	if region.Min.Y < 0 { region.Min.Y = 0 }
	if region.Max.X > screen.Cols() { region.Max.X = screen.Cols() }
	if region.Max.Y > screen.Rows() { region.Max.Y = screen.Rows() }
	sub := screen.Region(region)
	defer sub.Close()

	hsv := gocv.NewMat()
	defer hsv.Close()
	gocv.CvtColor(sub, &hsv, gocv.ColorBGRToHSV)

	activePixels := 0
	total := hsv.Rows() * hsv.Cols()

	for row := 0; row < hsv.Rows(); row++ {
		for col := 0; col < hsv.Cols(); col++ {
			hu := hsv.GetUCharAt(row, col*3)
			sa := hsv.GetUCharAt(row, col*3+1)
			va := hsv.GetUCharAt(row, col*3+2)

			isMap := (hu >= 35 && hu <= 90 && sa > 30) || (hu < 30 && sa < 50 && va < 80)
			if !isMap {
				if sa > 55 && va > 90 {
					activePixels++
				} else if sa < 30 && va > 220 {
					activePixels++
				}
			}
		}
	}

	activeRatio := float64(activePixels) / float64(total)
	isEmpty := activeRatio < 0.08
	return isEmpty, activeRatio
}

func main() {
	logger := zerolog.New(zerolog.ConsoleWriter{Out: os.Stderr}).With().Timestamp().Logger()
	
	if len(os.Args) < 2 {
		fmt.Println("Usage: go run debug_attack_vision.go <screenshot.png>")
		return
	}

	imgPath := os.Args[1]
	img := gocv.IMRead(imgPath, gocv.IMReadColor)
	if img.Empty() {
		fmt.Printf("Error: could not read image %s\n", imgPath)
		return
	}
	defer img.Close()

	cal := &game.Calibration{ScaleX: float64(img.Cols()) / 860.0, ScaleY: float64(img.Rows()) / 732.0}
	exec := &MockExecutor{logger: logger, cal: cal}

	mBarY := int(float64(img.Rows()) * 0.85)
	slotY := mBarY + (img.Rows()-mBarY)/2
	
	fmt.Printf("Testing Image: %s (%dx%d)\n", imgPath, img.Cols(), img.Rows())

	for x := 50; x < img.Cols()-50; x += 75 {
		sx := int(float64(x) * cal.ScaleX)
		empty, ratio := exec.isSlotEmpty(img, sx, slotY)
		status := "ACTIVE"
		if empty { status = "EMPTY" }
		fmt.Printf("X: %3d | Ratio: %.4f | Status: %s\n", sx, ratio, status)
		
		c := color.RGBA{0, 255, 0, 255}
		if empty { c = color.RGBA{255, 0, 0, 255} }
		gocv.Circle(&img, image.Pt(sx, slotY), 10, c, 2)
	}

	outPath := "debug_vision_result.png"
	gocv.IMWrite(outPath, img)
	fmt.Printf("Results saved to %s\n", outPath)
}
