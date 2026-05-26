package main

import (
	"fmt"
	"image"
	"image/color"
	"os"

	"github.com/Ducky705/ClashGo/internal/attack"
	"github.com/Ducky705/ClashGo/internal/config"
	"github.com/Ducky705/ClashGo/internal/game"
	"github.com/rs/zerolog"
	"gocv.io/x/gocv"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Println("Usage: go run test_slot_empty.go <screenshot>")
		return
	}

	screenPath := os.Args[1]
	logger := zerolog.New(os.Stdout).With().Timestamp().Logger()
	screen := gocv.IMRead(screenPath, gocv.IMReadColor)
	if screen.Empty() {
		fmt.Println("Failed to read screen")
		return
	}
	defer screen.Close()

	cfg := config.DefaultConfig()
	cal := &game.Calibration{PhysicalW: screen.Cols(), PhysicalH: screen.Rows()}
	executor := attack.NewExecutor(nil, cal, &cfg.Attack, logger)

	h := screen.Rows()
	barY := int(float64(h) * 0.85)
	iconWidth := int(float64(h) * 0.12)

	debugImg := screen.Clone()
	defer debugImg.Close()

	for x := iconWidth / 2; x < screen.Cols(); x += iconWidth {
		isEmpty := executor.IsSlotEmpty(screen, x, barY)
		
		label := "FULL"
		c := color.RGBA{255, 0, 0, 255} // Red for full (BGR in OpenCV means Blue)
		if isEmpty {
			label = "EMPTY"
			c = color.RGBA{0, 255, 0, 255} // Green for empty
		}

		fmt.Printf("Slot at x=%d: %s\n", x, label)
		
		rect := image.Rect(x-15, barY-15, x+15, barY+15)
		gocv.Rectangle(&debugImg, rect, c, 2)
	}

	gocv.IMWrite("slot_test_debug.png", debugImg)
	fmt.Println("Wrote results to slot_test_debug.png")
}
