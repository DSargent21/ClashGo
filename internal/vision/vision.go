package vision

import (
	"fmt"
	"gocv.io/x/gocv"
	"image"
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
	if screen.Empty() || template.Empty() {
		return nil, fmt.Errorf("empty image")
	}

	mask := gocv.NewMat()
	defer mask.Close()

	result := gocv.NewMat()
	defer result.Close()

	gocv.MatchTemplate(screen, template, &result, gocv.TmCcoeffNormed, mask)

	_, maxVal, _, maxLoc := gocv.MinMaxLoc(result)
	if maxVal < threshold {
		return nil, nil
	}

	pt := image.Pt(maxLoc.X+template.Cols()/2, maxLoc.Y+template.Rows()/2)
	return []Match{{Point: pt, Confidence: float64(maxVal), Scale: 1.0}}, nil
}

func MatchTemplateBest(screen, template gocv.Mat, threshold float32) (image.Point, float64, error) {
	matches, err := MatchTemplate(screen, template, threshold)
	if err != nil || len(matches) == 0 {
		return image.Point{}, 0, err
	}
	return matches[0].Point, matches[0].Confidence, nil
}

func MatchMultiScale(screen, template gocv.Mat, minScale, maxScale float64, steps int, threshold float32) ([]Match, error) {
	if screen.Empty() || template.Empty() {
		return nil, fmt.Errorf("empty image")
	}

	var allMatches []Match
	scaleStep := (maxScale - minScale) / float64(steps)
	for s := minScale; s <= maxScale; s += scaleStep {
		w := int(float64(template.Cols()) * s)
		h := int(float64(template.Rows()) * s)
		if w < 5 || h < 5 || w > screen.Cols() || h > screen.Rows() {
			continue
		}

		resized := gocv.NewMat()
		gocv.Resize(template, &resized, image.Point{X: w, Y: h}, 0, 0, gocv.InterpolationLinear)
		
		result := gocv.NewMat()
		gocv.MatchTemplate(screen, resized, &result, gocv.TmCcoeffNormed, gocv.NewMat())

		_, maxVal, _, maxLoc := gocv.MinMaxLoc(result)

		if maxVal >= threshold {
			// maxLoc is already in screen coordinates
			pt := image.Pt(maxLoc.X+resized.Cols()/2, maxLoc.Y+resized.Rows()/2)
			allMatches = append(allMatches, Match{Point: pt, Confidence: float64(maxVal), Scale: s})
		}
		resized.Close()
		result.Close()
	}

	return allMatches, nil
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

	lowerRed1 := gocv.NewScalar(0, 100, 100, 0)
	upperRed1 := gocv.NewScalar(10, 255, 255, 0)
	lowerRed2 := gocv.NewScalar(160, 100, 100, 0)
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
