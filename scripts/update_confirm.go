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

	// Load existing
	data, err := os.ReadFile("assets/stall_config.json")
	var stall StallConfig
	if err == nil {
		json.Unmarshal(data, &stall)
	}

	fmt.Println("1. Trigger the End Battle dialog ON YOUR DEVICE.")
	fmt.Println("2. Press 'r' in the window to capture the screen.")
	fmt.Println("3. Click the GREEN 'Confirm/Okay' button.")
	fmt.Println("4. Press 's' to save.")

	screen, _ := client.CaptureToMat()
	defer screen.Close()

	window := gocv.NewWindow("Update Confirm Button")
	defer window.Close()

	window.SetMouseHandler(func(event int, x, y int, flags int, userdata interface{}) {
		if event == 1 {
			stall.ConfirmBtn = image.Pt(x, y)
			fmt.Printf("Confirm Button set to: %v\n", stall.ConfirmBtn)
		}
	}, nil)

	for {
		display := screen.Clone()
		gocv.Circle(&display, stall.ConfirmBtn, 10, color.RGBA{0, 255, 0, 255}, 2)
		gocv.PutText(&display, "CLICK CONFIRM BUTTON", image.Pt(20, 40), 1, 1.2, color.RGBA{0, 255, 0, 255}, 2)
		
		window.IMShow(display)
		display.Close()
		key := window.WaitKey(30)
		if key == 'q' || key == 27 { break }
		if key == 'r' {
			newS, _ := client.CaptureToMat()
			screen.Close()
			screen = newS
			fmt.Println("Refreshed.")
		}
		if key == 's' {
			out, _ := json.MarshalIndent(stall, "", "  ")
			os.WriteFile("assets/stall_config.json", out, 0644)
			fmt.Println("Saved!")
			break
		}
	}
}
