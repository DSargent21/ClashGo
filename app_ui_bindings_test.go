package main

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/Ducky705/ClashGO/internal/bot"
	"github.com/Ducky705/ClashGO/internal/config"
)

// This file is the UI contract: every control the React app renders calls one
// of the methods below. A control whose binding was renamed, whose value is
// dropped on write, or whose save path is clobbered by a sibling binding is a
// dead button in the packaged app, and none of that is visible to `tsc`.

// TestUI_PetQueueCanBeCleared is the regression for the len>0 guard in
// SetAutoUpgradeConfig: emptying the queue in Config > Lab & pets saved
// "successfully" and the old queue came straight back on the next GetConfig.
func TestUI_PetQueueCanBeCleared(t *testing.T) {
	t.Setenv("CLASHGO_CONFIG_DIR", t.TempDir())
	a := NewApp()

	save := func(pets []string) {
		t.Helper()
		if err := a.SetAutoUpgradeConfig(
			true, 1, "heroes_first",
			true, true, true, true, true,
			0, 0, 0,
			true, "cheapest_first", true,
			true, pets,
			"both",
			false,
		); err != nil {
			t.Fatalf("SetAutoUpgradeConfig(%v): %v", pets, err)
		}
	}

	save([]string{"Diggy", "Phoenix"})
	if got := a.GetConfig().Upgrade.PetPriorities; len(got) != 2 {
		t.Fatalf("queued pets = %v, want Diggy/Phoenix", got)
	}

	// Remove every pet, exactly what the ConfigView remove buttons do.
	save([]string{})
	if got := a.GetConfig().Upgrade.PetPriorities; len(got) != 0 {
		t.Fatalf("emptied pet queue came back as %v; the remove buttons do not stick", got)
	}

	// A nil slice (JSON null from an older client) must clear too, not panic.
	save(nil)
	if got := a.GetConfig().Upgrade.PetPriorities; len(got) != 0 {
		t.Fatalf("nil pet queue came back as %v", got)
	}
}

// TestUI_ConfigFormSaveOrder covers ConfigView's single Save button, which
// issues four bindings in a fixed order. SaveConfig writes upgrade_walls, so a
// later binding that rewrote it would silently undo the Storage tab's
// "Auto upgrade walls" switch.
func TestUI_ConfigFormSaveOrder(t *testing.T) {
	t.Setenv("CLASHGO_CONFIG_DIR", t.TempDir())
	a := NewApp()

	// Same order and shape as App.saveSettings.
	if err := a.SaveConfig(400000, 450000, 2500, true, "valk_spam.yaml", true, 45); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	if err := a.SetStorageConfig(true, true, false, "ALL", 88, 80, true, true); err != nil {
		t.Fatalf("SetStorageConfig: %v", err)
	}
	if err := a.SetScheduleConfig(true, "09:30", "22:15", 75, 15, 25, 5, false, 9, 55); err != nil {
		t.Fatalf("SetScheduleConfig: %v", err)
	}
	if err := a.SetAutoUpgradeConfig(
		true, 2, "max",
		false, true, false, true, true,
		6000000, 7000000, 90000,
		true, "spells_first", false,
		true, []string{"Sneezy", "Frosty"},
		"elixir",
		true,
	); err != nil {
		t.Fatalf("SetAutoUpgradeConfig: %v", err)
	}

	cfg := a.GetConfig()
	if cfg.Search.MinLootGold != 400000 || cfg.Search.MinLootElixir != 450000 ||
		cfg.Search.MinLootDarkElixir != 2500 || !cfg.Search.Enabled ||
		cfg.Attack.StallTimerSeconds != 45 || cfg.Attack.StrategyFile != "valk_spam.yaml" {
		t.Fatalf("attack/search tab lost values: %+v %+v", cfg.Search, cfg.Attack)
	}
	if !cfg.Upgrade.UpgradeWalls || !cfg.Storage.DumpToWallsBeforeHalt {
		t.Fatalf("wall switches were clobbered by a later binding: walls=%v dump=%v",
			cfg.Upgrade.UpgradeWalls, cfg.Storage.DumpToWallsBeforeHalt)
	}
	if cfg.Storage.HaltRule != "ALL" || cfg.Storage.FullThresholdPercent != 88 ||
		!cfg.Storage.HaltWhenGoldFull || !cfg.Storage.HaltWhenElixirFull ||
		cfg.Storage.HaltWhenDEFull || !cfg.Storage.AutoCollect {
		t.Fatalf("storage tab lost values: %+v", cfg.Storage)
	}
	if !cfg.Schedule.Enabled || cfg.Schedule.ActiveHoursStart != "09:30" ||
		cfg.Schedule.ActiveHoursEnd != "22:15" || cfg.Schedule.SessionDurationMinutes != 75 ||
		cfg.Schedule.BreakDurationMinutes != 25 || cfg.Schedule.ForceStopOnBreak ||
		cfg.Schedule.MaxAttacksPerHour != 9 || cfg.Schedule.MaxAttacksPerDay != 55 {
		t.Fatalf("schedule tab lost values: %+v", cfg.Schedule)
	}
	if !cfg.Upgrade.AutoBuilders || cfg.Upgrade.ReserveBuilders != 2 ||
		cfg.Upgrade.UpgradeStrategy != "max" || cfg.Upgrade.UpgradeDefenses ||
		!cfg.Upgrade.UpgradeResources || cfg.Upgrade.UpgradeArmy ||
		!cfg.Upgrade.UpgradeTraps || !cfg.Upgrade.UpgradeHeroes ||
		cfg.Upgrade.MaxGoldPerUpgrade != 6000000 || cfg.Upgrade.MaxElixirPerUpgrade != 7000000 ||
		cfg.Upgrade.MaxDEPerUpgrade != 90000 {
		t.Fatalf("builders tab lost values: %+v", cfg.Upgrade)
	}
	if !cfg.Upgrade.AutoLab || cfg.Upgrade.LabStrategy != "spells_first" || cfg.Upgrade.LabAllowDE {
		t.Fatalf("lab tab lost values: %+v", cfg.Upgrade)
	}
	if !cfg.Upgrade.AutoPets || len(cfg.Upgrade.PetPriorities) != 2 || cfg.Upgrade.PetPriorities[0] != "Sneezy" {
		t.Fatalf("pet tab lost values: %+v", cfg.Upgrade.PetPriorities)
	}
	if cfg.Upgrade.WallResource != "elixir" {
		t.Fatalf("wall resource = %q, want elixir", cfg.Upgrade.WallResource)
	}
	if !cfg.Upgrade.DryRun {
		t.Fatal("dry run switch did not persist")
	}
}

// TestUI_DeviceTestTools covers the Settings > Testing buttons without a
// device: an unknown action must be rejected, and the guard must refuse while
// the bot owns the device instead of tapping over a live run.
func TestUI_DeviceTestTools(t *testing.T) {
	t.Setenv("CLASHGO_CONFIG_DIR", t.TempDir())
	a := NewApp()

	if err := a.TestDeviceMenu("bogus"); err == nil {
		t.Fatal("unknown action accepted; the button could tap the wrong menu")
	}

	a.bot = &bot.Bot{}
	a.cancel = func() {}
	t.Cleanup(func() { a.bot = nil; a.cancel = nil })
	for _, action := range []string{"open", "filters", "close"} {
		err := a.TestDeviceMenu(action)
		if err == nil || !strings.Contains(err.Error(), "stop the bot") {
			t.Fatalf("%s while running returned %v, want the stop-the-bot refusal", action, err)
		}
	}
}

// TestUI_LaunchWizardBindings covers the setup wizard's Start path, which
// writes upgrades and loot floors through two separate bindings.
func TestUI_LaunchWizardBindings(t *testing.T) {
	t.Setenv("CLASHGO_CONFIG_DIR", t.TempDir())
	a := NewApp()

	if err := a.SaveLaunchSettings(-1, 0, 0); err == nil {
		t.Fatal("negative loot floor accepted")
	}

	if err := a.SaveUpgradeSetup(
		true, "strategic_rush",
		true, true, false, true, true,
		true, true, "troops_first", true,
	); err != nil {
		t.Fatalf("SaveUpgradeSetup: %v", err)
	}
	if err := a.SaveLaunchSettings(123000, 456000, 7890); err != nil {
		t.Fatalf("SaveLaunchSettings: %v", err)
	}
	cfg := a.GetConfig()
	if !cfg.Upgrade.AutoBuilders || cfg.Upgrade.UpgradeStrategy != "strategic_rush" ||
		cfg.Upgrade.UpgradeDefenses || !cfg.Upgrade.UpgradeHeroes ||
		!cfg.Upgrade.AutoLab || cfg.Upgrade.LabStrategy != "troops_first" ||
		!cfg.Upgrade.AutoPets || !cfg.Upgrade.UpgradeWalls || cfg.Upgrade.ReserveBuilders != 1 {
		t.Fatalf("wizard upgrades did not persist: %+v", cfg.Upgrade)
	}
	if cfg.Search.MinLootGold != 123000 || cfg.Search.MinLootElixir != 456000 ||
		cfg.Search.MinLootDarkElixir != 7890 {
		t.Fatalf("wizard loot floors did not persist: %+v", cfg.Search)
	}
	if len(cfg.Upgrade.PetPriorities) == 0 {
		t.Fatal("wizard left an empty pet queue with auto pets on")
	}
}

// TestUI_BindingsExistForUITests is the static half of the audit: every method
// the React tree imports from the generated Wails bindings must exist on *App,
// and every generated export must still be a real method. A rename on either
// side fails here instead of silently disabling a control in the packaged app.
func TestUI_BindingsExistForUITests(t *testing.T) {
	declared := generatedBindingNames(t)
	if len(declared) == 0 {
		t.Fatal("parsed no functions out of web/wailsjs/go/main/App.d.ts")
	}

	appType := reflect.TypeOf(&App{})
	methods := map[string]bool{}
	for i := 0; i < appType.NumMethod(); i++ {
		methods[appType.Method(i).Name] = true
	}

	var missing []string
	for name := range declared {
		if !methods[name] {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Fatalf("web/wailsjs exports methods *App does not have (stale bindings): %v", missing)
	}

	// And the other direction: a UI import that the generated file no longer
	// exports means the click handler will throw at call time.
	used := uiImportedBindingNames(t)
	if len(used) == 0 {
		t.Fatal("found no UI imports of the Wails bindings; the audit cannot pass")
	}
	for name := range used {
		if !declared[name] {
			t.Errorf("web/src imports %s from the Wails bindings, but App.d.ts does not export it", name)
		}
		if !methods[name] {
			t.Errorf("web/src calls %s, but *App has no such method", name)
		}
	}
}

func generatedBindingNames(t *testing.T) map[string]bool {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("web", "wailsjs", "go", "main", "App.d.ts"))
	if err != nil {
		t.Fatalf("read generated bindings: %v", err)
	}
	re := regexp.MustCompile(`(?m)^export function ([A-Za-z0-9_]+)\(`)
	names := map[string]bool{}
	for _, match := range re.FindAllStringSubmatch(string(raw), -1) {
		names[match[1]] = true
	}
	return names
}

func uiImportedBindingNames(t *testing.T) map[string]bool {
	t.Helper()
	importRe := regexp.MustCompile(`(?s)import\s*\{([^}]*)\}\s*from\s*'\.\./wailsjs/go/main/App'`)
	names := map[string]bool{}
	root := filepath.Join("web", "src")
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		if !strings.HasSuffix(path, ".tsx") && !strings.HasSuffix(path, ".ts") {
			return nil
		}
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		for _, match := range importRe.FindAllStringSubmatch(string(raw), -1) {
			for _, field := range strings.Split(match[1], ",") {
				name := strings.TrimSpace(field)
				if name == "" || strings.Contains(name, " as ") {
					continue
				}
				names[name] = true
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk web/src: %v", err)
	}
	return names
}

// TestUI_ReserveFieldsAreNotRendered keeps the UI honest: the wall loop cannot
// read absolute loot, so the persisted reserve fields must not be offered as if
// they worked. Re-expose them only together with enforcement.
func TestUI_ReserveFieldsAreNotRendered(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("web", "src", "components", "ConfigView.tsx"))
	if err != nil {
		t.Fatalf("read ConfigView: %v", err)
	}
	if strings.Contains(string(raw), "Min gold reserve") || strings.Contains(string(raw), "Min elixir reserve") {
		t.Fatal("ConfigView renders wall reserve inputs the bot never reads")
	}
	// The fields stay in the persisted config for compatibility.
	cfg := config.DefaultConfig()
	if cfg.Upgrade.WallMinGoldReserve == 0 && cfg.Upgrade.WallMinElixirReserve == 0 {
		t.Fatal("wall reserve defaults disappeared from the config contract")
	}
}

// TestUI_PlanAndWallControlsHaveOneHome enforces D-9. Plan-level controls
// belong to Army (the page that lists plans); a second copy on Config drifts
// from the actual plan and mislabels it (the removed switch claimed 50% while
// the plan carried its own percent).
func TestUI_PlanAndWallControlsHaveOneHome(t *testing.T) {
	read := func(name string) string {
		raw, err := os.ReadFile(filepath.Join("web", "src", "components", name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		return string(raw)
	}
	configView := read("ConfigView.tsx")
	armyView := read("ArmyView.tsx")

	for _, needle := range []string{"Auto-end battle", "Attack strategy", "onSetArmySlot", "onSetAutoEnd"} {
		if strings.Contains(configView, needle) {
			t.Errorf("ConfigView renders %q; per-plan controls belong to ArmyView", needle)
		}
	}
	for _, needle := range []string{"armySlot", "onSetAutoEnd", "Auto-end battle"} {
		if !strings.Contains(armyView, needle) {
			t.Errorf("ArmyView lost %q; the active plan must expose it", needle)
		}
	}
	if strings.Count(configView, "ariaLabel=\"Auto upgrade walls\"") != 1 {
		t.Error("ConfigView must render exactly one Auto upgrade walls switch")
	}
	for _, needle := range []string{"wall-resource", "resumeThreshold"} {
		if !strings.Contains(configView, needle) {
			t.Errorf("ConfigView lost the %q control", needle)
		}
	}
}

// TestUI_ConfigSectionsEditableWhenOff enforces the honesty contract for
// master switches: a switch turns the engine feature on/off, it must not wall
// off its own form. Every input in Config stays editable and is written by the
// Save button; the switch alone decides whether the engine acts on it.
func TestUI_ConfigSectionsEditableWhenOff(t *testing.T) {
	read := func(name string) string {
		raw, err := os.ReadFile(filepath.Join("web", "src", name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		return string(raw)
	}
	configView := read(filepath.Join("components", "ConfigView.tsx"))
	app := read("App.tsx")

	forbidden := []string{
		"is-disabled",
		"pointer-events",
		"disabled={!searchEnabled}",
		"disabled={!autoBuilders}",
		"disabled={!autoLab}",
		"disabled={!autoPets}",
		"disabled={!upgradeWalls",
	}
	for _, needle := range forbidden {
		if strings.Contains(configView, needle) {
			t.Errorf("ConfigView gates its form with %q; controls must stay editable and save regardless of the master switch", needle)
		}
	}

	// The values the old gates used to lock must reach the bindings, including
	// the resume/jitter fields that were hardcoded before T.6.
	for _, needle := range []string{"resumeThreshold", "sessionJitter", "breakJitter"} {
		if !strings.Contains(app, needle) {
			t.Errorf("App save path lost %q", needle)
		}
	}
}

// TestUI_AnalyticsStatesWindow enforces D-9's companion rule: every number,
// chart and table names its time window. Session metrics reset at launch;
// attack activity reads the saved raid history. A card without a window label
// is ambiguous by definition.
func TestUI_AnalyticsStatesWindow(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("web", "src", "components", "Analytics.tsx"))
	if err != nil {
		t.Fatalf("read Analytics: %v", err)
	}
	analytics := string(raw)

	for _, needle := range []string{
		"session attacks",
		"session 3★ rate",
		"session avg loot / raid",
		"session avg stars",
		"Saved history",
	} {
		if !strings.Contains(analytics, needle) {
			t.Errorf("Analytics lost the window label %q", needle)
		}
	}
	if got := strings.Count(analytics, "This session"); got < 4 {
		t.Errorf("Analytics labels only %d session surfaces, want chart + outcomes + revenue + strategies", got)
	}
	for _, ambiguous := range []string{"Session totals and per-attack averages", "Aggregated from the current report window"} {
		if strings.Contains(analytics, ambiguous) {
			t.Errorf("Analytics still uses the window-ambiguous copy %q", ambiguous)
		}
	}
}

// TestUI_ConfigSectionsPersistWhileDisabled proves the editable-before-enable
// contract at the engine boundary: values written while every master switch is
// off survive a save and a fresh app load. UI-only guards cannot see this.
func TestUI_ConfigSectionsPersistWhileDisabled(t *testing.T) {
	t.Setenv("CLASHGO_CONFIG_DIR", t.TempDir())
	a := NewApp()

	if err := a.SaveConfig(123000, 234000, 3000, false, "", false, 0); err != nil {
		t.Fatalf("SaveConfig with search disabled: %v", err)
	}
	if err := a.SetScheduleConfig(false, "08:15", "21:45", 80, 17, 25, 7, false, 8, 50); err != nil {
		t.Fatalf("SetScheduleConfig disabled: %v", err)
	}
	if err := a.SetAutoUpgradeConfig(
		false, 2, "max",
		true, false, true, false, true,
		700000, 0, 9000,
		false, "spells_first", true,
		false, []string{"Diggy", "Phoenix"},
		"elixir",
		false,
	); err != nil {
		t.Fatalf("SetAutoUpgradeConfig disabled: %v", err)
	}

	cfg := a.GetConfig()
	if cfg.Search.Enabled || cfg.Search.MinLootGold != 123000 || cfg.Search.MinLootElixir != 234000 ||
		cfg.Search.MinLootDarkElixir != 3000 {
		t.Fatalf("disabled search lost form values: %+v", cfg.Search)
	}
	if cfg.Schedule.Enabled || cfg.Schedule.ActiveHoursStart != "08:15" ||
		cfg.Schedule.SessionDurationMinutes != 80 || cfg.Schedule.BreakDurationMinutes != 25 ||
		cfg.Schedule.MaxAttacksPerHour != 8 || cfg.Schedule.MaxAttacksPerDay != 50 {
		t.Fatalf("disabled schedule lost form values: %+v", cfg.Schedule)
	}
	if cfg.Upgrade.AutoBuilders || cfg.Upgrade.UpgradeStrategy != "max" ||
		!cfg.Upgrade.UpgradeDefenses || cfg.Upgrade.UpgradeResources ||
		cfg.Upgrade.LabStrategy != "spells_first" || !cfg.Upgrade.LabAllowDE ||
		!reflect.DeepEqual(cfg.Upgrade.PetPriorities, []string{"Diggy", "Phoenix"}) ||
		cfg.Upgrade.WallResource != "elixir" {
		t.Fatalf("disabled builder/lab/pet form lost values: %+v", cfg.Upgrade)
	}

	// A fresh App reads what was written; nothing here is UI memory only.
	fresh := NewApp().GetConfig()
	if fresh.Search.MinLootGold != 123000 || fresh.Schedule.ActiveHoursStart != "08:15" ||
		fresh.Upgrade.LabStrategy != "spells_first" ||
		!reflect.DeepEqual(fresh.Upgrade.PetPriorities, []string{"Diggy", "Phoenix"}) {
		t.Fatalf("disabled section values did not survive reload: %+v %+v", fresh.Schedule, fresh.Upgrade)
	}
}
