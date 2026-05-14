package main

import (
	"fmt"
	"gocv.io/x/gocv"
)

func main() {
	img := gocv.IMRead("assets/grab/full_screen.png", gocv.IMReadGrayScale)
	if img.Empty() { return }
	defer img.Close()

	// Scan left side for "Available Loot" text lines
	// Look for vertical blocks of white pixels between x=50 and x=200
	fmt.Println("Scanning for text lines (y-coordinates):")
	for y := 100; y < 350; y++ {
		whiteCount := 0
		for x := 50; x < 200; x++ {
			if img.GetUCharAt(y, x) > 150 {
				whiteCount++
			}
		}
		if whiteCount > 10 {
			fmt.Printf("y=%d: %d white pixels\n", y, whiteCount)
		}
	}
}
