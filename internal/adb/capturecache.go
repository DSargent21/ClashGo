package adb

import (
	"sync"
	"time"

	"gocv.io/x/gocv"
)

// captureCache coalesces screen captures that land close together.
//
// A capture is not a memory read: it is `screencap` running inside the Android
// VM, reading the whole 2.5 MB framebuffer out of the guest heap and pushing it
// across adb. That is the single most expensive *repeated* thing the bot asks
// BlueStacks to do, and it is asked for the same picture more than once: during
// a battle the capture loop ticks every 250 ms while the deploy verifier and the
// battle-end watcher each capture on their own schedule, so one screen state can
// be screencapped two to four times inside the same frame interval. Every caller
// is looking at the same screen and reaching the same conclusion from it.
//
// The safety rule is the input barrier. The bot's control flow is check-act-check
// — capture, decide, tap, capture again to verify the tap landed — so a frame
// taken *before* a tap must never satisfy the capture that follows it. Every
// input method advances a generation counter (`noteInput`), and a cached frame is
// reused only while that counter is unchanged: only between the bot's own input
// events, never across one. A frame captured before a tap therefore cannot be
// handed to the verification after it, no matter how the two goroutines interleave.
//
// Freshness is bounded a second time by the TTL, which is what keeps a frame from
// outliving its usefulness if nothing the bot did caused the screen to change.
//
// Both bounds fail safe: a disabled, expired, empty or input-invalidated cache
// means the caller pays for exactly the capture it would have paid for anyway.
// The cache can only ever remove captures, never change what a frame contains.
type captureCache struct {
	mu  sync.Mutex
	ttl time.Duration
	now func() time.Time

	// mat is the cached frame. It is owned by the cache (never handed out
	// directly) and replaced — then Closed — on every store.
	mat  gocv.Mat
	have bool
	at   time.Time

	// gen is bumped by every input event; storedGen is the value gen had when
	// the cached frame was captured. They differ means the guest's screen may
	// have changed because of something the bot did.
	gen       uint64
	storedGen uint64

	hits        uint64
	misses      uint64
	invalidated uint64
	expired     uint64
}

// newCaptureCache builds a cache with the given TTL. A non-positive TTL disables
// it: every lookup misses and nothing is retained.
func newCaptureCache(ttl time.Duration) *captureCache {
	return &captureCache{ttl: ttl, now: time.Now}
}

// enabled reports whether the cache can serve frames at all.
func (c *captureCache) enabled() bool {
	return c != nil && c.ttl > 0
}

// setTTL replaces the reuse window. A non-positive value disables the cache and
// drops the retained frame.
func (c *captureCache) setTTL(ttl time.Duration) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ttl = ttl
	if ttl <= 0 {
		c.dropLocked()
	}
}

// lookup returns a frame for the caller if one is still valid.
//
// On a hit the returned Mat is a clone the caller owns and must Close. On a miss
// it is an allocated-but-empty Mat, not the zero Mat: a zero Mat's native handle
// is nil, so the first Cols()/Rows()/Empty() on it is a hard cgo segfault (the
// same trap CaptureToMat documents). A miss therefore returns something safe to
// inspect, and the caller closes it either way.
func (c *captureCache) lookup() (gocv.Mat, bool) {
	if !c.enabled() {
		return gocv.NewMat(), false
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.have {
		c.misses++
		return gocv.NewMat(), false
	}
	if c.storedGen != c.gen {
		// The bot has injected input since this frame was taken, so the screen
		// may already look different: this frame must not answer for it.
		c.invalidated++
		c.misses++
		return gocv.NewMat(), false
	}
	if c.now().Sub(c.at) > c.ttl {
		c.expired++
		c.misses++
		return gocv.NewMat(), false
	}

	c.hits++
	return c.mat.Clone(), true
}

// store retains a copy of a freshly captured frame. The caller keeps ownership of
// the Mat it passed in.
func (c *captureCache) store(m gocv.Mat) {
	if !c.enabled() || m.Cols() < 1 || m.Rows() < 1 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.dropLocked()
	c.mat = m.Clone()
	c.have = !c.mat.Empty() && c.mat.Cols() > 0
	c.at = c.now()
	c.storedGen = c.gen
}

// noteInput records that the bot has injected input, which may have changed the
// screen. Called from every input method.
func (c *captureCache) noteInput() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.gen++
}

// stats reports the cache's counters for observability.
func (c *captureCache) stats() CaptureCacheStats {
	if c == nil {
		return CaptureCacheStats{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return CaptureCacheStats{
		TTLms:       int(c.ttl / time.Millisecond),
		Enabled:     c.ttl > 0,
		Hits:        c.hits,
		Misses:      c.misses,
		Invalidated: c.invalidated,
		Expired:     c.expired,
	}
}

// dropLocked releases the retained frame. Caller must hold c.mu.
func (c *captureCache) dropLocked() {
	if c.have && !c.mat.Empty() {
		c.mat.Close()
	}
	c.mat = gocv.Mat{}
	c.have = false
}

// close releases the retained frame.
func (c *captureCache) close() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.dropLocked()
}

// CaptureCacheStats reports how often a capture request was answered from the
// coalescing cache instead of the device. Hits are the captures the bot did not
// ask the emulator for; Invalidated counts the misses the input barrier caused
// (a frame discarded because the bot had tapped since), Expired the ones the TTL
// caused. A run where Invalidated dominates means the barrier is doing the work
// and the TTL is not; Hits near zero with Enabled true means nothing captures
// concurrently on this workload.
type CaptureCacheStats struct {
	TTLms       int    `json:"ttl_ms"`
	Enabled     bool   `json:"enabled"`
	Hits        uint64 `json:"hits"`
	Misses      uint64 `json:"misses"`
	Invalidated uint64 `json:"invalidated"`
	Expired     uint64 `json:"expired"`
}

// WithCaptureCache coalesces screen captures taken within ttl of each other into
// one device capture, except across the bot's own input events (see
// captureCache). ttl <= 0 leaves capture coalescing off, which is the default:
// the diagnostic tools want the freshest frame available and their callers are
// one-shot, while the bot — the only caller with overlapping consumers — opts in.
func WithCaptureCache(ttl time.Duration) Option {
	return func(c *Client) {
		if c.frames == nil {
			c.frames = newCaptureCache(ttl)
			return
		}
		c.frames.setTTL(ttl)
	}
}

// CaptureCache reports the coalescing counters (a disabled cache reports zeros).
func (c *Client) CaptureCache() CaptureCacheStats {
	return c.frames.stats()
}

// markInput tells the capture cache that the bot has injected input. Every
// method that taps, swipes, keys or types calls it, because the whole point of
// the barrier is that no input path can forget it: a capture that follows any of
// them must go to the device.
func (c *Client) markInput() {
	c.frames.noteInput()
}
