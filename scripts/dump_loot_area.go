package main

import (
	"image"
	"gocv.io/x/gocv"
)

func main() {
	img := gocv.IMRead("assets/grab/full_screen.png", gocv.IMReadColor)
	if img.Empty() { return }
	defer img.Close()

	// Available Loot section
	lootArea := image.Rect(0, 0, 300, 400)
	crop := img.Region(lootArea)
	gocv.IMWrite("assets/verify/available_loot_full.png", crop)
	crop.Close()
}
