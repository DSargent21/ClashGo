package attack

import (
	"encoding/json"
	"fmt"
	"image"
	"math/rand"
	"os"
	"strings"
	"time"

	"github.com/Ducky705/ClashGO/internal/paths"
	"github.com/Ducky705/ClashGO/pkg/formula"
	"github.com/Ducky705/ClashGO/pkg/strategy"
	"gocv.io/x/gocv"
)

// DeployDynamicV2 deploys troops using dynamic red line detection.
// No hardcoded precision_config.json needed - detects deployment boundary live.
//
// strategyPath is the on-disk YAML path. The orchestrator uses it to find
// the matching formula.json (loaded as <stem>_formula.json next to the
// YAML). Pass "" to skip formula lookup entirely.
func (e *Executor) DeployDynamicV2(s *strategy.DynamicStrategy, screen gocv.Mat, strategyPath string) (int, error) {
	w, h := screen.Cols(), screen.Rows()
	targetEdge := s.TargetEdge

	// Pre-flight validation
	if err := e.Validate(s); err != nil {
		e.logger.Error().Err(err).Msg("pre-flight validation failed")
		return 0, err
	}

	// 0. Resolve "Random" targetEdge BEFORE we read configuration that
	//    depends on it. Previously this happened AFTER the red-zone pass,
	//    so heroes/sweep saw a different edge than troops — a silent
	//    coordinate-mismatch bug.
	if strings.EqualFold(targetEdge, "Random") {
		edges := []string{"TopLeft", "TopRight", "BottomLeft", "BottomRight"}
		targetEdge = edges[rand.Intn(len(edges))]
		e.logger.Info().Str("edge", targetEdge).Msg("random edge selected")
	}

	// 1. Detect red zone (deployment boundary)
	redDetector := NewRedLineDetector(e.logger)
	uiCutoff := int(float64(h) * 0.85) // above troop bar
	redZone := redDetector.Detect(screen, uiCutoff)

	// 2. Load precision config FIRST so we can detect user-pinned coords
	//    before computing the deploy line. "Pinned" = user-authored non-zero
	//    entries for the chosen target. If the user pinned something we
	//    respect it; otherwise the dynamic red-zone line takes over.
	var pCfg PrecisionConfig
	mBarY := int(float64(h) * 0.78)
	pData, err := os.ReadFile(paths.Resolve("precision_config.json"))
	if err == nil && json.Unmarshal(pData, &pCfg) == nil {
		scaleX, scaleY := float64(w)/float64(pCfg.Width), float64(h)/float64(pCfg.Height)
		for k, v := range pCfg.Edges {
			pCfg.Edges[k] = ManualEdge{
				P1: image.Pt(int(float64(v.P1.X)*scaleX), int(float64(v.P1.Y)*scaleY)),
				P2: image.Pt(int(float64(v.P2.X)*scaleX), int(float64(v.P2.Y)*scaleY)),
			}
		}
		for k, v := range pCfg.SpellEdgesA {
			pCfg.SpellEdgesA[k] = ManualEdge{
				P1: image.Pt(int(float64(v.P1.X)*scaleX), int(float64(v.P1.Y)*scaleY)),
				P2: image.Pt(int(float64(v.P2.X)*scaleX), int(float64(v.P2.Y)*scaleY)),
			}
		}
		for k, v := range pCfg.SpellEdgesB {
			pCfg.SpellEdgesB[k] = ManualEdge{
				P1: image.Pt(int(float64(v.P1.X)*scaleX), int(float64(v.P1.Y)*scaleY)),
				P2: image.Pt(int(float64(v.P2.X)*scaleX), int(float64(v.P2.Y)*scaleY)),
			}
		}
		for k, v := range pCfg.HeroTargets {
			pCfg.HeroTargets[k] = image.Pt(int(float64(v.X)*scaleX), int(float64(v.Y)*scaleY))
		}
		for k, v := range pCfg.SpellTargets {
			pCfg.SpellTargets[k] = image.Pt(int(float64(v.X)*scaleX), int(float64(v.Y)*scaleY))
		}
		if pCfg.Sides != nil {
			for k, v := range pCfg.Sides {
				pCfg.Sides[k] = ManualEdge{
					P1: image.Pt(int(float64(v.P1.X)*scaleX), int(float64(v.P1.Y)*scaleY)),
					P2: image.Pt(int(float64(v.P2.X)*scaleX), int(float64(v.P2.Y)*scaleY)),
				}
			}
		}
		mBarY = int(float64(pCfg.BarY) * scaleY)
		if mBarY > int(float64(h)*0.92) {
			mBarY = int(float64(h) * 0.92)
		}
	}

	// 2a. Detect user-pinned coords for the SPECIFIC chosen target.
	// A pin "exists" when targetEdge has non-zero coords in Edges, or a
	// matching side has non-zero coords in Sides. We deliberately avoid
	// falling back to phantom (0,0)→(0,0) entries (Go zero-default for
	// missing JSON keys) which would otherwise look "pinned".
	userPinnedForTarget := hasPinnedForTarget(pCfg, targetEdge)

	// 2b. If we have neither a red zone nor a pin, don't guess. The original
	// fall-through path deployed at x=60 regardless of base orientation —
	// that's how the legacy "scatters across the corner" symptom started.
	if !redZone.Valid && !userPinnedForTarget {
		return 0, fmt.Errorf("no red zone detected AND no user-pinned sides/edges in precision_config.json for target=%q — re-pin via `cmd/pick_coords -mode=strict` or capture a battle shot", targetEdge)
	}

	// 2c. If user pinned the chosen target, build the deploy line directly
	// from those pinned coords using a real linspace. We bypass textual
	// side mapping (BottomLeft → "bottom" / "left") to preserve the path
	// the user actually drew. Side is set to targetEdge as a stable identifier
	// for downstream consumers (Sweep, SpellLine).
	var pinnedLine DeployLine
	if userPinnedForTarget {
		if e2, ok := pCfg.Edges[targetEdge]; ok && !isZeroManualEdge(e2) {
			pinnedLine = manualEdgeToDeployLine(e2, targetEdge, linePoints)
		} else if side := cornerToSide(targetEdge); side != "" {
			if e2, ok := pCfg.Sides[side]; ok && !isZeroManualEdge(e2) {
				pinnedLine = manualEdgeToDeployLine(e2, targetEdge, linePoints)
			}
		}
		e.logger.Info().
			Str("target", targetEdge).
			Bool("red_zone_valid", redZone.Valid).
			Int("pinned_points", len(pinnedLine.Points)).
			Msg("using user-pinned deploy line; skipping dynamic override")
	}

	// 0a. Load formula.json (if present). When the user authored one via
	//     `cmd/design_attack`, it overrides the dynamic red-zone / corner-
	//     based deploy path so every unit drops on the chosen SIDE with
	//     coordinates the user actually pinned, not the legacy corner
	//     heuristics that kept attacking in the corner.
	//
	// strategyPath is the *YAML* path (passed by bot.go / debug_attack);
	// candidatePaths inside the formula loader computes the parallel
	// "<stem>_formula.json" location, which is the same directory the
	// user creates formula files in via cmd/design_attack.
	formulaPtr, hasFormula, ferr := formula.Load(strategyPath)
	if ferr != nil {
		e.logger.Warn().Err(ferr).Str("strategy_path", strategyPath).Msg("formula found but failed to parse")
	}
	if hasFormula {
		// Scale the formula's authored coordinates to the live screen
		// dimensions. Without this, a JSON pinned on a 860×732 PNG
		// won't land correctly on a different live resolution.
		formulaPtr.ApplyScreenScale(formulaPtr.Screen.W, formulaPtr.Screen.H, w, h)
		e.logger.Info().
			Str("strategy", s.Name).
			Int("units", len(formulaPtr.Units)).
			Int("formula_w", formulaPtr.Screen.W).
			Int("formula_h", formulaPtr.Screen.H).
			Int("screen_w", w).
			Int("screen_h", h).
			Msg("formula.json loaded; per-unit explicit coordinates will override edge-based deploy")
	}

	// 3. Calculate dynamic deployment line. We always compute it for the
	//    use-case where target is NOT user-pinned (the live red-zone path)
	//    OR for when red zone is valid even if pinned (so the dynamic line
	//    can serve as the deployLine passed to Sweep / SpellLine).
	deployCalc := NewDeployLineCalculator(e.logger)
	deployLine := deployCalc.Calculate(redZone, w, h, uiCutoff, cornerToSide(targetEdge), linePoints)

	// 3a. Apply corner override. When the user did NOT pin the target,
	//    all four corners get the dynamic red-zone line (original Duke-
	//    coherence fix preserved). When the user DID pin the target,
	//    ONLY the unpinned adjacent corners get overridden — so Duke's
	//    random adjacent-corner pick still lands on the actual chosen
	//    side, while the user's pinned target stays intact.
	//
	//    Sides (top/right/bottom/left) gets mirrored in both cases so
	//    SpotsForSide / future side-aware readers see the dynamic line.
	applyCornerOverride(&pCfg, deployLine, redZone.Valid, targetEdge)

	// 3b. Once the override is decided, the active deployLine is what
	//     Sweep and SpellLine read. If the user pinned (and forces — or
	//     chose — a real line), use it as the active deployLine so
	//     every consumer (troops, spells, sweep) hits the pin; else the
	//     dynamic line stands. This eliminates the "troops obey pin but
	//     spells/sweep scatter off-pin" partial-inconsistency bug.
	if userPinnedForTarget && len(pinnedLine.Points) >= 2 {
		deployLine = pinnedLine
	}

	// 4. Initialize SlotManager
	slotMgr := NewSlotManager(screen, pCfg, w, h, mBarY, e.templates, e.classify, e.logger)
	if len(slotMgr.GetAllSlots()) == 0 {
		return 0, fmt.Errorf("no active slots detected")
	}

	// 5. Detect troop counts
	troopCounter := NewTroopCounter(pCfg.Width, pCfg.Height, e.logger)
	troopCounts := troopCounter.DetectCounts(screen, slotMgr.GetAllSlots(), mBarY)
	countMap := GetAllCounts(troopCounts)
	e.logger.Info().Interface("counts", countMap).Msg("detected troop counts")

	// 6. Initialize tap executor
	tapExec := NewTapExecutor(e.client, e.cal, e.logger)

	// 7. Plan phases
	planner := NewDeployPlanner(slotMgr, pCfg, targetEdge, w, h, e.logger)
	plans := planner.PlanDeployment(s)

	// 8. Collect strategy unit names
	strategyNames := GetStrategyUnitNames(s)

	// 9. Execute each phase
	for _, plan := range plans {
		e.logger.Info().Str("phase", plan.Phase.Name).Msg("attack phase")
		if e.OnPhaseStart != nil {
			e.OnPhaseStart(plan.Phase.Name, targetEdge)
		}

		// Deploy spells
		spellDeployer := NewSpellDeployer(tapExec, pCfg, formulaPtr, w, h, e.logger)
		for _, up := range ResolveSpellTargets(plan) {
			if up.Slot == nil {
				continue
			}
			e.logger.Info().
				Str("unit", up.Unit.Name).
				Int("x", up.Slot.X).
				Msg("deploying spell")

			if e.OnUnitDeploy != nil {
				e.OnUnitDeploy(up.Unit.Name, up.Slot.X, slotMgr.GetSlotY())
			}

			tapExec.TapSlot(up.Slot, 8)
			tapExec.HumanSleep(35, 10)

			success := spellDeployer.DeploySpell(up.Unit, up.Slot, targetEdge, plan.Phase.Pattern)
			if success {
				slotMgr.MarkDeployed(strings.ToLower(up.Unit.Name))
			}
		}

		// Deploy troops
		heroMgr := NewHeroManager(tapExec, slotMgr, pCfg, targetEdge, w, h, formulaPtr, e.logger)
	// Bridge: when HeroManager's resolveHeroTarget fires for the
	// Dragon Duke, route the event through Executor.OnDukePick so a
	// single observer (live bot's NDJSON writer, debug_test's
	// recorder) sees BOTH the legacy adjacent-corner random pick and
	// the new "follow the chosen edge" behavior. chosen == target in
	// the new path — Duke falls through to the chosen edge with a
	// random point along it.
	heroMgr.OnDukeDeployed = func(target string) {
		if e.OnDukePick != nil {
			e.OnDukePick(target, target)
		}
	}
		for _, up := range ResolveTroopTargets(plan) {
			if up.Slot == nil {
				continue
			}
			e.logger.Info().
				Str("unit", up.Unit.Name).
				Int("x", up.Slot.X).
				Msg("deploying troop")

			if e.OnUnitDeploy != nil {
				e.OnUnitDeploy(up.Unit.Name, up.Slot.X, slotMgr.GetSlotY())
			}

			tapExec.TapSlot(up.Slot, 8)
			tapExec.HumanSleep(35, 10)

			detectedCount := GetCountForSlot(troopCounts, up.Slot.X)
			heroMgr.DeployTroops(up.Unit, up.Slot, plan.Phase.Pattern, plan.Phase.Offset, plan.Phase.Pattern, screen, detectedCount)
		}

		// Deploy siege
		for _, up := range ResolveSiegeTargets(plan) {
			if up.Slot == nil {
				continue
			}
			e.logger.Info().
				Str("unit", up.Unit.Name).
				Int("x", up.Slot.X).
				Msg("deploying siege")

			if e.OnUnitDeploy != nil {
				e.OnUnitDeploy(up.Unit.Name, up.Slot.X, slotMgr.GetSlotY())
			}

			tapExec.TapSlot(up.Slot, 8)
			tapExec.HumanSleep(35, 10)

			heroMgr.DeploySiege(up.Unit, up.Slot)
		}

		// Deploy heroes
		if strings.Contains(plan.Phase.Name, "Heroes") {
			heroUnits := make([]strategy.Unit, 0)
			for _, up := range ResolveHeroTargets(plan) {
				heroUnits = append(heroUnits, up.Unit)
			}
			if len(heroUnits) > 0 {
				heroMgr.DeployHeroes(heroUnits, screen)
			}
		}

		// Phase delay defaults tightened: Heroes/Siege used to sit at
		// 500ms post-phase, which compounded with each hero's 800ms settle
		// inside hero_manager.go to produce a >1.5s wall-clock gap between
		// heroes (visibly bot-paced).
		//
		// USER YAML WINS. The previous override unconditionally clamped
		// to 100ms regardless of the strategy's `delay_after_ms`, which
		// made the field useless for Heroes/Siege. New rule: only fall
		// back when the YAML value is unset (0); any explicit YAML value
		// (including small ones like 50ms) is preserved.
		//
		// Overall cap is maxPhaseDelay. Anything above gets WARN-logged
		// so authors can spot unintentional bloat. The cap is high
		// enough for spells (which legitimately need a long settle for
		// the multi-tap flow to register).
		const heroSiegeDefault = 30 * time.Millisecond
		const maxPhaseDelay = 200 * time.Millisecond
		pDelay := time.Duration(plan.Phase.DelayAfterMS) * time.Millisecond
		isHeroOrSiege := strings.Contains(plan.Phase.Name, "Heroes") || strings.Contains(plan.Phase.Name, "Siege")
		if isHeroOrSiege && pDelay <= 0 {
			pDelay = heroSiegeDefault
		}
		if pDelay > maxPhaseDelay {
			e.logger.Warn().
				Str("phase", plan.Phase.Name).
				Dur("requested", pDelay).
				Dur("clamped_to", maxPhaseDelay).
				Msg("phase delay_after_ms exceeds cap; clamping to keep attack human-paced")
			pDelay = maxPhaseDelay
		}
		if pDelay > 0 {
			time.Sleep(pDelay)
		}
	}

	// 10. Sweep remaining. Pass formulaPtr so the sweep path honors
	// user-pinned _event_troop / _event_spell coords the same way
	// DeployHeroes / DeployTroops already do. Without this the bot
	// silently dropped event troops on the dynamically-detected
	// red-zone line, ignoring the user's pin entirely.
	sweeper := NewSweeper(tapExec, slotMgr, pCfg, deployLine, w, h, formulaPtr, e.logger)
	sweeper.Sweep(strategyNames, countMap)

	// 11. Verify
	verifier := NewVerifier(tapExec, slotMgr, pCfg, targetEdge, w, h, DefaultVerifyConfig(), e.logger)
	remainingCount := verifier.VerifyAll()

	return remainingCount, nil
}

// ---- pinned-line helpers --------------------------------------------------
//
// These helpers were added together with the Path A orchestrator fix that
// respects user-pinned coords in precision_config.json. The bug being
// addressed: the orchestrator's "override all 4 corners" block in
// DeployDynamicV2 was clobbering every corner with the dynamic red-zone
// line, so users who pinned a corner in cmd/pick_coords saw all their
// lines vanish at runtime. These helpers detect a "real" pin and route a
// linspace DeployLine through it.

// hasPinnedForTarget returns true when the user authored a non-zero edge
// for `targetEdge`, OR a matching side has non-zero coords in Sides.
// We deliberately exclude Go's (0,0)→(0,0) zero-default (the result of an
// unmarshaled missing JSON key) so an empty config doesn't look "pinned".
func hasPinnedForTarget(pCfg PrecisionConfig, targetEdge string) bool {
	if pCfg.Edges != nil {
		if e, ok := pCfg.Edges[targetEdge]; ok && !isZeroManualEdge(e) {
			return true
		}
	}
	if side := cornerToSide(targetEdge); side != "" && pCfg.Sides != nil {
		if s, ok := pCfg.Sides[side]; ok && !isZeroManualEdge(s) {
			return true
		}
	}
	return false
}

// isZeroManualEdge is true for the (0,0)→(0,0) zero-default Go produces
// when a JSON key is absent. Such entries are NOT considered user-pinned.
func isZeroManualEdge(e ManualEdge) bool {
	return e.P1.X == 0 && e.P1.Y == 0 && e.P2.X == 0 && e.P2.Y == 0
}

// cornerToSide maps the four legacy corner keys to a physical side name.
// Returns "" for inputs the orchestrator doesn't know how to classify.
// This is used both for matching pinned Sides entries and for selecting
// which physical side the DeployLineCalculator should prefer.
func cornerToSide(targetEdge string) string {
	switch strings.ToLower(targetEdge) {
	case "topleft", "topright":
		return "top"
	case "bottomleft", "bottomright":
		return "bottom"
	case "left":
		return "left"
	case "right":
		return "right"
	default:
		return ""
	}
}

// manualEdgeToDeployLine produces a DeployLine by direct linspace between
// P1 and P2 of the given manual edge. We bypass textual side mapping so
// a diagonal pinned line is preserved verbatim — that's the contract
// the new orchestrator override-skip path relies on. Side is set to
// `targetEdge` so downstream consumers (Sweep, SpellLine) see a stable
// identifier instead of an inferred compass direction.
func manualEdgeToDeployLine(edge ManualEdge, targetEdge string, n int) DeployLine {
	if n < 2 {
		n = 15
	}
	pts := make([]image.Point, n)
	for i := 0; i < n; i++ {
		t := float64(i) / float64(n - 1)
		pts[i] = image.Pt(
			edge.P1.X+int(t*float64(edge.P2.X-edge.P1.X)),
			edge.P1.Y+int(t*float64(edge.P2.Y-edge.P1.Y)),
		)
	}
	return DeployLine{
		Points:  pts,
		Side:    targetEdge,
		Anchor:  pts[len(pts)/2],
		Outside: true,
	}
}

// applyCornerOverride is the orchestrator corner-mirror step. It exists
// for two reasons:
//
//  1. Duke-coherence: HeroManager.resolveHeroTarget picks the Dragon
//     Duke's deploy line from ONE OF THE LEGACY CORNER KEYS
//     (`pCfg.Edges[adjacentCorner]`). Without mirroring, an attacker
//     that pinned BottomLeft but not TopLeft/BottomRight would let Duke
//     scatter to whatever default was at those unpinned corners —
//     chasing the formula drift symptom this whole change set is
//     fighting.
//
//  2. Sides feed SpotsForSide: subsequent strict-side consumers read
//     from pCfg.Sides, so we keep that map populated.
//
// When the user explicitly pinned the chosen target, the override ONLY
// touches UNPINNED corners and writes the SAME PINNED LINE into them
// — Duke's adjacent-corner random pick then drops on the user's
// pin, eliminating the cross-side scatter. The pinned target itself
// is left untouched.
//
// When the user did NOT pin the target, the legacy "clobber all 4
// corners with the dynamic red-zone line" path is preserved so an
// unpinned attack still has a sensible fallback.
func applyCornerOverride(pCfg *PrecisionConfig, deployLine DeployLine, redZoneValid bool, targetEdge string) {
	if !redZoneValid || len(deployLine.Points) < 2 {
		return
	}
	if pCfg.Edges == nil {
		pCfg.Edges = make(map[string]ManualEdge)
	}
	if pCfg.Sides == nil {
		pCfg.Sides = make(map[string]ManualEdge)
	}

	// Resolve the user's pin (if any) for the chosen target. Used as
	// the override source so Duke's adjacent-corner pick lands on the
	// user's pinned line, not the dynamic red-zone default.
	var pinnedOverride ManualEdge
	hasPinned := false
	if e, ok := pCfg.Edges[targetEdge]; ok && !isZeroManualEdge(e) {
		pinnedOverride = e
		hasPinned = true
	} else if side := cornerToSide(targetEdge); side != "" {
		if s, ok := pCfg.Sides[side]; ok && !isZeroManualEdge(s) {
			pinnedOverride = s
			hasPinned = true
		}
	}

	targets := []string{"TopLeft", "TopRight", "BottomLeft", "BottomRight"}
	sideNames := []string{"top", "right", "bottom", "left"}

	if hasPinned {
		// Pinned target path: leave target alone, mirror the user's pin
		// into the 3 unpinned corners + all 4 side names. This is the
		// fix for "attacks in corner on 2 sides" — Duke's random pick
		// can no longer scatter to garbage default coords.
		for _, c := range targets {
			if c == targetEdge {
				continue
			}
			pCfg.Edges[c] = pinnedOverride
		}
		for _, s := range sideNames {
			pCfg.Sides[s] = pinnedOverride
		}
		return
	}

	// Unpinned path: clobber all 4 corners + sides with the dynamic
	// red-zone deploy line. Preserves the original behavior so a
	// fresh-config run still finds SOMETHING to attack on.
	lineStart := deployLine.Points[0]
	lineEnd := deployLine.Points[len(deployLine.Points)-1]
	dyn := ManualEdge{P1: lineStart, P2: lineEnd}
	for _, c := range targets {
		pCfg.Edges[c] = dyn
	}
	for _, s := range sideNames {
		pCfg.Sides[s] = dyn
	}
}
