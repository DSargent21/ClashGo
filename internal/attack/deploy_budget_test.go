package attack

import (
	"testing"
	"time"

	"github.com/Ducky705/ClashGO/internal/game"
	"github.com/rs/zerolog"
)

// Deploy-budget guards: every reconcile/sweep/verify loop must stop
// firing taps once the deploy budget (which bounds the whole deploy phase
// to well under a CoC 3-minute battle) is exhausted. These tests pin the
// TapExecutor helper the loops consult.

func TestDeployBudget_NotExhaustedBeforeStart(t *testing.T) {
	te := NewTapExecutor(nil, &game.Calibration{ScaleX: 1, ScaleY: 1}, zerolog.Nop())
	if te.DeployBudgetExhausted() {
		t.Fatal("budget reported exhausted before StartDeployBudget was ever called")
	}
}

func TestDeployBudget_ExhaustsAfterBudget(t *testing.T) {
	te := NewTapExecutor(nil, &game.Calibration{ScaleX: 1, ScaleY: 1}, zerolog.Nop())
	te.StartDeployBudgetAt(time.Now().Add(-time.Second))
	if !te.DeployBudgetExhausted() {
		t.Fatal("budget with an expired deadline must report exhausted")
	}
	if got := te.DeployBudgetRemaining(); got != 0 {
		t.Fatalf("remaining budget on expired deadline = %v, want 0", got)
	}
}

func TestDeployBudget_HasRemainingInsideWindow(t *testing.T) {
	te := NewTapExecutor(nil, &game.Calibration{ScaleX: 1, ScaleY: 1}, zerolog.Nop())
	te.StartDeployBudgetAt(time.Now().Add(10 * time.Second))
	if te.DeployBudgetExhausted() {
		t.Fatal("budget with a future deadline must not report exhausted")
	}
	if got := te.DeployBudgetRemaining(); got <= 0 || got > 10*time.Second {
		t.Fatalf("remaining budget = %v, want (0, 10s]", got)
	}
}
