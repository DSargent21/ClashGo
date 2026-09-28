package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Ducky705/ClashGO/internal/logger"
)

// Live button-path smoke: exercises the exact bindings the sidebar
// Start/Stop buttons call, against the running emulator. Deleted after
// the manual verification run (needs live BlueStacks, not CI-safe).
func TestLive_StartStopPath(t *testing.T) {
	if testing.Short() {
		t.Skip("needs live BlueStacks; run without -short")
	}
	a := NewApp()
	// Same bridge wiring as App.startup in production: without it the
	// zerolog output never reaches GetLogs (which is what the test
	// measures — the Dashboard console source).
	logger.Init(false, &WailsLogWriter{app: a})
	st := a.StartBot(400000, 400000, 2000, false, true)
	if !st.Running {
		t.Fatalf("StartBot refused: %s", st.Message)
	}
	t.Logf("start: %s", st.Message)
	// Boot runs async (BlueStacks already warm); poll IsRunning.
	deadline := time.Now().Add(90 * time.Second)
	for {
		if a.IsRunning() {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("bot never reached running state")
		}
		time.Sleep(2 * time.Second)
	}
	time.Sleep(5 * time.Second) // let capture loop + polls tick
	logs := a.GetLogs()
	t.Logf("log lines buffered: %d", len(logs))
	if len(logs) == 0 {
		t.Error("GetLogs empty while bot runs")
	}
	stats := a.GetStats()
	t.Logf("stats: attacks=%d uptime=%s", stats.AttacksCompleted, stats.Uptime)
	stop := a.StopBot()
	if stop.Running {
		t.Fatal("StopBot still reports running")
	}
	t.Logf("stop: %s", stop.Message)
	if a.IsRunning() {
		t.Fatal("IsRunning true after stop")
	}
}

// Updater read paths: Settings page version row + update banner state.
// No network: GetUpdateStatus serves the cached snapshot.
func TestLive_UpdaterReads(t *testing.T) {
	a := NewApp()
	if v := a.GetAppVersion(); v == "" {
		t.Error("GetAppVersion empty")
	}
	st := a.GetUpdateStatus()
	t.Logf("updater state=%q latest=%q", st.State, st.LatestVersion)
}

// Config save path: the Config page's Save button. Writes to a throwaway
// dir so the user's real config.json is untouched, then asserts on the
// written file itself (not the merged in-memory view).
func TestLive_SaveConfigRoundtrip(t *testing.T) {
	t.Setenv("CLASHGO_CONFIG_DIR", t.TempDir())
	a := NewApp()
	if err := a.SaveConfig(111111, 222222, 3333, true, "", true, 45); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(os.Getenv("CLASHGO_CONFIG_DIR"), "config.json"))
	if err != nil {
		t.Fatalf("config not written: %v", err)
	}
	var m struct {
		Search struct {
			MinLootGold   int  `json:"min_loot_gold"`
			MinLootElixir int  `json:"min_loot_elixir"`
			MinLootDE     int  `json:"min_loot_de"`
			Enabled       bool `json:"enabled"`
		} `json:"search"`
		Upgrade struct {
			Walls bool `json:"upgrade_walls"`
		} `json:"upgrade"`
		Attack struct {
			Stall int `json:"stall_timer_seconds"`
		} `json:"attack"`
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("config not parseable: %v", err)
	}
	if m.Search.MinLootGold != 111111 || m.Search.MinLootElixir != 222222 ||
		m.Search.MinLootDE != 3333 || !m.Search.Enabled ||
		!m.Upgrade.Walls || m.Attack.Stall != 45 {
		t.Fatalf("written config mismatch: %s", string(raw))
	}
}
