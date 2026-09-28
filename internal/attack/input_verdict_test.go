package attack

import (
	"errors"
	"testing"
)

// The sticky input verdict is the deploy's "delivery uncertain" contract: the
// FIRST definite tap transport failure is recorded for the battle, every
// later failure is ignored, and StartDeployBudget clears it. The last
// non-hero selection is what a post-ability RestoreSelection re-taps.

func verdictSlot(name, category string, x, y int) *TrackedSlot {
	return &TrackedSlot{
		TroopSlot: TroopSlot{X: x, Y: y, Category: category},
		UnitName:  name,
	}
}

func TestNoteInput_KeepsFirstFailureAndResetsPerBattle(t *testing.T) {
	tap, _ := newTapProbe(t)

	first := errors.New("first transport failure")
	second := errors.New("second transport failure")

	tap.noteInput(first, "slot select")
	tap.noteInput(second, "field tap")
	if !errors.Is(tap.InputErr(), first) {
		t.Fatalf("InputErr() = %v, want the FIRST failure", tap.InputErr())
	}
	if errors.Is(tap.InputErr(), second) {
		t.Fatalf("later failure overwrote the sticky verdict: %v", tap.InputErr())
	}

	tap.StartDeployBudget()
	if tap.InputErr() != nil {
		t.Fatalf("StartDeployBudget did not clear the verdict: %v", tap.InputErr())
	}
	tap.noteInput(nil, "no-op")
	if tap.InputErr() != nil {
		t.Fatalf("nil error recorded a verdict: %v", tap.InputErr())
	}
}

// A definite tap failure must stick for the battle so the orchestrator stops
// at the next checkpoint instead of blind-retrying an uncertain delivery.
func TestTapSlot_TapFailureBecomesStickyVerdict(t *testing.T) {
	tap, probe := newTapProbe(t)
	tap.StartDeployBudget()

	if !tap.TapSlot(verdictSlot("barb", "Troop", 100, 682), 0) {
		t.Fatal("TapSlot refused a troop slot")
	}
	if tap.InputErr() == nil {
		t.Fatal("closed-client tap did not record a transport verdict")
	}
	if got := probe.tapCalls(); got != 1 {
		t.Fatalf("fired %d taps, want 1", got)
	}
}

// TapField is the choke point for spell casts (which used to call the client
// directly); it must feed the same sticky verdict as every other path.
func TestTapField_FeedsStickyVerdict(t *testing.T) {
	tap, probe := newTapProbe(t)
	tap.StartDeployBudget()

	if err := tap.TapField(200, 300, 12.0); err == nil {
		t.Fatal("TapField on a closed client returned nil error")
	}
	if tap.InputErr() == nil {
		t.Fatal("TapField did not record the transport verdict")
	}
	if got := probe.tapCalls(); got != 1 {
		t.Fatalf("fired %d taps, want 1", got)
	}
}

// TapSlot records the last NON-hero selection for RestoreSelection; a hero
// slot selection must never replace it (the restore exists precisely to get
// back to a troop/spell card after a hero ability re-selects the hero).
func TestRestoreSelection_ReTapsLastNonHeroSlotOnly(t *testing.T) {
	tap, probe := newTapProbe(t)
	tap.StartDeployBudget()

	troop := verdictSlot("wizard", "Troop", 300, 682)
	hero := verdictSlot("archer_queen", "Hero", 500, 682)

	// Nothing selected yet: restore must not fire blindly.
	if tap.RestoreSelection() {
		t.Fatal("RestoreSelection succeeded with no recorded selection")
	}
	if got := probe.cards(); len(got) != 0 {
		t.Fatalf("restore tapped %v with no selection recorded", got)
	}

	tap.TapSlot(troop, 0)
	tap.TapSlot(hero, 0) // consumes the hero's placement tap, must not become the restore target

	// Transport is closed, so the restore attempt cannot SUCCEED — but it
	// must still fire, and aim at the troop slot, never the hero card.
	if tap.RestoreSelection() {
		t.Fatal("restore succeeded against a closed client")
	}

	cards := probe.cards()
	if len(cards) != 3 {
		t.Fatalf("recorded %d card taps, want 3 (troop, hero, restore): %v", len(cards), cards)
	}
	if want := troop.TroopSlot; cards[0].X != want.X || cards[0].Y != want.Y {
		t.Fatalf("troop selection tapped %v, want %v", cards[0], want)
	}
	if want := hero.TroopSlot; cards[1].X != want.X || cards[1].Y != want.Y {
		t.Fatalf("hero selection tapped %v, want %v", cards[1], want)
	}
	if want := troop.TroopSlot; cards[2].X != want.X || cards[2].Y != want.Y {
		t.Fatalf("restore tapped %v, want the last non-hero slot %v", cards[2], want)
	}
}

// Only a hero selection ever made: RestoreSelection must refuse rather than
// tap a stale or hero point.
func TestRestoreSelection_NeverTargetsHeroSlot(t *testing.T) {
	tap, probe := newTapProbe(t)
	tap.StartDeployBudget()

	hero := verdictSlot("warden", "Hero", 700, 682)
	tap.TapSlot(hero, 0)

	if tap.RestoreSelection() {
		t.Fatal("restore succeeded with only a hero selection recorded")
	}
	if got := probe.cards(); len(got) != 1 {
		t.Fatalf("restore fired an extra tap at %v; want only the hero selection", got)
	}
	if tap.InputErr() == nil {
		t.Fatal("hero card tap on a closed client should record a verdict")
	}
}
