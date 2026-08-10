package bot

import (
	"testing"

	"github.com/Ducky705/ClashGO/internal/game"
	"gocv.io/x/gocv"
)

// TestResultPanelHashDeterministic verifies that the same frame hashes to
// the same value and that a changed pixel inside the panel region changes
// the hash (the property the battle-result settle logic relies on).
func TestResultPanelHashDeterministic(t *testing.T) {
	cal := &game.Calibration{
		PhysicalW: 860,
		PhysicalH: 732,
		ScaleX:    1.0,
		ScaleY:    1.0,
	}

	img := gocv.NewMatWithSize(732, 860, gocv.MatTypeCV8UC3)
	defer img.Close()
	img.SetTo(gocv.NewScalar(30, 30, 30, 0))

	// White block in the star row area (inside the panel ROI).
	for y := 200; y < 210; y++ {
		for x := 430; x < 440; x++ {
			img.SetUCharAt(y, x*3, 255)
			img.SetUCharAt(y, x*3+1, 255)
			img.SetUCharAt(y, x*3+2, 255)
		}
	}

	h1 := resultPanelHash(img, cal)
	if h1 == 0 {
		t.Fatal("hash of populated frame must not be zero")
	}
	if h2 := resultPanelHash(img, cal); h2 != h1 {
		t.Fatalf("same frame hashed differently: %d vs %d", h1, h2)
	}

	// Change one pixel inside the panel -> hash must change.
	img.SetUCharAt(420, 300*3, 255)
	if h3 := resultPanelHash(img, cal); h3 == h1 {
		t.Fatal("changed panel pixel did not change the hash")
	}
}

// TestResultPanelHashOutsideRegion verifies that edits outside the result
// panel ROI do not affect the hash — a moving village background behind the
// overlay must not look like ongoing result animation.
func TestResultPanelHashOutsideRegion(t *testing.T) {
	cal := &game.Calibration{
		PhysicalW: 860,
		PhysicalH: 732,
		ScaleX:    1.0,
		ScaleY:    1.0,
	}

	img := gocv.NewMatWithSize(732, 860, gocv.MatTypeCV8UC3)
	defer img.Close()
	img.SetTo(gocv.NewScalar(10, 10, 10, 0))

	h1 := resultPanelHash(img, cal)

	// Bottom-left corner is far outside the panel ROI (ref 300,180-690,470).
	for y := 600; y < 620; y++ {
		for x := 0; x < 40; x++ {
			img.SetUCharAt(y, x*3, 200)
			img.SetUCharAt(y, x*3+1, 200)
			img.SetUCharAt(y, x*3+2, 200)
		}
	}

	if h2 := resultPanelHash(img, cal); h2 != h1 {
		t.Fatalf("hash changed for edits outside the panel ROI: %d vs %d", h1, h2)
	}
}

// TestResultPanelHashScaled verifies the hash works at non-1.0 scale and
// returns 0 on degenerate (zero-area) panels.
func TestResultPanelHashScaled(t *testing.T) {
	cal := &game.Calibration{
		PhysicalW: 430,
		PhysicalH: 366,
		ScaleX:    0.5,
		ScaleY:    0.5,
	}

	img := gocv.NewMatWithSize(366, 430, gocv.MatTypeCV8UC3)
	defer img.Close()
	img.SetTo(gocv.NewScalar(20, 20, 20, 0))

	if h := resultPanelHash(img, cal); h == 0 {
		t.Fatal("scaled frame hash must not be zero")
	}

	// Degenerate: tiny 1x1 mat -> hash must be 0 (caller treats as unstable).
	tiny := gocv.NewMatWithSize(1, 1, gocv.MatTypeCV8UC3)
	defer tiny.Close()
	if h := resultPanelHash(tiny, cal); h != 0 {
		t.Fatalf("degenerate panel hash = %d, want 0", h)
	}
}
