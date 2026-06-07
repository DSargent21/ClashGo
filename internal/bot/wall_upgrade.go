package bot

import (
	"encoding/json"
	"image"
	"os"
	"time"

	"gocv.io/x/gocv"

	"github.com/Ducky705/ClashGo/internal/game"
	"github.com/Ducky705/ClashGo/internal/vision"
)

// UpgradeWalls executes the automated wall upgrade sequence repeatedly until no more affordable options exist.
func (b *Bot) UpgradeWalls(gc *game.GameContext) {
	b.logger.Info().Msg("Starting wall upgrade sequence...")

	for upgradeCount := 1; ; upgradeCount++ {
		b.logger.Info().Int("count", upgradeCount).Msg("Starting wall upgrade loop iteration")

		// 1. Verify we are in MainVillage state
		deadline := time.Now().Add(30 * time.Second)
		ok := false
		for time.Now().Before(deadline) {
			screen, err := b.client.CaptureToMat()
			if err != nil {
				time.Sleep(500 * time.Millisecond)
				continue
			}
			state, _ := b.classify(screen)
			screen.Close()

			if state == game.StateMainVillage {
				ok = true
				break
			}
			b.dismissInterruptions()
			time.Sleep(500 * time.Millisecond)
		}

		if !ok {
			b.logger.Warn().Msg("Wall upgrade loop stopped: not in main village")
			break
		}

		// 2. Click the builder head button in the top middle
		bx, by := b.cal.ScaleRef(430, 30)
		b.logger.Debug().Int("x", bx).Int("y", by).Msg("Clicking builder head icon")
		if err := b.client.Tap(bx, by); err != nil {
			b.logger.Error().Err(err).Msg("Failed to tap builder head")
			break
		}
		time.Sleep(1500 * time.Millisecond) // Wait for menu to appear

		// ROI for the upgrades menu (default right side of the screen)
		menuROI := image.Rect(
			int(400 * b.cal.ScaleX),
			int(50 * b.cal.ScaleY),
			int(860 * b.cal.ScaleX),
			int(732 * b.cal.ScaleY),
		)

		// Load custom ROI if it exists
		if roiData, err := os.ReadFile("assets/builder_menu_roi.json"); err == nil {
			type ROIConfig struct {
				Physical map[string]int `json:"physical"`
			}
			var cfg ROIConfig
			if json.Unmarshal(roiData, &cfg) == nil {
				menuROI = image.Rect(
					cfg.Physical["x1"],
					cfg.Physical["y1"],
					cfg.Physical["x2"],
					cfg.Physical["y2"],
				)
				b.logger.Info().
					Int("x1", menuROI.Min.X).Int("y1", menuROI.Min.Y).
					Int("x2", menuROI.Max.X).Int("y2", menuROI.Max.Y).
					Msg("Loaded custom builder menu ROI from assets/builder_menu_roi.json")
			}
		}

		// Compute swipe center X coordinate within the menu ROI to avoid dragging the map background
		scrollX := menuROI.Min.X + menuROI.Dx()/2
		// Swipe strictly within the scrollable container bounds to maximize swipe distance
		topMargin := int(30 * b.cal.ScaleY)
		bottomMargin := int(30 * b.cal.ScaleY)
		sy1 := menuROI.Max.Y - bottomMargin
		sy2 := menuROI.Min.Y + topMargin

		// 3. Scroll robustly to the dead bottom of the menu (swipe Y from bottom to top 6 times)
		b.logger.Debug().Int("scrollX", scrollX).Int("sy1", sy1).Int("sy2", sy2).Msg("Scrolling upgrades menu to the bottom")
		for i := 0; i < 6; i++ {
			if err := b.client.Swipe(scrollX, sy1, scrollX, sy2, 300); err != nil {
				b.logger.Error().Err(err).Msg("Failed to swipe menu down")
				return
			}
			time.Sleep(450 * time.Millisecond)
		}
		// Wait for scrolling momentum/animation to fully settle
		time.Sleep(1200 * time.Millisecond)

		// 4. Slowly scroll back up and search for "Wall" text
		wallTpl, ok := b.templates.Get("text_wall")
		if !ok {
			b.logger.Error().Msg("Wall text template ('text_wall') not loaded, aborting upgrade")
			b.client.Back()
			break
		}

		wallClicked := false

		for attempt := 0; attempt < 6; attempt++ {
			screen, err := b.client.CaptureToMat()
			if err != nil {
				time.Sleep(500 * time.Millisecond)
				continue
			}

			// Search for Wall text with a robust threshold of 0.78 and 60 scale steps to avoid false positives
			matches, _ := vision.MatchMultiScaleROI(screen, wallTpl, 0.3, 1.5, 60, 0.78, menuROI)
			screen.Close()

			if len(matches) > 0 {
				best := matches[0]
				b.logger.Info().
					Float64("conf", best.Confidence).
					Int("x", best.Point.X).
					Int("y", best.Point.Y).
					Msg("Wall text template found")

				// Click the wall item
				if err := b.client.Tap(best.Point.X, best.Point.Y); err == nil {
					wallClicked = true
					break
				}
			}

			// Scroll up slowly using a precise micro-swipe (140 pixels) to be both fast and prevent skipping
			b.logger.Debug().Int("scrollX", scrollX).Msg("Wall text not visible, scrolling up slowly...")
			startY := menuROI.Min.Y + menuROI.Dx()/2
			endY := startY + int(140*b.cal.ScaleY)
			if err := b.client.Swipe(scrollX, startY, scrollX, endY, 400); err != nil {
				b.logger.Error().Err(err).Msg("Failed to swipe up")
				break
			}
			// Wait 1000ms to ensure the list has stopped moving completely before next capture
			time.Sleep(1000 * time.Millisecond)
		}

		if !wallClicked {
			b.logger.Warn().Msg("Failed to locate Wall text in builder menu, ending sequence")
			b.client.Back() // Close menu
			break
		}

		// 5. Wait for map camera to focus on the selected wall and upgrade menu to show (increased to 2500ms)
		time.Sleep(2500 * time.Millisecond)

		// 6. Find and click the Upgrade Wall button
		upgradeTpl, ok := b.templates.Get("btn_upgrade_wall")
		if !ok {
			b.logger.Error().Msg("Upgrade button template ('btn_upgrade_wall') not loaded")
			b.dismissSelection()
			break
		}

		screen, err := b.client.CaptureToMat()
		if err != nil {
			b.logger.Error().Err(err).Msg("Failed to capture screen for upgrade button check")
			b.dismissSelection()
			break
		}

		// ROI: bottom half of the screen
		bottomROI := image.Rect(0, int(400 * b.cal.ScaleY), screen.Cols(), screen.Rows())
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
			b.logger.Warn().Msg("Upgrade wall button not found on screen")
			b.dismissSelection()
			break
		}

		// 8. Find and click Confirm button template
		confirmTpl, ok := b.templates.Get("btn_confirm_upgrade")
		if !ok {
			b.logger.Error().Msg("Confirm button template ('btn_confirm_upgrade') not loaded")
			b.dismissSelection()
			break
		}

		success := false
		for idx, match := range uniqueMatches {
			b.logger.Info().
				Int("btn_idx", idx).
				Int("x", match.Point.X).
				Int("y", match.Point.Y).
				Float64("conf", match.Confidence).
				Msg("Tapping upgrade button option to check affordability")

			if err := b.client.Tap(match.Point.X, match.Point.Y); err != nil {
				b.logger.Error().Err(err).Msg("Failed to tap upgrade button")
				continue
			}
			time.Sleep(1200 * time.Millisecond) // Wait for confirm dialog or gem purchase popup

			confirmScreen, err := b.client.CaptureToMat()
			if err != nil {
				b.logger.Error().Err(err).Msg("Failed to capture screen for confirm check")
				b.client.Back()
				time.Sleep(1500 * time.Millisecond)
				continue
			}

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
				
				isRed := b.checkConfirmRed(confirmScreen, roiConfirmCost)
				confirmScreen.Close()
				
				if isRed {
					b.logger.Info().Msg("Confirm button price is RED (unaffordable). Dismissing dialog.")
					b.client.Back()
					time.Sleep(1500 * time.Millisecond)
					continue
				}

				b.logger.Info().
					Float64("conf", bestConfirm.Confidence).
					Int("x", bestConfirm.Point.X).
					Int("y", bestConfirm.Point.Y).
					Msg("Confirm button found and affordable! Completing upgrade...")
				
				if err := b.client.Tap(bestConfirm.Point.X, bestConfirm.Point.Y); err != nil {
					b.logger.Error().Err(err).Msg("Failed to tap confirm button")
				}
				time.Sleep(2000 * time.Millisecond)
				success = true
				break
			} else {
				confirmScreen.Close()
				b.logger.Info().Msg("Confirm button not found. Dismissing dialog.")
				b.client.Back()
				time.Sleep(1500 * time.Millisecond) // Wait for dialog to close
			}
		}

		if success {
			b.logger.Info().Msg("Wall upgrade completed successfully! Continuing to next wall...")
		} else {
			b.logger.Warn().Msg("Failed to upgrade wall: all options checked, none were affordable. Ending loop.")
			b.dismissSelection()
			break
		}
	}
}

// checkAffordable inspects the button cost text area for white/light pixels (affordable)
func (b *Bot) checkAffordable(screen gocv.Mat, btnCenter image.Point, scale float64) bool {
	// Precise ROI covering the price digits while excluding the resource icon on the right
	textROI := image.Rect(
		btnCenter.X - int(90 * scale),
		btnCenter.Y - int(75 * scale),
		btnCenter.X + int(40 * scale),
		btnCenter.Y - int(25 * scale),
	)

	// Ensure ROI is in bounds
	if textROI.Min.X < 0 { textROI.Min.X = 0 }
	if textROI.Min.Y < 0 { textROI.Min.Y = 0 }
	if textROI.Max.X > screen.Cols() { textROI.Max.X = screen.Cols() }
	if textROI.Max.Y > screen.Rows() { textROI.Max.Y = screen.Rows() }

	sub := screen.Region(textROI)
	defer sub.Close()

	// White/Light text BGR bounds (high B, G, R values)
	lowerWhite := gocv.NewScalar(200, 200, 200, 0)
	upperWhite := gocv.NewScalar(255, 255, 255, 0)

	maskWhite := gocv.NewMat()
	defer maskWhite.Close()
	gocv.InRangeWithScalar(sub, lowerWhite, upperWhite, &maskWhite)
	whitePixels := gocv.CountNonZero(maskWhite)

	// Red/Alert text BGR bounds (high R, low-medium G, low-medium B values for orange-red game text)
	lowerRed := gocv.NewScalar(0, 0, 160, 0)
	upperRed := gocv.NewScalar(140, 150, 255, 0)

	maskRed := gocv.NewMat()
	defer maskRed.Close()
	gocv.InRangeWithScalar(sub, lowerRed, upperRed, &maskRed)
	redPixels := gocv.CountNonZero(maskRed)

	b.logger.Debug().
		Int("white_pixels", whitePixels).
		Int("red_pixels", redPixels).
		Msg("Affordability color check")

	// If there are 30 or more red pixels matching relaxed bounds, the price is red (cannot afford)
	return redPixels < 30 && whitePixels > 40
}

// dismissSelection taps in the background/empty space to close any active selection menus
func (b *Bot) dismissSelection() {
	// Tap a safe neutral area at the bottom left to clear selection
	tx, ty := b.cal.ScaleRef(50, 450)
	_ = b.client.Tap(tx, ty)
	time.Sleep(500 * time.Millisecond)
}

func (b *Bot) checkConfirmRed(screen gocv.Mat, roi image.Rectangle) bool {
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

	b.logger.Debug().Int("red_pixels", redPixels).Msg("Confirm button red pixel check")
	return redPixels > 50
}
