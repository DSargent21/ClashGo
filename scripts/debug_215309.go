package main

import (
	"fmt"
	"image"
	"gocv.io/x/gocv"
)

func main() {
	img := gocv.IMRead("screen_20260515_215309.png", gocv.IMReadColor)
	if img.Empty() { return }
	defer img.Close()

	gray := gocv.NewMat()
	gocv.CvtColor(img, &gray, gocv.ColorBGRToGray)
	defer gray.Close()

	roi := image.Rect(0, 0, 400, 300)
	sub := gray.Region(roi)
	defer sub.Close()

	thresh := gocv.NewMat()
	defer thresh.Close()
	gocv.AdaptiveThreshold(sub, &thresh, 255, gocv.AdaptiveThresholdGaussian, gocv.ThresholdBinary, 9, 3)

	contours := gocv.FindContours(thresh, gocv.RetrievalExternal, gocv.ChainApproxSimple)
	defer contours.Close()

	for i := 0; i < contours.Size(); i++ {
		rect := gocv.BoundingRect(contours.At(i))
		if rect.Dy() >= 10 && rect.Dy() <= 25 {
			fmt.Printf("Blob at x=%d y=%d w=%d h=%d\n", rect.Min.X, rect.Min.Y, rect.Dx(), rect.Dy())
		}
	}
}
