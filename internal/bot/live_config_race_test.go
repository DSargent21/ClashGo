package bot

import (
	"sync"
	"testing"

	"github.com/Ducky705/ClashGO/internal/config"
)

// TestLiveConfigSaveIsRaceFree is the regression guard for the S1 race in
// docs/AUDIT.md: a UI save published a new config while the capture loop, the
// attack goroutine and the stats poll were reading the old fields, which the
// race detector reported at bot.go:3713 / live_config.go:34.
//
// Run it under -race (the full suite does). It also asserts the publication is
// visible after UpdateConfig returns, and that the same save reached the
// scheduler and builder-manager snapshots, so a future "fix" that reads a
// stale copy fails here instead of only being race-free.
func TestLiveConfigSaveIsRaceFree(t *testing.T) {
	const rounds = 100
	b := NewHeadlessBot(config.DefaultConfig())

	var wg sync.WaitGroup
	wg.Add(3)

	// Writer: what App.SaveConfig does from the UI goroutine.
	go func() {
		defer wg.Done()
		for i := 0; i < rounds; i++ {
			cfg := config.DefaultConfig()
			cfg.Schedule.Enabled = i%2 == 0
			cfg.Search.MinLootGold = 600000 + i
			cfg.Search.MinLootElixir = 600000 + i
			cfg.Upgrade.AllowTownHall = i%2 == 1
			cfg.Attack.StrategyFile = "valk_spam.yaml"
			b.UpdateConfig(cfg)
		}
	}()

	// Readers: the capture loop's gate, the UI's stats poll, and LiveConfig.
	go func() {
		defer wg.Done()
		for i := 0; i < rounds; i++ {
			b.scheduleAllowed()
			_ = b.Stats()
			_ = b.LiveConfig()
			_ = b.HaltedReason()
		}
	}()

	// Readers: direct field access through the accessor plus a builder-manager
	// policy read (the old code copied the UpgradeConfig struct in place).
	go func() {
		defer wg.Done()
		for i := 0; i < rounds; i++ {
			if cfg := b.cfg(); cfg != nil {
				_ = cfg.Search.MinLootGold
				_ = cfg.Upgrade.AutoBuilders
				_ = cfg.Device.DisableChestDismissal
			}
			_ = b.armySlot.Load()
			_ = b.builderManager.isCategoryAllowed("defenses")
		}
	}()

	wg.Wait()

	// Publication is visible once UpdateConfig returns.
	final := config.DefaultConfig()
	final.Schedule.Enabled = false
	final.Search.MinLootGold = 1234567
	final.Upgrade.AllowTownHall = true
	b.UpdateConfig(final)

	if got := b.cfg().Search.MinLootGold; got != 1234567 {
		t.Fatalf("published gold floor = %d, want 1234567", got)
	}
	live := b.LiveConfig()
	if live.Config.Search.MinLootGold != 1234567 {
		t.Fatalf("LiveConfig gold floor = %d, want 1234567", live.Config.Search.MinLootGold)
	}
	if live.Scheduler == nil || live.Scheduler.Enabled {
		t.Fatalf("scheduler snapshot did not receive the save: %+v", live.Scheduler)
	}
	if live.Upgrade == nil || !live.Upgrade.AllowTownHall {
		t.Fatalf("builder-manager snapshot did not receive the save: %+v", live.Upgrade)
	}
}
