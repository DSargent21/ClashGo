package main

import (
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/Ducky705/ClashGo/internal/adb"
	"github.com/Ducky705/ClashGo/internal/config"
	"github.com/Ducky705/ClashGo/internal/game"
	"gocv.io/x/gocv"
)

func main() {
	cfg := config.DefaultConfig()

	// Default reference coordinates
	refX := 430
	refY := 30

	if len(os.Args) >= 3 {
		x, err1 := strconv.Atoi(os.Args[1])
		y, err2 := strconv.Atoi(os.Args[2])
		if err1 == nil && err2 == nil {
			refX = x
			refY = y
		}
	}

	fmt.Printf("Starting builder click test using reference coordinates: (%d, %d)\n", refX, refY)

	client := adb.NewClient(
		adb.WithHost(cfg.Device.ADBHost),
		adb.WithPort(cfg.Device.ADBPort),
	)
	client.DeviceID = cfg.Device.DeviceID

	if err := client.Connect(); err != nil {
		fmt.Printf("ADB Connection Error: %v\n", err)
		return
	}
	defer client.Close()

	calibrator := game.NewCalibrator(client)
	cal, err := calibrator.Calibrate()
	if err != nil {
		fmt.Printf("Calibration failed: %v\n", err)
		return
	}

	// Capture before image
	fmt.Println("Capturing screen BEFORE tap...")
	imgBefore, err := client.CaptureToMat()
	if err == nil {
		gocv.IMWrite("before_builder_click.png", imgBefore)
		imgBefore.Close()
		fmt.Println("Saved to before_builder_click.png")
	}

	// Tap the scaled coordinates
	tx, ty := cal.ScaleRef(refX, refY)
	fmt.Printf("Tapping physical screen coordinates: (%d, %d)...\n", tx, ty)
	if err := client.Tap(tx, ty); err != nil {
		fmt.Printf("Tap failed: %v\n", err)
		return
	}

	time.Sleep(1500 * time.Millisecond)

	// Capture after image
	fmt.Println("Capturing screen AFTER tap...")
	imgAfter, err := client.CaptureToMat()
	if err == nil {
		gocv.IMWrite("after_builder_click.png", imgAfter)
		imgAfter.Close()
		fmt.Println("Saved to after_builder_click.png")
	}

	fmt.Println("\nFinished! Verify click behavior using 'before_builder_click.png' and 'after_builder_click.png'.")
	fmt.Println("If the click missed, run again passing custom reference coordinates:")
	fmt.Println("  go run scripts/test_builder_click.go <x> <y>")
}
