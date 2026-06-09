package main

import (
	"fmt"
	"image"
	"image/color"
	"math"
	"os"
	"strings"

	"github.com/Ducky705/ClashGO/internal/adb"
	"github.com/Ducky705/ClashGO/internal/game"
	"github.com/Ducky705/ClashGO/internal/vision"
	"github.com/Ducky705/ClashGO/pkg/strategy"
	"gocv.io/x/gocv"
)

type mockClient struct {
	*adb.Client
	taps []image.Point
}

func (m *mockClient) TapFast(x, y int, stdDev float64) error {
	m.taps = append(m.taps, image.Pt(x, y))
	return nil
}

func (m *mockClient) Tap(x, y int) error {
	m.taps = append(m.taps, image.Pt(x, y))
	return nil
}

func (m *mockClient) HumanSleep(base, stddev int) {}

type ManualEdge struct {
	P1 image.Point `json:"p1"`
	P2 image.Point `json:"p2"`
}

type PrecisionConfig struct {
	Edges        map[string]ManualEdge   `json:"edges"`
	SpellEdgesA  map[string]ManualEdge   `json:"spell_edges_a"`
	SpellEdgesB  map[string]ManualEdge   `json:"spell_edges_b"`
	HeroTargets  map[string]image.Point  `json:"hero_targets"`
	BarY         int                    `json:"bar_y"`
	Width        int                    `json:"width"`
	Height       int                    `json:"height"`
}

func main() {
	imgPath := "screen_20260515_215234.png"
	if len(os.Args) > 1 {
		imgPath = os.Args[1]
	}

	fmt.Printf("Loading test image: %s\n", imgPath)
	screen := gocv.IMRead(imgPath, gocv.IMReadColor)
	if screen.Empty() {
		fmt.Printf("Failed to load %s\n", imgPath)
		return
	}
	defer screen.Close()

	w, h := screen.Cols(), screen.Rows()
	debugImg := screen.Clone()
	defer debugImg.Close()

	// Parse strategy
	stratPath := "assets/strategies/auto_edrag_rush.yaml"
	s, err := strategy.ParseYAML(stratPath)
	if err != nil {
		fmt.Printf("Failed to load strategy: %v\n", err)
		return
	}
	fmt.Printf("Strategy: %s, Edge: %s\n", s.Name, s.TargetEdge)

	// Mock calibration
	cal := &game.Calibration{
		PhysicalW: w,
		PhysicalH: h,
		ScaleX:    float64(w) / 860.0,
		ScaleY:    float64(h) / 732.0,
	}

	templates, err := game.NewTemplateStore("assets/templates")
	if err != nil {
		fmt.Println("No template store")
		return
	}
	templates.LoadTemplates()

	// Default layout parameters
	mBarY := int(float64(h) * 0.78)
	targetEdge := "TopRight" // Fixed for simulation

	// Precision config (mocking edges)
	pCfg := PrecisionConfig{
		Edges: make(map[string]ManualEdge),
		SpellEdgesA: make(map[string]ManualEdge),
		SpellEdgesB: make(map[string]ManualEdge),
		HeroTargets: make(map[string]image.Point),
	}
	// Setup mock edges for TopRight
	pCfg.Edges[targetEdge] = ManualEdge{
		P1: image.Pt(w/2, 50),
		P2: image.Pt(w-50, h/2),
	}
	pCfg.SpellEdgesA[targetEdge] = ManualEdge{
		P1: image.Pt(w/2+50, 100),
		P2: image.Pt(w-100, h/2+50),
	}
	pCfg.SpellEdgesB[targetEdge] = ManualEdge{
		P1: image.Pt(w/2+100, 150),
		P2: image.Pt(w-150, h/2+100),
	}

	globalUsedSlots := make(map[int]bool)

	// Simulate template matching for strategy
	fmt.Println("\n--- Phase 1: Deploying Strategy Units ---")
	for _, phase := range s.Phases {
		fmt.Printf("Phase: %s\n", phase.Name)
		for _, unit := range phase.Units {
			unitName := strings.ToLower(strings.TrimSpace(unit.Name))
			fileName := strings.ReplaceAll(unitName, " ", "_")
			tPath := fmt.Sprintf("assets/templates/attack/%s.png", fileName)
			tpl := gocv.IMRead(tPath, gocv.IMReadColor)
			if tpl.Empty() {
				fmt.Printf("  Unit %s: template missing\n", unit.Name)
				continue
			}
			defer tpl.Close()

			barROI := image.Rect(0, mBarY, w, h)
			matches, _ := vision.MatchMultiScaleROI(screen, tpl, 0.2, 1.2, 20, 0.6, barROI)
			if len(matches) == 0 {
				fmt.Printf("  Unit %s: NOT FOUND via templates (level mismatch or missing)\n", unit.Name)
				continue
			}

			// Find best match that is not already used
			var match *vision.Match
			for _, m := range matches {
				used := false
				for ux := range globalUsedSlots {
					if math.Abs(float64(m.Point.X-ux)) < float64(w)*0.05 {
						used = true
						break
					}
				}
				if !used {
					match = &m
					break
				}
			}

			if match == nil {
				fmt.Printf("  Unit %s: slot already processed\n", unit.Name)
				continue
			}

			globalUsedSlots[match.Point.X] = true
			fmt.Printf("  Unit %s: Found slot at X=%d (Conf: %.2f) -> Deployed using Strategy edge\n", unit.Name, match.Point.X, match.Confidence)
			
			// Draw green circle on template match slots
			gocv.Circle(&debugImg, match.Point, 15, color.RGBA{0, 255, 0, 255}, 2)
			gocv.PutText(&debugImg, unit.Name, match.Point.Add(image.Pt(10, -10)), gocv.FontHersheySimplex, 0.4, color.RGBA{0, 255, 0, 255}, 1)
		}
	}

	// Simulate Sweep
	fmt.Println("\n--- Phase 2: Sweeping Remaining Active Slots ---")
	slotY := mBarY + (h-mBarY)/2
	step := int(75.0 * cal.ScaleX)
	startX := int(40.0 * cal.ScaleX)

	// Since we mock isSlotEmpty, we can just find any non-grey pixel cluster in the bar,
	// or check if there is an icon. For simulation, let's scan the Y-level in the image.
	// We'll consider a slot active if it has significant color variance (standard deviation > 15)
	for x := startX; x < w-20; x += step {
		// Check globalUsedSlots
		alreadyUsed := false
		for ux := range globalUsedSlots {
			if math.Abs(float64(x-ux)) < float64(w)*0.04 {
				alreadyUsed = true
				break
			}
		}
		if alreadyUsed {
			continue
		}

		// Calculate slot region
		region := image.Rect(x-15, slotY-15, x+15, slotY+15)
		if region.Min.X < 0 { region.Min.X = 0 }
		if region.Max.X > w { region.Max.X = w }
		sub := screen.Region(region)
		
		// Check if it's empty (mostly grey/blue background)
		hsv := gocv.NewMat()
		gocv.CvtColor(sub, &hsv, gocv.ColorBGRToHSV)
		lower := gocv.NewScalar(90, 20, 30, 0)
		upper := gocv.NewScalar(130, 120, 100, 0)
		mask := gocv.NewMat()
		gocv.InRangeWithScalar(hsv, lower, upper, &mask)
		
		ratio := float64(gocv.CountNonZero(mask)) / float64(sub.Rows()*sub.Cols())
		hsv.Close()
		mask.Close()
		sub.Close()

		if ratio <= 0.7 { // Not empty -> active slot!
			fmt.Printf("  Active slot found at X=%d (Empty ratio: %.2f) -> Deployed using Sweep fallback\n", x, ratio)
			// Draw yellow circle on swept slots
			gocv.Circle(&debugImg, image.Pt(x, slotY), 15, color.RGBA{0, 255, 255, 255}, 2)
			gocv.PutText(&debugImg, "SWEEP", image.Pt(x-15, slotY-20), gocv.FontHersheySimplex, 0.4, color.RGBA{0, 255, 255, 255}, 1)
		}
	}

	// Save simulation result
	gocv.IMWrite("deploy_simulation.png", debugImg)
	fmt.Println("\nSaved deploy_simulation.png. Open it to verify slot selection.")
}
