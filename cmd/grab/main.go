package main

import (
	"fmt"
	"os"
	"time"

	"github.com/Ducky705/ClashGo/internal/adb"
	"gocv.io/x/gocv"
)

func main() {
	client := adb.NewClient(
		adb.WithHost("127.0.0.1"),
		adb.WithPort(5037),
		adb.WithTimeout(30*time.Second),
	)
	client.DeviceID = "emulator-5554"

	if err := client.Connect(); err != nil {
		fmt.Fprintf(os.Stderr, "ADB Error: %v\n", err)
		os.Exit(1)
	}
	defer client.Close()

	screen, err := client.CaptureToMat()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Capture Error: %v\n", err)
		os.Exit(1)
	}
	defer screen.Close()

	timestamp := time.Now().Format("20060102_150405")
	filename := fmt.Sprintf("screen_%s.png", timestamp)
	if len(os.Args) > 1 {
		filename = os.Args[1]
	}

	if gocv.IMWrite(filename, screen) {
		fmt.Printf("Saved screenshot to: %s\n", filename)
	} else {
		fmt.Fprintf(os.Stderr, "Failed to save screenshot\n")
		os.Exit(1)
	}
}
