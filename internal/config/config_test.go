package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The Wails GUI marshals Duration as {"Duration": <nanoseconds>} (the embedded
// time.Duration has no custom marshaller) while hand-written configs use "5s".
// Both encodings are in the wild and both must load: accepting only the string
// made Load fail on every GUI-written file, LoadOrDefault silently returned
// DefaultConfig, and strategy_slots vanished with it — resolveArmySlot then
// fell through to the strategy YAML's stale army_slot and armed the wrong
// recipe (observed live: valk_spam resolved slot 4 while config.json said 2).
func TestDurationUnmarshalAcceptsObjectAndString(t *testing.T) {
	cases := map[string]time.Duration{
		`{"Duration": 5000000000}`: 5 * time.Second,
		`"5s"`:                     5 * time.Second,
	}
	for in, want := range cases {
		var d Duration
		if err := json.Unmarshal([]byte(in), &d); err != nil {
			t.Fatalf("unmarshal %s: %v", in, err)
		}
		if d.Duration != want {
			t.Errorf("unmarshal %s = %v, want %v", in, d.Duration, want)
		}
	}
}

func TestLoadGUIWrittenConfigKeepsStrategySlots(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	data := `{
	  "training": {"sleep_after_train": {"Duration": 5000000000}},
	  "attack": {
	    "strategy_file": "valk_spam.yaml",
	    "drop_delay": {"Duration": 500000000},
	    "strategy_slots": {"valk_spam.yaml": 2}
	  }
	}`
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg.Attack.StrategySlots["valk_spam.yaml"]; got != 2 {
		t.Errorf("strategy_slots[valk_spam.yaml] = %d, want 2 (config must not fall back to defaults)", got)
	}
	if cfg.Training.SleepAfterTrain.Duration != 5*time.Second {
		t.Errorf("sleep_after_train = %v, want 5s", cfg.Training.SleepAfterTrain.Duration)
	}
}
