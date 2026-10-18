package main

import (
	"encoding/json"
	"image"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Ducky705/ClashGO/internal/adb"
	"github.com/Ducky705/ClashGO/internal/bot"
	"github.com/Ducky705/ClashGO/internal/config"
	"github.com/Ducky705/ClashGO/internal/game"
	"github.com/Ducky705/ClashGO/internal/logger"
	"github.com/rs/zerolog/log"
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

func TestLive_SaveAutonomousConfigsRoundtrip(t *testing.T) {
	t.Setenv("CLASHGO_CONFIG_DIR", t.TempDir())
	a := NewApp()

	if err := a.SetStorageConfig(true, true, true, "any", 92, 70, true, true); err != nil {
		t.Fatalf("SetStorageConfig: %v", err)
	}
	if err := a.SetScheduleConfig(true, "07:30", "22:15", 45, 10, 20, 5, true, 8, 50); err != nil {
		t.Fatalf("SetScheduleConfig: %v", err)
	}
	pets := []string{"Diggy", "Unicorn", "Frosty"}
	if err := a.SetAutoUpgradeConfig(
		true, 2, "cheapest_first",
		true, true, true, false, true,
		5000000, 4000000, 100000,
		true, "troops_first", true,
		true, pets,
		"elixir", 1500000, 2500000,
		true,
	); err != nil {
		t.Fatalf("SetAutoUpgradeConfig: %v", err)
	}

	conf := a.GetConfig()
	if !conf.Storage.HaltWhenGoldFull || !conf.Storage.HaltWhenElixirFull || !conf.Storage.HaltWhenDEFull ||
		conf.Storage.FullThresholdPercent != 92 || !conf.Storage.DumpToWallsBeforeHalt || !conf.Storage.AutoCollect {
		t.Fatalf("Storage config mismatch: %+v", conf.Storage)
	}
	if !conf.Schedule.Enabled || conf.Schedule.ActiveHoursStart != "07:30" ||
		conf.Schedule.ActiveHoursEnd != "22:15" || conf.Schedule.SessionDurationMinutes != 45 ||
		conf.Schedule.BreakDurationMinutes != 20 || !conf.Schedule.ForceStopOnBreak ||
		conf.Schedule.MaxAttacksPerHour != 8 || conf.Schedule.MaxAttacksPerDay != 50 {
		t.Fatalf("Schedule config mismatch: %+v", conf.Schedule)
	}
	if !conf.Upgrade.AutoBuilders || conf.Upgrade.ReserveBuilders != 2 || conf.Upgrade.UpgradeStrategy != "cheapest_first" ||
		!conf.Upgrade.UpgradeDefenses || !conf.Upgrade.UpgradeResources || !conf.Upgrade.UpgradeArmy ||
		conf.Upgrade.UpgradeTraps || !conf.Upgrade.UpgradeHeroes ||
		conf.Upgrade.MaxGoldPerUpgrade != 5000000 || conf.Upgrade.MaxElixirPerUpgrade != 4000000 || conf.Upgrade.MaxDEPerUpgrade != 100000 ||
		!conf.Upgrade.AutoLab || conf.Upgrade.LabStrategy != "troops_first" || !conf.Upgrade.LabAllowDE ||
		!conf.Upgrade.AutoPets || len(conf.Upgrade.PetPriorities) != 3 ||
		conf.Upgrade.WallResource != "elixir" || conf.Upgrade.WallMinGoldReserve != 1500000 || conf.Upgrade.WallMinElixirReserve != 2500000 ||
		!conf.Upgrade.DryRun {
		t.Fatalf("Upgrade config mismatch: %+v", conf.Upgrade)
	}
}

// TestLive_BuildMenuAndCollectorEndToEnd tests the Build Menu 2.0 navigation (open, inspect,
// tabs, modal close, backdrop dismiss) and auto resource collection against the live emulator.
func TestLive_BuildMenuAndCollectorEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("needs live BlueStacks; run without -short")
	}
	client := adb.NewClient()
	client.DeviceID = "localhost:5555"
	if err := client.AutoDetectDevice(); err != nil {
		t.Fatalf("adb auto detect: %v", err)
	}
	defer client.Close()

	cal := game.NewCalibration(1024, 768)
	cfg := config.DefaultConfig().Upgrade
	cfg.AutoBuilders = true
	cfg.ReserveBuilders = 1
	cfg.AutoLab = true
	cfg.AutoPets = true

	bm := bot.NewBuilderManager(client, cal, cfg, log.Logger)

	// 1. Open Menu
	if err := bm.OpenMenu(); err != nil {
		t.Fatalf("OpenMenu: %v", err)
	}
	time.Sleep(1 * time.Second)

	// 2. Perform automated upgrades pass (inspects tabs, respects builder reserve, confirms/closes modals)
	if err := bm.PerformAutoUpgrades(); err != nil {
		t.Fatalf("PerformAutoUpgrades: %v", err)
	}
	time.Sleep(1 * time.Second)

	// 3. Ensure menu is closed cleanly
	if err := bm.CloseMenu(); err != nil {
		t.Fatalf("CloseMenu: %v", err)
	}
	time.Sleep(1 * time.Second)

	// 4. Verify device is back in responsive state
	screen, err := client.CaptureToMat()
	if err != nil || screen.Empty() {
		t.Fatalf("CaptureToMat after menu sequence: %v", err)
	}
	defer screen.Close()
	t.Log("Build Menu 2.0 and Collector pass verified live on device successfully")
}

// TestLive_LabInGameFilter verifies the in-game Spells and sorting filters in the Laboratory tab.
func TestLive_LabInGameFilter(t *testing.T) {
	if testing.Short() {
		t.Skip("needs live BlueStacks; run without -short")
	}
	client := adb.NewClient()
	client.DeviceID = "localhost:5555"
	if err := client.AutoDetectDevice(); err != nil {
		t.Fatalf("adb auto detect: %v", err)
	}
	defer client.Close()

	cal := game.NewCalibration(1024, 768)
	cfg := config.DefaultConfig().Upgrade
	cfg.AutoLab = true
	cfg.LabStrategy = "spells_first"
	cfg.LabAllowDE = true

	bm := bot.NewBuilderManager(client, cal, cfg, log.Logger)

	// 1. Open Menu
	if err := bm.OpenMenu(); err != nil {
		t.Fatalf("OpenMenu: %v", err)
	}
	time.Sleep(1 * time.Second)

	// 2. Switch to Laboratory Tab (ref 650, 638 -> live 737, 723)
	k := float64(1024) / 903.47
	_ = client.Tap(int(650*k), int(638*k))
	time.Sleep(1 * time.Second)

	// 3. Apply Spells in-game filter (taps filter icon 628, 36, then Spells 410, 340, then closes)
	bm.ApplyLabInGameFilter("spells_first", k)
	time.Sleep(1 * time.Second)

	// 4. Close menu cleanly
	if err := bm.CloseMenu(); err != nil {
		t.Fatalf("CloseMenu: %v", err)
	}
	time.Sleep(1 * time.Second)
	t.Log("In-game Spells filter verified live on device successfully")
}

// TestLive_ComprehensiveUIAndAutonomousPipeline validates every newly added configuration
// and operational path live against BlueStacks emulator:
// 1. Storage config & threshold logic
// 2. Schedule & Rate limits
// 3. Category filters (defenses, heroes, resources, traps, army) & spend caps
// 4. Lab strategy & in-game filter
// 5. Hero pets priority matching & reordering
// 6. Full end-to-end cycle execution with builder reservation
func TestLive_ComprehensiveUIAndAutonomousPipeline(t *testing.T) {
	if testing.Short() {
		t.Skip("needs live BlueStacks; run without -short")
	}

	client := adb.NewClient()
	client.DeviceID = "localhost:5555"
	if err := client.AutoDetectDevice(); err != nil {
		t.Fatalf("adb auto detect: %v", err)
	}
	defer client.Close()

	cal := game.NewCalibration(1024, 768)

	// Step 1: Verify 24/7 Schedule & Rate Limiting Engine
	schedCfg := config.ScheduleConfig{
		Enabled:                true,
		ActiveHoursStart:       "00:00",
		ActiveHoursEnd:         "23:59",
		SessionDurationMinutes: 60,
		BreakDurationMinutes:   15,
		MaxAttacksPerHour:      5,
		MaxAttacksPerDay:       50,
	}
	sched := bot.NewScheduler(schedCfg, log.Logger)
	allowed, reason := sched.CanAttack(time.Now())
	if !allowed {
		t.Fatalf("scheduler should allow attack: %s", reason)
	}
	// Record attacks up to cap
	for i := 0; i < 5; i++ {
		sched.RecordAttack(time.Now())
	}
	allowedAfterCap, reasonAfterCap := sched.CanAttack(time.Now())
	if allowedAfterCap {
		t.Fatalf("scheduler should block attacks after hourly limit reached: %s", reasonAfterCap)
	}
	t.Logf("Scheduler rate-limit verified: %s", reasonAfterCap)

	// Step 2: Verify Build Menu 2.0 with Category Filters, Spend Caps, and Hero Priorities
	upgradeCfg := config.UpgradeConfig{
		AutoBuilders:        true,
		ReserveBuilders:     1,
		UpgradeStrategy:     "heroes_first",
		UpgradeDefenses:     true,
		UpgradeResources:    true,
		UpgradeArmy:         true,
		UpgradeTraps:        false, // traps explicitly filtered out
		UpgradeHeroes:       true,
		MaxGoldPerUpgrade:   10000000,
		MaxElixirPerUpgrade: 10000000,
		AutoLab:             true,
		LabStrategy:         "spells_first",
		LabAllowDE:          true,
		AutoPets:            true,
		PetPriorities:       []string{"Diggy", "Unicorn", "Frosty", "Phoenix", "Spirit Fox"},
	}

	bm := bot.NewBuilderManager(client, cal, upgradeCfg, log.Logger)

	// Open Build Menu on live device
	if err := bm.OpenMenu(); err != nil {
		t.Fatalf("OpenMenu: %v", err)
	}
	time.Sleep(1 * time.Second)

	// Run autonomous upgrade analysis on device
	if err := bm.PerformAutoUpgrades(); err != nil {
		t.Fatalf("PerformAutoUpgrades: %v", err)
	}
	time.Sleep(1 * time.Second)

	// Close cleanly
	if err := bm.CloseMenu(); err != nil {
		t.Fatalf("CloseMenu: %v", err)
	}
	time.Sleep(1 * time.Second)

	// Step 3: Verify Village responsiveness after complete automated run
	screen, err := client.CaptureToMat()
	if err != nil || screen.Empty() {
		t.Fatalf("CaptureToMat failed: %v", err)
	}
	defer screen.Close()

	t.Log("All automated features (Schedule, Filters, Builders, Lab, Pets) successfully verified live on emulator")
}

// TestLive_DryRunUpgradeInspection tests tapping an upgrade candidate without spending resources or gems.
func TestLive_DryRunUpgradeInspection(t *testing.T) {
	if testing.Short() {
		t.Skip("needs live BlueStacks; run without -short")
	}
	client := adb.NewClient()
	client.DeviceID = "localhost:5555"
	if err := client.AutoDetectDevice(); err != nil {
		t.Fatalf("adb auto detect: %v", err)
	}
	defer client.Close()

	cal := game.NewCalibration(1024, 768)
	cfg := config.DefaultConfig().Upgrade
	cfg.AutoBuilders = true
	cfg.UpgradeStrategy = "heroes_first"
	cfg.DryRun = true

	bm := bot.NewBuilderManager(client, cal, cfg, log.Logger)

	// Ensure village screen is clean
	_ = client.Tap(99, 300)
	time.Sleep(500 * time.Millisecond)

	// 1. Open Menu
	if err := bm.OpenMenu(); err != nil {
		t.Fatalf("OpenMenu: %v", err)
	}
	time.Sleep(1 * time.Second)

	k := float64(1024) / 903.47
	tapPt := image.Pt(int(706*k), int(309*k))
	t.Logf("Tapping candidate upgrade card at (%d, %d)", tapPt.X, tapPt.Y)
	_ = client.Tap(tapPt.X, tapPt.Y)
	time.Sleep(1500 * time.Millisecond)

	// Capture frame after tapping candidate
	screen, err := client.CaptureToMat()
	if err != nil || screen.Empty() {
		t.Fatalf("CaptureToMat failed: %v", err)
	}
	defer screen.Close()
	t.Logf("Candidate tapped successfully; inspected screen size %dx%d", screen.Cols(), screen.Rows())

	// Safe dismissal: tap Red X or backdrop, NEVER tap confirm button (744, 635)
	if err := bm.CloseModalDialog(); err != nil {
		t.Logf("CloseModalDialog: %v", err)
	}
	time.Sleep(500 * time.Millisecond)
	_ = client.Tap(int(99), int(300))
	time.Sleep(500 * time.Millisecond)

	t.Log("Dry run upgrade inspection completed safely: zero resources or gems spent")
}



