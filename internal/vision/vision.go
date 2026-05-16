package vision

import (
	"fmt"
	"gocv.io/x/gocv"
	"image"
	"image/color"
	"math"
)

type Match struct {
	Point      image.Point
	Confidence float64
	Scale      float64
}

func ResizeToHeight(src gocv.Mat, targetHeight int) gocv.Mat {
	if src.Empty() {
		return gocv.NewMat()
	}
	ratio := float64(targetHeight) / float64(src.Rows())
	targetWidth := int(float64(src.Cols()) * ratio)

	dst := gocv.NewMat()
	gocv.Resize(src, &dst, image.Point{X: targetWidth, Y: targetHeight}, 0, 0, gocv.InterpolationLinear)
	return dst
}

func MatchTemplate(screen, template gocv.Mat, threshold float32) ([]Match, error) {
	// Standardize to multi-scale search to handle different screen densities
	// Use a wider range (0.2 to 2.0) and more steps for better reliability
	return MatchMultiScale(screen, template, 0.2, 2.0, 30, threshold)
}

func MatchTemplateBest(screen, template gocv.Mat, threshold float32) (image.Point, float64, error) {
	matches, err := MatchTemplate(screen, template, threshold)
	if err != nil || len(matches) == 0 {
		return image.Point{}, 0, err
	}
	return matches[0].Point, matches[0].Confidence, nil
}

func MatchMultiScale(screen, template gocv.Mat, minScale, maxScale float64, steps int, threshold float32) ([]Match, error) {
	return MatchMultiScaleROI(screen, template, minScale, maxScale, steps, threshold, image.Rect(0, 0, screen.Cols(), screen.Rows()))
}

func MatchMultiScaleROI(screen, template gocv.Mat, minScale, maxScale float64, steps int, threshold float32, roi image.Rectangle) ([]Match, error) {
	if screen.Empty() || template.Empty() {
		return nil, fmt.Errorf("empty image or template")
	}

	// Safety check: ensure template is not larger than screen
	if template.Cols() > screen.Cols() || template.Rows() > screen.Rows() {
		return nil, nil
	}

	// Clamp ROI to screen bounds
	if roi.Min.X < 0 {
		roi.Min.X = 0
	}
	if roi.Min.Y < 0 {
		roi.Min.Y = 0
	}
	if roi.Max.X > screen.Cols() {
		roi.Max.X = screen.Cols()
	}
	if roi.Max.Y > screen.Rows() {
		roi.Max.Y = screen.Rows()
	}

	if roi.Dx() < 2 || roi.Dy() < 2 {
		return nil, nil
	}

	searchArea := screen.Region(roi)
	defer searchArea.Close()
	
	if searchArea.Empty() {
		return nil, nil
	}

	bestConfidence := -1.0
	var bestMatch *Match

	// Optimization: if steps is 1, just use minScale
	if steps <= 1 {
		steps = 1
	}

	for i := 0; i < steps; i++ {
		scale := minScale
		if steps > 1 {
			scale = minScale + (maxScale-minScale)*float64(i)/float64(steps-1)
		}

		scaledTpl := gocv.NewMat()
		gocv.Resize(template, &scaledTpl, image.Point{}, scale, scale, gocv.InterpolationLinear)

		if scaledTpl.Empty() || scaledTpl.Cols() > searchArea.Cols() || scaledTpl.Rows() > searchArea.Rows() {
			scaledTpl.Close()
			continue
		}

		if scaledTpl.Cols() < 2 || scaledTpl.Rows() < 2 {
			scaledTpl.Close()
			continue
		}

		res := gocv.NewMat()
		gocv.MatchTemplate(searchArea, scaledTpl, &res, gocv.TmCcoeffNormed, gocv.NewMat())

		if res.Empty() {
			res.Close()
			scaledTpl.Close()
			continue
		}

		_, maxVal, _, maxLoc := gocv.MinMaxLoc(res)
		if float64(maxVal) > bestConfidence {
			bestConfidence = float64(maxVal)
			cx := maxLoc.X + scaledTpl.Cols()/2 + roi.Min.X
			cy := maxLoc.Y + scaledTpl.Rows()/2 + roi.Min.Y
			bestMatch = &Match{
				Point:      image.Pt(cx, cy),
				Confidence: float64(maxVal),
				Scale:      scale,
			}
		}

		res.Close()
		scaledTpl.Close()
	}

	if bestMatch != nil && bestMatch.Confidence >= float64(threshold) {
		return []Match{*bestMatch}, nil
	}

	return nil, nil
}

// MatchMultiScaleAll returns ALL matches above threshold for each scale,
// not just the single best one. This is essential for OCR where the same
// digit (e.g. "00") may appear multiple times.
func MatchMultiScaleAll(screen, template gocv.Mat, minScale, maxScale float64, steps int, threshold float32) ([]Match, error) {
	return MatchMultiScaleAllROI(screen, template, minScale, maxScale, steps, threshold, image.Rect(0, 0, screen.Cols(), screen.Rows()))
}

func MatchMultiScaleAllROI(screen, template gocv.Mat, minScale, maxScale float64, steps int, threshold float32, roi image.Rectangle) ([]Match, error) {
	if screen.Empty() || template.Empty() {
		return nil, fmt.Errorf("empty image or template")
	}
	if template.Cols() > screen.Cols() || template.Rows() > screen.Rows() {
		return nil, nil
	}
	roi = roi.Intersect(image.Rect(0, 0, screen.Cols(), screen.Rows()))
	if roi.Dx() < 2 || roi.Dy() < 2 {
		return nil, nil
	}

	searchArea := screen.Region(roi)
	defer searchArea.Close()
	if searchArea.Empty() {
		return nil, nil
	}

	if steps <= 1 {
		steps = 1
	}

	var allMatches []Match

	for i := 0; i < steps; i++ {
		scale := minScale
		if steps > 1 {
			scale = minScale + (maxScale-minScale)*float64(i)/float64(steps-1)
		}

		scaledTpl := gocv.NewMat()
		gocv.Resize(template, &scaledTpl, image.Point{}, scale, scale, gocv.InterpolationLinear)

		if scaledTpl.Empty() || scaledTpl.Cols() > searchArea.Cols() || scaledTpl.Rows() > searchArea.Rows() {
			scaledTpl.Close()
			continue
		}
		if scaledTpl.Cols() < 2 || scaledTpl.Rows() < 2 {
			scaledTpl.Close()
			continue
		}

		res := gocv.NewMat()
		gocv.MatchTemplate(searchArea, scaledTpl, &res, gocv.TmCcoeffNormed, gocv.NewMat())

		if res.Empty() {
			res.Close()
			scaledTpl.Close()
			continue
		}

		// Iteratively find the best match, record it, and mask it out.
		for {
			_, maxVal, _, maxLoc := gocv.MinMaxLoc(res)
			if float64(maxVal) < float64(threshold) {
				break
			}
			cx := maxLoc.X + scaledTpl.Cols()/2 + roi.Min.X
			cy := maxLoc.Y + scaledTpl.Rows()/2 + roi.Min.Y
			allMatches = append(allMatches, Match{
				Point:      image.Pt(cx, cy),
				Confidence: float64(maxVal),
				Scale:      scale,
			})

			// Suppress this region in the result matrix so MinMaxLoc finds the
			// next peak. Suppression radius = 1 template width.
			suppress := image.Rect(
				maxLoc.X-scaledTpl.Cols()/2,
				maxLoc.Y-scaledTpl.Rows()/2,
				maxLoc.X+scaledTpl.Cols()/2,
				maxLoc.Y+scaledTpl.Rows()/2,
			)
			suppress = suppress.Intersect(image.Rect(0, 0, res.Cols(), res.Rows()))
			if suppress.Dx() > 0 && suppress.Dy() > 0 {
				gocv.Rectangle(&res, suppress, color.RGBA{0, 0, 0, 255}, -1)
			}
		}

		res.Close()
		scaledTpl.Close()
	}

	if len(allMatches) > 0 {
		return allMatches, nil
	}
	return nil, nil
}

func MatchTemplateRegion(screen, template gocv.Mat, rect image.Rectangle, threshold float32) (image.Point, float64, error) {
	if screen.Empty() || template.Empty() {
		return image.Point{}, 0, fmt.Errorf("empty image")
	}

	// Ensure rect is within screen bounds
	if rect.Min.X < 0 { rect.Min.X = 0 }
	if rect.Min.Y < 0 { rect.Min.Y = 0 }
	if rect.Max.X > screen.Cols() { rect.Max.X = screen.Cols() }
	if rect.Max.Y > screen.Rows() { rect.Max.Y = screen.Rows() }

	if rect.Dx() < template.Cols() || rect.Dy() < template.Rows() {
		return image.Point{}, 0, fmt.Errorf("region smaller than template")
	}

	region := screen.Region(rect)
	defer region.Close()

	pt, conf, err := MatchTemplateBest(region, template, threshold)
	if err != nil {
		return image.Point{}, 0, err
	}

	// Offset point back to global screen coordinates
	return pt.Add(rect.Min), conf, nil
}

func PixelSearch(screen gocv.Mat, rect image.Rectangle, r, g, b int, tolerance int) (image.Point, error) {
	region := screen.Region(rect)
	defer region.Close()

	toleranceF := float64(tolerance)

	for row := 0; row < region.Rows(); row++ {
		for col := 0; col < region.Cols(); col++ {
			b0 := region.GetUCharAt(row, col*3)
			g0 := region.GetUCharAt(row, col*3+1)
			r0 := region.GetUCharAt(row, col*3+2)

			dr := math.Abs(float64(r0) - float64(r))
			dg := math.Abs(float64(g0) - float64(g))
			db := math.Abs(float64(b0) - float64(b))
			if math.Sqrt(dr*dr+dg*dg+db*db) <= toleranceF {
				return image.Pt(rect.Min.X+col, rect.Min.Y+row), nil
			}
		}
	}

	return image.Point{}, nil
}

func MultiPixelSearch(screen gocv.Mat, rect image.Rectangle, pixels []Pixel, tolerance int) bool {
	region := screen.Region(rect)
	defer region.Close()

	for _, pix := range pixels {
		py := pix.Y - rect.Min.Y
		px := pix.X - rect.Min.X
		if py < 0 || py >= region.Rows() || px < 0 || px >= region.Cols() {
			return false
		}

		b0 := region.GetUCharAt(py, px*3)
		g0 := region.GetUCharAt(py, px*3+1)
		r0 := region.GetUCharAt(py, px*3+2)

		dr := math.Abs(float64(r0) - float64(pix.R))
		dg := math.Abs(float64(g0) - float64(pix.G))
		db := math.Abs(float64(b0) - float64(pix.B))

		if dr > float64(tolerance) || dg > float64(tolerance) || db > float64(tolerance) {
			return false
		}
	}

	return true
}

type Pixel struct {
	X, Y int
	R, G, B int
}

func (p Pixel) String() string {
	return fmt.Sprintf("Pixel(%d,%d RGB(%d,%d,%d))", p.X, p.Y, p.R, p.G, p.B)
}

func FindRedArea(screen gocv.Mat, minArea int) ([]image.Point, error) {
	blurred := gocv.NewMat()
	defer blurred.Close()
	gocv.GaussianBlur(screen, &blurred, image.Point{X: 5, Y: 5}, 0, 0, gocv.BorderDefault)

	hsv := gocv.NewMat()
	defer hsv.Close()
	gocv.CvtColor(blurred, &hsv, gocv.ColorBGRToHSV)

	lowerRed1 := gocv.NewScalar(0, 150, 150, 0)
	upperRed1 := gocv.NewScalar(10, 255, 255, 0)
	lowerRed2 := gocv.NewScalar(160, 150, 150, 0)
	upperRed2 := gocv.NewScalar(180, 255, 255, 0)

	mask1 := gocv.NewMat()
	mask2 := gocv.NewMat()
	gocv.InRangeWithScalar(hsv, lowerRed1, upperRed1, &mask1)
	gocv.InRangeWithScalar(hsv, lowerRed2, upperRed2, &mask2)

	mask := gocv.NewMat()
	defer mask.Close()
	gocv.BitwiseOr(mask1, mask2, &mask)
	defer mask1.Close()
	defer mask2.Close()

	kernel := gocv.GetStructuringElement(gocv.MorphRect, image.Point{X: 3, Y: 3})
	defer kernel.Close()
	gocv.MorphologyEx(mask, &mask, gocv.MorphOpen, kernel)

	contours := gocv.FindContours(mask, gocv.RetrievalExternal, gocv.ChainApproxSimple)
	defer contours.Close()

	var points []image.Point
	for i := 0; i < contours.Size(); i++ {
		area := gocv.ContourArea(contours.At(i))
		if area < float64(minArea) {
			continue
		}
		rect := gocv.BoundingRect(contours.At(i))
		points = append(points, image.Pt(rect.Min.X+rect.Dx()/2, rect.Min.Y+rect.Dy()/2))
	}

	return points, nil
}

func CalculateBaseEdges(screen gocv.Mat, minArea int) (top, bottom, left, right image.Point, err error) {
	blurred := gocv.NewMat()
	defer blurred.Close()
	gocv.GaussianBlur(screen, &blurred, image.Point{X: 5, Y: 5}, 0, 0, gocv.BorderDefault)

	hsv := gocv.NewMat()
	defer hsv.Close()
	gocv.CvtColor(blurred, &hsv, gocv.ColorBGRToHSV)

	lowerRed1 := gocv.NewScalar(0, 150, 150, 0)
	upperRed1 := gocv.NewScalar(10, 255, 255, 0)
	lowerRed2 := gocv.NewScalar(160, 150, 150, 0)
	upperRed2 := gocv.NewScalar(180, 255, 255, 0)

	mask1 := gocv.NewMat()
	mask2 := gocv.NewMat()
	gocv.InRangeWithScalar(hsv, lowerRed1, upperRed1, &mask1)
	gocv.InRangeWithScalar(hsv, lowerRed2, upperRed2, &mask2)

	mask := gocv.NewMat()
	defer mask.Close()
	gocv.BitwiseOr(mask1, mask2, &mask)
	defer mask1.Close()
	defer mask2.Close()

	kernel := gocv.GetStructuringElement(gocv.MorphRect, image.Point{X: 3, Y: 3})
	defer kernel.Close()
	gocv.MorphologyEx(mask, &mask, gocv.MorphOpen, kernel)

	contours := gocv.FindContours(mask, gocv.RetrievalExternal, gocv.ChainApproxSimple)
	defer contours.Close()

	// Combine all contour points into a single PointVector
	allContourPoints := gocv.NewPointVector()
	defer allContourPoints.Close()
	for i := 0; i < contours.Size(); i++ {
		area := gocv.ContourArea(contours.At(i))
		if area < float64(minArea) {
			continue
		}
		c := contours.At(i)
		for j := 0; j < c.Size(); j++ {
			allContourPoints.Append(c.At(j))
		}
	}

	if allContourPoints.Size() == 0 {
		return image.Point{}, image.Point{}, image.Point{}, image.Point{}, fmt.Errorf("no red area detected")
	}

	hull := gocv.NewMat()
	defer hull.Close()
	gocv.ConvexHull(allContourPoints, &hull, true, false)

	// Extract extreme points from hull
	top = allContourPoints.At(0)
	bottom = allContourPoints.At(0)
	left = allContourPoints.At(0)
	right = allContourPoints.At(0)

	for i := 0; i < hull.Rows(); i++ {
		idx := int(hull.GetIntAt(i, 0))
		pt := allContourPoints.At(idx)
		if pt.Y < top.Y {
			top = pt
		}
		if pt.Y > bottom.Y {
			bottom = pt
		}
		if pt.X < left.X {
			left = pt
		}
		if pt.X > right.X {
			right = pt
		}
	}

	return top, bottom, left, right, nil
}

func IsInsideDiamond(pt image.Point, cx, cy, left, right, top, bottom int) bool {
	dx := pt.X - cx
	dy := pt.Y - cy

	if dx == 0 {
		return math.Abs(float64(dy)) <= float64(top)
	}

	slope := float64(top) / float64(left)
	edgeY := math.Abs(float64(dx)) * slope

	return math.Abs(float64(dy)) <= edgeY
}

func Crop(img gocv.Mat, rect image.Rectangle) gocv.Mat {
	return img.Region(rect)
}

func SaveImage(img gocv.Mat, path string) bool {
	return gocv.IMWrite(path, img)
}

func LoadImage(path string) gocv.Mat {
	return gocv.IMRead(path, gocv.IMReadColor)
}

func ToGray(src gocv.Mat) gocv.Mat {
	var dst gocv.Mat
	gocv.CvtColor(src, &dst, gocv.ColorBGRToGray)
	return dst
}

func PixelColor(c [3]uint8) (r, g, b int) {
	b = int(c[0])
	g = int(c[1])
	r = int(c[2])
	return
}
