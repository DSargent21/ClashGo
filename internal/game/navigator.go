package game

import (
	"fmt"
	"image"
	"math"
	"sort"
	"time"

	"gocv.io/x/gocv"

	"github.com/diegosargent/coc-bot/internal/vision"
)

type Navigator struct {
	cfg       NavigatorConfig
	cal       *Calibration
	graph     *StateGraph
	client    Device
	classify  func(gocv.Mat) (GameState, int)
	templates *TemplateStore
}

func NewNavigator(client Device, cal *Calibration, graph *StateGraph, classify func(gocv.Mat) (GameState, int)) *Navigator {
	return &Navigator{
		cfg:      DefaultNavigatorConfig(),
		cal:      cal,
		graph:    graph,
		client:   client,
		classify: classify,
	}
}

func (n *Navigator) SetTemplates(ts *TemplateStore) {
	n.templates = ts
}

func (n *Navigator) Navigate(ctx *GameContext, target GameState) bool {
	path := n.graph.ShortestPath(ctx.State, target)
	if path == nil {
		return false
	}

	for _, step := range path.Steps {
		if err := n.handleInterruptions(ctx); err != nil {
			return false
		}

		edges := n.graph.TransitionsFrom(step.From)
		var edge *StateTransition
		for i := range edges {
			if edges[i].To == step.To {
				edge = &edges[i]
				break
			}
		}

		if edge == nil {
			continue
		}

		if !n.executeStep(edge) {
			return false
		}

		time.Sleep(edge.Duration)

		screen, err := n.client.CaptureToMat()
		if err != nil {
			return false
		}
		defer screen.Close()

		state, _ := n.classify(screen)
		if state != step.To {
			if state == StateObstacleDialog || state == StateGemDialog {
				n.handleInterruptions(ctx)
			}
		}
	}

	return true
}

func (n *Navigator) executeStep(edge *StateTransition) bool {
	switch edge.Action {
	case ActionTap:
		return n.client.Tap(edge.X, edge.Y) == nil
	case ActionBack:
		return n.client.Back() == nil
	case ActionSwipe:
		return n.client.Swipe(edge.X, edge.Y, edge.X2, edge.Y2, 300) == nil
	case ActionHold:
		return n.client.Hold(edge.X, edge.Y, 500) == nil
	case ActionNone:
		return true
	default:
		return false
	}
}

func (n *Navigator) handleInterruptions(ctx *GameContext) error {
	for i := 0; i < n.cfg.InterruptDepth; i++ {
		screen, err := n.client.CaptureToMat()
		if err != nil {
			return err
		}
		defer screen.Close()

		state, _ := n.classify(screen)

		switch state {
		case StateObstacleDialog:
			n.dismissObstacle()
		case StateGemDialog:
			n.dismissGemDialog()
		case StateShieldInfo:
			n.dismissShieldInfo()
		case StateChatOpen:
			n.client.Back()
		default:
			return nil
		}

		time.Sleep(n.cfg.SettleTime)
	}
	return fmt.Errorf("too many nested interruptions")
}

func (n *Navigator) dismissObstacle() {
	candidates := []image.Point{
		{X: 400, Y: 300},
		{X: 430, Y: 430},
		{X: 400, Y: 500},
		{X: 500, Y: 430},
	}
	for _, pt := range candidates {
		sx, sy := n.cal.ScaleRef(pt.X, pt.Y)
		n.client.TapRandomized(sx, sy)
		time.Sleep(500 * time.Millisecond)
	}
	n.client.Back()
}

func (n *Navigator) dismissGemDialog() {
	n.client.TapRandomized(175, 30)
	time.Sleep(300 * time.Millisecond)
}

func (n *Navigator) dismissShieldInfo() {
	n.client.TapRandomized(175, 30)
	time.Sleep(300 * time.Millisecond)
}

func (n *Navigator) TapAt(x, y int) error {
	return n.client.Tap(x, y)
}

func (n *Navigator) TapAtScaled(x, y int) error {
	sx, sy := n.cal.ScaleRef(x, y)
	return n.client.Tap(sx, sy)
}

func (n *Navigator) Back() error {
	return n.client.Back()
}

func (n *Navigator) WaitForState(ctx *GameContext, target GameState, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		screen, err := n.client.CaptureToMat()
		if err != nil {
			time.Sleep(500 * time.Millisecond)
			continue
		}
		defer screen.Close()

		state, _ := n.classify(screen)
		if state == target {
			return true
		}

		if state == StateObstacleDialog || state == StateGemDialog {
			n.handleInterruptions(ctx)
		}

		time.Sleep(200 * time.Millisecond)
	}
	return false
}

func (n *Navigator) NavigateTo(ctx *GameContext, target GameState) bool {
	return n.Navigate(ctx, target)
}

func (n *Navigator) NavigateToMainVillage(ctx *GameContext) bool {
	if ctx.State == StateMainVillage {
		return true
	}

	seq := []struct {
		from, to GameState
		action  TransitionAction
		x, y    int
	}{
		{StateBattle, StateBattleEnd, ActionTap, 50, 548},
		{StateBattleEnd, StateReturnHome, ActionTap, 430, 566},
		{StateReturnHome, StateMainVillage, ActionTap, 430, 566},
		{StateArmyCamp, StateMainVillage, ActionBack, 0, 0},
		{StateSettings, StateMainVillage, ActionBack, 0, 0},
	}

	for _, step := range seq {
		if ctx.State == step.from {
			if step.action == ActionBack {
				n.client.Back()
			} else {
				n.client.Tap(step.x, step.y)
			}
			time.Sleep(1500 * time.Millisecond)
			return true
		}
	}

	return false
}

func (n *Navigator) NavigateToBattle(ctx *GameContext) bool {
	if ctx.State == StateBattle {
		return true
	}

	if ctx.State == StateMainVillage {
		ax, ay := n.cal.ScaleRef(60, 548)
		
		// Try to find the battle button via template matching
		if n.templates != nil {
			tpl, ok := n.templates.Get("btn_battle")
			if ok {
				screen, err := n.client.CaptureToMat()
				if err == nil {
					defer screen.Close()
					matches, err := vision.MatchMultiScale(screen, tpl, 0.3, 1.3, 10, 0.7)
					if err == nil && len(matches) > 0 {
						sort.Slice(matches, func(i, j int) bool {
							return matches[i].Confidence > matches[j].Confidence
						})
						ax, ay = matches[0].Point.X, matches[0].Point.Y
					}
				}
			}
		}

		n.client.Tap(ax, ay)
		time.Sleep(1500 * time.Millisecond)
		return true
	}

	return false
}

func (n *Navigator) NavigateToArmyCamp(ctx *GameContext) bool {
	if ctx.State == StateArmyCamp {
		return true
	}

	if ctx.State == StateMainVillage {
		ax, ay := n.cal.ScaleRef(40, 525)
		n.client.Tap(ax, ay)
		time.Sleep(1500 * time.Millisecond)
		return true
	}

	return false
}

func (n *Navigator) captureNormalized() (gocv.Mat, float64, error) {
	raw, err := n.client.CaptureToMat()
	if err != nil {
		return gocv.Mat{}, 0, err
	}
	if raw.Empty() {
		raw.Close()
		return gocv.Mat{}, 0, fmt.Errorf("empty capture")
	}

	norm := vision.ResizeToHeight(raw, 732)
	physScale := float64(raw.Rows()) / 732.0
	raw.Close()

	return norm, physScale, nil
}

func (n *Navigator) NavigateToFindMatch(ctx *GameContext) bool {
	if ctx.State == StateFindMatch {
		return true
	}

	// If we are in Main Village, first click Battle to open the menu
	if ctx.State == StateMainVillage {
		ax, ay := n.cal.ScaleRef(60, 548)
		n.client.Tap(ax, ay)
		time.Sleep(1500 * time.Millisecond)
		// Update state to check if we are in the menu
		return true
	}

	// If we are in the attack menu, find and click the "Find a Match" button
	if n.templates != nil {
		tpl, ok := n.templates.Get("btn_find_match")
		if ok {
			norm, physScale, err := n.captureNormalized()
			if err == nil {
				defer norm.Close()

				// Search for the button in bottom-left area of the normalized (h=732) screen
				searchRect := image.Rect(0, 400, 600, 732)
				pt, conf, err := vision.MatchTemplateRegion(norm, tpl, searchRect, 0.6)

				if err == nil && conf > 0.6 {
					// Scale back to physical coordinates
					ax := int(float64(pt.X) * physScale)
					ay := int(float64(pt.Y) * physScale)

					fmt.Printf("Found 'Find a Match' button: conf=%.4f at %v -> clicking phys (%d, %d)\n", conf, pt, ax, ay)
					n.client.Tap(ax, ay)
					time.Sleep(1500 * time.Millisecond)
					return true
				} else {
					fmt.Printf("Button 'step5_findmatch' not found in region: conf=%.4f err=%v\n", conf, err)
				}
			}
		}
	}

	// Fallback to scaled coordinates for the yellow "Find a Match" button
	ax, ay := n.cal.ScaleRef(150, 540)
	fmt.Printf("Using fallback coordinates for 'Find a Match': (%d, %d)\n", ax, ay)
	n.client.Tap(ax, ay)
	time.Sleep(1500 * time.Millisecond)
	return true
}

func (n *Navigator) CheckPixel(screen gocv.Mat, x, y int, r, g, b uint8, tol int) bool {
	if x < 0 || y < 0 || x >= screen.Cols() || y >= screen.Rows() {
		return false
	}
	bgr := screen.GetUCharAt(y, x*3)
	ggg := screen.GetUCharAt(y, x*3+1)
	rrr := screen.GetUCharAt(y, x*3+2)

	dr := absDiff(int(rrr), int(r))
	dg := absDiff(int(ggg), int(g))
	db := absDiff(int(bgr), int(b))

	return math.Sqrt(float64(dr*dr+dg*dg+db*db)) <= float64(tol)
}

func (n *Navigator) ClickElement(elem *Clickable) error {
	return n.client.Tap(elem.Center.X, elem.Center.Y)
}

func (n *Navigator) ClickElementRandomized(elem *Clickable) error {
	return n.client.TapRandomized(elem.Center.X, elem.Center.Y)
}

func (n *Navigator) NavigateToBuilderBase(ctx *GameContext) bool {
	if ctx.State == StateBuilderBase {
		return true
	}

	if ctx.State == StateMainVillage {
		bx, by := n.cal.ScaleRef(830, 16)
		n.client.Tap(bx, by)
		time.Sleep(2000 * time.Millisecond)
		return true
	}

	return false
}

func (n *Navigator) NavigateToMainVillageFromBB(ctx *GameContext) bool {
	if ctx.State == StateMainVillage {
		return true
	}

	if ctx.State == StateBuilderBase {
		bx, by := n.cal.ScaleRef(830, 16)
		n.client.Tap(bx, by)
		time.Sleep(2000 * time.Millisecond)
		return true
	}

	return false
}