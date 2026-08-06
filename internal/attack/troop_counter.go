package attack

import (
	"fmt"
	"image"
	"os"
	"path/filepath"
	"strconv"

	"github.com/Ducky705/ClashGO/internal/paths"
	"github.com/Ducky705/ClashGO/internal/vision"
	"github.com/rs/zerolog"
	"gocv.io/x/gocv"
)

// TroopCounter detects troop count numbers above each card slot.
// Uses template matching on digit_0..digit_9 templates to read the count.
type TroopCounter struct {
	digitTemplates [10]gocv.Mat

	scaledDigitCache map[string][10]gocv.Mat
	logger           zerolog.Logger
}

// TroopCount represents a detected troop count for a slot.
type TroopCount struct {
	X          int
	Count      int
	Confidence float64
	Digits     []int
}

// NewTroopCounter creates a new troop counter with digit templates.
func NewTroopCounter(refW, refH int, logger zerolog.Logger) *TroopCounter {
	tc := &TroopCounter{
		logger:           logger.With().Str("component", "troop_counter").Logger(),
		scaledDigitCache: make(map[string][10]gocv.Mat),
	}
	tc.loadDigitTemplates()
	return tc
}

// loadDigitTemplates loads digit_0..digit_9 templates from the templates directory.
func (tc *TroopCounter) loadDigitTemplates() {
	digitDir := paths.Resolve("templates/digits")
	if _, err := os.Stat(digitDir); os.IsNotExist(err) {

		digitDir = paths.Resolve("templates")
	}

	loaded := 0
	for i := 0; i < 10; i++ {
		name := fmt.Sprintf("digit_%d", i)
		path := filepath.Join(digitDir, name+".png")
		mat := gocv.IMRead(path, gocv.IMReadGrayScale)
		if mat.Empty() {
			tc.logger.Debug().Str("name", name).Msg("digit template not found")
			continue
		}

		bin := gocv.NewMat()
		gocv.Threshold(mat, &bin, 128, 255, gocv.ThresholdBinary)

		rect := tightBoundingBox(bin)
		if !rect.Empty() {
			tight := bin.Region(rect)
			tc.digitTemplates[i] = tight.Clone()
			tight.Close()
		} else {
			tc.digitTemplates[i] = bin.Clone()
		}
		bin.Close()
		mat.Close()
		loaded++
	}

	tc.logger.Info().Int("loaded", loaded).Msg("digit templates loaded")
}

// DetectCounts detects troop counts for all slots on the bar.
// The count number appears above each card in the troop bar.
func (tc *TroopCounter) DetectCounts(screen gocv.Mat, slots []*TrackedSlot, barY int) []TroopCount {
	w := screen.Cols()
	h := screen.Rows()
	scaleX := float64(w) / 860.0
	scaleY := float64(h) / 732.0

	var results []TroopCount

	for _, slot := range slots {
		count := tc.detectSlotCount(screen, slot.X, slot.Y, barY, scaleX, scaleY)
		results = append(results, count)
	}

	return results
}

// detectSlotCount detects the count for a single slot.
// The count number appears as "x60", "x9" etc. above each card in the troop bar.
func (tc *TroopCounter) detectSlotCount(screen gocv.Mat, slotX, slotY, barY int, scaleX, scaleY float64) TroopCount {
	result := TroopCount{
		X:     slotX,
		Count: 0,
	}

	cardWidth := int(60.0 * scaleX)
	digitHeight := int(18.0 * scaleY)
	digitWidth := int(12.0 * scaleX)

	roiX1 := slotX - cardWidth/2 + int(5.0*scaleX)
	roiY1 := barY - int(5.0*scaleY)
	roiX2 := slotX + cardWidth/2 - int(5.0*scaleX)
	roiY2 := slotY - int(25.0*scaleY)

	if roiX1 < 0 {
		roiX1 = 0
	}
	if roiY1 < 0 {
		roiY1 = 0
	}
	if roiX2 > screen.Cols() {
		roiX2 = screen.Cols()
	}
	if roiY2 > screen.Rows() {
		roiY2 = screen.Rows()
	}

	if roiX2 <= roiX1 || roiY2 <= roiY1 {
		return result
	}

	roi := screen.Region(image.Rect(roiX1, roiY1, roiX2, roiY2))
	defer roi.Close()

	gray := gocv.NewMat()
	defer gray.Close()
	if roi.Channels() == 3 {
		gocv.CvtColor(roi, &gray, gocv.ColorBGRToGray)
	} else {
		roi.CopyTo(&gray)
	}

	bin := gocv.NewMat()
	defer bin.Close()
	gocv.Threshold(gray, &bin, 160, 255, gocv.ThresholdBinary)

	digits := tc.extractDigits(bin, digitWidth, digitHeight)
	if len(digits) == 0 {
		return result
	}

	var detectedDigits []int
	var totalConf float64
	for _, d := range digits {
		digit, conf := tc.matchDigit(d)
		if digit >= 0 {
			detectedDigits = append(detectedDigits, digit)
			totalConf += conf
		}
		d.Close()
	}

	if len(detectedDigits) == 0 {
		return result
	}

	count := 0
	for _, d := range detectedDigits {
		count = count*10 + d
	}

	result.Count = count
	result.Digits = detectedDigits
	result.Confidence = totalConf / float64(len(detectedDigits))

	tc.logger.Debug().
		Int("x", slotX).
		Int("count", count).
		Float64("conf", result.Confidence).
		Msg("detected troop count")

	return result
}

// extractDigits extracts individual digit regions from a binary image.
func (tc *TroopCounter) extractDigits(bin gocv.Mat, digitWidth, digitHeight int) []gocv.Mat {

	contours := gocv.FindContours(bin, gocv.RetrievalExternal, gocv.ChainApproxSimple)
	defer contours.Close()

	var digits []gocv.Mat
	var rects []image.Rectangle

	for i := 0; i < contours.Size(); i++ {
		c := contours.At(i)
		rect := gocv.BoundingRect(c)
		w := rect.Dx()
		h := rect.Dy()

		if w < digitWidth/3 || w > digitWidth*3 {
			continue
		}
		if h < digitHeight/3 || h > digitHeight*3 {
			continue
		}

		aspect := float64(w) / float64(h)
		if aspect < 0.2 || aspect > 2.0 {
			continue
		}

		rects = append(rects, rect)
	}

	sortRectsLeftToRight(rects)

	for _, rect := range rects {
		sub := bin.Region(rect)
		digits = append(digits, sub.Clone())
		sub.Close()
	}

	return digits
}

// matchDigit matches a single digit image against templates. Digit size is
// fixed within a read, so the 10 templates are scaled to (bw,bh) once per size
// and cached on the counter — avoiding a Resize of all templates for every
// digit. The result Mat is drawn from the shared pool; the match mask is the
// zero-value (no mask), which avoids a throwaway allocation per template.
func (tc *TroopCounter) matchDigit(digitImg gocv.Mat) (int, float64) {
	bestDigit := -1
	maxConf := float32(0.0)
	bw, bh := digitImg.Cols(), digitImg.Rows()
	if bw < 1 || bh < 1 {
		return -1, 0.0
	}

	key := strconv.Itoa(bw) + "x" + strconv.Itoa(bh)
	scaled, ok := tc.scaledDigitCache[key]
	if !ok {
		var built [10]gocv.Mat
		for i, tpl := range tc.digitTemplates {
			if tpl.Empty() {
				continue
			}
			s := gocv.NewMat()
			gocv.Resize(tpl, &s, image.Point{X: bw, Y: bh}, 0, 0, gocv.InterpolationLinear)
			built[i] = s
		}
		scaled = built
		tc.scaledDigitCache[key] = built
	}

	for i, tpl := range scaled {
		if tpl.Empty() {
			continue
		}
		res := vision.GetMat(digitImg.Rows()-tpl.Rows()+1, digitImg.Cols()-tpl.Cols()+1, gocv.MatTypeCV32FC1)
		gocv.MatchTemplate(digitImg, tpl, &res, gocv.TmCcoeffNormed, vision.EmptyMask())
		_, conf, _, _ := gocv.MinMaxLoc(res)
		vision.PutMat(res)

		if float32(conf) > maxConf {
			maxConf = float32(conf)
			bestDigit = i
		}
	}

	if bestDigit == -1 || maxConf < 0.55 {
		if bw >= 1 && bw <= 8 && bh >= 10 {
			fill := float64(gocv.CountNonZero(digitImg)) / float64(bw*bh)
			if fill > 0.65 {
				return 1, 0.6
			}
		}
	}

	if maxConf < 0.5 {
		return -1, 0.0
	}
	return bestDigit, float64(maxConf)
}

// sortRectsLeftToRight sorts rectangles by X coordinate (left to right).
func sortRectsLeftToRight(rects []image.Rectangle) {
	for i := 1; i < len(rects); i++ {
		for j := i; j > 0 && rects[j].Min.X < rects[j-1].Min.X; j-- {
			rects[j], rects[j-1] = rects[j-1], rects[j]
		}
	}
}

// tightBoundingBox finds the tight bounding box of white pixels in a binary image.
func tightBoundingBox(bin gocv.Mat) image.Rectangle {
	rows, cols := bin.Rows(), bin.Cols()
	xMin, xMax, yMin, yMax := cols, 0, rows, 0
	found := false

	for y := 0; y < rows; y++ {
		for x := 0; x < cols; x++ {
			if bin.GetUCharAt(y, x) > 128 {
				if x < xMin {
					xMin = x
				}
				if x > xMax {
					xMax = x
				}
				if y < yMin {
					yMin = y
				}
				if y > yMax {
					yMax = y
				}
				found = true
			}
		}
	}

	if !found {
		return image.Rectangle{}
	}
	return image.Rect(xMin, yMin, xMax+1, yMax+1)
}

// GetCountForSlot returns the detected count for a specific slot X coordinate.
func GetCountForSlot(counts []TroopCount, slotX int) int {
	for _, c := range counts {
		if c.X == slotX {
			return c.Count
		}
	}
	return 0
}

// GetAllCounts returns a map of slot X -> count.
func GetAllCounts(counts []TroopCount) map[int]int {
	result := make(map[int]int)
	for _, c := range counts {
		result[c.X] = c.Count
	}
	return result
}

// HasDigitTemplates returns true if digit templates are loaded.
func (tc *TroopCounter) HasDigitTemplates() bool {
	for _, tpl := range tc.digitTemplates {
		if !tpl.Empty() {
			return true
		}
	}
	return false
}

// DetectCount returns the live detected count above a single slot's card
// in the provided screen. Convenience wrapper around the per-slot OCR so
// HeroManager / Sweeper / Verifier can re-read counts at deploy time
// instead of trusting the once-cached snapshot.
//
// Returns 0 when OCR fails or the count read is 0 — caller should treat
// 0 as "unknown" and combine with a visual empty check to decide whether
// the slot is actually empty.
func (tc *TroopCounter) DetectCount(screen gocv.Mat, slot *TrackedSlot, barY int) int {
	if screen.Empty() || slot == nil {
		return 0
	}
	scaleX := float64(screen.Cols()) / 860.0
	scaleY := float64(screen.Rows()) / 732.0
	res := tc.detectSlotCount(screen, slot.X, slot.Y, barY, scaleX, scaleY)
	return res.Count
}
