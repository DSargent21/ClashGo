package attack

import (
	"testing"

	"gocv.io/x/gocv"
)

// ---------------------------------------------------------------------------
// Verifier failed-slot visibility
//
// checkRemainingSlots used to skip SlotFailed slots entirely (only
// SlotDeployed was skipped after the hero fix). A slot the sweep gave up on
// then vanished from the remaining count, which is what let a run with troops
// still on the bar print "Deploy Health: SUCCESS (100% Deployed)": the failed
// card contributed nothing to remainingUndeployed, so DeploySuccess stayed
// true. The contract now is: a failed slot that STILL shows content must be
// reported as undeployed (and get a retry pass); a failed slot whose card is
// genuinely empty must not.
// ---------------------------------------------------------------------------

// solidMat returns a w x h BGR Mat filled with the given pixel value.
func solidMat(w, h int, b, g, r uint8) gocv.Mat {
	m := gocv.NewMatWithSize(h, w, gocv.MatTypeCV8UC3)
	m.SetTo(gocv.NewScalar(float64(b), float64(g), float64(r), 0))
	return m
}

func TestCheckRemainingSlots_CountsVisiblyNonEmptyFailedSlot(t *testing.T) {
	slot := &TrackedSlot{TroopSlot: TroopSlot{X: 360, Y: 682, Category: "Troop"}, UnitName: "balloon", Confidence: 0.9}
	v := newHeroVerifier(t, []*TrackedSlot{slot})
	slot.State = SlotFailed

	// Bright/colourful card content: white scores maskActB (sat 0-29,
	// val 221-255) so the slot ratio sits at ~1.0, far above the 0.08 empty
	// and 0.4 ability thresholds.
	nonEmpty := solidMat(900, 720, 255, 255, 255)
	defer nonEmpty.Close()

	remaining := v.checkRemainingSlots(nonEmpty)
	if len(remaining) != 1 || remaining[0] != slot {
		t.Fatalf("failed slot showing content must count as undeployed; got %d remaining", len(remaining))
	}
}

func TestCheckRemainingSlots_IgnoresEmptyFailedSlot(t *testing.T) {
	slot := &TrackedSlot{TroopSlot: TroopSlot{X: 360, Y: 682, Category: "Troop"}, UnitName: "balloon", Confidence: 0.9}
	v := newHeroVerifier(t, []*TrackedSlot{slot})
	slot.State = SlotFailed

	// Black reads as ratio 0: the card is genuinely gone, so a failed slot
	// must NOT inflate the remaining count after the sweep cleaned it up.
	empty := solidMat(900, 720, 0, 0, 0)
	defer empty.Close()

	if remaining := v.checkRemainingSlots(empty); len(remaining) != 0 {
		t.Fatalf("empty failed slot must not count as undeployed; got %d remaining", len(remaining))
	}
	if !slot.IsEmpty {
		t.Fatal("empty failed slot should have been flagged IsEmpty")
	}
}
