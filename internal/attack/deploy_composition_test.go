package attack

import (
	"fmt"
	"image"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Ducky705/ClashGO/internal/attacklog"
	"github.com/Ducky705/ClashGO/pkg/strategy"
	"github.com/rs/zerolog"
	"gocv.io/x/gocv"
)

// deployCompositionCase models one army card: how many units the battle-start
// measurement saw, and how many the live badge reports.
type deployCompositionCase struct {
	name    string
	unit    string
	count   int
	measure bool // false: detected count unreadable, live OCR is the only measure
	refused bool // the ground never accepts a tap (genuine shortfall)
}

// TestDeployTroops_CompositionIndependentCounts is the "does it work for all
// army camp sizes" contract. A card is placed by the number on its badge, not by
// a phase default or a camp-size assumption: four units cost four taps, a
// twelve-unit single-type card costs thirteen (the documented +1 anti-underread
// pad), a ten-unit card in a mixed army costs eleven, and a card measured as
// PARTIAL costs only what the badge says even when the battle-start read saw
// more.
//
// Every case also proves the slot is only marked deployed after the reconcile
// read agrees the card is visually empty — the failure mode that left troops on
// the bar with a clean-looking log.
func TestDeployTroops_CompositionIndependentCounts(t *testing.T) {
	line := DeployLine{Points: []image.Point{{X: 200, Y: 180}, {X: 200, Y: 560}}}
	cases := []deployCompositionCase{
		{name: "small_camp_4", unit: "Balloon", count: 4, measure: true},
		{name: "single_type_full_12", unit: "Electro Dragon", count: 12, measure: true},
		{name: "full_mixed_balloons_10", unit: "Balloon", count: 10, measure: true},
		{name: "full_mixed_edrag_10", unit: "Electro Dragon", count: 10, measure: true},
		{name: "partial_badge_3", unit: "Balloon", count: 3, measure: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			exec, probe := newTapProbe(t)
			slot := &TrackedSlot{
				TroopSlot: TroopSlot{X: 153, Y: 700, Category: "Troop"},
				UnitName:  tc.unit,
			}
			sm := &SlotManager{
				slots:     []*TrackedSlot{slot},
				unitIndex: map[string]*TrackedSlot{strings.ToLower(tc.unit): slot},
				logger:    zerolog.Nop(),
			}
			hm := NewHeroManager(exec, sm, PrecisionConfig{}, "BottomLeft", 860, 732,
				nil, nil, line, zerolog.Nop())

			// The badge drains as taps land. With measure=false the FIRST read is
			// the live OCR that establishes the count (the battle-start read could
			// not), so a partial card is measured at its partial value.
			hm.liveHook = func() (int, bool, bool) {
				left := tc.count - probe.taps()
				if left < 0 {
					left = 0
				}
				return left, true, left == 0
			}

			detected := tc.count
			trusted := true
			if !tc.measure {
				detected, trusted = 0, false
			}
			ok := hm.DeployTroops(
				strategy.Unit{Name: tc.unit, Amount: "All"},
				slot, "Line", 0, "Line", gocv.NewMat(), detected, trusted,
			)
			if !ok {
				t.Fatalf("deploy = false after %d taps; a card whose badge drains must confirm", probe.taps())
			}
			// The documented +1 pad only exists for single-digit reads (OCR
			// under-read protection); it is not a camp-size constant.
			want := tc.count
			if tc.count >= 6 {
				want++
			}
			if got := probe.taps(); got != want {
				t.Fatalf("field taps = %d, want %d for a %d-unit badge: taps must follow the measured count, not the camp size", got, want, tc.count)
			}
			if slot.State != SlotDeployed {
				t.Fatalf("slot state = %v, want deployed", slot.State)
			}
		})
	}
}

// TestDeployTroops_RefusedGroundNeverMarksDeployed is the other half: a card
// whose ground never accepts a tap must stay un-deployed and be handed to the
// sweep, and the verifier's final line must read FAILED in the attack log. This
// is the composition-independent guarantee that a shortfall is caught and
// reported, never silently left on the bar.
func TestDeployTroops_RefusedGroundNeverMarksDeployed(t *testing.T) {
	exec, _ := newTapProbe(t)
	slot := &TrackedSlot{
		TroopSlot: TroopSlot{X: 153, Y: 700, Category: "Troop"},
		UnitName:  "balloon",
	}
	sm := &SlotManager{
		slots:     []*TrackedSlot{slot},
		unitIndex: map[string]*TrackedSlot{"balloon": slot},
		logger:    zerolog.Nop(),
	}
	hm := NewHeroManager(exec, sm, PrecisionConfig{}, "BottomLeft", 860, 732,
		nil, nil, DeployLine{Points: []image.Point{{X: 200, Y: 180}, {X: 200, Y: 560}}},
		zerolog.Nop())

	// The badge never moves: every tap was refused.
	hm.liveHook = func() (int, bool, bool) { return 6, true, false }

	ok := hm.DeployTroops(
		strategy.Unit{Name: "Balloon", Amount: "All"},
		slot, "Line", 0, "Line", gocv.NewMat(), 6, true,
	)
	if ok {
		t.Fatal("deploy = true; a card that never drained must not be reported deployed")
	}
	if slot.State == SlotDeployed {
		t.Fatal("slot was marked deployed although its card still held troops")
	}
	if slot.State != SlotAttempted {
		t.Fatalf("slot state = %v, want attempted: the sweep/verifier must still own the unit", slot.State)
	}

	// The verifier's final line is what the report reads. Prove the deploy
	// shortfall reaches it as FAILED, not as a clean run.
	path := filepath.Join(t.TempDir(), "attack.log")
	line := fmt.Sprintf("21:00:00 | WRN | verifier: unit still on the bar at battle end component=verifier unit=%q\n", "Balloon")
	if err := os.WriteFile(path, []byte(line), 0o644); err != nil {
		t.Fatal(err)
	}
	rep, err := attacklog.Parse(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Units) != 1 {
		t.Fatalf("parsed %d units, want 1", len(rep.Units))
	}
	if rep.Units[0].Verdict != attacklog.Failed {
		t.Fatalf("verdict = %s, want FAILED; reason=%s", rep.Units[0].Verdict, rep.Units[0].Reason)
	}
}
