package bot

import (
	"image"
	"testing"

	"github.com/Ducky705/ClashGO/internal/config"
	"github.com/Ducky705/ClashGO/internal/game"
	"github.com/Ducky705/ClashGO/internal/vision"
	"gocv.io/x/gocv"
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
		{"Spell Factory", "army"},
		{"DaRk Spell FactoRY", "army"},

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

func TestMenuButtonSelected(t *testing.T) {
	mat := gocv.NewMatWithSize(80, 160, gocv.MatTypeCV8UC3)
	defer mat.Close()

	mat.SetTo(gocv.NewScalar(70, 105, 150, 0)) // brown, unselected
	if menuButtonSelected(mat, 80, 40) {
		t.Error("brown button read as selected")
	}

	mat.SetTo(gocv.NewScalar(60, 180, 230, 0)) // gold, selected
	if !menuButtonSelected(mat, 80, 40) {
		t.Error("gold button read as unselected")
	}

	mat.SetTo(gocv.NewScalar(255, 255, 255, 0)) // white label fill
	if menuButtonSelected(mat, 80, 40) {
		t.Error("white fill read as selected")
	}

	if menuButtonSelected(gocv.NewMat(), 10, 10) {
		t.Error("empty frame read as selected")
	}
}

func TestFilterPopupPoint(t *testing.T) {
	cal := game.NewCalibration(1024, 768)
	if got := filterPopupPoint(cal, 0.618, 0.034); got.X != 632 || got.Y != 26 {
		t.Errorf("filter icon point = %v; want (632,26)", got)
	}
	if got := filterPopupPoint(nil, 0.5, 0.5); got != (image.Point{}) {
		t.Errorf("nil calibration point = %v; want zero", got)
	}
}

func TestFilterPopupOpen(t *testing.T) {
	cal := game.NewCalibration(1024, 768)
	mat := gocv.NewMatWithSize(768, 1024, gocv.MatTypeCV8UC3)
	defer mat.Close()

	mat.SetTo(gocv.NewScalar(62, 92, 127, 0)) // panel brown
	if !filterPopupOpen(mat, cal) {
		t.Error("panel brown read as closed")
	}

	mat.SetTo(gocv.NewScalar(5, 102, 58, 0)) // grass
	if filterPopupOpen(mat, cal) {
		t.Error("grass read as open")
	}

	if filterPopupOpen(gocv.NewMat(), cal) || filterPopupOpen(mat, nil) {
		t.Error("empty frame or nil calibration read as open")
	}
}

func TestParseTabCounter(t *testing.T) {
	cases := []struct {
		in          string
		free, total int
		ok          bool
	}{
		{"1/6", 1, 6, true},
		{"2/5", 2, 5, true},
		{"V6", 1, 6, true},  // live OCR dropped the slash
		{"V/2", 1, 2, true}, // live OCR read the 1 as a V
		{"0/1", 0, 1, true}, // parsed, but the caller rejects a 1-slot counter
		{"Available", 0, 0, false},
		{"1d", 0, 0, false},
	}
	for _, tc := range cases {
		free, total, ok := parseTabCounter(tc.in)
		if ok != tc.ok || free != tc.free || total != tc.total {
			t.Errorf("parseTabCounter(%q) = %d/%d,%v; want %d/%d,%v", tc.in, free, total, ok, tc.free, tc.total, tc.ok)
		}
	}
}

// TestParseBuilderOverviewOCRReadsDegradedCounter pins the live frame the
// auto-upgrade path used to read as 0/5, which skipped building upgrades even
// with a free builder.
func TestParseBuilderOverviewOCRReadsDegradedCounter(t *testing.T) {
	regions := []vision.TextRegion{
		{X: 715, Y: 311, W: 192, H: 18, Text: "Suggested upgrades"},
		{X: 667, Y: 345, W: 158, H: 24, Text: "BaRbaRiaN King"},
		{X: 919, Y: 369, W: 70, H: 20, Text: "14 500"},
		{X: 631, Y: 723, W: 34, H: 18, Text: "V6"},
		{X: 739, Y: 723, W: 32, H: 18, Text: "V/2"},
		{X: 953, Y: 723, W: 36, H: 18, Text: "0/1"},
	}
	stats := ParseBuilderOverviewOCR(regions)
	if stats.FreeBuilders != 1 || stats.TotalBuilders != 6 {
		t.Fatalf("Builders = %d/%d; want 1/6 from the degraded counter", stats.FreeBuilders, stats.TotalBuilders)
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
	if got := parseCurrency("resources", "Gold Mine"); got != "elixir" {
		t.Errorf("Gold Mine currency = %q; want elixir", got)
	}
	if got := parseCurrency("resources", "Gold Storage"); got != "gold" {
		t.Errorf("Gold Storage currency = %q; want gold (it is paid in gold)", got)
	}
	if got := parseCurrency("resources", "Elixir Storage"); got != "elixir" {
		t.Errorf("Elixir Storage currency = %q; want elixir", got)
	}
	if got := parseCurrency("resources", "Dark Elixir Drill"); got != "elixir" {
		t.Errorf("Dark Elixir Drill currency = %q; want elixir", got)
	}
	if got := parseCurrency("siege", "Wall Wrecker"); got != "elixir" {
		t.Errorf("Wall Wrecker currency = %q; want elixir", got)
	}
	if got := parseCurrency("army", "DaRk Spell FactoRY"); got != "elixir" {
		t.Errorf("Dark Spell Factory currency = %q; want elixir", got)
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
	// A category no toggle names must not bypass an active category filter.
	if bm.isCategoryAllowed("spells") || bm.isCategoryAllowed("") {
		t.Error("unlisted categories should follow the menu only when every category is enabled")
	}
	allOn := &BuilderManager{cfg: config.UpgradeConfig{
		UpgradeHeroes: true, UpgradeDefenses: true, UpgradeResources: true,
		UpgradeArmy: true, UpgradeTraps: true,
	}}
	if !allOn.isCategoryAllowed("spells") || !allOn.isCategoryAllowed("") {
		t.Error("with every category enabled, unlisted rows should stay eligible")
	}

	// Filter eligibility
	items := []UpgradeItem{
		{Name: "Barbarian King", Category: "heroes", Cost: 13000, Currency: "dark_elixir"}, // exceeds MaxDE 10000
		{Name: "Archer Queen", Category: "heroes", Cost: 8000, Currency: "dark_elixir"},    // eligible
		{Name: "Grand Warden", Category: "heroes", Cost: 6100000, Currency: "elixir"},      // exceeds MaxElixir 5000000
		{Name: "Cannon", Category: "defenses", Cost: 500, Currency: "gold"},                // defense disabled
		{Name: "Gold Mine", Category: "resources", Cost: 800, Currency: "elixir"},          // eligible
	}

	// The same filters the menu applies: Queen is the only survivor (defenses
	// off, King and Warden over their caps).
	best, ok := bm.selectBuildingUpgrade(items)
	if !ok {
		t.Fatal("selectBuildingUpgrade found no eligible item, want Archer Queen")
	}
	if best.Name != "Archer Queen" {
		t.Errorf("selected %q; want the only eligible item, Archer Queen", best.Name)
	}

	// Everything filtered out must skip, never fall back to a guessed row.
	if _, ok := bm.selectBuildingUpgrade(items[2:4]); ok {
		t.Error("selectBuildingUpgrade picked an item with every category/cap filtered out")
	}
}

// TestSelectBuildingUpgradeStrategies covers the upgrade-priority menu for all
// four options plus the spend caps and the unknown-category fallback.
func TestStrategicRushOrder(t *testing.T) {
	all := config.UpgradeConfig{
		UpgradeHeroes: true, UpgradeDefenses: true, UpgradeResources: true,
		UpgradeArmy: true, UpgradeTraps: true, UpgradeStrategy: "strategic_rush",
	}
	items := []UpgradeItem{
		{Name: "Wizard Tower", Category: "defenses", Cost: 9000, Currency: "gold"},
		{Name: "Gold Mine", Category: "resources", Cost: 500, Currency: "elixir"},
		{Name: "Army Camp", Category: "army", Cost: 900, Currency: "elixir"},
		{Name: "Grand Warden", Category: "heroes", Cost: 7000, Currency: "elixir"},
		{Name: "Archer Queen", Category: "heroes", Cost: 8000, Currency: "dark_elixir"},
	}
	bm := &BuilderManager{cfg: all}
	best, ok := bm.selectBuildingUpgrade(items)
	if !ok || best.Name != "Archer Queen" {
		t.Fatalf("rush picked %q, %v; want Archer Queen first", best.Name, ok)
	}

	// OCR splits and punctuation do not change the ranking: no space, mixed
	// case, and a stray glyph still resolve to the same hero/building.
	ocrItems := []UpgradeItem{
		{Name: "DaRk ElixİR DRill x 3", Category: "resources", Cost: 1},
		{Name: "ARCHERQUeEN", Category: "heroes", Cost: 2},
	}
	best, ok = bm.selectBuildingUpgrade(ocrItems)
	if !ok || best.Name != "ARCHERQUeEN" {
		t.Fatalf("rush picked %q, %v; want spaced-out Archer Queen", best.Name, ok)
	}
	if got := normalizeItemName("DaRk ElixİR DRill x 3"); got != "darkelixirdrillx3" {
		t.Fatalf("normalizeItemName = %q", got)
	}

	// Without heroes the army-capacity building outranks resources and defense.
	all.UpgradeHeroes = false
	bm = &BuilderManager{cfg: all}
	best, ok = bm.selectBuildingUpgrade(items)
	if !ok || best.Name != "Army Camp" {
		t.Fatalf("rush picked %q, %v; want Army Camp after heroes", best.Name, ok)
	}

	// Defenses are last, never skipped when they are the only thing left.
	all.UpgradeArmy, all.UpgradeResources = false, false
	bm = &BuilderManager{cfg: all}
	best, ok = bm.selectBuildingUpgrade(items)
	if !ok || best.Name != "Wizard Tower" {
		t.Fatalf("rush picked %q, %v; want Wizard Tower last", best.Name, ok)
	}

	// Max ignores the rush order and takes the biggest upgrade available.
	maxCfg := config.UpgradeConfig{
		UpgradeHeroes: true, UpgradeDefenses: true, UpgradeResources: true,
		UpgradeArmy: true, UpgradeTraps: true, UpgradeStrategy: "max",
	}
	bm = &BuilderManager{cfg: maxCfg}
	best, ok = bm.selectBuildingUpgrade(items)
	if !ok || best.Name != "Wizard Tower" {
		t.Fatalf("max picked %q, %v; want most expensive Wizard Tower", best.Name, ok)
	}
}

func TestSelectBuildingUpgradeStrategies(t *testing.T) {
	all := config.UpgradeConfig{
		UpgradeHeroes: true, UpgradeDefenses: true, UpgradeResources: true,
		UpgradeArmy: true, UpgradeTraps: true,
	}
	items := []UpgradeItem{
		{Name: "Archer Queen", Category: "heroes", Cost: 8000, Currency: "dark_elixir"},
		{Name: "Cannon", Category: "defenses", Cost: 500, Currency: "gold"},
		{Name: "Gold Mine", Category: "resources", Cost: 800, Currency: "elixir"},
	}

	cases := []struct {
		name     string
		strategy string
		goldCap  int
		want     string
	}{
		{"heroes first", "heroes_first", 0, "Archer Queen"},
		{"cheapest first", "cheapest_first", 0, "Cannon"},
		{"most expensive first", "most_expensive_first", 0, "Archer Queen"},
		{"balanced keeps menu order", "balanced", 0, "Archer Queen"},
		{"unknown strategy keeps menu order", "nonsense", 0, "Archer Queen"},
		{"gold cap excludes Cannon", "cheapest_first", 400, "Gold Mine"},
		{"gold cap boundary is inclusive", "cheapest_first", 500, "Cannon"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := all
			cfg.UpgradeStrategy = tc.strategy
			cfg.MaxGoldPerUpgrade = tc.goldCap
			bm := &BuilderManager{cfg: cfg}
			best, ok := bm.selectBuildingUpgrade(items)
			if !ok {
				t.Fatalf("no selection for %q", tc.strategy)
			}
			if best.Name != tc.want {
				t.Errorf("selected %q; want %q", best.Name, tc.want)
			}
		})
	}

	// A category toggled off in the UI removes exactly its rows, and an
	// unrecognised category cannot sneak past an active category filter.
	cfg := all
	cfg.UpgradeDefenses = false
	cfg.UpgradeHeroes = false
	cfg.UpgradeResources = false
	bm := &BuilderManager{cfg: cfg}
	misc := []UpgradeItem{
		{Name: "Cannon", Category: "defenses", Cost: 500, Currency: "gold"},
		{Name: "Builder Hut", Category: "other", Cost: 100, Currency: "gems"},
	}
	if _, ok := bm.selectBuildingUpgrade(misc); ok {
		t.Error("an unlisted category slipped past an active category filter")
	}

	// With every category enabled it follows the menu suggestions again.
	bmAll := &BuilderManager{cfg: all}
	best, ok := bmAll.selectBuildingUpgrade(misc[1:])
	if !ok || best.Name != "Builder Hut" {
		t.Errorf("selected %+v, %v; want the 'other' row Builder Hut with all categories on", best.Name, ok)
	}
}
