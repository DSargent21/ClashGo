package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Ducky705/ClashGO/internal/autotune"
	"github.com/Ducky705/ClashGO/internal/config"
	"github.com/Ducky705/ClashGO/internal/paths"
)

// autotuneApp points every path helper at a temp tree, seeds a config with the
// manual thresholds and the sample log with the given loot, and returns the app.
func autotuneApp(t *testing.T, loot int, samples int) (*App, string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("CLASHGO_CONFIG_DIR", dir)

	cfg := config.DefaultConfig()
	cfg.Search.MinLootGold = 400000
	cfg.Search.MinLootElixir = 400000
	cfg.Search.MinLootDarkElixir = 2000
	if err := writeConfigFile(cfg); err != nil {
		t.Fatalf("seed config: %v", err)
	}

	// ResolveConfig is what the bindings and the bot read; assert it really is
	// the temp tree so a future paths change cannot silently point this test at
	// the developer's real config.
	if got := paths.ResolveConfig("search_samples.jsonl"); filepath.Dir(got) != dir {
		t.Fatalf("config tree leaked outside the temp dir: %s", got)
	}

	store := autotune.Open(paths.ResolveConfig("search_samples.jsonl"))
	for i := 0; i < samples; i++ {
		if err := store.Append(autotune.NewSample(time.Unix(0, 0), loot, loot, loot/10, i%2 == 0, 2*time.Second)); err != nil {
			t.Fatalf("seed sample: %v", err)
		}
	}
	return NewApp(), dir
}

func TestLootAutotune_NoHistoryIsNotReady(t *testing.T) {
	app, _ := autotuneApp(t, 2000000, 0)

	status := app.GetLootAutotune()

	if status.Ready {
		t.Fatal("an empty history must not be ready")
	}
	if status.Current.Gold != 400000 {
		t.Errorf("current gold = %d, want the config value 400000", status.Current.Gold)
	}
	if status.Suggested != status.Current {
		t.Errorf("suggestion %+v changed with no data", status.Suggested)
	}
	if status.Reason == "" {
		t.Error("expected a human-readable reason")
	}
}

func TestLootAutotune_RecommendsWithoutWriting(t *testing.T) {
	app, _ := autotuneApp(t, 2000000, 300)

	status := app.GetLootAutotune()

	if !status.Ready || status.Samples != 300 {
		t.Fatalf("status = %+v, want ready with 300 samples", status)
	}
	if status.Suggested.Gold != 540000 {
		t.Errorf("suggested gold = %d, want one clamped step 540000", status.Suggested.Gold)
	}

	// Reading a recommendation must never touch config.json.
	cfg := config.LoadOrDefault("config.json")
	if cfg.Search.MinLootGold != 400000 {
		t.Errorf("GetLootAutotune wrote the config: min_loot_gold = %d", cfg.Search.MinLootGold)
	}
}

func TestLootAutotune_ApplyWritesThresholds(t *testing.T) {
	app, _ := autotuneApp(t, 2000000, 300)

	status, err := app.ApplyLootAutotune()
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if status.Current.Gold != 540000 || status.Current.Elixir != 540000 || status.Current.DE != 2700 {
		t.Errorf("applied current = %+v, want 540000/540000/2700", status.Current)
	}

	cfg := config.LoadOrDefault("config.json")
	if cfg.Search.MinLootGold != 540000 || cfg.Search.MinLootElixir != 540000 || cfg.Search.MinLootDarkElixir != 2700 {
		t.Errorf("config after apply = %d/%d/%d, want 540000/540000/2700",
			cfg.Search.MinLootGold, cfg.Search.MinLootElixir, cfg.Search.MinLootDarkElixir)
	}
}

func TestLootAutotune_ApplyRefusesWithoutData(t *testing.T) {
	app, _ := autotuneApp(t, 2000000, 3)

	if _, err := app.ApplyLootAutotune(); err == nil {
		t.Fatal("apply with 3 samples should fail rather than write a guess")
	}
	cfg := config.LoadOrDefault("config.json")
	if cfg.Search.MinLootGold != 400000 {
		t.Errorf("refused apply still wrote the config: %d", cfg.Search.MinLootGold)
	}
}

func TestLootAutotune_TogglePersists(t *testing.T) {
	app, _ := autotuneApp(t, 2000000, 300)

	if err := app.SetAutoLoot(true); err != nil {
		t.Fatalf("enable: %v", err)
	}
	if !config.LoadOrDefault("config.json").Search.AutoLoot {
		t.Error("auto_loot not persisted")
	}
	if !app.GetLootAutotune().Enabled {
		t.Error("status did not report the flag")
	}

	if err := app.SetAutoLoot(false); err != nil {
		t.Fatalf("disable: %v", err)
	}
	cfg := config.LoadOrDefault("config.json")
	if cfg.Search.AutoLoot {
		t.Error("auto_loot not cleared")
	}
	// Disabling keeps the thresholds that are in place instead of reverting
	// them to a stale default.
	if cfg.Search.MinLootGold != 400000 {
		t.Errorf("disable changed the threshold to %d", cfg.Search.MinLootGold)
	}
}

func TestLootAutotune_ResetClearsHistory(t *testing.T) {
	app, dir := autotuneApp(t, 2000000, 300)

	status, err := app.ResetLootAutotune()
	if err != nil {
		t.Fatalf("reset: %v", err)
	}
	if status.Ready || status.Samples != 0 {
		t.Errorf("status after reset = %+v, want empty", status)
	}
	if _, err := os.Stat(filepath.Join(dir, "search_samples.jsonl")); !os.IsNotExist(err) {
		t.Errorf("sample log still on disk: %v", err)
	}
}
