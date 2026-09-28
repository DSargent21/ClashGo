package game

import "image"

// Search windows for the UI buttons the bot looks for, in reference
// coordinates.
//
// A full-frame template match costs roughly (frame area / window area) times as
// much as a windowed one, and the classifier used to search the whole frame for
// every template-bearing rule. That is the single most expensive thing a rule
// does: on the live 1280x720 geometry a full-frame, three-scale match is
// ~200 ms, while the same match inside the Attack! button's window (~12 % of the
// frame) is ~25 ms. Measured with the corpus benchmarks — see the "classifier
// search windows" entry in docs/PERFORMANCE.md.
//
// Every window here is a *click* window the bot already trusts, not a new
// guess: these rectangles were authored for the tap paths, where a wrong window
// means a missed button, so they carry the tightest bound the project is
// willing to bet on. Sharing the table is what keeps the classifier's search
// from drifting away from the window the click uses for the same button.
//
// A template with no entry keeps the historical full-frame search: windowing a
// button whose position has never been measured would trade a real detection
// for a guess.
var buttonROIBounds = map[string]image.Rectangle{
	// HUD chrome, authored for the tap paths (bot.buttonROI / waitForButton).
	"btn_attack":     image.Rect(0, 500, 300, 732),   // Attack! button, bottom-left
	"btn_find_match": image.Rect(50, 400, 400, 600),  // Find a Match, centre-left panel
	"btn_battle":     image.Rect(300, 150, 860, 732), // green Battle button, army bar
	"btn_next":       image.Rect(600, 450, 860, 732), // Next Match, bottom-right
	"btn_army_arrow": image.Rect(350, 100, 700, 300),
	"btn_army_1":     image.Rect(400, 150, 650, 350),

	// Centred dialog buttons. btn_okay is the welcome-back OK button and
	// btn_return_home the result overlay's RETURN HOME; both windows are built
	// around the coordinates the bot already taps (pinpointButtons: (430,520)
	// and (431,581)) with room for the button artwork and its shadow. The
	// navigator's own welcome-back dismissal searches (200,350,660,650) for
	// btn_okay, so this window is deliberately tighter than the one the bot
	// already uses for the same button.
	"btn_okay":        image.Rect(280, 420, 580, 620),
	"btn_return_home": image.Rect(280, 480, 590, 700),
}

// ButtonROIBounds returns the authored reference-geometry search window for a
// known button template. The bool is false for a template the project has never
// measured a window for; callers must then fall back to the whole frame rather
// than invent one.
func ButtonROIBounds(templateName string) (image.Rectangle, bool) {
	r, ok := buttonROIBounds[templateName]
	return r, ok
}

// ReferenceFrame is the whole authored 860x732 screen, i.e. the search window
// of a template the project has no measured window for.
func ReferenceFrame() image.Rectangle {
	return image.Rect(0, 0, RefWidth, RefHeight)
}

// RuleSearchROI maps the authored search window of a rule's template into the
// live frame. Full-frame is the fallback in every case where the mapping cannot
// be trusted, because the two failure modes are not symmetric: a window that is
// too small loses evidence and changes a state verdict, while a window that is
// too large only costs time — and the frame is the largest honest window.
//
// The anchor picks the mapping the rule's own probes use (see
// Calibration.AnchorPoint), so a HUD button's window keeps its distance to the
// screen edges while a centred dialog button's window follows the centre.
//
// HUD windows are mapped per edge (Calibration.MapRect), not by the
// whole-rectangle heuristic Calibration.HudRect uses for taps. The two models
// disagree for a window whose edges fall in different bands — and they disagree
// in the direction that matters: with a display scale that does not match the
// frame, an edge-mapped window drifts with the button's own tap point while a
// whole-rect window picks a different anchor and can leave the button entirely.
// TestRuleWindowsContainPinpoints in internal/bot asserts the button stays
// inside the window at every geometry.
func RuleSearchROI(cal *Calibration, templateName string, anchor Anchor, frame image.Rectangle) image.Rectangle {
	ref, ok := ButtonROIBounds(templateName)
	if !ok || cal == nil {
		// A caller with no calibration has no mapping information, so it also
		// has no basis for narrowing anything.
		return frame
	}
	var mapped image.Rectangle
	switch anchor {
	case AnchorCenter:
		mapped = cal.CentreRect(ref)
	case AnchorCenterBottom:
		mapped = cal.Rect2(ref, AnchorMid, AnchorHigh)
	default:
		mapped = cal.MapRect(ref, AnchorEdge)
	}
	// Intersect rather than clamp: matching a region lets the helper do its own
	// clamping, but an empty intersection means the window mapped entirely off
	// this frame, which is exactly the case that must fall back to the frame.
	mapped = mapped.Intersect(frame)
	if mapped.Dx() < 2 || mapped.Dy() < 2 {
		return frame
	}
	return mapped
}
