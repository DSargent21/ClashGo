package game

import (
	"testing"

	"gocv.io/x/gocv"
)

// stuckVillageFrame builds an 860x732 CV8UC3 Mat filled with a mid-gray
// that is deliberately far from every rule's signature pixels, so a test
// can stamp exactly the anchors it wants to reason about and nothing
// else fires accidentally.
func stuckVillageFrame() gocv.Mat {
	m := gocv.NewMatWithSize(RefHeight, RefWidth, gocv.MatTypeCV8UC3)
	m.SetTo(gocv.NewScalar(0x80, 0x80, 0x80, 0)) // neutral mid-gray
	return m
}

// TestClassify_ArmyCampNeedsBothAnchors is the regression test for the
// main-village -> ArmyCamp false positive behind the stuck loop. The
// rule used MinPass 1, and its loose brown anchor at (479,149) also
// passes on ordinary village frames (live village sampled RGB(61,53,62),
// within tolerance 25 of 0x4D3E33). processFrame then pressed Back, which
// on the real village opens CoC's quit-confirm dialog and wedged the bot
// (observed live: village -> ArmyCamp -> Back -> 5min grace -> emergency
// restart, repeating). With MinPass 2 the brown anchor alone must never
// classify as ArmyCamp.
func TestClassify_ArmyCampNeedsBothAnchors(t *testing.T) {
	c := newChestClassifier(t)

	m := stuckVillageFrame()
	defer m.Close()
	setRGB(m, 479, 149, 0x4D, 0x3E, 0x33) // the loose brown anchor, village-like

	if state, _ := c.ClassifyState(m); state == StateArmyCamp {
		t.Fatalf("ArmyCamp fired with only the brown anchor (state=%s)", state)
	}
}

// TestClassify_ArmyCampDetectedWithBothAnchors pins the positive case so
// tightening MinPass did not disable genuine army-camp detection.
func TestClassify_ArmyCampDetectedWithBothAnchors(t *testing.T) {
	c := newChestClassifier(t)

	m := stuckVillageFrame()
	defer m.Close()
	setRGB(m, 529, 149, 0xF1, 0x55, 0x4F) // red tab header
	setRGB(m, 479, 149, 0x4D, 0x3E, 0x33) // brown tab header

	if state, _ := c.ClassifyState(m); state != StateArmyCamp {
		t.Fatalf("expected StateArmyCamp, got %s", state)
	}
}

// TestClassify_ConfirmExitDialogDetected covers CoC's "Do you want to
// quit the game?" dialog, which previously had NO rule. When a
// misclassified ArmyCamp frame pressed Back on the real village, the bot
// landed here, sat on it for the whole boot-splash grace and then force
// restarted — the second half of the stuck loop.
func TestClassify_ConfirmExitDialogDetected(t *testing.T) {
	c := newChestClassifier(t)

	m := stuckVillageFrame()
	defer m.Close()
	setRGB(m, 497, 431, 0xD6, 0xF4, 0x76) // green Okay button
	setRGB(m, 279, 429, 0xFE, 0xC3, 0x69) // orange Cancel button
	setRGB(m, 430, 340, 0xE8, 0xE8, 0xE0) // light-gray dialog body

	if state, _ := c.ClassifyState(m); state != StateConfirmExit {
		t.Fatalf("expected StateConfirmExit, got %s", state)
	}
}

// TestConfirmExitRuleRequiresEveryProbe keeps the quit-confirm rule's
// evidence bar equal to its own probe count. Those probes have to stay
// inseparable: DismissOverlay taps exactly one control for this state — the
// orange Cancel face — and refuses to fire when that probe cannot be re-read
// on the frame (game.overlayControlProbes). A rule that matches while its
// control is absent can therefore only ever produce the loop of 2026-09-29:
// state=ConfirmExit every frame, "no verified control on this frame; no tap
// fired", watchdog restart, same dialog after the relaunch. Adding a probe
// without raising MinPass (or lowering MinPass) re-opens exactly that, so this
// asserts the count rather than the numbers.
func TestConfirmExitRuleRequiresEveryProbe(t *testing.T) {
	c := newChestClassifier(t)

	probes := overlayControlProbes[StateConfirmExit]
	if len(probes) == 0 {
		t.Fatal("ConfirmExit has no dismissal control; the rule below cannot be checked against one")
	}

	for _, rule := range c.GetRules() {
		if rule.State != StateConfirmExit {
			continue
		}
		if rule.MinPass != len(rule.Checks) {
			t.Errorf("ConfirmExit MinPass=%d over %d probes: a match may be missing the dismissal control %v, which is the detected-but-undismissable deadlock",
				rule.MinPass, len(rule.Checks), probes)
		}
		for _, p := range probes {
			found := false
			for _, chk := range rule.Checks {
				if chk.X == p.X && chk.Y == p.Y {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("ConfirmExit dismissal control (%d,%d) is not one of the rule's probes; the rule could fire on a dialog it cannot dismiss", p.X, p.Y)
			}
		}
		return
	}
	t.Fatal("no ConfirmExit rule")
}

// TestClassify_ConfirmExitNeedsCancelProbe is the regression test for the
// false positive above. CoC's "Update Available! / please update your game"
// dialog hits two of the rule's three probes (the green button face and the
// light-gray body: live 729,446 and 640,326 on this project's 1280x720
// device), and at MinPass 2 that was enough to classify as the quit dialog.
// Those two alone must not.
func TestClassify_ConfirmExitNeedsCancelProbe(t *testing.T) {
	c := newChestClassifier(t)

	m := stuckVillageFrame()
	defer m.Close()
	setRGB(m, 497, 431, 0xD6, 0xF4, 0x76) // green Okay face (the update dialog's button area)
	setRGB(m, 430, 340, 0xE8, 0xE8, 0xE0) // light-gray body

	if state, _ := c.ClassifyState(m); state == StateConfirmExit {
		t.Fatalf("ConfirmExit fired without its orange Cancel face (state=%s); the bot would refuse to tap it forever", state)
	}
}

// TestClassify_ConnectionLostDialogDetected covers the game's disconnect
// dialog. It renders its own RETURN HOME button, so the old template-only
// BattleEnd rule (MinPass 0) matched it and the bot tapped dead
// result-screen coordinates forever.
func TestClassify_ConnectionLostDialogDetected(t *testing.T) {
	c := newChestClassifier(t)

	m := stuckVillageFrame()
	defer m.Close()
	setRGB(m, 300, 478, 0xCB, 0xE6, 0xFF) // TRY AGAIN button
	setRGB(m, 431, 581, 0xCB, 0xE6, 0xFF) // RETURN HOME button
	setRGB(m, 430, 520, 0x1A, 0x1C, 0x1E) // dimmed panel between them

	state, _ := c.ClassifyState(m)
	if state == StateBattleEnd {
		t.Fatal("connection-lost dialog classified as BattleEnd; bot would tap dead result coordinates")
	}
	if state != StateConnectionLost {
		t.Fatalf("expected StateConnectionLost, got %s", state)
	}
}

// TestClassify_BattleEndStillDetected pins the tightened BattleEnd rule:
// MinPass 2 over the result-panel anchors, so genuine result screens are still
// recognized after the fix. The anchors below are the ones re-measured on a live
// defeat overlay (2026-09-16); the previous pair (a white header and a light
// blue-gray sub-band at x=430) read the dimmed battlefield instead and the rule
// never fired, which hung the battle-end wait after every battle.
func TestClassify_BattleEndStillDetected(t *testing.T) {
	c := newChestClassifier(t)

	m := stuckVillageFrame()
	defer m.Close()
	setRGB(m, 300, 240, 0xED, 0xCE, 0x5E) // gold star/bonus band, left flank
	setRGB(m, 560, 240, 0xF0, 0xD4, 0x70) // gold band, right flank
	setRGB(m, 431, 600, 0x6C, 0xBB, 0x1F) // RETURN HOME button face

	if state, _ := c.ClassifyState(m); state != StateBattleEnd {
		t.Fatalf("expected StateBattleEnd, got %s", state)
	}
}

// TestClassify_BattleEndNeedsPanelPixels guards the exact loosening that
// caused the misread: a template-only match (MinPass 0) is no longer
// enough, so a single stray band pixel must not produce BattleEnd.
func TestClassify_BattleEndNeedsPanelPixels(t *testing.T) {
	c := newChestClassifier(t)

	m := stuckVillageFrame()
	defer m.Close()
	setRGB(m, 430, 240, 0xF1, 0xCB, 0x53) // single gold pixel only

	if state, _ := c.ClassifyState(m); state == StateBattleEnd {
		t.Fatal("BattleEnd fired on a single panel pixel (MinPass tightening regressed)")
	}
}
