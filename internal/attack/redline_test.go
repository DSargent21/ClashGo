package attack

import (
	"image"
	"image/color"
	"testing"

	"github.com/rs/zerolog"
	"gocv.io/x/gocv"
)

// The detector's mask is not specific to the deploy boundary: on live 720p
// battle frames it also catches the gold/elixir top HUD and the warm terrain,
// roughly 9% of the frame, and the 35x3 / 3x35 / 9x9 closes then fuse that noise
// into one contour spanning the whole playfield. Measured live, the "red zone"
// came back as (63,0)-(1217,612) and (0,0)-(1156,612) — the base, not a
// boundary — and everything downstream believed nearly the whole screen was
// undeployable.
//
// These two cases pin the gate that separates the two: a thin closed curve inset
// from every playfield edge is a boundary and must still be accepted, and a
// sheet that reaches the resource HUD rows and the far edge must not be.
func TestRedLineDetectGate(t *testing.T) {
	const (
		w, h     = 1280, 720
		uiCutoff = 612
	)
	background := gocv.NewScalar(40, 60, 40, 0) // BGR: a muted green the windows ignore
	red := color.RGBA{220, 0, 0, 255}           // a saturated red, as gocv.Rectangle wants

	t.Run("thin inset ring is accepted", func(t *testing.T) {
		frame := gocv.NewMatWithSize(h, w, gocv.MatTypeCV8UC3)
		defer frame.Close()
		frame.SetTo(background)
		// Inset from all four playfield edges, and below the resource HUD.
		gocv.Rectangle(&frame, image.Rect(180, 190, 1100, 540), red, 3)

		got := NewRedLineDetector(zerolog.Nop()).Detect(frame, uiCutoff)
		if !got.Valid {
			t.Fatalf("a thin closed red boundary inset from every edge must be accepted; got %+v", got)
		}
		if got.BBox.Min.Y < resourceHudRows {
			t.Errorf("accepted a boundary starting in the resource HUD: %+v", got.BBox)
		}
	})

	t.Run("playfield-spanning sheet is rejected", func(t *testing.T) {
		frame := gocv.NewMatWithSize(h, w, gocv.MatTypeCV8UC3)
		defer frame.Close()
		frame.SetTo(background)
		// Dashes every 12px, close enough for the 35x3 / 3x35 kernels to bridge
		// into a solid sheet — the observed live failure, reproduced.
		for y := 0; y < uiCutoff; y += 12 {
			for x := 0; x < w; x += 12 {
				gocv.Rectangle(&frame, image.Rect(x, y, x+4, y+4), red, -1)
			}
		}

		got := NewRedLineDetector(zerolog.Nop()).Detect(frame, uiCutoff)
		if got.Valid {
			t.Fatalf("a sheet covering the playfield was reported as a no-deploy boundary: %+v", got)
		}
	})

	t.Run("gate rejects the shapes that hid the bug", func(t *testing.T) {
		cases := []struct {
			name   string
			rect   image.Rectangle
			wantOK bool
		}{
			{"live bbox from last_troop_bar.png", image.Rect(63, 0, 1217, 612), false},
			{"live bbox from last_failure.png", image.Rect(0, 0, 1156, 612), false},
			{"sheet with the HUD rows cleared", image.Rect(0, 130, 1280, 612), false},
			{"sheet inset on the left but reaching the bottom", image.Rect(40, 130, 1240, 612), false},
			{"sheet inset on the left but touching the top HUD", image.Rect(40, 0, 1240, 560), false},
			{"plausible inset boundary", image.Rect(180, 190, 1100, 540), true},
		}
		for _, c := range cases {
			ok, why := boundaryPlausible(c.rect, w, uiCutoff)
			if ok != c.wantOK {
				t.Errorf("%s: boundaryPlausible=%v (%s), want %v", c.name, ok, why, c.wantOK)
			}
			if !c.wantOK && why == "" {
				t.Errorf("%s: rejected with no reason given", c.name)
			}
		}
	})
}
