package game

import (
	"image"
	"os"
	"testing"

	"github.com/rs/zerolog"
	"gocv.io/x/gocv"
)

// newChestClassifier builds a Classifier with identity calibration so
// reference (860x732) pixel coordinates map 1:1 onto the synthetic frame.
func newChestClassifier(t *testing.T) *Classifier {
	t.Helper()
	cal := &Calibration{PhysicalW: RefWidth, PhysicalH: RefHeight, ScaleX: 1, ScaleY: 1}
	return NewClassifier(cal, DefaultClassifierConfig(), zerolog.Nop())
}

// loadHammerTemplate loads the hammer template from assets/templates
// onto the classifier. Must be called before ClassifyState in any test
// that expects the template-only ChestReward rule to fire.
func loadHammerTemplate(t *testing.T, c *Classifier) {
	t.Helper()
	templateDir := "../../assets/templates"
	if _, err := os.Stat(templateDir); os.IsNotExist(err) {
		templateDir = "assets/templates"
	}
	ts, err := NewTemplateStore(templateDir)
	if err != nil {
		t.Fatalf("NewTemplateStore(%s): %v", templateDir, err)
	}
	if err := ts.LoadTemplates(); err != nil {
		t.Fatalf("LoadTemplates: %v", err)
	}
	t.Cleanup(ts.Close)
	c.SetTemplates(ts)
}

// TestClassify_ChestDetectedOnChestFrame asserts that StateChestReward
// fires when the hammer template ("TAP TO OPEN" prompt) is present on
// screen. Detection is template-only to avoid false positives from dark
// bottom pixels in non-chest screens.
func TestClassify_ChestDetectedOnChestFrame(t *testing.T) {
	c := newChestClassifier(t)
	loadHammerTemplate(t, c)

	m := gocv.NewMatWithSize(RefHeight, RefWidth, gocv.MatTypeCV8UC3)
	defer m.Close()
	// Fill with a dark neutral background so the hammer stands out.
	m.SetTo(gocv.NewScalar(30, 30, 30, 0))

	hammer := gocv.IMRead("../../assets/templates/hammer.png", gocv.IMReadColor)
	defer hammer.Close()
	if hammer.Empty() {
		t.Fatal("hammer.png could not be loaded")
	}
	// Paste the hammer into the frame at the location where it appears
	// on a real chest screen (~center of the "TAP TO OPEN" band).
	r := image.Rect(344, 506, 504, 547)
	roi := m.Region(r)
	hammer.CopyTo(&roi)
	roi.Close()

	state, _ := c.ClassifyState(m)
	if state != StateChestReward {
		t.Fatalf("expected StateChestReward, got %s", state)
	}
}

// TestClassify_NoFalseChestOnMainVillage asserts the chest rule does NOT
// fire on a MainVillage-looking frame — even with the hammer template
// loaded, it must not match on a screen that lacks the TAP TO OPEN
// prompt. This guards against the template matching latching onto random
// pixel patterns in normal gameplay.
func TestClassify_NoFalseChestOnMainVillage(t *testing.T) {
	c := newChestClassifier(t)
	loadHammerTemplate(t, c)

	m := gocv.NewMatWithSize(RefHeight, RefWidth, gocv.MatTypeCV8UC3)
	defer m.Close()
	// Bright grass bottom (no dark band).
	m.SetTo(gocv.NewScalar(0x2A, 0x5F, 0x3A, 0))
	// Neutral center (village ground, not a warm chest glow).
	// Some MainVillage HUD pixels so the frame isn't "unknown".
	setRGB(m, 830, 35, 0xB2, 0x90, 0x0F)
	setRGB(m, 830, 95, 0x54, 0x19, 0x59)

	state, _ := c.ClassifyState(m)
	if state == StateChestReward {
		t.Fatalf("chest rule falsely fired on a MainVillage frame (state=%s)", state)
	}
}

// setRGB writes an RGB triple at (x,y) of a 3-channel BGR Mat.
func setRGB(m gocv.Mat, x, y int, r, g, b uint8) {
	m.SetUCharAt(y, x*3, b)
	m.SetUCharAt(y, x*3+1, g)
	m.SetUCharAt(y, x*3+2, r)
}
