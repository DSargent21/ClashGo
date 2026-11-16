package game

import (
	"image"
	"os"
	"testing"

	"github.com/rs/zerolog"
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

// TestClassify_TapToContinueNeedsEverySignal guards against warm pixels
// elsewhere on the screen tripping the rule: the two parchment points are set
// but the dark corner is not, so the state must NOT fire. This is the live
// Leagues-window false positive that made the bot tap league cards.
func TestClassify_TapToContinueNeedsEverySignal(t *testing.T) {
	c := newChestClassifier(t)

	m := makeSplashFrame()
	setRGB(m, 414, 182, 0xE1, 0xCF, 0xBC) // parchment text points…
	setRGB(m, 450, 185, 0xE2, 0xD0, 0xBC) // …without the dark corner
	defer m.Close()

	state, _ := c.ClassifyState(m)
	if state == StateTapToContinue {
		t.Fatalf("TapToContinue fired on a bright panel: the dark corner is missing")
	}
}

// TestClassify_BrightLeaguesPanelIsNotASplash pins the live false positive:
// the Leagues window is warm parchment at both text points and bright elsewhere,
// so it must classify as something other than the tap-to-continue splash.
func TestClassify_BrightLeaguesPanelIsNotASplash(t *testing.T) {
	c := newChestClassifier(t)

	m := makeSplashFrame()
	m.SetTo(gocv.NewScalar(120, 90, 70, 0)) // warm parchment everywhere
	setRGB(m, 414, 182, 0xE1, 0xCF, 0xBC)
	setRGB(m, 450, 185, 0xE2, 0xD0, 0xBC)
	defer m.Close()

	state, _ := c.ClassifyState(m)
	if state == StateTapToContinue {
		t.Fatalf("a bright parchment panel must not classify as the boot splash")
	}
}

// TestClassify_LoadingScreenTapPrompt pins the live 2026-11-16 defect: the
// loading screen's "Tap!" prompt on the wooden table was unknown to the
// classifier, so the bot restart-looped on it instead of tapping it away.
// Probes are the prompt's white glyph row plus the dark vignette.
func TestClassify_LoadingScreenTapPrompt(t *testing.T) {
	c := newChestClassifier(t)

	m := makeSplashFrame()
	setRGB(m, 435, 247, 0xF8, 0xF7, 0xF8) // white glyph
	setRGB(m, 415, 250, 0xFF, 0xFF, 0xFF) // white glyph
	setRGB(m, 375, 253, 0xFA, 0xFA, 0xFA) // white glyph
	setRGB(m, 40, 40, 0x14, 0x0D, 0x1E)   // dark vignette
	setRGB(m, 430, 398, 0x98, 0x26, 0x0B) // red chest front (the control)
	defer m.Close()

	state, _ := c.ClassifyState(m)
	if state != StateTapToContinue {
		t.Fatalf("expected StateTapToContinue for the loading Tap! prompt, got %s", state)
	}
}

// TestClassify_LoadingTapPromptQuietOnVillage: the same probes sampled from a
// live village frame (bright sky, grass) must not fire the prompt rule.
func TestClassify_LoadingTapPromptQuietOnVillage(t *testing.T) {
	c := newChestClassifier(t)

	m := gocv.NewMatWithSize(RefHeight, RefWidth, gocv.MatTypeCV8UC3)
	defer m.Close()
	m.SetTo(gocv.NewScalar(0x2A, 0x5F, 0x3A, 0)) // grass
	setRGB(m, 435, 247, 0x6B, 0xCE, 0xDD)        // sky
	setRGB(m, 415, 250, 0x84, 0xB7, 0x3D)        // grass highlight
	setRGB(m, 375, 253, 0x7D, 0x9E, 0x28)
	setRGB(m, 40, 40, 0x07, 0x17, 0x01)

	state, _ := c.ClassifyState(m)
	if state == StateTapToContinue {
		t.Fatal("village colours fired the loading Tap! prompt rule")
	}
}

// TestClassify_LoadingTableRewardContinue covers the event-reward popup that
// follows the prompt on the same table: green Continue button + white headline.
func TestClassify_LoadingTableRewardContinue(t *testing.T) {
	c := newChestClassifier(t)

	m := makeSplashFrame()
	setRGB(m, 419, 557, 0x8B, 0xD4, 0x3A) // green button face
	setRGB(m, 420, 530, 0xBF, 0xEA, 0x8E) // top highlight
	setRGB(m, 414, 205, 0xFF, 0xFF, 0xFF) // white headline
	defer m.Close()

	state, _ := c.ClassifyState(m)
	if state != StateNewsSplash {
		t.Fatalf("expected StateNewsSplash for the loading-table reward, got %s", state)
	}
}

// TestClassify_LoadingRewardIsNotTheTapPrompt: the reward's own pixels must
// not satisfy the Tap! prompt's white-glyph row.
func TestClassify_LoadingRewardIsNotTheTapPrompt(t *testing.T) {
	c := newChestClassifier(t)

	m := makeSplashFrame()
	setRGB(m, 435, 247, 0x3F, 0x40, 0x52)
	setRGB(m, 415, 250, 0x61, 0x61, 0x71)
	setRGB(m, 375, 253, 0x42, 0x43, 0x54)
	setRGB(m, 40, 40, 0x43, 0x25, 0x27)
	setRGB(m, 419, 557, 0x8B, 0xD4, 0x3A)
	setRGB(m, 420, 530, 0xBF, 0xEA, 0x8E)
	defer m.Close()

	state, _ := c.ClassifyState(m)
	if state == StateTapToContinue {
		t.Fatal("the loading-table reward fired the Tap! prompt rule")
	}
}

// TestDismissLoadingChestFromTheLiveFrame drives the WHOLE dismissal path against
// the real 1024x768 frame that restart-looped the bot after the 2026-11-16
// disconnect: classify it, then let DismissOverlay pick its tap point. The tap has
// to be the chest front (live 512,420) — the metal handle the old candidate
// pointed at (495,253) advanced nothing live — with no Back and no other input.
//
// The frame is a live capture and stays out of the repo like the other
// testdata/live fixtures, so this gate skips on a machine that never saw it.
func TestDismissLoadingChestFromTheLiveFrame(t *testing.T) {
	const path = "testdata/live/chest_loading_1024x768.png"
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
	cl := NewClassifier(cal, DefaultClassifierConfig(), zerolog.Nop())

	state, score := cl.ClassifyState(frame)
	if state != StateTapToContinue {
		t.Fatalf("live chest frame classified as %s (score %d); the capture loop would self-heal/restart instead of dismissing it", state, score)
	}

	dev := newFrameDevice(frame.Clone())
	defer dev.Close()
	if acted := DismissOverlay(dev, cal, cl, StateTapToContinue, nil, zerolog.Nop()); !acted {
		t.Fatal("dismissal refused the live chest frame")
	}
	if dev.wentBack {
		t.Fatal("dismissal pressed Back on the chest screen")
	}
	if len(dev.recorded) != 1 {
		t.Fatalf("want exactly 1 tap, got %d (%v)", len(dev.recorded), dev.recorded)
	}
	if got, want := dev.recorded[0], image.Pt(512, 420); got != want {
		t.Fatalf("tapped %v, want the chest front %v", got, want)
	}
	t.Logf("live chest frame -> %s score=%d, tapped %v", state, score, dev.recorded[0])
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
