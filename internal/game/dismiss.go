// Package game — dismiss.go
//
// Overlay dismissal: the one place that moves an overlay out of the way, and the
// rules that keep that movement evidence-backed.
//
// The bot used to get out of an overlay by tapping where the overlay usually is:
// four blind taps across the middle of the screen for the obstacle sheet, a blind
// tap at (400,300) for whatever dialog was up, predicted reference coordinates for
// the gem/shield X, the Welcome Back centre and the lost-connection TRY AGAIN
// button, and a uniformly-random point inside the chest ROI. Each call looks
// harmless alone; together they are what a run reads as random tapping, and every
// one of them shares the same defect — the tap is aimed at a point nobody checked
// on the frame it fired on. docs/RESOLUTION.md measured the cost: the second tap
// of a predicted pair fired while the panel was still animating in, landed on the
// village behind it, closed the menu, and every later tap hit the village too.
//
// So the whole surface lives here, and every action is held to two checks on the
// frame it captures:
//
//  1. the state is still on that frame — the classifier's own rule for it reports
//     Matched through the same scorer ClassifyState uses, so a stale or misread
//     state produces no input at all; and
//  2. the point a tap lands on is a probe the classifier verified (the pixel that
//     made the state fire), a template-located button, or — for the one overlay
//     whose only exit is a touch outside its panel — a point measured to be
//     outside that panel and clear of the HUD (see overlayOutsideTapStates).
//
// Failing either check means NO input: the caller's stuck-watchdog / restart
// ladder owns that case, because a tap nobody could aim is not progress.
//
// The overlays whose only safe exit is the cancel action are dismissed with the
// device's Back key instead of a tap. That is not politeness: the gem popup's
// primary button buys gems, and the lost-connection dialog's RETURN HOME walks
// away from the battle, so "tap the biggest bright button" is the wrong rule for
// them — and Back needs no coordinate to be right.
package game

import (
	"image"
	"math"
	"time"

	"github.com/Ducky705/ClashGO/internal/vision"
	"github.com/rs/zerolog"
	"gocv.io/x/gocv"
)

// overlayDismissSettle is the pause after a dismissal action. The per-dialog
// sleeps this replaces (300ms for the gem popup, 1000ms for Welcome Back) were
// waiting for the same thing: the overlay's close animation, before the caller
// captures again.
const overlayDismissSettle = 400 * time.Millisecond

// overlayControlProbes maps a tap-dismissed overlay to the candidate control
// points, in the state's own reference coordinates, in preference order.
//
// Every point here is verbatim one of that state's classifier probes — pinned by
// TestDismissOverlayControlProbesAreClassifierProbes — which is what makes the tap
// evidence-backed: the pixel is the one that made the overlay fire, re-read on the
// frame about to be tapped. Several states carry more than one candidate because
// the same overlay renders at a different pixel in different geometries (see
// StateConnectionLost below); the candidate that still reads as its control wins.
var overlayControlProbes = map[GameState][]image.Point{
	// The "ТАР!" collect splash has no button; the prompt text IS the target, and
	// the classifier's beige-glyph probe is the only thing on that screen the bot
	// has ever verified.
	StateTapToContinue: {image.Pt(450, 185)},
	// Light-green Continue button, bottom-centre.
	StateNewsSplash: {image.Pt(403, 535)},
	// The orange Cancel face of the quit-confirm dialog. The green Okay face is
	// a probe of the same rule and deliberately absent: it quits the game.
	StateConfirmExit: {image.Pt(279, 429)},
	// TRY AGAIN, twice. The dialog's panel is drawn at a fixed pixel size instead
	// of scaling with the display, so the reference probe maps 34 px BELOW the
	// button at 1280x720 (measured live; corpus/connection_lost_720p.png). The
	// measured 720p probe is listed first and the reference probe second; at the
	// reference geometry the first candidate reads as bare panel and loses, at
	// 720p the second does.
	StateConnectionLost: {image.Pt(303, 451), image.Pt(300, 478)},
}

// overlayBackStates are the overlays this path closes with the device's Back key,
// with the reason the tap is refused. Kept as a table so the set is visible in one
// place instead of falling out of a switch's default branch.
var overlayBackStates = map[GameState]string{
	StateObstacleDialog: "bottom sheet: Back cancels it, and it used to get four blind taps across the screen first",
	StateGemDialog:      "its primary button buys gems; the cancel action must not be a tap",
	StateShieldInfo:     "same overlay family as the gem popup: cancel, never a predicted X coordinate",
	StateChatOpen:       "already Back-dismissed before this change; listed so this file is the whole surface",
	StateOfferPopup:     "offer modal: no close X on the frame, and its one button (KNOW MORE!) leaves the game for the store; Back is the cancel action",
}

// overlayOutsideTapStates maps the overlays whose only safe exit is a touch
// OUTSIDE their panel to that touch point, in reference coordinates with the
// state rule's own anchor.
//
// This is the one dismissal whose point is deliberately not one of the
// classifier's probes, which is why it cannot live in overlayControlProbes — that
// table's invariant (every tap point is a probe of the rule that fired) is what
// makes a tap evidence-backed. It is not a blind tap either: gate 1 above still
// applies, so the point only goes out on a frame whose own rule says the overlay
// is up.
//
// The point exists because CoC's "Update Available!" login prompt has exactly one
// control, the green Update button, and pressing it leaves the game for the app
// store — the same trap as the gem popup's primary button. What dismisses the
// prompt is a touch anywhere outside the panel (measured on this project's device,
// 2026-09-29); the panel itself swallows touches that land on it. So the point is
// chosen to sit outside the panel and clear of the HUD chrome, which
// TestUpdatePromptOutsideTapIsClearOfControls asserts against the rule's own
// probes and against every authored button window.
var overlayOutsideTapStates = map[GameState]image.Point{
	// Left of the centred panel, at mid-height. Mapped at 1280x720 this is
	// (229,352): left of the panel's measured left edge (342) and right of the
	// HUD's left button column (x < 103 in reference space). At the reference
	// geometry it is (120,360), which clears the panel there under either model
	// of how the prompt is laid out — it is outside both the centre map's panel
	// edge (205) and the fixed-pixel panel's (130) that the smaller dialog in
	// this family renders as.
	StateUpdatePrompt: {120, 360},
}

// welcomeBackOkayRegion is the window (reference coordinates) the Welcome Back
// popup's Okay button is located in: the lower half of the centred popup.
var welcomeBackOkayRegion = image.Rect(200, 350, 660, 650)

// welcomeBackOkayTemplate is the template that locates that button. The state's
// own classifier rule declares the same one, so the classifier has already
// confirmed the button is on the frame before this runs.
const welcomeBackOkayTemplate = "btn_okay"

// DismissDevice is the slice of Device the dismissal path uses: capture a frame,
// tap a verified point, or press Back. It is deliberately narrower than Device so
// every caller that can already move the screen (the bot's adb client, the
// wall-upgrade loop's wallClient) can hand its own device over without growing the
// interface it was handed — DismissOverlay must never be the reason a caller needs
// capabilities it does not have.
type DismissDevice interface {
	// CaptureToMat returns the frame the action is verified against.
	CaptureToMat() (gocv.Mat, error)
	// TapRandomized taps a verified point with the caller's usual jitter.
	TapRandomized(x, y int) error
	// Back cancels an overlay whose only safe exit is the cancel action.
	Back() error
}

// DismissOverlay dismisses the overlay the caller classified, using only evidence
// from a frame it captures itself. Returns true when it acted (a tap or a Back
// press went out); false means it refused, which leaves the overlay to the caller's
// watchdog ladder.
//
// cl may carry templates (see Classifier.SetTemplates); templates may be nil, in
// which case the popups that need a located button produce no input.
func DismissOverlay(dev DismissDevice, cal *Calibration, cl *Classifier, state GameState, templates *TemplateStore, logger zerolog.Logger) bool {
	if dev == nil || cal == nil || cl == nil {
		return false
	}

	frame, err := dev.CaptureToMat()
	if err != nil {
		logger.Debug().Err(err).Str("state", state.String()).Msg("dismiss: capture failed; no input fired")
		return false
	}
	if frame.Empty() {
		frame.Close()
		return false
	}
	defer frame.Close()

	// Gate 1: the state has to still be on THIS frame. A caller that classified an
	// older frame, or a screen that moved on in between, gets no input.
	if !stateStillOnFrame(cl, frame, state) {
		logger.Warn().Str("state", state.String()).
			Msg("dismiss: the captured frame no longer shows this overlay; no input fired")
		return false
	}

	// Gate 2, per overlay: a template-located button, a verified probe, or Back.
	if state == StateWelcomeBack {
		if located, ok := locateOkayButton(frame, templates, logger); ok {
			_ = dev.TapRandomized(located.X, located.Y)
			time.Sleep(overlayDismissSettle)
			return true
		}
		logger.Warn().Msg("dismiss: Welcome Back popup is up but its Okay button was not located; leaving it to the watchdog")
		return false
	}

	if why, needsBack := overlayBackStates[state]; needsBack {
		if err := dev.Back(); err != nil {
			logger.Warn().Err(err).Str("state", state.String()).Msg("dismiss: Back failed")
			return false
		}
		logger.Debug().Str("state", state.String()).Str("why", why).Msg("dismiss: overlay closed with Back")
		time.Sleep(overlayDismissSettle)
		return true
	}

	if pt, outsideTap := overlayOutsideTapStates[state]; outsideTap {
		rule, ok := classifierRuleFor(cl, state)
		if !ok {
			logger.Warn().Str("state", state.String()).
				Msg("dismiss: no classifier rule to carry the outside-tap anchor; no tap fired")
			return false
		}
		x, y := cal.AnchorPoint(pt.X, pt.Y, rule.Anchor)
		if x < 0 || y < 0 || x >= frame.Cols() || y >= frame.Rows() {
			logger.Warn().Str("state", state.String()).Int("x", x).Int("y", y).
				Msg("dismiss: the outside-panel tap point maps off this frame; no tap fired")
			return false
		}
		logger.Info().
			Str("state", state.String()).
			Int("x", x).Int("y", y).
			Msg("dismiss: tapping outside the panel to dismiss it (its only button leaves the game)")
		if err := dev.TapRandomized(x, y); err != nil {
			logger.Warn().Err(err).Str("state", state.String()).Msg("dismiss: tap failed")
			return false
		}
		time.Sleep(overlayDismissSettle)
		return true
	}

	probes, tapState := overlayControlProbes[state]
	if !tapState {
		// Not an overlay this path owns (village, battle, search, unknown): the
		// caller stops asking, and nothing is tapped.
		return false
	}

	rule, ok := classifierRuleFor(cl, state)
	if !ok {
		logger.Warn().Str("state", state.String()).Msg("dismiss: no classifier rule to verify the control against; no tap fired")
		return false
	}
	for _, probe := range probes {
		chk, ok := rule.probeAt(probe)
		if !ok {
			continue
		}
		if !probeReadsAsControl(frame, cal, rule.Anchor, chk) {
			continue
		}
		x, y := cal.AnchorPoint(probe.X, probe.Y, rule.Anchor)
		if state == StateConfirmExit && !cal.IsReferenceGeometry() {
			// At non-reference geometry (1280x720), the probe (279,429 -> live 444,442)
			// lands on the upper border of the Cancel button (bounds: [435..556, 445..479]).
			// Nudge into the button's center so the click reliably registers.
			x += int(45.0 * (float64(cal.PhysicalW) / 1280.0))
			y += int(18.0 * (float64(cal.PhysicalH) / 720.0))
		}
		logger.Info().
			Str("state", state.String()).
			Int("x", x).Int("y", y).
			Msg("dismiss: tapping the control the classifier verified on this frame")
		if err := dev.TapRandomized(x, y); err != nil {
			logger.Warn().Err(err).Str("state", state.String()).Msg("dismiss: tap failed")
			return false
		}
		time.Sleep(overlayDismissSettle)
		return true
	}

	logger.Warn().Str("state", state.String()).
		Msg("dismiss: no verified control on this frame; no tap fired")
	return false
}

// stateStillOnFrame reports whether the classifier's rule for `state` matches the
// frame, through the same scorer ClassifyState uses (see Classifier.EvaluateRules).
func stateStillOnFrame(cl *Classifier, frame gocv.Mat, state GameState) bool {
	for _, verdict := range cl.EvaluateRules(frame, false) {
		if verdict.State == state {
			return verdict.Matched
		}
	}
	return false
}

// classifierRuleFor returns the classifier's authored rule for a state.
func classifierRuleFor(cl *Classifier, state GameState) (StateRule, bool) {
	rules := cl.GetRules()
	for i := range rules {
		if rules[i].State == state {
			return rules[i], true
		}
	}
	return StateRule{}, false
}

// probeAt returns the rule's own probe at a reference point, so a tap point can
// never carry a colour or tolerance of its own that drifts from the classifier.
func (r StateRule) probeAt(p image.Point) (PixelCheck, bool) {
	for _, chk := range r.Checks {
		if chk.X == p.X && chk.Y == p.Y {
			return chk, true
		}
	}
	return PixelCheck{}, false
}

// probeReadsAsControl re-reads the probe on the live frame, using the classifier's
// own distance and tolerance so the two can never disagree about what satisfies a
// probe. A probe that no longer reads as its control means the overlay is not where
// the tap would land, and the caller falls through to the next candidate.
func probeReadsAsControl(frame gocv.Mat, cal *Calibration, anchor Anchor, chk PixelCheck) bool {
	sx, sy := cal.AnchorPoint(chk.X, chk.Y, anchor)
	if sx < 0 || sy < 0 || sx >= frame.Cols() || sy >= frame.Rows() {
		return false
	}
	b := frame.GetUCharAt(sy, sx*3)
	g := frame.GetUCharAt(sy, sx*3+1)
	r := frame.GetUCharAt(sy, sx*3+2)
	dr := absDiff(int(r), int(chk.R))
	dg := absDiff(int(g), int(chk.G))
	db := absDiff(int(b), int(chk.B))
	return math.Sqrt(float64(dr*dr+dg*dg+db*db)) <= float64(chk.Tolerance)
}

// locateOkayButton finds the Welcome Back popup's Okay button in the live frame.
// The match runs on the frame the caller already captured (resized to the
// reference height, which is how the templates were captured) so the button is
// located on the same screen the popup was classified from.
func locateOkayButton(frame gocv.Mat, templates *TemplateStore, logger zerolog.Logger) (image.Point, bool) {
	if templates == nil {
		return image.Point{}, false
	}
	tpl, ok := templates.Get(welcomeBackOkayTemplate)
	if !ok {
		return image.Point{}, false
	}
	norm := vision.ResizeToHeight(frame, RefHeight)
	defer norm.Close()
	physScale := float64(frame.Rows()) / float64(RefHeight)

	pt, conf, err := vision.MatchTemplateRegion(norm, tpl, welcomeBackOkayRegion, 0.6)
	if err != nil || conf < 0.6 {
		return image.Point{}, false
	}
	logger.Debug().Float64("conf", conf).Msg("dismiss: located the Okay button")
	return image.Pt(int(float64(pt.X)*physScale), int(float64(pt.Y)*physScale)), true
}
