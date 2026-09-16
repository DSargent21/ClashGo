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
// MinPass 2 over the real result-panel anchors (sampled live at 12:30) so
// genuine result screens are still recognized after the fix.
func TestClassify_BattleEndStillDetected(t *testing.T) {
	c := newChestClassifier(t)

	m := stuckVillageFrame()
	defer m.Close()
	setRGB(m, 430, 240, 0xF1, 0xCB, 0x53) // gold star/bonus band
	setRGB(m, 430, 120, 0xF7, 0xFD, 0xFE) // white header above the stars

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
