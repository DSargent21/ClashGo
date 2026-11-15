package attack

import (
	"math"
	"time"

	"gocv.io/x/gocv"
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

// minArmedCyan is how many armed-card highlight pixels make a card "selected for
// placement", in reference px. Measured live (1024x768): an armed card reads
// 1429, its unarmed neighbours 0-4, so the bar only has to clear noise.
const minArmedCyan = 120

// SlotArmedStatic reports whether a card is SELECTED for placement. The game
// paints an armed card's background cyan and draws a green down-arrow on it.
//
// This is the only placement evidence that survives a refusal on a card the
// template set cannot name: the count badge reads "no label" either way (live
// 2026-11-15, the siege machine's card sat ARMED with its badge a full second
// after "deploying siege", while the run logged "count label gone; slot
// drained" and skipped the machine for the rest of the battle).
func SlotArmedStatic(screen gocv.Mat, x, y int, scale float64) bool {
	if screen.Empty() || x <= 0 || y <= 0 {
		return false
	}
	if scale <= 0 {
		scale = 1
	}
	half := int(28 * scale)
	vhalf := int(34 * scale)
	cyan := 0
	for yy := y - vhalf; yy <= y+vhalf; yy++ {
		if yy < 0 || yy >= screen.Rows() {
			continue
		}
		for xx := x - half; xx <= x+half; xx++ {
			if xx < 0 || xx >= screen.Cols() {
				continue
			}
			b := int(screen.GetUCharAt(yy, xx*3))
			g := int(screen.GetUCharAt(yy, xx*3+1))
			r := int(screen.GetUCharAt(yy, xx*3+2))
			if b > 170 && g > 150 && r < 110 {
				cyan++
			}
		}
	}
	return cyan >= int(float64(minArmedCyan)*scale*scale)
}

// SlotArmed reads one fresh frame and reports whether the card is armed. ok is
// false when the screen could not be captured at all.
func (t *TapExecutor) SlotArmed(slot *TrackedSlot) (armed bool, ok bool) {
	if slot == nil {
		return false, false
	}
	screen, err := t.CaptureFresh()
	if err != nil || screen.Empty() {
		return false, false
	}
	defer screen.Close()
	return SlotArmedStatic(screen, slot.X, slot.Y, t.cal.DisplayScale()), true
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
