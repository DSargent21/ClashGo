package bot

import (
	"testing"

	"github.com/Ducky705/ClashGO/internal/config"
	"github.com/Ducky705/ClashGO/internal/vision"
)

func TestClassifyItemCategory(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		// Heroes
		{"Barbarian King", "heroes"},
		{"Archer Queen", "heroes"},
		{"Grand Warden", "heroes"},
		{"Royal Champion", "heroes"},
		{"Minion Prince", "heroes"},

		// Pets
		{"Mighty Yak", "pets"},
		{"Frosty", "pets"},
		{"Angry Jelly", "pets"},
		{"Electro Owl", "pets"},
		{"ElectRO OWI", "pets"},
		{"L.A.S.S.I", "pets"},
		{"Unicorn", "pets"},
		{"Poison Lizard", "pets"}, // must classify as pets, not spells
		{"Diggy", "pets"},
		{"Spirit Fox", "pets"},
		{"Phoenix", "pets"},

		// Spells
		{"Poison Spell", "spells"},
		{"Earthquake Spell", "spells"},
		{"Lightning Spell", "spells"},
		{"Healing Spell", "spells"},
		{"Rage Spell", "spells"},
		{"Jump Spell", "spells"},
		{"Freeze Spell", "spells"},
		{"Clone Spell", "spells"},
		{"Invisibility Spell", "spells"},
		{"Revive Spell", "spells"},

		// Siege
		{"Stone Slammer", "siege"},
		{"Wall Wrecker", "siege"},
		{"Battle Blimp", "siege"},
		{"Log Launcher", "siege"},
		{"Flame Flinger", "siege"},
		{"Battle Drill", "siege"},

		// Troops
		{"Valkyrie", "troops"},
		{"Wall Breaker", "troops"},
		{"Minion", "troops"},
		{"Golem", "troops"},
		{"Hog Rider", "troops"},
		{"Root Rider", "troops"},
		{"Druid", "troops"},
		{"Electro Titan", "troops"},

		// Defenses
		{"Cannon", "defenses"},
		{"Archer Tower", "defenses"},
		{"Wizard Tower", "defenses"},
		{"Air Defense", "defenses"},
		{"Monolith", "defenses"},
		{"MONOlit H", "defenses"}, // OCR split
		{"Revenge Tower", "defenses"},
		{"Scattershot", "defenses"},
		{"Eagle Artillery", "defenses"},

		// Resources
		{"Elixir Collector", "resources"},
		{"Gold Mine", "resources"},
		{"Dark Elixir Drill", "resources"},
		{"Elixir Storage", "resources"},

		// Army
		{"Barracks", "army"},
		{"Army Camp", "army"},
		{"Pet House", "army"},
		{"Blacksmith", "army"},

		// Traps
		{"Giant Bomb", "traps"},
		{"Air Mine", "traps"},
		{"Seeking Air Mine", "traps"},
		{"Tornado Trap", "traps"},
	}

	for _, tc := range tests {
		got := classifyItemCategory(tc.input)
		if got != tc.expected {
			t.Errorf("classifyItemCategory(%q) = %q; want %q", tc.input, got, tc.expected)
		}
	}
}

func TestIsDurationText(t *testing.T) {
	durations := []string{
		"11d 12H", "4d 12H", "5d", "1d 12H", "12H", "16 H", "10H", "8d", "7d :",
		"2h 18m", "1m 0s", "4d 12h ©", "5d ø", "4d G", "12H •", "Sd",
	}
	for _, d := range durations {
		if !isDurationText(d) {
			t.Errorf("isDurationText(%q) = false; want true", d)
		}
		if cost := parseResourceCost(d); cost != 0 {
			t.Errorf("parseResourceCost(%q) = %d; want 0", d, cost)
		}
	}

	nonDurations := []string{
		"105 000", "6 500 000", "55 000", "15 000", "6 000", "4 000", "2 000",
		"13 000", "6 100 000", "700", "250 000", "220 000", "80 000", "145 000",
		"160 000", "90 000", "125 000", "60 000",
	}
	for _, nd := range nonDurations {
		if isDurationText(nd) {
			t.Errorf("isDurationText(%q) = true; want false", nd)
		}
		if cost := parseResourceCost(nd); cost <= 0 {
			t.Errorf("parseResourceCost(%q) = %d; want > 0", nd, cost)
		}
	}
}

func TestParseCurrency(t *testing.T) {
	if got := parseCurrency("heroes", "Grand Warden"); got != "elixir" {
		t.Errorf("Grand Warden currency = %q; want elixir", got)
	}
	if got := parseCurrency("heroes", "Barbarian King"); got != "dark_elixir" {
		t.Errorf("Barbarian King currency = %q; want dark_elixir", got)
	}
	if got := parseCurrency("pets", "Frosty"); got != "dark_elixir" {
		t.Errorf("Frosty currency = %q; want dark_elixir", got)
	}
	if got := parseCurrency("troops", "Valkyrie"); got != "dark_elixir" {
		t.Errorf("Valkyrie currency = %q; want dark_elixir", got)
	}
	if got := parseCurrency("troops", "Wall Breaker"); got != "elixir" {
		t.Errorf("Wall Breaker currency = %q; want elixir", got)
	}
	if got := parseCurrency("spells", "Poison Spell"); got != "dark_elixir" {
		t.Errorf("Poison Spell currency = %q; want dark_elixir", got)
	}
	if got := parseCurrency("spells", "Lightning Spell"); got != "elixir" {
		t.Errorf("Lightning Spell currency = %q; want elixir", got)
	}
	if got := parseCurrency("defenses", "Monolith"); got != "dark_elixir" {
		t.Errorf("Monolith currency = %q; want dark_elixir", got)
	}
	if got := parseCurrency("defenses", "Cannon"); got != "gold" {
		t.Errorf("Cannon currency = %q; want gold", got)
	}
	if got := parseCurrency("resources", "Elixir Collector"); got != "gold" {
		t.Errorf("Elixir Collector currency = %q; want gold", got)
	}
}

func TestParseBuilderOverviewOCR_Mock(t *testing.T) {
	regions := []vision.TextRegion{
		{X: 717, Y: 263, W: 188, H: 15, Text: "Suggested upgrades"},
		{X: 665, Y: 297, W: 160, H: 24, Text: "BaRbaRiaN King"},
		{X: 961, Y: 297, W: 30, H: 15, Text: "1d"},
		{X: 917, Y: 321, W: 76, H: 20, Text: "13 000"},
		{X: 665, Y: 371, W: 156, H: 19, Text: "GRaNd WaRdeN"},
		{X: 961, Y: 371, W: 30, H: 15, Text: "1d"},
		{X: 883, Y: 391, W: 104, H: 21, Text: "6 100 000"},
		{X: 665, Y: 441, W: 194, H: 19, Text: "ElixiR CollectoR"},
		{X: 937, Y: 443, W: 50, H: 15, Text: "1m 0s"},
		{X: 943, Y: 463, W: 44, H: 20, Text: "700"},
		{X: 629, Y: 723, W: 32, H: 18, Text: "2/5"},
		{X: 737, Y: 723, W: 34, H: 18, Text: "1/1"},
		{X: 953, Y: 723, W: 34, H: 15, Text: "0/1"},
	}

	stats := ParseBuilderOverviewOCR(regions)
	if stats.FreeBuilders != 2 || stats.TotalBuilders != 5 {
		t.Errorf("Builders = %d/%d; want 2/5", stats.FreeBuilders, stats.TotalBuilders)
	}
	if !stats.LabFree {
		t.Errorf("LabFree = false; want true")
	}
	if stats.PetsFree {
		t.Errorf("PetsFree = true; want false")
	}
	if len(stats.Items) != 3 {
		t.Fatalf("len(Items) = %d; want 3", len(stats.Items))
	}

	// King
	if stats.Items[0].Name != "BaRbaRiaN King" || stats.Items[0].Cost != 13000 || stats.Items[0].Currency != "dark_elixir" {
		t.Errorf("Item 0 = %+v; want King, 13000, dark_elixir", stats.Items[0])
	}
	// Warden
	if stats.Items[1].Name != "GRaNd WaRdeN" || stats.Items[1].Cost != 6100000 || stats.Items[1].Currency != "elixir" {
		t.Errorf("Item 1 = %+v; want Warden, 6100000, elixir", stats.Items[1])
	}
	// Collector
	if stats.Items[2].Name != "ElixiR CollectoR" || stats.Items[2].Cost != 700 || stats.Items[2].Currency != "gold" {
		t.Errorf("Item 2 = %+v; want Collector, 700, gold", stats.Items[2])
	}
}

func TestCategoryTogglesAndLimits(t *testing.T) {
	bm := &BuilderManager{
		cfg: config.UpgradeConfig{
			UpgradeHeroes:       true,
			UpgradeDefenses:     false,
			UpgradeResources:    true,
			UpgradeArmy:         true,
			UpgradeTraps:        false,
			MaxGoldPerUpgrade:   1000,
			MaxElixirPerUpgrade: 5000000,
			MaxDEPerUpgrade:     10000,
		},
	}

	if !bm.isCategoryAllowed("heroes") {
		t.Error("heroes should be allowed")
	}
	if bm.isCategoryAllowed("defenses") {
		t.Error("defenses should NOT be allowed")
	}
	if !bm.isCategoryAllowed("resources") {
		t.Error("resources should be allowed")
	}
	if bm.isCategoryAllowed("traps") {
		t.Error("traps should NOT be allowed")
	}

	// Filter eligibility
	items := []UpgradeItem{
		{Name: "Barbarian King", Category: "heroes", Cost: 13000, Currency: "dark_elixir"}, // exceeds MaxDE 10000
		{Name: "Archer Queen", Category: "heroes", Cost: 8000, Currency: "dark_elixir"},    // eligible
		{Name: "Grand Warden", Category: "heroes", Cost: 6100000, Currency: "elixir"},      // exceeds MaxElixir 5000000
		{Name: "Cannon", Category: "defenses", Cost: 500, Currency: "gold"},                // defense disabled
		{Name: "Gold Mine", Category: "resources", Cost: 800, Currency: "elixir"},          // eligible
	}

	var eligible []UpgradeItem
	for _, it := range items {
		if !bm.isCategoryAllowed(it.Category) {
			continue
		}
		if it.Currency == "gold" && bm.cfg.MaxGoldPerUpgrade > 0 && it.Cost > bm.cfg.MaxGoldPerUpgrade {
			continue
		}
		if it.Currency == "elixir" && bm.cfg.MaxElixirPerUpgrade > 0 && it.Cost > bm.cfg.MaxElixirPerUpgrade {
			continue
		}
		if it.Currency == "dark_elixir" && bm.cfg.MaxDEPerUpgrade > 0 && it.Cost > bm.cfg.MaxDEPerUpgrade {
			continue
		}
		eligible = append(eligible, it)
	}

	if len(eligible) != 2 {
		t.Fatalf("eligible count = %d; want 2", len(eligible))
	}
	if eligible[0].Name != "Archer Queen" || eligible[1].Name != "Gold Mine" {
		t.Errorf("unexpected eligible items: %+v", eligible)
	}
}
