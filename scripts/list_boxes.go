package main

import (
	"fmt"
	"image"
	"sort"
	"gocv.io/x/gocv"
)

func main() {
	img := gocv.IMRead("assets/verify/available_loot_full.png", gocv.IMReadGrayScale)
	if img.Empty() { return }
	defer img.Close()

	thresh := gocv.NewMat()
	defer thresh.Close()
	gocv.Threshold(img, &thresh, 180, 255, gocv.ThresholdBinary)

	contours := gocv.FindContours(thresh, gocv.RetrievalExternal, gocv.ChainApproxSimple)
	defer contours.Close()

	type box struct {
		rect image.Rectangle
	}
	var boxes []box
	for i := 0; i < contours.Size(); i++ {
		r := gocv.BoundingRect(contours.At(i))
		if r.Dx() >= 2 && r.Dy() >= 10 && r.Dy() <= 30 {
			boxes = append(boxes, box{rect: r})
		}
	}

	sort.Slice(boxes, func(i, j int) bool {
		if boxes[i].rect.Min.Y / 10 != boxes[j].rect.Min.Y / 10 {
			return boxes[i].rect.Min.Y < boxes[j].rect.Min.Y
		}
		return boxes[i].rect.Min.X < boxes[j].rect.Min.X
	})

	for i, b := range boxes {
		fmt.Printf("Box %d: rect=(%d,%d)-(%d,%d) size=%dx%d\n", i, b.rect.Min.X, b.rect.Min.Y, b.rect.Max.X, b.rect.Max.Y, b.rect.Dx(), b.rect.Dy())
	}
}
