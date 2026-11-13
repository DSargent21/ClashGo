package config

import (
	"encoding/json"
	"testing"
)

// The flag is read from config.json written by the UI and by hand, so the JSON
// name is part of the contract: a rename would silently disable auto tuning.
func TestSearchConfig_AutoLootJSON(t *testing.T) {
	raw := []byte(`{"enabled":true,"auto_loot":true,"min_loot_gold":420000}`)
	var search SearchConfig
	if err := json.Unmarshal(raw, &search); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !search.AutoLoot {
		t.Fatal("auto_loot did not unmarshal into SearchConfig.AutoLoot")
	}

	out, err := json.Marshal(SearchConfig{AutoLoot: true})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var round map[string]any
	if err := json.Unmarshal(out, &round); err != nil {
		t.Fatalf("round-trip unmarshal: %v", err)
	}
	if got, _ := round["auto_loot"].(bool); !got {
		t.Errorf(`marshalled JSON is missing "auto_loot": true: %s`, out)
	}
}

// Default config ships auto tuning off: a fresh install must attack on the same
// thresholds it always has until the player opts in.
func TestDefaultConfig_AutoLootOff(t *testing.T) {
	if DefaultConfig().Search.AutoLoot {
		t.Error("auto_loot defaulted on; a fresh profile must keep the manual thresholds")
	}
}
