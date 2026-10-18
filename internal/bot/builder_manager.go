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
	k := bm.cal.DisplayScale()
	// Builder icon button located at bottom-right of top HUD:
	// In the modern UI at 1024x768 reference (835, 540) maps to (947, 611).
	pt := image.Pt(int(835*k), int(540*k))
	bm.logger.Debug().Int("x", pt.X).Int("y", pt.Y).Msg("opening builder overview menu")
	if err := bm.client.Tap(pt.X, pt.Y); err != nil {
		return fmt.Errorf("tap builder menu icon: %w", err)
	}
	time.Sleep(1200 * time.Millisecond)
	return nil
}

// CloseMenu closes the Builder Overview modal by tapping the safe dismiss area on the left.
func (bm *BuilderManager) CloseMenu() error {
	if bm.cal == nil || bm.client == nil {
		return errors.New("uninitialized calibration or adb client")
	}
	k := bm.cal.DisplayScale()
	// Tapping in the dark village backdrop (reference 88, 265 -> ~100, 300) safely dismisses the modal.
	pt := image.Pt(int(88*k), int(265*k))
	bm.logger.Debug().Int("x", pt.X).Int("y", pt.Y).Msg("closing builder overview menu")
	if err := bm.client.Tap(pt.X, pt.Y); err != nil {
		return fmt.Errorf("dismiss builder menu: %w", err)
	}
	time.Sleep(800 * time.Millisecond)
	return nil
}

// CloseModalDialog taps the top-right X button on a modal confirmation dialog.
func (bm *BuilderManager) CloseModalDialog() error {
	k := bm.cal.DisplayScale()
	// Top right X button on full confirmation modal: reference (845, 92) -> live (958, 105)
	pt := image.Pt(int(845*k), int(92*k))
	bm.logger.Debug().Int("x", pt.X).Int("y", pt.Y).Msg("closing confirmation modal dialog")
	return bm.client.Tap(pt.X, pt.Y)
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

func (bm *BuilderManager) executeBuildingUpgrade(stats BuilderOverviewStats, k float64) {
	bm.logger.Info().Str("strategy", bm.cfg.UpgradeStrategy).Msg("selecting best building upgrade")

	// Determine target item to tap
	tapPt := image.Pt(int(706*k), int(309*k)) // default row 1: ~(800, 350)
	selectedName := "Suggested Row 1"

	var eligible []UpgradeItem
	for _, it := range stats.Items {
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
		bm.logger.Info().Msg("no eligible building upgrades found matching filters; skipping building upgrade")
		return
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
	tapPt = best.Region.Center()
	selectedName = best.Name

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

	// Confirm dialog: standard upgrade button with resources is at reference (656, 560) -> live (744, 635)
	confirmPt := image.Pt(int(656*k), int(560*k))
	bm.logger.Info().Int("x", confirmPt.X).Int("y", confirmPt.Y).Msg("confirming building upgrade")
	_ = bm.client.Tap(confirmPt.X, confirmPt.Y)
	time.Sleep(1000 * time.Millisecond)

	// In case gems dialog or insufficient resources popup opened, safely close modal
	_ = bm.CloseModalDialog()
	time.Sleep(500 * time.Millisecond)

	// Re-open overview menu
	_ = bm.OpenMenu()
}

func (bm *BuilderManager) ApplyLabInGameFilter(strategy string, k float64) {
	filterPt := image.Pt(int(554*k), int(32*k))
	bm.logger.Info().Str("strategy", strategy).Msg("applying in-game filter for lab research")
	_ = bm.client.Tap(filterPt.X, filterPt.Y)
	time.Sleep(500 * time.Millisecond)

	switch strategy {
	case "spells_first":
		// Tap Spells filter button ~(410, 340)
		_ = bm.client.Tap(int(362*k), int(300*k))
	case "troops_first":
		// Tap Troops filter button ~(250, 340)
		_ = bm.client.Tap(int(220*k), int(300*k))
	case "siege_first":
		// Tap Siege filter button ~(566, 340)
		_ = bm.client.Tap(int(500*k), int(300*k))
	case "cheapest_first":
		// Tap Cost Min sorting button ~(410, 550)
		_ = bm.client.Tap(int(362*k), int(485*k))
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

	// Match available pets against PetPriorities list
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
		return true
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

// ParseBuilderOverviewOCR parses recognized text regions from a builder overview frame.
func ParseBuilderOverviewOCR(regions []vision.TextRegion) BuilderOverviewStats {
	var stats BuilderOverviewStats
	stats.TotalBuilders = 5

	for _, r := range regions {
		txt := strings.ToLower(r.Text)
		// Match builder pattern e.g. "1/5", "2/5", "0/5" near bottom tabs
		if r.Y >= 680 {
			var free, total int
			if _, err := fmt.Sscanf(r.Text, "%d/%d", &free, &total); err == nil && total >= 2 && total <= 6 {
				stats.FreeBuilders = free
				stats.TotalBuilders = total
			}
			if strings.Contains(r.Text, "0/1") || strings.Contains(r.Text, "0л1") {
				// Tab counter shows busy
			} else if strings.Contains(r.Text, "1/1") {
				if r.X >= 700 && r.X <= 800 {
					stats.LabFree = true
				} else if r.X >= 880 {
					stats.PetsFree = true
				}
			}
		}

		// Detect suggested items in menu
		if r.X >= 550 && r.Y >= 140 && r.Y <= 680 {
			cat := classifyItemCategory(txt)
			if cat != "" {
				cost := parseResourceCost(txt)
				stats.Items = append(stats.Items, UpgradeItem{
					Name:     r.Text,
					Category: cat,
					Cost:     cost,
					Currency: parseCurrency(txt),
					Region:   r,
				})
			}
		}
	}
	return stats
}

func classifyItemCategory(txt string) string {
	// Spells
	if strings.Contains(txt, "spell") || strings.Contains(txt, "lightning") ||
		strings.Contains(txt, "heal") || strings.Contains(txt, "rage") ||
		strings.Contains(txt, "jump") || strings.Contains(txt, "freeze") ||
		strings.Contains(txt, "clone") || strings.Contains(txt, "invis") ||
		strings.Contains(txt, "recall") || strings.Contains(txt, "revive") ||
		strings.Contains(txt, "poison") || strings.Contains(txt, "earthquake") ||
		strings.Contains(txt, "haste") || strings.Contains(txt, "bat") ||
		strings.Contains(txt, "overgrowth") {
		return "spells"
	}
	// Siege Machines
	if strings.Contains(txt, "slammer") || strings.Contains(txt, "blimp") ||
		strings.Contains(txt, "wrecker") || strings.Contains(txt, "launcher") ||
		strings.Contains(txt, "flinger") || strings.Contains(txt, "drill") ||
		strings.Contains(txt, "siege") {
		return "siege"
	}
	// Troops
	if strings.Contains(txt, "barbarian") || strings.Contains(txt, "archer") ||
		strings.Contains(txt, "giant") || strings.Contains(txt, "goblin") ||
		strings.Contains(txt, "wall breaker") || strings.Contains(txt, "balloon") ||
		strings.Contains(txt, "wizard") || strings.Contains(txt, "dragon") ||
		strings.Contains(txt, "pekka") || strings.Contains(txt, "p.e.k.k.a") ||
		strings.Contains(txt, "miner") || strings.Contains(txt, "yeti") ||
		strings.Contains(txt, "titan") || strings.Contains(txt, "root rider") ||
		strings.Contains(txt, "minion") || strings.Contains(txt, "hog") ||
		strings.Contains(txt, "valkyrie") || strings.Contains(txt, "golem") ||
		strings.Contains(txt, "witch") || strings.Contains(txt, "hound") ||
		strings.Contains(txt, "bowler") || strings.Contains(txt, "headhunter") ||
		strings.Contains(txt, "druid") || strings.Contains(txt, "healer") {
		return "troops"
	}
	// Heroes
	if strings.Contains(txt, "king") || strings.Contains(txt, "queen") ||
		strings.Contains(txt, "warden") || strings.Contains(txt, "champion") ||
		strings.Contains(txt, "prince") {
		return "heroes"
	}
	// Defenses
	if strings.Contains(txt, "cannon") || strings.Contains(txt, "archer tower") ||
		strings.Contains(txt, "wizard tower") || strings.Contains(txt, "mortar") ||
		strings.Contains(txt, "air defense") || strings.Contains(txt, "tesla") ||
		strings.Contains(txt, "x-bow") || strings.Contains(txt, "inferno") ||
		strings.Contains(txt, "eagle") || strings.Contains(txt, "scattershot") ||
		strings.Contains(txt, "monolith") || strings.Contains(txt, "spell tower") ||
		strings.Contains(txt, "revenge tower") || strings.Contains(txt, "ricochet") {
		return "defenses"
	}
	// Resources
	if strings.Contains(txt, "collector") || strings.Contains(txt, "mine") ||
		strings.Contains(txt, "drill") || strings.Contains(txt, "storage") {
		return "resources"
	}
	// Army Buildings
	if strings.Contains(txt, "barracks") || strings.Contains(txt, "camp") ||
		strings.Contains(txt, "spell factory") || strings.Contains(txt, "workshop") ||
		strings.Contains(txt, "pet house") || strings.Contains(txt, "blacksmith") {
		return "army"
	}
	// Traps
	if strings.Contains(txt, "bomb") || strings.Contains(txt, "spring") ||
		strings.Contains(txt, "air mine") || strings.Contains(txt, "skeleton") ||
		strings.Contains(txt, "tornado") {
		return "traps"
	}
	return ""
}

func parseResourceCost(txt string) int {
	clean := strings.ReplaceAll(txt, " ", "")
	for _, field := range strings.Fields(clean) {
		if val, err := strconv.Atoi(field); err == nil && val > 100 {
			return val
		}
	}
	return 0
}

func parseCurrency(txt string) string {
	if strings.Contains(txt, "dark") || strings.Contains(txt, "de") {
		return "dark_elixir"
	}
	if strings.Contains(txt, "elixir") {
		return "elixir"
	}
	return "gold"
}
