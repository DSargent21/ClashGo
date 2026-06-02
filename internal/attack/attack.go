package attack

import (
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"math"
	"math/rand"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/Ducky705/ClashGo/internal/adb"
	"github.com/Ducky705/ClashGo/internal/config"
	"github.com/Ducky705/ClashGo/internal/game"
	"github.com/Ducky705/ClashGo/internal/vision"
	"github.com/Ducky705/ClashGo/pkg/strategy"
	"github.com/rs/zerolog"
	"gocv.io/x/gocv"
)

type Executor struct {
	client        *adb.Client
	cal           *game.Calibration
	cfg           *config.AttackConfig
	logger        zerolog.Logger
	classify      func(gocv.Mat) (game.GameState, int)
	tappedSiegeXs map[int]bool
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

type BaseCalibration struct {
	BaseTop      image.Point `json:"base_top"`
	BaseRight    image.Point `json:"base_right"`
	BaseBottom   image.Point `json:"base_bottom"`
	BaseLeft     image.Point `json:"base_left"`
	FieldTop     image.Point `json:"field_top"`
	FieldRight   image.Point `json:"field_right"`
	FieldBottom  image.Point `json:"field_bottom"`
	FieldLeft    image.Point `json:"field_left"`
	BarY         int         `json:"bar_y"`
	Width        int         `json:"width"`
	Height       int         `json:"height"`
}

type ManualEdge struct {
	P1 image.Point `json:"p1"`
	P2 image.Point `json:"p2"`
}

func NewExecutor(client *adb.Client, cal *game.Calibration, cfg *config.AttackConfig, logger zerolog.Logger) *Executor {
	// Create a default classifier if none provided or for internal use
	classifier := game.NewClassifier(cal, game.ClassifierConfig{
		ConfirmFrames:     1,
		TemplateThreshold: 0.7,
	}, logger)

	return &Executor{
		client:        client,
		cal:           cal,
		cfg:           cfg,
		logger:        logger.With().Str("component", "attack_executor").Logger(),
		classify:      classifier.ClassifyState,
		tappedSiegeXs: make(map[int]bool),
	}
}

func (e *Executor) SetClassifier(fn func(gocv.Mat) (game.GameState, int)) {
	e.classify = fn
}

func (e *Executor) isUnitSelected(screen gocv.Mat, x, y int) bool {
	if screen.Empty() || x < 0 || y < 0 || x >= screen.Cols() || y >= screen.Rows() {
		return false
	}

	// Selection glow usually encompasses the whole icon or a large part of it
	h := screen.Rows()
	size := int(float64(h) * 0.045) // Slightly larger: ~33px on 732h
	region := image.Rect(x-size, y-size, x+size, y+size)
	if region.Min.X < 0 { region.Min.X = 0 }
	if region.Min.Y < 0 { region.Min.Y = 0 }
	if region.Max.X > screen.Cols() { region.Max.X = screen.Cols() }
	if region.Max.Y > screen.Rows() { region.Max.Y = screen.Rows() }

	sub := screen.Region(region)
	defer sub.Close()

	hsv := gocv.NewMat()
	defer hsv.Close()
	gocv.CvtColor(sub, &hsv, gocv.ColorBGRToHSV)

	// Wider teal selection color in HSV to catch variations
	lower := gocv.NewScalar(75, 80, 40, 0)
	upper := gocv.NewScalar(115, 255, 255, 0)

	mask := gocv.NewMat()
	defer mask.Close()
	gocv.InRangeWithScalar(hsv, lower, upper, &mask)

	count := gocv.CountNonZero(mask)
	e.logger.Debug().Int("x", x).Int("y", y).Int("teal_pixels", count).Msg("selection verification")
	
	// Threshold: ~4% of region
	threshold := int(float64(region.Dx()*region.Dy()) * 0.04)
	return count > threshold
}

// Validate ensures all required templates for the strategy exist
func (e *Executor) Validate(s *strategy.DynamicStrategy) error {
	for _, phase := range s.Phases {
		for _, unit := range phase.Units {
			unitName := strings.ToLower(strings.TrimSpace(unit.Name))
			fileName := strings.ReplaceAll(unitName, " ", "_")
			tplPath := fmt.Sprintf("assets/templates/attack/%s.png", fileName)
			if _, err := os.Stat(tplPath); os.IsNotExist(err) {
				return fmt.Errorf("missing template for unit '%s' at path: %s", unit.Name, tplPath)
			}
		}
	}
	return nil
}

func (e *Executor) DeployDynamic(s *strategy.DynamicStrategy, screen gocv.Mat) (int, error) {
	w, h := screen.Cols(), screen.Rows()
	targetEdge := s.TargetEdge

	// Pre-flight validation
	if err := e.Validate(s); err != nil {
		e.logger.Error().Err(err).Msg("pre-flight validation failed")
		return 0, err
	}

	// 1. Load Precision Config
	var pCfg PrecisionConfig
	usePrecision := false
	mBarY := int(float64(h) * 0.78) // Default fallback

	pData, err := os.ReadFile("assets/precision_config.json")
	if err == nil && json.Unmarshal(pData, &pCfg) == nil {
		usePrecision = true
		scaleX, scaleY := float64(w)/float64(pCfg.Width), float64(h)/float64(pCfg.Height)
		// Scale everything
		for k, v := range pCfg.Edges {
			pCfg.Edges[k] = ManualEdge{
				P1: image.Pt(int(float64(v.P1.X)*scaleX), int(float64(v.P1.Y)*scaleY)),
				P2: image.Pt(int(float64(v.P2.X)*scaleX), int(float64(v.P2.Y)*scaleY)),
			}
		}
		for k, v := range pCfg.SpellEdgesA {
			pCfg.SpellEdgesA[k] = ManualEdge{
				P1: image.Pt(int(float64(v.P1.X)*scaleX), int(float64(v.P1.Y)*scaleY)),
				P2: image.Pt(int(float64(v.P2.X)*scaleX), int(float64(v.P2.Y)*scaleY)),
			}
		}
		for k, v := range pCfg.SpellEdgesB {
			pCfg.SpellEdgesB[k] = ManualEdge{
				P1: image.Pt(int(float64(v.P1.X)*scaleX), int(float64(v.P1.Y)*scaleY)),
				P2: image.Pt(int(float64(v.P2.X)*scaleX), int(float64(v.P2.Y)*scaleY)),
			}
		}
		for k, v := range pCfg.HeroTargets {
			pCfg.HeroTargets[k] = image.Pt(int(float64(v.X)*scaleX), int(float64(v.Y)*scaleY))
		}
		mBarY = int(float64(pCfg.BarY) * scaleY)
		if mBarY > int(float64(h)*0.92) { mBarY = int(float64(h) * 0.92) }
		e.logger.Info().Int("bar_y", mBarY).Msg("using ULTIMATE PRECISION config")
	}

	if !usePrecision {
		return 0, fmt.Errorf("precision config required (run cmd/precision_setup)")
	}

	if strings.EqualFold(targetEdge, "Random") {
		edges := []string{"TopLeft", "TopRight", "BottomLeft", "BottomRight"}
		targetEdge = edges[rand.Intn(len(edges))]
		e.logger.Info().Str("edge", targetEdge).Msg("random edge selected")
	}

	lastBar := gocv.NewMat()
	defer func() {
		if !lastBar.Closed() { lastBar.Close() }
	}()

	var deployedHeroSlots []image.Point
	globalUsedSlots := make(map[int]bool)
	e.tappedSiegeXs = make(map[int]bool)

	// 1.5. Parse Troop Bar Layout and segment active slots into categories
	slots := e.ParseLayout(screen, pCfg, w, h, mBarY)
	
	var siegeXs []int
	for _, slot := range slots {
		if slot.Category == "Siege" {
			siegeXs = append(siegeXs, slot.X)
		}
	}

	// Create slot map of category -> slice of slots (grouped by X coordinate, sorted left-to-right)
	slotMap := make(map[string][]image.Point)
	for _, slot := range slots {
		slotMap[slot.Category] = append(slotMap[slot.Category], image.Pt(slot.X, slot.Y))
	}

	// 1.6. Save Visual Diagnostics Overlay
	debugImg := screen.Clone()
	defer debugImg.Close()
	// Draw Y-bar limit
	gocv.Line(&debugImg, image.Pt(0, mBarY), image.Pt(w, mBarY), color.RGBA{0, 0, 255, 255}, 2)
	// Draw Green deployment edge
	if edge, ok := pCfg.Edges[targetEdge]; ok {
		gocv.Line(&debugImg, edge.P1, edge.P2, color.RGBA{0, 255, 0, 255}, 3)
	}
	// Draw categorized circles over detected active slots
	for _, slot := range slots {
		c := color.RGBA{255, 255, 255, 255} // Default white
		switch slot.Category {
		case "Troop": c = color.RGBA{255, 0, 0, 255} // Blue in BGR
		case "Siege": c = color.RGBA{0, 255, 255, 255} // Yellow in BGR
		case "Hero": c = color.RGBA{0, 255, 0, 255} // Green in BGR
		case "Spell": c = color.RGBA{255, 0, 255, 255} // Purple in BGR
		case "CC": c = color.RGBA{0, 165, 255, 255} // Orange in BGR
		}
		gocv.Circle(&debugImg, image.Pt(slot.X, slot.Y), 18, c, 2)
		gocv.PutText(&debugImg, slot.Category, image.Pt(slot.X-15, slot.Y-25), gocv.FontHersheySimplex, 0.4, c, 1)
	}
	gocv.IMWrite("attack_deploy_debug.png", debugImg)
	e.logger.Info().Msg("saved visual diagnostics to attack_deploy_debug.png")

	for _, phase := range s.Phases {
		e.logger.Info().Str("phase", phase.Name).Msg("attack phase")

		// Sort units: Spells -> Regular Units -> Abilities
		sortedUnits := make([]struct {
			unit strategy.Unit
			isAbility bool
		}, 0, len(phase.Units))

		for _, u := range phase.Units {
			isAbility := u.Pattern == "Ability" || phase.Pattern == "Ability"
			sortedUnits = append(sortedUnits, struct {
				unit strategy.Unit
				isAbility bool
			}{u, isAbility})
		}

		sort.SliceStable(sortedUnits, func(i, j int) bool {
			u1, u2 := sortedUnits[i], sortedUnits[j]
			n1, n2 := strings.ToLower(u1.unit.Name), strings.ToLower(u2.unit.Name)
			
			// Priority: Spells (0) > Others (1) > Abilities (2)
			p1, p2 := 1, 1
			if strings.Contains(n1, "spell") { p1 = 0 } else if u1.isAbility { p1 = 2 }
			if strings.Contains(n2, "spell") { p2 = 0 } else if u2.isAbility { p2 = 2 }
			
			return p1 < p2
		})

		// Pre-scan heroes if this is the heroes phase to ensure we only pick the top 4
		var heroMatches []struct {
			unit strategy.Unit
			match *vision.Match
			isAbility bool
		}

		isHeroesPhase := phase.Name == "Heroes"
		usedSlots := make(map[int]bool) // x-coordinates of slots used in this phase
		
		// Capture ONCE at start of phase unless we need to refresh
		if lastBar.Closed() || lastBar.Empty() {
			var err error
			lastBar, err = e.client.CaptureToMat()
			if err != nil {
				e.logger.Warn().Err(err).Msg("failed initial phase capture")
				continue
			}
		}

		for _, su := range sortedUnits {
			if su.isAbility {
				continue // Skip hero abilities during main phase loop (activated right after spells)
			}
			unit := su.unit
			unitName := strings.ToLower(strings.TrimSpace(unit.Name))
			isAbility := su.isAbility
			
			if lastBar.Closed() || lastBar.Empty() {
				var err error
				lastBar, err = e.client.CaptureToMat()
				if err != nil {
					e.logger.Warn().Err(err).Msg("failed capture")
					continue
				}
			}

			isSpell := strings.Contains(unitName, "spell")
			isSiege := strings.Contains(unitName, "slammer") || strings.Contains(unitName, "siege")
			isHero := strings.Contains(unitName, "king") || strings.Contains(unitName, "queen") || strings.Contains(unitName, "warden") || strings.Contains(unitName, "prince") || strings.Contains(unitName, "duke") || strings.Contains(unitName, "champion")

			fileName := strings.ReplaceAll(unitName, " ", "_")
			tplPath := fmt.Sprintf("assets/templates/attack/%s.png", fileName)
			tpl := gocv.IMRead(tplPath, gocv.IMReadColor)
			if tpl.Empty() {
				e.logger.Error().Str("unit", unit.Name).Msg("template empty after validation")
				continue
			}

			findMatch := func(screen gocv.Mat, currentThreshold float32) *vision.Match {
				barROI := image.Rect(0, mBarY, w, h)
				matches, _ := vision.MatchMultiScaleROI(screen, tpl, 0.2, 1.2, 20, currentThreshold, barROI)
				if len(matches) > 0 {
					sort.Slice(matches, func(i, j int) bool { return matches[i].Confidence > matches[j].Confidence })
					
					// Filter out matches that overlap with already used slots
					for _, m := range matches {
						isUsed := false
						for ux := range usedSlots {
							// If the matched X is within 5% of screen width from a used slot, it's the same slot
							if math.Abs(float64(m.Point.X-ux)) < float64(w)*0.05 {
								isUsed = true
								break
							}
						}
						if !isUsed {
							return &m
						}
					}
				}
				return nil
			}

			var match *vision.Match
			thresholds := []float32{0.80, 0.70, 0.60, 0.55}
			if isHero || isSiege { thresholds = []float32{0.75, 0.65, 0.55, 0.50} }
			if isSpell { thresholds = []float32{0.80, 0.75, 0.70, 0.65} }

			for _, t := range thresholds {
				match = findMatch(lastBar, t)
				if match != nil { break }
			}
			tpl.Close()

			category := "Troop"
			if isSpell {
				category = "Spell"
			} else if isHero {
				category = "Hero"
			} else if isSiege {
				category = "Siege"
			}

			if match == nil && !isAbility {
				// Fallback to dynamic layout parser mapping
				if len(slotMap[category]) > 0 {
					targetPt := slotMap[category][0]
					slotMap[category] = slotMap[category][1:]
					match = &vision.Match{
						Point:      targetPt,
						Confidence: 1.0,
					}
					e.logger.Info().Str("unit", unit.Name).Str("category", category).Interface("pos", targetPt).Msg("layout parser fallback mapping")
				}
			} else if match != nil {
				// Remove matched X from slotMap to avoid double deployment
				for idx, slot := range slotMap[category] {
					if math.Abs(float64(match.Point.X-slot.X)) < float64(w)*0.05 {
						slotMap[category] = append(slotMap[category][:idx], slotMap[category][idx+1:]...)
						break
					}
				}
			}

			if isHeroesPhase && isHero && match != nil {
				heroMatches = append(heroMatches, struct {
					unit      strategy.Unit
					match     *vision.Match
					isAbility bool
				}{unit, match, isAbility})
				usedSlots[match.Point.X] = true
				globalUsedSlots[match.Point.X] = true
				continue // Process after collecting all heroes
			}

			if match == nil {
				if !isAbility {
					e.logger.Warn().Str("unit", unit.Name).Msg("unit not found in bar")
				}
				continue
			}

			usedSlots[match.Point.X] = true
			globalUsedSlots[match.Point.X] = true
			e.deployUnit(unit, match, pCfg, targetEdge, w, h, isAbility, lastBar)
		}

		// Handle Heroes Phase (Deployment Only)
		if isHeroesPhase && len(heroMatches) > 0 {
			// 1. Separate Main and Bonus Deployments
			var mainDeployments []struct {
				unit      strategy.Unit
				match     *vision.Match
				isAbility bool
			}
			var bonusDeployments []struct {
				unit      strategy.Unit
				match     *vision.Match
				isAbility bool
			}

			for _, hm := range heroMatches {
				if hm.isAbility {
					continue // Skip abilities here
				}
				name := strings.ToLower(hm.unit.Name)
				isMain := strings.Contains(name, "king") || strings.Contains(name, "queen") || strings.Contains(name, "warden") || strings.Contains(name, "champion")
				
				if isMain {
					mainDeployments = append(mainDeployments, hm)
				} else {
					bonusDeployments = append(bonusDeployments, hm)
				}
			}

			// 2. Sort main deployments by confidence descending and take top 4
			sort.Slice(mainDeployments, func(i, j int) bool {
				return mainDeployments[i].match.Confidence > mainDeployments[j].match.Confidence
			})

			limitMain := 4
			if len(mainDeployments) < limitMain {
				limitMain = len(mainDeployments)
			}

			for i := 0; i < limitMain; i++ {
				e.deployUnit(mainDeployments[i].unit, mainDeployments[i].match, pCfg, targetEdge, w, h, false, lastBar)
				deployedHeroSlots = append(deployedHeroSlots, mainDeployments[i].match.Point)
			}

			// 3. Sort bonus deployments by confidence descending
			sort.Slice(bonusDeployments, func(i, j int) bool {
				return bonusDeployments[i].match.Confidence > bonusDeployments[j].match.Confidence
			})

			for i := 0; i < len(bonusDeployments); i++ {
				e.deployUnit(bonusDeployments[i].unit, bonusDeployments[i].match, pCfg, targetEdge, w, h, false, lastBar)
				deployedHeroSlots = append(deployedHeroSlots, bonusDeployments[i].match.Point)
			}
		}

		pDelay := time.Duration(phase.DelayAfterMS) * time.Millisecond
		if phase.Name == "Heroes" || phase.Name == "Siege Machine" { pDelay = 0 } // No delay between Siege and Heroes
		if pDelay > 5 { pDelay = 5 } // Hard cap for speed
		if pDelay > 0 {
			time.Sleep(pDelay)
		}
	}
	
	// 4. Activate hero abilities right after spells (just retap hero icons again once)
	if len(deployedHeroSlots) > 0 {
		e.logger.Info().Msg("activating hero abilities right after spells...")
		for _, pt := range deployedHeroSlots {
			e.logger.Info().Int("x", pt.X).Int("y", pt.Y).Msg("retapping hero icon once for ability/equipment")
			e.client.TapFast(pt.X, pt.Y, 2.0)
			e.client.HumanSleep(25, 5)
		}
	}
	
	// Final Sweep: Deploy any remaining event or mismatched troops in the bar
	sweepScreen, err := e.client.CaptureToMat()
	if err == nil {
		e.SweepRemainingSlots(sweepScreen, pCfg, targetEdge, w, h, mBarY, globalUsedSlots, siegeXs)
		sweepScreen.Close()
	}

	// 5. Deployment Success Verifier & Retry Loop
	e.logger.Info().Msg("verifying deployment success...")
	var remainingCount int
	for attempt := 1; attempt <= 2; attempt++ {
		verifyScreen, err := e.client.CaptureToMat()
		if err != nil {
			break
		}
		
		// Re-scan active slots
		var remainingActiveSlots []TroopSlot
		verifySlots := e.ParseLayout(verifyScreen, pCfg, w, h, mBarY)
		for _, slot := range verifySlots {
			// Skip if it is a Siege slot to avoid triggering release
			isSiegeSlot := false
			for _, sx := range siegeXs {
				if math.Abs(float64(slot.X-sx)) < float64(w)*0.04 {
					isSiegeSlot = true
					break
				}
			}
			if !isSiegeSlot {
				for sx := range e.tappedSiegeXs {
					if math.Abs(float64(slot.X-sx)) < float64(w)*0.04 {
						isSiegeSlot = true
						break
					}
				}
			}
			if isSiegeSlot {
				continue
			}

			// Sweep any remaining slots that are not empty (excluding Heroes that are already deployed but show ability)
			if slot.Category == "Troop" || slot.Category == "Spell" || slot.Category == "CC" {
				remainingActiveSlots = append(remainingActiveSlots, slot)
			}
		}
		verifyScreen.Close()

		remainingCount = len(remainingActiveSlots)
		if remainingCount == 0 {
			e.logger.Info().Msg("all troops, spells, and CC successfully deployed!")
			break
		}

		e.logger.Warn().Int("attempt", attempt).Int("remaining_slots", len(remainingActiveSlots)).Msg("detected undeployed units, retrying...")
		
		// Redeploy remaining slots
		for _, slot := range remainingActiveSlots {
			e.logger.Info().Int("x", slot.X).Str("category", slot.Category).Msg("re-deploying remaining slot")
			
			// Select the slot
			e.client.TapFast(slot.X, slot.Y, 2.0)
			e.client.HumanSleep(35, 10)
			
			// Deploy
			var p1, p2 image.Point
			if edge, ok := pCfg.Edges[targetEdge]; ok {
				p1, p2 = edge.P1, edge.P2
			} else {
				p1 = image.Pt(w/2, h/2)
				p2 = p1
			}

			maxRetryAttempts := 4
			for batch := 0; batch < maxRetryAttempts; batch++ {
				if p1 == p2 {
					for i := 0; i < 4; i++ {
						e.client.TapFast(p1.X, p1.Y, 12.0)
						e.client.HumanSleep(20, 5)
					}
				} else {
					steps := 8
					for i := 0; i < steps; i++ {
						pct := float64(i) / float64(steps-1)
						tx, ty := int(float64(p1.X)+float64(p2.X-p1.X)*pct), int(float64(p1.Y)+float64(p2.Y-p1.Y)*pct)
						e.client.TapFast(tx, ty, 15.0)
						e.client.HumanSleep(25, 5)
					}
				}

				checkMat, err := e.client.CaptureToMat()
				if err != nil {
					break
				}
				isEmpty := e.isSlotEmpty(checkMat, slot.X, slot.Y)
				checkMat.Close()

				if isEmpty {
					break
				}
				// Re-select
				e.client.TapFast(slot.X, slot.Y, 2.0)
				e.client.HumanSleep(35, 10)
			}
			e.client.HumanSleep(35, 10)
		}
	}

	return remainingCount, nil
}

func (e *Executor) IsSlotEmpty(screen gocv.Mat, x, y int) bool {
	return e.isSlotEmpty(screen, x, y)
}

func (e *Executor) isSlotEmpty(screen gocv.Mat, x, y int) bool {
	if screen.Empty() || x < 0 || y < 0 || x >= screen.Cols() || y >= screen.Rows() {
		return true
	}

	h := screen.Rows()
	size := int(float64(h) * 0.015) // Small center sample (~11px for 732h)
	region := image.Rect(x-size, y-size, x+size, y+size)
	if region.Min.X < 0 { region.Min.X = 0 }
	if region.Min.Y < 0 { region.Min.Y = 0 }
	if region.Max.X > screen.Cols() { region.Max.X = screen.Cols() }
	if region.Max.Y > screen.Rows() { region.Max.Y = screen.Rows() }

	sub := screen.Region(region)
	defer sub.Close()

	hsv := gocv.NewMat()
	defer hsv.Close()
	gocv.CvtColor(sub, &hsv, gocv.ColorBGRToHSV)

	brightNonGrass := 0
	colorPixels := 0
	total := hsv.Rows() * hsv.Cols()

	for row := 0; row < hsv.Rows(); row++ {
		for col := 0; col < hsv.Cols(); col++ {
			hu := hsv.GetUCharAt(row, col*3)
			sa := hsv.GetUCharAt(row, col*3+1)
			va := hsv.GetUCharAt(row, col*3+2)
			isGrass := hu >= 35 && hu <= 85 && sa > 50
			if va > 100 && !isGrass {
				brightNonGrass++
			}
			// Color presence: saturation > 50 and value > 70, excluding grass
			if sa > 50 && va > 70 && !isGrass {
				colorPixels++
			}
		}
	}

	ratio := float64(brightNonGrass) / float64(total)
	colorRatio := float64(colorPixels) / float64(total)
	e.logger.Debug().
		Int("x", x).
		Int("y", y).
		Float64("card_ratio", ratio).
		Float64("color_ratio", colorRatio).
		Msg("slot card presence check")

	// Empty if either the slot card isn't present (ratio < 0.25) OR
	// the card is greyed out (colorRatio < 0.08)
	return ratio < 0.25 || colorRatio < 0.08
}

func (e *Executor) deployUnit(unit strategy.Unit, match *vision.Match, pCfg PrecisionConfig, targetEdge string, w, h int, isAbility bool, currentScreen gocv.Mat) {
	unitName := strings.ToLower(strings.TrimSpace(unit.Name))
	isSiege := strings.Contains(unitName, "slammer") || strings.Contains(unitName, "siege")
	isHero := strings.Contains(unitName, "king") || strings.Contains(unitName, "queen") || strings.Contains(unitName, "warden") || strings.Contains(unitName, "prince") || strings.Contains(unitName, "duke") || strings.Contains(unitName, "champion")
	isHeroOrSiege := isHero || isSiege
	isSpell := strings.Contains(unitName, "spell")

	uPt := match.Point
	e.logger.Info().Str("unit", unit.Name).Bool("ability", isAbility).Int("x", uPt.X).Int("y", uPt.Y).Float64("conf", match.Confidence).Msg("selecting unit")

	if isAbility {
		// Hero abilities: Verify hero is alive (slot not empty)
		if !currentScreen.Empty() {
			if e.isSlotEmpty(currentScreen, uPt.X, uPt.Y) {
				e.logger.Info().Str("unit", unit.Name).Msg("hero dead or ability used, skipping")
				return
			}
		} else {
			verify, err := e.client.CaptureToMat()
			if err == nil {
				defer verify.Close()
				if e.isSlotEmpty(verify, uPt.X, uPt.Y) {
					e.logger.Info().Str("unit", unit.Name).Msg("hero dead or ability used, skipping")
					return
				}
			}
		}

		e.client.HumanSleep(10, 5)
		// Reduced to ONE tap for ability to prevent "over and over"
		e.client.TapFast(uPt.X, uPt.Y, 4.0)
		e.client.HumanSleep(10, 5)
		return
	}

	// 1. Verify slot is not empty before selection
	if !currentScreen.Empty() {
		if e.isSlotEmpty(currentScreen, uPt.X, uPt.Y) {
			e.logger.Info().Str("unit", unit.Name).Msg("slot is empty/already deployed, skipping")
			return
		}
	} else {
		verify, err := e.client.CaptureToMat()
		if err == nil {
			defer verify.Close()
			if e.isSlotEmpty(verify, uPt.X, uPt.Y) {
				e.logger.Info().Str("unit", unit.Name).Msg("slot is empty/already deployed, skipping")
				return
			}
		}
	}

	// 2. Prevent retapping Siege slot
	if isSiege {
		for sx := range e.tappedSiegeXs {
			if math.Abs(float64(uPt.X-sx)) < float64(w)*0.04 {
				e.logger.Info().Str("unit", unit.Name).Msg("siege slot already tapped, skipping to avoid destruction")
				return
			}
		}
	}

	isSpamUnit := strings.Contains(unitName, "balloon") || strings.Contains(unitName, "electro")
	
	// Select slot (idempotent, no glow check needed to avoid false-positives on blue units)
	e.client.TapFast(uPt.X, uPt.Y, 2.0)
	e.client.HumanSleep(35, 10)

	if isSiege {
		e.tappedSiegeXs[uPt.X] = true
	}

	if !isSpell && !isSpamUnit {
		e.client.HumanSleep(10, 5)
	}

	// Deployment Logic
	isRage := strings.Contains(unitName, "rage")
	isFreeze := strings.Contains(unitName, "ice") || strings.Contains(unitName, "freeze")
	_ = isFreeze
	isDragonDuke := strings.Contains(unitName, "duke")

	if isSpell {
		edgeA, okA := pCfg.SpellEdgesA[targetEdge]
		edgeB, okB := pCfg.SpellEdgesB[targetEdge]
		if okA && okB {
			maxSpellAttempts := 4
			for batch := 0; batch < maxSpellAttempts; batch++ {
				if isRage {
					e.logger.Info().Str("unit", unit.Name).Msg("deploying spell lines batch (Rage)")
					lines := []ManualEdge{edgeA, edgeB}
					for _, edge := range lines {
						p1, p2 := edge.P1, edge.P2
						for i := 0; i < 2; i++ { // 2 taps per line = 4 total per batch
							pct := float64(i)
							tx, ty := int(float64(p1.X)+float64(p2.X-p1.X)*pct), int(float64(p1.Y)+float64(p2.Y-p1.Y)*pct)
							e.client.TapFast(tx, ty, 8.0)
							e.client.HumanSleep(10, 2)
						}
					}
				} else { // Freeze or other spells
					e.logger.Info().Str("unit", unit.Name).Msg("deploying spell line batch")
					p1, p2 := edgeB.P1, edgeB.P2
					for i := 0; i < 3; i++ { // 3 taps per batch
						pct := float64(i) / 2.0
						tx, ty := int(float64(p1.X)+float64(p2.X-p1.X)*pct), int(float64(p1.Y)+float64(p2.Y-p1.Y)*pct)
						e.client.TapFast(tx, ty, 8.0)
						e.client.HumanSleep(10, 2)
					}
				}

				// Check if empty
				checkMat, err := e.client.CaptureToMat()
				if err != nil {
					break
				}
				isEmpty := e.isSlotEmpty(checkMat, uPt.X, uPt.Y)
				checkMat.Close()

				if isEmpty {
					e.logger.Info().Str("unit", unit.Name).Msg("spell slot empty, finished deploying")
					break
				}
				e.logger.Info().Str("unit", unit.Name).Msg("spell slot not empty yet, selecting again and deploying next batch...")
				e.client.TapFast(uPt.X, uPt.Y, 2.0)
				e.client.HumanSleep(35, 10)
			}
		}
	} else {
		var p1, p2 image.Point
		deploymentEdge := targetEdge

		if isDragonDuke && !isAbility {
			// Dragon Duke goes on adjacent side
			adjacents := map[string][]string{
				"TopLeft":     {"TopRight", "BottomLeft"},
				"TopRight":    {"TopLeft", "BottomRight"},
				"BottomLeft":  {"TopLeft", "BottomRight"},
				"BottomRight": {"TopRight", "BottomLeft"},
			}
			if opts, ok := adjacents[targetEdge]; ok {
				deploymentEdge = opts[rand.Intn(len(opts))]
				e.logger.Info().Str("target", targetEdge).Str("duke_edge", deploymentEdge).Msg("Dragon Duke adjacent placement")
			}
		}

		if isHeroOrSiege {
			if pt, ok := pCfg.HeroTargets[deploymentEdge]; ok { p1, p2 = pt, pt }
		} else {
			if edge, ok := pCfg.Edges[deploymentEdge]; ok { p1, p2 = edge.P1, edge.P2 }
		}

		if isHero || isSiege {
			e.logger.Info().Str("unit", unit.Name).Int("x", p1.X).Int("y", p1.Y).Msg("deploying hero/siege point")
			e.client.TapFast(p1.X, p1.Y, 12.0)
			e.client.HumanSleep(8, 2)
		} else {
			// Regular troops / spam units / event troops
			maxAttempts := 6
			for batch := 0; batch < maxAttempts; batch++ {
				if p1 == p2 { // Point deployment batch
					e.logger.Info().Str("unit", unit.Name).Int("x", p1.X).Int("y", p1.Y).Msg("deploying troop point batch")
					for i := 0; i < 6; i++ {
						e.client.TapFast(p1.X, p1.Y, 12.0)
						e.client.HumanSleep(8, 2)
					}
				} else { // Line deployment batch
					e.logger.Info().Str("unit", unit.Name).Msg("deploying troop line batch")
					steps := 8
					for i := 0; i < steps; i++ {
						pct := float64(i) / float64(steps-1)
						tx, ty := int(float64(p1.X)+float64(p2.X-p1.X)*pct), int(float64(p1.Y)+float64(p2.Y-p1.Y)*pct)
						e.client.TapFast(tx, ty, 15.0)
						e.client.HumanSleep(10, 2)
					}
				}

				// Check if slot is empty now
				checkMat, err := e.client.CaptureToMat()
				if err != nil {
					break
				}
				isEmpty := e.isSlotEmpty(checkMat, uPt.X, uPt.Y)
				checkMat.Close()

				if isEmpty {
					e.logger.Info().Str("unit", unit.Name).Msg("troop slot empty, finished deploying")
					break
				}
				
				e.logger.Info().Str("unit", unit.Name).Msg("troop slot not empty yet, selecting again and deploying next batch...")
				e.client.TapFast(uPt.X, uPt.Y, 2.0)
				e.client.HumanSleep(35, 10)
			}
		}
		// Ensure touch is fully released and processed by the system before any next card selection
		e.client.HumanSleep(35, 10)
	}
}

func (e *Executor) CalculateInBetween(edge string, offset int, bT, bB, bL, bR, fT, fB, fL, fR image.Point) (p1, p2 image.Point) {
	pct := float64(offset) / 100.0
	var baseP1, baseP2, fieldP1, fieldP2 image.Point
	switch edge {
	case "TopRight": baseP1, baseP2, fieldP1, fieldP2 = bT, bR, fT, fR
	case "BottomRight": baseP1, baseP2, fieldP1, fieldP2 = bR, bB, fR, fB
	case "BottomLeft": baseP1, baseP2, fieldP1, fieldP2 = bB, bL, fB, fL
	case "TopLeft": baseP1, baseP2, fieldP1, fieldP2 = bL, bT, fL, fT
	default: baseP1, baseP2, fieldP1, fieldP2 = bT, bR, fT, fR
	}

	p1 = image.Pt(
		int(float64(baseP1.X) + float64(fieldP1.X-baseP1.X)*pct),
		int(float64(baseP1.Y) + float64(fieldP1.Y-baseP1.Y)*pct),
	)
	p2 = image.Pt(
		int(float64(baseP2.X) + float64(fieldP2.X-baseP2.X)*pct),
		int(float64(baseP2.Y) + float64(fieldP2.Y-baseP2.Y)*pct),
	)
	return
}

func (e *Executor) MaximizeLineSpread(p1, p2 image.Point, w, mBarY int) (image.Point, image.Point) {
	return p1, p2 // Simplified for now, just return as is
}
func (e *Executor) EndBattle() error {
	ex, ey := e.cal.ScaleRef(34, 558)
	if err := e.client.TapHuman(ex, ey, 5.0); err != nil { return err }
	time.Sleep(3 * time.Second); return nil
}

func (e *Executor) ReturnHome() error {
	hx, hy := e.cal.ScaleRef(429, 582)
	if err := e.client.TapHuman(hx, hy, 5.0); err != nil { return err }
	time.Sleep(5 * time.Second)
	screen, err := e.client.CaptureToMat()
	if err != nil { return err }
	defer screen.Close()
	state, _ := e.classify(screen)
	if state != game.StateMainVillage { return fmt.Errorf("did not return home") }
	return nil
}

func (e *Executor) WaitForBattleEnd(timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(1000 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			screen, err := e.client.CaptureToMat()
			if err != nil { continue }
			defer screen.Close()
			state, _ := e.classify(screen)
			if state == game.StateBattleEnd { return true }
			if time.Now().After(deadline) { return false }
		}
	}
}

func (e *Executor) SweepRemainingSlots(screen gocv.Mat, pCfg PrecisionConfig, targetEdge string, w, h int, mBarY int, usedSlots map[int]bool, siegeXs []int) {
	e.logger.Info().Msg("starting sweep of remaining/event slots...")
	
	// The center Y of the troop slots is roughly midway between mBarY and the screen bottom
	slotY := mBarY + (h - mBarY)/2
	
	// We scan horizontally from X = 40 to w - 20 with a step of 75 pixels (scaled)
	step := int(75.0 * e.cal.ScaleX)
	startX := int(40.0 * e.cal.ScaleX)
	
	for x := startX; x < w - 20; x += step {
		// Check if this X coordinate is already close to a slot we processed
		alreadyUsed := false
		for ux := range usedSlots {
			if math.Abs(float64(x - ux)) < float64(w)*0.04 {
				alreadyUsed = true
				break
			}
		}
		if alreadyUsed {
			continue
		}
		
		// If the slot at (x, slotY) is not empty, it contains a troop/spell to deploy!
		if !e.isSlotEmpty(screen, x, slotY) {
			// Skip if it is a Siege slot to avoid triggering release
			isSiegeSlot := false
			for _, sx := range siegeXs {
				if math.Abs(float64(x-sx)) < float64(w)*0.04 {
					isSiegeSlot = true
					break
				}
			}
			if !isSiegeSlot {
				for sx := range e.tappedSiegeXs {
					if math.Abs(float64(x-sx)) < float64(w)*0.04 {
						isSiegeSlot = true
						break
					}
				}
			}
			if isSiegeSlot {
				continue
			}

			e.logger.Info().Int("x", x).Int("y", slotY).Msg("found undeployed/event troop slot during sweep, deploying...")
			
			// Select the slot (click it)
			e.client.TapFast(x, slotY, 2.0)
			e.client.HumanSleep(35, 10)
			
			// Determine deployment target (default to line spread along target edge)
			var p1, p2 image.Point
			if edge, ok := pCfg.Edges[targetEdge]; ok {
				p1, p2 = edge.P1, edge.P2
			} else {
				p1 = image.Pt(w/2, h/2)
				p2 = p1
			}
			
			maxSweepAttempts := 4
			for batch := 0; batch < maxSweepAttempts; batch++ {
				if p1 == p2 {
					steps := 4
					for i := 0; i < steps; i++ {
						e.client.TapFast(p1.X, p1.Y, 12.0)
						e.client.HumanSleep(20, 5) // Small delay between taps to prevent gesture coalescing
					}
				} else {
					steps := 8
					for i := 0; i < steps; i++ {
						pct := float64(i) / float64(steps-1)
						tx, ty := int(float64(p1.X)+float64(p2.X-p1.X)*pct), int(float64(p1.Y)+float64(p2.Y-p1.Y)*pct)
						e.client.TapFast(tx, ty, 15.0)
						e.client.HumanSleep(25, 5)
					}
				}

				checkMat, err := e.client.CaptureToMat()
				if err != nil {
					break
				}
				isEmpty := e.isSlotEmpty(checkMat, x, slotY)
				checkMat.Close()

				if isEmpty {
					e.logger.Info().Int("x", x).Msg("swept slot empty, finished deploying")
					break
				}
				// Re-select
				e.client.TapFast(x, slotY, 2.0)
				e.client.HumanSleep(35, 10)
			}
			
			// Add to usedSlots so we don't double-process
			usedSlots[x] = true
			e.client.HumanSleep(35, 10) // Delay to ensure release before next selection
		}
	}
}

type TroopSlot struct {
	X        int
	Y        int
	Category string // "Troop", "Siege", "Hero", "Spell", "CC"
}

func (e *Executor) ParseLayout(screen gocv.Mat, pCfg PrecisionConfig, w, h, mBarY int) []TroopSlot {
	// 1. Detect all active slots
	slotY := mBarY + (h-mBarY)/2
	step := int(75.0 * e.cal.ScaleX)
	startX := int(40.0 * e.cal.ScaleX)

	var activeXs []int
	for x := startX; x < w-20; x += step {
		if !e.isSlotEmpty(screen, x, slotY) {
			activeXs = append(activeXs, x)
		}
	}
	e.logger.Info().Ints("active_xs", activeXs).Msg("detected active slots in bar")

	if len(activeXs) == 0 {
		return nil
	}

	// 2. Identify Hero and Spell anchors using template matching
	barROI := image.Rect(0, mBarY, w, h)
	heroNames := []string{"barbarian_king", "archer_queen", "grand_warden", "minion_prince", "dragon_duke", "royal_champion"}
	spellNames := []string{"rage_spell", "ice_spell", "freeze_spell", "heal_spell", "jump_spell", "poison_spell"}
	siegeNames := []string{"stone_slammer", "battle_blimp", "wall_wrecker", "siege_barracks", "log_launcher"}

	matchedHeroes := make(map[int]bool)
	matchedSpells := make(map[int]bool)
	matchedSieges := make(map[int]bool)

	// Search Heroes
	for _, name := range heroNames {
		tplPath := fmt.Sprintf("assets/templates/attack/%s.png", name)
		tpl := gocv.IMRead(tplPath, gocv.IMReadColor)
		if tpl.Empty() {
			continue
		}
		matches, _ := vision.MatchMultiScaleROI(screen, tpl, 0.2, 1.2, 20, 0.50, barROI)
		tpl.Close()
		for _, m := range matches {
			matchedHeroes[m.Point.X] = true
		}
	}

	// Search Spells
	for _, name := range spellNames {
		tplPath := fmt.Sprintf("assets/templates/attack/%s.png", name)
		tpl := gocv.IMRead(tplPath, gocv.IMReadColor)
		if tpl.Empty() {
			continue
		}
		matches, _ := vision.MatchMultiScaleROI(screen, tpl, 0.2, 1.2, 20, 0.55, barROI)
		tpl.Close()
		for _, m := range matches {
			matchedSpells[m.Point.X] = true
		}
	}

	// Search Sieges
	for _, name := range siegeNames {
		tplPath := fmt.Sprintf("assets/templates/attack/%s.png", name)
		tpl := gocv.IMRead(tplPath, gocv.IMReadColor)
		if tpl.Empty() {
			continue
		}
		matches, _ := vision.MatchMultiScaleROI(screen, tpl, 0.2, 1.2, 20, 0.55, barROI)
		tpl.Close()
		for _, m := range matches {
			matchedSieges[m.Point.X] = true
		}
	}

	// 3. Classify slots based on anchors
	firstHeroX := 9999
	lastHeroX := -1
	for hx := range matchedHeroes {
		if hx < firstHeroX { firstHeroX = hx }
		if hx > lastHeroX { lastHeroX = hx }
	}

	firstSpellX := 9999
	for sx := range matchedSpells {
		if sx < firstSpellX { firstSpellX = sx }
	}

	// If anchors not found, use default layout thresholds
	if firstHeroX == 9999 {
		// Fallback: assume heroes start around 50% of the active slots
		idx := len(activeXs) / 2
		if idx < len(activeXs) {
			firstHeroX = activeXs[idx]
			lastHeroX = firstHeroX
		}
	}
	if firstSpellX == 9999 {
		// Fallback: assume spells are after heroes
		firstSpellX = lastHeroX + int(70.0*e.cal.ScaleX)
	}

	var slots []TroopSlot
	for _, x := range activeXs {
		cat := "Troop"

		isSiege := false
		for sx := range matchedSieges {
			if math.Abs(float64(x-sx)) < float64(w)*0.04 {
				isSiege = true
				break
			}
		}
		if !isSiege && firstHeroX != 9999 && x < firstHeroX {
			isLastBeforeHero := true
			for _, otherX := range activeXs {
				if otherX > x && otherX < firstHeroX {
					isLastBeforeHero = false
					break
				}
			}
			if isLastBeforeHero && x != activeXs[0] {
				isSiege = true
			}
		}

		if x >= firstSpellX - int(30.0*e.cal.ScaleX) {
			cat = "Spell"
		} else if x >= firstHeroX - int(30.0*e.cal.ScaleX) && x <= lastHeroX + int(30.0*e.cal.ScaleX) {
			cat = "Hero"
		} else if isSiege {
			cat = "Siege"
		}

		slots = append(slots, TroopSlot{
			X:        x,
			Y:        slotY,
			Category: cat,
		})
		e.logger.Info().Int("x", x).Str("category", cat).Msg("classified slot")
	}

	// Classify last slot as CC if far right
	if len(slots) > 0 {
		lastIdx := len(slots) - 1
		if slots[lastIdx].Category == "Spell" && slots[lastIdx].X > w - int(100.0*e.cal.ScaleX) {
			slots[lastIdx].Category = "CC"
			e.logger.Info().Int("x", slots[lastIdx].X).Msg("classified last slot as CC")
		}
	}

	return slots
}

