package main

import (
	"fmt"
	"image"

	"gocv.io/x/gocv"
)

func main() {
	images := []string{
		"screen_20260515_215234.png",
		"screen_20260515_215242.png",
		"screen_20260515_215252.png",
		"screen_20260515_215301.png",
		"screen_20260515_215309.png",
		"screen_20260515_215316.png",
	}

	for _, imgPath := range images {
		fmt.Printf("Analyzing %s...\n", imgPath)
		img := gocv.IMRead(imgPath, gocv.IMReadColor)
		if img.Empty() {
			fmt.Println("  Failed to load")
			continue
		}
		defer img.Close()

		gray := gocv.NewMat()
		gocv.CvtColor(img, &gray, gocv.ColorBGRToGray)
		defer gray.Close()

		// Search in top-left area
		searchROI := image.Rect(0, 0, 400, 300)
		sub := gray.Region(searchROI)
		defer sub.Close()

		thresh := gocv.NewMat()
		defer thresh.Close()
		gocv.AdaptiveThreshold(sub, &thresh, 255, gocv.AdaptiveThresholdGaussian, gocv.ThresholdBinary, 11, 4)

		contours := gocv.FindContours(thresh, gocv.RetrievalExternal, gocv.ChainApproxSimple)
		defer contours.Close()

		for i := 0; i < contours.Size(); i++ {
			rect := gocv.BoundingRect(contours.At(i))
			if rect.Dy() >= 5 && rect.Dy() <= 40 && rect.Dx() >= 2 && rect.Dx() <= 40 {
				// Potential digit
				fmt.Printf("  Candidate at (%d, %d) size %dx%d\n", rect.Min.X, rect.Min.Y, rect.Dx(), rect.Dy())
			}
		}
	}
}
