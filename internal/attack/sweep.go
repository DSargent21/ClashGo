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
	executor    *TapExecutor
	slotManager *SlotManager
	pCfg        PrecisionConfig
	deployLine  DeployLine
	formula     *formula.Formula
	w, h        int
	logger      zerolog.Logger
}

// NewSweeper creates a new sweeper. formula may be nil; when non-nil,
// the sweeper consults it FIRST so user-pinned _event_troop / _event_spell
// coordinates win over the dynamic red-zone fallback. Without this, even
// with a per-unit formula authored, the sweep phase re-tapped along the
// old red-zone line, scattering event troops to the wrong side.
func NewSweeper(
	executor *TapExecutor,
	slotManager *SlotManager,
	pCfg PrecisionConfig,
	deployLine DeployLine,
	w, h int,
	f *formula.Formula,
	logger zerolog.Logger,
) *Sweeper {
	return &Sweeper{
		executor:    executor,
		slotManager: slotManager,
		pCfg:        pCfg,
		deployLine:  deployLine,
		formula:     f,
		w:           w,
		h:           h,
		logger:      logger.With().Str("component", "sweeper").Logger(),
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

	// Also sweep event troops not in strategy
	eventTroops := sw.slotManager.GetEventTroops(strategyUnitNames)
	for _, slot := range eventTroops {
		// Capture FRESH screen for each slot check
		freshScreen, err := sw.executor.CaptureFresh()
		if err != nil {
			sw.logger.Warn().Err(err).Msg("failed to capture fresh screen for sweep")
			continue
		}

		empty := isSlotEmptyStatic(freshScreen, slot.X, slot.Y, sw.w, sw.h)
		freshScreen.Close()

		if empty {
			slot.IsEmpty = true
			continue
		}

		// Default the event troop count to 1 if detection gave nothing
		// AND the formula gave no count. The previous 60 default re-tapped
		// empty slots 20 times AFTER the slot emptied — the "retapping
		// empty spaces" bug the user reported on the live attack.
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
			Msg("found event troop during sweep")

		success := sw.deploySlot(slot, count, true)
		if success {
			sw.slotManager.MarkDeployed(slot.UnitName)
			deployedCount++
		} else {
			sw.slotManager.MarkFailed(slot.UnitName)
		}
		time.Sleep(60 * time.Millisecond)
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
// Empty-slot guard: between TapTriple batches, capture a fresh screen
// and check `isSlotEmptyStatic`. If the slot emptied, mark `IsEmpty`
// + return success IMMEDIATELY so we don't fire extra troops into a
// dead card. This is the surgical fix for the "retapping empty spaces"
// symptom — the previous loop ran the full `count` taps unconditionally
// even when the slot card count already hit zero on attempt 1.
//
// isEventTroop signals the formula lookup path: event troops are
// typically not in the strategy YAML, so we don't second-guess their
// unit-name spelling when consulting the formula.
func (sw *Sweeper) deploySlot(slot *TrackedSlot, count int, isEventTroop bool) bool {
	if slot.Category == "Hero" {
		return sw.deployHeroSlotOnce(slot)
	}

	// Resolve the line we're going to deploy along. Formula wins for
	// event troops (and the per-unit entry for regular slots when the
	// user authored one) so the user's pin survives a sweep retry.
	p1, p2, ok := sw.resolveSweepLine(slot, isEventTroop)
	if !ok {
		sw.logger.Warn().Int("x", slot.X).Msg("sweep: no line available (no formula, no red-zone line)")
		return false
	}

	if entry, ok := sw.eventFormulaEntry(slot); ok && entry.Count > 0 {
		count = entry.Count
	}

	maxAttempts := 3
	for attempt := 0; attempt < maxAttempts; attempt++ {
		// Select slot
		sw.executor.TapSlot(slot, 4)
		sw.executor.HumanSleep(35, 10)

		sw.logger.Info().
			Int("x", slot.X).
			Str("unit", slot.UnitName).
			Int("count", count).
			Interface("p1", p1).
			Interface("p2", p2).
			Bool("event_troop", isEventTroop).
			Msg("sweep deploying")

		// Deploy the correct number of troops. BATCH exits early as
		// soon as the slot card empties — no further taps fire into
		// dead space.
		fired := sw.fireTapsBatched(slot, p1, p2, count)

		// If we bailed out because the slot is empty, the deploy is
		// already successful — return immediately to skip the
		// re-select + second attempt.
		if fired < 0 {
			sw.logger.Info().
				Int("x", slot.X).
				Int("attempt", attempt+1).
				Msg("sweep detected empty mid-batch; slot emptied cleanly")
			return true
		}

		time.Sleep(300 * time.Millisecond)

		// Final verify before next retry attempt.
		checkScreen, err := sw.executor.CaptureFresh()
		if err == nil {
			empty := isSlotEmptyStatic(checkScreen, slot.X, slot.Y, sw.w, sw.h)
			checkScreen.Close()
			if empty {
				sw.logger.Info().Int("x", slot.X).Int("attempt", attempt+1).Msg("swept slot empty")
				return true
			}
		}

		// Re-select for next attempt
		sw.executor.TapSlot(slot, 4)
		sw.executor.HumanSleep(50, 10)
	}

	return false
}

// fireTapsBatched fires up to `count` taps distributed along (p1,p2)
// in batches of 3 (matching the legacy TapTriple shape). Returns the
// remaining count NOT fired. Returns -1 when the slot emptied mid-batch
// (early-exit sentinel) so the caller can short-circuit the retry loop.
func (sw *Sweeper) fireTapsBatched(slot *TrackedSlot, p1, p2 image.Point, count int) int {
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
		sw.executor.HumanSleep(50, 15)

		// Empty-slot check. If the slot card emptied on this batch,
		// bail out — no further taps fire into dead space.
		if i+batchSize < count {
			checkScreen, err := sw.executor.CaptureFresh()
			if err == nil {
				empty := isSlotEmptyStatic(checkScreen, slot.X, slot.Y, sw.w, sw.h)
				checkScreen.Close()
				if empty {
					slot.IsEmpty = true
					return -1
				}
			}
		}
	}
	return count
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
		// Event troop without formula entry: don't even *try* to
		// sweep with the red-zone line for non-hero categories — the
		// previous behavior scattered event troops along the player's
		// chosen side which the user explicitly didn't want. The
		// orchestrator pass moved the responsibility to the formula
		// author's "I want my event troops here" pin.
		return image.Point{}, image.Point{}, !isEventTroop
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
	sw.executor.HumanSleep(350, 60)

	// Tight cluster of 3 device taps (+/- 3 px). The single TapFast
	// retry regressed BK / GW / Queen (live observed post_ratio 0.69,
	// 0.59, 0.70 = same as pre-deploy); multi-tap matches the working
	// initial-drop pattern.
	j1 := sw.executor.addJitter(pt, 3)
	j2 := sw.executor.addJitter(pt, 3)
	j3 := sw.executor.addJitter(pt, 3)
	sw.executor.client.TapTriple(j1.X, j1.Y, 12.0, j2.X, j2.Y, 12.0, j3.X, j3.Y, 12.0)

	sw.executor.HumanSleep(350, 50)

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
