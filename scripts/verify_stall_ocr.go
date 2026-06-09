package main

import (
	"encoding/json"
	"fmt"
	"image"
	"os"

	"github.com/Ducky705/ClashGO/internal/adb"
	"github.com/Ducky705/ClashGO/internal/config"
	"github.com/Ducky705/ClashGO/internal/game"
	"github.com/rs/zerolog"
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
	logger := zerolog.New(zerolog.ConsoleWriter{Out: os.Stderr}).With().Timestamp().Logger()
	cfg := config.DefaultConfig()
	client := adb.NewClient(adb.WithHost(cfg.Device.ADBHost), adb.WithPort(cfg.Device.ADBPort))
	client.DeviceID = cfg.Device.DeviceID
	if err := client.Connect(); err != nil {
		fmt.Printf("ADB Error: %v\n", err)
		return
	}
	defer client.Close()

	cal, _ := game.NewCalibrator(client).Calibrate()
	
	var sCfg StallConfig
	data, err := os.ReadFile("assets/stall_config.json")
	if err != nil {
		fmt.Println("Config not found. Run calibration first.")
		return
	}
	json.Unmarshal(data, &sCfg)

	fmt.Println("Capturing screen...")
	screen, _ := client.CaptureToMat()
	defer screen.Close()

	// Scale ROI
	scaleX, scaleY := float64(cal.PhysicalW)/float64(sCfg.RefWidth), float64(cal.PhysicalH)/float64(sCfg.RefHeight)
	pRoi := image.Rect(
		int(float64(sCfg.PercentROI.Min.X)*scaleX),
		int(float64(sCfg.PercentROI.Min.Y)*scaleY),
		int(float64(sCfg.PercentROI.Max.X)*scaleX),
		int(float64(sCfg.PercentROI.Max.Y)*scaleY),
	)

	tStore, _ := game.NewTemplateStore("assets/templates")
	tStore.LoadTemplates()
	lootRec := game.NewLootRecognizer(cal, tStore, logger)
	defer lootRec.Close()

	fmt.Printf("Testing OCR on ROI: %v\n", pRoi)
	pct := lootRec.ReadDestructionPercentage(screen, pRoi)
	fmt.Printf("DETECTED PERCENTAGE: %d%%\n", pct)

	// Save debug crop
	crop := screen.Region(pRoi)
	gocv.IMWrite("stall_debug_crop.png", crop)
	crop.Close()
	fmt.Println("Saved debug crop to stall_debug_crop.png")
}
