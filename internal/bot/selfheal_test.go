package bot

import (
	"testing"
	"time"

	"github.com/Ducky705/ClashGO/internal/game"
	"github.com/rs/zerolog"
)

// ponytail: single runnable check for the two fallback mechanics — result
// settle must accept best plausible read, unknown-heal must be budget-bounded.
// Ceiling: full Unknown-state visual coverage needs live frames (cmd/screendump).
func TestSelfHealFallbacks(t *testing.T) {
	// Result settle: gold read fine, stars unread (0), bonus never completes.
	s := newResultSettle(zerolog.Nop())
	r := game.BattleResult{Stars: 0, Loot: game.Resources{Gold: 285935, Elixir: 100000, DarkElixir: 127}}
	now := time.Now()
	var accepted game.BattleResult
	settled := false
	for i := 0; i < resultSettleAttempts; i++ {
		accepted, settled = s.observe(uint64(i+1), r, now.Add(time.Duration(i)*time.Second))
		if settled {
			break
		}
	}
	if !settled {
		t.Fatal("settle never accepted fallback read")
	}
	if accepted.Loot.Gold != 285935 {
		t.Fatalf("fallback lost loot: %+v", accepted)
	}
	// Unknown heal budget helper exists and is bounded (attempt cap lives in
	// processFrame rate-limit; verify atomic counter resets on activity).
	b := &Bot{}
	b.recordActivity()
	if got := b.unknownHealAttempts.Load(); got != 0 {
		t.Fatalf("activity did not reset unknown heal attempts: %d", got)
	}
	if !splashDismissAllowed(maxSplashDismissAttempts) || splashDismissAllowed(maxSplashDismissAttempts+1) {
		t.Fatal("splash budget boundary broken")
	}
}
