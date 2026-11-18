package main

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/Ducky705/ClashGO/internal/accounts"
	"github.com/Ducky705/ClashGO/internal/autotune"
	"github.com/Ducky705/ClashGO/internal/bot"
	"github.com/Ducky705/ClashGO/internal/config"
	"github.com/Ducky705/ClashGO/internal/game"
	"github.com/Ducky705/ClashGO/internal/paths"
	"github.com/rs/zerolog"
)

// These tests are the control -> engine proof. Each one calls the real *App
// binding the React control calls, then asserts the consequence where the
// running loop reads it: the Bot's live config (b.cfg), the scheduler copy the
// gate consults, the builder manager's upgrade filters, the persisted
// config.json the engine reloads, and the pure predicates (storage halt,
// schedule gate) fed the persisted values. No IPC stub is involved.

// engineHarness points every path helper at a temp tree and gives the App a
// real headless Bot: the same Bot type, config pointer and config-owning
// sub-managers a booted run has, minus the device.
func engineHarness(t *testing.T) *App {
	t.Helper()
	t.Setenv("CLASHGO_CONFIG_DIR", t.TempDir())
	if err := paths.SetProfile(""); err != nil {
		t.Fatalf("reset profile: %v", err)
	}
	t.Cleanup(func() { _ = paths.SetProfile("") })

	a := NewApp()
	a.bot = bot.NewHeadlessBot(config.LoadOrDefault("config.json"))
	t.Cleanup(func() { a.bot = nil })
	return a
}

func diskConfig(t *testing.T) *config.BotConfig {
	t.Helper()
	return config.LoadOrDefault("config.json")
}

// --- Attack & loot -------------------------------------------------------

func TestEngine_SearchAndAttackControls(t *testing.T) {
	a := engineHarness(t)

	if err := a.SaveConfig(510000, 520000, 5300, true, "valk_spam.yaml", false, 77, false, 0, 0, 0); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}

	live := a.bot.LiveConfig()
	if live.Config.Search.MinLootGold != 510000 ||
		live.Config.Search.MinLootElixir != 520000 ||
		live.Config.Search.MinLootDarkElixir != 5300 {
		t.Fatalf("live search floors = %+v, want the values just saved", live.Config.Search)
	}
	if live.Config.Search.Enabled {
		t.Fatal("live engine still has search enabled after the toggle was saved off")
	}
	if live.Config.Attack.StrategyFile != "valk_spam.yaml" || live.Config.Attack.StallTimerSeconds != 77 {
		t.Fatalf("live attack config = %+v, want valk_spam.yaml / 77s", live.Config.Attack)
	}
	if !live.Config.Upgrade.UpgradeWalls {
		t.Fatal("live engine did not receive upgrade_walls from the same save")
	}

	// Switching the strategy re-arms the recipe slot the train menu will tap.
	// valk_spam.yaml authors slot 4; resolveArmySlot is what the bot reads.
	if live.ArmySlot != 4 {
		t.Fatalf("live army slot = %d, want valk_spam.yaml's 4", live.ArmySlot)
	}

	disk := diskConfig(t)
	if disk.Search.MinLootGold != 510000 || disk.Search.Enabled ||
		disk.Attack.StrategyFile != "valk_spam.yaml" || !disk.Upgrade.UpgradeWalls {
		t.Fatalf("config.json did not persist the attack tab: %+v %+v", disk.Search, disk.Attack)
	}

	// A field the attack tab never touches must not be rewritten by it.
	if live.Scheduler == nil || live.Scheduler.MaxAttacksPerDay != 50 || live.Scheduler.SessionDurationMinutes != 75 {
		t.Fatalf("attack save leaked into the scheduler: %+v", live.Scheduler)
	}
	if live.Upgrade == nil || live.Upgrade.UpgradeStrategy != "heroes_first" {
		t.Fatalf("attack save leaked into upgrade filters: %+v", live.Upgrade)
	}
}

// --- Storage halt -------------------------------------------------------

func TestEngine_StorageControlsDriveHaltPredicate(t *testing.T) {
	a := engineHarness(t)

	if err := a.SetStorageConfig(false, true, true, "ALL", 91, 85, false, true); err != nil {
		t.Fatalf("SetStorageConfig: %v", err)
	}
	live := a.bot.LiveConfig()
	st := live.Config.Storage
	if st.HaltWhenGoldFull || !st.HaltWhenElixirFull || !st.HaltWhenDEFull ||
		st.HaltRule != "ALL" || st.FullThresholdPercent != 91 ||
		st.ResumeThresholdPercent != 85 || st.DumpToWallsBeforeHalt || !st.AutoCollect {
		t.Fatalf("live storage config = %+v, want the saved toggles", st)
	}

	// The loop's own halt predicate, fed the persisted fields.
	fill := game.VillageResources{GoldFillPercent: 99, ElixirFillPercent: 99, DEFillPercent: 50}
	if fill.IsFull(st.HaltWhenGoldFull, st.HaltWhenElixirFull, st.HaltWhenDEFull, st.HaltRule, st.FullThresholdPercent) {
		t.Fatal("ALL rule halted with only two storages full")
	}
	fill.DEFillPercent = 96
	if !fill.IsFull(st.HaltWhenGoldFull, st.HaltWhenElixirFull, st.HaltWhenDEFull, st.HaltRule, st.FullThresholdPercent) {
		t.Fatal("ALL rule did not halt with all three enabled storages full")
	}

	// Flipping the rule to ANY through the binding changes the same predicate.
	if err := a.SetStorageConfig(true, false, false, "ANY", 91, 85, true, false); err != nil {
		t.Fatalf("SetStorageConfig ANY: %v", err)
	}
	st = a.bot.LiveConfig().Config.Storage
	goldOnly := game.VillageResources{GoldFillPercent: 92, ElixirFillPercent: 0, DEFillPercent: 0}
	if !goldOnly.IsFull(st.HaltWhenGoldFull, st.HaltWhenElixirFull, st.HaltWhenDEFull, st.HaltRule, st.FullThresholdPercent) {
		t.Fatal("ANY rule did not halt on a full gold storage")
	}
	if !st.DumpToWallsBeforeHalt || st.AutoCollect {
		t.Fatalf("wall dump / auto-collect toggles did not reach the engine: %+v", st)
	}
}

// --- Schedule -----------------------------------------------------------

func TestEngine_ScheduleControlsDriveCanAttackGate(t *testing.T) {
	a := engineHarness(t)

	if err := a.SetScheduleConfig(true, "07:15", "21:45", 45, 15, 12, 5, true, 2, 33); err != nil {
		t.Fatalf("SetScheduleConfig: %v", err)
	}
	live := a.bot.LiveConfig()
	if live.Scheduler == nil {
		t.Fatal("headless bot has no scheduler; the gate would never see the schedule")
	}
	// The scheduler gate reads its own copy, not config.json.
	if !live.Scheduler.Enabled || live.Scheduler.ActiveHoursStart != "07:15" ||
		live.Scheduler.ActiveHoursEnd != "21:45" ||
		live.Scheduler.SessionDurationMinutes != 45 ||
		live.Scheduler.BreakDurationMinutes != 12 ||
		!live.Scheduler.ForceStopOnBreak ||
		live.Scheduler.MaxAttacksPerHour != 2 || live.Scheduler.MaxAttacksPerDay != 33 {
		t.Fatalf("scheduler copy = %+v, want the saved schedule", live.Scheduler)
	}

	// Fixed timestamps far in the past keep session expiry out of the way
	// (the session end is wall-clock "now + 45m").
	sched := bot.NewScheduler(live.Config.Schedule, zerolog.Nop())
	outside := time.Date(2020, 1, 1, 6, 30, 0, 0, time.Local)
	if allowed, reason := sched.CanAttack(outside); allowed || !strings.Contains(reason, "outside active hours (07:15 - 21:45") {
		t.Fatalf("06:30 gate = %v %q, want the saved 07:15-21:45 window to refuse", allowed, reason)
	}
	inside := time.Date(2020, 1, 1, 12, 0, 0, 0, time.Local)
	if allowed, reason := sched.CanAttack(inside); !allowed {
		t.Fatalf("12:00 gate refused inside the saved window: %q", reason)
	}

	// The saved hourly cap is what the gate enforces.
	sched.RecordAttack(inside)
	sched.RecordAttack(inside)
	if allowed, reason := sched.CanAttack(inside); allowed || !strings.Contains(reason, "hourly attack cap reached (2/2") {
		t.Fatalf("hourly cap gate = %v %q, want the saved cap of 2", allowed, reason)
	}

	disk := diskConfig(t)
	if !disk.Schedule.Enabled || disk.Schedule.MaxAttacksPerDay != 33 || disk.Schedule.MaxAttacksPerHour != 2 {
		t.Fatalf("config.json schedule = %+v", disk.Schedule)
	}
}

// --- Builders / lab / pets ----------------------------------------------

func TestEngine_UpgradeAndPetControlsReachBuilderManager(t *testing.T) {
	a := engineHarness(t)

	save := func(pets []string, dryRun bool) {
		t.Helper()
		if err := a.SetAutoUpgradeConfig(
			true, 2, "max",
			false, true, false, true, true,
			6000000, 7000000, 90000,
			true, "spells_first", false,
			true, pets,
			"elixir",
			dryRun,
		); err != nil {
			t.Fatalf("SetAutoUpgradeConfig: %v", err)
		}
	}
	save([]string{"Frosty", "Diggy"}, true)

	live := a.bot.LiveConfig()
	if live.Upgrade == nil {
		t.Fatal("headless bot has no builder manager; upgrade filters would be unread")
	}
	u := live.Upgrade
	if !u.AutoBuilders || u.ReserveBuilders != 2 || u.UpgradeStrategy != "max" ||
		u.UpgradeDefenses || !u.UpgradeResources || u.UpgradeArmy ||
		!u.UpgradeTraps || !u.UpgradeHeroes ||
		u.MaxGoldPerUpgrade != 6000000 || u.MaxElixirPerUpgrade != 7000000 ||
		u.MaxDEPerUpgrade != 90000 {
		t.Fatalf("builder filters = %+v, want the saved builder tab", u)
	}
	if !u.AutoLab || u.LabStrategy != "spells_first" || u.LabAllowDE {
		t.Fatalf("lab filters = %+v, want spells_first with DE blocked", u)
	}
	if !u.AutoPets || len(u.PetPriorities) != 2 || u.PetPriorities[0] != "Frosty" {
		t.Fatalf("pet queue = %v, want Frosty then Diggy", u.PetPriorities)
	}
	if u.WallResource != "elixir" || !u.DryRun {
		t.Fatalf("wall resource / dry run = %q/%v", u.WallResource, u.DryRun)
	}

	// Reorder, then clear: the manager's queue has to follow, since that is
	// what selectHeroPet iterates.
	save([]string{"Diggy", "Frosty"}, false)
	if got := a.bot.LiveConfig().Upgrade.PetPriorities; len(got) != 2 || got[0] != "Diggy" {
		t.Fatalf("reordered pet queue = %v, want Diggy first", got)
	}
	save([]string{}, false)
	if got := a.bot.LiveConfig().Upgrade.PetPriorities; len(got) != 0 {
		t.Fatalf("cleared pet queue = %v, want empty", got)
	}
	if a.bot.LiveConfig().Upgrade.DryRun {
		t.Fatal("dry run switch did not reach the builder manager")
	}

	// Reserve fields are persisted for compatibility but no longer written by
	// the UI: they keep their stored values instead of being reset to 0.
	disk := diskConfig(t)
	if disk.Upgrade.WallMinGoldReserve != 2000000 || disk.Upgrade.WallMinElixirReserve != 2000000 {
		t.Fatalf("reserve fields changed on save: %+v", disk.Upgrade)
	}
}

// --- Army slot & auto end ----------------------------------------------

func TestEngine_ArmySlotAndAutoEndReachEngine(t *testing.T) {
	a := engineHarness(t)

	if err := a.SaveConfig(400000, 400000, 2000, false, "valk_spam.yaml", true, 30, false, 0, 0, 0); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	if err := a.SetStrategyArmySlot("assets/strategies/valk_spam.yaml", 3); err != nil {
		t.Fatalf("SetStrategyArmySlot: %v", err)
	}
	if got := a.bot.LiveConfig().ArmySlot; got != 3 {
		t.Fatalf("live army slot = %d, want 3", got)
	}
	plans := a.GetArmyPlans()
	var active *ArmyPlan
	for i := range plans {
		if plans[i].Active {
			active = &plans[i]
			break
		}
	}
	if active == nil {
		t.Fatal("no active army plan after selecting the strategy")
	}
	if active.File != "valk_spam.yaml" || active.ArmySlot != 3 {
		t.Fatalf("active plan = %s slot %d, want valk_spam.yaml slot 3", active.File, active.ArmySlot)
	}
	if diskConfig(t).Attack.StrategySlots["valk_spam.yaml"] != 3 {
		t.Fatal("army slot did not persist to config.json")
	}

	// Auto end is per strategy and the report the UI renders must agree with
	// the config the attack executor reads.
	if err := a.SetStrategyAutoEnd("assets/strategies/valk_spam.yaml", false); err != nil {
		t.Fatalf("SetStrategyAutoEnd: %v", err)
	}
	if a.bot.LiveConfig().Config.Attack.StrategyAutoEnd["valk_spam.yaml"] {
		t.Fatal("live attack config still has auto end enabled")
	}
	for _, plan := range a.GetArmyPlans() {
		if plan.Active && plan.EndAtPercent != 0 {
			t.Fatalf("active plan still ends at %d%%, want 0", plan.EndAtPercent)
		}
	}
	if diskConfig(t).Attack.StrategyAutoEnd["valk_spam.yaml"] {
		t.Fatal("auto end did not persist to config.json")
	}
}

// --- Loot autotune ------------------------------------------------------

func TestEngine_AutoLootControlsReachEngine(t *testing.T) {
	a := engineHarness(t)

	if err := a.SetAutoLoot(true); err != nil {
		t.Fatalf("SetAutoLoot: %v", err)
	}
	if !a.bot.LiveConfig().Config.Search.AutoLoot {
		t.Fatal("auto loot toggle did not reach the live search config")
	}

	// Enough readable bases for the recommender to be ready.
	store := autotune.Open(paths.ResolveConfig("search_samples.jsonl"))
	for i := 0; i < 60; i++ {
		if err := store.Append(autotune.NewSample(time.Unix(0, 0), 2000000, 2000000, 200000, i%2 == 0, 2*time.Second)); err != nil {
			t.Fatalf("seed sample: %v", err)
		}
	}
	before := a.bot.LiveConfig().Config.Search
	status, err := a.ApplyLootAutotune()
	if err != nil {
		t.Fatalf("ApplyLootAutotune: %v", err)
	}
	live := a.bot.LiveConfig().Config.Search
	if live.MinLootGold == before.MinLootGold && live.MinLootElixir == before.MinLootElixir {
		t.Fatal("applying the recommendation did not move the live floors")
	}
	disk := diskConfig(t)
	if disk.Search.MinLootGold != live.MinLootGold || disk.Search.MinLootElixir != live.MinLootElixir ||
		disk.Search.MinLootDarkElixir != live.MinLootDarkElixir {
		t.Fatalf("config.json %+v and live %+v thresholds disagree after apply", disk.Search, live)
	}

	// The card shows the recommendation the binding returned; clicking Apply
	// again has to install exactly that number, not a different recomputation.
	// (The blend is intentionally gradual while the sample count is low, so the
	// suggestion keeps moving between clicks instead of jumping to the optimum.)
	if _, err := a.ApplyLootAutotune(); err != nil {
		t.Fatalf("second ApplyLootAutotune: %v", err)
	}
	live = a.bot.LiveConfig().Config.Search
	if live.MinLootGold != status.Suggested.Gold || live.MinLootElixir != status.Suggested.Elixir ||
		live.MinLootDarkElixir != status.Suggested.DE {
		t.Fatalf("second apply installed %+v, want the shown suggestion %+v", live, status.Suggested)
	}
	disk = diskConfig(t)
	if disk.Search.MinLootGold != status.Suggested.Gold || disk.Search.MinLootDarkElixir != status.Suggested.DE {
		t.Fatalf("config.json %+v did not take the shown suggestion %+v", disk.Search, status.Suggested)
	}

	after, err := a.ResetLootAutotune()
	if err != nil {
		t.Fatalf("ResetLootAutotune: %v", err)
	}
	if after.Samples != 0 || after.Recorded != 0 {
		t.Fatalf("reset left %d/%d samples", after.Samples, after.Recorded)
	}
	if got := a.bot.LiveConfig().Config.Search.MinLootGold; got != live.MinLootGold {
		t.Fatalf("reset changed the live floor to %d; it must keep the last tuned value %d", got, live.MinLootGold)
	}
}

// --- Accounts -----------------------------------------------------------

func TestEngine_AccountControlsReachStoreAndEngine(t *testing.T) {
	// No bot here: switching waits for a running session to end, and a bot
	// without a device never ends one. The engine-visible effect this asserts
	// is the device binding stamped into the config the next boot loads.
	t.Setenv("CLASHGO_CONFIG_DIR", t.TempDir())
	if err := paths.SetProfile(""); err != nil {
		t.Fatalf("reset profile: %v", err)
	}
	t.Cleanup(func() { _ = paths.SetProfile("") })
	a := NewApp()

	if _, err := a.GetAccounts(); err != nil {
		t.Fatalf("GetAccounts: %v", err)
	}
	// Switching now drives the game's own Supercell ID list, so every account
	// carries a slot and the driver is faked here (a live tap sequence is
	// covered by internal/game/accountswitch_test.go).
	if _, err := a.UpdateAccount(accounts.DefaultAccountID, accounts.Input{
		Name: "Main", Device: accounts.Device{DeviceID: "localhost:5555"}, Slot: 1, Enabled: true,
	}); err != nil {
		t.Fatalf("set default slot: %v", err)
	}
	a.switchGameAccounts = func(accounts.Account) error { return nil }
	created, err := a.CreateAccount(accounts.Input{
		Name:                 "Harness",
		Device:               accounts.Device{DeviceID: "localhost:5599"},
		Slot:                 1,
		Enabled:              true,
		Priority:             9,
		MaxAttacksPerDay:     40,
		MaxAttacksPerSession: 12,
	})
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}

	updated, err := a.UpdateAccount(created.ID, accounts.Input{
		Name:                 "Harness Renamed",
		Device:               accounts.Device{DeviceID: "localhost:5599"},
		Slot:                 1,
		Enabled:              true,
		Priority:             3,
		MaxAttacksPerDay:     25,
		MaxAttacksPerSession: 6,
	})
	if err != nil {
		t.Fatalf("UpdateAccount: %v", err)
	}
	if updated.Name != "Harness Renamed" || updated.Priority != 3 ||
		updated.MaxAttacksPerDay != 25 || updated.MaxAttacksPerSession != 6 {
		t.Fatalf("UpdateAccount returned %+v, want the edited fields", updated)
	}
	state, err := a.GetAccounts()
	if err != nil {
		t.Fatalf("GetAccounts after update: %v", err)
	}
	var stored *AccountView
	for i := range state.Accounts {
		if state.Accounts[i].ID == created.ID {
			stored = &state.Accounts[i]
		}
	}
	if stored == nil || stored.Name != "Harness Renamed" || stored.Priority != 3 {
		t.Fatalf("accounts list did not keep the edit: %+v", stored)
	}
	raw, err := os.ReadFile(filepath.Join(paths.GetConfigDir(), "accounts.json"))
	if err != nil {
		t.Fatalf("read accounts.json: %v", err)
	}
	if !strings.Contains(string(raw), "Harness Renamed") {
		t.Fatal("accounts.json on disk does not contain the rename")
	}

	// Switching makes it active and stamps its device into the config the bot
	// boots with; the switch count is the usage Reset clears.
	res, err := a.SwitchAccount(created.ID, false)
	if err != nil {
		t.Fatalf("SwitchAccount: %v", err)
	}
	if !res.Account.Active || paths.Profile() != created.ID {
		t.Fatalf("switch result = %+v profile %q", res, paths.Profile())
	}
	if cfg := diskConfig(t); cfg.Device.DeviceID != "localhost:5599" {
		t.Fatalf("switched config device = %q, want localhost:5599", cfg.Device.DeviceID)
	}
	if _, err := a.SwitchAccount(accounts.DefaultAccountID, false); err != nil {
		t.Fatalf("switch back: %v", err)
	}
	state, err = a.GetAccounts()
	if err != nil {
		t.Fatalf("GetAccounts after switches: %v", err)
	}
	for i := range state.Accounts {
		if state.Accounts[i].ID == created.ID && state.Accounts[i].Usage.Switches == 0 {
			t.Fatal("switching the account did not record usage")
		}
	}
	if err := a.ResetAccountUsage(created.ID); err != nil {
		t.Fatalf("ResetAccountUsage: %v", err)
	}
	state, err = a.GetAccounts()
	if err != nil {
		t.Fatalf("GetAccounts after reset: %v", err)
	}
	for i := range state.Accounts {
		if state.Accounts[i].ID == created.ID &&
			(state.Accounts[i].Usage.Switches != 0 || state.Accounts[i].TodayAttacks != 0) {
			t.Fatalf("reset usage left %+v", state.Accounts[i].Usage)
		}
	}
}

// --- Start / stop -------------------------------------------------------

func TestEngine_StartStopLifecycle(t *testing.T) {
	t.Setenv("CLASHGO_CONFIG_DIR", t.TempDir())
	if err := paths.SetProfile(""); err != nil {
		t.Fatalf("reset profile: %v", err)
	}
	t.Cleanup(func() { _ = paths.SetProfile("") })

	// Seam the device-bound lifecycle: a real boot launches BlueStacks and the
	// game, which would spend a real army. Everything else (start placeholder,
	// config handoff, running state, stop teardown) is the production path.
	oldNew, oldStart, oldStop := newBotInstance, startBotInstance, stopBotInstance
	started := make(chan struct{}, 1)
	stopped := make(chan struct{}, 1)
	newBotInstance = func(_ context.Context, cfg *config.BotConfig) (*bot.Bot, error) {
		return bot.NewHeadlessBot(cfg), nil
	}
	startBotInstance = func(*bot.Bot) error {
		started <- struct{}{}
		return nil
	}
	stopBotInstance = func(*bot.Bot) { stopped <- struct{}{} }
	t.Cleanup(func() { newBotInstance, startBotInstance, stopBotInstance = oldNew, oldStart, oldStop })

	a := NewApp()
	status := a.StartBot(500000, 510000, 5200, true, true)
	if !status.Running {
		t.Fatalf("StartBot returned %+v, want running", status)
	}
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("StartBot never started the bot")
	}
	waitFor(t, "bot installed", func() bool { return a.IsRunning() })

	a.mu.Lock()
	live := a.bot.LiveConfig()
	a.mu.Unlock()
	if live.Config.Search.MinLootGold != 500000 || live.Config.Search.MinLootElixir != 510000 ||
		live.Config.Search.MinLootDarkElixir != 5200 || !live.Config.Search.Enabled {
		t.Fatalf("the started bot got %+v, want the thresholds from the Start form", live.Config.Search)
	}
	if !live.Config.Upgrade.UpgradeWalls {
		t.Fatal("the started bot did not get upgrade_walls from the Start form")
	}

	// A second click must not start a second session.
	if dup := a.StartBot(1, 1, 1, false, false); !dup.Running || !strings.Contains(strings.ToLower(dup.Message), "already") {
		t.Fatalf("duplicate StartBot = %+v, want an already-running refusal", dup)
	}

	stop := a.StopBot()
	if stop.Running {
		t.Fatalf("StopBot returned %+v, want stopped", stop)
	}
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("StopBot never tore the bot down")
	}
	waitFor(t, "bot cleared", func() bool { return !a.IsRunning() })
	if again := a.StopBot(); again.Running {
		t.Fatalf("second StopBot = %+v, want not running", again)
	}

	// StopBot detaches its teardown, whose last act is saveStats. Let that
	// write land before t.TempDir's cleanup removes the tree it writes into;
	// otherwise cleanup fails with "directory not empty" when it wins the race.
	waitFor(t, "stats flushed", func() bool {
		_, err := os.Stat(filepath.Join(os.Getenv("CLASHGO_CONFIG_DIR"), "stats.json"))
		return err == nil
	})
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// --- Static contract: UI-written fields must have an engine reader --------

// TestEngine_EveryUIWrittenFieldHasEngineReader guards the class of defect the
// reserve fields were: a binding writes cfg.Section.Field, the UI round-trips
// it, and no engine loop ever reads it. Bindings are the only allowed writers,
// so every field they assign must be referenced by a non-test file under
// internal/ (excluding internal/config itself, which only defines the struct).
func TestEngine_EveryUIWrittenFieldHasEngineReader(t *testing.T) {
	assign := regexp.MustCompile(`\bcfg\.([A-Z][A-Za-z0-9_]*)\.([A-Z][A-Za-z0-9_]*)\s*=`)
	written := map[string]string{}
	files, err := filepath.Glob("app*.go")
	if err != nil {
		t.Fatalf("glob bindings: %v", err)
	}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		for _, m := range assign.FindAllStringSubmatch(string(raw), -1) {
			written[m[1]+"."+m[2]] = f
		}
	}
	if len(written) == 0 {
		t.Fatal("no cfg.Section.Field assignments found in the bindings; the guard is not scanning")
	}

	readers := map[string]bool{}
	fieldRef := regexp.MustCompile(`\.([A-Z][A-Za-z0-9_]*)\b`)
	err = filepath.WalkDir("internal", func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path == filepath.Join("internal", "config") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, m := range fieldRef.FindAllStringSubmatch(string(raw), -1) {
			readers[m[1]] = true
		}
		return nil
	})
	if err != nil {
		t.Fatalf("scan internal: %v", err)
	}

	for key, file := range written {
		field := key[strings.Index(key, ".")+1:]
		if !readers[field] {
			t.Errorf("%s is written by %s but no engine package reads it; wire it up or remove the control", key, file)
		}
	}
}
