package main

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/Ducky705/ClashGO/internal/bot"
	"github.com/Ducky705/ClashGO/internal/paths"
)

func TestApp_GetConfig(t *testing.T) {
	a := NewApp()
	cfg := a.GetConfig()
	if cfg == nil {
		t.Error("GetConfig returned nil")
	}
}

func TestApp_GetStats(t *testing.T) {
	a := NewApp()
	stats := a.GetStats()
	if stats.AttacksCompleted != 0 {
		t.Errorf("Expected 0 attacks, got %d", stats.AttacksCompleted)
	}
}

func TestApp_GetAttackHistory(t *testing.T) {
	a := NewApp()
	history := a.GetAttackHistory()
	if history == nil {
		t.Error("GetAttackHistory returned nil")
	}
}

func TestApp_GetStrategies(t *testing.T) {
	a := NewApp()
	strats := a.GetStrategies()
	if strats == nil {
		t.Error("GetStrategies returned nil")
	}
}

// TestApp_RefreshHistoryReReadsWarmCache is the regression test for the
// "latest attack never shows in history" bug. The App's cachedHistory
// is warmed by React's very first GetAttackHistory poll; after that,
// refreshHistory (fired per-attack from OnStatsUpdate) must STILL
// re-read attack_history.json from disk. Previously it delegated to
// ensureHistoryLoadedLocked, which no-ops on a warm cache — freezing
// the UI on the launch-time snapshot while loot totals kept climbing.
//
// It also guards the ordering contract: the bot writes the report to
// disk before firing OnStatsUpdate, so the re-read must see it.
func TestApp_RefreshHistoryReReadsWarmCache(t *testing.T) {
	// Redirect all state writes to a throwaway dir so the test never
	// touches the user's real config dir (or the wails dev watcher).
	t.Setenv("CLASHGO_CONFIG_DIR", t.TempDir())

	a := NewApp()
	histPath := paths.ResolveConfig("attack_history.json")

	writeHist := func(reps []bot.AttackReport) {
		t.Helper()
		data, err := json.Marshal(reps)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(histPath, data, 0644); err != nil {
			t.Fatal(err)
		}
	}

	// Prime the disk with one attack, then warm the App's cache via a
	// GetAttackHistory poll (same cold-start poll React fires at launch).
	writeHist([]bot.AttackReport{{Timestamp: "first", Stars: 3}})
	if got := a.GetAttackHistory(); len(got) != 1 {
		t.Fatalf("warm-up: want 1 history entry, got %d", len(got))
	}

	// Simulate a second attack completing: the bot appends the new
	// report to disk and fires OnStatsUpdate → refreshHistory.
	writeHist([]bot.AttackReport{
		{Timestamp: "second", Stars: 2},
		{Timestamp: "first", Stars: 3},
	})
	a.refreshHistory()

	got := a.GetAttackHistory()
	if len(got) != 2 {
		t.Fatalf("refreshHistory did not re-read disk: want 2 entries, got %d (cache is stale)", len(got))
	}
	if got[0].Timestamp != "second" {
		t.Errorf("latest attack missing from history head: got %+v", got)
	}
}
