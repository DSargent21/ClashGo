package adb

import (
	"testing"
	"time"

	"gocv.io/x/gocv"
)

// testFrame builds a small BGR frame filled with one value, so a test can tell
// two frames apart by reading a pixel.
func testFrame(t *testing.T, fill byte) gocv.Mat {
	t.Helper()
	m := gocv.NewMatWithSize(4, 6, gocv.MatTypeCV8UC3)
	m.SetUCharAt(0, 0, fill)
	m.SetUCharAt(0, 1, fill)
	m.SetUCharAt(0, 2, fill)
	return m
}

func cacheFill(m gocv.Mat) byte { return m.GetUCharAt(0, 0) }

func TestCaptureCacheServesRecentFrame(t *testing.T) {
	c := newCaptureCache(200 * time.Millisecond)
	now := time.Unix(0, 0)
	c.now = func() time.Time { return now }
	defer c.close()

	fr := testFrame(t, 7)
	defer fr.Close()
	c.store(fr)

	got, ok := c.lookup()
	if !ok {
		t.Fatal("a frame stored moments ago must be served")
	}
	defer got.Close()
	if cacheFill(got) != 7 {
		t.Errorf("cached frame has %d, want 7", cacheFill(got))
	}
	if got.Cols() != fr.Cols() || got.Rows() != fr.Rows() {
		t.Errorf("cached frame is %dx%d, want %dx%d", got.Cols(), got.Rows(), fr.Cols(), fr.Rows())
	}
}

// The cache must hand out a frame whose lifetime is independent of the one it
// retains: callers Close what they are given, and that must not blank the copy
// the next caller gets.
func TestCaptureCacheHandsOutIndependentCopies(t *testing.T) {
	c := newCaptureCache(time.Second)
	defer c.close()

	fr := testFrame(t, 11)
	defer fr.Close()
	c.store(fr)

	first, _ := c.lookup()
	first.Close()

	second, ok := c.lookup()
	if !ok {
		t.Fatal("closing a served frame must not invalidate the cache")
	}
	defer second.Close()
	if cacheFill(second) != 11 {
		t.Errorf("second copy has %d, want 11", cacheFill(second))
	}

	// And mutating the caller's original must not change what the cache holds.
	fr.SetUCharAt(0, 0, 99)
	third, ok := c.lookup()
	if !ok {
		t.Fatal("expected a cached frame")
	}
	defer third.Close()
	if cacheFill(third) != 11 {
		t.Errorf("cache tracked the caller's buffer: got %d, want 11", cacheFill(third))
	}
}

func TestCaptureCacheExpires(t *testing.T) {
	c := newCaptureCache(200 * time.Millisecond)
	now := time.Unix(0, 0)
	c.now = func() time.Time { return now }
	defer c.close()

	fr := testFrame(t, 3)
	defer fr.Close()
	c.store(fr)

	now = now.Add(199 * time.Millisecond)
	if _, ok := c.lookup(); !ok {
		t.Error("a frame inside the window must be served")
	}

	now = now.Add(2 * time.Millisecond) // 201 ms after the store
	if _, ok := c.lookup(); ok {
		t.Error("a frame past the window must not be served")
	}
	st := c.stats()
	if st.Expired == 0 {
		t.Error("an expired lookup must be counted as expired")
	}
}

// TestCaptureCacheInputBarrier is the property the whole optimisation rests on:
// a frame captured before the bot touched the device must never answer a capture
// request made after it. The bot's control flow is check-act-check, so serving a
// pre-tap frame to the check that follows the tap would verify a tap that had not
// happened yet.
func TestCaptureCacheInputBarrier(t *testing.T) {
	c := newCaptureCache(time.Minute)
	defer c.close()

	fr := testFrame(t, 1)
	defer fr.Close()
	c.store(fr)

	if _, ok := c.lookup(); !ok {
		t.Fatal("baseline: the frame should be served before any input")
	}

	c.noteInput()

	if got, ok := c.lookup(); ok {
		got.Close()
		t.Fatal("a frame captured before an input event answered a post-input capture request")
	}
	if st := c.stats(); st.Invalidated != 1 {
		t.Errorf("invalidated = %d, want 1", st.Invalidated)
	}

	// A frame captured after the input is served again: the barrier is about
	// ordering, not about distrusting the cache forever.
	now := testFrame(t, 2)
	defer now.Close()
	c.store(now)
	got, ok := c.lookup()
	if !ok {
		t.Fatal("a frame captured after the input must be served")
	}
	defer got.Close()
	if cacheFill(got) != 2 {
		t.Errorf("served the stale frame: got %d, want 2", cacheFill(got))
	}
}

// A frame from before an input stays invalid no matter how much time passes,
// and no matter how many inputs arrive.
func TestCaptureCacheStaysInvalidAcrossInputs(t *testing.T) {
	c := newCaptureCache(time.Hour)
	defer c.close()

	fr := testFrame(t, 5)
	defer fr.Close()
	c.store(fr)

	for i := 0; i < 3; i++ {
		c.noteInput()
		if _, ok := c.lookup(); ok {
			t.Fatalf("lookup %d served a frame invalidated by input", i)
		}
	}
}

func TestCaptureCacheDisabled(t *testing.T) {
	for _, ttl := range []time.Duration{0, -time.Second} {
		c := newCaptureCache(ttl)
		defer c.close()
		fr := testFrame(t, 4)
		c.store(fr)
		if _, ok := c.lookup(); ok {
			t.Errorf("ttl %v: a disabled cache must never serve a frame", ttl)
		}
		fr.Close()
		if st := c.stats(); st.Enabled || st.Hits != 0 {
			t.Errorf("ttl %v: stats report %+v, want disabled with no hits", ttl, st)
		}
	}
}

func TestCaptureCacheSetTTLDisablesAndDrops(t *testing.T) {
	c := newCaptureCache(time.Minute)
	defer c.close()

	fr := testFrame(t, 6)
	defer fr.Close()
	c.store(fr)
	if _, ok := c.lookup(); !ok {
		t.Fatal("expected a hit before disabling")
	}

	c.setTTL(0)
	if _, ok := c.lookup(); ok {
		t.Error("setTTL(0) must stop serving frames")
	}
	if c.ttl != 0 {
		t.Errorf("ttl = %v, want 0", c.ttl)
	}
	// Storing while disabled must not retain anything (and must not panic).
	c.store(fr)
	if _, ok := c.lookup(); ok {
		t.Error("a disabled cache retained a frame")
	}
}

func TestCaptureCacheEmptyAndDegenerateFramesAreNotStored(t *testing.T) {
	c := newCaptureCache(time.Minute)
	defer c.close()

	c.store(gocv.NewMat())                                 // empty
	c.store(gocv.NewMatWithSize(0, 0, gocv.MatTypeCV8UC3)) // degenerate
	if _, ok := c.lookup(); ok {
		t.Error("an empty capture must never be cached")
	}
}

func TestCaptureCacheStats(t *testing.T) {
	c := newCaptureCache(100 * time.Millisecond)
	defer c.close()

	fr := testFrame(t, 8)
	defer fr.Close()

	if _, ok := c.lookup(); ok {
		t.Error("expected a miss with nothing stored")
	}
	c.store(fr)
	if _, ok := c.lookup(); !ok {
		t.Error("expected a hit")
	}
	c.noteInput()
	if _, ok := c.lookup(); ok {
		t.Error("expected a miss after input")
	}

	st := c.stats()
	if !st.Enabled || st.TTLms != 100 {
		t.Errorf("stats = %+v, want enabled with 100 ms", st)
	}
	if st.Hits != 1 || st.Misses != 2 || st.Invalidated != 1 {
		t.Errorf("stats = %+v, want hits 1, misses 2, invalidated 1", st)
	}
}

// TestCaptureCacheBattleInterleaving models the sequence the cache exists for:
// during a battle the capture loop and the deploy machinery read the same screen
// on their own schedules, and the only thing that must force a real capture is a
// tap. It is written as the actual order of events rather than as a unit call, so
// that a future change to the barrier has to argue with the workload, not just
// with an assertion.
func TestCaptureCacheBattleInterleaving(t *testing.T) {
	c := newCaptureCache(120 * time.Millisecond)
	now := time.Unix(0, 0)
	c.now = func() time.Time { return now }
	defer c.close()

	// 1. The capture loop classifies the battle screen.
	loop, _ := c.lookup() // miss: nothing stored yet
	if !loop.Empty() {
		loop.Close()
		t.Fatal("expected a miss on the first capture")
	}
	frame := testFrame(t, 20)
	defer frame.Close()
	c.store(frame)

	// 2. The deploy code wants the same screen 80 ms later, having touched
	// nothing: it must be answered from the loop's frame.
	now = now.Add(80 * time.Millisecond)
	deployer, ok := c.lookup()
	if !ok {
		t.Fatal("a second consumer inside the window must share the loop's capture")
	}
	deployer.Close()

	// 3. A deploy tap, then the verifier's capture 30 ms later. The tap must
	// force a real capture: the verifier is asking whether the tap landed.
	now = now.Add(30 * time.Millisecond)
	c.noteInput()
	if stale, ok := c.lookup(); ok {
		stale.Close()
		t.Fatal("a post-tap capture was answered with a pre-tap frame")
	}
	verified := testFrame(t, 30)
	defer verified.Close()
	c.store(verified)

	// 4. The loop's next tick, 100 ms after the verifier's real capture and with
	// no further input: back to sharing.
	now = now.Add(100 * time.Millisecond)
	got, ok := c.lookup()
	if !ok {
		t.Fatal("the loop should share the verifier's capture")
	}
	defer got.Close()
	if cacheFill(got) != 30 {
		t.Errorf("served frame %d, want the verifier's post-tap frame 30", cacheFill(got))
	}

	// Three requests became one device capture per tap-free stretch.
	st := c.stats()
	if st.Hits != 2 || st.Invalidated != 1 {
		t.Errorf("stats = %+v, want 2 hits and 1 input-invalidated miss", st)
	}
}

// A client with no cache configured must report zeros rather than panic: every
// CLI tool constructs one.
func TestCaptureCacheStatsOnNilCache(t *testing.T) {
	var c *captureCache
	if st := c.stats(); st.Enabled || st.Hits != 0 {
		t.Errorf("nil cache stats = %+v, want zeros", st)
	}
	c.noteInput() // must not panic
	if _, ok := c.lookup(); ok {
		t.Error("a nil cache served a frame")
	}
	c.close() // must not panic
}

// The option is the client-facing contract: a default client has no cache, and
// opting in makes CaptureToMat consult it before touching the device.
func TestWithCaptureCacheOption(t *testing.T) {
	def := NewClient()
	if def.frames.enabled() {
		t.Error("a default client must not coalesce captures: the diagnostic tools want the freshest frame")
	}

	on := NewClient(WithCaptureCache(150 * time.Millisecond))
	st := on.CaptureCache()
	if !st.Enabled || st.TTLms != 150 {
		t.Errorf("stats = %+v, want enabled with 150 ms", st)
	}

	// Re-configuring the same client replaces the window instead of stacking.
	WithCaptureCache(0)(on)
	if st := on.CaptureCache(); st.Enabled {
		t.Errorf("stats = %+v, want disabled after WithCaptureCache(0)", st)
	}
}

// TestCaptureToMatServesFromCacheWithoutADevice is the end-to-end property at
// the seam that matters: when a valid frame is cached, CaptureToMat must return
// it *without* going to the device. The client here has no transport at all, so
// any attempt to capture for real fails loudly instead of silently passing.
func TestCaptureToMatServesFromCacheWithoutADevice(t *testing.T) {
	c := NewClient(WithCaptureCache(time.Minute))

	fr := testFrame(t, 42)
	defer fr.Close()
	c.frames.store(fr)

	got, err := c.CaptureToMat()
	if err != nil {
		t.Fatalf("a cached frame must be served without a device: %v", err)
	}
	defer got.Close()
	if cacheFill(got) != 42 {
		t.Errorf("served frame has %d, want the cached 42", cacheFill(got))
	}

	// After an input event the same call must go to the (nonexistent) device and
	// therefore fail: that failure is the proof it did not use the cache.
	c.markInput()
	if _, err := c.CaptureToMat(); err == nil {
		t.Error("a capture after input must not be answered from the cache")
	}
}
