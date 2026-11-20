package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Ducky705/ClashGO/internal/bot"
	"github.com/Ducky705/ClashGO/internal/config"
	"github.com/Ducky705/ClashGO/internal/paths"
)

func report(strategy string, at time.Time, gold int) bot.AttackReport {
	return bot.AttackReport{
		Timestamp:    at.Format(time.RFC3339),
		Strategy:     strategy,
		GoldStolen:   gold,
		ElixirStolen: gold,
		Stars:        2,
	}
}

// base is fixed so expected rates are exact; time.Now() would make the
// timestamps parse without their sub-second part and skew the window.
var lootBase = time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)

func TestEstimateLootPerHour_NoHistoryHasNoEstimate(t *testing.T) {
	reports := []bot.AttackReport{
		report("strategies/valk_spam.yaml", lootBase.Add(-3*time.Minute), 900000),
		report("strategies/valk_spam.yaml", lootBase.Add(-2*time.Minute), 900000),
	}

	got := estimateLootPerHour(reports, "valk", nil)

	if got.Measured {
		t.Error("two raids must not produce a measured estimate")
	}
	if got.PerHour != 0 {
		t.Errorf("perHour = %d, want 0 (no estimate, not an invented rate)", got.PerHour)
	}
	if got.Raids != 2 {
		t.Errorf("raids = %d, want 2", got.Raids)
	}
	if minutes := got.WindowMinutes(); minutes != 0 {
		t.Errorf("window = %d minutes, want 0 without an estimate", minutes)
	}
}

func TestEstimateLootPerHour_UsesSessionWallClock(t *testing.T) {
	reports := make([]bot.AttackReport, 0, 10)
	// One raid every two minutes, each securing 1M gold + 1M elixir.
	for i := 0; i < 10; i++ {
		reports = append(reports, report("strategies/valk_spam.yaml", lootBase.Add(time.Duration(i)*2*time.Minute), 1000000))
	}

	got := estimateLootPerHour(reports, "valk", nil)

	if !got.Measured {
		t.Fatal("ten evenly spaced raids should produce a measured estimate")
	}
	if got.Raids != 10 {
		t.Errorf("raids = %d, want 10", got.Raids)
	}
	// 20M total over the 18-minute first-to-last window.
	if got.PerHour != 66666666 {
		t.Errorf("perHour = %d, want 66666666 (20M loot / 18m wall clock)", got.PerHour)
	}
	if !got.WindowStart.Equal(lootBase) {
		t.Errorf("window start = %v, want first raid %v", got.WindowStart, lootBase)
	}
	if want := lootBase.Add(18 * time.Minute); !got.WindowEnd.Equal(want) {
		t.Errorf("window end = %v, want last raid %v", got.WindowEnd, want)
	}
	if got.WindowMinutes() != 18 {
		t.Errorf("window = %d minutes, want 18", got.WindowMinutes())
	}
}

func TestEstimateLootPerHour_CountsTrainingGap(t *testing.T) {
	reports := make([]bot.AttackReport, 0, 10)
	for i := 0; i < 9; i++ {
		reports = append(reports, report("strategies/valk_spam.yaml", lootBase.Add(time.Duration(i)*2*time.Minute), 1000000))
	}
	// Army retraining: 30 idle minutes before the last raid. Wall clock is 46m.
	reports = append(reports, report("strategies/valk_spam.yaml", lootBase.Add(46*time.Minute), 1000000))

	got := estimateLootPerHour(reports, "valk", nil)

	if !got.Measured {
		t.Fatal("expected a measured estimate")
	}
	// 20M over 46m = 26.08M/hour. Counting only raid cycles would say 66.7M.
	if got.PerHour != 26086956 {
		t.Errorf("perHour = %d, want 26086956 (training gap is wall clock)", got.PerHour)
	}
	if got.WindowMinutes() != 46 {
		t.Errorf("window = %d minutes, want 46", got.WindowMinutes())
	}
}

func TestEstimateLootPerHour_UsesLatestSession(t *testing.T) {
	reports := make([]bot.AttackReport, 0, 16)
	// Yesterday: 8 rich raids that must not decide today's rate.
	for i := 0; i < 8; i++ {
		reports = append(reports, report("strategies/valk_spam.yaml", lootBase.Add(-22*time.Hour+time.Duration(i)*2*time.Minute), 9000000))
	}
	// Today: 8 ordinary raids, 2-minute spacing, 600k loot each.
	for i := 0; i < 8; i++ {
		reports = append(reports, report("strategies/valk_spam.yaml", lootBase.Add(time.Duration(i)*2*time.Minute), 300000))
	}

	got := estimateLootPerHour(reports, "valk", nil)

	if !got.Measured {
		t.Fatal("expected a measured estimate from today's session")
	}
	if got.Raids != 8 {
		t.Errorf("raids = %d, want today's 8", got.Raids)
	}
	// 4.8M over 14m = 20.57M/hour, not the older session's 540M/hour.
	if got.PerHour != 20571428 {
		t.Errorf("perHour = %d, want 20571428 (latest session only)", got.PerHour)
	}
	if !got.WindowStart.Equal(lootBase) {
		t.Errorf("window start = %v, want today's first raid %v", got.WindowStart, lootBase)
	}
}

func TestEstimateLootPerHour_SplitsOvernightBreak(t *testing.T) {
	reports := make([]bot.AttackReport, 0, 11)
	for i := 0; i < 10; i++ {
		reports = append(reports, report("strategies/valk_spam.yaml", lootBase.Add(time.Duration(i)*2*time.Minute), 1000000))
	}
	// One lone raid after the break is not a session to measure.
	reports = append(reports, report("strategies/valk_spam.yaml", lootBase.Add(9*time.Hour), 1000000))

	got := estimateLootPerHour(reports, "valk", nil)

	if got.Measured {
		t.Errorf("perHour = %d measured from a single post-break raid, want no estimate", got.PerHour)
	}
}

func TestEstimateLootPerHour_FiltersByArmy(t *testing.T) {
	reports := make([]bot.AttackReport, 0, 18)
	for i := 0; i < 9; i++ {
		reports = append(reports, report("strategies/auto_edrag_rush.yaml", lootBase.Add(time.Duration(i)*3*time.Minute), 800000))
		reports = append(reports, report("strategies/valk_spam.yaml", lootBase.Add(time.Duration(i)*3*time.Minute), 300000))
	}

	got := estimateLootPerHour(reports, "valk", nil)
	if !got.Measured || got.Raids != 9 {
		t.Errorf("valk estimate used %d raids (measured=%v), want only its own 9", got.Raids, got.Measured)
	}
	// 5.4M over 24m = 13.5M/hour; edrag's 800k raids must not leak in.
	if got.PerHour != 13500000 {
		t.Errorf("perHour = %d, want 13500000 from valk raids only", got.PerHour)
	}
}

func sessionRecordAt(started, ended time.Time) sessionRecord {
	return sessionRecord{
		StartedAt: started.Format(time.RFC3339),
		EndedAt:   ended.Format(time.RFC3339),
	}
}

func TestEstimateLootPerHour_UsesRealSessionBounds(t *testing.T) {
	reports := make([]bot.AttackReport, 0, 10)
	for i := 0; i < 10; i++ {
		reports = append(reports, report("strategies/valk_spam.yaml", lootBase.Add(time.Duration(i)*2*time.Minute), 1000000))
	}
	// Bot went live 2 minutes before the first raid and stopped 5 minutes
	// after the last: 25 minutes of real session for 20M loot.
	sessions := []sessionRecord{
		sessionRecordAt(lootBase.Add(-2*time.Minute), lootBase.Add(23*time.Minute)),
	}

	got := estimateLootPerHour(reports, "valk", sessions)

	if !got.Measured {
		t.Fatal("expected a measured estimate from the session")
	}
	if got.PerHour != 48000000 {
		t.Errorf("perHour = %d, want 48000000 (20M over the real 25m session)", got.PerHour)
	}
	if !got.WindowStart.Equal(lootBase.Add(-2*time.Minute)) || !got.WindowEnd.Equal(lootBase.Add(23*time.Minute)) {
		t.Errorf("window = %v..%v, want the session start/stop", got.WindowStart, got.WindowEnd)
	}
	if got.WindowMinutes() != 25 {
		t.Errorf("window = %d minutes, want 25", got.WindowMinutes())
	}
}

func TestEstimateLootPerHour_SessionTooShortIsNoEstimate(t *testing.T) {
	reports := make([]bot.AttackReport, 0, 10)
	for i := 0; i < 10; i++ {
		reports = append(reports, report("strategies/valk_spam.yaml", lootBase.Add(time.Duration(i)*2*time.Minute), 1000000))
	}
	// The session only covers the last five raids. Enough raids exist in the
	// raw history, but not in this run — do not fall back and invent a rate.
	sessions := []sessionRecord{
		sessionRecordAt(lootBase.Add(10*time.Minute), lootBase.Add(20*time.Minute)),
	}

	got := estimateLootPerHour(reports, "valk", sessions)
	if got.Measured || got.PerHour != 0 {
		t.Errorf("perHour = %d (measured=%v) from 5 session raids, want no estimate", got.PerHour, got.Measured)
	}
	if got.Raids != 5 {
		t.Errorf("raids = %d, want the 5 inside the session", got.Raids)
	}
}

func TestEstimateLootPerHour_UsesLatestSessionWithThisArmy(t *testing.T) {
	reports := make([]bot.AttackReport, 0, 16)
	for i := 0; i < 8; i++ {
		reports = append(reports, report("strategies/valk_spam.yaml", lootBase.Add(-20*time.Hour+time.Duration(i)*2*time.Minute), 5000000))
		reports = append(reports, report("strategies/valk_spam.yaml", lootBase.Add(time.Duration(i)*2*time.Minute), 1000000))
	}
	sessions := []sessionRecord{
		sessionRecordAt(lootBase.Add(-20*time.Hour), lootBase.Add(-20*time.Hour+16*time.Minute)),
		sessionRecordAt(lootBase, lootBase.Add(16*time.Minute)),
	}

	got := estimateLootPerHour(reports, "valk", sessions)

	if !got.Measured || got.PerHour != 60000000 {
		t.Errorf("perHour = %d (measured=%v), want 60000000 from today's session", got.PerHour, got.Measured)
	}
	if !got.WindowStart.Equal(lootBase) {
		t.Errorf("window start = %v, want today's session", got.WindowStart)
	}
}

func TestEstimateLootPerHour_IgnoresMalformedSessionRecords(t *testing.T) {
	reports := make([]bot.AttackReport, 0, 10)
	for i := 0; i < 10; i++ {
		reports = append(reports, report("strategies/valk_spam.yaml", lootBase.Add(time.Duration(i)*2*time.Minute), 1000000))
	}
	sessions := []sessionRecord{
		{StartedAt: "not-a-time", EndedAt: lootBase.Format(time.RFC3339)},
		{StartedAt: lootBase.Format(time.RFC3339)}, // no end: interrupted write
	}

	got := estimateLootPerHour(reports, "valk", sessions)
	if !got.Measured {
		t.Fatal("malformed session records must not suppress the raid-span fallback")
	}
	// Fallback: 20M over the 18-minute first-to-last span.
	if got.PerHour != 66666666 {
		t.Errorf("perHour = %d, want fallback 66666666", got.PerHour)
	}
}

func TestEstimateLootPerHour_RejectsZeroWindow(t *testing.T) {
	reports := make([]bot.AttackReport, 0, 8)
	for i := 0; i < 8; i++ {
		reports = append(reports, report("strategies/valk_spam.yaml", lootBase, 1000000))
	}

	got := estimateLootPerHour(reports, "valk", nil)
	if got.Measured || got.PerHour != 0 {
		t.Errorf("perHour = %d (measured=%v) from a zero-length window, want no estimate", got.PerHour, got.Measured)
	}
}

func TestSaveUpgradeSetup_ArmsLabAndPets(t *testing.T) {
	t.Setenv("CLASHGO_CONFIG_DIR", t.TempDir())
	a := NewApp()

	err := a.SaveUpgradeSetup(
		true, "strategic_rush",
		true, true, true, true, true, true,
		true, "troops_first", true,
	)
	if err != nil {
		t.Fatalf("SaveUpgradeSetup failed: %v", err)
	}

	cfg := config.LoadOrDefault("config.json")
	if !cfg.Upgrade.AutoBuilders || !cfg.Upgrade.AutoLab || !cfg.Upgrade.AutoPets {
		t.Fatalf("strategic rush must arm builders, lab, and pets: %+v", cfg.Upgrade)
	}
	if cfg.Upgrade.LabStrategy != "troops_first" {
		t.Errorf("LabStrategy = %q, want troops_first", cfg.Upgrade.LabStrategy)
	}
	if len(cfg.Upgrade.PetPriorities) != 12 || cfg.Upgrade.PetPriorities[0] != "Phoenix" {
		t.Errorf("PetPriorities = %v, want the 12-pet recommended queue starting with Phoenix", cfg.Upgrade.PetPriorities)
	}
}

func TestSaveUpgradeSetup_NormalizesLegacyPetOrder(t *testing.T) {
	t.Setenv("CLASHGO_CONFIG_DIR", t.TempDir())
	cfg := config.DefaultConfig()
	cfg.Upgrade.PetPriorities = []string{"Diggy", "Unicorn", "Frosty", "Phoenix", "Spirit Fox"}
	if err := writeConfigFile(cfg); err != nil {
		t.Fatalf("write config: %v", err)
	}

	a := NewApp()
	if err := a.SaveUpgradeSetup(true, "strategic_rush", true, true, true, true, true, true, true, "troops_first", true); err != nil {
		t.Fatalf("SaveUpgradeSetup failed: %v", err)
	}

	loaded := config.LoadOrDefault("config.json")
	if len(loaded.Upgrade.PetPriorities) != 12 || loaded.Upgrade.PetPriorities[0] != "Phoenix" {
		t.Fatalf("legacy pet order was not upgraded: %v", loaded.Upgrade.PetPriorities)
	}
}

func TestSaveUpgradeSetup_KeepsCustomPetOrder(t *testing.T) {
	t.Setenv("CLASHGO_CONFIG_DIR", t.TempDir())
	custom := []string{"Unicorn", "Phoenix"}
	cfg := config.DefaultConfig()
	cfg.Upgrade.PetPriorities = custom
	if err := writeConfigFile(cfg); err != nil {
		t.Fatalf("write config: %v", err)
	}

	a := NewApp()
	if err := a.SaveUpgradeSetup(true, "strategic_rush", true, true, true, true, true, true, true, "troops_first", true); err != nil {
		t.Fatalf("SaveUpgradeSetup failed: %v", err)
	}

	loaded := config.LoadOrDefault("config.json")
	if len(loaded.Upgrade.PetPriorities) != len(custom) || loaded.Upgrade.PetPriorities[0] != custom[0] {
		t.Fatalf("custom pet order was overwritten: %v", loaded.Upgrade.PetPriorities)
	}
}

func TestLaunchOptions_NoHistoryHasNoEstimate(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLASHGO_CONFIG_DIR", dir)
	if err := writeConfigFile(config.DefaultConfig()); err != nil {
		t.Fatal(err)
	}

	app := NewApp()
	options := app.GetLaunchOptions()

	if len(options.Options) != 2 {
		t.Fatalf("got %d options, want valk + edrag", len(options.Options))
	}
	for _, option := range options.Options {
		if option.LootPerHour != 0 {
			t.Errorf("%s loot/hour = %d with no history, want 0 (no estimate)", option.Key, option.LootPerHour)
		}
		if option.Measured {
			t.Errorf("%s claims a measured estimate with no history", option.Key)
		}
		if option.Raids != 0 || option.WindowMinutes != 0 {
			t.Errorf("%s claims %d raids over %d minutes with no history", option.Key, option.Raids, option.WindowMinutes)
		}
		if option.Recommended {
			t.Errorf("%s is recommended without any measured rate to compare", option.Key)
		}
		if option.Name == "" || option.Pitch == "" {
			t.Errorf("%s is missing launch copy", option.Key)
		}
	}
}

func TestLaunchOptions_RecommendsMeasuredArmy(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLASHGO_CONFIG_DIR", dir)
	if err := writeConfigFile(config.DefaultConfig()); err != nil {
		t.Fatal(err)
	}

	reports := make([]bot.AttackReport, 0, 8)
	for i := 0; i < 8; i++ {
		reports = append(reports, report("strategies/valk_spam.yaml", lootBase.Add(time.Duration(i)*2*time.Minute), 1000000))
	}
	data, err := json.Marshal(reports)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.ResolveConfig("attack_history.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}

	options := NewApp().GetLaunchOptions()
	var recommended, unmeasured int
	for _, option := range options.Options {
		if option.Recommended {
			recommended++
			if option.Key != "valk" {
				t.Errorf("recommended %s, want the army with measured raids", option.Key)
			}
			if !option.Measured || option.LootPerHour <= 0 || option.Raids != 8 || option.WindowMinutes != 14 {
				t.Errorf("recommended option = %+v, want valk measured over 8 raids / 14 minutes", option)
			}
		}
		if option.Key == "edrag" {
			if option.LootPerHour != 0 || option.Measured {
				t.Errorf("edrag = %d/hour (measured=%v) without its own raids, want no estimate", option.LootPerHour, option.Measured)
			}
			unmeasured++
		}
	}
	if recommended != 1 || unmeasured != 1 {
		t.Errorf("recommended = %d, unmeasured options = %d, want 1 and 1", recommended, unmeasured)
	}
}

func TestChooseArmy_WritesStrategyAndAdaptive(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLASHGO_CONFIG_DIR", dir)
	if err := writeConfigFile(config.DefaultConfig()); err != nil {
		t.Fatal(err)
	}

	app := NewApp()
	if err := app.ChooseArmy("strategies/valk_spam.yaml", true, 0); err != nil {
		t.Fatalf("choose valk: %v", err)
	}

	cfg := config.LoadOrDefault("config.json")
	if filepath.Base(cfg.Attack.StrategyFile) != "valk_spam.yaml" {
		t.Errorf("strategy = %q, want valk_spam.yaml", cfg.Attack.StrategyFile)
	}
	if !cfg.Search.AutoLoot {
		t.Error("adaptive loot was not enabled")
	}
	// The stored path must resolve on this machine, not only look right.
	if _, err := filepath.Abs(cfg.Attack.StrategyFile); err != nil {
		t.Errorf("stored strategy path is not absolute: %v", err)
	}
	if filepath.Dir(cfg.Attack.StrategyFile) != paths.GetAssetsDir()+"/strategies" {
		t.Errorf("strategy stored outside the assets tree: %q", cfg.Attack.StrategyFile)
	}
}

func TestChooseArmy_RejectsUnknownAndNonYAML(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLASHGO_CONFIG_DIR", dir)
	if err := writeConfigFile(config.DefaultConfig()); err != nil {
		t.Fatal(err)
	}
	app := NewApp()
	before := config.LoadOrDefault("config.json").Attack.StrategyFile

	for _, bad := range []string{"strategies/nope.yaml", "strategies/default.csv", "../config.json"} {
		if err := app.ChooseArmy(bad, true, 0); err == nil {
			t.Errorf("ChooseArmy(%q) succeeded, want an error", bad)
		}
	}
	if after := config.LoadOrDefault("config.json").Attack.StrategyFile; after != before {
		t.Errorf("a rejected choice still changed the config: %q -> %q", before, after)
	}
}

func TestSaveUpgradeSetup_DisablingEngineStopsResearch(t *testing.T) {
	t.Setenv("CLASHGO_CONFIG_DIR", t.TempDir())
	a := NewApp()

	if err := a.SaveUpgradeSetup(false, "strategic_rush", true, true, true, true, true, true, true, "troops_first", true); err != nil {
		t.Fatalf("SaveUpgradeSetup failed: %v", err)
	}

	cfg := config.LoadOrDefault("config.json")
	if cfg.Upgrade.AutoLab || cfg.Upgrade.AutoPets {
		t.Fatalf("lab and pets must follow the engine switch: %+v", cfg.Upgrade)
	}
}

func TestLaunchOptions_ReflectsAdaptiveHistory(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLASHGO_CONFIG_DIR", dir)
	cfg := config.DefaultConfig()
	cfg.Search.AutoLoot = true
	if err := writeConfigFile(cfg); err != nil {
		t.Fatal(err)
	}

	app := NewApp()
	options := app.GetLaunchOptions()

	if !options.AdaptiveLoot {
		t.Error("adaptive flag not reported from config")
	}
	if options.AdaptiveReady {
		t.Error("adaptive reported ready with no search history")
	}
	if options.MinSamples <= 0 {
		t.Errorf("min samples = %d, want the solver gate", options.MinSamples)
	}
	if options.Samples != 0 {
		t.Errorf("samples = %d, want 0", options.Samples)
	}
}

// TestChooseArmy_WritesSlotAndLoadoutIsRendered is the launch-screen contract:
// the recipe slot the player picks is what the bot will arm, and every option
// carries the loadout the screen tells them to build.
func TestChooseArmy_WritesSlotAndLoadoutIsRendered(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLASHGO_CONFIG_DIR", dir)
	if err := writeConfigFile(config.DefaultConfig()); err != nil {
		t.Fatal(err)
	}

	app := NewApp()
	if err := app.ChooseArmy("strategies/auto_edrag_rush.yaml", false, 3); err != nil {
		t.Fatalf("choose edrag: %v", err)
	}
	cfg := config.LoadOrDefault("config.json")
	if got := cfg.Attack.StrategySlots["auto_edrag_rush.yaml"]; got != 3 {
		t.Fatalf("strategy slot = %d, want 3", got)
	}

	options := app.GetLaunchOptions()
	if options.MaxArmySlots != maxArmySlots {
		t.Errorf("max_army_slots = %d, want %d", options.MaxArmySlots, maxArmySlots)
	}
	for _, option := range options.Options {
		load := option.Loadout
		if load.ArmyCamp <= 0 {
			t.Errorf("%s: army camp space missing, the screen cannot instruct a build", option.Key)
		}
		if len(load.Troops) == 0 {
			t.Errorf("%s: no troop counts to show", option.Key)
		}
		total := 0
		for _, unit := range load.Troops {
			if unit.Name == "" || unit.Count <= 0 {
				t.Errorf("%s: bad troop line %+v", option.Key, unit)
			}
			total += unit.Count
		}
		if total == 0 {
			t.Errorf("%s: troop lines add up to nothing", option.Key)
		}
	}
	// The slot the player chose must be the slot the screen shows for that army.
	for _, option := range options.Options {
		if option.Key == "edrag" && option.ArmySlot != 3 {
			t.Errorf("edrag army_slot = %d, want the chosen 3", option.ArmySlot)
		}
	}

	// A slot the game does not hold is refused, and nothing is written.
	if err := app.ChooseArmy("strategies/auto_edrag_rush.yaml", false, maxArmySlots+1); err == nil {
		t.Error("out-of-range slot accepted, want an error")
	}
}
