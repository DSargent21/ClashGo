// Package autotune turns the loot the bot already reads on every searched base
// into a self-tuning search threshold.
//
// The idea is a renewal-reward calculation rather than a chosen percentile:
// searching is cheap (a few seconds per base) and attacking is expensive (most
// of a minute), so the threshold that maximises loot per hour is not "the 90th
// percentile" — it is the value that balances time spent skipping against the
// loot of the bases that pass. Given an empirical distribution of the loot the
// game is currently offering, that optimum can be computed exactly.
package autotune

import "time"

// Sample is one base whose loot the recognizer managed to read, plus whether
// the search gate attacked it.
//
// Taken is recorded even though the thresholds can be derived from the loot
// values alone: it is what makes a later comparison possible ("this threshold
// would have attacked 61 of these bases"), and it lets a recommendation be
// explained without re-running the gate.
type Sample struct {
	At       int64 `json:"at"` // unix milliseconds
	Gold     int   `json:"gold"`
	Elixir   int   `json:"elixir"`
	DE       int   `json:"de"`
	Taken    bool  `json:"taken"`
	SearchMs int64 `json:"search_ms"` // time spent finding this base
}

// NewSample stamps a sample with the current time.
func NewSample(now time.Time, gold, elixir, de int, taken bool, search time.Duration) Sample {
	return Sample{
		At:       now.UnixMilli(),
		Gold:     gold,
		Elixir:   elixir,
		DE:       de,
		Taken:    taken,
		SearchMs: search.Milliseconds(),
	}
}

// Empty reports whether every resource read as zero. A base with literally no
// loot is indistinguishable from a failed digit read, and feeding those into
// the distribution drags every threshold toward zero, so the solver ignores
// them (the caller still records them for diagnostics).
func (s Sample) Empty() bool {
	return s.Gold <= 0 && s.Elixir <= 0 && s.DE <= 0
}

// Thresholds are the three search minimums, in the same units as config.json.
type Thresholds struct {
	Gold   int
	Elixir int
	DE     int
}
