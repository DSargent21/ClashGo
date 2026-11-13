package autotune

import (
	"math"
	"testing"
	"time"
)

// noRamp returns options that report the raw optimum: no blending, no clamp.
func noRamp() Options {
	return Options{SkipSeconds: 3.5, AttackSeconds: 95, MinSamples: 1, BlendUntil: 0, ClampFraction: 0}
}

func rampOptions() Options {
	return Options{SkipSeconds: 3.5, AttackSeconds: 95, MinSamples: 1, BlendUntil: 200, ClampFraction: 0.35}
}

// twoPoint builds a distribution the optimum of which can be worked out by
// hand: half the bases offer little, half offer a lot.
func twoPoint(n int, low, high int) []Sample {
	samples := make([]Sample, 0, n)
	for i := 0; i < n; i++ {
		gold, elixir, de := low, low/2, low/10
		if i%2 == 0 {
			gold, elixir, de = high, high/2, high/10
		}
		samples = append(samples, Sample{Gold: gold, Elixir: elixir, DE: de})
	}
	return samples
}

func TestRecommend_PicksTheHigherTwoPointBranch(t *testing.T) {
	samples := twoPoint(1000, 100, 1000)
	current := Thresholds{Gold: 100, Elixir: 100, DE: 100}

	rec := Recommend(samples, current, noRamp())

	if !rec.Ready {
		t.Fatalf("expected a ready recommendation, reason: %s", rec.Reason)
	}
	if rec.Thresholds.Gold != 1000 {
		t.Errorf("gold threshold = %d, want 1000 (the point where the expensive half of the distribution starts)", rec.Thresholds.Gold)
	}
	// Hand calculation: at t=100 the cycle is all attacks (20800/hr); at
	// t=1000 half the bases are skipped, so the mean loot per attack doubles
	// and the cycle shortens to 49.25s -> ~36500/hr.
	wantNow := 3600 * 550.0 / 95.0
	wantNew := 3600 * 0.5 * 1000.0 / (0.5*95.0 + 0.5*3.5)
	if math.Abs(rec.Advice[0].PerHourNow-wantNow) > 1 {
		t.Errorf("per-hour at current = %.0f, want %.0f", rec.Advice[0].PerHourNow, wantNow)
	}
	if math.Abs(rec.Advice[0].PerHourNew-wantNew) > 1 {
		t.Errorf("per-hour at suggested = %.0f, want %.0f", rec.Advice[0].PerHourNew, wantNew)
	}
	if rec.Advice[0].PerHourNew <= rec.Advice[0].PerHourNow {
		t.Errorf("suggestion (%d) should beat the current threshold on throughput", rec.Thresholds.Gold)
	}
}

func TestRecommend_NotReadyBelowMinSamples(t *testing.T) {
	opts := noRamp()
	opts.MinSamples = 40
	current := Thresholds{Gold: 400000, Elixir: 400000, DE: 2000}

	rec := Recommend(twoPoint(10, 100, 1000), current, opts)

	if rec.Ready {
		t.Fatal("a 10-base sample must not produce a recommendation")
	}
	if rec.Thresholds != current {
		t.Errorf("thresholds changed below the sample gate: %+v", rec.Thresholds)
	}
	if len(rec.Advice) != 0 {
		t.Errorf("expected no advice rows, got %d", len(rec.Advice))
	}
	if rec.Reason == "" {
		t.Error("expected a reason explaining why there is no recommendation")
	}
}

func TestRecommend_CountsButDropsUnreadableBases(t *testing.T) {
	samples := twoPoint(100, 50000, 500000)
	samples = append(samples,
		Sample{Gold: 0, Elixir: 0, DE: 0}, // failed digit read
		Sample{Gold: 0, Elixir: 0, DE: 0},
	)

	rec := Recommend(samples, Thresholds{Gold: 100000, Elixir: 50000, DE: 5000}, noRamp())

	if rec.Recorded != 102 {
		t.Errorf("recorded = %d, want 102", rec.Recorded)
	}
	if rec.Samples != 100 {
		t.Errorf("samples = %d, want 100 (zero reads dropped)", rec.Samples)
	}
}

func TestRecommend_AllZeroReadsChangeNothing(t *testing.T) {
	samples := make([]Sample, 100)
	current := Thresholds{Gold: 400000, Elixir: 400000, DE: 2000}

	rec := Recommend(samples, current, noRamp())

	if rec.Ready {
		t.Fatal("all-zero reads must not be treated as a usable distribution")
	}
	if rec.Thresholds != current {
		t.Errorf("thresholds changed on unusable data: %+v", rec.Thresholds)
	}
}

func TestRecommend_BlendsAndClampsASingleJump(t *testing.T) {
	samples := twoPoint(100, 2000000, 2000000) // every base offers 2M gold, 1M elixir, 200k DE
	current := Thresholds{Gold: 400000, Elixir: 400000, DE: 200000}

	rec := Recommend(samples, current, rampOptions())

	// Optimum is 2M. With 100 of 200 ramp samples the blend asks for 1.2M,
	// then the 35% clamp holds the move to +140k -> 540k.
	if rec.Thresholds.Gold != 540000 {
		t.Errorf("gold = %d, want 540000 (blend then clamp)", rec.Thresholds.Gold)
	}
	if rec.Thresholds.DE != 200000 {
		t.Errorf("de = %d, want 200000 unchanged (already optimal)", rec.Thresholds.DE)
	}
	if rec.Advice[0].Changed != true {
		t.Error("expected the gold advice to be flagged as a change")
	}
}

// Past the ramp the blend is gone, but the clamp still caps one adjustment: a
// single call must not throw away a working threshold, however clear the data.
// Repeated calls walk the threshold up 35% at a time.
func TestRecommend_ClampCapsOneCallPastTheRamp(t *testing.T) {
	samples := twoPoint(400, 2000000, 2000000)
	current := Thresholds{Gold: 100000, Elixir: 100000, DE: 100}

	rec := Recommend(samples, current, Options{
		SkipSeconds: 3.5, AttackSeconds: 95, MinSamples: 1, BlendUntil: 200, ClampFraction: 0.35,
	})

	if rec.Thresholds.Gold != 135000 {
		t.Errorf("gold = %d, want 135000 (one 35%% step toward 2M)", rec.Thresholds.Gold)
	}

	// Feeding the answer back in converges on the optimum.
	for i := 0; i < 20; i++ {
		rec = Recommend(samples, rec.Thresholds, Options{
			SkipSeconds: 3.5, AttackSeconds: 95, MinSamples: 1, BlendUntil: 200, ClampFraction: 0.35,
		})
	}
	if rec.Thresholds.Gold != 2000000 {
		t.Errorf("gold = %d after 20 iterations, want 2000000 (the clamp must not stall the walk)", rec.Thresholds.Gold)
	}
}

func TestRecommend_ThresholdRisesWithAttackCost(t *testing.T) {
	samples := make([]Sample, 0, 2000)
	for i := 0; i < 2000; i++ {
		loot := (i%10 + 1) * 50000
		samples = append(samples, Sample{Gold: loot, Elixir: loot, DE: loot / 20})
	}
	current := Thresholds{Gold: 100000, Elixir: 100000, DE: 5000}

	cheap := Recommend(samples, current, Options{SkipSeconds: 3.5, AttackSeconds: 60, MinSamples: 1})
	pricey := Recommend(samples, current, Options{SkipSeconds: 3.5, AttackSeconds: 600, MinSamples: 1})

	if pricey.Thresholds.Gold < cheap.Thresholds.Gold {
		t.Errorf("attack cost 600s picked %d, cheaper than the 60s pick %d; a more expensive raid must be more selective",
			pricey.Thresholds.Gold, cheap.Thresholds.Gold)
	}
}

func TestRecommend_DeterministicOnTies(t *testing.T) {
	samples := make([]Sample, 500)
	for i := range samples {
		samples[i] = Sample{Gold: 500000, Elixir: 500000, DE: 5000}
	}
	current := Thresholds{Gold: 400000, Elixir: 400000, DE: 4000}

	first := Recommend(samples, current, noRamp())
	second := Recommend(samples, current, noRamp())

	if first.Thresholds != second.Thresholds {
		t.Errorf("non-deterministic: %+v vs %+v", first.Thresholds, second.Thresholds)
	}
	if first.Thresholds.Gold != 500000 {
		t.Errorf("gold = %d, want the only observed value 500000", first.Thresholds.Gold)
	}
}

func TestRecommend_ZeroOptionsStillSane(t *testing.T) {
	rec := Recommend(twoPoint(100, 1000, 10000), Thresholds{Gold: 5000, Elixir: 5000, DE: 500}, Options{})
	if rec.SkipSecs <= 0 || rec.AttackSecs <= 0 {
		t.Errorf("zero options produced zero costs: %+v", rec)
	}
	if !rec.Ready {
		t.Errorf("zero options should fall back to the default sample gate, got not ready: %s", rec.Reason)
	}
}

func TestNewSampleAndEmpty(t *testing.T) {
	at := time.Unix(1700000000, 0)
	s := NewSample(at, 100, 0, 0, true, 2500*time.Millisecond)

	if s.At != at.UnixMilli() {
		t.Errorf("At = %d, want %d", s.At, at.UnixMilli())
	}
	if s.SearchMs != 2500 {
		t.Errorf("SearchMs = %d, want 2500", s.SearchMs)
	}
	if s.Empty() {
		t.Error("a base with gold must not count as empty")
	}
	if !(Sample{}).Empty() {
		t.Error("an all-zero sample must count as empty")
	}
}
