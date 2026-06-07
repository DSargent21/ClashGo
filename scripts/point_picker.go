package main

import (
	"fmt"
	"image"
	"image/color"
	"runtime"
	"time"

	"github.com/Ducky705/ClashGo/internal/adb"
	"github.com/Ducky705/ClashGo/internal/config"
	"github.com/Ducky705/ClashGo/internal/game"
	"gocv.io/x/gocv"
)

func main() {
	runtime.LockOSThread()

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

	window := gocv.NewWindow("ClashGo Point Picker")
	defer window.Close()

	fmt.Println("Capturing screen... Click on the button in the window.")
	
	screen, err := client.CaptureToMat()
	if err != nil {
		fmt.Printf("Capture Error: %v\n", err)
		return
	}
	defer screen.Close()

	// Handle window resize for high-res screens
	window.ResizeWindow(screen.Cols(), screen.Rows())

	var lastX, lastY int
	var clicked bool

	window.SetMouseHandler(func(event int, x, y int, flags int, userdata interface{}) {
		if event == 1 { // Left Button Down
			lastX, lastY = x, y
			clicked = true
		}
	}, nil)

	for {
		display := screen.Clone()
		
		if clicked {
			refX, refY := cal.Unscale(lastX, lastY)
			bgr := screen.GetVecbAt(lastY, lastX)
			
			msg := fmt.Sprintf("REF: (%d, %d) | PHYS: (%d, %d) | BGR: (%d,%d,%d)", 
				refX, refY, lastX, lastY, bgr[0], bgr[1], bgr[2])
			
			fmt.Println(msg)
			
			// Draw crosshair
			gocv.Line(&display, image.Pt(0, lastY), image.Pt(screen.Cols(), lastY), color.RGBA{0, 255, 255, 255}, 1)
			gocv.Line(&display, image.Pt(lastX, 0), image.Pt(lastX, screen.Rows()), color.RGBA{0, 255, 255, 255}, 1)
			gocv.PutText(&display, msg, image.Pt(10, 30), gocv.FontHersheyPlain, 1.2, color.RGBA{0, 255, 255, 255}, 2)
		}

		window.IMShow(display)
		display.Close()

		key := window.WaitKey(10)
		if key == 'q' || key == 27 { // 'q' or ESC
			break
		}
		if key == 's' {
			gocv.IMWrite("point_picker_capture.png", screen)
			fmt.Println("Saved screenshot to point_picker_capture.png")
		}

		// Refresh screen on 'r'
		if key == 'r' {
			fmt.Println("Refreshing screen...")
			newScreen, err := client.CaptureToMat()
			if err == nil {
				screen.Close()
				screen = newScreen
			}
		}
		
		time.Sleep(50 * time.Millisecond)
	}
}
