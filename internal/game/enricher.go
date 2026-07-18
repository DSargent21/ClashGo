package game

import (
	"image"
	"time"

	"github.com/Ducky705/ClashGO/internal/bus"
	"github.com/Ducky705/ClashGO/internal/vision"
	"github.com/rs/zerolog"
	"gocv.io/x/gocv"
)

type Enricher struct {
	bus       *bus.EventBus
	ring      *bus.RingBuffer
	cal       *Calibration
	templates *TemplateStore
	logger    zerolog.Logger
	stopCh    chan struct{}
	interval  time.Duration
	loot      *LootRecognizer
}

func NewEnricher(b *bus.EventBus, r *bus.RingBuffer, cal *Calibration, ts *TemplateStore, logger zerolog.Logger) *Enricher {
	var loot *LootRecognizer
	if ts != nil {
		loot = NewLootRecognizer(cal, ts, logger)
	}
	return &Enricher{
		bus:       b,
		ring:      r,
		cal:       cal,
		templates: ts,
		logger:    logger.With().Str("component", "enricher").Logger(),
		stopCh:    make(chan struct{}),
		interval:  time.Second,
		loot:      loot,
	}
}

func (e *Enricher) Start() {
	if e == nil || e.bus == nil || !e.bus.Enabled() || e.ring == nil {
		return
	}
	go e.run()
}

func (e *Enricher) Stop() {
	if e == nil {
		return
	}
	select {
	case <-e.stopCh:
		return
	default:
	}
	close(e.stopCh)
	if e.loot != nil {
		e.loot.Close()
	}
}

func (e *Enricher) run() {
	ticker := time.NewTicker(e.interval)
	defer ticker.Stop()
	for {
		select {
		case <-e.stopCh:
			return
		case <-ticker.C:
			e.process()
		}
	}
}

func (e *Enricher) process() {
	if e.templates == nil && e.loot == nil {
		return
	}
	frame := e.ring.LatestFrame()
	if frame == nil || len(frame.JPEG) == 0 {
		return
	}
	mat, err := gocv.IMDecode(frame.JPEG, gocv.IMReadColor)
	if err != nil || mat.Empty() {
		return
	}
	defer mat.Close()

	ev := &bus.EnrichedStateEvent{
		TimestampMs: time.Now().UnixMilli(),
		BaseState:   frame.State,
	}

	if e.templates != nil {
		ev.UiElements = e.detectElements(mat)
	}
	if e.loot != nil && (frame.State == bus.GameState_MAIN_VILLAGE || frame.State == bus.GameState_BATTLE || frame.State == bus.GameState_BATTLE_END) {
		report, err := e.loot.ReadLootDetailed(mat)
		if err == nil {
			ev.Loot = &bus.LootInfo{
				Gold:       int32(report.Resources.Gold),
				Elixir:     int32(report.Resources.Elixir),
				DarkElixir: int32(report.Resources.DarkElixir),
				Valid:      report.Resources.Gold > 0 || report.Resources.Elixir > 0 || report.Resources.DarkElixir > 0,
			}
		}
	}

	if len(ev.UiElements) > 0 || (ev.Loot != nil && ev.Loot.Valid) {
		if err := e.bus.PublishEnrichedState(ev); err != nil {
			e.logger.Debug().Err(err).Msg("publish enriched state failed")
		}
	}
}

func (e *Enricher) detectElements(mat gocv.Mat) []*bus.UIElement {
	names := []struct {
		name string
		kind string
		roi  int
	}{
		{"btn_attack", "button", 1},
		{"btn_find_match", "button", 1},
		{"btn_battle", "button", 1},
		{"btn_army_arrow", "button", 1},
		{"btn_army_1", "button", 1},
		{"btn_next", "button", 1},
		{"btn_return_home", "button", 1},
		{"btn_okay", "button", 1},
	}
	elements := make([]*bus.UIElement, 0, len(names))
	for _, item := range names {
		tpl, ok := e.templates.Get(item.name)
		if !ok || tpl.Empty() {
			continue
		}
		matches, err := vision.MatchMultiScaleROICached(mat, tpl, item.name, 0.9, 1.1, 2, 0.45, imageRectFull(mat))
		if err != nil || len(matches) == 0 {
			continue
		}
		m := matches[0]
		elements = append(elements, &bus.UIElement{
			Name:        item.name,
			X:           int32(m.Point.X),
			Y:           int32(m.Point.Y),
			Confidence:  float32(m.Confidence),
			ElementType: item.kind,
		})
	}
	return elements
}

func imageRectFull(mat gocv.Mat) image.Rectangle {
	return image.Rect(0, 0, mat.Cols(), mat.Rows())
}
