package attack

import (
	"math"
	"time"
)

// The hero "did it deploy?" check is a delta on a slot's activity ratio, so it
// is only meaningful when both samples come from a settled card. They are not
// naturally settled: a drop tap leaves the slot animating for about a second
// (the selection glow decays, then the cooldown silhouette fades in), so a
// fixed post-tap delay measures whichever frame the animation happened to be
// on.
//
// Live evidence at 720p (run 2026-09-24; tmp/taps.ndjson + app.log): the
// Barbarian King's slot read 0.753 350ms after its drop, while the sweeper's
// check a few seconds later read the same slot as empty (<0.08) — the hero had
// deployed, and only the timing of the read was wrong. Every hero failed its
// own 0.15 delta bar that way (-0.078, +0.034, +0.019, -0.061, -0.179) and
// earned a retry tap: exactly the "it keeps tapping the hero" symptom. The same
// run also showed the animation is slow enough that the pin attempt's post read
// and the retry's pre read were served from one cached frame (both exactly
// 4121/5476 active pixels).
//
// Rather than guess the animation length, poll: read the slot, then keep reading
// every settledPollInterval until two consecutive reads agree within
// settledEpsilon, or settledBudget runs out — and use that settled value.
const (
	settledPollInterval = 250 * time.Millisecond
	settledBudget       = 2000 * time.Millisecond
	settledEpsilon      = 0.02
)

// settleRatio polls sample until two consecutive readings agree within
// settledEpsilon (the card has stopped animating), the budget expires, or a read
// fails. It returns the last good reading. now/sleep are parameters so the
// policy is unit-testable without a device.
func settleRatio(sample func() (float64, bool), sleep func(), now func() time.Time, budget time.Duration) (float64, bool) {
	deadline := now().Add(budget)
	prev := -1.0
	for {
		r, ok := sample()
		if !ok {
			return 0, false
		}
		if prev >= 0 && math.Abs(r-prev) <= settledEpsilon {
			return r, true
		}
		if !now().Before(deadline) {
			// Out of budget: return the freshest reading rather than nothing.
			// A too-early read is the bug this function exists to avoid, but a
			// missing sample is strictly worse for the caller's delta check.
			return r, true
		}
		prev = r
		sleep()
	}
}

// CaptureSettledSlotRatio reads slot's activity ratio once its card has stopped
// animating, so a deploy's cooldown silhouette is measured instead of whatever
// frame its animation was on. Returns (ratio, capturedOK); capturedOK is false
// only when the screen could not be captured at all.
func (t *TapExecutor) CaptureSettledSlotRatio(screenW int, slot *TrackedSlot) (float64, bool) {
	sample := func() (float64, bool) {
		screen, err := t.CaptureFresh()
		if err != nil {
			return 0, false
		}
		defer screen.Close()
		return GetSlotActivityRatioStatic(screen, slot.X, slot.Y, screenW), true
	}
	return settleRatio(sample, func() {
		t.client.HumanSleep(int(settledPollInterval/time.Millisecond), 25)
	}, time.Now, settledBudget)
}
