package main

import (
	"fmt"
	"gocv.io/x/gocv"
	"image"
)

func main() {
	for i := 0; i <= 9; i++ {
		tpl := gocv.IMRead(fmt.Sprintf("assets/templates/digit_%d.png", i), gocv.IMReadGrayScale)
		if tpl.Empty() {
			fmt.Printf("digit_%d: empty\n", i)
			continue
		}
		
		// Simulate what prepareDigitTemplates does
		gocv.Threshold(tpl, &tpl, 0, 255, gocv.ThresholdBinary|gocv.ThresholdOtsu)
		
		tplContours := gocv.FindContours(tpl, gocv.RetrievalExternal, gocv.ChainApproxSimple)
		var bestRect image.Rectangle
		if tplContours.Size() > 0 {
			maxArea := -1.0
			for j := 0; j < tplContours.Size(); j++ {
				area := gocv.ContourArea(tplContours.At(j))
				if area > maxArea {
					maxArea = area
					bestRect = gocv.BoundingRect(tplContours.At(j))
				}
			}
		}
		tplContours.Close()
		
		fmt.Printf("digit_%d: size=%dx%d, bestRect=%v, nonZero=%d\n", i, tpl.Cols(), tpl.Rows(), bestRect, gocv.CountNonZero(tpl))
		tpl.Close()
	}
}
