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
		return
	}
	
	for r := 0; r < img.Rows(); r++ {
		for c := 0; c < img.Cols(); c++ {
			fmt.Printf("%3d ", img.GetUCharAt(r, c))
		}
		fmt.Println()
	}
}
