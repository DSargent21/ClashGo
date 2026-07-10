package attack

// captureSlotLiveCount performs a single fresh screencap and reads BOTH
// the per-card count (live OCR via the digit-template matcher) AND the
// activity ratio (HSV visual-empty check). Returns (count, visuallyEmpty).
//
// Shared helper for HeroManager / Sweeper / Verifier so the live-OCR
// reconcile loop has one canonical source. Behavior:
//
//   - When troopCounter is nil OR has no digit templates loaded
//     (HasDigitTemplates == false), the live OCR step is skipped and
//     the caller falls back to the visual empty check alone. This is
//     the legacy-safe mode preserved for offline tests of HeroManager
//     / Sweeper / Verifier that don't wire a TroopCounter through.
//   - On ADB screencap failure, returns (0, false) — the caller should
//     treat capture failure as "unknown" rather than bail.
//
// A slot is considered TRULY empty only when (count == 0 AND
// visuallyEmpty). When they disagree (e.g. visual non-empty but live
// OCR returned 0 due to a transient frame, or live OCR > 0 while the
// visual reads empty because the cursor is highlighted), the caller
// should keep reconciling rather than mark deployed — this is the
// surgical fix for the "balloons/EDs sometimes don't all get placed"
// bug where a single visual-empty snapshot could silently under-fire
// the slot.
func captureSlotLiveCount(
	executor *TapExecutor,
	troopCounter *TroopCounter,
	slot *TrackedSlot,
	barY int,
	w, h int,
) (int, bool) {
	if executor == nil || slot == nil {
		return 0, false
	}
	screen, err := executor.CaptureFresh()
	if err != nil {
		return 0, false
	}
	defer screen.Close()

	var count int
	if troopCounter != nil && troopCounter.HasDigitTemplates() {
		count = troopCounter.DetectCount(screen, slot, barY)
	}
	empty := isSlotEmptyStatic(screen, slot.X, slot.Y, w, h)
	return count, empty
}
