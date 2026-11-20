//go:build !cli

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Ducky705/ClashGO/internal/autotune"
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
	File          string        `json:"file"` // strategies/<file>
	Key           string        `json:"key"`  // valk | edrag
	Name          string        `json:"name"`
	Pitch         string        `json:"pitch"`         // one line, plain language
	LootPerHour   int64         `json:"loot_per_hour"` // 0 = no estimate yet
	Measured      bool          `json:"measured"`      // true = from real raids
	Raids         int           `json:"raids"`         // raids in the measured session
	WindowStart   string        `json:"window_start,omitempty"`
	WindowEnd     string        `json:"window_end,omitempty"`
	WindowMinutes int           `json:"window_minutes"` // wall clock, training gaps included
	Recommended   bool          `json:"recommended"`
	ArmySlot      int           `json:"army_slot"`
	EndAtPercent  int           `json:"end_at_percent"`
	Loadout       LaunchLoadout `json:"loadout"`
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
	PetPriorities []string           `json:"pet_priorities"`
}

// launchPitch is the one-line "what is this army" copy.
var launchPitch = map[string]string{
	"valk":  "Valkyries ring the base, earthquakes crack it open, heroes finish. Fast cycles, cheap to retrain.",
	"edrag": "Balloon and E-Drag with authored per-unit drops. Slower cycles, bigger payouts on dense bases.",
}

// minMeasuredRaids is the history a measured estimate needs. Below it a couple
// of lucky raids would outrank an army on noise alone.
const minMeasuredRaids = 8

// lootSessionGap is where one farming session ends and the next begins. A
// training gap is part of the wall clock (the army is retraining, not earning),
// but an overnight break or a switched-off emulator is not: counting either as
// time per raid would make the rate depend on how long the app sat idle.
const lootSessionGap = 2 * time.Hour

// minMeasuredWindow rejects a "session" whose raids all landed in the same
// minute; loot divided by near-zero wall clock is not a rate.
const minMeasuredWindow = time.Minute

// GetLaunchOptions builds the launch screen: the armies the app ships, an
// estimated loot per hour for each, and which one to recommend.
func (a *App) GetLaunchOptions() LaunchOptions {
	cfg := config.LoadOrDefault("config.json")
	plans := a.GetArmyPlans()
	history := a.GetAttackHistory()
	sessions := loadSessionRecords()

	options := make([]LaunchArmyOption, 0, 2)
	for _, key := range []string{"edrag", "valk"} {
		plan, ok := findPlan(plans, key)
		if !ok {
			continue
		}
		estimate := estimateLootPerHour(history, key, sessions)
		options = append(options, LaunchArmyOption{
			File:          plan.File,
			Key:           key,
			Name:          plan.Name,
			Pitch:         launchPitch[key],
			LootPerHour:   estimate.PerHour,
			Measured:      estimate.Measured,
			Raids:         estimate.Raids,
			WindowStart:   estimate.WindowStartString(),
			WindowEnd:     estimate.WindowEndString(),
			WindowMinutes: estimate.WindowMinutes(),
			ArmySlot:      plan.ArmySlot,
			EndAtPercent:  plan.EndAtPercent,
			Loadout:       planLoadout(plan.File),
		})
	}

	// The recommendation follows measured history only. Two zero rates are not
	// a tie to break: with no raids yet the launch screen has no basis for
	// calling either army the better earner, so it lets the player choose.
	best := -1
	for i := range options {
		if !options[i].Measured {
			continue
		}
		if best == -1 || options[i].LootPerHour > options[best].LootPerHour {
			best = i
		}
	}
	if best >= 0 {
		options[best].Recommended = true
	}

	samples := len(lootStore().Samples())
	return LaunchOptions{
		Options:       options,
		AdaptiveLoot:  cfg.Search.AutoLoot,
		AdaptiveReady: samples >= autotune.DefaultOptions().MinSamples,
		Samples:       samples,
		MinSamples:    autotune.DefaultOptions().MinSamples,
		MaxArmySlots:  maxArmySlots,
		MinGold:       cfg.Search.MinLootGold,
		MinElixir:     cfg.Search.MinLootElixir,
		MinDark:       cfg.Search.MinLootDarkElixir,
		PetPriorities: launchPetPriorities(cfg.Upgrade.PetPriorities),
	}
}

// launchPetPriorities upgrades the old five-pet default to the researched
// queue without touching a custom order.
func launchPetPriorities(priorities []string) []string {
	if len(priorities) == 0 || config.IsLegacyPetOrder(priorities) {
		return config.DefaultPetPriorities()
	}
	return priorities
}

// maxArmySlots is how many saved-army recipes the game holds. The launch screen
// offers exactly these, so a slot the player picks is one the game can show.
const maxArmySlots = 4

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

// lootEstimate is one army's measured loot rate: total loot of the latest
// farming session divided by that session's wall clock. WindowStart/WindowEnd
// travel to the UI so the number can say which raids it is based on instead of
// looking like an authored constant.
type lootEstimate struct {
	PerHour     int64
	Measured    bool
	Raids       int
	WindowStart time.Time
	WindowEnd   time.Time
}

// WindowStartString is RFC3339 for the API, empty when there is no estimate.
func (e lootEstimate) WindowStartString() string {
	if e.WindowStart.IsZero() {
		return ""
	}
	return e.WindowStart.Format(time.RFC3339)
}

// WindowEndString is RFC3339 for the API, empty when there is no estimate.
func (e lootEstimate) WindowEndString() string {
	if e.WindowEnd.IsZero() {
		return ""
	}
	return e.WindowEnd.Format(time.RFC3339)
}

// WindowMinutes is the measured wall-clock span, rounded to the nearest minute.
func (e lootEstimate) WindowMinutes() int {
	if e.WindowStart.IsZero() || e.WindowEnd.IsZero() {
		return 0
	}
	return int((e.WindowEnd.Sub(e.WindowStart) + 30*time.Second) / time.Minute)
}

// estimateLootPerHour measures one army from the player's own attack history.
//
// Preferred denominator is the bot's real session: start to stop, with startup,
// search, training and teardown all counting as time the army was not earning.
// Raids are those for this army that landed inside that session.
//
// History written before sessions.json existed has no run timestamps; for it
// the estimator keeps the older first-to-last-raid span, so an upgrade does not
// blank every rate until the next run.
//
// No history, too few raids, a zero-length window or zero loot all mean no
// estimate at all: the caller shows "No raids yet" rather than an invented
// number.
func estimateLootPerHour(reports []bot.AttackReport, key string, sessions []sessionRecord) lootEstimate {
	timed := make([]timedRaid, 0, len(reports))
	for _, report := range reports {
		if !strings.Contains(strings.ToLower(report.Strategy), key) {
			continue
		}
		at, err := time.Parse(time.RFC3339, report.Timestamp)
		if err != nil {
			continue
		}
		timed = append(timed, timedRaid{
			at:   at,
			loot: int64(report.GoldStolen + report.ElixirStolen + report.DarkElixirStolen),
		})
	}
	sort.Slice(timed, func(i, j int) bool { return timed[i].at.Before(timed[j].at) })
	if len(timed) == 0 {
		return lootEstimate{}
	}

	if started, ended, ok := latestSessionFor(sessions, timed); ok {
		inside := make([]timedRaid, 0, len(timed))
		for _, raid := range timed {
			if !raid.at.Before(started) && !raid.at.After(ended) {
				inside = append(inside, raid)
			}
		}
		return measureRaids(inside, started, ended)
	}

	// Fallback for history without session timestamps: latest run of raids,
	// split where the bot was idle long enough that the "session" is over.
	start := 0
	for i := 1; i < len(timed); i++ {
		if timed[i].at.Sub(timed[i-1].at) > lootSessionGap {
			start = i
		}
	}
	session := timed[start:]
	return measureRaids(session, session[0].at, session[len(session)-1].at)
}

// latestSessionFor returns the most recent completed session that contains at
// least one raid for this army. Records are validated: an interrupted write or
// a hand-edited entry without both bounds is ignored.
func latestSessionFor(sessions []sessionRecord, raids []timedRaid) (time.Time, time.Time, bool) {
	var bestStart, bestEnd time.Time
	found := false
	for _, record := range sessions {
		started, ended, ok := record.parse()
		if !ok {
			continue
		}
		contains := false
		for _, raid := range raids {
			if !raid.at.Before(started) && !raid.at.After(ended) {
				contains = true
				break
			}
		}
		if contains && (!found || started.After(bestStart)) {
			bestStart, bestEnd, found = started, ended, true
		}
	}
	return bestStart, bestEnd, found
}

// measureRaids divides the raids' stolen loot by the given wall clock.
func measureRaids(raids []timedRaid, started, ended time.Time) lootEstimate {
	if len(raids) < minMeasuredRaids {
		return lootEstimate{Raids: len(raids)}
	}
	window := ended.Sub(started)
	if window < minMeasuredWindow {
		return lootEstimate{Raids: len(raids)}
	}
	var total int64
	for _, raid := range raids {
		total += raid.loot
	}
	if total <= 0 {
		return lootEstimate{Raids: len(raids)}
	}

	estimate := lootEstimate{
		PerHour:     total * 3600 / int64(window.Seconds()),
		Raids:       len(raids),
		WindowStart: started,
		WindowEnd:   ended,
	}
	estimate.Measured = estimate.PerHour > 0
	if !estimate.Measured {
		return lootEstimate{Raids: len(raids)}
	}
	return estimate
}

// timedRaid is one history row reduced to what the rate needs.
type timedRaid struct {
	at   time.Time
	loot int64
}
