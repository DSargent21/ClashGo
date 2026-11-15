package village

// Loot floors by Town Hall.
//
// A static pair of search minimums is wrong for most accounts: 750k gold is a
// routine base at TH15 and a base that does not exist at TH6, where every Next
// is a skip. The curve below is the one COCFARM published from its own 2026-10
// and 2026-11 search statistics (about one base in four offering it), and it is
// the reason a low-Town-Hall account can farm at all with a table instead of a
// guess.
//
// Numbers, not code: the values are the measured shape of the game's search
// pool, which is the same for every bot that reads it.
var lootFloorByTownHall = map[int]int{
	1:  1_000,
	2:  5_000,
	3:  10_000,
	4:  30_000,
	5:  60_000,
	6:  100_000,
	7:  150_000,
	8:  200_000,
	9:  300_000,
	10: 400_000,
	11: 500_000,
	12: 600_000,
	13: 750_000,
	14: 900_000,
}

const (
	// lootFloorTop is every Town Hall above the table's last entry (15 and up).
	lootFloorTop = 900_000

	// MostLoot is more than any base offers to loot. A floor above it is a typo
	// (an extra zero), and with a floor like that the search skips every base
	// forever — the failure this cap exists to prevent.
	MostGold   = 5_000_000
	MostElixir = 5_000_000
	MostDE     = 50_000
)

// SuggestedFloors is the gold, elixir and dark elixir floor that suits the given
// Town Hall. An unknown Town Hall (0) returns zeros so the caller keeps its
// manual values instead of adopting a table entry that describes nothing.
func SuggestedFloors(townHall int) (gold, elixir, dark int) {
	if townHall < 1 {
		return 0, 0, 0
	}
	amount, ok := lootFloorByTownHall[townHall]
	switch {
	case ok:
	case townHall > 14:
		amount = lootFloorTop
	default:
		amount = lootFloorByTownHall[1]
	}
	// Dark elixir stays at zero: a TH7+ base that carries 2k DE is common, but
	// making it a floor would skip the majority of bases for one resource.
	return amount, amount, 0
}

// ClampToReachable caps any floor above what a base can actually offer and
// reports which resource names were capped. The caller logs them once: a floor
// nobody can meet is a config typo whose symptom (an endless search) looks
// exactly like a broken search.
func ClampToReachable(gold, elixir, dark int) (int, int, int, []string) {
	var capped []string
	if gold > MostGold {
		gold, capped = MostGold, append(capped, "gold")
	}
	if elixir > MostElixir {
		elixir, capped = MostElixir, append(capped, "elixir")
	}
	if dark > MostDE {
		dark, capped = MostDE, append(capped, "dark elixir")
	}
	return gold, elixir, dark, capped
}
