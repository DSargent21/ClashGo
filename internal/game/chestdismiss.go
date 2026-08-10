// Package game — chestdismiss.go
//
// ChestReward modal recovery. Two strategies, in priority order:
//
//  1. FAST PATH (Skip → Confirm Yes): one Skip tap, one Confirm Yes
//     tap, one verify capture. The fastest and most deterministic
//     path; only enabled when both button rects are configured.
//
//  2. FALLBACK (bounded tap-scan loop): tap a uniformly-random point
//     inside the chest's tap zone (tap_roi, alternating with
//     tap_roi_alt if configured), capture+classify, repeat until
//     the classifier sees a non-chest state OR the bound expires.
//
// Bounds (defense-in-depth):
//   - MaxChestDismissLoops = 15      hard iteration ceiling
//   - ChestWallClockLimit  = 25s     hard wall-clock ceiling
//
// Budget trim after Skip failure: when the Skip fast path is attempted
// AND fails, the tap-scan budget is reduced to MaxChestDismissLoops/3
// (= 5 iterations) instead of the full 15. Rationale: by the time
// we'd return-to-tap-scan, the Skip path already had a 2-tap shot —
// spending the full 15 iterations on top would burn 25s of wall-clock
// for unlikely gain. 5 iterations × ChestAnimSettle is roughly 6s of
// additional recovery, which is plenty when the Skip failure was
// caused by misconfiguration (the chest will still be on screen and
// the bot will exit cleanly via the cascade cap).
//
// Either cap breached → error returned → Navigator.handleInterruptions
// caps consecutive chest iterations at 1, then escalates to the
// bot's stuck-watchdog / restart-game ladder.
//
// Coords are in REFERENCE space (860x732) and scaled at runtime via
// (*Calibration).ScaleRef, matching every other modal in the project.
package game

import (
	"encoding/json"
	"fmt"
	"image"
	"math/rand"
	"os"
	"time"

	"github.com/Ducky705/ClashGO/internal/paths"
	"github.com/Ducky705/ClashGO/internal/vision"
	"gocv.io/x/gocv"
)

// ChestROISchema is the on-disk JSON shape. Coordinates are in the
// reference frame (RefWidth=860, RefHeight=732).
//
//   - tap_roi            — REQUIRED. Tap zone inside the chest screen.
//   - tap_roi_alt        — OPTIONAL. Alternate zone alternating on odd iters.
//   - skip_button        — OPTIONAL. Small rect around Skip on the chest.
//   - confirm_yes_button — OPTIONAL. Small rect around Confirm Yes on
//     the post-skip dialog. Captured in a SEPARATE picker run
//     (tools/picker.py --rect confirm_yes_button) AFTER the user manually
//     presses Skip on the chest screen, because the dialog isn't
//     visible during the initial chest-screen screenshot.
type ChestROISchema struct {
	TapROI           *Rectangle `json:"tap_roi"`
	TapROIAlt        *Rectangle `json:"tap_roi_alt,omitempty"`
	SkipButton       *Rectangle `json:"skip_button,omitempty"`
	ConfirmYesButton *Rectangle `json:"confirm_yes_button,omitempty"`

	// HammerTaps is the number of rapid taps fired at the chest box
	// per loop iteration before re-capturing + classifying. CoC
	// "hammer" event chests must be broken with several consecutive
	// taps at (roughly) the same spot before the reward screen
	// appears; a single tap per iteration (the historical default)
	// often failed to register the break and the chest stayed on
	// screen. Default 1 preserves the original single-tap behavior
	// when the field is absent.
	HammerTaps int `json:"hammer_taps,omitempty"`
}

const (
	MaxChestDismissLoops = 15
	ChestWallClockLimit  = 25 * time.Second

	// DefaultHammerTaps is the per-iteration tap count when
	// assets/chest_dismiss_roi.json omits hammer_taps. 1 = original
	// single-tap behavior. Hammer chests need more (try 6-10).
	DefaultHammerTaps = 1

	// chestHammerInterTap is the spacing between the rapid taps fired
	// inside a single hammer iteration. Tight enough to register as a
	// continuous "break" gesture, loose enough to not collapse into a
	// single coalesced tap on the device.
	chestHammerInterTap = 120 * time.Millisecond

	// chestTapScanBudgetAfterSkipFail is the trim applied to the
	// tap-scan loop after a Skip→Confirm failure. Stored here so
	// callers/tests can reason about the worst-case shape of the
	// combined flow: full 15 iters when Skip was never attempted;
	// 5 iters when Skip failed (full Skip path = 2 taps, 1 verify
	// capture before fall-through).
	chestTapScanBudgetAfterSkipFail = MaxChestDismissLoops / 3
)

// ChestAnimSettle — see comment on ChestSkipConfirmSettle.
var ChestAnimSettle = 1200 * time.Millisecond

// chestDebugDumps toggles the diagnostic PNG writes in
// chestContinueTap (debug_chest_opened.png at entry,
// debug_chest_continue_fail.png on final failure). On in
// production so a stuck/mislocated Continue rect can be recaptured
// from the exact frame; off in tests so capture counts stay stable.
// TEST-MUTABLE (withFastChestAnimSettle sets it to false).
var chestDebugDumps = true

// chestContinueAppearTimeout bounds how long chestContinueTap polls
// for the btn_continue template to render before falling back to
// the configured rect (or assuming no overlay). The Continue
// button lags the chest-open animation by ~1s, so a single
// early check misses it; polling absorbs that. TEST-MUTABLE like
// ChestAnimSettle (withFastChestAnimSettle sets it tiny).
var chestContinueAppearTimeout = 4 * time.Second

// chestContinuePollInterval is the gap between btn_continue
// template probes while waiting for it to appear. TEST-MUTABLE.
var chestContinuePollInterval = 250 * time.Millisecond

// chestContinueMaxTaps bounds how many times we tap the Continue rect
// (or template point) before giving up. The overlay can lag the
// chest-open animation by ~1s, so the first tap can land a frame early
// and classify as a transitional Unknown instead of MainVillage; retrying
// with a settle between attempts absorbs that. TEST-MUTABLE like
// ChestAnimSettle (withFastChestAnimSettle sets it to 1).
var chestContinueMaxTaps = 3

// ChestSkipConfirmSettle is the settle window between Skip → Confirm
// taps (and after Confirm Yes → verify-capture). 800ms is enough for
// CoC to animate the confirm dialog in on most runs.
//
// ⚠ TEST-MUTABLE. Only `withFastChestAnimSettle(t)` writes to this;
// production paths should only read. If a future caller writes from
// a goroutine you must wrap reads/writes in sync/atomic or a mutex —
// Go's race detector will otherwise flag it as a data race that's
// hard to diagnose.
var ChestSkipConfirmSettle = 800 * time.Millisecond

// init seeds the global PRNG so the chest-dismiss taps land at a
// different pixel each iteration. math/rand auto-seeds since Go 1.20
// but we seed explicitly so the behavior is robust across toolchains.
func init() {
	rand.Seed(time.Now().UnixNano())
}

// LoadChestDismissConfig reads assets/chest_dismiss_roi.json.
//
// Returns (cfg, nil) on a valid file, (nil, nil) when the file is
// absent so callers fall back to a safe default, and (nil, err) only
// on truly unrecoverable problems. Skip-button and Confirm-button
// rects are validated IF present; invalid ones are log-warned and
// cleared so a partially-broken config can still drive the fallback
// tap-scan loop.
func LoadChestDismissConfig() (*ChestROISchema, error) {
	path := paths.Resolve("chest_dismiss_roi.json")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	cfg := &ChestROISchema{}
	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if cfg.TapROI == nil {
		return nil, fmt.Errorf("%s: tap_roi is required", path)
	}
	if !cfg.TapROI.isValid() {
		return nil, fmt.Errorf("%s: tap_roi has invalid geometry %v", path, cfg.TapROI)
	}
	if cfg.TapROIAlt != nil && !cfg.TapROIAlt.isValid() {
		cfg.TapROIAlt = nil
	}
	if cfg.SkipButton != nil && !cfg.SkipButton.isValid() {
		cfg.SkipButton = nil
	}
	if cfg.ConfirmYesButton != nil && !cfg.ConfirmYesButton.isValid() {
		cfg.ConfirmYesButton = nil
	}
	return cfg, nil
}

// isValid reports whether the rectangle has positive area in the
// reference frame (860x732).
func (r *Rectangle) isValid() bool {
	if r == nil {
		return false
	}
	if r.X2 <= r.X1 || r.Y2 <= r.Y1 {
		return false
	}
	if r.X1 < 0 || r.Y1 < 0 || r.X2 > RefWidth || r.Y2 > RefHeight {
		return false
	}
	return true
}

// randomPointInRect returns a uniformly-random integer point inside
// the rectangle. Defensive against degenerate geometry.
func randomPointInRect(r Rectangle) (int, int) {
	if r.X2 <= r.X1 {
		return r.X1, r.Y1
	}
	if r.Y2 <= r.Y1 {
		return r.X1, r.Y1
	}
	x := r.X1 + rand.Intn(r.X2-r.X1+1)
	y := r.Y1 + rand.Intn(r.Y2-r.Y1+1)
	return x, y
}

// LoadContinueButtonConfig reads assets/continue_button.json.
//
// Returns (*Rectangle, nil) on a valid file, (nil, nil) when the
// file is absent so callers proceed without a Continue overlay,
// and (nil, err) only on truly unrecoverable problems (parse error
// or invalid geometry).
//
// JSON shape (matches the picker output of tools/picker.py --rect):
//
//	{
//	  "x1": 100, "y1": 100, "x2": 200, "y2": 200
//	}
//
// The shape is the bare Rectangle so cmd/pick_rect can write it
// without a wrapper struct and the engine can json.Unmarshal it
// directly into *game.Rectangle without a wrapper type.
func LoadContinueButtonConfig() (*Rectangle, error) {
	path := paths.Resolve("continue_button.json")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	raw := struct {
		X1, Y1, X2, Y2 int
	}{}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	r := &Rectangle{X1: raw.X1, Y1: raw.Y1, X2: raw.X2, Y2: raw.Y2}
	if !r.isValid() {
		return nil, fmt.Errorf("%s: invalid rect %v", path, r)
	}
	return r, nil
}

// defaultChestROIFallback is used when assets/chest_dismiss_roi.json
// is missing or invalid. Center-screen rect for tap_roi (safe —
// "tap into the void" on a non-chest screen is a no-op). Skip /
// Confirm rects are left nil; the user must run the picker to set
// them if they want the fast path.
func defaultChestROIFallback() *ChestROISchema {
	return &ChestROISchema{
		TapROI: &Rectangle{X1: 330, Y1: 280, X2: 530, Y2: 480},
	}
}

// DismissChestReward is the public recovery API. Honors the runtime
// kill-switch (SetDisableChestDismissal) first, then loads
// assets/chest_dismiss_roi.json + assets/continue_button.json and
// forwards to the inner dismissChestRewardWithCfg so tests can drive
// the engine directly with a hand-built cfg + continueRect (no
// on-disk config override required).
//
// When the kill-switch is set we log once and return nil — we do
// NOT loop, because the whole point of disabling the chest recovery
// is to let the bot's other ladders (stuck-watchdog / restartGame)
// take over with their own behavior. Returning nil here would
// otherwise make handleInterruptions happy and the chest would
// stay on screen forever; instead the cascade cap above us will
// escalate on the next capture.
func (n *Navigator) DismissChestReward() error {
	if n.disableChestDismissal {
		n.logger.Info().Msg("chest dismissal disabled by config; deferring to stuck-watchdog ladder")
		return nil
	}
	cfg, err := LoadChestDismissConfig()
	if err != nil {
		n.logger.Warn().Err(err).Msg("chest ROI config invalid; using center-screen fallback")
		cfg = defaultChestROIFallback()
	}
	if cfg == nil {
		cfg = defaultChestROIFallback()
	}
	// Continue button is OPTIONAL. If missing or invalid we log
	// once and proceed without it — chest dismiss still works,
	// the bot just won't auto-tap Continue.
	continueRect, cerr := LoadContinueButtonConfig()
	if cerr != nil {
		n.logger.Warn().Err(cerr).Msg("chest continue config invalid; continuing without it")
		continueRect = nil
	}
	if continueRect != nil {
		n.logger.Info().
			Int("x1", continueRect.X1).Int("y1", continueRect.Y1).
			Int("x2", continueRect.X2).Int("y2", continueRect.Y2).
			Msg("chest continue button configured")
	}
	return n.dismissChestRewardWithCfg(cfg, continueRect)
}

// dismissChestRewardWithCfg is the testable core. Skips the disk
// read; tests construct cfg + continueRect inline. Production calls
// go through DismissChestReward which loads + forwards.
//
// Flow:
//   - if SkipButton && ConfirmYesButton are configured:
//     run tryChestSkipFlow (1 attempt)
//     on success: fall through to chestContinueTap (if continueRect != nil)
//     on failure: skipAttempted=true → reduce tap-scan budget
//   - run chestTapScanLoop(cfg, maxIter) with the chosen budget
//   - on success: fall through to chestContinueTap (if continueRect != nil)
//
// Worst-case caps:
//   - direct (no Skip): 15 tap-scan iters + 1 Continue tap
//   - after Skip fail:  1 Skip attempt (2 taps + 1 capture) + 5 tap-scan iters + 1 Continue tap
//
// continueRect is OPTIONAL. nil means "skip the Continue step" — the
// flow ends at the chest tap-scan loop and returns whatever it
// returned (nil on success, error on circuit-breaker).
func (n *Navigator) dismissChestRewardWithCfg(cfg *ChestROISchema, continueRect *Rectangle) error {
	skipAttempted := false
	if cfg.SkipButton != nil && cfg.ConfirmYesButton != nil {
		n.logger.Info().Msg("chest: trying Skip+Confirm fast path")
		if sErr := n.tryChestSkipFlow(cfg); sErr == nil {
			if err := n.chestContinueTap(continueRect); err != nil {
				return err
			}
			return nil
		} else {
			n.logger.Warn().Err(sErr).
				Msg("chest: Skip+Confirm did not dismiss; tap-scan budget will be reduced before fall-back")
			skipAttempted = true
		}
	} else {
		n.logger.Info().Msg("chest: Skip+Confirm path disabled (skip_button and/or confirm_yes_button not configured)")
	}

	budget := MaxChestDismissLoops
	if skipAttempted {
		budget = chestTapScanBudgetAfterSkipFail
	}
	if err := n.chestTapScanLoop(cfg, budget); err != nil {
		return err
	}
	if err := n.chestContinueTap(continueRect); err != nil {
		return err
	}
	return nil
}

// tryChestSkipFlow performs ONE Skip → Confirm Yes attempt. Returns
// nil on success (Classifier sees a non-chest state on verify
// capture), an error otherwise. NO retry: a second Skip tap on a
// moving target (or after the Skip happened to fire correctly but the
// Coordinate map is slightly off) would misfire into other UI. The
// caller falls back to the (budget-trimmed) tap-scan loop on failure.
//
// 1 attempt × (2 taps + 1 capture) = the entire fast-path cost on
// success. Failures bubble up immediately so the bot doesn't burn
// extra wall-clock on a clearly-broken Skip config.
func (n *Navigator) tryChestSkipFlow(cfg *ChestROISchema) error {
	// 1. tap Skip (uniform-random within the configured rect).
	sx, sy := randomPointInRect(*cfg.SkipButton)
	skipX, skipY := n.cal.ScaleRef(sx, sy)
	if err := n.client.TapRandomized(skipX, skipY); err != nil {
		n.logger.Warn().Err(err).Msg("chest: Skip tap failed; continuing")
	}

	// 2. wait for the Confirm dialog to animate in.
	time.Sleep(ChestSkipConfirmSettle)

	// 3. tap Confirm Yes.
	cx, cy := randomPointInRect(*cfg.ConfirmYesButton)
	confirmX, confirmY := n.cal.ScaleRef(cx, cy)
	if err := n.client.TapRandomized(confirmX, confirmY); err != nil {
		n.logger.Warn().Err(err).Msg("chest: Confirm tap failed; continuing")
	}

	// 4. wait for the dialog dismissal animation.
	time.Sleep(ChestSkipConfirmSettle)

	// 5. capture + classify to verify state changed away from chest.
	mat, capErr := n.client.CaptureToMat()
	if capErr != nil {
		return fmt.Errorf("chest: skip+confirm verify capture failed: %w", capErr)
	}
	state, score := n.classify(mat)
	if !mat.Empty() {
		mat.Close()
	}

	if state != StateChestReward {
		n.logger.Info().
			Str("next_state", state.String()).
			Int("score", score).
			Msg("chest dismissed via Skip+Confirm fast path")
		return nil
	}
	return fmt.Errorf("chest: Skip+Confirm did not dismiss (verify still StateChestReward)")
}

// chestContinueTap bridges the post-chest overlay back to MainVillage.
//
// Detection order (event-agnostic first):
//  1. If a `btn_continue` template is loaded (captured once via
//     `go run cmd/capture_template -name=btn_continue`), match it in the
//     bottom half of the screen and tap the matched point. No manual rect
//     needed and survives art swaps — the preferred path.
//  2. Else fall back to the configured `continue_button.json` rect
//     (assets/continue_button.json), tapped uniformly-random inside.
//  3. If neither is available, assume this event has no Continue overlay
//     and return nil (success) without tapping.
//
// The Continue overlay can lag the chest-open animation by ~1s, so the
// first tap can land before the button is rendered and classify as a
// transitional Unknown instead of MainVillage. The rect path therefore
// retries up to chestContinueMaxTaps times (each with a settle), and on
// final failure dumps debug_chest_continue_fail.png so a wrong/missing
// rect can be recaptured from that exact frame.
//
// continueRect is OPTIONAL: pass nil and this step is a no-op unless a
// btn_continue template is present.
func (n *Navigator) chestContinueTap(continueRect *Rectangle) error {
	// Dump the OPENED chest at entry so the real Continue button
	// (which renders ~1s after the chest breaks and is NOT where the
	// pre-open "TAP TO OPEN" band was) is visible for recapture.
	if chestDebugDumps {
		if m, err := n.client.CaptureToMat(); err == nil && !m.Empty() {
			gocv.IMWrite(paths.ResolveConfig("debug_chest_opened.png"), m)
			m.Close()
		}
	}

	// 1. Preferred: wait for the Continue button (btn_continue
	// template) to actually RENDER, then tap the matched point.
	// The button lags the chest-open animation by ~1s, so we
	// poll (up to chestContinueAppearTimeout) instead of a one-shot
	// match — this is the "wait for it to appear" step.
	if n.templates != nil {
		if pt, conf, ok := n.waitContinueButton(); ok {
			n.logger.Info().Float64("conf", conf).Msg("chest continue: button appeared — tapping Continue")
			if err := n.client.Tap(pt.X, pt.Y); err != nil {
				n.logger.Warn().Err(err).Msg("chest continue: tap failed; continuing")
			}
			time.Sleep(ChestAnimSettle)
			if n.verifyAtMainVillage("chest continue (template)") == nil {
				return nil
			}
			n.logger.Warn().Msg("chest continue: button tap did not reach MainVillage; falling back to rect")
		} else {
			n.logger.Debug().Msg("chest continue: btn_continue never appeared; falling back to configured rect")
		}
	}

	// 2. Fallback: configured rect.
	if continueRect == nil {
		// No template and no configured rect — assume no Continue overlay.
		n.logger.Debug().Msg("chest continue: no template and no configured rect; assuming no Continue overlay")
		return nil
	}

	// Retried because the Continue overlay can lag the chest-open
	// animation by ~1s; the first tap can land before the button is
	// rendered and classify as a transitional Unknown instead of
	// MainVillage. Each attempt waits for the overlay to animate in
	// before tapping and verifies afterwards.
	for attempt := 0; attempt < chestContinueMaxTaps; attempt++ {
		time.Sleep(ChestAnimSettle)
		rx, ry := randomPointInRect(*continueRect)
		cx, cy := n.cal.ScaleRef(rx, ry)
		if err := n.client.TapRandomized(cx, cy); err != nil {
			n.logger.Warn().Err(err).Msg("chest continue: tap failed; continuing")
		}
		time.Sleep(ChestAnimSettle)
		if n.verifyAtMainVillage("chest continue") == nil {
			return nil
		}
		n.logger.Warn().Int("attempt", attempt+1).Int("max", chestContinueMaxTaps).
			Msg("chest continue: not at MainVillage yet; retrying")
	}

	// All attempts exhausted — dump the screen so a wrong/missing
	// Continue rect can be recaptured from this exact frame.
	if chestDebugDumps {
		if m, err := n.client.CaptureToMat(); err == nil && !m.Empty() {
			gocv.IMWrite(paths.ResolveConfig("debug_chest_continue_fail.png"), m)
			m.Close()
		}
	}
	return fmt.Errorf("chest continue: tapped Continue rect %d times without reaching MainVillage (see debug_chest_continue_fail.png)", chestContinueMaxTaps)
}

// waitContinueButton polls for the `btn_continue` template until
// it actually renders on the (normalized) screen, then returns the
// physical point to tap. This is the "wait for it to appear"
// step: the Continue button lags the chest-open animation by ~1s,
// so a one-shot match misses it. Returns (_, _, false) on
// timeout (caller falls back to the configured rect, or nil if
// no rect). Polls the bottom half (Continue buttons live low on
// the chest overlay); ref-width 860 spans the full normalized width.
func (n *Navigator) waitContinueButton() (image.Point, float64, bool) {
	if n.templates == nil {
		return image.Point{}, 0, false
	}
	tpl, ok := n.templates.Get("btn_continue")
	if !ok {
		return image.Point{}, 0, false
	}
	region := image.Rect(0, 360, 860, 732)
	deadline := time.Now().Add(chestContinueAppearTimeout)
	for time.Now().Before(deadline) {
		if pt, conf, ok := n.matchButtonTemplate(tpl, "btn_continue", region, 0.6); ok {
			return pt, conf, true
		}
		time.Sleep(chestContinuePollInterval)
	}
	return image.Point{}, 0, false
}

// matchButtonTemplate normalizes the current capture, matches tpl inside
// region (normalized coords), and returns the matched point scaled back
// to physical screen pixels. Returns (_, _, false) on any failure so the
// caller can fall back.
func (n *Navigator) matchButtonTemplate(tpl gocv.Mat, name string, region image.Rectangle, minConf float32) (image.Point, float64, bool) {
	norm, physScale, err := n.captureNormalized()
	if err != nil {
		return image.Point{}, 0, false
	}
	defer norm.Close()
	pt, conf, err := vision.MatchTemplateRegion(norm, tpl, region, minConf)
	if err != nil || conf < float64(minConf) {
		return image.Point{}, 0, false
	}
	n.logger.Debug().
		Str("template", name).
		Float64("conf", conf).
		Int("nx", pt.X).Int("ny", pt.Y).
		Msg("button template matched")
	return image.Pt(int(float64(pt.X)*physScale), int(float64(pt.Y)*physScale)), conf, true
}

// verifyAtMainVillage captures + classifies and returns nil iff the
// classifier sees StateMainVillage. Shared by both Continue-tap paths so
// the verify logic stays in exactly one place.
func (n *Navigator) verifyAtMainVillage(who string) error {
	mat, capErr := n.client.CaptureToMat()
	if capErr != nil {
		return fmt.Errorf("%s: verify capture failed: %w", who, capErr)
	}
	state, score := n.classify(mat)
	if !mat.Empty() {
		mat.Close()
	}
	if state == StateMainVillage {
		n.logger.Info().Int("score", score).Msg(who + ": at MainVillage")
		return nil
	}
	return fmt.Errorf("%s: expected MainVillage, got %s", who, state)
}

// hammerTaps returns the per-iteration tap count, defaulting to
// DefaultHammerTaps when the config omits the field (or sets <= 0).
func (c *ChestROISchema) hammerTaps() int {
	if c.HammerTaps <= 0 {
		return DefaultHammerTaps
	}
	return c.HammerTaps
}

// chestTapScanLoop is the bounded fallback. `maxIter` bounds the loop
// iterations; the wall-clock ceiling ChestWallClockLimit still
// applies regardless. Caller picks maxIter based on whether the Skip
// path was already attempted (full MaxChestDismissLoops) or not
// (chestTapScanBudgetAfterSkipFail).
//
// Each iteration fires cfg.hammerTaps() rapid taps (default 1) at the
// chest box — alternating with tap_roi_alt on odd iterations if
// configured — before recapturing + reclassifying. Multi-tap is what
// lets "hammer" event chests (which must be broken with several
// consecutive taps at roughly the same spot) clear reliably; the
// historical single-tap-per-iteration path left the chest on screen
// when one tap wasn't enough to register the break. The loop exits as
// soon as the classifier sees a non-chest state.
func (n *Navigator) chestTapScanLoop(cfg *ChestROISchema, maxIter int) error {
	deadline := time.Now().Add(ChestWallClockLimit)
	hammer := cfg.hammerTaps()
	n.logger.Info().
		Int("max_loops", maxIter).
		Int("hammer_taps_per_loop", hammer).
		Dur("wall_limit", ChestWallClockLimit).
		Bool("has_alt", cfg.TapROIAlt != nil).
		Bool("after_skip_fail", maxIter < MaxChestDismissLoops).
		Msg("chest: tap-scan loop")

	for i := 0; i < maxIter && time.Now().Before(deadline); i++ {
		var active *Rectangle
		if cfg.TapROIAlt != nil && i%2 == 1 {
			active = cfg.TapROIAlt
		} else {
			active = cfg.TapROI
		}
		// Center of the active zone — hammer chests need repeated
		// hits on the box itself, not scattered random points.
		cxRef, cyRef := (active.X1+active.X2)/2, (active.Y1+active.Y2)/2

		for t := 0; t < hammer; t++ {
			sx, sy := n.cal.ScaleRef(cxRef, cyRef)
			if err := n.client.TapRandomized(sx, sy); err != nil {
				n.logger.Warn().Err(err).Int("iter", i).Int("hammer", t).
					Msg("chest hammer tap failed; continuing")
			}
			if t < hammer-1 {
				time.Sleep(chestHammerInterTap)
			}
		}
		time.Sleep(ChestAnimSettle)

		mat, capErr := n.client.CaptureToMat()
		if capErr != nil {
			return fmt.Errorf("chest loop capture failed (iter=%d): %w", i, capErr)
		}
		state, score := n.classify(mat)
		if !mat.Empty() {
			mat.Close()
		}

		if state != StateChestReward {
			n.logger.Info().
				Int("iterations", i+1).
				Int("hammer_taps_per_loop", hammer).
				Str("next_state", state.String()).
				Int("score", score).
				Msg("chest dismissed via tap-scan loop")
			return nil
		}
	}

	return fmt.Errorf("chest-dismiss circuit-breaker: exhausted "+
		"%d loops / %s wall-clock limit without leaving StateChestReward",
		maxIter, ChestWallClockLimit)
}
