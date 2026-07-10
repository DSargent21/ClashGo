package game

import (
	"encoding/json"
	"fmt"
	"image"
	"math"
	"os"
	"sort"
	"strconv"
	"sync"

	"github.com/Ducky705/ClashGO/internal/paths"
	"github.com/rs/zerolog"
	"gocv.io/x/gocv"
)

type LootRecognizer struct {
	cal            *Calibration
	templates      *TemplateStore
	digitTemplates []gocv.Mat
	logger         zerolog.Logger
	mu             sync.Mutex
	Debug          bool
}

type detectedDigit struct {
	rect  image.Rectangle
	digit int
	conf  float32
}

func NewLootRecognizer(cal *Calibration, ts *TemplateStore, logger zerolog.Logger) *LootRecognizer {
	lr := &LootRecognizer{
		cal:       cal,
		templates: ts,
		logger:    logger.With().Str("component", "loot_recognizer").Logger(),
	}
	lr.prepareDigitTemplates()
	return lr
}

func (lr *LootRecognizer) prepareDigitTemplates() {
	lr.digitTemplates = make([]gocv.Mat, 10)
	for i := 0; i < 10; i++ {
		name := fmt.Sprintf("digit_%d", i)
		tpl, ok := lr.templates.Get(name)
		if !ok || tpl.Empty() {
			continue
		}
		gray := gocv.NewMat()
		if tpl.Channels() == 3 {
			gocv.CvtColor(tpl, &gray, gocv.ColorBGRToGray)
		} else {
			tpl.CopyTo(&gray)
		}
		bin := gocv.NewMat()
		gocv.Threshold(gray, &bin, 128, 255, gocv.ThresholdBinary)
		rect := tightBoundingBox(bin)
		if !rect.Empty() {
			tight := bin.Region(rect)
			lr.digitTemplates[i] = tight.Clone()
			tight.Close()
		} else {
			lr.digitTemplates[i] = bin.Clone()
		}
		bin.Close()
		gray.Close()
	}
}

func (lr *LootRecognizer) Close() {
	for _, tpl := range lr.digitTemplates {
		if !tpl.Empty() {
			tpl.Close()
		}
	}
}

type LootReport struct{ Resources Resources }

type BattleResult struct {
	Loot  Resources
	Bonus Resources
	Stars int
}

func (lr *LootRecognizer) ReadAvailableLoot(screen gocv.Mat) (Resources, error) {
	report, _ := lr.ReadLootDetailed(screen)
	return report.Resources, nil
}

func (lr *LootRecognizer) ReadDestructionPercentage(screen gocv.Mat, roi image.Rectangle) int {
	if roi.Empty() {
		return 0
	}
	return lr.readRow(screen, roi)
}

// ReadBattleResult reads the loot and star counts shown on the Clash of
// Clans end-of-battle screen. Implementation mirrors ReadLootDetailed so
// end-of-battle parses at the same accuracy as scout-screen filtrering:
//
//   - Icon template match anchors the digit region (proven pattern from
//     ReadLootDetailed; 0.65 is the empirically safe threshold across
//     themes and emulator sizes).
//   - Static per-column slot rectangles are the universal safety net;
//     they are derived once from the column ROI so HUD shifts no longer
//     pull digits out of alignment (the old code hard-coded absolute
//     coordinates and went negative once calibrated ROIs moved).
//   - Digit extraction is bounded to the column width, preventing bleed
//     from the Bonus column into Battle Loot (or vice versa).
//
// Column ROIs can be overridden by assets/battle_loot_rois.json (preferred)
// and assets/star_points.json (star pixel centers); both files are written
// by tools/picker.py --preset battle-loot / star-points.
func (lr *LootRecognizer) ReadBattleResult(screen gocv.Mat) (BattleResult, error) {
	gray := gocv.NewMat()
	gocv.CvtColor(screen, &gray, gocv.ColorBGRToGray)
	defer gray.Close()

	var result BattleResult

	battleSearch := image.Rect(311, 313, 501, 428) // Battle Loot column
	bonusSearch := image.Rect(571, 366, 674, 452)  // League Bonus column

	if data, err := os.ReadFile(paths.Resolve("battle_loot_rois.json")); err == nil {
		var custom struct {
			BattleSearch struct{ X1, Y1, X2, Y2 int } `json:"battleSearch"`
			BonusSearch  struct{ X1, Y1, X2, Y2 int } `json:"bonusSearch"`
		}
		if json.Unmarshal(data, &custom) == nil {
			if custom.BattleSearch.X2 > custom.BattleSearch.X1 {
				battleSearch = image.Rect(custom.BattleSearch.X1, custom.BattleSearch.Y1, custom.BattleSearch.X2, custom.BattleSearch.Y2)
			}
			if custom.BonusSearch.X2 > custom.BonusSearch.X1 {
				bonusSearch = image.Rect(custom.BonusSearch.X1, custom.BonusSearch.Y1, custom.BonusSearch.X2, custom.BonusSearch.Y2)
			}
			lr.logger.Info().Msg("loaded custom battle loot ROIs")
		}
	}

	lr.captureBattleColumn(screen, battleSearch, &result.Loot)
	lr.captureBattleColumn(screen, bonusSearch, &result.Bonus)

	// Star detection. CoC's end-of-battle star centers were captured
	// empirically (see cmd/verify_end/main.go star debug); old hard-coded
	// 365/220 / 495/220 sat slightly off the gold pixels and undercounted
	// earned stars on bigger three-star outbreaks.
	starPoints := []image.Point{
		{X: 327, Y: 205}, // Left
		{X: 430, Y: 196}, // Middle
		{X: 535, Y: 210}, // Right
	}
	if data, err := os.ReadFile(paths.Resolve("star_points.json")); err == nil {
		var custom struct {
			Stars []struct{ X, Y int } `json:"stars"`
		}
		if json.Unmarshal(data, &custom) == nil && len(custom.Stars) == 3 {
			for i := 0; i < 3; i++ {
				starPoints[i] = image.Pt(custom.Stars[i].X, custom.Stars[i].Y)
			}
			lr.logger.Info().Msg("loaded custom star points")
		}
	}

	validStars := 0
	for _, pt := range starPoints {
		sx := int(float64(pt.X) * lr.cal.ScaleX)
		sy := int(float64(pt.Y) * lr.cal.ScaleY)
		rect := lr.safeRect(screen, image.Rect(sx-2, sy-2, sx+3, sy+3))
		if rect.Empty() {
			continue
		}
		sub := gray.Region(rect)
		if sub.Mean().Val1 > 100 {
			validStars++
		}
		sub.Close()
	}
	result.Stars = validStars
	if result.Stars > 3 {
		result.Stars = 3
	}

	return result, nil
}

// captureBattleColumn reads the three loot values (gold, elixir, DE) for
// a single column on the end-of-battle screen. `colRef` is the column's
// reference (860x732) ROI; it is split evenly into three absolute-physical
// slot rectangles. Each slot is then re-anchored on the icon template, so
// small theme/scale shifts still find the first digit immediately.
//
// Writes gold/elixir/de into the supplied *Resources.
func (lr *LootRecognizer) captureBattleColumn(screen gocv.Mat, colRef image.Rectangle, dst *Resources) {
	if colRef.Empty() {
		return
	}

	scaleX, scaleY := lr.cal.ScaleX, lr.cal.ScaleY
	sx1 := int(float64(colRef.Min.X) * scaleX)
	sy1 := int(float64(colRef.Min.Y) * scaleY)
	sx2 := int(float64(colRef.Max.X) * scaleX)
	sy2 := int(float64(colRef.Max.Y) * scaleY)

	// Inward padding so the row never touches the icon column edge.
	padL := int(8 * scaleX)
	padR := int(4 * scaleX)
	if sx2-sx1 < padL+padR+int(20*scaleX) {
		padL, padR = 4, 2 // Narrow column (bonus); relax padding proportionally.
	}

	colHeight := sy2 - sy1
	rowHeight := colHeight / 3
	if rowHeight < int(6*scaleY) {
		rowHeight = colHeight
	}

	makeRow := func(i int) image.Rectangle {
		y1 := sy1 + i*rowHeight
		y2 := y1 + rowHeight
		if i == 2 {
			y2 = sy2 // Pin the last row's bottom to the column boundary.
		}
		return lr.safeRect(screen, image.Rect(sx1+padL, y1, sx2-padR, y2))
	}
	rows := []image.Rectangle{makeRow(0), makeRow(1), makeRow(2)}
	iconNames := []string{"icon_gold", "icon_elixir", "icon_de"}
	values := []int{0, 0, 0}

	// Anchor search ROI = full column, but capped to the column boundary
	// so the icon template never returns hits from the next column.
	anchorROI := lr.safeRect(screen, image.Rect(sx1, sy1, sx2, sy2))

	const minConf = float32(0.65)
	for i, name := range iconNames {
		// Try icon-anchored read first (same proven pattern as
		// ReadLootDetailed). On match we offset from the icon out into
		// the slot's right edge, which guarantees digit span fits.
		if !anchorROI.Empty() {
			if tpl, ok := lr.templates.Get(name); ok && !tpl.Empty() {
				region := screen.Region(anchorROI)
				res := gocv.NewMat()
				gocv.MatchTemplate(region, tpl, &res, gocv.TmCcoeffNormed, gocv.NewMat())
				_, maxConf, _, maxLoc := gocv.MinMaxLoc(res)
				res.Close()
				region.Close()

				if maxConf > minConf {
					absX := anchorROI.Min.X + maxLoc.X
					absY := anchorROI.Min.Y + maxLoc.Y
					x1 := absX + int(4*scaleX)
					if x1 < rows[i].Min.X {
						x1 = rows[i].Min.X
					}
					x2 := min(absX+int(220*scaleX), rows[i].Max.X)
					rect := image.Rect(x1, absY-int(5*scaleY), x2, absY+tpl.Rows()+int(5*scaleY))
					values[i] = lr.readRow(screen, rect)
					continue
				}
			}
		}
		// Static fallback: the row rectangle derived from the column ROI.
		values[i] = lr.readRow(screen, rows[i])
	}

	dst.Gold = values[0]
	dst.Elixir = values[1]
	dst.DarkElixir = values[2]
}

// safeRect clamps r to img bounds. Returns image.Rectangle{} when r collapses.
func (lr *LootRecognizer) safeRect(img gocv.Mat, r image.Rectangle) image.Rectangle {
	if r.Min.X < 0 {
		r.Min.X = 0
	}
	if r.Min.Y < 0 {
		r.Min.Y = 0
	}
	if r.Max.X > img.Cols() {
		r.Max.X = img.Cols()
	}
	if r.Max.Y > img.Rows() {
		r.Max.Y = img.Rows()
	}
	if r.Max.X < r.Min.X {
		r.Max.X = r.Min.X
	}
	if r.Max.Y < r.Min.Y {
		r.Max.Y = r.Min.Y
	}
	return r
}

func (lr *LootRecognizer) ReadLootDetailed(screen gocv.Mat) (LootReport, error) {
	// High-Precision ROIs for Scouting (Reference 860x732)
	// We use very inclusive X range (starting at 40) because digits start immediately after icons
	icons := []struct {
		name, tpl string
		y1, y2    int
	}{
		{"gold", "icon_gold", 66, 100},
		{"elixir", "icon_elixir", 95, 128},
		{"de", "icon_de", 124, 157},
	}

	var results [3]int
	for i, ic := range icons {
		tpl, ok := lr.templates.Get(ic.tpl)
		if ok && !tpl.Empty() {
			res := gocv.NewMat()
			gocv.MatchTemplate(screen, tpl, &res, gocv.TmCcoeffNormed, gocv.NewMat())
			_, maxConf, _, maxLoc := gocv.MinMaxLoc(res)
			res.Close()

			if maxConf > 0.8 {
				// Anchor to icon. Starting ROI directly inside the icon area
				// because readRow uses Color/Saturation to skip the actual icon bits.
				rect := image.Rect(
					maxLoc.X+int(4*lr.cal.ScaleX),
					maxLoc.Y-int(5*lr.cal.ScaleY),
					maxLoc.X+int(450*lr.cal.ScaleX),
					maxLoc.Y+tpl.Rows()+int(5*lr.cal.ScaleY),
				)
				results[i] = lr.readRow(screen, rect)
				continue
			}
		}
		// Fallback ROIs: Inclusive X1=40 to catch the very first digit
		rect := image.Rect(int(40*lr.cal.ScaleX), int(float64(ic.y1)*lr.cal.ScaleY), int(450*lr.cal.ScaleX), int(float64(ic.y2)*lr.cal.ScaleY))
		results[i] = lr.readRow(screen, rect)
	}

	return LootReport{Resources: Resources{Gold: results[0], Elixir: results[1], DarkElixir: results[2]}}, nil
}

func (lr *LootRecognizer) readRow(screen gocv.Mat, roi image.Rectangle) int {
	roi = lr.safeRect(screen, roi)
	if roi.Empty() {
		return 0
	}

	sub := screen.Region(roi)
	defer sub.Close()

	gray := gocv.NewMat()
	defer gray.Close()
	gocv.CvtColor(sub, &gray, gocv.ColorBGRToGray)

	hsv := gocv.NewMat()
	defer hsv.Close()
	gocv.CvtColor(sub, &hsv, gocv.ColorBGRToHSV)

	// 1. Resize 5x first to ensure image dimensions are larger than kernel size (61x61)
	scaled := gocv.NewMat()
	defer scaled.Close()
	gocv.Resize(gray, &scaled, image.Point{X: 0, Y: 0}, 5.0, 5.0, gocv.InterpolationCubic)

	// 2. Estimate background brightness on scaled image with dynamic kernel size safety
	kSize := 61
	if scaled.Rows() < kSize {
		kSize = scaled.Rows()
	}
	if scaled.Cols() < kSize {
		kSize = scaled.Cols()
	}
	if kSize%2 == 0 {
		kSize--
	}
	if kSize < 3 {
		kSize = 3
	}

	bg := gocv.NewMat()
	defer bg.Close()
	gocv.GaussianBlur(scaled, &bg, image.Point{X: kSize, Y: kSize}, 0, 0, gocv.BorderDefault)

	// 3. Remove slow illumination changes
	norm := gocv.NewMat()
	defer norm.Close()
	gocv.Subtract(scaled, bg, &norm)

	// 4. Stretch contrast
	gocv.Normalize(norm, &norm, 0, 255, gocv.NormMinMax)

	// 5. Scaled Otsu thresholding to prevent hollow digits by keeping shaded inner text regions white
	binary := gocv.NewMat()
	defer binary.Close()
	dummy := gocv.NewMat()
	defer dummy.Close()
	otsuVal := gocv.Threshold(norm, &dummy, 0, 255, gocv.ThresholdBinary|gocv.ThresholdOtsu)
	gocv.Threshold(norm, &binary, otsuVal*0.60, 255, gocv.ThresholdBinary)

	// 6. Morphological Open (1x to clean noise)
	kernelOpen := gocv.GetStructuringElement(gocv.MorphRect, image.Point{X: 3, Y: 3})
	defer kernelOpen.Close()
	gocv.MorphologyEx(binary, &binary, gocv.MorphOpen, kernelOpen)

	// 6.5 Morphological Close (5x5 ellipse) to fill hollow digit centers on victory screens
	kernelClose := gocv.GetStructuringElement(gocv.MorphEllipse, image.Point{X: 5, Y: 5})
	defer kernelClose.Close()
	gocv.MorphologyEx(binary, &binary, gocv.MorphClose, kernelClose)

	// 7. Invert colors if background is light (mostly white pixels)
	nonZero := gocv.CountNonZero(binary)
	total := binary.Rows() * binary.Cols()
	if float64(nonZero)/float64(total) > 0.5 {
		gocv.BitwiseNot(binary, &binary)
	}

	bestVal, bestScore := 0, -1.0
	roiCenterY := scaled.Rows() / 2

	contours := gocv.FindContours(binary, gocv.RetrievalExternal, gocv.ChainApproxSimple)
	var detected []detectedDigit
	for i := 0; i < contours.Size(); i++ {
		rect := gocv.BoundingRect(contours.At(i))
		minH := int(7*lr.cal.ScaleY) * 5
		maxH := int(40*lr.cal.ScaleY) * 5
		minW := int(1*lr.cal.ScaleX) * 5
		maxW := int(40*lr.cal.ScaleX) * 5

		if rect.Dy() < minH || rect.Dy() > maxH || rect.Dx() < minW || rect.Dx() > maxW {
			continue
		}

		// Vertical alignment check
		blobCenterY := rect.Min.Y + rect.Dy()/2
		if math.Abs(float64(blobCenterY-roiCenterY)) > float64(scaled.Rows())/2.0 {
			continue
		}

		// Color Filter: Digits are strictly white/grey (Low Saturation)
		// Sample from the original HSV region by scaling coordinates back down 5x
		origRect := image.Rect(rect.Min.X/5, rect.Min.Y/5, rect.Max.X/5, rect.Max.Y/5)
		origRect = lr.safeRect(sub, origRect)
		if !origRect.Empty() {
			blobHSV := hsv.Region(origRect)
			mean := blobHSV.Mean()
			blobHSV.Close()

			// Saturation check is our primary icon-rejection tool.
			// White text Saturation is usually < 40. Icons are > 100.
			// Increased to 105 to tolerate colorful/grass background bleeding into transparent text regions.
			if mean.Val2 > 105 {
				continue
			}
		}

		blob := binary.Region(rect)
		d := lr.matchDigit(blob)
		blob.Close()
		if d.digit >= 0 {
			d.rect = image.Rect(rect.Min.X/5, rect.Min.Y/5, rect.Max.X/5, rect.Max.Y/5)
			detected = append(detected, d)
		}
	}
	contours.Close()

	if len(detected) > 0 {
		sort.Slice(detected, func(i, j int) bool { return detected[i].rect.Min.X < detected[j].rect.Min.X })

		// Deduplicate overlaps
		cleaned := []detectedDigit{}
		for _, d := range detected {
			found := false
			for i, c := range cleaned {
				if d.rect.Min.X >= c.rect.Min.X-2 && d.rect.Min.X <= c.rect.Min.X+2 {
					found = true
					if d.conf > c.conf {
						cleaned[i] = d
					}
					break
				}
			}
			if !found {
				cleaned = append(cleaned, d)
			}
		}

		// Cluster Detection: Find the group of digits with small gaps
		var clusters [][]detectedDigit
		if len(cleaned) > 0 {
			current := []detectedDigit{cleaned[0]}
			maxGap := int(80 * lr.cal.ScaleX)
			for i := 1; i < len(cleaned); i++ {
				gap := cleaned[i].rect.Min.X - cleaned[i-1].rect.Max.X
				if gap <= maxGap {
					current = append(current, cleaned[i])
				} else {
					clusters = append(clusters, current)
					current = []detectedDigit{cleaned[i]}
				}
			}
			clusters = append(clusters, current)
		}

		// Select best cluster (most digits)
		var bestCluster []detectedDigit
		for _, c := range clusters {
			if len(c) > len(bestCluster) {
				bestCluster = c
			} else if len(c) == len(bestCluster) && len(c) > 0 {
				// Tie-breaker: prefer the more LEFT cluster (loot digits start immediately)
				if bestCluster == nil || c[0].rect.Min.X < bestCluster[0].rect.Min.X {
					bestCluster = c
				}
			}
		}
		cleaned = bestCluster

		score := float64(len(cleaned)*len(cleaned)) * 100.0
		s := ""
		details := ""
		for _, d := range cleaned {
			// Get mean intensity for logging
			origRect := lr.safeRect(gray, d.rect)
			if !origRect.Empty() {
				blobGray := gray.Region(origRect)
				mean := blobGray.Mean()
				blobGray.Close()
				s += strconv.Itoa(d.digit)
				details += fmt.Sprintf("[%d@%d-%d m%.0f]", d.digit, d.rect.Min.X, d.rect.Max.X, mean.Val1)
			}
		}
		if lr.Debug {
			lr.logger.Debug().Str("digits", s).Str("pos", details).Msg("row OCR pass")
		}
		if score > bestScore {
			val, _ := strconv.Atoi(s)
			if val < 100000000 {
				bestVal = val
				bestScore = score
			}
		}
	}
	return bestVal
}

func (lr *LootRecognizer) matchDigit(bin gocv.Mat) detectedDigit {
	bestDigit, maxConf := -1, float32(0.0)
	bw, bh := bin.Cols(), bin.Rows()
	for i, tpl := range lr.digitTemplates {
		if tpl.Empty() {
			continue
		}
		scaledTpl := gocv.NewMat()
		gocv.Resize(tpl, &scaledTpl, image.Point{X: bw, Y: bh}, 0, 0, gocv.InterpolationLinear)
		res := gocv.NewMat()
		mask := gocv.NewMat()
		gocv.MatchTemplate(bin, scaledTpl, &res, gocv.TmCcoeffNormed, mask)
		mask.Close()
		_, conf, _, _ := gocv.MinMaxLoc(res)
		if float32(conf) > maxConf {
			maxConf = float32(conf)
			bestDigit = i
		}
		res.Close()
		scaledTpl.Close()
	}

	// Thin vertical blobs are almost always '1'
	if bestDigit == -1 || maxConf < 0.55 {
		minH1 := int(12 * lr.cal.ScaleY)
		maxW1 := int(6 * lr.cal.ScaleX)
		if bw >= 1 && bw <= maxW1 && bh >= minH1 { // Narrower and taller
			fill := float64(gocv.CountNonZero(bin)) / float64(bw*bh)
			if fill > 0.65 {
				return detectedDigit{digit: 1, conf: 0.6}
			}
		}
	}

	if maxConf < 0.5 {
		return detectedDigit{digit: -1}
	}
	return detectedDigit{digit: bestDigit, conf: maxConf}
}

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
