package bot

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/rs/zerolog"

	"github.com/Ducky705/ClashGO/internal/autotune"
	"github.com/Ducky705/ClashGO/internal/config"
	"github.com/Ducky705/ClashGO/internal/game"
)

// autoLootBot builds the smallest Bot the search gate needs: a config, a
// sample log and a discard logger.
func autoLootBot(t *testing.T, auto bool, samples []autotune.Sample) *Bot {
	t.Helper()
	store := autotune.Open(filepath.Join(t.TempDir(), "search_samples.jsonl"))
	for _, s := range samples {
		if err := store.Append(s); err != nil {
			t.Fatalf("seed sample: %v", err)
		}
	}

	cfg := config.DefaultConfig()
	cfg.Search.MinLootGold = 400000
	cfg.Search.MinLootElixir = 400000
	cfg.Search.MinLootDarkElixir = 2000
	cfg.Search.AutoLoot = auto

	return &Bot{cfg: cfg, samples: store, logger: zerolog.Nop()}
}

func flat(gold, elixir, de, n int) []autotune.Sample {
	out := make([]autotune.Sample, n)
	for i := range out {
		out[i] = autotune.Sample{Gold: gold, Elixir: elixir, DE: de}
	}
	return out
}

func TestSearchThresholds_ManualWhenAutoDisabled(t *testing.T) {
	b := autoLootBot(t, false, flat(4000000, 4000000, 40000, 500))

	gold, elixir, de := b.searchThresholds()
	if gold != 400000 || elixir != 400000 || de != 2000 {
		t.Errorf("got %d/%d/%d, want the config values 400000/400000/2000", gold, elixir, de)
	}
}

func TestSearchThresholds_ManualUntilEnoughHistory(t *testing.T) {
	b := autoLootBot(t, true, flat(4000000, 4000000, 40000, 5))

	gold, _, _ := b.searchThresholds()
	if gold != 400000 {
		t.Errorf("gold = %d, want the config value while the sample is below the gate", gold)
	}
}

func TestSearchThresholds_TunedWhenAutoEnabled(t *testing.T) {
	b := autoLootBot(t, true, flat(2000000, 2000000, 200000, 300))

	gold, elixir, de := b.searchThresholds()
	// Optimum is the observed loot, one 35% clamp step per call from the current
	// value: 400k -> 540k, 400k -> 540k, 2k -> 2.7k.
	if gold != 540000 || elixir != 540000 || de != 2700 {
		t.Errorf("got %d/%d/%d, want one clamped step 540000/540000/2700", gold, elixir, de)
	}
}

func TestSearchThresholds_UnreadableHistoryFallsBack(t *testing.T) {
	b := autoLootBot(t, true, flat(0, 0, 0, 300))

	gold, elixir, de := b.searchThresholds()
	if gold != 400000 || elixir != 400000 || de != 2000 {
		t.Errorf("got %d/%d/%d, want the config values when every read was zero", gold, elixir, de)
	}
}

func TestRecordLootSample_WritesBothOutcomes(t *testing.T) {
	b := autoLootBot(t, true, nil)
	path := b.samples.Path()

	if err := b.samples.Append(autotune.NewSample(time.Now(), 1, 1, 1, false, time.Second)); err != nil {
		t.Fatal(err)
	}
	b.recordLootSample(game.Resources{Gold: 900000, Elixir: 800000, DarkElixir: 4000}, true, 4*time.Second)

	samples := autotune.Open(path).Samples()
	if len(samples) != 2 {
		t.Fatalf("logged %d samples, want 2", len(samples))
	}
	if samples[1].Gold != 900000 || samples[1].Elixir != 800000 || samples[1].DE != 4000 {
		t.Errorf("loot not recorded: %+v", samples[1])
	}
	if !samples[1].Taken {
		t.Error("an attacked base must be recorded as taken")
	}
	if samples[1].SearchMs != 4000 {
		t.Errorf("SearchMs = %d, want 4000", samples[1].SearchMs)
	}
}

// A nil store means "not wired" and must not crash the search loop: the sample
// log is telemetry, not a precondition for attacking.
func TestRecordLootSample_NilStoreIsSafe(t *testing.T) {
	b := &Bot{logger: zerolog.Nop()}
	b.recordLootSample(game.Resources{Gold: 1}, true, time.Second)
}
