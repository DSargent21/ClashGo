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
	executor     *TapExecutor
	slotManager  *SlotManager
	pCfg         PrecisionConfig
	formula      *formula.Formula
	troopCounter *TroopCounter // optional; enables live per-slot re-OCR
	targetEdge   string
	w, h         int
	logger       zerolog.Logger

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
// positions via cmd/design_attack. troopCounter may also be nil; when
// non-nil, DeployTroops uses it to live-OCR the slot's per-card count
// at deploy time AND after the main tap pass — so balloons/EDs (and any
// "amount: All" troop) always reach a true empty state before being
// marked deployed.
func NewHeroManager(
	executor *TapExecutor,
	slotManager *SlotManager,
	pCfg PrecisionConfig,
	targetEdge string,
	w, h int,
	formula *formula.Formula,
	troopCounter *TroopCounter,
	logger zerolog.Logger,
) *HeroManager {
	return &HeroManager{
		executor:     executor,
		slotManager:  slotManager,
		pCfg:         pCfg,
		formula:      formula,
		troopCounter: troopCounter,
		targetEdge:   targetEdge,
		w:            w,
		h:            h,
		logger:       logger.With().Str("component", "hero_manager").Logger(),
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

		if hm.deploySingleHero(d, screen) {
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

		if hm.deploySingleHero(d, screen) {
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
//  0. Use the parent screen captured ONCE at attack start as the
//     pre-ratio source for every hero — eliminating per-hero
//     CaptureFresh() calls and the inter-hero ADB-screencap
//     deadlock. Sibling hero icons don't visually change when an
//     adjacent hero deploys, so the parent frame is accurate for
//     ALL pre-deploy measurements in this loop.
//  1. SELECT the hero slot.
//  2. Wait for selection animation (~350ms).
//  3. Drop with a tight cluster of 3 jittered taps (+/- 3 px).
//  4. Settle window so the slot transitions out of "highlighted".
//  5. Capture post-drop ratio; DELTA verify with threshold 0.15.
//     On failure, the sweeper retries.
func (hm *HeroManager) deploySingleHero(d HeroDeployment, preScreen gocv.Mat) bool {
	slot := d.Slot

	// 0. Pre-ratio from the attack-start parent screen (shared
	// across all heroes). Skips 1 CaptureFresh per hero; on a 4-hero
	// attack saves 4 ADB screencaps × ~75ms each = ~300ms of inter-
	// hero wait — and importantly removes the screencap deadlock
	// (the post-capture cost from hero N is no longer hidden behind
	// hero N+1's pre-boot).
	preRatio := 0.0
	capturedPre := !preScreen.Empty()
	if !capturedPre {
		// Defensive: if the orchestrator ever passes an uninitialized
		// (zero) gocv.Mat, fall back to a fresh capture so the
		// delta verify below still has a baseline.
		if fresh, err := hm.executor.CaptureFresh(); err == nil {
			defer fresh.Close()
			preRatio = GetSlotActivityRatioStatic(fresh, slot.X, slot.Y, hm.w)
			capturedPre = true
		}
	} else {
		preRatio = GetSlotActivityRatioStatic(preScreen, slot.X, slot.Y, hm.w)
	}

	// 1. SELECT the hero slot on the troop bar.
	hm.executor.TapSlot(slot, 8)
	hm.logger.Debug().
		Str("unit", d.Unit.Name).
		Int("slot_x", slot.X).
		Int("slot_y", slot.Y).
		Msg("selected hero slot")

	// 2. CoC selection-animation window.
	hm.executor.client.HumanSleep(150, 30)

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
	hm.executor.client.HumanSleep(150, 30)

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
	time.Sleep(100 * time.Millisecond)

	for _, slot := range deployedSlots {
		// Heroes always remain on bar (cooldown) - just tap ability
		hm.executor.TapHeroAbility(slot)
		hm.logger.Info().
			Str("unit", slot.UnitName).
			Int("x", slot.X).
			Msg("hero ability activated")
		time.Sleep(60 * time.Millisecond)
	}
}

// resolveHeroTarget returns the deployment point for a hero.
//
// Order of precedence:
//  1. Formula entry (the user pinned a specific point or line) — wins
//     whenever a point OR a line is present, so users with both
//     formula.json AND pCfg.HeroTargets keep the formula's per-unit
//     pins and do not silently get demoted to the corner pin.
//  2. Per-corner pin from precision_config.json (pCfg.HeroTargets) —
//     looked up first by exact targetEdge (TopLeft/TopRight/...), then
//     by the physical side via cornerToSide (so users who pin
//     hero_targets.top get the same point for both top corners).
//  3. Grand Warden drops in the screen center (Eternal Tome radius).
//  4. Random interpolation along the chosen edge (legacy fallback).
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

	// pCfg.HeroTargets (precision_config.json) - user pinned a per-corner
	// hero drop point. Without this wire-up, "Rotate" mode would still
	// randomize hero drops along the chosen edge instead of using the
	// user's per-corner pins. We check the active targetEdge first, then
	// fall back to the matching side (so a user who pins
	// hero_targets.top + hero_targets.bottom gets the same point for both
	// top corners and the same point for both bottom corners). Zero-
	// defaults (0,0) are treated as "not pinned" and skipped.
	if pt, ok := hm.pCfg.HeroTargets[hm.targetEdge]; ok && (pt.X != 0 || pt.Y != 0) {
		hm.logger.Debug().
			Str("unit", unitName).
			Str("corner", hm.targetEdge).
			Interface("target", pt).
			Msg("hero target from precision_config.json")
		return pt, pt
	}
	if side := cornerToSide(hm.targetEdge); side != "" {
		if pt, ok := hm.pCfg.HeroTargets[side]; ok && (pt.X != 0 || pt.Y != 0) {
			hm.logger.Debug().
				Str("unit", unitName).
				Str("side", side).
				Interface("target", pt).
				Msg("hero target from precision_config.json (side fallback)")
			return pt, pt
		}
	}

	// Grand Warden is the only hero that should drop in the SCREEN CENTER.
	// His Eternal Tome covers the entire funnel, so edge deployment wastes
	// the radius and risks him dying before the ability fires. Other heroes
	// (BK, AQ, Prince, Duke, Champion) keep going to the chosen edge.
	if strings.Contains(unitName, "warden") {
		pt := image.Pt(hm.w/2, hm.h/2)
		return pt, pt
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
//
// Root-cause fix for the "balloons/EDs sometimes don't all get placed"
// user-reported bug: the previous path fired `count` taps and either
// returned success (formula-driven) or did a single visual-empty check,
// then marked the slot SlotDeployed regardless of whether troop icons
// remained. When the cached detectedCount was wrong (template-OCR is
// brittle across themes / emulator sizes), troops were left behind
// without ever being re-counted.
//
// The new path:
//  1. Live-OCR the slot's per-card count BEFORE the main tap pass.
//     Live count wins over detectedCount/YAML when > 0. When OCR fails
//     AND the slot is visually empty, the deploy is a true no-op and
//     we mark deployed without firing taps.
//  2. Main pass fires exactly `count` taps on the formula/pinned or
//     legacy edge line.
//  3. Reconcile loop: up to reconcileRounds rounds, each one captures
//     a fresh screen, live-OCRs + visual-empty checks, and re-selects
//     + fires compensating taps if there's still count > 0. The slot
//     is only MarkDeployed after a (live OCR == 0 AND visual-empty)
//     confirmation — or after the reconcile budget runs out, in which
//     case we record SlotAttempted so the sweep phase retries with its
//     own reconcile loop.
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
		// FourSides pattern deploys on all 4 corners simultaneously
		// using pCfg.Edges. Apply the phase's offset to a local copy of
		// the edges so the outer (balloons) / inner (EDs) / spell
		// lines all sit at distinct distances from base center.
		// Without this, the offset parameter was silently ignored for
		// FourSides (only Line deploys applied it), so all 3 lines
		// landed at the same depth — breaking the "Double Diamond"
		// formation.
		cfg := hm.pCfg
		if offset > 0 {
			cfg.Edges = make(map[string]ManualEdge)
			cx, cy := hm.w/2, hm.h/2
			pct := float64(offset) / 200.0
			for k, e := range hm.pCfg.Edges {
				p1 := image.Pt(
					int(float64(e.P1.X)+float64(cx-e.P1.X)*pct),
					int(float64(e.P1.Y)+float64(cy-e.P1.Y)*pct),
				)
				p2 := image.Pt(
					int(float64(e.P2.X)+float64(cx-e.P2.X)*pct),
					int(float64(e.P2.Y)+float64(cy-e.P2.Y)*pct),
				)
				cfg.Edges[k] = ManualEdge{P1: p1, P2: p2}
			}
		}
		hm.executor.TapDeployFourSides(cfg, hm.targetEdge, 12, 8)
		// Mark the slot deployed so the sweeper doesn't re-tap. The
		// slot may visually persist (CC-troop icon for balloons, etc.)
		// which used to be a problem for the Line/Point path that
		// polled isSlotEmptyStatic, but FourSides taps 12× per corner
		// (48 total) so we trust the deploy and mark now to avoid 4
		// wasted deploySlot retries in the sweep phase.
		hm.slotManager.MarkDeployed(unitName)
		return true
	}

	// Resolve target line once so the reconcile loop can re-fire on
	// the SAME coordinates as the main pass. Formula check FIRST so
	// user-pinned coords win everywhere.
	var p1, p2 image.Point
	hasFormula := false
	if entry, ok := hm.formulaEntry(unit.Name); ok {
		switch {
		case entry.IsLine() && entry.P1 != nil && entry.P2 != nil:
			p1 = entry.P1.Image()
			p2 = entry.P2.Image()
			hasFormula = true
		case entry.IsPoint() && entry.P != nil:
			p := entry.P.Image()
			p1, p2 = p, p
			hasFormula = true
		default:
			// Formula entry has no usable coords — fall back to the
			// dynamic/pinned edge path below.
			hasFormula = false
		}
	}
	if !hasFormula {
		p1, p2 = hm.resolveTroopTarget(slot, offset)
	}

	// ─── Live-OCR pre-deploy ───────────────────────────────────────
	// Verify the slot hasn't already been emptied by a prior phase
	// (e.g. live-troop-bar transition). If visually empty we skip the
	// main pass and mark deployed — no wasted taps.
	preCount, preVisualEmpty := hm.liveCountAndEmpty(slot)
	if preVisualEmpty && preCount <= 0 {
		hm.logger.Info().
			Str("unit", unit.Name).
			Bool("formula_pinned", hasFormula).
			Msg("slot already empty at deploy start; marking deployed (no taps fired)")
		hm.slotManager.MarkDeployed(unitName)
		return true
	}

	tapCount := hm.resolveLiveTapCount(unit, slot, preCount, detectedCount)

	// ─── Main tap pass ─────────────────────────────────────────────
	var deployed func(int, int) // (count, jitterPx) -> no return
	if p1 == p2 {
		deployed = func(c, j int) { hm.executor.TapDeployPoint(p1, c, j) }
	} else {
		deployed = func(c, j int) { hm.executor.TapDeployLine(p1, p2, c, j) }
	}
	if hasFormula {
		hm.logger.Info().
			Str("unit", unit.Name).
			Str("src", "formula").
			Int("count", tapCount).
			Int("live_count", preCount).
			Int("detected_count", detectedCount).
			Interface("p1", p1).
			Interface("p2", p2).
			Msg("deploying troop (formula-driven live-count)")
	} else {
		hm.logger.Info().
			Str("unit", unit.Name).
			Str("src", "edge").
			Int("count", tapCount).
			Int("live_count", preCount).
			Int("detected_count", detectedCount).
			Msg("deploying troop (live-count-driven)")
	}
	deployed(tapCount, hm.lineJitter(hasFormula))

	// ─── Reconcile loop ────────────────────────────────────────────
	const reconcileRounds = 3
	const reconcileSettleMs = 150
	for round := 0; round < reconcileRounds; round++ {
		hm.executor.HumanSleep(reconcileSettleMs, 30)

		live, visualEmpty := hm.liveCountAndEmpty(slot)
		if live <= 0 && visualEmpty {
			hm.slotManager.MarkDeployed(unitName)
			hm.logger.Info().
				Str("unit", unit.Name).
				Int("round", round+1).
				Int("fired_total", tapCount).
				Msg("reconcile confirmed slot empty; deploy complete")
			return true
		}
		if live <= 0 {
			// Visual non-empty but OCR didn't see a count — likely a
			// ghost of a queued CC troop. Wait one more round and let
			// CoC settle into a stable state. We do NOT mark deployed.
			hm.logger.Warn().
				Str("unit", unit.Name).
				Int("round", round+1).
				Bool("visual_empty", visualEmpty).
				Msg("reconcile: visual-empty-but-OCR-zero; treating as transient ghost")
			continue
		}

		// Still troops on the bar. Re-select the slot (CoC deselects
		// after the natural fire-window expires) and fire live more
		// taps on the same line.
		hm.executor.TapSlot(slot, 4)
		hm.executor.HumanSleep(reconcileSettleMs, 30)
		deployed(live, hm.lineJitter(hasFormula))
		hm.logger.Info().
			Str("unit", unit.Name).
			Int("round", round+1).
			Int("remaining", live).
			Msg("reconcile: re-selected slot and fired top-up taps")
	}

	// Reconcile budget exhausted. We deliberately DO NOT mark deployed —
	// we leave the state as SlotAttempted so the sweep phase takes over
	// with its own reconcile loop. The slot will be picked up by the
	// next phase's verifier pass.
	hm.logger.Warn().
		Str("unit", unit.Name).
		Int("reconcile_rounds", reconcileRounds).
		Int("initial_count", tapCount).
		Msg("reconcile exhausted; leaving slot in SlotAttempted for sweep to retry")
	hm.slotManager.RecordAttempt(unitName, false)
	return false
}

// DeploySiege deploys a siege machine.
//
// Formula takes precedence: when the user authored a "point" or "line"
// entry for this siege (e.g. stone_slammer → {"type":"point","p":{...}}),
// the user-pinned geometry wins and the legacy pCfg.Edges path is
// skipped. Falls back to the dynamic red-zone edge line otherwise.
//
// Live-test fix: ON SUCCESS the slot is marked deployed via
// slot.UnitName (canonical key in unitIndex), NOT via unit.Name.
// Template matching may identify the slot under a slightly different
// spelling than the strategy YAML uses, so the
// strategy-derived `unit.Name` key often fails GetSlot() lookup and
// MarkDeployed becomes a silent no-op. That left the slot in a
// non-terminal state and the sweeper picked it up — 3 deploySlot
// retries × ~12 taps each = ~36 wasted taps per attack.
//
// Live-test fix #2: the legacy path no longer polls
// isSlotEmptyStatic after the drop. Siege machines in CoC NEVER
// transition the troop-bar slot back to a clean "empty" state on
// success — the slot visually persists as the next queued icon
// (often a CC-troop icon) or a skeleton silhouette until the next
// production cycle. Trusting the tap and marking deployed directly
// is the only safe path.
func (hm *HeroManager) DeploySiege(unit strategy.Unit, slot *TrackedSlot) bool {
	// Formula FIRST — pinned geometry wins.
	if entry, ok := hm.formulaEntry(unit.Name); ok {
		if hm.deploySiegeFromFormula(unit, slot, entry) {
			hm.slotManager.MarkDeployed(slot.UnitName)
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

	// Trust the drop + mark with the canonical slot key. The
	// isSlotEmptyStatic poll below was unreliable for sieges
	// (siege slot icon never returns to grass pixels) and is what
	// caused the "sweep retaps the deployed siege" bug.
	time.Sleep(80 * time.Millisecond)
	hm.slotManager.MarkDeployed(slot.UnitName)
	return true
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

// (deployFromFormula — REMOVED. DeployTroops now owns the formula-driven
// path inline, with the live-OCR reconcile loop. The previous standalone
// function returned true unconditionally after firing its taps and marked
// the slot SlotDeployed without verifying — the silent under-deployment
// bug for balloons/EDs. The SpellDeployer has its own private
// deployFromFormula for spell-specific deployment and is unrelated.)

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

// resolveFormulaCount is removed: DeployTroops uses resolveLiveTapCount
// (which threads live OCR + heuristic fallbacks) directly, including the
// "+1 when count >= 6" safety pad for OCR under-reads. The dedicated
// formula-only helper is no longer reachable.

// liveCountAndEmpty is a thin shim to the shared captureSlotLiveCount
// helper in live_count.go. Kept as a method to keep call-site code
// readable inside HeroManager.DeployTroops's reconcile loop.
func (hm *HeroManager) liveCountAndEmpty(slot *TrackedSlot) (int, bool) {
	return captureSlotLiveCount(
		hm.executor,
		hm.troopCounter,
		slot,
		hm.slotManager.GetBarY(),
		hm.w, hm.h,
	)
}

// resolveLiveTapCount chooses the canonical tap count for the main pass.
// Order of precedence:
//
//  1. Live OCR count from the pre-deploy screen (most authoritative —
//     captures whatever CoC currently shows). Padded by +1 when ≥ 6 to
//     absorb single-digit OCR under-reads.
//  2. detectedCount (the once-cached orchestrator-start OCR). Same +1
//     pad. Fallback when live OCR is unavailable (e.g. troopCounter
//     not threaded through).
//  3. YAML amount (parseAmount with the "All" → 0 → default path).
//  4. Safe heuristic default (8) — reconcile corrects under-firing.
func (hm *HeroManager) resolveLiveTapCount(unit strategy.Unit, slot *TrackedSlot, liveCount, detectedCount int) int {
	const padFloor = 6
	pad := func(n int) int {
		if n >= padFloor {
			return n + 1
		}
		if n > 0 {
			return n
		}
		return 0
	}
	if v := pad(liveCount); v > 0 {
		hm.logger.Debug().
			Str("unit", unit.Name).
			Int("live_count", liveCount).
			Int("padded", v).
			Msg("tap count from live OCR")
		return v
	}
	if v := pad(detectedCount); v > 0 {
		hm.logger.Debug().
			Str("unit", unit.Name).
			Int("detected_count", detectedCount).
			Int("padded", v).
			Msg("tap count from orchestrator-cached detected count")
		return v
	}
	if unit.Amount != "" && unit.Amount != "All" {
		if v := parseAmount(unit.Amount); v > 0 {
			hm.logger.Debug().
				Str("unit", unit.Name).
				Str("amount", unit.Amount).
				Int("count", v).
				Msg("tap count from YAML amount")
			return v
		}
	}
	// Safe default \u2014 the reconcile loop corrects under-firing. We pick
	// 8 (typical balloon/ED count) instead of the legacy 12 so the worst
	// case wastes fewer taps when OCR is completely broken AND the slot
	// genuinely holds a small army.
	hm.logger.Debug().
		Str("unit", unit.Name).
		Msg("tap count fallback: heuristic default 8 + reconcile")
	return 8
}

// lineJitter returns the per-tap jitter for the main/reconcile tap
// sequence. Formula-pinned units have authored jitter (default 3); the
// legacy edge path uses 10 for lines / 8 for points (matching the
// pre-refactor values).
func (hm *HeroManager) lineJitter(formulaPinned bool) int {
	if formulaPinned {
		return 3
	}
	return 10
}
