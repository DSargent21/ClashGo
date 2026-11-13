//go:build livemenu

package bot

// Live, read-only check of the Build Menu filters against the real device.
// It opens the builder overview, OCRs the suggested rows, runs every
// category/priority/cap scenario over those rows, then closes the menu. It
// never taps an upgrade row, so nothing is spent or confirmed.
//
// Run explicitly (game must be sitting in the main village):
//
//	go test -tags livemenu -run TestLiveBuilderMenuFilters -v ./internal/bot
//
// CLASHGO_LIVE_DEVICE overrides the ADB device (default: config.json's device).

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Ducky705/ClashGO/internal/adb"
	"github.com/Ducky705/ClashGO/internal/config"
	"github.com/Ducky705/ClashGO/internal/game"
	"github.com/Ducky705/ClashGO/internal/vision"
	"github.com/rs/zerolog"
	"gocv.io/x/gocv"
)

func TestLiveBuilderMenuFilters(t *testing.T) {
	cfg := config.LoadOrDefault("config.json")
	device := os.Getenv("CLASHGO_LIVE_DEVICE")
	if device == "" {
		device = cfg.Device.DeviceID
	}
	if device == "" {
		t.Skip("no ADB device configured; set CLASHGO_LIVE_DEVICE")
	}

	client := adb.NewClient(
		adb.WithHost(cfg.Device.ADBHost),
		adb.WithPort(cfg.Device.ADBPort),
		adb.WithTimeout(30*time.Second),
		adb.WithJitterTaps(false),
		adb.WithJitterDelays(false),
	)
	client.DeviceID = device
	defer client.Close()

	w, h, err := client.ScreenSize()
	if err != nil {
		t.Fatalf("screen size: %v", err)
	}
	cal := game.NewCalibration(w, h)
	cal.Verified = true
	if cfg.Device.DisplayScale > 0 {
		cal.SetDisplayScale(cfg.Device.DisplayScale)
	}
	t.Logf("device=%s screen=%dx%d display_scale=%.4f", device, w, h, cal.DisplayScale())

	up := cfg.Upgrade
	up.AutoBuilders = true
	up.AutoLab = false
	up.AutoPets = false
	up.DryRun = true

	bm := NewBuilderManager(client, cal, up, zerolog.New(zerolog.ConsoleWriter{Out: os.Stderr}).With().Timestamp().Logger())

	if err := bm.OpenMenu(); err != nil {
		t.Fatalf("open builder menu: %v", err)
	}
	time.Sleep(1200 * time.Millisecond)
	mat, err := client.CaptureToMat()
	if err != nil || mat.Empty() {
		t.Fatalf("capture builder menu: %v", err)
	}
	framePath := filepath.Join(os.TempDir(), "clashgo_live_builder_menu.png")
	if ok := gocv.IMWrite(framePath, mat); !ok {
		t.Fatalf("write builder menu frame to %s", framePath)
	}
	mat.Close()
	regions, err := vision.OCRTextRegions(framePath)
	if err != nil {
		t.Fatalf("OCR %s: %v", framePath, err)
	}
	t.Logf("frame=%s OCR regions=%d", framePath, len(regions))
	for _, r := range regions {
		t.Logf("ocr: %q (%d,%d %dx%d)", r.Text, r.X, r.Y, r.W, r.H)
	}
	stats := ParseBuilderOverviewOCR(regions)
	_ = bm.CloseMenu()
	time.Sleep(600 * time.Millisecond)

	t.Logf("parsed: builders=%d/%d labFree=%v petsFree=%v rows=%d",
		stats.FreeBuilders, stats.TotalBuilders, stats.LabFree, stats.PetsFree, len(stats.Items))
	for i, it := range stats.Items {
		t.Logf("row %d: name=%q category=%q cost=%d currency=%q region=(%d,%d %dx%d)",
			i, it.Name, it.Category, it.Cost, it.Currency, it.Region.X, it.Region.Y, it.Region.W, it.Region.H)
	}
	if len(stats.Items) == 0 {
		t.Fatal("no upgrade rows parsed from the live menu: game not in the main village, menu did not open, or OCR read nothing")
	}

	live := make(map[string]bool, len(stats.Items))
	for _, it := range stats.Items {
		live[it.Name] = true
		if it.Category == "" {
			t.Errorf("row %q has no category, so category filters cannot apply", it.Name)
		}
		if it.Currency == "" {
			t.Errorf("row %q has no currency, so spend caps cannot apply", it.Name)
		}
		if it.Region.W <= 0 || it.Region.H <= 0 {
			t.Errorf("row %q has no tap region", it.Name)
		}
	}

	base := up
	base.AutoBuilders = true
	base.DryRun = true
	base.UpgradeStrategy = "balanced"
	base.UpgradeHeroes = true
	base.UpgradeDefenses = true
	base.UpgradeResources = true
	base.UpgradeArmy = true
	base.UpgradeTraps = true
	base.MaxGoldPerUpgrade = 0
	base.MaxElixirPerUpgrade = 0
	base.MaxDEPerUpgrade = 0

	only := func(cat string) func(*config.UpgradeConfig) {
		return func(c *config.UpgradeConfig) {
			c.UpgradeHeroes = cat == "heroes"
			c.UpgradeDefenses = cat == "defenses"
			c.UpgradeResources = cat == "resources"
			c.UpgradeArmy = cat == "army"
			c.UpgradeTraps = cat == "traps"
		}
	}

	goldCap := 0
	for _, it := range stats.Items {
		if it.Currency == "gold" && it.Cost > 0 && (goldCap == 0 || it.Cost < goldCap) {
			goldCap = it.Cost
		}
	}
	if goldCap > 0 {
		goldCap--
	}

	scenarios := []struct {
		name   string
		mutate func(*config.UpgradeConfig)
	}{
		{"balanced / all categories", func(c *config.UpgradeConfig) {}},
		{"heroes first", func(c *config.UpgradeConfig) { c.UpgradeStrategy = "heroes_first" }},
		{"cheapest first", func(c *config.UpgradeConfig) { c.UpgradeStrategy = "cheapest_first" }},
		{"most expensive first", func(c *config.UpgradeConfig) { c.UpgradeStrategy = "most_expensive_first" }},
		{"only heroes", only("heroes")},
		{"only defenses", only("defenses")},
		{"only resources", only("resources")},
		{"only army buildings", only("army")},
		{"only traps", only("traps")},
		{"no categories", func(c *config.UpgradeConfig) {
			c.UpgradeHeroes = false
			c.UpgradeDefenses = false
			c.UpgradeResources = false
			c.UpgradeArmy = false
			c.UpgradeTraps = false
		}},
	}
	if goldCap > 0 {
		cap := goldCap
		scenarios = append(scenarios, struct {
			name   string
			mutate func(*config.UpgradeConfig)
		}{"gold cap below cheapest gold row", func(c *config.UpgradeConfig) { c.MaxGoldPerUpgrade = cap }})
	}

	for _, tc := range scenarios {
		uc := base
		tc.mutate(&uc)
		probe := &BuilderManager{cfg: uc}
		best, ok := probe.selectBuildingUpgrade(stats.Items)
		if !ok {
			t.Logf("%-30s -> no eligible row (everything filtered out)", tc.name)
			if tc.name == "balanced / all categories" {
				t.Error("all-categories scenario filtered out every live row, want at least one")
			}
			continue
		}
		t.Logf("%-30s -> %q [%s, %d %s]", tc.name, best.Name, best.Category, best.Cost, best.Currency)

		if !live[best.Name] {
			t.Errorf("%s selected %q, which is not a live row", tc.name, best.Name)
		}
		if !probe.isCategoryAllowed(best.Category) {
			t.Errorf("%s selected disabled category %q", tc.name, best.Category)
		}
		if uc.MaxGoldPerUpgrade > 0 && best.Currency == "gold" && best.Cost > uc.MaxGoldPerUpgrade {
			t.Errorf("%s selected gold cost %d above cap %d", tc.name, best.Cost, uc.MaxGoldPerUpgrade)
		}
		if uc.MaxElixirPerUpgrade > 0 && best.Currency == "elixir" && best.Cost > uc.MaxElixirPerUpgrade {
			t.Errorf("%s selected elixir cost %d above cap %d", tc.name, best.Cost, uc.MaxElixirPerUpgrade)
		}
		if uc.MaxDEPerUpgrade > 0 && best.Currency == "dark_elixir" && best.Cost > uc.MaxDEPerUpgrade {
			t.Errorf("%s selected DE cost %d above cap %d", tc.name, best.Cost, uc.MaxDEPerUpgrade)
		}
	}
}
