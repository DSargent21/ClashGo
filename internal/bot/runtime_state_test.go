package bot

import (
	"testing"
	"time"

	"github.com/Ducky705/ClashGO/internal/config"
	"github.com/rs/zerolog"
)

func newGateTestBot(cfg *config.BotConfig) *Bot {
	return &Bot{cfg: cfg, logger: zerolog.Nop()}
}

func TestTrainingDueThrottle(t *testing.T) {
	now := time.Now()
	if !trainingDue(time.Time{}, now, trainingRetryInterval) {
		t.Fatal("a zero last-attempt must be due: the first empty army has to queue immediately")
	}
	if trainingDue(now.Add(-time.Minute), now, trainingRetryInterval) {
		t.Fatal("one minute after a queue attempt must not be due again (3 min throttle)")
	}
	if !trainingDue(now.Add(-trainingRetryInterval), now, trainingRetryInterval) {
		t.Fatal("an attempt exactly one interval old is due")
	}
}

// A full village may only latch the storage_full halt when something can spend
// the loot down. The shipped defaults enable no spender, so a fresh CLI install
// that latched it stopped farming for good: nothing in the bot drains storages
// on its own, and no later frame could reopen the gate.
func TestStorageHaltNeedsASpender(t *testing.T) {
	cfg := config.DefaultConfig()
	if storageHaltApplies(cfg) {
		t.Fatal("DefaultConfig reports a spender; a fresh CLI install would latch storage_full forever")
	}

	cfg.Upgrade.AutoBuilders = true
	if !storageHaltApplies(cfg) {
		t.Fatal("builders enabled must let the halt wait for the drain")
	}

	cfg.Upgrade.AutoBuilders = false
	cfg.Upgrade.UpgradeWalls = true
	cfg.Storage.DumpToWallsBeforeHalt = true
	if !storageHaltApplies(cfg) {
		t.Fatal("wall dump enabled must count as a spender")
	}

	cfg.Storage.DumpToWallsBeforeHalt = false
	if storageHaltApplies(cfg) {
		t.Fatal("walls enabled but the dump is off: nothing spends at the halt, so it must not latch")
	}
}

func TestEffectiveHaltPriority(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Schedule.Enabled = false // no scheduler: this test is about the other gates
	cfg.Attack.MaxAttackPerSession = 0

	b := newGateTestBot(cfg)
	if got := b.HaltedReason(); got != HaltNone {
		t.Fatalf("idle bot halted with %q, want none", got)
	}

	b.storageHalted.Store(true)
	if got := b.HaltedReason(); got != HaltStorageFull {
		t.Fatalf("full storages reported %q, want %q", got, HaltStorageFull)
	}

	// Storage outranks the army latch: it is the gate that costs loot.
	b.armyEmpty.Store(true)
	if got := b.HaltedReason(); got != HaltStorageFull {
		t.Fatalf("storage+army reported %q, want storage first", got)
	}

	b.storageHalted.Store(false)
	if got := b.HaltedReason(); got != HaltArmyEmpty {
		t.Fatalf("empty army reported %q, want %q", got, HaltArmyEmpty)
	}

	b.armyEmpty.Store(false)
	cfg.Attack.MaxAttackPerSession = 2
	b.attackCount.Store(2)
	if got := b.HaltedReason(); got != HaltCap {
		t.Fatalf("reached cap reported %q, want %q", got, HaltCap)
	}
}

// A halted village must never look like a hang: that is what made the old
// watchdog restart the game every 35 s on a full-storage village.
func TestIntentionalIdleOnlyInVillage(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Schedule.Enabled = false

	b := newGateTestBot(cfg)
	b.storageHalted.Store(true)

	if !b.intentionalIdle(true) {
		t.Fatal("a full-storage village with the attack gate closed is an intentional idle")
	}
	if b.intentionalIdle(false) {
		t.Fatal("the same halt must not excuse a stale action on a non-village screen")
	}
}

func TestSchedulerCountersSurviveRestart(t *testing.T) {
	t.Setenv("CLASHGO_CONFIG_DIR", t.TempDir())

	schedCfg := config.ScheduleConfig{
		Enabled:           true,
		MaxAttacksPerHour: 8,
		MaxAttacksPerDay:  50,
	}
	first := NewScheduler(schedCfg, zerolog.Nop())
	now := time.Now()
	first.RecordAttack(now)
	first.RecordAttack(now)

	botA := &Bot{cfg: config.DefaultConfig(), scheduler: first, logger: zerolog.Nop()}
	botA.persistSchedulerCounters()

	second := NewScheduler(schedCfg, zerolog.Nop())
	botB := &Bot{cfg: config.DefaultConfig(), scheduler: second, logger: zerolog.Nop()}
	botB.restoreSchedulerCounters()

	today, cap := second.AttacksToday()
	if today != 2 || cap != 50 {
		t.Fatalf("restored attacks today = %d/%d, want 2/50", today, cap)
	}
	if daily, hourly, _ := second.Counters(); daily != 2 || len(hourly) != 2 {
		t.Fatalf("restored counters daily=%d hourly=%d, want 2 and 2", daily, len(hourly))
	}
}

func TestRestoreCountersDropsStaleDayAndOldTimestamps(t *testing.T) {
	schedCfg := config.ScheduleConfig{Enabled: true, MaxAttacksPerDay: 50}
	s := NewScheduler(schedCfg, zerolog.Nop())

	stale := time.Now().AddDate(0, 0, -3).Format("2006-01-02")
	old := time.Now().Add(-3 * time.Hour)
	s.RestoreCounters(9, stale, []time.Time{old, time.Now()})

	today, _ := s.AttacksToday()
	if today != 0 {
		t.Fatalf("a counter from another day must be dropped, got %d", today)
	}
	if _, hourly, _ := s.Counters(); len(hourly) != 1 {
		t.Fatalf("hourly window kept %d timestamps, want only the in-window one", len(hourly))
	}
}

func TestSessionStateFileIsAtomicAndParseable(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLASHGO_CONFIG_DIR", dir)

	want := sessionState{
		DailyAttacks:  5,
		DailyDate:     localDay(time.Now()),
		HourlyAttacks: []string{time.Now().Format(time.RFC3339Nano)},
	}
	if err := saveSessionState(want); err != nil {
		t.Fatalf("saveSessionState: %v", err)
	}
	got, err := loadSessionState()
	if err != nil {
		t.Fatalf("loadSessionState: %v", err)
	}
	if got.DailyAttacks != 5 || got.DailyDate != want.DailyDate || len(got.HourlyAttacks) != 1 {
		t.Fatalf("round trip lost data: %+v", got)
	}
	if got.UpdatedAt == "" {
		t.Fatal("UpdatedAt should be stamped on save")
	}
}

func TestTownHallIsNeverInferred(t *testing.T) {
	if cat := classifyItemCategory("Town Hall"); cat != "townhall" {
		t.Fatalf("classifyItemCategory(Town Hall) = %q, want townhall", cat)
	}

	bm := &BuilderManager{cfg: config.UpgradeConfig{
		UpgradeStrategy:  "max",
		UpgradeDefenses:  true,
		UpgradeResources: true,
		UpgradeArmy:      true,
		UpgradeTraps:     true,
		UpgradeHeroes:    true,
	}}
	if bm.isCategoryAllowed("townhall") {
		t.Fatal("Town Hall must be excluded while allow_town_hall_upgrade is off")
	}

	items := []UpgradeItem{
		{Name: "Town Hall", Category: "townhall", Cost: 99000000, Currency: "gold"},
		{Name: "Cannon", Category: "defenses", Cost: 100, Currency: "gold"},
	}
	best, ok := bm.selectBuildingUpgrade(items)
	if !ok || best.Name != "Cannon" {
		t.Fatalf("max strategy picked %q (ok=%v); the Town Hall must be skipped", best.Name, ok)
	}

	bm.cfg.AllowTownHall = true
	if !bm.isCategoryAllowed("townhall") {
		t.Fatal("the explicit switch must enable Town Hall upgrades")
	}
}

func TestHeroPrioritiesDriveStrategicRush(t *testing.T) {
	base := config.UpgradeConfig{
		UpgradeStrategy: "strategic_rush",
		UpgradeHeroes:   true,
	}
	items := []UpgradeItem{
		{Name: "Archer Queen", Category: "heroes", Currency: "dark_elixir"},
		{Name: "Barbarian King", Category: "heroes", Currency: "dark_elixir"},
	}

	withDefault := &BuilderManager{cfg: base}
	if best, ok := withDefault.selectBuildingUpgrade(items); !ok || best.Name != "Archer Queen" {
		t.Fatalf("default queue picked %q (ok=%v), want Archer Queen", best.Name, ok)
	}

	custom := base
	custom.HeroPriorities = []string{"Barbarian King", "Archer Queen"}
	withCustom := &BuilderManager{cfg: custom}
	if best, ok := withCustom.selectBuildingUpgrade(items); !ok || best.Name != "Barbarian King" {
		t.Fatalf("custom queue picked %q (ok=%v), want Barbarian King", best.Name, ok)
	}
}
