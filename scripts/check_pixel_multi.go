package main

import (
	"fmt"
	"image"
	"os"

	"gocv.io/x/gocv"
)

func main() {
	if len(os.Args) < 2 {
		return
	}
	img := gocv.IMRead(os.Args[1], gocv.IMReadColor)
	if img.Empty() {
		return
	}
	defer img.Close()

	// Normalize to 732 height like the classifier does
	norm := gocv.NewMat()
	defer norm.Close()
	gocv.Resize(img, &norm, image.Point{X: int(float64(img.Cols()) * 732.0 / float64(img.Rows())), Y: 732}, 0, 0, gocv.InterpolationLinear)

	checks := []struct{ x, y int }{
		{290, 366},
		{135, 204},
		{405, 509},
		{38, 603},
	}

	for _, c := range checks {
		b := norm.GetUCharAt(c.y, c.x*3)
		g := norm.GetUCharAt(c.y, c.x*3+1)
		r := norm.GetUCharAt(c.y, c.x*3+2)
		fmt.Printf("Color at (%d, %d): R=%d, G=%d, B=%d\n", c.x, c.y, r, g, b)
	}
}
