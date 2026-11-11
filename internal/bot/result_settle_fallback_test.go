package bot

import (
	"testing"
	"time"

	"github.com/Ducky705/ClashGO/internal/game"
	"github.com/rs/zerolog"
)

// Live 2026-11-10, 1024x768: the defeat panel showed "You got 1 159 086 gold"
// with no league-bonus row, so bonusPending never cleared. The settle machine
// rejected all 28 reads and the attack banked 0 stars / 0 loot even though the
// panel was finished and readable. These pin the last-resort Fallback: it banks
// the measured loot, and it still refuses a panel that was mid-bonus-animation.
func TestResultSettleFallbackBanksLootWhenBonusRowNeverPaints(t *testing.T) {
	s := newResultSettle(zerolog.Nop())
	start := time.Now()

	// The order the live OCR actually produced: stars flapping down from the
	// painted star art, loot appearing late, bonus always zero.
	reads := []game.BattleResult{
		{Stars: 3},
		{Stars: 2},
		{Stars: 2},
		{Stars: 1},
		{Stars: 1, Loot: game.Resources{Gold: 1159086, Elixir: 535916, DarkElixir: 8670}},
	}

	var got game.BattleResult
	settled := false
	for i := 0; i < resultSettleAttempts && !settled; i++ {
		r := reads[len(reads)-1]
		if i < len(reads) {
			r = reads[i]
		}
		got, settled = s.observe(uint64(i+1), r, start.Add(time.Duration(i)*resultSettleAnimatingPause))
	}
	if settled {
		t.Fatal("strict settle accepted a read with no league-bonus row; the victory wait contract must hold")
	}
	got, ok := s.Fallback()
	if !ok {
		t.Fatal("fallback refused a repeated, plausible, non-zero read")
	}
	if got.Loot.Gold != 1159086 || got.Loot.Elixir != 535916 || got.Loot.DarkElixir != 8670 {
		t.Fatalf("fallback lost loot: %+v", got.Loot)
	}
	if got.Bonus != (game.Resources{}) {
		t.Fatalf("accepted read invented a bonus: %+v", got.Bonus)
	}
}

func TestResultSettleFallbackKeepsBestLootRead(t *testing.T) {
	s := newResultSettle(zerolog.Nop())
	start := time.Now()

	// Stars alternate forever, so the full read is never stable and the strict
	// cases can never fire. The loot is constant and plausible.
	good := game.BattleResult{Stars: 1, Loot: game.Resources{Gold: 1159086, Elixir: 535916, DarkElixir: 8670}}
	flapped := good
	flapped.Stars = 2

	for i := 0; i < resultSettleAttempts; i++ {
		r := good
		if i%2 == 1 {
			r = flapped
		}
		if _, settled := s.observe(uint64(i+1), r, start.Add(time.Duration(i)*resultSettleAnimatingPause)); settled {
			t.Fatalf("read with a flapping star field settled at attempt %d", i)
		}
	}

	fb, ok := s.Fallback()
	if !ok {
		t.Fatal("fallback refused a repeated, plausible, non-zero read")
	}
	if fb.Loot.Gold != good.Loot.Gold || fb.Stars != 1 {
		t.Fatalf("fallback = %+v, want the best read %+v", fb, good)
	}
}

// A settle that only ever saw zero can still fall back to nothing: an unread
// panel must never bank a fabricated value.
func TestResultSettleFallbackRefusesZeroRead(t *testing.T) {
	s := newResultSettle(zerolog.Nop())
	start := time.Now()
	for i := 0; i < resultSettleAttempts; i++ {
		_, _ = s.observe(uint64(i+1), game.BattleResult{}, start.Add(time.Duration(i)*resultSettleAnimatingPause))
	}
	if fb, ok := s.Fallback(); ok {
		t.Fatalf("fallback banked a zero read: %+v", fb)
	}
}
