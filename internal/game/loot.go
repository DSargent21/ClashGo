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

	"github.com/rs/zerolog"
	"github.com/Ducky705/ClashGO/internal/paths"
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
		if !ok || tpl.Empty() { continue }
		gray := gocv.NewMat()
		if tpl.Channels() == 3 { gocv.CvtColor(tpl, &gray, gocv.ColorBGRToGray) } else { tpl.CopyTo(&gray) }
		bin := gocv.NewMat()
		gocv.Threshold(gray, &bin, 128, 255, gocv.ThresholdBinary)
		rect := tightBoundingBox(bin)
		if !rect.Empty() {
			tight := bin.Region(rect); lr.digitTemplates[i] = tight.Clone(); tight.Close()
		} else { lr.digitTemplates[i] = bin.Clone() }
		bin.Close(); gray.Close()
	}
}

func (lr *LootRecognizer) Close() {
	for _, tpl := range lr.digitTemplates { if !tpl.Empty() { tpl.Close() } }
}

type LootReport struct { Resources Resources }

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

func (lr *LootRecognizer) ReadBattleResult(screen gocv.Mat) (BattleResult, error) {
	gray := gocv.NewMat()
	gocv.CvtColor(screen, &gray, gocv.ColorBGRToGray)
	defer gray.Close()

	var result BattleResult

	// Battle Loot (Center column)
	battleRois := []struct {
		name           string
		x1, y1, x2, y2 int
	}{
		{"gold", 50, 318, 450, 345},
		{"elixir", 50, 357, 450, 385},
		{"de", 100, 395, 450, 420},
	}
	battleSearch := image.Rect(
		int(20*lr.cal.ScaleX), int(200*lr.cal.ScaleY), // Expanded Y
		int(480*lr.cal.ScaleX), int(550*lr.cal.ScaleY),
	)

	// Bonus Loot (Right column box)
	bonusRois := []struct {
		name           string
		x1, y1, x2, y2 int
	}{
		{"gold", 520, 368, 720, 387},
		{"elixir", 520, 401, 720, 420},
		{"de", 550, 432, 720, 450},
	}
	bonusSearch := image.Rect(
		int(500*lr.cal.ScaleX), int(200*lr.cal.ScaleY), // Expanded Y
		int(800*lr.cal.ScaleX), int(600*lr.cal.ScaleY), // Expanded X/Y
	)

	// Load custom ROIs if they exist
	if data, err := os.ReadFile(paths.Resolve("battle_loot_rois.json")); err == nil {
		var custom struct {
			BattleSearch struct{ X1, Y1, X2, Y2 int } `json:"battleSearch"`
			BonusSearch  struct{ X1, Y1, X2, Y2 int } `json:"bonusSearch"`
		}
		if json.Unmarshal(data, &custom) == nil {
			battleSearch = image.Rect(
				int(float64(custom.BattleSearch.X1)*lr.cal.ScaleX),
				int(float64(custom.BattleSearch.Y1)*lr.cal.ScaleY),
				int(float64(custom.BattleSearch.X2)*lr.cal.ScaleX),
				int(float64(custom.BattleSearch.Y2)*lr.cal.ScaleY),
			)
			bonusSearch = image.Rect(
				int(float64(custom.BonusSearch.X1)*lr.cal.ScaleX),
				int(float64(custom.BonusSearch.Y1)*lr.cal.ScaleY),
				int(float64(custom.BonusSearch.X2)*lr.cal.ScaleX),
				int(float64(custom.BonusSearch.Y2)*lr.cal.ScaleY),
			)
			lr.logger.Info().Msg("Loaded custom battle loot ROIs")
		}
	}

	result.Loot = lr.readLootColumn(screen, gray, battleSearch, battleRois, 0.65)
	// Force fallback row reading directly for the Bonus column by using 1.1 threshold.
	// This makes it completely immune to poor icon contrast or shifted graphics.
	result.Bonus = lr.readLootColumn(screen, gray, bonusSearch, bonusRois, 1.1)

	// Star Detection: Use brightness at 3 specific points (Left, Middle, Right stars).
	// Filled stars (yellow/gold/silver) are bright; empty stars are dark.
	starPoints := []image.Point{
		{X: 365, Y: 220}, // Left
		{X: 430, Y: 190}, // Middle
		{X: 495, Y: 220}, // Right
	}

	// Load custom star points if they exist
	if data, err := os.ReadFile(paths.Resolve("star_points.json")); err == nil {
		var custom struct {
			Stars []struct{ X, Y int } `json:"stars"`
		}
		if json.Unmarshal(data, &custom) == nil && len(custom.Stars) == 3 {
			for i := 0; i < 3; i++ {
				starPoints[i] = image.Pt(custom.Stars[i].X, custom.Stars[i].Y)
			}
			lr.logger.Info().Msg("Loaded custom star points")
		}
	}

	validStars := 0
	for _, pt := range starPoints {
		// Scale to current resolution
		sx := int(float64(pt.X) * lr.cal.ScaleX)
		sy := int(float64(pt.Y) * lr.cal.ScaleY)

		// Define a tiny 5x5 ROI around the point
		rect := image.Rect(sx-2, sy-2, sx+3, sy+3)
		rect = lr.safeRect(screen, rect)
		if rect.Empty() {
			continue
		}

		sub := gray.Region(rect)
		mean := sub.Mean().Val1
		sub.Close()

		// A filled star center is bright (>100), an empty one is dark.
		if mean > 100 {
			validStars++
		}
	}
	result.Stars = validStars
	if result.Stars > 3 {
		result.Stars = 3
	}

	return result, nil
}

func (lr *LootRecognizer) safeRect(img gocv.Mat, r image.Rectangle) image.Rectangle {
	if r.Min.X < 0 { r.Min.X = 0 }
	if r.Min.Y < 0 { r.Min.Y = 0 }
	if r.Max.X > img.Cols() { r.Max.X = img.Cols() }
	if r.Max.Y > img.Rows() { r.Max.Y = img.Rows() }
	if r.Max.X < r.Min.X { r.Max.X = r.Min.X }
	if r.Max.Y < r.Min.Y { r.Max.Y = r.Min.Y }
	return r
}

func (lr *LootRecognizer) readLootColumn(screen, gray gocv.Mat, searchRoi image.Rectangle, fallbacks []struct {
	name           string
	x1, y1, x2, y2 int
}, minConfThreshold float32) Resources {
	searchRoi = lr.safeRect(screen, searchRoi)
	if searchRoi.Empty() { return Resources{} }

	region := screen.Region(searchRoi)
	defer region.Close()

	var results [3]int
	iconNames := []string{"icon_gold", "icon_elixir", "icon_de"}

	for i, name := range iconNames {
		tpl, ok := lr.templates.Get(name)
		if ok && !tpl.Empty() {
			res := gocv.NewMat()
			gocv.MatchTemplate(region, tpl, &res, gocv.TmCcoeffNormed, gocv.NewMat())
			_, maxConf, _, maxLoc := gocv.MinMaxLoc(res)
			res.Close()

			if maxConf > minConfThreshold {
				// Robust anchor: start slightly inside the icon to catch digits immediately after.
				// readRow uses color/saturation filtering to ignore the actual icon pixels.
				rect := image.Rect(
					maxLoc.X+int(4*lr.cal.ScaleX),
					maxLoc.Y-int(5*lr.cal.ScaleY),
					maxLoc.X+int(350*lr.cal.ScaleX), // Large enough to cover all digits
					maxLoc.Y+tpl.Rows()+int(5*lr.cal.ScaleY),
				)
				results[i] = lr.readRow(region, rect)
				continue
			}
		}
		f := fallbacks[i]
		rect := image.Rect(
			int(float64(f.x1)*lr.cal.ScaleX)-searchRoi.Min.X,
			int(float64(f.y1)*lr.cal.ScaleY)-searchRoi.Min.Y,
			int(float64(f.x2)*lr.cal.ScaleX)-searchRoi.Min.X,
			int(float64(f.y2)*lr.cal.ScaleY)-searchRoi.Min.Y,
		)
		results[i] = lr.readRow(region, rect)
	}
	return Resources{Gold: results[0], Elixir: results[1], DarkElixir: results[2]}
}

func (lr *LootRecognizer) ReadLootDetailed(screen gocv.Mat) (LootReport, error) {
	// High-Precision ROIs for Scouting (Reference 860x732)
	// We use very inclusive X range (starting at 40) because digits start immediately after icons
	icons := []struct { name, tpl string; y1, y2 int }{
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
	if roi.Empty() { return 0 }

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
		minH := int(7 * lr.cal.ScaleY) * 5
		maxH := int(40 * lr.cal.ScaleY) * 5
		minW := int(1 * lr.cal.ScaleX) * 5
		maxW := int(40 * lr.cal.ScaleX) * 5
		
		if rect.Dy() < minH || rect.Dy() > maxH || rect.Dx() < minW || rect.Dx() > maxW { continue }
		
		// Vertical alignment check
		blobCenterY := rect.Min.Y + rect.Dy()/2
		if math.Abs(float64(blobCenterY-roiCenterY)) > float64(scaled.Rows())/2.0 { continue }

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
			if mean.Val2 > 105 { continue } 
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
					if d.conf > c.conf { cleaned[i] = d }
					break
				}
			}
			if !found { cleaned = append(cleaned, d) }
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
			if val < 100000000 { bestVal = val; bestScore = score }
		}
	}
	return bestVal
}


func (lr *LootRecognizer) matchDigit(bin gocv.Mat) detectedDigit {
	bestDigit, maxConf := -1, float32(0.0)
	bw, bh := bin.Cols(), bin.Rows()
	for i, tpl := range lr.digitTemplates {
		if tpl.Empty() { continue }
		scaledTpl := gocv.NewMat()
		gocv.Resize(tpl, &scaledTpl, image.Point{X: bw, Y: bh}, 0, 0, gocv.InterpolationLinear)
		res := gocv.NewMat()
		mask := gocv.NewMat()
		gocv.MatchTemplate(bin, scaledTpl, &res, gocv.TmCcoeffNormed, mask)
		mask.Close()
		_, conf, _, _ := gocv.MinMaxLoc(res)
		if float32(conf) > maxConf { maxConf = float32(conf); bestDigit = i }
		res.Close(); scaledTpl.Close()
	}
	
	// Thin vertical blobs are almost always '1'
	if bestDigit == -1 || maxConf < 0.55 {
		minH1 := int(12 * lr.cal.ScaleY)
		maxW1 := int(6 * lr.cal.ScaleX)
		if bw >= 1 && bw <= maxW1 && bh >= minH1 { // Narrower and taller
			fill := float64(gocv.CountNonZero(bin)) / float64(bw*bh)
			if fill > 0.65 { return detectedDigit{digit: 1, conf: 0.6} }
		}
	}

	if maxConf < 0.5 { return detectedDigit{digit: -1} }
	return detectedDigit{digit: bestDigit, conf: maxConf}
}

func tightBoundingBox(bin gocv.Mat) image.Rectangle {
	rows, cols := bin.Rows(), bin.Cols()
	xMin, xMax, yMin, yMax := cols, 0, rows, 0
	found := false
	for y := 0; y < rows; y++ {
		for x := 0; x < cols; x++ {
			if bin.GetUCharAt(y, x) > 128 {
				if x < xMin { xMin = x }; if x > xMax { xMax = x }
				if y < yMin { yMin = y }; if y > yMax { yMax = y }
				found = true
			}
		}
	}
	if !found { return image.Rectangle{} }
	return image.Rect(xMin, yMin, xMax+1, yMax+1)
}
