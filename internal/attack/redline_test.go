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

// The boundary is a dashed curve, so its mask fills very little of its own
// bounding box. Live 2026-11-10 at 1024x768 the mask covered 19.6% of the
// playfield while the frame held no red dashes at all, and the candidate was a
// solid block inset from every edge — so the edge/HUD gate alone could not see
// that it was terrain. This pins the fill test that does, and keeps the
// "plausible inset ring" case above green.
func TestRedLineDetectRejectsSolidSheet(t *testing.T) {
	const (
		w, h     = 1024, 768
		uiCutoff = 652
	)
	frame := gocv.NewMatWithSize(h, w, gocv.MatTypeCV8UC3)
	defer frame.Close()
	frame.SetTo(gocv.NewScalar(40, 60, 40, 0))
	// Inset from every edge and below the HUD: the only thing wrong with it is
	// that it is filled, which is exactly what fused terrain and HUD look like.
	gocv.Rectangle(&frame, image.Rect(140, 150, 900, 560), color.RGBA{220, 0, 0, 255}, -1)

	if got := NewRedLineDetector(zerolog.Nop()).Detect(frame, uiCutoff); got.Valid {
		t.Fatalf("a solid red block inset from every edge was accepted as a no-deploy boundary: %+v", got.BBox)
	}
}

// The windows were tightened on 2026-11-10 because they matched 50.5% of a live
// battle playfield (grass, trees, gold HUD, roofs, lava walls). Tightening only
// earns its keep if a real dashed ring still survives it over noisy terrain, so
// this draws one: saturated red dashes with gaps along the perimeter of an inset
// box, over muted grass with orange roof/rock blobs scattered through it.
func TestRedLineDetectAcceptsDashedRingOverTerrain(t *testing.T) {
	const (
		w, h     = 1024, 768
		uiCutoff = 652
	)
	frame := gocv.NewMatWithSize(h, w, gocv.MatTypeCV8UC3)
	defer frame.Close()
	frame.SetTo(gocv.NewScalar(44, 62, 40, 0)) // muted grass in BGR

	orange := color.RGBA{180, 120, 50, 255} // warm roof/rock tone: saturated, but not the bright near-pure orange the windows now demand
	for y := 130; y < 610; y += 41 {
		for x := 40; x < 960; x += 57 {
			gocv.Rectangle(&frame, image.Rect(x, y, x+20, y+15), orange, -1)
		}
	}

	// Height must clear the detector's own size floor (55% of the playfield,
	// 359 rows here) or the ring is refused as "too small", not as red.
	box := image.Rect(150, 150, 880, 540)
	red := color.RGBA{235, 15, 15, 255}
	for x := box.Min.X; x < box.Max.X; x += 10 {
		gocv.Rectangle(&frame, image.Rect(x, box.Min.Y, x+6, box.Min.Y+3), red, -1)
		gocv.Rectangle(&frame, image.Rect(x, box.Max.Y-3, x+6, box.Max.Y), red, -1)
	}
	for y := box.Min.Y; y < box.Max.Y; y += 10 {
		gocv.Rectangle(&frame, image.Rect(box.Min.X, y, box.Min.X+3, y+6), red, -1)
		gocv.Rectangle(&frame, image.Rect(box.Max.X-3, y, box.Max.X, y+6), red, -1)
	}

	got := NewRedLineDetector(zerolog.Nop()).Detect(frame, uiCutoff)
	if !got.Valid {
		for _, c := range NewRedLineDetector(zerolog.Nop()).Candidates(frame, uiCutoff) {
			t.Logf("candidate %v fill %.2f enclosed %.0f: %s", c.Rect, c.Fill, c.Enclosed, c.Reason)
		}
		t.Fatalf("a saturated dashed ring inset from every edge must survive the tightened windows; got %+v", got)
	}
}
