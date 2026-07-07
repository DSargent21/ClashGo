package attack

import (
	"image"
	"math"
	"math/rand"
	"sort"
	"strings"
	"time"

	"github.com/Ducky705/ClashGO/pkg/formula"
	"github.com/Ducky705/ClashGO/pkg/strategy"
	"github.com/rs/zerolog"
	"gocv.io/x/gocv"
)

// HeroManager handles hero-specific deployment logic.
type HeroManager struct {
	executor    *TapExecutor
	slotManager *SlotManager
	pCfg        PrecisionConfig
	formula     *formula.Formula
	targetEdge  string
	w, h        int
	logger      zerolog.Logger

	// OnDukeDeployed (debug-only). When non-nil and the unit being
	// deployed is the Dragon Duke, fire after resolveHeroTarget. The
	// orchestrator wires this to Executor.OnDukePick so legacy + new
	// paths funnel through a single observer. chosenEdge is always
	// equal to targetEdge in the current HeroManager behavior — Duke
	// falls through to the chosen edge with a random point along it.
	OnDukeDeployed func(targetEdge string)
}

// NewHeroManager creates a new hero manager. formula may be nil; when
// non-nil, per-unit formula entries override the legacy pCfg.Edges /
// dynamic red-zone deploy coordinates so the user can pin exact side
// positions via cmd/design_attack.
func NewHeroManager(
	executor *TapExecutor,
	slotManager *SlotManager,
	pCfg PrecisionConfig,
	targetEdge string,
	w, h int,
	formula *formula.Formula,
	logger zerolog.Logger,
) *HeroManager {
	return &HeroManager{
		executor:    executor,
		slotManager: slotManager,
		pCfg:        pCfg,
		formula:     formula,
		targetEdge:  targetEdge,
		w:           w,
		h:           h,
		logger:      logger.With().Str("component", "hero_manager").Logger(),
	}
}

// HeroDeployment represents a resolved hero deployment.
type HeroDeployment struct {
	Unit      strategy.Unit
	Slot      *TrackedSlot
	IsAbility bool
}

// DeployHeroes deploys all heroes and activates abilities.
// Returns list of deployed hero slots for ability tracking.
func (hm *HeroManager) DeployHeroes(heroUnits []strategy.Unit, screen gocv.Mat) []*TrackedSlot {
	// 1. Resolve hero slots from strategy
	var deployments []HeroDeployment
	for _, unit := range heroUnits {
		unitName := strings.ToLower(strings.TrimSpace(unit.Name))
		isAbility := unit.Pattern == "Ability"

		if isAbility {
			continue // Handle abilities after deployment
		}

		slot := hm.slotManager.GetSlot(unitName)
		if slot == nil {
			hm.logger.Warn().Str("unit", unit.Name).Msg("hero not found in bar")
			continue
		}

		deployments = append(deployments, HeroDeployment{
			Unit:      unit,
			Slot:      slot,
			IsAbility: false,
		})
	}

	// 2. Separate main and bonus heroes
	var mainDeployments []HeroDeployment
	var bonusDeployments []HeroDeployment

	for _, d := range deployments {
		if isHeroStatic(d.Unit.Name) {
			mainDeployments = append(mainDeployments, d)
		} else {
			bonusDeployments = append(bonusDeployments, d)
		}
	}

	// 3. Sort main by confidence descending
	sort.Slice(mainDeployments, func(i, j int) bool {
		return mainDeployments[i].Slot.Confidence > mainDeployments[j].Slot.Confidence
	})

	// 4. Deploy main heroes
	var deployedSlots []*TrackedSlot
	for _, d := range mainDeployments {
		hm.logger.Info().
			Str("unit", d.Unit.Name).
			Int("x", d.Slot.X).
			Float64("conf", d.Slot.Confidence).
			Msg("deploying main hero")

		if hm.deploySingleHero(d) {
			deployedSlots = append(deployedSlots, d.Slot)
			hm.slotManager.MarkDeployed(d.Unit.Name)
		} else {
			// Failed hero drop: keep state as SlotAttempted so the
			// sweeper picks it up via deployHeroSlotOnce and retries.
			hm.slotManager.RecordAttempt(d.Unit.Name, false)
		}
	}

	// 5. Deploy bonus heroes
	for _, d := range bonusDeployments {
		hm.logger.Info().
			Str("unit", d.Unit.Name).
			Int("x", d.Slot.X).
			Msg("deploying bonus hero")

		if hm.deploySingleHero(d) {
			deployedSlots = append(deployedSlots, d.Slot)
			hm.slotManager.MarkDeployed(d.Unit.Name)
		} else {
			// same failed-deal semantics as main heroes: keep state as
			// SlotAttempted so the sweeper can retry.
			hm.slotManager.RecordAttempt(d.Unit.Name, false)
		}
	}

	// 6. Activate abilities after all heroes are down
	hm.activateAbilities(deployedSlots)

	return deployedSlots
}

// deploySingleHero deploys a single hero at the edge point with a
// delta-based verify. Live data shows the second-verify retry never
// recovered a hero on the user's setup (all 4 heroes got WRN and the
// bot then re-deployed them via sweep anyway). The second-pass block
// has been removed entirely; recovery now lives in the sweep phase's
// deployHeroSlotOnce.
//
// Steps:
//  0. Capture pre-ratio BEFORE any tap.
//  1. SELECT the hero slot.
//  2. Wait for selection animation (~350ms).
//  3. Drop with a tight cluster of 3 jittered taps (+/- 3 px).
//  4. Settle window so the slot transitions out of "highlighted".
//  5. Capture post-drop ratio; DELTA verify with threshold 0.15.
//     On failure, the sweeper retries.
func (hm *HeroManager) deploySingleHero(d HeroDeployment) bool {
	slot := d.Slot

	// 0. Capture pre-deploy baseline BEFORE any tap.
	var preRatio float64
	var capturedPre bool
	if preScreen, err := hm.executor.CaptureFresh(); err == nil {
		preRatio = GetSlotActivityRatioStatic(preScreen, slot.X, slot.Y, hm.w)
		preScreen.Close()
		capturedPre = true
	}

	// 1. SELECT the hero slot on the troop bar.
	hm.executor.TapSlot(slot, 8)
	hm.logger.Debug().
		Str("unit", d.Unit.Name).
		Int("slot_x", slot.X).
		Int("slot_y", slot.Y).
		Msg("selected hero slot")

	// 2. CoC selection-animation window.
	hm.executor.client.HumanSleep(350, 60)

	// 3. Drop with a tight cluster of 3 jittered taps.
	p1, _ := hm.resolveHeroTarget(slot)
	j1 := hm.executor.addJitter(p1, 3)
	j2 := hm.executor.addJitter(p1, 3)
	j3 := hm.executor.addJitter(p1, 3)
	hm.executor.client.TapTriple(j1.X, j1.Y, 12.0, j2.X, j2.Y, 12.0, j3.X, j3.Y, 12.0)

	// 3a. Duke observer (new path). Fires AFTER the TapTriple so
	// downstream observers see "Duke committed" not "Duke about to".
	if hm.OnDukeDeployed != nil && strings.Contains(strings.ToLower(d.Unit.Name), "duke") {
		hm.OnDukeDeployed(hm.targetEdge)
	}

	// 4. Settle window so the slot transitions out of selected.
	// 350±50ms is symmetric with the front-side cursor wait and
	// empirically gives CoC enough time to commit the slot icon
	// transition. Lower than 300ms produces false verify negatives.
	hm.executor.client.HumanSleep(350, 50)

	// 5. Capture post-drop ratio and verify via DELTA.
	postRatio, capturedPost := hm.captureSlotRatio(slot)
	delta := preRatio - postRatio

	// Delta verify. A successful drop transitions the slot from a
	// bright icon (0.39 Duke to 0.67 BK) to a cooldown silhouette
	// (0.10-0.30). delta = pre - post >= 0.15 reliably distinguishes
	// the two regardless of the hero's per-icon baseline activity.
	const heroDroppedDelta = 0.15
	if capturedPre && capturedPost && delta >= heroDroppedDelta {
		hm.logger.Info().
			Str("unit", d.Unit.Name).
			Int("slot_x", slot.X).
			Int("slot_y", slot.Y).
			Interface("target", p1).
			Float64("pre_ratio", preRatio).
			Float64("post_ratio", postRatio).
			Float64("delta", delta).
			Msg("hero deployed (slot-selected + multi-tap sent)")
		return true
	}

	// Verify failed; the second-verify retry has been removed. Recovery
	// lives in the sweep phase's deployHeroSlotOnce, which has its own
	// pre/post delta verify with a stable settle window.
	hm.logger.Warn().
		Str("unit", d.Unit.Name).
		Int("slot_x", slot.X).
		Int("slot_y", slot.Y).
		Interface("target", p1).
		Float64("pre_ratio", preRatio).
		Float64("post_ratio", postRatio).
		Bool("captured_post", capturedPost).
		Float64("delta", delta).
		Msg("hero slot did not visibly transition to cooldown (capture failed or delta below threshold); deployment may have failed - sweep will retry")
	return false
}

// captureSlotRatio takes a fresh screen and reads the slot's activity
// ratio at (slot.X, slot.Y). Returns (ratio, capturedOK).
func (hm *HeroManager) captureSlotRatio(slot *TrackedSlot) (float64, bool) {
	postScreen, err := hm.executor.CaptureFresh()
	if err != nil {
		return 0, false
	}
	defer postScreen.Close()
	return GetSlotActivityRatioStatic(postScreen, slot.X, slot.Y, hm.w), true
}

// activateAbilities activates abilities for deployed heroes.
func (hm *HeroManager) activateAbilities(deployedSlots []*TrackedSlot) {
	if len(deployedSlots) == 0 {
		return
	}

	hm.logger.Info().Int("count", len(deployedSlots)).Msg("activating hero abilities")

	// Wait for heroes to land on map
	time.Sleep(600 * time.Millisecond)

	for _, slot := range deployedSlots {
		// Heroes always remain on bar (cooldown) - just tap ability
		hm.executor.TapHeroAbility(slot)
		hm.logger.Info().
			Str("unit", slot.UnitName).
			Int("x", slot.X).
			Msg("hero ability activated")
		time.Sleep(200 * time.Millisecond)
	}
}

// resolveHeroTarget returns the deployment point for a hero.
//
// Order of precedence:
//  1. Formula entry (the user pinned a specific point or line).
//  2. Random interpolation along the chosen edge.
func (hm *HeroManager) resolveHeroTarget(slot *TrackedSlot) (image.Point, image.Point) {
	unitName := strings.ToLower(slot.UnitName)

	// Formula FIRST — the user explicitly pinned where every hero should
	// land, sidestepping the legacy random-interpolation heuristic that
	// placed all heroes on a single chosen edge (and the Duke on a
	// second corner, hence the "two sides in the corner" symptom).
	if entry, ok := hm.formulaEntry(unitName); ok {
		switch {
		case entry.IsPoint() && entry.P != nil:
			pt := entry.P.Image()
			return pt, pt
		case entry.IsLine() && entry.P1 != nil && entry.P2 != nil:
			p1 := entry.P1.Image()
			p2 := entry.P2.Image()
			t := rand.Float64()
			px := p1.X + int(float64(p2.X-p1.X)*t)
			py := p1.Y + int(float64(p2.Y-p1.Y)*t)
			return image.Pt(px, py), image.Pt(px, py)
		}
	}

	// All heroes (including Dragon Duke) deploy on the chosen edge.
	// Previously Duke took an "adjacent corner" branch that picked a
	// different corner from the chosen target, so the bot ended up
	// attacking on two sides. Duke now falls through to the chosen
	// edge with the rest.
	edge, ok := hm.pCfg.Edges[hm.targetEdge]
	if !ok {
		return image.Pt(hm.w/2, hm.h/2), image.Pt(hm.w/2, hm.h/2)
	}
	scaled := ScaleEdge(edge, hm.pCfg.Width, hm.pCfg.Height, hm.w, hm.h)
	t := rand.Float64()
	px := scaled.P1.X + int(float64(scaled.P2.X-scaled.P1.X)*t)
	py := scaled.P1.Y + int(float64(scaled.P2.Y-scaled.P1.Y)*t)
	pt := image.Pt(px, py)
	return pt, pt
}

// offsetEdgeOutward pushes both P1 and P2 of an edge outward from the base center.
// This ensures deployment taps land outside the base walls, not inside.
func offsetEdgeOutward(p1, p2 image.Point, screenW, screenH, offsetPx int) (image.Point, image.Point) {
	cx, cy := screenW/2, screenH/2
	outward := func(p image.Point) image.Point {
		dx, dy := float64(p.X-cx), float64(p.Y-cy)
		dist := math.Sqrt(dx*dx + dy*dy)
		if dist < 1.0 {
			return p
		}
		scale := (dist + float64(offsetPx)) / dist
		return image.Pt(int(float64(p.X-cx)*scale)+cx, int(float64(p.Y-cy)*scale)+cy)
	}
	return outward(p1), outward(p2)
}

// DeployTroops deploys a group of regular troops (non-hero, non-spell).
func (hm *HeroManager) DeployTroops(
	unit strategy.Unit,
	slot *TrackedSlot,
	pattern string,
	offset int,
	phasePattern string,
	screen gocv.Mat,
	detectedCount int,
) bool {
	unitName := strings.ToLower(strings.TrimSpace(unit.Name))
	isFourSides := pattern == "FourSides" || phasePattern == "FourSides"

	if isFourSides {
		hm.executor.TapDeployFourSides(hm.pCfg, hm.targetEdge, 12, 8)
		return true
	}

	// Formula check FIRST. When the user pinned this unit's deploy line
	// via cmd/design_attack, use those explicit coordinates and skip
	// the pCfg.Edges / dynamic red-zone fallback.
	if entry, ok := hm.formulaEntry(unit.Name); ok {
		ok := hm.deployFromFormula(unit, slot, entry, detectedCount)
		if ok {
			hm.slotManager.MarkDeployed(unitName)
		}
		return ok
	}

	// Get deployment target
	p1, p2 := hm.resolveTroopTarget(slot, offset)

	// Calculate tap count: use detected count if available, otherwise fallback
	tapCount := hm.resolveTapCount(unit, slot)
	if detectedCount > 0 {
		tapCount = detectedCount
		hm.logger.Info().
			Str("unit", unit.Name).
			Int("detected_count", detectedCount).
			Msg("using detected troop count")
	}

	// Point vs Line deployment
	if p1 == p2 {
		hm.logger.Info().Str("unit", unit.Name).Int("count", tapCount).Msg("deploying troop point")
		hm.executor.TapDeployPoint(p1, tapCount, 8)
	} else {
		hm.logger.Info().Str("unit", unit.Name).Int("count", tapCount).Msg("deploying troop line")
		hm.executor.TapDeployLine(p1, p2, tapCount, 10)
	}

	// Verify slot emptied
	time.Sleep(300 * time.Millisecond)
	freshScreen, err := hm.executor.CaptureFresh()
	if err == nil {
		defer freshScreen.Close()
		empty := isSlotEmptyStatic(freshScreen, slot.X, slot.Y, hm.w, hm.h)
		if empty {
			hm.slotManager.MarkDeployed(unitName)
			return true
		}
	}

	return false
}

// DeploySiege deploys a siege machine.
//
// Formula takes precedence: when the user authored a "point" or "line"
// entry for this siege (e.g. stone_slammer → {"type":"point","p":{...}}),
// the user-pinned geometry wins and the legacy pCfg.Edges path is
// skipped. Falls back to the dynamic red-zone edge line otherwise.
func (hm *HeroManager) DeploySiege(unit strategy.Unit, slot *TrackedSlot) bool {
	unitName := strings.ToLower(strings.TrimSpace(unit.Name))

	// Formula FIRST — pinned geometry wins.
	if entry, ok := hm.formulaEntry(unit.Name); ok {
		if hm.deploySiegeFromFormula(unit, slot, entry) {
			hm.slotManager.MarkDeployed(unitName)
			return true
		}
		// Formula entry exists but undeployable — fall through to legacy.
	}

	// Siege: deploy along edge line
	edge, ok := hm.pCfg.Edges[hm.targetEdge]
	if !ok {
		hm.logger.Warn().Msg("no edge configured for siege deployment")
		return false
	}
	scaled := ScaleEdge(edge, hm.pCfg.Width, hm.pCfg.Height, hm.w, hm.h)
	p1, p2 := scaled.P1, scaled.P2

	hm.logger.Info().Str("unit", unit.Name).Msg("deploying siege machine")
	hm.executor.TapDeployLine(p1, p2, 12, 10)

	// Verify
	time.Sleep(300 * time.Millisecond)
	freshScreen, err := hm.executor.CaptureFresh()
	if err == nil {
		defer freshScreen.Close()
		if isSlotEmptyStatic(freshScreen, slot.X, slot.Y, hm.w, hm.h) {
			hm.slotManager.MarkDeployed(unitName)
			return true
		}
	}

	return false
}

// resolveTroopTarget returns the deployment line for a troop.
func (hm *HeroManager) resolveTroopTarget(slot *TrackedSlot, offset int) (image.Point, image.Point) {
	edge, ok := hm.pCfg.Edges[hm.targetEdge]
	if !ok {
		return image.Pt(hm.w/2, hm.h/2), image.Pt(hm.w/2, hm.h/2)
	}
	scaled := ScaleEdge(edge, hm.pCfg.Width, hm.pCfg.Height, hm.w, hm.h)
	p1, p2 := scaled.P1, scaled.P2

	// Apply offset
	if offset > 0 {
		centerX, centerY := hm.w/2, hm.h/2
		pct := float64(offset) / 200.0
		p1 = image.Pt(int(float64(p1.X)+float64(centerX-p1.X)*pct), int(float64(p1.Y)+float64(centerY-p1.Y)*pct))
		p2 = image.Pt(int(float64(p2.X)+float64(centerX-p2.X)*pct), int(float64(p2.Y)+float64(centerY-p2.Y)*pct))
	}

	return p1, p2
}

// resolveTapCount returns the number of taps for a troop unit.
func (hm *HeroManager) resolveTapCount(unit strategy.Unit, slot *TrackedSlot) int {
	if unit.Amount != "All" && unit.Amount != "" {
		if val := parseAmount(unit.Amount); val > 0 {
			return val
		}
	}
	return 12 // Default
}

// parseAmount parses an amount string to int.
func parseAmount(s string) int {
	if s == "All" || s == "" {
		return 0
	}
	val := 0
	for _, c := range s {
		if c >= '0' && c <= '9' {
			val = val*10 + int(c-'0')
		}
	}
	return val
}

// formulaEntry is a nil-safe wrapper around Formula.LookUp for use by the
// deploy path. When the bot didn't load a formula (file missing or
// invalid) this returns (_, false) and the caller falls back to pCfg
// coordinates / dynamic red zone.
func (hm *HeroManager) formulaEntry(unitName string) (formula.UnitEntry, bool) {
	if hm.formula == nil {
		return formula.UnitEntry{}, false
	}
	return hm.formula.LookUp(unitName)
}

// deployFromFormula uses the user-pinned line/point coordinates from the
// formula instead of the pCfg.Edges / dynamic red-zone detection result.
// Counts fall back to detectedCount → unit.Amount → resolveTapCount.
func (hm *HeroManager) deployFromFormula(unit strategy.Unit, slot *TrackedSlot, entry formula.UnitEntry, detectedCount int) bool {
	count := hm.resolveFormulaCount(unit, slot, entry.Count, detectedCount)
	jitter := entry.Jitter
	if jitter <= 0 {
		jitter = 3
	}

	switch {
	case entry.IsLine() && entry.P1 != nil && entry.P2 != nil:
		p1 := entry.P1.Image()
		p2 := entry.P2.Image()
		hm.logger.Info().Str("unit", unit.Name).Int("count", count).
			Interface("p1", p1).Interface("p2", p2).
			Msg("formula-driven line troop deploy")
		hm.executor.TapDeployLine(p1, p2, count, jitter)
		return true
	case entry.IsPoint() && entry.P != nil:
		p := entry.P.Image()
		hm.logger.Info().Str("unit", unit.Name).Int("count", count).
			Interface("p", p).
			Msg("formula-driven point troop deploy")
		hm.executor.TapDeployPoint(p, count, jitter)
		return true
	default:
		hm.logger.Warn().Str("unit", unit.Name).Str("type", entry.Type).
			Msg("formula entry present but has no usable coordinates; falling back to legacy path")
		return false
	}
}

// deploySiegeFromFormula drops the siege at the user-pinned point (or
// along a pinned line for wrecker-style multi-tap drops). When the
// formula has no entry the caller falls back to the pCfg edge path.
func (hm *HeroManager) deploySiegeFromFormula(unit strategy.Unit, slot *TrackedSlot, entry formula.UnitEntry) bool {
	jitter := entry.Jitter
	if jitter <= 0 {
		jitter = 6
	}
	switch {
	case entry.IsPoint() && entry.P != nil:
		p := entry.P.Image()
		hm.logger.Info().Str("unit", unit.Name).Interface("p", p).
			Msg("formula-driven siege deploy")
		hm.executor.TapDeployPoint(p, 12, jitter)
		return true
	case entry.IsLine() && entry.P1 != nil && entry.P2 != nil:
		p1 := entry.P1.Image()
		p2 := entry.P2.Image()
		hm.logger.Info().Str("unit", unit.Name).Interface("p1", p1).Interface("p2", p2).
			Msg("formula-driven siege line deploy")
		hm.executor.TapDeployLine(p1, p2, 12, jitter)
		return true
	}
	return false
}

// resolveFormulaCount selects the tap count for a formula-driven unit,
// preferring (in order): explicit count → detected → YAML amount →
// heuristic default.
//
// Safety pad: when the formula has no Count AND detectedCount >= 6,
// pad by +1 to absorb single-digit OCR mis-reads. Live data showed the
// troop_counter sometimes reports 9 when the icon shows x10, dropping
// the last troop. Pads only at >= 6 so small counts (5 balloons, 3
// rage) are not over-deployed into the slot-empty-state, which would
// silently waste taps.
func (hm *HeroManager) resolveFormulaCount(unit strategy.Unit, slot *TrackedSlot, entryCount, detectedCount int) int {
	if entryCount > 0 {
		return entryCount
	}
	if detectedCount > 0 {
		if detectedCount >= 6 {
			return detectedCount + 1
		}
		return detectedCount
	}
	return hm.resolveTapCount(unit, slot)
}
