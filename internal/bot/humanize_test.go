package bot

import (
	"testing"
	"time"
)

func TestHumanizedAttackGapVariesAroundTheConfiguredFloor(t *testing.T) {
	const base = 30 * time.Second
	seen := map[time.Duration]bool{}
	short, long := time.Hour, time.Duration(0)
	for i := 0; i < 500; i++ {
		g := humanizedAttackGap(base)
		seen[g] = true
		if g < 22*time.Second {
			t.Fatalf("gap %v is below the humanized floor for a %v base", g, base)
		}
		if g > 4*time.Minute {
			t.Fatalf("gap %v is beyond the long-pause ceiling", g)
		}
		if g < short {
			short = g
		}
		if g > long {
			long = g
		}
	}
	if len(seen) < 100 {
		t.Fatalf("only %d distinct gaps in 500 draws; the cadence is still periodic", len(seen))
	}
	if long < 2*time.Minute {
		t.Fatalf("longest gap %v; the occasional longer pause never happened", long)
	}
}

func TestHumanizedGapIsDisabledWithZeroBase(t *testing.T) {
	if got := humanizedGap(0, 0.5); got != 0 {
		t.Fatalf("humanizedGap(0) = %v, want 0", got)
	}
}

func TestIdlePanDelayIsIrregular(t *testing.T) {
	seen := map[time.Duration]bool{}
	short, long := time.Hour, time.Duration(0)
	for i := 0; i < 1000; i++ {
		d := idlePanDelay()
		seen[d] = true
		if d < 22*time.Second || d > 5*time.Minute {
			t.Fatalf("idle pan delay %v outside the 22s-5m band", d)
		}
		if d < short {
			short = d
		}
		if d > long {
			long = d
		}
	}
	// The generator has 36 normal second-values plus 181 long ones; anything
	// near that spread is irregular, a single constant is not.
	if len(seen) < 80 {
		t.Fatalf("only %d distinct delays in 1000 draws; the pan is still a metronome", len(seen))
	}
	if long < 2*time.Minute {
		t.Fatalf("longest delay %v; the occasional multi-minute stare never happened", long)
	}
}

func TestHumanizedMinutesStayDeterministicWithoutJitter(t *testing.T) {
	for i := 0; i < 100; i++ {
		if got := humanizedMinutes(75, 0); got != 75 {
			t.Fatalf("humanizedMinutes(75, 0) = %d, want 75", got)
		}
		if got := humanizedBreakMinutes(20, 0); got != 20 {
			t.Fatalf("humanizedBreakMinutes(20, 0) = %d, want 20", got)
		}
	}
}

func TestHumanizedMinutesProducesShortSessionsAndLongBreaks(t *testing.T) {
	shortest, longestBreak := 10_000, 0
	for i := 0; i < 3000; i++ {
		if got := humanizedMinutes(75, 20); got < 23 || got > 95 {
			t.Fatalf("session %d minutes outside the 55-95 minute jitter envelope", got)
		} else if got < shortest {
			shortest = got
		}
		if got := humanizedBreakMinutes(20, 7); got < 13 || got > 300 {
			t.Fatalf("break %d minutes outside the 13-27 minute jitter envelope plus long tail", got)
		} else if got > longestBreak {
			longestBreak = got
		}
	}
	if shortest > 60 {
		t.Fatalf("shortest session %d minutes; no short check-in session ever appeared", shortest)
	}
	if longestBreak < 90 {
		t.Fatalf("longest break %d minutes; no extended absence ever appeared", longestBreak)
	}
}
