package game

import (
	"image"
	"math"
	"sort"
	"sync"
	"time"

	"github.com/Ducky705/ClashGo/internal/vision"
	"github.com/rs/zerolog"
	"gocv.io/x/gocv"
)

type Classifier struct {
	cfg       ClassifierConfig
	cal       *Calibration
	rules     []StateRule
	rec       *Recognizer
	templates *TemplateStore
	logger    zerolog.Logger

	pending GameState
	confirm int
	mu      sync.Mutex
}

func NewClassifier(cal *Calibration, cfg ClassifierConfig, logger zerolog.Logger) *Classifier {
	c := &Classifier{
		cfg:    cfg,
		cal:    cal,
		rec:    NewRecognizer(),
		logger: logger.With().Str("component", "classifier").Logger(),
	}
	c.buildRules()
	return c
}

func (c *Classifier) GetRules() []StateRule {
	return c.rules
}

func (c *Classifier) SetTemplates(ts *TemplateStore) {
	c.templates = ts
}

func (c *Classifier) ClassifyState(screen gocv.Mat) (GameState, int) {
	if screen.Empty() {
		return StateUnknown, 0
	}

	// Normalize screen to reference height (732) to simplify rules and templates
	norm := vision.ResizeToHeight(screen, 732)
	defer norm.Close()

	var scores []scoredState

	for _, rule := range c.rules {
		passed := 0
		for _, chk := range rule.Checks {
			// No scaling needed on normalized screen!
			sx, sy := chk.X, chk.Y
			if sx < 0 || sy < 0 || sx >= norm.Cols() || sy >= norm.Rows() {
				continue
			}

			b := norm.GetUCharAt(sy, sx*3)
			g := norm.GetUCharAt(sy, sx*3+1)
			r := norm.GetUCharAt(sy, sx*3+2)

			dr := absDiff(int(r), int(chk.R))
			dg := absDiff(int(g), int(chk.G))
			db := absDiff(int(b), int(chk.B))

			if math.Sqrt(float64(dr*dr+dg*dg+db*db)) <= float64(chk.Tolerance) {
				passed++
			}
		}

		totalScore := 0
		if passed >= rule.MinPass && rule.MinPass > 0 {
			totalScore = passed*100 + rule.Weight
		}

		// Check template if defined
		if rule.Template != "" && c.templates != nil {
			tpl, ok := c.templates.Get(rule.Template)
			if ok {
				// Professional Multi-Scale matching for robustness across resolutions
				matches, err := vision.MatchMultiScale(norm, tpl, 0.2, 1.2, 10, c.cfg.TemplateThreshold)
				if err == nil && len(matches) > 0 {
					// Use the best match confidence
					bestConf := matches[0].Confidence
					totalScore += int(bestConf*1000) + rule.Weight
				}
			}
		}

		if totalScore > 0 {
			scores = append(scores, scoredState{State: rule.State, Score: totalScore})
		}
	}

	if len(scores) == 0 {
		c.logger.Trace().Msg("no states detected")
		return StateUnknown, 0
	}

	sort.Slice(scores, func(i, j int) bool {
		return scores[i].Score > scores[j].Score
	})

	c.logger.Trace().
		Str("state", scores[0].State.String()).
		Int("score", scores[0].Score).
		Msg("top state detected")

	return scores[0].State, scores[0].Score
}

func (c *Classifier) ConfirmState(state GameState) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	if state == c.pending {
		c.confirm++
	} else {
		c.pending = state
		c.confirm = 1
	}

	return c.confirm >= c.cfg.ConfirmFrames
}

func (c *Classifier) ResetConfirm() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pending = StateUnknown
	c.confirm = 0
}

func (c *Classifier) ForceState(state GameState) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pending = state
	c.confirm = c.cfg.ConfirmFrames
}

type scoredState struct {
	State GameState
	Score int
}

func (c *Classifier) buildRules() {
	baseRules := []StateRule{
		{
			State:    StateGemDialog,
			Priority: 100,
			Weight:   100,
			Desc:     "gem purchase popup",
			MinPass:  3,
			Checks: []PixelCheck{
				// Original: 608,240 @ 1280x720 -> ref 860x732
				{410, 244, 0xEB, 0x16, 0x17, 15},
				{411, 250, 0xCD, 0x16, 0x1A, 15},
				{421, 250, 0xCE, 0x15, 0x19, 15},
			},
		},
		{
			State:    StateObstacleDialog,
			Priority: 95,
			Weight:   95,
			Desc:     "blocking dialog",
			MinPass:  1,
			Checks: []PixelCheck{
				{324, 499, 0xCB, 0xCD, 0xD3, 15},
				{272, 11, 0xFE, 0xFE, 0xED, 15},
				{289, 515, 0x88, 0xD0, 0x39, 15},
			},
		},
		{
			State:    StateBattle,
			Priority: 90,
			Weight:   90,
			Desc:     "matchmaking or live battle",
			Template: "btn_next",
			MinPass:  1,
			Checks: []PixelCheck{
				// 100,560 (end battle) -> 67,570
				{67, 570, 0xCE, 0x0D, 0x0E, 25},
				// 1206,500 (next button) -> 813,509 @ 860x732 (right edge)
				{813, 509, 0xFC, 0xBA, 0x36, 25},
				// 40,110 (gold icon) -> 27,112
				{27, 112, 0xFF, 0xEC, 0x4A, 25},
			},
		},
		{
			State:    StateBattleEnd,
			Priority: 88,
			Weight:   88,
			Desc:     "battle result stars",
			MinPass:  1,
			Checks: []PixelCheck{
				{481, 548, 0xC0, 0xC8, 0xC0, 20},
				{498, 548, 0xC0, 0xC8, 0xC0, 20},
				{514, 548, 0xC0, 0xC8, 0xC0, 20},
			},
		},
		{
			State:    StateArmyCamp,
			Priority: 85,
			Weight:   85,
			Desc:     "army overview tab open",
			MinPass:  1,
			Checks: []PixelCheck{
				{529, 149, 0xF1, 0x55, 0x4F, 25},
				{479, 149, 0x4D, 0x3E, 0x33, 25},
			},
		},
		{
			State:    StateShieldInfo,
			Priority: 80,
			Weight:   80,
			Desc:     "shield info overlay",
			MinPass:  1,
			Checks: []PixelCheck{
				{455, 158, 0xFF, 0x8D, 0x95, 15},
			},
		},
		{
			State:    StateChatOpen,
			Priority: 75,
			Weight:   75,
			Desc:     "chat tab visible",
			MinPass:  2,
			Checks: []PixelCheck{
				{264, 295, 0xF3, 0xAB, 0x28, 15},
				{264, 316, 0xFF, 0xFF, 0xFF, 15},
				{264, 341, 0xEA, 0x8A, 0x3B, 15},
			},
		},
		{
			State:    StateSearchMap,
			Priority: 70,
			Weight:   70,
			Desc:     "search map - clouds",
			MinPass:  1,
			Checks: []PixelCheck{
				{290, 366, 0xFF, 0xFF, 0xFF, 30},
				{135, 204, 0xEE, 0xF5, 0xFF, 30},
				{405, 509, 0xEE, 0xF5, 0xFF, 30},
				{38, 603, 0x0A, 0x22, 0x3F, 25},
			},
		},
		{
			State:    StateBuilderBase,
			Priority: 65,
			Weight:   65,
			Desc:     "builder base indicator",
			MinPass:  1,
			Checks: []PixelCheck{
				{565, 16, 0xFF, 0xFF, 0x47, 15},
			},
		},
		{
			State:    StateMainVillage,
			Priority: 60,
			Weight:   60,
			Desc:     "main village - builder info icon or attack button",
			Template: "btn_attack",
			MinPass:  2,
			Checks: []PixelCheck{
				{255, 10, 0x7A, 0xBD, 0xE3, 15},
				{40, 558, 0xFF, 0xAF, 0x00, 20},
				{27, 558, 0x8D, 0x4B, 0x00, 20},
				{560, 20, 0xFF, 0xEE, 0x00, 20},
			},
		},
		{
			State:    StateReturnHome,
			Priority: 50,
			Weight:   50,
			Desc:     "return home button",
			MinPass:  1,
			Checks: []PixelCheck{
				{290, 576, 0x6C, 0xBB, 0x1F, 15},
			},
		},
		{
			State:    StateSettings,
			Priority: 50,
			Weight:   50,
			Desc:     "settings page",
			MinPass:  1,
			Checks: []PixelCheck{
				{556, 565, 0xFF, 0xFF, 0xFF, 10},
			},
		},
		{
			State:    StateFindMatch,
			Priority: 50,
			Weight:   50,
			Desc:     "find match button",
			Template: "btn_find_match",
			MinPass:  1,
			Checks: []PixelCheck{
				{215, 563, 0xD8, 0xA4, 0x20, 25},
			},
		},
		{
			State:    StateLoading,
			Priority: 45,
			Weight:   45,
			Desc:     "loading screen",
			MinPass:  1,
			Checks: []PixelCheck{
				{324, 499, 0xCB, 0xCD, 0xD3, 15},
			},
		},
	}

	for _, r := range baseRules {
		// We no longer scale rules because we normalize the screen height in ClassifyState
		c.rules = append(c.rules, r)
	}

	sort.Slice(c.rules, func(i, j int) bool {
		return c.rules[i].Priority > c.rules[j].Priority
	})
}

func (c *Classifier) DetectWithRedArea(screen gocv.Mat, minArea int) (GameState, []image.Point) {
	state, _ := c.ClassifyState(screen)

	if state == StateBattle || state == StateMainVillage {
		pts, _ := c.findRedArea(screen, minArea)
		if len(pts) > 10 {
			return StateBattle, pts
		}
	}

	return state, nil
}

func (c *Classifier) findRedArea(screen gocv.Mat, minArea int) ([]image.Point, error) {
	blurred := gocv.NewMat()
	defer blurred.Close()
	gocv.GaussianBlur(screen, &blurred, image.Point{X: 5, Y: 5}, 0, 0, gocv.BorderDefault)

	hsv := gocv.NewMat()
	defer hsv.Close()
	gocv.CvtColor(blurred, &hsv, gocv.ColorBGRToHSV)

	lowerRed1 := gocv.NewScalar(0, 100, 100, 0)
	upperRed1 := gocv.NewScalar(10, 255, 255, 0)
	lowerRed2 := gocv.NewScalar(160, 100, 100, 0)
	upperRed2 := gocv.NewScalar(180, 255, 255, 0)

	mask1 := gocv.NewMat()
	mask2 := gocv.NewMat()
	gocv.InRangeWithScalar(hsv, lowerRed1, upperRed1, &mask1)
	gocv.InRangeWithScalar(hsv, lowerRed2, upperRed2, &mask2)
	defer mask1.Close()
	defer mask2.Close()

	var mask gocv.Mat
	gocv.BitwiseOr(mask1, mask2, &mask)
	defer mask.Close()

	kernel := gocv.GetStructuringElement(gocv.MorphRect, image.Point{X: 3, Y: 3})
	defer kernel.Close()
	gocv.MorphologyEx(mask, &mask, gocv.MorphOpen, kernel)

	contours := gocv.FindContours(mask, gocv.RetrievalExternal, gocv.ChainApproxSimple)
	defer contours.Close()

	var points []image.Point
	for i := 0; i < contours.Size(); i++ {
		area := gocv.ContourArea(contours.At(i))
		if area < float64(minArea) {
			continue
		}
		rect := gocv.BoundingRect(contours.At(i))
		points = append(points, image.Pt(rect.Min.X+rect.Dx()/2, rect.Min.Y+rect.Dy()/2))
	}

	return points, nil
}

func (c *Classifier) SetCalibration(cal *Calibration) {
	c.cal = cal
	c.rules = nil
	c.buildRules()
}

type ClassifierStats struct {
	ConfirmFrames int
	PendingState  GameState
}

func (c *Classifier) Stats() ClassifierStats {
	c.mu.Lock()
	defer c.mu.Unlock()
	return ClassifierStats{
		ConfirmFrames: c.confirm,
		PendingState:  c.pending,
	}
}

type StateClassifier interface {
	ClassifyState(screen gocv.Mat) (GameState, int)
	ConfirmState(state GameState) bool
	ResetConfirm()
	Stats() ClassifierStats
}

var _ StateClassifier = (*Classifier)(nil)

func ClassifyStateFast(screen gocv.Mat, cal *Calibration, r []StateRule) GameState {
	if screen.Empty() {
		return StateUnknown
	}

	best := StateUnknown
	bestScore := 0

	for _, rule := range r {
		passed := 0
		for _, chk := range rule.Checks {
			sx, sy := cal.ScaleRef(chk.X, chk.Y)
			if sx < 0 || sy < 0 || sx >= screen.Cols() || sy >= screen.Rows() {
				continue
			}

			b := screen.GetUCharAt(sy, sx*3)
			g := screen.GetUCharAt(sy, sx*3+1)
			r := screen.GetUCharAt(sy, sx*3+2)

			dr := absDiff(int(r), int(chk.R))
			dg := absDiff(int(g), int(chk.G))
			db := absDiff(int(b), int(chk.B))

			if math.Sqrt(float64(dr*dr+dg*dg+db*db)) <= float64(chk.Tolerance) {
				passed++
			}
		}

		if passed >= rule.MinPass {
			score := passed*100 + rule.Weight
			if score > bestScore {
				bestScore = score
				best = rule.State
			}
		}
	}

	return best
}

type ClassifierResult struct {
	State       GameState
	Score       int
	Confirm     bool
	ClassifyMs  time.Duration
	DetectedAt  time.Time
}

func (c *Classifier) ClassifyWithTiming(screen gocv.Mat) ClassifierResult {
	start := time.Now()
	state, score := c.ClassifyState(screen)
	confirmed := c.ConfirmState(state)
	return ClassifierResult{
		State:      state,
		Score:      score,
		Confirm:    confirmed,
		ClassifyMs: time.Since(start),
		DetectedAt: time.Now(),
	}
}