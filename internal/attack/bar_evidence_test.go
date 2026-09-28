package attack

import (
	"image"
	"image/color"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Ducky705/ClashGO/internal/game"
	"github.com/rs/zerolog"
	"gocv.io/x/gocv"
)

// TestUnreadableBarBandDumpIsBoundedAndAnnotated pins the artifact a bad deploy
// is diagnosed from.
//
// "count label present but not readable" is the reader outcome that costs battle
// time: the caller fires a blind top-up batch because it has neither a number to
// fire nor proof the card is spent. The log line is identical whether the badge
// sits outside the searched window (a geometry bug) or inside it with digits the
// templates cannot match (a matching bug), so the only way to tell them apart
// after the fact is the frame. These assertions keep that frame (a) produced,
// (b) the full bar width rather than a guess at one card, (c) actually enlarged
// enough to read an 11px glyph, and (d) bounded, so a long battle cannot fill the
// config dir with bar strips or spend its deploy time writing them.
func TestUnreadableBarBandDumpIsBoundedAndAnnotated(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLASHGO_CONFIG_DIR", dir)

	screen := gocv.NewMatWithSize(720, 1280, gocv.MatTypeCV8UC3)
	defer screen.Close()
	// A bright badge row plus a card row below it: enough structure that the
	// crop, the resize and the annotation all have something to carry.
	gocv.Rectangle(&screen, image.Rect(60, 596, 1240, 614), color.RGBA{R: 240, G: 240, B: 240, A: 255}, -1)
	gocv.Rectangle(&screen, image.Rect(200, 620, 260, 700), color.RGBA{R: 60, G: 90, B: 200, A: 255}, -1)

	cal := game.NewCalibration(1280, 720)
	cal.SetDisplayScale(1.325)
	tc := NewTroopCounter(cal, 860, 732, zerolog.Nop())

	// More calls than the budget, exactly as a long reconcile would produce.
	for i := 0; i < unreadableDumpBudget+3; i++ {
		tc.dumpUnreadableBarBand(screen, 212, 682, 621)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read config dir: %v", err)
	}
	var pngs []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "debug_bar_unreadable_") {
			pngs = append(pngs, e.Name())
		}
	}
	if len(pngs) != unreadableDumpBudget {
		t.Fatalf("wrote %d bar bands, want the budget %d (got %v)", len(pngs), unreadableDumpBudget, pngs)
	}
	if tc.unreadableDumps != unreadableDumpBudget {
		t.Fatalf("counter = %d after the budget was reached, want %d", tc.unreadableDumps, unreadableDumpBudget)
	}

	for _, name := range pngs {
		img := gocv.IMRead(filepath.Join(dir, name), gocv.IMReadColor)
		if img.Empty() {
			t.Fatalf("%s is not a readable PNG", name)
		}
		if img.Cols() != 1280*2 {
			img.Close()
			t.Fatalf("%s is %dx%d; want the full bar width at 2x (2560) so a badge is legible",
				name, img.Cols(), img.Rows())
		}
		// The annotation must be visible: the yellow slot anchor runs the full
		// height at 2 * slotX, so a band saved without it is a bug in the dump
		// rather than in the read.
		anchor := img.GetVecbAt(img.Rows()/2, 212*2)
		img.Close()
		if anchor[2] < 200 || anchor[1] < 200 {
			t.Fatalf("%s carries no yellow slot anchor at x=%d (got BGR %v)", name, 212*2, anchor)
		}
	}
}
