package attack

import (
	"time"

	"github.com/rs/zerolog"
	"gocv.io/x/gocv"
)

// VerifyConfig holds verification thresholds.
type VerifyConfig struct {
	EmptyThreshold   float64       // 0.08 - ratio below which slot is empty
	AbilityThreshold float64       // 0.4 - ratio below which is ability icon
	MaxRetryAttempts int           // 3
	RetryDelay       time.Duration // 500ms
	SettleWait       time.Duration // 2s
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
	troopCounter *TroopCounter // optional; enables live-OCR count + reconcile
	logger       zerolog.Logger
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

	for attempt := 1; attempt <= v.config.MaxRetryAttempts; attempt++ {
		// First-attempt settle: only the first attempt needs an extra 300ms
		// post-deploy-sync window. Subsequent attempts are gated by the
		// retryDeploy()'s own cap-with-emit-check, which already sees a
		// fresh "empty=false → retry" path. Sized to ~3x ADB capture round-trip.
		if attempt == 1 {
			v.executor.WaitForSettle(300 * time.Millisecond)
		}
		// Capture fresh screen
		screen, err := v.executor.CaptureFresh()
		if err != nil {
			v.logger.Warn().Err(err).Msg("failed to capture verification screen")
			break
		}

		// Check each slot
		remainingSlots := v.checkRemainingSlots(screen)
		screen.Close()

		remainingCount = len(remainingSlots)
		if remainingCount == 0 {
			v.logger.Info().Msg("all units successfully deployed")
			break
		}

		v.logger.Warn().
			Int("attempt", attempt).
			Int("remaining", remainingCount).
			Msg("detected undeployed units, retrying")

		// Redeploy remaining slots
		for _, slot := range remainingSlots {
			v.retryDeploy(slot)
		}
	}

	return remainingCount
}

// checkRemainingSlots identifies slots that still have content.
func (v *Verifier) checkRemainingSlots(screen gocv.Mat) []*TrackedSlot {
	var remaining []*TrackedSlot

	for _, slot := range v.slotManager.GetAllSlots() {
		// Skip already deployed or failed
		if slot.State == SlotDeployed || slot.State == SlotFailed {
			continue
		}

		// Get activity ratio
		ratio := GetSlotActivityRatioStatic(screen, slot.X, slot.Y, v.w)

		// Empty slot
		if ratio < v.config.EmptyThreshold {
			slot.IsEmpty = true
			continue
		}

		// Ability icon or UI artifact
		if ratio < v.config.AbilityThreshold {
			v.logger.Debug().
				Int("x", slot.X).
				Float64("ratio", ratio).
				Str("unit", slot.UnitName).
				Msg("skipping low-ratio slot (ability icon?)")
			continue
		}

		// Real content still present
		if slot.Category == "Troop" || slot.Category == "Spell" || slot.Category == "CC" || slot.Category == "Event" {
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

	edge, ok := v.pCfg.Edges[v.targetEdge]
	if !ok {
		v.logger.Warn().Str("unit", slot.UnitName).Msg("retry: no edge configured; marking failed")
		v.slotManager.MarkFailed(slot.UnitName)
		return
	}
	scaled := ScaleEdge(edge, v.pCfg.Width, v.pCfg.Height, v.w, v.h)
	p1, p2 := scaled.P1, scaled.P2

	for batch := 0; batch < retryBatches; batch++ {
		live, empty := v.verifierLiveCount(slot)
		if live <= 0 && empty {
			v.logger.Info().
				Int("x", slot.X).
				Str("unit", slot.UnitName).
				Int("batch", batch).
				Msg("retry: slot already empty on fresh capture; marking deployed")
			v.slotManager.MarkDeployed(slot.UnitName)
			return
		}
		count := live
		if count <= 0 {
			// OCR failed and slot visually non-empty — fall back to a
			// safe minimum (typical balloons/EDs) so the slot can
			// actually drain. The reconcile after the batch catches
			// over-firing.
			count = 8
		} else if count >= 6 {
			count++ // safety pad (single-digit OCR under-reads)
		}

		// Select slot
		v.executor.TapSlot(slot, 4)
		v.executor.HumanSleep(150, 30)

		// Fire exactly `count` taps distributed along the edge line.
		// Steps must be ≥ 2 before using `i / (steps-1)` to avoid a
		// divide-by-zero producing NaN coords (which would batch-tap
		// at pixel (0,0) the first time a single-troop slot is
		// retried). For count < 3 we cluster all taps at single point.
		for i := 0; i < count; i += 3 {
			batchSize := 3
			if i+3 > count {
				batchSize = count - i
			}
			if count < 3 {
				// Single/dual troop cluster: fire as one TapTriple.
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

		// ─── Reconcile after batch ─────────────────────────────────
		v.executor.HumanSleep(150, 30)
		liveAfter, emptyAfter := v.verifierLiveCount(slot)
		if liveAfter <= 0 && emptyAfter {
			v.logger.Info().
				Int("x", slot.X).
				Str("unit", slot.UnitName).
				Int("batch", batch).
				Int("fired", count).
				Msg("retry: reconciled slot empty; deploy complete")
			v.slotManager.MarkDeployed(slot.UnitName)
			return
		}
	}

	// Reconcile budget exhausted. Mark failed so the operator can spot
	// the persistent under-deployment in the deploy health output.
	v.logger.Warn().
		Int("x", slot.X).
		Str("unit", slot.UnitName).
		Int("batches", retryBatches).
		Msg("retry: reconcile exhausted; marking failed")
	v.slotManager.MarkFailed(slot.UnitName)
}

// verifierLiveCount is a thin shim to the shared captureSlotLiveCount
// helper in live_count.go. Same semantics as HeroManager / Sweeper's
// shims so the reconcile contract is identical across phases.
func (v *Verifier) verifierLiveCount(slot *TrackedSlot) (int, bool) {
	return captureSlotLiveCount(
		v.executor,
		v.troopCounter,
		slot,
		v.slotManager.GetBarY(),
		v.w, v.h,
	)
}

// CheckSlotEmpty checks if a slot is empty using the verifier's config.
func (v *Verifier) CheckSlotEmpty(slot *TrackedSlot) bool {
	screen, err := v.executor.CaptureFresh()
	if err != nil {
		return false
	}
	defer screen.Close()

	ratio := GetSlotActivityRatioStatic(screen, slot.X, slot.Y, v.w)
	return ratio < v.config.EmptyThreshold
}
