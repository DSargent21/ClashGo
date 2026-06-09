package main

import (
	"fmt"
	"github.com/Ducky705/ClashGO/internal/game"
	"github.com/rs/zerolog"
	"gocv.io/x/gocv"
	"os"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Println("Usage: classify <image>")
		return
	}

	img := gocv.IMRead(os.Args[1], gocv.IMReadColor)
	if img.Empty() {
		fmt.Printf("Failed to read image: %s\n", os.Args[1])
		return
	}
	defer img.Close()

	// Need a calibration to init classifier
	cal := &game.Calibration{
		PhysicalW: 860,
		PhysicalH: 732,
		ScaleX:    1.0,
		ScaleY:    1.0,
	}
	
	logger := zerolog.New(os.Stdout)
	cls := game.NewClassifier(cal, game.DefaultClassifierConfig(), logger)
	
	state, score := cls.ClassifyState(img)
	fmt.Printf("Detected State: %s (Score: %d)\n", state.String(), score)
}
