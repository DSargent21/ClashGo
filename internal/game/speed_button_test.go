package game

import (
	"image"
	"testing"

	"gocv.io/x/gocv"
)

func loadSpeedFrame(t *testing.T, path string) gocv.Mat {
	t.Helper()
	img := gocv.IMRead(path, gocv.IMReadColor)
	if img.Empty() {
		t.Fatalf("cannot read %s", path)
	}
	t.Cleanup(func() { _ = img.Close() })
	return img
}

func speedTemplates(t *testing.T) *TemplateStore {
	t.Helper()
	ts, err := NewTemplateStore("../../assets/templates")
	if err != nil {
		t.Fatalf("NewTemplateStore: %v", err)
	}
	if err := ts.LoadTemplates(); err != nil {
		t.Fatalf("LoadTemplates: %v", err)
	}
	t.Cleanup(ts.Close)
	return ts
}

// Live 1024x768 battle frame captured 2026-11-19 with the pill reading "1x".
// The expected centre is the white glyph cluster's centre measured on that
// frame (978,478); a board that moves the pill must move this expectation.
func TestSpeedButtonPoint_Detects1xPill(t *testing.T) {
	ts := speedTemplates(t)
	screen := loadSpeedFrame(t, "testdata/live/battle_speed_1x_1024x768.png")

	pt, ok := SpeedButtonPoint(screen, ts)
	if !ok {
		t.Fatal("1x speed pill not detected on the live frame")
	}
	if d := pt.Sub(image.Pt(978, 478)); d.X < -8 || d.X > 8 || d.Y < -8 || d.Y > 8 {
		t.Fatalf("pill centre = %v, want (978,478) ±8", pt)
	}
}

// Same battle after one tap: the pill reads "4x". Detecting it here would tap
// it again and cycle the battle back down, so this frame must be refused.
func TestSpeedButtonPoint_Rejects4xPill(t *testing.T) {
	ts := speedTemplates(t)
	screen := loadSpeedFrame(t, "testdata/live/battle_speed_4x_1024x768.png")

	if pt, ok := SpeedButtonPoint(screen, ts); ok {
		t.Fatalf("4x pill must not be treated as 1x; got tap point %v", pt)
	}
}

// A village frame has no battle HUD at all; the detector must not invent one.
func TestSpeedButtonPoint_NoPillInVillage(t *testing.T) {
	ts := speedTemplates(t)
	screen := loadSpeedFrame(t, "testdata/live/village_hud_after_attack_1024x768.png")

	if pt, ok := SpeedButtonPoint(screen, ts); ok {
		t.Fatalf("village frame reported a speed pill at %v", pt)
	}
}
