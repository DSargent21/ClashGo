package training

import (
	"image"
	"os"
	"path/filepath"
	"testing"

	"gocv.io/x/gocv"
	"github.com/diegosargent/coc-bot/internal/game"
)

// MockDevice simplifies testing by avoiding real ADB calls
type MockDevice struct {
	Taps []image.Point
}

func (m *MockDevice) Tap(x, y int) error {
	m.Taps = append(m.Taps, image.Pt(x, y))
	return nil
}

func (m *MockDevice) TapRandomized(x, y int) error {
	m.Taps = append(m.Taps, image.Pt(x, y))
	return nil
}

func (m *MockDevice) Swipe(x1, y1, x2, y2 int, ms int) error {
	return nil
}

func (m *MockDevice) Hold(x, y int, ms int) error {
	return nil
}

func (m *MockDevice) Back() error {
	return nil
}

func (m *MockDevice) CaptureToMat() (gocv.Mat, error) {
	// Return a blank 100x100 image
	return gocv.NewMatWithSize(100, 100, gocv.MatTypeCV8UC3), nil
}

func (m *MockDevice) Close() error {
	return nil
}

func TestSelectArmy1_Logic(t *testing.T) {
	// 1. Setup Mock Data
	client := &MockDevice{}
	
	// Create a temporary directory for templates
	tmpDir, err := os.MkdirTemp("", "coc-bot-templates")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	// Create dummy templates
	arrowImg := gocv.NewMatWithSize(10, 10, gocv.MatTypeCV8UC3)
	defer arrowImg.Close()
	army1Img := gocv.NewMatWithSize(10, 10, gocv.MatTypeCV8UC3)
	defer army1Img.Close()

	// Write them to disk
	gocv.IMWrite(filepath.Join(tmpDir, "btn_army_arrow.png"), arrowImg)
	gocv.IMWrite(filepath.Join(tmpDir, "btn_army_1.png"), army1Img)

	ts, err := game.NewTemplateStore(tmpDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := ts.LoadTemplates(); err != nil {
		t.Fatal(err)
	}
	
	trainer := &Trainer{
		client:    client,
		templates: ts,
		cal: &game.Calibration{
			PhysicalW: 100,
			PhysicalH: 100,
			ScaleX:    1.0,
			ScaleY:    1.0,
		},
	}

	// 2. Run the logic
	screen := gocv.NewMatWithSize(100, 100, gocv.MatTypeCV8UC3)
	defer screen.Close()

	err = trainer.SelectArmy1(screen)
	if err != nil {
		t.Fatalf("SelectArmy1 failed: %v", err)
	}

	// 3. Verify
	if len(client.Taps) != 2 {
		t.Errorf("expected 2 taps, got %d", len(client.Taps))
	}
}
