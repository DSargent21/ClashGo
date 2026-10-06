package game

import (
	"image"
	"math"
	"testing"

	"github.com/rs/zerolog"
)

// updatePromptRule returns the classifier's authored rule for CoC's login-time
// "Update Available!" prompt, so a test cannot drift from the numbers the
// classifier actually scores.
func updatePromptRule(t *testing.T, cl *Classifier) StateRule {
	t.Helper()
	rules := cl.GetRules()
	for i := range rules {
		if rules[i].State == StateUpdatePrompt {
			return rules[i]
		}
	}
	t.Fatal("no StateUpdatePrompt rule")
	return StateRule{}
}

// isUpdateButtonProbe separates the Update button's solid green band from the
// two panel-body probes. The body colour is neutral (0xE8,0xE8,0xE0) and the
// button's green is not, so the green channel decides.
func isUpdateButtonProbe(chk PixelCheck) bool { return int(chk.G) > int(chk.R) }

// TestClassify_UpdatePromptFiresOnItsOwnEvidence pins that the new rule
// recognizes the screen it was measured on at all.
func TestClassify_UpdatePromptFiresOnItsOwnEvidence(t *testing.T) {
	c := newChestClassifier(t)
	rule := updatePromptRule(t, c)

	m := stuckVillageFrame()
	defer m.Close()
	for _, chk := range rule.Checks {
		setRGB(m, chk.X, chk.Y, chk.R, chk.G, chk.B)
	}

	if state, _ := c.ClassifyState(m); state != StateUpdatePrompt {
		t.Fatalf("expected StateUpdatePrompt on its own probe evidence, got %s", state)
	}
}

// TestClassify_UpdatePromptNeedsTheUpdateButton is the evidence bar of the rule.
// The prompt's light-gray body is the part it shares with the quit-confirm
// dialog — the same body colour, the same probe style — so a body-only match is
// exactly the ambiguity that made the quit rule fire on this prompt
// (TestClassify_ConfirmExitNeedsCancelProbe) and left the boot stuck for two
// minutes on 2026-09-29. The green Update band has to be there.
//
// MinPass is asserted as a count rather than a number for the reason
// TestConfirmExitRuleRequiresEveryProbe gives: the rule and its dismissal have to
// agree, and a bar that can drop a probe can drop the only thing the rule is
// about.
func TestClassify_UpdatePromptNeedsTheUpdateButton(t *testing.T) {
	c := newChestClassifier(t)
	rule := updatePromptRule(t, c)

	if rule.MinPass != len(rule.Checks) {
		t.Errorf("UpdatePrompt MinPass=%d over %d probes: a match may be missing the Update band, the one thing that tells this prompt apart from the quit dialog",
			rule.MinPass, len(rule.Checks))
	}

	m := stuckVillageFrame()
	defer m.Close()
	body, button := 0, 0
	for _, chk := range rule.Checks {
		if isUpdateButtonProbe(chk) {
			button++
			continue
		}
		body++
		setRGB(m, chk.X, chk.Y, chk.R, chk.G, chk.B)
	}
	if body == 0 || button == 0 {
		t.Fatalf("rule has %d body probes and %d button probes; this test needs both kinds", body, button)
	}

	if state, _ := c.ClassifyState(m); state == StateUpdatePrompt {
		t.Fatalf("UpdatePrompt fired on the panel body alone (state=%s); that is the evidence the quit-confirm dialog carries too", state)
	}
}

// TestUpdatePromptOutsideTapIsClearOfControls pins the two properties the
// outside-panel tap depends on, at the reference geometry and at the live device
// geometry the prompt was measured on. This is the one dismissal whose point is
// not a classifier probe (see overlayOutsideTapStates), so it is the one that
// needs an invariant of its own:
//
//  1. it is outside the panel, to the left of everything the rule measured
//     there — the prompt dismisses on a touch outside it, while its own Update
//     button would send the game to the app store; and
//  2. it is clear of the HUD, so a touch that lands after the prompt has already
//     gone falls on open ground rather than on the Attack! button's side of the
//     frame or into the army bar.
func TestUpdatePromptOutsideTapIsClearOfControls(t *testing.T) {
	pt, ok := overlayOutsideTapStates[StateUpdatePrompt]
	if !ok {
		t.Fatal("UpdatePrompt has no outside-panel tap point, so the prompt has no dismissal at all")
	}
	if len(overlayOutsideTapStates) != 1 {
		t.Errorf("the outside-tap table has %d entries; every one of them is a tap outside its panel and each needs this reasoning",
			len(overlayOutsideTapStates))
	}

	for _, geom := range []struct {
		name string
		w, h int
		k    float64
	}{
		{"reference 860x732", RefWidth, RefHeight, 0},
		{"device 1280x720", 1280, 720, 1.325},
	} {
		t.Run(geom.name, func(t *testing.T) {
			cal := NewCalibration(geom.w, geom.h)
			if geom.k > 0 {
				cal.SetDisplayScale(geom.k)
			}
			cl := NewClassifier(cal, DefaultClassifierConfig(), zerolog.Nop())
			rule := updatePromptRule(t, cl)

			x, y := cal.AnchorPoint(pt.X, pt.Y, rule.Anchor)

			minX := math.MaxInt
			for _, chk := range rule.Checks {
				sx, _ := cal.AnchorPoint(chk.X, chk.Y, rule.Anchor)
				if sx < minX {
					minX = sx
				}
			}
			if x >= minX {
				t.Errorf("outside-panel tap maps to x=%d, at or right of the panel's leftmost measured pixel (x=%d): the touch would land ON the prompt",
					x, minX)
			}

			colRight := cal.X(int(math.Round(hudXLowBand*RefWidth)), AnchorLow)
			if x <= colRight {
				t.Errorf("outside-panel tap maps to x=%d, at or left of the left HUD column's right edge (x=%d): a touch that outlives the prompt would hit a button",
					x, colRight)
			}

			topBand := cal.Y(int(math.Round(hudYLowBand*RefHeight)), AnchorLow)
			bottomBand := cal.Y(int(math.Round(hudYHighBand*RefHeight)), AnchorHigh)
			if y <= topBand || y >= bottomBand {
				t.Errorf("outside-panel tap maps to y=%d, outside the clear mid-band (%d..%d) between the top bar and the bottom stack",
					y, topBand, bottomBand)
			}
		})
	}
}

// TestDismissOverlayUpdatePromptTapsOutsideThePanel: the prompt's one button
// leaves the game for the app store, so the dismissal must be a touch outside the
// panel — exactly one, at the measured point, and never on the Update button.
// Gate 1 still applies: a frame without the prompt's own probes gets no input.
func TestDismissOverlayUpdatePromptTapsOutsideThePanel(t *testing.T) {
	cl := newDismissClassifier()
	rule := updatePromptRule(t, cl)

	dev := newFrameDevice(overlayEvidenceFrame(t, cl, StateUpdatePrompt))
	defer dev.Close()

	if acted := DismissOverlay(dev, cl.cal, cl, StateUpdatePrompt, nil, zerolog.Nop()); !acted {
		t.Fatal("update-available prompt was not dismissed")
	}
	if len(dev.recorded) != 1 {
		t.Fatalf("want exactly 1 tap, got %d (%v)", len(dev.recorded), dev.recorded)
	}

	pt := overlayOutsideTapStates[StateUpdatePrompt]
	wantX, wantY := cl.cal.AnchorPoint(pt.X, pt.Y, rule.Anchor)
	if got := dev.recorded[0]; got != image.Pt(wantX, wantY) {
		t.Errorf("tapped %v, want the measured outside-panel point %v", got, image.Pt(wantX, wantY))
	}
	for _, chk := range rule.Checks {
		if !isUpdateButtonProbe(chk) {
			continue
		}
		bx, by := cl.cal.AnchorPoint(chk.X, chk.Y, rule.Anchor)
		if dev.recorded[0] == image.Pt(bx, by) {
			t.Fatalf("tapped the Update button at (%d,%d); that leaves the game for the app store", bx, by)
		}
	}

	blank := newFrameDevice(blankFrame())
	defer blank.Close()
	if acted := DismissOverlay(blank, cl.cal, cl, StateUpdatePrompt, nil, zerolog.Nop()); acted {
		t.Error("acted on a frame that carries none of the prompt's evidence")
	}
	if len(blank.recorded) != 0 {
		t.Errorf("an unverified frame produced input: %v", blank.recorded)
	}
}
