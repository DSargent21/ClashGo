package main

import (
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"os"
	"runtime"

	"github.com/Ducky705/ClashGO/internal/adb"
	"github.com/Ducky705/ClashGO/internal/config"
	"github.com/Ducky705/ClashGO/internal/game"
	"gocv.io/x/gocv"
)

type StallConfig struct {
	PercentROI  image.Rectangle `json:"percent_roi"`
	EndButton   image.Point     `json:"end_button"`
	ConfirmBtn  image.Point     `json:"confirm_btn"`
	RefWidth    int             `json:"ref_width"`
	RefHeight   int             `json:"ref_height"`
}

func main() {
	runtime.LockOSThread()
	cfg := config.DefaultConfig()
	client := adb.NewClient(adb.WithHost(cfg.Device.ADBHost), adb.WithPort(cfg.Device.ADBPort))
	client.DeviceID = cfg.Device.DeviceID
	if err := client.Connect(); err != nil {
		fmt.Printf("ADB Error: %v\n", err)
		return
	}
	defer client.Close()

	cal, _ := game.NewCalibrator(client).Calibrate()
	screen, _ := client.CaptureToMat()
	defer screen.Close()

	window := gocv.NewWindow("Stall Timer Calibration")
	defer window.Close()

	var stall StallConfig
	stall.RefWidth, stall.RefHeight = cal.PhysicalW, cal.PhysicalH
	mode := "roi" // roi -> end -> confirm -> save
	var p1, p2 image.Point
	drawing := false

	window.SetMouseHandler(func(event int, x, y int, flags int, userdata interface{}) {
		switch mode {
		case "roi":
			if event == 1 { // LDown
				p1 = image.Pt(x, y)
				drawing = true
			} else if event == 0 && drawing { // Move
				p2 = image.Pt(x, y)
			} else if event == 4 { // LUp
				p2 = image.Pt(x, y)
				drawing = false
				stall.PercentROI = image.Rect(p1.X, p1.Y, p2.X, p2.Y).Canon()
				fmt.Printf("ROI Set: %v. Press 'n' for End Button.\n", stall.PercentROI)
			}
		case "end", "confirm":
			if event == 1 {
				pt := image.Pt(x, y)
				if mode == "end" {
					stall.EndButton = pt
					fmt.Printf("End Button set: %v. Press 'n' for Confirm Button.\n", pt)
				} else {
					stall.ConfirmBtn = pt
					fmt.Printf("Confirm Button set: %v. Press 's' to Save.\n", pt)
				}
			}
		}
	}, nil)

	fmt.Println("1. Drag ROI over destruction percentage. Press 'n' when done.")
	for {
		display := screen.Clone()
		switch mode {
		case "roi":
			gocv.Rectangle(&display, stall.PercentROI, color.RGBA{0, 255, 0, 255}, 2)
			gocv.PutText(&display, "MODE: SELECT % ROI", image.Pt(20, 40), 1, 1.2, color.RGBA{0, 255, 0, 255}, 2)
		case "end":
			gocv.Circle(&display, stall.EndButton, 10, color.RGBA{255, 0, 0, 255}, 2)
			gocv.PutText(&display, "MODE: CLICK END BUTTON", image.Pt(20, 40), 1, 1.2, color.RGBA{255, 0, 0, 255}, 2)
		case "confirm":
			gocv.Circle(&display, stall.ConfirmBtn, 10, color.RGBA{0, 0, 255, 255}, 2)
			gocv.PutText(&display, "MODE: CLICK CONFIRM BUTTON", image.Pt(20, 40), 1, 1.2, color.RGBA{0, 0, 255, 255}, 2)
		}

		window.IMShow(display)
		display.Close()
		key := window.WaitKey(30)
		if key == 'q' || key == 27 { break }
		if key == 'n' {
			if mode == "roi" { 
				mode = "end" 
				fmt.Println("2. Click the RED 'End Battle' button. Press 'n' when done.")
			} else if mode == "end" { 
				mode = "confirm" 
				fmt.Println("3. IMPORTANT: Tap 'End Battle' ON YOUR DEVICE so the dialog appears, then press 'r' to refresh this window.")
			}
		}
		if key == 's' {
			if stall.ConfirmBtn.X == 0 {
				fmt.Println("Error: Confirm button not set. Click it first!")
				continue
			}
			data, _ := json.MarshalIndent(stall, "", "  ")
			os.WriteFile("assets/stall_config.json", data, 0644)
			fmt.Println("Saved to assets/stall_config.json")
			break
		}
		if key == 'r' {
			fmt.Println("Refreshing screen from device...")
			newS, err := client.CaptureToMat()
			if err == nil {
				screen.Close()
				screen = newS
				fmt.Println("Screen refreshed.")
			} else {
				fmt.Printf("Refresh failed: %v\n", err)
			}
		}
	}
}
