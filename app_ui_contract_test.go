package main

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/Ducky705/ClashGO/internal/config"
	"github.com/Ducky705/ClashGO/internal/paths"
)

// uiBindingCoverage maps every method exposed to the React tree (generated
// App.d.ts, which is the Wails binding surface, plus anything the browser stub
// forwards) to the test that drives it through the real *App and asserts state.
//
// This is deliberately exhaustive: adding a binding without a state-asserting
// test fails TestUI_EveryFrontendBindingHasStateTest even though the binding
// compiles and the UI renders.
var uiBindingCoverage = map[string]string{
	"ApplyLootAutotune":    "TestEngine_AutoLootControlsReachEngine",
	"ApplyUpdate":          "TestUI_UpdaterBindingsGuarded",
	"CheckForUpdate":       "TestUI_UpdaterBindingsGuarded",
	"ChooseArmy":           "TestChooseArmy_WritesSlotAndLoadoutIsRendered",
	"ClearSkippedVersion":  "TestUI_UpdaterBindingsGuarded",
	"CreateAccount":        "TestEngine_AccountControlsReachStoreAndEngine",
	"DeleteAccount":        "TestApp_AccountLifecycle",
	"DownloadUpdate":       "TestUI_UpdaterBindingsGuarded",
	"GetAccounts":          "TestEngine_AccountControlsReachStoreAndEngine",
	"GetAppVersion":        "TestUI_ReadBindingsReflectEngineState",
	"GetArmyPlans":         "TestEngine_ArmySlotAndAutoEndReachEngine",
	"GetAttackHistory":     "TestUI_ReadBindingsReflectEngineState",
	"GetConfig":            "TestUI_ReadBindingsReflectEngineState",
	"GetLaunchOptions":     "TestUI_ReadBindingsReflectEngineState",
	"GetLogs":              "TestUI_ReadBindingsReflectEngineState",
	"GetLootAutotune":      "TestUI_ReadBindingsReflectEngineState",
	"GetStats":             "TestUI_ReadBindingsReflectEngineState",
	"GetStrategies":        "TestUI_ReadBindingsReflectEngineState",
	"GetUpdateStatus":      "TestUI_UpdaterBindingsGuarded",
	"InstallAndRestart":    "TestUI_UpdaterBindingsGuarded",
	"IsRunning":            "TestEngine_StartStopLifecycle",
	"ResetAccountUsage":    "TestEngine_AccountControlsReachStoreAndEngine",
	"ResetLootAutotune":    "TestEngine_AutoLootControlsReachEngine",
	"ResetStats":           "TestUI_ReadBindingsReflectEngineState",
	"SaveConfig":           "TestEngine_SearchAndAttackControls",
	"SaveFullConfig":       "TestUI_SaveFullConfigValidatesAndSaves",
	"SaveLaunchSettings":   "TestUI_LaunchWizardBindings",
	"SaveUpgradeSetup":     "TestUI_LaunchWizardBindings",
	"SetAutoLoot":          "TestEngine_AutoLootControlsReachEngine",
	"SetAutoUpgradeConfig": "TestEngine_UpgradeAndPetControlsReachBuilderManager",
	"SetRotation":          "TestApp_RotationPersists",
	"SetScheduleConfig":    "TestEngine_ScheduleControlsDriveCanAttackGate",
	"SetStorageConfig":     "TestEngine_StorageControlsDriveHaltPredicate",
	"SetStrategyArmySlot":  "TestEngine_ArmySlotAndAutoEndReachEngine",
	"SetStrategyAutoEnd":   "TestEngine_ArmySlotAndAutoEndReachEngine",
	"SkipCurrentVersion":   "TestUI_UpdaterBindingsGuarded",
	"StartBot":             "TestEngine_StartStopLifecycle",
	"StopBot":              "TestEngine_StartStopLifecycle",
	"SwitchAccount":        "TestEngine_AccountControlsReachStoreAndEngine",
	"TestDeviceMenu":       "TestUI_DeviceTestMenuRoutesActions",
	"UpdateAccount":        "TestEngine_AccountControlsReachStoreAndEngine",
}

func TestUI_EveryFrontendBindingHasStateTest(t *testing.T) {
	bindings := frontendBindingNames(t)
	if len(bindings) < 30 {
		t.Fatalf("parsed only %d frontend bindings; the coverage scan is broken", len(bindings))
	}
	tests := testFunctionBodies(t)

	for _, name := range bindings {
		testName, ok := uiBindingCoverage[name]
		if !ok {
			t.Errorf("%s is exposed to the frontend but has no entry in uiBindingCoverage: add a test that drives it through *App and asserts state", name)
			continue
		}
		body, exists := tests[testName]
		if !exists {
			t.Errorf("%s is mapped to %s, which is not a test in this package", name, testName)
			continue
		}
		if !strings.Contains(body, "."+name+"(") {
			t.Errorf("%s is mapped to %s, but that test never calls %s", name, testName, name)
		}
		if !observesEngineState(body) {
			t.Errorf("%s is mapped to %s, which only calls the binding: it must also read engine/config state (LiveConfig, GetConfig, diskConfig, GetStats, ...)", name, testName)
		}
		if strings.Contains(body, "t.Skip") {
			t.Errorf("%s relies on %s, which skips itself", name, testName)
		}
	}
	for name := range uiBindingCoverage {
		if !containsString(bindings, name) {
			t.Errorf("uiBindingCoverage lists %s, which is not exposed to the frontend anymore", name)
		}
	}
}

// frontendBindingNames is the union of the generated Wails bindings and every
// key the browser dev stub forwards. The stub is what the browser sweep and
// `vite dev` actually call, so a method only there would otherwise escape.
func frontendBindingNames(t *testing.T) []string {
	t.Helper()
	declared := generatedBindingNames(t)
	names := make([]string, 0, len(declared))
	seen := make(map[string]bool, len(declared))
	for n := range declared {
		seen[n] = true
		names = append(names, n)
	}

	raw, err := os.ReadFile("web/public/wails-stub.js")
	if err != nil {
		t.Fatalf("read stub: %v", err)
	}
	// Only the `window.go.main.App` table is a binding surface; the
	// `window.runtime` block below it stubs Wails events, not App methods.
	appBlock, _, _ := strings.Cut(string(raw), "window.runtime = {")
	re := regexp.MustCompile(`(?m)^\s+([A-Z][A-Za-z0-9_]*): function`)
	for _, m := range re.FindAllStringSubmatch(appBlock, -1) {
		if !seen[m[1]] {
			t.Errorf("stub forwards %s but App.d.ts does not expose it", m[1])
		}
	}
	return names
}

func testFunctionBodies(t *testing.T) map[string]string {
	t.Helper()
	files, err := filepath.Glob("*_test.go")
	if err != nil {
		t.Fatalf("glob tests: %v", err)
	}
	out := make(map[string]string, 128)
	for _, file := range files {
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("read %s: %v", file, err)
		}
		for _, chunk := range strings.Split(string(raw), "\nfunc ") {
			body := strings.TrimPrefix(chunk, "func ")
			name := body
			if i := strings.IndexByte(body, '('); i > 0 {
				name = body[:i]
			}
			if strings.HasPrefix(name, "Test") {
				out[name] = body
			}
		}
	}
	return out
}

// engineStateMarkers are the reads that make a test more than "the binding
// returned nil": live engine snapshots and persisted-config reads. A no-op or
// call-and-check-error mutation has none of them.
var engineStateMarkers = []string{
	"LiveConfig(", "diskConfig(", "config.LoadOrDefault(", "snapshotValidation(",
	"GetConfig(", "GetStats(", "GetAccounts(", "GetArmyPlans(",
	"GetLaunchOptions(", "GetLootAutotune(", "GetStrategies(",
	"GetAttackHistory(", "GetLogs(", "GetUpdateStatus(", "GetAppVersion(",
	"IsRunning(", "assertRejectedWithoutSideEffects(",
}

func observesEngineState(body string) bool {
	for _, marker := range engineStateMarkers {
		if strings.Contains(body, marker) {
			return true
		}
	}
	return false
}

func containsString(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

// TestUI_ReadBindingsReflectEngineState ties the getter surface to the state the
// engine actually holds, so a getter that keeps serving stale data fails.
func TestUI_ReadBindingsReflectEngineState(t *testing.T) {
	a := engineHarness(t)

	if err := a.SaveConfig(612000, 623000, 6400, true, "valk_spam.yaml", true, 55, false, 0, 0, 0); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	cfg := a.GetConfig()
	if cfg.Search.MinLootGold != 612000 || cfg.Attack.StallTimerSeconds != 55 {
		t.Fatalf("GetConfig = %+v, want the saved values", cfg.Search)
	}
	if !a.IsRunning() {
		t.Fatal("IsRunning false although the harness installed a bot")
	}
	stats := a.GetStats()
	if stats.AttacksCompleted != 0 {
		t.Fatalf("GetStats attacks = %d, want 0", stats.AttacksCompleted)
	}
	if a.GetAttackHistory() == nil {
		t.Fatal("GetAttackHistory returned nil; the UI would crash on length")
	}
	a.mu.Lock()
	a.logBuffer = append(a.logBuffer, "engine log line")
	a.mu.Unlock()
	if logs := a.GetLogs(); len(logs) == 0 || !strings.Contains(strings.Join(logs, "\n"), "engine log line") {
		t.Fatalf("GetLogs = %v, want the buffered line", logs)
	}
	if strategies := a.GetStrategies(); !containsString(strategies, paths.Resolve("strategies/valk_spam.yaml")) &&
		!containsString(strategies, "valk_spam.yaml") {
		t.Fatalf("GetStrategies = %v, want valk_spam.yaml", strategies)
	}
	options := a.GetLaunchOptions()
	if options.MinGold != 612000 || len(options.Options) == 0 {
		t.Fatalf("GetLaunchOptions = %+v, want the live floors and army cards", options)
	}
	if status := a.GetLootAutotune(); status.Samples != 0 {
		t.Fatalf("GetLootAutotune samples = %d, want 0", status.Samples)
	}
	if version := a.GetAppVersion(); version == "" {
		t.Fatal("GetAppVersion returned empty")
	}

	a.lastStats.AttacksCompleted = 9
	a.lastStats.TotalGold = 123
	a.ResetStats()
	if got := a.GetStats(); got.AttacksCompleted != 0 || got.TotalGold != 0 {
		t.Fatalf("ResetStats left %+v", got)
	}
}

// TestUI_UpdaterBindingsGuarded drives the update buttons without GitHub: each
// one must refuse visibly instead of downloading, applying or relaunching.
func TestUI_UpdaterBindingsGuarded(t *testing.T) {
	t.Setenv("CLASHGO_CONFIG_DIR", t.TempDir())
	a := NewApp()

	if st := a.GetUpdateStatus(); st.CurrentVersion == "" {
		t.Fatalf("GetUpdateStatus = %+v, want the current version", st)
	}
	if err := a.ClearSkippedVersion(); err != nil {
		t.Fatalf("ClearSkippedVersion: %v", err)
	}
	if err := a.SkipCurrentVersion(); err == nil {
		t.Fatal("SkipCurrentVersion accepted with no known release")
	}
	if _, err := a.DownloadUpdate(); err == nil {
		t.Fatal("DownloadUpdate downloaded with no available update")
	}
	if err := a.ApplyUpdate(); err == nil {
		t.Fatal("ApplyUpdate succeeded with no downloaded file")
	}
	if err := a.InstallAndRestart(); err == nil {
		t.Fatal("InstallAndRestart relaunched with no update")
	}
	a.updater = nil
	if _, err := a.CheckForUpdate(); err == nil {
		t.Fatal("CheckForUpdate succeeded without an updater")
	}
	if st := a.GetUpdateStatus(); st.State == "" {
		t.Fatalf("GetUpdateStatus lost its state after a refused update: %+v", st)
	}
}

// TestUI_DeviceTestMenuRoutesActions drives the Settings > Testing buttons with
// the device layer seamed: each action must reach the menu runner with the
// persisted device/display/upgrade config, and must not write config itself.
func TestUI_DeviceTestMenuRoutesActions(t *testing.T) {
	t.Setenv("CLASHGO_CONFIG_DIR", t.TempDir())
	cfg := config.DefaultConfig()
	cfg.Device.ADBHost = "10.9.8.7"
	cfg.Device.ADBPort = 5099
	cfg.Device.DeviceID = "localhost:5099"
	cfg.Device.DisplayScale = 1.25
	cfg.Upgrade.UpgradeStrategy = "max"
	if err := writeConfigFile(cfg); err != nil {
		t.Fatalf("write config: %v", err)
	}
	before, err := os.ReadFile(paths.ResolveConfig("config.json"))
	if err != nil {
		t.Fatalf("read config: %v", err)
	}

	oldRun := runDeviceTestMenu
	t.Cleanup(func() { runDeviceTestMenu = oldRun })
	var gotActions []string
	var gotConfigs []config.BotConfig
	runDeviceTestMenu = func(c *config.BotConfig, action string) error {
		gotActions = append(gotActions, action)
		gotConfigs = append(gotConfigs, *c)
		return nil
	}

	a := NewApp()
	for _, action := range []string{"open", "filters", "close"} {
		if err := a.TestDeviceMenu(action); err != nil {
			t.Fatalf("TestDeviceMenu(%q): %v", action, err)
		}
	}
	if len(gotActions) != 3 || gotActions[0] != "open" || gotActions[1] != "filters" || gotActions[2] != "close" {
		t.Fatalf("actions reached the menu runner as %v, want open/filters/close", gotActions)
	}
	for _, c := range gotConfigs {
		if c.Device.ADBHost != "10.9.8.7" || c.Device.ADBPort != 5099 ||
			c.Device.DisplayScale != 1.25 || c.Upgrade.UpgradeStrategy != "max" {
			t.Fatalf("menu runner got %+v, want the persisted device/upgrade settings", c.Device)
		}
	}
	if got := a.GetConfig().Device.ADBHost; got != "10.9.8.7" {
		t.Fatalf("GetConfig device host = %q after test tools, want 10.9.8.7", got)
	}
	after, err := os.ReadFile(paths.ResolveConfig("config.json"))
	if err != nil {
		t.Fatalf("read config after: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("the device test buttons wrote config.json")
	}
}

// TestUI_SaveFullConfigValidatesAndSaves covers the wizard's final step: one
// whole-config save that must reject invalid sections without touching state
// and accept a valid config all the way into the engine and disk.
func TestUI_SaveFullConfigValidatesAndSaves(t *testing.T) {
	a := validationHarness(t)

	bad := *a.GetConfig()
	bad.Schedule.ActiveHoursEnd = "not-a-time"
	before := snapshotValidation(t, a)
	assertRejectedWithoutSideEffects(t, a, "wizard full config with bad hour", a.SaveFullConfig(bad), before)

	good := *a.GetConfig()
	good.Search.MinLootElixir = 654000
	if err := a.SaveFullConfig(good); err != nil {
		t.Fatalf("valid wizard config rejected: %v", err)
	}
	if got := a.bot.LiveConfig().Config.Search.MinLootElixir; got != 654000 {
		t.Fatalf("wizard config did not reach the engine: %d", got)
	}
	if disk := diskConfig(t); disk.Search.MinLootElixir != 654000 {
		t.Fatalf("wizard config did not reach disk: %d", disk.Search.MinLootElixir)
	}
}

// The four F.2 controls have no React test harness in this repo; this guard
// pins the wiring that tsc cannot see: the ConfigView inputs and client-side
// invalid predicate exist, App loads the fields, and both SaveConfig calls
// forward them in the binding's argument order. The server-side round trip and
// rejection are behavioural tests in app_ui_bindings_test.go.
func TestUI_LootTargetControlsAreWired(t *testing.T) {
	read := func(path string) string {
		t.Helper()
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		return string(raw)
	}

	configView := read("web/src/components/ConfigView.tsx")
	for _, want := range []string{
		"endOnLootTarget: boolean;",
		"setEndOnLootTarget",
		"setLootTargetGold",
		"setLootTargetElixir",
		"setLootTargetDE",
		"ariaLabel=\"End raid on loot target\"",
		"htmlFor={`loot-target-${item.tone}`}",
		"endOnLootTarget && lootTargetGold === 0 && lootTargetElixir === 0 && lootTargetDE === 0",
		"invalid(lootTargetGold, THRESHOLD_MAX)",
		"invalid(lootTargetElixir, THRESHOLD_MAX)",
		"invalid(lootTargetDE, THRESHOLD_MAX)",
		"|| lootTargetsInvalid",
	} {
		if !strings.Contains(configView, want) {
			t.Errorf("ConfigView.tsx is missing %q; the loot-target control or its client validation is not wired", want)
		}
	}

	app := read("web/src/App.tsx")
	for _, want := range []string{
		"setEndOnLootTarget(conf.attack.end_on_loot_target ?? false)",
		"setLootTargetGold(conf.attack.loot_target_gold ?? 0)",
		"setLootTargetElixir(conf.attack.loot_target_elixir ?? 0)",
		"setLootTargetDE(conf.attack.loot_target_de ?? 0)",
	} {
		if !strings.Contains(app, want) {
			t.Errorf("App.tsx does not load %q; saved targets would not reach the controls", want)
		}
	}
	if !regexp.MustCompile(`(?s)SaveConfig\([^)]*endOnLootTarget,[^)]*lootTargetGold,[^)]*lootTargetElixir,[^)]*lootTargetDE,`).MatchString(app) {
		t.Error("App.tsx SaveConfig call does not forward the four loot-target values in binding order")
	}

	stub := read("web/public/wails-stub.js")
	if !strings.Contains(stub, "loot_target_gold = lootTargetGold") {
		t.Error("browser stub does not persist the loot-target fields, so the dev UI would silently lose them")
	}
}
