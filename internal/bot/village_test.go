package bot

import (
	"testing"

	"github.com/Ducky705/ClashGO/internal/config"
	"github.com/Ducky705/ClashGO/internal/village"
	"github.com/rs/zerolog"
)

// A TH6 account should not be searching with a TH15 floor: 100k is on the curve,
// 750k is a base that does not exist at that level.
func TestSearchThresholdsFollowTownHall(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Search.AutoLoot = false
	cfg.Search.UseSuggestedFloors = true

	b := &Bot{cfg: cfg, logger: zerolog.Nop(), villageTH: 6}
	gold, elixir, de := b.searchThresholds()
	if gold != 100_000 || elixir != 100_000 || de != 0 {
		t.Fatalf("TH6 floors = (%d,%d,%d), want (100000,100000,0)", gold, elixir, de)
	}
}

// Without a Town Hall (no export, no config override) the operator's numbers are
// the numbers. The feature must never quietly rewrite a config it cannot justify.
func TestSearchThresholdsKeepManualFloorsWithoutTownHall(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Search.AutoLoot = false

	b := &Bot{cfg: cfg, logger: zerolog.Nop()}
	if gold, elixir, de := b.searchThresholds(); gold != cfg.Search.MinLootGold || elixir != cfg.Search.MinLootElixir || de != cfg.Search.MinLootDarkElixir {
		t.Fatalf("floors = (%d,%d,%d), want the configured (%d,%d,%d)", gold, elixir, de,
			cfg.Search.MinLootGold, cfg.Search.MinLootElixir, cfg.Search.MinLootDarkElixir)
	}

	b.villageTH = 6
	b.cfg.Search.UseSuggestedFloors = false
	if gold, _, _ := b.searchThresholds(); gold != cfg.Search.MinLootGold {
		t.Fatalf("gold floor = %d with the feature off, want the configured %d", gold, cfg.Search.MinLootGold)
	}
}

// A floor above what any base offers has one symptom: an endless search. Cap it
// and say so once.
func TestSearchThresholdsCapUnreachableFloor(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Search.AutoLoot = false
	cfg.Search.UseSuggestedFloors = false
	cfg.Search.MinLootGold = 9_000_000

	b := &Bot{cfg: cfg, logger: zerolog.Nop()}
	gold, _, _ := b.searchThresholds()
	if gold != village.MostGold {
		t.Fatalf("gold floor = %d, want it capped at %d", gold, village.MostGold)
	}
	if !b.floorClampWarned.Load() {
		t.Fatal("the cap must latch so the warning is logged once, not every search")
	}
}
