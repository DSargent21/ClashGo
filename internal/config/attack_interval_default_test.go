package config

import (
	"os"
	"path/filepath"
	"testing"
)

// The inter-attack pause is the account's loudest server-side pattern: a 30 s
// default produced 22 raids in 68 minutes. The shipped default is now
// human-paced, while a config that names the key keeps the user's own value.
func TestAttackIntervalDefaultIsHumanPacedAndConfigurable(t *testing.T) {
	if got := DefaultConfig().Attack.MinSecondsBetweenAttacks; got != 300 {
		t.Fatalf("default min_seconds_between_attacks = %d, want 300 (5 minutes)", got)
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	if err := os.WriteFile(path, []byte(`{"attack":{"min_seconds_between_attacks":30}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Attack.MinSecondsBetweenAttacks != 30 {
		t.Fatalf("explicit min_seconds_between_attacks = %d, want the configured 30", cfg.Attack.MinSecondsBetweenAttacks)
	}

	if err := os.WriteFile(path, []byte(`{"attack":{"enabled":true}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err = Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Attack.MinSecondsBetweenAttacks != 300 {
		t.Fatalf("omitted min_seconds_between_attacks = %d, want the human-paced default 300", cfg.Attack.MinSecondsBetweenAttacks)
	}
}
