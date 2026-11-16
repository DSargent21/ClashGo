// Package bot — humanize.go
//
// Helpers that turn fixed wall-clock intervals into varied ones.
//
// A bot's loudest behavioural signature is not where it taps but WHEN: the
// same 30 s gap between raids, the same 18 s between camera pans, the same
// session and break length every single day. Server-side play-pattern checks
// see those, and a human never produces them. Every interval in this file is
// a distribution, not a constant; none of them changes what the bot decides,
// only how long it waits before doing it.
package bot

import (
	"math/rand"
	"time"
)

// humanizedGap returns base varied by ±spread (0.22 = 78%-122%). Callers use it
// where the interval is recomputed once per wait, so the draw sticks. Do not
// call it inside a per-frame comparison: re-drawing every frame would make the
// wait collapse to the minimum of the distribution.
func humanizedGap(base time.Duration, spread float64) time.Duration {
	if base <= 0 {
		return 0
	}
	if spread < 0 {
		spread = 0
	}
	d := float64(base) * (1 - spread + rand.Float64()*2*spread)
	if d < float64(5*time.Millisecond) {
		d = float64(5 * time.Millisecond)
	}
	return time.Duration(d)
}

// humanizedAttackGap is the pause between Return Home and the next search.
// The configured minimum is honoured as a floor-ish value (±22%), and roughly
// one raid in ten is followed by a longer pause — a human checks the result,
// answers a message, or simply does not tap again immediately. Without that
// tail, every raid of a 50-raid day starts on the same 30 s beat.
func humanizedAttackGap(base time.Duration) time.Duration {
	gap := humanizedGap(base, 0.22)
	if rand.Intn(10) == 0 {
		gap += time.Duration(30+rand.Intn(121)) * time.Second
	}
	return gap
}

// idlePanDelay is how long the account waits between idle camera pans.
//
// A pan every 18 s forever is a metronome. A waiting player mostly sits still
// and occasionally drags the map: 22-57 s normally, and about one wait in
// eight is a multi-minute stare (they put the phone down).
func idlePanDelay() time.Duration {
	if rand.Intn(8) == 0 {
		return 2*time.Minute + time.Duration(rand.Intn(181))*time.Second
	}
	return 22*time.Second + time.Duration(rand.Intn(36))*time.Second
}

// humanizedMinutes varies a scheduler duration. With jitter disabled (0) it
// returns base unchanged, which keeps a deterministic config deterministic and
// leaves the scheduler tests meaningful.
//
// The 1-in-6 short session models a check-in rather than a farming block: a
// player who mostly plays 75-minute sessions still has days where they open
// the game for half an hour. The 1-in-10 long break models stepping away.
// Both only apply when the user asked for jitter.
func humanizedMinutes(base, jitter int) int {
	if jitter <= 0 {
		return base
	}
	mins := base + rand.Intn(2*jitter+1) - jitter
	if rand.Intn(6) == 0 {
		mins = int(float64(mins) * (0.55 + rand.Float64()*0.2))
	}
	return mins
}

// humanizedBreakMinutes varies a break and occasionally stretches it into a
// real absence. See humanizedMinutes for why jitter 0 stays deterministic.
func humanizedBreakMinutes(base, jitter int) int {
	if jitter <= 0 {
		return base
	}
	mins := base + rand.Intn(2*jitter+1) - jitter
	if rand.Intn(10) == 0 {
		mins += 60 + rand.Intn(121)
	}
	return mins
}
