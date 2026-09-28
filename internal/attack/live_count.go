package attack

import (
	"time"
)

// captureSlotLiveCount performs fresh screencaps and reads BOTH the per-card
// count (live OCR via the digit-template matcher) AND the activity ratio
// (HSV visual-empty check). Returns (count, trusted, visuallyEmpty).
//
// Shared helper for HeroManager / Sweeper / Verifier / SpellDeployer so the
// live-OCR reconcile loop has one canonical source. Behavior:
//
//   - When troopCounter is nil OR has no digit templates loaded
//     (HasDigitTemplates == false), the live OCR step is skipped,
//     trusted=false, and the caller falls back to the visual empty check
//     alone. This is the legacy-safe mode preserved for offline tests of
//     HeroManager / Sweeper / Verifier that don't wire a TroopCounter.
//   - On ADB screencap failure, returns (0, false, false) — the caller
//     should treat capture failure as "unknown" rather than bail.
//
// # What `trusted` means, and why it exists
//
// A zero count is only evidence that the card is empty when the READER is
// working. The pre-fix reader sampled a band ~45px above the count label, so
// it returned count=0 for every slot of every frame — and the reconcilers
// downstream could not tell that apart from a bar full of spent cards. That is
// how "OCR reads zero, so the card is spent or locked" came to fire on cards
// that were still full, and how the sweep wrote off undeployed troops.
//
// So the count and the visual check are not enough on their own; the caller
// needs the third signal:
//
//	trusted=true,  count>0   → the card holds this many units.
//	trusted=true,  count==0  → the card shows no count while other cards in
//	                           the bar still do: it is spent or locked.
//	trusted=false            → unknown (capture failed, no templates, no card in
//	                           the bar is legible, or THIS card carries a label
//	                           the digits could not be settled). A zero here
//	                           carries no information; the visual check is the
//	                           only usable signal, exactly as before OCR existed.
//	                           A label present but unreadable is deliberately in
//	                           this bucket: the card is demonstrably not spent,
//	                           so no caller may write it off.
//
// A slot is considered TRULY empty only when (trusted AND count == 0 AND
// visuallyEmpty) — or, when the reader is untrusted, when visuallyEmpty alone
// says so. When they disagree the caller should keep reconciling rather than
// mark deployed — this is the surgical fix for the "balloons/EDs sometimes
// don't all get placed" bug where a single visual-empty snapshot could
// silently under-fire the slot.
//
// # One frame is not a read
//
// The deploy-time read merges several frames for exactly one reason: the bar
// animates, and a card caught mid-animation carries a label whose digits the
// reader cannot settle ("count label present but not readable"). The per-slot
// live read used to take ONE capture, so every reconcile round that landed on
// an animating card paid the full unknown price — measured live 2026-09-26,
// run5: fifteen "label present but not readable" warnings in one battle, each
// one a reconcile round that fired a blind 3-tap batch it did not need. The
// count is stable until the card's units are spent, so a second capture a
// settle-interval later is free and turns most of those into real numbers.
// liveCountVerdict stays a pure function so the merge policy stays testable
// without a device.
func captureSlotLiveCount(
	executor *TapExecutor,
	troopCounter *TroopCounter,
	slot *TrackedSlot,
	barY int,
	w, h int,
) (int, bool, bool) {
	if executor == nil || slot == nil {
		return 0, false, false
	}

	// First capture serves both the visual check and the OCR read.
	screen, err := executor.CaptureFresh()
	if err != nil {
		return 0, false, false
	}
	visuallyEmpty := isSlotEmptyStatic(screen, slot.X, slot.Y, w, h)

	if troopCounter == nil || !troopCounter.HasDigitTemplates() {
		screen.Close()
		// Legacy mode: no reader wired through. Keep the old contract of
		// "trusted" meaning nothing was measured, so callers use the visual
		// check alone.
		return 0, false, visuallyEmpty
	}

	res, labelSeen := troopCounter.detectSlotCountDetailed(screen, slot.X, slot.Y, barY)
	legible := troopCounter.BarLegible(screen, barY, slot.Y)
	count, trusted := liveCountVerdict(res, labelSeen, legible)

	// The label-present-but-unreadable outcome is per-frame noise: the same
	// card read clean on the next frame in every live case examined. One
	// retry capture costs a settledPollInterval (~250ms) of a three-minute
	// battle and removes most blind batches, so it is unconditional on the
	// first frame being unusable for this card. Only an unreadable LABEL
	// retries — a label-free card is the spent-or-blind-frame verdict, and
	// re-shooting it cannot change what the caller does with the visual
	// check it already has.
	if !trusted && labelSeen {
		time.Sleep(settledPollInterval)
		if next, nerr := executor.CaptureFresh(); nerr == nil && !next.Empty() {
			res2, seen2 := troopCounter.detectSlotCountDetailed(next, slot.X, slot.Y, barY)
			count2, trusted2 := liveCountVerdict(res2, seen2, troopCounter.BarLegible(next, barY, slot.Y))
			if trusted2 {
				count, trusted = count2, true
			}
			next.Close()
		}
	}
	screen.Close()

	return count, trusted, visuallyEmpty
}

// liveCountVerdict turns the two reader outcomes plus the frame-level probe into
// the (count, trusted) pair every reconciler consumes. It is the whole reason
// `trusted` exists, so it lives on its own and is covered by a truth-table test.
//
// A LABEL seen on a card whose digits could not be settled is NOT a spent card —
// a spent card carries no label at all — so it is unknown and must never be
// handed back as a trusted zero. This case used to fall through to the
// frame-level probe, which asks only "is ANY card in the bar readable?". On a
// 720p bar where 3 of 10 cards read, that probe answered yes, the unknown became
// `trusted=true, count=0`, and the sweep's zero-streak rule wrote the card off as
// "spent or locked — marking deployed" while its art was pixel-identical to the
// start of the battle. That is how Balloon and Electro Dragon were reported as
// placed in live run13 (2026-09-22) when they never left the bar.
func liveCountVerdict(res TroopCount, labelSeen, barLegible bool) (int, bool) {
	if res.OK {
		return res.Count, true
	}
	if labelSeen {
		return 0, false
	}
	// No label at all. Either the card is spent, or the frame as a whole is
	// unreadable; only the frame-level probe can tell those apart.
	if barLegible {
		return 0, true
	}
	return 0, false
}
