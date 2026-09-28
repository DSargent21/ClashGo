package attack

import (
	"context"
	"testing"
	"time"

	"github.com/rs/zerolog"
)

// The deploy-time hero watch arms as heroes land and fires abilities from the
// deployment watcher's own frames. The contracts pinned here: Track dedupes
// and filters, a failed ability tap keeps the 2-tap budget honest (revert),
// and a hero blocked by an ended battle never consumes its budget.

func watchableHero(name string, x, slotY int, spotTaps int) *TrackedSlot {
	return &TrackedSlot{
		TroopSlot: TroopSlot{X: x, Y: slotY, Category: "Hero"},
		State:     SlotDeployed,
		UnitName:  name,
		SpotTaps:  spotTaps,
	}
}

func TestArmHeroWatch_DedupesAndFilters(t *testing.T) {
	e := &Executor{logger: zerolog.Nop()}
	sm := &SlotManager{slots: []*TrackedSlot{
		watchableHero("archer_queen", 400, 682, 1),
		watchableHero("warden", 500, 682, 1),
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

	eligible := watchableHero("queen", 400, 682, 1)
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
		// 2-tap budget already spent: no ability left to fire
		{TroopSlot: TroopSlot{X: 330, Y: 682, Category: "Hero"}, State: SlotDeployed, UnitName: "minion_king", SpotTaps: maxHeroSpotTaps},
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

// A tap that never reached the device must give the slot's ability tap back:
// SpotTaps goes 1 -> 2 -> 1, and the caller is told the hero is unresolved so
// the next frame can retry.
func TestFireHeroAbility_TapFailureRevertsBudget(t *testing.T) {
	e, probe := heroWatchExecutor(t)
	slot := watchableHero("archer_queen", 400, 682, 1)
	h := &WatchedHero{X: 400, SlotY: 682, Slot: slot}

	if e.fireHeroAbility(h) {
		t.Fatal("ability reported success against a closed client")
	}
	if slot.SpotTaps != 1 {
		t.Fatalf("SpotTaps = %d after a failed tap, want it reverted to 1", slot.SpotTaps)
	}
	if got := probe.cards(); len(got) != 1 || got[0].X != 400 || got[0].Y != 682 {
		t.Fatalf("recorded taps %v, want exactly one at (400,682)", got)
	}
}

// Second failed attempt: the budget is still honest and the tap fires again
// (retry, not skip).
func TestFireHeroAbility_RetriesWithoutOverspending(t *testing.T) {
	e, probe := heroWatchExecutor(t)
	slot := watchableHero("warden", 500, 682, 1)
	h := &WatchedHero{X: 500, SlotY: 682, Slot: slot, Warden: true}

	for i := 1; i <= 3; i++ {
		if e.fireHeroAbility(h) {
			t.Fatalf("attempt %d reported success against a closed client", i)
		}
		if slot.SpotTaps != 1 {
			t.Fatalf("attempt %d: SpotTaps = %d, want 1 (never overspend)", i, slot.SpotTaps)
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

	slot := watchableHero("archer_queen", 400, 682, 1)
	h := &WatchedHero{X: 400, SlotY: 682, Slot: slot}

	if e.fireHeroAbility(h) {
		t.Fatal("ability fired while the deployment context was canceled")
	}
	if slot.SpotTaps != 1 {
		t.Fatalf("SpotTaps = %d, want 1 (nothing consumed on an ended deploy)", slot.SpotTaps)
	}
	if got := probe.cards(); len(got) != 0 {
		t.Fatalf("tapped %v despite an ended deployment", got)
	}
}

// The 2-tap contract's hard ceiling: an ability already spent elsewhere is
// never tapped a third time.
func TestFireHeroAbility_RefusesSpentBudget(t *testing.T) {
	e, probe := heroWatchExecutor(t)
	slot := watchableHero("archer_queen", 400, 682, maxHeroSpotTaps)
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
	slot := watchableHero("archer_queen", 100, slotY, 1)
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
	if slot.SpotTaps != 1 {
		t.Fatalf("SpotTaps = %d after failed tap, want 1", slot.SpotTaps)
	}

	// Second poll retries: another tap is fired, budget still honest.
	m.Poll(low, nil, time.Now(), e.fireHeroAbility)
	if got := len(probe.cards()); got != 2 {
		t.Fatalf("fired %d taps over two polls, want 2 (retry happened)", got)
	}
	if slot.SpotTaps != 1 {
		t.Fatalf("SpotTaps = %d after retry, want 1", slot.SpotTaps)
	}
	if h.Done {
		t.Fatal("hero marked done without a delivered tap")
	}
}
