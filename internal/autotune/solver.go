package autotune

import (
	"fmt"
	"math"
	"sort"
)

// Units are per-resource: gold and elixir move in thousands, dark elixir in
// hundreds. Rounding the suggestion to a step keeps the UI number stable from
// sample to sample instead of jittering by a few thousand every base.
const (
	largeLootStep = 1000
	darkLootStep  = 100
)

// Options tune the recommendation.
//
// SkipSeconds and AttackSeconds are the two costs the calculation trades off.
// They default to measured ballparks (a base takes a couple of seconds to
// find; a raid takes about a minute and a half including the result screen) and
// are overridable so the calibration can improve as real timings are recorded.
type Options struct {
	SkipSeconds   float64
	AttackSeconds float64
	// MinSamples gates any recommendation at all: below it the sample is too
	// small to separate signal from the particular streak the player is on.
	MinSamples int
	// BlendUntil ramps a new recommendation in. With n samples the movement
	// toward the optimum is scaled by n/BlendUntil, so early advice nudges the
	// threshold instead of throwing away a working one.
	BlendUntil int
	// ClampFraction caps a single adjustment as a fraction of the current
	// threshold. A typo-scale jump would cost a session of bad attacks.
	ClampFraction float64
}

// DefaultOptions are the values the app ships with.
//
// ponytail: the two cost constants are ballparks, not measurements — the
// sample log already records search time per base, so the next step is to feed
// its mean back in (and the raid duration from AttackReport timestamps) instead
// of hard-coding them. Ceiling today: a slow device or a long battle makes the
// recommendation slightly conservative, never wrong in direction.
func DefaultOptions() Options {
	return Options{
		SkipSeconds:   3.5,
		AttackSeconds: 95,
		MinSamples:    40,
		BlendUntil:    200,
		ClampFraction: 0.35,
	}
}

// normalized replaces unusable option values with the defaults, so a zero
// Options{} still produces a sane recommendation.
func (o Options) normalized() Options {
	d := DefaultOptions()
	if o.SkipSeconds <= 0 {
		o.SkipSeconds = d.SkipSeconds
	}
	if o.AttackSeconds <= 0 {
		o.AttackSeconds = d.AttackSeconds
	}
	if o.MinSamples <= 0 {
		o.MinSamples = d.MinSamples
	}
	if o.BlendUntil < 0 {
		o.BlendUntil = 0
	}
	if o.ClampFraction < 0 {
		o.ClampFraction = 0
	}
	return o
}

// Advice is the recommendation for one resource.
type Advice struct {
	Key        string  `json:"key"`   // gold | elixir | de
	Label      string  `json:"label"` // human label for the UI
	Current    int     `json:"current"`
	Suggested  int     `json:"suggested"`
	PerHourNow float64 `json:"per_hour_now"`
	PerHourNew float64 `json:"per_hour_new"`
	Changed    bool    `json:"changed"`
}

// Recommendation is the whole answer: what to search for, and why.
type Recommendation struct {
	Samples    int        `json:"samples"`  // samples considered (zeros dropped)
	Recorded   int        `json:"recorded"` // samples including unreadable ones
	Ready      bool       `json:"ready"`    // enough data for a recommendation
	SkipSecs   float64    `json:"skip_seconds"`
	AttackSecs float64    `json:"attack_seconds"`
	Thresholds Thresholds `json:"thresholds"`
	Advice     []Advice   `json:"advice"`
	Reason     string     `json:"reason"`
}

// Recommend computes the search thresholds that maximise loot per hour for the
// observed loot distribution.
//
// Per resource the candidate set is every distinct observed value: only those
// change which bases pass, so the optimum is always one of them. For each
// candidate t, with P = share of bases whose loot reaches t and E = mean loot
// of those bases, the cycle cost is P*attack + (1-P)*skip and the throughput is
// P*E / cycle. The winner is the t with the highest throughput.
func Recommend(samples []Sample, current Thresholds, opts Options) Recommendation {
	opts = opts.normalized()

	rec := Recommendation{
		Recorded:   len(samples),
		SkipSecs:   opts.SkipSeconds,
		AttackSecs: opts.AttackSeconds,
		Thresholds: current,
	}

	// A base whose loot read as 0/0/0 is indistinguishable from a failed digit
	// read and would drag every threshold toward zero.
	valid := make([]Sample, 0, len(samples))
	for _, s := range samples {
		if s.Empty() {
			continue
		}
		valid = append(valid, s)
	}
	rec.Samples = len(valid)

	if len(valid) < opts.MinSamples {
		rec.Reason = fmt.Sprintf("learning: %d of %d bases read so far", len(valid), opts.MinSamples)
		return rec
	}
	rec.Ready = true

	gold := recommendOne("gold", "Gold", collect(valid, func(s Sample) int { return s.Gold }), current.Gold, current.Gold, largeLootStep, opts)
	elixir := recommendOne("elixir", "Elixir", collect(valid, func(s Sample) int { return s.Elixir }), current.Elixir, current.Elixir, largeLootStep, opts)
	de := recommendOne("de", "Dark elixir", collect(valid, func(s Sample) int { return s.DE }), current.DE, current.DE, darkLootStep, opts)

	rec.Advice = []Advice{gold, elixir, de}
	rec.Thresholds = Thresholds{Gold: gold.Suggested, Elixir: elixir.Suggested, DE: de.Suggested}
	rec.Reason = fmt.Sprintf("based on %d bases at %.1fs per search and %.0fs per raid", len(valid), opts.SkipSeconds, opts.AttackSeconds)
	return rec
}

func collect(samples []Sample, pick func(Sample) int) []int {
	values := make([]int, 0, len(samples))
	for _, s := range samples {
		values = append(values, pick(s))
	}
	return values
}

func recommendOne(key, label string, values []int, current, fallback, step int, opts Options) Advice {
	advice := Advice{Key: key, Label: label, Current: current, Suggested: current}

	best, bestPerHour := optimalThreshold(values, opts.SkipSeconds, opts.AttackSeconds)
	if best <= 0 || bestPerHour <= 0 {
		// Nothing the data supports (every base read zero for this resource).
		return advice
	}

	advice.PerHourNow = throughputPerHour(values, current, opts.SkipSeconds, opts.AttackSeconds)
	advice.PerHourNew = bestPerHour

	suggested := blend(current, best, len(values), opts)
	suggested = roundToStep(suggested, step)
	if suggested <= 0 {
		suggested = fallback
	}
	advice.Suggested = suggested
	advice.Changed = suggested != current
	if advice.Changed {
		advice.PerHourNew = throughputPerHour(values, suggested, opts.SkipSeconds, opts.AttackSeconds)
	}
	return advice
}

// optimalThreshold returns the threshold with the highest loot per hour.
//
// Ties keep the lower threshold: when two candidates earn the same, the cheaper
// one passes more bases and finds an attack sooner, which is what the cycle
// model already priced in — the strict comparison only breaks exact ties.
func optimalThreshold(values []int, skipSec, attackSec float64) (int, float64) {
	if len(values) == 0 {
		return 0, 0
	}
	sorted := append([]int(nil), values...)
	sort.Ints(sorted)

	n := len(sorted)
	// suffix[i] = sum of sorted[i:], so E for a candidate is O(1).
	suffix := make([]int64, n+1)
	for i := n - 1; i >= 0; i-- {
		suffix[i] = suffix[i+1] + int64(sorted[i])
	}

	bestThreshold := 0
	bestPerHour := 0.0
	for i := 0; i < n; i++ {
		// Skip duplicate candidates: same P, same E, same throughput.
		if i > 0 && sorted[i] == sorted[i-1] {
			continue
		}
		threshold := sorted[i]
		if threshold <= 0 {
			continue
		}
		p := float64(n-i) / float64(n)
		mean := float64(suffix[i]) / float64(n-i)
		perHour := throughput(p, mean, skipSec, attackSec)
		if perHour > bestPerHour {
			bestPerHour = perHour
			bestThreshold = threshold
		}
	}
	return bestThreshold, bestPerHour
}

// throughputPerHour is the value of one threshold over the observed data.
func throughputPerHour(values []int, threshold int, skipSec, attackSec float64) float64 {
	if len(values) == 0 || threshold <= 0 {
		return 0
	}
	count := 0
	sum := int64(0)
	for _, v := range values {
		if v >= threshold {
			count++
			sum += int64(v)
		}
	}
	if count == 0 {
		return 0
	}
	p := float64(count) / float64(len(values))
	mean := float64(sum) / float64(count)
	return throughput(p, mean, skipSec, attackSec)
}

func throughput(passShare, meanLoot, skipSec, attackSec float64) float64 {
	if passShare <= 0 || meanLoot <= 0 {
		return 0
	}
	cycle := passShare*attackSec + (1-passShare)*skipSec
	if cycle <= 0 {
		return 0
	}
	return 3600 * passShare * meanLoot / cycle
}

// blend moves part of the way toward the optimum while the sample is small, so
// the first few dozen bases refine a working threshold instead of replacing it.
func blend(current, suggested, n int, opts Options) int {
	if opts.BlendUntil <= 0 || n >= opts.BlendUntil || current <= 0 {
		return clampToCurrent(current, suggested, opts)
	}
	share := float64(n) / float64(opts.BlendUntil)
	moved := float64(current) + float64(suggested-current)*share
	return clampToCurrent(current, int(math.Round(moved)), opts)
}

// clampToCurrent caps a single adjustment relative to the current value.
func clampToCurrent(current, suggested int, opts Options) int {
	if current <= 0 || opts.ClampFraction <= 0 {
		return suggested
	}
	maxDelta := int(math.Round(float64(current) * opts.ClampFraction))
	if maxDelta < 1 {
		maxDelta = 1
	}
	if delta := suggested - current; delta > maxDelta {
		return current + maxDelta
	} else if delta < -maxDelta {
		return current - maxDelta
	}
	return suggested
}

func roundToStep(value, step int) int {
	if step <= 0 || value == 0 {
		return value
	}
	rounded := int(math.Round(float64(value)/float64(step))) * step
	if rounded == 0 {
		return step
	}
	return rounded
}
