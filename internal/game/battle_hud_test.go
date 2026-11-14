package game

import (
	"os"
	"testing"

	"github.com/rs/zerolog"
	"gocv.io/x/gocv"
)

// battleHUDFixture paints a frame the way the live one looked: a village-coloured
// field, optionally with the SURRENDER pill in the bottom-left corner. The pill's
// colour and position are the live measurements recorded in battle_hud.go.
func battleHUDFixture(t *testing.T, w, h int, pill bool, dim float64) gocv.Mat {
	t.Helper()
	buf := make([]byte, w*h*3)
	for i := 0; i+2 < len(buf); i += 3 {
		// Village grass, BGR.
		buf[i] = uint8(float64(62) * dim)
		buf[i+1] = uint8(float64(141) * dim)
		buf[i+2] = uint8(float64(74) * dim)
	}
	if pill {
		// The measured pill: face (253,93,100), a white SURRENDER label, at
		// 1.8-14.2% of the width and 75.4-79.4% of the height.
		x0, x1 := int(0.018*float64(w)), int(0.142*float64(w))
		y0, y1 := int(0.754*float64(h)), int(0.794*float64(h))
		labelY0, labelY1 := y0+(y1-y0)/2, y0+(y1-y0)/2+(y1-y0)/4
		for y := y0; y < y1; y++ {
			for x := x0; x < x1; x++ {
				i := (y*w + x) * 3
				r, g, b := 253.0, 93.0, 100.0
				if y >= labelY0 && y < labelY1 && x > x0+(x1-x0)/4 && x < x1-(x1-x0)/4 {
					r, g, b = 255, 251, 252
				}
				buf[i] = uint8(b * dim)
				buf[i+1] = uint8(g * dim)
				buf[i+2] = uint8(r * dim)
			}
		}
	}
	frame, err := gocv.NewMatFromBytes(h, w, gocv.MatTypeCV8UC3, buf)
	if err != nil {
		t.Fatalf("build %dx%d fixture: %v", w, h, err)
	}
	return frame
}

// TestLiveBattleHUDDetectsTheSurrenderPill pins the marker at every geometry the
// bot has been validated at. The veto samples relative coordinates, so the pill
// must be found wherever the viewport places it.
func TestLiveBattleHUDDetectsTheSurrenderPill(t *testing.T) {
	for _, size := range []struct{ w, h int }{{1024, 768}, {1280, 720}, {860, 732}} {
		frame := battleHUDFixture(t, size.w, size.h, true, 1)
		if !liveBattleHUD(frame) {
			t.Errorf("%dx%d: SURRENDER pill not detected", size.w, size.h)
		}
		frame.Close()
	}
}

// TestLiveBattleHUDIgnoresTheVillage makes sure the veto cannot fire on a normal
// village frame — that would send the bot into an attack sequence from the
// village every frame.
func TestLiveBattleHUDIgnoresTheVillage(t *testing.T) {
	frame := battleHUDFixture(t, 1024, 768, false, 1)
	defer frame.Close()
	if liveBattleHUD(frame) {
		t.Fatal("village frame reported as a live battle")
	}
}

// TestLiveBattleHUDIgnoresADimmedBattle documents the deliberate safe direction:
// a modal dims the battle behind it, the pill falls below the threshold, and the
// modal's own rule decides the state. If this ever reports true, an "Update
// available" or "Connection lost" dialog over a battle would be swallowed by the
// veto and the bot would never dismiss it.
func TestLiveBattleHUDIgnoresADimmedBattle(t *testing.T) {
	frame := battleHUDFixture(t, 1024, 768, true, 0.5)
	defer frame.Close()
	if liveBattleHUD(frame) {
		t.Fatal("dimmed battle reported as a live battle; the veto would hide modal dialogs")
	}
}

// TestStateIsObstruction guards the exemption list: the states that unblock the
// bot must never be overridden by the battle HUD.
func TestStateIsObstruction(t *testing.T) {
	for _, s := range []GameState{StateConnectionLost, StateUpdatePrompt, StateOfferPopup, StateGemDialog, StateWelcomeBack} {
		if !stateIsObstruction(s) {
			t.Errorf("%s must be exempt from the battle veto", s)
		}
	}
	for _, s := range []GameState{StateChestReward, StateMainVillage, StateUnknown, StateBattleEnd} {
		if stateIsObstruction(s) {
			t.Errorf("%s must not be treated as an obstruction", s)
		}
	}
}

// TestClassifyStateOnTheLiveBattleFrame is the regression for the run that
// produced the fixture: a real 1024x768 battle frame (full army, 0% damage, red
// SURRENDER pill) that the classifier called ChestReward at score 405, which made
// the bot run the chest dismisser and never deploy.
func TestClassifyStateOnTheLiveBattleFrame(t *testing.T) {
	// Deliberately outside testdata/corpus: that manifest carries only the
	// reference and 720p geometries (its harness rejects other sizes), while this
	// frame is the 1024x768 evidence for the veto.
	const path = "testdata/live/battle_live_1024x768.png"
	if _, err := os.Stat(path); err != nil {
		t.Skipf("%s is not present (live captures are gitignored)", path)
	}
	frame := gocv.IMRead(path, gocv.IMReadColor)
	if frame.Empty() {
		t.Fatalf("could not read %s", path)
	}
	defer frame.Close()

	cal := NewCalibration(frame.Cols(), frame.Rows())
	cal.SetDisplayScale(1.1334)
	c := NewClassifier(cal, DefaultClassifierConfig(), zerolog.Nop())

	state, score := c.ClassifyState(frame)
	if state != StateBattle {
		t.Fatalf("live battle frame classified as %s (score %d); the bot would dismiss a chest that is not there and never deploy", state, score)
	}
	t.Logf("live battle frame -> %s score=%d", state, score)
}
