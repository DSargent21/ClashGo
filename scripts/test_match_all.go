package main

import (
	"fmt"
	"image"
	"path/filepath"
	"strings"

	"github.com/Ducky705/ClashGo/internal/vision"
	"gocv.io/x/gocv"
)

func main() {
	files, err := filepath.Glob("screen_20260515_*.png")
	if err != nil || len(files) == 0 {
		fmt.Println("No screen screenshots found")
		return
	}

	for _, fPath := range files {
		fmt.Printf("\n================ TESTING SCREEN: %s ================\n", fPath)
		screen := gocv.IMRead(fPath, gocv.IMReadColor)
		if screen.Empty() {
			fmt.Println("Failed to read", fPath)
			continue
		}
		defer screen.Close()

		w, h := screen.Cols(), screen.Rows()
		mBarY := int(float64(h) * 0.78)
		barROI := image.Rect(0, mBarY, w, h)

		templates, _ := filepath.Glob("assets/templates/attack/*.png")
		for _, tPath := range templates {
			tpl := gocv.IMRead(tPath, gocv.IMReadColor)
			if tpl.Empty() {
				continue
			}
			defer tpl.Close()

			name := filepath.Base(tPath)
			name = strings.TrimSuffix(name, ".png")

			// Search for matches in ROI
			matches, err := vision.MatchMultiScaleROI(screen, tpl, 0.2, 1.2, 20, 0.5, barROI)
			if err != nil {
				fmt.Printf("Unit: %-20s Match Error: %v\n", name, err)
				continue
			}

			if len(matches) > 0 {
				fmt.Printf("Unit: %-20s Found! Conf: %.3f Pos: (%d, %d)\n", name, matches[0].Confidence, matches[0].Point.X, matches[0].Point.Y)
			} else {
				fmt.Printf("Unit: %-20s NOT FOUND\n", name)
			}
		}
	}
}
