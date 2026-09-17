package game

import (
	"testing"

	"github.com/rs/zerolog"
	"gocv.io/x/gocv"
)

// The obstacle-dialog rule was re-authored from real frames of the same dialog
// at two geometries. The method matters as much as the values: the dialog was
// opened at 1280x720 and the device was resized *with it still on screen*, which
// yields the identical sheet at 860x732 — no inference about how overlays scale,
// just two captures of one state.
//
// The old anchors (a light-gray body pixel, a near-white pixel at ref (272,11),
// and a green button face, all under the centre anchor) meant the rule never
// fired at 1280x720. The modal then stayed open and swallowed every tap of the
// attack sequence, which is why a 720p run could not get past the village.

// Measured on the same dialog at both geometries.
var (
	obstacleTitleGlow = [3]uint8{255, 255, 183} // ref (435,543); 720p (647,470), identical
	obstacleSheetBody = [3]uint8{244, 251, 218} // ref (416,578); 720p (238,237,219)
	obstacleSheetCyan = [3]uint8{24, 206, 255}  // ref (452,574); 720p (26,198,255)
	// The same three points on a clean village frame, at the same geometries.
	obstacleVillageTitle = [3]uint8{75, 65, 65}  // ref
	obstacleVillageBody  = [3]uint8{93, 143, 44} // ref
	obstacleVillageCyan  = [3]uint8{90, 57, 13}  // ref
)

func newModalClassifier(t *testing.T) *Classifier {
	t.Helper()
	cal := &Calibration{PhysicalW: RefWidth, PhysicalH: RefHeight, ScaleX: 1, ScaleY: 1}
	return NewClassifier(cal, DefaultClassifierConfig(), zerolog.Nop())
}

// TestClassify_ObstacleDialogAsBottomSheet pins the geometry law the rule now
// declares: horizontally centred, vertically measured from the bottom edge. The
// numbers are the live measurement (title band ref (435,543) -> 1280x720
// (647,470)); the centre model would put that point at y 585 and the per-axis
// edge model at y 465/605, so any future change to the anchor shows up here.
func TestClassify_ObstacleDialogAsBottomSheet(t *testing.T) {
	cal := NewCalibration(1280, 720)
	cal.SetDisplayScale(1.325)

	if x, y := cal.AnchorPoint(435, 543, AnchorCenterBottom); x != 647 || y != 470 {
		t.Fatalf("AnchorCenterBottom(435,543) = (%d,%d), want (647,470) — the measured sheet position", x, y)
	}
	// The centre anchor, which the rule used before this fix, puts the same
	// probe 125 px below the sheet — far enough that the rule never fired.
	_, cy := cal.AnchorPoint(435, 543, AnchorCenter)
	if cy != 595 {
		t.Fatalf("AnchorCenter(435,543) y = %d, want 595 (the miss this fix removes)", cy)
	}
	if cy == 470 {
		t.Fatal("AnchorCenterBottom must not coincide with AnchorCenter for this probe")
	}
}

// The rule must fire on the reference geometry...
func TestClassify_ObstacleDialogAtReferenceGeometry(t *testing.T) {
	c := newModalClassifier(t)
	m := newFrame()
	defer m.Close()

	paint(m, 435, 543, obstacleTitleGlow)
	paint(m, 416, 578, obstacleSheetBody)
	paint(m, 452, 574, obstacleSheetCyan)

	if state, score := c.ClassifyState(m); state != StateObstacleDialog {
		t.Fatalf("want StateObstacleDialog at the reference geometry, got %s (score %d)", state, score)
	}
}

// ...and at 1280x720, which is the case that was broken: this is the whole
// point of the per-geometry re-authoring.
func TestClassify_ObstacleDialogAt720p(t *testing.T) {
	cal := NewCalibration(1280, 720)
	cal.SetDisplayScale(1.325)
	c := NewClassifier(cal, DefaultClassifierConfig(), zerolog.Nop())

	m := gocv.NewMatWithSize(720, 1280, gocv.MatTypeCV8UC3)
	defer m.Close()
	m.SetTo(gocv.NewScalar(18, 22, 16, 0))

	// Paint at the *mapped* positions, the way the live sheet renders.
	for _, probe := range []struct {
		x, y int
		c    [3]uint8
	}{
		{435, 543, obstacleTitleGlow},
		{416, 578, obstacleSheetBody},
		{452, 574, obstacleSheetCyan},
	} {
		lx, ly := cal.AnchorPoint(probe.x, probe.y, AnchorCenterBottom)
		if lx != 647 && lx != 621 && lx != 669 {
			t.Fatalf("unexpected mapped x %d for ref x %d", lx, probe.x)
		}
		paint(m, lx, ly, probe.c)
	}

	if state, score := c.ClassifyState(m); state != StateObstacleDialog {
		t.Fatalf("want StateObstacleDialog at 1280x720, got %s (score %d)", state, score)
	}
}

// A clean village at the same mapped points must not read as the modal: the
// probes were chosen to be >250 colour units away from the village at both
// geometries.
func TestClassify_ObstacleDialogRejectsCleanVillage(t *testing.T) {
	c := newModalClassifier(t)
	m := newFrame()
	defer m.Close()

	paint(m, 435, 543, obstacleVillageTitle)
	paint(m, 416, 578, obstacleVillageBody)
	paint(m, 452, 574, obstacleVillageCyan)

	state, score := c.ClassifyState(m)
	if state == StateObstacleDialog {
		t.Fatalf("clean village classified as the obstacle modal (score %d)", score)
	}
}
