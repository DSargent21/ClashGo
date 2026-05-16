package main

import (
	"fmt"
	"image"
	"image/color"
	"os"

	"gocv.io/x/gocv"
)

type Target struct {
	Label  string
	IsArea bool
}

func main() {
	if len(os.Args) < 2 {
		fmt.Println("Usage: go run cmd/pixel_tuner/main.go <image_path>")
		return
	}

	path := os.Args[1]
	img := gocv.IMRead(path, gocv.IMReadColor)
	if img.Empty() {
		fmt.Printf("Error: Could not read image at %s\n", path)
		return
	}
	defer img.Close()

	// Define the sequence of things to pick
	targets := []Target{
		{"Battle Gold (Area)", true},
		{"Battle Elixir (Area)", true},
		{"Battle DE (Area)", true},
		{"Bonus Gold (Area)", true},
		{"Bonus Elixir (Area)", true},
		{"Bonus DE (Area)", true},
		{"Star 1 - Left (Point)", false},
		{"Star 2 - Center (Point)", false},
		{"Star 3 - Right (Point)", false},
		{"Return Home Button (Point)", false},
	}

	window := gocv.NewWindow("Pixel Tuner Pro - [U]ndo | [Q]uit")
	defer window.Close()

	// State
	currentIndex := 0
	startPt := image.Point{-1, -1}
	currentPt := image.Point{-1, -1}
	
	type Result struct {
		Target Target
		Rect   image.Rectangle
		Pt     image.Point
	}
	results := []Result{}

	fmt.Println("=== Pixel Tuner Pro ===")
	fmt.Println("Instructions:")
	fmt.Println("  - Follow the prompt at the top of the window.")
	fmt.Println("  - Area: Click and Drag.")
	fmt.Println("  - Point: Single Click.")
	fmt.Println("  - Press 'U' or Backspace to Undo.")
	fmt.Println("  - Press 'Q' or ESC to Quit/Finish.")
	fmt.Println()

	window.SetMouseHandler(func(event int, x, y int, flags int, userdata interface{}) {
		if currentIndex >= len(targets) {
			return
		}
		target := targets[currentIndex]

		switch event {
		case 1: // LButtonDown
			startPt = image.Pt(x, y)
			currentPt = startPt
		case 0: // MouseMove
			if startPt.X != -1 {
				currentPt = image.Pt(x, y)
			}
		case 4: // LButtonUp
			if target.IsArea {
				rect := image.Rect(startPt.X, startPt.Y, x, y).Canon()
				if rect.Dx() > 1 && rect.Dy() > 1 {
					results = append(results, Result{Target: target, Rect: rect})
					fmt.Printf("✓ Saved %s: %v\n", target.Label, rect)
					currentIndex++
				}
			} else {
				pt := image.Pt(x, y)
				results = append(results, Result{Target: target, Pt: pt})
				fmt.Printf("✓ Saved %s: %v\n", target.Label, pt)
				currentIndex++
			}
			startPt = image.Point{-1, -1}
		}
	}, nil)

	for {
		canvas := img.Clone()
		
		// Draw previous results
		for _, res := range results {
			if res.Target.IsArea {
				gocv.Rectangle(&canvas, res.Rect, color.RGBA{0, 255, 0, 255}, 2)
				gocv.PutText(&canvas, res.Target.Label, res.Rect.Min.Add(image.Pt(0, -5)), gocv.FontHersheyPlain, 0.8, color.RGBA{0, 255, 0, 255}, 1)
			} else {
				gocv.Circle(&canvas, res.Pt, 5, color.RGBA{255, 0, 0, 255}, -1)
				gocv.PutText(&canvas, res.Target.Label, res.Pt.Add(image.Pt(10, 0)), gocv.FontHersheyPlain, 0.8, color.RGBA{255, 0, 0, 255}, 1)
			}
		}

		// Draw current action
		if currentIndex < len(targets) {
			target := targets[currentIndex]
			header := fmt.Sprintf("STEP %d/%d: %s", currentIndex+1, len(targets), target.Label)
			
			// Background bar for text
			gocv.Rectangle(&canvas, image.Rect(0, 0, img.Cols(), 40), color.RGBA{0, 0, 0, 180}, -1)
			gocv.PutText(&canvas, header, image.Pt(20, 30), gocv.FontHersheySimplex, 0.7, color.RGBA{255, 255, 0, 255}, 2)

			if startPt.X != -1 && target.IsArea {
				rect := image.Rect(startPt.X, startPt.Y, currentPt.X, currentPt.Y).Canon()
				gocv.Rectangle(&canvas, rect, color.RGBA{255, 255, 255, 255}, 1)
			}
		} else {
			gocv.Rectangle(&canvas, image.Rect(0, 0, img.Cols(), 40), color.RGBA{0, 100, 0, 180}, -1)
			gocv.PutText(&canvas, "ALL STEPS COMPLETE! PRESS 'Q' TO SAVE", image.Pt(20, 30), gocv.FontHersheySimplex, 0.7, color.RGBA{255, 255, 255, 255}, 2)
		}

		window.IMShow(canvas)
		canvas.Close()

		key := window.WaitKey(30)
		if key == 'q' || key == 27 {
			break
		} else if key == 'u' || key == 8 { // 'u' or Backspace
			if len(results) > 0 {
				last := results[len(results)-1]
				results = results[:len(results)-1]
				currentIndex--
				fmt.Printf("⟲ Undone: %s\n", last.Target.Label)
			}
		}
	}

	fmt.Println("\n=== FINAL CALIBRATION DATA ===")
	for _, res := range results {
		if res.Target.IsArea {
			fmt.Printf("%-25s: image.Rect(%d, %d, %d, %d),\n", res.Target.Label, res.Rect.Min.X, res.Rect.Min.Y, res.Rect.Max.X, res.Rect.Max.Y)
		} else {
			fmt.Printf("%-25s: image.Pt(%d, %d),\n", res.Target.Label, res.Pt.X, res.Pt.Y)
		}
	}
}
