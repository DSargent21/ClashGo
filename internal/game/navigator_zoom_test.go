package game

import (
	"context"
	"errors"
	"image/color"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"gocv.io/x/gocv"
)

// pinchZoomRecorder scripts PinchZoom so the zoom loop can be driven without a
// device. It embeds the package's existing fakeDevice (chestdismiss_test.go) so
// only the one method under test has to be defined here.
type pinchZoomRecorder struct {
	fakeDevice
	pinchErrs []error // one per attempt; a nil entry means "delivered"
	attempts  int

	// frames, when non-nil, are returned by CaptureToMat one per capture; once
	// they run out the last one repeats. They drive ZoomOutMax's change loop.
	frames   []color.RGBA
	captures int
	capErr   error // returned by every CaptureToMat when non-nil
}

func (p *pinchZoomRecorder) PinchZoom(zoomOut bool) error {
	i := p.attempts
	p.attempts++
	if i < len(p.pinchErrs) {
		return p.pinchErrs[i]
	}
	return nil
}

func (p *pinchZoomRecorder) CaptureToMat() (gocv.Mat, error) {
	if p.capErr != nil {
		return gocv.Mat{}, p.capErr
	}
	if len(p.frames) == 0 {
		return gocv.Mat{}, errors.New("no frames scripted")
	}
	c := p.frames[len(p.frames)-1]
	if p.captures < len(p.frames) {
		c = p.frames[p.captures]
	}
	p.captures++
	// 16x16 clears vision.Signature's minimum, and a uniform colour per frame
	// makes "changed" unambiguous: any per-channel delta above the threshold
	// only when the scripted colour differs.
	m := gocv.NewMatWithSize(16, 16, gocv.MatTypeCV8UC3)
	scalar := gocv.NewScalar(float64(c.B), float64(c.G), float64(c.R), 0)
	m.SetTo(scalar)
	return m, nil
}

func newPinchNavigator(d Device) *Navigator {
	return NewNavigator(d, nil, NewStateGraph(), nil, zerolog.Nop())
}

// Live 2026-09-30: the mandatory zoom-out logged "native pinch failed: context
// canceled" and the caller discarded the result, so the run attacked with
// whatever zoom the game already had. ZoomOut must report the failure.
func TestZoomOutReportsCancellationInsteadOfSwallowingIt(t *testing.T) {
	dev := &pinchZoomRecorder{pinchErrs: []error{context.Canceled}}
	n := newPinchNavigator(dev)

	err := n.ZoomOut()
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("ZoomOut() = %v, want context.Canceled: a cancelled run must be told its zoom did not happen", err)
	}
	if dev.attempts != 1 {
		t.Errorf("made %d pinch attempts, want 1: a cancelled context must stop immediately, not retry", dev.attempts)
	}
}

// The cancellation is reported distinctly from a device malfunction, because
// they mean different things: one is a dead run, the other is a broken zoom.
func TestPinchStopsOnDeviceErrorAndReturnsIt(t *testing.T) {
	boom := errors.New("injection failed")
	dev := &pinchZoomRecorder{pinchErrs: []error{boom}}
	n := newPinchNavigator(dev)

	if err := n.ZoomIn(); !errors.Is(err, boom) {
		t.Fatalf("ZoomIn() = %v, want %v", err, boom)
	}
	if dev.attempts != 1 {
		t.Errorf("made %d attempts, want 1: stop at the first failure", dev.attempts)
	}
}

// Repeats are insurance against a pinch landing while the camera settles, so a
// healthy zoom must still fire all of them and report success.
func TestPinchRepeatsAndReportsSuccess(t *testing.T) {
	dev := &pinchZoomRecorder{}
	n := newPinchNavigator(dev)

	if err := n.ZoomOut(); err != nil {
		t.Fatalf("ZoomOut() = %v, want nil", err)
	}
	if dev.attempts != zoomPinchRepeats {
		t.Errorf("made %d attempts, want %d", dev.attempts, zoomPinchRepeats)
	}
}

// A failure on the LAST repeat must still be reported: the old loop returned
// void, so a run could report a clean zoom having delivered only some of them.
func TestPinchReportsFailureOnTheLastRepeat(t *testing.T) {
	boom := errors.New("third pinch failed")
	errs := make([]error, zoomPinchRepeats)
	for i := range errs {
		errs[i] = nil
	}
	errs[zoomPinchRepeats-1] = boom
	dev := &pinchZoomRecorder{pinchErrs: errs}
	n := newPinchNavigator(dev)

	if err := n.ZoomOut(); !errors.Is(err, boom) {
		t.Fatalf("ZoomOut() = %v, want %v", err, boom)
	}
}

// An already-cancelled context must not burn the repeat sleeps: the caller
// already knows the run is over, so a fixed ~1s of sleeping is pure latency on
// the teardown path.
func TestPinchDoesNotSleepAfterCancellation(t *testing.T) {
	dev := &pinchZoomRecorder{pinchErrs: []error{context.Canceled}}
	n := newPinchNavigator(dev)

	start := time.Now()
	if err := n.ZoomOut(); !errors.Is(err, context.Canceled) {
		t.Fatalf("ZoomOut() = %v, want context.Canceled", err)
	}
	if elapsed := time.Since(start); elapsed > 200*time.Millisecond {
		t.Errorf("a cancelled zoom took %v: the loop slept between repeats without checking the context", elapsed)
	}
}

// ---- ZoomOutMax: the closed-loop variant ----

// Stable frames must differ by LESS than vision.DefaultDiffThreshold (12) per
// channel so the change detector reads them as unmoved; the moved frames differ
// by far more.
var (
	frameZoomedIn   = color.RGBA{R: 30, G: 90, B: 40, A: 255}  // a frame mid-zoom
	frameZoomedOut  = color.RGBA{R: 90, G: 150, B: 70, A: 255} // clearly different
	frameSettled    = color.RGBA{R: 96, G: 155, B: 74, A: 255} // within 6/channel of zoomedOut
	frameMovingMid1 = color.RGBA{R: 60, G: 120, B: 50, A: 255} // between the two
	frameMovingMid2 = color.RGBA{R: 75, G: 135, B: 60, A: 255} // between mid1 and out
)

// The live behaviour ZoomOutMax exists for: ONE pinch saturates the game's
// zoom-out, and the loop needs one MORE pinch to prove the view has stopped —
// a no-op pinch is only knowable after firing it. So saturation costs exactly
// two: one that moved, one that confirmed. The old fixed loop fired three
// without ever knowing whether any of them worked.
func TestZoomOutMaxStopsWhenTheViewStopsChanging(t *testing.T) {
	dev := &pinchZoomRecorder{
		frames: []color.RGBA{
			frameZoomedIn,  // pre-pinch
			frameZoomedOut, // post-pinch: moved
			frameSettled,   // pre-pinch 2 (game idle at max zoom-out)
			frameSettled,   // post-pinch 2: stable -> done
		},
	}
	n := newPinchNavigator(dev)

	if err := n.ZoomOutMax(); err != nil {
		t.Fatalf("ZoomOutMax() = %v, want nil", err)
	}
	if dev.attempts != 2 {
		t.Errorf("fired %d pinches, want 2: one that moved the view, one that confirmed saturation", dev.attempts)
	}
}

// A view that keeps responding keeps getting pinched, until it settles. Frames
// alternate capture, pinch, capture: pre-frame, two moving views, then a stable
// pair that ends the loop on the third pinch.
func TestZoomOutMaxKeepsPinchingWhileTheViewMoves(t *testing.T) {
	dev := &pinchZoomRecorder{
		frames: []color.RGBA{
			frameZoomedIn,   // pre-pinch 1
			frameMovingMid1, // post-pinch 1: moved
			frameMovingMid1, // pre-pinch 2
			frameZoomedOut,  // post-pinch 2: moved
			frameZoomedOut,  // pre-pinch 3
			frameSettled,    // post-pinch 3: stable -> done
		},
	}
	n := newPinchNavigator(dev)

	if err := n.ZoomOutMax(); err != nil {
		t.Fatalf("ZoomOutMax() = %v, want nil", err)
	}
	if dev.attempts != 3 {
		t.Errorf("fired %d pinches, want 3 (two that moved, one that confirmed stability)", dev.attempts)
	}
}

// A capture failure is NOT "nothing changed": an unverifiable zoom must be
// reported, or the caller deploys on a view it never saw.
func TestZoomOutMaxReportsACaptureFailure(t *testing.T) {
	boom := errors.New("capture died")
	dev := &pinchZoomRecorder{capErr: boom}
	n := newPinchNavigator(dev)

	if err := n.ZoomOutMax(); !errors.Is(err, boom) {
		t.Fatalf("ZoomOutMax() = %v, want %v", err, boom)
	}
	if dev.attempts != 0 {
		t.Errorf("fired %d pinches, want 0: the pre-pinch capture failed, so the zoom could not be verified", dev.attempts)
	}
}

// Cancellation propagates unchanged through the closed loop too.
func TestZoomOutMaxPropagatesCancellation(t *testing.T) {
	dev := &pinchZoomRecorder{
		pinchErrs: []error{context.Canceled},
		frames:    []color.RGBA{frameZoomedIn}, // the pre-pinch capture succeeds first
	}
	n := newPinchNavigator(dev)

	if err := n.ZoomOutMax(); !errors.Is(err, context.Canceled) {
		t.Fatalf("ZoomOutMax() = %v, want context.Canceled", err)
	}
}

// The attempt budget is a ceiling: a view that never stops moving must not pin
// forever, and the run continues (nil) with a warning rather than aborting.
func TestZoomOutMaxGivesUpAfterTheAttemptBudget(t *testing.T) {
	// Every comparison must read as CHANGED so the loop never converges:
	// strict alternation of two far-apart colours makes consecutive frames
	// (each attempt's pre and post) differ by ~100% every time.
	frames := make([]color.RGBA, 0, zoomOutMaxAttempts*2)
	for i := 0; i < zoomOutMaxAttempts*2; i++ {
		if i%2 == 0 {
			frames = append(frames, frameZoomedIn)
		} else {
			frames = append(frames, frameZoomedOut)
		}
	}
	dev := &pinchZoomRecorder{frames: frames}
	n := newPinchNavigator(dev)

	if err := n.ZoomOutMax(); err != nil {
		t.Fatalf("ZoomOutMax() = %v, want nil (an unverified-but-maxed zoom is a warning, not an error)", err)
	}
	if dev.attempts != zoomOutMaxAttempts {
		t.Errorf("fired %d pinches, want %d (the budget)", dev.attempts, zoomOutMaxAttempts)
	}
}
