package vision

import (
	"fmt"
	"gocv.io/x/gocv"
	"image"
	"image/color"
)

type Match struct {
	Point      image.Point
	Confidence float64
	Scale      float64
}

// emptyMask is a valid (non-nil native handle) empty cv::Mat used as the mask
// argument to cv::matchTemplate. Passing gocv.Mat{} (which has a NULL native
// handle) makes gocv dereference a NULL cv::Mat* inside OpenCV and SIGSEGV.
var emptyMask = gocv.NewMat()

// EmptyMask returns a shared valid (non-nil) empty cv::Mat for use as the mask
// argument of cv::matchTemplate. Never close the returned Mat.
func EmptyMask() gocv.Mat {
	return emptyMask
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
	// Standardize to multi-scale search to handle minor screen density differences.
	// 0.8 to 1.2 at 5 steps is 6x faster and covers standard device scaling.
	return MatchMultiScale(screen, template, 0.8, 1.2, 5, threshold)
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
	return MatchMultiScaleROICached(screen, template, "", minScale, maxScale, steps, threshold, roi)
}

func MatchMultiScaleROICached(screen, template gocv.Mat, templateName string, minScale, maxScale float64, steps int, threshold float32, roi image.Rectangle) ([]Match, error) {
	// NOTE: gocv.Mat.Empty() returns FALSE for a zero-size but allocated
	// Mat (native handle != nil, Cols()==0, Rows()==0). Such a Mat slips
	// past the historical .Empty() guard and crashes cgo MatchTemplate.
	// Use explicit dimension checks everywhere instead.
	if screen.Cols() < 1 || screen.Rows() < 1 || template.Cols() < 1 || template.Rows() < 1 {
		return nil, fmt.Errorf("empty image or template")
	}

	if template.Cols() > screen.Cols() || template.Rows() > screen.Rows() {
		return nil, nil
	}

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

	if searchArea.Cols() < 1 || searchArea.Rows() < 1 {
		return nil, nil
	}

	bestConfidence := -1.0
	var bestMatch *Match

	if steps <= 1 {
		steps = 1
	}

	var scaledTemplates []gocv.Mat
	if templateName != "" {
		scaledTemplates = GetScaledTemplates(templateName, template, minScale, maxScale, steps)
		steps = len(scaledTemplates)
	}

	for i := 0; i < steps; i++ {
		matchFound := func(i int) bool {
			var scaledTpl gocv.Mat
			var scale float64

			if templateName != "" && i < len(scaledTemplates) {
				scaledTpl = scaledTemplates[i]
				scale = minScale
				if steps > 1 {
					scale = minScale + (maxScale-minScale)*float64(i)/float64(steps-1)
				}
			} else {
				scale = minScale
				if steps > 1 {
					scale = minScale + (maxScale-minScale)*float64(i)/float64(steps-1)
				}
				scaledTpl = gocv.NewMat()
				gocv.Resize(template, &scaledTpl, image.Point{}, scale, scale, gocv.InterpolationLinear)
				defer scaledTpl.Close()
			}

			if scaledTpl.Empty() || scaledTpl.Cols() > searchArea.Cols() || scaledTpl.Rows() > searchArea.Rows() {
				return false
			}

			if scaledTpl.Cols() < 2 || scaledTpl.Rows() < 2 {
				return false
			}

			if searchArea.Empty() || scaledTpl.Empty() {
				return false
			}
			resRows := searchArea.Rows() - scaledTpl.Rows() + 1
			resCols := searchArea.Cols() - scaledTpl.Cols() + 1
			if resRows < 1 || resCols < 1 {
				return false
			}
			res := GetMat(resRows, resCols, gocv.MatTypeCV32FC1)
			if res.Empty() {
				res = gocv.NewMatWithSize(resRows, resCols, gocv.MatTypeCV32FC1)
			}
			defer PutMat(res)
			if res.Empty() {
				return false
			}
			if err := gocv.MatchTemplate(searchArea, scaledTpl, &res, gocv.TmCcoeffNormed, emptyMask); err != nil {
				return false
			}

			if res.Empty() {
				return false
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
			return true
		}(i)
		_ = matchFound
	}

	if bestMatch != nil && bestMatch.Confidence >= float64(threshold) {
		return []Match{*bestMatch}, nil
	}

	return nil, nil
}

func MatchMultiScaleAllROICached(screen, template gocv.Mat, templateName string, minScale, maxScale float64, steps int, threshold float32, roi image.Rectangle) ([]Match, error) {
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
	var scaledTemplates []gocv.Mat
	if templateName != "" {
		scaledTemplates = GetScaledTemplates(templateName, template, minScale, maxScale, steps)
		steps = len(scaledTemplates)
	}

	for i := 0; i < steps; i++ {
		matchFound := func(i int) bool {
			var scaledTpl gocv.Mat
			var scale float64

			if templateName != "" && i < len(scaledTemplates) {
				scaledTpl = scaledTemplates[i]
				scale = minScale
				if steps > 1 {
					scale = minScale + (maxScale-minScale)*float64(i)/float64(steps-1)
				}
			} else {
				scale = minScale
				if steps > 1 {
					scale = minScale + (maxScale-minScale)*float64(i)/float64(steps-1)
				}
				scaledTpl = gocv.NewMat()
				gocv.Resize(template, &scaledTpl, image.Point{}, scale, scale, gocv.InterpolationLinear)
				defer scaledTpl.Close()
			}

			if scaledTpl.Empty() || scaledTpl.Cols() > searchArea.Cols() || scaledTpl.Rows() > searchArea.Rows() {
				return false
			}
			if scaledTpl.Cols() < 2 || scaledTpl.Rows() < 2 {
				return false
			}

			if searchArea.Empty() || scaledTpl.Empty() {
				return false
			}
			resRows := searchArea.Rows() - scaledTpl.Rows() + 1
			resCols := searchArea.Cols() - scaledTpl.Cols() + 1
			if resRows < 1 || resCols < 1 {
				return false
			}
			res := GetMat(resRows, resCols, gocv.MatTypeCV32FC1)
			if res.Empty() {
				res = gocv.NewMatWithSize(resRows, resCols, gocv.MatTypeCV32FC1)
			}
			defer PutMat(res)
			if res.Empty() {
				return false
			}
			if err := gocv.MatchTemplate(searchArea, scaledTpl, &res, gocv.TmCcoeffNormed, emptyMask); err != nil {
				return false
			}

			if res.Empty() {
				return false
			}

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
			return true
		}(i)
		_ = matchFound
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
	if rect.Min.X < 0 {
		rect.Min.X = 0
	}
	if rect.Min.Y < 0 {
		rect.Min.Y = 0
	}
	if rect.Max.X > screen.Cols() {
		rect.Max.X = screen.Cols()
	}
	if rect.Max.Y > screen.Rows() {
		rect.Max.Y = screen.Rows()
	}

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

	lb := b - tolerance
	if lb < 0 {
		lb = 0
	}
	lg := g - tolerance
	if lg < 0 {
		lg = 0
	}
	lr := r - tolerance
	if lr < 0 {
		lr = 0
	}

	ub := b + tolerance
	if ub > 255 {
		ub = 255
	}
	ug := g + tolerance
	if ug > 255 {
		ug = 255
	}
	ur := r + tolerance
	if ur > 255 {
		ur = 255
	}

	lower := gocv.NewScalar(float64(lb), float64(lg), float64(lr), 0)
	upper := gocv.NewScalar(float64(ub), float64(ug), float64(ur), 0)

	mask := gocv.NewMat()
	defer mask.Close()
	gocv.InRangeWithScalar(region, lower, upper, &mask)

	contours := gocv.FindContours(mask, gocv.RetrievalExternal, gocv.ChainApproxSimple)
	defer contours.Close()

	if contours.Size() > 0 {
		c := contours.At(0)
		if c.Size() > 0 {
			pt := c.At(0)
			return image.Pt(rect.Min.X+pt.X, rect.Min.Y+pt.Y), nil
		}
	}

	return image.Point{}, nil
}
