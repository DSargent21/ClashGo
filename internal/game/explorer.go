package game

import (
	"fmt"
	"image"
	"math"
	"time"

	"gocv.io/x/gocv"
	"github.com/Ducky705/ClashGo/internal/adb"
	"github.com/rs/zerolog"
)

type Explorer struct {
	cfg       ExplorerConfig
	cal       *Calibration
	graph     *StateGraph
	templates *TemplateStore
	client    *adb.Client
	classify  func(gocv.Mat) (GameState, int)
	rec       *Recognizer
	logger    zerolog.Logger

	depth   int
	visited map[uint64]bool
	nav     *Navigator
}

func NewExplorer(
	client *adb.Client,
	cal *Calibration,
	graph *StateGraph,
	templates *TemplateStore,
	classify func(gocv.Mat) (GameState, int),
	logger zerolog.Logger,
) *Explorer {
	return &Explorer{
		cfg:       DefaultExplorerConfig(),
		cal:       cal,
		graph:     graph,
		templates: templates,
		client:    client,
		classify:  classify,
		rec:       NewRecognizer(),
		visited:   make(map[uint64]bool),
		logger:    logger.With().Str("component", "explorer").Logger(),
	}
}

func (e *Explorer) Explore(ctx *GameContext) error {
	e.logger.Info().Msg("starting auto-mapping")

	if err := e.exploreFromState(ctx); err != nil {
		return err
	}

	e.logger.Info().
		Int("states", e.graph.StateCount()).
		Int("edges", e.graph.EdgeCount()).
		Msg("mapping complete")

	return nil
}

func (e *Explorer) exploreFromState(ctx *GameContext) error {
	if e.depth > e.cfg.MaxDepth {
		return nil
	}

	screen, err := e.client.CaptureToMat()
	if err != nil {
		return fmt.Errorf("explorer capture: %w", err)
	}
	defer screen.Close()

	hash := e.rec.ScreenHash(screen)
	if e.visited[hash] {
		return nil
	}
	e.visited[hash] = true

	state, score := e.classify(screen)
	e.graph.AddNode(state)
	e.logger.Debug().Str("state", state.String()).Int("score", score).Msg("found state")

	elems := e.findClickableElements(screen)
	e.logger.Debug().Int("elements", len(elems)).Msg("found clickables")

	for _, elem := range elems {
		if err := e.tryElement(ctx, state, elem); err != nil {
			e.logger.Warn().Err(err).Msg("element click failed")
		}
	}

	return nil
}

func (e *Explorer) tryElement(ctx *GameContext, fromState GameState, elem Clickable) error {
	screenBefore, err := e.client.CaptureToMat()
	if err != nil {
		return err
	}
	hashBefore := e.rec.ScreenHash(screenBefore)
	screenBefore.Close()

	sx, sy := elem.Center.X, elem.Center.Y
	if e.cfg.ClickJitter > 0 {
		e.client.TapRandomized(sx, sy)
	} else {
		e.client.Tap(sx, sy)
	}

	time.Sleep(e.cfg.SettleTime)

	screenAfter, err := e.client.CaptureToMat()
	if err != nil {
		return err
	}
	defer screenAfter.Close()

	hashAfter := e.rec.ScreenHash(screenAfter)
	if hashAfter == hashBefore {
		return nil
	}

	toState, score := e.classify(screenAfter)
	e.graph.AddNode(toState)
	e.graph.AddTransition(fromState, toState, ActionTap, sx, sy)

	if toState != StateUnknown {
		e.saveTemplateForState(toState, elem.Region, screenAfter)
	}

	e.logger.Info().
		Str("from", fromState.String()).
		Str("to", toState.String()).
		Str("action", "tap").
		Int("x", sx).
		Int("y", sy).
		Int("score", score).
		Msg("transition")

	if !e.visited[e.rec.ScreenHash(screenAfter)] && e.depth < e.cfg.MaxDepth {
		e.depth++

		if !e.navigateBack() {
			e.navigateViaSafeZone()
		}

		e.exploreFromState(ctx)
		e.depth--
	}

	return nil
}

func (e *Explorer) navigateBack() bool {
	for i := 0; i < 3; i++ {
		e.client.Back()
		time.Sleep(e.cfg.SettleTime)

		screen, err := e.client.CaptureToMat()
		if err != nil {
			continue
		}
		defer screen.Close()

		state, _ := e.classify(screen)
		if state != StateUnknown && state != StateObstacleDialog && state != StateGemDialog {
			return true
		}
	}
	return false
}

func (e *Explorer) navigateViaSafeZone() {
	candidates := []image.Point{
		{X: 430, Y: 650},
		{X: 65, Y: 545},
		{X: 175, Y: 30},
	}

	for _, pt := range candidates {
		sx, sy := e.cal.ScaleRef(pt.X, pt.Y)
		e.client.TapRandomized(sx, sy)
		time.Sleep(e.cfg.SettleTime)

		screen, err := e.client.CaptureToMat()
		if err != nil {
			continue
		}
		defer screen.Close()

		state, _ := e.classify(screen)
		if state != StateUnknown {
			return
		}
	}
}

func (e *Explorer) findClickableElements(screen gocv.Mat) []Clickable {
	var elements []Clickable

	regions := e.rec.FindButtonLikeRegions(screen)
	for _, r := range regions {
		if r.Width() < 30 || r.Height() < 20 {
			continue
		}
		if r.Width() > 400 || r.Height() > 200 {
			continue
		}

		avgR, avgG, avgB, _ := e.rec.RegionMeanColor(screen, toImageRect(r))
		elements = append(elements, Clickable{
			Type:      "button",
			Region:    r,
			Center:    Point{X: r.CenterX(), Y: r.CenterY()},
			Color:     RGB{R: uint8(avgR), G: uint8(avgG), B: uint8(avgB)},
			Confidence: 0.7,
		})
	}

	knownPoints := e.getKnownButtonLocations()
	for _, pt := range knownPoints {
		sx, sy := e.cal.ScaleRef(pt.X, pt.Y)
		if sx <= 0 || sy <= 0 || sx >= screen.Cols() || sy >= screen.Rows() {
			continue
		}

		isDup := false
		for _, elem := range elements {
			dx := elem.Center.X - sx
			dy := elem.Center.Y - sy
			if math.Sqrt(float64(dx*dx+dy*dy)) < 30 {
				isDup = true
				break
			}
		}

		if !isDup {
			avgR, avgG, avgB, _ := e.rec.RegionMeanColor(screen, image.Rect(sx-20, sy-20, sx+20, sy+20))
			elements = append(elements, Clickable{
				Type:      "button",
				Region:    Rectangle{X1: sx - 20, Y1: sy - 20, X2: sx + 20, Y2: sy + 20},
				Center:    Point{X: sx, Y: sy},
				Color:     RGB{R: uint8(avgR), G: uint8(avgG), B: uint8(avgB)},
				Confidence: 0.9,
			})
		}
	}

	return elements
}

func (e *Explorer) getKnownButtonLocations() []Point {
	return []Point{
		{60, 548},
		{40, 525},
		{470, 20},
		{430, 566},
		{50, 548},
		{824, 555},
		{830, 16},
		{714, 538},
		{739, 538},
		{763, 538},
		{675, 155},
		{608, 240},
		{785, 146},
		{392, 290},
		{378, 10},
		{838, 16},
		{56, 592},
		{481, 490},
		{428, 506},
	}
}

func (e *Explorer) saveTemplateForState(state GameState, rgn Rectangle, screen gocv.Mat) {
	if e.templates == nil {
		return
	}

	name := fmt.Sprintf("%s_%dx%d", state.String(), rgn.CenterX(), rgn.CenterY())
	r := toImageRect(rgn)

	cropped := screen.Region(r)
	defer cropped.Close()

	if err := e.templates.Save(name, state, r, screen); err != nil {
		e.logger.Debug().Err(err).Msg("failed to save template")
	}
}

func (e *Explorer) ExploreWithUserConfirmation(ctx *GameContext, states []GameState) error {
	for _, state := range states {
		e.logger.Info().Str("state", state.String()).Msg("navigating to target state")

		if !e.navigateToState(ctx, state) {
			e.logger.Warn().Str("state", state.String()).Msg("could not reach state")
			continue
		}

		time.Sleep(2 * time.Second)

		screen, err := e.client.CaptureToMat()
		if err != nil {
			continue
		}

		elems := e.findClickableElements(screen)
		for _, elem := range elems {
			e.saveTemplateForState(state, elem.Region, screen)
		}
		screen.Close()

		e.graph.AddNode(state)
	}

	return nil
}

func (e *Explorer) navigateToState(ctx *GameContext, target GameState) bool {
	return e.nav.Navigate(ctx, target)
}

func (e *Explorer) SetNavigator(nav *Navigator) {
	e.nav = nav
}

func toImageRect(r Rectangle) image.Rectangle {
	return image.Rect(r.X1, r.Y1, r.X2, r.Y2)
}