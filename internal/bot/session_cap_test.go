package bot

import (
	"testing"

	"github.com/Ducky705/ClashGO/internal/config"
)

// The session cap contract, in one place because two call sites depend on it
// (the early return at the top of executeAttackSequence and the graceful
// shutdown after the battle report). A positive
// attack.max_attack_per_session ends the session on its own; 0 and negative
// never do. The unlimited case is the one that matters live: a session that
// stops by itself is a bug there, because the bot cancels its own context and
// the app's sidebar goes back to "stopped" with nobody having asked it to.
func TestSessionCapReached(t *testing.T) {
	for _, tc := range []struct {
		cap     int
		attacks int32
		want    bool
	}{
		// Unlimited: no attack count may ever trip the cap, including the
		// second attack of a session and a long unattended run.
		{0, 0, false},
		{0, 1, false},
		{0, 999999, false},
		{-1, 0, false},
		{-1, 50, false},
		// A positive cap trips exactly at its value, not before.
		{1, 0, false},
		{1, 1, true},
		{1, 2, true},
		{100, 99, false},
		{100, 100, true},
	} {
		if got := sessionCapReached(tc.cap, tc.attacks); got != tc.want {
			t.Errorf("sessionCapReached(cap=%d, attacks=%d) = %v, want %v", tc.cap, tc.attacks, got, tc.want)
		}
	}
}

// TestDefaultConfigRunsUntilStopped pins the shipped default to unlimited, so a
// fresh config (or one with no max_attack_per_session key) cannot silently
// reintroduce the one-attack-then-stop behaviour the cap used to impose.
func TestDefaultConfigRunsUntilStopped(t *testing.T) {
	cfg := config.DefaultConfig()
	if cfg.Attack.MaxAttackPerSession > 0 {
		t.Fatalf("default max_attack_per_session = %d; a session must run until the user stops it", cfg.Attack.MaxAttackPerSession)
	}
	for _, attacks := range []int32{1, 10, 1000} {
		if sessionCapReached(cfg.Attack.MaxAttackPerSession, attacks) {
			t.Fatalf("the default config stopped a session at attack %d", attacks)
		}
	}
}
