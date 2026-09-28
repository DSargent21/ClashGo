package vision

import (
	"image"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFrameStore_AddLoadAndIndex(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenFrameStore(dir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	base := solid(64, 32, 0, 0, 0)

	e1, err := store.Add(base, FrameMeta{State: "MainVillage", Score: 51, Label: "village"})
	if err != nil {
		t.Fatalf("add 1: %v", err)
	}
	if e1.ID != 1 || e1.ChangedPct != 0 {
		t.Fatalf("first entry = %+v, want id 1 and no change against nothing", e1)
	}
	if _, err := os.Stat(filepath.Join(dir, e1.File)); err != nil {
		t.Fatalf("frame file missing: %v", err)
	}

	// An identical frame must report no change: that is the signal a stuck
	// screen (a missed tap, a wedged dialog) leaves behind.
	e2, err := store.Add(solid(64, 32, 0, 0, 0), FrameMeta{State: "MainVillage", Score: 51})
	if err != nil {
		t.Fatalf("add 2: %v", err)
	}
	if e2.ID != 2 || e2.ChangedPct != 0 {
		t.Fatalf("identical frame = %+v, want id 2 and 0%% changed", e2)
	}

	e3, err := store.Add(fill(solid(64, 32, 0, 0, 0), image.Rect(0, 0, 32, 32), 255, 255, 255),
		FrameMeta{State: "FindMatch", Score: 12, Label: "after tap"})
	if err != nil {
		t.Fatalf("add 3: %v", err)
	}
	if e3.ChangedPct <= 0 {
		t.Fatalf("changed frame = %+v, want a positive change percentage", e3)
	}

	all, err := store.Entries()
	if err != nil || len(all) != 3 {
		t.Fatalf("entries = %d (%v), want 3", len(all), err)
	}
	last, err := store.Last()
	if err != nil || last == nil || last.ID != 3 {
		t.Fatalf("last = %+v (%v), want id 3", last, err)
	}
	byID, err := store.ByID(2)
	if err != nil || byID.Label != "" {
		t.Fatalf("ByID(2) = %+v (%v)", byID, err)
	}
	if _, err := store.ByID(99); err == nil {
		t.Fatal("ByID of a missing frame must error")
	}

	f, err := store.Load(*last)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if f.W != 64 || f.H != 32 {
		t.Fatalf("loaded frame = %dx%d, want 64x32", f.W, f.H)
	}
	if r, g, b := f.At(10, 10); r != 255 || g != 255 || b != 255 {
		t.Fatalf("loaded pixel = (%d,%d,%d), want white", r, g, b)
	}
}

func TestFrameStore_RecentAndTimeline(t *testing.T) {
	store, err := OpenFrameStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		if _, err := store.Add(solid(16, 16, byte(i*10), 0, 0), FrameMeta{State: "Battle", Score: i}); err != nil {
			t.Fatal(err)
		}
	}
	recent, err := store.Recent(2)
	if err != nil || len(recent) != 2 || recent[0].ID != 4 {
		t.Fatalf("Recent(2) = %+v (%v), want ids 4,5", recent, err)
	}
	out := Timeline(recent)
	for _, want := range []string{"id", "Battle", "changed"} {
		if !strings.Contains(out, want) {
			t.Fatalf("timeline missing %q:\n%s", want, out)
		}
	}
	if Timeline(nil) == "" {
		t.Fatal("an empty timeline needs a message, not silence")
	}
}

func TestFrameStore_PruneKeepsTheNewest(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenFrameStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 6; i++ {
		if _, err := store.Add(solid(8, 8, 0, 0, 0), FrameMeta{State: "Battle"}); err != nil {
			t.Fatal(err)
		}
	}
	removed, err := store.Prune(2)
	if err != nil || removed != 4 {
		t.Fatalf("prune removed %d (%v), want 4", removed, err)
	}
	left, err := store.Entries()
	if err != nil || len(left) != 2 || left[0].ID != 5 {
		t.Fatalf("after prune = %+v (%v), want ids 5,6", left, err)
	}
	files, _ := filepath.Glob(filepath.Join(dir, "frames", "*.png"))
	if len(files) != 2 {
		t.Fatalf("%d frame files on disk, want 2", len(files))
	}
}

func TestFrameStore_RejectsEmptyFrame(t *testing.T) {
	store, _ := OpenFrameStore(t.TempDir())
	if _, err := store.Add(Frame{}, FrameMeta{}); err == nil {
		t.Fatal("recording an empty frame must fail loudly")
	}
}

func TestFrame_CopyAndCropDropPadding(t *testing.T) {
	// A frame with row padding: Copy must produce a contiguous buffer, which
	// is what PNG writing and diffing assume.
	padded := Frame{W: 4, H: 2, Stride: 16, Pix: make([]byte, 32)}
	for i := range padded.Pix {
		padded.Pix[i] = byte(i)
	}
	c := padded.Copy()
	if c.Stride != 12 || len(c.Pix) != 24 {
		t.Fatalf("copy = stride %d len %d, want 12 and 24", c.Stride, len(c.Pix))
	}
	// Row 1 of the padded frame lives at byte 16, so its first pixel is
	// bytes 16,17,18 = BGR (16,17,18), i.e. RGB (18,17,16).
	if r, g, b := c.At(0, 1); r != 18 || g != 17 || b != 16 {
		t.Fatalf("copied row 1 starts at (%d,%d,%d), want (18,17,16)", r, g, b)
	}
	crop := c.Crop(image.Rect(1, 0, 3, 2))
	if crop.W != 2 || crop.H != 2 || crop.Stride != 6 {
		t.Fatalf("crop = %dx%d stride %d, want 2x2 stride 6", crop.W, crop.H, crop.Stride)
	}
	if crop.Empty() {
		t.Fatal("crop produced an empty frame")
	}
	if !c.Crop(image.Rect(0, 0, 0, 0)).Empty() {
		t.Fatal("an empty crop must be empty")
	}
}

func TestParseOCRRegions(t *testing.T) {
	out := `400,640,180,34 | RETURN HOME
12,8,90,20 | Clash of Clans
bad line without a pipe
7,40,3,4 | spam   text
`
	regions := ParseOCRRegions(out)
	if len(regions) != 3 {
		t.Fatalf("parsed %d regions, want 3:\n%+v", len(regions), regions)
	}
	// Reading order: top-to-bottom.
	if regions[0].Text != "Clash of Clans" || regions[1].Text != "spam text" {
		t.Fatalf("order = %q then %q", regions[0].Text, regions[1].Text)
	}
	last := regions[2]
	if last.Text != "RETURN HOME" || last.Center() != image.Pt(490, 657) {
		t.Fatalf("last region = %+v, want RETURN HOME centred at (490,657)", last)
	}
	if !strings.Contains(last.Describe(), "( 400, 640") {
		t.Fatalf("Describe = %q, want the box in it", last.Describe())
	}
}
