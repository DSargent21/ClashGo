package game

import (
	"fmt"
	"image"
	"math"
	"sort"
	"strconv"
	"sync"

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
		int(20*lr.cal.ScaleX), int(300*lr.cal.ScaleY),
		int(480*lr.cal.ScaleX), int(450*lr.cal.ScaleY),
	)
	result.Loot = lr.readLootColumn(screen, gray, battleSearch, battleRois)

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
		int(500*lr.cal.ScaleX), int(350*lr.cal.ScaleY),
		int(780*lr.cal.ScaleX), int(500*lr.cal.ScaleY),
	)
	result.Bonus = lr.readLootColumn(screen, gray, bonusSearch, bonusRois)

	// Star Detection (Search for yellow pixel clusters in the center-top results area)
	isResultsScreen := result.Loot.Gold > 0 || result.Loot.Elixir > 0 || result.Loot.DarkElixir > 0 ||
		result.Bonus.Gold > 0 || result.Bonus.Elixir > 0 || result.Bonus.DarkElixir > 0
	
	if isResultsScreen {
		yellowPixels := []image.Point{}
		startY, endY := int(140*lr.cal.ScaleY), int(260*lr.cal.ScaleY)
		startX, endX := int(280*lr.cal.ScaleX), int(580*lr.cal.ScaleX)
		
		for y := startY; y < endY; y++ {
			for x := startX; x < endX; x++ {
				b := screen.GetUCharAt(y, x*3)
				g := screen.GetUCharAt(y, x*3+1)
				r := screen.GetUCharAt(y, x*3+2)
				// Ultra-strict yellow for active stars
				if r > 240 && g > 210 && b < 120 && r > b+100 {
					yellowPixels = append(yellowPixels, image.Pt(x, y))
				}
			}
		}

		if len(yellowPixels) > 0 {
			clusters := [][]image.Point{}
			clusterDist := 60 * lr.cal.ScaleX
			for _, p := range yellowPixels {
				found := false
				for i, c := range clusters {
					dist := math.Sqrt(math.Pow(float64(p.X-c[0].X), 2) + math.Pow(float64(p.Y-c[0].Y), 2))
					if dist < clusterDist {
						clusters[i] = append(clusters[i], p); found = true; break
					}
				}
				if !found { clusters = append(clusters, []image.Point{p}) }
			}
			
			validClusters := 0
			for _, c := range clusters {
				if len(c) > 50 { validClusters++ }
			}
			result.Stars = validClusters
			if result.Stars > 3 { result.Stars = 3 }
		}
	}

	return result, nil
}

func (lr *LootRecognizer) readLootColumn(screen, gray gocv.Mat, searchRoi image.Rectangle, fallbacks []struct {
	name           string
	x1, y1, x2, y2 int
}) Resources {
	if searchRoi.Min.X < 0 { searchRoi.Min.X = 0 }
	if searchRoi.Min.Y < 0 { searchRoi.Min.Y = 0 }
	if searchRoi.Max.X > screen.Cols() { searchRoi.Max.X = screen.Cols() }
	if searchRoi.Max.Y > screen.Rows() { searchRoi.Max.Y = screen.Rows() }

	region := screen.Region(searchRoi)
	defer region.Close()
	grayReg := gray.Region(searchRoi)
	defer grayReg.Close()

	var results [3]int
	iconNames := []string{"icon_gold", "icon_elixir", "icon_de"}

	for i, name := range iconNames {
		tpl, ok := lr.templates.Get(name)
		if ok && !tpl.Empty() {
			res := gocv.NewMat()
			gocv.MatchTemplate(region, tpl, &res, gocv.TmCcoeffNormed, gocv.NewMat())
			_, maxConf, _, maxLoc := gocv.MinMaxLoc(res)
			res.Close()

			if maxConf > 0.7 {
				rect := image.Rect(maxLoc.X+tpl.Cols()+2, maxLoc.Y-2, maxLoc.X+tpl.Cols()+200, maxLoc.Y+tpl.Rows()+2)
				results[i] = lr.readRow(grayReg, rect)
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
		results[i] = lr.readRow(grayReg, rect)
	}
	return Resources{Gold: results[0], Elixir: results[1], DarkElixir: results[2]}
}

func (lr *LootRecognizer) ReadLootDetailed(screen gocv.Mat) (LootReport, error) {
	gray := gocv.NewMat()
	gocv.CvtColor(screen, &gray, gocv.ColorBGRToGray)
	defer gray.Close()

	icons := []struct { name, tpl string; y1, y2 int }{
		{"gold", "icon_gold", 72, 94},
		{"elixir", "icon_elixir", 101, 122},
		{"de", "icon_de", 130, 151},
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
				rect := image.Rect(maxLoc.X+54, maxLoc.Y-5, maxLoc.X+420, maxLoc.Y+tpl.Rows()+5)
				results[i] = lr.readRow(gray, rect)
				continue
			}
		}
		rect := image.Rect(int(54*lr.cal.ScaleX), int(float64(ic.y1-2)*lr.cal.ScaleY), int(420*lr.cal.ScaleX), int(float64(ic.y2+2)*lr.cal.ScaleY))
		results[i] = lr.readRow(gray, rect)
	}

	return LootReport{Resources: Resources{Gold: results[0], Elixir: results[1], DarkElixir: results[2]}}, nil
}

func (lr *LootRecognizer) readRow(gray gocv.Mat, roi image.Rectangle) int {
	if roi.Min.X < 0 { roi.Min.X = 0 }
	if roi.Min.Y < 0 { roi.Min.Y = 0 }
	if roi.Max.X > gray.Cols() { roi.Max.X = gray.Cols() }
	if roi.Max.Y > gray.Rows() { roi.Max.Y = gray.Rows() }
	if roi.Empty() { return 0 }

	region := gray.Region(roi)
	defer region.Close()

	bestVal, bestScore := 0, -1.0
	for _, tVal := range []float32{150, 175, 200} {
		thresh := gocv.NewMat()
		gocv.Threshold(region, &thresh, tVal, 255, gocv.ThresholdBinary)
		
		contours := gocv.FindContours(thresh, gocv.RetrievalExternal, gocv.ChainApproxSimple)
		var detected []detectedDigit
		for i := 0; i < contours.Size(); i++ {
			rect := gocv.BoundingRect(contours.At(i))
			if rect.Dy() < 8 || rect.Dy() > 30 || rect.Dx() < 1 || rect.Dx() > 35 { continue }
			
			blob := thresh.Region(rect)
			d := lr.matchDigit(blob)
			blob.Close()
			if d.digit >= 0 { d.rect = rect; detected = append(detected, d) }
		}
		contours.Close(); thresh.Close()

		if len(detected) > 0 {
			sort.Slice(detected, func(i, j int) bool { return detected[i].rect.Min.X < detected[j].rect.Min.X })
			cleaned := []detectedDigit{}
			for _, d := range detected {
				// Safety: Ignore blobs that are too far to the left (likely icon bleed)
				if d.rect.Min.X < 3 { continue }

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

			// Final sanity check: if the first digit is a '1' and is significantly 
			// separated from the next digit, it might be noise.
			if len(cleaned) > 1 {
				dist := cleaned[1].rect.Min.X - cleaned[0].rect.Max.X
				if cleaned[0].digit == 1 && dist > 15 {
					cleaned = cleaned[1:]
				}
			}

			// Trailing noise filter: if the last digit is a '1' and is significantly
			// separated from the previous digit, it might be noise/icon bleed.
			if len(cleaned) > 1 {
				lastIdx := len(cleaned) - 1
				dist := cleaned[lastIdx].rect.Min.X - cleaned[lastIdx-1].rect.Max.X
				if cleaned[lastIdx].digit == 1 && dist > 15 {
					cleaned = cleaned[:lastIdx]
				}
			}

			score := float64(len(cleaned)*len(cleaned)) * 100.0
			s := ""
			for _, d := range cleaned { s += strconv.Itoa(d.digit) }
			if score > bestScore {
				val, _ := strconv.Atoi(s)
				if val < 5000000 { bestVal = val; bestScore = score }
			}
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
		gocv.MatchTemplate(bin, scaledTpl, &res, gocv.TmCcoeffNormed, gocv.NewMat())
		_, conf, _, _ := gocv.MinMaxLoc(res)
		if float32(conf) > maxConf { maxConf = float32(conf); bestDigit = i }
		res.Close(); scaledTpl.Close()
	}
	
	// Thin vertical blobs are almost always '1'
	if bestDigit == -1 || maxConf < 0.55 {
		if bw >= 1 && bw <= 6 && bh >= 12 { // Narrower and taller
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
