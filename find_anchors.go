package main

import (
	"fmt"
	"os"

	"gocv.io/x/gocv"
)

func main() {
	screen := gocv.IMRead("assets/grab/full_screen.png", gocv.IMReadColor)
	if screen.Empty() {
		fmt.Println("Error: Could not read assets/grab/full_screen.png")
		os.Exit(1)
	}
	defer screen.Close()

	anchors := []string{"icon_gold", "icon_elixir", "icon_de"}
	for _, name := range anchors {
		tpl := gocv.IMRead(fmt.Sprintf("assets/templates/%s.png", name), gocv.IMReadColor)
		if tpl.Empty() {
			fmt.Printf("Error: Could not read template %s\n", name)
			continue
		}
		
		res := gocv.NewMat()
		gocv.MatchTemplate(screen, tpl, &res, gocv.TmCcoeffNormed, gocv.NewMat())
		_, maxConf, _, maxLoc := gocv.MinMaxLoc(res)
		res.Close()
		tpl.Close()

		fmt.Printf("Anchor %s: maxConf=%.4f at %v size %dx%d\n", name, maxConf, maxLoc, tpl.Cols(), tpl.Rows())
	}
}
