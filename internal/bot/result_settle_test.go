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
func TestResultSettleWaitsForLateLeagueBonus(t *testing.T) {
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
	}

	_, accepted, got := runSettle(t, frames)
	if accepted != len(frames)-1 {
		t.Fatalf("accepted capture %d, want the last one (%d)", accepted, len(frames)-1)
	}
	if got != final {
		t.Errorf("accepted %+v, want %+v", got, final)
	}
}

// TestResultSettleWaitsForTheLateLeagueBonus pins rule (3) on its own: a win
// whose read has stopped changing but shows no bonus is still an unfinished
// panel, because CoC pays a league bonus on every battle with a star.
func TestResultSettleWaitsForTheLateLeagueBonus(t *testing.T) {
	settle := newResultSettle(zerolog.Nop())
	base := time.Date(2026, 9, 23, 11, 26, 37, 0, time.UTC)
	win := game.BattleResult{Stars: 2, Loot: loot(1126838, 1141592, 12788)}

	// Twelve seconds of an unchanging win with no bonus row: past the
	// observation window, still not believable.
	for i := 0; i < resultSettleAttempts-1; i++ {
		if _, ok := settle.observe(200, win, base.Add(time.Duration(i)*time.Second)); ok {
			t.Fatalf("capture %d accepted a win with no league bonus on it", i)
		}
	}

	// The cap still returns the loot it did see, rather than nothing.
	got, ok := settle.observe(200, win, base.Add(time.Duration(resultSettleAttempts)*time.Second))
	if !ok {
		t.Fatal("the final attempt must return the last read")
	}
	if got.Loot != win.Loot {
		t.Errorf("loot = %+v, want %+v", got.Loot, win.Loot)
	}
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
	for i := 0; i <= int(resultPanelMinObserve/time.Second); i++ {
		at := time.Duration(i) * time.Second
		if _, ok := settle.observe(77, zero, base.Add(at)); ok {
			acceptedAt = at
			break
		}
	}
	if acceptedAt < resultPanelMinObserve {
		t.Fatalf("all-zero result accepted after %v, want no earlier than %v", acceptedAt, resultPanelMinObserve)
	}
}

// TestResultSettleFallsBackToLastRead covers a panel that never stops
// changing (adb jitter, or a themed shimmer). The caller gets the best read
// seen instead of nothing, with a warning that it may be short.
func TestResultSettleFallsBackToLastRead(t *testing.T) {
	settle := newResultSettle(zerolog.Nop())
	base := time.Date(2026, 9, 23, 11, 26, 37, 0, time.UTC)

	var got game.BattleResult
	var ok bool
	for i := 0; i < resultSettleAttempts; i++ {
		// A defeat, so the league-bonus guard is out of the picture: this
		// test is about the read never holding still.
		res := game.BattleResult{Loot: loot(100*(i+1), 0, 0)}
		got, ok = settle.observe(uint64(9000+i), res, base.Add(time.Duration(i)*time.Second))
		if i < resultSettleAttempts-1 && ok {
			t.Fatalf("attempt %d accepted a panel that never stopped changing", i)
		}
	}

	if !ok {
		t.Fatal("the final attempt must return the last read rather than nothing")
	}
	if want := 100 * resultSettleAttempts; got.Loot.Gold != want {
		t.Errorf("gold = %d, want %d (the LAST read, not the first)", got.Loot.Gold, want)
	}
}

// TestResultSettleUnsettledZeroReadIsNotAccepted covers a panel that never
// stops changing AND reads nothing: there is no read worth keeping, so the
// caller must fall through to the destruction-rule star count.
func TestResultSettleUnsettledZeroReadIsNotAccepted(t *testing.T) {
	settle := newResultSettle(zerolog.Nop())
	base := time.Date(2026, 9, 23, 11, 26, 37, 0, time.UTC)

	for i := 0; i < resultSettleAttempts; i++ {
		if _, ok := settle.observe(uint64(4000+i), game.BattleResult{}, base.Add(time.Duration(i)*time.Second)); ok {
			t.Fatalf("attempt %d accepted an unreadable, unstable panel", i)
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
		if _, ok := settle.observe(0, game.BattleResult{}, base.Add(time.Duration(i)*time.Second)); ok {
			t.Fatalf("attempt %d accepted a degenerate panel hash as a painted, zero result", i)
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
