package game

import (
	"context"
	"errors"
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

	// classifier is what DismissOverlay verifies an overlay against before it
	// moves it (see dismiss.go). Wired once at bot startup via SetClassifier;
	// nil means the navigator refuses to dismiss an overlay, which is the safe
	// direction — no evidence, no input.
	classifier *Classifier

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

// SetClassifier supplies the classifier whose rules DismissOverlay verifies an
// overlay's control against (dismiss.go). Wire it once at startup, before the
// capture loop can see an overlay: with no classifier the navigator will not
// dismiss anything, rather than tapping a predicted point.
func (n *Navigator) SetClassifier(cl *Classifier) {
	n.classifier = cl
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
		case StateObstacleDialog, StateGemDialog, StateWelcomeBack, StateShieldInfo, StateChatOpen:
			// One evidence-gated path for every overlay (dismiss.go). No input is
			// fired unless the state is still on the frame AND the control it
			// moves (a verified probe, a located button, or Back) is where the
			// action expects it. A refusal is an error, so the caller escalates to
			// the stuck-watchdog ladder rather than sweeping the screen.
			if !DismissOverlay(n.client, n.cal, n.classifier, state, n.templates, n.logger) {
				n.logger.Warn().Str("state", state.String()).
					Msg("overlay not dismissed from verified evidence; escalating")
				return fmt.Errorf("overlay %s not dismissed (no verified evidence)", state)
			}
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

	// Pan distance: 15-30% of screen width, mostly horizontal with an
	// occasional small vertical nudge for organic drift. Drawn from the
	// package generator: a per-call rand.New(rand.NewSource(now)) allocates a
	// ~4.9 KB state array for four numbers.
	dx := int(float64(w) * (0.15 + rand.Float64()*0.15))
	if rand.Float64() < 0.5 {
		dx = -dx
	}
	dy := int(float64(h) * rand.Float64() * 0.04)
	if rand.Float64() < 0.5 {
		dy = -dy
	}

	panMs := 280 + rand.Intn(120) // 280-400ms per leg
	x1, y1 := cx-dx/2, cy-dy/2
	x2, y2 := cx+dx/2, cy+dy/2

	// Out: deliberate drag away.
	_ = n.client.SwipeBezier(x1, y1, x2, y2, panMs)
	// Micro-pause at the far end — eyes on the base, thumb hovering.
	time.Sleep(time.Duration(180+rand.Intn(121)) * time.Millisecond)
	// Back: along a fresh randomized arc.
	_ = n.client.SwipeBezier(x2, y2, x1, y1, panMs)
}

func (n *Navigator) TapAt(x, y int) error {
	return n.client.Tap(x, y)
}

// zoomPinchRepeats is how many pinches a single zoom step fires. One pinch can
// land while the camera is still settling, and the view saturates after the
// first few, so repeating is cheap insurance rather than an attempt to travel
// further (verified live: pinch #1 moves the camera, #2 and #3 are no-ops).
const zoomPinchRepeats = 3

// Zoom saturation limits, measured live on this emulator (2026-09-30, village
// screen): ONE pinch reaches the game's max zoom-out — the green-terrain
// fraction goes 0.0042 -> 0.0144 with the first gesture and then never moves
// again, no matter how wide or fast the pinch is (a 1080px-apart gesture at
// 900ms moved nothing, while a zoom-IN moved freely, so the wall is the game's
// clamp, not the gesture's reach). The limits below are belt-and-braces for
// ZoomOutMax's loop, not a claim that they are reachable.
const (
	zoomOutMaxAttempts  = 5
	zoomOutMaxThreshold = 0.4 // percent of thumbnail pixels, from vision.ChangedPercent
	zoomSettleMillis    = 900 // the camera eases after a pinch; less reads as still moving
)

// ZoomOut is a single fixed zoom-out step; see ZoomOutMax for the closed-loop
// variant the startup path uses. The error is the point. Live 2026-09-30: every
// zoom-out logged "native pinch failed: context canceled" and the caller — which
// discarded the result — went on to attack with whatever zoom the game happened
// to be at. The pinch had not run at all, because the input context was already
// dead by the time the first attempt was made, and a zoom that never happened
// silently becomes a wrong-geometry attack.
func (n *Navigator) ZoomOut() error {
	n.logger.Info().Msg("performing focus-independent native zoom out...")
	return n.pinch(true)
}

// ZoomIn is ZoomOut's counterpart; see ZoomOut for why it reports an error.
func (n *Navigator) ZoomIn() error {
	n.logger.Info().Msg("performing focus-independent native zoom in...")
	return n.pinch(false)
}

// ZoomOutMax zooms out until the view STOPS responding, then reports what it
// saw. "As far out as the game allows" is a property of the screen, not of a
// gesture count: the fixed 3-pinch loop below cannot tell a saturated view from
// a stuck one, because both return nil after the same number of delivered
// gestures. This one reads the frame.
//
// The loop: capture a signature, pinch, wait for the camera to settle, capture
// again. When the two signatures agree (ChangedPercent under
// zoomOutMaxThreshold), the view has stopped moving — saturated, or already at
// max — and the last pinch changed nothing. Measured live: one pinch takes the
// village from 0.0042 to 0.0144 green fraction and a second moves nothing, so
// this exits after TWO delivered pinches in the common case — one that moved
// the view, one that confirmed saturation — instead of a fixed three that never
// verified anything.
//
// Every capture error is fatal to the loop rather than silently treated as "no
// change": a zoom that cannot be verified did not verifiably happen, and the
// caller needs that distinction. A cancelled context propagates unchanged.
func (n *Navigator) ZoomOutMax() error {
	n.logger.Info().Msg("zooming out until the view stops responding (max zoom-out)...")
	delivered := 0
	for attempt := 0; attempt < zoomOutMaxAttempts; attempt++ {
		prevSig, err := n.captureSignature()
		if err != nil {
			n.logger.Warn().Err(err).Int("delivered", delivered).
				Msg("zoom-out: could not capture a frame to verify against; the view may NOT be at max zoom-out")
			return err
		}
		if err := n.client.PinchZoom(true); err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				n.logger.Warn().Err(err).Int("delivered", delivered).
					Msg("zoom-out aborted: the run's context is cancelled, so this zoom did NOT happen")
				return err
			}
			n.logger.Warn().Err(err).Int("delivered", delivered).Msg("zoom-out pinch failed")
			return err
		}
		delivered++
		time.Sleep(zoomSettleMillis * time.Millisecond)
		curSig, err := n.captureSignature()
		if err != nil {
			n.logger.Warn().Err(err).Int("delivered", delivered).
				Msg("zoom-out: could not capture the post-pinch frame; the view may NOT be at max zoom-out")
			return err
		}
		changed := vision.ChangedPercent(prevSig, curSig, vision.DefaultChangeDelta)
		prevSig.Close()
		curSig.Close()
		n.logger.Info().
			Int("attempt", attempt+1).
			Int("delivered", delivered).
			Float64("changed_pct", changed).
			Msg("zoom-out pinch verified against the frame")
		if changed < zoomOutMaxThreshold {
			n.logger.Info().
				Int("pinches", delivered).
				Float64("last_changed_pct", changed).
				Msg("view stopped responding: at max zoom-out")
			return nil
		}
	}
	n.logger.Warn().
		Int("pinches", delivered).
		Msg("zoom-out: the view was STILL moving after the attempt budget; it may not be at max zoom-out")
	return nil
}

// captureSignature reduces the live frame to a change-detection thumbnail. The
// Mat is caller-owned: Close it when the comparison is done.
func (n *Navigator) captureSignature() (gocv.Mat, error) {
	frame, err := n.client.CaptureToMat()
	if err != nil {
		return gocv.Mat{}, err
	}
	defer frame.Close()
	if frame.Empty() {
		return gocv.Mat{}, errors.New("captured frame is empty")
	}
	sig := vision.Signature(frame)
	if sig.Empty() {
		return gocv.Mat{}, errors.New("frame too small for a change signature")
	}
	return sig, nil
}

// pinch fires zoomPinchRepeats pinches, stopping at the first failure, and
// returns that failure (nil when every pinch was delivered).
//
// A cancelled context is reported distinctly from a device failure. The old
// loop slept a fixed 500ms between repeats without ever consulting the
// context, so once the session was cancelled it kept sleeping and kept trying —
// burning up to a second of wall clock on gestures nobody would receive, and
// reporting the cancellation as if it were a pinch malfunction. A dead context
// means the run is over, and the caller needs to know that rather than retry.
func (n *Navigator) pinch(zoomOut bool) error {
	var lastErr error
	delivered := 0
	for i := 0; i < zoomPinchRepeats; i++ {
		if err := n.client.PinchZoom(zoomOut); err != nil {
			lastErr = err
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				n.logger.Warn().
					Err(err).
					Int("delivered", delivered).
					Bool("zoom_out", zoomOut).
					Msg("native pinch aborted: the run's context is already cancelled, so this zoom did NOT happen")
				return err
			}
			break
		}
		delivered++
		// Sleep only BETWEEN attempts: the last one has nothing to wait for,
		// and a cancelled context should end the loop immediately rather than
		// after a fixed sleep the caller already knows is pointless.
		if i < zoomPinchRepeats-1 {
			time.Sleep(500 * time.Millisecond)
		}
	}
	if lastErr != nil {
		n.logger.Warn().Err(lastErr).Int("delivered", delivered).Bool("zoom_out", zoomOut).Msg("native pinch failed")
		return lastErr
	}
	n.logger.Debug().Int("delivered", delivered).Bool("zoom_out", zoomOut).Msg("native pinch completed")
	return nil
}

// PinchAtScaled runs a pinch whose four reference points are on the game world
// (village zoom), which is centred content.
func (n *Navigator) PinchAtScaled(x1, y1, x2, y2, x3, y3, x4, y4, ms int) error {
	sx1, sy1 := n.cal.Centre(x1, y1)
	sx2, sy2 := n.cal.Centre(x2, y2)
	sx3, sy3 := n.cal.Centre(x3, y3)
	sx4, sy4 := n.cal.Centre(x4, y4)
	return n.client.Pinch(sx1, sy1, sx2, sy2, sx3, sy3, sx4, sy4, ms)
}

// TapAtScaled taps a reference coordinate on HUD chrome. Callers with world or
// overlay coordinates should map through Centre instead.
func (n *Navigator) TapAtScaled(x, y int) error {
	sx, sy := n.cal.Hud(x, y)
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
		anchor   Anchor
	}{
		// End Battle is bottom-left HUD chrome; Return Home sits on the centred
		// battle-result panel. Measured at 1280x720: the bottom-left HUD keeps
		// its bottom margin (the Attack! label lands at y 669 of 720), while a
		// centred anchor there would put the button 191 px too low.
		{StateBattle, StateBattleEnd, ActionTap, 34, 588, AnchorEdge},
		{StateBattleEnd, StateReturnHome, ActionTap, 430, 566, AnchorCenter},
		{StateReturnHome, StateMainVillage, ActionTap, 430, 566, AnchorCenter},
		{StateArmyCamp, StateMainVillage, ActionBack, 0, 0, AnchorEdge},
		{StateSettings, StateMainVillage, ActionBack, 0, 0, AnchorEdge},
	}

	for _, step := range seq {
		if ctx.State == step.from {
			if step.action == ActionBack {
				n.client.Back()
			} else {
				// Both tap steps in the sequence are on centred screens: the End
				// Battle button (battle HUD) and the result panel's Return Home.
				sx, sy := n.cal.AnchorPoint(step.x, step.y, step.anchor)
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
		ax, ay := n.cal.Hud(60, 548)

		// Try to find the battle button via template matching
		if n.templates != nil {
			tpl, ok := n.templates.Get("btn_battle")
			if ok {
				screen, err := n.client.CaptureToMat()
				if err == nil {
					defer screen.Close()
					// Templates are captured at display scale 1.0, so sweep around the
					// live display scale, not a per-axis ratio.
					k := n.cal.DisplayScale()
					matches, err := vision.MatchMultiScale(screen, tpl, 0.9*k, 1.1*k, 3, 0.6)
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
		ax, ay := n.cal.Hud(40, 525)
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

	norm := vision.ResizeToHeight(raw, RefHeight)
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
		ax, ay := n.cal.Hud(60, 548)
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

	// Fallback coordinates for the yellow "Find a Match" button: bottom-stack
	// HUD chrome on the search screen.
	ax, ay := n.cal.Hud(150, 540)
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
		bx, by := n.cal.Hud(830, 16)
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
		bx, by := n.cal.Hud(830, 16)
		n.client.Tap(bx, by)
		time.Sleep(2000 * time.Millisecond)
		return true
	}

	return false
}
