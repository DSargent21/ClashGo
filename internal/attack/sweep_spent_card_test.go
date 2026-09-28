package attack

import "testing"

// TestEventSweepContinue: an extra event-troop round may only fire on fresh,
// trusted evidence that units remain. This is the guard that stops a second
// round from re-firing the count the first round already spent.
func TestEventSweepContinue(t *testing.T) {
	cases := []struct {
		name    string
		live    int
		trusted bool
		want    int
		wantOK  bool
	}{
		{"trusted read of remaining units fires exactly that many", 5, true, 5, true},
		{"trusted read at the pad floor still pads, as the fire loop expects", 9, true, 10, true},
		{"trusted zero means the card is spent", 0, true, 0, false},
		{"untrusted read with a positive count is not evidence", 18, false, 0, false},
		{"untrusted zero is not evidence either (a blind read must not decide)", 0, false, 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := eventSweepContinue(tc.live, tc.trusted)
			if ok != tc.wantOK {
				t.Fatalf("continue = %v, want %v", ok, tc.wantOK)
			}
			if ok && got != tc.want {
				t.Errorf("count = %d, want %d", got, tc.want)
			}
		})
	}
}

// TestSweepSpentLocked: the stall write-off needs BOTH an unchanged observation
// across rounds AND a resolved count already fired at the card. A card whose
// count was never readable only ever took blind batches, and abandoning it there
// is how a full card gets left on the bar.
func TestSweepSpentLocked(t *testing.T) {
	cases := []struct {
		name              string
		identicalRounds   int
		firedResolvedOnce bool
		want              bool
	}{
		{"resolved count fired, then no change: spent", 2, true, true},
		{"no change but only blind batches: keep trying", 2, false, false},
		{"more rounds of no change: still spent", 4, true, true},
		{"resolved count fired but the reading moved: not spent", 1, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sweepSpentLocked(tc.identicalRounds, tc.firedResolvedOnce); got != tc.want {
				t.Errorf("sweepSpentLocked(%d, %v) = %v, want %v", tc.identicalRounds, tc.firedResolvedOnce, got, tc.want)
			}
		})
	}
}

// TestSweepStallOutcomeFor pins what a card that stopped responding to taps may
// be recorded as.
//
// A stalled card is DEPLOYED only when the frame independently shows the slot
// empty — the same measurement the verifier uses. Marking it deployed on the
// strength of "the taps changed nothing" is how a card with units still on it
// was reported as placed: live 2026-09-25 (TopLeft) the sweep's line projected to
// x=-11, the Electro Dragon card's taps went off-screen, and the slot was closed
// out as deployed with x8 still in the bar.
func TestSweepStallOutcomeFor(t *testing.T) {
	cases := []struct {
		name          string
		eventTroop    bool
		visuallyEmpty bool
		stallVolleys  int
		want          sweepStallOutcome
	}{
		{"non-event still showing units: FAILED, never deployed", false, false, 1, stallFail},
		{"non-event the frame says is empty: deployed", false, true, 1, stallDeploy},
		{"event still showing units with budget left: fire another volley", true, false, 1, stallKeepFiring},
		{"event still showing units with the budget spent: FAILED", true, false, maxEventStallVolleys, stallFail},
		{"event the frame says is empty: deployed, budget irrelevant", true, true, maxEventStallVolleys, stallDeploy},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := sweepStallOutcomeFor(tc.eventTroop, tc.visuallyEmpty, tc.stallVolleys)
			if got != tc.want {
				t.Errorf("sweepStallOutcomeFor(%v, %v, %d) = %v, want %v",
					tc.eventTroop, tc.visuallyEmpty, tc.stallVolleys, got, tc.want)
			}
		})
	}

	// The invariant the whole branch exists for: a card whose units are still on
	// the bar is never recorded as deployed, for any combination of event flag
	// and remaining volley budget.
	for _, eventTroop := range []bool{false, true} {
		for volleys := 0; volleys <= maxEventStallVolleys+1; volleys++ {
			if got := sweepStallOutcomeFor(eventTroop, false, volleys); got == stallDeploy {
				t.Errorf("event=%v with the card still non-empty must never deploy (volleys=%d)", eventTroop, volleys)
			}
		}
	}
}

// TestSlotHoldsCard pins the difference between a card the sweep must place and
// an empty position in the bar.
//
// GetEventTroops includes slots with no unit name on purpose (a seasonal card
// that template-matches nothing is still a card the user wants placed), which
// also makes it include empty bar positions. Live 2026-09-25 (run11) the two
// empty positions at x=1048/x=1148 were swept as event troops — 6 blind taps
// each in the troop pass and 6 more each in the event pass, 24 taps into empty
// space. The evidence that separates the two cases is a strategy name, a
// resolved count, or a count badge; every uncertain answer must stay "there is a
// card", or a real bonus troop gets silently skipped.
func TestSlotHoldsCard(t *testing.T) {
	slot := func(name string) *TrackedSlot {
		return &TrackedSlot{TroopSlot: TroopSlot{X: 1048, Y: 682, Category: "Spell"}, UnitName: name}
	}

	sw := &Sweeper{} // no counter, no executor

	if !sw.slotHoldsCard(slot("balloon"), 0) {
		t.Error("a named slot holds a card")
	}
	if !sw.slotHoldsCard(slot(""), 7) {
		t.Error("an unlabelled slot with a resolved count holds a card")
	}
	if sw.slotHoldsCard(nil, 5) {
		t.Error("a nil slot holds no card")
	}
	// No badge reader to ask: the answer must stay "yes", so the legacy visual
	// check keeps driving rather than the slot being skipped outright.
	if !sw.slotHoldsCard(slot(""), 0) {
		t.Error("with no badge reader the gate must not skip an unlabelled slot")
	}
}

// TestSlotReadObservationStallDetector: the recycle loop's spent/locked rule is
// "two consecutive rounds observed the same thing after firing". That is only a
// valid detector if the observation is a value comparison, so pin the premise:
// identical readings compare equal, and anything that moved does not.
func TestSlotReadObservationStallDetector(t *testing.T) {
	base := slotReadObservation{trusted: false, count: 0, visuallyEmpty: false}

	if base != (slotReadObservation{trusted: false, count: 0, visuallyEmpty: false}) {
		t.Error("identical observations must compare equal, or the stall detector never fires")
	}

	moved := []struct {
		name string
		obs  slotReadObservation
	}{
		{"the count label resolved", slotReadObservation{trusted: true, count: 0, visuallyEmpty: false}},
		{"a count appeared", slotReadObservation{trusted: true, count: 7, visuallyEmpty: false}},
		{"the visual check flipped to empty", slotReadObservation{trusted: false, count: 0, visuallyEmpty: true}},
	}
	for _, m := range moved {
		t.Run(m.name, func(t *testing.T) {
			if m.obs == base {
				t.Errorf("%s must count as a change, not a stall", m.name)
			}
		})
	}
}

// TestEventVolleyTarget pins the rule that stopped the sweep under-firing the
// event card.
//
// Live run 2026-09-24: the event card at x=137 resolved at 40 troops, a mid-sweep
// read of the half-drained card returned 26, the volley fired 27, the card was
// then written off as deployed, and the bar was left holding 9 of the 40 while the
// run reported "Deploy Health: SUCCESS (100% Deployed)". Firing more taps than the
// card holds costs nothing (taps onto a drained card drop nothing); firing fewer
// leaves troops on the bar.
func TestEventVolleyTarget(t *testing.T) {
	cases := []struct {
		name     string
		resolved int
		live     int
		trusted  bool
		want     int
	}{
		{"a smaller trusted live read never shrinks the volley", 40, 26, true, 40},
		{"a trusted live read below the pad floor cannot shrink it either", 40, 4, true, 40},
		{"a larger trusted live read raises the volley", 40, 55, true, 56},
		{"an untrusted live read is not a measurement", 40, 9, false, 40},
		{"an untrusted zero is not a measurement", 12, 0, false, 12},
		{"no resolved count and a trusted read: fire what is left", 0, 7, true, 8},
		{"nothing readable or resolved: the blind batch", 0, 0, false, blindBatchTaps},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := eventVolleyTarget(tc.resolved, tc.live, tc.trusted); got != tc.want {
				t.Errorf("eventVolleyTarget(%d, %d, %v) = %d, want %d",
					tc.resolved, tc.live, tc.trusted, got, tc.want)
			}
		})
	}
}

// TestEventStallVolleysLeft bounds the "keep firing at a card that will not
// drain" behaviour: an event-troop card gets extra full volleys while the volley
// budget lasts, and is then reported FAILED — never deployed.
func TestEventStallVolleysLeft(t *testing.T) {
	for held := 0; held < maxEventStallVolleys; held++ {
		if !eventStallVolleysLeft(held) {
			t.Errorf("after %d stall volleys the sweep must still fire; a card holding troops must not be written off", held)
		}
	}
	if eventStallVolleysLeft(maxEventStallVolleys) {
		t.Errorf("after %d stall volleys the budget is spent; the slot must be reported failed, not fired at forever", maxEventStallVolleys)
	}
}
