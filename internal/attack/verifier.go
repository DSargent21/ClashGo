package attack

import (
	"image"
	"time"

	"github.com/Ducky705/ClashGO/pkg/formula"
	"github.com/rs/zerolog"
	"gocv.io/x/gocv"
)

// VerifyConfig holds verification thresholds.
type VerifyConfig struct {
	EmptyThreshold   float64
	AbilityThreshold float64
	MaxRetryAttempts int
	RetryDelay       time.Duration
	SettleWait       time.Duration
}

// DefaultVerifyConfig returns default verification config.
func DefaultVerifyConfig() VerifyConfig {
	return VerifyConfig{
		EmptyThreshold:   0.08,
		AbilityThreshold: 0.4,
		MaxRetryAttempts: 3,
		RetryDelay:       500 * time.Millisecond,
		SettleWait:       250 * time.Millisecond,
	}
}

// Verifier handles post-deployment verification.
type Verifier struct {
	executor     *TapExecutor
	slotManager  *SlotManager
	pCfg         PrecisionConfig
	targetEdge   string
	w, h         int
	config       VerifyConfig
	troopCounter *TroopCounter
	zone         RedZone

	// formula and deployLine are the ground this battle actually deployed on.
	// The verifier's retries used to re-derive the corner edge from pCfg, which
	// is not the line the troops used whenever the strategy carries a formula or
	// the operator pinned a side — so a retry was fired at ground the battle
	// never used (live 2026-11-15: formula/pin ground, corner-edge retries).
	formula      *formula.Formula
	deployLine   DeployLine
	pinnedGround bool
	logger       zerolog.Logger
}

// SetGround wires the battle's deploy ground (strategy formula + the resolved
// deploy line) so a retry re-aims at the same ground the deploy phase used.
// pinnedGround makes the operator's pin outrank the formula, matching
// HeroManager.SetPinnedGround and Sweeper.SetPinnedGround.
func (v *Verifier) SetGround(f *formula.Formula, line DeployLine, pinned bool) {
	v.formula = f
	v.deployLine = line
	v.pinnedGround = pinned
}

// NewVerifier creates a new verifier. troopCounter may be nil; when
// non-nil, retryDeploy uses it to live-OCR the slot before re-firing
// so we never under-spot a slot whose cards still hold troops.
func NewVerifier(
	executor *TapExecutor,
	slotManager *SlotManager,
	pCfg PrecisionConfig,
	targetEdge string,
	w, h int,
	config VerifyConfig,
	troopCounter *TroopCounter,
	logger zerolog.Logger,
) *Verifier {
	return &Verifier{
		executor:     executor,
		slotManager:  slotManager,
		pCfg:         pCfg,
		targetEdge:   targetEdge,
		w:            w,
		h:            h,
		config:       config,
		troopCounter: troopCounter,
		logger:       logger.With().Str("component", "verifier").Logger(),
	}
}

// VerifyAll runs comprehensive post-attack verification.
// Returns number of remaining undeployed slots.
func (v *Verifier) VerifyAll() int {
	v.logger.Info().Msg("waiting for deployment to settle")
	v.executor.WaitForSettle(v.config.SettleWait)

	v.logger.Info().Msg("verifying deployment success")
	remainingCount := 0
	var lastRemaining []*TrackedSlot

	for attempt := 1; attempt <= v.config.MaxRetryAttempts; attempt++ {

		// Battle-timer guard: verification redeploys are taps too — they
		// must stop the moment the deploy budget is gone.
		if v.executor.DeployBudgetExhausted() {
			v.logger.Warn().Msg("verifier: deploy budget exhausted; stopping redeploy attempts")
			break
		}

		if attempt == 1 {
			v.executor.WaitForSettle(300 * time.Millisecond)
		}

		screen, err := v.executor.CaptureFresh()
		if err != nil {
			v.logger.Warn().Err(err).Msg("failed to capture verification screen")
			break
		}

		remainingSlots := v.checkRemainingSlots(screen)
		screen.Close()

		lastRemaining = remainingSlots
		remainingCount = len(remainingSlots)
		if remainingCount == 0 {
			v.logger.Info().Msg("all units successfully deployed")
			break
		}

		v.logger.Warn().
			Int("attempt", attempt).
			Int("remaining", remainingCount).
			Msg("detected undeployed units, retrying")

		for _, slot := range remainingSlots {
			v.retryDeploy(slot)
		}
	}

	// One line per unit the run could not get off the bar. The aggregate
	// "remaining=1" is easy to miss and says nothing about WHICH card; this is
	// what attack_report reads to name the unit instead of calling the deploy a
	// success. Logged only after the final check, so a unit the last retry
	// recovered is not accused.
	v.logUnaccountedSlots(lastRemaining)

	return remainingCount
}

// logUnaccountedSlots emits exactly one final line per unit neither proven
// deployed nor proven failed. A slot the verifier still saw is FAILED; a slot
// in the ambiguous card region (above empty, below the ability band) is
// UNKNOWN with the missing evidence named. Silence here is what let run 9's
// report call a placed troop unverified and a truly unreadable card disappear.
func (v *Verifier) logUnaccountedSlots(lastRemaining []*TrackedSlot) {
	reported := make(map[*TrackedSlot]bool, len(lastRemaining))
	for _, slot := range lastRemaining {
		if slot == nil {
			continue
		}
		reported[slot] = true
		if slot.State == SlotDeployed {
			continue
		}
		v.logger.Warn().
			Int("x", slot.X).
			Str("unit", slot.UnitName).
			Str("category", slot.Category).
			Msg("verifier: unit still on the bar at battle end")
	}

	for _, slot := range v.slotManager.GetAllSlots() {
		if slot == nil || reported[slot] || slot.State == SlotDeployed || slot.State == SlotFailed {
			continue
		}
		v.logger.Warn().
			Int("x", slot.X).
			Str("unit", slot.UnitName).
			Str("category", slot.Category).
			Msg("verifier: positive confirmation impossible; no settled post-deploy frame showed the card empty or drained")
	}
}

// checkRemainingSlots identifies slots that still have content.
//
// Hero slots ARE included: a hero whose main drop AND sweep retry both
// failed used to vanish from the report (live, valk_run6: Grand Warden
// failed both and the verifier still announced "all units successfully
// deployed" because checkRemainingSlots filtered the Hero category out).
// Heroes get their own retryDeploy branch below instead of the troop
// tap-spam, so including them here is safe and makes the returned count
// tell the truth.
func (v *Verifier) checkRemainingSlots(screen gocv.Mat) []*TrackedSlot {
	var remaining []*TrackedSlot

	for _, slot := range v.slotManager.GetAllSlots() {

		if slot.State == SlotDeployed {
			continue
		}

		// A placed siege machine never leaves its card looking empty (siege
		// cards keep a cooldown silhouette), so every measurement below reads
		// "still has content" forever. Counting it as undeployed is what sends
		// the retry pass to fire field taps at the machine that is already
		// standing there — the second tap that destroys it. It is not
		// undeployed: it has had its one placement tap (TrackedSlot.SiegePlaced).
		if slot.SiegePlaced {
			v.slotManager.MarkDeployed(slot.UnitName)
			continue
		}

		ratio := GetSlotActivityRatioStatic(screen, slot.X, slot.Y, v.w)

		if ratio < v.config.EmptyThreshold {
			slot.IsEmpty = true
			// POSITIVE CONFIRMATION. The card's region is empty on a settled
			// post-deploy frame: the unit is off the bar. Until this line
			// existed, a troop handed to the sweep after an unreadable badge was
			// silently closed here (IsEmpty=true, no log, no state change), so
			// attack_report had no evidence and called it UNVERIFIED even though
			// the sweep found zero troop cards left (live run 9, 3 stars).
			// Logging and marking it deployed is not an over-claim: the evidence
			// is the same empty-card observation the verifier already trusts to
			// count it as gone.
			if slot.UnitName != "" {
				v.logger.Info().
					Int("x", slot.X).
					Float64("ratio", ratio).
					Str("unit", slot.UnitName).
					Msg("verifier: slot visibly empty; deployment confirmed")
			}
			v.slotManager.MarkSlotDeployed(slot)
			continue
		}

		if ratio < v.config.AbilityThreshold {
			if slot.Category == "Hero" {
				// A hero card in cooldown silhouette reads far below the
				// ability-icon band — live run5 2026-09-26: the Barbarian King
				// the main pass deployed (post_ratio 0.67 right after the
				// displaced retry) had cooled to <0.4 by verify time, was
				// skipped here at Debug level, and the log never recorded its
				// deployment anywhere. Silence is the failure mode attack_report
				// reads as UNVERIFIED, so a cooled hero is confirmed, logged,
				// and marked deployed.
				v.logger.Info().
					Int("x", slot.X).
					Float64("ratio", ratio).
					Str("unit", slot.UnitName).
					Msg("hero retry: slot shows cooldown silhouette; deployed")
				v.slotManager.MarkDeployed(slot.UnitName)
				continue
			}
			v.logger.Debug().
				Int("x", slot.X).
				Float64("ratio", ratio).
				Str("unit", slot.UnitName).
				Msg("skipping low-ratio slot (ability icon?)")
			continue
		}

		// A slot the sweep gave up on is NOT a deployed slot, and skipping it
		// here is what let a run with troops still on the bar report
		// "Deploy Health: SUCCESS (100% Deployed)": the card had been marked
		// failed (or written off as spent) by the sweep, so the verifier never
		// looked at it again and the surviving troops counted as nothing. A
		// failed slot that still shows content is exactly the case this count
		// exists to report, and the retry pass below gets another chance at it.
		if slot.State == SlotFailed {
			v.logger.Warn().
				Int("x", slot.X).
				Str("unit", slot.UnitName).
				Float64("ratio", ratio).
				Msg("slot was reported failed but still shows content; counting it as undeployed")
			remaining = append(remaining, slot)
			continue
		}

		// Siege is included because a FAILED machine leaves its card armed with
		// the machine: skipping it here is how run 11 logged "the machine never
		// left its card" and then "all units successfully deployed", and the
		// report claimed success with no UNDEPLOYED slot at all.
		if slot.Category == "Troop" || slot.Category == "Spell" || slot.Category == "CC" || slot.Category == "Event" || slot.Category == "Hero" || slot.Category == "Siege" {
			remaining = append(remaining, slot)
		}
	}

	return remaining
}

// retryDeploy attempts to redeploy a slot with retries.
//
// Live-OCR-driven fire count: previous version fired a fixed 9-step
// triple regardless of how many troops were still on the bar. When the
// real count was larger (e.g. 12 EDs after a mid-deploy session
// hiccup) the retry under-fired and the slot was marked failed even
// though 3+ troops remained. Now each retry round live-OCRs the per-
// card count and fires exactly that many taps — the user-reported
// "balloons/EDs sometimes don't all get placed" symptom closes here
// as well as in HeroManager / Sweeper.
//
// Reconcile-after: each retry loop ends with a fresh capture +
// count+visual-empty confirmation before declaring success.
func (v *Verifier) retryDeploy(slot *TrackedSlot) {
	const retryBatches = 2

	// Never re-place a siege machine: the field tap of this retry lands on the
	// machine the first placement already put down, and destroys it. Nothing is
	// marked here — checkRemainingSlots already closes this slot out, and this
	// guard is the backstop for any future caller.
	if slot.SiegePlaced {
		v.logger.Warn().
			Int("x", slot.X).
			Str("unit", slot.UnitName).
			Msg("retry: the siege machine already took its placement tap; refusing to fire at it again")
		return
	}

	// A machine the deploy phase proved it never placed is reported, not
	// re-tapped here: its three ground attempts are already spent, and field
	// taps from this pass are aimed at a card that still holds the machine.
	// Counting it as remaining is what makes the run FAILED and visible.
	if slot.Category == "Siege" {
		v.logger.Warn().
			Int("x", slot.X).
			Str("unit", slot.UnitName).
			Msg("retry: siege card still holds the machine; reporting it instead of firing more field taps")
		return
	}

	// Heroes get the hero-shaped retry: one tight tap cluster on the
	// deploy line, delta-verified — never the troop triple-tap spam,
	// which spreads a single hero across the whole line (the exact
	// misuse the sweeper's deployHeroSlotOnce docs call out).
	if v.isHeroShapedRetry(slot) {
		v.retryHeroSlot(slot)
		return
	}

	p1, p2, ok := v.retryLine(slot)
	if !ok {
		v.logger.Warn().Str("unit", slot.UnitName).Msg("retry: no deploy ground configured; marking failed")
		v.slotManager.MarkFailed(slot.UnitName)
		return
	}

	for batch := 0; batch < retryBatches; batch++ {
		live, trusted, empty := v.verifierLiveCount(slot)
		// "Empty" needs the count read and the visual check to agree, or the
		// visual check alone when no card in the bar is readable. A blind zero
		// must not be able to mark a slot deployed.
		if empty && (!trusted || live <= 0) {
			v.logger.Info().
				Int("x", slot.X).
				Str("unit", slot.UnitName).
				Int("batch", batch).
				Bool("count_read_trusted", trusted).
				Msg("retry: slot already empty on fresh capture; marking deployed")
			v.slotManager.MarkDeployed(slot.UnitName)
			return
		}
		count := live
		if !trusted || count <= 0 {
			// No measured count: fire a bounded blind batch, never a
			// fabricated number. The reconcile below is what drains the card.
			count = blindBatchTaps
		} else if count >= 6 {
			count++
		}

		v.executor.TapSlot(slot, 4)
		v.executor.HumanSleep(150, 30)

		for i := 0; i < count; i += 3 {
			batchSize := 3
			if i+3 > count {
				batchSize = count - i
			}
			if count < 3 {

				tx, ty := intLerp(p1, p2, 0)
				if batchSize == 3 {
					v.executor.client.TapTriple(tx, ty, 15.0, tx+5, ty+3, 15.0, tx-3, ty+6, 15.0)
				} else if batchSize == 2 {
					v.executor.client.TapTriple(tx, ty, 15.0, tx+5, ty+3, 15.0, tx, ty, 15.0)
				} else {
					v.executor.client.TapTriple(tx, ty, 15.0, tx, ty, 15.0, tx, ty, 15.0)
				}
				continue
			}
			steps := count
			pct1 := float64(i) / float64(steps-1)
			pct2 := float64(i+1) / float64(steps-1)
			pct3 := float64(i+2) / float64(steps-1)
			tx1, ty1 := intLerp(p1, p2, pct1)
			tx2, ty2 := intLerp(p1, p2, pct2)
			tx3, ty3 := intLerp(p1, p2, pct3)
			v.executor.client.TapTriple(tx1, ty1, 15.0, tx2, ty2, 15.0, tx3, ty3, 15.0)
		}

		v.executor.HumanSleep(150, 30)
		liveAfter, trustedAfter, emptyAfter := v.verifierLiveCount(slot)
		if emptyAfter && (!trustedAfter || liveAfter <= 0) {
			v.logger.Info().
				Int("x", slot.X).
				Str("unit", slot.UnitName).
				Int("batch", batch).
				Int("fired", count).
				Bool("count_read_trusted", trustedAfter).
				Msg("retry: reconciled slot empty; deploy complete")
			v.slotManager.MarkDeployed(slot.UnitName)
			return
		}
	}

	v.logger.Warn().
		Int("x", slot.X).
		Str("unit", slot.UnitName).
		Int("batches", retryBatches).
		Msg("retry: reconcile exhausted; marking failed")
	v.slotManager.MarkFailed(slot.UnitName)
}

// isHeroShapedRetry reports whether a slot takes the single-hero retry
// path. A fallback-labeled "hero" is a bonus troop wearing a stale manual
// label — it must go through the troop retry like the sweeper treats it.
func (v *Verifier) isHeroShapedRetry(slot *TrackedSlot) bool {
	return slot.Category == "Hero" && !slot.FallbackLabeled
}

// retryHeroSlot re-drops a hero the verifier found still on the bar,
// mirroring deploySingleHero / deployHeroSlotOnce: select the slot, drop a
// tight cluster of 3 jittered taps on a random deploy-line point, settle,
// then delta-verify (pre captured BEFORE the selection tap so the
// highlight does not poison the baseline).
func (v *Verifier) retryHeroSlot(slot *TrackedSlot) {
	if len(v.deployLine.Points) < 2 && v.formula == nil {
		// No deploy line and no formula (no red zone, no pin): the drop below
		// still fires at the screen centre so the hero gets an attempt, but it
		// cannot be delta-verified meaningfully against a line.
		v.logger.Warn().
			Int("x", slot.X).
			Str("unit", slot.UnitName).
			Msg("hero retry: no deploy line; firing centre drop")
	}

	// 2-tap ceiling: never tap a hero card a third time. If the budget is
	// spent the hero was already given its placement and ability taps, and a
	// further tap can only re-select the card.
	if !v.executor.HeroSpotTapBudgetLeft(slot) {
		v.logger.Warn().
			Int("x", slot.X).
			Str("unit", slot.UnitName).
			Msg("hero retry: card tap budget exhausted (place + ability already used); leaving the slot as-is")
		return
	}

	preRatio, capturedPre := v.executor.CaptureSettledSlotRatio(v.w, slot)

	pt := v.heroRetryGround(slot)
	if !v.executor.TapSlot(slot, 4) {
		v.logger.Warn().
			Int("x", slot.X).
			Str("unit", slot.UnitName).
			Msg("hero retry: card tap budget exhausted; not firing the drop")
		return
	}
	v.executor.HumanSleep(150, 30)

	// Single tap — one tap deploys a hero; extra taps are bot-signature. Jitter
	// first, then TapUnitField, so the siege-clearance check is last: this retry
	// must not land on the machine the hero phase already placed.
	j1 := v.executor.addJitter(pt, 3)
	v.executor.TapUnitField(j1, 12.0)

	v.executor.HumanSleep(300, 40)

	postRatio, capturedPost := v.executor.CaptureSettledSlotRatio(v.w, slot)
	if !capturedPost {
		v.logger.Info().
			Int("x", slot.X).
			Str("unit", slot.UnitName).
			Msg("hero retry: capture unavailable; trusting tap")
		v.slotManager.MarkDeployed(slot.UnitName)
		return
	}

	if heroPlaced(preRatio, postRatio, true) {
		v.logger.Info().
			Int("x", slot.X).
			Str("unit", slot.UnitName).
			Float64("pre_ratio", preRatio).
			Float64("post_ratio", postRatio).
			Msg("hero retry: visibly transitioned to cooldown; deployed")
		v.slotManager.MarkDeployed(slot.UnitName)
		return
	}

	v.logger.Warn().
		Int("x", slot.X).
		Int("slot_y", slot.Y).
		Int("screen_w", v.w).
		Int("probe_half", SlotProbeSize(v.w)).
		Str("unit", slot.UnitName).
		Float64("pre_ratio", preRatio).
		Float64("post_ratio", postRatio).
		Bool("captured_pre", capturedPre).
		Msg("hero retry did not visibly transition; keeping the failure in the report")
	v.slotManager.MarkFailed(slot.UnitName)
}

// SetRedZone wires the battle's detected red no-deploy line so a verifier hero
// retry aims at ground the game accepts (see hero_ground.go). A zero-value zone
// is legal and means "unchecked".
func (v *Verifier) SetRedZone(z RedZone) {
	v.zone = z
}

// retryLine resolves the ground a troop/spell retry fires on, with the same
// precedence the deploy path used: the operator's pin, then the strategy
// formula's per-unit ground, then the battle's deploy line, and only last the
// corner edge. Returns ok=false when nothing is configured.
func (v *Verifier) retryLine(slot *TrackedSlot) (image.Point, image.Point, bool) {
	if v.pinnedGround && len(v.deployLine.Points) >= 2 {
		return v.deployLine.Points[0], v.deployLine.Points[len(v.deployLine.Points)-1], true
	}
	if v.formula != nil && slot != nil {
		if entry, ok := v.formula.LookUp(slot.UnitName); ok {
			switch {
			case entry.IsPoint() && entry.P != nil:
				pt := entry.P.Image()
				return pt, pt, true
			case entry.IsLine() && entry.P1 != nil && entry.P2 != nil:
				return entry.P1.Image(), entry.P2.Image(), true
			case entry.IsLines() && len(entry.Lines) > 0:
				return entry.Lines[0].P1.Image(), entry.Lines[0].P2.Image(), true
			}
		}
	}
	if len(v.deployLine.Points) >= 2 {
		return v.deployLine.Points[0], v.deployLine.Points[len(v.deployLine.Points)-1], true
	}
	edge, ok := v.pCfg.Edges[v.targetEdge]
	if !ok {
		return image.Point{}, image.Point{}, false
	}
	scaled := ScaleEdge(edge, v.pCfg.Width, v.pCfg.Height, v.w, v.h)
	return scaled.P1, scaled.P2, true
}

// heroRetryGround picks the ground the verifier's hero retry drops on, using the
// same authored-then-line order as HeroManager.heroDropCandidates and the
// sweeper: the strategy formula's per-unit point first, then the zone-filtered
// battle line. The old version only knew the corner edge, so a retry after a
// formula/pinned deploy was aimed at ground the battle never used.
func (v *Verifier) heroRetryGround(slot *TrackedSlot) image.Point {
	var cands []image.Point
	if v.formula != nil && slot != nil {
		if entry, ok := v.formula.LookUp(slot.UnitName); ok {
			cands = append(cands, heroFormulaCandidates(entry, v.h)...)
		}
	}
	cands = append(cands, heroGroundCandidates(v.deployLine.Points, v.h, v.zone)...)
	if len(cands) > 0 {
		return cands[0]
	}
	return image.Pt(v.w/2, v.h/2)
}

// verifierLiveCount is a thin shim to the shared captureSlotLiveCount
// helper in live_count.go. Same semantics as HeroManager / Sweeper's
// shims so the reconcile contract is identical across phases.
// Returns (count, trusted, visuallyEmpty).
func (v *Verifier) verifierLiveCount(slot *TrackedSlot) (int, bool, bool) {
	return captureSlotLiveCount(
		v.executor,
		v.troopCounter,
		slot,
		v.slotManager.GetBarY(),
		v.w, v.h,
	)
}
