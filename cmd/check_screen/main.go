package main

import (
	"fmt"
	"os"
	"time"

	"github.com/diegosargent/coc-bot/internal/adb"
	"gocv.io/x/gocv"
)

func main() {
	client := adb.NewClient(adb.WithHost("127.0.0.1"), adb.WithPort(5037), adb.WithTimeout(30*time.Second))
	client.DeviceID = "emulator-5554"
	if err := client.Connect(); err != nil {
		fmt.Println("ADB fail:", err)
		os.Exit(1)
	}
	mat, err := client.CaptureToMat()
	if err != nil {
		fmt.Println("Capture fail:", err)
		os.Exit(1)
	}
	defer mat.Close()
	fmt.Printf("Screen: %dx%d\n", mat.Cols(), mat.Rows())

	fmt.Println("\nLeft edge pixels (y=360):")
	for x := 0; x < 100; x += 10 {
		b := mat.GetUCharAt(360, x*3)
		g := mat.GetUCharAt(360, x*3+1)
		r := mat.GetUCharAt(360, x*3+2)
		fmt.Printf("  x=%3d: RGB(%3d,%3d,%3d)\n", x, r, g, b)
	}

	fmt.Println("\nRight edge pixels (y=360):")
	for x := 1180; x < 1280; x += 10 {
		b := mat.GetUCharAt(360, x*3)
		g := mat.GetUCharAt(360, x*3+1)
		r := mat.GetUCharAt(360, x*3+2)
		fmt.Printf("  x=%3d: RGB(%3d,%3d,%3d)\n", x, r, g, b)
	}

	gocv.IMWrite("/tmp/screen_debug.png", mat)
	fmt.Println("\nSaved to /tmp/screen_debug.png")
	fmt.Println("Open it: open /tmp/screen_debug.png")
	fmt.Println("Tell me: does the game fill the full screen horizontally?")
	fmt.Println("Or are there black bars / is it rotated?")
}
