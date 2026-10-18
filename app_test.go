package main

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

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

// TestApp_SessionEndMarksBotStopped covers the bug where a bot that ended
// its own session (attack cap reached) left the app believing it was still
// running: `a.bot` stayed set, IsRunning() kept answering true, and the
// sidebar sat on "STOP BOT" over a finished session while React — which
// only learns about changes from StartBot / StopBot / bot_error — never
// flipped. Dropping the bot reference is what makes IsRunning() false and
// a later StartBot accepted.
//
// The bot here is a zero-valued *bot.Bot: the watcher only ever compares
// its pointer and calls the injected stats/teardown callbacks, so nothing
// touches the real bot internals (no ADB, no BlueStacks).
func TestApp_SessionEndMarksBotStopped(t *testing.T) {
	t.Setenv("CLASHGO_CONFIG_DIR", t.TempDir())
	a := NewApp()

	// The state StartBot installs for a booted, running bot.
	b := &bot.Bot{}
	a.bot = b
	a.botCtx, a.cancel = context.WithCancel(context.Background())
	a.lastStats = bot.BotStats{AttacksCompleted: 1, TotalGold: 500}

	done := make(chan struct{})
	tornDown := make(chan struct{})
	a.watchSessionEnd(done, a.currentBotIs(b),
		func() bot.BotStats { return bot.BotStats{AttacksCompleted: 1, TotalGold: 500} },
		func() { close(tornDown) },
	)

	// The bot's runtime context is cancelled the moment the session ends.
	close(done)

	select {
	case <-tornDown:
	case <-time.After(5 * time.Second):
		t.Fatal("session end never reached teardown")
	}

	// IsRunning() reads a.bot, so this is the user-visible "the sidebar can
	// leave STOP BOT now" assertion.
	if a.IsRunning() {
		t.Error("IsRunning() still true after the bot ended its own session")
	}
	if a.bot != nil {
		t.Error("a.bot not cleared; a later StartBot would still be refused as already running")
	}
	if a.cancel != nil || a.botCtx != nil {
		t.Error("start placeholder (cancel/botCtx) not cleared; StartBot would keep reporting still starting up")
	}
	if a.lastStats.AttacksCompleted != 2 || a.lastStats.TotalGold != 1000 {
		t.Errorf("final stats not banked: got attacks=%d gold=%d, want attacks=2 gold=1000",
			a.lastStats.AttacksCompleted, a.lastStats.TotalGold)
	}
}

// TestApp_SessionEndAfterUserStopIsNoop is the ordering guard for the other
// side of the race: StopBot cancels the bot and nulls a.bot while holding
// a.mu, which also closes Bot.Done(). The session-end watcher must then
// find the bot is no longer the current one and do nothing — otherwise the
// bot gets torn down twice and its final stats are banked twice.
func TestApp_SessionEndAfterUserStopIsNoop(t *testing.T) {
	t.Setenv("CLASHGO_CONFIG_DIR", t.TempDir())
	a := NewApp()

	b := &bot.Bot{}
	a.bot = nil // StopBot already detached it
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a.botCtx, a.cancel = ctx, cancel
	a.lastStats = bot.BotStats{TotalGold: 500}

	done := make(chan struct{})
	teardownRan := make(chan struct{})
	a.watchSessionEnd(done, a.currentBotIs(b),
		func() bot.BotStats {
			t.Error("stats read for a bot StopBot already owns")
			return bot.BotStats{}
		},
		func() { close(teardownRan) },
	)

	close(done)

	select {
	case <-teardownRan:
		t.Fatal("watcher tore down a bot StopBot already stopped")
	case <-time.After(250 * time.Millisecond):
	}

	if a.lastStats.TotalGold != 500 {
		t.Errorf("stats double-counted on a user stop: gold=%d, want 500", a.lastStats.TotalGold)
	}
	if a.cancel == nil {
		t.Error("watcher cancelled the app's start placeholder; StopBot/StartBot own that")
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

func TestApp_UpdaterBindings(t *testing.T) {
	t.Setenv("CLASHGO_CONFIG_DIR", t.TempDir())
	a := NewApp()

	// Initially updater is initialized
	st := a.GetUpdateStatus()
	if st.State == "" {
		t.Errorf("expected non-empty state from GetUpdateStatus, got empty")
	}

	// InstallAndRestart with no update available returns error
	err := a.InstallAndRestart()
	if err == nil {
		t.Error("expected error from InstallAndRestart when no update available, got nil")
	}

	// Nil updater guard
	a.updater = nil
	nilSt := a.GetUpdateStatus()
	if nilSt.Error == "" {
		t.Errorf("expected error in status when updater is nil, got empty")
	}
	if err := a.InstallAndRestart(); err == nil {
		t.Error("expected error when updater is nil")
	}
}

func TestApp_AllUIBindingsAndFeatures(t *testing.T) {
	t.Setenv("CLASHGO_CONFIG_DIR", t.TempDir())
	a := NewApp()

	// 1. Initial State Getters
	cfg := a.GetConfig()
	if cfg == nil {
		t.Fatal("GetConfig is nil")
	}
	stats := a.GetStats()
	if stats.AttacksCompleted != 0 {
		t.Errorf("expected 0 attacks, got %d", stats.AttacksCompleted)
	}
	history := a.GetAttackHistory()
	if history == nil {
		t.Fatal("GetAttackHistory is nil")
	}
	logs := a.GetLogs()
	if logs == nil {
		t.Fatal("GetLogs is nil")
	}
	strats := a.GetStrategies()
	if strats == nil {
		t.Fatal("GetStrategies is nil")
	}
	if a.IsRunning() {
		t.Fatal("IsRunning should be false initially")
	}
	if v := a.GetAppVersion(); v == "" {
		t.Fatal("GetAppVersion returned empty string")
	}

	// 2. SaveConfig
	err := a.SaveConfig(350000, 350000, 1500, true, "barcher", true, 25)
	if err != nil {
		t.Fatalf("SaveConfig failed: %v", err)
	}

	// 3. SetStorageConfig
	err = a.SetStorageConfig(true, true, true, "ANY", 92.0, 80.0, true, true)
	if err != nil {
		t.Fatalf("SetStorageConfig failed: %v", err)
	}

	// 4. SetScheduleConfig
	err = a.SetScheduleConfig(true, "09:00", "22:00", 45, 10, 15, 5, true, 8, 50)
	if err != nil {
		t.Fatalf("SetScheduleConfig failed: %v", err)
	}

	// 5. SetAutoUpgradeConfig
	pets := []string{"Diggy", "Frosty", "Unicorn"}
	err = a.SetAutoUpgradeConfig(
		true, 2, "cheapest_first",
		true, true, false, true, true,
		6000000, 5000000, 80000,
		true, "troops_first", true,
		true, pets,
		"gold", 1000000, 1500000,
		false,
	)
	if err != nil {
		t.Fatalf("SetAutoUpgradeConfig failed: %v", err)
	}

	// 6. Strategy army slot & auto end
	err = a.SetStrategyArmySlot("barcher", 3)
	if err != nil {
		t.Fatalf("SetStrategyArmySlot failed: %v", err)
	}
	err = a.SetStrategyAutoEnd("barcher", true)
	if err != nil {
		t.Fatalf("SetStrategyAutoEnd failed: %v", err)
	}

	// 7. Verify all saved properties re-read accurately from GetConfig
	freshCfg := a.GetConfig()
	if freshCfg.Search.MinLootGold != 350000 || freshCfg.Search.MinLootElixir != 350000 || freshCfg.Search.MinLootDarkElixir != 1500 ||
		!freshCfg.Upgrade.UpgradeWalls || !freshCfg.Search.Enabled || freshCfg.Attack.StallTimerSeconds != 25 {
		t.Errorf("SaveConfig values mismatch: %+v", freshCfg)
	}
	if !freshCfg.Storage.HaltWhenGoldFull || !freshCfg.Storage.HaltWhenElixirFull || !freshCfg.Storage.HaltWhenDEFull ||
		freshCfg.Storage.FullThresholdPercent != 92.0 || !freshCfg.Storage.DumpToWallsBeforeHalt || !freshCfg.Storage.AutoCollect {
		t.Errorf("SetStorageConfig values mismatch: %+v", freshCfg.Storage)
	}
	if !freshCfg.Schedule.Enabled || freshCfg.Schedule.ActiveHoursStart != "09:00" || freshCfg.Schedule.ActiveHoursEnd != "22:00" ||
		freshCfg.Schedule.SessionDurationMinutes != 45 || freshCfg.Schedule.SessionJitterMinutes != 10 ||
		freshCfg.Schedule.BreakDurationMinutes != 15 || freshCfg.Schedule.BreakJitterMinutes != 5 ||
		!freshCfg.Schedule.ForceStopOnBreak || freshCfg.Schedule.MaxAttacksPerHour != 8 || freshCfg.Schedule.MaxAttacksPerDay != 50 {
		t.Errorf("SetScheduleConfig values mismatch: %+v", freshCfg.Schedule)
	}
	if !freshCfg.Upgrade.AutoBuilders || freshCfg.Upgrade.ReserveBuilders != 2 || freshCfg.Upgrade.UpgradeStrategy != "cheapest_first" ||
		!freshCfg.Upgrade.UpgradeDefenses || !freshCfg.Upgrade.UpgradeResources || freshCfg.Upgrade.UpgradeArmy ||
		!freshCfg.Upgrade.UpgradeTraps || !freshCfg.Upgrade.UpgradeHeroes ||
		freshCfg.Upgrade.MaxGoldPerUpgrade != 6000000 || freshCfg.Upgrade.MaxElixirPerUpgrade != 5000000 || freshCfg.Upgrade.MaxDEPerUpgrade != 80000 ||
		!freshCfg.Upgrade.AutoLab || freshCfg.Upgrade.LabStrategy != "troops_first" || !freshCfg.Upgrade.LabAllowDE ||
		!freshCfg.Upgrade.AutoPets || len(freshCfg.Upgrade.PetPriorities) != 3 ||
		freshCfg.Upgrade.WallResource != "gold" || freshCfg.Upgrade.WallMinGoldReserve != 1000000 || freshCfg.Upgrade.WallMinElixirReserve != 1500000 {
		t.Errorf("SetAutoUpgradeConfig values mismatch: %+v", freshCfg.Upgrade)
	}
	if freshCfg.Attack.StrategySlots["barcher"] != 3 {
		t.Errorf("StrategySlots mismatch: %d, want 3", freshCfg.Attack.StrategySlots["barcher"])
	}
	if !freshCfg.Attack.StrategyAutoEnd["barcher"] {
		t.Errorf("StrategyAutoEnd mismatch: want true")
	}

	// 8. Stats reset
	a.lastStats.AttacksCompleted = 5
	a.lastStats.TotalGold = 1000000
	a.ResetStats()
	clearedStats := a.GetStats()
	if clearedStats.AttacksCompleted != 0 || clearedStats.TotalGold != 0 {
		t.Errorf("ResetStats failed: %+v", clearedStats)
	}

	// 9. Version skip & clear (without update available, returns expected error)
	if err := a.SkipCurrentVersion(); err == nil || err.Error() != "no version available to skip" {
		t.Errorf("SkipCurrentVersion expected 'no version available to skip', got %v", err)
	}
	if err := a.ClearSkippedVersion(); err != nil {
		t.Errorf("ClearSkippedVersion: %v", err)
	}
}

