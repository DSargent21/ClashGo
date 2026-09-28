package bot

import (
	"image"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Ducky705/ClashGO/internal/config"
	"github.com/Ducky705/ClashGO/internal/game"
	"gocv.io/x/gocv"
)

// gateFrame builds a solid BGR frame big enough to have a signature.
func gateFrame(b, g, r uint8) gocv.Mat {
	m := gocv.NewMatWithSize(720, 1280, gocv.MatTypeCV8UC3)
	m.SetTo(gocv.NewScalar(float64(b), float64(g), float64(r), 0))
	return m
}

// gatePaint fills a rectangle, so a test can make a change of a known size.
func gatePaint(m gocv.Mat, rect image.Rectangle, b, g, r uint8) {
	for y := rect.Min.Y; y < rect.Max.Y; y++ {
		for x := rect.Min.X; x < rect.Max.X; x++ {
			m.SetUCharAt(y, x*3, b)
			m.SetUCharAt(y, x*3+1, g)
			m.SetUCharAt(y, x*3+2, r)
		}
	}
}

// countingClassifier records how often the gate let it run and answers by frame
// colour, so a test can tell a fresh verdict from a reused one.
type countingClassifier struct {
	calls  atomic.Int64
	answer func(gocv.Mat) game.GameState
}

func (c *countingClassifier) classify(m gocv.Mat) (game.GameState, int) {
	c.calls.Add(1)
	return c.answer(m), 100
}

// fixedClock lets a test move time without sleeping, which is the only way to
// exercise the staleness bound deterministically.
type fixedClock struct{ at time.Time }

func (c *fixedClock) now() time.Time          { return c.at }
func (c *fixedClock) advance(d time.Duration) { c.at = c.at.Add(d) }

func newTestGate(t *testing.T, cfg config.PerformanceConfig) (*classifyGate, *fixedClock) {
	t.Helper()
	g := newClassifyGate(cfg)
	clock := &fixedClock{at: time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)}
	g.now = clock.now
	t.Cleanup(g.close)
	return g, clock
}

// The core contract: an unchanged frame does not need the classifier, because
// the classifier is a pure function of the pixels.
func TestClassifyGateReusesVerdictForUnchangedFrame(t *testing.T) {
	gate, clock := newTestGate(t, config.PerformanceConfig{SkipUnchangedClassify: true})
	classifier := &countingClassifier{answer: func(gocv.Mat) game.GameState { return game.StateMainVillage }}

	frame := gateFrame(0, 0, 0)
	defer frame.Close()

	state, score, reused := gate.classifyFor(frame, classifier.classify)
	if reused {
		t.Fatal("the first frame has nothing to reuse; it must be classified")
	}
	if state != game.StateMainVillage || score != 100 {
		t.Errorf("first frame -> %s (%d), want MainVillage (100)", state, score)
	}

	// Same pixels again, 300 ms later (the village capture cadence).
	clock.advance(300 * time.Millisecond)
	state, score, reused = gate.classifyFor(frame, classifier.classify)
	if !reused {
		t.Error("an unchanged frame was classified again; the gate did not fire")
	}
	if state != game.StateMainVillage || score != 100 {
		t.Errorf("reused verdict -> %s (%d), want the cached MainVillage (100)", state, score)
	}
	if calls := classifier.calls.Load(); calls != 1 {
		t.Errorf("classifier ran %d times for two identical frames, want 1", calls)
	}
	if stats := gate.stats(); stats.Reused != 1 || stats.Classifies != 1 {
		t.Errorf("stats = %+v, want 1 classify + 1 reuse", stats)
	}
}

// A frame that really moved must be classified, and the verdict must be the new
// one — this is the half that a broken gate would silently get wrong.
func TestClassifyGateClassifiesAChangedFrame(t *testing.T) {
	gate, clock := newTestGate(t, config.PerformanceConfig{SkipUnchangedClassify: true})
	classifier := &countingClassifier{answer: func(m gocv.Mat) game.GameState {
		// A white block in the top-left corner means "battle" for this test.
		if m.GetUCharAt(10, 10) > 128 {
			return game.StateBattle
		}
		return game.StateMainVillage
	}}

	frame := gateFrame(0, 0, 0)
	defer frame.Close()
	if state, _, _ := gate.classifyFor(frame, classifier.classify); state != game.StateMainVillage {
		t.Fatalf("baseline -> %s, want MainVillage", state)
	}

	// A 640x360 block is 25 % of the frame: far past any animation floor.
	gatePaint(frame, image.Rect(0, 0, 640, 360), 255, 255, 255)
	clock.advance(300 * time.Millisecond)

	state, _, reused := gate.classifyFor(frame, classifier.classify)
	if reused {
		t.Fatal("a quarter of the frame changed and the gate still reused the old verdict")
	}
	if state != game.StateBattle {
		t.Errorf("changed frame -> %s, want the fresh Battle verdict", state)
	}
	if calls := classifier.calls.Load(); calls != 2 {
		t.Errorf("classifier ran %d times, want 2 (baseline + change)", calls)
	}

	// And the new verdict is what gets reused while nothing moves again.
	clock.advance(300 * time.Millisecond)
	if state, _, reused := gate.classifyFor(frame, classifier.classify); !reused || state != game.StateBattle {
		t.Errorf("after the change: reused=%v state=%s, want true/Battle", reused, state)
	}
}

// Small movement — the animation floor the gate is built on — must not force a
// classify, or the optimization never fires on a live scene.
func TestClassifyGateIgnoresSubThresholdMovement(t *testing.T) {
	gate, clock := newTestGate(t, config.PerformanceConfig{SkipUnchangedClassify: true})
	classifier := &countingClassifier{answer: func(gocv.Mat) game.GameState { return game.StateMainVillage }}

	frame := gateFrame(0, 0, 0)
	defer frame.Close()
	gate.classifyFor(frame, classifier.classify)

	// A 64x36 block is 0.25 % of the frame, well under the 6 % default.
	gatePaint(frame, image.Rect(0, 0, 64, 36), 255, 255, 255)
	clock.advance(300 * time.Millisecond)

	if _, _, reused := gate.classifyFor(frame, classifier.classify); !reused {
		t.Errorf("a 0.25 %% change forced a classify (threshold %.1f)", gate.threshold)
	}
}

// The staleness bound is the gate's own safety net: whatever the thumbnail
// cannot see, the bot re-checks within maxStale.
func TestClassifyGateRefreshesAfterMaxStale(t *testing.T) {
	cfg := config.PerformanceConfig{SkipUnchangedClassify: true, ClassifyMaxStaleSec: 1}
	gate, clock := newTestGate(t, cfg)
	classifier := &countingClassifier{answer: func(gocv.Mat) game.GameState { return game.StateMainVillage }}

	frame := gateFrame(0, 0, 0)
	defer frame.Close()
	gate.classifyFor(frame, classifier.classify)

	clock.advance(300 * time.Millisecond)
	if _, _, reused := gate.classifyFor(frame, classifier.classify); !reused {
		t.Fatal("inside the staleness window an unchanged frame was classified")
	}

	clock.advance(2 * time.Second) // past the 1 s bound
	if _, _, reused := gate.classifyFor(frame, classifier.classify); reused {
		t.Error("past maxStale the verdict was reused; the blind spot is unbounded")
	}
	if calls := classifier.calls.Load(); calls != 2 {
		t.Errorf("classifier ran %d times, want 2 (first frame + stale refresh)", calls)
	}
}

// With the gate off, every frame is classified and no signature work happens at
// all — the pre-existing behaviour, available as a single config flip.
func TestClassifyGateDisabledClassifiesEveryFrame(t *testing.T) {
	gate, clock := newTestGate(t, config.PerformanceConfig{SkipUnchangedClassify: false})
	classifier := &countingClassifier{answer: func(gocv.Mat) game.GameState { return game.StateMainVillage }}

	frame := gateFrame(0, 0, 0)
	defer frame.Close()
	for i := 0; i < 3; i++ {
		clock.advance(300 * time.Millisecond)
		if _, _, reused := gate.classifyFor(frame, classifier.classify); reused {
			t.Fatalf("frame %d was reused with the gate disabled", i)
		}
	}
	if calls := classifier.calls.Load(); calls != 3 {
		t.Errorf("classifier ran %d times with the gate disabled, want 3", calls)
	}
	if gate.havePrev {
		t.Error("a disabled gate retained a signature")
	}
}

// A gate built from an older config.json (all zeroes) must fall back to the
// measured defaults rather than treating "0 %" as a threshold.
func TestClassifyGateDefaultsFromZeroConfig(t *testing.T) {
	gate, _ := newTestGate(t, config.PerformanceConfig{SkipUnchangedClassify: true})
	if gate.threshold != config.DefaultClassifyChangeThreshold {
		t.Errorf("zero-config threshold = %v, want the default %v", gate.threshold, config.DefaultClassifyChangeThreshold)
	}
	if want := time.Duration(config.DefaultClassifyMaxStaleSec * float64(time.Second)); gate.maxStale != want {
		t.Errorf("zero-config maxStale = %v, want %v", gate.maxStale, want)
	}
	if !gate.enabled {
		t.Error("a zero PerformanceConfig lost the configured enabled flag")
	}
}

// close must be safe on a nil gate and idempotent, because teardown should not
// have to track whether the gate ever ran.
func TestClassifyGateCloseIsSafe(t *testing.T) {
	var nilGate *classifyGate
	nilGate.close() // must not panic

	gate, _ := newTestGate(t, config.PerformanceConfig{SkipUnchangedClassify: true})
	frame := gateFrame(0, 0, 0)
	defer frame.Close()
	gate.classifyFor(frame, func(gocv.Mat) (game.GameState, int) { return game.StateMainVillage, 1 })

	gate.close()
	gate.close()
	if gate.havePrev {
		t.Error("close left a retained signature")
	}
}
