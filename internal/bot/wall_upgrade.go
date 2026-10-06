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
// Coords are REFERENCE-frame (860x732) pixels: the pickers write the pick's
// `reference` block as `physical / scale`, and the shipped wall-upgrade assets
// were picked at 860x732, where that division is the identity. They are mapped
// into live geometry by Rect.Live at load time (see that method for the law and
// for what consuming them raw cost on a 1280x720 device).
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

// assetRect is one rectangle inside a wall-upgrade asset file. Two shapes are
// on disk:
//
//   - the FLAT REFERENCE shape the shipped files use — {"x1":393,"y1":568,…} —
//     written by the pickers of the day, which recorded the reference
//     coordinates of the pick directly;
//   - the TWO-BLOCK shape cmd/refmap writes — {"physical":{…},
//     "reference":{…}} — where `reference` is the coordinate the bot's HUD law
//     maps back onto the box the user dragged, and `physical` is that drag, kept
//     beside it so a human can see both.
//
// A `reference` block wins whenever present: it is the one the picking tool
// verified round-trips onto the drag, while a flat file is only correct for
// whatever geometry it was picked at. A file carrying `physical` alone is
// rejected rather than misread — those are live-device pixels and the loaders
// would treat them as reference ones, which is exactly the mismatch that put
// this flow's blind taps on builder-menu rows on the live 1280x720 run.
//
// The legacy point shape ({"x":551,"y":540}) is accepted too, as a 1-pixel box,
// because tools/picker.py's confirm preset still writes one and a point and a
// degenerate rect identify the same tap.
type assetRect struct {
	rect Rect
	// source names the shape the value came from, for the loader logs.
	source string
}

func (a *assetRect) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	if block, ok := raw["reference"]; ok {
		var r Rect
		if err := json.Unmarshal(block, &r); err != nil {
			return err
		}
		*a = assetRect{rect: r, source: "reference"}
		return nil
	}
	if _, ok := raw["physical"]; ok {
		*a = assetRect{source: "physical-only"}
		return nil
	}
	if _, ok := raw["x"]; ok {
		var pt struct {
			X int `json:"x"`
			Y int `json:"y"`
		}
		if err := json.Unmarshal(data, &pt); err != nil {
			return err
		}
		*a = assetRect{rect: Rect{X1: pt.X, Y1: pt.Y, X2: pt.X + 1, Y2: pt.Y + 1}, source: "flat-reference"}
		return nil
	}
	var r Rect
	if err := json.Unmarshal(data, &r); err != nil {
		return err
	}
	*a = assetRect{rect: r, source: "flat-reference"}
	return nil
}

// Empty reports a rect the loader must refuse: a missing/zero box, or a
// physical-only asset that cannot be mapped through the HUD law.
func (a assetRect) Empty() bool {
	return a.source == "physical-only" || a.rect.Empty()
}

// Live maps a rect from the authored frame the pickers wrote it in into the
// live geometry, so a device that is not the reference geometry taps its
// widgets and not the map next to them.
//
// Every rect in this family (wall_upgrade_buttons.json, ..._confirm.json,
// ..._x_roi.json) holds REFERENCE-frame (860x732) coordinates. Both pickers that
// write them derive a `reference` block as `physical / scale`
// (tools/picker.py's menu preset, tools/internal/select_builder_roi.py), and the
// shipped wall_upgrade_*.json files carry physical == reference — the values were
// picked at 860x732, where that division is the identity. The tap path used to
// consume those numbers RAW, which is right only while the device still runs the
// reference geometry. On the live 1280x720 device (pinned k = 1.325,
// device.display_scale) raw consumption put the resource buttons ~210 px and the
// gem-buy X ~110 px from their widgets, i.e. the blind taps fired into the
// village instead of the tray — the failure mode docs/RESOLUTION.md lists under
// "Consequences of flipping 860x732 -> 1280x720 with only a config change".
//
// The law is the HUD one (Calibration.HudRect): the widget keeps its distance to
// the edge it sits against — the tray buttons and the popup X are bottom- and
// right/centre-anchored chrome — and its size grows with k. It is the identity at
// the reference geometry and for a nil Calibration, which is what keeps the
// authored 1:1 dataset and the small-frame test fixtures valid unchanged.
func (r Rect) Live(cal *game.Calibration) Rect {
	if cal == nil || cal.IsReferenceGeometry() {
		return r
	}
	m := cal.HudRect(r.ImageRect())
	return Rect{X1: m.Min.X, Y1: m.Min.Y, X2: m.Max.X, Y2: m.Max.Y}
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
//     tool uses this to save annotated screenshots per phase. Frames are
//     only cloned for a wired receiver (see stepScreen).
//   - Sleep:    nil means time.Sleep. A no-op injection lets the test suite
//     run a full sequence in milliseconds.
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

	// Classifier is the classifier DismissOverlay verifies an interruption
	// against before moving it (see game/dismiss.go). May be nil; a nil
	// classifier makes interruption dismissal a no-op rather than a blind tap,
	// which is the safe direction. Production wires the bot's own classifier.
	Classifier *game.Classifier

	// Dismiss taps a neutral background area to clear any active
	// wall-selection after a failed button-template match. May be nil.
	Dismiss func()

	// StopCheck is consulted at the top of every wall-upgrade
	// iteration; when it returns true the loop breaks immediately
	// (between walls, never mid-tap). May be nil. Production wires
	// `func() bool { return b.ctx.Err() != nil }` so a user Stop
	// interrupts an otherwise-unbounded wall-upgrade run.
	StopCheck func() bool

	// OnStep is the optional phase-boundary instrumentation hook.
	OnStep func(step string, data map[string]any)

	// Sleep is the sequence's timing primitive. nil means time.Sleep, which
	// is the production behaviour. A caller (the test suite) may inject a
	// no-op so a full pass costs milliseconds instead of the ~20s of
	// animation/ADB settle the sequence is tuned for on a real device.
	Sleep func(time.Duration)
}

// sleep waits for d, or delegates to the injected Sleep hook when one is
// wired. Every wait in the sequence goes through here so the timing budget can
// be replaced in one place instead of at two dozen phase boundaries.
func (h *WallUpgradeHooks) sleep(d time.Duration) {
	if h.Sleep != nil {
		h.Sleep(d)
		return
	}
	time.Sleep(d)
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
		// Same classifier the bot's own dismiss path uses, so the wall loop's
		// interruption handling is the same evidence-gated code (no per-dialog
		// coordinate tables living in two places).
		Classifier: b.classifier,
		Dismiss:    b.dismissSelection,
		// StopCheck lets a user Stop interrupt the otherwise-unbounded
		// wall-upgrade loop at its next iteration boundary.
		StopCheck: func() bool { return b.ctx.Err() != nil },
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
//     a0. Capture ONE pre-tap baseline frame and gate on it:
//     locateWallTray must find a painted chip inside both mapped
//     gold/elixir rects (a builder-menu row drawn at those
//     coordinates has white text on a dark panel, zero
//     saturated pixels, and fails the gate), otherwise the
//     iteration ends with `wall_tray_absent` and fires NO input.
//     The same frame records each X rect's red-pixel level, so
//     every later popup check asks for GROWTH over it as well as
//     the absolute floor (xPopupPresent) rather than brightness
//     alone.
//     a. Tap the LOCATED gold chip Center (mapped rect when the
//     frame had nothing better to offer).
//     b. Capture the confirm page and require the CONFIRM control
//     to be DRAWN inside the mapped confirm rect (a green face
//     with a white caption, confirmButtonDrawn). If it is not
//     there the tap is skipped — no input — and the next button
//     is tried.
//     c. Tap the confirm rect Center, wait + capture. Then check
//     BOTH xPopupPresent(x_popup_roi) AND
//     xPopupPresent(x_popup_roi_alt) (alt only if configured).
//     The alt tap that follows is evidence-gated on its own fresh
//     capture, so a clean primary popup no longer drags a blind
//     tap onto whatever the alt rect happens to cover.
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
//     so the user knows which rect mis-picked. If the dismissal
//     SUCCEEDED the button is unaffordable too — a popup that had to
//     be closed is the "this would cost gems" signal, not an upgrade
//     — so that also falls through to the next button, logged as
//     `asset_driven_unaffordable_dismissed`. Once both buttons are
//     unaffordable the post-button-loop check surfaces the
//     `all_unaffordable` exit and the sequence ends. Wall-level
//     aborts only fire on defensive capture failures (modalErr /
//     verErr — transport sick), not on rect mis-picks.
//     f. No popup at all → silent spawn = success. Move to next wall.
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
	goldBtn, elixirBtn, hasButtons := loadWallUpgradeButtons(h.Cal, h.Logger)
	confirmRect, hasConfirmRect := loadWallUpgradeConfirmRect(h.Cal, h.Logger)
	xPopupRect, xPopupAlt, hasXPopupRect := loadWallUpgradeXPopupRect(h.Cal, h.Logger)
	builderX, builderY, hasBuilderButton := loadWallUpgradeBuilderButton(h.Cal, h.Logger)

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
		// Stop check: the loop is otherwise unbounded (it only exits
		// when no affordable wall remains). Breaking at the iteration
		// boundary keeps a user Stop from leaving the bot tapping
		// walls for minutes.
		if h.StopCheck != nil && h.StopCheck() {
			h.Logger.Info().Msg("Wall upgrade loop stopped by stop signal")
			h.step("stopped", nil)
			break
		}
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

		// 2. Click the builder head button in the top middle. The authored
		// reference point is the fallback; a picked assets/builder_button.json
		// (the picking session's first step) wins when it is present, because the
		// top bar's chrome is the part of this sequence most likely to move.
		bx, by := h.Cal.Hud(430, 30)
		builderSource := "authored-reference"
		if hasBuilderButton {
			bx, by = builderX, builderY
			builderSource = "assets/builder_button.json"
		}
		h.Logger.Debug().Int("x", bx).Int("y", by).Str("source", builderSource).Msg("Clicking builder head icon")
		if err := h.Client.Tap(bx, by); err != nil {
			h.Logger.Error().Err(err).Msg("Failed to tap builder head")
			h.step("tap_builder_failed", map[string]any{"err": err.Error()})
			break
		}
		h.step("builder_tapped", map[string]any{"x": bx, "y": by, "source": builderSource})
		h.sleep(1500 * time.Millisecond) // Wait for menu to appear

		// ROI for the upgrades menu (default right side of the screen)
		menuROI := image.Rect(
			int(h.Cal.Length(400)),
			int(h.Cal.Length(50)),
			int(h.Cal.Length(860)),
			int(h.Cal.Length(732)),
		)

		// Load custom ROI if it exists (assets/builder_menu_roi.json)
		//
		// The pickers write the rect as a `reference` block (the authored
		// 860x732 frame, `physical / scale`) plus the raw `physical` one. The
		// reference block is the one that is comparable across geometries, so
		// it wins when present, and either way the values are mapped into the
		// live frame through the HUD law: a menu panel's edges are anchored to
		// the edges they sit against, so each edge is mapped on its own band
		// (the panel's top is top-anchored just under the top bar while its
		// bottom is mid-screen, and mapping both with the centre's band would
		// stretch the panel's top edge to y=0). Consuming the block raw, as
		// this used to, searched the reference-frame ROI on a 1280x720 device —
		// 170 px left of where the menu is, where `text_wall` can never match.
		if roiData, err := os.ReadFile(paths.Resolve("builder_menu_roi.json")); err == nil {
			type ROIConfig struct {
				Physical  map[string]int `json:"physical"`
				Reference map[string]int `json:"reference"`
			}
			var cfg ROIConfig
			// A malformed file must be reported, not swallowed. The record is a
			// pair of rect blocks at the top level; a picking tool that wrapped the
			// pair under an extra `physical` key produces JSON that unmarshals into
			// nothing here, and silently keeping the default ROI would look like a
			// working run.
			if err := json.Unmarshal(roiData, &cfg); err != nil {
				h.Logger.Error().Err(err).
					Str("want", `{"reference":{"x1":0,"y1":0,"x2":0,"y2":0},"physical":{...}}`).
					Msg("assets/builder_menu_roi.json is malformed; keeping the default menu ROI")
			} else {
				src, source := cfg.Reference, "reference"
				if _, ok := src["x1"]; !ok {
					src, source = cfg.Physical, "physical"
				}
				if _, ok := src["x1"]; !ok {
					h.Logger.Error().
						Str("want", `{"reference":{"x1":0,"y1":0,"x2":0,"y2":0}}`).
						Msg("assets/builder_menu_roi.json holds no usable rect block; keeping the default menu ROI")
				}
				if _, ok := src["x1"]; ok {
					x1, y1 := h.Cal.Hud(src["x1"], src["y1"])
					x2, y2 := h.Cal.Hud(src["x2"], src["y2"])
					menuROI = image.Rect(min(x1, x2), min(y1, y2), max(x1, x2), max(y1, y2))
					h.Logger.Info().
						Str("block", source).
						Int("ref_x1", src["x1"]).Int("ref_y1", src["y1"]).
						Int("ref_x2", src["x2"]).Int("ref_y2", src["y2"]).
						Int("x1", menuROI.Min.X).Int("y1", menuROI.Min.Y).
						Int("x2", menuROI.Max.X).Int("y2", menuROI.Max.Y).
						Msg("Loaded builder menu ROI (ref coords mapped to live geometry)")
				}
			}
		}
		// Keep the ROI inside the live frame. The reference-frame default spans
		// ref y 50..732, i.e. past the bottom of a 720-tall frame, which would
		// put the scroll margins off-screen (sy1 would clamp at the frame edge
		// and the swipe would run outside its own ROI).
		if h.Cal.PhysicalW > 0 && h.Cal.PhysicalH > 0 {
			menuROI = image.Rect(
				max(0, menuROI.Min.X), max(0, menuROI.Min.Y),
				min(h.Cal.PhysicalW, menuROI.Max.X), min(h.Cal.PhysicalH, menuROI.Max.Y),
			)
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
		topMargin := int(h.Cal.Length(30))
		bottomMargin := int(h.Cal.Length(30))
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
			h.sleep(450 * time.Millisecond)
		}
		h.sleep(1200 * time.Millisecond) // momentum settle
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
				h.sleep(500 * time.Millisecond)
				continue
			}
			// Robust 0.78 threshold, 60 scale steps.
			matches, _ := vision.MatchMultiScaleROICached(screen, wallTpl, "text_wall", 0.3, 1.5, 20, 0.78, menuROI)

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
				h.stepScreen("wall_text_found", screen, map[string]any{
					"attempt": attempt,
					"conf":    best.Confidence,
					"scale":   best.Scale,
					"x":       best.Point.X,
					"y":       best.Point.Y,
					"matches": matches,
				})
				// Tap from a FRESH frame: the menu keeps gliding for a second or more
				// after the last swipe, so the row under a match computed from an
				// older capture can move ~40 px before the tap fires. On the live
				// 1280x720 run the ~100 ms between "Wall x127" matching at (504,251)
				// and the tap put the tap on the "Hero Hunter" row above it and
				// opened that unit's confirm dialog instead of the wall tray.
				tapped, tapData := tapWallRow(h, wallTpl, menuROI, *best, wallRowTapWindow)
				h.stepScreen("wall_row_tap", screen, tapData)
				screen.Close()
				wallClicked = tapped
			} else {
				screen.Close()
			}

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
			endY := startY + int(h.Cal.Length(100))
			if err := h.Client.Swipe(scrollX, startY, scrollX, endY, 400); err != nil {
				h.Logger.Error().Err(err).Msg("Failed to swipe up")
				h.step("scroll_up_failed", map[string]any{"err": err.Error()})
				break
			}
			h.sleep(1000 * time.Millisecond)
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
		h.sleep(2500 * time.Millisecond)

		// 6. Choose flow: asset-driven (preferred), probe-and-discard,
		// or template-matching fallback.
		if hasBlindFlow {
			// ASSET-DRIVEN: all three rects loaded. Gate, THEN tap, then observe.
			//
			// Per the user's simplified flow: "It just hits upgrade
			// button, and it either upgrades successfully, or asks to
			// buy with gems (in which case we close)". No pre-check of
			// cost color (jitter on AA'd text + theme drift made that
			// fragile in the prior approach). The success signal is
			// the gem-buy popup NOT appearing after the blind-confirm
			// tap; the failure signal is the popup appearing, in which
			// case we close via the x_popup_roi rect center.
			//
			// One pre-tap baseline frame feeds both gates below, and both
			// refuse to fire input when they cannot prove the tray is up:
			//
			//   1. locateWallTray — the mapped gold/elixir chip rects must
			//      hold PAINTED faces in the live frame. Without this gate
			//      the mapped centres tap whatever sits at those
			//      coordinates, which on the live 1280x720 run was the
			//      builder menu's row band: the mapped gold centre
			//      (641,552) opened an "Upgrade Hero Hunter to Level 4?"
			//      panel and the mapped elixir centre opened a storage
			//      detail panel. A tap that lands on a menu row activates
			//      unrelated UI, which is strictly worse than stopping.
			//   2. xPopupPresent — every later popup check compares against
			//      the baseline count for the same rect, because a rect
			//      parked on permanent chrome (x_popup_roi_alt over the
			//      dark-elixir readout) would otherwise testify "popup up"
			//      on every verify and turn both buttons into
			//      asset_driven_modal_close_failed. The signal itself is the
			//      popup's RED close control, not brightness: the confirm
			//      page's own bright panel covers x_popup_roi on this client,
			//      which is what made an affordable wall look unaffordable.
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

			// Pre-tap baseline. A capture failure here aborts the iteration
			// without any input: with no baseline there is no tray gate and no
			// popup reference, and the whole point of this block is to stop
			// tapping blind when the screen cannot be read.
			baseScreen, baseErr := h.Client.CaptureToMat()
			if baseErr != nil {
				h.Logger.Error().Err(baseErr).Msg("asset-driven baseline capture failed; firing no input (no frame to gate the tray or to verify a popup against)")
				h.step("asset_driven_baseline_capture_failed", map[string]any{"err": baseErr.Error()})
				break
			}
			basePopupRed := redFacePixelsIn(baseScreen, xPopupRect)
			baseAltRed := 0
			if xPopupAlt != nil {
				baseAltRed = redFacePixelsIn(baseScreen, *xPopupAlt)
			}
			gate := locateWallTray(baseScreen, goldBtn, elixirBtn)
			gateData := gate.Data()
			gateData["base_popup_red"] = basePopupRed
			gateData["base_alt_red"] = baseAltRed
			h.stepScreen("asset_driven_tray_gate", baseScreen, gateData)
			baseScreen.Close()

			if !gate.Present() {
				h.Logger.Warn().
					Int("gold_face_px", gate.GoldFacePx).
					Int("elixir_face_px", gate.ElixirFacePx).
					Bool("gold_found", gate.GoldFound).
					Bool("elixir_found", gate.ElixirFound).
					Msg("Wall-upgrade tray is not on screen: the mapped gold/elixir chip rects hold no painted chip (bright-saturated pixels), so the sequence is stopping instead of blind-tapping whatever is there. Re-pick the chip rects with the game parked in the wall tray: ./tools/picker.py --preset buttons")
				h.step("wall_tray_absent", nil)
				runDismiss(h)
				break
			}
			if xPopupAlt != nil && baseAltRed >= popupRedPixelsMin {
				// Evidence, not a refusal: the growth gate still keeps this rect
				// quiet, but a chained popup landing on it would go undetected, so
				// say so while the run is happening rather than after a failure.
				h.Logger.Warn().
					Int("base_alt_red", baseAltRed).
					Msg("x_popup_roi_alt already reads as a popup's close control on the pre-tap baseline (it overlaps permanent screen chrome, e.g. a red HUD element). It can no longer witness a chained popup. Re-pick it: ./tools/picker.py -o assets/wall_upgrade_x_roi.json --rect x_popup_roi_alt")
			}

			success := false
			type btnInfo struct {
				rect   Rect
				mapped Rect
				name   string
			}
			buttons := []btnInfo{
				{rect: gate.GoldChip, mapped: goldBtn, name: "gold"},
				{rect: gate.ElixirChip, mapped: elixirBtn, name: "elixir"},
			}
			for _, btn := range buttons {
				bcx, bcy := btn.rect.Center()
				h.step("asset_driven_tap_upgrade", map[string]any{
					"name": btn.name, "x": bcx, "y": bcy,
					"mapped": btn.mapped.ImageRect(),
				})
				if err := h.Client.Tap(bcx, bcy); err != nil {
					h.Logger.Error().Err(err).Msg("asset-driven upgrade tap failed")
					h.step("asset_driven_upgrade_tap_failed", map[string]any{
						"name": btn.name, "err": err.Error(),
					})
					continue
				}
				h.sleep(1200 * time.Millisecond)

				// Pre-confirm evidence, read from the frame this button's
				// tap produced: the CONFIRM control has to be DRAWN inside
				// the mapped rect. The shipped confirm asset mapped 94 live
				// px above this client's button, so the old blind tap pressed
				// the dialog's flavour text, nothing upgraded, and the flow
				// then reported a silent spawn on a wall nobody had upgraded.
				// Skipping the tap when the control is not there is the same
				// rule the tray gate applies to the chips.
				pageScreen, pageErr := h.Client.CaptureToMat()
				if pageErr != nil {
					h.Logger.Error().Err(pageErr).Msg("asset-driven confirm-page capture failed; firing no input for this button")
					h.step("asset_driven_confirm_page_capture_failed", map[string]any{"name": btn.name, "err": pageErr.Error()})
					aborted = true
					break
				}
				confirmDrawn, confirmBright, confirmGreen := confirmButtonDrawn(pageScreen, confirmRect)
				h.stepScreen("asset_driven_confirm_page", pageScreen, map[string]any{
					"name":           btn.name,
					"confirm_drawn":  confirmDrawn,
					"confirm_bright": confirmBright,
					"confirm_green":  confirmGreen,
					"confirm_rect":   confirmRect.ImageRect(),
				})
				pageScreen.Close()

				if !confirmDrawn {
					h.Logger.Warn().
						Int("confirm_bright", confirmBright).
						Int("confirm_green", confirmGreen).
						Str("button", btn.name).
						Msg("The confirm dialog's button is not drawn inside the mapped confirm rect (no green face with a white caption there), so this button's confirm tap is being skipped instead of pressing whatever sits at those coordinates. Re-pick it: ./tools/picker.py -o assets/wall_upgrade_confirm.json --rect confirm_button")
					h.step("asset_driven_confirm_button_absent", map[string]any{
						"name":           btn.name,
						"confirm_bright": confirmBright,
						"confirm_green":  confirmGreen,
						"confirm_rect":   confirmRect.ImageRect(),
					})
					continue
				}

				// Confirm tap. The mapped centre is used as-is now that the
				// frame proves the control is drawn there; the post-tap
				// capture below decides success-vs-popup.
				h.step("asset_driven_tap_confirm", map[string]any{
					"name": btn.name, "x": ccx, "y": ccy,
					"confirm_rect": confirmRect.ImageRect(),
				})
				if err := h.Client.Tap(ccx, ccy); err != nil {
					h.Logger.Error().Err(err).Msg("asset-driven confirm tap failed")
					h.step("asset_driven_confirm_tap_failed", map[string]any{
						"name": btn.name, "err": err.Error(),
					})
					continue
				}
				h.sleep(1200 * time.Millisecond)

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
				primaryUp, primaryRed := xPopupPresent(modalScreen, xPopupRect, basePopupRed)
				altUp := false
				altRed := 0
				if xPopupAlt != nil {
					altUp, altRed = xPopupPresent(modalScreen, *xPopupAlt, baseAltRed)
				}
				allUp := primaryUp || altUp
				h.stepScreen("asset_driven_modal_checked", modalScreen, map[string]any{
					"name":       btn.name,
					"primary_up": primaryUp, "primary_red": primaryRed,
					"alt_up": altUp, "alt_red": altRed,
					"all_up": allUp,
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
						h.sleep(1000 * time.Millisecond)
					}
					if xPopupAlt != nil {
						// Evidence-gated, and on a FRESH capture: closing the primary
						// popup is what can reveal a chained one, so the alt rect has
						// to be re-read after that close rather than assumed. The old
						// unconditional alt tap is what pressed the resource HUD at
						// (1209,174) on the live 720p run, with no popup on screen.
						altScreen, altCapErr := h.Client.CaptureToMat()
						if altCapErr != nil {
							h.Logger.Warn().Err(altCapErr).Msg("alt-popup probe capture failed; skipping the alt tap (there is no evidence a chained popup is up)")
							h.step("asset_driven_alt_probe_failed", map[string]any{"name": btn.name, "err": altCapErr.Error()})
						} else {
							altNowUp, altNowRed := xPopupPresent(altScreen, *xPopupAlt, baseAltRed)
							h.stepScreen("asset_driven_alt_probe", altScreen, map[string]any{
								"name":    btn.name,
								"alt_red": altNowRed, "alt_baseline_red": baseAltRed,
								"alt_up": altNowUp,
							})
							altScreen.Close()
							if !altNowUp {
								h.step("asset_driven_alt_skipped", map[string]any{"name": btn.name, "alt_red": altNowRed})
							} else {
								acx, acy := xPopupAlt.Center()
								h.step("asset_driven_modal_close_alt", map[string]any{
									"name": btn.name, "x": acx, "y": acy,
								})
								if err := h.Client.Tap(acx, acy); err != nil {
									h.Logger.Error().Err(err).Msg("alt X tap failed")
								}
								h.sleep(1000 * time.Millisecond)
							}
						}
					}

					// Final verify: did both popups dismiss?
					verCap, verErr := h.Client.CaptureToMat()
					if verErr != nil {
						h.Logger.Warn().Err(verErr).Msg("verify-capture failed; defensively tapping both X centers then aborting iteration")
						defensiveDualTapAndLogClose(h, xcx, xcy, xPopupAlt, btn.name, "capture_failed")
						aborted = true
						break
					}
					verPrimaryUp, verPrimaryRed := xPopupPresent(verCap, xPopupRect, basePopupRed)
					verAltUp, verAltRed := false, 0
					if xPopupAlt != nil {
						verAltUp, verAltRed = xPopupPresent(verCap, *xPopupAlt, baseAltRed)
					}
					verAllUp := verPrimaryUp || verAltUp
					h.stepScreen("asset_driven_modal_verified", verCap, map[string]any{
						"name":       btn.name,
						"primary_up": verPrimaryUp, "primary_red": verPrimaryRed,
						"alt_up": verAltUp, "alt_red": verAltRed,
						"all_up": verAllUp,
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

					// Dismissed popups mean this resource could NOT pay for the
					// upgrade: the gem-buy popup IS the "that would cost gems"
					// signal, so the tap did not upgrade anything. Reporting
					// success here made the outer loop re-select the same wall
					// for ever — every iteration raises the same popup, closes it,
					// declares success and starts over, so the sequence never
					// reached its `all_unaffordable` exit and only a user Stop (or
					// a mis-picked X rect, which takes the close_failed branch
					// below) could end it. Treat it exactly like the failed-close
					// case: try the next button on this same wall, so both-
					// unaffordable ends the sequence cleanly.
					h.Logger.Info().Str("button", btn.name).
						Msg("Gem-buy popup dismissed: this resource cannot pay for the upgrade. Trying the next button.")
					h.step("asset_driven_unaffordable_dismissed", map[string]any{
						"name": btn.name, "when": "after_confirm",
					})
					// Closing the popup reveals the confirm dialog again, and that
					// dialog covers the tray: without leaving it the next craft's
					// chip tap lands on the dialog's body, the following CONFIRM
					// press raises the SAME gold prompt, and the elixir option is
					// never actually evaluated. leaveConfirmDialog only sends the
					// Back when the dialog's button is drawn, and the next chip's
					// tray gate catches an over-eager Back.
					leaveConfirmDialog(h, confirmRect)
					// No runDismiss(h) here, for the same reason as the
					// close_failed path below: the next button's modal check
					// re-evaluates the rects, and the post-button-loop
					// `all_unaffordable` exit runs the single sequence-end
					// dismiss.
					continue
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
			xBtnX, xBtnY := loadWallUpgradeXButton(h.Cal, h.Logger)
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
				h.sleep(1200 * time.Millisecond)

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
					h.sleep(1000 * time.Millisecond)
					continue
				}
				bottomROI := image.Rect(0, int(h.Cal.Length(400)), modalScreen.Cols(), modalScreen.Rows())
				confirmMatches, _ := vision.MatchMultiScaleROICached(modalScreen, confirmTpl, "btn_confirm_upgrade", 0.3, 1.5, 20, 0.70, bottomROI)

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
				h.stepScreen("hardcoded_confirm_checked", modalScreen, map[string]any{
					"name":   btn.name,
					"is_red": isRed,
				})
				modalScreen.Close()

				if isRed {
					// Unaffordable: dismiss the modal with X (top-right)
					// then loop to try elixir.
					h.step("hardcoded_confirm_red", map[string]any{"name": btn.name})
					if err := h.Client.Tap(xBtnX, xBtnY); err != nil {
						h.Logger.Error().Err(err).Msg("X-button dismiss tap failed")
					}
					h.sleep(1000 * time.Millisecond)
					continue
				}

				// Affordable: tap confirm and success.
				h.step("hardcoded_confirm_white", map[string]any{"name": btn.name})
				if err := h.Client.Tap(bestConfirm.Point.X, bestConfirm.Point.Y); err != nil {
					h.Logger.Error().Err(err).Msg("hardcoded confirm tap failed")
				}
				h.sleep(2000 * time.Millisecond)
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
				h.sleep(1000 * time.Millisecond)
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
			bottomROI := image.Rect(0, int(h.Cal.Length(400)), captureScreen.Cols(), captureScreen.Rows())
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
			bottomROI := image.Rect(0, int(h.Cal.Length(400)), captureScreen.Cols(), captureScreen.Rows())
			debugMatches, _ := vision.MatchMultiScaleAllROICached(captureScreen, upgradeTpl, "btn_upgrade_wall", 0.3, 1.5, 60, 0.40, bottomROI)
			h.stepScreen("upgrade_not_found", captureScreen, map[string]any{
				"debug_matches": debugMatches,
			})
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
			h.sleep(1200 * time.Millisecond) // Wait for confirm dialog or gem popup

			confirmScreen, err := h.Client.CaptureToMat()
			if err != nil {
				h.Logger.Error().Err(err).Msg("Failed to capture screen for confirm check")
				_ = h.Client.Back()
				h.sleep(1500 * time.Millisecond)
				continue
			}

			// ROI for the confirm-button match: bottom half of the screen.
			// Declared here (in candidate-iteration scope) because the
			// retry-loop's captureScreen is closed before the matched
			// path enters this block — the prior one-shot block used a
			// wider-scope bottomROI; the multi-retry refactor moved that
			// declaration into the loop body, so the candidate path
			// re-declares it locally.
			bottomROI := image.Rect(0, int(h.Cal.Length(400)), confirmScreen.Cols(), confirmScreen.Rows())
			confirmMatches, _ := vision.MatchMultiScaleROICached(confirmScreen, confirmTpl, "btn_confirm_upgrade", 0.3, 1.5, 20, 0.70, bottomROI)
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
				h.stepScreen("confirm_checked", confirmScreen, map[string]any{
					"btn_idx": idx,
					"is_red":  isRed,
					"matches": confirmMatches,
				})
				confirmScreen.Close()

				if isRed {
					h.Logger.Info().Msg("Confirm button price is RED (unaffordable). Dismissing dialog.")
					h.step("confirm_red", map[string]any{"btn_idx": idx})
					_ = h.Client.Back()
					h.sleep(1500 * time.Millisecond)
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
				h.sleep(2000 * time.Millisecond)
				success = true
				break
			} else {
				h.stepScreen("confirm_missing", confirmScreen, map[string]any{"btn_idx": idx})
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
			h.sleep(500 * time.Millisecond)
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
		h.sleep(500 * time.Millisecond)
	}
	return false
}

// dismissInterruptionsFor clears an interruption dialog seen on the
// waitForMainVillage loop's frames. It delegates to game.DismissOverlay, which is
// the same evidence-gated path the bot's capture loop uses (game/dismiss.go), so
// there is exactly one overlay-dismissal implementation in the project and no
// second coordinate table to drift from it.
//
// This used to carry its own copy of the coordinate table: a blind tap at
// (400,300) for the obstacle sheet, a predicted (175,30) X for the gem/shield
// popups, calibrated Welcome-Back / splash coordinates, and a comment claiming
// that made it "mirror Bot.dismissInterruptions". Two copies of a guessed tap is
// two places for a guessed tap to survive a cleanup.
//
// Called with Classify nil (manual mode) there is no state to verify, and with
// Classifier nil there is nothing to verify it against — either way DismissOverlay
// refuses to fire, which is the safe direction.
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

	if !game.DismissOverlay(h.Client, h.Cal, h.Classifier, state, h.Templates, h.Logger) {
		h.Logger.Debug().Str("state", state.String()).
			Msg("no overlay to dismiss from verified evidence; no input fired")
	}
}

// runDismiss invokes the optional hooks.Dismiss to clear a wall-selection
// menu. Safe to call when Dismiss is nil.
func runDismiss(h *WallUpgradeHooks) {
	if h.Dismiss != nil {
		h.Dismiss()
	}
}

// wallRowTapWindow is how far around the seed match the fresh-frame row tap
// looks, in live pixels. The builder menu's rows are ~40 px apart and the menu
// glides by roughly that much between a capture and the tap, so the window has to
// be wider than one row while staying far too tight to reach the menu's other
// columns.
const wallRowTapWindow = 70

// tapWallRow taps the builder menu's "Wall" row from the freshest possible
// frame, and reports the tap it actually made.
//
// The row a match was computed on is not the row that will be under the finger:
// CoC's menu keeps gliding after the swipe ends, so the ~100 ms between the
// capture and the tap moved the rows ~40 px on the live run and the tap landed on
// the neighbouring "Hero Hunter" row. Re-matching inside a window around the seed
// (bounded, so the matcher cannot wander to another row) and tapping the centre
// of THAT match is what closes the gap. When the fresh frame no longer holds a
// match at all (the row scrolled away entirely) the seed's own centre is tapped
// as the fallback, so a menu that settled instantly behaves as it did before.
func tapWallRow(h *WallUpgradeHooks, tpl gocv.Mat, menuROI image.Rectangle, seed vision.Match, window int) (bool, map[string]any) {
	mw := int(float64(tpl.Cols()) * seed.Scale)
	mh := int(float64(tpl.Rows()) * seed.Scale)
	cx, cy := seed.Point.X+mw/2, seed.Point.Y+mh/2

	screen, err := h.Client.CaptureToMat()
	if err != nil {
		h.step("wall_row_fresh_capture_failed", map[string]any{"err": err.Error()})
		return false, nil
	}
	defer screen.Close()

	hunt := image.Rect(cx-window, cy-window/2, cx+window, cy+window/2).Intersect(menuROI)
	if hunt.Empty() {
		hunt = menuROI
	}
	matches, _ := vision.MatchMultiScaleROICached(screen, tpl, "text_wall", 0.3, 1.5, 20, 0.78, hunt)
	tx, ty := cx, cy
	data := map[string]any{
		"seed":   []int{seed.Point.X, seed.Point.Y},
		"window": hunt,
		"fresh":  false,
	}
	for _, m := range matches {
		if !m.Point.In(hunt) {
			continue
		}
		fw := int(float64(tpl.Cols()) * m.Scale)
		fh := int(float64(tpl.Rows()) * m.Scale)
		tx, ty = m.Point.X+fw/2, m.Point.Y+fh/2
		data["fresh"] = true
		data["fresh_point"] = []int{m.Point.X, m.Point.Y}
		data["fresh_conf"] = m.Confidence
		data["fresh_scale"] = m.Scale
		break
	}
	data["tap"] = []int{tx, ty}
	if tapErr := h.Client.Tap(tx, ty); tapErr != nil {
		data["err"] = tapErr.Error()
		return false, data
	}
	return true, data
}

// leaveConfirmDialog backs out of the confirm dialog after an unaffordable
// dismissal so the next resource chip is reachable, but only on evidence that the
// dialog is still up (its CONFIRM control is drawn).
//
// Closing the popup reveals the confirm dialog again, and the dialog covers the
// tray: without this the flow tapped "elixir" into the dialog's body, the
// following CONFIRM press raised the SAME gold prompt, and the elixir option was
// never evaluated at all — the exact "switch to elixir" the user asked for.
//
// Back() on a screen that is already back at the tray would close the wall
// selection, which the next chip's tray gate then catches (wall_tray_absent, no
// input), so this recovery can only make the following attempt safer.
func leaveConfirmDialog(h *WallUpgradeHooks, confirmRect Rect) {
	screen, err := h.Client.CaptureToMat()
	if err != nil {
		h.Logger.Warn().Err(err).Msg("could not capture before leaving the confirm dialog; sending no Back")
		return
	}
	drawn, bright, green := confirmButtonDrawn(screen, confirmRect)
	screen.Close()
	if !drawn {
		return
	}
	h.step("asset_driven_leave_confirm_dialog", map[string]any{
		"confirm_bright": bright, "confirm_green": green,
	})
	if err := h.Client.Back(); err != nil {
		h.Logger.Warn().Err(err).Msg("Back failed while leaving the confirm dialog")
	}
	h.sleep(1000 * time.Millisecond)
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
	h.sleep(1000 * time.Millisecond)
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

// stepScreen is step() for the phases whose payload carries a frame.
//
// The clone is taken ONLY when a receiver is wired, because a receiver takes
// ownership of it and closes it. With no receiver the clone was unreachable the
// moment step() returned, and gocv.Mat has no finalizer: production (which wires
// no OnStep) leaked one full-screen Mat — ~1.9 MB at 860x732 — per phase, several
// per wall upgrade, for the whole run. Cloning here instead of at the call sites
// also keeps the no-instrumentation path free of the allocation entirely.
func (h *WallUpgradeHooks) stepScreen(name string, frame gocv.Mat, data map[string]any) {
	if h.OnStep == nil {
		h.step(name, data)
		return
	}
	if data == nil {
		data = make(map[string]any, 1)
	}
	data["screen"] = frame.Clone()
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
	px := modalBrightPixelsIn(screen, rect)
	return px > modalBrightPixels, px
}

// modalBrightPixels is the near-white pixel count above which a rect is treated
// as holding a popup's X-button glyph. See hasModalInRect for how it was
// measured (45x40 ref-px rect, X border routinely 50+ px, empty screens single
// digits).
const modalBrightPixels = 15

// modalBrightPixelsIn returns the raw near-white pixel count inside rect. Split
// out of hasModalInRect so a caller can compare one rect's count ACROSS frames
// (the pre-tap baseline against the post-tap verify) without re-deriving the
// threshold. Returns 0 for an out-of-bounds or empty rect.
func modalBrightPixelsIn(screen gocv.Mat, rect Rect) int {
	bounds := clampRectToFrame(screen, rect)
	if bounds.Empty() {
		return 0
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
	return gocv.CountNonZero(mask)
}

// clampRectToFrame clamps rect to the frame and returns the image rectangle the
// pixel counters below read. A rect that clamps empty (off-frame, degenerate or
// inverted) answers with an empty rectangle, which every caller treats as "no
// evidence" instead of handing Mat.Region an illegal ROI — which aborts the
// process rather than returning an error.
func clampRectToFrame(screen gocv.Mat, rect Rect) image.Rectangle {
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
	return bounds
}

// The buy-with-gems popup and the confirm dialog's CONFIRM control are told
// apart by COLOUR, not by brightness, because on this client the confirm
// dialog's own (bright) panel covers the rect the assets park the popup's close
// button on. Both floors below are measured on the live 1280x720 frames in
// output/wall_pick — the four states the asset-driven flow runs through — read
// inside the rect the shipped assets map each control onto.
const (
	// popupRedPixelsMin is the red-pixel floor for x_popup_roi. Measured:
	//
	//	step6 popup        2016 red   (the popup's round red X fills two thirds)
	//	step5 confirm page    0 red   (the confirm dialog's own X is elsewhere)
	//	step3 panel         183 red
	//	step1 village         2 red
	//
	// 400 sits an order of magnitude below the popup and above every other
	// measured state, so the test reads as "the popup's close control is on
	// screen". The brightness test this replaces read 3172 bright px in the
	// same rect on the CONFIRM page and 580 on the popup — backwards — which is
	// what made an affordable wall, sat on its confirm dialog, look like a wall
	// that had just raised a gem-buy prompt.
	popupRedPixelsMin = 400

	// confirmBrightMin / confirmGreenMin are the CONFIRM-control floors: the
	// control is a saturated green face with a white caption, so BOTH have to be
	// cleared (green alone is the village's grass, bright alone is any caption on
	// a dark panel). Measured inside the mapped confirm rect, which
	// assets/wall_upgrade_confirm.json maps to (796,586)-(998,667) here:
	//
	//	step5 confirm page   bright 1859  green 4831
	//	step3 upgrade panel  bright 1147  green 1937   (an Upgrade chip in the same band)
	//	step1 village        bright   49  green 2638
	//	step6 popup          bright    0  green    0   (the popup covers the dialog)
	confirmBrightMin = 400
	confirmGreenMin  = 400
)

// redFacePixelsIn counts the strongly red pixels inside rect. CoC paints the
// buy-with-gems popup's close control as a red disc with a white glyph, and no
// other pixel in the wall-flow states comes near that saturation.
func redFacePixelsIn(screen gocv.Mat, rect Rect) int {
	return colourFacePixelsIn(screen, rect, func(b, g, r int) bool {
		return r > 150 && r-g > 60 && r-b > 60
	})
}

// greenFacePixelsIn counts the saturated green pixels inside rect — a button
// face rather than the desaturated green of the village grass.
func greenFacePixelsIn(screen gocv.Mat, rect Rect) int {
	return colourFacePixelsIn(screen, rect, func(b, g, r int) bool {
		return g > 140 && g-r > 40 && g-b > 40
	})
}

// colourFacePixelsIn counts the pixels inside rect that satisfy pred (channels
// in gocv's BGR order). The rect is clamped to the frame first.
func colourFacePixelsIn(screen gocv.Mat, rect Rect, pred func(b, g, r int) bool) int {
	bounds := clampRectToFrame(screen, rect)
	if bounds.Empty() {
		return 0
	}
	sub := screen.Region(bounds)
	defer sub.Close()
	n := 0
	for y := 0; y < sub.Rows(); y++ {
		for x := 0; x < sub.Cols(); x++ {
			p := sub.GetVecbAt(y, x)
			if pred(int(p[0]), int(p[1]), int(p[2])) {
				n++
			}
		}
	}
	return n
}

// xPopupPresent reports whether the buy-with-gems popup is on screen: its red
// close control is drawn inside xPopupRect AND the rect holds more red than the
// pre-tap baseline did.
//
// The absolute floor is what tells the popup apart from the confirm dialog; the
// growth gate is what stops a rect parked on permanent screen chrome from
// witnessing a popup that is not there. assets/wall_upgrade_x_roi.json maps
// x_popup_roi_alt to the dark-elixir readout in this client's resource HUD, which
// reads the same (red) count on the baseline and on every later frame, so growth
// stays zero and it stays down; a genuine popup is red where the chrome was not.
func xPopupPresent(screen gocv.Mat, xPopup Rect, baselineRed int) (up bool, red int) {
	red = redFacePixelsIn(screen, xPopup)
	return red >= popupRedPixelsMin && red > baselineRed, red
}

// confirmButtonDrawn reports whether the confirm dialog's CONFIRM control is
// drawn inside the mapped rect, along with the two counts behind that verdict.
//
// This is the asset-driven flow's asset sanity check: the box the picker wrote
// has to land ON the control it names. The shipped wall_upgrade_confirm.json
// mapped 94 live px above this client's CONFIRM button (the drag had been picked
// against a taller dialog), so the blind tap pressed the dialog's flavour text,
// nothing upgraded, and the popup check then found no popup — a silent spawn the
// loop reported as a successful upgrade. Refusing the tap unless the face is
// drawn there is the same rule the tray gate below already applies to the chips.
func confirmButtonDrawn(screen gocv.Mat, confirm Rect) (drawn bool, bright, green int) {
	bright = modalBrightPixelsIn(screen, confirm)
	green = greenFacePixelsIn(screen, confirm)
	return bright >= confirmBrightMin && green >= confirmGreenMin, bright, green
}

// wallTrayChipConfig bounds the search for a wall-upgrade tray chip. The tray's
// gold and elixir upgrade chips are square-ish painted faces (the shipped
// assets/wall_upgrade_buttons.json rects are 76x74 reference px), so the
// wide-and-flat aspect window DefaultActionButtonConfig uses for panel action
// buttons would reject them. Everything else — the bright-saturated mask, the
// frame-relative width bounds, the beat-the-neighbours winner rule — is shared
// with FindActionButton, so the "is this a painted control" verdict stays one
// implementation.
func wallTrayChipConfig() vision.ActionButtonConfig {
	cfg := vision.DefaultActionButtonConfig()
	cfg.MinAspect = 0.45
	cfg.MaxAspect = 2.2
	// A chip is a small object on screen; the defaults' 0.12 frame-width floor
	// is tuned for a panel's full-width action button and would reject it.
	cfg.MinFrameWidthFrac = 0.03
	cfg.MaxFrameWidthFrac = 0.30
	cfg.MaxFrameHeightFrac = 0.30
	return cfg
}

// locateWallTrayChip looks for a painted upgrade chip near the rect the assets
// predict. The window is that rect grown by 40% of its own size on each side,
// which absorbs the small reflow a panel can show between the reference frame the
// assets were picked on and the live frame, while staying far too tight to reach
// a builder-menu row elsewhere on screen.
//
// Returns the located rect (LIVE frame), the bright-saturated pixel count inside
// the mapped rect (the diagnostic the user can compare against the JSONL), and
// whether a chip was found. When !found the located rect is the mapped one, so
// callers can use the return value as a fallback tap point unconditionally.
func locateWallTrayChip(screen gocv.Mat, mapped Rect) (located Rect, facePx int, found bool) {
	located = mapped
	facePx = vision.FacePixels(screen, mapped.ImageRect())

	r := mapped.ImageRect()
	if r.Empty() || screen.Cols() == 0 || screen.Rows() == 0 {
		return
	}
	growX, growY := r.Dx()*2/5, r.Dy()*2/5
	window := image.Rect(r.Min.X-growX, r.Min.Y-growY, r.Max.X+growX, r.Max.Y+growY)
	window = window.Intersect(image.Rect(0, 0, screen.Cols(), screen.Rows()))
	if window.Empty() {
		return
	}
	btn, ok := vision.FindActionButton(screen, window, wallTrayChipConfig())
	if !ok {
		return
	}
	return Rect{X1: btn.Rect.Min.X, Y1: btn.Rect.Min.Y, X2: btn.Rect.Max.X, Y2: btn.Rect.Max.Y}, facePx, true
}

// locateWallTray is the asset-driven flow's presence gate. Both upgrade chips
// must be locatable in the live frame before a single blind tap fires.
//
// Why a gate at all: the flow used to tap the mapped gold, confirm and elixir
// centres unconditionally. On the live 1280x720 run those taps landed on
// builder-menu ROWS — the mapped gold centre (641,552) is inside the menu's row
// band, and the run's own screenshots show the "Upgrade Hero Hunter to Level 4?"
// and storage detail panels the taps opened instead of a tray. A tap that lands
// on a menu row activates unrelated UI: at best a wasted iteration, at worst a
// purchase or a builder assignment nobody asked for.
//
// The gate is deliberately colour-based rather than position-based, which is
// what makes it survive the thing that broke the assets: CoC reflows panels by
// content and aspect (docs/RESOLUTION.md) while painting its chips in the same
// two saturated faces. A menu row drawn where a chip belongs has white text on a
// dark panel, zero saturated pixels, and fails the gate.
//
// wallTrayGate is the evidence the asset-driven flow burns before its first
// blind tap: where each mapped chip rect landed in the live frame (or the
// mapped rect itself when nothing was found, so it doubles as the tap point) and
// how much painted face the mapped rect holds.
type wallTrayGate struct {
	GoldChip     Rect
	ElixirChip   Rect
	GoldMapped   Rect
	ElixirMapped Rect
	GoldFacePx   int
	ElixirFacePx int
	GoldFound    bool
	ElixirFound  bool
}

// Present reports whether both upgrade chips are on screen as painted faces.
func (g wallTrayGate) Present() bool { return g.GoldFound && g.ElixirFound }

// Data renders the gate's evidence for an OnStep payload. No *gocv.Mat here, so
// the payload stays loggable — stepScreen is what carries frames.
func (g wallTrayGate) Data() map[string]any {
	return map[string]any{
		"gold_face_px":   g.GoldFacePx,
		"elixir_face_px": g.ElixirFacePx,
		"gold_found":     g.GoldFound,
		"elixir_found":   g.ElixirFound,
		"gold_mapped":    g.GoldMapped.ImageRect(),
		"elixir_mapped":  g.ElixirMapped.ImageRect(),
		"gold_located":   g.GoldChip.ImageRect(),
		"elixir_located": g.ElixirChip.ImageRect(),
	}
}

func locateWallTray(screen gocv.Mat, gold, elixir Rect) wallTrayGate {
	gChip, gFace, gFound := locateWallTrayChip(screen, gold)
	eChip, eFace, eFound := locateWallTrayChip(screen, elixir)
	return wallTrayGate{
		GoldChip: gChip, ElixirChip: eChip,
		GoldMapped: gold, ElixirMapped: elixir,
		GoldFacePx: gFace, ElixirFacePx: eFace,
		GoldFound: gFound, ElixirFound: eFound,
	}
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

	// The clamp above can leave an INVERTED rect: the cost band sits below a
	// confirm button (Point.Y + 5..55 px), so on a button in the bottom rows
	// Min.Y ends up past the frame edge while Max.Y was pulled back to it. An
	// inverted rect is not a legal cgo ROI, so the check must refuse it instead
	// of handing it to Region (OpenCV throws on the ROI and gocv does not catch
	// C++ exceptions, which aborts the process). "No red seen" is the safe
	// answer: the caller then falls through to the tap-and-probe path.
	if roi.Max.X <= roi.Min.X || roi.Max.Y <= roi.Min.Y {
		return false
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

	// See checkConfirmRed for why an inverted rect after clamping must be
	// refused: this band is anchored BELOW the matched icon (y_min_off 30,
	// y_max_off 80), so any candidate within ~50 px of the tray's bottom edge
	// clamps to Min.Y > Max.Y. That is the normal case for the bottom-most wall
	// row in the legacy template flow — the one flow that still calls this.
	if roi.Max.X <= roi.Min.X || roi.Max.Y <= roi.Min.Y {
		return false, 0
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
// assets/wall_upgrade_buttons.json. Gold and Elixir are both assetRects — the
// same type used by the wall_upgrade_confirm.json and wall_upgrade_x_roi.json
// loaders for consistent typing across the wall-upgrade asset files.
//
// Lifecycle: loaded once per sequence at the start of
// RunWallUpgradeLoop. Per the user's "They appear in same spot
// every time", the values are constant for the duration of the
// bot's run.
type wallUpgradeButtonsConfig struct {
	Gold   assetRect `json:"gold"`
	Elixir assetRect `json:"elixir"`
}

// loadWallUpgradeButtons reads assets/wall_upgrade_buttons.json and
// returns the gold + elixir Rects in LIVE pixels (mapped from the file's
// reference frame via Rect.Live) plus ok=true if both shapes parsed
// cleanly AND neither rect is the all-zero sentinel. When the file is
// missing or malformed, ok=false and the caller falls back to the legacy
// template path.
//
// The x1/y1/x2/y2 keys must all be present in BOTH gold and elixir —
// partial configs (e.g. only gold set, elixir empty) trigger the
// Empty() guard and reject rather than running with a degenerate
// elixir centroid that would tap into random screen territory.
func loadWallUpgradeButtons(cal *game.Calibration, logger zerolog.Logger) (gold, elixir Rect, ok bool) {
	data, err := os.ReadFile(paths.Resolve("wall_upgrade_buttons.json"))
	if err != nil {
		return
	}
	var cfg wallUpgradeButtonsConfig
	if json.Unmarshal(data, &cfg) != nil {
		return
	}
	if cfg.Gold.Empty() || cfg.Elixir.Empty() {
		if cfg.Gold.source == "physical-only" || cfg.Elixir.source == "physical-only" {
			logger.Error().Msg("assets/wall_upgrade_buttons.json holds a physical-only box: live-device pixels cannot be mapped through the HUD law, so the asset-driven flow is off. Re-pick it so both blocks are written: make refmap && ./tools/select_wall_upgrade_buttons.py")
		}
		return
	}
	gold = cfg.Gold.rect.Live(cal)
	elixir = cfg.Elixir.rect.Live(cal)
	gcx, gcy := gold.Center()
	ecx, ecy := elixir.Center()
	rgcx, rgcy := cfg.Gold.rect.Center()
	recx, recy := cfg.Elixir.rect.Center()
	logger.Info().
		Str("gold_source", cfg.Gold.source).Str("elixir_source", cfg.Elixir.source).
		Int("gold_cx", gcx).Int("gold_cy", gcy).
		Int("elixir_cx", ecx).Int("elixir_cy", ecy).
		Int("ref_gold_cx", rgcx).Int("ref_gold_cy", rgcy).
		Int("ref_elixir_cx", recx).Int("ref_elixir_cy", recy).
		Msg("Loaded wall-upgrade button rects (assets/wall_upgrade_buttons.json; ref coords mapped to live geometry)")
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
	ConfirmButton assetRect `json:"confirm_button"`
}

// loadWallUpgradeConfirmRect reads assets/wall_upgrade_confirm.json and
// returns the post-upgrade Confirm-button rect in LIVE pixels (mapped
// from the file's reference frame) plus ok=true if it parsed cleanly and
// is non-empty.
//
// Absent or malformed JSON returns ok=false; the asset-driven flow
// gate fails and the bot falls through to probe-and-discard or
// template-matching. Empty rect also fails loud — a degenerate
// confirm-button rect would silently tap the screen origin.
func loadWallUpgradeConfirmRect(cal *game.Calibration, logger zerolog.Logger) (Rect, bool) {
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
		if cfg.ConfirmButton.source == "physical-only" {
			logger.Error().Msg("assets/wall_upgrade_confirm.json holds a physical-only box; re-pick it so both blocks are written (make refmap && ./tools/select_wall_upgrade_buttons.py)")
		}
		return r, false
	}
	mapped := cfg.ConfirmButton.rect.Live(cal)
	cx, cy := mapped.Center()
	rcx, rcy := cfg.ConfirmButton.rect.Center()
	logger.Info().
		Str("source", cfg.ConfirmButton.source).
		Int("cx", cx).Int("cy", cy).
		Int("ref_cx", rcx).Int("ref_cy", rcy).
		Msg("Loaded post-upgrade Confirm rect (assets/wall_upgrade_confirm.json; ref coords mapped to live geometry)")
	return mapped, true
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
	XPopupROI    assetRect  `json:"x_popup_roi"`
	XPopupROIAlt *assetRect `json:"x_popup_roi_alt,omitempty"`
}

// loadWallUpgradeXPopupRect reads assets/wall_upgrade_x_roi.json and
// returns the gem-buy-popup X-button rect in LIVE pixels (mapped from the
// file's reference frame) plus an OPTIONAL alternate rect (nil if not
// configured) plus ok=true if the primary parsed cleanly and is non-empty.
//
// Three-argument return shape replaces the prior two-argument `Rect, bool`
// signature to thread the optional XPopupROIAlt through to the retry
// loop. The alt is *Rect so a missing JSON key leaves it nil naturally
// (JSON nil-pointer idiom — json.Unmarshal does not allocate a Rect
// for an absent key).
//
// Retvals:
//   - primary: x_popup_roi rect (LIVE pixels), used for hasModalInRect
//     detection AND as the first candidate tap point in the retry loop.
//   - alt:     x_popup_roi_alt rect (nullable, LIVE pixels), the second
//     dismissal target after primary.
//
// Absent primary or malformed JSON returns ok=false; the asset-driven
// flow gate fails and the bot falls through to the probe-and-discard flow.
func loadWallUpgradeXPopupRect(cal *game.Calibration, logger zerolog.Logger) (primary Rect, alt *Rect, ok bool) {
	data, err := os.ReadFile(paths.Resolve("wall_upgrade_x_roi.json"))
	if err != nil {
		return
	}
	var cfg wallUpgradeXPopupConfig
	if json.Unmarshal(data, &cfg) != nil {
		return
	}
	if cfg.XPopupROI.Empty() {
		if cfg.XPopupROI.source == "physical-only" {
			logger.Error().Msg("assets/wall_upgrade_x_roi.json holds a physical-only box; re-pick it so both blocks are written (make refmap && ./tools/select_wall_upgrade_buttons.py)")
		}
		return
	}
	cx, cy := cfg.XPopupROI.rect.Live(cal).Center()
	rcx, rcy := cfg.XPopupROI.rect.Center()
	logger.Info().
		Str("source", cfg.XPopupROI.source).
		Int("cx", cx).Int("cy", cy).
		Int("ref_cx", rcx).Int("ref_cy", rcy).
		Msg("Loaded gem-buy popup X ROI (assets/wall_upgrade_x_roi.json; ref coords mapped to live geometry)")
	if cfg.XPopupROIAlt == nil {
		// No alt configured — fine; the dual-rect verifier treats
		// nil as "do not check alt" and the retry chain stays
		// at 5 primary candidates only.
	} else if cfg.XPopupROIAlt.Empty() {
		// Alt present in JSON but degenerate ({"x1":0,…}, a partial
		// picker drag, or a physical-only block). Demote to nil so the
		// verifier treats it as "no alt" instead of a zero-area rect that
		// hasModalInRect would always return (false, 0) for — which would
		// mask a real chained popup as non-existent. Loud warning so the
		// user re-picks.
		logger.Warn().Msg("assets/wall_upgrade_x_roi.json: x_popup_roi_alt is degenerate (zero area, partial drag, or physical-only). Ignoring. Re-pick with: ./tools/select_wall_upgrade_buttons.py")
		cfg.XPopupROIAlt = nil
	} else {
		acx, acy := cfg.XPopupROIAlt.rect.Live(cal).Center()
		logger.Info().
			Str("source", cfg.XPopupROIAlt.source).
			Int("cx", acx).Int("cy", acy).
			Msg("Loaded alternate gem-buy popup X ROI (from assets/wall_upgrade_x_roi.json; ref coords mapped to live geometry)")
	}
	return cfg.XPopupROI.rect.Live(cal), xPopupAltLive(cal, cfg.XPopupROIAlt), true
}

// xPopupAltLive maps the optional alternate X rect, preserving nil (the alt is
// absent-or-degenerate, which every consumer reads as "do not check alt").
func xPopupAltLive(cal *game.Calibration, alt *assetRect) *Rect {
	if alt == nil {
		return nil
	}
	mapped := alt.rect.Live(cal)
	return &mapped
}

// wallUpgradeBuilderConfig is the JSON schema for assets/builder_button.json:
// the builder-head button in the village top bar, the tap that opens the upgrades
// menu. Its absence is not an error — the loop falls back to the authored
// reference point — but it is the one input to this sequence that a user can see
// move between client builds, so the picking session covers it.
type wallUpgradeBuilderConfig struct {
	BuilderButton assetRect `json:"builder_button"`
}

// loadWallUpgradeBuilderButton returns the LIVE tap point for the builder-head
// button, or ok=false when the asset is absent (the caller then uses the authored
// reference point). The box the user dragged is tapped at its centre.
func loadWallUpgradeBuilderButton(cal *game.Calibration, logger zerolog.Logger) (x, y int, ok bool) {
	data, err := os.ReadFile(paths.Resolve("builder_button.json"))
	if err != nil {
		return
	}
	var cfg wallUpgradeBuilderConfig
	if json.Unmarshal(data, &cfg) != nil || cfg.BuilderButton.Empty() {
		if cfg.BuilderButton.source == "physical-only" {
			logger.Error().Msg("assets/builder_button.json holds a physical-only box; re-pick it so both blocks are written (make refmap && ./tools/select_wall_upgrade_buttons.py)")
		}
		return
	}
	mapped := cfg.BuilderButton.rect.Live(cal)
	x, y = mapped.Center()
	logger.Info().
		Str("source", cfg.BuilderButton.source).
		Int("x", x).Int("y", y).
		Msg("Loaded builder-head button rect (assets/builder_button.json; ref coords mapped to live geometry)")
	return x, y, true
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

// loadWallUpgradeXButton reads assets/wall_upgrade_modal.json and returns the
// X-button tap position in LIVE pixels. The file (and the default) is
// REFERENCE-frame, so the point is mapped through the calibration's HUD law —
// right-anchored on x because ref 800 sits past the 0.88 band, top-anchored on
// y. Default (800, 100) is the modal X's authored position against the 860x732
// dataset, which maps to (1201, 133) at 1280x720/k=1.325.
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
func loadWallUpgradeXButton(cal *game.Calibration, logger zerolog.Logger) (x, y int) {
	refX, refY := 800, 100
	data, err := os.ReadFile(paths.Resolve("wall_upgrade_modal.json"))
	if err == nil {
		var cfg wallUpgradeModalConfig
		if json.Unmarshal(data, &cfg) == nil && (cfg.XButton.X != 0 || cfg.XButton.Y != 0) {
			refX, refY = cfg.XButton.X, cfg.XButton.Y
			logger.Info().Int("ref_x", refX).Int("ref_y", refY).Msg("Loaded custom modal X-button position (from assets/wall_upgrade_modal.json)")
		}
	}
	if cal == nil {
		return refX, refY
	}
	return cal.Hud(refX, refY)
}
