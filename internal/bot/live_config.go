package bot

import (
	"github.com/Ducky705/ClashGO/internal/config"
	"github.com/rs/zerolog"
)

// LiveConfig is the configuration the running loop and its config-owning
// sub-managers currently hold. It exists so a wiring check can prove that a
// settings change reached the object the loop reads, instead of only proving
// that a value reached config.json.
//
// The pointers are nil when the corresponding owner is not built (a headless
// wiring bot has no attack executor, for example).
type LiveConfig struct {
	// Config is the pointer the capture loop, storage gate, wall upgrade and
	// scheduler gate read directly through b.cfg().
	Config config.BotConfig
	// ArmySlot is the recipe slot resolved from Attack.StrategySlots and the
	// strategy YAML — what the train menu will arm.
	ArmySlot int
	// Scheduler is the copy the scheduler gate (CanAttack) uses.
	Scheduler *config.ScheduleConfig
	// Upgrade is the copy the builder manager's filters and pet/lab selection
	// use (BuilderManager.cfg).
	Upgrade *config.UpgradeConfig
}

// LiveConfig snapshots the effective configuration. Every part is read through
// the owner's own publication mechanism (atomic pointer or mutex-guarded copy),
// so it is safe to call while a save is in flight.
func (b *Bot) LiveConfig() LiveConfig {
	out := LiveConfig{}
	if cfg := b.cfg(); cfg != nil {
		out.Config = *cfg
		out.ArmySlot = int(b.armySlot.Load())
	}
	if b.scheduler != nil {
		cfg := b.scheduler.Config()
		out.Scheduler = &cfg
	}
	if b.builderManager != nil {
		cfg := b.builderManager.settings()
		out.Upgrade = &cfg
	}
	return out
}

// NewHeadlessBot builds a Bot that owns cfg and the same config-consuming
// sub-managers a booted bot builds, without dialing ADB or launching the game.
// UpdateConfig then takes the identical code path it takes during a run, so a
// wiring check can assert that a UI change reaches the scheduler gate and the
// builder manager filters. It cannot run a session: Start needs a device.
func NewHeadlessBot(cfg *config.BotConfig) *Bot {
	b := &Bot{logger: zerolog.Nop()}
	if cfg == nil {
		return b
	}
	b.cfgPtr.Store(cfg)
	b.armySlot.Store(int32(resolveArmySlot(cfg)))
	b.scheduler = NewScheduler(cfg.Schedule, b.logger)
	b.builderManager = NewBuilderManager(nil, nil, cfg.Upgrade, b.logger)
	return b
}
