package main

import (
	"encoding/json"
	"fmt"
	"image"
	"os"
	"time"

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

	// Restart game to ensure clean state
	packageName := "com.supercell.clashofclans"
	fmt.Printf("Restarting game (%s)...\n", packageName)
	if err := client.ForceStop(packageName); err != nil {
		fmt.Printf("Warning: ForceStop failed: %v\n", err)
	}
	time.Sleep(2 * time.Second)
	if err := client.StartApp(packageName); err != nil {
		fmt.Printf("Error: StartApp failed: %v\n", err)
		return
	}
	fmt.Println("Waiting 15 seconds for game to load and settle on home screen...")
	time.Sleep(15 * time.Second)

	calibrator := game.NewCalibrator(client)
	cal, err := calibrator.Calibrate()
	if err != nil {
		fmt.Printf("Calibration failed: %v\n", err)
		return
	}

	templates, _ := game.NewTemplateStore("assets/templates")
	templates.LoadTemplates()

	// Load Menu ROI
	menuROI := image.Rect(
		int(400 * cal.ScaleX),
		int(50 * cal.ScaleY),
		int(860 * cal.ScaleX),
		int(732 * cal.ScaleY),
	)
	if roiData, err := os.ReadFile("assets/builder_menu_roi.json"); err == nil {
		type ROIConfig struct {
			Physical map[string]int `json:"physical"`
		}
		var rCfg ROIConfig
		if json.Unmarshal(roiData, &rCfg) == nil {
			menuROI = image.Rect(
				rCfg.Physical["x1"], rCfg.Physical["y1"],
				rCfg.Physical["x2"], rCfg.Physical["y2"],
			)
			fmt.Printf("Loaded custom builder menu ROI: (%d, %d) -> (%d, %d)\n", menuROI.Min.X, menuROI.Min.Y, menuROI.Max.X, menuROI.Max.Y)
		}
	}

	for loopCount := 1; ; loopCount++ {
		fmt.Printf("\n--- [Loop Iteration %d] Starting Wall Upgrade ---\n", loopCount)

		// -------------------------------------------------------------
		// STEP 1: Click Builder Head
		// -------------------------------------------------------------
		bx, by := cal.ScaleRef(430, 30)
		fmt.Printf("[Step 1] Tapping builder head at physical (%d, %d)...\n", bx, by)
		_ = client.Tap(bx, by)
		time.Sleep(1500 * time.Millisecond)

		saveScreenshot(client, "step1_after_builder_head.png")

		// -------------------------------------------------------------
		// STEP 2: Scroll Menu to Bottom
		// -------------------------------------------------------------
		scrollX := menuROI.Min.X + menuROI.Dx()/2
		topMargin := int(30 * cal.ScaleY)
		bottomMargin := int(30 * cal.ScaleY)
		sy1 := menuROI.Max.Y - bottomMargin
		sy2 := menuROI.Min.Y + topMargin
		fmt.Printf("[Step 2] Scrolling menu down using Swipe at X: %d, Y1: %d, Y2: %d...\n", scrollX, sy1, sy2)
		for i := 0; i < 6; i++ {
			_ = client.Swipe(scrollX, sy1, scrollX, sy2, 300)
			time.Sleep(450 * time.Millisecond)
		}
		// Wait for scrolling momentum/animation to fully settle
		time.Sleep(1200 * time.Millisecond)

		saveScreenshot(client, "step2_after_menu_scroll.png")

		// -------------------------------------------------------------
		// STEP 3: Match and click Wall Suggestion
		// -------------------------------------------------------------
		fmt.Println("[Step 3] Matching Wall text template...")
		wallTpl, ok := templates.Get("text_wall")
		if !ok {
			fmt.Println("Error: 'text_wall' template not found!")
			return
		}

		var bestWall *vision.Match

		for attempt := 0; attempt < 6; attempt++ {
			screen, _ := client.CaptureToMat()
			matches, _ := vision.MatchMultiScaleROI(screen, wallTpl, 0.3, 1.5, 60, 0.82, menuROI)
			screen.Close()

			if len(matches) > 0 {
				bestWall = &matches[0]
				break
			}

			fmt.Println("  Wall text not visible, scrolling up slowly...")
			startY := menuROI.Min.Y + menuROI.Dx()/2
			endY := startY + int(140*cal.ScaleY)
			_ = client.Swipe(scrollX, startY, scrollX, endY, 400)
			time.Sleep(1000 * time.Millisecond) // Wait 1000ms for list motion to stop completely
		}

		if bestWall == nil {
			fmt.Println("Error: Failed to find 'text_wall' template after scrolls. Stopping loop.")
			_ = client.Back()
			break
		}

		fmt.Printf("  Found Wall text! Conf: %.3f at (%d, %d). Tapping...\n", bestWall.Confidence, bestWall.Point.X, bestWall.Point.Y)
		_ = client.Tap(bestWall.Point.X, bestWall.Point.Y)
		time.Sleep(2500 * time.Millisecond) // Increased to 2500ms to allow camera pan/zoom transition to finish

		saveScreenshot(client, "step3_after_wall_click.png")

		// -------------------------------------------------------------
		// STEP 4: Match and click Upgrade Button
		// -------------------------------------------------------------
		fmt.Println("[Step 4] Matching Upgrade button template...")
		upgradeTpl, ok := templates.Get("btn_upgrade_wall")
		if !ok {
			fmt.Println("Error: 'btn_upgrade_wall' template not found!")
			return
		}

		screen, _ := client.CaptureToMat()
		bottomROI := image.Rect(0, int(400*cal.ScaleY), screen.Cols(), screen.Rows())
		rawMatches, _ := vision.MatchMultiScaleAllROI(screen, upgradeTpl, 0.3, 1.5, 60, 0.72, bottomROI)
		screen.Close()

		// Filter and group matches to get unique buttons
		var uniqueMatches []vision.Match
		for _, m := range rawMatches {
			duplicate := false
			for idx, um := range uniqueMatches {
				distX := m.Point.X - um.Point.X
				distY := m.Point.Y - um.Point.Y
				dist := distX*distX + distY*distY
				if dist < 3600 { // 60 pixels distance threshold to prevent matching hybrid gap areas
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

		if len(uniqueMatches) == 0 {
			fmt.Println("Error: Upgrade button not found on screen!")
			dismissSelection(client, cal)
			break
		}
		// -------------------------------------------------------------
		// STEP 4/5: Tap Upgrade Buttons and check if Confirm Button appears
		// -------------------------------------------------------------
		fmt.Println("[Step 5] Matching Confirm button template...")
		confirmTpl, ok := templates.Get("btn_confirm_upgrade")
		if !ok {
			fmt.Println("Error: 'btn_confirm_upgrade' template not found!")
			return
		}

		success := false
		for idx, match := range uniqueMatches {
			fmt.Printf("  [Attempt %d] Tapping Upgrade button at (%d, %d)...\n", idx+1, match.Point.X, match.Point.Y)
			_ = client.Tap(match.Point.X, match.Point.Y)
			time.Sleep(1200 * time.Millisecond) // Wait for popup to open

			confirmScreen, _ := client.CaptureToMat()
			confirmMatches, _ := vision.MatchMultiScaleROI(confirmScreen, confirmTpl, 0.3, 1.5, 60, 0.70, bottomROI)

			if len(confirmMatches) > 0 {
				bestConfirm := confirmMatches[0]
				scale := bestConfirm.Scale
				
				// Define ROI for cost text below Confirm button
				roiConfirmCost := image.Rect(
					bestConfirm.Point.X - int(100 * scale),
					bestConfirm.Point.Y + int(5 * scale),
					bestConfirm.Point.X + int(60 * scale),
					bestConfirm.Point.Y + int(55 * scale),
				)
				
				isRed := isConfirmRed(confirmScreen, roiConfirmCost)
				confirmScreen.Close()
				
				if isRed {
					fmt.Println("    Confirm button cost is RED (unaffordable). Dismissing dialog.")
					_ = client.Back()
					time.Sleep(1500 * time.Millisecond)
					continue
				}

				fmt.Printf("    Found Confirm button! Conf: %.3f at (%d, %d). Tapping to complete...\n", bestConfirm.Confidence, bestConfirm.Point.X, bestConfirm.Point.Y)
				_ = client.Tap(bestConfirm.Point.X, bestConfirm.Point.Y)
				time.Sleep(2000 * time.Millisecond)
				saveScreenshot(client, "step5_after_confirm_click.png")
				success = true
				break
			} else {
				confirmScreen.Close()
				fmt.Println("    Confirm button not found. Dismissing dialog.")
				_ = client.Back()
				time.Sleep(1500 * time.Millisecond) // Wait for dialog to close
			}
		}

		if success {
			fmt.Println("Success! Wall upgrade iteration completed. Repeating loop...")
		} else {
			fmt.Println("Failed! All available upgrade options checked, none were affordable. Ending loop.")
			dismissSelection(client, cal)
			break
		}
	}
}

func saveScreenshot(client *adb.Client, filename string) {
	screen, err := client.CaptureToMat()
	if err == nil {
		gocv.IMWrite(filename, screen)
		screen.Close()
		fmt.Printf("  Saved screenshot to %s\n", filename)
	}
}

func dismissSelection(client *adb.Client, cal *game.Calibration) {
	tx, ty := cal.ScaleRef(50, 450)
	_ = client.Tap(tx, ty)
	time.Sleep(500 * time.Millisecond)
}

func isConfirmRed(screen gocv.Mat, roi image.Rectangle) bool {
	if roi.Min.X < 0 { roi.Min.X = 0 }
	if roi.Min.Y < 0 { roi.Min.Y = 0 }
	if roi.Max.X > screen.Cols() { roi.Max.X = screen.Cols() }
	if roi.Max.Y > screen.Rows() { roi.Max.Y = screen.Rows() }

	sub := screen.Region(roi)
	defer sub.Close()

	// Red BGR bounds: B <= 160, G <= 160, R >= 160
	lowerRed := gocv.NewScalar(0, 0, 160, 0)
	upperRed := gocv.NewScalar(160, 160, 255, 0)

	maskRed := gocv.NewMat()
	defer maskRed.Close()
	gocv.InRangeWithScalar(sub, lowerRed, upperRed, &maskRed)
	redPixels := gocv.CountNonZero(maskRed)

	fmt.Printf("    [Affordability Check] Detected red pixels: %d\n", redPixels)
	return redPixels > 50
}
