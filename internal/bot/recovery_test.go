package bot

import (
	"testing"
	"time"
)

func testLadder(t *testing.T) []RecoveryStrategy {
	p := NewRecoveryPolicy(RecoveryConfig{
		MaxAttempts:    5,
		InitialBackoff: time.Second,
		MaxBackoff:     time.Minute,
	}, nil)
	return p.Strategies("com.supercell.clashofclans", 1280, 720, 320)
}

func TestEscalate_LinearWalkUnchanged(t *testing.T) {
	p := NewRecoveryPolicy(RecoveryConfig{}, nil)
	strats := testLadder(t)
	for attempt := 1; attempt <= len(strats); attempt++ {
		if got := p.Escalate(strats, attempt, FailureContext{}); got != attempt-1 {
			t.Fatalf("attempt %d → %d, want %d", attempt, got, attempt-1)
		}
	}
	if got := p.Escalate(strats, len(strats)+1, FailureContext{}); got != -1 {
		t.Fatalf("exhausted ladder → %d, want -1", got)
	}
}

func TestEscalate_SkipsRelaunchWhenTransportDown(t *testing.T) {
	p := NewRecoveryPolicy(RecoveryConfig{}, nil)
	strats := testLadder(t)
	relaunch := -1
	for i, s := range strats {
		if s.Name == "RelaunchGame" {
			relaunch = i
		}
	}
	if relaunch < 0 {
		t.Fatal("ladder has no RelaunchGame to skip")
	}
	fc := FailureContext{TransportDown: true}
	if got := p.Escalate(strats, relaunch+1, fc); got == relaunch {
		t.Fatal("transport down but ladder still picks RelaunchGame")
	}
	// The pick must be a later strategy or exhaustion, never earlier.
	if got := p.Escalate(strats, relaunch+1, fc); got != -1 && got < relaunch {
		t.Fatalf("skip moved backwards to %d", got)
	}
}
