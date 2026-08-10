package game

import (
	"testing"

	"gocv.io/x/gocv"
)

// makeSplashFrame builds an 860x732 CV8UC3 Mat pre-filled with a dark
// neutral background (the shared backdrop of all post-boot splash
// screens) so tests only have to stamp the distinctive signature pixels.
//
// The value (60,54,50) is deliberately NOT within tolerance of any
// splash check pixel (corner dark-bg check is (20,13,30)/tol 30 → dist
// ~60; logo dark-left check is (47,26,14)/tol 35 → dist ~47), so tests
// that stamp only one signature pixel can assert MinPass behavior
// without the fill contributing accidental matches.
func makeSplashFrame() gocv.Mat {
	m := gocv.NewMatWithSize(RefHeight, RefWidth, gocv.MatTypeCV8UC3)
	m.SetTo(gocv.NewScalar(60, 54, 50, 0)) // dark neutral background
	return m
}

// TestClassify_TapToContinueSplash asserts the post-boot "ТАР!"
// tap-to-continue / collect splash is recognized. Signature pixels are
// the beige prompt text, dark corners, and red/orange artwork — sampled
// live from the frozen collect screen that was misread as Battle and
// forced the bot into an endless restart loop.
func TestClassify_TapToContinueSplash(t *testing.T) {
	c := newChestClassifier(t)

	m := makeSplashFrame()
	setRGB(m, 414, 182, 0xE1, 0xCF, 0xBC) // beige ТАР! text (glyph)
	setRGB(m, 450, 185, 0xE2, 0xD0, 0xBC) // beige ТАР! text (glyph)
	setRGB(m, 40, 40, 0x14, 0x0D, 0x1E)   // dark top-left corner
	defer m.Close()

	state, _ := c.ClassifyState(m)
	if state != StateTapToContinue {
		t.Fatalf("expected StateTapToContinue, got %s", state)
	}
}

// TestClassify_TapToContinueNeedsTwoSignals guards against a single
// warm pixel elsewhere on the screen tripping the rule: only the text
// pixel is set, so the state must NOT fire (MinPass=2).
func TestClassify_TapToContinueNeedsTwoSignals(t *testing.T) {
	c := newChestClassifier(t)

	m := makeSplashFrame()
	setRGB(m, 414, 182, 0xE1, 0xCF, 0xBC) // beige text only (one glyph)
	defer m.Close()

	state, _ := c.ClassifyState(m)
	if state == StateTapToContinue {
		t.Fatalf("TapToContinue fired with only one signature pixel")
	}
}

// TestClassify_NewsSplash asserts the post-boot news/announcement splash
// (e.g. the "Meteor Golem" troop intro) with its light-green Continue
// button is recognized.
func TestClassify_NewsSplash(t *testing.T) {
	c := newChestClassifier(t)

	m := makeSplashFrame()
	setRGB(m, 403, 535, 0xBE, 0xEA, 0x8C) // green Continue button
	setRGB(m, 403, 525, 0xFD, 0xFF, 0xF6) // button top highlight
	setRGB(m, 430, 200, 0xFE, 0xCA, 0xFF) // pink announcement art
	defer m.Close()

	state, _ := c.ClassifyState(m)
	if state != StateNewsSplash {
		t.Fatalf("expected StateNewsSplash, got %s", state)
	}
}

// TestClassify_LogoSplash asserts the CoC castle logo / connecting
// splash (static for 1-3 min after the tap-to-continue screen) is
// recognized so the stuck-watchdog doesn't force-restart mid-boot.
func TestClassify_LogoSplash(t *testing.T) {
	c := newChestClassifier(t)

	m := makeSplashFrame()
	setRGB(m, 430, 220, 0xFF, 0x74, 0xD0) // pink/red logo art
	setRGB(m, 430, 320, 0xFF, 0xE7, 0xE0) // light logo band
	setRGB(m, 60, 400, 0x2F, 0x1A, 0x0E)  // dark left background
	defer m.Close()

	state, _ := c.ClassifyState(m)
	if state != StateLogo {
		t.Fatalf("expected StateLogo, got %s", state)
	}
}

// TestClassify_SplashRulesQuietOnMainVillage ensures the boot-splash
// rules do not false-positive on a MainVillage frame (the chest test's
// negative fixture). The village has a bright HUD and no dark splash
// backdrop, so none of the new states should fire.
func TestClassify_SplashRulesQuietOnMainVillage(t *testing.T) {
	c := newChestClassifier(t)

	m := gocv.NewMatWithSize(RefHeight, RefWidth, gocv.MatTypeCV8UC3)
	defer m.Close()
	m.SetTo(gocv.NewScalar(0x2A, 0x5F, 0x3A, 0)) // bright grass
	setRGB(m, 830, 35, 0xB2, 0x90, 0x0F)         // gold storage icon
	setRGB(m, 830, 95, 0x54, 0x19, 0x59)         // elixir storage icon

	state, _ := c.ClassifyState(m)
	switch state {
	case StateTapToContinue, StateNewsSplash, StateLogo:
		t.Fatalf("boot-splash rule falsely fired on MainVillage frame (state=%s)", state)
	}
}
