package attack

import (
	"fmt"
	"image"
	"sort"
	"sync"

	"github.com/Ducky705/ClashGO/internal/vision"
	"github.com/rs/zerolog"
	"gocv.io/x/gocv"
)

// RedZone represents detected deployment boundary.
type RedZone struct {
	BBox     image.Rectangle
	Valid    bool
	Contours int

	// Polygon is the outline of the red dashed line itself, as a closed loop
	// in live-frame coordinates. The BBox is a bounding box and says nothing
	// about the shape: the deployable strip in a corner lies well inside the
	// bbox while being perfectly legal, so only the outline can answer "is this
	// tap inside the no-deploy area". That question is not academic — a live
	// 720p run with a user-pinned formula line along the bottom-left corner got
	// the game's own "You cannot deploy troops on the Red area!" banner on
	// screen for the whole deploy, because the pinned line bypasses the
	// red-zone-aware dynamic line entirely.
	Polygon []image.Point
}

// Inside reports whether a live-frame point falls inside the red line, i.e. in
// the area the game refuses to deploy into. A zone with no usable outline
// answers false: "unknown" must never silently move a tap the user pinned.
func (z RedZone) Inside(x, y int) bool {
	return pointInPolygon(x, y, z.Polygon)
}

// boundaryPlausible reports whether a candidate bounding box could actually be
// the game's no-deploy boundary, and when it could not, why.
//
// This gate exists because the detector's mask is not specific to the boundary.
// On live 1280x720 battle frames the HSV windows latch onto the gold/elixir top
// HUD and the warm-toned terrain — measured at 9% of the frame — and the 35x3 /
// 3x35 / 9x9 closes then fuse that noise into ONE contour spanning the whole
// playfield. The observed boxes were (63,0)-(1217,612) on last_troop_bar.png and
// (0,0)-(1156,612) on last_failure.png; a box that starts in the resource HUD
// and reaches the far playfield edge is not a boundary, it is the base.
//
// Accepting such a box is worse than reporting no box at all, because every
// consumer believed nearly the whole screen was undeployable: the dynamic
// calculator's per-side free space collapsed to ~60px, and the red-line push
// shoved formula points radially away from a meaningless centre, moving real
// taps off the ground the user authored. The "no boundary known" path is
// already well defined (the push is skipped and the formula/pinned line
// stands), so the gate only ever removes a lie.
func boundaryPlausible(rect image.Rectangle, w, uiCutoff int) (bool, string) {
	switch {
	case rect.Dx() <= 0 || rect.Dy() <= 0:
		return false, "empty box"
	case rect.Min.Y < resourceHudRows:
		return false, fmt.Sprintf("box starts in the top resource HUD (min y %d < %d)", rect.Min.Y, resourceHudRows)
	case rect.Min.X < 6:
		return false, fmt.Sprintf("box reaches the left playfield edge (min x %d)", rect.Min.X)
	case rect.Max.X > w-6:
		return false, fmt.Sprintf("box reaches the right playfield edge (max x %d of %d)", rect.Max.X, w)
	case rect.Max.Y > uiCutoff-6:
		return false, fmt.Sprintf("box reaches the bottom playfield edge (max y %d of %d)", rect.Max.Y, uiCutoff)
	}
	// No separate "covers too much of the playfield" rule: the four edge tests
	// above already bound the box to at most ~0.79 of the playfield, so a
	// coverage threshold loose enough to accept a real boundary could never
	// fire. The two ways the live detector failed were reaching the HUD rows and
	// reaching a far edge, and both are named above.
	return true, ""
}

// resourceHudRows is the height of the top resource HUD in live-frame rows. A
// no-deploy boundary never starts inside it, and anything that does is the HUD
// itself (its gold/elixir bars sit squarely in the orange window). It mirrors
// the deploy band's own top bound so the detector and the deploy path agree on
// where the playfield starts.
const resourceHudRows = yTopMin

// Centre is the centroid of the red outline's bounding box, used as the origin
// to push a blocked deploy point away from.
func (z RedZone) Centre() (int, int) {
	if !z.Valid {
		return 0, 0
	}
	return (z.BBox.Min.X + z.BBox.Max.X) / 2, (z.BBox.Min.Y + z.BBox.Max.Y) / 2
}

// pointInPolygon is the standard even-odd ray cast. Points exactly on an edge
// are a coin toss in every implementation of this test; the caller pushes a
// blocked point outward by whole steps, so a hair either way costs one step.
func pointInPolygon(x, y int, poly []image.Point) bool {
	if len(poly) < 3 {
		return false
	}
	inside := false
	j := len(poly) - 1
	for i := 0; i < len(poly); i++ {
		yi, yj := poly[i].Y, poly[j].Y
		xi, xj := poly[i].X, poly[j].X
		if (yi > y) != (yj > y) {
			t := float64(xj-xi)*float64(y-yi)/float64(yj-yi) + float64(xi)
			if float64(x) < t {
				inside = !inside
			}
		}
		j = i
	}
	return inside
}

// RedLineDetector finds the red deployment boundary on screen.
type RedLineDetector struct {
	logger zerolog.Logger
}

// NewRedLineDetector creates detector.
func NewRedLineDetector(logger zerolog.Logger) *RedLineDetector {
	return &RedLineDetector{logger: logger.With().Str("component", "redline").Logger()}
}

// Detect finds the red deployment boundary in the screenshot.
// Returns bounding box of the red zone (the no-deploy area).
// Troops must deploy OUTSIDE this box.
func (r *RedLineDetector) Detect(screen gocv.Mat, uiCutoff int) RedZone {
	h, w := screen.Rows(), screen.Cols()

	roi := screen
	if uiCutoff < h {
		roi = screen.Region(image.Rect(0, 0, w, uiCutoff))
	}
	defer roi.Close()

	mask := r.detectRedMask(roi)
	defer mask.Close()

	contours := gocv.FindContours(mask, gocv.RetrievalExternal, gocv.ChainApproxSimple)
	if contours.Size() == 0 {
		r.logger.Debug().Msg("no red zone contours found")
		return RedZone{Valid: false}
	}

	type bbox struct {
		rect    image.Rectangle
		area    float64
		polygon []image.Point
	}
	var boxes []bbox

	for i := 0; i < contours.Size(); i++ {
		cnt := contours.At(i)
		area := gocv.ContourArea(cnt)
		if area < 400 {
			continue
		}
		rect := gocv.BoundingRect(cnt)
		boxes = append(boxes, bbox{rect: rect, area: area, polygon: contourPoints(cnt)})
	}

	if len(boxes) == 0 {
		r.logger.Debug().Msg("no valid red zone contours (all too small)")
		return RedZone{Valid: false}
	}

	sort.Slice(boxes, func(i, j int) bool {
		return boxes[i].area > boxes[j].area
	})

	minW := int(float64(w) * 0.55)
	minH := int(float64(uiCutoff) * 0.55)

	rejected := ""
	for _, b := range boxes {
		rect := b.rect

		if rect.Dx() < minW || rect.Dy() < minH {
			continue
		}

		if rect.Dx() > int(float64(w)*0.97) && rect.Dy() > int(float64(uiCutoff)*0.97) {
			continue
		}

		if ok, why := boundaryPlausible(rect, w, uiCutoff); !ok {
			rejected = fmt.Sprintf("(%d,%d)-(%d,%d): %s", rect.Min.X, rect.Min.Y, rect.Max.X, rect.Max.Y, why)
			continue
		}

		r.logger.Info().
			Int("x", rect.Min.X).
			Int("y", rect.Min.Y).
			Int("w", rect.Dx()).
			Int("h", rect.Dy()).
			Float64("area", b.area).
			Msg("red zone detected")

		return RedZone{
			BBox:     rect,
			Valid:    true,
			Contours: contours.Size(),
			Polygon:  b.polygon,
		}
	}

	xMin, yMin := w, uiCutoff
	xMax, yMax := 0, 0
	for _, b := range boxes {
		if b.rect.Min.X < xMin {
			xMin = b.rect.Min.X
		}
		if b.rect.Min.Y < yMin {
			yMin = b.rect.Min.Y
		}
		if b.rect.Max.X > xMax {
			xMax = b.rect.Max.X
		}
		if b.rect.Max.Y > yMax {
			yMax = b.rect.Max.Y
		}
	}

	combined := image.Rect(xMin, yMin, xMax, yMax)
	if combined.Dx() >= minW && combined.Dy() >= minH {
		// The union box and the polygon disagree by construction (the polygon
		// stays the largest single contour while the box spans every one of
		// them), so the union is only usable when the polygon's own box says
		// the same thing. Otherwise fall through to "no boundary known"
		// rather than hand a mismatched outline to Inside().
		if ok, why := boundaryPlausible(combined, w, uiCutoff); ok && boxes[0].rect == combined {
			r.logger.Info().
				Int("x", xMin).Int("y", yMin).
				Int("w", combined.Dx()).Int("h", combined.Dy()).
				Msg("red zone detected (combined contours)")

			return RedZone{
				BBox:     combined,
				Valid:    true,
				Contours: contours.Size(),
				// boxes is sorted by area, so the first entry is the outline of
				// the same contour the BBox came from.
				Polygon: boxes[0].polygon,
			}
		} else if why != "" {
			rejected = fmt.Sprintf("(%d,%d)-(%d,%d): %s", combined.Min.X, combined.Min.Y, combined.Max.X, combined.Max.Y, why)
		} else {
			rejected = fmt.Sprintf("(%d,%d)-(%d,%d): union box disagrees with the largest contour's own box", combined.Min.X, combined.Min.Y, combined.Max.X, combined.Max.Y)
		}
	}

	r.logger.Warn().
		Str("largest_rejected", rejected).
		Msg("red zone detection failed: no contour is a plausible no-deploy boundary (see largest_rejected for why)")
	return RedZone{Valid: false}
}

// Mask returns the closed binary mask Detect searches: the union of the
// red/orange HSV windows after the same morphology, cropped to uiCutoff. It
// exists for the review tools (cmd/attack_verify, cmd/see), which have to show
// a human *what* the detector is calling red. Nothing rendered the mask before,
// which is how a false "red zone" spanning the playfield survived: the report
// said "valid=true, bbox 63,0 1217x612" and read like a measurement.
//
// The caller owns the returned Mat and must Close it. Its top-left corner is
// the frame's top-left, so mask pixels overlay the screenshot at the same
// coordinates.
func (r *RedLineDetector) Mask(screen gocv.Mat, uiCutoff int) gocv.Mat {
	h, w := screen.Rows(), screen.Cols()
	if uiCutoff >= h {
		return r.detectRedMask(screen)
	}
	roi := screen.Region(image.Rect(0, 0, w, uiCutoff))
	defer roi.Close()
	return r.detectRedMask(roi)
}

// contourPoints flattens an OpenCV contour into Go points. The contour is
// owned by the FindContours result, so the points are copied out here and the
// caller keeps no OpenCV lifetime.
func contourPoints(cnt gocv.PointVector) []image.Point {
	pts := make([]image.Point, 0, cnt.Size())
	for i := 0; i < cnt.Size(); i++ {
		p := cnt.At(i)
		pts = append(pts, image.Pt(p.X, p.Y))
	}
	return pts
}

// redlineKernels are static structuring elements reused across every
// detectRedMask call. Building them once avoids repeatedly allocating +
// freeing kernel Mats on the per-deploy hot path.
var (
	redlineKernelsOnce              sync.Once
	redlineKh, redlineKv, redlineKs gocv.Mat
)

func redlineKernels() (gocv.Mat, gocv.Mat, gocv.Mat) {
	redlineKernelsOnce.Do(func() {
		redlineKh = gocv.GetStructuringElement(gocv.MorphRect, image.Pt(35, 3))
		redlineKv = gocv.GetStructuringElement(gocv.MorphRect, image.Pt(3, 35))
		redlineKs = gocv.GetStructuringElement(gocv.MorphRect, image.Pt(9, 9))
	})
	return redlineKh, redlineKv, redlineKs
}

// detectRedMask creates binary mask of red/orange/pink/magenta pixels.
// These colors form the deployment boundary dashed line.
func (r *RedLineDetector) detectRedMask(roi gocv.Mat) gocv.Mat {
	hsv := vision.GetMat(roi.Rows(), roi.Cols(), gocv.MatTypeCV8UC3)
	defer vision.PutMat(hsv)
	gocv.CvtColor(roi, &hsv, gocv.ColorBGRToHSV)

	m1 := vision.GetMatFrom(hsv)
	defer vision.PutMat(m1)
	m2 := vision.GetMatFrom(hsv)
	defer vision.PutMat(m2)
	m3 := vision.GetMatFrom(hsv)
	defer vision.PutMat(m3)
	m4 := vision.GetMatFrom(hsv)
	defer vision.PutMat(m4)
	m5 := vision.GetMatFrom(hsv)
	defer vision.PutMat(m5)

	gocv.InRangeWithScalar(hsv, gocv.NewScalar(0, 100, 100, 0), gocv.NewScalar(12, 255, 255, 0), &m1)
	gocv.InRangeWithScalar(hsv, gocv.NewScalar(168, 100, 100, 0), gocv.NewScalar(180, 255, 255, 0), &m2)
	gocv.InRangeWithScalar(hsv, gocv.NewScalar(10, 120, 120, 0), gocv.NewScalar(24, 255, 255, 0), &m3)
	gocv.InRangeWithScalar(hsv, gocv.NewScalar(140, 60, 160, 0), gocv.NewScalar(170, 220, 255, 0), &m4)
	gocv.InRangeWithScalar(hsv, gocv.NewScalar(150, 80, 140, 0), gocv.NewScalar(175, 255, 255, 0), &m5)

	mask := gocv.NewMat()

	gocv.BitwiseOr(m1, m2, &mask)

	tmp := vision.GetMatFrom(hsv)
	defer vision.PutMat(tmp)
	gocv.BitwiseOr(mask, m3, &tmp)
	tmp.CopyTo(&mask)
	gocv.BitwiseOr(mask, m4, &tmp)
	tmp.CopyTo(&mask)
	gocv.BitwiseOr(mask, m5, &tmp)
	tmp.CopyTo(&mask)

	kh, kv, ks := redlineKernels()
	gocv.MorphologyEx(mask, &tmp, gocv.MorphClose, kh)
	tmp.CopyTo(&mask)
	gocv.MorphologyEx(mask, &tmp, gocv.MorphClose, kv)
	tmp.CopyTo(&mask)
	gocv.MorphologyEx(mask, &tmp, gocv.MorphClose, ks)
	tmp.CopyTo(&mask)

	return mask
}
