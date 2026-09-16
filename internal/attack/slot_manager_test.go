package attack

import (
	"strings"
	"testing"

	"github.com/rs/zerolog"
)

// newTestSlotManager builds a SlotManager with the given pre-assigned
// slots (unit names already resolved, e.g. by template matching) and no
// filesystem dependency — enough to exercise applyManualLabels.
func newTestSlotManager(slots []*TrackedSlot) *SlotManager {
	sm := &SlotManager{
		slots:     slots,
		unitIndex: make(map[string]*TrackedSlot),
		xIndex:    make(map[int]*TrackedSlot),
		logger:    zerolog.Nop(),
	}
	for _, s := range slots {
		sm.xIndex[s.X] = s
		if s.UnitName != "" {
			sm.unitIndex[strings.ToLower(s.UnitName)] = s
		}
	}
	return sm
}

// TestApplyManualLabels_SkipsDuplicateName guards against the live bug
// seen with valk_spam: manual_labels.json (calibrated on an old army)
// labels the unidentified Dragon Duke card as "Grand Warden" while the
// REAL warden slot was already identified by template matching. Without
// the dedup, "grand warden" resolves to the wrong card, the real hero
// never gets its main-phase deploy, and the mislabeled card is never
// swept (it looks deployed).
func TestApplyManualLabels_SkipsDuplicateName(t *testing.T) {
	// x=360 already identified as the warden via template match;
	// x=433 is an unidentified card (the Duke) that the stale manual
	// labels want to call "Grand Warden".
	warden := &TrackedSlot{TroopSlot: TroopSlot{X: 360, Y: 682}, UnitName: "grand warden", Confidence: 0.94}
	duke := &TrackedSlot{TroopSlot: TroopSlot{X: 433, Y: 682}}
	sm := newTestSlotManager([]*TrackedSlot{warden, duke})

	sm.applyManualLabels([]byte(`{
	  "slots": [
	    {"x": 62,  "name": "Valkyrie"},
	    {"x": 360, "name": "Dragon Duke"},
	    {"x": 433, "name": "Grand Warden"}
	  ]
	}`))

	if duke.UnitName != "" {
		t.Fatalf("stale fallback label duplicated a template-assigned name: duke slot got %q, want empty", duke.UnitName)
	}
	if duke.FallbackLabeled {
		t.Fatal("duplicate fallback should not have marked the slot fallback-labeled")
	}
	if warden.UnitName != "grand warden" {
		t.Fatalf("template-assigned warden was clobbered: got %q", warden.UnitName)
	}
}

// TestApplyManualLabels_FillsUnidentified ensures the fallback still
// labels genuinely unidentified slots (the valkyrie card has no
// template, so it must come from manual_labels.json).
func TestApplyManualLabels_FillsUnidentified(t *testing.T) {
	valk := &TrackedSlot{TroopSlot: TroopSlot{X: 62, Y: 682}}
	queen := &TrackedSlot{TroopSlot: TroopSlot{X: 212, Y: 682}, UnitName: "archer queen", Confidence: 0.95}
	sm := newTestSlotManager([]*TrackedSlot{valk, queen})

	sm.applyManualLabels([]byte(`{
	  "slots": [
	    {"x": 62,  "name": "Valkyrie"},
	    {"x": 134, "name": "Siege Machine"},
	    {"x": 212, "name": "Archer Queen"},
	    {"x": 433, "name": "Grand Warden"}
	  ]
	}`))

	if valk.UnitName != "valkyrie" {
		t.Fatalf("unidentified valkyrie slot not labeled: got %q, want %q", valk.UnitName, "valkyrie")
	}
	if !valk.FallbackLabeled {
		t.Fatal("fallback-labeled slot should carry FallbackLabeled=true so the sweep dumps it as an event troop")
	}
	if queen.UnitName != "archer queen" {
		t.Fatalf("template-assigned queen clobbered: got %q", queen.UnitName)
	}
}

// TestApplyManualLabels_IgnoresEmptyLabels ensures "Empty" entries and
// missing coordinates don't label anything.
func TestApplyManualLabels_IgnoresEmptyLabels(t *testing.T) {
	slot := &TrackedSlot{TroopSlot: TroopSlot{X: 730, Y: 682}}
	sm := newTestSlotManager([]*TrackedSlot{slot})

	sm.applyManualLabels([]byte(`{
	  "slots": [
	    {"x": 730, "name": "Empty"},
	    {"x": 999, "name": "Event Troop"}
	  ]
	}`))

	if slot.UnitName != "" {
		t.Fatalf("Empty/manual-only slot got labeled: %q", slot.UnitName)
	}
}

// TestApplyManualLabels_InvalidJSON silently no-ops like the production path.
func TestApplyManualLabels_InvalidJSON(t *testing.T) {
	slot := &TrackedSlot{TroopSlot: TroopSlot{X: 62, Y: 682}}
	sm := newTestSlotManager([]*TrackedSlot{slot})

	sm.applyManualLabels([]byte(`not json`))
	if slot.UnitName != "" {
		t.Fatalf("invalid config labeled a slot: %q", slot.UnitName)
	}
}
