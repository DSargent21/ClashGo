package bot

import (
	"testing"
	"time"

	"github.com/Ducky705/ClashGO/internal/game"
	"github.com/rs/zerolog"
)

// The result-panel settle state machine is exercised with the frame
// sequences the live 720p runs actually produced: each capture is a panel
// hash, the result parsed off that frame, and a timestamp — exactly what the
// machine sees and what a recorded run provides.

// panel is one recorded capture.
type panel struct {
	name string
	hash uint64
	res  game.BattleResult
	// at is when the capture was taken, relative to the first one.
	at time.Duration
}

// run rec feeds frames to a fresh machine and reports which index was
// accepted (-1 when none was).
func runSettle(t *testing.T, frames []panel) (*resultSettle, int, game.BattleResult) {
	t.Helper()
	settle := newResultSettle(zerolog.Nop())
	base := time.Date(2026, 9, 23, 11, 26, 37, 0, time.UTC)

	for i, f := range frames {
		got, ok := settle.observe(f.hash, f.res, base.Add(f.at))
		if ok {
			return settle, i, got
		}
	}
	return settle, -1, game.BattleResult{}
}

func loot(gold, elixir, de int) game.Resources {
	return game.Resources{Gold: gold, Elixir: elixir, DarkElixir: de}
}

// TestResultSettleWaitsForLateLeagueBonus replays the frames recorded from
// the live run23 attack (tmp/live_check/anim). The old rule accepted the
// FIRST of them — the star emblem's paint frame — and so recorded a 2-star
// victory as 0 gold / 0 elixir / 0 dark elixir / 0 league bonus. The frames
// also show the trap that a naive "two identical captures" rule falls into:
// the loot rows sit complete and unchanging for five seconds while the
// bonus column is still empty.
func TestResultSettleAcceptsStableResultAfterLateLeagueBonus(t *testing.T) {
	final := game.BattleResult{
		Stars: 2,
		Loot:  loot(1126838, 1141592, 12788),
		Bonus: loot(312400, 312400, 2310),
	}
	frames := []panel{
		{"emblem painted, loot rows empty (the frame the old rule accepted)", 100, game.BattleResult{Stars: 2, Loot: loot(0, 0, 127)}, 0},
		{"loot counted up, bonus column not drawn yet", 101, game.BattleResult{Stars: 2, Loot: loot(1126838, 1141592, 12788)}, 1 * time.Second},
		{"loot unchanged, bonus still empty", 102, game.BattleResult{Stars: 2, Loot: loot(1126838, 1141592, 12788)}, 2 * time.Second},
		{"loot unchanged, bonus still empty", 103, game.BattleResult{Stars: 2, Loot: loot(1126838, 1141592, 12788)}, 3 * time.Second},
		{"loot unchanged, bonus still empty", 104, game.BattleResult{Stars: 2, Loot: loot(1126838, 1141592, 12788)}, 4 * time.Second},
		{"bonus column starts counting", 105, game.BattleResult{Stars: 2, Loot: loot(1126838, 1141592, 12788), Bonus: loot(0, 0, 868)}, 5 * time.Second},
		{"bonus still counting", 106, game.BattleResult{Stars: 2, Loot: loot(1126838, 1141592, 12788), Bonus: loot(0, 868, 275)}, 6 * time.Second},
		{"bonus landed", 107, final, 8 * time.Second},
		{"whole panel unchanged", 107, final, 9 * time.Second},
		{"settled beyond minimum observation window", 107, final, 10 * time.Second},
	}

	_, accepted, got := runSettle(t, frames)
	wantAccepted := len(frames) - 1
	if accepted != wantAccepted {
		t.Fatalf("accepted capture %d, want the last one (%d)", accepted, wantAccepted)
	}
	if got != final {
		t.Errorf("accepted %+v, want %+v", got, final)
	}
}

// TestResultSettleWaitsForCompleteLeagueBonus pins the league-bonus gate: a
// win with no bonus, or with mismatched gold/elixir bonus, is unfinished or
// misread even after the observation window.
func TestResultSettleWaitsForCompleteLeagueBonus(t *testing.T) {
	cases := []struct {
		name  string
		bonus game.Resources
	}{
		{"missing bonus", loot(0, 0, 0)},
		{"gold and elixir bonus disagree", loot(312400, 278400, 2310)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			settle := newResultSettle(zerolog.Nop())
			base := time.Date(2026, 9, 23, 11, 26, 37, 0, time.UTC)
			win := game.BattleResult{
				Stars: 2,
				Loot:  loot(1126838, 1141592, 12788),
				Bonus: tc.bonus,
			}

			// Eleven seconds of an unchanging but incomplete win: beyond the
			// observation window it must still be rejected, not banked.
			for i := 0; i < resultSettleAttempts; i++ {
				got, ok := settle.observe(200, win, base.Add(time.Duration(i)*resultSettleAnimatingPause))
				if ok {
					t.Fatalf("capture %d accepted an incomplete league bonus: %+v", i, got)
				}
			}
		})
	}
}

func TestResultSettleAcceptsStableZeroStarLootWithoutBonus(t *testing.T) {
	settle := newResultSettle(zerolog.Nop())
	base := time.Date(2026, 9, 23, 11, 26, 37, 0, time.UTC)
	defeat := game.BattleResult{Loot: loot(648538, 499912, 2360)}

	for i := 0; i < resultSettleAttempts; i++ {
		got, ok := settle.observe(200, defeat, base.Add(time.Duration(i)*resultSettleAnimatingPause))
		if ok {
			if got != defeat {
				t.Fatalf("accepted %+v, want %+v", got, defeat)
			}
			if time.Duration(i)*resultSettleAnimatingPause < resultPanelMinObserve {
				t.Fatalf("accepted before observation window at capture %d", i)
			}
			return
		}
	}
	t.Fatal("stable zero-star loot result was not accepted; defeat loot does not carry a league bonus")
}

func TestResultSettleAcceptsCompleteMatchingLeagueBonus(t *testing.T) {
	settle := newResultSettle(zerolog.Nop())
	base := time.Date(2026, 9, 23, 11, 26, 37, 0, time.UTC)
	win := game.BattleResult{
		Stars: 2,
		Loot:  loot(1126838, 1141592, 12788),
		Bonus: loot(312400, 312400, 2310),
	}

	for i := 0; i < resultSettleAttempts; i++ {
		got, ok := settle.observe(200, win, base.Add(time.Duration(i)*resultSettleAnimatingPause))
		if ok {
			if got != win {
				t.Fatalf("accepted %+v, want %+v", got, win)
			}
			if time.Duration(i)*resultSettleAnimatingPause < resultPanelMinObserve {
				t.Fatalf("accepted before observation window at capture %d", i)
			}
			return
		}
	}
	t.Fatal("complete, matching stable result was not accepted within the budget")
}

// TestResultSettleAcceptsStableZeroResult covers a genuine no-loot defeat,
// which reads zero whether the panel has painted or not. It is a real result
// and must still be recorded, but only on panel stillness held past the
// observation window.
func TestResultSettleAcceptsStableZeroResult(t *testing.T) {
	settle := newResultSettle(zerolog.Nop())
	base := time.Date(2026, 9, 23, 11, 26, 37, 0, time.UTC)
	zero := game.BattleResult{}

	var acceptedAt time.Duration = -1
	for i := 0; i <= int(resultPanelMinObserve/resultSettleAnimatingPause); i++ {
		at := time.Duration(i) * resultSettleAnimatingPause
		if _, ok := settle.observe(77, zero, base.Add(at)); ok {
			acceptedAt = at
			break
		}
	}
	if acceptedAt < resultPanelMinObserve {
		t.Fatalf("all-zero result accepted after %v, want no earlier than %v", acceptedAt, resultPanelMinObserve)
	}
}

// TestResultSettleRejectsUnstableRead covers a panel that never stops
// changing (adb jitter, or a themed shimmer). A plausible but changing value
// must not be persisted as a settled result.
func TestResultSettleRejectsUnstableRead(t *testing.T) {
	settle := newResultSettle(zerolog.Nop())
	base := time.Date(2026, 9, 23, 11, 26, 37, 0, time.UTC)

	for i := 0; i < resultSettleAttempts; i++ {
		// A defeat, so the league-bonus guard is out of the picture: this
		// test is about refusing a plausible value that never holds still.
		res := game.BattleResult{Loot: loot(100*(i+1), 0, 0)}
		got, ok := settle.observe(uint64(9000+i), res, base.Add(time.Duration(i)*time.Second))
		if ok {
			t.Fatalf("attempt %d accepted changing result %+v", i, got)
		}
	}
	if settle.reads != resultSettleAttempts {
		t.Errorf("reads=%d, want exhausted budget of %d", settle.reads, resultSettleAttempts)
	}
}

func TestResultSettleRejectsRepeatedImplausibleResourceReads(t *testing.T) {
	settle := newResultSettle(zerolog.Nop())
	base := time.Date(2026, 9, 23, 11, 26, 37, 0, time.UTC)
	bad := game.BattleResult{Stars: 2, Loot: loot(500_000, 400_000, 10_000), Bonus: loot(300_000, 300_000, 316_800)}

	for i := 0; i < resultSettleAttempts; i++ {
		got, done := settle.observe(uint64(i+1), bad, base.Add(time.Duration(i)*resultSettleAnimatingPause))
		if done {
			t.Fatalf("attempt %d accepted implausible OCR result %+v", i, got)
		}
	}
	if settle.reads != resultSettleAttempts {
		t.Fatalf("reads=%d, want exhausted attempts=%d", settle.reads, resultSettleAttempts)
	}
}

func TestPlausibleBattleResultRejectsBadRanges(t *testing.T) {
	cases := []struct {
		name string
		res  game.BattleResult
		want bool
	}{
		{"normal result", game.BattleResult{Stars: 3, Loot: loot(2_000_000, 1_900_000, 21_000), Bonus: loot(320_000, 320_000, 2_400)}, true},
		{"dark elixir bonus hallucination", game.BattleResult{Stars: 2, Loot: loot(650_000, 500_000, 2_300), Bonus: loot(280_000, 280_000, 316_800)}, false},
		{"negative value", game.BattleResult{Loot: loot(-1, 0, 0)}, false},
		{"too many stars", game.BattleResult{Stars: 4}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := plausibleBattleResult(tc.res); got != tc.want {
				t.Errorf("plausibleBattleResult(%+v) = %v, want %v", tc.res, got, tc.want)
			}
		})
	}
}

// TestResultSettleUnsettledZeroReadIsNotAccepted covers a panel that never
// stops changing AND reads nothing: there is no read worth keeping, so the
// caller must fall through to the destruction-rule star count.
func TestResultSettleRejectsUnsettledZeroRead(t *testing.T) {
	settle := newResultSettle(zerolog.Nop())
	base := time.Date(2026, 9, 23, 11, 26, 37, 0, time.UTC)

	for i := 0; i < resultSettleAttempts; i++ {
		got, ok := settle.observe(uint64(4000+i), game.BattleResult{}, base.Add(time.Duration(i)*resultSettleAnimatingPause))
		if i < resultSettleAttempts-1 && ok {
			t.Fatalf("attempt %d accepted an unreadable, unstable panel", i)
		}
		if i == resultSettleAttempts-1 && ok {
			t.Fatalf("final unreadable zero result was accepted as (%+v, %v); zero hash is no evidence of a settled result", got, ok)
		}
	}
}

// TestResultSettleZeroHashIsNeverStable covers a panel region the hash
// function could not bound (resultPanelHash returns 0). Repeated zero hashes
// must not read as "the panel stopped changing", so an all-zero read against
// a zero hash can never be accepted as a real zero result — there is no
// evidence the panel ever painted.
func TestResultSettleZeroHashIsNeverStable(t *testing.T) {
	settle := newResultSettle(zerolog.Nop())
	base := time.Date(2026, 9, 23, 11, 26, 37, 0, time.UTC)

	for i := 0; i < resultSettleAttempts; i++ {
		got, ok := settle.observe(0, game.BattleResult{}, base.Add(time.Duration(i)*resultSettleAnimatingPause))
		if ok {
			t.Fatalf("attempt %d accepted a degenerate panel hash as a painted, zero result: %+v", i, got)
		}
	}
}

// TestKeepResultFrame pins when the post-battle artifact is written down. The
// settle loop used to re-encode the full frame to PNG and rewrite the same file
// on every attempt — up to resultSettleAttempts writes, seconds apart, for a
// file each one replaced. A frame earns a write only when it is the read the
// battle result came from, the evidence for a parse failure, or the last
// attempt (a panel that never settled must still leave something to look at).
func TestKeepResultFrame(t *testing.T) {
	const last = resultSettleAttempts - 1

	cases := []struct {
		name        string
		attempt     int
		accepted    bool
		parseFailed bool
		want        bool
		rationale   string
	}{
		{"mid-panel attempt", 3, false, false, false, "the animating panel the next capture replaces"},
		{"accepted read", 3, true, false, true, "the frame the result was read from"},
		{"parse failure", 3, false, true, true, "the evidence for a parse error"},
		{"last attempt, never settled", last, false, false, true, "degraded reads still need an artifact"},
		{"last attempt and accepted", last, true, false, true, "both reasons agree"},
		{"first attempt accepted", 0, true, false, true, "settling on the first capture is normal"},
	}
	for _, tc := range cases {
		if got := keepResultFrame(tc.attempt, last, tc.accepted, tc.parseFailed); got != tc.want {
			t.Errorf("%s: keepResultFrame(attempt=%d, last=%d, accepted=%v, parseFailed=%v) = %v, want %v (%s)",
				tc.name, tc.attempt, last, tc.accepted, tc.parseFailed, got, tc.want, tc.rationale)
		}
	}
}

// TestBattleResultIsZero pins the predicate the settle logic and the empty
// read check share: any value on any row makes a result non-zero.
func TestBattleResultIsZero(t *testing.T) {
	cases := []struct {
		name string
		res  game.BattleResult
		want bool
	}{
		{"empty", game.BattleResult{}, true},
		{"stars only", game.BattleResult{Stars: 2}, false},
		{"loot only", game.BattleResult{Loot: game.Resources{Gold: 1}}, false},
		{"bonus only", game.BattleResult{Bonus: game.Resources{DarkElixir: 2310}}, false},
	}
	for _, tc := range cases {
		if got := tc.res.IsZero(); got != tc.want {
			t.Errorf("%s: IsZero() = %v, want %v", tc.name, got, tc.want)
		}
	}
}
