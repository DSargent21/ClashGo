//go:build livemenu

package bot

// Live verification of the Build Menu's own filter popup. Each case drives the
// real UI, re-opens the popup to confirm the buttons landed in the requested
// state, then OCRs the filtered list and checks the rows match the categories.
// No upgrade row is tapped.

import (
	"os"
	"testing"
	"time"

	"github.com/Ducky705/ClashGO/internal/adb"
	"github.com/Ducky705/ClashGO/internal/config"
	"github.com/Ducky705/ClashGO/internal/game"
	"github.com/rs/zerolog"
)

func liveBuilderSetup(t *testing.T) (*adb.Client, *game.Calibration) {
	t.Helper()
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

	w, h, err := client.ScreenSize()
	if err != nil {
		client.Close()
		t.Fatalf("screen size: %v", err)
	}
	cal := game.NewCalibration(w, h)
	cal.Verified = true
	if cfg.Device.DisplayScale > 0 {
		cal.SetDisplayScale(cfg.Device.DisplayScale)
	}
	t.Logf("device=%s screen=%dx%d display_scale=%.4f", device, w, h, cal.DisplayScale())
	return client, cal
}

func allCategories(up *config.UpgradeConfig) {
	up.UpgradeHeroes = true
	up.UpgradeDefenses = true
	up.UpgradeResources = true
	up.UpgradeArmy = true
	up.UpgradeTraps = true
}

func TestLiveBuildingMenuFilterPopup(t *testing.T) {
	client, cal := liveBuilderSetup(t)
	defer client.Close()
	logger := zerolog.New(zerolog.ConsoleWriter{Out: os.Stderr}).With().Timestamp().Logger()

	cases := []struct {
		name     string
		mutate   func(*config.UpgradeConfig)
		wantCat  string
		wantType string
		wantMin  bool
		wantMax  bool
	}{
		{"army only", func(c *config.UpgradeConfig) {
			allCategories(c)
			c.UpgradeArmy = true
			c.UpgradeHeroes, c.UpgradeDefenses, c.UpgradeResources, c.UpgradeTraps = false, false, false, false
		}, "army", "army", false, false},
		{"heroes only", func(c *config.UpgradeConfig) {
			allCategories(c)
			c.UpgradeHeroes = true
			c.UpgradeArmy, c.UpgradeDefenses, c.UpgradeResources, c.UpgradeTraps = false, false, false, false
		}, "heroes", "hero", false, false},
		{"defenses only", func(c *config.UpgradeConfig) {
			allCategories(c)
			c.UpgradeDefenses = true
			c.UpgradeArmy, c.UpgradeHeroes, c.UpgradeResources, c.UpgradeTraps = false, false, false, false
		}, "defenses", "defense", false, false},
		{"resources only", func(c *config.UpgradeConfig) {
			allCategories(c)
			c.UpgradeResources = true
			c.UpgradeArmy, c.UpgradeHeroes, c.UpgradeDefenses, c.UpgradeTraps = false, false, false, false
		}, "resources", "resource", false, false},
		{"all + cheapest sorting", func(c *config.UpgradeConfig) { allCategories(c); c.UpgradeStrategy = "cheapest_first" }, "", "", true, false},
		{"all + most expensive sorting", func(c *config.UpgradeConfig) { allCategories(c); c.UpgradeStrategy = "most_expensive_first" }, "", "", false, true},
	}

	cfgAll := config.LoadOrDefault("config.json").Upgrade
	allCategories(&cfgAll)
	cfgAll.AutoBuilders = true
	cfgAll.AutoLab = false
	cfgAll.AutoPets = false
	cfgAll.DryRun = true
	cfgAll.UpgradeStrategy = "balanced"
	restore := NewBuilderManager(client, cal, cfgAll, logger)
	defer func() {
		_ = restore.OpenMenu()
		_ = restore.ApplyBuildingInGameFilter()
		_ = restore.CloseMenu()
	}()

	for _, tc := range cases {
		if only := os.Getenv("CLASHGO_LIVE_CASE"); only != "" && only != tc.name {
			continue
		}
		t.Run(tc.name, func(t *testing.T) {
			up := config.LoadOrDefault("config.json").Upgrade
			up.AutoBuilders = true
			up.AutoLab = false
			up.AutoPets = false
			up.DryRun = true
			up.UpgradeStrategy = "balanced"
			allCategories(&up)
			tc.mutate(&up)

			bm := NewBuilderManager(client, cal, up, logger)
			if err := bm.OpenMenu(); err != nil {
				t.Fatalf("open menu: %v", err)
			}
			defer func() { _ = bm.CloseMenu() }()
			if err := bm.ApplyBuildingInGameFilter(); err != nil {
				t.Fatalf("apply filter: %v", err)
			}

			// Re-open the popup (Apply closes it) and confirm every button
			// landed where asked.
			icon := filterPopupPoint(cal, 0.626, 0.035)
			_ = client.Tap(icon.X, icon.Y)
			time.Sleep(1200 * time.Millisecond)
			popup, err := client.CaptureToMat()
			if err != nil || popup.Empty() {
				t.Fatalf("capture popup: %v", err)
			}
			state := func(fx, fy float64) bool {
				p := filterPopupPoint(cal, fx, fy)
				return menuButtonSelected(popup, p.X, p.Y)
			}
			has := map[string]bool{
				"army":     state(0.253, 0.340),
				"defense":  state(0.420, 0.340),
				"hero":     state(0.588, 0.340),
				"resource": state(0.253, 0.421),
				"gold":     state(0.231, 0.206),
				"elixir":   state(0.401, 0.206),
				"de":       state(0.569, 0.206),
				"costMax":  state(0.253, 0.552),
				"costMin":  state(0.420, 0.552),
			}
			popup.Close()
			t.Logf("filter state: %+v", has)

			wantTypes := map[string]bool{"army": false, "defense": false, "hero": false, "resource": false}
			if tc.wantType != "" {
				wantTypes[tc.wantType] = true
			}
			for name, want := range wantTypes {
				if has[name] != want {
					t.Errorf("%s button = %v, want %v (single-select Type row)", name, has[name], want)
				}
			}
			if has["gold"] || has["elixir"] || has["de"] {
				t.Errorf("resource currencies not cleared: %+v", has)
			}
			if has["costMin"] != tc.wantMin {
				t.Errorf("Cost Min = %v, want %v", has["costMin"], tc.wantMin)
			}
			if has["costMax"] != tc.wantMax {
				t.Errorf("Cost Max = %v, want %v", has["costMax"], tc.wantMax)
			}

			// Dismiss popup and read the list it now shows.
			outside := filterPopupPoint(cal, 0.078, 0.521)
			_ = client.Tap(outside.X, outside.Y)
			time.Sleep(900 * time.Millisecond)
			stats := bm.inspectMenuFrame()
			for _, it := range stats.Items {
				t.Logf("filtered row: %q [%s, %d %s]", it.Name, it.Category, it.Cost, it.Currency)
				if !bm.isCategoryAllowed(it.Category) {
					t.Errorf("row %q [%s] survived a filter that excludes it", it.Name, it.Category)
				}
				if tc.wantCat != "" && it.Category != tc.wantCat {
					t.Errorf("row %q category = %q, want only %q", it.Name, it.Category, tc.wantCat)
				}
			}
			if len(stats.Items) == 0 {
				t.Errorf("no rows after applying %q; the filter popup hid everything", tc.name)
			}
		})
	}
}
