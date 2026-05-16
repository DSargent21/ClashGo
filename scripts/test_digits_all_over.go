package main

import (
	"fmt"
	"image"
	"strconv"
	"gocv.io/x/gocv"
)

func main() {
	lr := NewLootRecognizer(&Calibration{ScaleX: 1.0, ScaleY: 1.0}, &TemplateStore{}, nil) // This won't work easily
}
