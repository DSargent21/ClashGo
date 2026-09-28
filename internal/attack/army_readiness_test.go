package attack

import (
	"reflect"
	"testing"

	"github.com/Ducky705/ClashGO/pkg/strategy"
)

// The full-army gate must act ONLY on positive evidence of an empty card: a
// MERGE-CONFIRMED zero in the counts map (present-in-map). An unreadable card
// is absent, never 0 — so absent counts must never block an attack. Heroes and
// siege machines carry no trustworthy count badge and are always excluded.

func readinessPlan(units ...UnitPlan) []PhasePlan {
	return []PhasePlan{{UnitPlans: units}}
}

func readinessUnit(name, category string, x int) UnitPlan {
	return UnitPlan{
		Unit: strategy.Unit{Name: name},
		Slot: &TrackedSlot{TroopSlot: TroopSlot{X: x, Y: 682, Category: category}},
	}
}

func TestArmyReadinessGap_PresentZeroBlocks(t *testing.T) {
	sm := &SlotManager{}
	plans := readinessPlan(readinessUnit("barb", "Troop", 100))

	got := armyReadinessGap(plans, map[int]int{100: 0}, sm)
	if want := []string{"barb"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("gap = %v, want %v", got, want)
	}

	got = armyReadinessGap(plans, map[int]int{100: 5}, sm)
	if len(got) != 0 {
		t.Fatalf("counted card blocked: %v", got)
	}
}

// Absent from the map = unreadable, NOT empty. Silence must never gate.
func TestArmyReadinessGap_AbsentCountsNeverBlock(t *testing.T) {
	sm := &SlotManager{}
	plans := readinessPlan(
		readinessUnit("barb", "Troop", 100),
		readinessUnit("archer", "Troop", 200),
		readinessUnit("lightning", "Spell", 300),
	)

	cases := []struct {
		name   string
		counts map[int]int
	}{
		{"nil counts", nil},
		{"empty counts", map[int]int{}},
		{"other slots only", map[int]int{999: 0}},
		{"all present and non-zero", map[int]int{100: 3, 200: 7, 300: 2}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := armyReadinessGap(plans, tc.counts, sm); len(got) != 0 {
				t.Fatalf("gap = %v, want none (only proven empties gate)", got)
			}
		})
	}
}

// A zero on ONE card blocks only that card; siblings stay out of the list.
func TestArmyReadinessGap_ReportsOnlyEmptyCards(t *testing.T) {
	sm := &SlotManager{}
	plans := readinessPlan(
		readinessUnit("barb", "Troop", 100),
		readinessUnit("giant", "Troop", 200),
		readinessUnit("heal", "Spell", 300),
	)
	counts := map[int]int{100: 4, 200: 0, 300: 0}

	got := armyReadinessGap(plans, counts, sm)
	if want := []string{"giant", "heal"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("gap = %v, want %v", got, want)
	}
}

// Heroes and siege machines have no trustworthy count badge: their cards are
// silent, and silence there is not evidence.
func TestArmyReadinessGap_ExcludesHeroAndSiege(t *testing.T) {
	sm := &SlotManager{}
	plans := readinessPlan(
		readinessUnit("archer_queen", "Hero", 400),
		readinessUnit("log_launcher", "Siege", 500),
	)
	counts := map[int]int{400: 0, 500: 0}

	if got := armyReadinessGap(plans, counts, sm); len(got) != 0 {
		t.Fatalf("hero/siege zero gated the deploy: %v", got)
	}
}

func TestArmyReadinessGap_EdgeCases(t *testing.T) {
	sm := &SlotManager{}
	plans := readinessPlan(
		readinessUnit("barb", "Troop", 100),
		readinessUnit("barb", "Troop", 100),                     // same card, second phase
		UnitPlan{Unit: strategy.Unit{Name: "ghost"}, Slot: nil}, // no slot: not on the bar
	)

	got := armyReadinessGap(plans, map[int]int{100: 0}, sm)
	if want := []string{"barb"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("gap = %v, want %v (deduped, nil slot skipped)", got, want)
	}

	if got := armyReadinessGap(plans, map[int]int{100: 0}, nil); got != nil {
		t.Fatalf("nil slot manager: gap = %v, want nil", got)
	}
}
