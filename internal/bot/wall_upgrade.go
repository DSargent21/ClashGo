package bot

import (
	"encoding/json"
	"image"
	"math"
	"os"
	"time"

	"github.com/rs/zerolog"
	"gocv.io/x/gocv"

	"github.com/Ducky705/ClashGO/internal/game"
	"github.com/Ducky705/ClashGO/internal/paths"
	"github.com/Ducky705/ClashGO/internal/vision"
)

// wallClient is the minimal ADB surface the wall-upgrade sequence needs.
// *adb.Client satisfies this interface. Declared as an interface so the
// diagnostic tool at cmd/test_wall_upgrade can drive the same sequence
// against a real device without instantiating a full Bot.
//
// TapRandomized is requested here for parity with prod Bot.dismissInterruptions,
// which uses Gaussian-distributed taps for some dialog states. Plain Tap is
// still acceptable when the caller doesn't need jitter.
type wallClient interface {
	Tap(x, y int) error
	TapRandomized(x, y int) error
	Swipe(x1, y1, x2, y2 int, ms int) error
	CaptureToMat() (gocv.Mat, error)
	Back() error
}

// Rect is the JSON-friendly shape for picker'd tap regions. Each of the
// three wall-upgrade asset files
//   - assets/wall_upgrade_buttons.json     (top-level: gold, elixir)
//   - assets/wall_upgrade_confirm.json     (top-level: confirm_button)
//   - assets/wall_upgrade_x_roi.json       (top-level: x_popup_roi)
//
// writes a top-level dict with one or more {x1, y1, x2, y2} rects,
// which this struct decodes uniformly.
//
// Coords are PHYSICAL pixels captured at the picker session's actual
// screen size. Per the user's calibration baseline (860x732 on BlueStacks
// Air + adb screencap at the device's native frame), the picker and the
// bot's Cal.ScaleX/ScaleY agree at 1:1, so JSON values land directly in
// bot tap coords without re-scaling. Mismatch on a different device
// frame is a separate concern — fix at picker or bot, not within the
// loader.
//
// The legacy tap pattern in earlier versions of this file used the
// image.Rectangle stdlib type via image.Rect(x1,y1,x2,y2). image.Rectangle
// serializes to JSON as `{"Min":{"X":x,"Y":y},"Max":{"X":x2,"Y":y2}}`,
// which doesn't match the picker's flat x1/y1/x2/y2 output. The Rect
// struct here decodes the flat shape 1:1 with no per-file ad-hoc
// map[string]int shape, so adding a new asset file is one struct-tag
// each.
type Rect struct {
	X1 int `json:"x1"`
	Y1 int `json:"y1"`
	X2 int `json:"x2"`
	Y2 int `json:"y2"`
}

// Center returns ((X1+X2)/2, (Y1+Y2)/2). Integer truncation matches
// adb input.tap's nearest-pixel rounding, so a 153-wide rect lands at
// ±0.5 px off geometric center — visually indistinguishable from the
// user's drag.
func (r Rect) Center() (cx, cy int) {
	return (r.X1 + r.X2) / 2, (r.Y1 + r.Y2) / 2
}

// ImageRect converts to Go's stdlib image.Rectangle for use with
// gocv.Region / image.Rect expected by vision math elsewhere. The
// returned rectangle uses the same Min/Max convention as image.Rect.
func (r Rect) ImageRect() image.Rectangle {
	return image.Rect(r.X1, r.Y1, r.X2, r.Y2)
}

// Empty returns true if the rect has zero area — i.e., zero width
// (X1==X2) OR zero height (Y1==Y2). The picker writes degenerate
// (0,0,0,0) only when handed a broken drag; the loaders use Empty()
// to reject that so the bot's tap path starts with `ok=false` rather
// than emitting a 0-area tap region that would silently tap the
// top-left corner of the screen. The all-zero (0,0,0,0) case is
// covered by the X1==X2 path because (X1==X2 AND Y1==Y2) is a subset
// of (X1==X2 OR Y1==Y2).
func (r Rect) Empty() bool {
	return r.X1 == r.X2 || r.Y1 == r.Y2
}

// WallUpgradeHooks groups the dependencies a wall-upgrade loop iteration
// needs. Used both by Bot.UpgradeWalls (production path) and the
// cmd/test_wall_upgrade diagnostic tool.
//
// Optional fields:
//   - Classify: nil disables state verification + interruption dismissal
//     (the diagnostic tool's "manual" mode assumes the user navigated
//     the game into MainVillage themselves and skips dialog handling).
//   - OnStep:   nil silences all instrumentation events. When wired, each
//     phase boundary calls OnStep("phase_name", data). The diagnostic
//     tool uses this to save annotated screenshots per phase.
//   - Dismiss:  nil makes the loop's fallback skip the neutral-tap
//     dismiss (e.g. when btn_upgrade_wall or btn_confirm_upgrade fail
//     matching). Production wires this to Bot.dismissSelection.
type WallUpgradeHooks struct {
	Logger    zerolog.Logger
	Client    wallClient
	Cal       *game.Calibration
	Templates *game.TemplateStore

	// Classify is invoked with each capture during the MainVillage
	// verify loop and to detect interruption dialogs. May be nil.
	Classify func(gocv.Mat) (game.GameState, int)

	// Dismiss taps a neutral background area to clear any active
	// wall-selection after a failed button-template match. May be nil.
	Dismiss func()

	// OnStep is the optional phase-boundary instrumentation hook.
	OnStep func(step string, data map[string]any)
}

// UpgradeWalls executes the wall-upgrade sequence repeatedly until no
// more affordable options exist. After this refactor it is a thin
// wrapper that delegates to runWallUpgradeLoop with the production
// Bot's dependencies. The diagnostic tool at cmd/test_wall_upgrade
// calls runWallUpgradeLoop directly with a hand-built hooks struct.
func (b *Bot) UpgradeWalls(gc *game.GameContext) {
	RunWallUpgradeLoop(&WallUpgradeHooks{
		Logger:    b.logger,
		Client:    b.client,
		Cal:       b.cal,
		Templates: b.templates,
		Classify:  b.classify,
		Dismiss:   b.dismissSelection,
	})
}

// RunWallUpgradeLoop drives the wall-upgrade sequence with explicit deps
// (parametrised by h). Production wires a Bot-built hooks struct; the
// cmd/test_wall_upgrade diagnostic tool wires its own hooks struct
// against a live adb.Client without instantiating a full *Bot.
//
// Flow selection (in priority order):
//
//  1. ASSET-DRIVEN (BLIND-TAP) — when wall_upgrade_buttons.json +
//     wall_upgrade_confirm.json + wall_upgrade_x_roi.json ALL load
//     cleanly:
//     a. Tap gold rect Center.
//     b. Tap Confirm rect Center (BLIND — no template match).
//     c. Wait + capture. Then check BOTH hasModalInRect(x_popup_roi)
//     AND hasModalInRect(x_popup_roi_alt) (alt only if configured).
//     Chained-popup support per the user's spec: dismissing the
//     gem-buy modal can reveal a SECOND popup whose X lives at
//     x_popup_roi_alt; both must be down before declaring success.
//     d. If ANY popup is still up, single-tap each X in sequence:
//     primary center, then alt center (if configured). ONE tap per
//     X, no retry, no offsets. Per the user's "click X, then click
//     X again in the confirm menu" spec.
//     e. Verify both rects dismissed after the sequence. If either is
//     still up → fall through to the NEXT button on this same wall
//     (gold → elixir per spec "if gold was unsuccessful, click
//     elixir"), emitting primary_still_up / alt_still_up diagnostics
//     so the user knows which rect mis-picked. If BOTH buttons fire
//     this path, the post-button-loop check surfaces the
//     `all_unaffordable` exit and the sequence ends. Wall-level
//     aborts only fire on defensive capture failures (modalErr /
//     verErr — transport sick), not on rect mis-picks.
//     f. Both rects down → silent spawn = success. Move to next button.
//
//  2. PROBE-AND-DISCARD — when only wall_upgrade_buttons.json loads
//     (legacy mode from the original pre-rect refactor):
//     - Tap gold → if btn_confirm_upgrade template matches ∧
//     checkConfirmRed says white → tap template-derived confirm.
//     - Tap gold → confirm missing → silent spawn = success.
//     - Tap gold → confirm shown + checkConfirmRed says red → tap X.
//     - Then loop to elixir with the same logic.
//
//  3. LEGACY TEMPLATE — when no rect assets load:
//     - For each btn_upgrade_wall candidate (multi-scale match):
//     pre-tap cost-color check via costROI, then probe-and-discard
//     with btn_confirm_upgrade template match.
//
// Each phase boundary emits an OnStep event so the diagnostic tool can
// capture + annotate a screenshot at every decision point. Payloads
// follow the Mat-ownership contract documented on WallUpgradeHooks.step.
func RunWallUpgradeLoop(h *WallUpgradeHooks) {
	h.Logger.Info().Msg("Starting wall upgrade sequence...")
	h.step("sequence_start", nil)

	// Load all three rect assets. The "asset-driven" flow requires ALL
	// three to be both well-formed and non-empty. The "probe-and-discard"
	// flow (legacy) only requires wall_upgrade_buttons.json. The
	// "template-matching" fallback requires none.
	//
	// Hoisted out of the for loop so the load log fires exactly once
	// per sequence, not once per iteration. Per the user's "They appear
	// in same spot every time", the JSON content is constant across
	// iterations.
	goldBtn, elixirBtn, hasButtons := loadWallUpgradeButtons(h.Logger)
	confirmRect, hasConfirmRect := loadWallUpgradeConfirmRect(h.Logger)
	xPopupRect, xPopupAlt, hasXPopupRect := loadWallUpgradeXPopupRect(h.Logger)

	hasBlindFlow := hasButtons && hasConfirmRect && hasXPopupRect
	switch {
	case hasBlindFlow:
		h.Logger.Info().Msg("Asset-driven flow: all three rect assets loaded — using blind-confirm + observe-popup")
	case hasButtons:
		h.Logger.Info().Msg("Probe-and-discard flow: only buttons rect loaded — using legacy template + cost-color with X dismiss")
	default:
		h.Logger.Info().Msg("Template-matching fallback: no rect assets loaded — using btn_upgrade_wall template + cost-color")
	}

	for upgradeCount := 1; ; upgradeCount++ {
		h.Logger.Info().Int("count", upgradeCount).Msg("Starting wall upgrade loop iteration")
		h.step("iteration_start", map[string]any{"count": upgradeCount})
		// Reset per-wall: defensive capture failures on wall N
		// must not silently abort wall N+1+. Without this reset
		// inside the upgradeCount loop, a transport hiccup on one
		// wall would leak `aborted=true` into the post-button-loop
		// check of every subsequent wall.
		aborted := false

		// 1. Verify we are in MainVillage state
		ok := waitForMainVillage(h, 30*time.Second)
		if !ok {
			h.Logger.Warn().Msg("Wall upgrade loop stopped: not in main village")
			h.step("not_in_main_village", nil)
			break
		}
		h.step("main_village_verified", nil)

		// 2. Click the builder head button in the top middle
		bx, by := h.Cal.ScaleRef(430, 30)
		h.Logger.Debug().Int("x", bx).Int("y", by).Msg("Clicking builder head icon")
		if err := h.Client.Tap(bx, by); err != nil {
			h.Logger.Error().Err(err).Msg("Failed to tap builder head")
			h.step("tap_builder_failed", map[string]any{"err": err.Error()})
			break
		}
		h.step("builder_tapped", map[string]any{"x": bx, "y": by})
		time.Sleep(1500 * time.Millisecond) // Wait for menu to appear

		// ROI for the upgrades menu (default right side of the screen)
		menuROI := image.Rect(
			int(400*h.Cal.ScaleX),
			int(50*h.Cal.ScaleY),
			int(860*h.Cal.ScaleX),
			int(732*h.Cal.ScaleY),
		)

		// Load custom ROI if it exists (assets/builder_menu_roi.json)
		if roiData, err := os.ReadFile(paths.Resolve("builder_menu_roi.json")); err == nil {
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
				h.Logger.Info().
					Int("x1", menuROI.Min.X).Int("y1", menuROI.Min.Y).
					Int("x2", menuROI.Max.X).Int("y2", menuROI.Max.Y).
					Msg("Loaded custom builder menu ROI")
			}
		}
		h.step("menu_roi_loaded", map[string]any{"roi": menuROI})

		// Cost-text ROI defaults live at iter-body scope so the legacy
		// flow's pre-tap cost-color check (which runs later in the same
		// iteration) can read them. Inside the legacy-template branch we
		// (a) optionally override from assets/wall_upgrade_cost_roi.json
		// (partial overrides apply — see schema docs), and (b) emit a
		// log line so the user can verify which values are in play.
		//
		// The asset-driven + probe-and-discard branches don't consume
		// these values; emitting the log there would be misleading noise.
		costROIXMin, costROIYMin := -50, 30
		costROIXMax, costROIYMax := 50, 80
		customSource := "defaults"
		if !hasBlindFlow && !hasButtons {
			// Cost-text ROI config loaded from
			// assets/wall_upgrade_cost_roi.json. Coordinates are
			// RELATIVE to the btn_upgrade_wall match center and
			// scaled by match.Scale so the band tracks the icon size.
			// The user can drag-adjust by editing the JSON (see
			// assets/wall_upgrade_cost_roi.json for the schema).
			//
			// Partial-overrides apply: any single nonzero field in the
			// JSON takes effect, so the user can drag-nudge one knob at
			// a time without having to fill in all four. Empty /
			// missing fields stay at their hardcoded defaults.
			// All-zero JSONs ({"x_min_off": 0, ...}) are treated as
			// "explicit default" and accepted without complaint.
			if costROIData, err := os.ReadFile(paths.Resolve("wall_upgrade_cost_roi.json")); err == nil {
				var cfg struct {
					XMinOff int `json:"x_min_off"`
					YMinOff int `json:"y_min_off"`
					XMaxOff int `json:"x_max_off"`
					YMaxOff int `json:"y_max_off"`
				}
				if json.Unmarshal(costROIData, &cfg) == nil {
					if cfg.XMinOff != 0 {
						costROIXMin = cfg.XMinOff
					}
					if cfg.YMinOff != 0 {
						costROIYMin = cfg.YMinOff
					}
					if cfg.XMaxOff != 0 {
						costROIXMax = cfg.XMaxOff
					}
					if cfg.YMaxOff != 0 {
						costROIYMax = cfg.YMaxOff
					}
					customSource = "json"
				}
			}
			h.Logger.Info().
				Str("source", customSource).
				Int("x_min", costROIXMin).Int("y_min", costROIYMin).
				Int("x_max", costROIXMax).Int("y_max", costROIYMax).
				Msg("Effective cost-ROI (x_min..x_max, y_min..y_max relative to btn_upgrade_wall match center, scaled by match.Scale)")
		}

		// Compute swipe center X coordinate within the menu ROI to
		// avoid dragging the map background.
		scrollX := menuROI.Min.X + menuROI.Dx()/2
		// Swipe strictly within scrollable bounds to maximize distance
		topMargin := int(30 * h.Cal.ScaleY)
		bottomMargin := int(30 * h.Cal.ScaleY)
		sy1 := menuROI.Max.Y - bottomMargin
		sy2 := menuROI.Min.Y + topMargin

		// 3. Scroll robustly to the dead bottom of the menu
		h.Logger.Debug().Int("scrollX", scrollX).Int("sy1", sy1).Int("sy2", sy2).Msg("Scrolling upgrades menu to the bottom")
		for i := 0; i < 6; i++ {
			if err := h.Client.Swipe(scrollX, sy1, scrollX, sy2, 300); err != nil {
				h.Logger.Error().Err(err).Msg("Failed to swipe menu down")
				h.step("scroll_failed", map[string]any{"err": err.Error(), "iter": i})
				return
			}
			time.Sleep(450 * time.Millisecond)
		}
		time.Sleep(1200 * time.Millisecond) // momentum settle
		h.step("menu_scrolled_to_bottom", nil)

		// 4. Slowly scroll back up and search for "Wall" text
		wallTpl, ok := h.Templates.Get("text_wall")
		if !ok {
			h.Logger.Error().Msg("Wall text template ('text_wall') not loaded, aborting upgrade")
			h.step("wall_template_missing", nil)
			_ = h.Client.Back()
			break
		}

		wallClicked := false
		for attempt := 0; attempt < 12; attempt++ {
			screen, err := h.Client.CaptureToMat()
			if err != nil {
				time.Sleep(500 * time.Millisecond)
				continue
			}
			// Robust 0.78 threshold, 60 scale steps.
			matches, _ := vision.MatchMultiScaleROICached(screen, wallTpl, "text_wall", 0.3, 1.5, 60, 0.78, menuROI)

			h.step("wall_text_search", map[string]any{
				"attempt": attempt,
				"matches": len(matches),
			})

			// Filter out false-positives that landed OUTSIDE the menu ROI
			// even though the matcher was given the ROI as an argument
			// (the ROI is advisory for MatchMultiScale — real returns can
			// still be at y < menuROI.Min.Y, e.g. against the top-bar gold
			// text "1/2" or username area on the village map). The user's
			// freshly-captured text_wall.png will match on a real wall row
			// within the menu bbox; matches below the bottom HUD or above
			// the top bar are not the wall row we're looking for.
			//
			// Note: we deliberately do NOT filter by scale — the user's
			// current 56x12 text_wall.png has padding around a real 22x5
			// letter region, so its natural match lands at scale ~0.40.
			// Filtering by scale >= 0.60 would block all real matches.
			var best *vision.Match
			for _, m := range matches {
				if m.Point.Y < menuROI.Min.Y || m.Point.Y > menuROI.Max.Y ||
					m.Point.X < menuROI.Min.X || m.Point.X > menuROI.Max.X {
					continue
				}
				mCopy := m
				best = &mCopy
				break
			}

			if best != nil {
				h.Logger.Info().
					Float64("conf", best.Confidence).
					Float64("scale", best.Scale).
					Int("x", best.Point.X).
					Int("y", best.Point.Y).
					Msg("Wall text template found")
				hookScreen := screen.Clone()
				if hookScreen.Empty() {
					h.step("wall_text_found", map[string]any{
						"attempt": attempt,
						"conf":    best.Confidence,
						"scale":   best.Scale,
						"x":       best.Point.X,
						"y":       best.Point.Y,
						"matches": matches,
					})
				} else {
					h.step("wall_text_found", map[string]any{
						"attempt": attempt,
						"conf":    best.Confidence,
						"scale":   best.Scale,
						"x":       best.Point.X,
						"y":       best.Point.Y,
						"screen":  hookScreen,
						"matches": matches,
					})
				}
				if err := h.Client.Tap(best.Point.X, best.Point.Y); err == nil {
					wallClicked = true
				}
			}
			screen.Close()

			if wallClicked {
				break
			}

			// Aggressive multiple-traverse swipe up (100px × 12 attempts =
			// 1200px ≈ 3.5x traverses of the 339px menu). Per the user's
			// empirical observation, over-scrolling is harmless (CoC's
			// menu momentum-curve absorbs it) while under-scrolling has
			// repeatedly hidden the wall row inside the matcher's blind
			// spot between animation settle windows. The 12 attempts give
			// the matcher ~36s of wall time to find any row in the scroll
			// envelope; the loop's wallClicked early-break short-circuits
			// on the first hit so a well-populated menu costs no extra
			// time over the prior 6×90 = 540px / 7×90 = 630px runs.
			h.Logger.Debug().Int("scrollX", scrollX).Msg("Wall text not visible, scrolling up...")
			startY := menuROI.Min.Y + menuROI.Dx()/2
			endY := startY + int(100*h.Cal.ScaleY)
			if err := h.Client.Swipe(scrollX, startY, scrollX, endY, 400); err != nil {
				h.Logger.Error().Err(err).Msg("Failed to swipe up")
				h.step("scroll_up_failed", map[string]any{"err": err.Error()})
				break
			}
			time.Sleep(1000 * time.Millisecond)
		}

		if !wallClicked {
			h.Logger.Warn().Msg("Failed to locate Wall text in builder menu, ending sequence")
			h.step("wall_text_not_found", nil)
			// Do NOT call Client.Back() here. The outer loop's runDismiss
			// (paired with `break`) is responsible for menu cleanup.
			// Calling Back here would pop the builder menu out from under
			// the loop, so any retry path that re-enters this loop body
			// would start on the village map and have to re-tap the
			// builder icon again (1.5s settle + 6 scroll-down swipes).
			break
		}
		h.step("wall_clicked", nil)

		// 5. Wait for map camera to focus on the selected wall and the
		// upgrade menu to appear.
		//
		// CoC's wall-tap animation pipeline:
		//   - camera pan to selected wall: ~1s (varies with distance)
		//   - zoom-in on the wall: ~0.5s
		//   - bottom upgrade-tray slide-in: ~0.5-1s
		// On BlueStacks + a fresh wall selection, total is 2-4s depending
		// on lag. The 2.5s baseline below covers the typical case; the
		// retry loop after captures+re-matches adds up to 3 more seconds
		// of headroom for slow pans without permanently slowing down the
		// fast path on a quick UI response.
		time.Sleep(2500 * time.Millisecond)

		// 6. Choose flow: asset-driven (preferred), probe-and-discard,
		// or template-matching fallback.
		if hasBlindFlow {
			// ASSET-DRIVEN: all three rects loaded. Tap blind, observe.
			//
			// Per the user's simplified flow: "It just hits upgrade
			// button, and it either upgrades successfully, or asks to
			// buy with gems (in which case we close)". No pre-check of
			// cost color (jitter on AA'd text + theme drift made that
			// fragile in the prior approach). The success signal is
			// the gem-buy popup NOT appearing after the blind-confirm
			// tap; the failure signal is the popup appearing, in which
			// case we close via the x_popup_roi rect center.
			gcx, gcy := goldBtn.Center()
			ecx, ecy := elixirBtn.Center()
			ccx, ccy := confirmRect.Center()
			xcx, xcy := xPopupRect.Center()
			h.step("asset_driven_taps_loaded", map[string]any{
				"gold_center":    []int{gcx, gcy},
				"elixir_center":  []int{ecx, ecy},
				"confirm_center": []int{ccx, ccy},
				"x_popup_center": []int{xcx, xcy},
			})
			success := false
			type btnInfo struct {
				rect Rect
				name string
			}
			buttons := []btnInfo{
				{rect: goldBtn, name: "gold"},
				{rect: elixirBtn, name: "elixir"},
			}
			for _, btn := range buttons {
				bcx, bcy := btn.rect.Center()
				h.step("asset_driven_tap_upgrade", map[string]any{
					"name": btn.name, "x": bcx, "y": bcy,
				})
				if err := h.Client.Tap(bcx, bcy); err != nil {
					h.Logger.Error().Err(err).Msg("asset-driven upgrade tap failed")
					h.step("asset_driven_upgrade_tap_failed", map[string]any{
						"name": btn.name, "err": err.Error(),
					})
					continue
				}
				time.Sleep(1200 * time.Millisecond)

				// Blind-confirm tap. No template matching, no
				// cost-color check. The post-tap capture below
				// decides success-vs-popup via pixel detection.
				h.step("asset_driven_tap_confirm", map[string]any{
					"name": btn.name, "x": ccx, "y": ccy,
				})
				if err := h.Client.Tap(ccx, ccy); err != nil {
					h.Logger.Error().Err(err).Msg("asset-driven confirm tap failed")
					h.step("asset_driven_confirm_tap_failed", map[string]any{
						"name": btn.name, "err": err.Error(),
					})
					continue
				}
				time.Sleep(1200 * time.Millisecond)

				modalScreen, modalErr := h.Client.CaptureToMat()
				if modalErr != nil {
					h.Logger.Error().Err(modalErr).Msg("asset-driven modal capture failed; defensively tapping x_popup_centers then aborting iteration (single-tap flow has no retry chain to absorb an undismissed popup)")
					// See defensiveDualTapAndLogClose for the rationale on
					// blocking the bot's next-button tap from crashing
					// into an undismissed popup after a capture failure.
					defensiveDualTapAndLogClose(h, xcx, xcy, xPopupAlt, btn.name, "capture_failed_then_defensive_both_x_tapped")
					aborted = true
					break
				} // DUAL-RECT INITIAL CHECK, not just primary. The pre-fix
				// loop only checked xPopupRect at this gate, so a chained
				// popup whose X is at x_popup_roi_alt (visible WITHOUT
				// the gem-buy modal showing) would silently spawn into
				// it. Checking BOTH rects here covers single-modal AND
				// chained cases uniformly; the all_up field in the
				// asset_driven_modal_checked JSONL event is the
				// disjunction that the per-attempt verifier inside the
				// retry loop ALSO emits (same shape, just different
				// gating role).
				primaryUp, primaryPx := hasModalInRect(modalScreen, xPopupRect)
				altUp, altPx := false, 0
				if xPopupAlt != nil {
					altUp, altPx = hasModalInRect(modalScreen, *xPopupAlt)
				}
				allUp := primaryUp || altUp
				hookScreen := modalScreen.Clone()
				h.step("asset_driven_modal_checked", map[string]any{
					"name":       btn.name,
					"primary_up": primaryUp, "primary_px": primaryPx,
					"alt_up": altUp, "alt_px": altPx,
					"all_up": allUp,
					"screen": hookScreen,
				})
				modalScreen.Close()

				if allUp {
					// SINGLE-TAP-EACH-X DISMISS per the user's spec:
					//   "click X, then click X again in the confirm menu".
					// ONE tap per X, no retry, no offsets. The pre-fix
					// retry-with-verification design (5+5=10 candidate
					// cross-pattern taps) was overengineered for what the
					// user actually wants: dismiss ASAP, fall through to
					// the next button / next wall on success, abort
					// cleanly on miss so the user can re-pick.
					//
					// The two X's are dismissed in sequence (primary
					// always first because the gem-buy modal reveals the
					// confirm/secondary menu; then alt if configured).
					// Each tap is followed by a 1s settle so CoC's modal
					// close-fade has time to complete on slow BlueStacks
					// 5.21 macOS frames. After both taps we re-capture
					// once and verify both rects are down; if either is
					// still up the iteration exits with explicit
					// primary_still_up / alt_still_up flags so the user
					// knows which rect mis-picked.
					if primaryUp {
						h.step("asset_driven_modal_close_primary", map[string]any{
							"name": btn.name, "x": xcx, "y": xcy,
						})
						if err := h.Client.Tap(xcx, xcy); err != nil {
							h.Logger.Error().Err(err).Msg("primary X tap failed")
						}
						time.Sleep(1000 * time.Millisecond)
					}
					if xPopupAlt != nil {
						acx, acy := xPopupAlt.Center()
						h.step("asset_driven_modal_close_alt", map[string]any{
							"name": btn.name, "x": acx, "y": acy,
						})
						if err := h.Client.Tap(acx, acy); err != nil {
							h.Logger.Error().Err(err).Msg("alt X tap failed")
						}
						time.Sleep(1000 * time.Millisecond)
					}

					// Final verify: did both popups dismiss?
					verCap, verErr := h.Client.CaptureToMat()
					if verErr != nil {
						h.Logger.Warn().Err(verErr).Msg("verify-capture failed; defensively tapping both X centers then aborting iteration")
						defensiveDualTapAndLogClose(h, xcx, xcy, xPopupAlt, btn.name, "capture_failed")
						aborted = true
						break
					}
					verPrimaryUp, verPrimaryPx := hasModalInRect(verCap, xPopupRect)
					verAltUp, verAltPx := false, 0
					if xPopupAlt != nil {
						verAltUp, verAltPx = hasModalInRect(verCap, *xPopupAlt)
					}
					verAllUp := verPrimaryUp || verAltUp
					hookScreen := verCap.Clone()
					h.step("asset_driven_modal_verified", map[string]any{
						"name":       btn.name,
						"primary_up": verPrimaryUp, "primary_px": verPrimaryPx,
						"alt_up": verAltUp, "alt_px": verAltPx,
						"all_up": verAllUp,
						"screen": hookScreen,
					})
					verCap.Close()

					if verPrimaryUp || verAltUp {
						// Single-tap dismiss on this button did not fully
						// clear its popups. Per the user's "if gold was
						// unsuccessful, click Alexa [elixir]" spec — fall
						// through to the next button on this same wall
						// rather than aborting the wall. If BOTH buttons
						// hit this branch, the post-button-loop check
						// surfaces `all_unaffordable` and the sequence
						// ends. The diagnostic stay in the log step so
						// JSONL traces surface which rect mis-picked.
						h.Logger.Warn().
							Bool("primary_still_up", verPrimaryUp).
							Bool("alt_still_up", verAltUp).
							Msg("X taps did not clear popups on this button; trying next button (gold -> elixir). Re-pick x_popup_roi (if primary_still_up) or x_popup_roi_alt (if alt_still_up).")
						h.step("asset_driven_modal_close_failed", map[string]any{
							"name":             btn.name,
							"reason":           "verify_post_tap_still_up_try_next_button",
							"primary_still_up": verPrimaryUp,
							"alt_still_up":     verAltUp,
						})
						// DELIBERATELY no runDismiss(h) before this continue.
						//
						// The user reported "It does one click too much
						// when exiting out" after gold's close_failed — the
						// bot was firing (50, 450) between gold's X-tap
						// cycle and the elixir attempt, AND on elixir's
						// close_failed the post-button-loop `all_unaffordable`
						// exit was firing ANOTHER (50, 450) ~500ms later for
						// a double-tap-at-exit pattern (two back-to-back
						// taps at sequence_end with no UI benefit between
						// them: trace shows primary_px and alt_px unchanged
						// across the between-buttons dismiss).
						//
						// On the not-last button (gold), the next iteration's
						// modal_checked re-evaluates the dual-rect state, so
						// a between-buttons dismiss was redundant.
						//
						// On the last button (elixir), the post-button-loop
						// `all_unaffordable` path still calls runDismiss(h)
						// once at sequence-end for clean state handoff, so
						// removing this call doesn't lose any cleanup.
						continue
					}

					// Both popups dismissed → fall through to the
					// silent_spawn success marker below.
				}

				// Modal absent = silent spawn = success.
				h.step("asset_driven_silent_spawn", map[string]any{"name": btn.name})
				success = true
				break
			}
			if aborted {
				// This branch fires ONLY for defensive capture failures
				// (modalErr or verErr via defensiveDualTapAndLogClose), NOT
				// for close_failed on a single button — that path now
				// continues to the next button per the user's spec. The
				// diagnostic below therefore frames the issue as a
				// transport/adb connectivity problem, not a rect mis-pick.
				h.Logger.Warn().Msg("Wall upgrade aborted (asset-driven flow): capture transport failure (initial or post-tap screen capture). Likely adb/BlueStacks hiccup — check `adb devices` shows localhost:5555 online and re-run.")
				h.step("aborted_capture_defensive", nil)
				break
			}
			if success {
				h.Logger.Info().Msg("Wall upgrade completed (asset-driven flow). Continuing to next wall...")
				h.step("upgrade_success", nil)
				continue
			}
			h.Logger.Warn().Msg("Wall upgrade failed (asset-driven flow): both gold and elixir options unaffordable. Ending loop.")
			h.step("all_unaffordable", nil)
			runDismiss(h)
			break
		}

		if hasButtons {
			// PROBE-AND-DISCARD FLOW (legacy — only buttons loaded):
			//   1. Tap gold (user-pinned centroid).
			//   2. Read modal:
			//      - If btn_confirm_upgrade absent → silent instant-spawn
			//        (CoC's affordable upgrade path). Set success=true.
			//      - If present + price WHITE → tap confirm. success=true.
			//      - If present + price RED → tap X (top-right of modal)
			//        to dismiss, then loop to elixir.
			//   3. Tap elixir (same logic). If still RED → all_unaffordable,
			//      end loop.
			//
			// Per the user's direction: "Should click gold first, then
			// once in there it scans for number to see if its red, if so
			// dont buy, and click x in top right, then try again with
			// second upgrade button (elexir)".
			//
			// loadWallUpgradeXButton preserves a legacy point-shape asset
			// (assets/wall_upgrade_modal.json) for users who haven't
			// re-picked their X-button as a rect yet. The center of
			// xPopupRect (loaded by loadWallUpgradeXPopupRect) supersedes
			// this when both are present — hasBlindFlow's gate above
			// would already have triggered in that case, so reaching this
			// branch means xPopupRect wasn't a workable Rect.
			xBtnX, xBtnY := loadWallUpgradeXButton(h.Logger, h.Cal.ScaleX, h.Cal.ScaleY)
			// Load btn_confirm_upgrade now (after the wall-pan settle) so
			// log-spam is local to the new flow rather than the for loop.
			// Nil-check is critical: vision.MatchMultiScaleROICached panics
			// on a nil template, so dry-run mode or any test profile that
			// doesn't ship the confirm template would crash the bot.
			confirmTpl, hasConfirmTpl := h.Templates.Get("btn_confirm_upgrade")
			if !hasConfirmTpl {
				h.Logger.Error().Msg("btn_confirm_upgrade template missing in hardcoded flow — falling back to legacy template path")
				h.step("confirm_template_missing", nil)
				runDismiss(h)
				break
			}
			gcx, gcy := goldBtn.Center()
			ecx, ecy := elixirBtn.Center()
			h.step("upgrade_buttons_hardcoded", map[string]any{
				"gold_cx":   gcx,
				"gold_cy":   gcy,
				"elixir_cx": ecx,
				"elixir_cy": ecy,
				"x_btn_x":   xBtnX,
				"x_btn_y":   xBtnY,
			})
			successHardcoded := false
			type btnInfo2 struct {
				rect image.Rectangle
				name string
			}
			buttons := []btnInfo2{
				{rect: goldBtn.ImageRect(), name: "gold"},
				{rect: elixirBtn.ImageRect(), name: "elixir"},
			}
			for _, btn := range buttons {
				cx := btn.rect.Min.X + btn.rect.Dx()/2
				cy := btn.rect.Min.Y + btn.rect.Dy()/2
				h.step("hardcoded_tap_upgrade", map[string]any{
					"name": btn.name, "x": cx, "y": cy,
				})
				if err := h.Client.Tap(cx, cy); err != nil {
					h.Logger.Error().Err(err).Msg("hardcoded upgrade tap failed")
					continue
				}
				time.Sleep(1200 * time.Millisecond)

				modalScreen, modalErr := h.Client.CaptureToMat()
				if modalErr != nil {
					h.Logger.Error().Err(modalErr).Msg("hardcoded modal capture failed; dismissing modal defensively before trying next button")
					// Defensive: if a capture failed mid-modal, a previous
					// tap may have left a modal open. Best-effort tap-X
					// before moving on; ignore the tap error so the bot
					// finishes the iteration regardless. Without this,
					// the next-button tap could land on top of an
					// undismissed prior modal and trigger a chained
					// pop-up.
					_ = h.Client.Tap(xBtnX, xBtnY)
					time.Sleep(1000 * time.Millisecond)
					continue
				}
				bottomROI := image.Rect(0, int(400*h.Cal.ScaleY), modalScreen.Cols(), modalScreen.Rows())
				confirmMatches, _ := vision.MatchMultiScaleROICached(modalScreen, confirmTpl, "btn_confirm_upgrade", 0.3, 1.5, 60, 0.70, bottomROI)

				if len(confirmMatches) == 0 {
					// No confirm dialog → silent spawn = affordable upgrade.
					h.step("hardcoded_silent_spawn", map[string]any{"name": btn.name})
					modalScreen.Close()
					successHardcoded = true
					break
				}

				bestConfirm := confirmMatches[0]
				scl := bestConfirm.Scale
				roiConfirmCost := image.Rect(
					bestConfirm.Point.X-int(100*scl),
					bestConfirm.Point.Y+int(5*scl),
					bestConfirm.Point.X+int(60*scl),
					bestConfirm.Point.Y+int(55*scl),
				)
				isRed := checkConfirmRed(modalScreen, roiConfirmCost, h.Logger)
				hookScreen := modalScreen.Clone()
				h.step("hardcoded_confirm_checked", map[string]any{
					"name":   btn.name,
					"is_red": isRed,
					"screen": hookScreen,
				})
				modalScreen.Close()

				if isRed {
					// Unaffordable: dismiss the modal with X (top-right)
					// then loop to try elixir.
					h.step("hardcoded_confirm_red", map[string]any{"name": btn.name})
					if err := h.Client.Tap(xBtnX, xBtnY); err != nil {
						h.Logger.Error().Err(err).Msg("X-button dismiss tap failed")
					}
					time.Sleep(1000 * time.Millisecond)
					continue
				}

				// Affordable: tap confirm and success.
				h.step("hardcoded_confirm_white", map[string]any{"name": btn.name})
				if err := h.Client.Tap(bestConfirm.Point.X, bestConfirm.Point.Y); err != nil {
					h.Logger.Error().Err(err).Msg("hardcoded confirm tap failed")
				}
				time.Sleep(2000 * time.Millisecond)
				successHardcoded = true
				break
			}
			if successHardcoded {
				h.Logger.Info().Msg("Wall upgrade completed (hardcoded flow). Continuing to next wall...")
				h.step("upgrade_success", nil)
				continue
			}
			h.Logger.Warn().Msg("Wall upgrade failed (hardcoded flow): both gold and elixir options unaffordable. Ending loop.")
			h.step("all_unaffordable", nil)
			runDismiss(h)
			break
		}

		// 7. LEGACY TEMPLATE FLOW: Find btn_upgrade_wall candidates via
		// template match. Triggered when no rect assets loaded — the
		// first-pass asset-driven + probe-and-discard paths above are
		// both unavailable for this iteration. Keeps the bot's pre-rect
		// behavior intact for users who haven't picked the rects yet.
		upgradeTpl, ok := h.Templates.Get("btn_upgrade_wall")
		if !ok {
			h.Logger.Error().Msg("Upgrade button template ('btn_upgrade_wall') not loaded")
			h.step("upgrade_template_missing", nil)
			runDismiss(h)
			break
		}

		// Retry loop: re-capture + re-match at 0.65 threshold gives the
		// bottom-tray slide-up animation up to 5s of headroom. Retry 0
		// runs at +2.5s after the wall-tap (the baseline); each
		// subsequent retry sleeps 1s more before re-capturing. A non-zero
		// match-count breaks early so a quick UI response costs no extra
		// time over the previous single-shot path.
		//
		// Threshold lowered 0.72 → 0.65 because btn_upgrade_wall.png's
		// 220x218 native rendering on BlueStacks peaks at ~0.65-0.72
		// confidence against slightly-AA'd screen pixels; 0.72 was
		// catching only the most-pristine frames and missing the screen
		// captures taken mid-animation (the bottom tray hasn't fully
		// resolved at that point).
		var rawMatches []vision.Match
		var uniqueMatches []vision.Match
		var captureScreen gocv.Mat
		var lastErr error
		// hasCapture tracks whether captureScreen holds a valid gocv.Mat
		// that must be Closed before the next capture. Calling .Empty() on
		// a closed Mat SIGSEGV's via gocv's cgo wrapper — Mat_Empty
		// dereferences a pointer that's been zeroed by .Close(). A
		// separate bool flag is the safe lifecycle pattern; the bot's
		// previous version crashed at line 347 right after the second
		// retry iteration's `if !captureScreen.Empty()` check.
		hasCapture := false
		const upgradeMatchMaxRetries = 3
		for retry := 0; retry < upgradeMatchMaxRetries; retry++ {
			if retry > 0 {
				time.Sleep(1000 * time.Millisecond)
			}
			if hasCapture {
				captureScreen.Close()
				hasCapture = false
			}
			var err error
			captureScreen, err = h.Client.CaptureToMat()
			if err != nil {
				lastErr = err
				h.Logger.Warn().Err(err).Int("retry", retry).Msg("capture during upgrade-button retry failed")
				continue
			}
			hasCapture = true
			bottomROI := image.Rect(0, int(400*h.Cal.ScaleY), captureScreen.Cols(), captureScreen.Rows())
			rawMatches, _ = vision.MatchMultiScaleAllROICached(captureScreen, upgradeTpl, "btn_upgrade_wall", 0.3, 1.5, 60, 0.65, bottomROI)

			// Dedupe at 60px distance.
			uniqueMatches = uniqueMatches[:0]
			for _, m := range rawMatches {
				duplicate := false
				for idx, um := range uniqueMatches {
					distX := m.Point.X - um.Point.X
					distY := m.Point.Y - um.Point.Y
					dist := distX*distX + distY*distY
					if dist < 3600 {
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

			if len(uniqueMatches) > 0 {
				break
			}
		}

		if !hasCapture {
			errMsg := ""
			if lastErr != nil {
				errMsg = lastErr.Error()
			}
			h.Logger.Error().Err(lastErr).Msg("Failed to capture screen for upgrade button check after retries")
			h.step("upgrade_screen_capture_failed", map[string]any{"err": errMsg})
			runDismiss(h)
			break
		}

		h.step("upgrade_buttons_found", map[string]any{"count": len(uniqueMatches)})

		if len(uniqueMatches) == 0 {
			h.Logger.Warn().Msg("Upgrade wall button not found on screen (retry exhausted)")

			// Diagnostic: emit the last captured screen even on failure
			// so the user can eyeball whether the upgrade tray actually
			// appeared. Pair it with a loose-threshold (0.40) match so
			// future JSONL logs surface what the matcher peaked at below
			// the cut-off — distinguishing "tray never appeared"
			// (debug-matches empty) from "tray appeared but template
			// peak was <0.65" (debug-matches shows e.g. 0.62).
			//
			// The 0.40 threshold is intentionally very loose: it's only
			// for diagnostic logging, never used for actual taps, so
			// false-positives here are harmless.
			bottomROI := image.Rect(0, int(400*h.Cal.ScaleY), captureScreen.Cols(), captureScreen.Rows())
			debugMatches, _ := vision.MatchMultiScaleAllROICached(captureScreen, upgradeTpl, "btn_upgrade_wall", 0.3, 1.5, 60, 0.40, bottomROI)
			if captureScreen.Empty() {
				h.step("upgrade_not_found", map[string]any{
					"debug_matches": debugMatches,
				})
			} else {
				hookScreen := captureScreen.Clone()
				h.step("upgrade_not_found", map[string]any{
					"screen":        hookScreen,
					"debug_matches": debugMatches,
				})
			}
			captureScreen.Close()
			runDismiss(h)
			break
		}
		// Matched path: close captureScreen before the candidate iteration
		// starts. The cost-color pre-check below captures a fresh screen per
		// candidate (cheaper than holding one for the whole loop, and the
		// tray's bottom region can shift as the bot does its thing).
		if hasCapture {
			captureScreen.Close()
			hasCapture = false
		}

		// 7. For each candidate: tap → Confirm match → affordability color
		confirmTpl, ok := h.Templates.Get("btn_confirm_upgrade")
		if !ok {
			h.Logger.Error().Msg("Confirm button template ('btn_confirm_upgrade') not loaded")
			h.step("confirm_template_missing", nil)
			runDismiss(h)
			break
		}

		success := false
		for idx, match := range uniqueMatches {
			// PRE-TAP COST-COLOR CHECK: scan a small ROI around the
			// candidate's (x, y) for red pixels (unaffordable price).
			//
			// Per the user's request: "scan for update text (if red
			// don't click, if white do)". This skips the probe-and-
			// discard round-trip (tap → confirm dialog → Back) for
			// candidates whose cost is already red in the in-tray
			// view. CoC places the cost label below the upgrade
			// icon — the ROI is centered on the icon at (x, y) and
			// extends ±60px horizontally, +50 / -10px vertically so
			// it captures the cost-number band at the icon's footer.
			//
			// This is cheaper than the per-candidate tap cycle because
			// we save the rejected tap, the confirm-screen capture,
			// the confirm-red check, the Back() key event, and the
			// 1.5s post-Back settle. On an all-unaffordable run the
			// bot now exits in <500ms instead of several seconds.
			//
			// A captured fresh screen is used (vs reusing
			// captureScreen) because the user can be zooming the map
			// while we look, which would invalidate the post-match
			// capture. Per-candidate capture cost is ~150ms — tight
			// enough that we don't amortize across candidates.
			costScreen, costErr := h.Client.CaptureToMat()
			if costErr == nil {
				scale := match.Scale
				if scale < 0.5 {
					scale = 0.5
				}
				// Cost-ROI is loaded from
				// assets/wall_upgrade_cost_roi.json with defaults
				// baked in if the file is missing. The coordinates
				// are RELATIVE to the btn_upgrade_wall match center
				// and scaled by match.Scale so the band tracks the
				// icon size. Default values place a 100x50 px band
				// centered below the icon (CoC puts the cost label
				// there). Going wider (e.g. ±60 px horizontal)
				// catches neighbour-icon edges and top-bar bleed,
				// producing false red positives in icon borders.
				// The user can drag-adjust by editing the JSON.
				costROI := image.Rect(
					match.Point.X+int(math.Round(float64(costROIXMin)*scale)),
					match.Point.Y+int(math.Round(float64(costROIYMin)*scale)),
					match.Point.X+int(math.Round(float64(costROIXMax)*scale)),
					match.Point.Y+int(math.Round(float64(costROIYMax)*scale)),
				)
				// Clamp to screen bounds.
				if costROI.Min.X < 0 {
					costROI.Min.X = 0
				}
				if costROI.Min.Y < 0 {
					costROI.Min.Y = 0
				}
				if costROI.Max.X > costScreen.Cols() {
					costROI.Max.X = costScreen.Cols()
				}
				if costROI.Max.Y > costScreen.Rows() {
					costROI.Max.Y = costScreen.Rows()
				}
				isCostRed, redPx := checkUpgradeTrayRedWithCount(costScreen, costROI, h.Logger)
				// Promote redPx to JSONL so the user can verify the
				// 30-pixel threshold is appropriate on their screen;
				// logs previously kept redPx at debug level only.
				// Also carry the post-scale ROI dimensions so the
				// user can verify the band is what they configured
				// after the match.Scale multiplication.
				h.step("upgrade_cost_check", map[string]any{
					"btn_idx":    idx,
					"x":          match.Point.X,
					"y":          match.Point.Y,
					"is_red":     isCostRed,
					"red_pixels": redPx,
					"roi_w":      costROI.Dx(),
					"roi_h":      costROI.Dy(),
				})
				costScreen.Close()
				if isCostRed {
					h.Logger.Info().
						Int("btn_idx", idx).
						Int("x", match.Point.X).
						Int("y", match.Point.Y).
						Msg("In-tray upgrade cost is RED (unaffordable). Skipping without tap.")
					h.step("upgrade_unaffordable_skip", map[string]any{
						"btn_idx": idx,
						"x":       match.Point.X,
						"y":       match.Point.Y,
					})
					continue
				}
				h.Logger.Info().
					Int("btn_idx", idx).
					Int("x", match.Point.X).
					Int("y", match.Point.Y).
					Msg("In-tray upgrade cost is WHITE (affordable). Proceeding to tap.")
			} else {
				// Cost-screen capture failed: fall through to the
				// existing tap+confirm-discard loop. The confirm-screen
				// red check will still catch unaffordable buttons here,
				// so this is a graceful degradation, not a regression.
				h.Logger.Warn().Err(costErr).Int("btn_idx", idx).Msg("Cost-screen capture failed; falling back to post-tap confirm-red check (slower path)")
			}

			h.Logger.Info().
				Int("btn_idx", idx).
				Int("x", match.Point.X).
				Int("y", match.Point.Y).
				Float64("conf", match.Confidence).
				Msg("Tapping upgrade button option to check affordability")
			h.step("tap_upgrade_button", map[string]any{
				"btn_idx": idx,
				"x":       match.Point.X,
				"y":       match.Point.Y,
				"conf":    match.Confidence,
			})

			if err := h.Client.Tap(match.Point.X, match.Point.Y); err != nil {
				h.Logger.Error().Err(err).Msg("Failed to tap upgrade button")
				continue
			}
			time.Sleep(1200 * time.Millisecond) // Wait for confirm dialog or gem popup

			confirmScreen, err := h.Client.CaptureToMat()
			if err != nil {
				h.Logger.Error().Err(err).Msg("Failed to capture screen for confirm check")
				_ = h.Client.Back()
				time.Sleep(1500 * time.Millisecond)
				continue
			}

			// ROI for the confirm-button match: bottom half of the screen.
			// Declared here (in candidate-iteration scope) because the
			// retry-loop's captureScreen is closed before the matched
			// path enters this block — the prior one-shot block used a
			// wider-scope bottomROI; the multi-retry refactor moved that
			// declaration into the loop body, so the candidate path
			// re-declares it locally.
			bottomROI := image.Rect(0, int(400*h.Cal.ScaleY), confirmScreen.Cols(), confirmScreen.Rows())
			confirmMatches, _ := vision.MatchMultiScaleROICached(confirmScreen, confirmTpl, "btn_confirm_upgrade", 0.3, 1.5, 60, 0.70, bottomROI)
			if len(confirmMatches) > 0 {
				bestConfirm := confirmMatches[0]
				scale := bestConfirm.Scale

				// ROI for cost text below Confirm button
				roiConfirmCost := image.Rect(
					bestConfirm.Point.X-int(100*scale),
					bestConfirm.Point.Y+int(5*scale),
					bestConfirm.Point.X+int(60*scale),
					bestConfirm.Point.Y+int(55*scale),
				)

				isRed := checkConfirmRed(confirmScreen, roiConfirmCost, h.Logger)
				if confirmScreen.Empty() {
					h.step("confirm_checked", map[string]any{
						"btn_idx": idx,
						"is_red":  isRed,
						"matches": confirmMatches,
					})
				} else {
					hookScreen := confirmScreen.Clone()
					h.step("confirm_checked", map[string]any{
						"btn_idx": idx,
						"is_red":  isRed,
						"screen":  hookScreen,
						"matches": confirmMatches,
					})
				}
				confirmScreen.Close()

				if isRed {
					h.Logger.Info().Msg("Confirm button price is RED (unaffordable). Dismissing dialog.")
					h.step("confirm_red", map[string]any{"btn_idx": idx})
					_ = h.Client.Back()
					time.Sleep(1500 * time.Millisecond)
					continue
				}

				h.Logger.Info().
					Float64("conf", bestConfirm.Confidence).
					Int("x", bestConfirm.Point.X).
					Int("y", bestConfirm.Point.Y).
					Msg("Confirm button found and affordable! Completing upgrade...")
				h.step("confirm_affordable", map[string]any{
					"btn_idx": idx,
					"conf":    bestConfirm.Confidence,
					"x":       bestConfirm.Point.X,
					"y":       bestConfirm.Point.Y,
				})

				if err := h.Client.Tap(bestConfirm.Point.X, bestConfirm.Point.Y); err != nil {
					h.Logger.Error().Err(err).Msg("Failed to tap confirm button")
				}
				time.Sleep(2000 * time.Millisecond)
				success = true
				break
			} else {
				if confirmScreen.Empty() {
					h.step("confirm_missing", map[string]any{"btn_idx": idx})
				} else {
					hookScreen := confirmScreen.Clone()
					h.step("confirm_missing", map[string]any{"btn_idx": idx, "screen": hookScreen})
				}
				confirmScreen.Close()

				// In Clash of Clans, AFFORDABLE wall upgrades DO NOT show
				// a confirm dialog — the upgrade tier icon spawns the
				// build queue instantly the moment it is tapped. The
				// confirm capture above (1.2s after the tap) is past
				// the spawn animation, so btn_confirm_upgrade WILL be
				// missing here even on a successful affordable upgrade.
				//
				// Honor that reality: treat confirm_missing as a
				// silent-upgrade-spawn, skip the Back()/dismiss (the
				// Back keyevent would either reverse the just-spawned
				// build, bring up a 'cancel build?' modal, or be a
				// harmless neutral action — all three are bad outcomes
				// when the spawn already succeeded). Outer loop sets
				// success=true and continues; the next iteration's
				// wall_text_search returns 0 matches when no further
				// wall is upgradable, breaking out cleanly.
				h.step("confirm_silent_spawn", map[string]any{"btn_idx": idx})
				h.Logger.Info().Msg("Upgrade tap fired; no confirm dialog (CoC affordable wall upgrades are silent instant-spawn). Treating as success and skipping Back().")
				success = true
				break
			}
		}

		if success {
			h.Logger.Info().Msg("Wall upgrade completed successfully! Continuing to next wall...")
			h.step("upgrade_success", nil)
		} else {
			h.Logger.Warn().Msg("Failed to upgrade wall: all options checked, none were affordable. Ending loop.")
			h.step("all_unaffordable", nil)
			runDismiss(h)
			break
		}
	}

	h.step("sequence_end", nil)
}

// waitForMainVillage polls the classifier (if provided) until the screen
// shows StateMainVillage or the deadline expires. Mirrors the prior
// 30s-window logic. When Classify is nil (manual mode), returns true
// after the first successful capture — the user is expected to have
// navigated to MainVillage themselves.
func waitForMainVillage(h *WallUpgradeHooks, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		screen, err := h.Client.CaptureToMat()
		if err != nil {
			time.Sleep(500 * time.Millisecond)
			continue
		}
		if h.Classify == nil {
			screen.Close()
			return true
		}
		state, _ := h.Classify(screen)
		screen.Close()
		if state == game.StateMainVillage {
			return true
		}
		dismissInterruptionsFor(h)
		time.Sleep(500 * time.Millisecond)
	}
	return false
}

// dismissInterruptionsFor mirrors Bot.dismissInterruptions but operates
// on the hooks-provided client + classifier. Called from the
// waitForMainVillage loop between captures. When Classify is nil
// (manual mode), no classification → no dismiss attempt.
//
// Uses TapRandomized for StateObstacleDialog / GemDialog / ShieldInfo to
// match prod's Gaussian-distributed taps (adb.Client.TapRandomized
// wraps TapHuman → TapFast with stddev=3.5). Plain Tap is used for
// WelcomeBack's calibrated pinpoint.
func dismissInterruptionsFor(h *WallUpgradeHooks) {
	if h.Classify == nil {
		return
	}
	screen, err := h.Client.CaptureToMat()
	if err != nil {
		return
	}
	state, _ := h.Classify(screen)
	screen.Close()
	switch state {
	case game.StateObstacleDialog:
		_ = h.Client.TapRandomized(400, 300)
		time.Sleep(400 * time.Millisecond)
		_ = h.Client.Back()
	case game.StateGemDialog, game.StateShieldInfo:
		_ = h.Client.TapRandomized(175, 30)
	case game.StateWelcomeBack:
		ox, oy := h.Cal.ScaleRef(430, 520)
		_ = h.Client.Tap(ox, oy)
	case game.StateChatOpen:
		_ = h.Client.Back()
	}
}

// runDismiss invokes the optional hooks.Dismiss to clear a wall-selection
// menu. Safe to call when Dismiss is nil.
func runDismiss(h *WallUpgradeHooks) {
	if h.Dismiss != nil {
		h.Dismiss()
	}
}

// defensiveDualTapAndLogClose handles both capture-failure exit paths in
// the asset-driven single-tap-each-X dismiss flow: the initial modal
// capture failing (`modalErr`) and the final verify-after-taps capture
// failing (`verErr`). Both fire AFTER tapping both X candidates but
// BEFORE we can verify their dismissal, so without this helper the
// next iteration's tap could crash into an undismissed popup.
//
// Centralises the 5-line sequence shared by both sites so they stay in
// lockstep: defensive tap both X centers (conservative — capture failed,
// we don't know which popup is up), emit asset_driven_modal_close_failed
// with both `primary_still_up` and `alt_still_up` set to true, runDismiss.
// The caller is responsible for setting `aborted = true` and `break`ing
// the inner button loop; the helper can't see those loop-control vars.
//
// The two call sites pass their xcx/xcy/xPopupAlt closure values plus
// the per-site reason tag (`capture_failed_then_defensive_both_x_tapped`
// vs `capture_failed`) so JSONL distinguishes which capture path fired.
func defensiveDualTapAndLogClose(h *WallUpgradeHooks, xcx, xcy int, xPopupAlt *Rect, btnName, reason string) {
	_ = h.Client.Tap(xcx, xcy)
	if xPopupAlt != nil {
		acx, acy := xPopupAlt.Center()
		_ = h.Client.Tap(acx, acy)
	}
	time.Sleep(1000 * time.Millisecond)
	h.step("asset_driven_modal_close_failed", map[string]any{
		"name":             btnName,
		"reason":           reason,
		"primary_still_up": true,
		"alt_still_up":     true,
	})
	runDismiss(h)
}

// step emits an instrumentation event when OnStep is wired. The phase
// name is stable so external tools can key on it. When OnStep is nil
// the call is a no-op.
//
// Mat ownership contract: when a `screen` key is present in `data`,
// the value is a *clone* of an in-flight capture and is owned by the
// OnStep receiver — receivers MUST Close it. The caller (this file)
// keeps the original capture alive so receivers don't accidentally
// race the underlying transport-cached buffer.
func (h *WallUpgradeHooks) step(name string, data map[string]any) {
	if h.OnStep == nil {
		return
	}
	h.OnStep(name, data)
}

// hasModalInRect returns (modalUp, brightPixels) where modalUp is
// true when the pixel pattern inside `rect` on the captured screen
// is consistent with a CoC "Buy with Gems" popup being currently
// up, and brightPixels is the raw count used for that decision.
//
// Heuristic: count bright pixels (B >= 200 ∧ G >= 200 ∧ R >= 200 —
// close to white). Inside the user's reported 45×40 px x_popup_roi
// rect the X-button border has a dense cluster of bright pixels;
// the surrounding popup background is dim grey. When the popup is
// not up, this rect typically shows the MainVillage map / bottom
// upgrade tray — far fewer bright pixels.
//
// Threshold of 15 bright pixels (out of ~1800 total inner pixels)
// was chosen at the user's reported 45×40 px ROI size: well above
// the tray's stray highlight density but well below the X-button's
// border brightness (routinely 50+ px on a real popup at this size).
//
// The bright-px count is returned alongside the boolean so callers
// can surface it in OnStep JSONL for live tuning — if a real run
// shows the threshold firing on white-but-not-modal pixels (false
// positive) or not firing on actual X-button pixels (false
// negative), the brightPx count tells the user the right
// threshold without re-running the loop.
//
// Returns (false, 0) for any out-of-bounds rect or empty rect.
func hasModalInRect(screen gocv.Mat, rect Rect) (bool, int) {
	bounds := rect.ImageRect()
	if bounds.Min.X < 0 {
		bounds.Min.X = 0
	}
	if bounds.Min.Y < 0 {
		bounds.Min.Y = 0
	}
	if bounds.Max.X > screen.Cols() {
		bounds.Max.X = screen.Cols()
	}
	if bounds.Max.Y > screen.Rows() {
		bounds.Max.Y = screen.Rows()
	}
	if bounds.Empty() {
		return false, 0
	}
	sub := screen.Region(bounds)
	defer sub.Close()

	// Bright-white pixels: any pixel with B/G/R > 200. CoC's X-button
	// glyph is approximately white-on-grey with anti-aliased edges;
	// 200/200/200 lower bound keeps the threshold above the popup's
	// mid-grey background while staying inside the X-button's lighter
	// pixels. Loose enough to absorb BlueStacks AA variance across
	// renders.
	lower := gocv.NewScalar(200, 200, 200, 0)
	upper := gocv.NewScalar(255, 255, 255, 0)
	mask := gocv.NewMat()
	defer mask.Close()
	gocv.InRangeWithScalar(sub, lower, upper, &mask)
	brightPixels := gocv.CountNonZero(mask)
	return brightPixels > 15, brightPixels
}

// checkConfirmRed inspects the cost-text ROI for red pixels (unaffordable
// price). Same algorithm as the prior Bot.checkConfirmRed; lifted to a
// free function so callers without a *Bot can run the affordability
// check.
//
// Only used by the legacy template-matching flow (when no rect assets
// are loaded). The asset-driven + probe-and-discard flows don't
// consult this — they use post-tap popup detection instead.
func checkConfirmRed(screen gocv.Mat, roi image.Rectangle, logger zerolog.Logger) bool {
	if roi.Min.X < 0 {
		roi.Min.X = 0
	}
	if roi.Min.Y < 0 {
		roi.Min.Y = 0
	}
	if roi.Max.X > screen.Cols() {
		roi.Max.X = screen.Cols()
	}
	if roi.Max.Y > screen.Rows() {
		roi.Max.Y = screen.Rows()
	}

	sub := screen.Region(roi)
	defer sub.Close()

	// Red BGR bounds for the unaffordable price text.
	lowerRed := gocv.NewScalar(0, 0, 160, 0)
	upperRed := gocv.NewScalar(160, 160, 255, 0)

	maskRed := gocv.NewMat()
	defer maskRed.Close()
	gocv.InRangeWithScalar(sub, lowerRed, upperRed, &maskRed)
	redPixels := gocv.CountNonZero(maskRed)

	logger.Debug().Int("red_pixels", redPixels).Msg("Confirm button red pixel check")
	return redPixels > 50
}

// checkUpgradeTrayRedWithCount inspects a small ROI beneath a wall-upgrade
// candidate icon for red pixels indicating an unaffordable in-tray
// price label. Returns (isRed, redPixelCount) so the caller can both
// decide and surface the diagnostic pixel count in JSONL phase logs.
//
// Tuned with a lower red-pixel threshold (30 vs the 50 used by
// checkConfirmRed) and a slightly looser red band (lower bound 150
// vs 160) because the in-tray cost labels are smaller and have more
// anti-aliasing variance than the confirm-dialog prices. If a live
// run shows the threshold firing on white-but-rendered-as-red
// pixels (or not firing on actual red), the red_pixels JSONL field
// lets the user calibrate without re-running the loop.
//
// Only used by the legacy template-matching flow.
func checkUpgradeTrayRedWithCount(screen gocv.Mat, roi image.Rectangle, logger zerolog.Logger) (bool, int) {
	if roi.Min.X < 0 {
		roi.Min.X = 0
	}
	if roi.Min.Y < 0 {
		roi.Min.Y = 0
	}
	if roi.Max.X > screen.Cols() {
		roi.Max.X = screen.Cols()
	}
	if roi.Max.Y > screen.Rows() {
		roi.Max.Y = screen.Rows()
	}

	sub := screen.Region(roi)
	defer sub.Close()

	// Looser red band than checkConfirmRed (lower bound 150 vs 160)
	// to catch slightly-darker reds that BlueStacks AA's at the
	// smaller text scale of the in-tray cost label.
	lowerRed := gocv.NewScalar(0, 0, 150, 0)
	upperRed := gocv.NewScalar(160, 160, 255, 0)

	maskRed := gocv.NewMat()
	defer maskRed.Close()
	gocv.InRangeWithScalar(sub, lowerRed, upperRed, &maskRed)
	redPixels := gocv.CountNonZero(maskRed)

	// Note: red_pixels is also surfaced via the upgrade_cost_check
	// OnStep payload in wall_upgrade.go; this debug log was redundant
	// given that visibility and has been removed.
	return redPixels > 30, redPixels
}

// wallUpgradeButtonsConfig is the JSON schema for
// assets/wall_upgrade_buttons.json. Gold and Elixir are both Rects
// (x1/y1/x2/y2 shape) — the same Rect struct used by the
// wall_upgrade_confirm.json and wall_upgrade_x_roi.json loaders for
// consistent typing across the three wall-upgrade asset files.
//
// Lifecycle: loaded once per sequence at the start of
// RunWallUpgradeLoop. Per the user's "They appear in same spot
// every time", the values are constant for the duration of the
// bot's run.
type wallUpgradeButtonsConfig struct {
	Gold   Rect `json:"gold"`
	Elixir Rect `json:"elixir"`
}

// loadWallUpgradeButtons reads assets/wall_upgrade_buttons.json and
// returns the gold + elixir Rects (physical-pixel coords) plus
// ok=true if both shapes parsed cleanly AND neither rect is the
// all-zero sentinel. When the file is missing or malformed, ok=false
// and the caller falls back to the legacy template path.
//
// The x1/y1/x2/y2 keys must all be present in BOTH gold and elixir —
// partial configs (e.g. only gold set, elixir empty) trigger the
// Empty() guard and reject rather than running with a degenerate
// elixir centroid that would tap into random screen territory.
func loadWallUpgradeButtons(logger zerolog.Logger) (gold, elixir Rect, ok bool) {
	data, err := os.ReadFile(paths.Resolve("wall_upgrade_buttons.json"))
	if err != nil {
		return
	}
	var cfg wallUpgradeButtonsConfig
	if json.Unmarshal(data, &cfg) != nil {
		return
	}
	if cfg.Gold.Empty() || cfg.Elixir.Empty() {
		return
	}
	gold = cfg.Gold
	elixir = cfg.Elixir
	gcx, gcy := gold.Center()
	ecx, ecy := elixir.Center()
	logger.Info().
		Int("gold_cx", gcx).Int("gold_cy", gcy).
		Int("elixir_cx", ecx).Int("elixir_cy", ecy).
		Msg("Loaded hardcoded wall-upgrade button positions (from assets/wall_upgrade_buttons.json)")
	ok = true
	return
}

// wallUpgradeConfirmConfig is the JSON schema for
// assets/wall_upgrade_confirm.json. The single confirm_button key
// holds the Rect where the post-upgrade Confirm dialog lives.
//
// Decoded by loadWallUpgradeConfirmRect for the asset-driven flow
// (gold/elixir → confirm rect Center → blind-confirm tap).
type wallUpgradeConfirmConfig struct {
	ConfirmButton Rect `json:"confirm_button"`
}

// loadWallUpgradeConfirmRect reads assets/wall_upgrade_confirm.json and
// returns the post-upgrade Confirm-button rect (physical-pixel coords)
// plus ok=true if it parsed cleanly and is non-empty.
//
// Absent or malformed JSON returns ok=false; the asset-driven flow
// gate fails and the bot falls through to probe-and-discard or
// template-matching. Empty rect also fails loud — a degenerate
// confirm-button rect would silently tap the screen origin.
func loadWallUpgradeConfirmRect(logger zerolog.Logger) (Rect, bool) {
	var r Rect
	data, err := os.ReadFile(paths.Resolve("wall_upgrade_confirm.json"))
	if err != nil {
		return r, false
	}
	var cfg wallUpgradeConfirmConfig
	if json.Unmarshal(data, &cfg) != nil {
		return r, false
	}
	if cfg.ConfirmButton.Empty() {
		return r, false
	}
	cx, cy := cfg.ConfirmButton.Center()
	logger.Info().
		Int("cx", cx).Int("cy", cy).
		Msg("Loaded hardcoded post-upgrade Confirm rect (from assets/wall_upgrade_confirm.json)")
	return cfg.ConfirmButton, true
}

// wallUpgradeXPopupConfig is the JSON schema for
// assets/wall_upgrade_x_roi.json. The single x_popup_roi key holds
// the Rect where the X-button of the gem-buy popup lives.
//
// Used by the asset-driven flow for BOTH detection (the rect's
// pixel pattern reveals whether the popup is currently up) AND
// close-tap (the rect's Center is where we tap to dismiss).
//
// NOTE: this obsoletes the older `assets/wall_upgrade_modal.json`
// point-shape asset (loaded by loadWallUpgradeXButton). Users can
// leave the old point asset on disk uninstalled — it's no longer
// read by the asset-driven flow, only the legacy probe-and-discard
// flow (which doesn't have the new rect assets loaded) still reads
// it. Loose coupling preserved so the legacy path doesn't silently
// regress.
type wallUpgradeXPopupConfig struct {
	XPopupROI    Rect  `json:"x_popup_roi"`
	XPopupROIAlt *Rect `json:"x_popup_roi_alt,omitempty"`
}

// loadWallUpgradeXPopupRect reads assets/wall_upgrade_x_roi.json and
// returns the gem-buy-popup X-button rect (physical-pixel coords)
// plus an OPTIONAL alternate rect (nil if not configured) plus
// ok=true if the primary parsed cleanly and is non-empty.
//
// Three-argument return shape replaces the prior two-argument `Rect, bool`
// signature to thread the optional XPopupROIAlt through to the retry
// loop. The alt is *Rect so a missing JSON key leaves it nil naturally
// (JSON nil-pointer idiom — json.Unmarshal does not allocate a Rect
// for an absent key).
//
// Retvals:
//   - primary: x_popup_roi rect, used for hasModalInRect detection AND
//     as the first candidate tap point in the retry loop.
//   - alt:     x_popup_roi_alt rect (nullable), appended as 5 more
//     candidate tap points after primary's 5.
//
// Absent primary or malformed JSON returns ok=false; the asset-driven
// flow gate fails and the bot falls through to the probe-and-discard flow.
func loadWallUpgradeXPopupRect(logger zerolog.Logger) (primary Rect, alt *Rect, ok bool) {
	data, err := os.ReadFile(paths.Resolve("wall_upgrade_x_roi.json"))
	if err != nil {
		return
	}
	var cfg wallUpgradeXPopupConfig
	if json.Unmarshal(data, &cfg) != nil {
		return
	}
	if cfg.XPopupROI.Empty() {
		return
	}
	cx, cy := cfg.XPopupROI.Center()
	logger.Info().
		Int("cx", cx).Int("cy", cy).
		Msg("Loaded hardcoded gem-buy popup X ROI (from assets/wall_upgrade_x_roi.json)")
	if cfg.XPopupROIAlt == nil {
		// No alt configured — fine; the dual-rect verifier treats
		// nil as "do not check alt" and the retry chain stays
		// at 5 primary candidates only.
	} else if cfg.XPopupROIAlt.Empty() {
		// Alt present in JSON but degenerate (e.g. {"x1":0,...} or
		// a partially-dragged picker drag). Silently demote to nil
		// so the verifier treats it as "no alt" instead of a zero-area
		// rect that hasModalInRect would always return (false, 0)
		// for — which would mask a real chained popup as non-existent.
		// Loud warning so the user re-picks.
		logger.Warn().Msg("assets/wall_upgrade_x_roi.json: x_popup_roi_alt is degenerate (zero area or partial drag). Ignoring. Re-pick with: ./tools/picker.py -o assets/wall_upgrade_x_roi.json --rect x_popup_roi_alt")
		cfg.XPopupROIAlt = nil
	} else {
		acx, acy := cfg.XPopupROIAlt.Center()
		logger.Info().
			Int("cx", acx).Int("cy", acy).
			Msg("Loaded alternate gem-buy popup X ROI (from assets/wall_upgrade_x_roi.json)")
	}
	return cfg.XPopupROI, cfg.XPopupROIAlt, true
}

// wallUpgradeModalConfig is the JSON schema for assets/wall_upgrade_modal.json
// — the OLD point-shape X-button loader. Kept for backward compat: users
// who haven't re-picked the X as a rect yet still get a usable tap point
// via the probe-and-discard flow.
//
// Superseded by wall_upgrade_x_roi.json + loadWallUpgradeXPopupRect in the
// new asset-driven flow. Both loaders can coexist on disk; the asset-driven
// gate chooses Rect.Center() when all three rects load, the legacy
// point-loader runs only when the x_popup Rect is unavailable.
type wallUpgradeModalConfig struct {
	XButton struct {
		X int `json:"x"`
		Y int `json:"y"`
	} `json:"x_button"`
}

// loadWallUpgradeXButton reads assets/wall_upgrade_modal.json and
// returns the X-button tap position. Defaults to (800, 100) on the
// 860x732 reference resolution (which scales via the calibration) when
// the file is missing or the XButton coords are zero.
//
// Sentinel note: (X==0 || Y==0) is treated as "no override" rather
// than "tap the origin". If the user genuinely wants to tap the
// top-left corner of the modal they can set negative X/Y to break
// out of the sentinel — the calibration tap will land somewhere
// visible anyway.
//
// Only used by the probe-and-discard flow (buttons-only mode).
// loadWallUpgradeXPopupRect's Rect.Center() supersedes this when
// the x_popup rect asset is available.
func loadWallUpgradeXButton(logger zerolog.Logger, scaleX, scaleY float64) (x, y int) {
	x, y = int(800*scaleX), int(100*scaleY)
	data, err := os.ReadFile(paths.Resolve("wall_upgrade_modal.json"))
	if err != nil {
		return
	}
	var cfg wallUpgradeModalConfig
	if json.Unmarshal(data, &cfg) != nil {
		return
	}
	if cfg.XButton.X != 0 || cfg.XButton.Y != 0 {
		x, y = cfg.XButton.X, cfg.XButton.Y
		logger.Info().Int("x", x).Int("y", y).Msg("Loaded custom modal X-button position (from assets/wall_upgrade_modal.json)")
	}
	return
}
