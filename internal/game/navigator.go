package game

import (
	"fmt"
	"image"
	"math"
	"math/rand"
	"sort"
	"time"

	"github.com/Ducky705/ClashGO/internal/vision"
	"github.com/rs/zerolog"
	"gocv.io/x/gocv"
)

type Navigator struct {
	cfg       NavigatorConfig
	cal       *Calibration
	graph     *StateGraph
	client    Device
	classify  func(gocv.Mat) (GameState, int)
	templates *TemplateStore
	logger    zerolog.Logger

	// chestCascadeCount tracks how many times consecutively StateChestReward
	// has been handled inside one handleInterruptions invocation.
	// Reset to 0 every time a non-chest state is observed.
	//
	// Why: InterruptDepth defaults to 5 and ChestWallClockLimit is 25s.
	// Without this guard, a non-dismissing chest could earn 5 * 25s = ~2min
	// of monopolized capture time before the outer loop bails. We instead
	// cap chest re-entries at 1, return an error after the first attempt,
	// and let the bot's stuck-watchdog / restart-game ladder take over.
	chestCascadeCount int

	// disableChestDismissal is the runtime kill-switch for the chest
	// recovery flow. When true, DismissChestReward returns nil
	// immediately on any chest state, allowing the bot's other ladders
	// (stuck-watchdog / restartGame) to handle the modal instead.
	//
	// Wired from config.DeviceConfig.DisableChestDismissal at bot
	// startup via SetDisableChestDismissal. NOT exposed through
	// NavigatorConfig because the field is feature-flag-shaped
	// (operator-level switch), not a runtime tunable.
	disableChestDismissal bool
}

func NewNavigator(client Device, cal *Calibration, graph *StateGraph, classify func(gocv.Mat) (GameState, int), logger zerolog.Logger) *Navigator {
	return &Navigator{
		cfg:      DefaultNavigatorConfig(),
		cal:      cal,
		graph:    graph,
		client:   client,
		classify: classify,
		logger:   logger.With().Str("component", "navigator").Logger(),
	}
}

func (n *Navigator) SetTemplates(ts *TemplateStore) {
	n.templates = ts
}

// SetDisableChestDismissal flips the runtime kill-switch for the
// chest recovery flow. When true, DismissChestReward becomes a
// no-op (returns nil immediately) and the bot's other ladders
// (stuck-watchdog / restartGame) take over. Wire this once at bot
// startup from config.DeviceConfig.DisableChestDismissal.
//
// Lives on Navigator (not NavigatorConfig) because the flag is
// feature-flag-shaped (operator-level switch), not a runtime
// tunable like SettleTime or InterruptDepth.
func (n *Navigator) SetDisableChestDismissal(disable bool) {
	n.disableChestDismissal = disable
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
		// Camera pans ride a bezier arc with ease-in-out velocity so the
		// map movement reads as a human drag, not a straight-line machine
		// flick. SwipeBezier degrades to the linear path on devices that
		// can't inject raw sendevent streams, so navigation never breaks.
		return n.client.SwipeBezier(edge.X, edge.Y, edge.X2, edge.Y2, 300) == nil
	case ActionHold:
		return n.client.Hold(edge.X, edge.Y, 500) == nil
	case ActionNone:
		return true
	default:
		return false
	}
}

// handleInterruptions runs the modal-dismiss ladder. Returns an error
// if no nested-interrupt strategy resolves the current state.
//
// Capture Mat lifetime is INLINE (close after classify) rather than
// `defer`, because deferring across for-loop iterations accumulates
// Mat handles until the function returns — with the chest dismissal
// loop nested inside, that could pile up ~25 live Mats for ~125s
// of wall-clock.
func (n *Navigator) handleInterruptions(ctx *GameContext) error {
	for i := 0; i < n.cfg.InterruptDepth; i++ {
		screen, err := n.client.CaptureToMat()
		if err != nil {
			return err
		}
		state, _ := n.classify(screen)
		if !screen.Empty() {
			screen.Close()
		}

		switch state {
		case StateObstacleDialog:
			n.dismissObstacle()
		case StateGemDialog:
			n.dismissGemDialog()
		case StateWelcomeBack:
			n.dismissWelcomeBack()
		case StateShieldInfo:
			n.dismissShieldInfo()
		case StateChatOpen:
			n.client.Back()
		case StateChestReward:
			// Cascade guard: only ONE chest-dismiss attempt per
			// handleInterruptions invocation. If the chest is STILL
			// detected after DismissChestReward has run its full
			// bounded loop, escalate to the caller instead of looping.
			if n.chestCascadeCount >= 1 {
				n.logger.Warn().
					Int("attempts", n.chestCascadeCount).
					Msg("chest still detected after dismiss attempt; escalating")
				return fmt.Errorf("chest loop did not converge (cascade cap hit)")
			}
			n.chestCascadeCount++
			if err := n.DismissChestReward(); err != nil {
				return err
			}
		default:
			// Any non-chest state resets the cascade counter.
			n.chestCascadeCount = 0
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

func (n *Navigator) dismissWelcomeBack() {
	if n.templates != nil {
		tpl, ok := n.templates.Get("btn_okay")
		if ok {
			norm, physScale, err := n.captureNormalized()
			if err == nil {
				defer norm.Close()
				// The button is usually in the lower half of the screen
				searchRect := image.Rect(200, 350, 660, 650)
				pt, conf, err := vision.MatchTemplateRegion(norm, tpl, searchRect, 0.6)
				if err == nil && conf > 0.6 {
					ax := int(float64(pt.X) * physScale)
					ay := int(float64(pt.Y) * physScale)
					n.client.Tap(ax, ay)
					time.Sleep(1000 * time.Millisecond)
					return
				}
			}
		}
	}
	// Fallback to center-ish tap
	sx, sy := n.cal.ScaleRef(430, 520)
	n.client.TapRandomized(sx, sy)
	time.Sleep(1000 * time.Millisecond)
}

func (n *Navigator) dismissGemDialog() {
	n.client.TapRandomized(175, 30)
	time.Sleep(300 * time.Millisecond)
}

func (n *Navigator) dismissShieldInfo() {
	n.client.TapRandomized(175, 30)
	time.Sleep(300 * time.Millisecond)
}

// IdlePan performs a small, randomized camera pan and back — the kind of
// micro-movement a real player's hand makes while waiting on their
// village. Each leg rides a bezier arc (SwipeBezier) with a human
// micro-pause at the far end, and the return leg draws a fresh random
// arc so the two legs never mirror each other geometrically.
//
// Only call while confirmed on the main village: the pan touches no
// buttons and the camera returns to its origin, so classification is
// unaffected afterward. Callers should throttle it (e.g. every ~20s)
// so it never overlaps an active attack sequence.
func (n *Navigator) IdlePan() {
	w, h := n.cal.PhysicalW, n.cal.PhysicalH
	if w <= 0 || h <= 0 {
		return
	}
	cx, cy := w/2, h/2

	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	// Pan distance: 15-30% of screen width, mostly horizontal with an
	// occasional small vertical nudge for organic drift.
	dx := int(float64(w) * (0.15 + r.Float64()*0.15))
	if r.Float64() < 0.5 {
		dx = -dx
	}
	dy := int(float64(h) * r.Float64() * 0.04)
	if r.Float64() < 0.5 {
		dy = -dy
	}

	panMs := 280 + r.Intn(120) // 280-400ms per leg
	x1, y1 := cx-dx/2, cy-dy/2
	x2, y2 := cx+dx/2, cy+dy/2

	// Out: deliberate drag away.
	_ = n.client.SwipeBezier(x1, y1, x2, y2, panMs)
	// Micro-pause at the far end — eyes on the base, thumb hovering.
	time.Sleep(time.Duration(180+r.Intn(121)) * time.Millisecond)
	// Back: along a fresh randomized arc.
	_ = n.client.SwipeBezier(x2, y2, x1, y1, panMs)
}

func (n *Navigator) TapAt(x, y int) error {
	return n.client.Tap(x, y)
}

func (n *Navigator) ZoomOut() {
	n.logger.Info().Msg("performing focus-independent native zoom out...")
	err := n.client.PinchZoom(true)
	if err != nil {
		n.logger.Warn().Err(err).Msg("native ZoomOut failed")
	} else {
		n.logger.Debug().Msg("native ZoomOut completed")
	}
}

func (n *Navigator) ZoomIn() {
	n.logger.Info().Msg("performing focus-independent native zoom in...")
	err := n.client.PinchZoom(false)
	if err != nil {
		n.logger.Warn().Err(err).Msg("native ZoomIn failed")
	} else {
		n.logger.Debug().Msg("native ZoomIn completed")
	}
}

func (n *Navigator) PinchAtScaled(x1, y1, x2, y2, x3, y3, x4, y4, ms int) error {
	sx1, sy1 := n.cal.ScaleRef(x1, y1)
	sx2, sy2 := n.cal.ScaleRef(x2, y2)
	sx3, sy3 := n.cal.ScaleRef(x3, y3)
	sx4, sy4 := n.cal.ScaleRef(x4, y4)
	return n.client.Pinch(sx1, sy1, sx2, sy2, sx3, sy3, sx4, sy4, ms)
}

func (n *Navigator) TapAtScaled(x, y int) error {
	sx, sy := n.cal.ScaleRef(x, y)
	return n.client.Tap(sx, sy)
}

func (n *Navigator) Back() error {
	return n.client.Back()
}

// WaitForState polls capture+classify until target is observed OR
// the deadline expires. Capture Mat lifetime is INLINE close — same
// defer-in-for-loop accumulation risk as handleInterruptions, just
// bounded by the caller-supplied timeout.
func (n *Navigator) WaitForState(ctx *GameContext, target GameState, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		screen, err := n.client.CaptureToMat()
		if err != nil {
			time.Sleep(500 * time.Millisecond)
			continue
		}
		state, _ := n.classify(screen)
		if !screen.Empty() {
			screen.Close()
		}

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
		action   TransitionAction
		x, y     int
	}{
		{StateBattle, StateBattleEnd, ActionTap, 34, 588},
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
				sx, sy := n.cal.ScaleRef(step.x, step.y)
				n.client.Tap(sx, sy)
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
					matches, err := vision.MatchMultiScale(screen, tpl, 0.9*n.cal.ScaleY, 1.1*n.cal.ScaleY, 3, 0.6)
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

					n.logger.Debug().
						Float64("confidence", conf).
						Interface("point", pt).
						Int("phys_x", ax).
						Int("phys_y", ay).
						Msg("found 'Find a Match' button")
					n.client.Tap(ax, ay)
					time.Sleep(1500 * time.Millisecond)
					return true
				} else {
					n.logger.Trace().
						Float64("confidence", conf).
						Err(err).
						Msg("button 'btn_find_match' not found in region")
				}
			}
		}
	}

	// Fallback to scaled coordinates for the yellow "Find a Match" button
	ax, ay := n.cal.ScaleRef(150, 540)
	n.logger.Info().
		Int("ax", ax).
		Int("ay", ay).
		Msg("using fallback coordinates for 'Find a Match'")
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
