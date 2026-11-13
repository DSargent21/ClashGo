package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Ducky705/ClashGO/internal/bot"
	"github.com/Ducky705/ClashGO/internal/config"
	"github.com/Ducky705/ClashGO/internal/paths"
	"github.com/Ducky705/ClashGO/pkg/strategy"
)

// LaunchLoadoutUnit is one "make this many" line on the launch screen.
type LaunchLoadoutUnit struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
	Slots int    `json:"slots"`
}

// LaunchLoadout is the army the player has to assemble in-game for an option:
// the space it has to fit and the units that fill it. Rendered as the setup
// instructions under the chosen army card.
type LaunchLoadout struct {
	ArmyCamp      int                 `json:"army_camp"`
	SpellCapacity int                 `json:"spell_capacity"`
	SiegeCapacity int                 `json:"siege_capacity"`
	Troops        []LaunchLoadoutUnit `json:"troops"`
	Spells        []LaunchLoadoutUnit `json:"spells"`
	Siege         []LaunchLoadoutUnit `json:"siege"`
}

// LaunchArmyOption is one army card on the launch screen.
type LaunchArmyOption struct {
	File         string        `json:"file"` // strategies/<file>
	Key          string        `json:"key"`  // valk | edrag
	Name         string        `json:"name"`
	Pitch        string        `json:"pitch"` // one line, plain language
	LootPerHour  int64         `json:"loot_per_hour"`
	Measured     bool          `json:"measured"` // false = placeholder estimate
	Raids        int           `json:"raids"`    // raids behind a measured estimate
	Recommended  bool          `json:"recommended"`
	ArmySlot     int           `json:"army_slot"`
	EndAtPercent int           `json:"end_at_percent"`
	Loadout      LaunchLoadout `json:"loadout"`
}

// LaunchOptions is everything the launch screen renders.
type LaunchOptions struct {
	Options       []LaunchArmyOption `json:"options"`
	AdaptiveLoot  bool               `json:"adaptive_loot"`
	AdaptiveReady bool               `json:"adaptive_ready"`
	Samples       int                `json:"samples"`
	MinSamples    int                `json:"min_samples"`
	MaxArmySlots  int                `json:"max_army_slots"`
	MinGold       int                `json:"min_gold"`
	MinElixir     int                `json:"min_elixir"`
	MinDark       int                `json:"min_dark"`
	DryRun        bool               `json:"dry_run"`
}

// placeholderLootPerHour is a stand-in until real numbers exist.
//
// ponytail: these two numbers are illustrative, not measured. The estimator
// below already prefers the player's own raid history (median loot per raid
// over median cycle time) once a profile has enough attacks; the placeholder is
// only the first-launch answer, when there is no history at all. Replacing it
// with telemetry needs no code change here — just raids.
var placeholderLootPerHour = map[string]int64{
	"valk":  5_200_000,
	"edrag": 4_600_000,
}

// launchPitch is the one-line "what is this army" copy.
var launchPitch = map[string]string{
	"valk":  "Valkyries ring the base, earthquakes crack it open, heroes finish. Fast cycles, cheap to retrain.",
	"edrag": "Balloon and E-Drag with authored per-unit drops. Slower cycles, bigger payouts on dense bases.",
}

// minMeasuredRaids is the history a measured estimate needs. Below it a couple
// of lucky raids would outrank an army on noise alone.
const minMeasuredRaids = 8

// GetLaunchOptions builds the launch screen: the armies the app ships, an
// estimated loot per hour for each, and which one to recommend.
func (a *App) GetLaunchOptions() LaunchOptions {
	cfg := config.LoadOrDefault("config.json")
	plans := a.GetArmyPlans()
	history := a.GetAttackHistory()

	options := make([]LaunchArmyOption, 0, len(placeholderLootPerHour))
	for _, key := range []string{"valk", "edrag"} {
		plan, ok := findPlan(plans, key)
		if !ok {
			continue
		}
		perHour, measured, raids := estimateLootPerHour(history, key, placeholderLootPerHour[key])
		options = append(options, LaunchArmyOption{
			File:         plan.File,
			Key:          key,
			Name:         plan.Name,
			Pitch:        launchPitch[key],
			LootPerHour:  perHour,
			Measured:     measured,
			Raids:        raids,
			ArmySlot:     plan.ArmySlot,
			EndAtPercent: plan.EndAtPercent,
			Loadout:      planLoadout(plan.File),
		})
	}

	// The recommendation follows the estimate: the army that earns more per
	// hour wins, and a measured estimate beats a placeholder at equal value.
	best := -1
	for i := range options {
		if best == -1 ||
			options[i].LootPerHour > options[best].LootPerHour ||
			(options[i].LootPerHour == options[best].LootPerHour && options[i].Measured && !options[best].Measured) {
			best = i
		}
	}
	if best >= 0 {
		options[best].Recommended = true
	}

	return LaunchOptions{
		Options:       options,
		AdaptiveLoot:  cfg.Search.AutoLoot,
		AdaptiveReady: false,
		Samples:       0,
		MinSamples:    adaptiveMinSamples,
		MaxArmySlots:  maxArmySlots,
		MinGold:       cfg.Search.MinLootGold,
		MinElixir:     cfg.Search.MinLootElixir,
		MinDark:       cfg.Search.MinLootDarkElixir,
		DryRun:        cfg.Upgrade.DryRun,
	}
}

// maxArmySlots is how many saved-army recipes the game holds. The launch screen
// offers exactly these, so a slot the player picks is one the game can show.
const maxArmySlots = 4

// adaptiveMinSamples is the number of searched bases auto-loot needs before it
// trusts its own numbers. Mirrors internal/autotune's default.
const adaptiveMinSamples = 40

// planLoadout reads the strategy's optional loadout block, i.e. the army the
// player has to save into the recipe before the bot can run it.
func planLoadout(file string) LaunchLoadout {
	strat, err := strategy.ParseYAML(paths.Resolve(filepath.Join("strategies", filepath.Base(filepath.ToSlash(file)))))
	if err != nil || strat.Loadout == nil {
		return LaunchLoadout{}
	}
	convert := func(units []strategy.LoadoutUnit) []LaunchLoadoutUnit {
		out := make([]LaunchLoadoutUnit, 0, len(units))
		for _, unit := range units {
			out = append(out, LaunchLoadoutUnit{Name: unit.Name, Count: unit.Count, Slots: unit.Slots})
		}
		return out
	}
	return LaunchLoadout{
		ArmyCamp:      strat.Loadout.ArmyCamp,
		SpellCapacity: strat.Loadout.SpellCapacity,
		SiegeCapacity: strat.Loadout.SiegeCapacity,
		Troops:        convert(strat.Loadout.Troops),
		Spells:        convert(strat.Loadout.Spells),
		Siege:         convert(strat.Loadout.Siege),
	}
}

// ChooseArmy applies a launch-screen choice: the strategy to run, whether
// adaptive loot tuning is on, and which saved-recipe slot holds that army
// (0 keeps whatever the strategy YAML / config already says).
func (a *App) ChooseArmy(file string, adaptiveLoot bool, armySlot int) error {
	base := filepath.Base(filepath.ToSlash(file))
	if !strings.HasSuffix(base, ".yaml") {
		return fmt.Errorf("army file must be a strategy .yaml, got %q", file)
	}
	// Accept both "strategies/x.yaml" and an absolute path; store the resolved
	// absolute path, which is what config.json holds by default.
	resolved := paths.Resolve(filepath.Join("strategies", base))
	if _, err := os.Stat(resolved); err != nil {
		return fmt.Errorf("army file %q is not on disk: %w", base, err)
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	if armySlot < 0 || armySlot > maxArmySlots {
		return fmt.Errorf("army slot must be 0 (keep) or 1-%d, got %d", maxArmySlots, armySlot)
	}

	cfg := config.LoadOrDefault("config.json")
	cfg.Attack.StrategyFile = resolved
	cfg.Search.AutoLoot = adaptiveLoot
	if armySlot > 0 {
		if cfg.Attack.StrategySlots == nil {
			cfg.Attack.StrategySlots = make(map[string]int)
		}
		cfg.Attack.StrategySlots[base] = armySlot
	}
	if a.bot != nil {
		a.bot.UpdateConfig(cfg)
	}
	return writeConfigFile(cfg)
}

// findPlan matches an army key against the strategy file names.
func findPlan(plans []ArmyPlan, key string) (ArmyPlan, bool) {
	for _, plan := range plans {
		if strings.Contains(strings.ToLower(plan.File), key) {
			return plan, true
		}
	}
	return ArmyPlan{}, false
}

// estimateLootPerHour returns loot per hour for one army, preferring the
// player's own raids: median loot of a raid divided by the median gap between
// raids. Median rather than mean because a single 1-star raid should not move
// the headline number, and both are cheap over a profile's history.
//
// The third return is the raid count behind a measured estimate (0 when the
// placeholder is used), so the UI can label the number honestly.
func estimateLootPerHour(reports []bot.AttackReport, key string, placeholder int64) (int64, bool, int) {
	matching := make([]bot.AttackReport, 0, len(reports))
	for _, report := range reports {
		if strings.Contains(strings.ToLower(report.Strategy), key) {
			matching = append(matching, report)
		}
	}
	if len(matching) < minMeasuredRaids {
		return placeholder, false, len(matching)
	}

	loot := make([]int, 0, len(matching))
	times := make([]time.Time, 0, len(matching))
	for _, report := range matching {
		loot = append(loot, report.GoldStolen+report.ElixirStolen+report.DarkElixirStolen)
		if at, err := time.Parse(time.RFC3339, report.Timestamp); err == nil {
			times = append(times, at)
		}
	}

	medianLoot := medianInt(loot)
	if medianLoot <= 0 || len(times) < minMeasuredRaids {
		return placeholder, false, len(matching)
	}

	sort.Slice(times, func(i, j int) bool { return times[i].Before(times[j]) })
	gaps := make([]int, 0, len(times)-1)
	for i := 1; i < len(times); i++ {
		secs := int(times[i].Sub(times[i-1]).Seconds())
		// A gap outside this window is a paused session or a clock jump, not a
		// raid cycle; dropping it keeps a coffee break out of the estimate.
		if secs >= 20 && secs <= 900 {
			gaps = append(gaps, secs)
		}
	}
	if len(gaps) == 0 {
		return placeholder, false, len(matching)
	}

	cycle := medianInt(gaps)
	if cycle <= 0 {
		return placeholder, false, len(matching)
	}
	perHour := int64(medianLoot) * 3600 / int64(cycle)
	if perHour <= 0 {
		return placeholder, false, len(matching)
	}
	return perHour, true, len(matching)
}

// medianInt returns the median of a copy of values (0 for an empty slice).
func medianInt(values []int) int {
	if len(values) == 0 {
		return 0
	}
	sorted := append([]int(nil), values...)
	sort.Ints(sorted)
	mid := len(sorted) / 2
	if len(sorted)%2 == 0 {
		return (sorted[mid-1] + sorted[mid]) / 2
	}
	return sorted[mid]
}
