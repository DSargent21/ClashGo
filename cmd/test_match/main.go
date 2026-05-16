package main

import (
	"fmt"
	"os"

	"gocv.io/x/gocv"
	"github.com/Ducky705/ClashGo/internal/vision"
)

func main() {
	if len(os.Args) < 3 {
		fmt.Println("Usage: go run main.go <screenshot> <template>")
		return
	}

	screen := gocv.IMRead(os.Args[1], gocv.IMReadColor)
	tpl := gocv.IMRead(os.Args[2], gocv.IMReadColor)
	if screen.Empty() || tpl.Empty() {
		fmt.Println("Error reading images")
		return
	}
	defer screen.Close()
	defer tpl.Close()

	matches, err := vision.MatchTemplate(screen, tpl, 0.7)
	if err != nil {
		fmt.Printf("Match error: %v\n", err)
		return
	}

	if len(matches) == 0 {
		fmt.Println("No matches found.")
		return
	}

	for i, m := range matches {
		fmt.Printf("Match %d: Point(%d,%d) Confidence: %.2f\n", i, m.Point.X, m.Point.Y, m.Confidence)
	}
}
