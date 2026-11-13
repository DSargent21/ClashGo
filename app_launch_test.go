package main

import (
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

func TestEstimateLootPerHour_PlaceholderBelowThreshold(t *testing.T) {
	now := time.Now()
	reports := []bot.AttackReport{
		report("strategies/valk_spam.yaml", now.Add(-3*time.Minute), 900000),
		report("strategies/valk_spam.yaml", now.Add(-2*time.Minute), 900000),
	}

	perHour, measured, raids := estimateLootPerHour(reports, "valk", 5200000)

	if measured {
		t.Error("two raids must not produce a measured estimate")
	}
	if perHour != 5200000 {
		t.Errorf("perHour = %d, want the placeholder", perHour)
	}
	if raids != 2 {
		t.Errorf("raids = %d, want 2", raids)
	}
}

func TestEstimateLootPerHour_MeasuredFromHistory(t *testing.T) {
	now := time.Now()
	reports := make([]bot.AttackReport, 0, 10)
	// One raid every two minutes, each securing 1M gold + 1M elixir.
	for i := 0; i < 10; i++ {
		reports = append(reports, report("strategies/valk_spam.yaml", now.Add(-time.Duration(10-i)*2*time.Minute), 1000000))
	}

	perHour, measured, raids := estimateLootPerHour(reports, "valk", 5200000)

	if !measured {
		t.Fatal("ten evenly spaced raids should produce a measured estimate")
	}
	if raids != 10 {
		t.Errorf("raids = %d, want 10", raids)
	}
	// 2M loot per raid over a 120s cycle = 30 raids/hour = 60M/hour.
	if perHour != 60000000 {
		t.Errorf("perHour = %d, want 60000000 (2M loot per 2-minute cycle)", perHour)
	}
}

func TestEstimateLootPerHour_IgnoresPausedSessions(t *testing.T) {
	now := time.Now()
	reports := make([]bot.AttackReport, 0, 9)
	for i := 0; i < 8; i++ {
		reports = append(reports, report("strategies/valk_spam.yaml", now.Add(-time.Duration(9-i)*90*time.Second), 500000))
	}
	// Five hours later: a new session, not a raid cycle.
	reports = append(reports, report("strategies/valk_spam.yaml", now.Add(5*time.Hour), 500000))

	perHour, measured, _ := estimateLootPerHour(reports, "valk", 1)

	if !measured {
		t.Fatal("expected a measured estimate from the in-session gaps")
	}
	// 1M per raid over 90s = 40 raids/hour = 40M/hour; the 5h gap must be dropped.
	if perHour != 40000000 {
		t.Errorf("perHour = %d, want 40000000 (paused session excluded)", perHour)
	}
}

func TestEstimateLootPerHour_FiltersByArmy(t *testing.T) {
	now := time.Now()
	reports := make([]bot.AttackReport, 0, 18)
	for i := 0; i < 9; i++ {
		reports = append(reports, report("strategies/auto_edrag_rush.yaml", now.Add(-time.Duration(20-i)*3*time.Minute), 800000))
		reports = append(reports, report("strategies/valk_spam.yaml", now.Add(-time.Duration(20-i)*3*time.Minute), 300000))
	}

	_, measured, raids := estimateLootPerHour(reports, "valk", 1)
	if !measured || raids != 9 {
		t.Errorf("valk estimate used %d raids (measured=%v), want only its own 9", raids, measured)
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

func TestLaunchOptions_RecommendsHigherEstimate(t *testing.T) {
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
	recommended := 0
	for _, option := range options.Options {
		if option.Recommended {
			recommended++
			if option.Key != "edrag" {
				t.Errorf("recommended %s, want edrag by default", option.Key)
			}
		}
		if option.LootPerHour <= 0 {
			t.Errorf("%s has no loot estimate", option.Key)
		}
		if option.Measured {
			t.Errorf("%s claims a measured estimate with no history", option.Key)
		}
		if option.Name == "" || option.Pitch == "" {
			t.Errorf("%s is missing launch copy", option.Key)
		}
	}
	if recommended != 1 {
		t.Errorf("%d options flagged recommended, want exactly 1", recommended)
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

func TestMedianInt(t *testing.T) {
	cases := []struct {
		in   []int
		want int
	}{
		{nil, 0},
		{[]int{7}, 7},
		{[]int{3, 1, 2}, 2},
		{[]int{4, 1, 3, 2}, 2},
	}
	for _, c := range cases {
		if got := medianInt(c.in); got != c.want {
			t.Errorf("medianInt(%v) = %d, want %d", c.in, got, c.want)
		}
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
