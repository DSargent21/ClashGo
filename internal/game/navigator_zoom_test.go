package game

import (
	"context"
	"errors"
	"image"
	"image/color"
	"slices"
	"testing"
	"time"

	"github.com/Ducky705/ClashGO/internal/vision"
	"github.com/rs/zerolog"
	"gocv.io/x/gocv"
)

// pinchZoomRecorder scripts PinchZoomVariant so the zoom ladder can be driven
// without a device. It embeds the package's existing fakeDevice
// (chestdismiss_test.go) so only the methods under test have to be defined here.
type pinchZoomRecorder struct {
	fakeDevice
	pinchErrs []error // one per gesture; a nil entry means "delivered"
	attempts  int

	// variants records the gesture shape of every delivered pinch, in order.
	variants []int

	// frames, when non-nil, are returned by CaptureToMat one per capture; once
	// they run out the last one repeats. They drive the change detection.
	frames   []color.RGBA
	captures int
	capErr   error // returned by every CaptureToMat when non-nil

	// patches, parallel to frames, paints a white block over the middle of that
	// capture: an overlay or popup, i.e. a change that is NOT a camera move.
	patches []bool
}

func (p *pinchZoomRecorder) PinchZoom(zoomOut bool) error { return p.PinchZoomVariant(zoomOut, 0) }

func (p *pinchZoomRecorder) PinchZoomVariant(zoomOut bool, variant int) error {
	p.variants = append(p.variants, variant)
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
	idx := p.captures
	if idx >= len(p.frames) {
		idx = len(p.frames) - 1
	}
	p.captures++
	// 16x16 clears vision.Signature's minimum, and a uniform colour per frame
	// makes "changed" unambiguous: any per-channel delta above the threshold
	// only when the scripted colour differs.
	m := uniformMat(p.frames[idx])
	if idx < len(p.patches) && p.patches[idx] {
		// 6x6 over the middle third: one cell of the 3x3 grid, ~10% of the
		// frame. Over the 5% change threshold, under the broad-zoom bar.
		block := m.Region(image.Rect(5, 5, 11, 11))
		block.SetTo(gocv.NewScalar(255, 255, 255, 0))
		block.Close()
	}
	return m, nil
}

// uniformMat is a 16x16 CV_8UC3 frame of one colour.
func uniformMat(c color.RGBA) gocv.Mat {
	m := gocv.NewMatWithSize(16, 16, gocv.MatTypeCV8UC3)
	m.SetTo(gocv.NewScalar(float64(c.B), float64(c.G), float64(c.R), 0))
	return m
}

func newPinchNavigator(d Device) *Navigator {
	return NewNavigator(d, nil, NewStateGraph(), nil, zerolog.Nop())
}

// Stable frames must differ by LESS than vision.DefaultDiffThreshold (12) per
// channel so the change detector reads them as unmoved; the moved frames differ
// by far more.
var (
	frameZoomedIn   = color.RGBA{R: 30, G: 90, B: 40, A: 255}  // a frame mid-zoom
	frameZoomedOut  = color.RGBA{R: 90, G: 150, B: 70, A: 255} // clearly different
	frameSettled    = color.RGBA{R: 96, G: 155, B: 74, A: 255} // within 6/channel of zoomedOut
	frameMovingMid1 = color.RGBA{R: 60, G: 120, B: 50, A: 255} // between the two
)

// ---- ZoomOut: one verified step, with the gesture shape as a search space ----

// Live 2026-09-30: the mandatory zoom-out logged "native pinch failed: context
// canceled" and the caller discarded the result, so the run attacked with
// whatever zoom the game already had. ZoomOut must report the failure.
func TestZoomOutReportsCancellationInsteadOfSwallowingIt(t *testing.T) {
	dev := &pinchZoomRecorder{
		pinchErrs: []error{context.Canceled},
		frames:    []color.RGBA{frameZoomedIn},
	}
	n := newPinchNavigator(dev)

	err := n.ZoomOut()
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("ZoomOut() = %v, want context.Canceled: a cancelled run must be told its zoom did not happen", err)
	}
	if dev.attempts != 1 {
		t.Errorf("made %d pinch attempts, want 1: a cancelled context must stop immediately, not retry", dev.attempts)
	}
}

// A device malfunction is reported as itself, not as an inert gesture.
func TestZoomOutReportsDeviceError(t *testing.T) {
	boom := errors.New("injection failed")
	dev := &pinchZoomRecorder{pinchErrs: []error{boom}, frames: []color.RGBA{frameZoomedIn}}
	n := newPinchNavigator(dev)

	if err := n.ZoomOut(); !errors.Is(err, boom) {
		t.Fatalf("ZoomOut() = %v, want %v", err, boom)
	}
	if dev.attempts != 1 {
		t.Errorf("made %d attempts, want 1: stop at the first failure", dev.attempts)
	}
}

// The live BlueStacks behaviour: Android accepts the gesture, the game ignores
// it, the frame never moves. ZoomOut used to return nil here, which is how a run
// proceeded on a view nobody had zoomed.
func TestZoomOutReportsInertPinches(t *testing.T) {
	dev := &pinchZoomRecorder{frames: []color.RGBA{frameZoomedIn, frameZoomedIn, frameZoomedIn, frameZoomedIn, frameZoomedIn}}
	n := newPinchNavigator(dev)

	err := n.ZoomOut()
	if !errors.Is(err, ErrZoomInert) {
		t.Fatalf("ZoomOut() = %v, want ErrZoomInert: an accepted-but-ignored pinch is not a zoom", err)
	}
	if dev.attempts != pinchVariantCount {
		t.Errorf("tried %d gesture shapes, want %d: every shape must be ruled out before calling the device inert", dev.attempts, pinchVariantCount)
	}
	if want := []int{0, 1, 2, 3}; !slices.Equal(dev.variants, want) {
		t.Errorf("variants = %v, want %v", dev.variants, want)
	}
}

// The first shape the game ignores must not end the search: a wider, slower
// pinch is what a zoom recogniser with a minimum-distance rule accepts.
func TestZoomOutUsesTheFirstVariantThatMovesTheView(t *testing.T) {
	dev := &pinchZoomRecorder{frames: []color.RGBA{frameZoomedIn, frameZoomedIn, frameZoomedOut}}
	n := newPinchNavigator(dev)

	if err := n.ZoomOut(); err != nil {
		t.Fatalf("ZoomOut() = %v, want nil: shape 0 was ignored and shape 1 moved the view", err)
	}
	if want := []int{0, 1}; !slices.Equal(dev.variants, want) {
		t.Errorf("variants = %v, want %v", dev.variants, want)
	}
}

// ---- ZoomIn keeps the repeat semantics: it is not the verified path ----

func TestZoomInRepeatsAndReportsSuccess(t *testing.T) {
	dev := &pinchZoomRecorder{}
	n := newPinchNavigator(dev)

	if err := n.ZoomIn(); err != nil {
		t.Fatalf("ZoomIn() = %v, want nil", err)
	}
	if dev.attempts != zoomPinchRepeats {
		t.Errorf("made %d attempts, want %d", dev.attempts, zoomPinchRepeats)
	}
}

// A failure on the LAST repeat must still be reported: the old loop returned
// void, so a run could report a clean zoom having delivered only some of them.
func TestZoomInReportsFailureOnTheLastRepeat(t *testing.T) {
	boom := errors.New("third pinch failed")
	errs := make([]error, zoomPinchRepeats)
	errs[zoomPinchRepeats-1] = boom
	dev := &pinchZoomRecorder{pinchErrs: errs}
	n := newPinchNavigator(dev)

	if err := n.ZoomIn(); !errors.Is(err, boom) {
		t.Fatalf("ZoomIn() = %v, want %v", err, boom)
	}
}

// An already-cancelled context must not burn the repeat sleeps: the caller
// already knows the run is over, so a fixed ~1s of sleeping is pure latency on
// the teardown path.
func TestZoomInDoesNotSleepAfterCancellation(t *testing.T) {
	dev := &pinchZoomRecorder{pinchErrs: []error{context.Canceled}}
	n := newPinchNavigator(dev)

	start := time.Now()
	if err := n.ZoomIn(); !errors.Is(err, context.Canceled) {
		t.Fatalf("ZoomIn() = %v, want context.Canceled", err)
	}
	if elapsed := time.Since(start); elapsed > 200*time.Millisecond {
		t.Errorf("a cancelled zoom took %v: the loop slept between repeats without checking the context", elapsed)
	}
}

// ---- ZoomOutMax: the closed, verified loop ----

// The live behaviour ZoomOutMax exists for: one pinch saturates the game's
// zoom-out, and the loop needs MORE pinches to prove the view has stopped — a
// no-op pinch is only knowable after firing it, and one silent gesture is not
// proof (the camera is still easing). Saturation costs the move plus
// zoomSaturationConfirmations silent gestures.
func TestZoomOutMaxStopsWhenTheViewStopsChanging(t *testing.T) {
	dev := &pinchZoomRecorder{
		frames: []color.RGBA{
			frameZoomedIn,  // pre-pinch 1
			frameZoomedOut, // post-pinch 1: moved
			frameSettled,   // post-pinch 2: stable
			frameSettled,   // post-pinch 3: stable again -> done
		},
	}
	n := newPinchNavigator(dev)

	if err := n.ZoomOutMax(); err != nil {
		t.Fatalf("ZoomOutMax() = %v, want nil", err)
	}
	if want := 1 + zoomSaturationConfirmations; dev.attempts != want {
		t.Errorf("fired %d pinches, want %d: one that moved the view, %d that confirmed saturation", dev.attempts, want, zoomSaturationConfirmations)
	}
	// The shape that moved the view is sticky: every confirmation reuses it
	// instead of re-walking the ladder.
	if want := []int{0, 0, 0}; !slices.Equal(dev.variants, want) {
		t.Errorf("variants = %v, want %v (sticky shape)", dev.variants, want)
	}
}

// A view that keeps responding keeps getting pinched, until it settles.
func TestZoomOutMaxKeepsPinchingWhileTheViewMoves(t *testing.T) {
	dev := &pinchZoomRecorder{
		frames: []color.RGBA{
			frameZoomedIn,   // pre-pinch 1
			frameMovingMid1, // post-pinch 1: moved
			frameZoomedOut,  // post-pinch 2: moved
			frameSettled,    // post-pinch 3: stable -> done
		},
	}
	n := newPinchNavigator(dev)

	if err := n.ZoomOutMax(); err != nil {
		t.Fatalf("ZoomOutMax() = %v, want nil", err)
	}
	if want := 2 + zoomSaturationConfirmations; dev.attempts != want {
		t.Errorf("fired %d pinches, want %d (two that moved, %d that confirmed stability)", dev.attempts, want, zoomSaturationConfirmations)
	}
}

// A popup or HUD animation is not a zoom: the change has to cover the frame.
// The old test was "more than 5% of the frame changed", which an overlay passes —
// and the gesture after it then looked like saturation, i.e. "max zoom-out
// confirmed" on a camera nobody had moved.
func TestZoomChangeCellsRejectsALocalizedChange(t *testing.T) {
	base := uniformMat(frameZoomedIn)
	defer base.Close()

	popup := uniformMat(frameZoomedIn)
	defer popup.Close()
	block := popup.Region(image.Rect(5, 5, 11, 11))
	block.SetTo(gocv.NewScalar(255, 255, 255, 0))
	block.Close()

	// Premise: the popup is over the change threshold, so only the broadness
	// gate can reject it.
	if pct := vision.ChangedPercent(base, popup, vision.DefaultChangeDelta); pct < zoomOutMaxThreshold {
		t.Fatalf("test premise broken: the overlay changes %.1f%% of the frame, need >= %.1f%%", pct, zoomOutMaxThreshold)
	}
	if cells, total := zoomChangeCells(base, popup, vision.DefaultChangeDelta); total == 0 || cells >= zoomBroadCells {
		t.Fatalf("zoomChangeCells(popup) = %d/%d cells, want fewer than %d: an overlay must not read as a camera move", cells, total, zoomBroadCells)
	}

	zoom := uniformMat(frameZoomedOut)
	defer zoom.Close()
	if cells, total := zoomChangeCells(base, zoom, vision.DefaultChangeDelta); total == 0 || cells < zoomBroadCells {
		t.Fatalf("zoomChangeCells(zoom) = %d/%d cells, want >= %d: a camera move changes the whole frame", cells, total, zoomBroadCells)
	}
}

// The overlay case end to end: variant 0's frame changes ~10% (popup) and must
// NOT be accepted as the move; the ladder has to keep walking and the shape that
// really moved the camera is the one that gets confirmed twice.
func TestZoomOutMaxDoesNotTreatAnOverlayAsAZoom(t *testing.T) {
	dev := &pinchZoomRecorder{
		frames:  []color.RGBA{frameZoomedIn, frameZoomedIn, frameZoomedOut, frameSettled, frameSettled},
		patches: []bool{false, true, false, false, false},
	}
	n := newPinchNavigator(dev)

	if err := n.ZoomOutMax(); err != nil {
		t.Fatalf("ZoomOutMax() = %v, want nil", err)
	}
	if want := []int{0, 1, 1, 1}; !slices.Equal(dev.variants, want) {
		t.Fatalf("variants = %v, want %v: the overlay frame must not count as a zoom", dev.variants, want)
	}
}

// The shape that worked is remembered across calls, so the next battle does not
// re-pay the gestures this device never answers.
func TestZoomOutMaxRemembersTheShapeThatMovedTheView(t *testing.T) {
	dev := &pinchZoomRecorder{
		frames: []color.RGBA{
			// call 1: shape 0 is silent, shape 1 moves the camera
			frameZoomedIn, frameZoomedIn, frameZoomedOut, frameSettled, frameSettled, frameSettled,
			// call 2: starts already learned, so shape 1 moves it immediately
			frameZoomedIn, frameZoomedOut, frameSettled, frameSettled, frameSettled, frameSettled,
		},
	}
	n := newPinchNavigator(dev)

	for i := 1; i <= 2; i++ {
		if err := n.ZoomOutMax(); err != nil {
			t.Fatalf("call %d: ZoomOutMax() = %v, want nil", i, err)
		}
	}
	if want := []int{0, 1, 1, 1, 1, 1, 1}; !slices.Equal(dev.variants, want) {
		t.Errorf("variants = %v, want %v: the second call must open with the learned shape, not re-walk the ladder", dev.variants, want)
	}
}

// A learned shape that goes silent is NOT proof of max zoom-out: gesture
// acceptance is screen-dependent on this emulator, so the ladder has to be
// walked before a camera that never moved this call can be called maxed out.
// Live basis: shape 2 moved the village view 96%, while in a battle shape 0 moved
// it 60% and shapes 1-3 were silent.
func TestZoomOutMaxRefreshesAStaleLearnedShape(t *testing.T) {
	dev := &pinchZoomRecorder{
		frames: []color.RGBA{
			// call 1: shape 0 is silent, shape 1 moves the camera
			frameZoomedIn, frameZoomedIn, frameZoomedOut, frameSettled, frameSettled, frameSettled,
			// call 2: learned shape 1 is silent here; the ladder finds shape 2
			frameZoomedIn, frameZoomedIn, frameZoomedIn, frameZoomedIn, frameZoomedIn,
			frameZoomedOut, frameSettled, frameSettled, frameSettled,
		},
	}
	n := newPinchNavigator(dev)

	for i := 1; i <= 2; i++ {
		if err := n.ZoomOutMax(); err != nil {
			t.Fatalf("call %d: ZoomOutMax() = %v, want nil", i, err)
		}
	}
	want := []int{0, 1, 1, 1, 1, 0, 1, 2, 2, 2}
	if !slices.Equal(dev.variants, want) {
		t.Fatalf("variants = %v, want %v: a silent learned shape must trigger the ladder, not a max-zoom claim", dev.variants, want)
	}
}

// The dangerous case the old code reported as success: no shape moves the view in
// EITHER direction. The zoom-in probe is what separates this from an already-maxed
// camera, so the ladder is walked twice before the device is called inert.
func TestZoomOutMaxReportsAnInertDevice(t *testing.T) {
	dev := &pinchZoomRecorder{
		frames: []color.RGBA{frameZoomedIn, frameZoomedIn, frameZoomedIn, frameZoomedIn, frameZoomedIn},
	}
	n := newPinchNavigator(dev)

	if err := n.ZoomOutMax(); !errors.Is(err, ErrZoomInert) {
		t.Fatalf("ZoomOutMax() = %v, want ErrZoomInert: the caller must not deploy believing the view is zoomed out", err)
	}
	if want := 2 * pinchVariantCount; dev.attempts != want {
		t.Errorf("tried %d shapes, want %d: the zoom-out ladder and then the zoom-in probe", dev.attempts, want)
	}
}

// The live 2026-11-11 case: the camera is ALREADY at the game's max zoom-out, so
// no zoom-out shape moves it. A verified zoom-in proves the camera still responds,
// and the zoom-out ladder then has to reach max again — reporting ErrZoomInert here
// made the battle guard refuse to deploy on a camera that was exactly right.
func TestZoomOutMaxProvesAnAlreadyMaxedCameraWithAZoomIn(t *testing.T) {
	dev := &pinchZoomRecorder{
		frames: []color.RGBA{
			frameZoomedIn,   // pre
			frameZoomedIn,   // zoom-out ladder: every shape silent
			frameZoomedIn,   //
			frameZoomedIn,   //
			frameZoomedIn,   //
			frameMovingMid1, // zoom-in probe: the camera does respond
			frameZoomedOut,  // zoom-out ladder: moves again
			frameSettled,    // confirmation 1
			frameSettled,    // re-measure
			frameSettled,    // confirmation 2
		},
	}
	n := newPinchNavigator(dev)

	if err := n.ZoomOutMax(); err != nil {
		t.Fatalf("ZoomOutMax() = %v, want nil: the camera was maxed and the zoom-in probe proved it responds", err)
	}
	if want := []int{0, 1, 2, 3, 0, 0, 0, 0}; !slices.Equal(dev.variants, want) {
		t.Errorf("variants = %v, want %v (silent zoom-out ladder, zoom-in probe, then zoom-out again)", dev.variants, want)
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
	// Every comparison must read as CHANGED so the loop never converges.
	frames := make([]color.RGBA, 0, zoomOutMaxAttempts+1)
	for i := 0; i <= zoomOutMaxAttempts; i++ {
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

// pinchOrder is the sticky-shape rule the fast path depends on: a known shape is
// tried alone, and an unknown one starts the full ladder.
func TestPinchOrderPrefersTheStickyVariant(t *testing.T) {
	if got := pinchOrder(2); !slices.Equal(got, []int{2}) {
		t.Errorf("pinchOrder(2) = %v, want the sticky shape alone", got)
	}
	if got := pinchOrder(-1); !slices.Equal(got, []int{0, 1, 2, 3}) {
		t.Errorf("pinchOrder(-1) = %v, want the full ladder", got)
	}
}
