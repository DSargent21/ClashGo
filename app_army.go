package main

import (
	"path/filepath"
	"sort"
	"strings"

	"github.com/Ducky705/ClashGO/internal/config"
	"github.com/Ducky705/ClashGO/internal/paths"
	"github.com/Ducky705/ClashGO/pkg/strategy"
)

// ArmyPlanUnit is one authored unit line from a strategy phase.
type ArmyPlanUnit struct {
	Name   string `json:"name"`
	Amount string `json:"amount"`
}

// ArmyPlanPhase is a deployment phase exactly as authored in the YAML.
type ArmyPlanPhase struct {
	Name  string         `json:"name"`
	Units []ArmyPlanUnit `json:"units"`
}

// ArmyPlan is the actual on-disk strategy definition plus the effective
// recipe slot / auto-end values the bot will use for it and the same loot/hour
// estimate the launch screen shows (history-backed once a profile has raids).
type ArmyPlan struct {
	File         string          `json:"file"`
	Name         string          `json:"name"`
	Description  string          `json:"description"`
	Emblem       string          `json:"emblem"`
	TargetEdge   string          `json:"target_edge"`
	ArmySlot     int             `json:"army_slot"`
	EndAtPercent int             `json:"end_at_percent"`
	LootPerHour  int64           `json:"loot_per_hour"`
	Measured     bool            `json:"measured"`
	Raids        int             `json:"raids"`
	Active       bool            `json:"active"`
	Phases       []ArmyPlanPhase `json:"phases"`
}

// GetArmyPlans reads assets/strategies/*.yaml and reports what the bot will
// actually deploy. No estimates or hard-coded loadouts: phase units come from
// the YAML, while slot and auto-end honour config.json overrides.
func (a *App) GetArmyPlans() []ArmyPlan {
	cfg := config.LoadOrDefault("config.json")
	activeBase := filepath.Base(filepath.ToSlash(cfg.Attack.StrategyFile))

	plans := []ArmyPlan{}
	history := a.GetAttackHistory()
	matches, err := filepath.Glob(paths.Resolve("strategies/*.yaml"))
	if err != nil {
		return plans
	}
	sort.Strings(matches)

	for _, path := range matches {
		strat, err := strategy.ParseYAML(path)
		if err != nil {
			continue
		}

		base := filepath.Base(filepath.ToSlash(path))
		phases := make([]ArmyPlanPhase, 0, len(strat.Phases))
		for _, phase := range strat.Phases {
			units := make([]ArmyPlanUnit, 0, len(phase.Units))
			for _, unit := range phase.Units {
				units = append(units, ArmyPlanUnit{Name: unit.Name, Amount: unit.Amount})
			}
			if len(units) > 0 {
				phases = append(phases, ArmyPlanPhase{Name: phase.Name, Units: units})
			}
		}

		slot := strat.SelectedArmySlot()
		if override, ok := cfg.Attack.StrategySlots[base]; ok && override >= 1 && override <= 4 {
			slot = override
		} else if base == activeBase {
			// The bot also accepts an exact strategy-path key as a fallback.
			if override, ok := cfg.Attack.StrategySlots[cfg.Attack.StrategyFile]; ok && override >= 1 && override <= 4 {
				slot = override
			}
		}

		emblem := ""
		if len(strat.Phases) > 0 && len(strat.Phases[0].Units) > 0 {
			emblem = strat.Phases[0].Units[0].Name
		}

		// Same estimate the launch screen uses, so the two pages cannot
		// disagree about an army's loot rate.
		key := lootEstimateKey(base)
		lootPerHour, measured, raids := estimateLootPerHour(history, key, placeholderLootPerHour[key])

		plans = append(plans, ArmyPlan{
			File:         base,
			Name:         strat.Name,
			Description:  strat.Description,
			Emblem:       emblem,
			TargetEdge:   strat.TargetEdge,
			ArmySlot:     slot,
			EndAtPercent: effectivePlanEnd(cfg.Attack.StrategyAutoEnd, base, strat.Name, strat.EndAtPercent),
			LootPerHour:  lootPerHour,
			Measured:     measured,
			Raids:        raids,
			Active:       base == activeBase,
			Phases:       phases,
		})
	}
	return plans
}

// lootEstimateKey maps a strategy file to the estimate key used by history
// matching. The two shipped armies keep their placeholder keys so a fresh
// install still shows a first-launch estimate; any other strategy simply
// matches its own file stem and stays unmeasured until raids exist.
func lootEstimateKey(base string) string {
	lower := strings.ToLower(base)
	for _, key := range []string{"edrag", "valk"} {
		if strings.Contains(lower, key) {
			return key
		}
	}
	return strings.TrimSuffix(lower, ".yaml")
}

// effectivePlanEnd mirrors the bot's per-strategy auto-end resolution.
func effectivePlanEnd(overrides map[string]bool, base, name string, yamlEndAt int) int {
	enabled := yamlEndAt > 0 ||
		strings.Contains(strings.ToLower(base), "valk") ||
		strings.Contains(strings.ToLower(name), "valk")
	if override, ok := overrides[base]; ok {
		enabled = override
	}
	if !enabled {
		return 0
	}
	if yamlEndAt > 0 {
		return yamlEndAt
	}
	return 50
}
