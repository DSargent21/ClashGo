package main

import (
	"testing"

	"github.com/Ducky705/ClashGO/internal/config"
)

func TestGetArmyPlansReadsAuthoredStrategies(t *testing.T) {
	t.Setenv("CLASHGO_CONFIG_DIR", t.TempDir())

	plans := (&App{}).GetArmyPlans()
	if len(plans) < 2 {
		t.Fatalf("GetArmyPlans returned %d plans, want at least 2", len(plans))
	}

	var auto, valk *ArmyPlan
	for i := range plans {
		switch plans[i].File {
		case "auto_edrag_rush.yaml":
			auto = &plans[i]
		case "valk_spam.yaml":
			valk = &plans[i]
		}
	}
	if auto == nil || valk == nil {
		t.Fatalf("missing strategy plan: auto=%v valk=%v", auto != nil, valk != nil)
	}
	if !auto.Active {
		t.Fatal("default strategy should mark auto_edrag_rush.yaml active")
	}
	if len(auto.Phases) != 6 || len(auto.Phases[3].Units) != 2 || auto.Phases[3].Units[0].Amount != "5" {
		t.Fatalf("auto EDrag phases do not match YAML: %+v", auto.Phases)
	}
	if valk.ArmySlot != 4 || valk.EndAtPercent != 50 {
		t.Fatalf("valk effective settings = slot %d end %d, want 4/50", valk.ArmySlot, valk.EndAtPercent)
	}
}

func TestArmyControlsFlowThroughToPlans(t *testing.T) {
	t.Setenv("CLASHGO_CONFIG_DIR", t.TempDir())
	a := &App{}

	if err := a.SetStrategyArmySlot("valk_spam.yaml", 2); err != nil {
		t.Fatalf("SetStrategyArmySlot: %v", err)
	}
	if err := a.SetStrategyAutoEnd("valk_spam.yaml", false); err != nil {
		t.Fatalf("SetStrategyAutoEnd: %v", err)
	}
	if err := a.SaveConfig(400000, 400000, 2000, false, "valk_spam.yaml", true, 30); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}

	plans := a.GetArmyPlans()
	var valk *ArmyPlan
	for i := range plans {
		if plans[i].File == "valk_spam.yaml" {
			valk = &plans[i]
			break
		}
	}
	if valk == nil {
		t.Fatal("valk_spam.yaml missing from plans")
	}
	if !valk.Active {
		t.Fatal("SaveConfig did not mark the selected strategy active")
	}
	if valk.ArmySlot != 2 || valk.EndAtPercent != 0 {
		t.Fatalf("plan overrides = slot %d end %d, want 2/0", valk.ArmySlot, valk.EndAtPercent)
	}
}

// TestSavedArmySlotSelectsAllFourRecipes pins the slot selector for every slot
// the game holds: the Army page (SetStrategyArmySlot) and the launch screen
// (ChooseArmy) must both accept 1-4, the plan the UI renders must read the
// value back, and picking one strategy's slot must not move another's.
func TestSavedArmySlotSelectsAllFourRecipes(t *testing.T) {
	t.Setenv("CLASHGO_CONFIG_DIR", t.TempDir())
	a := NewApp()

	slotOf := func(file string) int {
		for _, plan := range a.GetArmyPlans() {
			if plan.File == file {
				return plan.ArmySlot
			}
		}
		t.Fatalf("%s missing from plans", file)
		return 0
	}

	for slot := 1; slot <= maxArmySlots; slot++ {
		if err := a.SetStrategyArmySlot("strategies/valk_spam.yaml", slot); err != nil {
			t.Fatalf("SetStrategyArmySlot(%d): %v", slot, err)
		}
		if got := config.LoadOrDefault("config.json").Attack.StrategySlots["valk_spam.yaml"]; got != slot {
			t.Fatalf("SetStrategyArmySlot(%d) stored %d", slot, got)
		}
		if got := slotOf("valk_spam.yaml"); got != slot {
			t.Fatalf("army plan slot = %d after selecting %d", got, slot)
		}
		if got := slotOf("auto_edrag_rush.yaml"); got != 1 {
			t.Fatalf("selecting valk slot %d moved edrag to %d, want its own 1", slot, got)
		}
	}

	for slot := 1; slot <= maxArmySlots; slot++ {
		if err := a.ChooseArmy("strategies/valk_spam.yaml", false, slot); err != nil {
			t.Fatalf("ChooseArmy(%d): %v", slot, err)
		}
		found := false
		for _, option := range a.GetLaunchOptions().Options {
			if option.Key != "valk" {
				continue
			}
			found = true
			if option.ArmySlot != slot {
				t.Fatalf("launch option slot = %d after choosing %d", option.ArmySlot, slot)
			}
		}
		if !found {
			t.Fatal("valk missing from launch options")
		}
		if got := slotOf("valk_spam.yaml"); got != slot {
			t.Fatalf("army plan slot = %d after launch choose %d", got, slot)
		}
	}
}

func TestEffectivePlanEnd(t *testing.T) {
	cases := []struct {
		name      string
		overrides map[string]bool
		base      string
		planName  string
		yamlEnd   int
		want      int
	}{
		{"yaml threshold", nil, "auto_edrag_rush.yaml", "Auto EDrag Rush", 0, 0},
		{"valk defaults to 50", nil, "valk_spam.yaml", "Valkyrie Earthquake Spam", 50, 50},
		{"explicit off", map[string]bool{"valk_spam.yaml": false}, "valk_spam.yaml", "Valkyrie Earthquake Spam", 50, 0},
		{"explicit on adds default", map[string]bool{"auto_edrag_rush.yaml": true}, "auto_edrag_rush.yaml", "Auto EDrag Rush", 0, 50},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := effectivePlanEnd(tc.overrides, tc.base, tc.planName, tc.yamlEnd); got != tc.want {
				t.Fatalf("effectivePlanEnd() = %d, want %d", got, tc.want)
			}
		})
	}
}
