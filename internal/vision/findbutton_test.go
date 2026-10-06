package vision

import (
	"image"
	"testing"

	"gocv.io/x/gocv"
)

// paintButton draws a solid button face with a dark label band across its
// middle, mimicking a CoC button whose glyphs punch holes in the colour mask.
// The closing pass in FindActionButton is what has to bridge those holes.
func paintButton(img *gocv.Mat, r image.Rectangle, bgr [3]uint8) {
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			img.SetUCharAt(y, x*3, bgr[0])
			img.SetUCharAt(y, x*3+1, bgr[1])
			img.SetUCharAt(y, x*3+2, bgr[2])
		}
	}
	band := image.Rect(r.Min.X+6, r.Min.Y+r.Dy()/2-2, r.Max.X-6, r.Min.Y+r.Dy()/2+2)
	for y := band.Min.Y; y < band.Max.Y; y++ {
		for x := band.Min.X; x < band.Max.X; x++ {
			img.SetUCharAt(y, x*3, 20)
			img.SetUCharAt(y, x*3+1, 20)
			img.SetUCharAt(y, x*3+2, 20)
		}
	}
}

func newFrame(w, h int) gocv.Mat {
	img := gocv.NewMatWithSize(h, w, gocv.MatTypeCV8UC3)
	// Dark background, like a dimmed village behind a panel.
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetUCharAt(y, x*3, 40)
			img.SetUCharAt(y, x*3+1, 45)
			img.SetUCharAt(y, x*3+2, 35)
		}
	}
	return img
}

// The army sheet case, in both measured geometries: the primary button is the
// widest green blob in the lower-right region, and the Boost-sized buttons
// beside it must not win.
func TestFindActionButton_PicksWidestInRegion(t *testing.T) {
	cases := []struct {
		name     string
		w, h     int
		button   image.Rectangle
		wantCent image.Point
	}{
		{"reference 860x732", 860, 732, image.Rect(656, 522, 800, 551), image.Pt(728, 536)},
		{"720p 1280x720", 1280, 720, image.Rect(1012, 618, 1250, 665), image.Pt(1131, 641)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			img := newFrame(tc.w, tc.h)
			defer img.Close()

			// The primary button, plus two Boost-sized buttons beside it that
			// are green and bright but too narrow to be the action button. The
			// 20 px gap between the Boost faces is the live 720p measurement: a
			// closing kernel wide enough to fuse them would report a button in
			// the space between them.
			paintButton(&img, tc.button, [3]uint8{98, 237, 200}) // BGR of (200,237,98)
			boostY := tc.button.Min.Y - 62
			paintButton(&img, image.Rect(tc.button.Min.X, boostY, tc.button.Min.X+110, boostY+45), [3]uint8{98, 237, 200})
			paintButton(&img, image.Rect(tc.button.Min.X+130, boostY, tc.button.Min.X+240, boostY+45), [3]uint8{98, 237, 200})
			if got := ClosingKernelWidth(img.Cols()); got >= 20 {
				t.Fatalf("closing kernel %d would fuse the Boost buttons 20 px apart", got)
			}

			region := FractionRect(tc.w, tc.h, 0.5, 0.5, 1.0, 1.0)
			got, ok := FindActionButton(img, region, DefaultActionButtonConfig())
			if !ok {
				t.Fatalf("no button found in %v", region)
			}
			if got.Color != ButtonGreen {
				t.Errorf("color = %s, want green", got.Color)
			}
			if got.Centre() != tc.wantCent {
				t.Errorf("centre = %v, want %v (%s)", got.Centre(), tc.wantCent, got.Describe())
			}
			if got.Fill < 0.6 {
				t.Errorf("fill = %.2f, want >= 0.6 for a solid face", got.Fill)
			}
		})
	}
}

// A village frame has no primary button in the searched region: the gold
// Attack! blob is bottom-LEFT and the green things in the bottom bar are
// banners, not button faces. The locator must report nothing rather than hand
// the bot a tap point on scenery.
func TestFindActionButton_RejectsScenery(t *testing.T) {
	img := newFrame(1280, 720)
	defer img.Close()

	// Gold Attack! blob on the village, measured at (23,574)-(146,632).
	paintButton(&img, image.Rect(23, 574, 147, 633), [3]uint8{96, 214, 255})
	// A wide but hollow green banner (low fill): the colour is there, the face
	// is not.
	hollow := image.Rect(900, 600, 1150, 660)
	for y := hollow.Min.Y; y < hollow.Max.Y; y++ {
		for x := hollow.Min.X; x < hollow.Max.X; x++ {
			edge := y < hollow.Min.Y+3 || y >= hollow.Max.Y-3
			if !edge {
				continue
			}
			img.SetUCharAt(y, x*3, 98)
			img.SetUCharAt(y, x*3+1, 237)
			img.SetUCharAt(y, x*3+2, 200)
		}
	}

	region := FractionRect(1280, 720, 0.5, 0.5, 1.0, 1.0)
	if got, ok := FindActionButton(img, region, DefaultActionButtonConfig()); ok {
		t.Errorf("found %s in a scenery-only region; want no match", got.Describe())
	}
}

// A tall narrow blob (a ribbon or a banner hanging down the panel) must not be
// mistaken for a button face however wide it is in absolute pixels.
func TestFindActionButton_RejectsTallBlob(t *testing.T) {
	img := newFrame(1280, 720)
	defer img.Close()
	paintButton(&img, image.Rect(900, 380, 1060, 660), [3]uint8{98, 237, 200}) // 160x280

	region := FractionRect(1280, 720, 0.5, 0.5, 1.0, 1.0)
	if got, ok := FindActionButton(img, region, DefaultActionButtonConfig()); ok {
		t.Errorf("found %s for a 160x280 blob; want no match", got.Describe())
	}
}

// An empty region and an out-of-bounds region are not errors, they are misses:
// the bot asks for a button before every tap and must not panic on a frame it
// does not expect.
func TestFindActionButton_EmptyRegion(t *testing.T) {
	img := newFrame(200, 200)
	defer img.Close()
	for _, region := range []image.Rectangle{
		image.Rect(0, 0, 0, 0),
		image.Rect(500, 500, 900, 900),
		image.Rect(10, 10, 14, 14),
	} {
		if got, ok := FindActionButton(img, region, DefaultActionButtonConfig()); ok {
			t.Errorf("region %v: found %s, want no match", region, got.Describe())
		}
	}
}

// The three palettes are told apart by their dominant channel, which is how the
// live buttons separate: gold (247,177,54), green (200,237,98), blue
// (66,156,218).
func TestFindActionButton_ClassifiesPalette(t *testing.T) {
	cases := []struct {
		name string
		bgr  [3]uint8
		want ButtonColor
	}{
		{"gold", [3]uint8{54, 177, 247}, ButtonGold},   // (247,177,54)
		{"green", [3]uint8{98, 237, 200}, ButtonGreen}, // (200,237,98)
		{"blue", [3]uint8{218, 156, 66}, ButtonBlue},   // (66,156,218)
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			img := newFrame(1280, 720)
			defer img.Close()
			paintButton(&img, image.Rect(700, 560, 1000, 620), tc.bgr)
			got, ok := FindActionButton(img, FractionRect(1280, 720, 0.5, 0.5, 1.0, 1.0), DefaultActionButtonConfig())
			if !ok {
				t.Fatalf("no button found")
			}
			if got.Color != tc.want {
				t.Errorf("color = %s, want %s", got.Color, tc.want)
			}
		})
	}
}

// FacePixels is the "is a painted control drawn here" probe a caller uses when it
// already knows the rect to look in (the wall-upgrade loop's tray gate). The
// distinction it has to make is painted face versus on-screen text, because a
// panel reflow can put a menu row where a control used to be.
func TestFacePixelsSeparatesFacesFromText(t *testing.T) {
	face := image.Rect(200, 300, 300, 380)

	empty := newFrame(1280, 720)
	defer empty.Close()
	if got := FacePixels(empty, face); got != 0 {
		t.Errorf("FacePixels on empty panel background = %d, want 0", got)
	}

	painted := newFrame(1280, 720)
	defer painted.Close()
	paintButton(&painted, face, [3]uint8{54, 177, 247}) // gold face
	got := FacePixels(painted, face)
	if got < face.Dx()*face.Dy()/2 {
		t.Errorf("FacePixels over a gold face = %d, want most of %d px", got, face.Dx()*face.Dy())
	}

	// White text is the thing the mask must reject: bright, zero saturation.
	text := newFrame(1280, 720)
	defer text.Close()
	for y := face.Min.Y; y < face.Max.Y; y++ {
		for x := face.Min.X; x < face.Max.X; x++ {
			if (x*7+y*3)%9 < 2 { // sparse glyph strokes
				for c := 0; c < 3; c++ {
					text.SetUCharAt(y, x*3+c, 255)
				}
			}
		}
	}
	if got := FacePixels(text, face); got != 0 {
		t.Errorf("FacePixels over white label glyphs = %d, want 0 (text is bright but unsaturated)", got)
	}

	// Degenerate and off-frame rects answer 0 rather than touching OpenCV with an
	// illegal ROI.
	if got := FacePixels(painted, image.Rect(0, 0, 0, 0)); got != 0 {
		t.Errorf("FacePixels on a zero rect = %d, want 0", got)
	}
	if got := FacePixels(painted, image.Rect(5000, 5000, 5100, 5100)); got != 0 {
		t.Errorf("FacePixels on an off-frame rect = %d, want 0", got)
	}
}

// ColourPixels is what lets a caller ask "is a face painted inside this box?"
// instead of sampling one pixel, so the count and the share both have to be
// right, and a box the mask does not paint has to come back as zero rather than
// as a fraction of nothing.
func TestColourPixelsMeasuresTheMaskedShare(t *testing.T) {
	img := newFrame(200, 100)
	defer img.Close()

	// Half the frame painted in the band's own colour: BGR(16,69,169), the live
	// village Attack! face measured 2026-09-29.
	face := image.Rect(0, 0, 100, 100)
	paintButton(&img, face, [3]uint8{16, 69, 169})

	lower := gocv.NewScalar(0, 40, 150, 0)
	upper := gocv.NewScalar(160, 230, 255, 0)

	px, frac := ColourPixels(img, face, lower, upper)
	// paintButton stamps a dark label band through the middle of the face; that
	// band is BGR(20,20,20) and is correctly outside the warm band.
	band := (face.Dx() - 12) * 4
	want := face.Dx()*face.Dy() - band
	if px != want {
		t.Errorf("ColourPixels over a painted face = %d px, want %d", px, want)
	}
	if got := float64(want) / float64(face.Dx()*face.Dy()); frac != got {
		t.Errorf("ColourPixels fraction = %v, want %v", frac, got)
	}

	// White label glyphs and the village's greens and blues are outside the band.
	white := newFrame(50, 50)
	defer white.Close()
	paintButton(&white, image.Rect(0, 0, 50, 50), [3]uint8{255, 255, 255})
	if px, frac := ColourPixels(white, image.Rect(0, 0, 50, 50), lower, upper); px != 0 || frac != 0 {
		t.Errorf("ColourPixels over white glyphs = (%d, %v), want (0, 0)", px, frac)
	}

	// Degenerate and off-frame rects answer (0, 0) rather than handing OpenCV an
	// illegal ROI.
	if px, frac := ColourPixels(img, image.Rect(0, 0, 0, 0), lower, upper); px != 0 || frac != 0 {
		t.Errorf("ColourPixels on a zero rect = (%d, %v), want (0, 0)", px, frac)
	}
	if px, frac := ColourPixels(img, image.Rect(5000, 5000, 5100, 5100), lower, upper); px != 0 || frac != 0 {
		t.Errorf("ColourPixels on an off-frame rect = (%d, %v), want (0, 0)", px, frac)
	}
}

func TestFractionRect(t *testing.T) {
	got := FractionRect(1280, 720, 0.5, 0.5, 1.0, 1.0)
	want := image.Rect(640, 360, 1280, 720)
	if got != want {
		t.Errorf("FractionRect = %v, want %v", got, want)
	}
}
