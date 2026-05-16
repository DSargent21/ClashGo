package attack

import (
	"encoding/json"
	"fmt"
	"image"
	"math/rand"
	"os"
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

func (e *Executor) DeployDynamic(s *strategy.DynamicStrategy, screen gocv.Mat) error {
	w, h := screen.Cols(), screen.Rows()
	targetEdge := s.TargetEdge

	// 1. Load Precision Config
	var pCfg PrecisionConfig
	usePrecision := false
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
		mBarY := int(float64(pCfg.BarY) * scaleY)
		if mBarY > int(float64(h)*0.78) { mBarY = int(float64(h) * 0.78) }
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

	debugImg := screen.Clone()
	defer debugImg.Close()

	var barY int = -1
	lastBar := gocv.NewMat()
	defer func() {
		if !lastBar.Empty() { lastBar.Close() }
	}()

	for _, phase := range s.Phases {
		e.logger.Info().Str("phase", phase.Name).Msg("attack phase")

		for _, unit := range phase.Units {
			unitName := strings.ToLower(strings.TrimSpace(unit.Name))
			isAbility := unit.Pattern == "Ability" || phase.Pattern == "Ability"
			
			// Force refresh for abilities or if empty
			if isAbility || lastBar.Empty() {
				if !lastBar.Empty() { lastBar.Close() }
				var err error
				lastBar, err = e.client.CaptureToMat()
				if err != nil {
					e.logger.Warn().Err(err).Msg("failed capture")
					continue
				}
			}

			isSpell := strings.Contains(unitName, "spell")
			isHero := strings.Contains(unitName, "king") || strings.Contains(unitName, "queen") || strings.Contains(unitName, "warden") || strings.Contains(unitName, "prince") || strings.Contains(unitName, "slammer")

			threshold := 0.55
			if isHero { threshold = 0.45 } else if isSpell { threshold = 0.60 }

			fileName := strings.ReplaceAll(unitName, " ", "_")
			tplPath := fmt.Sprintf("assets/templates/attack/%s.png", fileName)
			tpl := gocv.IMRead(tplPath, gocv.IMReadColor)
			
			findAndTap := func(screen gocv.Mat) *vision.Match {
				if tpl.Empty() { return nil }
				barROI := image.Rect(0, int(float64(h)*0.6), w, h)
				matches, _ := vision.MatchMultiScaleROI(screen, tpl, 0.2, 1.2, 15, float32(threshold), barROI)
				for _, m := range matches {
					if barY == -1 || (m.Point.Y > barY-120 && m.Point.Y < barY+120) {
						if barY == -1 { barY = m.Point.Y }
						return &m
					}
				}
				return nil
			}

			match := findAndTap(lastBar)
			if match == nil && !isAbility { // Refresh if not found and not already forced
				if !lastBar.Empty() { lastBar.Close() }
				var err error
				lastBar, err = e.client.CaptureToMat()
				if err != nil {
					lastBar = gocv.NewMat() 
				}
				match = findAndTap(lastBar)
			}
			tpl.Close()

			if match == nil {
				e.logger.Warn().Str("unit", unit.Name).Msg("unit not found")
				continue
			}

			uPt := match.Point
			e.logger.Info().Str("unit", unit.Name).Int("x", uPt.X).Int("y", uPt.Y).Msg("selecting unit")
			e.client.Tap(uPt.X, uPt.Y)
			
			if isAbility {
				time.Sleep(150 * time.Millisecond) // Wait for ability button to be ready
				e.logger.Info().Str("unit", unit.Name).Msg("activating ability")
				e.client.Tap(uPt.X, uPt.Y)
				time.Sleep(50 * time.Millisecond)
				continue
			}
			
			time.Sleep(150 * time.Millisecond) // Wait for selection to register

			// Deployment Logic
			isRage := strings.Contains(unitName, "rage")
			isFreeze := strings.Contains(unitName, "ice") || strings.Contains(unitName, "freeze")

			if isSpell {
				edgeA, okA := pCfg.SpellEdgesA[targetEdge]
				edgeB, okB := pCfg.SpellEdgesB[targetEdge]
				if !okA || !okB { continue }

				if isRage {
					lines := []ManualEdge{edgeA, edgeB}
					for _, edge := range lines {
						p1, p2 := edge.P1, edge.P2
						for i := 0; i < 3; i++ {
							pct := float64(i) / 2.0
							tx, ty := int(float64(p1.X)+float64(p2.X-p1.X)*pct), int(float64(p1.Y)+float64(p2.Y-p1.Y)*pct)
							e.client.Tap(tx, ty)
							time.Sleep(30 * time.Millisecond)
						}
					}
				} else if isFreeze {
					p1, p2 := edgeB.P1, edgeB.P2
					for i := 0; i < 3; i++ {
						pct := float64(i) / 2.0
						tx, ty := int(float64(p1.X)+float64(p2.X-p1.X)*pct), int(float64(p1.Y)+float64(p2.Y-p1.Y)*pct)
						e.client.Tap(tx, ty)
						time.Sleep(30 * time.Millisecond)
					}
				}
			} else {
				var p1, p2 image.Point
				if isHero {
					if pt, ok := pCfg.HeroTargets[targetEdge]; ok { p1, p2 = pt, pt }
				} else {
					if edge, ok := pCfg.Edges[targetEdge]; ok { p1, p2 = edge.P1, edge.P2 }
				}

				steps := 1
				if strings.Contains(unitName, "balloon") || strings.Contains(unitName, "electro") { steps = 15 }

				if p1 == p2 { // Point
					e.logger.Debug().Str("unit", unit.Name).Int("x", p1.X).Int("y", p1.Y).Msg("deploying point")
					for i := 0; i < steps; i++ {
						e.client.Tap(p1.X+rand.Intn(21)-10, p1.Y+rand.Intn(21)-10)
						if steps > 1 { time.Sleep(25 * time.Millisecond) }
					}
				} else { // Line
					e.logger.Debug().Str("unit", unit.Name).Msg("deploying line")
					for i := 0; i < steps; i++ {
						pct := float64(i) / float64(steps-1)
						tx, ty := int(float64(p1.X)+float64(p2.X-p1.X)*pct), int(float64(p1.Y)+float64(p2.Y-p1.Y)*pct)
						e.client.Tap(tx+rand.Intn(15)-7, ty+rand.Intn(15)-7)
						time.Sleep(15 * time.Millisecond)
					}
				}
			}
		}
		if !lastBar.Empty() { lastBar.Close(); lastBar = gocv.NewMat() }
		
		pDelay := time.Duration(phase.DelayAfterMS) * time.Millisecond
		if phase.Name == "Heroes" || phase.Name == "Siege Machine" { pDelay = 50 * time.Millisecond }
		if pDelay > 0 { time.Sleep(pDelay) }
	}
	return nil
}

func (e *Executor) EndBattle() error {
	ex, ey := e.cal.ScaleRef(34, 558)
	if err := e.client.Tap(ex, ey); err != nil { return err }
	time.Sleep(3 * time.Second); return nil
}

func (e *Executor) ReturnHome() error {
	hx, hy := e.cal.ScaleRef(290, 576)
	if err := e.client.Tap(hx, hy); err != nil { return err }
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
	ticker := time.NewTicker(500 * time.Millisecond)
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
