package main

import (
	"fmt"
	"image"
	"image/color"
	"os"

	"gocv.io/x/gocv"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Println("Usage: go run main.go <screenshot.png>")
		return
	}

	img := gocv.IMRead(os.Args[1], gocv.IMReadColor)
	if img.Empty() {
		fmt.Printf("Error: Could not read %s\n", os.Args[1])
		return
	}
	defer img.Close()

	win := gocv.NewWindow("Hero Bar Selector")
	defer win.Close()

	fmt.Println("\n--- HERO BAR SELECTOR ---")
	fmt.Println("Click anywhere on the TOP EDGE of your Hero/Deployment Bar.")
	fmt.Println("This tells the bot where the 'safe grass' ends and the 'UI' begins.")
	fmt.Println("\nControls:")
	fmt.Println("- Click: Select Y-coordinate")
	fmt.Println("- 's': Save and Exit")
	fmt.Println("- 'q': Quit")

	selectedY := -1

	win.SetMouseHandler(func(event int, x, y int, flags int, userdata interface{}) {
		if event == 1 { // LBUTTONDOWN
			selectedY = y
			fmt.Printf("Selected Deployment Limit: Y=%d\n", y)
		}
	}, nil)

	for {
		display := img.Clone()
		if selectedY != -1 {
			// Draw the safety line
			gocv.Line(&display, image.Pt(0, selectedY), image.Pt(img.Cols(), selectedY), color.RGBA{255, 0, 0, 255}, 2)
			gocv.PutText(&display, "SAFE DEPLOYMENT LIMIT", image.Pt(10, selectedY-10), gocv.FontHersheySimplex, 0.7, color.RGBA{255, 0, 0, 255}, 2)
		}

		win.IMShow(display)
		key := win.WaitKey(10)
		display.Close()

		if key == 'q' {
			break
		} else if key == 's' && selectedY != -1 {
			fmt.Printf("\n✅ Success! Your Hero Bar Y-Coordinate is: %d\n", selectedY)
			fmt.Println("Paste this number back to me, and I'll update the bot's memory.")
			break
		}
	}
}
