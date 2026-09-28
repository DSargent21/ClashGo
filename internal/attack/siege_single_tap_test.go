package attack

import (
	"image"
	"sync"
	"testing"

	"github.com/Ducky705/ClashGO/internal/adb"
	"github.com/Ducky705/ClashGO/internal/game"
	"github.com/rs/zerolog"
)

// A siege machine deploys on its FIRST field tap and every tap after that lands
// on the machine now standing there, which destroys it. These tests pin the
// contract at the one place that can fire that tap (TapExecutor.PlaceSiege) and
// at each path that could otherwise fire it again (the card select, the sweep,
// the verifier's retry, the event-troop pass).

// tapProbe counts every tap an executor fires, in every shape it fires them:
// TapFast (spell/siege field drops, card selects) and TapTriple / TapDual (the
// batched troop passes). The spell tests' own tapLog deliberately records only
// tap_fast events, which makes it deaf to the troop passes — the wrong window on
// the paths that matter here.
// It also counts BATCHES, because the closed client is the limit on what a test
// can see: a batched call (TapTriple / TapDual) fires its hook per tap, but the
// client returns as soon as the first transport call fails, so only the first
// event of each batch is observable. Counting batches is therefore how a test
// counts the taps a batched pass asked for — 11 taps arrive as 3+3+3+2, i.e. four
// batch calls — without a device.
type tapProbe struct {
	mu      sync.Mutex
	field   []image.Point
	card    []image.Point
	batches int
}

func (p *tapProbe) record(ev adb.TapEvent) {
	p.mu.Lock()
	defer p.mu.Unlock()
	switch ev.Type {
	case "tap_fast", "tap_triple_1", "tap_dual_1":
		p.batches++
	}
	// Field taps are fired at stdDev >= 8; card taps at 2-4.
	if ev.StdDev > 4 {
		p.field = append(p.field, image.Pt(ev.X, ev.Y))
		return
	}
	p.card = append(p.card, image.Pt(ev.X, ev.Y))
}

func (p *tapProbe) fields() []image.Point {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]image.Point(nil), p.field...)
}

func (p *tapProbe) cards() []image.Point {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]image.Point(nil), p.card...)
}

func (p *tapProbe) all() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.field) + len(p.card)
}

// tapCalls is the number of tap calls the executor made, field or card, however
// many taps each one carried. See the type comment for why a batched call is
// counted by its call and not by its taps.
func (p *tapProbe) tapCalls() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.batches
}

// newTapProbe builds a TapExecutor whose taps are recorded but never sent: the
// adb client is closed, which fails every transport call fast while the tap hook
// still fires. Same seam the spell-deployer tests use.
func newTapProbe(t *testing.T) (*TapExecutor, *tapProbe) {
	t.Helper()
	client := adb.NewClient(
		adb.WithJitterTaps(false),
		adb.WithJitterDelays(false),
		adb.WithTimeout(1),
	)
	client.Close()

	p := &tapProbe{}
	client.SetTapHook(p.record)
	cal := &game.Calibration{PhysicalW: 860, PhysicalH: 732, ScaleX: 1.0, ScaleY: 1.0}
	return NewTapExecutor(client, cal, zerolog.Nop()), p
}

func siegeSlot(name string, x int) *TrackedSlot {
	return &TrackedSlot{
		TroopSlot: TroopSlot{X: x, Y: 682, Category: "Siege"},
		UnitName:  name,
	}
}

func TestPlaceSiege_FiresExactlyOneTapPerBattle(t *testing.T) {
	exec, probe := newTapProbe(t)
	slot := siegeSlot("Stone Slammer", 360)

	if !exec.PlaceSiege(slot, image.Pt(900, 300), 12.0) {
		t.Fatal("the first siege placement tap must fire")
	}
	if got := len(probe.fields()); got != 1 {
		t.Fatalf("field taps after the first placement = %d, want 1", got)
	}

	if exec.PlaceSiege(slot, image.Pt(905, 310), 12.0) {
		t.Fatal("a second siege placement tap must be refused")
	}
	if got := len(probe.fields()); got != 1 {
		t.Fatalf("field taps after a refused second placement = %d, want 1: a second tap destroys the deployed machine", got)
	}
	if !slot.SiegePlaced {
		t.Fatal("a placed siege slot must record that its one tap is spent")
	}
}

func TestPlaceSiege_CardSelectIsRefusedAfterPlacement(t *testing.T) {
	exec, probe := newTapProbe(t)
	slot := siegeSlot("Stone Slammer", 360)

	// The card select that precedes a placement is legal: it is how the deploy
	// phase arms the machine.
	if !exec.TapSlot(slot, 8) {
		t.Fatal("selecting the siege card before its placement must be allowed")
	}
	if got := len(probe.cards()); got != 1 {
		t.Fatalf("card taps before placement = %d, want 1", got)
	}

	if !exec.PlaceSiege(slot, image.Pt(900, 300), 12.0) {
		t.Fatal("the placement tap must fire")
	}
	if exec.TapSlot(slot, 8) {
		t.Fatal("re-selecting the siege card after placement must be refused: re-selecting it is the first half of a second deployment")
	}
	if got := len(probe.cards()); got != 1 {
		t.Fatalf("card taps after placement = %d, want 1 (only the pre-placement select)", got)
	}
}

func TestPlaceSiege_SecondSiegeSlotGetsItsOwnTap(t *testing.T) {
	exec, probe := newTapProbe(t)
	first := siegeSlot("Stone Slammer", 360)
	second := siegeSlot("Wall Wrecker", 460)

	if !exec.PlaceSiege(first, image.Pt(900, 300), 12.0) {
		t.Fatal("first machine's placement tap must fire")
	}
	if !exec.PlaceSiege(second, image.Pt(950, 320), 12.0) {
		t.Fatal("a different siege slot is a different machine and must get its own placement tap")
	}
	if got := len(probe.fields()); got != 2 {
		t.Fatalf("field taps = %d, want 2 (one per machine)", got)
	}
}

func TestSweeper_DoesNotTapAPlacedSiege(t *testing.T) {
	exec, probe := newTapProbe(t)
	slot := siegeSlot("Stone Slammer", 360)
	slot.SiegePlaced = true

	sw := &Sweeper{executor: exec, logger: zerolog.Nop()}
	// isEventTroop=true is the worst case: it is exactly the branch that used to
	// bypass the siege skip and re-deploy the card as a bonus troop.
	if !sw.deploySlot(slot, 5, true) {
		t.Fatal("a placed siege slot must close out as placed, not failed — a fault verdict sends the verifier after it")
	}
	if got := probe.all(); got != 0 {
		t.Fatalf("sweep fired %d taps at a placed siege machine, want 0", got)
	}
}

func TestVerifier_DoesNotRetryAPlacedSiege(t *testing.T) {
	exec, probe := newTapProbe(t)
	slot := siegeSlot("Stone Slammer", 360)
	slot.SiegePlaced = true

	v := &Verifier{executor: exec, w: 1280, h: 720, logger: zerolog.Nop()}
	v.retryDeploy(slot)
	if got := probe.all(); got != 0 {
		t.Fatalf("verifier retry fired %d taps at a placed siege machine, want 0", got)
	}
}

func TestGetEventTroops_ExcludesAPlacedSiege(t *testing.T) {
	placed := siegeSlot("Stone Slammer", 360)
	placed.SiegePlaced = true
	bonus := &TrackedSlot{TroopSlot: TroopSlot{X: 460, Y: 682, Category: "Troop"}}

	sm := &SlotManager{slots: []*TrackedSlot{placed, bonus}}
	got := sm.GetEventTroops(nil)

	for _, s := range got {
		if s.X == placed.X {
			t.Fatal("a placed siege machine must not be swept as an undeployed event troop")
		}
	}
	if len(got) != 1 || got[0].X != bonus.X {
		t.Fatalf("event troops = %d slots, want only the bonus troop at x=%d", len(got), bonus.X)
	}
}
