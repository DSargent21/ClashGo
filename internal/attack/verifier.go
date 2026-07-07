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
		SettleWait:       2 * time.Second,
	}
}

// Verifier handles post-deployment verification.
type Verifier struct {
	executor    *TapExecutor
	slotManager *SlotManager
	pCfg        PrecisionConfig
	targetEdge  string
	w, h        int
	config      VerifyConfig
	logger      zerolog.Logger
}

// NewVerifier creates a new verifier.
func NewVerifier(
	executor *TapExecutor,
	slotManager *SlotManager,
	pCfg PrecisionConfig,
	targetEdge string,
	w, h int,
	config VerifyConfig,
	logger zerolog.Logger,
) *Verifier {
	return &Verifier{
		executor:    executor,
		slotManager: slotManager,
		pCfg:        pCfg,
		targetEdge:  targetEdge,
		w:           w,
		h:           h,
		config:      config,
		logger:      logger.With().Str("component", "verifier").Logger(),
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
// Empty-slot guard: at the top of each batch, capture the live screen
// and bail out immediately if the slot's card is already empty. The
// previous version unconditionally fired 9 taps then checked — over-
// deploying on slots that emptied during the spike of earlier troop
// phases. The fresh-capture check here also lets a slot that emptied
// mid-retry short-circuit the second batch entirely.
func (v *Verifier) retryDeploy(slot *TrackedSlot) {
	for batch := 0; batch < 2; batch++ {
		// Empty-slot pre-check before any new tap. Cheap (~120ms)
		// ADB screencap + ratio check; saves 9 wasted taps when the
		// slot emptied from the prior batch.
		preScreen, err := v.executor.CaptureFresh()
		if err == nil {
			empty := isSlotEmptyStatic(preScreen, slot.X, slot.Y, v.w, v.h)
			preScreen.Close()
			if empty {
				v.logger.Info().
					Int("x", slot.X).
					Str("unit", slot.UnitName).
					Int("batch", batch).
					Msg("retry: slot already empty; marking deployed without retap")
				v.slotManager.MarkDeployed(slot.UnitName)
				return
			}
		}

		// Select slot
		v.executor.TapSlot(slot, 4)
		v.executor.HumanSleep(35, 10)

		// Deploy
		edge, ok := v.pCfg.Edges[v.targetEdge]
		if !ok {
			return
		}
		scaled := ScaleEdge(edge, v.pCfg.Width, v.pCfg.Height, v.w, v.h)
		p1, p2 := scaled.P1, scaled.P2

		// Line deployment. The loop fires TapTriple triples along
		// the line at fixed 9-step positions. We do NOT re-check
		// emptiness between the 3 taps inside a single batch because
		// each batch is one logical "deploy" gesture to CoC.
		steps := 9
		for i := 0; i < steps; i += 3 {
			pct1 := float64(i) / float64(steps-1)
			pct2 := float64(i+1) / float64(steps-1)
			pct3 := float64(i+2) / float64(steps-1)
			tx1, ty1 := intLerp(p1, p2, pct1)
			tx2, ty2 := intLerp(p1, p2, pct2)
			tx3, ty3 := intLerp(p1, p2, pct3)
			v.executor.client.TapTriple(tx1, ty1, 15.0, tx2, ty2, 15.0, tx3, ty3, 15.0)
		}

		time.Sleep(200 * time.Millisecond)

		// Verify
		checkScreen, err := v.executor.CaptureFresh()
		if err == nil {
			empty := isSlotEmptyStatic(checkScreen, slot.X, slot.Y, v.w, v.h)
			checkScreen.Close()
			if empty {
				v.slotManager.MarkDeployed(slot.UnitName)
				return
			}
		}

		// Re-select
		v.executor.TapSlot(slot, 4)
		time.Sleep(50 * time.Millisecond)
	}

	// Mark as failed after retries
	v.slotManager.MarkFailed(slot.UnitName)
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
