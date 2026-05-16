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
	img := gocv.IMRead(os.Args[1], gocv.IMReadColor)
	fmt.Printf("%dx%d\n", img.Cols(), img.Rows())
}
