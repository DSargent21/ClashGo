package attack

import (
	"context"
	"testing"
	"time"

	"github.com/rs/zerolog"
)

// The deploy-time hero watch arms as heroes land and fires abilities from the
// deployment watcher's own frames. The contracts pinned here: Track dedupes
// and filters, a failed ability tap gives the one-shot flag back (revert), and a
// hero blocked by an ended battle never spends it.

func watchableHero(name string, x, slotY int, abilityFired bool) *TrackedSlot {
	return &TrackedSlot{
		TroopSlot:    TroopSlot{X: x, Y: slotY, Category: "Hero"},
		State:        SlotDeployed,
		UnitName:     name,
		AbilityFired: abilityFired,
	}
}

func TestArmHeroWatch_DedupesAndFilters(t *testing.T) {
	e := &Executor{logger: zerolog.Nop()}
	sm := &SlotManager{slots: []*TrackedSlot{
		watchableHero("archer_queen", 400, 682, false),
		watchableHero("warden", 500, 682, false),
		{TroopSlot: TroopSlot{X: 300, Y: 682, Category: "Troop"}, State: SlotDeployed, UnitName: "barb"},
	}}

	if n := e.armHeroWatch(sm); n != 2 {
		t.Fatalf("armed %d heroes, want 2", n)
	}
	// Re-arming (every phase does) must not duplicate the watch.
	if n := e.armHeroWatch(sm); n != 2 {
		t.Fatalf("re-arm produced %d heroes, want 2 (dedupe broken)", n)
	}

	e.clearHeroWatch()
	if e.heroWatch != nil {
		t.Fatal("clearHeroWatch left a watch behind")
	}
	if n := e.armHeroWatch(nil); n != 0 {
		t.Fatalf("nil slot manager armed %d heroes, want 0", n)
	}
}

func TestHeroTrack_IgnoresNonEligibleSlots(t *testing.T) {
	m := NewHeroHPMonitor(nil)

	eligible := watchableHero("queen", 400, 682, false)
	m.Track(eligible)
	m.Track(eligible) // same (X, SlotY): already watched
	if len(m.heroes) != 1 {
		t.Fatalf("tracked %d heroes, want 1", len(m.heroes))
	}

	filtered := []*TrackedSlot{
		// not a hero card
		{TroopSlot: TroopSlot{X: 300, Y: 682, Category: "Troop"}, State: SlotDeployed, UnitName: "barb"},
		// fallback label = bonus troop wearing a stale manual label
		{TroopSlot: TroopSlot{X: 310, Y: 682, Category: "Hero"}, State: SlotDeployed, UnitName: "ghost", FallbackLabeled: true},
		// not yet deployed
		{TroopSlot: TroopSlot{X: 320, Y: 682, Category: "Hero"}, State: SlotAttempted, UnitName: "wiz"},
		// ability already fired: nothing left for the watch to fire
		{TroopSlot: TroopSlot{X: 330, Y: 682, Category: "Hero"}, State: SlotDeployed, UnitName: "minion_king", AbilityFired: true},
	}
	for _, slot := range filtered {
		m.Track(slot)
	}
	if len(m.heroes) != 1 {
		t.Fatalf("filters leaked: %d heroes tracked, want 1", len(m.heroes))
	}
}

func heroWatchExecutor(t *testing.T) (*Executor, *tapProbe) {
	t.Helper()
	tap, probe := newTapProbe(t)
	e := &Executor{
		client: tap.client,
		cal:    tap.cal,
		logger: zerolog.Nop(),
	}
	return e, probe
}

// A tap that never reached the device must give the slot's ability flag back:
// AbilityFired goes false -> true -> false, and the caller is told the hero is
// unresolved so the next frame can retry.
func TestFireHeroAbility_TapFailureRevertsBudget(t *testing.T) {
	e, probe := heroWatchExecutor(t)
	slot := watchableHero("archer_queen", 400, 682, false)
	h := &WatchedHero{X: 400, SlotY: 682, Slot: slot}

	if e.fireHeroAbility(h) {
		t.Fatal("ability reported success against a closed client")
	}
	if slot.AbilityFired {
		t.Fatal("AbilityFired stayed set after a failed tap; the next frame would skip the hero")
	}
	if got := probe.cards(); len(got) != 1 || got[0].X != 400 || got[0].Y != 682 {
		t.Fatalf("recorded taps %v, want exactly one at (400,682)", got)
	}
}

// Second failed attempt: the budget is still honest and the tap fires again
// (retry, not skip).
func TestFireHeroAbility_RetriesWithoutOverspending(t *testing.T) {
	e, probe := heroWatchExecutor(t)
	slot := watchableHero("warden", 500, 682, false)
	h := &WatchedHero{X: 500, SlotY: 682, Slot: slot, Warden: true}

	for i := 1; i <= 3; i++ {
		if e.fireHeroAbility(h) {
			t.Fatalf("attempt %d reported success against a closed client", i)
		}
		if slot.AbilityFired {
			t.Fatalf("attempt %d: AbilityFired stayed set after a failed tap", i)
		}
	}
	if got := len(probe.cards()); got != 3 {
		t.Fatalf("fired %d taps across 3 attempts, want 3", got)
	}
}

// A canceled deployment (Stop or confirmed battle end) short-circuits before
// any tap: no transport call, no budget consumed.
func TestFireHeroAbility_SkipsWhenDeploymentEnded(t *testing.T) {
	e, probe := heroWatchExecutor(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	e.deployCtx = ctx

	slot := watchableHero("archer_queen", 400, 682, false)
	h := &WatchedHero{X: 400, SlotY: 682, Slot: slot}

	if e.fireHeroAbility(h) {
		t.Fatal("ability fired while the deployment context was canceled")
	}
	if slot.AbilityFired {
		t.Fatal("AbilityFired set on an ended deploy; nothing should have been spent")
	}
	if got := probe.cards(); len(got) != 0 {
		t.Fatalf("tapped %v despite an ended deployment", got)
	}
}

// The one-activation contract: an ability already fired elsewhere is never
// tapped a second time.
func TestFireHeroAbility_RefusesSpentBudget(t *testing.T) {
	e, probe := heroWatchExecutor(t)
	slot := watchableHero("archer_queen", 400, 682, true)
	h := &WatchedHero{X: 400, SlotY: 682, Slot: slot}

	if e.fireHeroAbility(h) {
		t.Fatal("ability fired with an exhausted tap budget")
	}
	if got := probe.cards(); len(got) != 0 {
		t.Fatalf("tapped %v with an exhausted budget", got)
	}
	if e.fireHeroAbility(nil) {
		t.Fatal("nil hero reported success")
	}
}

// End-to-end through Poll: an unresolved (failed) tap keeps the hero on the
// watch for the next frame instead of marking it done.
func TestPoll_RetriesHeroWhoseTapFailed(t *testing.T) {
	e, probe := heroWatchExecutor(t)
	const slotY = 682
	slot := watchableHero("archer_queen", 100, slotY, false)
	h := &WatchedHero{X: 100, SlotY: slotY, Slot: slot, DeployedAt: time.Now()}
	m := NewHeroHPMonitor([]*WatchedHero{h})

	// Low-HP frame: green only in 5 of the strip's 34 columns reads ~15%,
	// inside [heroHPMinAlive, heroHPThreshold) where the ability is due.
	low := paintStrip(200, 720, slotY-8, slotY-3, 83, 88, 40, 200, 60)
	defer low.Close()

	// First poll: the tap fails (closed client) — hero must stay unresolved.
	m.Poll(low, nil, time.Now(), e.fireHeroAbility)
	if h.Done || m.AllDone() {
		t.Fatal("failed tap marked the hero done")
	}
	if slot.AbilityFired {
		t.Fatal("AbilityFired stayed set after a failed tap")
	}

	// Second poll retries: another tap is fired, the flag still honest.
	m.Poll(low, nil, time.Now(), e.fireHeroAbility)
	if got := len(probe.cards()); got != 2 {
		t.Fatalf("fired %d taps over two polls, want 2 (retry happened)", got)
	}
	if slot.AbilityFired {
		t.Fatal("AbilityFired stayed set after the retry failed too")
	}
	if h.Done {
		t.Fatal("hero marked done without a delivered tap")
	}
}
