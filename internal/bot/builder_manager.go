package bot

import (
	"errors"
	"fmt"
	"image"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Ducky705/ClashGO/internal/adb"
	"github.com/Ducky705/ClashGO/internal/config"
	"github.com/Ducky705/ClashGO/internal/game"
	"github.com/Ducky705/ClashGO/internal/vision"
	"github.com/rs/zerolog"
	"gocv.io/x/gocv"
)

// BuilderManager automates village progression using the modern Clash of Clans
// Build Overview Menu (Suggested Upgrades, Laboratory, and Hero Pets) with fine-grained
// strategy filters (heroes first, cheapest first, category toggles, and max spend limits).
type BuilderManager struct {
	client *adb.Client
	cal    *game.Calibration
	cfg    config.UpgradeConfig
	logger zerolog.Logger
}

func NewBuilderManager(client *adb.Client, cal *game.Calibration, cfg config.UpgradeConfig, logger zerolog.Logger) *BuilderManager {
	return &BuilderManager{
		client: client,
		cal:    cal,
		cfg:    cfg,
		logger: logger.With().Str("component", "builder_manager").Logger(),
	}
}

// OpenMenu opens the Builder Overview modal from the main village.
func (bm *BuilderManager) OpenMenu() error {
	if bm.cal == nil || bm.client == nil {
		return errors.New("uninitialized calibration or adb client")
	}
	bx, by := int(808*bm.cal.DisplayScale()), int(534*bm.cal.DisplayScale())
	if x, y, ok := loadWallUpgradeBuilderButton(bm.cal, bm.logger); ok {
		bx, by = x, y
	} else if bm.cal.PhysicalW > 0 && bm.cal.PhysicalH > 0 {
		bx = int(float64(bm.cal.PhysicalW) * 0.893)
		by = int(float64(bm.cal.PhysicalH) * 0.788)
	}
	bm.logger.Debug().Int("x", bx).Int("y", by).Msg("opening builder overview menu")
	if err := bm.client.Tap(bx, by); err != nil {
		return fmt.Errorf("tap builder menu icon: %w", err)
	}
	time.Sleep(1200 * time.Millisecond)
	return nil
}

// CloseMenu closes the Builder Overview modal by tapping the close button.
func (bm *BuilderManager) CloseMenu() error {
	if bm.cal == nil || bm.client == nil {
		return errors.New("uninitialized calibration or adb client")
	}
	closeX := int(float64(bm.cal.PhysicalW) * 0.952)
	closeY := int(float64(bm.cal.PhysicalH) * 0.047)
	if closeX == 0 || closeY == 0 {
		closeX, closeY = 975, 36
	}
	bm.logger.Debug().Int("x", closeX).Int("y", closeY).Msg("closing builder overview menu via X button")
	if err := bm.client.Tap(closeX, closeY); err != nil {
		return fmt.Errorf("dismiss builder menu: %w", err)
	}
	time.Sleep(800 * time.Millisecond)
	return nil
}

// CloseModalDialog taps the top-right X button on a modal confirmation dialog.
func (bm *BuilderManager) CloseModalDialog() error {
	if bm.cal == nil || bm.client == nil {
		return errors.New("uninitialized calibration or adb client")
	}
	closeX := int(float64(bm.cal.PhysicalW) * 0.915)
	closeY := int(float64(bm.cal.PhysicalH) * 0.178)
	if closeX == 0 || closeY == 0 {
		closeX, closeY = 937, 137
	}
	bm.logger.Debug().Int("x", closeX).Int("y", closeY).Msg("closing confirmation modal dialog via X button")
	return bm.client.Tap(closeX, closeY)
}

// BuilderOverviewStats holds parsed status of the builder menu tabs.
type BuilderOverviewStats struct {
	FreeBuilders  int
	TotalBuilders int
	LabFree       bool
	PetsFree      bool
	Items         []UpgradeItem
}

type UpgradeItem struct {
	Name     string
	Category string // "heroes", "defenses", "resources", "army", "traps", "other"
	Cost     int
	Currency string // "gold", "elixir", "dark_elixir"
	Duration string
	Region   vision.TextRegion
}

// PerformAutoUpgrades inspects the builder overview, checks builder availability,
// and starts eligible building, laboratory, or pet upgrades according to user priorities.
func (bm *BuilderManager) PerformAutoUpgrades() error {
	if !bm.cfg.AutoBuilders && !bm.cfg.AutoLab && !bm.cfg.AutoPets {
		return nil
	}

	bm.logger.Info().Msg("checking automated upgrades (builders, lab, pets)...")
	if err := bm.OpenMenu(); err != nil {
		return err
	}
	defer func() {
		_ = bm.CloseMenu()
	}()

	k := bm.cal.DisplayScale()

	// Align the game's own filter popup with the configured categories before
	// reading rows: the menu otherwise shows whatever the user left selected.
	if bm.cfg.AutoBuilders {
		if err := bm.ApplyBuildingInGameFilter(); err != nil {
			bm.logger.Warn().Err(err).Msg("could not set build menu filters; using the menu as-is")
		}
	}

	// Capture frame to inspect tab counters and suggested items
	stats := bm.inspectMenuFrame()

	// 1. Check Builders tab if auto builders enabled
	if bm.cfg.AutoBuilders {
		bm.logger.Info().
			Int("free_builders", stats.FreeBuilders).
			Int("total_builders", stats.TotalBuilders).
			Int("reserve_builders", bm.cfg.ReserveBuilders).
			Msg("evaluating builder capacity")

		if stats.FreeBuilders <= 0 || (stats.TotalBuilders > 0 && stats.FreeBuilders <= bm.cfg.ReserveBuilders) {
			bm.logger.Info().
				Int("free", stats.FreeBuilders).
				Int("reserve", bm.cfg.ReserveBuilders).
				Msg("no free builders or builders at/below reserve limit; skipping building upgrades")
		} else {
			bm.executeBuildingUpgrade(stats, k)
		}
	}

	// 2. Check Lab tab if auto lab enabled
	if bm.cfg.AutoLab {
		labTabPt := image.Pt(int(650*k), int(638*k))
		bm.logger.Info().Msg("switching to laboratory upgrades tab")
		_ = bm.client.Tap(labTabPt.X, labTabPt.Y)
		time.Sleep(1000 * time.Millisecond)

		labStats := bm.inspectMenuFrame()
		if !labStats.LabFree {
			bm.logger.Info().Msg("laboratory research is currently in progress (0/1 free); skipping")
		} else {
			bm.executeLabUpgrade(labStats, k)
		}
	}

	// 3. Check Pets tab if auto pets enabled
	if bm.cfg.AutoPets {
		petsTabPt := image.Pt(int(840*k), int(638*k))
		bm.logger.Info().Msg("switching to hero pets tab")
		_ = bm.client.Tap(petsTabPt.X, petsTabPt.Y)
		time.Sleep(1000 * time.Millisecond)

		petStats := bm.inspectMenuFrame()
		if !petStats.PetsFree {
			bm.logger.Info().Msg("hero pet upgrade is currently in progress (0/1 free); skipping")
		} else {
			bm.executePetUpgrade(petStats, k)
		}
	}

	return nil
}

// selectBuildingUpgrade applies the category toggles and per-currency spend
// caps, then orders the survivors by the configured strategy. Split out from
// the tap sequence so the menu filters can be tested without a device.
func (bm *BuilderManager) selectBuildingUpgrade(items []UpgradeItem) (UpgradeItem, bool) {
	eligible := make([]UpgradeItem, 0, len(items))
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
	if len(eligible) == 0 {
		return UpgradeItem{}, false
	}

	best := eligible[0]
	switch bm.cfg.UpgradeStrategy {
	case "heroes_first":
		for _, it := range eligible {
			if it.Category == "heroes" {
				best = it
				break
			}
		}
	case "cheapest_first":
		for _, it := range eligible {
			if it.Cost > 0 && (best.Cost == 0 || it.Cost < best.Cost) {
				best = it
			}
		}
	case "most_expensive_first":
		for _, it := range eligible {
			if it.Cost > best.Cost {
				best = it
			}
		}
	}
	return best, true
}

func (bm *BuilderManager) executeBuildingUpgrade(stats BuilderOverviewStats, k float64) {
	bm.logger.Info().Str("strategy", bm.cfg.UpgradeStrategy).Msg("selecting best building upgrade")

	best, ok := bm.selectBuildingUpgrade(stats.Items)
	if !ok {
		bm.logger.Info().Msg("no eligible building upgrades found matching filters; skipping building upgrade")
		return
	}
	if best.Region.W <= 0 || best.Region.H <= 0 {
		bm.logger.Warn().Str("item", best.Name).Msg("parsed upgrade item has no tap region; skipping instead of tapping")
		return
	}
	tapPt := best.Region.Center()
	selectedName := best.Name

	bm.logger.Info().Str("item", selectedName).Int("x", tapPt.X).Int("y", tapPt.Y).Msg("tapping upgrade item")
	_ = bm.client.Tap(tapPt.X, tapPt.Y)
	time.Sleep(1400 * time.Millisecond)

	if bm.cfg.DryRun {
		bm.logger.Info().Str("item", selectedName).Msg("dry run: upgrade modal inspected; dismissing safely without confirming")
		_ = bm.CloseModalDialog()
		time.Sleep(500 * time.Millisecond)
		_ = bm.OpenMenu()
		return
	}

	// Confirm dialog: check if confirmation modal is drawn
	screen, err := bm.client.CaptureToMat()
	if err == nil {
		defer screen.Close()
		confirmRect, ok := loadWallUpgradeConfirmRect(bm.cal, bm.logger)
		if !ok {
			confirmRect = Rect{X1: int(580 * k / 1.1334), Y1: int(550 * k / 1.1334), X2: int(850 * k / 1.1334), Y2: int(663 * k / 1.1334)}
		}
		drawn, _, _ := confirmButtonDrawn(screen, confirmRect)
		if drawn {
			cx, cy := confirmRect.Center()
			bm.logger.Info().Int("x", cx).Int("y", cy).Msg("confirming building upgrade")
			_ = bm.client.Tap(cx, cy)
			time.Sleep(1000 * time.Millisecond)
		} else {
			bm.logger.Warn().Msg("confirmation dialog not drawn; skipping tap to prevent mis-click")
		}
	} else {
		confirmPt := image.Pt(int(656*k), int(560*k))
		bm.logger.Info().Int("x", confirmPt.X).Int("y", confirmPt.Y).Msg("confirming building upgrade (fallback)")
		_ = bm.client.Tap(confirmPt.X, confirmPt.Y)
		time.Sleep(1000 * time.Millisecond)
	}

	// In case gems dialog or insufficient resources popup opened, safely close modal
	_ = bm.CloseModalDialog()
	time.Sleep(500 * time.Millisecond)

	// Re-open overview menu
	_ = bm.OpenMenu()
}

// filterPopupPoint converts a fraction of the live screen into a tap point.
// The filter popup is laid out against the full frame, so ratios keep it usable
// at the resolutions the bot supports.
func filterPopupPoint(cal *game.Calibration, fx, fy float64) image.Point {
	if cal == nil || cal.PhysicalW <= 0 || cal.PhysicalH <= 0 {
		return image.Point{}
	}
	return image.Pt(int(fx*float64(cal.PhysicalW)), int(fy*float64(cal.PhysicalH)))
}

// menuButtonSelected reports whether a filter-popup button is drawn in the
// selected (gold) style. It samples the top-inside strip of the button, above
// the label: selected fill there is bright gold (live BGR 53,166,219), while an
// unselected button is brown with B≈76. Sampling the label row or the area
// above the button both read wrong (white glyphs / popup background).
func menuButtonSelected(m gocv.Mat, cx, cy int) bool {
	if m.Empty() || cx <= 0 || cy <= 0 {
		return false
	}
	x1, y1 := cx-15, cy-9
	x2, y2 := cx+15, cy-3
	if x1 < 0 {
		x1 = 0
	}
	if y1 < 0 {
		y1 = 0
	}
	if x2 > m.Cols() {
		x2 = m.Cols()
	}
	if y2 > m.Rows() {
		y2 = m.Rows()
	}
	if x2 <= x1 || y2 <= y1 {
		return false
	}
	roi := m.Region(image.Rect(x1, y1, x2, y2))
	defer roi.Close()
	mask := gocv.NewMat()
	defer mask.Close()
	if err := gocv.InRangeWithScalar(roi, gocv.NewScalar(0, 135, 185, 0), gocv.NewScalar(110, 230, 255, 0), &mask); err != nil {
		return false
	}
	area := roi.Cols() * roi.Rows()
	if area == 0 {
		return false
	}
	return float64(gocv.CountNonZero(mask))/float64(area) > 0.35
}

// filterPopupOpen reports whether the filter panel covers the village. The
// panel's flat brown background is checked well inside its left edge; with the
// panel closed that point is grass or path and fails the colour bounds.
func filterPopupOpen(m gocv.Mat, cal *game.Calibration) bool {
	if m.Empty() || cal == nil {
		return false
	}
	p := filterPopupPoint(cal, 0.166, 0.390)
	x1, y1 := p.X-6, p.Y-6
	x2, y2 := p.X+6, p.Y+6
	if x1 < 0 {
		x1 = 0
	}
	if y1 < 0 {
		y1 = 0
	}
	if x2 > m.Cols() {
		x2 = m.Cols()
	}
	if y2 > m.Rows() {
		y2 = m.Rows()
	}
	if x2 <= x1 || y2 <= y1 {
		return false
	}
	roi := m.Region(image.Rect(x1, y1, x2, y2))
	defer roi.Close()
	mask := gocv.NewMat()
	defer mask.Close()
	// Panel brown, live sample BGR(62,92,127).
	if err := gocv.InRangeWithScalar(roi, gocv.NewScalar(45, 70, 100, 0), gocv.NewScalar(85, 115, 150, 0), &mask); err != nil {
		return false
	}
	return float64(gocv.CountNonZero(mask))/float64(roi.Cols()*roi.Rows()) > 0.5
}

// ApplyBuildingInGameFilter drives the Build Menu's own filter popup so the
// rows the bot OCRs are the rows it asked for: Type buttons follow the category
// toggles, currency filters are cleared (a selected currency ANDs with Type and
// hid every row in a live run), and Sorting follows the priority where it maps.
func (bm *BuilderManager) ApplyBuildingInGameFilter() error {
	if bm.cal == nil || bm.client == nil {
		return errors.New("uninitialized calibration or adb client")
	}

	// The popup stays open across menu closes, and tapping the sliders while it
	// is open closes it. Only tap the icon after confirming it is closed, or
	// every following tap lands on the menu behind.
	outside := filterPopupPoint(bm.cal, 0.078, 0.521)
	icon := filterPopupPoint(bm.cal, 0.626, 0.035)
	screen, err := bm.client.CaptureToMat()
	if err != nil || screen.Empty() {
		return fmt.Errorf("capture build menu: %w", err)
	}
	if !filterPopupOpen(screen, bm.cal) {
		bm.logger.Info().Int("x", icon.X).Int("y", icon.Y).Msg("opening build menu filter popup")
		if err := bm.client.Tap(icon.X, icon.Y); err != nil {
			screen.Close()
			return fmt.Errorf("tap filter button: %w", err)
		}
		time.Sleep(900 * time.Millisecond)
		screen.Close()
		screen, err = bm.client.CaptureToMat()
		if err != nil || screen.Empty() {
			return fmt.Errorf("capture filter popup: %w", err)
		}
	}
	defer screen.Close()

	// Type is single-select live: tapping one button selects it and clears the
	// rest, so asking for several categories means leaving every Type button
	// unselected and letting the Go-side category filter do the work. Traps have
	// no Type button; they are listed under Defense, and the Go filter keeps the
	// two apart again. New/Permanent are only ever cleared: either would hide
	// rows the bot asked for.
	want := ""
	switch {
	case bm.cfg.UpgradeArmy && !bm.cfg.UpgradeDefenses && !bm.cfg.UpgradeTraps && !bm.cfg.UpgradeHeroes && !bm.cfg.UpgradeResources:
		want = "army"
	case (bm.cfg.UpgradeDefenses || bm.cfg.UpgradeTraps) && !bm.cfg.UpgradeArmy && !bm.cfg.UpgradeHeroes && !bm.cfg.UpgradeResources:
		want = "defense"
	case bm.cfg.UpgradeHeroes && !bm.cfg.UpgradeArmy && !bm.cfg.UpgradeDefenses && !bm.cfg.UpgradeTraps && !bm.cfg.UpgradeResources:
		want = "hero"
	case bm.cfg.UpgradeResources && !bm.cfg.UpgradeArmy && !bm.cfg.UpgradeDefenses && !bm.cfg.UpgradeTraps && !bm.cfg.UpgradeHeroes:
		want = "resource"
	}
	types := []struct {
		name   string
		fx, fy float64
	}{
		{"army", 0.253, 0.340},
		{"defense", 0.420, 0.340},
		{"hero", 0.588, 0.340},
		{"resource", 0.253, 0.421},
		{"new", 0.420, 0.421},
		{"permanent", 0.588, 0.421},
	}
	for _, t := range types {
		p := filterPopupPoint(bm.cal, t.fx, t.fy)
		selected := menuButtonSelected(screen, p.X, p.Y)
		bm.logger.Info().Str("type", t.name).Int("x", p.X).Int("y", p.Y).Bool("selected", selected).Str("want", want).Msg("build menu type button state")
		if want == "" {
			// Clear whatever single Type is selected; one tap toggles it off.
			if selected {
				bm.logger.Info().Str("type", t.name).Msg("clearing build menu type filter")
				_ = bm.client.Tap(p.X, p.Y)
				time.Sleep(400 * time.Millisecond)
				break
			}
			continue
		}
		if t.name == want && !selected {
			bm.logger.Info().Str("type", t.name).Msg("setting build menu type filter")
			_ = bm.client.Tap(p.X, p.Y)
			time.Sleep(400 * time.Millisecond)
		}
		if t.name == want {
			break
		}
	}

	// Resource: clear every selected currency. Unselected currencies mean "no
	// constraint"; a selected one ANDs with Type and hides rows paid in the
	// others (live: Army + Gold showed "Nothing to see here").
	for _, fx := range []float64{0.231, 0.401, 0.569} {
		p := filterPopupPoint(bm.cal, fx, 0.206)
		if !menuButtonSelected(screen, p.X, p.Y) {
			continue
		}
		bm.logger.Info().Int("x", p.X).Int("y", p.Y).Msg("clearing build menu resource filter")
		_ = bm.client.Tap(p.X, p.Y)
		time.Sleep(220 * time.Millisecond)
	}

	// Sorting: only the two priorities that name an order have a button. Any
	// previous sort is cleared when the strategy does not ask for one.
	costMax := filterPopupPoint(bm.cal, 0.253, 0.552)
	costMin := filterPopupPoint(bm.cal, 0.420, 0.552)
	switch bm.cfg.UpgradeStrategy {
	case "cheapest_first":
		if !menuButtonSelected(screen, costMin.X, costMin.Y) {
			bm.logger.Info().Msg("setting build menu sorting to cost min")
			_ = bm.client.Tap(costMin.X, costMin.Y)
			time.Sleep(220 * time.Millisecond)
		}
	case "most_expensive_first":
		if !menuButtonSelected(screen, costMax.X, costMax.Y) {
			bm.logger.Info().Msg("setting build menu sorting to cost max")
			_ = bm.client.Tap(costMax.X, costMax.Y)
			time.Sleep(220 * time.Millisecond)
		}
	default:
		for _, p := range []image.Point{costMax, costMin} {
			if menuButtonSelected(screen, p.X, p.Y) {
				bm.logger.Info().Msg("clearing build menu sorting")
				_ = bm.client.Tap(p.X, p.Y)
				time.Sleep(220 * time.Millisecond)
			}
		}
	}

	// Dismiss the popup on the village side so no menu row is tapped.
	_ = bm.client.Tap(outside.X, outside.Y)
	time.Sleep(700 * time.Millisecond)
	return nil
}

func (bm *BuilderManager) ApplyLabInGameFilter(strategy string, k float64) {
	filterPt := image.Pt(int(554*k), int(32*k))
	bm.logger.Info().Str("strategy", strategy).Msg("applying in-game filter for lab research")
	_ = bm.client.Tap(filterPt.X, filterPt.Y)
	time.Sleep(500 * time.Millisecond)

	switch strategy {
	case "spells_first":
		// Tap Spells filter button ~(416, 265)
		_ = bm.client.Tap(int(367*k), int(234*k))
	case "troops_first":
		// Tap Troops filter button ~(255, 265)
		_ = bm.client.Tap(int(225*k), int(234*k))
	case "siege_first":
		// Tap Siege filter button ~(579, 265)
		_ = bm.client.Tap(int(511*k), int(234*k))
	case "cheapest_first":
		// Tap Cost Min sorting button ~(416, 425)
		_ = bm.client.Tap(int(367*k), int(375*k))
	}
	time.Sleep(400 * time.Millisecond)

	// Dismiss filter popup by tapping backdrop ~(100, 300)
	_ = bm.client.Tap(int(100*k), int(300*k))
	time.Sleep(500 * time.Millisecond)
}

func (bm *BuilderManager) executeLabUpgrade(stats BuilderOverviewStats, k float64) {
	bm.logger.Info().Str("strategy", bm.cfg.LabStrategy).Bool("allow_de", bm.cfg.LabAllowDE).Msg("selecting lab research")

	// Apply in-game filter first to align menu items to strategy
	if bm.cfg.LabStrategy != "" {
		bm.ApplyLabInGameFilter(bm.cfg.LabStrategy, k)
		// Re-inspect items after filter application
		stats = bm.inspectMenuFrame()
	}

	// Target suggested research item (row 1 ref ~680, 220)
	tapPt := image.Pt(int(680*k), int(220*k))
	selectedName := "Suggested Lab Item"

	var candidates []UpgradeItem
	for _, it := range stats.Items {
		if !bm.cfg.LabAllowDE && it.Currency == "dark_elixir" {
			continue
		}
		candidates = append(candidates, it)
	}

	if len(candidates) > 0 {
		best := candidates[0]
		switch bm.cfg.LabStrategy {
		case "spells_first":
			for _, it := range candidates {
				if it.Category == "spells" {
					best = it
					bm.logger.Info().Str("spell", it.Name).Msg("prioritizing spell research")
					break
				}
			}
		case "troops_first":
			for _, it := range candidates {
				if it.Category == "troops" {
					best = it
					bm.logger.Info().Str("troop", it.Name).Msg("prioritizing troop research")
					break
				}
			}
		case "siege_first":
			for _, it := range candidates {
				if it.Category == "siege" {
					best = it
					bm.logger.Info().Str("siege", it.Name).Msg("prioritizing siege machine research")
					break
				}
			}
		case "cheapest_first":
			for _, it := range candidates {
				if it.Cost > 0 && (best.Cost == 0 || it.Cost < best.Cost) {
					best = it
				}
			}
		}

		if best.Region.W > 0 {
			tapPt = best.Region.Center()
			selectedName = best.Name
		}
	}

	bm.logger.Info().Str("item", selectedName).Int("x", tapPt.X).Int("y", tapPt.Y).Msg("tapping research item")
	_ = bm.client.Tap(tapPt.X, tapPt.Y)
	time.Sleep(1400 * time.Millisecond)

	if bm.cfg.DryRun {
		bm.logger.Info().Str("item", selectedName).Msg("dry run: lab research modal inspected; dismissing safely without confirming")
		_ = bm.CloseModalDialog()
		time.Sleep(500 * time.Millisecond)
		return
	}

	confirmPt := image.Pt(int(656*k), int(560*k))
	bm.logger.Info().Int("x", confirmPt.X).Int("y", confirmPt.Y).Msg("confirming lab research")
	_ = bm.client.Tap(confirmPt.X, confirmPt.Y)
	time.Sleep(1000 * time.Millisecond)

	_ = bm.CloseModalDialog()
	time.Sleep(500 * time.Millisecond)

	if bm.cfg.AutoPets {
		_ = bm.OpenMenu()
	}
}

func (bm *BuilderManager) executePetUpgrade(stats BuilderOverviewStats, k float64) {
	bm.logger.Info().Interface("priorities", bm.cfg.PetPriorities).Msg("selecting hero pet upgrade")

	tapPt := image.Pt(int(680*k), int(150*k))

	// Match available pets against PetPriorities list. With no match there is
	// nothing the user asked for: tapping the fallback row would spend dark
	// elixir on a pet the priority list never selected.
	matched := false
	for _, priorityPet := range bm.cfg.PetPriorities {
		for _, it := range stats.Items {
			if strings.Contains(strings.ToLower(it.Name), strings.ToLower(priorityPet)) {
				tapPt = it.Region.Center()
				matched = true
				bm.logger.Info().Str("pet", it.Name).Msg("matched prioritized hero pet")
				break
			}
		}
		if matched {
			break
		}
	}
	if !matched {
		bm.logger.Info().Msg("no available pet matches the priority list; skipping pet upgrade")
		return
	}

	bm.logger.Info().Int("x", tapPt.X).Int("y", tapPt.Y).Msg("tapping pet item")
	_ = bm.client.Tap(tapPt.X, tapPt.Y)
	time.Sleep(1400 * time.Millisecond)

	if bm.cfg.DryRun {
		bm.logger.Info().Msg("dry run: pet upgrade modal inspected; dismissing safely without confirming")
		_ = bm.CloseModalDialog()
		time.Sleep(500 * time.Millisecond)
		return
	}

	confirmPt := image.Pt(int(656*k), int(560*k))
	bm.logger.Info().Int("x", confirmPt.X).Int("y", confirmPt.Y).Msg("confirming pet upgrade")
	_ = bm.client.Tap(confirmPt.X, confirmPt.Y)
	time.Sleep(1000 * time.Millisecond)

	_ = bm.CloseModalDialog()
	time.Sleep(500 * time.Millisecond)
}

func (bm *BuilderManager) isCategoryAllowed(cat string) bool {
	switch cat {
	case "heroes":
		return bm.cfg.UpgradeHeroes
	case "defenses":
		return bm.cfg.UpgradeDefenses
	case "resources":
		return bm.cfg.UpgradeResources
	case "army":
		return bm.cfg.UpgradeArmy
	case "traps":
		return bm.cfg.UpgradeTraps
	default:
		// Names the toggles do not cover (unrecognised OCR, or a lab category
		// leaking into the menu) follow the menu only while the user has every
		// category enabled; once they filter, nothing sneaks past the filters.
		return bm.cfg.UpgradeHeroes && bm.cfg.UpgradeDefenses && bm.cfg.UpgradeResources &&
			bm.cfg.UpgradeArmy && bm.cfg.UpgradeTraps
	}
}

func (bm *BuilderManager) inspectMenuFrame() BuilderOverviewStats {
	stats := BuilderOverviewStats{
		FreeBuilders:  0,
		TotalBuilders: 5,
		LabFree:       false,
		PetsFree:      false,
	}

	mat, err := bm.client.CaptureToMat()
	if err != nil || mat.Empty() {
		return stats
	}
	defer mat.Close()

	// Write temporary frame to disk for Apple Vision OCR
	tmpFile := filepath.Join(os.TempDir(), fmt.Sprintf("builder_menu_%d.png", time.Now().UnixNano()))
	if ok := gocv.IMWrite(tmpFile, mat); !ok {
		return stats
	}
	defer func() { _ = os.Remove(tmpFile) }()

	regions, err := vision.OCRTextRegions(tmpFile)
	if err != nil {
		bm.logger.Debug().Err(err).Msg("ocr failed on builder menu frame; using fallback coordinates")
		return stats
	}

	stats = ParseBuilderOverviewOCR(regions)
	return stats
}

// parseTabCounter reads a bottom-tab counter such as "1/6" or "2/5". The
// slash is the first glyph OCR drops on this font (live frame: "V6" and
// "V/2"), so the leading V/v is read as a 1 and a two-character token without
// a slash is read digit-by-digit rather than as one number.
func parseTabCounter(txt string) (int, int, bool) {
	clean := strings.NewReplacer(" ", "", "V", "1", "v", "1").Replace(strings.TrimSpace(txt))
	if strings.Contains(clean, "/") {
		parts := strings.SplitN(clean, "/", 2)
		free, errFree := strconv.Atoi(parts[0])
		total, errTotal := strconv.Atoi(parts[1])
		if errFree == nil && errTotal == nil {
			return free, total, true
		}
		return 0, 0, false
	}
	if len(clean) == 2 && clean[0] >= '0' && clean[0] <= '9' && clean[1] >= '0' && clean[1] <= '9' {
		return int(clean[0] - '0'), int(clean[1] - '0'), true
	}
	return 0, 0, false
}

func isDurationText(txt string) bool {
	clean := strings.ToLower(strings.TrimSpace(txt))
	clean = strings.Trim(clean, "©•:(øOG ")
	if clean == "sd" || clean == "5d" {
		return true
	}
	hasDigit := false
	hasUnit := false
	for _, ch := range clean {
		if ch >= '0' && ch <= '9' {
			hasDigit = true
		}
		if ch == 'd' || ch == 'h' || ch == 'm' || ch == 's' || ch == 'д' || ch == 'н' || ch == 'м' {
			hasUnit = true
		}
	}
	return hasDigit && hasUnit
}

// ParseBuilderOverviewOCR parses recognized text regions from a builder overview frame.
func ParseBuilderOverviewOCR(regions []vision.TextRegion) BuilderOverviewStats {
	var stats BuilderOverviewStats
	stats.TotalBuilders = 5

	// First pass: locate bottom tab counters and header Y ("Suggested", "Filtered", "Assigned")
	suggestedY := 110 // default threshold
	builderX := 0
	for _, r := range regions {
		txt := strings.ToLower(r.Text)
		if strings.Contains(txt, "suggested") || strings.Contains(txt, "filtered") ||
			(strings.Contains(txt, "assigned") && !strings.Contains(txt, "un")) {
			suggestedY = r.Y
		}
		// The builder tab is the leftmost counter on the bottom row. OCR
		// regularly loses the slash (live: "V6" for "1/6"), and the lab/pet
		// counters share the row, so only the leftmost valid count is used.
		if r.Y >= 680 && r.X < 700 {
			if free, total, ok := parseTabCounter(r.Text); ok && total >= 2 && total <= 6 && free <= total {
				if builderX == 0 || r.X < builderX {
					builderX = r.X
					stats.FreeBuilders = free
					stats.TotalBuilders = total
				}
			}
		}
		if r.Y >= 680 {
			if strings.Contains(r.Text, "0/1") || strings.Contains(r.Text, "0л1") || strings.Contains(r.Text, "oл1") {
				// Tab counter shows busy
			} else if strings.Contains(r.Text, "1/1") {
				if r.X >= 700 && r.X <= 800 {
					stats.LabFree = true
				} else if r.X >= 880 {
					stats.PetsFree = true
				}
			}
		}
	}

	// Second pass: detect suggested, filtered, and assigned items inside the overview panel
	for _, r := range regions {
		txt := strings.ToLower(r.Text)
		if r.X >= 590 && r.Y >= suggestedY+10 && r.Y <= 680 {
			cat := classifyItemCategory(txt)
			if cat != "" {
				cost := 0
				if !isDurationText(txt) {
					cost = parseResourceCost(txt)
				}
				for _, c := range regions {
					if c.X > r.X && c.Y >= r.Y-35 && c.Y <= r.Y+45 {
						if isDurationText(c.Text) {
							continue
						}
						if cCost := parseResourceCost(c.Text); cCost > 0 {
							cost = cCost
							break
						}
					}
				}
				curr := parseCurrency(cat, txt)
				stats.Items = append(stats.Items, UpgradeItem{
					Name:     r.Text,
					Category: cat,
					Cost:     cost,
					Currency: curr,
					Region:   r,
				})
			}
		}
	}
	return stats
}

func classifyItemCategory(txt string) string {
	txt = strings.ToLower(txt)
	norm := strings.ReplaceAll(txt, " ", "")

	// 1. Traps first: must beat heroes (seeking air mine != king), troops (giant bomb != giant), resources (air mine != mine)
	if strings.Contains(norm, "bomb") || strings.Contains(norm, "spring") ||
		strings.Contains(norm, "airmine") || strings.Contains(norm, "skeleton") ||
		strings.Contains(norm, "tornado") {
		return "traps"
	}

	// 2. Defenses: must beat troops (archer tower != archer, wizard tower != wizard)
	if strings.Contains(norm, "cannon") || strings.Contains(norm, "archertower") ||
		strings.Contains(norm, "wizardtower") || strings.Contains(norm, "mortar") ||
		strings.Contains(norm, "airdefense") || strings.Contains(norm, "tesla") ||
		strings.Contains(norm, "x-bow") || strings.Contains(norm, "xbow") ||
		strings.Contains(norm, "inferno") || strings.Contains(norm, "eagle") ||
		strings.Contains(norm, "scattershot") || strings.Contains(norm, "monolith") ||
		strings.Contains(norm, "spelltower") || strings.Contains(norm, "revengetower") ||
		strings.Contains(norm, "ricochet") {
		return "defenses"
	}

	// 3. Resources: must beat spells (storage != rage) and siege (dark elixir drill != battle drill)
	if strings.Contains(norm, "collector") || strings.Contains(norm, "mine") ||
		(!strings.Contains(txt, "battle") && strings.Contains(norm, "drill")) ||
		strings.Contains(norm, "storage") {
		return "resources"
	}

	// 4. Heroes
	if strings.Contains(txt, "king") || strings.Contains(txt, "queen") ||
		strings.Contains(txt, "warden") || strings.Contains(txt, "champion") ||
		strings.Contains(txt, "prince") {
		return "heroes"
	}

	// 5. Pets (must beat spells so Poison Lizard != spell)
	if strings.Contains(txt, "yak") || strings.Contains(txt, "frosty") ||
		strings.Contains(txt, "jelly") || strings.Contains(txt, "owl") || strings.Contains(txt, "owi") ||
		strings.Contains(txt, "l.a.s.s.i") || strings.Contains(txt, "lassi") ||
		strings.Contains(txt, "unicorn") || strings.Contains(txt, "lizard") ||
		strings.Contains(txt, "diggy") || strings.Contains(txt, "fox") ||
		strings.Contains(txt, "phoenix") {
		return "pets"
	}

	// 6. Siege Machines (must beat spells so battle blimp != bat)
	if strings.Contains(txt, "slammer") || strings.Contains(txt, "blimp") ||
		strings.Contains(txt, "wrecker") || strings.Contains(txt, "launcher") ||
		strings.Contains(txt, "flinger") || strings.Contains(txt, "drill") ||
		strings.Contains(txt, "siege") {
		return "siege"
	}

	// 7. Army Buildings (before spells: "Dark Spell Factory" is a building)
	if strings.Contains(norm, "barracks") || strings.Contains(norm, "camp") ||
		strings.Contains(norm, "spellfactory") || strings.Contains(norm, "workshop") ||
		strings.Contains(norm, "pethouse") || strings.Contains(norm, "blacksmith") {
		return "army"
	}

	// 8. Spells
	if strings.Contains(txt, "spell") || strings.Contains(txt, "lightning") ||
		strings.Contains(txt, "heal") || strings.Contains(txt, "jump") ||
		strings.Contains(txt, "freeze") || strings.Contains(txt, "clone") ||
		strings.Contains(txt, "invis") || strings.Contains(txt, "recall") ||
		strings.Contains(txt, "revive") || strings.Contains(txt, "poison") ||
		strings.Contains(txt, "earthquake") || strings.Contains(txt, "haste") ||
		strings.Contains(txt, "overgrowth") ||
		(!strings.Contains(txt, "storage") && strings.Contains(txt, "rage")) ||
		(!strings.Contains(txt, "battle") && strings.Contains(txt, "bat")) {
		return "spells"
	}

	// 9. Troops
	if strings.Contains(txt, "barbarian") || strings.Contains(txt, "archer") ||
		strings.Contains(txt, "giant") || strings.Contains(txt, "goblin") ||
		strings.Contains(txt, "wall breaker") || strings.Contains(norm, "wallbreaker") ||
		strings.Contains(txt, "balloon") || strings.Contains(txt, "wizard") ||
		strings.Contains(txt, "dragon") || strings.Contains(txt, "pekka") ||
		strings.Contains(txt, "p.e.k.k.a") || strings.Contains(txt, "miner") ||
		strings.Contains(txt, "yeti") || strings.Contains(txt, "titan") ||
		strings.Contains(txt, "root rider") || strings.Contains(txt, "minion") ||
		strings.Contains(txt, "hog") || strings.Contains(txt, "valkyrie") ||
		strings.Contains(txt, "golem") || strings.Contains(txt, "witch") ||
		strings.Contains(txt, "hound") || strings.Contains(txt, "bowler") ||
		strings.Contains(txt, "headhunter") || strings.Contains(txt, "druid") ||
		strings.Contains(txt, "healer") {
		return "troops"
	}

	return ""
}

func parseResourceCost(txt string) int {
	if isDurationText(txt) {
		return 0
	}
	clean := strings.ReplaceAll(txt, " ", "")
	var digits strings.Builder
	for _, ch := range clean {
		if ch >= '0' && ch <= '9' {
			digits.WriteRune(ch)
		}
	}
	if val, err := strconv.Atoi(digits.String()); err == nil && val > 0 {
		return val
	}
	return 0
}

func parseCurrency(cat, txt string) string {
	txt = strings.ToLower(txt)
	norm := strings.ReplaceAll(txt, " ", "")
	if cat == "pets" {
		return "dark_elixir"
	}
	if cat == "heroes" {
		if strings.Contains(txt, "warden") {
			return "elixir"
		}
		return "dark_elixir"
	}
	if cat == "spells" {
		if strings.Contains(txt, "poison") || strings.Contains(txt, "earthquake") ||
			strings.Contains(txt, "haste") || strings.Contains(txt, "bat") ||
			strings.Contains(txt, "overgrowth") {
			return "dark_elixir"
		}
		return "elixir"
	}
	if cat == "troops" {
		if strings.Contains(txt, "minion") || strings.Contains(txt, "hog") ||
			strings.Contains(txt, "valkyrie") || strings.Contains(txt, "golem") ||
			strings.Contains(txt, "witch") || strings.Contains(txt, "hound") ||
			strings.Contains(txt, "bowler") || strings.Contains(txt, "headhunter") {
			return "dark_elixir"
		}
		return "elixir"
	}
	if cat == "defenses" {
		if strings.Contains(txt, "monolith") {
			return "dark_elixir"
		}
		return "gold"
	}
	if cat == "resources" {
		// Mines cost elixir, collectors cost gold; storages cost their own
		// resource. Matching the full name keeps "Gold Mine" out of the gold
		// spend cap, where it never belonged.
		if strings.Contains(txt, "collector") || strings.Contains(norm, "goldstorage") {
			return "gold"
		}
		return "elixir"
	}
	if cat == "siege" || cat == "army" {
		return "elixir"
	}
	return "gold"
}
