package main

import (
	"fmt"
	"gocv.io/x/gocv"
)

func main() {
	img := gocv.IMRead("screen_20260515_215234.png", gocv.IMReadColor)
	tpl := gocv.IMRead("assets/templates/icon_gold.png", gocv.IMReadColor)
	if img.Empty() || tpl.Empty() { return }
	defer img.Close()
	defer tpl.Close()

	res := gocv.NewMat()
	defer res.Close()
	gocv.MatchTemplate(img, tpl, &res, gocv.TmCcoeffNormed, gocv.NewMat())
	
	for y := 0; y < res.Rows(); y++ {
		for x := 0; x < res.Cols(); x++ {
			if res.GetFloatAt(y, x) > 0.50 {
				fmt.Printf("Gold icon match at (%d, %d) conf %f\n", x, y, res.GetFloatAt(y, x))
			}
		}
	}
}
