package bot

import (
	"testing"
	"time"

	"github.com/Ducky705/ClashGO/internal/config"
)

// TestCaptureCacheCannotBridgeSettleAttempts guards a safety bound that spans two
// packages, which is exactly the kind of invariant that rots silently.
//
// The battle-result settle machine believes a read only when two consecutive
// captures agree — that is how it knows the loot and bonus counters have stopped
// animating. The capture cache (internal/adb/capturecache.go) answers a capture
// request from the previous frame when it is inside its window and the bot has
// not injected input since, so a window at or above the pause between two settle
// attempts would let one frame answer both observations. The panel would then
// "settle" while it was still animating, and the bot would bank the loot and
// bonus columns mid-count: a wrong result on every win.
//
// The input barrier does not cover this case, because no tap happens between two
// settle attempts — the pause is the only thing keeping the two observations
// apart. So the assertion is on the pause itself.
func TestCaptureCacheCannotBridgeSettleAttempts(t *testing.T) {
	window := time.Duration(config.DefaultCaptureCacheMs * float64(time.Millisecond))

	for name, pause := range map[string]time.Duration{
		"panel animating": resultSettleAnimatingPause,
		"capture retry":   resultSettleCapturePause,
		"parse retry":     resultSettleParsePause,
	} {
		if window >= pause {
			t.Errorf("the %s pause is %v but the default capture cache window is %v: one frame could answer two settle observations, so an animating result panel would settle on its own",
				name, pause, window)
		}
	}

	// And the guard has to be about a window that is actually on by default,
	// otherwise it is asserting something the bot never does.
	if !config.DefaultConfig().Performance.CoalesceCaptures {
		t.Fatal("capture coalescing is off by default, so this bound protects nothing")
	}
	if got := config.DefaultConfig().Performance.CaptureCacheTTL(); got != window {
		t.Errorf("the default config's window is %v, want the documented %v", got, window)
	}
}
