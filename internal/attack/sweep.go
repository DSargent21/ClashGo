package attack

import (
	"image"
	"math"
	"strings"
	"time"

	"github.com/Ducky705/ClashGO/pkg/formula"
	"github.com/rs/zerolog"
)

// maxIdenticalObservations is how many consecutive reconcile rounds may observe
// the exact same card state after taps went out before the slot is written off
// as spent/locked. Two is the bound: the round that fired the card's resolved
// count, plus at most one top-up that changed nothing measurable. It mirrors the
// existing trusted-zero rule (zeroOCRStreak >= 2) and extends it to rounds where
// the count label could not be resolved at all.
//
// Live (2026-09-24, "siege machine" x=134): the reader reported "count label
// present but not readable" on every post-fire frame, so the trusted-zero rule
// never fired (a blind read must not write a slot off), and the reconcile loop
// burned all 8 attempts on blind 3-tap batches at a card whose state never
// changed — 41 taps across two outer rounds.
const maxIdenticalObservations = 2

// sweepSpentLocked decides the stall write-off: the card produced the same
// observation across consecutive rounds of taps AND at least one of those rounds
// fired a count the card was believed to hold. Both halves matter — identical
// observations alone are also what a blind reader looks like on a card that still
// holds troops, and abandoning that card is the "Balloon and Electro Dragon were
// reported as placed but never left the bar" bug (run13, 2026-09-22).
func sweepSpentLocked(obsStreak int, firedResolvedOnce bool) bool {
	return obsStreak >= maxIdenticalObservations && firedResolvedOnce
}

// sweepStallOutcome is how a stalled card closes out.
type sweepStallOutcome int

const (
	// stallDeploy: the frame independently shows the slot empty.
	stallDeploy sweepStallOutcome = iota
	// stallKeepFiring: an event-troop card that still holds units and has
	// volley budget left.
	stallKeepFiring
	// stallFail: the taps went out and the card did not drain. Report FAILED.
	stallFail
)

// sweepStallOutcomeFor decides what a card is that stopped responding to taps,
// given the frame's own verdict on the slot and the event-troop volley budget.
//
// `visuallyEmpty` is the ONLY condition that may close a card out as deployed,
// and it is the same measurement the verifier uses (slotActivity < 0.08), so the
// sweep can never mark deployed what the verifier would count as surviving
// content. Without it a stalled card was recorded as deployed on the strength of
// its taps not changing anything — which is equally what a card that never took
// the taps looks like (live 2026-09-25, TopLeft: the sweep's line projected to
// x=-11, the taps landed off-screen, and the card was written off with x8 still
// on it). A stalled non-event card is now reported FAILED; an event card keeps
// firing volleys until its budget is spent, then FAILED too.
func sweepStallOutcomeFor(isEventTroop, visuallyEmpty bool, stallVolleys int) sweepStallOutcome {
	if visuallyEmpty {
		return stallDeploy
	}
	if isEventTroop && eventStallVolleysLeft(stallVolleys) {
		return stallKeepFiring
	}
	return stallFail
}

// slotReadObservation is everything one reconcile round could measure about a
// card. Comparing two of these is how the sweeper tells "the taps are working"
// (the reading changes) from "the taps are no longer landing on a live card"
// (the reading is identical), which is the only spent/locked signal available
// when the count label cannot be read.
type slotReadObservation struct {
	trusted       bool
	count         int
	visuallyEmpty bool
}

// eventSweepContinue decides whether an event-troop sweep may fire another
// round, and with what count.
//
// The first round fires the caller-resolved count (the orchestrator's cached
// troopCounts, or the formula's _event_troop entry). Later rounds may NOT: no
// code path re-measures the card for them, so re-resolving hands back the very
// number the previous round already spent. Live (2026-09-24, "siege machine"
// x=134): round 1 fired the formula's 18 plus blind top-ups, the slot still read
// non-empty, and round 2 resolved 18 again and fired it a second time — a card
// the sweep had never actually measured took ~41 taps.
//
// So an extra round is admissible only on fresh, trusted evidence that units
// remain. With no such read the card is unmeasurable from here on: spent, locked,
// or its count label unresolvable — all of which mean more taps cannot help.
func eventSweepContinue(live int, trusted bool) (int, bool) {
	if trusted && live > 0 {
		return padCount(live), true
	}
	return 0, false
}

// maxEventStallVolleys bounds how many full volleys an event-troop sweep fires
// after its observations stop changing while the card is still not empty. Once
// they are spent the slot is reported FAILED, never deployed.
const maxEventStallVolleys = 3

// eventVolleyTarget returns the tap count for an event-troop volley.
//
// `resolved` is what the card was measured to HOLD (the orchestrator's bar read
// at the start of the battle, or a formula pin) and `live` is a fresh read of
// what is LEFT. Firing the smaller of the two is how a live run left troops on
// the bar: on 2026-09-24 the event card at x=137 was resolved at 40, a mid-sweep
// read of the half-drained card said 26, the volley fired 27, and the card ended
// holding 9 while the run reported "100% Deployed". A troop volley can fire MORE
// than the card holds at no cost — taps onto an empty card drop nothing — while
// firing less leaves troops behind, so the target is the resolved count and a
// trusted live read may only raise it.
func eventVolleyTarget(resolved, live int, trustedLive bool) int {
	if trustedLive && live > 0 {
		if padded := padCount(live); padded > resolved {
			return padded
		}
	}
	if resolved > 0 {
		return resolved
	}
	return blindBatchTaps
}

// eventStallVolleysLeft reports whether an event-troop reconcile that has
// watched the same card state for `held` rounds may fire another full volley.
//
// An event-troop card is never written off as DEPLOYED on a stall. Taps at a
// card that cannot be read back are the only way its troops get placed, and the
// alternative — calling it "spent or locked" — is how a card with 9 troops still
// on it was reported as fully deployed (live run 2026-09-24, x=137). When the
// volleys are spent the slot is reported FAILED, which is the honest verdict:
// the taps went out and the card did not drain.
func eventStallVolleysLeft(held int) bool {
	return held < maxEventStallVolleys
}

// spellBatchSpacingRef is the minimum gap between two casts of a swept spell,
// in reference px — the same half-a-cast's-reach yardstick the spell deployer
// enforces (see minCastSpacingRef) and the post-run report measures with.
const spellBatchSpacingRef = 37.7

// retryClearanceRef is added to a retry's sideways displacement. The displacement
// is the spacing the post-run report itself calls "the same ground", and rounding
// the perpendicular offset onto whole pixels can land it a fraction under that
// bar: a live sweep retry displaced exactly one spacing measured 49.x px between
// attempts and was reported as OVERLAPPED, which is the report telling the truth
// about a computed value that was one rounding step from correct. A few extra
// reference px make a retry clear the bar instead of touching it.
const retryClearanceRef = 4.0

// Sweeper handles final sweep to catch undeployed troops.
type Sweeper struct {
	executor     *TapExecutor
	slotManager  *SlotManager
	pCfg         PrecisionConfig
	deployLine   DeployLine
	zone         RedZone
	formula      *formula.Formula
	troopCounter *TroopCounter // optional; enables live-OCR count + reconcile
	autoEvent    bool          // deploy bonus/event troops not in strategy (default on)
	pinnedGround bool          // deployLine came from the user's precision_config pins
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

// SetRedZone wires the battle's detected red no-deploy line so a swept hero
// retry aims at ground the game accepts instead of a stale pinned line's
// midpoint (see hero_ground.go; live run10 the whole hero phase was refused on
// that midpoint and the sweep had to re-do it). A zero-value zone is legal and
// means "unchecked".
func (sw *Sweeper) SetRedZone(z RedZone) {
	sw.zone = z
}

// SetPinnedGround marks the deploy line as the user's pin, which makes it
// authoritative for every sweep tap (see HeroManager.SetPinnedGround).
func (sw *Sweeper) SetPinnedGround(v bool) { sw.pinnedGround = v }

// heroDropCandidates mirrors HeroManager.heroDropCandidates: the strategy
// formula's ground for this hero first (the same ground this battle's troop pass
// deployed on), then the zone-filtered deploy line. See hero_ground.go.
func (sw *Sweeper) heroDropCandidates(slot *TrackedSlot) []image.Point {
	var cands []image.Point
	if entry, ok := sw.formula.LookUp(slot.UnitName); ok {
		cands = append(cands, heroFormulaCandidates(entry, sw.h)...)
	}
	cands = append(cands, heroGroundCandidates(sw.deployLine.Points, sw.h, sw.zone)...)
	// The tile the siege machine is standing on is not hero ground: a placement
	// tap there is refused by the game and is the second siege tap the user asked
	// us never to fire. See HeroManager.heroDropCandidates for the live numbers.
	if _, ok := sw.executor.SiegeGround(); ok {
		clear := make([]image.Point, 0, len(cands))
		for _, p := range cands {
			if sw.executor.clearsSiege(p) {
				clear = append(clear, p)
			}
		}
		if len(clear) > 0 {
			cands = clear
		}
	}
	// Retry ground for the rescue: step outward from the ground that failed (the
	// hero phase's pick when this sweep was triggered by it) rather than re-tap
	// it. Field taps only — the sweep's own card tap is already spent.
	if len(cands) > 0 {
		cands = append(cands, outwardHeroLadder(cands[0], sw.deployLine.Points, sw.w, sw.h, maxHeroGroundCandidates-1)...)
	}
	if len(cands) > maxHeroGroundCandidates {
		cands = cands[:maxHeroGroundCandidates]
	}
	return cands
}

// fireHeroFieldTap sends one hero field tap at pt (+/- 3 px). No card tap is
// involved, so it can never spend a hero's 2-tap card budget. Goes through
// TapUnitField so the siege machine's tile stays untouched.
func (sw *Sweeper) fireHeroFieldTap(pt image.Point) {
	j := sw.executor.addJitter(pt, 3)
	sw.executor.TapUnitField(j, 12.0)
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
		// Battle-timer guard: never start a new slot's taps once the
		// deploy budget is gone (see DeployBudget). The reconcile loops
		// below also check per round, but this keeps us from even
		// beginning a doomed slot.
		if sw.executor.DeployBudgetExhausted() {
			sw.logger.Warn().Msg("sweep: deploy budget exhausted; abandoning remaining slots")
			break
		}
		// Skip siege slots (handled separately)
		if slot.Category == "Siege" {
			continue
		}
		// Skip fallback-labeled slots here — the label is stale (old
		// manual_labels.json army) and can masquerade as a hero ("archer
		// queen") that template matching couldn't confirm. Routing it
		// through the hero path fails and marks it Failed, which then
		// hides the real bonus troop from the event pass below. The
		// event pass (GetEventTroops) treats fallback-labeled slots as
		// event troops and deploys them along the line.
		if slot.FallbackLabeled {
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

		// Get troop count for this slot. Zero means UNRESOLVED, not "one" —
		// deploySlot re-reads the card live and, if that also fails, fires a
		// bounded blind batch (blindBatchTaps). The old 12 default silently
		// over-deployed when the slot only held 5 troops; the later "1"
		// default silently under-deployed a full card and left troops behind.
		count := troopCounts[slot.X]
		if entry, ok := sw.eventFormulaEntry(slot); ok && entry.Count > 0 {
			count = entry.Count
		}

		sw.logger.Info().
			Int("x", slot.X).
			Str("unit", slot.UnitName).
			Str("category", slot.Category).
			Int("count", count).
			Bool("count_resolved", count > 0).
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
			// unreadableRounds counts the outer rounds spent firing at a card
			// whose count could not be read back while it still showed contents.
			unreadableRounds := 0
			for round := 0; round < maxRounds; round++ {
				// Battle-timer guard between rounds: a stuck slot (spent
				// card reading visually non-empty, OCR down) must not burn
				// the whole 6-round budget re-firing into an ended battle.
				if sw.executor.DeployBudgetExhausted() {
					sw.logger.Warn().Str("unit", slot.UnitName).Msg("event troop sweep: deploy budget exhausted; abandoning slot")
					break
				}
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
					sw.slotManager.MarkSlotDeployed(slot)
					placed = true
					break
				}

				// Default the event troop count to 1 if detection gave
				// nothing AND the formula gave no count; live OCR at deploy
				// time will raise it if the card actually holds more.
				// Zero means unresolved; deploySlot drives the real read (see the
				// main sweep loop above).
				count := troopCounts[slot.X]
				if entry, ok := sw.eventFormulaEntry(slot); ok && entry.Count > 0 {
					count = entry.Count
				}

				// Extra rounds need fresh evidence. Nothing here re-measures the
				// card, so a second round would re-resolve the same static count
				// and re-fire it verbatim (see eventSweepContinue). Ask the card
				// directly: only a trusted live read justifies another round.
				if round > 0 {
					live, trustedLive, visuallyEmpty := sw.sweepLiveCount(slot)
					if visuallyEmpty && (!trustedLive || live <= 0) {
						slot.IsEmpty = true
						sw.slotManager.MarkSlotDeployed(slot)
						placed = true
						sw.logger.Info().
							Int("x", slot.X).
							Str("unit", slot.UnitName).
							Int("round", round+1).
							Bool("count_read_trusted", trustedLive).
							Msg("event troop sweep: card verified empty")
						break
					}
					nextCount, cont := eventSweepContinue(live, trustedLive)
					if cont {
						count = nextCount
					} else {
						// Not readable, and the card is not visually empty either. That
						// is NOT evidence the card is spent — it is the case where the
						// card still holds what the resolved count said it held. Fire
						// that count again, for a bounded number of rounds, and report
						// the slot FAILED if the card never drains: calling it
						// "deployed" here is exactly how a live run left 9 event troops
						// on the bar under a 100%-deployed verdict.
						unreadableRounds++
						if unreadableRounds > maxEventStallVolleys {
							sw.slotManager.MarkSlotFailed(slot)
							sw.logger.Warn().
								Int("x", slot.X).
								Str("unit", slot.UnitName).
								Int("remaining_read", live).
								Bool("count_read_trusted", trustedLive).
								Msg("event troop sweep: card never readable and never empty; reporting the slot failed")
							break
						}
						count = eventVolleyTarget(count, live, trustedLive)
						sw.logger.Info().
							Int("x", slot.X).
							Str("unit", slot.UnitName).
							Int("round", round+1).
							Int("next_fire", count).
							Bool("count_read_trusted", trustedLive).
							Msg("event troop sweep: no readable count but the card is not empty; firing the resolved count again")
					}
				}

				sw.logger.Info().
					Int("x", slot.X).
					Str("unit", slot.UnitName).
					Int("count", count).
					Bool("count_resolved", count > 0).
					Int("round", round+1).
					Msg("found event troop during sweep")

				success := sw.deploySlot(slot, count, true)
				if success {
					sw.slotManager.MarkSlotDeployed(slot)
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
				sw.slotManager.MarkSlotFailed(slot)
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
	// Hero path only for slots template-matched as a real hero (or a
	// strategy-declared hero). Fallback-labeled "heroes" are bonus
	// troops wearing a stale manual label — they must be dumped along
	// the line like a troop, not single-tapped like a hero.
	if slot.Category == "Hero" && !slot.FallbackLabeled {
		return sw.deployHeroSlotOnce(slot)
	}

	// The siege machine took its one placement tap (see
	// TrackedSlot.SiegePlaced). Nothing about it is undeployed, and every tap
	// this path fires would land on the machine standing on the field: a swept
	// siege retry is not a rescue, it is the second tap the user reported
	// destroying the machine. Close the slot out with no taps at all.
	if slot.SiegePlaced {
		sw.logger.Warn().
			Int("x", slot.X).
			Str("unit", slot.UnitName).
			Msg("sweep: the siege machine already took its one placement tap; marking the slot closed without tapping")
		return true
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

	// Nothing to place here.
	//
	// GetEventTroops deliberately includes slots with no unit name, because a
	// seasonal card the templates cannot identify is still a card the user wants
	// placed. The gap is that an EMPTY BAR POSITION looks identical to it, and
	// empty positions are what the manual slot list carries at the right-hand
	// end: live 2026-09-25 (run11) the two positions at x=1048 and x=1148 were
	// swept as event troops — category Spell, no name, no count — and took 6
	// blind taps each in the sweep plus 6 more each in the event pass, 24 taps
	// into empty bar space that belong to no card at all.
	//
	// Evidence a card is really there: a strategy name, a resolved count, or a
	// count badge drawn on it. A card holding units always carries one; empty bar
	// space carries none.
	if !sw.slotHoldsCard(slot, count) {
		slot.IsEmpty = true
		sw.logger.Warn().
			Int("x", slot.X).
			Str("unit", slot.UnitName).
			Str("category", slot.Category).
			Int("count", count).
			Msg("sweep: slot carries no card — no strategy name, no resolved count and no count badge; skipping without tapping")
		return true
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
	// that the legacy code used.
	//
	// The no-op branch requires trusted=true: "the card shows no count while
	// other cards still do" is evidence the card is spent, whereas an
	// unreadable frame produces the same count=0 and means nothing. Treating
	// the latter as empty is how the sweep came to mark undeployed troops
	// deployed.
	livePre, trustedPre, visualEmptyPre := sw.sweepLiveCount(slot)
	if visualEmptyPre && trustedPre && livePre <= 0 {
		slot.IsEmpty = true
		sw.logger.Info().
			Str("unit", slot.UnitName).
			Bool("event_troop", isEventTroop).
			Msg("sweep: slot already empty on fresh capture; marking deployed")
		return true
	}
	// countResolved tracks whether `count` is a real measurement of what the
	// card holds, rather than the blind-batch floor. The spent/locked stall
	// write-off below is only allowed once a resolved count has actually been
	// fired at the card, because that is the only case where "nothing changed"
	// means the card had nothing left to give.
	countResolved := count > 0
	if trustedPre && livePre > 0 {
		count = padCount(livePre)
		countResolved = true
	}
	if entry, ok := sw.eventFormulaEntry(slot); ok && entry.Count > 0 {
		// Explicit formula count wins (the user authored this).
		count = entry.Count
		countResolved = true
	}
	if isEventTroop && countResolved {
		// The event-troop volley is the card's resolved contents, never a
		// smaller mid-drain read (see eventVolleyTarget). volleyFloor keeps that
		// number for the stall rounds below, which have no readable remaining to
		// fire and must not fall back to a 3-tap batch on a card known to hold
		// more.
		count = eventVolleyTarget(count, livePre, trustedPre)
	}
	volleyFloor := count
	if count <= 0 {
		// Nothing measured and nothing authored: fire a bounded blind batch
		// and let the reconcile loop below drive against the visual empty
		// check. See blindBatchTaps.
		count = blindBatchTaps
		countResolved = false
		sw.logger.Warn().
			Str("unit", slot.UnitName).
			Bool("live_read_trusted", trustedPre).
			Int("blind_batch", count).
			Msg("sweep: no readable card count; firing a blind batch and reconciling on the visual check")
	}

	// 8 rounds: each round makes progress even when OCR is down (fallback
	// taps), so a visually-full slot that OCR keeps misreading still gets
	// drained instead of being abandoned at the old 3-round budget.
	// Each round fires ONLY the remaining count (count is updated from the
	// live read), so a 50-unit slot fires 51→37→16→3 instead of re-firing
	// the full 51 on every attempt (the old 260-tap waste).
	//
	// zeroOCRStreak bounds the pathological "never visually empty" case
	// (live: a spent earthquake-spell card routed through the troop sweep
	// reads OCR=0 + visually-busy forever — its cooldown icon never
	// "empties" — and the old code re-fired a blind batch of 3 for 8
	// attempts × 6 outer rounds ≈ 24+ wasted taps into an empty card).
	//
	// The streak now counts only TRUSTED zero reads. That is the whole point
	// of the trusted flag: "a card that holds units renders a count digit, so
	// a zero means spent" is only true when the reader is demonstrably able
	// to read counts on this frame. While the bar is unreadable the sweep
	// falls back to the visual check — the same signal it had before OCR
	// existed — instead of writing the slot off on a blind zero.
	maxAttempts := 8
	// stallVolleys counts the event-troop stall rounds that fired a full volley
	// instead of writing the card off; see eventStallVolleysLeft.
	stallVolleys := 0
	zeroOCRStreak := 0
	// obsStreak counts consecutive rounds that observed the SAME card state
	// after taps went out. It is the spent/locked detector for the rounds
	// where the count label cannot be resolved: the trusted-zero rule below
	// deliberately does not fire on a blind read, and without this bound a
	// card whose label never resolves takes the full 8 attempts of blind
	// batches. Reset whenever a trusted positive count appears, because that
	// is real evidence the card still holds units and "identical to last
	// round" is then just OCR lag.
	obsStreak := 0
	var prevObs slotReadObservation
	// firedResolvedOnce is sticky for the whole reconcile pass: it records
	// whether any round fired a count the card was believed to hold. Without
	// it, a slot whose count could never be read (blind batches only) would be
	// written off by the stall rule below, and a full card would be abandoned
	// with its troops still on the bar.
	firedResolvedOnce := false
	for attempt := 0; attempt < maxAttempts; attempt++ {
		// Battle-timer guard: 8 reconcile attempts exist to survive OCR
		// hiccups, not to re-fire a slot that never empties for the whole
		// battle. Once the deploy budget is gone the slot is abandoned.
		if sw.executor.DeployBudgetExhausted() {
			sw.logger.Warn().
				Str("unit", slot.UnitName).
				Int("attempt", attempt+1).
				Msg("sweep reconcile: deploy budget exhausted; abandoning slot")
			return false
		}
		if count <= 0 {
			count = blindBatchTaps
		}
		// Select slot
		sw.executor.TapSlot(slot, 4)
		// 150ms matches the orchestrator + hero-slot settle floor.
		// The previous 35ms was too short — CoC's slot-selection
		// animation can run longer than that on slow shells or
		// immediately after a prior phase's last deploy, and the
		// first fireTapsBatched tap would land on the previous
		// unit-type cursor instead of the intended retry.
		sw.executor.HumanSleep(heroArmSettleMs, 30)

		// Resolve the line this attempt actually fires along BEFORE logging it.
		// The report reads p1/p2 off this line to reconstruct where the taps
		// went; logging the un-displaced endpoints made a correctly displaced
		// re-fire read as four taps on two points (a 0 px closest pair) on a
		// live run whose taps had in fact been moved.
		fp1, fp2 := p1, p2
		if attempt > 0 && (spellLikeSlot(slot) || isEventTroop) {
			// A retry must cover new ground — for event troops as much as for
			// spells. Live run 2026-09-24: the event card's pass 1 fired 41 taps
			// and pass 2 fired 40 more along the SAME two points, and the card
			// still ended holding 9. Re-firing refused ground is how a volley
			// spends its taps without draining the card.
			// A retry must cover new ground. Re-firing the same two points is
			// how a live run ended up with four earthquake taps whose closest
			// pair was 0 px: the first attempt cast onto both, the second
			// attempt cast onto the same two.
			fp1, fp2 = sw.shiftLineForAttempt(fp1, fp2, attempt)
			sw.logger.Info().
				Str("unit", slot.UnitName).
				Int("attempt", attempt+1).
				Interface("p1", fp1).
				Interface("p2", fp2).
				Msg("sweep retry displaced sideways: this attempt lands beside the previous one")
		}

		sw.logger.Info().
			Int("x", slot.X).
			Str("unit", slot.UnitName).
			Int("count", count).
			Int("attempt", attempt+1).
			Interface("p1", fp1).
			Interface("p2", fp2).
			Bool("event_troop", isEventTroop).
			Msg("sweep deploying")

		// Deploy the correct number of troops. fireTapsBatched bails
		// early ONLY when BOTH live OCR AND visual empty agree, so we
		// don't false-positive on a transient frame mid-burst while
		// still saving taps when the slot genuinely emptied. Event
		// troops (and any slot >= 12 units) skip the per-batch screencap
		// — large dumps need speed, and the reconcile below re-checks
		// the slot anyway.
		if countResolved {
			firedResolvedOnce = true
		}
		bailedEarly := sw.fireTapsBatched(slot, fp1, fp2, count, isEventTroop || count >= 12, isEventTroop)
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
		liveAfter, trustedAfter, visualAfter := sw.sweepLiveCount(slot)
		if visualAfter && (!trustedAfter || liveAfter <= 0) {
			slot.IsEmpty = true
			sw.logger.Info().
				Str("unit", slot.UnitName).
				Int("attempt", attempt+1).
				Int("fired", count).
				Bool("count_read_trusted", trustedAfter).
				Msg("sweep reconciled slot empty; deploy complete")
			return true
		}
		// Observation fingerprint for the stall detector below. Compared
		// AFTER the branch that reads a trusted positive count, so real
		// contents reset the streak instead of counting as "no change".
		obs := slotReadObservation{trusted: trustedAfter, count: liveAfter, visuallyEmpty: visualAfter}
		if obs == prevObs {
			obsStreak++
		} else {
			obsStreak = 1
		}
		prevObs = obs

		// Next round fires exactly what's left. visualAfter=true with
		// live OCR=0 (or vice versa) still means "not done" — keep the
		// remaining as the fire target so we converge.
		if trustedAfter && liveAfter > 0 {
			// A real digit: the card genuinely holds units. Reset the
			// zero-streak and the stall streak, and fire exactly what's left.
			zeroOCRStreak = 0
			obsStreak = 0
			count = padCount(liveAfter)
			countResolved = true
		} else if trustedAfter {
			// A real zero on a card that is not visually empty: a queued CC
			// troop, or a spent card whose cooldown icon never "empties".
			// Fire one small fallback batch so a genuinely-full card still
			// makes progress; if the SECOND consecutive trusted zero read
			// also disagrees with the visual check, blind taps will never
			// help — abandon the card.
			zeroOCRStreak++
			if zeroOCRStreak >= 2 {
				slot.IsEmpty = true
				sw.logger.Warn().
					Str("unit", slot.UnitName).
					Int("attempt", attempt+1).
					Int("zero_reads", zeroOCRStreak).
					Msg("sweep reconcile: card shows no count while the rest of the bar is readable; card is spent or locked — marking deployed")
				return true
			}
			count = blindBatchTaps
			countResolved = false
		} else {
			// The reader is blind on this frame, so the zero count says
			// nothing. Keep the blind batch and let the visual check drive.
			// Deliberately NOT counted towards zeroOCRStreak: a blind read
			// must never be able to write a slot off.
			count = blindBatchTaps
			countResolved = false
		}
		// Nothing measured changed across two rounds of taps, and a resolved
		// count has already been fired: the card is spent, locked, or otherwise
		// not responding to the slot selection. Assigning the outcome rather than
		// spending the rest of the reconcile budget (and the whole event-troop
		// round budget) on a card that cannot change.
		//
		// The firedResolvedOnce requirement is what keeps the stall detector
		// honest: a card whose count was never readable only ever took blind
		// batches, so identical observations say nothing about whether it still
		// holds troops.
		if sweepSpentLocked(obsStreak, firedResolvedOnce) {
			switch sweepStallOutcomeFor(isEventTroop, visualAfter, stallVolleys+1) {
			case stallDeploy:
				// The frame is the only thing that may close a card out as
				// deployed, and it says the slot is empty.
				slot.IsEmpty = true
				sw.logger.Warn().
					Str("unit", slot.UnitName).
					Int("attempt", attempt+1).
					Int("identical_rounds", obsStreak).
					Bool("count_read_trusted", trustedAfter).
					Msg("sweep reconcile: card state unchanged across rounds after firing and the frame shows it empty; marking deployed")
				return true
			case stallKeepFiring:
				// An event-troop card is not spent until it is empty. Firing taps
				// is the only way its troops get placed, so keep at it while the
				// volley budget lasts (see eventStallVolleysLeft).
				stallVolleys++
				count = volleyFloor
				countResolved = true
				sw.logger.Info().
					Str("unit", slot.UnitName).
					Int("attempt", attempt+1).
					Int("next_fire", count).
					Int("visual_nonempty", 1).
					Msg("event troop sweep: card still not empty and unreadable; firing the resolved count again")
			default: // stallFail
				// Taps went out and the card did not drain. "Deployed" on a card
				// that still shows its units is the lie this branch used to tell:
				// live 2026-09-25 (TopLeft) the sweep's line projected off-screen,
				// the Electro Dragon card took its taps without its badge moving,
				// and the slot was closed out as deployed with x8 still in the
				// bar. The verifier's own frame check decides whether the leftover
				// counts as undeployed; here it is reported for what it is.
				sw.logger.Warn().
					Str("unit", slot.UnitName).
					Int("attempt", attempt+1).
					Int("identical_rounds", obsStreak).
					Int("stall_volleys", stallVolleys).
					Bool("count_read_trusted", trustedAfter).
					Bool("visual_nonempty", !visualAfter).
					Msg("sweep reconcile: card state unchanged after firing and it still shows units; reporting the slot failed rather than deployed")
				return false
			}
		}

		sw.logger.Info().
			Str("unit", slot.UnitName).
			Int("attempt", attempt+1).
			Int("remaining_read", liveAfter).
			Bool("count_read_trusted", trustedAfter).
			Int("next_fire", count).
			Int("zero_streak", zeroOCRStreak).
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

// spellLikeSlot reports whether a slot holds a spell, which must be cast on
// distinct ground, rather than a troop dump.
//
// The bar classifier's Category is the primary signal, but it only assigns
// "Spell" to slots at or right of firstSpellX, and a spell carried in the
// event/bonus slots (or one whose x falls left of that boundary) keeps
// Category "Troop" while its unit name still says "... spell". Trusting
// Category alone let a live earthquake line re-fire onto its own two points —
// a 0 px closest pair — because both the retry displacement and the
// spread-the-batch taps were gated on Category == "Spell".
func spellLikeSlot(slot *TrackedSlot) bool {
	if slot == nil {
		return false
	}
	if strings.EqualFold(slot.Category, "Spell") {
		return true
	}
	return strings.Contains(strings.ToLower(slot.UnitName), "spell")
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
	// A spell batch is not a troop spam-batch. Three rapid taps five pixels
	// apart place three spells on the same spot — one effect for the price of
	// three cards, measured live as an earthquake line whose four taps sat on
	// two points 0 px apart. For spells the three taps of a batch are spread
	// along the line instead, one cast each, which is what the spell deployer
	// does and what a player dragging a spell does.
	spellBatch := spellLikeSlot(slot)
	for i := 0; i < count; i += 3 {
		// Battle-timer guard inside the hot loop: even a single batch of
		// triple-taps is wasted (and risky — it can land on the result
		// overlay) once the battle is over.
		if sw.executor.DeployBudgetExhausted() {
			return false
		}
		batchSize := 3
		if i+3 > count {
			batchSize = count - i
		}
		jitter := 8
		if spellBatch {
			taps := sw.spellBatchPoints(p1, p2, i, batchSize, count)
			sw.executor.client.TapTriple(
				taps[0].X, taps[0].Y, 12.0,
				taps[1].X, taps[1].Y, 12.0,
				taps[2].X, taps[2].Y, 12.0,
			)
		} else {
			pct := float64(i) / float64(count)
			tx := p1.X + int(float64(p2.X-p1.X)*pct)
			ty := p1.Y + int(float64(p2.Y-p1.Y)*pct)
			tx += (i%5 - 2) * jitter
			ty += (i%3 - 1) * jitter
			// Troop batches are unit placements, and the pinned deploy line can run
			// within a tile of the machine this battle already placed — live
			// 2026-09-27 the sweep's re-fired balloon/e-dragon line passed 26 px from
			// it. Move the batch base clear of the machine; the three taps of the
			// batch keep their shape relative to it.
			tx, ty = sw.executor.avoidSiegeGroundXY(tx, ty)
			if batchSize == 3 {
				sw.executor.client.TapTriple(tx, ty, 12.0, tx+5, ty+3, 12.0, tx-3, ty+6, 12.0)
			} else if batchSize == 2 {
				sw.executor.client.TapTriple(tx, ty, 12.0, tx+5, ty+3, 12.0, tx, ty, 12.0)
			} else {
				sw.executor.client.TapTriple(tx, ty, 12.0, tx, ty, 12.0, tx, ty, 12.0)
			}
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
			live, trusted, visualEmpty := captureSlotLiveCount(
				sw.executor, sw.troopCounter, slot,
				sw.slotManager.GetBarY(), sw.w, sw.h,
			)
			if visualEmpty && (!trusted || live <= 0) {
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
// HeroManager / Sweeper / Verifier. Returns (count, trusted, visuallyEmpty).
// slotHoldsCard reports whether the slot really carries a card, as opposed to
// being an empty position in the troop bar. See the call site for why this
// distinction decides whether any taps go out.
//
// It is deliberately conservative: every uncertainty answers "yes, there is a
// card" so a failed capture or a bar with no badge reader can never silently
// skip a card that needed placing.
func (sw *Sweeper) slotHoldsCard(slot *TrackedSlot, count int) bool {
	if slot == nil {
		return false
	}
	if strings.TrimSpace(slot.UnitName) != "" {
		return true
	}
	if count > 0 {
		return true
	}
	if sw.troopCounter == nil || !sw.troopCounter.HasDigitTemplates() || sw.executor == nil {
		// No badge reader to ask: keep the pre-existing behaviour and let the
		// visual check drive, rather than inventing an empty position.
		return true
	}
	screen, err := sw.executor.CaptureFresh()
	if err != nil {
		return true
	}
	defer screen.Close()
	return sw.troopCounter.SlotHasCountLabel(screen, slot.X, slot.Y, sw.slotManager.GetBarY())
}

func (sw *Sweeper) sweepLiveCount(slot *TrackedSlot) (int, bool, bool) {
	return captureSlotLiveCount(
		sw.executor,
		sw.troopCounter,
		slot,
		sw.slotManager.GetBarY(),
		sw.w, sw.h,
	)
}

// blindBatchTaps is the batch fired when no card count could be measured or
// authored. It is NOT a count: nothing in the log should be able to read it as
// one. It exists so a slot whose number cannot be read still makes progress
// each reconcile round while the loop drives on the visual empty check, and
// the loop's round budget is what bounds the total.
const blindBatchTaps = 3

// padCount returns n+1 when n >= 6 (absorbs single-digit OCR under-reads
// like "x9" misread for "x10"); returns n unchanged for small counts.
func padCount(n int) int {
	const padFloor = 6
	if n >= padFloor {
		return n + 1
	}
	return n
}

// shiftLineForAttempt displaces a swept spell's line perpendicular to itself by
// attempt × one cast spacing, alternating sides, so retries spread instead of
// restacking. Returns the segment unchanged when there is no spacing to apply.
func (sw *Sweeper) shiftLineForAttempt(p1, p2 image.Point, attempt int) (image.Point, image.Point) {
	if attempt <= 0 || sw.executor == nil || sw.executor.cal == nil {
		return p1, p2
	}
	spacing := sw.executor.cal.Length(spellBatchSpacingRef) + sw.executor.cal.Length(retryClearanceRef)
	dx := float64(p2.X - p1.X)
	dy := float64(p2.Y - p1.Y)
	length := math.Hypot(dx, dy)
	if length < 1 || spacing <= 0 {
		return p1, p2
	}
	side, steps := 1.0, (attempt+1)/2
	if attempt%2 == 0 {
		side, steps = -1.0, attempt/2
	}
	off := side * spacing * float64(steps)
	ux := -dy / length * off
	uy := dx / length * off
	return image.Pt(p1.X+int(math.Round(ux)), p1.Y+int(math.Round(uy))),
		image.Pt(p2.X+int(math.Round(ux)), p2.Y+int(math.Round(uy)))
}

// spellBatchPoints places the three taps of one spell batch at three distinct
// points along p1→p2, at least minCastSpacing apart, instead of stacking them
// five pixels from each other. Insets of 0.22/0.78 match the spell deployer's
// own line spreading so a swept spell lands where a designed one would; a batch
// of fewer than three repeats the last point (the client's TapTriple takes three
// coordinates by signature, and a repeated coordinate is one cast aimed once).
func (sw *Sweeper) spellBatchPoints(p1, p2 image.Point, i, batchSize, count int) [3]image.Point {
	spacing := 0.0
	if sw.executor != nil && sw.executor.cal != nil {
		spacing = sw.executor.cal.Length(spellBatchSpacingRef)
	}
	span := math.Hypot(float64(p2.X-p1.X), float64(p2.Y-p1.Y))

	var out [3]image.Point
	for k := 0; k < 3; k++ {
		tap := i + k
		if tap >= i+batchSize {
			tap = i + batchSize - 1
		}
		if tap < 0 {
			tap = 0
		}
		pct := 0.5
		switch {
		case count <= 1:
			pct = 0.5
		case spacing > 0 && span >= spacing*float64(count-1):
			// Room for every cast at the minimum spacing: spread evenly over
			// the usable span, the same 0.22→0.78 the designed deploy uses.
			pct = 0.22 + 0.56*float64(tap)/float64(count-1)
		default:
			// Not enough line for that many casts: spread over the whole
			// segment so the smaller batch still covers new ground.
			pct = float64(tap) / float64(count)
		}
		out[k] = image.Pt(
			p1.X+int(float64(p2.X-p1.X)*pct),
			p1.Y+int(float64(p2.Y-p1.Y)*pct),
		)
	}
	return out
}

// resolveSweepLine returns the deploy line for the sweep retry. Formula
// wins over the dynamic red-zone line. The formula may carry a "point"
// entry (we synthesize a degenerate line) or a "line"/"lines" entry
// (we use P1+P2 of the first line). Falls through to deployLine.Points
// when no formula entry applies.
func (sw *Sweeper) resolveSweepLine(slot *TrackedSlot, isEventTroop bool) (image.Point, image.Point, bool) {
	// The user's pins are the ground for the sweep too: a sweep exists to place
	// what the phases could not, and re-aiming it at the strategy formula after
	// the user pinned a line is how the same unit got tapped on two different
	// lines in one battle.
	if sw.pinnedGround && len(sw.deployLine.Points) >= 2 {
		return sw.deployLine.Points[0], sw.deployLine.Points[len(sw.deployLine.Points)-1], true
	}
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

// deployHeroSlotOnce performs a hero sweep retry with ONE device tap at
// +/- 3 px around a random deploy-line point, matching the single-tap
// drop in HeroManager.deploySingleHero (a hero deploys on one tap;
// extra taps land on an already-deployed hero and only look robotic).
//
// We use the same delta-based verify as the initial drop: capture a
// SETTLED pre-ratio BEFORE the tap (so the slot is not yet highlighted),
// drop, settle, capture a settled post-ratio, and accept if
// pre - post >= 0.15. Both reads poll until the card stops animating
// (see slot_settle.go) — fixed-delay reads used to catch the card
// mid-animation and fail heroes that had deployed.
// Absolute thresholds are unreliable across hero icons (BK baseline
// 0.67 already exceeds the previous 0.40 cutoff), but the delta is
// consistent because all heroes transition to a ~0.10-0.30 cooldown
// silhouette on success.
func (sw *Sweeper) deployHeroSlotOnce(slot *TrackedSlot) bool {
	cands := sw.heroDropCandidates(slot)
	if len(cands) == 0 {
		sw.logger.Warn().Int("x", slot.X).Msg("hero sweep: no in-band deploy ground available")
		return false
	}
	pt := cands[0]

	// The hero card is capped at two taps per battle (place + ability). If
	// the main drop and the ability pass already spent them, do not tap
	// again — a third card tap is exactly the reported defect.
	if !sw.executor.HeroSpotTapBudgetLeft(slot) {
		sw.logger.Warn().
			Int("x", slot.X).
			Str("unit", slot.UnitName).
			Msg("hero sweep: card tap budget exhausted (place + ability already used); skipping hero")
		return false
	}

	// 0. Capture pre-retry baseline BEFORE any tap. Settled, so a card still
	// animating out of the failed attempt cannot poison the baseline.
	preRatio, capturedPre := sw.executor.CaptureSettledSlotRatio(sw.w, slot)

	if !sw.executor.TapSlot(slot, heroCardTapJitter) {
		sw.logger.Warn().
			Int("x", slot.X).
			Str("unit", slot.UnitName).
			Msg("hero sweep: card tap budget exhausted; not firing the drop")
		return false
	}
	sw.executor.HumanSleep(150, 30)

	// Single tap: one tap deploys a hero in CoC. The old 3-tap cluster
	// (and before that a single tap that "regressed") was fighting the
	// verify-timing bug — the post-drop capture was reading the card's
	// decaying selection highlight, not the cooldown silhouette. With
	// the settle windows fixed, the extra taps are pure bot-signature:
	// they land on an already-deployed hero and do nothing.
	sw.fireHeroFieldTap(pt)

	sw.executor.HumanSleep(heroDropSettleMs, 40)

	postRatio, capturedPost := sw.executor.CaptureSettledSlotRatio(sw.w, slot)

	confirm := func(attempt int, pt image.Point, pre, post float64) bool {
		delta := pre - post
		if !heroPlaced(pre, post, true) {
			return false
		}
		sw.logger.Info().
			Int("x", slot.X).
			Str("unit", slot.UnitName).
			Int("attempt", attempt).
			Interface("target", pt).
			Float64("pre_ratio", pre).
			Float64("post_ratio", post).
			Float64("delta", delta).
			Msg("hero sweep deploy succeeded")
		return true
	}

	if capturedPre && capturedPost && confirm(0, pt, preRatio, postRatio) {
		return true
	}

	// A refused drop leaves the card selected, so the field can be re-aimed at
	// the next candidate ground WITHOUT spending another card tap: the hero's
	// remaining tap stays free for its ability (run10: the pinned mid-line
	// ground was inside the red line, so the first field tap on every hero was
	// refused and the sweep's single card tap was spent re-placing rather than
	// firing the ability).
	if capturedPost {
		won := runHeroFieldRetries(
			cands, postRatio, true,
			sw.fireHeroFieldTap,
			func() (float64, bool) {
				sw.executor.HumanSleep(300, 40)
				return sw.executor.CaptureSettledSlotRatio(sw.w, slot)
			},
			func() bool { return sw.executor.DeployBudgetExhausted() },
			func(attempt int, pt image.Point, pre, post, delta float64, ok bool) {
				sw.logger.Info().
					Int("x", slot.X).
					Str("unit", slot.UnitName).
					Int("attempt", attempt).
					Interface("target", pt).
					Float64("pre_ratio", pre).
					Float64("post_ratio", post).
					Float64("delta", delta).
					Bool("confirmed", ok).
					Msg("hero sweep field-tap retry on new ground (no extra card tap)")
			},
		)
		if won {
			sw.logger.Info().
				Int("x", slot.X).
				Str("unit", slot.UnitName).
				Msg("hero sweep deploy succeeded")
			return true
		}
		sw.logger.Warn().
			Int("x", slot.X).
			Int("slot_y", slot.Y).
			Int("screen_w", sw.w).
			Int("probe_half", SlotProbeSize(sw.w)).
			Str("unit", slot.UnitName).
			Float64("pre_ratio", preRatio).
			Float64("post_ratio", postRatio).
			Msg("hero sweep retry did not visibly transition slot; will be marked failed")
		return false
	}

	sw.logger.Info().
		Int("x", slot.X).
		Str("unit", slot.UnitName).
		Msg("hero sweep deploy (capture unavailable; trusting tap)")
	return true
}
