package main

import (
	"fmt"
	"image"
	"image/color"
	"os"

	"github.com/Ducky705/ClashGO/internal/adb"
	"github.com/Ducky705/ClashGO/internal/config"
	"github.com/Ducky705/ClashGO/internal/game"
	"github.com/Ducky705/ClashGO/internal/vision"
	"gocv.io/x/gocv"
)

func main() {
	cfg := config.DefaultConfig()

	fmt.Println("Connecting to ADB...")
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

	fmt.Println("Calibrating screen dimensions...")
	calibrator := game.NewCalibrator(client)
	cal, err := calibrator.Calibrate()
	if err != nil {
		fmt.Printf("Calibration failed: %v\n", err)
		return
	}

	templates, _ := game.NewTemplateStore("assets/templates")
	templates.LoadTemplates()

	upgradeTpl, ok := templates.Get("btn_upgrade_wall")
	if !ok {
		fmt.Println("Error: 'btn_upgrade_wall' template not found!")
		return
	}

	fmt.Println("Capturing screen...")
	screen, err := client.CaptureToMat()
	if err != nil {
		fmt.Printf("Failed to capture screen: %v\n", err)
		return
	}
	defer screen.Close()

	bottomROI := image.Rect(0, int(400*cal.ScaleY), screen.Cols(), screen.Rows())
	rawMatches, _ := vision.MatchMultiScaleAllROI(screen, upgradeTpl, 0.3, 1.5, 60, 0.60, bottomROI)

	// Filter and group matches to get unique buttons
	var uniqueMatches []vision.Match
	for _, m := range rawMatches {
		duplicate := false
		for idx, um := range uniqueMatches {
			distX := m.Point.X - um.Point.X
			distY := m.Point.Y - um.Point.Y
			dist := distX*distX + distY*distY
			if dist < 900 { // 30 pixels distance threshold
				duplicate = true
				if m.Confidence > um.Confidence {
					uniqueMatches[idx] = m
				}
				break
			}
		}
		if !duplicate {
			uniqueMatches = append(uniqueMatches, m)
		}
	}
	matches := uniqueMatches

	if len(matches) == 0 {
		fmt.Println("Error: Upgrade button not found on screen!")
		fmt.Println("Please make sure a wall is selected on the emulator so the green Upgrade button is visible.")
		gocv.IMWrite("scratch/failed_find_btn.png", screen)
		fmt.Println("Saved current screen capture to 'scratch/failed_find_btn.png' for diagnostic check.")
		return
	}

	fmt.Printf("Found %d Upgrade button matches on screen.\n", len(matches))

	for idx, match := range matches {
		fmt.Printf("\n--- Analyzing Button %d at Center (%d, %d) Conf: %.3f Scale: %.3f ---\n", 
			idx+1, match.Point.X, match.Point.Y, match.Confidence, match.Scale)

		// Define the precise cost text ROI (excludes resource icon on right)
		textROI := image.Rect(
			match.Point.X - int(90 * match.Scale),
			match.Point.Y - int(75 * match.Scale),
			match.Point.X + int(40 * match.Scale),
			match.Point.Y - int(25 * match.Scale),
		)

		// Ensure bounds
		if textROI.Min.X < 0 { textROI.Min.X = 0 }
		if textROI.Min.Y < 0 { textROI.Min.Y = 0 }
		if textROI.Max.X > screen.Cols() { textROI.Max.X = screen.Cols() }
		if textROI.Max.Y > screen.Rows() { textROI.Max.Y = screen.Rows() }

		sub := screen.Region(textROI)
		defer sub.Close()

		// Analyze colors
		lowerWhite := gocv.NewScalar(200, 200, 200, 0)
		upperWhite := gocv.NewScalar(255, 255, 255, 0)
		maskWhite := gocv.NewMat()
		defer maskWhite.Close()
		gocv.InRangeWithScalar(sub, lowerWhite, upperWhite, &maskWhite)
		whitePixels := gocv.CountNonZero(maskWhite)

		// Relaxed red bounds to capture salmon/orange-red text in game
		lowerRed := gocv.NewScalar(0, 0, 160, 0)
		upperRed := gocv.NewScalar(140, 150, 255, 0)
		maskRed := gocv.NewMat()
		defer maskRed.Close()
		gocv.InRangeWithScalar(sub, lowerRed, upperRed, &maskRed)
		redPixels := gocv.CountNonZero(maskRed)

		// If there are more than 30 red pixels, the cost text is red (unaffordable)
		affordable := redPixels < 30 && whitePixels > 40

		fmt.Println("TEST RESULTS:")
		fmt.Printf("  White Pixels (BGR >= 200): %d\n", whitePixels)
		fmt.Printf("  Red Pixels (Relaxed): %d\n", redPixels)
		fmt.Printf("  Affordability Decision: %t (Is affordable)\n", affordable)

		// Draw debug graphics on screen
		btnW := int(220 * match.Scale)
		btnH := int(218 * match.Scale)
		btnRect := image.Rect(
			match.Point.X - btnW/2,
			match.Point.Y - btnH/2,
			match.Point.X + btnW/2,
			match.Point.Y + btnH/2,
		)

		debugImg := screen.Clone()
		defer debugImg.Close()

		// Box around the detected button (Green if affordable, Red if not)
		btnColor := color.RGBA{255, 0, 0, 255}
		if affordable {
			btnColor = color.RGBA{0, 255, 0, 255}
		}
		gocv.Rectangle(&debugImg, btnRect, btnColor, 3)
		// Blue box around the cost text ROI
		gocv.Rectangle(&debugImg, textROI, color.RGBA{255, 0, 0, 255}, 2)

		// Write text on the image
		statusText := fmt.Sprintf("Button %d: Unaffordable (RED)", idx+1)
		if affordable {
			statusText = fmt.Sprintf("Button %d: Affordable (WHITE)", idx+1)
		}
		gocv.PutText(&debugImg, statusText, image.Point{10, 50 + idx*40}, gocv.FontHersheySimplex, 0.8, btnColor, 2)

		os.MkdirAll("scratch", 0755)
		gocv.IMWrite(fmt.Sprintf("scratch/debug_result_%d.png", idx+1), debugImg)
		gocv.IMWrite(fmt.Sprintf("scratch/debug_cost_crop_%d.png", idx+1), sub)
	}
	fmt.Println("=======================================================")
}
