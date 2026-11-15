package village

import "testing"

// A floor that rises then falls would make a higher Town Hall search more than a
// lower one: the curve has to be monotonic, and the top of the table has to hold
// for every Town Hall above it.
func TestSuggestedFloorsMonotonic(t *testing.T) {
	prev := 0
	for th := 1; th <= 18; th++ {
		gold, elixir, _ := SuggestedFloors(th)
		if gold != elixir {
			t.Fatalf("TH%d: gold %d != elixir %d", th, gold, elixir)
		}
		if gold < prev {
			t.Fatalf("TH%d floor %d is below TH%d's %d", th, gold, th-1, prev)
		}
		prev = gold
	}
	if gold, _, _ := SuggestedFloors(18); gold != lootFloorTop {
		t.Fatalf("TH18 = %d, want the top entry %d", gold, lootFloorTop)
	}
}

func TestSuggestedFloorsUnknownTownHallIsNeutral(t *testing.T) {
	for _, th := range []int{0, -3} {
		gold, elixir, dark := SuggestedFloors(th)
		if gold != 0 || elixir != 0 || dark != 0 {
			t.Fatalf("TH%d = (%d,%d,%d), want zeros so the caller keeps its manual values", th, gold, elixir, dark)
		}
	}
}

func TestSuggestedFloorsDarkIsZero(t *testing.T) {
	for th := 1; th <= 18; th++ {
		if _, _, dark := SuggestedFloors(th); dark != 0 {
			t.Fatalf("TH%d dark floor = %d; a DE floor would skip most bases", th, dark)
		}
	}
}

func TestClampToReachable(t *testing.T) {
	gold, elixir, dark, capped := ClampToReachable(9_000_000, 800_000, 120_000)
	if gold != MostGold || elixir != 800_000 || dark != MostDE {
		t.Fatalf("clamped to (%d,%d,%d), want (%d,800000,%d)", gold, elixir, dark, MostGold, MostDE)
	}
	if len(capped) != 2 || capped[0] != "gold" || capped[1] != "dark elixir" {
		t.Fatalf("capped = %v, want gold and dark elixir", capped)
	}

	g, e, d, capped := ClampToReachable(300_000, 400_000, 1_000)
	if g != 300_000 || e != 400_000 || d != 1_000 || len(capped) != 0 {
		t.Fatalf("reachable floors were changed: (%d,%d,%d) capped=%v", g, e, d, capped)
	}
}
