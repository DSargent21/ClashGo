package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/Ducky705/ClashGO/internal/autotune"
	"github.com/Ducky705/ClashGO/internal/config"
	"github.com/Ducky705/ClashGO/internal/paths"
)

// AutotuneStatus is the loot-tuning snapshot the UI renders: what the search
// gate is asking for now, what the observed bases say it should ask for, and
// whether there is enough history to say anything at all.
type AutotuneStatus struct {
	Enabled   bool                `json:"enabled"`
	Samples   int                 `json:"samples"`
	Recorded  int                 `json:"recorded"`
	Ready     bool                `json:"ready"`
	Reason    string              `json:"reason"`
	Current   autotune.Thresholds `json:"current"`
	Suggested autotune.Thresholds `json:"suggested"`
	Advice    []autotune.Advice   `json:"advice"`
}

// lootStore opens the tuning log. Each binding call opens its own handle: the
// bot owns the writer, and a reader that reused a cached copy would report a
// recommendation the running bot has already moved past.
func lootStore() *autotune.Store {
	// ResolveConfig, not Resolve: the sample log is written, and Resolve points
	// at the read-only assets tree (inside the app bundle when packaged).
	return autotune.Open(paths.ResolveConfig("search_samples.jsonl"))
}

func loadAutotuneStatus() AutotuneStatus {
	cfg := config.LoadOrDefault("config.json")
	current := autotune.Thresholds{
		Gold:   cfg.Search.MinLootGold,
		Elixir: cfg.Search.MinLootElixir,
		DE:     cfg.Search.MinLootDarkElixir,
	}
	rec := autotune.Recommend(lootStore().Samples(), current, autotune.DefaultOptions())
	return AutotuneStatus{
		Enabled:   cfg.Search.AutoLoot,
		Samples:   rec.Samples,
		Recorded:  rec.Recorded,
		Ready:     rec.Ready,
		Reason:    rec.Reason,
		Current:   current,
		Suggested: rec.Thresholds,
		Advice:    rec.Advice,
	}
}

// GetLootAutotune reports the recommendation without changing anything. Safe to
// poll: it reads at most a few thousand lines.
func (a *App) GetLootAutotune() AutotuneStatus {
	return loadAutotuneStatus()
}

// ApplyLootAutotune writes the recommendation into config.json and hands the
// running bot the new config, so the next searched base is judged by it.
func (a *App) ApplyLootAutotune() (AutotuneStatus, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	cfg := config.LoadOrDefault("config.json")
	current := autotune.Thresholds{
		Gold:   cfg.Search.MinLootGold,
		Elixir: cfg.Search.MinLootElixir,
		DE:     cfg.Search.MinLootDarkElixir,
	}
	rec := autotune.Recommend(lootStore().Samples(), current, autotune.DefaultOptions())
	if !rec.Ready {
		return loadAutotuneStatus(), fmt.Errorf("loot tuning: %s", rec.Reason)
	}

	cfg.Search.MinLootGold = rec.Thresholds.Gold
	cfg.Search.MinLootElixir = rec.Thresholds.Elixir
	cfg.Search.MinLootDarkElixir = rec.Thresholds.DE
	if a.bot != nil {
		a.bot.UpdateConfig(cfg)
	}
	if err := writeConfigFile(cfg); err != nil {
		return loadAutotuneStatus(), err
	}
	return loadAutotuneStatus(), nil
}

// SetAutoLoot turns self-tuning on or off. Turning it off keeps whatever
// thresholds are in config.json, so the last tuned values become the manual
// ones instead of snapping back to a stale default.
func (a *App) SetAutoLoot(enabled bool) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	cfg := config.LoadOrDefault("config.json")
	cfg.Search.AutoLoot = enabled
	if a.bot != nil {
		a.bot.UpdateConfig(cfg)
	}
	return writeConfigFile(cfg)
}

// ResetLootAutotune deletes the search history so the recommendation relearns
// from the player's current trophy range instead of averaging in old raids.
func (a *App) ResetLootAutotune() (AutotuneStatus, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if err := lootStore().Reset(); err != nil {
		return loadAutotuneStatus(), err
	}
	return loadAutotuneStatus(), nil
}

// writeConfigFile persists a config the same way SaveConfig does, so the two
// paths cannot drift in indentation or file mode.
func writeConfigFile(cfg *config.BotConfig) error {
	bytes, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(paths.ResolveConfig("config.json"), bytes, 0o644)
}
