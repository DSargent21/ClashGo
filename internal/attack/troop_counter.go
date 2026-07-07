package attack

import (
	"fmt"
	"image"
	"os"
	"path/filepath"
	"strings"

	"github.com/Ducky705/ClashGO/internal/paths"
	"github.com/rs/zerolog"
	"gocv.io/x/gocv"
)

// TroopCounter detects troop count numbers above each card slot.
// Uses template matching on digit_0..digit_9 templates to read the count.
type TroopCounter struct {
	digitTemplates [10]gocv.Mat
	calibrated     bool
	scaleX         float64
	scaleY         float64
	logger         zerolog.Logger
}

// TroopCount represents a detected troop count for a slot.
type TroopCount struct {
	X          int     // Slot X coordinate
	Count      int     // Detected count (0 = unknown)
	Confidence float64 // Average confidence of digit matches
	Digits     []int   // Individual digits detected
}

// NewTroopCounter creates a new troop counter with digit templates.
func NewTroopCounter(refW, refH int, logger zerolog.Logger) *TroopCounter {
	tc := &TroopCounter{
		logger: logger.With().Str("component", "troop_counter").Logger(),
	}
	tc.loadDigitTemplates()
	return tc
}

// loadDigitTemplates loads digit_0..digit_9 templates from the templates directory.
func (tc *TroopCounter) loadDigitTemplates() {
	digitDir := paths.Resolve("templates/digits")
	if _, err := os.Stat(digitDir); os.IsNotExist(err) {
		// Try alternative location
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

		// Pre-process: threshold to binary
		bin := gocv.NewMat()
		gocv.Threshold(mat, &bin, 128, 255, gocv.ThresholdBinary)

		// Tight bounding box
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

	// Define ROI for digit detection above the card
	// From screenshot: numbers like "x60" appear just above the card image
	// Cards are at Y=682 (slotY), numbers appear at approximately Y=630-650
	cardWidth := int(60.0 * scaleX)
	digitHeight := int(18.0 * scaleY)
	digitWidth := int(12.0 * scaleX)

	// ROI: centered above the card, where the "xN" text appears
	// The number is typically centered horizontally on the card
	// and positioned just above the card image (between barY and slotY)
	roiX1 := slotX - cardWidth/2 + int(5.0*scaleX) // Slight inset from card edge
	roiY1 := barY - int(5.0*scaleY)                 // Just above bar line
	roiX2 := slotX + cardWidth/2 - int(5.0*scaleX)
	roiY2 := slotY - int(25.0*scaleY)               // Above the card icon

	// Clamp to screen bounds
	if roiX1 < 0 { roiX1 = 0 }
	if roiY1 < 0 { roiY1 = 0 }
	if roiX2 > screen.Cols() { roiX2 = screen.Cols() }
	if roiY2 > screen.Rows() { roiY2 = screen.Rows() }

	if roiX2 <= roiX1 || roiY2 <= roiY1 {
		return result
	}

	roi := screen.Region(image.Rect(roiX1, roiY1, roiX2, roiY2))
	defer roi.Close()

	// Convert to grayscale
	gray := gocv.NewMat()
	defer gray.Close()
	if roi.Channels() == 3 {
		gocv.CvtColor(roi, &gray, gocv.ColorBGRToGray)
	} else {
		roi.CopyTo(&gray)
	}

	// Threshold to get white digits on dark background
	// The numbers are white/light colored text
	bin := gocv.NewMat()
	defer bin.Close()
	gocv.Threshold(gray, &bin, 160, 255, gocv.ThresholdBinary)

	// Find digit bounding boxes
	digits := tc.extractDigits(bin, digitWidth, digitHeight)
	if len(digits) == 0 {
		return result
	}

	// Match each digit
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

	// Combine digits into number
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
	// Find connected components (white blobs)
	contours := gocv.FindContours(bin, gocv.RetrievalExternal, gocv.ChainApproxSimple)
	defer contours.Close()

	var digits []gocv.Mat
	var rects []image.Rectangle

	for i := 0; i < contours.Size(); i++ {
		c := contours.At(i)
		rect := gocv.BoundingRect(c)
		w := rect.Dx()
		h := rect.Dy()

		// Filter by size: digits are typically small and roughly square
		if w < digitWidth/3 || w > digitWidth*3 {
			continue
		}
		if h < digitHeight/3 || h > digitHeight*3 {
			continue
		}

		// Filter by aspect ratio
		aspect := float64(w) / float64(h)
		if aspect < 0.2 || aspect > 2.0 {
			continue
		}

		rects = append(rects, rect)
	}

	// Sort left to right (digits are read left to right)
	sortRectsLeftToRight(rects)

	// Extract each digit region
	for _, rect := range rects {
		sub := bin.Region(rect)
		digits = append(digits, sub.Clone())
		sub.Close()
	}

	return digits
}

// matchDigit matches a single digit image against templates.
func (tc *TroopCounter) matchDigit(digitImg gocv.Mat) (int, float64) {
	bestDigit := -1
	maxConf := float32(0.0)
	bw, bh := digitImg.Cols(), digitImg.Rows()

	for i, tpl := range tc.digitTemplates {
		if tpl.Empty() {
			continue
		}

		// Resize template to match digit size
		scaledTpl := gocv.NewMat()
		gocv.Resize(tpl, &scaledTpl, image.Point{X: bw, Y: bh}, 0, 0, gocv.InterpolationLinear)

		// Template match
		res := gocv.NewMat()
		mask := gocv.NewMat()
		gocv.MatchTemplate(digitImg, scaledTpl, &res, gocv.TmCcoeffNormed, mask)
		mask.Close()

		_, conf, _, _ := gocv.MinMaxLoc(res)
		if float32(conf) > maxConf {
			maxConf = float32(conf)
			bestDigit = i
		}
		res.Close()
		scaledTpl.Close()
	}

	// Fallback: narrow vertical blob is likely '1'
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
				if x < xMin { xMin = x }
				if x > xMax { xMax = x }
				if y < yMin { yMin = y }
				if y > yMax { yMax = y }
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

// troopNameClean cleans a troop name for matching.
func troopNameClean(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}
