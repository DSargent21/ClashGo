//go:build livemenu

package bot

// Live dry-run of the full builder path: filter popup, paged list scan with
// scrolling, strategic-rush selection, re-find after scrolling, then the row
// tap. DryRun dismisses the upgrade dialog without confirming, so nothing is
// spent. The chosen item is logged by PerformAutoUpgrades.

import (
	"os"
	"testing"
	"time"

	"github.com/Ducky705/ClashGO/internal/config"
	"github.com/rs/zerolog"
)

func TestLiveStrategicRushDryRun(t *testing.T) {
	client, cal := liveBuilderSetup(t)
	defer client.Close()
	logger := zerolog.New(zerolog.ConsoleWriter{Out: os.Stderr}).With().Timestamp().Logger()

	up := config.LoadOrDefault("config.json").Upgrade
	allCategories(&up)
	up.AutoBuilders = true
	up.AutoLab = false
	up.AutoPets = false
	up.DryRun = true
	up.ReserveBuilders = 0
	up.UpgradeStrategy = "strategic_rush"
	bm := NewBuilderManager(client, cal, up, logger)

	if err := bm.OpenMenu(); err != nil {
		t.Fatalf("open menu: %v", err)
	}
	if err := bm.ApplyBuildingInGameFilter(); err != nil {
		t.Fatalf("apply filter: %v", err)
	}
	_ = bm.CloseMenu()
	time.Sleep(500 * time.Millisecond)

	if err := bm.PerformAutoUpgrades(); err != nil {
		t.Fatalf("auto upgrades: %v", err)
	}
}
