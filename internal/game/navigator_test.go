package game

import (
	"image"
	"os"
	"path/filepath"
	"testing"

	"gocv.io/x/gocv"
)

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
	return gocv.NewMatWithSize(RefHeight, RefWidth, gocv.MatTypeCV8UC3), nil
}

func (m *MockDevice) Close() error {
	return nil
}

func TestNavigateToBattle_Template(t *testing.T) {
	client := &MockDevice{}
	cal := &Calibration{
		PhysicalW: RefWidth,
		PhysicalH: RefHeight,
		ScaleX:    1.0,
		ScaleY:    1.0,
	}
	
	tmpDir, _ := os.MkdirTemp("", "navigator-templates")
	defer os.RemoveAll(tmpDir)

	// Create dummy battle button template
	btnImg := gocv.NewMatWithSize(10, 10, gocv.MatTypeCV8UC3)
	defer btnImg.Close()
	gocv.IMWrite(filepath.Join(tmpDir, "battle_button.png"), btnImg)

	ts, _ := NewTemplateStore(tmpDir)
	ts.LoadTemplates()

	navigator := NewNavigator(client, cal, nil, func(gocv.Mat) (GameState, int) {
		return StateMainVillage, 100
	})
	navigator.SetTemplates(ts)

	ctx := &GameContext{State: StateMainVillage}

	ok := navigator.NavigateToBattle(ctx)
	if !ok {
		t.Fatal("NavigateToBattle failed")
	}

	if len(client.Taps) != 1 {
		t.Errorf("expected 1 tap, got %d", len(client.Taps))
	}
}

func TestNavigateToFindMatch_Template(t *testing.T) {
	client := &MockDevice{}
	cal := &Calibration{
		PhysicalW: RefWidth,
		PhysicalH: RefHeight,
		ScaleX:    1.0,
		ScaleY:    1.0,
	}
	
	tmpDir, _ := os.MkdirTemp("", "findmatch-templates")
	defer os.RemoveAll(tmpDir)

	// Create dummy find_match button template
	btnImg := gocv.NewMatWithSize(10, 10, gocv.MatTypeCV8UC3)
	defer btnImg.Close()
	gocv.IMWrite(filepath.Join(tmpDir, "btn_find_match.png"), btnImg)

	ts, _ := NewTemplateStore(tmpDir)
	ts.LoadTemplates()

	navigator := NewNavigator(client, cal, nil, func(gocv.Mat) (GameState, int) {
		return StateMainVillage, 100
	})
	navigator.SetTemplates(ts)

	ctx := &GameContext{State: StateMainVillage}

	// 1. Click Battle from Main Village
	ok := navigator.NavigateToFindMatch(ctx)
	if !ok {
		t.Fatal("NavigateToFindMatch step 1 failed")
	}
	if len(client.Taps) != 1 {
		t.Errorf("expected 1 tap for Battle, got %d", len(client.Taps))
	}

	// 2. Click Find a Match from menu (simulated state)
	ctx.State = StateUnknown // Simulate being in the menu
	ok = navigator.NavigateToFindMatch(ctx)
	if !ok {
		t.Fatal("NavigateToFindMatch step 2 failed")
	}
	if len(client.Taps) != 2 {
		t.Errorf("expected 2 total taps, got %d", len(client.Taps))
	}
}
