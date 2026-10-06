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

// Live regression for the "bot looks stuck after its last attack" bug: the
// bot's own graceful shutdown (attack cap reached) cancels its runtime
// context, which closes Bot.Done() — and the App must clear itself so
// IsRunning() goes false and StartBot is accepted again WITHOUT the user
// pressing Stop. Nothing else in the app did that before.
//
// The shutdown trigger is Bot.Cancel(), which is the same call the cap path
// makes (internal/bot/bot.go's `b.cancel()` after MaxAttackPerSession); the
// only difference is who fires it. No attack is started — the bot is
// cancelled as soon as it reaches the running state, before the search loop
// deploys anything — but the session is real: real boot, real ADB, real
// capture loop, real teardown.
func TestLive_SessionEndAutoStops(t *testing.T) {
	if testing.Short() {
		t.Skip("needs live BlueStacks; run without -short")
	}
	a := NewApp()
	logger.Init(false, &WailsLogWriter{app: a})
	if st := a.StartBot(400000, 400000, 2000, false, true); !st.Running {
		t.Fatalf("StartBot refused: %s", st.Message)
	}

	deadline := time.Now().Add(90 * time.Second)
	for !a.IsRunning() {
		if time.Now().After(deadline) {
			t.Fatal("bot never reached running state")
		}
		time.Sleep(2 * time.Second)
	}

	a.mu.Lock()
	b := a.bot
	a.mu.Unlock()
	if b == nil {
		t.Fatal("running bot is not reachable on the app")
	}

	// End the session the way the attack cap does.
	b.Cancel()

	// The fix under test: the app notices on its own (React's 2s IsRunning
	// poll is the user-visible half; this is the Go state it reads).
	stoppedBy := time.Now().Add(20 * time.Second)
	for a.IsRunning() {
		if time.Now().After(stoppedBy) {
			t.Fatal("session ended on its own but IsRunning() stayed true: the app would keep showing STOP BOT over a finished session")
		}
		time.Sleep(500 * time.Millisecond)
	}

	// A later Start must be accepted (the placeholder is cleared, not just
	// the bot pointer), which is what a reload of the dashboard needs.
	a.mu.Lock()
	placeholder := a.cancel != nil || a.botCtx != nil
	a.mu.Unlock()
	if placeholder {
		t.Error("start placeholder left behind after the session ended; a later StartBot would answer still starting up")
	}

	stats := a.GetStats()
	t.Logf("session ended on its own: attacks=%d uptime=%s", stats.AttacksCompleted, stats.Uptime)
	// Nothing is left running, so a second StartBot is safe and proves the
	// app is not permanently wedged.
	st := a.StartBot(400000, 400000, 2000, false, true)
	if !st.Running {
		t.Fatalf("StartBot after a self-ended session was refused: %s", st.Message)
	}
	t.Logf("restart after self-ended session accepted: %s", st.Message)
	if stop := a.StopBot(); stop.Running {
		t.Fatal("StopBot still reports running")
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
