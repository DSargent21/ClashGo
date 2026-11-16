package main

import (
	"bytes"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/Ducky705/ClashGO/internal/config"
	"github.com/Ducky705/ClashGO/internal/paths"
)

// Validation contract: every form value the UI can send (including hand-written
// IPC calls and future UI bugs) is checked before anything is persisted or
// handed to the running engine. A rejected save must leave both config.json and
// the live Bot unchanged, so a typo cannot half-apply a settings page.

type validationState struct {
	disk []byte
	live interface{}
}

func validationHarness(t *testing.T) *App {
	t.Helper()
	a := engineHarness(t)
	// Persist the defaults so "unchanged on disk" has bytes to compare.
	if err := a.SaveFullConfig(*a.GetConfig()); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	return a
}

func snapshotValidation(t *testing.T, a *App) validationState {
	t.Helper()
	raw, err := os.ReadFile(paths.ResolveConfig("config.json"))
	if err != nil {
		t.Fatalf("read config.json: %v", err)
	}
	a.mu.Lock()
	live := a.bot.LiveConfig()
	a.mu.Unlock()
	return validationState{disk: raw, live: live}
}

func assertRejectedWithoutSideEffects(t *testing.T, a *App, label string, err error, before validationState) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s was accepted; want a visible validation error", label)
	}
	after := snapshotValidation(t, a)
	if !bytes.Equal(before.disk, after.disk) {
		t.Fatalf("%s changed config.json despite being rejected:\nbefore %s\nafter  %s", label, before.disk, after.disk)
	}
	if !reflect.DeepEqual(before.live, after.live) {
		t.Fatalf("%s changed the live engine config despite being rejected", label)
	}
}

func TestValidation_SearchFormRejectsBadValues(t *testing.T) {
	a := validationHarness(t)

	cases := []struct {
		label string
		call  func() error
	}{
		{"negative gold floor", func() error { return a.SaveConfig(-1, 0, 0, false, "", true, 30) }},
		{"floor above UI ceiling", func() error { return a.SaveConfig(50_000_000, 0, 0, false, "", true, 30) }},
		{"stall timer zero", func() error { return a.SaveConfig(0, 0, 0, false, "", true, 0) }},
		{"stall timer above UI ceiling", func() error { return a.SaveConfig(0, 0, 0, false, "", true, 601) }},
		{"strategy that is not on disk", func() error { return a.SaveConfig(0, 0, 0, false, "no_such_army.yaml", true, 30) }},
	}
	for _, c := range cases {
		before := snapshotValidation(t, a)
		assertRejectedWithoutSideEffects(t, a, c.label, c.call(), before)
	}

	// The same form with valid values still lands in disk and live config.
	if err := a.SaveConfig(510000, 520000, 5300, true, "valk_spam.yaml", true, 45); err != nil {
		t.Fatalf("valid search form rejected: %v", err)
	}
	if got := a.bot.LiveConfig().Config.Search.MinLootGold; got != 510000 {
		t.Fatalf("valid floors did not reach the engine: %d", got)
	}
}

func TestValidation_StorageFormRejectsBadValues(t *testing.T) {
	a := validationHarness(t)

	cases := []struct {
		label string
		call  func() error
	}{
		{"empty halt rule", func() error { return a.SetStorageConfig(true, true, true, "", 95, 80, false, true) }},
		{"unknown halt rule", func() error { return a.SetStorageConfig(true, true, true, "SOME", 95, 80, false, true) }},
		{"zero full threshold", func() error { return a.SetStorageConfig(true, true, true, "ANY", 0, 0, false, true) }},
		{"full threshold above 100", func() error { return a.SetStorageConfig(true, true, true, "ANY", 101, 80, false, true) }},
		{"resume at or above full", func() error { return a.SetStorageConfig(true, true, true, "ANY", 80, 80, false, true) }},
	}
	for _, c := range cases {
		before := snapshotValidation(t, a)
		assertRejectedWithoutSideEffects(t, a, c.label, c.call(), before)
	}
}

func TestValidation_ScheduleFormRejectsBadValues(t *testing.T) {
	a := validationHarness(t)

	cases := []struct {
		label string
		call  func() error
	}{
		{"empty start hour", func() error { return a.SetScheduleConfig(true, "", "21:45", 45, 15, 12, 5, true, 4, 33) }},
		{"zero hour", func() error { return a.SetScheduleConfig(true, "00:00", "00:00", 45, 15, 12, 5, true, 4, 33) }},
		{"out-of-range hour", func() error { return a.SetScheduleConfig(true, "25:00", "21:45", 45, 15, 12, 5, true, 4, 33) }},
		{"negative hour", func() error { return a.SetScheduleConfig(true, "-1:00", "21:45", 45, 15, 12, 5, true, 4, 33) }},
		{"zero session", func() error { return a.SetScheduleConfig(true, "07:15", "21:45", 0, 15, 12, 5, true, 4, 33) }},
		{"session above UI ceiling", func() error { return a.SetScheduleConfig(true, "07:15", "21:45", 500, 15, 12, 5, true, 4, 33) }},
		{"zero hourly cap", func() error { return a.SetScheduleConfig(true, "07:15", "21:45", 45, 15, 12, 5, true, 0, 33) }},
		{"zero daily cap", func() error { return a.SetScheduleConfig(true, "07:15", "21:45", 45, 15, 12, 5, true, 4, 0) }},
		{"daily cap below hourly", func() error { return a.SetScheduleConfig(true, "07:15", "21:45", 45, 15, 12, 5, true, 30, 10) }},
	}
	for _, c := range cases {
		before := snapshotValidation(t, a)
		assertRejectedWithoutSideEffects(t, a, c.label, c.call(), before)
	}

	if err := a.SetScheduleConfig(true, "07:15", "21:45", 45, 15, 12, 5, true, 4, 33); err != nil {
		t.Fatalf("valid schedule rejected: %v", err)
	}
	if got := a.bot.LiveConfig().Scheduler; got == nil || !got.Enabled || got.ActiveHoursStart != "07:15" {
		t.Fatalf("valid schedule did not reach the scheduler: %+v", got)
	}
}

func TestValidation_UpgradeFormRejectsBadValues(t *testing.T) {
	a := validationHarness(t)

	save := func(strategy, lab, wall string, pets []string, reserve, gold int) error {
		return a.SetAutoUpgradeConfig(
			true, reserve, strategy,
			true, true, true, true, true,
			gold, 0, 0,
			true, lab, true,
			true, pets,
			wall,
			false,
		)
	}
	cases := []struct {
		label string
		call  func() error
	}{
		{"empty upgrade strategy", func() error { return save("", "cheapest_first", "both", []string{"Diggy"}, 1, 0) }},
		{"unknown upgrade strategy", func() error { return save("sideways", "cheapest_first", "both", []string{"Diggy"}, 1, 0) }},
		{"empty lab strategy", func() error { return save("max", "", "both", []string{"Diggy"}, 1, 0) }},
		{"unknown lab strategy", func() error { return save("max", "fastest", "both", []string{"Diggy"}, 1, 0) }},
		{"missing pet entry", func() error { return save("max", "cheapest_first", "both", []string{"Fire Dragon"}, 1, 0) }},
		{"duplicate pet entry", func() error { return save("max", "cheapest_first", "both", []string{"Diggy", "Diggy"}, 1, 0) }},
		{"negative reserved builders", func() error { return save("max", "cheapest_first", "both", []string{"Diggy"}, -1, 0) }},
		{"reserved builders above UI ceiling", func() error { return save("max", "cheapest_first", "both", []string{"Diggy"}, 6, 0) }},
		{"negative upgrade cap", func() error { return save("max", "cheapest_first", "both", []string{"Diggy"}, 1, -5) }},
		{"unknown wall resource", func() error { return save("max", "cheapest_first", "dark", []string{"Diggy"}, 1, 0) }},
	}
	for _, c := range cases {
		before := snapshotValidation(t, a)
		assertRejectedWithoutSideEffects(t, a, c.label, c.call(), before)
	}

	// Valid queue, including the empty queue, still saves.
	if err := save("max", "cheapest_first", "both", nil, 1, 0); err != nil {
		t.Fatalf("valid empty pet queue rejected: %v", err)
	}
	if got := a.bot.LiveConfig().Upgrade.PetPriorities; len(got) != 0 {
		t.Fatalf("engine pet queue = %v, want empty", got)
	}
}

func TestValidation_StrategyOverridesRejectBadReferences(t *testing.T) {
	a := validationHarness(t)

	cases := []struct {
		label string
		call  func() error
	}{
		{"slot zero (old code clamped to 1)", func() error { return a.SetStrategyArmySlot("valk_spam.yaml", 0) }},
		{"slot above max", func() error { return a.SetStrategyArmySlot("valk_spam.yaml", 5) }},
		{"unknown strategy slot", func() error { return a.SetStrategyArmySlot("missing.yaml", 2) }},
		{"non-yaml strategy", func() error { return a.SetStrategyArmySlot("config.json", 2) }},
		{"unknown strategy auto-end", func() error { return a.SetStrategyAutoEnd("missing.yaml", true) }},
	}
	for _, c := range cases {
		before := snapshotValidation(t, a)
		assertRejectedWithoutSideEffects(t, a, c.label, c.call(), before)
	}

	// The slot only takes effect for the active strategy, which is the
	// arrangement the UI produces (army chosen, then slot picked).
	if err := a.SaveConfig(0, 0, 0, false, "valk_spam.yaml", true, 30); err != nil {
		t.Fatalf("select strategy: %v", err)
	}
	if err := a.SetStrategyArmySlot("valk_spam.yaml", 3); err != nil {
		t.Fatalf("valid slot rejected: %v", err)
	}
	if got := a.bot.LiveConfig().ArmySlot; got != 3 {
		t.Fatalf("valid slot did not reach the engine: %d", got)
	}
}

func TestValidation_FullConfigAndWizardRejectBadValues(t *testing.T) {
	a := validationHarness(t)

	bad := func(mutate func(*config.BotConfig)) func() error {
		return func() error {
			cfg := *a.GetConfig()
			mutate(&cfg)
			return a.SaveFullConfig(cfg)
		}
	}
	cases := []struct {
		label string
		call  func() error
	}{
		{"full config with bad hour", bad(func(c *config.BotConfig) { c.Schedule.ActiveHoursStart = "99:99" })},
		{"full config with missing pet", bad(func(c *config.BotConfig) { c.Upgrade.PetPriorities = []string{"Dragon"} })},
		{"full config with slot nine", bad(func(c *config.BotConfig) {
			c.Attack.StrategySlots = map[string]int{"valk_spam.yaml": 9}
		})},
		{"full config with zero stall", bad(func(c *config.BotConfig) { c.Attack.StallTimerSeconds = 0 })},
		{"full config with negative floor", bad(func(c *config.BotConfig) { c.Search.MinLootGold = -1 })},
		{"full config with empty halt rule", bad(func(c *config.BotConfig) { c.Storage.HaltRule = "" })},
		{"wizard loot above ceiling", func() error { return a.SaveLaunchSettings(20_000_000, 0, 0) }},
		{"wizard with empty lab strategy", func() error {
			return a.SaveUpgradeSetup(true, "max", true, true, true, true, true, true, true, "", true)
		}},
	}
	for _, c := range cases {
		before := snapshotValidation(t, a)
		assertRejectedWithoutSideEffects(t, a, c.label, c.call(), before)
	}

	// A valid full-config save still reaches disk and the engine.
	cfg := *a.GetConfig()
	cfg.Search.MinLootGold = 321000
	if err := a.SaveFullConfig(cfg); err != nil {
		t.Fatalf("valid full config rejected: %v", err)
	}
	if got := a.bot.LiveConfig().Config.Search.MinLootGold; got != 321000 {
		t.Fatalf("valid full config did not reach the engine: %d", got)
	}
}

// TestValidation_FreshInvalidInputsStillLeaveStateUntouched covers invalid
// values outside the main table: a launch-screen slot outside the recipe grid
// and a wizard save that would persist a hand-edited duplicate pet queue.
func TestValidation_FreshInvalidInputsStillLeaveStateUntouched(t *testing.T) {
	a := validationHarness(t)

	before := snapshotValidation(t, a)
	assertRejectedWithoutSideEffects(t, a, "launch army slot 5",
		a.ChooseArmy("strategies/valk_spam.yaml", true, 5), before)

	before = snapshotValidation(t, a)
	assertRejectedWithoutSideEffects(t, a, "launch army slot zero for a real army",
		a.ChooseArmy("strategies/valk_spam.yaml", true, -1), before)

	// A hand-edited config with a duplicated pet must not be re-saved by the
	// wizard as if it were valid.
	dup := *a.GetConfig()
	dup.Upgrade.PetPriorities = []string{"Diggy", "Diggy"}
	if err := writeConfigFile(&dup); err != nil {
		t.Fatalf("seed duplicate pet config: %v", err)
	}
	a.bot.UpdateConfig(&dup)
	before = snapshotValidation(t, a)
	assertRejectedWithoutSideEffects(t, a, "wizard save with duplicate pet queue",
		a.SaveUpgradeSetup(true, "max", true, true, true, true, true, true, true, "troops_first", true), before)

	// Single-digit hours parse in some Go layouts but not the UI's HH:MM.
	before = snapshotValidation(t, a)
	assertRejectedWithoutSideEffects(t, a, "schedule with single-digit hour",
		a.SetScheduleConfig(true, "7:00", "21:45", 45, 15, 12, 5, true, 4, 33), before)
}

func TestValidation_StartBotRejectsBadFloors(t *testing.T) {
	t.Setenv("CLASHGO_CONFIG_DIR", t.TempDir())
	if err := paths.SetProfile(""); err != nil {
		t.Fatalf("reset profile: %v", err)
	}
	t.Cleanup(func() { _ = paths.SetProfile("") })
	a := NewApp()
	if err := writeConfigFile(config.DefaultConfig()); err != nil {
		t.Fatalf("seed config: %v", err)
	}

	raw, err := os.ReadFile(paths.ResolveConfig("config.json"))
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	before := validationState{disk: raw}
	status := a.StartBot(-1, 0, 0, false, true)
	if status.Running {
		t.Fatal("StartBot accepted negative floors and started")
	}
	if !strings.Contains(strings.ToLower(status.Message), "loot floor") {
		t.Fatalf("StartBot refusal message = %q, want a loot-floor error", status.Message)
	}
	if a.IsRunning() {
		t.Fatal("rejected start left the running state set")
	}
	a.mu.Lock()
	cancel := a.cancel
	a.mu.Unlock()
	if cancel != nil {
		t.Fatal("rejected start left a boot placeholder behind")
	}
	after, err := os.ReadFile(paths.ResolveConfig("config.json"))
	if err != nil {
		t.Fatalf("read config after: %v", err)
	}
	if !bytes.Equal(before.disk, after) {
		t.Fatal("rejected StartBot changed config.json")
	}
}
