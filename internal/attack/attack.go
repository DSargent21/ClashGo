package attack

import (
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Ducky705/ClashGO/internal/adb"
	"github.com/Ducky705/ClashGO/internal/config"
	"github.com/Ducky705/ClashGO/internal/game"
	"github.com/Ducky705/ClashGO/internal/paths"
	"github.com/Ducky705/ClashGO/internal/vision"
	"github.com/Ducky705/ClashGO/pkg/strategy"
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
	templates     map[string]gocv.Mat

	// Debug callbacks — nil when not debugging
	OnPhaseStart func(phase string, edge string)
	OnUnitDeploy func(unit string, slotX int, slotY int)

	// OnDukePick (debug-only). When non-nil, the legacy deployUnit
	// Dragon Duke branch fires it after the adjacent-corner random
	// pick — chosen is one of {TopLeft, TopRight, BottomLeft,
	// BottomRight}. The new path (HeroManager.resolveHeroTarget) does
	// NOT fire this — Duke falls through to the chosen edge there.
	// Either path is observable via the structured log line
	// "Dragon Duke adjacent-edge placement" already emitted by
	// deployUnit; this callback exists so a downstream tool can pin
	// every Duke pick to disk for after-the-fact corpus analysis.
	OnDukePick func(targetEdge string, chosenEdge string)
}

type PrecisionConfig struct {
	Edges map[string]ManualEdge `json:"edges"`
	// Sides holds the 4 STRICT deploy lines (top/right/bottom/left).
	// Optional — populated by pick_coords -mode=four and consumed by
	// SpotsForSide in internal/attack/spots.go so each side is a fully
	// first-class deployment target instead of an awkward corner-pair.
	Sides        map[string]ManualEdge  `json:"sides,omitempty"`
	SpellEdgesA  map[string]ManualEdge  `json:"spell_edges_a"`
	SpellEdgesB  map[string]ManualEdge  `json:"spell_edges_b"`
	HeroTargets  map[string]image.Point `json:"hero_targets"`
	SpellTargets map[string]image.Point `json:"spell_targets"`
	BarY         int                    `json:"bar_y"`
	Width        int                    `json:"width"`
	Height       int                    `json:"height"`
}

type StallConfig struct {
	PercentROI image.Rectangle `json:"percent_roi"`
	EndButton  image.Point     `json:"end_button"`
	ConfirmBtn image.Point     `json:"confirm_btn"`
	RefWidth   int             `json:"ref_width"`
	RefHeight  int             `json:"ref_height"`
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

	exec := &Executor{
		client:        client,
		cal:           cal,
		cfg:           cfg,
		logger:        logger.With().Str("component", "attack_executor").Logger(),
		classify:      classifier.ClassifyState,
		tappedSiegeXs: make(map[int]bool),
		templates:     make(map[string]gocv.Mat),
	}
	exec.loadTemplates()
	return exec
}

// configCache memoizes small attack JSON configs for a short window so steady-state
// reads (per attack / per phase) do not re-hit disk. Entries are invalidated after a
// TTL so on-disk edits are still picked up without a restart.
type configCacheEntry struct {
	data    []byte
	readAt  time.Time
}

var (
	configCacheMu sync.Mutex
	configCache   = map[string]configCacheEntry{}
	configCacheTTL = 5 * time.Second
)

func readConfigJSON(name string) ([]byte, bool) {
	configCacheMu.Lock()
	if e, ok := configCache[name]; ok && time.Since(e.readAt) < configCacheTTL {
		data := e.data
		configCacheMu.Unlock()
		return data, true
	}
	configCacheMu.Unlock()

	data, err := os.ReadFile(paths.Resolve(name))
	if err != nil {
		return nil, false
	}
	configCacheMu.Lock()
	configCache[name] = configCacheEntry{data: data, readAt: time.Now()}
	configCacheMu.Unlock()
	return data, true
}

func (e *Executor) loadTemplates() {
	dir := paths.Resolve("templates/attack")
	files, err := os.ReadDir(dir)
	if err != nil {
		e.logger.Error().Err(err).Msg("failed to read templates/attack directory")
		return
	}
	for _, f := range files {
		if f.IsDir() || !strings.HasSuffix(strings.ToLower(f.Name()), ".png") {
			continue
		}
		name := strings.TrimSuffix(strings.ToLower(f.Name()), ".png")
		path := filepath.Join(dir, f.Name())
		mat := gocv.IMRead(path, gocv.IMReadColor)
		if mat.Empty() {
			e.logger.Warn().Str("path", path).Msg("failed to load template image")
			continue
		}
		e.templates[name] = mat
		e.logger.Debug().Str("name", name).Msg("cached attack template")
	}
}

func (e *Executor) SetClassifier(fn func(gocv.Mat) (game.GameState, int)) {
	e.classify = fn
}

func (e *Executor) UpdateConfig(cfg *config.AttackConfig) {
	e.cfg = cfg
}

func (e *Executor) isUnitSelected(screen gocv.Mat, x, y int) bool {
	if screen.Empty() || x < 0 || y < 0 || x >= screen.Cols() || y >= screen.Rows() {
		return false
	}

	// Selection glow usually encompasses the whole icon or a large part of it
	h := screen.Rows()
	size := int(float64(h) * 0.045) // Slightly larger: ~33px on 732h
	region := image.Rect(x-size, y-size, x+size, y+size)
	if region.Min.X < 0 {
		region.Min.X = 0
	}
	if region.Min.Y < 0 {
		region.Min.Y = 0
	}
	if region.Max.X > screen.Cols() {
		region.Max.X = screen.Cols()
	}
	if region.Max.Y > screen.Rows() {
		region.Max.Y = screen.Rows()
	}

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

// Validate ensures all required templates for the strategy exist or are covered by manual labels
func (e *Executor) Validate(s *strategy.DynamicStrategy) error {
	// Load manual labels to see if templates are even needed
	manualUnits := make(map[string]bool)
	if data, ok := readConfigJSON("manual_labels.json"); ok {
		var lConf struct {
			Slots []struct {
				X    int    `json:"x"`
				Name string `json:"name"`
			} `json:"slots"`
		}
		if json.Unmarshal(data, &lConf) == nil {
			for _, s := range lConf.Slots {
				manualUnits[strings.ToLower(strings.TrimSpace(s.Name))] = true
			}
		}
	}

	for _, phase := range s.Phases {
		for _, unit := range phase.Units {
			unitName := strings.ToLower(strings.TrimSpace(unit.Name))

			// Skip if manually labeled
			if manualUnits[unitName] {
				continue
			}

			fileName := strings.ReplaceAll(unitName, " ", "_")
			if _, ok := e.templates[fileName]; !ok {
				return fmt.Errorf("missing template for unit '%s' (and not found in manual_labels.json)", unit.Name)
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

	pData, ok := readConfigJSON("precision_config.json")
	if ok && json.Unmarshal(pData, &pCfg) == nil {
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
		for k, v := range pCfg.SpellTargets {
			pCfg.SpellTargets[k] = image.Pt(int(float64(v.X)*scaleX), int(float64(v.Y)*scaleY))
		}
		// Sides scale-down — same scaleX/scaleY edges above use. Without this,
		// SpotsForSide (which scales internally) would double-scale callers
		// that already ran precision config through this loader.
		if pCfg.Sides != nil {
			for k, v := range pCfg.Sides {
				pCfg.Sides[k] = ManualEdge{
					P1: image.Pt(int(float64(v.P1.X)*scaleX), int(float64(v.P1.Y)*scaleY)),
					P2: image.Pt(int(float64(v.P2.X)*scaleX), int(float64(v.P2.Y)*scaleY)),
				}
			}
		}
		mBarY = int(float64(pCfg.BarY) * scaleY)
		if mBarY > int(float64(h)*0.92) {
			mBarY = int(float64(h) * 0.92)
		}
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
		if !lastBar.Closed() {
			lastBar.Close()
		}
	}()

	var deployedHeroSlots []image.Point
	globalUsedSlots := make(map[int]bool)
	e.tappedSiegeXs = make(map[int]bool)

	// 1.5. Parse Troop Bar Layout and segment active slots into categories
	slots := e.ParseLayout(screen, pCfg, w, h, mBarY)

	// Build the unitCache dynamically by scanning the bar ROI once at the start (100% robust and handles scaling)
	unitCache := make(map[string]*vision.Match)
	scaleY := float64(h) / float64(pCfg.Height)
	slotY := mBarY + int(38.0*scaleY)
	if data, ok := readConfigJSON("manual_slots.json"); ok {
		var mConf struct {
			SlotY      int `json:"slot_y"`
			CardHeight int `json:"card_height"`
		}
		if json.Unmarshal(data, &mConf) == nil {
			if mConf.SlotY > 0 {
				slotY = mConf.SlotY
			} else if mConf.CardHeight > 0 {
				slotY = mBarY + mConf.CardHeight/2
			}
		}
	}
	barROI := image.Rect(0, mBarY, w, h)

	// Load fallback manual labels mapping for safety/failsafe
	manualMap := make(map[int]string)
	if data, ok := readConfigJSON("manual_labels.json"); ok {
		var lConf struct {
			Slots []struct {
				X    int    `json:"x"`
				Name string `json:"name"`
			} `json:"slots"`
		}
		if json.Unmarshal(data, &lConf) == nil {
			for _, sVal := range lConf.Slots {
				manualMap[sVal.X] = sVal.Name
			}
		}
	}

	e.logger.Info().Msg("🤖 Pinpointing slot identities dynamically via template matching...")
	for tplName, tpl := range e.templates {
		if tpl.Empty() {
			continue
		}
		matches, _ := vision.MatchMultiScaleROICached(screen, tpl, tplName, 0.2, 1.2, 20, 0.55, barROI)
		if len(matches) > 0 {
			sort.Slice(matches, func(i, j int) bool { return matches[i].Confidence > matches[j].Confidence })
			bestMatch := matches[0]
			cleanName := strings.ReplaceAll(tplName, "_", " ")
			e.logger.Info().Str("unit", cleanName).Int("x", bestMatch.Point.X).Float64("conf", bestMatch.Confidence).Msg("pinpointed unit via template match")

			unitCache[cleanName] = &bestMatch
		}
	}

	var siegeXs []int
	for idx := range slots {
		slot := &slots[idx]

		matchedName := ""
		for name, match := range unitCache {
			if math.Abs(float64(slot.X-match.Point.X)) < float64(w)*0.04 {
				matchedName = name
				break
			}
		}

		if matchedName != "" {
			// Snap template match coordinates to actual slot position
			unitCache[matchedName].Point = image.Pt(slot.X, slot.Y)

			if e.isSiege(matchedName) {
				slot.Category = "Siege"
			} else if e.isHero(matchedName) {
				slot.Category = "Hero"
			} else if e.isSpell(matchedName) {
				slot.Category = "Spell"
			} else if strings.Contains(matchedName, "cc") || strings.Contains(matchedName, "castle") {
				slot.Category = "CC"
			} else {
				slot.Category = "Troop"
			}
		} else {
			// Fallback: Use manual labels override if template match is missing for this slot
			if label, ok := manualMap[slot.X]; ok && label != "Empty" {
				cleanName := strings.ToLower(strings.TrimSpace(label))
				e.logger.Warn().Int("x", slot.X).Str("fallback", cleanName).Msg("using fallback manual label for slot")

				unitCache[cleanName] = &vision.Match{
					Point:      image.Pt(slot.X, slotY),
					Confidence: 1.0,
					Scale:      1.0,
				}

				if e.isSiege(cleanName) {
					slot.Category = "Siege"
				} else if e.isHero(cleanName) {
					slot.Category = "Hero"
				} else if e.isSpell(cleanName) {
					slot.Category = "Spell"
				} else if strings.Contains(cleanName, "cc") || strings.Contains(cleanName, "castle") {
					slot.Category = "CC"
				} else {
					slot.Category = "Troop"
				}
			}
		}

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
		case "Troop":
			c = color.RGBA{255, 0, 0, 255} // Blue in BGR
		case "Siege":
			c = color.RGBA{0, 255, 255, 255} // Yellow in BGR
		case "Hero":
			c = color.RGBA{0, 255, 0, 255} // Green in BGR
		case "Spell":
			c = color.RGBA{255, 0, 255, 255} // Purple in BGR
		case "CC":
			c = color.RGBA{0, 165, 255, 255} // Orange in BGR
		}
		gocv.Circle(&debugImg, image.Pt(slot.X, slot.Y), 18, c, 2)
		gocv.PutText(&debugImg, slot.Category, image.Pt(slot.X-15, slot.Y-25), gocv.FontHersheySimplex, 0.4, c, 1)
	}
	gocv.IMWrite(paths.ResolveConfig("attack_deploy_debug.png"), debugImg)
	e.logger.Info().Msg("saved visual diagnostics to attack_deploy_debug.png")

	// 2.1. Remove already-cached manual/template units from slotMap to prevent fallback theft
	for name, match := range unitCache {
		category := "Troop"
		nameLower := strings.ToLower(name)
		if e.isSpell(nameLower) {
			category = "Spell"
		} else if e.isHero(nameLower) {
			category = "Hero"
		} else if e.isSiege(nameLower) {
			category = "Siege"
		}

		for idx, slot := range slotMap[category] {
			if math.Abs(float64(match.Point.X-slot.X)) < float64(w)*0.05 {
				slotMap[category] = append(slotMap[category][:idx], slotMap[category][idx+1:]...)
				e.logger.Debug().Str("unit", name).Int("x", slot.X).Str("category", category).Msg("pre-removed cached unit from fallback slotMap")
				break
			}
		}
	}

	for _, phase := range s.Phases {
		e.logger.Info().Str("phase", phase.Name).Msg("attack phase")
		if e.OnPhaseStart != nil {
			e.OnPhaseStart(phase.Name, targetEdge)
		}

		// Sort units: Spells -> Regular Units -> Abilities
		sortedUnits := make([]struct {
			unit      strategy.Unit
			isAbility bool
		}, 0, len(phase.Units))

		for _, u := range phase.Units {
			isAbility := u.Pattern == "Ability" || phase.Pattern == "Ability"
			sortedUnits = append(sortedUnits, struct {
				unit      strategy.Unit
				isAbility bool
			}{u, isAbility})
		}

		sort.SliceStable(sortedUnits, func(i, j int) bool {
			u1, u2 := sortedUnits[i], sortedUnits[j]
			n1, n2 := strings.ToLower(u1.unit.Name), strings.ToLower(u2.unit.Name)

			// Priority: Spells (0) > Others (1) > Abilities (2)
			p1, p2 := 1, 1
			if strings.Contains(n1, "spell") {
				p1 = 0
			} else if u1.isAbility {
				p1 = 2
			}
			if strings.Contains(n2, "spell") {
				p2 = 0
			} else if u2.isAbility {
				p2 = 2
			}

			return p1 < p2
		})

		// Pre-scan heroes if this is the heroes phase to ensure we only pick the top 4
		var heroMatches []struct {
			unit      strategy.Unit
			match     *vision.Match
			isAbility bool
		}

		isHeroesPhase := strings.Contains(phase.Name, "Heroes")
		usedSlots := make(map[int]bool) // x-coordinates of slots used in this phase

		// Setup lastBar with initial screen or refresh if needed (e.g. abilities)
		if lastBar.Closed() || lastBar.Empty() {
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

			if isAbility && phase.Name != "Abilities" {
				continue // Skip hero abilities during main phase loop (unless it is the dedicated Abilities phase)
			}

			isSpell := e.isSpell(unitName)
			isSiege := e.isSiege(unitName)
			isHero := e.isHero(unitName)

			// 1. Try manual labels cache first (100% reliable and instant)
			var match *vision.Match
			match = unitCache[unitName]
			if match == nil && isSiege {
				if m, ok := unitCache["siege machine"]; ok {
					match = m
					e.logger.Info().Str("unit", unit.Name).Interface("pos", m.Point).Msg("mapped to manual 'siege machine' slot")
				}
			}
			if match != nil {
				e.logger.Info().Str("unit", unit.Name).Interface("pos", match.Point).Msg("using manual label coordinates from cache")
			}

			// 2. Fallback to live template scanner only if not cached
			if match == nil {
				fileName := strings.ReplaceAll(unitName, " ", "_")
				tpl, ok := e.templates[fileName]
				if ok && !tpl.Empty() {
					if lastBar.Closed() || lastBar.Empty() {
						var err error
						lastBar, err = e.client.CaptureToMat()
						if err != nil {
							e.logger.Warn().Err(err).Msg("failed capture")
							continue
						}
					}

					findMatch := func(screen gocv.Mat, currentThreshold float32) *vision.Match {
						matches, _ := vision.MatchMultiScaleROICached(screen, tpl, fileName, 0.2, 1.2, 20, currentThreshold, barROI)
						if len(matches) > 0 {
							sort.Slice(matches, func(i, j int) bool { return matches[i].Confidence > matches[j].Confidence })
							for _, m := range matches {
								isUsed := false
								for ux := range usedSlots {
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

					thresholds := []float32{0.80, 0.70, 0.60, 0.55}
					if isHero {
						thresholds = []float32{0.85, 0.80, 0.75, 0.70}
					}
					if isSiege {
						thresholds = []float32{0.75, 0.65, 0.55, 0.50}
					}
					if isSpell {
						thresholds = []float32{0.80, 0.75, 0.70, 0.65}
					}

					for _, t := range thresholds {
						match = findMatch(lastBar, t)
						if match != nil {
							if strings.EqualFold(unitName, "grand warden") {
								shiftX := int(-6.0 * e.cal.ScaleX)
								shiftY := int(-16.0 * e.cal.ScaleY)
								match.Point.X += shiftX
								match.Point.Y += shiftY
								e.logger.Info().Int("orig_x", match.Point.X-shiftX).Int("orig_y", match.Point.Y-shiftY).
									Int("new_x", match.Point.X).Int("new_y", match.Point.Y).Msg("shifted grand warden click target upward/leftward")
							}
							break
						}
					}
				}
			}

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
				// BUT skip heroes — hero cards need template match to avoid slot theft
				if isHero {
					e.logger.Warn().Str("unit", unit.Name).Msg("hero has no template match, skipping to prevent slot theft")
				} else if len(slotMap[category]) > 0 {
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
			if e.OnUnitDeploy != nil {
				e.OnUnitDeploy(unit.Name, match.Point.X, slotY)
			}
			e.deployUnit(unit, match, pCfg, targetEdge, w, h, slotY, isAbility, lastBar, phase)
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
				isMain := e.isHero(name)

				if isMain {
					mainDeployments = append(mainDeployments, hm)
					e.logger.Debug().Str("unit", hm.unit.Name).Msg("added to mainDeployments")
				} else {
					bonusDeployments = append(bonusDeployments, hm)
					e.logger.Debug().Str("unit", hm.unit.Name).Msg("added to bonusDeployments")
				}
			}

			// 2. Sort main deployments by confidence descending and take top 4
			sort.Slice(mainDeployments, func(i, j int) bool {
				return mainDeployments[i].match.Confidence > mainDeployments[j].match.Confidence
			})

			limitMain := len(mainDeployments)

			e.logger.Debug().Int("main", len(mainDeployments)).Int("bonus", len(bonusDeployments)).Int("limit", limitMain).Msg("hero phase summary")
			for i := 0; i < limitMain; i++ {
				e.logger.Debug().Str("unit", mainDeployments[i].unit.Name).Int("x", mainDeployments[i].match.Point.X).Msg("deploying main hero")
				if e.OnUnitDeploy != nil {
					e.OnUnitDeploy(mainDeployments[i].unit.Name, mainDeployments[i].match.Point.X, slotY)
				}
				if e.deployUnit(mainDeployments[i].unit, mainDeployments[i].match, pCfg, targetEdge, w, h, slotY, false, lastBar, phase) {
					deployedHeroSlots = append(deployedHeroSlots, mainDeployments[i].match.Point)
				}
			}

			// 3. Sort bonus deployments by confidence descending
			sort.Slice(bonusDeployments, func(i, j int) bool {
				return bonusDeployments[i].match.Confidence > bonusDeployments[j].match.Confidence
			})

			for i := 0; i < len(bonusDeployments); i++ {
				e.logger.Debug().Str("unit", bonusDeployments[i].unit.Name).Int("x", bonusDeployments[i].match.Point.X).Msg("deploying bonus hero")
				if e.OnUnitDeploy != nil {
					e.OnUnitDeploy(bonusDeployments[i].unit.Name, bonusDeployments[i].match.Point.X, slotY)
				}
				if e.deployUnit(bonusDeployments[i].unit, bonusDeployments[i].match, pCfg, targetEdge, w, h, slotY, false, lastBar, phase) {
					deployedHeroSlots = append(deployedHeroSlots, bonusDeployments[i].match.Point)
				}
			}

			// 4. BULK ABILITY ACTIVATION: Activate all abilities now that all heroes are down
			if len(deployedHeroSlots) > 0 {
				e.logger.Info().Int("count", len(deployedHeroSlots)).Msg("bulk activating hero abilities...")
				time.Sleep(80 * time.Millisecond) // Wait for last hero to land (tightened)
				for _, pt := range deployedHeroSlots {
					e.logger.Info().Int("x", pt.X).Int("y", pt.Y).Msg("tapping hero icon for ability (bulk)")
					e.client.TapFast(pt.X, pt.Y, 2.0)
					time.Sleep(80 * time.Millisecond)
				}
			}
		}

		pDelay := time.Duration(phase.DelayAfterMS) * time.Millisecond
		if strings.Contains(phase.Name, "Heroes") || strings.Contains(phase.Name, "Siege") {
			pDelay = 80 * time.Millisecond
		}
		if pDelay > 200*time.Millisecond {
			pDelay = 1000 * time.Millisecond
		}
		if pDelay > 0 {
			time.Sleep(pDelay)
		}
	}

	// Final Sweep: Deploy any remaining event or mismatched troops in the bar
	sweepScreen, err := e.client.CaptureToMat()
	if err == nil {
		e.SweepRemainingSlots(sweepScreen, pCfg, targetEdge, w, h, mBarY, globalUsedSlots, siegeXs, slots, slotY)
		sweepScreen.Close()
	}

	// 5. Deployment Success Verifier & Retry Loop
	e.logger.Info().Msg("waiting for deployment to settle before verification...")
	// (post-sweep 2s settle removed — verifier immediately below handles server sync)

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
			// Skip Siege slots
			if slot.Category == "Siege" || e.isSiegeTapped(slot.X, w) {
				e.logger.Debug().Int("x", slot.X).Msg("skipping siege slot in verify")
				continue
			}

			// Get actual activity ratio to distinguish real troops from ability icons
			ratio := e.getSlotActivityRatio(verifyScreen, slot.X, slot.Y)

			// Empty slot (ratio < 0.08) — skip
			if ratio < 0.08 {
				continue
			}

			// Ability icon or UI artifact (ratio 0.08-0.4) — likely hero ability, skip
			if ratio < 0.4 {
				e.logger.Debug().Int("x", slot.X).Float64("ratio", ratio).Str("category", slot.Category).Msg("skipping low-ratio slot in verify (ability icon?)")
				continue
			}

			// Real troop/spell (ratio >= 0.4) — needs redeployment
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
			// Skip if this slot was already deployed by main phases (globalUsedSlots)
			alreadyDeployed := false
			for ux := range globalUsedSlots {
				if math.Abs(float64(slot.X-ux)) < float64(w)*0.04 {
					alreadyDeployed = true
					break
				}
			}
			if alreadyDeployed {
				e.logger.Debug().Int("x", slot.X).Msg("skipping main-phase-deployed slot in verify")
				continue
			}

			// Skip if this slot was a hero or siege we already deployed (cards stay on bar)
			isDeployedUnit := false
			for _, hp := range deployedHeroSlots {
				if math.Abs(float64(slot.X-hp.X)) < float64(w)*0.06 {
					isDeployedUnit = true
					break
				}
			}
			if !isDeployedUnit && e.isSiegeTapped(slot.X, w) {
				isDeployedUnit = true
			}
			if isDeployedUnit {
				e.logger.Debug().Int("x", slot.X).Msg("skipping already-deployed unit in verify")
				continue
			}

			e.logger.Info().Int("x", slot.X).Str("category", slot.Category).Msg("re-deploying remaining slot")

			// Select the slot
			e.client.TapFast(slot.X, slot.Y, 2.0)
			e.client.HumanSleep(35, 10)

			// Deploy
			var p1, p2 image.Point
			if slot.Category == "Spell" {
				if edge, ok := pCfg.SpellEdgesB[targetEdge]; ok {
					p1, p2 = edge.P1, edge.P2
					// Push slightly deeper towards center
					centerX, centerY := w/2, h/2
					pct := 0.20
					p1 = image.Pt(int(float64(p1.X)+float64(centerX-p1.X)*pct), int(float64(p1.Y)+float64(centerY-p1.Y)*pct))
					p2 = image.Pt(int(float64(p2.X)+float64(centerX-p2.X)*pct), int(float64(p2.Y)+float64(centerY-p2.Y)*pct))
				}
			}
			if p1.X == 0 && p1.Y == 0 {
				if edge, ok := pCfg.Edges[targetEdge]; ok {
					p1, p2 = edge.P1, edge.P2
				} else {
					p1 = image.Pt(w/2, h/2)
					p2 = p1
				}
			}

			maxRetryAttempts := 2
			for batch := 0; batch < maxRetryAttempts; batch++ {
				if p1 == p2 {
					// Use triple tap approach for point deployment
					e.client.TapTriple(p1.X, p1.Y, 12.0, p1.X, p1.Y, 12.0, p1.X, p1.Y, 12.0)
				} else {
					// Deploy three lines simultaneously with triple finger taps
					steps := 9
					for i := 0; i < steps; i += 3 {
						pct1 := float64(i) / float64(steps-1)
						pct2 := float64(i+1) / float64(steps-1)
						pct3 := float64(i+2) / float64(steps-1)
						tx1, ty1 := int(float64(p1.X)+float64(p2.X-p1.X)*pct1), int(float64(p1.Y)+float64(p2.Y-p1.Y)*pct1)
						tx2, ty2 := int(float64(p1.X)+float64(p2.X-p1.X)*pct2), int(float64(p1.Y)+float64(p2.Y-p1.Y)*pct2)
						tx3, ty3 := int(float64(p1.X)+float64(p2.X-p1.X)*pct3), int(float64(p1.Y)+float64(p2.Y-p1.Y)*pct3)
						e.client.TapTriple(tx1, ty1, 15.0, tx2, ty2, 15.0, tx3, ty3, 15.0)
					}
				}

				time.Sleep(80 * time.Millisecond) // server sync (tightened)
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
				time.Sleep(50 * time.Millisecond)
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
	size := int(25.0 * e.cal.ScaleX)
	ratio := slotActivity(screen, x, y, 860, size)
	isEmpty := ratio < 0.08

	e.logger.Debug().
		Int("x", x).
		Float64("active_ratio", ratio).
		Bool("is_empty", isEmpty).
		Msg("slot occupancy check")

	return isEmpty
}

func (e *Executor) getSlotActivityRatio(screen gocv.Mat, x, y int) float64 {
	size := int(25.0 * e.cal.ScaleX)
	return slotActivity(screen, x, y, 860, size)
}

// GetSlotActivityRatio is the exported wrapper for debug scripts.
func (e *Executor) GetSlotActivityRatio(screen gocv.Mat, x, y int) float64 {
	return e.getSlotActivityRatio(screen, x, y)
}

// GetSlotY returns the Y coordinate used for slot detection.
func (e *Executor) GetSlotY(h, mBarY int) int {
	slotY := mBarY + int(38.0*e.cal.ScaleY)
	if data, ok := readConfigJSON("manual_slots.json"); ok {
		var mConf struct {
			SlotY      int `json:"slot_y"`
			CardHeight int `json:"card_height"`
		}
		if json.Unmarshal(data, &mConf) == nil {
			if mConf.SlotY > 0 {
				slotY = mConf.SlotY
			} else if mConf.CardHeight > 0 {
				slotY = mBarY + mConf.CardHeight/2
			}
		}
	}
	return slotY
}

func (e *Executor) isHero(name string) bool {
	n := strings.ToLower(name)
	return strings.Contains(n, "king") || strings.Contains(n, "queen") || strings.Contains(n, "warden") ||
		strings.Contains(n, "prince") || strings.Contains(n, "duke") || strings.Contains(n, "champion")
}

func (e *Executor) isSiege(name string) bool {
	n := strings.ToLower(name)
	return strings.Contains(n, "slammer") || strings.Contains(n, "siege") || strings.Contains(n, "blimp") ||
		strings.Contains(n, "wrecker") || strings.Contains(n, "launcher") || strings.Contains(n, "drill")
}

func (e *Executor) isSpell(name string) bool {
	return strings.Contains(strings.ToLower(name), "spell")
}

func (e *Executor) isSiegeTapped(x int, w int) bool {
	if e.tappedSiegeXs == nil {
		return false
	}
	for sx := range e.tappedSiegeXs {
		if math.Abs(float64(x-sx)) < float64(w)*0.06 {
			return true
		}
	}
	return false
}

func (e *Executor) deployUnit(unit strategy.Unit, match *vision.Match, pCfg PrecisionConfig, targetEdge string, w, h, slotY int, isAbility bool, currentScreen gocv.Mat, phase strategy.Phase) bool {
	unitName := strings.ToLower(strings.TrimSpace(unit.Name))
	isSiege := e.isSiege(unitName)
	isHero := e.isHero(unitName)
	isSpell := e.isSpell(unitName)

	uPt := image.Pt(match.Point.X, slotY)

	// Siege Protection: NEVER tap the same siege slot twice, regardless of what unit thinks it is.
	if e.isSiegeTapped(uPt.X, w) {
		e.logger.Warn().Str("unit", unit.Name).Int("x", uPt.X).Msg("slot marked as siege, blocking tap for any unit")
		return false
	}

	e.logger.Info().Str("unit", unit.Name).Bool("ability", isAbility).Int("x", uPt.X).Int("y", uPt.Y).Float64("conf", match.Confidence).Msg("selecting unit")

	if isAbility {
		// Hero abilities: Verify hero is alive (slot not empty)
		if !currentScreen.Empty() {
			if e.isSlotEmpty(currentScreen, uPt.X, uPt.Y) {
				e.logger.Info().Str("unit", unit.Name).Msg("hero dead or ability used, skipping")
				return false
			}
		} else {
			verify, err := e.client.CaptureToMat()
			if err == nil {
				defer verify.Close()
				if e.isSlotEmpty(verify, uPt.X, uPt.Y) {
					e.logger.Info().Str("unit", unit.Name).Msg("hero dead or ability used, skipping")
					return false
				}
			}
		}

		e.client.HumanSleep(10, 5)
		// Reduced to ONE tap for ability to prevent "over and over"
		e.client.TapFast(uPt.X, uPt.Y, 4.0)
		e.client.HumanSleep(10, 5)
		return true
	}

	// 1. Verify slot is not empty before selection
	if !currentScreen.Empty() {
		if e.isSlotEmpty(currentScreen, uPt.X, uPt.Y) {
			e.logger.Info().Str("unit", unit.Name).Msg("slot is empty/already deployed, skipping")
			return false
		}
	} else {
		verify, err := e.client.CaptureToMat()
		if err == nil {
			defer verify.Close()
			if e.isSlotEmpty(verify, uPt.X, uPt.Y) {
				e.logger.Info().Str("unit", unit.Name).Msg("slot is empty/already deployed, skipping")
				return false
			}
		}
	}

	// 2. Prevent retapping Siege slot
	if isSiege && e.isSiegeTapped(uPt.X, w) {
		e.logger.Info().Str("unit", unit.Name).Msg("siege slot already tapped, skipping to avoid destruction")
		return false
	}

	// Select slot immediately (no pre-wait - sweep doesn't use it, matches successful pattern)
	jCard := e.addJitter(uPt, 8)
	e.logger.Debug().Int("x", jCard.X).Int("y", jCard.Y).Msg("tapping slot for selection (with jitter)")
	e.client.TapFast(jCard.X, jCard.Y, 2.0)

	if isSiege {
		if e.tappedSiegeXs == nil {
			e.tappedSiegeXs = make(map[int]bool)
		}
		e.tappedSiegeXs[uPt.X] = true
		e.logger.Debug().Int("x", uPt.X).Msg("marked siege as tapped in cache")
	}

	if isHero {
		e.client.HumanSleep(250, 50)
	} else {
		// Match sweep timing: 35ms for reliable selection-to-deploy window
		e.client.HumanSleep(35, 10)
	}

	// Deployment Logic
	isDragonDuke := strings.Contains(unitName, "duke")

	if isSpell {
		e.logger.Debug().Str("unit", unit.Name).Str("uPattern", unit.Pattern).Str("pPattern", phase.Pattern).Msg("spell pattern check")
		isFourSides := unit.Pattern == "FourSides" || phase.Pattern == "FourSides"
		if isFourSides {
			edges := []string{"TopRight", "BottomRight", "BottomLeft", "TopLeft"}
			// FAST SPAM: No screen checks between batches
			for i := 0; i < 4; i++ {
				currentEdge := edges[i]
				e.logger.Info().Str("unit", unit.Name).Str("edge", currentEdge).Msg("FourSides spell deployment")

				if targetPt, okT := pCfg.SpellTargets[currentEdge]; okT {
					// Deploy around spell target point
					for j := 0; j < 4; j++ {
						angle := float64(j) * 2.0 * math.Pi / 4.0
						radius := 18.0 * e.cal.ScaleX
						tx := targetPt.X + int(radius*math.Cos(angle))
						ty := targetPt.Y + int(radius*math.Sin(angle))
						jPt := e.addJitter(image.Pt(tx, ty), 6)
						e.client.TapFast(jPt.X, jPt.Y, 8.0)
						time.Sleep(50 * time.Millisecond) // Faster inter-tap
					}
				} else {
					// Legacy fallback to SpellEdgesB
					edge, ok := pCfg.SpellEdgesB[currentEdge]
					if !ok {
						edge, ok = pCfg.SpellEdgesA[currentEdge]
						if !ok {
							edge, _ = pCfg.Edges[currentEdge]
						}
					}

					p1, p2 := edge.P1, edge.P2

					// Apply inward offset from YAML if provided
					off := unit.Offset
					if off == 0 {
						off = phase.Offset
					}
					if off > 0 {
						// Push toward screen center (true inward)
						centerX, centerY := w/2, h/2
						pct := float64(off) / 300.0 // Scaling for center push
						p1 = image.Pt(int(float64(p1.X)+float64(centerX-p1.X)*pct), int(float64(p1.Y)+float64(centerY-p1.Y)*pct))
						p2 = image.Pt(int(float64(p2.X)+float64(centerX-p2.X)*pct), int(float64(p2.Y)+float64(centerY-p2.Y)*pct))
					}
					e.logger.Debug().Interface("p1", p1).Interface("p2", p2).Msg("FourSides spell edge coords (inward)")

					for j := 0; j < 4; j++ { // 4 taps per side
						pct := float64(j) / 3.0
						tx, ty := int(float64(p1.X)+float64(p2.X-p1.X)*pct), int(float64(p1.Y)+float64(p2.Y-p1.Y)*pct)
						jPt := e.addJitter(image.Pt(tx, ty), 6)
						e.client.TapFast(jPt.X, jPt.Y, 8.0)
						time.Sleep(50 * time.Millisecond) // Faster inter-tap
					}
				}
				time.Sleep(45 * time.Millisecond) // Faster inter-side
			}
			time.Sleep(200 * time.Millisecond)
			return true
		}

		spellTarget, okT := pCfg.SpellTargets[targetEdge]
		isPointPattern := unit.Pattern == "Point" || phase.Pattern == "Point"

		e.logger.Debug().
			Str("unit", unit.Name).
			Str("targetEdge", targetEdge).
			Bool("isPointPattern", isPointPattern).
			Bool("hasSpellTargetPoint", okT).
			Msg("Determining spell deployment parameters")

		if isPointPattern && okT {
			maxSpells := 5 // Default fallback to ensure all spells deploy
			if unit.Amount != "All" && unit.Amount != "" {
				if val, err := strconv.Atoi(unit.Amount); err == nil {
					maxSpells = val
				}
			}

			e.logger.Info().Str("unit", unit.Name).Interface("point", spellTarget).Int("count", maxSpells).Msg("Deploying spells clustered around point target")

			// Deploy spells clustered slightly around the spellTarget
			points := make([]image.Point, 0, maxSpells)
			for i := 0; i < maxSpells; i++ {
				var offset image.Point
				if maxSpells > 1 {
					angle := float64(i) * 2.0 * math.Pi / float64(maxSpells)
					radius := 18.0 * e.cal.ScaleX
					offset = image.Pt(int(radius*math.Cos(angle)), int(radius*math.Sin(angle)))
				}
				pt := image.Pt(spellTarget.X+offset.X, spellTarget.Y+offset.Y)
				points = append(points, e.addJitter(pt, 6))
			}

			for idx, pt := range points {
				e.logger.Info().Str("unit", unit.Name).Int("idx", idx).Interface("pt", pt).Msg("tapping spell target point")
				e.client.TapFast(pt.X, pt.Y, 8.0)

				if idx < len(points)-1 {
					e.client.HumanSleep(180, 40) // Spacing between spell placements
				}
			}
			return true
		}

		// Line deployment: Rage Spell -> SpellEdgesA, Ice Spell / Others -> SpellEdgesB
		var edge ManualEdge
		var ok bool
		var selectedLine string
		isRage := strings.Contains(unitName, "rage")

		if isRage {
			edge, ok = pCfg.SpellEdgesA[targetEdge]
			selectedLine = "SpellEdgesA (Line A)"
			if !ok {
				edge, ok = pCfg.SpellEdgesB[targetEdge]
				selectedLine = "SpellEdgesB (Line B fallback)"
			}
		} else {
			edge, ok = pCfg.SpellEdgesB[targetEdge]
			selectedLine = "SpellEdgesB (Line B)"
			if !ok {
				edge, ok = pCfg.SpellEdgesA[targetEdge]
				selectedLine = "SpellEdgesA (Line A fallback)"
			}
		}

		e.logger.Info().
			Str("unit", unit.Name).
			Str("line", selectedLine).
			Bool("found", ok).
			Interface("coords", edge).
			Msg("Routing spell to deployment line")

		if ok {
			maxSpells := 5 // Default fallback to ensure all spells deploy
			if unit.Amount != "All" && unit.Amount != "" {
				if val, err := strconv.Atoi(unit.Amount); err == nil {
					maxSpells = val
				}
			}

			// Special Custom Layout for Rage Spells (3 on Line A, 2 on Line B)
			if isRage && maxSpells == 5 {
				edgeA, okA := pCfg.SpellEdgesA[targetEdge]
				edgeB, okB := pCfg.SpellEdgesB[targetEdge]
				if okA && okB {
					e.logger.Info().Msg("Deploying Rage Spells: 3 on Line A (even), 2 on Line B (far)")

					p1A, p2A := edgeA.P1, edgeA.P2

					pointsA := []image.Point{
						e.addJitter(image.Pt(int(float64(p1A.X)+float64(p2A.X-p1A.X)*0.10), int(float64(p1A.Y)+float64(p2A.Y-p1A.Y)*0.10)), 8),
						e.addJitter(image.Pt(int(float64(p1A.X)+float64(p2A.X-p1A.X)*0.50), int(float64(p1A.Y)+float64(p2A.Y-p1A.Y)*0.50)), 8),
						e.addJitter(image.Pt(int(float64(p1A.X)+float64(p2A.X-p1A.X)*0.90), int(float64(p1A.Y)+float64(p2A.Y-p1A.Y)*0.90)), 8),
					}

					p1B, p2B := edgeB.P1, edgeB.P2

					pointsB := []image.Point{
						e.addJitter(image.Pt(int(float64(p1B.X)+float64(p2B.X-p1B.X)*0.10), int(float64(p1B.Y)+float64(p2B.Y-p1B.Y)*0.10)), 8),
						e.addJitter(image.Pt(int(float64(p1B.X)+float64(p2B.X-p1B.X)*0.90), int(float64(p1B.Y)+float64(p2B.Y-p1B.Y)*0.90)), 8),
					}

					for idx, pt := range pointsA {
						e.logger.Info().Int("idx", idx).Interface("pt", pt).Msg("tapping Rage Spell Line A")
						e.client.TapFast(pt.X, pt.Y, 8.0)
						e.client.HumanSleep(180, 40)
					}
					for idx, pt := range pointsB {
						e.logger.Info().Int("idx", idx+3).Interface("pt", pt).Msg("tapping Rage Spell Line B")
						e.client.TapFast(pt.X, pt.Y, 8.0)
						if idx < len(pointsB)-1 {
							e.client.HumanSleep(180, 40)
						}
					}
					return true
				}
			}

			p1, p2 := edge.P1, edge.P2

			off := unit.Offset
			if off == 0 {
				off = phase.Offset
			}
			if off > 0 {
				centerX, centerY := w/2, h/2
				pct := float64(off)/150.0 + 0.12 // Pushed deeper/further in
				p1 = image.Pt(int(float64(p1.X)+float64(centerX-p1.X)*pct), int(float64(p1.Y)+float64(centerY-p1.Y)*pct))
				p2 = image.Pt(int(float64(p2.X)+float64(centerX-p2.X)*pct), int(float64(p2.Y)+float64(centerY-p2.Y)*pct))
				e.logger.Debug().Interface("offset_p1", p1).Interface("offset_p2", p2).Msg("Applied inward offset to spell line")
			}

			// Distribute spell targets at 0.22, 0.50, 0.78 to be slightly tighter but not overlap
			points := make([]image.Point, 0, maxSpells)
			for i := 0; i < maxSpells; i++ {
				pct := 0.5
				if maxSpells > 1 {
					pct = 0.22 + float64(i)*0.56/float64(maxSpells-1) // 0.22, 0.50, 0.78
				}
				tx := int(float64(p1.X) + float64(p2.X-p1.X)*pct)
				ty := int(float64(p1.Y) + float64(p2.Y-p1.Y)*pct)
				points = append(points, e.addJitter(image.Pt(tx, ty), 8))
			}

			for idx, pt := range points {
				e.logger.Info().Str("unit", unit.Name).Int("idx", idx).Interface("pt", pt).Msg("tapping spell target line")
				e.client.TapFast(pt.X, pt.Y, 8.0)

				if idx < len(points)-1 {
					e.client.HumanSleep(180, 40) // Spacing between spell placements
				}
			}
		} else {
			e.logger.Warn().Str("unit", unit.Name).Msg("No spell deployment line or target point configured for edge")
		}
		return true
	} else {
		var p1, p2 image.Point
		deploymentEdge := targetEdge

		isFourSides := unit.Pattern == "FourSides" || phase.Pattern == "FourSides"
		if isFourSides {
			edges := []string{"TopRight", "BottomRight", "BottomLeft", "TopLeft"}
			// ULTRA FAST DENSE SPAM
			for _, edgeName := range edges {
				edge, ok := pCfg.Edges[edgeName]
				if !ok {
					continue
				}
				p1, p2 = edge.P1, edge.P2

				// Apply YAML offset
				off := unit.Offset
				if off == 0 {
					off = phase.Offset
				}
				if off > 0 {
					if target, ok := pCfg.HeroTargets[edgeName]; ok {
						pct := float64(off) / 200.0
						p1 = image.Pt(int(float64(p1.X)+float64(target.X-p1.X)*pct), int(float64(p1.Y)+float64(target.Y-p1.Y)*pct))
						p2 = image.Pt(int(float64(p2.X)+float64(target.X-p2.X)*pct), int(float64(p2.Y)+float64(target.Y-p2.Y)*pct))
					}
				}

				e.logger.Info().Str("unit", unit.Name).Str("edge", edgeName).Msg("FourSides rapid dense spam")
				e.logger.Debug().Interface("p1", p1).Interface("p2", p2).Msg("FourSides edge coords")
				steps := 12 // 4 batches of 3 = 12 taps per side. 48 taps total.
				for i := 0; i < steps; i += 3 {
					pct1 := float64(i) / float64(steps-1)
					pct2 := float64(i+1) / float64(steps-1)
					pct3 := float64(i+2) / float64(steps-1)
					tx1, ty1 := int(float64(p1.X)+float64(p2.X-p1.X)*pct1), int(float64(p1.Y)+float64(p2.Y-p1.Y)*pct1)
					tx2, ty2 := int(float64(p1.X)+float64(p2.X-p1.X)*pct2), int(float64(p1.Y)+float64(p2.Y-p1.Y)*pct2)
					tx3, ty3 := int(float64(p1.X)+float64(p2.X-p1.X)*pct3), int(float64(p1.Y)+float64(p2.Y-p1.Y)*pct3)
					j1 := e.addJitter(image.Pt(tx1, ty1), 8)
					j2 := e.addJitter(image.Pt(tx2, ty2), 8)
					j3 := e.addJitter(image.Pt(tx3, ty3), 8)
					e.client.TapTriple(j1.X, j1.Y, 12.0, j2.X, j2.Y, 12.0, j3.X, j3.Y, 12.0)
					time.Sleep(45 * time.Millisecond)
				}
			}
			time.Sleep(120 * time.Millisecond)
			return true
		}

		if isDragonDuke && !isAbility {
			// Dragon Duke deploys to an ADJACENT edge (not the target edge).
			// Note: the NEW orchestrator path (HeroManager.resolveHeroTarget)
			// no longer uses this branch — Duke falls through to the chosen
			// edge there. This legacy branch is preserved for callers using
			// DeployDynamic directly and for the per-attack OnDukePick
			// observer.
			adjacentEdges := map[string][]string{
				"TopLeft":     {"TopRight", "BottomLeft"},
				"TopRight":    {"TopLeft", "BottomRight"},
				"BottomLeft":  {"TopLeft", "BottomRight"},
				"BottomRight": {"TopRight", "BottomLeft"},
			}
			chosen := targetEdge
			if adj, ok := adjacentEdges[targetEdge]; ok && len(adj) > 0 {
				chosen = adj[rand.Intn(len(adj))]
			}
			deploymentEdge = chosen
			e.logger.Info().Str("target", targetEdge).Str("duke_edge", deploymentEdge).Msg("Dragon Duke adjacent-edge placement")
			if e.OnDukePick != nil {
				e.OnDukePick(targetEdge, chosen)
			}

			// Deploy along the edge LINE (not hero target) for adjacent-edge placement
			if edge, ok := pCfg.Edges[deploymentEdge]; ok {
				scaled := ScaleEdge(edge, pCfg.Width, pCfg.Height, w, h)
				p1, p2 = scaled.P1, scaled.P2
				e.logger.Info().Str("edge", deploymentEdge).Interface("p1", p1).Interface("p2", p2).Msg("placing Dragon Duke along adjacent edge line")
			}
		}

		// Deployment target logic:
		// - Dragon Duke: adjacent edge line (already set above)
		// - Heroes: HeroTargets (interior point for ability range)
		// - Siege: edge LINE (open ground, like troops)
		// - Troops: edge LINE (open ground)
		// All units deploy along edge LINE for open ground
		// Heroes: deploy at outer edge point (p1) for guaranteed open ground
		// Troops/Siege: deploy along full edge line
		if isHero && !(isDragonDuke && !isAbility) {
			// Use outer edge point (p1) for hero deployment - guaranteed open ground
			if edge, ok := pCfg.Edges[deploymentEdge]; ok {
				scaled := ScaleEdge(edge, pCfg.Width, pCfg.Height, w, h)
				p1, p2 = scaled.P1, scaled.P1 // Single point at outer edge
			}
		} else if !isDragonDuke || isAbility {
			// Troops + Siege: always use edge LINE for reliable open-ground deployment
			if edge, ok := pCfg.Edges[deploymentEdge]; ok {
				scaled := ScaleEdge(edge, pCfg.Width, pCfg.Height, w, h)
				p1, p2 = scaled.P1, scaled.P2
			}
		}

		// Heroes: point deployment at outer edge (open ground)
		// Siege + Troops: line deployment along edge (open ground)
		if isHero {
			jPt := e.addJitter(p1, 10)
			e.logger.Info().Str("unit", unit.Name).Int("x", jPt.X).Int("y", jPt.Y).Msg("deploying hero")
			// Triple tap for reliable registration (matches sweep pattern)
			j2 := e.addJitter(p1, 10)
			j3 := e.addJitter(p1, 10)
			e.client.TapTriple(jPt.X, jPt.Y, 12.0, j2.X, j2.Y, 12.0, j3.X, j3.Y, 12.0)
			e.client.HumanSleep(200, 40)
		} else {
			// Siege + Regular troops / spam units / event troops
			maxTaps := 12 // Default fallback
			if unit.Amount != "All" && unit.Amount != "" {
				if val, err := strconv.Atoi(unit.Amount); err == nil {
					maxTaps = val
				}
			} else {
				if strings.Contains(unitName, "balloon") {
					maxTaps = 12
				} else if strings.Contains(unitName, "electro") || strings.Contains(unitName, "dragon") {
					maxTaps = 12
				}
			}

			if p1 == p2 { // Point deployment
				e.logger.Info().Str("unit", unit.Name).Int("count", maxTaps).Msg("deploying troop point batch")
				for i := 0; i < maxTaps; {
					rem := maxTaps - i
					if rem >= 3 {
						j1 := e.addJitter(p1, 8)
						j2 := e.addJitter(p1, 8)
						j3 := e.addJitter(p1, 8)
						e.client.TapTriple(j1.X, j1.Y, 12.0, j2.X, j2.Y, 12.0, j3.X, j3.Y, 12.0)
						i += 3
					} else if rem == 2 {
						j1 := e.addJitter(p1, 8)
						j2 := e.addJitter(p1, 8)
						e.client.TapDual(j1.X, j1.Y, 12.0, j2.X, j2.Y, 12.0)
						i += 2
					} else {
						j1 := e.addJitter(p1, 8)
						e.client.TapFast(j1.X, j1.Y, 12.0)
						i += 1
					}
					e.client.HumanSleep(200, 40)
				}
			} else { // Line deployment
				e.logger.Info().Str("unit", unit.Name).Int("count", maxTaps).Msg("deploying troop line precisely")
				points := make([]image.Point, 0, maxTaps)
				for i := 0; i < maxTaps; i++ {
					pct := 0.5
					if maxTaps > 1 {
						pct = float64(i) / float64(maxTaps-1)
					}
					tx := int(float64(p1.X) + float64(p2.X-p1.X)*pct)
					ty := int(float64(p1.Y) + float64(p2.Y-p1.Y)*pct)
					points = append(points, e.addJitter(image.Pt(tx, ty), 10))
				}

				// Randomize direction: 50% chance to deploy in reverse direction
				if rand.Float64() < 0.5 {
					e.logger.Debug().Msg("reversing deployment order along line for human variability")
					for i, j := 0, len(points)-1; i < j; i, j = i+1, j-1 {
						points[i], points[j] = points[j], points[i]
					}
				}

				// Deploy in batches of 3 with verification (match sweep pattern)
				for i := 0; i < len(points); {
					rem := len(points) - i
					if rem >= 3 {
						e.client.TapTriple(points[i].X, points[i].Y, 15.0, points[i+1].X, points[i+1].Y, 15.0, points[i+2].X, points[i+2].Y, 15.0)
						i += 3
					} else if rem == 2 {
						e.client.TapDual(points[i].X, points[i].Y, 15.0, points[i+1].X, points[i+1].Y, 15.0)
						i += 2
					} else {
						e.client.TapFast(points[i].X, points[i].Y, 15.0)
						i += 1
					}

					// Variable human inter-tap pacing (match sweep's 200ms for reliable registration)
					sleepBase := 200
					sleepDev := 40
					if rand.Float64() < 0.10 {
						// 10% chance of a longer pause representing human finger relocation
						sleepBase = 300
						sleepDev = 50
					}
					e.client.HumanSleep(sleepBase, sleepDev)

					// Verify slot emptied after each batch (match sweep pattern)
					if i < len(points) {
						if verify, err := e.client.CaptureToMat(); err == nil {
							empty := e.isSlotEmpty(verify, match.Point.X, slotY)
							verify.Close()
							if empty {
								e.logger.Info().Str("unit", unit.Name).Msg("slot emptied mid-deploy, stopping batch")
								break
							}
						}
					}
				}
			}
		}
		time.Sleep(15 * time.Millisecond)
		return true
	}
}

func (e *Executor) CalculateInBetween(edge string, offset int, bT, bB, bL, bR, fT, fB, fL, fR image.Point) (p1, p2 image.Point) {
	pct := float64(offset) / 100.0
	var baseP1, baseP2, fieldP1, fieldP2 image.Point
	switch edge {
	case "TopRight":
		baseP1, baseP2, fieldP1, fieldP2 = bT, bR, fT, fR
	case "BottomRight":
		baseP1, baseP2, fieldP1, fieldP2 = bR, bB, fR, fB
	case "BottomLeft":
		baseP1, baseP2, fieldP1, fieldP2 = bB, bL, fB, fL
	case "TopLeft":
		baseP1, baseP2, fieldP1, fieldP2 = bL, bT, fL, fT
	default:
		baseP1, baseP2, fieldP1, fieldP2 = bT, bR, fT, fR
	}

	p1 = image.Pt(
		int(float64(baseP1.X)+float64(fieldP1.X-baseP1.X)*pct),
		int(float64(baseP1.Y)+float64(fieldP1.Y-baseP1.Y)*pct),
	)
	p2 = image.Pt(
		int(float64(baseP2.X)+float64(fieldP2.X-baseP2.X)*pct),
		int(float64(baseP2.Y)+float64(fieldP2.Y-baseP2.Y)*pct),
	)
	return
}

func (e *Executor) MaximizeLineSpread(p1, p2 image.Point, w, mBarY int) (image.Point, image.Point) {
	return p1, p2 // Simplified for now, just return as is
}
func (e *Executor) EndBattle() error {
	// Try to load StallConfig for pinpoint accuracy
	var sCfg StallConfig
	pinpoint := false
	if data, ok := readConfigJSON("stall_config.json"); ok && json.Unmarshal(data, &sCfg) == nil {
		pinpoint = true
	}

	ex, ey := e.cal.ScaleRef(34, 588)
	if pinpoint {
		scaleX, scaleY := float64(e.cal.PhysicalW)/float64(sCfg.RefWidth), float64(e.cal.PhysicalH)/float64(sCfg.RefHeight)
		ex, ey = int(float64(sCfg.EndButton.X)*scaleX), int(float64(sCfg.EndButton.Y)*scaleY)
		e.logger.Info().Int("x", ex).Int("y", ey).Msg("using pinpoint End Battle button")
	} else {
		screen, err := e.client.CaptureToMat()
		if err == nil {
			defer screen.Close()
			positions := []image.Point{
				{X: 34, Y: 588},
				{X: 67, Y: 570},
				{X: 112, Y: 408},
			}
			for _, pos := range positions {
				sx, sy := e.cal.ScaleRef(pos.X, pos.Y)
				if sx >= 0 && sy >= 0 && sx < screen.Cols() && sy < screen.Rows() {
					b := screen.GetUCharAt(sy, sx*3)
					g := screen.GetUCharAt(sy, sx*3+1)
					r := screen.GetUCharAt(sy, sx*3+2)
					if r > 130 && g < 110 && b < 110 {
						ex, ey = sx, sy
						e.logger.Info().Int("x", pos.X).Int("y", pos.Y).Msg("dynamically detected End Battle button location")
						break
					}
				}
			}
		}
	}
	if err := e.client.TapHuman(ex, ey, 5.0); err != nil {
		return err
	}
	time.Sleep(120 * time.Millisecond) // confirmation dialog (tightened)

	// Tap green "End Battle" or "Okay" confirmation button
	okX, okY := e.cal.ScaleRef(520, 430)
	if pinpoint {
		scaleX, scaleY := float64(e.cal.PhysicalW)/float64(sCfg.RefWidth), float64(e.cal.PhysicalH)/float64(sCfg.RefHeight)
		okX, okY = int(float64(sCfg.ConfirmBtn.X)*scaleX), int(float64(sCfg.ConfirmBtn.Y)*scaleY)
		e.logger.Info().Int("x", okX).Int("y", okY).Msg("using pinpoint Confirm button")
	}
	if err := e.client.TapHuman(okX, okY, 5.0); err != nil {
		return err
	}
	time.Sleep(120 * time.Millisecond) // screen transition (tightened)
	return nil
}

func (e *Executor) ReturnHome() error {
	hx, hy := e.cal.ScaleRef(430, 566)
	if err := e.client.TapHuman(hx, hy, 5.0); err != nil {
		return err
	}
	time.Sleep(900 * time.Millisecond)
	screen, err := e.client.CaptureToMat()
	if err != nil {
		return err
	}
	defer screen.Close()
	state, _ := e.classify(screen)
	if state != game.StateMainVillage {
		return fmt.Errorf("did not return home")
	}
	return nil
}

func (e *Executor) WaitForBattleEnd(timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(1000 * time.Millisecond)
	defer ticker.Stop()

	// Stall Detection State
	lastPct := 0
	lastPctTime := time.Now()
	stallLimit := time.Duration(e.cfg.StallTimerSeconds) * time.Second

	// Load Stall Config for ROI
	var sCfg StallConfig
	hasStallROI := false
	if data, ok := readConfigJSON("stall_config.json"); ok && json.Unmarshal(data, &sCfg) == nil {
		hasStallROI = !sCfg.PercentROI.Empty()
	}

	// Prepare OCR
	tStore, err := game.NewTemplateStore(paths.Resolve("templates"))
	if err != nil {
		e.logger.Error().Err(err).Msg("failed to create template store for stall detection")
		return false
	}
	tStore.LoadTemplates()
	lootRec := game.NewLootRecognizer(e.cal, tStore, e.logger)
	defer lootRec.Close()

	var pRoi image.Rectangle
	if hasStallROI {
		scaleX, scaleY := float64(e.cal.PhysicalW)/float64(sCfg.RefWidth), float64(e.cal.PhysicalH)/float64(sCfg.RefHeight)
		pRoi = image.Rect(
			int(float64(sCfg.PercentROI.Min.X)*scaleX),
			int(float64(sCfg.PercentROI.Min.Y)*scaleY),
			int(float64(sCfg.PercentROI.Max.X)*scaleX),
			int(float64(sCfg.PercentROI.Max.Y)*scaleY),
		)
	}

	for {
		select {
		case <-ticker.C:
			screen, err := e.client.CaptureToMat()
			if err != nil {
				continue
			}
			state, _ := e.classify(screen)

			if state == game.StateBattleEnd || state == game.StateReturnHome {
				screen.Close()
				return true
			}

			// Check Stall
			if e.cfg.StallTimerSeconds > 0 && hasStallROI {
				currentPct := lootRec.ReadDestructionPercentage(screen, pRoi)
				if currentPct > lastPct {
					lastPct = currentPct
					lastPctTime = time.Now()
					e.logger.Info().Int("percent", currentPct).Msg("destruction increased, resetting stall timer")
				} else {
					elapsed := time.Since(lastPctTime)
					if elapsed > stallLimit {
						e.logger.Warn().Int("last_pct", lastPct).Dur("elapsed", elapsed).Msg("stall detected, ending battle!")
						screen.Close()
						e.EndBattle()
						return true
					}
				}
			}

			screen.Close()
			if time.Now().After(deadline) {
				return false
			}
		}
	}
}

func (e *Executor) SweepRemainingSlots(screen gocv.Mat, pCfg PrecisionConfig, targetEdge string, w, h int, mBarY int, usedSlots map[int]bool, siegeXs []int, allSlots []TroopSlot, slotY int) {
	e.logger.Info().Msg("starting sweep of remaining/event slots...")

	// Scan ONLY known slot positions from the initial parse — no fixed-step scanning
	for _, slot := range allSlots {
		x := slot.X

		// Skip Siege slots
		if slot.Category == "Siege" || e.isSiegeTapped(x, w) {
			continue
		}

		// Check if this X coordinate is already close to a slot we processed
		alreadyUsed := false
		for ux := range usedSlots {
			if math.Abs(float64(x-ux)) < float64(w)*0.04 {
				alreadyUsed = true
				break
			}
		}
		if alreadyUsed {
			continue
		}

		// If the slot at (x, slotY) is not empty, it contains a troop/spell to deploy!
		if !e.isSlotEmpty(screen, x, slotY) {
			e.logger.Info().Int("x", x).Str("category", slot.Category).Msg("found undeployed troop slot during sweep, deploying...")

			// Select the slot (click it)
			e.client.TapFast(x, slotY, 2.0)
			e.client.HumanSleep(35, 10)

			// Determine deployment target (default to line spread along target edge)
			var p1, p2 image.Point
			if slot.Category == "Spell" {
				if edge, ok := pCfg.SpellEdgesB[targetEdge]; ok {
					p1, p2 = edge.P1, edge.P2
				}
			}
			if p1.X == 0 && p1.Y == 0 {
				if edge, ok := pCfg.Edges[targetEdge]; ok {
					p1, p2 = edge.P1, edge.P2
				} else {
					p1 = image.Pt(w/2, h/2)
					p2 = p1
				}
			}

			maxSweepAttempts := 2
			for batch := 0; batch < maxSweepAttempts; batch++ {
				if p1 == p2 {
					e.client.TapTriple(p1.X, p1.Y, 12.0, p1.X, p1.Y, 12.0, p1.X, p1.Y, 12.0)
				} else {
					steps := 9
					for i := 0; i < steps; i += 3 {
						pct1 := float64(i) / float64(steps-1)
						pct2 := float64(i+1) / float64(steps-1)
						pct3 := float64(i+2) / float64(steps-1)
						tx1, ty1 := int(float64(p1.X)+float64(p2.X-p1.X)*pct1), int(float64(p1.Y)+float64(p2.Y-p1.Y)*pct1)
						tx2, ty2 := int(float64(p1.X)+float64(p2.X-p1.X)*pct2), int(float64(p1.Y)+float64(p2.Y-p1.Y)*pct2)
						tx3, ty3 := int(float64(p1.X)+float64(p2.X-p1.X)*pct3), int(float64(p1.Y)+float64(p2.Y-p1.Y)*pct3)
						e.client.TapTriple(tx1, ty1, 15.0, tx2, ty2, 15.0, tx3, ty3, 15.0)
					}
				}

				time.Sleep(200 * time.Millisecond)
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
				e.client.TapFast(x, slotY, 2.0)
				time.Sleep(50 * time.Millisecond)
			}

			usedSlots[x] = true
			e.client.HumanSleep(35, 10)
		}
	}
}

type TroopSlot struct {
	X        int
	Y        int
	Category string // "Troop", "Siege", "Hero", "Spell", "CC"
}

func (e *Executor) ParseLayout(screen gocv.Mat, pCfg PrecisionConfig, w, h, mBarY int) []TroopSlot {
	var activeXs []int
	slotY := mBarY + int(38.0*e.cal.ScaleY)

	// 1. Try to load manual calibration for 100% precision
	if data, ok := readConfigJSON("manual_slots.json"); ok {
		var mConf struct {
			CardWidth  int   `json:"card_width"`
			CardHeight int   `json:"card_height"`
			SlotXs     []int `json:"slot_xs"`
			SlotY      int   `json:"slot_y"`
		}
		if json.Unmarshal(data, &mConf) == nil {
			e.logger.Info().Int("slots", len(mConf.SlotXs)).Msg("using 100% precise manual slot mapping")
			if mConf.SlotY > 0 {
				slotY = mConf.SlotY
			} else if mConf.CardHeight > 0 {
				slotY = mBarY + mConf.CardHeight/2
			}
			// Verify each slot actually has content (isn't empty/dark)
			for _, x := range mConf.SlotXs {
				if !e.isSlotEmpty(screen, x, slotY) {
					activeXs = append(activeXs, x)
				}
			}
		}
	}

	// 2. Fallback to grid detection if manual config missing or empty
	if len(activeXs) == 0 {
		e.logger.Info().Msg("manual calibration missing/empty, falling back to grid detection")
		step := int(75.0 * e.cal.ScaleX)
		startX := int(40.0 * e.cal.ScaleX)
		for x := startX; x < w-20; x += step {
			if !e.isSlotEmpty(screen, x, slotY) {
				activeXs = append(activeXs, x)
			}
		}
	}
	e.logger.Info().Ints("active_xs", activeXs).Msg("detected active slots in bar")

	if len(activeXs) == 0 {
		return nil
	}

	// 2. Identify Hero and Spell anchors using template matching
	barROI := image.Rect(0, mBarY, w, h)
	heroNames := []string{"barbarian_king", "archer_queen", "grand_warden", "minion_prince", "dragon_duke", "royal_champion"}
	spellNames := []string{"rage_spell", "ice_spell", "freeze_spell", "heal_spell", "jump_spell", "poison_spell", "recall_spell", "revive_spell"}
	siegeNames := []string{"stone_slammer", "battle_blimp", "wall_wrecker", "siege_barracks", "log_launcher"}

	matchedHeroes := make(map[int]bool)
	matchedSpells := make(map[int]bool)
	matchedSieges := make(map[int]bool)

	// Search Heroes
	for _, name := range heroNames {
		tpl, ok := e.templates[name]
		if !ok || tpl.Empty() {
			continue
		}
		matches, _ := vision.MatchMultiScaleROICached(screen, tpl, name, 0.2, 1.2, 20, 0.65, barROI)
		for _, m := range matches {
			matchedHeroes[m.Point.X] = true
		}
	}

	// Search Spells
	for _, name := range spellNames {
		tpl, ok := e.templates[name]
		if !ok || tpl.Empty() {
			continue
		}
		matches, _ := vision.MatchMultiScaleROICached(screen, tpl, name, 0.2, 1.2, 20, 0.75, barROI)
		for _, m := range matches {
			matchedSpells[m.Point.X] = true
		}
	}

	// Search Sieges
	for _, name := range siegeNames {
		tpl, ok := e.templates[name]
		if !ok || tpl.Empty() {
			continue
		}
		matches, _ := vision.MatchMultiScaleROICached(screen, tpl, name, 0.2, 1.2, 20, 0.55, barROI)
		for _, m := range matches {
			matchedSieges[m.Point.X] = true
		}
	}

	// 3. Classify slots based on anchors
	firstHeroX := 9999
	lastHeroX := -1
	for hx := range matchedHeroes {
		if hx < firstHeroX {
			firstHeroX = hx
		}
		if hx > lastHeroX {
			lastHeroX = hx
		}
	}

	firstSpellX := 9999
	for sx := range matchedSpells {
		if sx < firstSpellX {
			firstSpellX = sx
		}
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
			if math.Abs(float64(x-sx)) < float64(w)*0.06 {
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

		if x >= firstSpellX-int(30.0*e.cal.ScaleX) {
			cat = "Spell"
		} else if x >= firstHeroX-int(30.0*e.cal.ScaleX) && x <= lastHeroX+int(30.0*e.cal.ScaleX) {
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
		if slots[lastIdx].Category == "Spell" && slots[lastIdx].X > w-int(100.0*e.cal.ScaleX) {
			slots[lastIdx].Category = "CC"
			e.logger.Info().Int("x", slots[lastIdx].X).Msg("classified last slot as CC")
		}
	}

	return slots
}

// hasColorSignature checks if the image contains enough pixels of a specific color profile.
// Supported: "cyan" (EDrag), "pink" (Rage).
func (e *Executor) hasColorSignature(img gocv.Mat, colorType string) bool {
	if img.Empty() {
		return false
	}

	hsv := gocv.NewMat()
	defer hsv.Close()
	gocv.CvtColor(img, &hsv, gocv.ColorBGRToHSV)

	var lower, upper gocv.Scalar
	minRatio := 0.04 // 4% coverage required

	switch colorType {
	case "cyan": // EDrag Blue/Cyan
		lower = gocv.NewScalar(80, 50, 50, 0)
		upper = gocv.NewScalar(110, 255, 255, 0)
	case "pink", "magenta": // Rage Pink/Magenta
		lower = gocv.NewScalar(130, 50, 50, 0)
		upper = gocv.NewScalar(175, 255, 255, 0)
	case "red": // Balloon / Dragon Duke Red
		lower1 := gocv.NewScalar(0, 70, 50, 0)
		upper1 := gocv.NewScalar(15, 255, 255, 0)
		lower2 := gocv.NewScalar(160, 70, 50, 0)
		upper2 := gocv.NewScalar(180, 255, 255, 0)

		mask1, mask2 := gocv.NewMat(), gocv.NewMat()
		defer mask1.Close()
		defer mask2.Close()
		gocv.InRangeWithScalar(hsv, lower1, upper1, &mask1)
		gocv.InRangeWithScalar(hsv, lower2, upper2, &mask2)

		totalMask := gocv.NewMat()
		defer totalMask.Close()
		gocv.BitwiseOr(mask1, mask2, &totalMask)

		count := gocv.CountNonZero(totalMask)
		return float64(count)/float64(totalMask.Rows()*totalMask.Cols()) > minRatio

	case "purple": // Warden/Queen/Prince Purple
		lower = gocv.NewScalar(115, 40, 40, 0)
		upper = gocv.NewScalar(145, 255, 255, 0)
	case "light_blue": // Freeze/Ice Spell
		lower = gocv.NewScalar(90, 40, 100, 0)
		upper = gocv.NewScalar(120, 255, 255, 0)
	case "orange": // Dragon Duke / King Brown/Orange
		lower = gocv.NewScalar(10, 50, 50, 0)
		upper = gocv.NewScalar(25, 255, 255, 0)
	default:
		return false
	}

	mask := gocv.NewMat()
	defer mask.Close()
	gocv.InRangeWithScalar(hsv, lower, upper, &mask)
	count := gocv.CountNonZero(mask)
	return float64(count)/float64(mask.Rows()*mask.Cols()) > minRatio
}

func (e *Executor) addJitter(pt image.Point, maxPixels int) image.Point {
	if maxPixels <= 0 {
		return pt
	}
	jx := int(float64(maxPixels) * e.cal.ScaleX)
	jy := int(float64(maxPixels) * e.cal.ScaleY)
	if jx <= 0 {
		jx = 1
	}
	if jy <= 0 {
		jy = 1
	}
	return image.Pt(
		pt.X+rand.Intn(jx*2+1)-jx,
		pt.Y+rand.Intn(jy*2+1)-jy,
	)
}

func (e *Executor) GetTemplates() map[string]gocv.Mat {
	return e.templates
}

func ScaleEdge(e ManualEdge, refW, refH, curW, curH int) ManualEdge {
	if refW == 0 || refH == 0 {
		return e
	}
	sx, sy := float64(curW)/float64(refW), float64(curH)/float64(refH)
	return ManualEdge{
		P1: image.Pt(int(float64(e.P1.X)*sx), int(float64(e.P1.Y)*sy)),
		P2: image.Pt(int(float64(e.P2.X)*sx), int(float64(e.P2.Y)*sy)),
	}
}
