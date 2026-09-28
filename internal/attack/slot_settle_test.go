package attack

import (
	"testing"
	"time"
)

// fakeClock advances only when the policy sleeps, so these tests exercise the
// poll/stop policy without real delays.
type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time { return c.t }
func (c *fakeClock) sleep()         { c.t = c.t.Add(settledPollInterval) }

// TestSettleRatioStopsAtFirstStablePair: the first read is immediate, and the
// policy returns as soon as two consecutive reads agree — no wasted polls when
// the card was already settled (a failed drop that never animated).
func TestSettleRatioStopsAtFirstStablePair(t *testing.T) {
	clock := &fakeClock{t: time.Unix(0, 0)}
	reads := []float64{0.31, 0.31}
	i := 0
	sample := func() (float64, bool) {
		v := reads[i]
		i++
		return v, true
	}

	got, ok := settleRatio(sample, clock.sleep, clock.now, settledBudget)
	if !ok {
		t.Fatal("expected a captured ratio")
	}
	if got != 0.31 {
		t.Errorf("ratio = %v, want 0.31", got)
	}
	if i != 2 {
		t.Errorf("took %d reads, want 2 (two agreeing reads is enough)", i)
	}
}

// TestSettleRatioWaitsOutTheAnimation: the first read catches the card
// mid-animation (the live 0.753 highlight), later reads settle on the cooldown
// silhouette, and the settled value is what the delta check must see.
func TestSettleRatioWaitsOutTheAnimation(t *testing.T) {
	clock := &fakeClock{t: time.Unix(0, 0)}
	reads := []float64{0.753, 0.62, 0.20, 0.09, 0.08}
	i := 0
	sample := func() (float64, bool) {
		v := reads[i]
		i++
		return v, true
	}

	got, ok := settleRatio(sample, clock.sleep, clock.now, settledBudget)
	if !ok {
		t.Fatal("expected a captured ratio")
	}
	if got != 0.08 {
		t.Errorf("ratio = %v, want the settled 0.08 (not the mid-animation 0.753)", got)
	}
}

// TestSettleRatioOutOfBudgetReturnsLastRead: a card that never stops moving must
// not hang the deploy. The budget caps the polling and the freshest reading is
// returned, because a missing sample is worse for the caller than an early one.
func TestSettleRatioOutOfBudgetReturnsLastRead(t *testing.T) {
	clock := &fakeClock{t: time.Unix(0, 0)}
	values := []float64{0.1, 0.2, 0.3, 0.4, 0.5, 0.6, 0.7, 0.8, 0.9, 1.0}
	i := 0
	sample := func() (float64, bool) {
		v := values[i%len(values)]
		i++
		return v, true
	}

	got, ok := settleRatio(sample, clock.sleep, clock.now, 3*settledPollInterval)
	if !ok {
		t.Fatal("expected a captured ratio even after the budget expires")
	}
	want := values[i-1]
	if got != want {
		t.Errorf("ratio = %v, want the last read %v", got, want)
	}
	if elapsed := clock.now().Sub(time.Unix(0, 0)); elapsed > settledBudget {
		t.Errorf("polled for %v, past the %v budget", elapsed, settledBudget)
	}
}

// TestSettleRatioFailsOnCaptureError: a device that will not give a frame must
// surface as not-captured on the first read, not spin until the budget drains.
func TestSettleRatioFailsOnCaptureError(t *testing.T) {
	clock := &fakeClock{t: time.Unix(0, 0)}
	calls := 0
	sample := func() (float64, bool) {
		calls++
		return 0, false
	}

	if _, ok := settleRatio(sample, clock.sleep, clock.now, settledBudget); ok {
		t.Error("expected captured=false when the capture fails")
	}
	if calls != 1 {
		t.Errorf("sampled %d times, want 1 (a failed capture must not be retried in a loop)", calls)
	}
}
