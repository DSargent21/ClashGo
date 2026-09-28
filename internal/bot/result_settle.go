package bot

import (
	"time"

	"github.com/Ducky705/ClashGO/internal/game"
	"github.com/Ducky705/ClashGO/internal/paths"
	"github.com/rs/zerolog"
	"gocv.io/x/gocv"
)

// Post-battle result panel: which read is final.
//
// The panel does not appear finished when it is. Live frames recorded at
// 1280x720 on 2026-09-23 (tmp/live_check/anim, run23) show the real order:
//
//	+0.0s  star emblem painted, loot rows empty      stars=2 loot=(0,0,127)
//	+1.0s  loot counted up                           stars=2 loot=(1126838,1141592,12788)
//	+2.0s  loot unchanged                            bonus=(0,0,0)
//	+3.0s  loot unchanged                            bonus=(0,0,0)
//	+4.0s  loot unchanged                            bonus=(0,0,0)
//	+4.8s  league-bonus column starts counting       bonus=(0,0,868)
//	+5.8s  bonus still counting                      bonus=(0,868,275)
//
// Two traps, and the old rule fell into both. It accepted the first capture
// in which ANY field was non-zero, which the star emblem satisfies on its
// own — so it read the +0.0s frame and recorded a 2-star victory that stole
// 0 gold, 0 elixir, 0 dark elixir and 0 league bonus. Twelve of the last
// twenty-two recorded attacks logged all-zero loot this way; the league
// bonus read zero in every attack since the 720p geometry landed.
//
// The second trap is subtler and is why "wait until two captures look the
// same" is not enough on its own: the loot rows sit frozen and complete for
// five seconds while the bonus column is still empty, so a stable read at
// +2s is a *plausible* final answer that is missing a whole column. The
// panel is translucent over a battlefield that keeps animating, so its
// pixels never stop changing either — pixel stillness cannot be the only
// signal.
//
// What is left is a read that is trusted for three separate reasons:
//
//	1. the panel has been observed for resultPanelMinObserve, which is
//	   longer than a bonus column has ever taken to appear
//	2. the full read — stars, loot rows AND bonus rows — repeated
//	   unchanged across two captures
//	3. a win shows a league bonus: CoC pays one on every battle with at
//	   least one star, so a win reading zero bonus is an unfinished panel,
//	   not a result
//
// A genuine no-loot defeat reads zero forever, so it can never satisfy (2);
// it is accepted instead on panel-pixel stillness held past the observation
// window, which no count-up can fake.

const (
	// resultSettleAttempts caps how many times the panel is re-read. At the
	// caller's cadence that observes the panel for roughly twelve seconds,
	// against a battle that just ran for one to four minutes. The widest
	// league-bonus delay measured live was 8 s, so the cap has room to keep
	// reading past it rather than falling back to a read taken too soon.
	resultSettleAttempts = 18

	// resultPanelMinObserve is how long the panel is watched before any read
	// is believed. The league-bonus column has been measured landing 4.8 s
	// after the first captured frame; the margin covers a slower device.
	resultPanelMinObserve = 7 * time.Second

	// resultZeroStableCaptures is how many consecutive identical PANEL
	// frames an all-zero read must show before it is believed: three frames
	// either side of two stable intervals.
	resultZeroStableCaptures = 3

	// The pauses between settle attempts. They are named because the shortest
	// of them is a safety bound, not a tuning preference: the settle machine
	// believes a read when two consecutive observations agree, so if a capture
	// cache window were long enough to answer two of these attempts with one
	// frame, an animating panel would "settle" on its own. These pauses are the
	// floor that makes that impossible — see
	// TestCaptureCacheCannotBridgeSettleAttempts in the bot package.
	resultSettleAnimatingPause = 400 * time.Millisecond
	resultSettleCapturePause   = 500 * time.Millisecond
	resultSettleParsePause     = 800 * time.Millisecond
)

// resultSettle decides which post-battle panel read is the final one.
//
// It is deliberately free of OpenCV and ADB — it sees only the panel hash,
// the parsed read and the clock — so the animation race can be tested
// against a recorded frame sequence instead of a live emulator.
type resultSettle struct {
	log zerolog.Logger

	reads int       // captures observed
	first time.Time // when the first capture was taken

	prevHash uint64
	havePrev bool
	stable   int // consecutive captures whose panel pixels are identical

	prevRead game.BattleResult
	sameRead int // consecutive captures whose full read is identical

	last     game.BattleResult
	haveLast bool
}

func newResultSettle(log zerolog.Logger) *resultSettle {
	return &resultSettle{log: log}
}

// keepResultFrame reports whether a settle attempt's frame should be written to
// last_battle_result.png.
//
// The artifact exists to be looked at after the fact, so the frames worth
// keeping are the one the accepted read came from, the one that explains a
// parse failure, and — if the panel never settles — the last one captured.
// Every other frame is an animating panel that the next attempt replaces.
// Encoding them all cost a full-frame PNG encode and a disk write on each of up
// to resultSettleAttempts passes, seconds apart, for a file that already had a
// fresher copy by the time anyone could open it.
func keepResultFrame(attempt, lastAttempt int, accepted, parseFailed bool) bool {
	return accepted || parseFailed || attempt >= lastAttempt
}

// saveResultFrame writes the settle frame to the artifact path. It is
// deliberately best-effort: a failure to save a debugging screenshot must never
// abort the battle-result bookkeeping the read is there to feed.
func (b *Bot) saveResultFrame(screen gocv.Mat) {
	path := paths.ResolveConfig("last_battle_result.png")
	if !gocv.IMWrite(path, screen) {
		b.logger.Warn().Str("path", path).Msg("could not save battle result screenshot")
		return
	}
	b.logger.Info().Str("path", path).Msg("saved battle result screenshot to last_battle_result.png")
}

// observe feeds one capture to the state machine and reports whether its
// read is final. The machine owns the attempt budget and the observation
// window, so the caller only has to keep capturing while it says no.
func (s *resultSettle) observe(hash uint64, res game.BattleResult, now time.Time) (game.BattleResult, bool) {
	s.reads++
	if s.first.IsZero() {
		s.first = now
	}
	watched := now.Sub(s.first)

	if !res.IsZero() {
		s.last, s.haveLast = res, true
	}

	// Read stability: the same stars, loot and bonus on two captures in a
	// row. This is what survives a translucent panel over a moving
	// battlefield, where the pixels never hold still.
	if s.reads > 1 && res == s.prevRead {
		s.sameRead++
	} else {
		s.sameRead = 1
	}
	s.prevRead = res

	// Panel-pixel stability, kept for the one case a read cannot settle: a
	// genuine all-zero result, whose read is zero whether the panel has
	// painted or not. A zero hash means the panel region was degenerate or
	// unreadable and can never count as stable.
	stable := hash != 0 && s.havePrev && hash == s.prevHash
	if stable {
		s.stable++
	} else {
		s.stable = 0
	}
	s.prevHash, s.havePrev = hash, hash != 0

	// A win that shows no league bonus yet is a panel still being written:
	// the bonus column lands seconds after the loot rows and counts up.
	bonusPending := res.Stars >= 1 && res.Bonus.Gold == 0

	switch {
	case watched >= resultPanelMinObserve && !bonusPending && s.sameRead >= 2 && !res.IsZero():
		s.log.Debug().
			Int("reads", s.reads).
			Float64("watched_s", watched.Seconds()).
			Msg("battle result accepted: stars, loot and bonus all stopped changing")
		return res, true
	case watched >= resultPanelMinObserve && s.stable >= resultZeroStableCaptures-1 && res.IsZero():
		s.log.Info().
			Int("reads", s.reads).
			Float64("watched_s", watched.Seconds()).
			Msg("battle result accepted: panel froze and reads zero (real no-loot result)")
		return res, true
	case s.reads >= resultSettleAttempts && s.haveLast:
		s.log.Warn().
			Int("reads", s.reads).
			Float64("watched_s", watched.Seconds()).
			Bool("bonus_pending", bonusPending).
			Msg("battle result never stopped changing; keeping the last non-empty read (loot or bonus may be short)")
		return s.last, true
	}
	return res, false
}
