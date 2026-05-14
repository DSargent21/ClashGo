package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/Ducky705/ClashGo/internal/adb"
	"github.com/Ducky705/ClashGo/internal/game"
	"gocv.io/x/gocv"
)

func main() {
	inputPath := flag.String("input", "", "Path to screenshot (omit to capture live)")
	deviceID := flag.String("device", "", "ADB Device ID (optional)")
	flag.Parse()

	// 1. Load or Capture Screen
	var screen gocv.Mat
	var cal *game.Calibration
	
	if *inputPath != "" && fileExists(*inputPath) {
		fmt.Printf("Loading local image: %s\n", *inputPath)
		screen = gocv.IMRead(*inputPath, gocv.IMReadColor)
		// For local images, we assume standard reference resolution or calculate scale
		cal = &game.Calibration{
			PhysicalW: screen.Cols(),
			PhysicalH: screen.Rows(),
			ScaleX:    float64(screen.Cols()) / game.RefWidth,
			ScaleY:    float64(screen.Rows()) / game.RefHeight,
		}
	} else {
		client := adb.NewClient(
			adb.WithHost("127.0.0.1"),
			adb.WithPort(5037),
			adb.WithTimeout(10*time.Second),
		)

		fmt.Println("Connecting to ADB...")
		
		// If no device specified, try to find one
		if *deviceID == "" {
			devs, err := client.Devices()
			if err != nil {
				fmt.Printf("Error listing devices: %v\n", err)
				os.Exit(1)
			}
			if len(devs) == 0 {
				fmt.Println("Error: No ADB devices found. Make sure BlueStacks is running and ADB is enabled.")
				os.Exit(1)
			}
			if len(devs) == 1 {
				*deviceID = devs[0]
			} else {
				// Multiple devices: prefer 127.0.0.1:5555
				for _, d := range devs {
					if strings.Contains(d, "127.0.0.1:5555") {
						*deviceID = d
						break
					}
				}
				if *deviceID == "" {
					fmt.Println("Multiple devices found. Please specify one with -device:")
					for _, d := range devs {
						fmt.Printf("  - %s\n", d)
					}
					os.Exit(1)
				}
			}
			fmt.Printf("Auto-selected device: %s\n", *deviceID)
		}
		
		client.DeviceID = *deviceID

		if err := client.Connect(); err != nil {
			fmt.Printf("ADB Connect Error: %v\n", err)
			os.Exit(1)
		}
		defer client.Close()
		
		fmt.Println("Capturing live screen...")
		var err error
		screen, err = client.CaptureToMat()
		if err != nil {
			fmt.Printf("Capture Error: %v\n", err)
			os.Exit(1)
		}

		// Calibrate based on live screen
		calibrator := game.NewCalibrator(client)
		cal, err = calibrator.Calibrate()
		if err != nil {
			fmt.Printf("Calibration Error: %v\n", err)
			os.Exit(1)
		}
	}
	defer screen.Close()

	if screen.Empty() {
		fmt.Println("Error: Empty screen buffer")
		os.Exit(1)
	}

	// 2. Initialize Loot Recognizer
	ts, err := game.NewTemplateStore("assets/templates")
	if err != nil {
		fmt.Printf("TemplateStore Error: %v\n", err)
		os.Exit(1)
	}
	if err := ts.LoadTemplates(); err != nil {
		fmt.Printf("LoadTemplates Error: %v\n", err)
		os.Exit(1)
	}
	defer ts.Close()

	lr := game.NewLootRecognizerWithDebug(cal, ts, true)
	defer lr.Close()

	// 3. Perform Recognition
	fmt.Println("\n--- Loot Recognition Results ---")
	start := time.Now()
	loot, err := lr.ReadAvailableLoot(screen)
	elapsed := time.Since(start)

	if err != nil {
		fmt.Printf("Recognition Error: %v\n", err)
	} else {
		fmt.Printf("\nFINAL RESULTS:\n")
		fmt.Printf("  Gold:         %d\n", loot.Gold)
		fmt.Printf("  Elixir:       %d\n", loot.Elixir)
		fmt.Printf("  Dark Elixir:  %d\n", loot.DarkElixir)
	}
	fmt.Printf("\nDetection took: %v\n", elapsed)
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
