package main

import (
	"fmt"
	"gocv.io/x/gocv"
)

func main() {
	tpl := gocv.IMRead("assets/templates/digit_0.png", gocv.IMReadGrayScale)
	if tpl.Empty() {
		fmt.Println("empty")
		return
	}
	fmt.Printf("Size: %dx%d\n", tpl.Cols(), tpl.Rows())
	for y := 0; y < tpl.Rows(); y++ {
		for x := 0; x < tpl.Cols(); x++ {
			val := tpl.GetUCharAt(y, x)
			if val > 128 {
				fmt.Print("#")
			} else {
				fmt.Print(".")
			}
		}
		fmt.Println()
	}
}
