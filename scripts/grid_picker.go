package main

import (
	"fmt"
	"image"
	"image/color"

	"github.com/Ducky705/ClashGo/internal/adb"
	"github.com/Ducky705/ClashGo/internal/config"
	"github.com/Ducky705/ClashGo/internal/game"
	"gocv.io/x/gocv"
)

func main() {
	cfg := config.DefaultConfig()
	client := adb.NewClient(
		adb.WithHost(cfg.Device.ADBHost),
		adb.WithPort(cfg.Device.ADBPort),
	)
	client.DeviceID = cfg.Device.DeviceID

	if err := client.Connect(); err != nil {
		fmt.Printf("ADB Error: %v\n", err)
		return
	}
	defer client.Close()

	calibrator := game.NewCalibrator(client)
	cal, err := calibrator.Calibrate()
	if err != nil {
		fmt.Printf("Calibration Error: %v\n", err)
		return
	}

	screen, err := client.CaptureToMat()
	if err != nil {
		fmt.Printf("Capture Error: %v\n", err)
		return
	}
	defer screen.Close()

	// Draw Grid based on Reference coordinates (860x732)
	for x := 0; x <= 860; x += 50 {
		px, _ := cal.ScaleRef(x, 0)
		gocv.Line(&screen, image.Pt(px, 0), image.Pt(px, screen.Rows()), color.RGBA{128, 128, 128, 255}, 1)
		if x%100 == 0 {
			gocv.PutText(&screen, fmt.Sprintf("%d", x), image.Pt(px+2, 20), gocv.FontHersheyPlain, 0.8, color.RGBA{255, 255, 255, 255}, 1)
		}
	}
	for y := 0; y <= 732; y += 50 {
		_, py := cal.ScaleRef(0, y)
		gocv.Line(&screen, image.Pt(0, py), image.Pt(screen.Cols(), py), color.RGBA{128, 128, 128, 255}, 1)
		if y%100 == 0 {
			gocv.PutText(&screen, fmt.Sprintf("%d", y), image.Pt(2, py-2), gocv.FontHersheyPlain, 0.8, color.RGBA{255, 255, 255, 255}, 1)
		}
	}

	out := "coordinate_grid.png"
	gocv.IMWrite(out, screen)
	fmt.Printf("Saved grid image to %s\n", out)
	fmt.Println("Use this image to find the Reference (REF) coordinates for the button.")
}
