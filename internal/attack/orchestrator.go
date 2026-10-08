package attack

import (
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"math/rand"
	"path/filepath"
	"strings"
	"time"

	"github.com/Ducky705/ClashGO/internal/game"
	"github.com/Ducky705/ClashGO/internal/paths"
	"github.com/Ducky705/ClashGO/pkg/formula"
	"github.com/Ducky705/ClashGO/pkg/strategy"
	"gocv.io/x/gocv"
)

// saveTroopBarFrame writes the full frame the count reader used to path.
// It exists because a count read that fails live leaves nothing behind to
// debug: the bar animates, the battle starts, and the frame that decided the
// deploy is gone. The whole frame is kept, not a crop, so the saved pixels can
// be replayed through the reader itself (DetectCounts on the file) and through
// `see look -img`, which is the only way a live miss becomes a reproducible one.
func saveTroopBarFrame(frame gocv.Mat, path string) bool {
	if frame.Empty() || path == "" {
		return false
	}
	return gocv.IMWrite(path, frame)
}

const (
	// troopCountExtraFrames is how many extra captures the troop-bar read takes
	// beyond the frame the caller already holds. It is the whole point of
	// DetectCountsMerged: a card mid-animation carries no readable label, and a
	// second look a beat later does.
	troopCountExtraFrames = 2

	// troopCountFrameSettle is the pause between those captures. It has to be
	// long enough for the bar's fade-in to move (the frames must differ) and
	// short enough that it never delays the first drop by anything a player would
	// notice: three frames land inside half a second.
	troopCountFrameSettle = 220 * time.Millisecond
)

// spellReconcileEntry is one fired spell unit awaiting the deferred post-deploy
// reconcile (see DeployDynamicV2: the phase loop collects, the recovery phase
// runs them after the placements window closes).
type spellReconcileEntry struct {
	unit    strategy.Unit
	slot    *TrackedSlot
	pattern string
	edge    string
}

// armyReadinessGap reports strategy troops/spells whose bar card has positive
// evidence of being empty at deploy start: a MERGE-CONFIRMED zero count (an
// unreadable card is absent from the counts map, never zero). Heroes, siege
// machines and units with no slot on the bar are excluded — their cards do not
// carry a trustworthy count badge, so silence there is not evidence.
func armyReadinessGap(plans []PhasePlan, counts map[int]int, sm *SlotManager) []string {
	if sm == nil {
		return nil
	}
	var gap []string
	seen := make(map[string]bool)
	for _, plan := range plans {
		for _, up := range plan.UnitPlans {
			if up.Slot == nil || seen[up.Unit.Name] {
				continue
			}
			if up.Slot.Category != "Troop" && up.Slot.Category != "Spell" {
				continue
			}
			if n, ok := counts[up.Slot.X]; ok && n <= 0 {
				seen[up.Unit.Name] = true
				gap = append(gap, up.Unit.Name)
			}
		}
	}
	return gap
}

// DeployDynamicV2 deploys troops using dynamic red line detection.
// No hardcoded precision_config.json needed - detects deployment boundary live.
//
// strategyPath is the on-disk YAML path. The orchestrator uses it to find
// the matching formula.json (loaded as <stem>_formula.json next to the
// YAML). Pass "" to skip formula lookup entirely.
func (e *Executor) DeployDynamicV2(s *strategy.DynamicStrategy, screen gocv.Mat, strategyPath string) (int, error) {
	// User Stop observed before any capture or tap.
	if err := e.runtimeContext().Err(); err != nil {
		return 0, err
	}
	w, h := screen.Cols(), screen.Rows()
	targetEdge := s.TargetEdge

	// Record the active strategy so the battle-end wait can honor
	// per-strategy knobs (e.g. end_at_percent auto-end threshold).
	e.activeStrategy = s
	// A new battle owns a new HP watch; heroes are re-tracked as they deploy.
	e.clearHeroWatch()

	// Pre-flight validation
	if err := e.Validate(s); err != nil {
		e.logger.Error().Err(err).Msg("pre-flight validation failed")
		return 0, err
	}

	// 0. Resolve "Rotate" / "Random" targetEdge BEFORE we read configuration
	//    that depends on it. Previously this happened AFTER the red-zone
	//    pass, so heroes/sweep saw a different edge than troops — a silent
	//    coordinate-mismatch bug.
	switch {
	case strings.EqualFold(targetEdge, "Rotate"):
		// EqualFold is intentional: YAML authors may type "rotate" or
		// "ROTATE" or "Rotate" and the bot should not silently fall
		// through to the per-corner default. Parity with the "Random"
		// branch below, which uses the same case-insensitive match.
		//
		// Cycle through the 4 corners in a top→right→bottom→left
		// pattern via a persistent on-disk counter. The bot distributes
		// attacks evenly across sides over multiple runs instead of
		// re-picking TopLeft every process restart. See
		// rotation_state.go for the failure-mode / concurrency story.
		targetEdge = NextEdgeIndex()
		e.logger.Info().Str("edge", targetEdge).Msg("rotated to next edge")
	case strings.EqualFold(targetEdge, "Random"):
		edges := []string{"TopLeft", "TopRight", "BottomLeft", "BottomRight"}
		targetEdge = edges[rand.Intn(len(edges))]
		e.logger.Info().Str("edge", targetEdge).Msg("random edge selected")
	}

	// 1. Detect red zone (deployment boundary)
	redDetector := NewRedLineDetector(e.logger)
	uiCutoff := UICutoff(h) // the troop bar starts here (see deploy_line.go)
	redZone := redDetector.Detect(screen, uiCutoff)

	// 2. Load precision config FIRST so we can detect user-pinned coords
	//    before computing the deploy line. "Pinned" = user-authored non-zero
	//    entries for the chosen target. If the user pinned something we
	//    respect it; otherwise the dynamic red-zone line takes over.
	var pCfg PrecisionConfig
	mBarY := int(float64(h) * 0.78)
	pData, ok := readConfigJSON("precision_config.json")
	if ok && json.Unmarshal(pData, &pCfg) == nil {
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
		// Backstop for pinned points that sit under the game's chrome. The top
		// resource HUD and the troop bar are not field: a tap there is eaten by
		// the UI, so the unit is silently lost. `make pick-coords` warns about
		// these, but a second can be enough to lose a tap to a stray pixel, and
		// losing the whole deploy phase to one is never the intent. Clamping only
		// moves the point back onto the field - the direction of the line, and
		// the user's choice of side, are preserved.
		pinsAdjusted := 0
		fixEdge := func(m map[string]ManualEdge) {
			for k, v := range m {
				c := ClampEdgeToDeployBand(v, uiCutoff)
				if c.P1 != v.P1 {
					pinsAdjusted++
				}
				if c.P2 != v.P2 {
					pinsAdjusted++
				}
				m[k] = c
			}
		}
		fixEdge(pCfg.Edges)
		fixEdge(pCfg.SpellEdgesA)
		fixEdge(pCfg.SpellEdgesB)
		if pCfg.Sides != nil {
			fixEdge(pCfg.Sides)
		}
		for k, v := range pCfg.HeroTargets {
			c := ClampToDeployBand(v, uiCutoff)
			if c != v {
				pinsAdjusted++
			}
			pCfg.HeroTargets[k] = c
		}
		for k, v := range pCfg.SpellTargets {
			c := ClampToDeployBand(v, uiCutoff)
			if c != v {
				pinsAdjusted++
			}
			pCfg.SpellTargets[k] = c
		}
		if pinsAdjusted > 0 {
			e.logger.Warn().
				Int("adjusted", pinsAdjusted).
				Int("band_top", BandTop()).
				Int("band_bottom", uiCutoff).
				Msg("pinned points sat under the game's HUD or troop bar; clamped onto the field so those taps are not thrown away")
		}

		mBarY = int(float64(pCfg.BarY) * scaleY)
		if mBarY > int(float64(h)*0.92) {
			mBarY = int(float64(h) * 0.92)
		}
		// Every coordinate above is now in LIVE frame pixels, so the config must
		// stop advertising the reference geometry it was authored at. The
		// consumers (HeroManager.resolveTroopTarget, Verifier.retryDeploy, the
		// executor's deploy path) each re-run ScaleEdge on these same maps, and
		// ScaleEdge is `curW/refW` — so with Width/Height still 860/732 the
		// already-live pin is scaled a SECOND time. Live 2026-09-30: the TopRight
		// pin came back as (1213,128)->(1616,246) on a 1280px frame, i.e. x past
		// the right edge, and the game refused every such tap with "You cannot
		// deploy troops on the Red area!" while the whole army stayed in the bar.
		// Claiming the live size makes those ScaleEdge calls the identity they
		// were always meant to be, and leaves a config that describes the frame
		// it is actually expressed in.
		pCfg.Width, pCfg.Height = w, h
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
		// Resolve this corner's units (override, else mirror). Kept in one
		// function because anything that predicts what the bot will tap has to
		// apply the same precedence — the deploy-geometry gate in
		// deploy_geometry_test.go uses it for exactly that reason, and the
		// first version of that gate mirrored every corner, which described a
		// path this bot never takes: the shipped formula carries authored
		// overrides for all four corners.
		if applyCornerUnits(formulaPtr, targetEdge) {
			e.logger.Info().
				Str("edge", targetEdge).
				Int("units", len(formulaPtr.Units)).
				Msg("formula corner override applied (no mirror)")
		}
		// Project (the merged or mirrored units) onto the live screen.
		// Doing mirror-before-projection keeps formula.Screen.W/H
		// meaningful as the formula's intent reference and avoids
		// "which center do we reflect around" ambiguity.
		//
		// The projection is the game's own world law: one uniform display
		// scale k about the viewport centre, NOT a per-axis stretch to fill
		// the framebuffer. At 1280x720 the framebuffer ratio is 1.4884
		// horizontally against 0.9836 vertically (-34% aspect drift), and
		// applying it put every authored deploy point 19-76 px — one to two
		// village tiles — off its building. k comes from the calibration, so
		// the deploy geometry and the classifier share one measurement.
		k := e.cal.DisplayScale()
		formulaPtr.ProjectUniform(k, formulaPtr.Screen.W, formulaPtr.Screen.H, w, h)
		law := "uniform display scale about the viewport centre"
		switch {
		case formulaPtr.Screen.W == w && formulaPtr.Screen.H == h:
			law = "none (the formula carries the live screen size)"
		case formulaPtr.Screen.W != game.RefWidth || formulaPtr.Screen.H != game.RefHeight:
			// The reference-geometry projection is only meaningful from the
			// reference frame: a formula authored on some other device's
			// geometry carries no record of that device's scale, so the
			// result is a guess and must not pass silently.
			law = "uniform display scale about the viewport centre (GUESS: the formula was authored at neither the reference nor the live geometry)"
			e.logger.Warn().
				Int("authored_w", formulaPtr.Screen.W).
				Int("authored_h", formulaPtr.Screen.H).
				Int("ref_w", game.RefWidth).
				Int("ref_h", game.RefHeight).
				Msg("formula was not authored at the reference geometry or the live one; re-author it, the projection cannot be trusted")
		}
		// A correctly projected reference point can still land outside the band
		// the deploy path is willing to tap, at BOTH edges, because the world
		// scales uniformly about the centre while the HUD is edge-anchored:
		// at 1280x720 an authored y of 581 projects to 645 on a 720px screen
		// (past the troop bar, so the tap hits the bar and the unit never
		// deploys), and an authored y of 176 projects to 108 (behind the top
		// resource HUD). Measured on the shipped auto_edrag_rush formula: three
		// of its units project outside the band at this geometry, two below and
		// one above — the "it didn't use all the troops" report.
		//
		// Clamp into the band the rest of this function already uses — the same
		// [yTopMin, uiCutoff] range DeployLineCalculator refuses to place line
		// points outside — and say so, because the durable fix is re-authoring
		// the point, not moving the tap.
		if clamped := formulaPtr.ClampY(yTopMin, uiCutoff); clamped > 0 {
			e.logger.Warn().
				Int("points_clamped", clamped).
				Int("band_top", yTopMin).
				Int("band_bottom", uiCutoff).
				Msg("formula points projected outside the deployable band (behind the top HUD or under the troop bar) and were clamped into it; re-author them at the target geometry")
		}

		// …and push out of the red line. The band clamp answers a frame-edge
		// question; the red line is a shape in the middle of the frame, and a
		// tap inside it is refused by the game ("You cannot deploy troops on
		// the Red area!") while the bot's tap call still reports success. A
		// live 720p run with a pinned bottom-left line showed that banner for
		// the whole deploy phase: every tap landed, none deployed.
		if redZone.Valid && len(redZone.Polygon) >= 3 {
			cx, cy := redZone.Centre()
			// One village tile per step. CoC's village is ~44 tiles across the
			// reference width, so a tile is ~19.5 reference px; the point walks
			// outward in whole tiles (up to 24 of them) until it clears the red
			// line, which keeps the moved tap on the same axis the user aimed
			// along instead of nudging it a few pixels inside the same zone.
			step := int(e.cal.Length(19.5))
			if moved := formulaPtr.PushOutOfZone(redZone.Inside, cx, cy, step, 24); moved > 0 {
				e.logger.Warn().
					Int("points_moved", moved).
					Int("zone_centre_x", cx).
					Int("zone_centre_y", cy).
					Int("step_px", step).
					Msg("formula points projected INSIDE the red deployment line (the game refuses taps there); pushed radially outward to the nearest free step — re-author them outside the red line at the target geometry")
			}
		} else if !redZone.Valid {
			e.logger.Warn().Msg("no plausible red deployment boundary in this frame; formula points are used as authored (the red-line check is skipped rather than run against terrain)")
		}

		// The push walks radially in whole tiles and knows nothing about the
		// deployable band, so it can leave a point above the top resource HUD or
		// under the troop bar — a tap that hits chrome, which is exactly what the
		// band clamp above exists to prevent. Re-clamp and say so: a point that
		// had to be pulled back means the red-line push and the band disagree,
		// and the reader needs to know the tap is not where the push put it.
		if reclamped := formulaPtr.ClampY(yTopMin, uiCutoff); reclamped > 0 {
			e.logger.Warn().
				Int("points_reclamped", reclamped).
				Int("band_top", yTopMin).
				Int("band_bottom", uiCutoff).
				Msg("red-line push moved formula points out of the deployable band (behind the top HUD or under the troop bar); pulled them back into it")
		}

		// Deliberately NOT translated into a guessed "safe" box when the red
		// boundary is unknown. That guess (SafeGroundRect, a 20% inset of the
		// frame) is not where the map's no-deploy ring is — the ring belongs to
		// the map, not to the screen — and it silently overrode authored geometry.
		// Live 2026-10-01: a user-drawn TopLeft line (187,317)->(506,71) was moved
		// to (256,413)->(575,226) before any tap, so the attack ran on ground
		// nobody chose. The band clamp above is kept; it only pulls a point out
		// from under the HUD. Which side is deployable is the picker's call.
		e.logger.Info().
			Str("strategy", s.Name).
			Int("units", len(formulaPtr.Units)).
			Int("formula_w", formulaPtr.Screen.W).
			Int("formula_h", formulaPtr.Screen.H).
			Int("screen_w", w).
			Int("screen_h", h).
			Float64("display_scale", k).
			Str("projection", law).
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

	// 3c. When the strategy carries a formula, the line this battle actually
	//     deploys on is the formula's own troop ground — not the corner pin and
	//     not the red-zone box line. Publish that instead of the pin, because
	//     the two disagreed and the log described the wrong one: run15
	//     reported "active deploy line line_start={136,404} line_end={446,554}"
	//     while every troop, spell and hero tap went to x=192 between y=144 and
	//     y=576. A deploy line the reader cannot match to the taps is worse than
	//     no line at all, and it is also the fallback ground the sweep and hero
	//     paths use when their own formula lookup misses — where a diagonal
	//     across the middle of the base is the last thing that should stand in
	//     for the ground the troops just deployed on.
	// A USER PIN outranks the strategy formula once and for all. The pins in
	// precision_config.json are placed by a human looking at the battle screen
	// (cmd/pick_coords), which is the only source of geometry in this system that
	// knows where the game actually accepts a drop — the red-line detector has
	// been wrong in both directions and the formula is a one-off authoring pass.
	// Substituting the formula here is exactly what made pinning feel useless:
	// the log said "user-pinned deploy line" and every tap went to the formula's
	// ground instead.
	if hasFormula && !userPinnedForTarget {
		if fl, ok := formulaGroundLine(formulaPtr, s, targetEdge); ok {
			if len(deployLine.Points) >= 2 {
				e.logger.Info().
					Str("configured_side", deployLine.Side).
					Interface("configured_start", deployLine.Points[0]).
					Interface("configured_end", deployLine.Points[len(deployLine.Points)-1]).
					Interface("formula_start", fl.Points[0]).
					Interface("formula_end", fl.Points[len(fl.Points)-1]).
					Msg("configured deploy line is not the line the taps use; adopting the strategy formula's own troop ground")
			}
			deployLine = fl
		}
	} else if userPinnedForTarget && hasFormula {
		e.logger.Info().
			Str("side", targetEdge).
			Interface("pinned_start", deployLine.Points[0]).
			Interface("pinned_end", deployLine.Points[len(deployLine.Points)-1]).
			Msg("using the USER-PINNED deploy line for every troop, hero and sweep tap (strategy formula not substituted for this side)")
	}

	// The pinned line is used exactly as drawn. It is NOT translated into a
	// guessed "safe" box when the red boundary is unknown: that box is an inset
	// of the FRAME, while the no-deploy ring belongs to the MAP, so the guess has
	// no way to be right and every run it fires on rewrites the user's geometry.
	// (The earlier version of this did exactly that: a drawn TopLeft line
	// (187,317)->(506,71) became (256,413)->(575,226).) A pin the user placed on
	// the battle screen is the one piece of deploy geometry that is known good;
	// if a tap is refused, re-pin rather than let the bot move it.

	// Which pin FILE this battle is obeying, and how old it is. The pins are the
	// one source of geometry a human authored (cmd/pick_coords), so a run that
	// reads a different copy than the picker wrote is attacking with geometry
	// nobody chose — and nothing in the old log could show it. Live 2026-09-27:
	// `wails dev` runs build/bin/ClashGO.app, whose bundled assets/ were copied
	// when that bundle was last built, so every run that night attacked with
	// pins from 2026-08-10 while the picker kept writing current ones into the
	// project tree. GetAssetsDir now prefers the source tree for a dev build;
	// this line is what proves it, every battle.
	if pinPath := paths.Resolve("precision_config.json"); pinPath != "" {
		ev := e.logger.Info().
			Str("pins_path", pinPath).
			Str("assets_source", paths.DescribeAssets().Source)
		if age, ok := paths.FileAge(pinPath); ok {
			ev = ev.Float64("pins_age_hours", age.Hours())
		}
		if ignored := paths.DescribeAssets().IgnoredDir; ignored != "" {
			ev = ev.Str("ignored_assets_dir", ignored)
			if age, ok := paths.FileAge(filepath.Join(ignored, "precision_config.json")); ok {
				ev = ev.Float64("ignored_pins_age_hours", age.Hours())
			}
		}
		ev.Msg("deploy geometry comes from this pin file (a second asset tree, if any, is named here)")
	}

	// The ground the hero path and the sweep will drop on. Logged once per
	// battle because it is the difference between a drop the game accepts and
	// one it silently refuses: run10 dropped both heroes on the pinned edge
	// line's midpoint, 150px inland of the line every troop and spell had just
	// deployed along, and the game refused all of them.
	if len(deployLine.Points) > 0 {
		e.logger.Info().
			Str("side", deployLine.Side).
			Interface("line_start", deployLine.Points[0]).
			Interface("line_end", deployLine.Points[len(deployLine.Points)-1]).
			Int("line_points", len(deployLine.Points)).
			Bool("pinned", userPinnedForTarget).
			Bool("red_zone_valid", redZone.Valid).
			Int("red_zone_polygon", len(redZone.Polygon)).
			Msg("active deploy line for this battle")
	}

	// 4. Initialize SlotManager
	slotMgr := NewSlotManager(screen, e.cal, pCfg, w, h, mBarY, e.templates, e.classify, e.logger)
	if len(slotMgr.GetAllSlots()) == 0 {
		return 0, fmt.Errorf("no active slots detected")
	}

	// 5. Detect troop counts.
	//
	// Read the bar over several frames, not one. The bar animates in at the
	// start of a battle and a card caught mid-animation can carry no glyph the
	// reader can find at all: a live 720p run read ONE of ten cards on the
	// frame the deploy started from (`readable=7 slots=10`, six of them measured
	// zeros for cards that had 9, 13 and 21 troops on them once the same bar had
	// settled), while four of six labels read cleanly on a saved frame a second
	// later. The count is stable until the first drop, so extra captures here are
	// free — the battle timer starts at the first drop, not at the first capture.
	//
	// Deliberately unconditional: a heuristic that only re-reads when the first
	// frame "looks bad" has to recognise a bad frame from the same pixels that
	// made it bad. Two extra captures cost a few hundred milliseconds of a
	// three-minute battle.
	troopCounter := NewTroopCounter(e.cal, pCfg.Width, pCfg.Height, e.logger)
	slots := slotMgr.GetAllSlots()
	extraCountFrames := troopCountExtraFrames
	if e.perfMode {
		// Performance mode: the multi-frame merge costs two extra captures plus
		// ~0.5s before the first drop; the battle-start frame stands alone.
		extraCountFrames = 0
	}
	countFrames := []gocv.Mat{screen}
	for attempt := 0; attempt < extraCountFrames; attempt++ {
		if err := e.runtimeContext().Err(); err != nil {
			return len(slotMgr.GetUndeployedSlots()), err
		}
		time.Sleep(troopCountFrameSettle)
		if err := e.runtimeContext().Err(); err != nil {
			return len(slotMgr.GetUndeployedSlots()), err
		}
		next, err := e.client.CaptureToMat()
		if err != nil {
			e.logger.Warn().Err(err).Int("captured", len(countFrames)).Msg("extra troop-bar capture failed; merging what was captured")
			break
		}
		if next.Empty() {
			next.Close()
			break
		}
		countFrames = append(countFrames, next)
	}
	defer func() {
		// screen is the caller's; only the extras are ours to release.
		for _, extra := range countFrames[1:] {
			extra.Close()
		}
	}()

	troopCounts := troopCounter.DetectCountsMerged(countFrames, slots, mBarY)
	countMap := GetAllCounts(troopCounts)
	e.logger.Info().
		Interface("counts", countMap).
		Int("frames", len(countFrames)).
		Msg("detected troop counts")

	// When the merged read still cannot settle every card, the pixels are the
	// only thing that can explain why, and the frame is gone a second later. Save
	// the bar region of the last (most settled) capture next to the other
	// evidence artifacts, so a live failure can be diagnosed with
	// `see look -img <file>` or by eye instead of re-running the attack and
	// hoping. One small PNG per attack, overwritten each time.
	if readable, total := ReadableCounts(troopCounts); readable < total {
		last := countFrames[len(countFrames)-1]
		if saveTroopBarFrame(last, paths.ResolveConfig("last_troop_bar.png")) {
			e.logger.Warn().
				Int("readable", readable).
				Int("slots", total).
				Int("frames", len(countFrames)).
				Msg("troop bar not fully resolved; saved the frame the reader used to last_troop_bar.png")
		}
	}
	// troopCounter is threaded below to NewHeroManager / NewSweeper /
	// NewVerifier so they can live-OCR per-slot counts at deploy time
	// and reconcile until the slot is truly empty (fixes the
	// "balloons/EDs sometimes don't all get placed" bug).

	// 6. Initialize tap executor
	tapExec := NewTapExecutor(e.client, e.cal, e.logger)
	// Give deployers a unit-name -> slot lookup (used to pair Amount:"All"
	// spells with their live OCR counts).
	tapExec.SetSlotResolver(slotMgr.GetSlot)

	// Arm the deploy budget. Every phase/reconcile/sweep loop below
	// consults tapExec.DeployBudgetExhausted() between rounds so a stuck
	// slot (spent spell card still reading visually non-empty, broken OCR,
	// etc.) can never fire taps past the 3-minute battle timer. Observed
	// live: the EQ-spell sweep kept re-firing for ~2 minutes after the
	// battle had already ended because no wall-clock bound existed.
	tapExec.StartDeployBudget()
	// Arm the deployment guard: a fresh-frame watcher that (a) blocks all
	// further device input once the bot is stopped or the battle-end overlay
	// is confirmed twice, and (b) supplies the frames the hero HP monitor
	// polls during the deploy. deferred teardown joins the watcher before the
	// tap executor is released.
	stopDeploy := e.SetDeploymentContext(e.runtimeContext(), tapExec)
	defer stopDeploy()
	// deployStart is the clock behind the DeployGoal line reported after the
	// phase loop: the user's "all troops and spells in under seven seconds"
	// window, measured rather than guessed. It is the first placement phase's
	// start, so it includes the inter-phase settles and every per-unit slot
	// selection that the deploy actually pays for.
	deployStart := time.Now()

	// 7. Plan phases
	planner := NewDeployPlanner(slotMgr, pCfg, targetEdge, w, h, e.logger)
	plans := planner.PlanDeployment(s)

	// Full-army readiness, honored only on POSITIVE evidence: a strategy
	// troop/spell whose card measurably reads zero on the settled multi-frame
	// bar means the army is incomplete. Unknown counts never block — there is
	// no detector for "full" yet, so claiming one would be a guess.
	if e.fullArmyRequired {
		if gap := armyReadinessGap(plans, countMap, slotMgr); len(gap) > 0 {
			return 0, fmt.Errorf(
				"army not full (training.full_army_before_attack): %s measurably empty on the troop bar; train before attacking",
				strings.Join(gap, ", "))
		}
	}

	// 8. Collect strategy unit names
	strategyNames := GetStrategyUnitNames(s)

	// 9. Execute each phase
	//
	// pendingAbilities collects ability unit plans from ANY phase (both
	// a dedicated "Abilities" phase and pattern:Ability units declared
	// inline inside the Heroes phase — auto_edrag_rush uses the latter
	// shape). They are fired only when an explicit "Abilities" phase is
	// reached, or — for the inline shape — at the very END of the deploy,
	// after the sweep and the verifier, never mid-deploy. Tapping the
	// hero icon a second time re-selects the hero's card, which poisons
	// the next slot-selection tap until that card is deployed or
	// deselected; deferring the pass keeps it from eating the next
	// unit's selection (valk_run6: abilities fired 60ms after the hero
	// drops and the EQ phase then tapped the siege slot instead of the
	// earthquake card). Running it after every placement step means no
	// slot-selection tap can follow it at all.
	// Spell deployer for the phase loop + the deferred reconcile pass.
	spellDeployer := NewSpellDeployerWithCounts(tapExec, pCfg, formulaPtr, w, h, countMap, e.logger)
	spellDeployer.SetCounter(troopCounter, slotMgr.GetBarY())
	spellDeployer.SetPinnedGround(userPinnedForTarget)
	var spellReconcile []spellReconcileEntry

	var pendingAbilities []UnitPlan
	fireAbilities := func() {
		if len(pendingAbilities) == 0 {
			return
		}
		time.Sleep(40 * time.Millisecond)
		for _, up := range pendingAbilities {
			if up.Slot == nil {
				continue
			}
			// Only activate a hero that actually DEPLOYED. Tapping the
			// hero icon while its card is still on the bar re-SELECTS the
			// card, and marking it deployed here would hide it from the
			// sweep — the exact bug class this pass was built to avoid
			// (live, 21:03 run: BK's drop verify failed twice, the pass
			// tapped + marked it deployed and the sweep skipped it).
			if up.Slot.State != SlotDeployed {
				e.logger.Warn().
					Str("unit", up.Unit.Name).
					Int("x", up.Slot.X).
					Str("state", up.Slot.State.String()).
					Msg("ability pass: hero not confirmed deployed; skipping ability (sweep owns the retry)")
				continue
			}
			if tapExec.DeployBudgetExhausted() {
				e.logger.Warn().Msg("ability pass: deploy budget exhausted; stopping")
				pendingAbilities = nil
				return
			}
			if err := e.deploymentErr(); err != nil {
				pendingAbilities = nil
				return
			}
			if !tapExec.TapHeroAbility(up.Slot) {
				// The ability is a one-shot per hero: TapHeroAbility refuses only
				// when it already fired (the HP watcher got there first).
				e.logger.Warn().
					Str("unit", up.Unit.Name).
					Int("x", up.Slot.X).
					Msg("ability pass: hero ability already fired this battle; skipping")
				continue
			}
			e.logger.Info().
				Str("unit", up.Unit.Name).
				Int("x", up.Slot.X).
				Msg("hero ability activated")
			// Fast sequential burst across hero cards: activates all abilities
			// together in a rapid human-like wave.
			tapExec.HumanSleep(25, 10)
		}
		pendingAbilities = nil
	}

	// The hero manager is created ONCE per battle, not per phase. It carries the
	// ground the troop passes recorded (see HeroManager.troopGround) and the
	// per-hero 2-tap bookkeeping the phases share; a per-phase instance threw
	// both away — live run14 the hero phase ran with troop_ground_points=0 for
	// exactly that reason.
	heroMgr := NewHeroManager(tapExec, slotMgr, pCfg, targetEdge, w, h, formulaPtr, troopCounter, deployLine, e.logger)
	// Bridge: when HeroManager's resolveHeroTarget fires for the Dragon Duke,
	// route the event through Executor.OnDukePick so a single observer (live
	// bot's NDJSON writer, debug_test's recorder) sees BOTH the legacy
	// adjacent-corner random pick and the new "follow the chosen edge"
	// behavior. chosen == target in the new path — Duke falls through to the
	// chosen edge with a random point along it.
	heroMgr.OnDukeDeployed = func(target string) {
		if e.OnDukePick != nil {
			e.OnDukePick(target, target)
		}
	}
	// Ability activation is owned by the dedicated "Abilities" phase below,
	// which runs AFTER the spell phases (strategy YAML order is the user's
	// intent). The old always-on auto-activation inside DeployHeroes fired the
	// abilities ~60ms after the heroes dropped and BEFORE the spell phase: the
	// tap re-selected the hero's card, and the next slot-selection tap then
	// landed on the still-highlighted hero icon — live, that burned an ability
	// and tapped the siege machine's slot instead of the earthquake card
	// (valk_run6, 15:11:39).
	heroMgr.SetActivateAbility(false)
	// Hero drops are filtered against this battle's red line: the active
	// deployLine can be the user's pinned reference-geometry edge (which no
	// other code path validates), and dropping a hero on that ground was
	// live-refused for the whole hero phase in run10.
	heroMgr.SetRedZone(redZone)
	heroMgr.SetPinnedGround(userPinnedForTarget)

	for _, plan := range plans {
		if err := e.deploymentErr(); err != nil {
			return len(slotMgr.GetUndeployedSlots()), err
		}
		// A definite tap transport failure is sticky: no retry can know
		// whether the earlier tap landed, so the deploy stops at this
		// checkpoint instead of firing duplicate or blind taps.
		if err := tapExec.InputErr(); err != nil {
			remaining := len(slotMgr.GetUndeployedSlots())
			return remaining, fmt.Errorf("tap transport failed (%w); %d slots undeployed", err, remaining)
		}
		// Hard deploy-time stop: if the budget ran out mid-plan, abandon
		// the remaining phases instead of tapping into the battle timer.
		// The leftover slots are reported as undeployed so the attack
		// report shows the partial failure instead of a phantom success.
		if tapExec.DeployBudgetExhausted() {
			remaining := len(slotMgr.GetUndeployedSlots())
			e.logger.Warn().
				Str("phase", plan.Phase.Name).
				Dur("budget", DeployBudget).
				Int("undeployed", remaining).
				Msg("deploy budget exhausted before phases completed; stopping deploy (battle-timer guard)")
			return remaining, fmt.Errorf("deploy budget exhausted (%s); %d slots undeployed", DeployBudget, remaining)
		}

		e.logger.Info().Str("phase", plan.Phase.Name).Msg("attack phase")
		if e.OnPhaseStart != nil {
			e.OnPhaseStart(plan.Phase.Name, targetEdge)
		}
		phaseStart := time.Now()

		// Deploy spells. The deployer is hoisted out of the loop (it threads
		// the live OCR counts + slot resolver so Amount:"All" spells tap
		// exactly the count the army carries instead of a hardcoded 5: the
		// valk EQ army carries e.g. 4 EQs on one card; the edrag rush carries
		// 11 rage on another) and its reconcile pass runs AFTER the placements
		// window — see spellReconcile below.
		for _, up := range ResolveSpellTargets(plan) {
			if err := e.deploymentErr(); err != nil {
				return len(slotMgr.GetUndeployedSlots()), err
			}
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
			// 150ms is the empirically-required CoC slot-selection
			// animation floor (matches deploySingleHero's settle on
			// heroes). The previous 35ms was too fast: the bot
			// frequently "selected" the ED slot but CoC was still
			// mid-animation from the prior phase, so taps fired on
			// the OLD unit type instead of ED. Live data:
			// auto_edrag_rush showed zero EDs on the field after
			// the deploy despite the bot reporting "all units
			// successfully deployed". Same fix applies to Spells +
			// Siege (they share the 35ms gap and the same bug).
			tapExec.HumanSleep(150, 30)

			spellTargetEdge := targetEdge
			if strings.EqualFold(plan.Phase.Position, "Center") {
				spellTargetEdge = "Center"
			}

			success := spellDeployer.DeploySpell(up.Unit, up.Slot, spellTargetEdge, plan.Phase.Pattern)
			if success {
				slotMgr.MarkDeployed(strings.ToLower(up.Unit.Name))
				// Post-deploy reconcile is DEFERRED to after the placements
				// window: the top-up decision is better made on settled cards,
				// and a slow OCR round must not sit inside the seven-second
				// deploy window. Any extra taps still fire before the sweep and
				// stay budget-guarded.
				spellReconcile = append(spellReconcile, spellReconcileEntry{unit: up.Unit, slot: up.Slot, pattern: plan.Phase.Pattern, edge: spellTargetEdge})
			}
		}

		// Deploy troops
		for _, up := range ResolveTroopTargets(plan) {
			if err := e.deploymentErr(); err != nil {
				return len(slotMgr.GetUndeployedSlots()), err
			}
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
			// 150ms is the empirically-required CoC slot-selection
			// animation floor (matches deploySingleHero's settle on
			// heroes). The previous 35ms was too fast: the bot
			// frequently "selected" the ED slot but CoC was still
			// mid-animation from the prior phase, so taps fired on
			// the OLD unit type instead of ED. Live data:
			// auto_edrag_rush showed zero EDs on the field after
			// the deploy despite the bot reporting "all units
			// successfully deployed". Same fix applies to Spells +
			// Siege (they share the 35ms gap and the same bug).
			tapExec.HumanSleep(150, 30)

			// detectedTrusted travels with the count: an unreadable card and an
			// empty one both read 0, and DeployTroops uses the difference to
			// decide whether it may skip its own pre-deploy capture.
			detectedCount, detectedTrusted := GetCountForSlotMeasured(troopCounts, up.Slot.X)
			unitStart := time.Now()
			heroMgr.DeployTroops(up.Unit, up.Slot, plan.Phase.Pattern, plan.Phase.Offset, plan.Phase.Pattern, screen, detectedCount, detectedTrusted)
			e.logger.Info().
				Str("unit", up.Unit.Name).
				Int("x", up.Slot.X).
				Int64("elapsed_ms", time.Since(unitStart).Milliseconds()).
				Msg("unit deploy elapsed")
		}

		// Deploy siege
		for _, up := range ResolveSiegeTargets(plan) {
			if err := e.deploymentErr(); err != nil {
				return len(slotMgr.GetUndeployedSlots()), err
			}
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
			// 150ms is the empirically-required CoC slot-selection
			// animation floor (matches deploySingleHero's settle on
			// heroes). The previous 35ms was too fast: the bot
			// frequently "selected" the ED slot but CoC was still
			// mid-animation from the prior phase, so taps fired on
			// the OLD unit type instead of ED. Live data:
			// auto_edrag_rush showed zero EDs on the field after
			// the deploy despite the bot reporting "all units
			// successfully deployed". Same fix applies to Spells +
			// Siege (they share the 35ms gap and the same bug).
			tapExec.HumanSleep(150, 30)

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
				// Heroes are eligible for HP-triggered abilities from the
				// moment they land, not only after the whole deploy.
				e.armHeroWatch(slotMgr)
			}
			if err := e.deploymentErr(); err != nil {
				return len(slotMgr.GetUndeployedSlots()), err
			}
		}

		// Collect this phase's ability plans; fire them only when the
		// strategy explicitly reaches an "Abilities" phase (valk_spam
		// shape), so spells authored before it still go first.
		pendingAbilities = append(pendingAbilities, ResolveAbilityTargets(plan)...)
		if strings.EqualFold(strings.TrimSpace(plan.Phase.Name), "Abilities") || plan.Phase.Pattern == "Ability" {
			fireAbilities()
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
		const heroSiegeDefault = 50 * time.Millisecond
		const interPhaseMin = 50 * time.Millisecond // Safety floor so YAML values like delay_after_ms: 10 do not bypass the inter-phase settle window — CoC needs ~50ms minimum between phases to register the new troop bar state.
		const maxPhaseDelay = 200 * time.Millisecond
		pDelay := time.Duration(plan.Phase.DelayAfterMS) * time.Millisecond
		isHeroOrSiege := strings.Contains(plan.Phase.Name, "Heroes") || strings.Contains(plan.Phase.Name, "Siege")
		if isHeroOrSiege && pDelay <= 0 {
			pDelay = heroSiegeDefault
		}
		if pDelay < interPhaseMin {
			pDelay = interPhaseMin
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
			if err := e.deploymentErr(); err != nil {
				return len(slotMgr.GetUndeployedSlots()), err
			}
		}
		e.logger.Info().
			Str("phase", plan.Phase.Name).
			Int64("phase_elapsed_ms", time.Since(phaseStart).Milliseconds()).
			Int64("deploy_elapsed_ms", time.Since(deployStart).Milliseconds()).
			Int64("goal_ms", DeployGoal.Milliseconds()).
			Msg("phase complete")
	}

	// The user's target, in the run's own words: every placement phase (troops,
	// siege, heroes, spells) from the first tap to the last cast. Reported
	// unconditionally — a wall clock can only be moved by measuring it, and the
	// line above gives the per-phase split that says where it went.
	placementsMs := time.Since(deployStart).Milliseconds()
	goalMs := DeployGoal.Milliseconds()
	e.logger.Info().
		Int64("placements_ms", placementsMs).
		Int64("goal_ms", goalMs).
		Bool("within_goal", placementsMs <= goalMs).
		// The siege contract, in the summary: one placement, and how many later
		// placement taps had to be moved off the machine to keep it alive. A
		// placement count above 1 means a path found a way around PlaceSiege.
		Int("siege_placements", tapExec.SiegePlacements()).
		Int("siege_clearance_nudges", tapExec.SiegeNudges()).
		Msg("deploy window: troops + siege + heroes + spells")
	if placementsMs > goalMs {
		e.logger.Warn().
			Int64("placements_ms", placementsMs).
			Int64("over_by_ms", placementsMs-goalMs).
			Msg("deploy window is over the seven-second goal; see the per-phase elapsed lines above for where it went")
	}

	// Deferred spell reconciliation: live-OCR each fired spell card and re-fire
	// any spells that did not drop, now that the cards have settled. 4 rounds:
	// each round fires only the unconfirmed remainder, and the internal
	// no-progress stop aborts early when OCR reads the same count twice — so
	// more headroom only helps genuinely draining multi-charge cards, never the
	// spent-card loop (live: 1-charge rage re-fired for the old 2-round budget
	// while OCR read "1" every time).
	for _, se := range spellReconcile {
		if err := e.deploymentErr(); err != nil {
			return len(slotMgr.GetUndeployedSlots()), err
		}
		reconcileEdge := se.edge
		if reconcileEdge == "" {
			reconcileEdge = targetEdge
		}
		if extra, confirmed := spellDeployer.VerifyAndReconcile(se.unit, se.slot, reconcileEdge, se.pattern, 4); extra > 0 {
			e.logger.Info().
				Str("unit", se.unit.Name).
				Int("extra_fired", extra).
				Bool("confirmed_empty", confirmed).
				Msg("spell reconcile fired extra spells")
		}
	}

	// 10. Sweep remaining. Pass formulaPtr so the sweep path honors
	// user-pinned _event_troop / _event_spell coords the same way
	// DeployHeroes / DeployTroops already do. Without this the bot
	// silently dropped event troops on the dynamically-detected
	// red-zone line, ignoring the user's pin entirely.
	sweeper := NewSweeper(tapExec, slotMgr, pCfg, deployLine, w, h, formulaPtr, troopCounter, s.EventTroopsAutoDeployEnabled(), e.logger)
	sweeper.SetRedZone(redZone)
	sweeper.SetPinnedGround(userPinnedForTarget)
	sweepStart := time.Now()
	sweeper.Sweep(strategyNames, countMap)
	e.logger.Info().
		Int64("sweep_ms", time.Since(sweepStart).Milliseconds()).
		Int64("deploy_total_ms", time.Since(deployStart).Milliseconds()).
		Msg("sweep complete with deploy window total")

	// 11. Verify
	verifier := NewVerifier(tapExec, slotMgr, pCfg, targetEdge, w, h, DefaultVerifyConfig(), troopCounter, e.logger)
	verifier.SetRedZone(redZone)
	remainingCount := verifier.VerifyAll()

	// 12. Evidence capture: save a post-deploy annotated screenshot so every	// real attack can be reviewed afterwards (did spells land on the formula
	// lines? did every card drain?). Passes the formula the deploy path used —
	// corner-resolved, projected, clamped — so the picture shows the taps that
	// were fired and not a re-derivation of them. Best-effort: a capture failure
	// must never fail an otherwise-good attack. Skipped in performance mode
	// (CLI -perf): the PNGs are review artifacts, not part of the attack.
	if !e.perfMode {
		e.dumpAttackEvidence(formulaPtr, targetEdge, w, h, remainingCount)
	}

	// Hero abilities go LAST, after every placement step there is (the
	// phase loop, the sweep, the verifier's re-deploy). The user's contract:
	// both heroes down, then spells, then abilities — with the hero ->
	// ability gap spanning the whole deploy, because the activation taps are
	// slow to land and firing them early also buys nothing. Doing it here
	// means no slot-selection tap can ever follow the ability tap (the
	// valk_run6 mis-select class cannot happen), and a hero that only the
	// sweep or the verifier recovered still gets its ability.
	// Abilities declared inline (auto_edrag_rush's pattern:Ability units
	// inside the Heroes phase, with a Spells phase after) never hit an
	// "Abilities" phase, so this is their only firing point.
	fireAbilities()

	// Catch any hero deployed outside the main hero phase (sweep recovery,
	// event troops) so the battle-end wait can still fire its ability. Runs
	// after the pass above: a hero whose ability already fired has spent its
	// 2-tap budget and is not watched again.
	e.armHeroWatch(slotMgr)

	return remainingCount, nil
}

// dumpAttackEvidence saves last_attack_overview.png (the raw post-deploy
// frame) plus last_attack_taps.png (the same frame annotated with every
// planned formula tap point for the corner actually attacked) under the
// config dir. Skipped silently when a fresh capture isn't possible.
//
// It annotates the formula the DEPLOY PATH actually used — the one passed in,
// already corner-resolved, projected at the live display scale and clamped —
// rather than reloading the file and re-deriving it. That is not an
// optimisation: this function used to reload the formula, re-apply the corner
// override, re-project with the legacy per-axis ApplyScreenScale, and judge the
// result against a third band of its own (6%..78% of the height). Measured on a
// real 720p attack, those points sat up to 76 px from the taps that were fired,
// so the artifact a human reviews after a bad attack showed taps that never
// happened, in a geometry the bot does not run, and reported
// "taps_outside_band=0" for points that were inside the top HUD. Evidence that
// disagrees with the run is worse than no evidence.
func (e *Executor) dumpAttackEvidence(f *formula.Formula, targetEdge string, w, h, remaining int) {
	tapExec := NewTapExecutor(e.client, e.cal, e.logger)
	capture, err := tapExec.CaptureFresh()
	if err != nil {
		e.logger.Warn().Err(err).Msg("evidence capture: could not grab post-deploy frame")
		return
	}
	defer capture.Close()

	if ok := gocv.IMWrite(paths.ResolveConfig("last_attack_overview.png"), capture); !ok {
		e.logger.Warn().Msg("evidence capture: IMWrite overview failed")
	}

	if f == nil {
		return
	}
	annotated := capture.Clone()
	defer annotated.Close()
	// The deploy path's own band, not a percentage invented here: the same
	// [yTopMin, uiCutoff] the formula clamp and DeployLineCalculator use.
	bandTop, bandBottom := yTopMin, int(float64(h)*0.85)
	inBand, outside := 0, 0
	for name, entry := range f.Units {
		if strings.HasPrefix(name, "_") {
			continue
		}
		c := color.RGBA{255, 255, 255, 255}
		switch {
		case strings.Contains(strings.ToLower(name), "spell"):
			c = color.RGBA{255, 0, 255, 255}
		case strings.Contains(strings.ToLower(name), "king") || strings.Contains(strings.ToLower(name), "queen"):
			c = color.RGBA{0, 255, 0, 255}
		}
		for _, pt := range formulaEntryPoints(entry) {
			if pt.Y < bandTop || pt.Y > bandBottom {
				// After the clamp this should be impossible; ring it anyway,
				// because a point that gets here is a tap on the HUD.
				outside++
				gocv.Circle(&annotated, pt, 14, color.RGBA{0, 0, 255, 255}, 3)
			} else {
				inBand++
			}
			gocv.Circle(&annotated, pt, 5, c, 2)
		}
	}
	// The band is drawn too, so a reader can see which side of the line a tap is
	// on instead of taking the count on trust.
	gocv.Rectangle(&annotated, image.Rect(0, bandTop, w, bandBottom), color.RGBA{0, 128, 255, 255}, 1)
	gocv.PutText(&annotated,
		fmt.Sprintf("edge=%s k=%.3f band y=%d..%d taps_in_band=%d outside_band=%d remaining=%d",
			targetEdge, e.cal.DisplayScale(), bandTop, bandBottom, inBand, outside, remaining),
		image.Pt(10, 20), gocv.FontHersheySimplex, 0.5, color.RGBA{0, 255, 255, 255}, 1)
	if ok := gocv.IMWrite(paths.ResolveConfig("last_attack_taps.png"), annotated); !ok {
		e.logger.Warn().Msg("evidence capture: IMWrite taps failed")
	}
	e.logger.Info().
		Str("edge", targetEdge).
		Float64("display_scale", e.cal.DisplayScale()).
		Int("band_top", bandTop).
		Int("band_bottom", bandBottom).
		Int("taps_in_band", inBand).
		Int("taps_outside_band", outside).
		Int("remaining", remaining).
		Msg("evidence capture: saved last_attack_overview.png + last_attack_taps.png")
}

// formulaEntryPoints flattens a UnitEntry into the tap points the deploy
// path would fire (shared by the evidence annotator).
func formulaEntryPoints(e formula.UnitEntry) []image.Point {
	var pts []image.Point
	switch {
	case e.IsPoint() && e.P != nil:
		pts = append(pts, e.P.Image())
	case e.IsLine() && e.P1 != nil && e.P2 != nil:
		pts = append(pts, e.P1.Image(), e.P2.Image())
	case e.IsLines():
		for _, lp := range e.Lines {
			pts = append(pts, lp.P1.Image(), lp.P2.Image())
		}
	}
	return pts
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
	return lineFromPoints(edge.P1, edge.P2, targetEdge, n)
}

// lineFromPoints interpolates a deploy line between two points. It is the one
// place the bot turns a pair of coordinates into the point list every consumer
// reads, so a pinned edge and a formula-derived ground cannot drift apart in
// spacing or anchor convention.
func lineFromPoints(p1, p2 image.Point, side string, n int) DeployLine {
	if n < 2 {
		n = linePoints
	}
	pts := make([]image.Point, n)
	for i := 0; i < n; i++ {
		t := float64(i) / float64(n-1)
		pts[i] = image.Pt(
			p1.X+int(t*float64(p2.X-p1.X)),
			p1.Y+int(t*float64(p2.Y-p1.Y)),
		)
	}
	return DeployLine{
		Points:  pts,
		Side:    side,
		Anchor:  pts[len(pts)/2],
		Outside: true,
	}
}

// formulaGroundLine derives the deploy line from the strategy formula entry of
// the FIRST troop unit the strategy deploys.
//
// First troop, not every unit: the troop phase runs first and a strategy
// deploys its troops along one side, so that unit's entry is the authored
// statement of where this battle's ground is. Spells, heroes and the siege
// machine all have their own entries and their own (different) intent, so
// folding them in would average away the line instead of reporting it.
//
// Returns ok=false when there is no formula, no troop unit, or the entry is a
// point rather than a line — a point is a legitimate plan, but it is not a
// deploy LINE and must not be published as one.
func formulaGroundLine(f *formula.Formula, s *strategy.DynamicStrategy, corner string) (DeployLine, bool) {
	if f == nil || s == nil {
		return DeployLine{}, false
	}
	for _, phase := range s.Phases {
		for _, unit := range phase.Units {
			name := strings.ToLower(strings.TrimSpace(unit.Name))
			if unit.Pattern == "Ability" || phase.Pattern == "Ability" {
				continue
			}
			if isSpellStatic(name) || isHeroStatic(name) || isSiegeStatic(name) {
				continue
			}
			entry, ok := f.LookUp(name)
			if !ok {
				continue
			}
			p1, p2, have := entryEndpoints(entry, 0)
			if !have || p1 == p2 {
				return DeployLine{}, false
			}
			return lineFromPoints(p1, p2, corner, linePoints), true
		}
	}
	return DeployLine{}, false
}

// entryEndpoints returns the endpoints of a formula entry's idx'th segment.
// Point entries yield the same point twice; entries with no coordinates yield
// have=false so a caller can fall through instead of deploying at (0,0).
func entryEndpoints(entry formula.UnitEntry, idx int) (image.Point, image.Point, bool) {
	switch {
	case entry.IsPoint() && entry.P != nil:
		p := entry.P.Image()
		return p, p, true
	case entry.IsLine() && entry.P1 != nil && entry.P2 != nil:
		return entry.P1.Image(), entry.P2.Image(), true
	case entry.IsLines() && idx < len(entry.Lines):
		// LinePoint carries coordinates by value, so "present" is decided by
		// the entry actually listing a segment at this index, not by a nil
		// check.
		lp := entry.Lines[idx]
		return lp.P1.Image(), lp.P2.Image(), true
	}
	return image.Point{}, image.Point{}, false
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
// applyCornerUnits resolves which unit coordinates apply to a corner, and
// reports whether an authored override was used. Per-corner override (if
// present) wins over the mirror: the user may have authored explicit coords for
// this corner via `cmd/design_attack -corner BL` (which writes to
// formula.corner_overrides.BL). That is more accurate than reflecting the BR
// default, because different base geometries have different red-line positions
// on each side, and a mirror across a non-symmetric base puts the line either
// too close to the new side's red line (overlap) or too far from it.
//
// The merge is per-unit, and a typical override is PARTIAL: only the units that
// differ from the mirrored default are re-pinned, and the rest fall through to
// formula.units.
//
// When there is no override the BR default is mirrored around the formula's
// authored 860×732 frame — the replacement for the older FourSides pattern, so
// the user authors ONE attack and the orchestrator mirrors it per corner.
func applyCornerUnits(f *formula.Formula, corner string) bool {
	if f == nil {
		return false
	}
	if cornerUnits, ok := f.CornerOverrides[corner]; ok {
		merged := make(map[string]formula.UnitEntry, len(f.Units)+len(cornerUnits))
		for k, v := range f.Units {
			merged[k] = v
		}
		for k, v := range cornerUnits {
			merged[k] = v
		}
		f.Units = merged
		return true
	}
	f.MirrorForCorner(corner)
	return false
}

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
			if e, exists := pCfg.Edges[c]; !exists || isZeroManualEdge(e) {
				pCfg.Edges[c] = pinnedOverride
			}
		}
		for _, s := range sideNames {
			if sEdge, exists := pCfg.Sides[s]; !exists || isZeroManualEdge(sEdge) {
				pCfg.Sides[s] = pinnedOverride
			}
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
