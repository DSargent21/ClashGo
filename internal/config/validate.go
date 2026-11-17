package config

import (
	"fmt"
	"time"
)

// Validation limits shared by the App bindings and SaveFullConfig. They mirror
// what the React inputs allow, so a hand-crafted IPC call (or a future UI bug)
// cannot persist a value the UI itself would never offer.
const (
	MaxLootFloor       = 10_000_000
	MaxStallSeconds    = 600
	MaxArmySlot        = 4
	MaxReserveBuilders = 5
	MaxUpgradeSpend    = 1_000_000_000
)

var (
	upgradeStrategies = map[string]bool{
		"strategic_rush": true, "max": true, "heroes_first": true,
		"cheapest_first": true, "most_expensive_first": true, "balanced": true,
	}
	labStrategies = map[string]bool{
		"cheapest_first": true, "troops_first": true,
		"spells_first": true, "siege_first": true,
		"recommended": true,
	}
	wallResources = map[string]bool{"gold": true, "elixir": true, "both": true}
	haltRules     = map[string]bool{"ANY": true, "ALL": true, "ALL_3": true}
	knownPets     = func() map[string]bool {
		out := make(map[string]bool, len(DefaultPetPriorities()))
		for _, name := range DefaultPetPriorities() {
			out[name] = true
		}
		return out
	}()
)

// ValidateLootFloors rejects values the loot inputs cannot produce: negative
// floors or anything past the UI's 10M ceiling.
func ValidateLootFloors(gold, elixir, dark int) error {
	for _, f := range []struct {
		name string
		v    int
	}{{"gold", gold}, {"elixir", elixir}, {"dark elixir", dark}} {
		if f.v < 0 || f.v > MaxLootFloor {
			return fmt.Errorf("%s loot floor must be between 0 and %d, got %d", f.name, MaxLootFloor, f.v)
		}
	}
	return nil
}

// ValidateSearch covers the load-bearing search fields a full config save can
// carry in addition to the three floors.
func ValidateSearch(s SearchConfig) error {
	if err := ValidateLootFloors(s.MinLootGold, s.MinLootElixir, s.MinLootDarkElixir); err != nil {
		return err
	}
	if s.AttackIfDarkElixirGT < 0 {
		return fmt.Errorf("dark elixir trigger must be >= 0, got %d", s.AttackIfDarkElixirGT)
	}
	if s.MaxSearchSkips < 0 || s.MaxSearchSkips > 1000 {
		return fmt.Errorf("max search skips must be between 0 and 1000, got %d", s.MaxSearchSkips)
	}
	if s.TownHallLevel < 0 || s.TownHallLevel > 17 {
		return fmt.Errorf("town hall level must be between 0 and 17, got %d", s.TownHallLevel)
	}
	return nil
}

// ValidateAttack checks the deployment fields the config form writes.
func ValidateAttack(a AttackConfig) error {
	// 0 disables the stall timer: the engine resolves it that way and the form
	// offers it as "0 disables". Values 1-9 would abort a battle almost
	// immediately, so they stay rejected.
	if a.StallTimerSeconds != 0 && (a.StallTimerSeconds < 10 || a.StallTimerSeconds > MaxStallSeconds) {
		return fmt.Errorf("stall timer must be 0 (disabled) or between 10 and %d seconds, got %d", MaxStallSeconds, a.StallTimerSeconds)
	}
	if a.MinSecondsBetweenAttacks < 0 || a.MinSecondsBetweenAttacks > 3600 {
		return fmt.Errorf("minimum seconds between attacks must be between 0 and 3600, got %d", a.MinSecondsBetweenAttacks)
	}
	if a.QueenChargeAtPct < 0 || a.QueenChargeAtPct > 100 {
		return fmt.Errorf("queen charge percent must be between 0 and 100, got %d", a.QueenChargeAtPct)
	}
	if a.WardenUseAtPct < 0 || a.WardenUseAtPct > 100 {
		return fmt.Errorf("warden use percent must be between 0 and 100, got %d", a.WardenUseAtPct)
	}
	for name, slot := range a.StrategySlots {
		if slot < 1 || slot > MaxArmySlot {
			return fmt.Errorf("army slot for %q must be between 1 and %d, got %d", name, MaxArmySlot, slot)
		}
	}
	return nil
}

// ValidateStorage rejects contradictory halt settings: a rule the loop does not
// implement, a full threshold outside the UI range, or a resume point at/above
// the point that triggers the halt (which would flap on every tick).
func ValidateStorage(s StorageConfig) error {
	if !haltRules[s.HaltRule] {
		return fmt.Errorf("halt rule must be ANY, ALL or ALL_3, got %q", s.HaltRule)
	}
	if s.FullThresholdPercent < 50 || s.FullThresholdPercent > 100 {
		return fmt.Errorf("full storage threshold must be between 50 and 100%%, got %g", s.FullThresholdPercent)
	}
	if s.ResumeThresholdPercent < 0 || s.ResumeThresholdPercent >= s.FullThresholdPercent {
		return fmt.Errorf("resume threshold (%g%%) must be below the full threshold (%g%%)", s.ResumeThresholdPercent, s.FullThresholdPercent)
	}
	return nil
}

// ParseClock parses the HH:MM strings the time inputs emit. An empty value is
// an error: saving a schedule with no window used to keep the old one and
// report success, which looked like the form had been applied.
func ParseClock(label, value string) (int, error) {
	// time.Parse accepts "7:00"; the time inputs and every stored config use
	// the two-digit form, so require it explicitly rather than storing a mix.
	if len(value) != 5 || value[2] != ':' {
		return 0, fmt.Errorf("%s must be HH:MM, got %q", label, value)
	}
	t, err := time.Parse("15:04", value)
	if err != nil {
		return 0, fmt.Errorf("%s must be HH:MM, got %q", label, value)
	}
	return t.Hour()*60 + t.Minute(), nil
}

// ValidateSchedule checks the pacing form. start == end is rejected because it
// describes a zero-length or 24-hour window depending on interpretation, and
// the scheduler would disagree with the user either way.
func ValidateSchedule(s ScheduleConfig) error {
	start, err := ParseClock("active hours start", s.ActiveHoursStart)
	if err != nil {
		return err
	}
	end, err := ParseClock("active hours end", s.ActiveHoursEnd)
	if err != nil {
		return err
	}
	if start == end {
		return fmt.Errorf("active hours start and end must differ, both are %q", s.ActiveHoursStart)
	}
	if s.SessionDurationMinutes < 10 || s.SessionDurationMinutes > 360 {
		return fmt.Errorf("session duration must be between 10 and 360 minutes, got %d", s.SessionDurationMinutes)
	}
	if s.SessionJitterMinutes < 0 || s.SessionJitterMinutes > 240 {
		return fmt.Errorf("session jitter must be between 0 and 240 minutes, got %d", s.SessionJitterMinutes)
	}
	if s.BreakDurationMinutes < 5 || s.BreakDurationMinutes > 180 {
		return fmt.Errorf("break duration must be between 5 and 180 minutes, got %d", s.BreakDurationMinutes)
	}
	if s.BreakJitterMinutes < 0 || s.BreakJitterMinutes > 240 {
		return fmt.Errorf("break jitter must be between 0 and 240 minutes, got %d", s.BreakJitterMinutes)
	}
	if s.MaxAttacksPerHour < 1 || s.MaxAttacksPerHour > 60 {
		return fmt.Errorf("max attacks per hour must be between 1 and 60, got %d", s.MaxAttacksPerHour)
	}
	if s.MaxAttacksPerDay < 1 || s.MaxAttacksPerDay > 300 {
		return fmt.Errorf("max attacks per day must be between 1 and 300, got %d", s.MaxAttacksPerDay)
	}
	if s.MaxAttacksPerDay < s.MaxAttacksPerHour {
		return fmt.Errorf("max attacks per day (%d) cannot be lower than per hour (%d)", s.MaxAttacksPerDay, s.MaxAttacksPerHour)
	}
	return nil
}

// ValidateUpgrade checks the builder/lab/pet form. Unknown enum values used to
// be dropped silently ("keep the old one") and unknown pets were accepted into
// a queue the bot would then try to select in-game.
func ValidateUpgrade(u UpgradeConfig) error {
	if !upgradeStrategies[u.UpgradeStrategy] {
		return fmt.Errorf("unknown upgrade strategy %q", u.UpgradeStrategy)
	}
	if !labStrategies[u.LabStrategy] {
		return fmt.Errorf("unknown lab strategy %q", u.LabStrategy)
	}
	if !wallResources[u.WallResource] {
		return fmt.Errorf("wall resource must be gold, elixir or both, got %q", u.WallResource)
	}
	if u.ReserveBuilders < 0 || u.ReserveBuilders > MaxReserveBuilders {
		return fmt.Errorf("reserved builders must be between 0 and %d, got %d", MaxReserveBuilders, u.ReserveBuilders)
	}
	for _, c := range []struct {
		name string
		v    int
	}{
		{"gold", u.MaxGoldPerUpgrade},
		{"elixir", u.MaxElixirPerUpgrade},
		{"dark elixir", u.MaxDEPerUpgrade},
	} {
		if c.v < 0 || c.v > MaxUpgradeSpend {
			return fmt.Errorf("max %s per upgrade must be between 0 and %d, got %d", c.name, MaxUpgradeSpend, c.v)
		}
	}
	return ValidatePetQueue(u.PetPriorities)
}

// ValidatePetQueue rejects pet names the game does not have and duplicates:
// the bot iterates this list to pick a hero pet, so a stale or repeated name
// silently burns the pet slot every cycle.
func ValidatePetQueue(pets []string) error {
	seen := make(map[string]bool, len(pets))
	for _, pet := range pets {
		if !knownPets[pet] {
			return fmt.Errorf("unknown pet %q in the upgrade queue", pet)
		}
		if seen[pet] {
			return fmt.Errorf("pet %q is queued more than once", pet)
		}
		seen[pet] = true
	}
	return nil
}

// Validate is the whole-config gate used by SaveFullConfig. It walks the
// sections the config form can write; byte-level fields (debug, notify,
// performance) are owned by the CLI and file editing, not this form.
func Validate(cfg *BotConfig) error {
	if cfg == nil {
		return fmt.Errorf("config is nil")
	}
	if err := ValidateSearch(cfg.Search); err != nil {
		return err
	}
	if err := ValidateAttack(cfg.Attack); err != nil {
		return err
	}
	if err := ValidateStorage(cfg.Storage); err != nil {
		return err
	}
	if err := ValidateSchedule(cfg.Schedule); err != nil {
		return err
	}
	if err := ValidateUpgrade(cfg.Upgrade); err != nil {
		return err
	}
	return nil
}
