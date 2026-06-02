package main

import (
	"fmt"
	"os"

	"gocv.io/x/gocv"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Println("Usage: check_pixel <image> [x y]")
		return
	}
	img := gocv.IMRead(os.Args[1], gocv.IMReadColor)
	if img.Empty() {
		fmt.Println("Failed to read image")
		return
	}
	defer img.Close()

	fmt.Printf("Image size: %dx%d\n", img.Cols(), img.Rows())

	if len(os.Args) >= 4 {
		var x, y int
		fmt.Sscanf(os.Args[2], "%d", &x)
		fmt.Sscanf(os.Args[3], "%d", &y)
		if x >= 0 && x < img.Cols() && y >= 0 && y < img.Rows() {
			bgr := img.GetUCharAt(y, x*3)
			ggg := img.GetUCharAt(y, x*3+1)
			rrr := img.GetUCharAt(y, x*3+2)
			fmt.Printf("Pixel at (%d, %d): RGB(%d, %d, %d) BGR(%d, %d, %d)\n", x, y, rrr, ggg, bgr, bgr, ggg, rrr)
		} else {
			fmt.Println("Coordinates out of bounds")
		}
	}

	targetR, targetG, targetB := 252, 186, 54
	tol := 40
	foundCount := 0
	for y := 0; y < img.Rows(); y++ {
		for x := 0; x < img.Cols(); x++ {
			b := int(img.GetUCharAt(y, x*3))
			g := int(img.GetUCharAt(y, x*3+1))
			r := int(img.GetUCharAt(y, x*3+2))
			
			if abs(r-targetR) <= tol && abs(g-targetG) <= tol && abs(b-targetB) <= tol {
				if foundCount < 5 {
					fmt.Printf("Color match #%d at (%d, %d): RGB(%d, %d, %d)\n", foundCount+1, x, y, r, g, b)
				}
				foundCount++
			}
		}
	}
	fmt.Printf("Total orange matches: %d\n", foundCount)
}

func abs(x int) int {
	if x < 0 { return -x }
	return x
}
