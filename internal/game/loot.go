package game

import (
	"fmt"
	"image"
	"sort"
	"strconv"
	"sync"

	"github.com/diegosargent/coc-bot/internal/vision"
	"gocv.io/x/gocv"
)

type LootRecognizer struct {
	cal            *Calibration
	templates      *TemplateStore
	digitTemplates []gocv.Mat
	mu             sync.Mutex
	Debug          bool
}

type detectedBlob struct {
	rect  image.Rectangle
	digit int
	conf  float32
}

func NewLootRecognizer(cal *Calibration, ts *TemplateStore) *LootRecognizer {
	return NewLootRecognizerWithDebug(cal, ts, false)
}

func NewLootRecognizerWithDebug(cal *Calibration, ts *TemplateStore, debug bool) *LootRecognizer {
	lr := &LootRecognizer{
		cal:       cal,
		templates: ts,
		Debug:     debug,
	}
	lr.prepareDigitTemplates()
	return lr
}

func (lr *LootRecognizer) prepareDigitTemplates() {
	lr.digitTemplates = make([]gocv.Mat, 10)
	const stdW, stdH = 16, 24
	loadedCount := 0

	for i := 0; i < 10; i++ {
		name := fmt.Sprintf("digit_%d", i)
		tpl, ok := lr.templates.Get(name)
		if !ok {
			// Try subdirectories if not found in root (due to recursive loading)
			for _, sub := range []string{"Gold/", "Elixir/", "DE/"} {
				if t, ok2 := lr.templates.Get(sub + name); ok2 {
					tpl = t
					ok = true
					break
				}
			}
		}
		if !ok { continue }

		tplGray := gocv.NewMat()
		if tpl.Channels() > 1 {
			gocv.CvtColor(tpl, &tplGray, gocv.ColorBGRToGray)
		} else {
			tpl.CopyTo(&tplGray)
		}

		// Cleanup: find the largest contour to crop tight to the digit
		// This handles templates captured with some background
		thresh := gocv.NewMat()
		gocv.Threshold(tplGray, &thresh, 128, 255, gocv.ThresholdBinary)
		
		contours := gocv.FindContours(thresh, gocv.RetrievalExternal, gocv.ChainApproxSimple)
		thresh.Close()

		if contours.Size() > 0 {
			bestIdx := 0
			maxArea := 0.0
			for j := 0; j < contours.Size(); j++ {
				area := gocv.ContourArea(contours.At(j))
				if area > maxArea {
					maxArea = area
					bestIdx = j
				}
			}
			bestRect := gocv.BoundingRect(contours.At(bestIdx))
			tight := tplGray.Region(bestRect)
			stdTpl := gocv.NewMat()
			gocv.Resize(tight, &stdTpl, image.Point{X: stdW, Y: stdH}, 0, 0, gocv.InterpolationCubic)
			tight.Close()
			
			// Binarize for fast matching
			gocv.Threshold(stdTpl, &stdTpl, 128, 255, gocv.ThresholdBinary)
			lr.digitTemplates[i] = stdTpl
		} else {
			stdTpl := gocv.NewMat()
			gocv.Resize(tplGray, &stdTpl, image.Point{X: stdW, Y: stdH}, 0, 0, gocv.InterpolationCubic)
			gocv.Threshold(stdTpl, &stdTpl, 128, 255, gocv.ThresholdBinary)
			lr.digitTemplates[i] = stdTpl
		}
		tplGray.Close()
		loadedCount++
	}
	if lr.Debug {
		fmt.Printf("  LootRecognizer: Loaded %d digit templates (binary mode)\n", loadedCount)
	}
}

func (lr *LootRecognizer) Close() {
	for _, tpl := range lr.digitTemplates {
		if !tpl.Empty() {
			tpl.Close()
		}
	}
}

func (lr *LootRecognizer) ReadAvailableLoot(screen gocv.Mat) (Resources, error) {
	var r Resources
	searchROI := image.Rect(0, 0, screen.Cols()/2, screen.Rows()*3/4)
	if searchROI.Max.X < 500 { searchROI.Max.X = 600 }

	// 1. Find anchors sequentially to share scale and enforce vertical order
	// This is very fast (single scale match for 2nd and 3rd icons)
	_, goldY, goldScale, goldTextROI := lr.findAnchorAndROI(screen, "icon_gold", searchROI, -1, -1.0)
	_, elixirY, _, elixirTextROI := lr.findAnchorAndROI(screen, "icon_elixir", searchROI, goldY, goldScale)
	_, _, _, deTextROI := lr.findAnchorAndROI(screen, "icon_de", searchROI, elixirY, goldScale)

	// 2. Perform OCR in parallel for all three resources
	var wg sync.WaitGroup
	var mu sync.Mutex
	
	ocrTask := func(roi image.Rectangle, targetH int, out *int) {
		defer wg.Done()
		if roi.Empty() { return }
		val := lr.readTextFromROI(screen, roi, targetH)
		mu.Lock()
		*out = val
		mu.Unlock()
	}

	wg.Add(3)
	// We use anchor height roughly as targetH (approx 24-40px depending on scale)
	stdH := 24
	if goldScale > 0 {
		stdH = int(32.0 * goldScale) 
	}

	go ocrTask(goldTextROI, stdH, &r.Gold)
	go ocrTask(elixirTextROI, stdH, &r.Elixir)
	go ocrTask(deTextROI, stdH, &r.DarkElixir)
	wg.Wait()

	return r, nil
}

func (lr *LootRecognizer) findAnchorAndROI(screen gocv.Mat, anchorName string, searchROI image.Rectangle, minHeight int, scaleHint float64) (int, int, float64, image.Rectangle) {
	anchor, ok := lr.templates.Get(anchorName)
	if !ok {
		return 0, -1, -1.0, image.Rectangle{}
	}

	// Narrow ROI based on minHeight if provided
	effectiveROI := searchROI
	if minHeight > 0 {
		effectiveROI.Min.Y = minHeight + 10 // At least 10px below previous icon
		if effectiveROI.Min.Y >= effectiveROI.Max.Y {
			return 0, -1, -1.0, image.Rectangle{}
		}
	}

	roi := screen.Region(effectiveROI)
	defer roi.Close()

	var matches []vision.Match
	var err error

	if scaleHint > 0 {
		// Fast path: use pinned scale with tight range
		matches, err = vision.MatchMultiScale(roi, anchor, scaleHint*0.99, scaleHint*1.01, 3, 0.45)
	} else {
		// Full search
		matches, err = vision.MatchMultiScale(roi, anchor, 0.7, 1.3, 12, 0.45)
	}

	if err != nil || len(matches) == 0 {
		return 0, -1, -1.0, image.Rectangle{}
	}

	sort.Slice(matches, func(i, j int) bool {
		return matches[i].Confidence > matches[j].Confidence
	})

	best := &matches[0]

	anchorW := int(float64(anchor.Cols()) * best.Scale)
	anchorH := int(float64(anchor.Rows()) * best.Scale)
	absPoint := best.Point.Add(effectiveROI.Min)

	// Define text ROI relative to the icon center
	textRect := image.Rect(
		absPoint.X + anchorW/2 + 2,
		absPoint.Y - anchorH/2 - 5,
		absPoint.X + anchorW/2 + (anchorW * 10),
		absPoint.Y + anchorH/2 + 5,
	)

	if textRect.Min.X < 0 { textRect.Min.X = 0 }
	if textRect.Min.Y < 0 { textRect.Min.Y = 0 }
	if textRect.Max.X > screen.Cols() { textRect.Max.X = screen.Cols() }
	if textRect.Max.Y > screen.Rows() { textRect.Max.Y = screen.Rows() }

	return 0, absPoint.Y, best.Scale, textRect
}

func (lr *LootRecognizer) readTextFromROI(screen gocv.Mat, textRect image.Rectangle, targetH int) int {
	textROI := screen.Region(textRect)
	defer textROI.Close()

	gray := gocv.NewMat()
	defer gray.Close()
	gocv.CvtColor(textROI, &gray, gocv.ColorBGRToGray)

	// Ensemble detection across multiple thresholds in parallel
	thresholds := []float32{150, 175, 200, 225}
	
	type blobGroup struct {
		x int
		votes map[int]int
	}
	groups := make([]*blobGroup, 0)
	var groupMu sync.Mutex

	var wg sync.WaitGroup
	wg.Add(len(thresholds))
	for _, t := range thresholds {
		go func(threshVal float32) {
			defer wg.Done()
			
			tMat := gocv.NewMat()
			defer tMat.Close()
			gocv.Threshold(gray, &tMat, threshVal, 255, gocv.ThresholdBinary)
			
			blobs := lr.recognizeBlobs(tMat, gray, targetH)
			
			groupMu.Lock()
			defer groupMu.Unlock()
			for _, b := range blobs {
				found := false
				for _, g := range groups {
					if absDiff(g.x, b.rect.Min.X) < 5 {
						g.votes[b.digit]++
						found = true
						break
					}
				}
				if !found {
					groups = append(groups, &blobGroup{
						x: b.rect.Min.X,
						votes: map[int]int{b.digit: 1},
					})
				}
			}
		}(t)
	}
	wg.Wait()

	// Reconstruct the number from groups sorted by X position
	sort.Slice(groups, func(i, j int) bool {
		return groups[i].x < groups[j].x
	})

	finalStr := ""
	for _, g := range groups {
		bestDigit := -1
		maxVotes := 0
		for digit, votes := range g.votes {
			if votes > maxVotes {
				maxVotes = votes
				bestDigit = digit
			}
		}
		// Require at least 2 thresholds to agree for high reliability
		if maxVotes >= 2 {
			finalStr += strconv.Itoa(bestDigit)
		}
	}

	val, _ := strconv.Atoi(finalStr)
	return val
}

func (lr *LootRecognizer) recognizeBlobs(thresh gocv.Mat, gray gocv.Mat, targetH int) []detectedBlob {
	contours := gocv.FindContours(thresh, gocv.RetrievalExternal, gocv.ChainApproxSimple)
	defer contours.Close()

	var detected []detectedBlob
	const stdW, stdH = 16, 24
	const totalPixels = float64(stdW * stdH)

	for i := 0; i < contours.Size(); i++ {
		rect := gocv.BoundingRect(contours.At(i))
		
		// Filter blobs by size relative to anchor height
		if rect.Dy() < targetH/3 || rect.Dy() > targetH*3 { continue }
		if rect.Dx() < 1 || rect.Dx() > targetH*2 { continue }
		
		// Extract grayscale blob for template matching
		blobGray := gray.Region(rect)
		stdBlob := gocv.NewMat()
		gocv.Resize(blobGray, &stdBlob, image.Point{X: stdW, Y: stdH}, 0, 0, gocv.InterpolationCubic)
		blobGray.Close()

		// Binarize the blob to match templates
		gocv.Threshold(stdBlob, &stdBlob, 128, 255, gocv.ThresholdBinary)

		bestDigit := -1
		maxConf := float32(-1.0)

		diff := gocv.NewMat()
		for j, tpl := range lr.digitTemplates {
			if tpl.Empty() { continue }
			
			// Fast L1 matching on binary images
			gocv.AbsDiff(stdBlob, tpl, &diff)
			sum := diff.Sum()
			// sum.Val1 is the sum of differences (0 or 255 per pixel)
			conf := 1.0 - float32(sum.Val1 / (totalPixels * 255.0))

			if conf > maxConf {
				maxConf = conf
				bestDigit = j
			}
			// Early exit for perfect match
			if maxConf > 0.98 { break }
		}
		diff.Close()
		stdBlob.Close()

		if bestDigit >= 0 && maxConf > 0.8 { // Higher threshold for binary matching
			detected = append(detected, detectedBlob{
				rect:  rect,
				digit: bestDigit,
				conf:  maxConf,
			})
		}
	}
	
	return detected
}
