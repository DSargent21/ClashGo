package main

import (
	"fmt"
	"gocv.io/x/gocv"
	"github.com/Ducky705/ClashGo/internal/game"
)

func main() {
	templateDir := "assets/templates"
	ts, _ := game.NewTemplateStore(templateDir)
	ts.LoadTemplates()

	images := []string{
		"screen_20260515_215234.png",
		"screen_20260515_215242.png",
		"screen_20260515_215252.png",
		"screen_20260515_215301.png",
		"screen_20260515_215309.png",
		"screen_20260515_215316.png",
	}

	icons := []string{"icon_gold", "icon_elixir", "icon_de"}

	for _, imgPath := range images {
		img := gocv.IMRead(imgPath, gocv.IMReadColor)
		if img.Empty() { continue }
		defer img.Close()

		fmt.Printf("--- %s ---\n", imgPath)
		for _, iconName := range icons {
			tpl, ok := ts.Get(iconName)
			if !ok { continue }
			
			res := gocv.NewMat()
			gocv.MatchTemplate(img, tpl, &res, gocv.TmCcoeffNormed, gocv.NewMat())
			_, maxVal, _, maxLoc := gocv.MinMaxLoc(res)
			res.Close()

			if maxVal > 0.5 {
				fmt.Printf("  %s: loc=(%d, %d) conf=%.3f\n", iconName, maxLoc.X, maxLoc.Y, maxVal)
			}
		}
	}
}
