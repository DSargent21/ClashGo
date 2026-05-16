package main

import (
	"fmt"
	"gocv.io/x/gocv"
	"os"
)

func main() {
	if len(os.Args) < 2 {
		return
	}
	img := gocv.IMRead(os.Args[1], gocv.IMReadGrayScale)
	if img.Empty() {
		fmt.Println("Empty")
		return
	}
	
	white := 0
	for r := 0; r < img.Rows(); r++ {
		for c := 0; c < img.Cols(); c++ {
			if img.GetUCharAt(r, c) > 128 {
				white++
			}
		}
	}
	
	fmt.Printf("%dx%d, white pixels: %d/%d\n", img.Cols(), img.Rows(), white, img.Cols()*img.Rows())
}
