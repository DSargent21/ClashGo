package attack

import (
	"testing"

	"github.com/Ducky705/ClashGO/pkg/strategy"
)

// TestResolveLiveTapCount_LiveWinsOverDetected: when liveCount > 0, the
// live OCR value drives the tap count (after the +1 pad when >= 6).
// detectedCount, amount, and the heuristic default are all ignored.
func TestResolveLiveTapCount_LiveWinsOverDetected(t *testing.T) {
	hm := &HeroManager{} // no executor / slotManager / logger needed for this pure helper

	cases := []struct {
		name         string
		live, cached int
		want         int
	}{
		{"live=10 padded to 11", 10, 0, 11},
		{"live=8 padded to 9", 8, 0, 9},
		{"live=6 padded to 7", 6, 0, 7},
		{"live=5 unchanged", 5, 0, 5},
		{"live=1 unchanged", 1, 0, 1},
		{"live=12 wins over detected=20", 12, 20, 13},
		{"live=8 wins over detected=30", 8, 30, 9},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := hm.resolveLiveTapCount(strategy.Unit{Name: "Balloon"}, nil, c.live, c.cached)
			if got != c.want {
				t.Errorf("resolveLiveTapCount(live=%d, cached=%d) = %d, want %d",
					c.live, c.cached, got, c.want)
			}
		})
	}
}

// TestResolveLiveTapCount_DetectedFallback: when liveCount==0, the
// detectedCount from the orchestrator-start OCR drives the tap count.
// This is the fallback path used when the bot is constructed without
// a TroopCounter (legacy tests, offline replay, etc.).
func TestResolveLiveTapCount_DetectedFallback(t *testing.T) {
	hm := &HeroManager{}
	cases := []struct {
		cached int
		want   int
	}{
		{0, 8}, // both fail → heuristic default 8 (reconcile corrects under-firing)
		{3, 3}, // small count, no pad
		{6, 7}, // pad floor
		{10, 11},
		{20, 21},
	}
	for _, c := range cases {
		got := hm.resolveLiveTapCount(strategy.Unit{Name: "Electro Dragon", Amount: "All"}, nil, 0, c.cached)
		if got != c.want {
			t.Errorf("resolveLiveTapCount(live=0, cached=%d) = %d, want %d", c.cached, got, c.want)
		}
	}
}

// TestResolveLiveTapCount_AmountFallback: when both live and detected
// are 0, parse the YAML amount. "All" / "" falls through to the
// heuristic default 8 (and the reconcile loop catches under-firing).
func TestResolveLiveTapCount_AmountFallback(t *testing.T) {
	hm := &HeroManager{}
	cases := []struct {
		amount string
		want   int
	}{
		{"3", 3},
		{"12", 12},
		{"All", 8}, // heuristic default
		{"", 8},    // empty amount -> heuristic default
	}
	for _, c := range cases {
		got := hm.resolveLiveTapCount(strategy.Unit{Name: "Balloon", Amount: c.amount}, nil, 0, 0)
		if got != c.want {
			t.Errorf("resolveLiveTapCount(amount=%q) = %d, want %d", c.amount, got, c.want)
		}
	}
}

// TestResolveLiveTapCount_NeverZero: even when ALL inputs are wrong
// (no live OCR, no detected, no amount, empty strategy.Unit), the
// resolver returns at least the heuristic default (8) so the bot
// always fires SOME taps. Returning 0 would skip the main pass and
// leave all troops in the slot when OCR was completely broken.
func TestResolveLiveTapCount_NeverZero(t *testing.T) {
	hm := &HeroManager{}
	got := hm.resolveLiveTapCount(strategy.Unit{Name: "Balloon"}, nil, 0, 0)
	if got < 1 {
		t.Fatalf("resolveLiveTapCount must never return <1 (got %d) so the bot never skips the main pass", got)
	}
}

// TestPadCount_SafetyPadFloor: padCount is the no-pad-when-small rule
// so we don't over-fire tiny slots (5 balloons, 3 rage) into the
// already-empty state.
func TestPadCount_SafetyPadFloor(t *testing.T) {
	cases := []struct {
		in, want int
	}{
		{0, 0}, // treated as "unknown" — caller falls through
		{1, 1}, // no pad on small counts
		{5, 5}, // no pad below floor
		{6, 7}, // floor: pad +1
		{10, 11},
		{20, 21},
	}
	for _, c := range cases {
		if got := padCount(c.in); got != c.want {
			t.Errorf("padCount(%d) = %d, want %d", c.in, got, c.want)
		}
	}
}
