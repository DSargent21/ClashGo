package main

import (
	"fmt"
	"os"

	"gocv.io/x/gocv"
)

func main() {
	path := "assets/templates/btn_attack.png"
	mat := gocv.IMRead(path, gocv.IMReadColor)
	if mat.Empty() {
		fmt.Printf("❌ Cannot load %s\n", path)
		os.Exit(1)
	}
	defer mat.Close()

	mean := mat.Mean()
	fmt.Printf("Template %s:\n", path)
	fmt.Printf("  Size: %dx%d\n", mat.Cols(), mat.Rows())
	fmt.Printf("  Avg Color: RGB(%.1f, %.1f, %.1f)\n", mean.Val3, mean.Val2, mean.Val1) // BGR in gocv
}
