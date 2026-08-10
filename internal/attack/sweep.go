package attack

import (
	"image"
	"math/rand"
	"strings"
	"time"

	"github.com/Ducky705/ClashGO/pkg/formula"
	"github.com/rs/zerolog"
)

// Sweeper handles final sweep to catch undeployed troops.
type Sweeper struct {
	executor     *TapExecutor
	slotManager  *SlotManager
	pCfg         PrecisionConfig
	deployLine   DeployLine
	formula      *formula.Formula
	troopCounter *TroopCounter // optional; enables live-OCR count + reconcile
	autoEvent    bool          // deploy bonus/event troops not in strategy (default on)
	lineForward  bool          // boustrophedon direction toggle for fireTapsBatched
	w, h         int
	logger       zerolog.Logger
}

// NewSweeper creates a new sweeper. formula may be nil; when non-nil,
// the sweeper consults it FIRST so user-pinned _event_troop / _event_spell
// coordinates win over the dynamic red-zone fallback. Without this, even
// with a per-unit formula authored, the sweep phase re-tapped along the
// old red-zone line, scattering event troops to the wrong side.
// troopCounter may also be nil; when non-nil, the sweeper uses it to
// live-OCR the slot's per-card count at retry time AND runs the
// reconcile loop until the slot is truly empty (live count 0 AND
// visual-empty). This is the belt-and-braces fix for the
// "balloons/EDs sometimes don't all get placed" user-reported bug.
// autoEvent gates the event-troop dump; true (the default) places ALL
// bonus/seasonal troops that the strategy didn't declare.
func NewSweeper(
	executor *TapExecutor,
	slotManager *SlotManager,
	pCfg PrecisionConfig,
	deployLine DeployLine,
	w, h int,
	f *formula.Formula,
	troopCounter *TroopCounter,
	autoEvent bool,
	logger zerolog.Logger,
) *Sweeper {
	return &Sweeper{
		executor:     executor,
		slotManager:  slotManager,
		pCfg:         pCfg,
		deployLine:   deployLine,
		formula:      f,
		troopCounter: troopCounter,
		autoEvent:    autoEvent,
		w:            w,
		h:            h,
		logger:       logger.With().Str("component", "sweeper").Logger(),
	}
}

// Sweep deploys any remaining undeployed slots.
// Uses FRESH screen capture for each slot check (fixes stale screen bug).
//
// Empty-slot guard added between batches inside deploySlot: a slot that
// emptied mid-batch short-circuits without firing the remaining taps.
// Defaults are now 1 tap (not 12) when neither troop detection nor the
// formula gave a count, so we don't over-deploy when the slot only held
// 5 troops. The previous 12 default silently wasted ~7 taps per slot.
func (sw *Sweeper) Sweep(strategyUnitNames []string, troopCounts map[int]int) int {
	sw.logger.Info().Msg("starting sweep of remaining slots")

	deployedCount := 0

	// Get all undeployed slots
	undeployed := sw.slotManager.GetUndeployedSlots()
	for _, slot := range undeployed {
		// Skip siege slots (handled separately)
		if slot.Category == "Siege" {
			continue
		}

		// Capture FRESH screen for each slot check (critical fix)
		freshScreen, err := sw.executor.CaptureFresh()
		if err != nil {
			sw.logger.Warn().Err(err).Msg("failed to capture fresh screen for sweep")
			continue
		}

		// Check if slot is actually empty on fresh screen
		empty := isSlotEmptyStatic(freshScreen, slot.X, slot.Y, sw.w, sw.h)
		freshScreen.Close()

		if empty {
			slot.IsEmpty = true
			continue
		}

		// Get troop count for this slot. Default to a single tap only when
		// detection came back empty AND the formula gave us no explicit
		// count — the old 12 default silently over-deployed when the slot
		// only held 5 troops, causing phantom extra taps on empty space.
		count := troopCounts[slot.X]
		if entry, ok := sw.eventFormulaEntry(slot); ok && entry.Count > 0 {
			count = entry.Count
		}
		if count <= 0 {
			count = 1
		}

		sw.logger.Info().
			Int("x", slot.X).
			Str("unit", slot.UnitName).
			Str("category", slot.Category).
			Int("count", count).
			Msg("found undeployed slot during sweep")

		// Deploy this slot with correct count
		success := sw.deploySlot(slot, count, false)
		if success {
			sw.slotManager.MarkDeployed(slot.UnitName)
			deployedCount++
		} else {
			sw.slotManager.MarkFailed(slot.UnitName)
		}
		time.Sleep(60 * time.Millisecond)
	}

	// Also sweep event troops not in strategy. The user asked for ALL
	// bonus/seasonal troops on the bar to be placed down until none
	// remain — a single deploySlot call is NOT enough because its
	// reconcile budget can be exhausted while the slot still holds
	// troops (live OCR returning 0 on a visually-full slot is the
	// classic case). So we LOOP: fresh-check, deploy, re-check, until
	// the slot is truly empty or a generous round budget is spent.
	// Gated on autoEvent (strategy `auto_deploy_eventTroops`, default
	// enabled) so armies that WANT the dump get it without listing the
	// seasonal unit in the YAML, while `false` opts out.
	if sw.autoEvent {
		eventTroops := sw.slotManager.GetEventTroops(strategyUnitNames)
		for _, slot := range eventTroops {
			const maxRounds = 6
			placed := false
			for round := 0; round < maxRounds; round++ {
				// Capture FRESH screen for each check
				freshScreen, err := sw.executor.CaptureFresh()
				if err != nil {
					sw.logger.Warn().Err(err).Msg("failed to capture fresh screen for sweep")
					break
				}

				empty := isSlotEmptyStatic(freshScreen, slot.X, slot.Y, sw.w, sw.h)
				freshScreen.Close()

				if empty {
					slot.IsEmpty = true
					sw.slotManager.MarkDeployed(slot.UnitName)
					placed = true
					break
				}

				// Default the event troop count to 1 if detection gave
				// nothing AND the formula gave no count; live OCR at deploy
				// time will raise it if the card actually holds more.
				count := troopCounts[slot.X]
				if entry, ok := sw.eventFormulaEntry(slot); ok && entry.Count > 0 {
					count = entry.Count
				}
				if count <= 0 {
					count = 1
				}

				sw.logger.Info().
					Int("x", slot.X).
					Str("unit", slot.UnitName).
					Int("count", count).
					Int("round", round+1).
					Msg("found event troop during sweep")

				success := sw.deploySlot(slot, count, true)
				if success {
					sw.slotManager.MarkDeployed(slot.UnitName)
					deployedCount++
					placed = true
					break
				}
				// deploySlot exhausted its internal reconcile budget but the
				// slot is still non-empty on the previous capture. Loop and
				// re-check from a fresh screen instead of giving up.
				time.Sleep(100 * time.Millisecond)
			}
			if !placed {
				sw.slotManager.MarkFailed(slot.UnitName)
				sw.logger.Warn().
					Int("x", slot.X).
					Str("unit", slot.UnitName).
					Msg("event troop sweep: slot still non-empty after max rounds")
			}
		}
	}

	sw.logger.Info().Int("deployed", deployedCount).Msg("sweep complete")
	return deployedCount
}

// eventFormulaEntry returns the formula entry that should drive this
// slot's sweep deployment. For regular slots we honor a per-unit entry
// (e.g. user pinned "super witch" → a specific coord); for slots the
// strategy didn't declare (e.g. an event troop on the bar) we fall
// back to the _event_troop / _event_spell keys the user may have
// authored. Without this fallthrough the sweep path always used the
// dynamic red-zone line, ignoring the user's pin entirely.
func (sw *Sweeper) eventFormulaEntry(slot *TrackedSlot) (formula.UnitEntry, bool) {
	if sw.formula == nil {
		return formula.UnitEntry{}, false
	}
	if e, ok := sw.formula.LookUp(slot.UnitName); ok {
		return e, true
	}
	// Event routing: per-slot name first, then KEYWORD, then bar
	// POSITION. Slot category is unreliable for event spells because
	// CoC's template matching reuses icon frames between seasonal
	// troops and spells — a "skeleton spell" may template-match to
	// just "skeleton", leaving slot.UnitName absent of "spell" AND
	// category="Troop". Two-tier fallback covers that case:
	//   1. Name contains "spell" → _event_spell
	//   2. Slot is on the right half of the bar AND user authored
	//      _event_spell in the formula → _event_spell
	//   3. Otherwise → _event_troop
	//
	// The bar-position heuristic is the standard CoC layout where
	// troops are on the LEFT of the troop bar and spells are on the
	// RIGHT. Failing clans that put spells on the left will need to
	// edit formula.json to set per-slot explicit entry names.
	prefer := "_event_troop"
	_, hasSpell := sw.formula.LookUp("_event_spell")
	if strings.Contains(strings.ToLower(strings.TrimSpace(slot.UnitName)), "spell") ||
		(slot.X > sw.w/2 && hasSpell) {
		prefer = "_event_spell"
	}
	if e, ok := sw.formula.LookUp(prefer); ok {
		return e, true
	}
	return formula.UnitEntry{}, false
}

// deploySlot deploys a single slot with retry.
//
// Heroes get a dedicated single-tap path (deployHeroSlotOnce) that mirrors
// HeroManager.deploySingleHero. Without this branch, a hero whose main-
// phase drop silently failed would be retried with the troop-style
// TapTriple-along-line loop below - which spreads taps across the line and
// is wrong for a single hero drop.
//
// Defense-in-depth: a Siege slot that ARIVED here is a bug — DeploySiege
// in HeroManager owns sieges end-to-end and now always marks the slot
// deployed via slot.UnitName on success. Skipping here avoids the
// 12-48 wasted taps the live test showed (3 deploySlot retries × ~4
// TapTriple batches × 3 taps = ~36 wasted taps per attack when the
// empty-check returned false even though the siege was already down).
//
// isEventTroop signals the formula lookup path: event troops are
// typically not in the strategy YAML, so we don't second-guess their
// unit-name spelling when consulting the formula.
//
// Reconcile-after-pass: the previous version used a mid-batch single-
// frame empty-check that was too eager (a transient frame mid-burst can
// false-positive). The new path lets fireTapsBatched run all the
// planned taps and reconciles AFTER the pass with both live OCR and a
// visual empty check. Any troops still on the bar trigger a top-up
// re-fire on the same line, guaranteeing the slot ends empty.
func (sw *Sweeper) deploySlot(slot *TrackedSlot, count int, isEventTroop bool) bool {
	if slot.Category == "Hero" {
		return sw.deployHeroSlotOnce(slot)
	}

	// Siege defense-in-depth: see comment above. If we somehow got
	// here with an undeployed siege slot, do NOT fire taps — they're
	// guaranteed wasted because DeploySiege already attempted (and
	// either succeeded silently or already threw a bad harvest).
	// Exception: event troops. A seasonal/bonus card that got
	// fallback-labeled "siege machine" is NOT owned by DeploySiege —
	// the strategy never declared it, so nobody tried to deploy it.
	// isEventTroop=true means we must place it like a normal card.
	if slot.Category == "Siege" && !isEventTroop {
		sw.logger.Warn().
			Int("x", slot.X).
			Str("unit", slot.UnitName).
			Msg("sweep: siege slot arrived undeployed; DeploySiege owns these — skipping to avoid wasted taps")
		return false
	}

	// Resolve the line we're going to deploy along. Formula wins for
	// event troops (and the per-unit entry for regular slots when the
	// user authored one) so the user's pin survives a sweep retry.
	p1, p2, ok := sw.resolveSweepLine(slot, isEventTroop)
	if !ok {
		sw.logger.Warn().Int("x", slot.X).Msg("sweep: no line available (no formula, no red-zone line)")
		return false
	}

	// Live-OCR pre-deploy. Prefer live count over the cached map
	// passed from the orchestrator AND over the heuristic `count`
	// that the legacy code used. When OCR fails AND the slot is
	// visually empty, the slot is genuinely done — no taps needed.
	livePre, visualEmptyPre := sw.sweepLiveCount(slot)
	if visualEmptyPre && livePre <= 0 {
		slot.IsEmpty = true
		sw.logger.Info().
			Str("unit", slot.UnitName).
			Bool("event_troop", isEventTroop).
			Msg("sweep: slot already empty on fresh capture; marking deployed")
		return true
	}
	if livePre > 0 {
		count = padCount(livePre)
	}
	if entry, ok := sw.eventFormulaEntry(slot); ok && entry.Count > 0 {
		// Explicit formula count wins (the user authored this).
		count = entry.Count
	}
	if count <= 0 {
		count = 1
	}

	// 8 rounds: each round makes progress even when OCR is down (fallback
	// taps), so a visually-full slot that OCR keeps misreading still gets
	// drained instead of being abandoned at the old 3-round budget.
	// Each round fires ONLY the remaining count (count is updated from the
	// live read), so a 50-unit slot fires 51→37→16→3 instead of re-firing
	// the full 51 on every attempt (the old 260-tap waste).
	maxAttempts := 8
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if count <= 0 {
			count = 3
		}
		// Select slot
		sw.executor.TapSlot(slot, 4)
		// 150ms matches the orchestrator + hero-slot settle floor.
		// The previous 35ms was too short — CoC's slot-selection
		// animation can run longer than that on slow shells or
		// immediately after a prior phase's last deploy, and the
		// first fireTapsBatched tap would land on the previous
		// unit-type cursor instead of the intended retry.
		sw.executor.HumanSleep(150, 30)

		sw.logger.Info().
			Int("x", slot.X).
			Str("unit", slot.UnitName).
			Int("count", count).
			Interface("p1", p1).
			Interface("p2", p2).
			Bool("event_troop", isEventTroop).
			Msg("sweep deploying")

		// Deploy the correct number of troops. fireTapsBatched bails
		// early ONLY when BOTH live OCR AND visual empty agree, so we
		// don't false-positive on a transient frame mid-burst while
		// still saving taps when the slot genuinely emptied. Event
		// troops (and any slot >= 12 units) skip the per-batch screencap
		// — large dumps need speed, and the reconcile below re-checks
		// the slot anyway.
		bailedEarly := sw.fireTapsBatched(slot, p1, p2, count, isEventTroop || count >= 12, isEventTroop)
		if bailedEarly {
			slot.IsEmpty = true
			sw.logger.Info().
				Str("unit", slot.UnitName).
				Int("attempt", attempt+1).
				Int("requested", count).
				Msg("sweep: mid-batch genuine-empty bail; slot drained")
			return true
		}

		// ─── Reconcile after pass ────────────────────────────────
		sw.executor.HumanSleep(150, 30)
		liveAfter, visualAfter := sw.sweepLiveCount(slot)
		if liveAfter <= 0 && visualAfter {
			slot.IsEmpty = true
			sw.logger.Info().
				Str("unit", slot.UnitName).
				Int("attempt", attempt+1).
				Int("fired", count).
				Msg("sweep reconciled slot empty; deploy complete")
			return true
		}
		// Next round fires exactly what's left. visualAfter=true with
		// live OCR=0 (or vice versa) still means "not done" — keep the
		// remaining as the fire target so we converge.
		if liveAfter > 0 {
			count = padCount(liveAfter)
		} else {
			// visual non-empty but live OCR returned 0 — could be a
			// queued CC troop, a transient frame, OR (critically) OCR
			// that just keeps failing. Fire a small fallback batch so
			// progress is always made; the next attempt re-checks from
			// a fresh screen.
			count = 3
		}
		sw.logger.Info().
			Str("unit", slot.UnitName).
			Int("attempt", attempt+1).
			Int("remaining_read", liveAfter).
			Int("next_fire", count).
			Bool("visual_nonempty", !visualAfter).
			Msg("sweep reconcile: slot still non-empty; re-firing remaining")
		time.Sleep(200 * time.Millisecond)
	}

	sw.logger.Warn().
		Str("unit", slot.UnitName).
		Int("attempts", maxAttempts).
		Msg("sweep reconcile exhausted; slot still non-empty")
	return false
}

// fireTapsBatched fires up to `count` taps distributed along (p1,p2)
// in batches of 3 (matching the legacy TapTriple shape) AND exits
// early when BOTH the live OCR count reads 0 AND the visual empty
// check agrees — so we never tap past the real count, but we ALSO
// never false-positive on a transient empty frame mid-burst.
// fast skips the per-batch screencap for event troops and large slots
// (they can hold 50+ units and the caller's reconcile loop re-checks).
// humanPace applies the slower human-like cadence; it is enabled for
// event troops so bonus dumps read as rapid human spamming instead of
// a machine.
// Returns true when the function bailed early because the slot became
// genuinely empty mid-pass.
func (sw *Sweeper) fireTapsBatched(slot *TrackedSlot, p1, p2 image.Point, count int, fast, humanPace bool) bool {
	sw.lineForward = !sw.lineForward
	if !sw.lineForward {
		p1, p2 = p2, p1
	}
	for i := 0; i < count; i += 3 {
		batchSize := 3
		if i+3 > count {
			batchSize = count - i
		}
		pct := float64(i) / float64(count)
		tx := p1.X + int(float64(p2.X-p1.X)*pct)
		ty := p1.Y + int(float64(p2.Y-p1.Y)*pct)
		jitter := 8
		tx += (i%5 - 2) * jitter
		ty += (i%3 - 1) * jitter
		if batchSize == 3 {
			sw.executor.client.TapTriple(tx, ty, 12.0, tx+5, ty+3, 12.0, tx-3, ty+6, 12.0)
		} else if batchSize == 2 {
			sw.executor.client.TapTriple(tx, ty, 12.0, tx+5, ty+3, 12.0, tx, ty, 12.0)
		} else {
			sw.executor.client.TapTriple(tx, ty, 12.0, tx, ty, 12.0, tx, ty, 12.0)
		}

		// Fast path still needs a human cadence — 50ms between batches is
		// robotic and detectable. Slow it to ~160-210ms with variance so
		// bonus-troop dumps read as rapid human spamming, not a machine.
		// The non-fast path keeps its tighter 50ms floor (the reconcile
		// screencap already paces it naturally).
		if humanPace {
			sw.executor.HumanSleep(160, 50)
		} else {
			sw.executor.HumanSleep(50, 15)
		}

		// Smart mid-batch bail. Only exit early when BOTH conditions
		// agree the slot is currently empty: a transient empty frame
		// in the troop bar (very common mid-burst) won't satisfy both,
		// so we keep firing. Cost: one ~150ms screencap per 3-tap batch.
		if !fast && i+batchSize < count {
			live, visualEmpty := captureSlotLiveCount(
				sw.executor, sw.troopCounter, slot,
				sw.slotManager.GetBarY(), sw.w, sw.h,
			)
			if live <= 0 && visualEmpty {
				slot.IsEmpty = true
				return true
			}
		}
	}
	return false
}

// sweepLiveCount is a thin shim to the shared captureSlotLiveCount
// helper in live_count.go. The single-source-of-truth helper ensures
// the live-OCR reconcile semantics stay identical across
// HeroManager / Sweeper / Verifier.
func (sw *Sweeper) sweepLiveCount(slot *TrackedSlot) (int, bool) {
	return captureSlotLiveCount(
		sw.executor,
		sw.troopCounter,
		slot,
		sw.slotManager.GetBarY(),
		sw.w, sw.h,
	)
}

// padCount returns n+1 when n >= 6 (absorbs single-digit OCR under-reads
// like "x9" misread for "x10"); returns n unchanged for small counts.
func padCount(n int) int {
	const padFloor = 6
	if n >= padFloor {
		return n + 1
	}
	return n
}

// resolveSweepLine returns the deploy line for the sweep retry. Formula
// wins over the dynamic red-zone line. The formula may carry a "point"
// entry (we synthesize a degenerate line) or a "line"/"lines" entry
// (we use P1+P2 of the first line). Falls through to deployLine.Points
// when no formula entry applies.
func (sw *Sweeper) resolveSweepLine(slot *TrackedSlot, isEventTroop bool) (image.Point, image.Point, bool) {
	if entry, ok := sw.eventFormulaEntry(slot); ok {
		switch {
		case entry.IsPoint() && entry.P != nil:
			pt := entry.P.Image()
			return pt, pt, true
		case entry.IsLine() && entry.P1 != nil && entry.P2 != nil:
			return entry.P1.Image(), entry.P2.Image(), true
		case entry.IsLines() && len(entry.Lines) > 0:
			lp := entry.Lines[0]
			return lp.P1.Image(), lp.P2.Image(), true
		}
	}
	if isEventTroop {
		// Event troop without a formula pin: fall through to the deploy
		// line below so the card still gets placed. Earlier behavior
		// refused to sweep non-pinned event troops, leaving them on the
		// bar — but the user wants ALL bonus troops placed.
	}
	if len(sw.deployLine.Points) < 2 {
		return image.Point{}, image.Point{}, false
	}
	p1 := sw.deployLine.Points[0]
	p2 := sw.deployLine.Points[len(sw.deployLine.Points)-1]
	return p1, p2, true
}

// deployHeroSlotOnce performs a hero sweep retry using a tight cluster
// of 3 device taps at +/- 3 px around a random deploy-line point. This
// mirrors the multi-tap pattern in HeroManager.deploySingleHero so the
// retry matches the initial drop behavior.
//
// We use the same delta-based verify as the initial drop: capture
// pre-ratio BEFORE the tap (so the slot is not yet highlighted),
// drop, settle, capture post-ratio, and accept if pre - post >= 0.15.
// Absolute thresholds are unreliable across hero icons (BK baseline
// 0.67 already exceeds the previous 0.40 cutoff), but the delta is
// consistent because all heroes transition to a ~0.10-0.30 cooldown
// silhouette on success.
func (sw *Sweeper) deployHeroSlotOnce(slot *TrackedSlot) bool {
	pts := sw.deployLine.Points
	if len(pts) == 0 {
		sw.logger.Warn().Int("x", slot.X).Msg("hero sweep: no deploy line points available")
		return false
	}

	// 0. Capture pre-retry baseline BEFORE any tap.
	var preRatio float64
	var capturedPre bool
	if preScreen, err := sw.executor.CaptureFresh(); err == nil {
		preRatio = GetSlotActivityRatioStatic(preScreen, slot.X, slot.Y, sw.w)
		preScreen.Close()
		capturedPre = true
	}

	// Pick a random point along the chosen deploy line so flankers
	// don't stack on the same pixel.
	pt := pts[rand.Intn(len(pts))]

	sw.executor.TapSlot(slot, 4)
	sw.executor.HumanSleep(150, 30)

	// Tight cluster of 3 device taps (+/- 3 px). The single TapFast
	// retry regressed BK / GW / Queen (live observed post_ratio 0.69,
	// 0.59, 0.70 = same as pre-deploy); multi-tap matches the working
	// initial-drop pattern.
	j1 := sw.executor.addJitter(pt, 3)
	j2 := sw.executor.addJitter(pt, 3)
	j3 := sw.executor.addJitter(pt, 3)
	sw.executor.client.TapTriple(j1.X, j1.Y, 12.0, j2.X, j2.Y, 12.0, j3.X, j3.Y, 12.0)

	sw.executor.HumanSleep(300, 40)

	var postRatio float64
	var capturedPost bool
	if postScreen, err := sw.executor.CaptureFresh(); err == nil {
		postRatio = GetSlotActivityRatioStatic(postScreen, slot.X, slot.Y, sw.w)
		postScreen.Close()
		capturedPost = true
	}

	const heroDroppedDelta = 0.15
	if capturedPre && capturedPost {
		delta := preRatio - postRatio
		if delta < heroDroppedDelta {
			sw.logger.Warn().
				Int("x", slot.X).
				Str("unit", slot.UnitName).
				Float64("pre_ratio", preRatio).
				Float64("post_ratio", postRatio).
				Float64("delta", delta).
				Msg("hero sweep retry did not visibly transition slot; will be marked failed")
			return false
		}
		sw.logger.Info().
			Int("x", slot.X).
			Str("unit", slot.UnitName).
			Float64("pre_ratio", preRatio).
			Float64("post_ratio", postRatio).
			Float64("delta", delta).
			Msg("hero sweep deploy succeeded")
		return true
	}

	sw.logger.Info().
		Int("x", slot.X).
		Str("unit", slot.UnitName).
		Msg("hero sweep deploy (capture unavailable; trusting tap)")
	return true
}
