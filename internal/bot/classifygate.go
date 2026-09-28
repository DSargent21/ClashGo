package bot

import (
	"math"
	"sync/atomic"
	"time"

	"github.com/Ducky705/ClashGO/internal/config"
	"github.com/Ducky705/ClashGO/internal/game"
	"github.com/Ducky705/ClashGO/internal/vision"
	"gocv.io/x/gocv"
)

// classifyGate decides whether a captured frame needs the classifier at all.
//
// The classifier is a pure function of the pixels, so a frame that has not moved
// cannot have a different verdict — and a classify is expensive. Measured on a
// 720p device (docs/PERFORMANCE.md, "Measured"): 200-490 ms per call while the
// capture loop runs every 300 ms, i.e. the vision layer, not the emulator, is
// what saturates a core while the bot waits in the village. Recognising "nothing
// moved" is therefore the cheapest CPU saving available to the bot.
//
// Safety comes from the margin between the two populations, measured on live
// frames: consecutive frames of the same scene differ by ~3 % of thumbnail
// pixels (water, particles, unit animation), while every real state transition
// measured differs by 55-95 % (search map, battle, result overlay, dialogs).
// The default threshold sits above the first and far below the second.
//
// Two properties keep the gate honest:
//
//   - It is fail-safe. vision.ChangedPercent reports 100 (changed) for anything
//     it cannot compare, and a stale verdict is never reused past maxStale, so
//     the gate can only ever do MORE work than it needed.
//   - It is off-switchable and observable. config.performance disables it;
//     BotStats reports how many classifies it skipped and how much the last
//     frame changed, so "is it firing" is answerable from the running bot
//     rather than inferred.
type classifyGate struct {
	enabled   bool
	threshold float64
	maxStale  time.Duration
	now       func() time.Time

	// prev is the signature of the previous frame — pooled, one small thumbnail,
	// not a retained frame.
	prev     gocv.Mat
	havePrev bool

	// pending holds the verdict the last real classify produced.
	pendingState game.GameState
	pendingScore int

	lastClassify time.Time

	classifies atomic.Int64
	reused     atomic.Int64
	// lastChangeBits is the last measured change percentage as float64 bits:
	// written by the capture loop, read by Stats on the Wails/CLI goroutine, so
	// it has to be atomic.
	lastChangeBits atomic.Uint64
}

// newClassifyGate builds the gate from config, applying the documented defaults
// for any non-positive setting (a config.json written by an older version
// carries the zero value, and zero is not a meaningful threshold).
func newClassifyGate(p config.PerformanceConfig) *classifyGate {
	return &classifyGate{
		enabled:   p.SkipUnchangedClassify,
		threshold: p.ClassifyThreshold(),
		maxStale:  p.ClassifyMaxStale(),
		now:       time.Now,
	}
}

// classifyFor returns the state and score for a captured frame, running classify
// only when the frame has moved enough to justify it. reuse reports whether the
// previous verdict was used instead of classifying.
//
// The comparison base advances on every frame, not only on classified ones: the
// question is "did the screen move since the last look", and a scene that keeps
// animating at 3 % per frame would otherwise accumulate past any threshold
// within a second and stop the gate from ever firing.
func (g *classifyGate) classifyFor(screen gocv.Mat, classify func(gocv.Mat) (game.GameState, int)) (state game.GameState, score int, reuse bool) {
	if !g.enabled || classify == nil {
		state, score := classify(screen)
		return state, score, false
	}

	now := g.now()
	cur := vision.Signature(screen)

	// A verdict older than maxStale is not eligible: the gate bounds its own
	// blind spot instead of trusting the threshold indefinitely.
	fresh := !g.lastClassify.IsZero() && now.Sub(g.lastClassify) < g.maxStale
	if g.havePrev && fresh {
		changed := vision.ChangedPercent(g.prev, cur, vision.DefaultChangeDelta)
		g.lastChangeBits.Store(math.Float64bits(changed))
		reuse = changed < g.threshold
	}

	if g.havePrev && !g.prev.Empty() {
		vision.PutMat(g.prev)
	}
	g.prev = cur
	g.havePrev = true

	if reuse {
		g.reused.Add(1)
		return g.pendingState, g.pendingScore, true
	}

	state, score = classify(screen)
	g.pendingState, g.pendingScore = state, score
	g.lastClassify = now
	g.classifies.Add(1)
	return state, score, false
}

// gateStats is what the running bot reports about the gate.
type gateStats struct {
	Classifies     int64
	Reused         int64
	LastChangePct  float64
	Enabled        bool
	Threshold      float64
	MaxStaleSecond float64
}

func (g *classifyGate) stats() gateStats {
	return gateStats{
		Classifies:     g.classifies.Load(),
		Reused:         g.reused.Load(),
		LastChangePct:  math.Float64frombits(g.lastChangeBits.Load()),
		Enabled:        g.enabled,
		Threshold:      g.threshold,
		MaxStaleSecond: g.maxStale.Seconds(),
	}
}

// close releases the retained signature. Safe on a nil gate and safe to call
// twice, so the bot's teardown does not need to track whether it ever ran.
func (g *classifyGate) close() {
	if g == nil {
		return
	}
	if g.havePrev && !g.prev.Empty() {
		vision.PutMat(g.prev)
	}
	g.prev = gocv.NewMat()
	g.havePrev = false
}
