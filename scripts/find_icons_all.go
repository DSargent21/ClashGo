package main

import (
	"fmt"
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
		img := gocv.IMRead(imgPath, gocv.IMReadColor)
		if img.Empty() { continue }
		defer img.Close()

		fmt.Printf("--- %s ---\n", imgPath)
		for _, name := range []string{"gold", "elixir", "de"} {
			tpl := gocv.IMRead(fmt.Sprintf("assets/templates/icon_%s.png", name), gocv.IMReadColor)
			if tpl.Empty() { continue }
			defer tpl.Close()

			res := gocv.NewMat()
			gocv.MatchTemplate(img, tpl, &res, gocv.TmCcoeffNormed, gocv.NewMat())
			_, maxVal, _, maxLoc := gocv.MinMaxLoc(res)
			fmt.Printf("  %s icon: %v (conf %.2f)\n", name, maxLoc, maxVal)
			res.Close()
		}
	}
}
