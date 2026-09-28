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
//	1. the panel has been observed for resultPanelMinObserve, longer than
//	   the latest measured bonus animation plus a safety margin
//	2. the full read — stars, loot rows AND bonus rows — matched across
//	   consecutive captures
//	3. a win shows a complete league bonus: CoC pays one on every battle
//	   with at least one star, and gold/elixir league bonuses are equal
//	   amounts, so missing or mismatched rows are not final
//
// A genuine no-loot defeat has no nonzero read to settle, so it is accepted
// only on panel-pixel stillness held past the observation window, which no
// count-up can fake.

const (
	maxBattleLootGold        = 10_000_000
	maxBattleLootElixir      = 10_000_000
	maxBattleLootDarkElixir  = 100_000
	maxLeagueBonusGold       = 1_000_000
	maxLeagueBonusElixir     = 1_000_000
	maxLeagueBonusDarkElixir = 100_000

	// resultSettleAttempts and resultPanelMinObserve must allow a result read
	// beyond the latest measured bonus-count animation (8 s), then confirm it
	// again. At 400 ms cadence this allows about 11 s of observation.
	resultSettleAttempts = 28

	// Never accept a stable-looking result before every bonus counter had time
	// to animate. This exceeds the latest measured 8 s delay with 2 s margin.
	resultPanelMinObserve = 10 * time.Second

	// resultZeroStableCaptures is how many consecutive identical PANEL
	// frames an all-zero read must show before it is believed.
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
}

func newResultSettle(log zerolog.Logger) *resultSettle {
	return &resultSettle{log: log}
}

// keepResultFrame reports whether a settle attempt's frame should be written to
// last_battle_result.png.
//
// plausibleBattleResult rejects impossible OCR values before they can enter
// session totals. Caps are deliberately generous: well above observed loot and
// league bonuses, but below five/six-digit OCR shifts into the wrong resource.
func plausibleBattleResult(res game.BattleResult) bool {
	if res.Stars < 0 || res.Stars > 3 {
		return false
	}
	return plausibleResource(res.Loot.Gold, maxBattleLootGold) &&
		plausibleResource(res.Loot.Elixir, maxBattleLootElixir) &&
		plausibleResource(res.Loot.DarkElixir, maxBattleLootDarkElixir) &&
		plausibleResource(res.Bonus.Gold, maxLeagueBonusGold) &&
		plausibleResource(res.Bonus.Elixir, maxLeagueBonusElixir) &&
		plausibleResource(res.Bonus.DarkElixir, maxLeagueBonusDarkElixir)
}

func plausibleResource(value, max int) bool {
	return value >= 0 && value <= max
}

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

	valid := plausibleBattleResult(res)

	// Read stability: the same stars, loot and bonus on two captures in a
	// row. This is what survives a translucent panel over a moving
	// battlefield, where the pixels never hold still.

	if valid {
		if s.sameRead > 0 && res == s.prevRead {
			s.sameRead++
		} else {
			s.sameRead = 1
		}
		s.prevRead = res
	} else {
		s.sameRead = 0
	}

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

	// A win that shows incomplete or mismatched league gold/elixir is a panel
	// still being written (or a row-anchor OCR error): the two resource bonuses
	// are the same league amount, while the count-up can expose partial values.
	bonusPending := res.Stars >= 1 &&
		(res.Bonus.Gold == 0 || res.Bonus.Elixir == 0 || res.Bonus.Gold != res.Bonus.Elixir)

	switch {
	case valid && watched >= resultPanelMinObserve && !bonusPending && s.sameRead >= 2 && !res.IsZero():
		s.log.Debug().
			Int("reads", s.reads).
			Float64("watched_s", watched.Seconds()).
			Msg("battle result accepted: stars, loot and bonus all stopped changing")
		return res, true
	case valid && hash != 0 && watched >= resultPanelMinObserve && s.stable >= resultZeroStableCaptures-1 && res.IsZero():
		s.log.Info().
			Int("reads", s.reads).
			Float64("watched_s", watched.Seconds()).
			Msg("battle result accepted: panel froze and reads zero (real no-loot result)")
		return res, true
	case s.reads >= resultSettleAttempts:
		s.log.Error().
			Int("reads", s.reads).
			Bool("bonus_pending", bonusPending).
			Msg("battle result OCR did not produce a plausible settled result; refusing to persist suspicious resource values")
		return game.BattleResult{}, false
	}
	return res, false
}
