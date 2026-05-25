package attack

import (
	"encoding/json"
	"fmt"
	"image"
	"math/rand"
	"os"
	"sort"
	"strings"
	"time"

	"gocv.io/x/gocv"

	"github.com/Ducky705/ClashGo/internal/adb"
	"github.com/Ducky705/ClashGo/internal/config"
	"github.com/Ducky705/ClashGo/internal/game"
	"github.com/Ducky705/ClashGo/internal/vision"
	"github.com/Ducky705/ClashGo/pkg/strategy"
	"github.com/rs/zerolog"
)

type BaseCalibration struct {
	BaseTop, BaseRight, BaseBottom, BaseLeft image.Point
	FieldTop, FieldRight, FieldBottom, FieldLeft image.Point
	BarY, Width, Height int
}

type PrecisionConfig struct {
	Units        map[string]image.Point `json:"units"`
	Edges        map[string]ManualEdge  `json:"edges"`
	SpellEdgesA  map[string]ManualEdge  `json:"spell_edges_a"`
	SpellEdgesB  map[string]ManualEdge  `json:"spell_edges_b"`
	HeroTargets  map[string]image.Point  `json:"hero_targets"`
	BarY         int                    `json:"bar_y"`
	Width        int                    `json:"width"`
	Height       int                    `json:"height"`
}

type ManualEdge struct {
	P1 image.Point `json:"p1"`
	P2 image.Point `json:"p2"`
}

type Executor struct {
	client   *adb.Client
	cal      *game.Calibration
	cfg      *config.AttackConfig
	classify func(gocv.Mat) (game.GameState, int)
	logger   zerolog.Logger
}

func NewExecutor(client *adb.Client, cal *game.Calibration, cfg *config.AttackConfig, logger zerolog.Logger) *Executor {
	return &Executor{
		client: client,
		cal:    cal,
		cfg:    cfg,
		logger: logger.With().Str("component", "attack_executor").Logger(),
	}
}

func (e *Executor) SetClassifier(fn func(gocv.Mat) (game.GameState, int)) { e.classify = fn }

func (e *Executor) isUnitSelected(screen gocv.Mat, x, y int) bool {
	if screen.Empty() || x < 0 || y < 0 || x >= screen.Cols() || y >= screen.Rows() {
		return false
	}

	// Selection glow usually encompasses the whole icon or a large part of it
	// Scale size based on screen height to be more robust
	h := screen.Rows()
	size := int(float64(h) * 0.04) // ~30px on 732h
	region := image.Rect(x-size, y-size, x+size, y+size)
	if region.Min.X < 0 { region.Min.X = 0 }
	if region.Min.Y < 0 { region.Min.Y = 0 }
	if region.Max.X > screen.Cols() { region.Max.X = screen.Cols() }
	if region.Max.Y > screen.Rows() { region.Max.Y = screen.Rows() }

	sub := screen.Region(region)
	defer sub.Close()

	// Convert to HSV for robust color detection
	hsv := gocv.NewMat()
	defer hsv.Close()
	gocv.CvtColor(sub, &hsv, gocv.ColorBGRToHSV)

	// Teal selection color in HSV:
	// H: ~185° -> OpenCV Range (0-180): ~92
	// S: ~80%  -> OpenCV Range (0-255): ~204
	// V: ~55%  -> OpenCV Range (0-255): ~140
	lower := gocv.NewScalar(80, 100, 50, 0)
	upper := gocv.NewScalar(110, 255, 255, 0)

	mask := gocv.NewMat()
	defer mask.Close()
	gocv.InRangeWithScalar(hsv, lower, upper, &mask)

	count := gocv.CountNonZero(mask)
	e.logger.Debug().Int("x", x).Int("y", y).Int("teal_pixels", count).Msg("selection verification")
	
	// Significant number of teal pixels relative to region size
	threshold := (size * 2) * (size * 2) / 20 // ~5% of region
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

func (e *Executor) DeployDynamic(s *strategy.DynamicStrategy, screen gocv.Mat) error {
	w, h := screen.Cols(), screen.Rows()
	targetEdge := s.TargetEdge

	// Pre-flight validation
	if err := e.Validate(s); err != nil {
		e.logger.Error().Err(err).Msg("pre-flight validation failed")
		return err
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
		return fmt.Errorf("precision config required (run cmd/precision_setup)")
	}

	if strings.EqualFold(targetEdge, "Random") {
		edges := []string{"TopLeft", "TopRight", "BottomLeft", "BottomRight"}
		targetEdge = edges[rand.Intn(len(edges))]
		e.logger.Info().Str("edge", targetEdge).Msg("random edge selected")
	}

	lastBar := gocv.NewMat()
	defer func() {
		if !lastBar.Empty() { lastBar.Close() }
	}()

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
		
		// Capture ONCE at start of phase unless we need to refresh
		if lastBar.Empty() {
			var err error
			lastBar, err = e.client.CaptureToMat()
			if err != nil {
				e.logger.Warn().Err(err).Msg("failed initial phase capture")
				continue
			}
		}

		for _, su := range sortedUnits {
			unit := su.unit
			unitName := strings.ToLower(strings.TrimSpace(unit.Name))
			isAbility := su.isAbility
			
			// For abilities, we usually need a fresh capture because the hero was just deployed
			// But if we are in the collection phase of heroes, we don't want to refresh every time
			if isAbility && !isHeroesPhase {
				if !lastBar.Empty() { lastBar.Close() }
				var err error
				lastBar, err = e.client.CaptureToMat()
				if err != nil {
					e.logger.Warn().Err(err).Msg("failed ability refresh")
					continue
				}
			}

			if lastBar.Empty() {
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
					return &matches[0]
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

			if isHeroesPhase && isHero && match != nil {
				heroMatches = append(heroMatches, struct {
					unit      strategy.Unit
					match     *vision.Match
					isAbility bool
				}{unit, match, isAbility})
				continue // Process after collecting all heroes
			}

			if match == nil {
				if !isAbility {
					e.logger.Warn().Str("unit", unit.Name).Msg("unit not found in bar")
				}
				continue
			}

			e.deployUnit(unit, match, pCfg, targetEdge, w, h, isAbility)
		}

		// Handle Heroes Phase (Deployment first, then Abilities)
		if isHeroesPhase && len(heroMatches) > 0 {
			// 1. Separate Deployments and Abilities
			var deployments []struct {
				unit      strategy.Unit
				match     *vision.Match
				isAbility bool
			}
			var abilities []struct {
				unit      strategy.Unit
				match     *vision.Match
				isAbility bool
			}

			for _, hm := range heroMatches {
				if hm.isAbility {
					abilities = append(abilities, hm)
				} else {
					deployments = append(deployments, hm)
				}
			}

			// 2. Sort deployments by confidence descending and take top 4
			sort.Slice(deployments, func(i, j int) bool {
				return deployments[i].match.Confidence > deployments[j].match.Confidence
			})

			limit := 4
			if len(deployments) < limit {
				limit = len(deployments)
			}
			activeHeroes := make(map[string]bool)

			for i := 0; i < limit; i++ {
				e.deployUnit(deployments[i].unit, deployments[i].match, pCfg, targetEdge, w, h, false)
				activeHeroes[strings.ToLower(strings.TrimSpace(deployments[i].unit.Name))] = true
			}

			// 3. Process abilities for active heroes only
			if len(abilities) > 0 {
				// Recapture ONCE for all abilities after all heroes are deployed
				if !lastBar.Empty() { lastBar.Close() }
				lastBar, _ = e.client.CaptureToMat()
				
				for _, ab := range abilities {
					name := strings.ToLower(strings.TrimSpace(ab.unit.Name))
					if activeHeroes[name] {
						// Re-find the ability icon on the fresh capture
						// (Simplified: we use the old match position for now, but in reality 
						// hero icons might shift. However, for a fast attack, it's usually fine.)
						e.deployUnit(ab.unit, ab.match, pCfg, targetEdge, w, h, true)
					}
				}
			}
		}

		if !lastBar.Empty() { lastBar.Close(); lastBar = gocv.NewMat() }
		
		pDelay := time.Duration(phase.DelayAfterMS) * time.Millisecond
		if phase.Name == "Heroes" || phase.Name == "Siege Machine" { pDelay = 5 * time.Millisecond }
		if pDelay > 10 { pDelay = 10 } // Hard cap for speed
		if pDelay > 0 {
			// Add randomized delay variance (+/- 5ms)
			variance := time.Duration(rand.Intn(11)-5) * time.Millisecond
			time.Sleep(pDelay + variance)
		}
	}
	
	// Final Sweep: Dump any remaining troops (e.g. promotional units)
	e.dumpRemainingTroops(pCfg, targetEdge, w, h, mBarY)

	return nil
}

func (e *Executor) dumpRemainingTroops(pCfg PrecisionConfig, targetEdge string, w, h, mBarY int) {
	e.logger.Info().Msg("starting final sweep for remaining troops")

	// Calculate icon width spacing - roughly 12% of screen height is a safe bet for CoC icons
	iconWidth := int(float64(h) * 0.12)
	barCenterY := mBarY + (h-mBarY)/2

	// Sweep across the bar
	for x := iconWidth / 2; x < w; x += iconWidth {
		// Tap the bar to select slot
		e.client.TapFast(x, barCenterY, 3.0)
		e.client.HumanSleep(50, 10)

		// Capture and check if something is selected
		screen, err := e.client.CaptureToMat()
		if err != nil {
			continue
		}

		if e.isUnitSelected(screen, x, barCenterY) {
			e.logger.Info().Int("x", x).Msg("found remaining troop, dumping...")
			
			// Get deployment points
			var p1 image.Point
			if pt, ok := pCfg.HeroTargets[targetEdge]; ok {
				p1 = pt
			} else if edge, ok := pCfg.Edges[targetEdge]; ok {
				p1 = edge.P1
			} else {
				// Fallback to center of field if no edge config
				p1 = image.Pt(w/2, h/2)
			}

			// Deploy until slot empty (max 10 batches to prevent infinite loop)
			for batch := 0; batch < 10; batch++ {
				for i := 0; i < 5; i++ {
					e.client.TapFast(p1.X, p1.Y, 15.0)
					e.client.HumanSleep(20, 5)
				}
				
				// Re-verify selection
				verify, _ := e.client.CaptureToMat()
				if !verify.Empty() {
					stillSelected := e.isUnitSelected(verify, x, barCenterY)
					verify.Close()
					if !stillSelected {
						break
					}
				}
			}
		}
		screen.Close()
	}
}

func (e *Executor) deployUnit(unit strategy.Unit, match *vision.Match, pCfg PrecisionConfig, targetEdge string, w, h int, isAbility bool) {
	unitName := strings.ToLower(strings.TrimSpace(unit.Name))
	isSiege := strings.Contains(unitName, "slammer") || strings.Contains(unitName, "siege")
	isHero := strings.Contains(unitName, "king") || strings.Contains(unitName, "queen") || strings.Contains(unitName, "warden") || strings.Contains(unitName, "prince") || strings.Contains(unitName, "duke") || strings.Contains(unitName, "champion")
	isHeroOrSiege := isHero || isSiege
	isSpell := strings.Contains(unitName, "spell")

	uPt := match.Point
	e.logger.Info().Str("unit", unit.Name).Bool("ability", isAbility).Int("x", uPt.X).Int("y", uPt.Y).Float64("conf", match.Confidence).Msg("selecting unit")

	if isAbility {
		// Hero abilities: retap the icon to activate. 
		e.client.HumanSleep(30, 10)
		for i := 0; i < 2; i++ {
			e.client.TapFast(uPt.X, uPt.Y, 4.0)
			e.client.HumanSleep(30, 10)
		}
		return
	}

	selected := false
	isSpamUnit := strings.Contains(unitName, "balloon") || strings.Contains(unitName, "electro")
	
	if isSpell || isSpamUnit {
		// Spells and spam units need to be fast. Skip verification.
		e.client.TapFast(uPt.X, uPt.Y, 3.5)
		e.client.HumanSleep(30, 10)
		selected = true
	} else {
		for i := 0; i < 2; i++ { // Reduced from 3
			e.client.TapFast(uPt.X, uPt.Y, 3.5)
			e.client.HumanSleep(50, 15) // Fast selection tap

			if isSiege {
				e.client.HumanSleep(20, 10)
				e.client.TapFast(uPt.X, uPt.Y, 3.5)
				e.client.HumanSleep(50, 15)
			}

			// ONLY verify for heroes/siege if not in a hurry, otherwise just assume selected
			// For now, let's just use a very fast check
			verifyScreen, _ := e.client.CaptureToMat()
			if !verifyScreen.Empty() {
				if e.isUnitSelected(verifyScreen, uPt.X, uPt.Y) {
					selected = true
					verifyScreen.Close()
					break
				}
				verifyScreen.Close()
			}
		}
	}

	if !selected && !isSpell && !isSpamUnit {
		e.logger.Warn().Str("unit", unit.Name).Msg("could not verify selection (teal glow), trying to deploy anyway...")
	}

	if !isSpell && !isSpamUnit {
		e.client.HumanSleep(20, 10)
	}

	// Deployment Logic
	isRage := strings.Contains(unitName, "rage")
	isFreeze := strings.Contains(unitName, "ice") || strings.Contains(unitName, "freeze")
	isDragonDuke := strings.Contains(unitName, "duke")

	if isSpell {
		edgeA, okA := pCfg.SpellEdgesA[targetEdge]
		edgeB, okB := pCfg.SpellEdgesB[targetEdge]
		if okA && okB {
			if isRage {
				lines := []ManualEdge{edgeA, edgeB}
				for _, edge := range lines {
					p1, p2 := edge.P1, edge.P2
					for i := 0; i < 3; i++ {
						pct := float64(i) / 2.0
						tx, ty := int(float64(p1.X)+float64(p2.X-p1.X)*pct), int(float64(p1.Y)+float64(p2.Y-p1.Y)*pct)
						e.client.TapFast(tx, ty, 8.0)
						e.client.HumanSleep(30, 10)
					}
				}
			} else if isFreeze {
				p1, p2 := edgeB.P1, edgeB.P2
				for i := 0; i < 3; i++ {
					pct := float64(i) / 2.0
					tx, ty := int(float64(p1.X)+float64(p2.X-p1.X)*pct), int(float64(p1.Y)+float64(p2.Y-p1.Y)*pct)
					e.client.TapFast(tx, ty, 8.0)
					e.client.HumanSleep(30, 10)
				}
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

		steps := 1
		if isSpamUnit {
			steps = 15
		} else if p1 != p2 {
			steps = 12 // Default for standard units on a line
		}

		if p1 == p2 { // Point
			e.logger.Info().Str("unit", unit.Name).Int("x", p1.X).Int("y", p1.Y).Msg("deploying point")
			for i := 0; i < steps; i++ {
				e.client.TapFast(p1.X, p1.Y, 12.0)
				if steps > 1 {
					e.client.HumanSleep(15, 5) // Ultra fast deployment
				}
			}
		} else { // Line (Simulated 2-Finger Alternating Taps)
			e.logger.Info().Str("unit", unit.Name).Msg("deploying line (2-finger simulation)")
			
			// We split the steps into two interleaving streams to simulate two fingers
			for i := 0; i < steps; i++ {
				// Alternating logic: Finger 1 (left side of progress), Finger 2 (right side of progress)
				// This simulates two thumbs moving along the line.
				var pct float64
				if i%2 == 0 {
					// Finger 1: even steps, scaled from 0 to 0.5
					pct = (float64(i) / float64(steps-1)) * 0.5
				} else {
					// Finger 2: odd steps, scaled from 0.5 to 1.0
					pct = 0.5 + ((float64(i-1) / float64(steps-1)) * 0.5)
				}

				tx, ty := int(float64(p1.X)+float64(p2.X-p1.X)*pct), int(float64(p1.Y)+float64(p2.Y-p1.Y)*pct)
				e.client.TapFast(tx, ty, 15.0) // High spread for field deployment
				
				// Rapid alternation between "fingers" (shorter delay) vs between "sets"
				if i%2 == 0 {
					e.client.HumanSleep(10, 5) // Ultra fast tap between fingers
				} else {
					e.client.HumanSleep(25, 10) // Ultra fast human delay between dual taps
				}
			}
		}
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
	deadline := time.Now().Add(3 * time.Minute)
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
