package attack

import (
	"testing"

	"github.com/Ducky705/ClashGO/pkg/strategy"
	"github.com/rs/zerolog"
)

// ---------------------------------------------------------------------------
// Deferred ability pass — the valk_run6 defect (2026-09-23)
//
// HeroManager auto-activated abilities ~60ms after the heroes dropped,
// INSIDE the "Siege & Heroes" phase and BEFORE the Earthquakes phase. That
// second tap on the hero icon re-selects the hero's card, so the next
// slot-selection tap landed on the still-highlighted hero icon instead of
// the earthquake card (live: the siege slot got tapped instead). The fix
// routes every ability plan through a deferred pass fired only when the
// strategy reaches an explicit "Abilities" phase — or after the last
// phase, for strategies like auto_edrag_rush that declare
// pattern:Ability units inline inside the Heroes phase.
// ---------------------------------------------------------------------------

// TestPlanPhase_CollectsInlineAbilityUnits ensures pattern:Ability units
// declared inside a Heroes phase (auto_edrag_rush shape) resolve into
// ability plans the orchestrator can defer — they must not vanish.
func TestPlanPhase_CollectsInlineAbilityUnits(t *testing.T) {
	sm := newTestSlotManager([]*TrackedSlot{
		{TroopSlot: TroopSlot{X: 287, Y: 682, Category: "Hero"}, UnitName: "barbarian king", Confidence: 0.95},
	})
	dp := NewDeployPlanner(sm, PrecisionConfig{}, "BottomRight", 860, 732, zerolog.Nop())

	plan := dp.planPhase(strategy.Phase{
		Name: "Heroes",
		Units: []strategy.Unit{
			{Name: "Barbarian King", Amount: "All"},
			{Name: "Barbarian King", Amount: "All", Pattern: "Ability"},
		},
	})

	heroes := ResolveHeroTargets(plan)
	abilities := ResolveAbilityTargets(plan)
	if len(heroes) != 1 {
		t.Fatalf("got %d hero plans, want 1", len(heroes))
	}
	if len(abilities) != 1 {
		t.Fatalf("pattern:Ability unit inside a Heroes phase produced no ability plan — it would never activate")
	}
	if abilities[0].Slot == nil || abilities[0].Slot.X != 287 {
		t.Fatalf("ability plan did not resolve to the hero's slot: %+v", abilities[0].Slot)
	}
}

// TestShippedStrategies_AllAbilitiesResolvable walks every shipped strategy
// YAML and verifies each pattern:Ability unit resolves to a hero slot
// through the planner contract the deferred pass depends on (abilities
// without slots are skipped by fireAbilities, so an unresolvable ability
// would silently never fire).
func TestShippedStrategies_AllAbilitiesResolvable(t *testing.T) {
	for _, path := range []string{"../../assets/strategies/valk_spam.yaml", "../../assets/strategies/auto_edrag_rush.yaml"} {
		s, err := strategy.ParseYAML(path)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}

		// Build a slot manager carrying every unit the strategy names
		// (heroes as heroes; the EQ card stays UNNAMED to prove the
		// spell fallback covers it — the valk_run6 shape).
		var slots []*TrackedSlot
		x := 62
		for _, phase := range s.Phases {
			for _, u := range phase.Units {
				name := u.Name
				if name == "Earthquake Spell" {
					slots = append(slots, &TrackedSlot{TroopSlot: TroopSlot{X: x, Y: 682, Category: "Spell"}})
				} else {
					slots = append(slots, &TrackedSlot{TroopSlot: TroopSlot{X: x, Y: 682}, UnitName: name})
				}
				x += 75
			}
		}
		sm := newTestSlotManager(slots)
		dp := NewDeployPlanner(sm, PrecisionConfig{}, "BottomRight", 860, 732, zerolog.Nop())

		abilitiesWithSlots := 0
		for _, phase := range s.Phases {
			plan := dp.planPhase(phase)
			for _, up := range ResolveAbilityTargets(plan) {
				if up.Slot == nil {
					t.Errorf("%s: ability %q has no slot; the deferred pass would silently skip it", path, up.Unit.Name)
					continue
				}
				abilitiesWithSlots++
			}
		}
		if abilitiesWithSlots == 0 {
			t.Errorf("%s: no resolvable ability plans found — abilities would never fire", path)
		}
	}
}

// TestValkSpam_AbilitiesPhaseIsAfterSpells pins the ordering contract the
// orchestrator relies on: in valk_spam, the Earthquakes (spell) phase must
// precede the Abilities phase, and the planner must preserve phase order.
func TestValkSpam_AbilitiesPhaseIsAfterSpells(t *testing.T) {
	s, err := strategy.ParseYAML("../../assets/strategies/valk_spam.yaml")
	if err != nil {
		t.Fatal(err)
	}
	spellsSeen := false
	for _, phase := range s.Phases {
		switch {
		case hasSpellUnit(phase):
			spellsSeen = true
		case phase.Pattern == "Ability":
			if !spellsSeen {
				t.Fatal("valk_spam Abilities phase precedes the spell phase; the deferred pass would fire abilities before spells")
			}
			return
		}
	}
	t.Fatal("valk_spam has no Ability-pattern phase")
}

func hasSpellUnit(p strategy.Phase) bool {
	for _, u := range p.Units {
		if isSpellStatic(u.Name) {
			return true
		}
	}
	return false
}
