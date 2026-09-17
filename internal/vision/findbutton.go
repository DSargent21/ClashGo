package vision

import (
	"fmt"
	"image"

	"gocv.io/x/gocv"
)

// ButtonColor names the palette family of a located action button. Clash of
// Clans paints primary buttons in three families, all measured on live frames:
// warm gold (247,177,54) for village chrome and the menu's Find a Match, green
// (200,237,98) for commit actions (the army sheet's Attack!, TRY AGAIN, the
// reward dialog's Okay), and blue (66,156,218) for the secondary panel actions
// such as Join Tournament.
type ButtonColor uint8

const (
	// ButtonGold is the warm gold button face.
	ButtonGold ButtonColor = iota
	// ButtonGreen is the saturated green button face.
	ButtonGreen
	// ButtonBlue is the blue secondary button face.
	ButtonBlue
)

func (c ButtonColor) String() string {
	switch c {
	case ButtonGreen:
		return "green"
	case ButtonBlue:
		return "blue"
	default:
		return "gold"
	}
}

// ActionButton is a primary button located in a live frame.
type ActionButton struct {
	// Rect is the button face's bounding box in frame coordinates.
	Rect image.Rectangle
	// Color is which palette family the face matched.
	Color ButtonColor
	// Fill is the fraction of the bounding box covered by the button colour,
	// after bridging the label glyphs. A solid button face sits high; a
	// decorated or hollow overlay (a badge, a banner, an army card) is low.
	Fill float64
	// Pixels is the masked pixel count inside Rect.
	Pixels int
}

// Centre is the tap point for the button: the centre of its face.
func (b ActionButton) Centre() image.Point {
	return image.Pt(b.Rect.Min.X+b.Rect.Dx()/2, b.Rect.Min.Y+b.Rect.Dy()/2)
}

// Describe renders a located button for logs.
func (b ActionButton) Describe() string {
	return fmt.Sprintf("%s %dx%d at (%d,%d) fill=%.2f",
		b.Color, b.Rect.Dx(), b.Rect.Dy(), b.Centre().X, b.Centre().Y, b.Fill)
}

// ActionButtonConfig bounds what counts as a primary action button. The
// defaults are measured on live frames from both geometries: the army sheet's
// Attack! face is 238x47 at 1280x720 and 144x27 at the 860x732 reference
// (aspect ~5), the gold Attack! face on the village is 124x59 (aspect 2.1), and
// the reward dialog's Okay is 238x96 (aspect 2.5). Shape alone cannot separate
// those, so the region the caller searches is what keeps village chrome out;
// these bounds only reject what is clearly not a button face.
type ActionButtonConfig struct {
	// MinFrameWidthFrac rejects components narrower than this fraction of the
	// whole frame's width. A primary button is a substantial object on screen;
	// icons, badges, Boost chips, unit cards and the village's own SHOP button
	// are all under it. Bounds are frame-relative, not region-relative, so the
	// verdict does not change with the window the caller searched through — the
	// window only decides *where* to look.
	MinFrameWidthFrac float64
	// MinFill rejects hollow or heavily decorated components. A button face is
	// solid; chained scenery is not.
	MinFill float64
	// MinAspect and MaxAspect bound width/height. Button faces are wider than
	// tall; the bounds keep ribbons, banners and tall blobs out.
	MinAspect float64
	MaxAspect float64
	// MaxFrameWidthFrac and MaxFrameHeightFrac are upper bounds measured
	// against the whole frame, not the searched region, because a button face
	// is a small object on screen whatever window it was found through. They
	// are what rejects scenery: the closing pass chains bright saturated grass,
	// walls and storage roofs into one sprawling component, and without a
	// ceiling that component passes every lower bound there is (measured on a
	// live 1280x720 village: 1261x720, fill 0.59).
	MaxFrameWidthFrac  float64
	MaxFrameHeightFrac float64
}

// DefaultActionButtonConfig returns the bounds measured to hold on both the
// reference geometry and 1280x720.
func DefaultActionButtonConfig() ActionButtonConfig {
	return ActionButtonConfig{
		MinFrameWidthFrac:  0.12,
		MaxFrameWidthFrac:  0.45,
		MaxFrameHeightFrac: 0.18,
		MinFill:            0.60,
		MinAspect:          1.6,
		MaxAspect:          12.0,
	}
}

// FindActionButton locates the primary action button inside region: the widest
// bright saturated gold-or-green blob whose bounding box lies fully inside the
// region.
//
// This exists because overlay panels do NOT follow a geometry law. Clash of
// Clans scales and anchors the village HUD predictably (internal/game's
// calibration models that, and it holds), but it *reflows* panels and dialogs by
// content and by aspect ratio. The army sheet is the case that broke the bot
// live: its Attack! button is the widest green blob at (727,536) on the 860x732
// reference and at (1130,641) on 1280x720 — 180 px away in y, and no per-axis
// anchor reproduces both, because the panel around it is laid out differently at
// 16:9. Locating the button in the frame instead of predicting its coordinates
// works at both, from one rule.
//
// The caller passes the window it expects the button in: a fraction of the frame
// for a panel whose place is stable (the sheet's action button is always
// bottom-right), or the box of a dialog it has already detected (see
// FindPanel). Widest-wins is what picks the primary button: on the sheet that is
// the 238 px Attack! face, not the 110 px Boost chips beside it.
func FindActionButton(img gocv.Mat, region image.Rectangle, cfg ActionButtonConfig) (ActionButton, bool) {
	region = region.Intersect(image.Rect(0, 0, img.Cols(), img.Rows()))
	if region.Empty() || region.Dx() < 8 || region.Dy() < 8 {
		return ActionButton{}, false
	}

	sub := img.Region(region)
	defer sub.Close()

	// Bounds against the frame: a button face is a small, solid object on
	// screen whatever window it was found through.
	minW := cfg.MinFrameWidthFrac * float64(img.Cols())
	maxW := cfg.MaxFrameWidthFrac * float64(img.Cols())
	maxH := cfg.MaxFrameHeightFrac * float64(img.Rows())

	mask := buttonMask(sub)
	defer mask.Close()

	// Bridge the label glyphs and the bevel trim, which punch holes in the
	// face, so the button reads as one component instead of as letters. The
	// kernel is sized off the region, not a fixed pixel count, so it behaves
	// the same at every geometry — see ClosingKernelWidth for why it sits
	// between the intra-label and inter-button gap scales.
	k := closingKernelWidth(region.Dx())
	kernel := gocv.GetStructuringElement(gocv.MorphRect, image.Pt(k, k))
	gocv.MorphologyEx(mask, &mask, gocv.MorphClose, kernel)
	kernel.Close()

	labels := gocv.NewMat()
	stats := gocv.NewMat()
	centroids := gocv.NewMat()
	defer labels.Close()
	defer stats.Close()
	defer centroids.Close()
	n := gocv.ConnectedComponentsWithStats(mask, &labels, &stats, &centroids)

	var best ActionButton
	found := false
	for i := 1; i < n; i++ { // label 0 is the background
		// stats is CV_32S: GetIntAt reads the raw int32 fields (GetFloatAt
		// would reinterpret the bits).
		x := int(stats.GetIntAt(i, int(gocv.CC_STAT_LEFT)))
		y := int(stats.GetIntAt(i, int(gocv.CC_STAT_TOP)))
		w := int(stats.GetIntAt(i, int(gocv.CC_STAT_WIDTH)))
		h := int(stats.GetIntAt(i, int(gocv.CC_STAT_HEIGHT)))
		area := int(stats.GetIntAt(i, int(gocv.CC_STAT_AREA)))
		if w <= 0 || h <= 0 {
			continue
		}
		aspect := float64(w) / float64(h)
		fill := float64(area) / float64(w*h)
		if float64(w) < minW ||
			fill < cfg.MinFill ||
			aspect < cfg.MinAspect || aspect > cfg.MaxAspect ||
			(cfg.MaxFrameWidthFrac > 0 && float64(w) > maxW) ||
			(cfg.MaxFrameHeightFrac > 0 && float64(h) > maxH) {
			continue
		}
		rect := image.Rect(region.Min.X+x, region.Min.Y+y, region.Min.X+x+w, region.Min.Y+y+h)
		cand := ActionButton{
			Rect:   rect,
			Color:  classifyButtonColor(img, mask, rect, region),
			Fill:   fill,
			Pixels: area,
		}
		if !found || cand.Rect.Dx() > best.Rect.Dx() ||
			(cand.Rect.Dx() == best.Rect.Dx() && cand.Pixels > best.Pixels) {
			best = cand
			found = true
		}
	}

	return best, found
}

// buttonMask thresholds a BGR region down to "bright and strongly saturated",
// which is what a button face is and what its label glyphs, its bevel trim, its
// drop shadow and the artwork behind it are not.
//
// One mask rather than one per palette: hue does not separate the families
// cleanly (the gold and green faces share a hue band once the bevels are
// included), but a face's *dominant* channel does, and that is decided per blob
// afterwards by classifyButtonColor. The two floors are measured: the green face
// (200,237,98) is brightness 237 / saturation 139 and the gold face is 255/159,
// while glyph black is 15/0, white trim 255/0 and the dimmed village behind a
// dialog stays under 170.
func buttonMask(sub gocv.Mat) gocv.Mat {
	planes := gocv.Split(sub)
	defer func() {
		for i := range planes {
			planes[i].Close()
		}
	}()
	if len(planes) < 3 {
		return gocv.NewMat()
	}
	b, g, r := planes[0], planes[1], planes[2]

	mx := gocv.NewMat()
	mn := gocv.NewMat()
	gocv.Max(b, g, &mx)
	gocv.Max(mx, r, &mx)
	gocv.Min(b, g, &mn)
	gocv.Min(mn, r, &mn)
	defer mx.Close()
	defer mn.Close()

	sat := gocv.NewMat()
	gocv.Subtract(mx, mn, &sat)
	defer sat.Close()

	bright := gocv.NewMat()
	gocv.Threshold(mx, &bright, brightnessFloor, 255, gocv.ThresholdBinary)
	defer bright.Close()

	saturated := gocv.NewMat()
	gocv.Threshold(sat, &saturated, saturationFloor, 255, gocv.ThresholdBinary)
	defer saturated.Close()

	mask := gocv.NewMat()
	gocv.BitwiseAnd(bright, saturated, &mask)
	return mask
}

// classifyButtonColor decides which palette a located face belongs to from the
// pixels of the face itself: the dominant channel decides, which is how gold
// (red-dominant), green and blue faces separate. Only pixels the mask kept are
// sampled, so the label glyphs, the bevel trim and the artwork showing through a
// rounded corner cannot pull the verdict — on the attack menu that is what the
// gold Find a Match face and the blue Join Tournament face beside it look like.
//
// rect is in frame coordinates and region is the window that was searched, so
// mask (region-sized) and img (frame-sized) can both be sampled at the same
// pixel.
func classifyButtonColor(img, mask gocv.Mat, rect, region image.Rectangle) ButtonColor {
	local := rect.Sub(region.Min).Intersect(image.Rect(0, 0, mask.Cols(), mask.Rows()))
	if local.Empty() {
		return ButtonGold
	}
	frame := image.Rect(0, 0, img.Cols(), img.Rows())
	stepY := max(1, local.Dy()/16)
	stepX := max(1, local.Dx()/32)
	var sumR, sumG, sumB, n int
	for y := local.Min.Y; y < local.Max.Y; y += stepY {
		for x := local.Min.X; x < local.Max.X; x += stepX {
			if mask.GetUCharAt(y, x) == 0 {
				continue
			}
			fx, fy := region.Min.X+x, region.Min.Y+y
			if !image.Pt(fx, fy).In(frame) {
				continue
			}
			sumB += int(img.GetUCharAt(fy, fx*3))
			sumG += int(img.GetUCharAt(fy, fx*3+1))
			sumR += int(img.GetUCharAt(fy, fx*3+2))
			n++
		}
	}
	if n == 0 {
		return ButtonGold
	}
	switch {
	case sumR >= sumG && sumR >= sumB:
		return ButtonGold
	case sumG >= sumB:
		return ButtonGreen
	default:
		return ButtonBlue
	}
}

// Measured floors for buttonMask.
const (
	brightnessFloor = 170
	saturationFloor = 60
)

// ClosingKernelWidth returns the closing kernel used to bridge a button label's
// glyph gaps. It is exported for tests and diagnostics that need to reason about
// the same scale.
//
// The width is a hundredth of the searched region, which is the window that
// separates the two scales involved. Measured on live frames: the gap between
// glyphs inside a label is ~0.5% of the frame width, while neighbouring buttons
// (the army sheet's two Boost chips) sit ~1.5% apart. A kernel in between — 1% —
// bridges the letters of a label without fusing two buttons into one blob, which
// would hand the bot a tap point in the gap between them.
func ClosingKernelWidth(regionWidth int) int { return closingKernelWidth(regionWidth) }

func closingKernelWidth(regionWidth int) int {
	w := regionWidth / 100
	if w < 5 {
		w = 5
	}
	if w > 25 {
		w = 25
	}
	if w%2 == 0 {
		w++
	}
	return w
}

// FractionRect returns the sub-rectangle of a w-by-h frame covering
// [fx0,fx1) x [fy0,fy1) in fractional coordinates. Panel buttons are located by
// fraction rather than by a reference-frame ROI precisely because the panels
// reflow; a fraction of the frame is the one description that survives that.
func FractionRect(w, h int, fx0, fy0, fx1, fy1 float64) image.Rectangle {
	return image.Rect(
		int(fx0*float64(w)),
		int(fy0*float64(h)),
		int(fx1*float64(w)),
		int(fy1*float64(h)),
	)
}
