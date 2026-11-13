//go:build livemenu

package bot

// Live check of the builder list scroll: reset to the top, page down until the
// rows stop changing (bottom), then reset to the top again and compare the
// page. Read-only: opens the menu, OCRs, swipes inside the list, closes it. No
// upgrade row is tapped.

import (
	"os"
	"testing"
	"time"

	"github.com/Ducky705/ClashGO/internal/config"
	"github.com/rs/zerolog"
)

func TestLiveBuilderMenuScroll(t *testing.T) {
	client, cal := liveBuilderSetup(t)
	defer client.Close()
	logger := zerolog.New(zerolog.ConsoleWriter{Out: os.Stderr}).With().Timestamp().Logger()

	up := config.LoadOrDefault("config.json").Upgrade
	allCategories(&up)
	up.AutoBuilders = true
	up.AutoLab = false
	up.AutoPets = false
	up.UpgradeStrategy = "strategic_rush"
	bm := NewBuilderManager(client, cal, up, logger)

	if err := bm.OpenMenu(); err != nil {
		t.Fatalf("open menu: %v", err)
	}
	defer func() { _ = bm.CloseMenu() }()
	if err := bm.ApplyBuildingInGameFilter(); err != nil {
		t.Fatalf("apply filter: %v", err)
	}

	top := bm.resetBuilderListTop()
	for _, it := range top.Items {
		t.Logf("top: %q [%s]", it.Name, it.Category)
	}
	if len(top.Items) == 0 {
		t.Fatal("top page has no rows")
	}

	seen := make(map[string]bool, len(top.Items))
	for _, it := range top.Items {
		seen[normalizeItemName(it.Name)] = true
	}

	pages := 1
	bottomReached := false
	for i := 0; i < maxBuilderPages; i++ {
		bm.scrollBuilderList(true)
		time.Sleep(300 * time.Millisecond)
		page := bm.inspectMenuFrame()
		newRows := 0
		for _, it := range page.Items {
			name := normalizeItemName(it.Name)
			if name != "" && !seen[name] {
				seen[name] = true
				newRows++
				t.Logf("page %d: %q [%s]", i+2, it.Name, it.Category)
			}
		}
		if newRows == 0 {
			bottomReached = true
			break
		}
		pages++
	}
	if pages < 2 {
		t.Fatalf("list did not scroll: only %d page with %d rows", pages, len(top.Items))
	}
	if !bottomReached {
		t.Fatalf("list did not settle at the bottom within %d pages", maxBuilderPages)
	}
	t.Logf("scrolled %d pages and reached the bottom", pages)

	back := bm.resetBuilderListTop()
	if builderPageSignature(back) != builderPageSignature(top) {
		t.Fatalf("reset did not return to the top page: top=%q back=%q",
			builderPageSignature(top), builderPageSignature(back))
	}
	t.Logf("reset returned to the top page")
}
