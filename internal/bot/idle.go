// Package bot — idle.go
//
// Everything the bot does between battles, in one place.
//
// It used to live at the bottom of processFrame, *after* the "attack button
// detected" branch — which returns. So every frame where the Attack button was
// on screen (i.e. every frame in the village) skipped the maintenance work
// entirely: the storage gate refused the attack, the branch returned, and the
// upgrade/collect/wander blocks below it were unreachable. A full village with
// maxed walls therefore produced no input at all, forever.
//
// The maintenance pass now runs before the attack decision, owns the storage
// verdict the attack path reads, and is the only place that decides "this
// village is fine, we are just not attacking right now".
package bot

import (
	"time"

	"github.com/Ducky705/ClashGO/internal/config"
	"github.com/Ducky705/ClashGO/internal/game"
	"gocv.io/x/gocv"
)

const (
	// storageGateInterval bounds how often the storage fill is re-read. The
	// fill is measured from the village frame the capture loop already holds,
	// and storages move slowly (a full collector is minutes of farming), so
	// half a minute is far finer than the signal needs while keeping the
	// per-frame cost at zero.
	storageGateInterval = 30 * time.Second

	// villageUpgradeInterval and villageCollectInterval keep the maintenance
	// taps sparse: the account should look like a player checking in, not a
	// machine sweeping the village every few seconds.
	villageUpgradeInterval = 10 * time.Minute
	villageCollectInterval = 5 * time.Minute
	villagePanInterval     = 18 * time.Second
)

// maintainVillage is the between-battle pass. It returns true when it dispatched
// an army-training queue, in which case the caller must not start an attack in
// the same frame (the training path is standing in the attack panel).
func (b *Bot) maintainVillage(gc *game.GameContext, screen gocv.Mat) (trainingDispatched bool) {
	if !isVillageState(gc.State) {
		return false
	}

	b.refreshStorageGate(gc, screen)

	// A paused scheduler owns the account: no taps of any kind, including
	// maintenance. This is what makes a scheduler break look like an offline
	// player instead of a bot that keeps rearranging its village.
	if allowed, _ := b.scheduleAllowed(); !allowed {
		return false
	}

	if b.maybeTrainArmy() {
		trainingDispatched = true
	}

	if b.seqRunning.Load() {
		return trainingDispatched
	}

	// Free builder / lab / pet slot opened up while waiting or training.
	if b.builderManager != nil &&
		(b.cfg.Upgrade.AutoBuilders || b.cfg.Upgrade.AutoLab || b.cfg.Upgrade.AutoPets) &&
		time.Since(b.lastUpgradeCheck) > villageUpgradeInterval {
		b.lastUpgradeCheck = time.Now()
		go func() {
			if b.seqRunning.Load() {
				return
			}
			if err := b.builderManager.PerformAutoUpgrades(); err != nil {
				b.logger.Warn().Err(err).Msg("auto upgrades encountered error")
			}
		}()
	}

	// Collectors, drills, and the loot cart.
	if b.cfg.Storage.AutoCollect && time.Since(b.lastCollectTime) > villageCollectInterval {
		b.lastCollectTime = time.Now()
		go func() {
			if b.seqRunning.Load() {
				return
			}
			b.CollectVillageResources()
		}()
	}

	// Idle humanization: a waiting player drifts the camera. Deliberately not
	// recordActivity — a genuinely stuck bot must still trip the watchdog.
	if time.Since(b.lastIdlePan) > villagePanInterval {
		b.lastIdlePan = time.Now()
		go func() {
			if b.seqRunning.Load() {
				return
			}
			b.navigator.IdlePan()
		}()
	}

	return trainingDispatched
}

// storageHaltApplies reports whether a full village may latch the storage_full
// halt: only when something in the bot can actually spend the loot down.
//
// A halt is a wait, not a decision — it buys minutes for walls or an upgrade to
// drain the storages. With no spender enabled nothing ever drains them, so the
// halt becomes permanent and the account stops farming. That is exactly what a
// fresh CLI install did: config.DefaultConfig() ships walls, builders, lab and
// pets all off, so its storages filled once and every later frame was refused.
func storageHaltApplies(cfg *config.BotConfig) bool {
	return (cfg.Storage.DumpToWallsBeforeHalt && cfg.Upgrade.UpgradeWalls) ||
		cfg.Upgrade.AutoBuilders || cfg.Upgrade.AutoLab || cfg.Upgrade.AutoPets
}

// refreshStorageGate re-reads the village storage fill and publishes the
// verdict the attack path consumes. It replaces the capture-per-attempt check
// that used to live inside executeAttackSequence: that one ran on every frame
// where the Attack button was visible, so a full village captured the screen
// 3-4 times a second just to decide, again, not to attack.
func (b *Bot) refreshStorageGate(gc *game.GameContext, screen gocv.Mat) {
	// attack.attack_when_full is the documented escape hatch from the halt: the
	// account keeps raiding and simply wastes the overflow. The halt itself only
	// applies while a spender is enabled (see storageHaltApplies): with nothing
	// able to drain the storages, halting would stop the account for good.
	if b.cfg.Attack.AttackWhenFull {
		if b.storageHalted.Swap(false) {
			b.logger.Info().Msg("attack.attack_when_full is on; attacks continue with full storages")
		}
		return
	}
	if !b.cfg.Storage.HaltWhenGoldFull && !b.cfg.Storage.HaltWhenElixirFull && !b.cfg.Storage.HaltWhenDEFull {
		b.storageHalted.Store(false)
		return
	}
	if screen.Empty() || b.cal == nil {
		return
	}
	if time.Since(b.lastStorageCheck) < storageGateInterval {
		return
	}
	b.lastStorageCheck = time.Now()

	fill := game.ReadVillageStorageFillMat(screen, b.cal.DisplayScale())
	thresh := b.cfg.Storage.FullThresholdPercent
	if b.storageHalted.Load() {
		// Hysteresis: a village that halted at 95% resumes at the configured
		// resume level, so one wall upgrade cannot flip the gate every cycle.
		thresh = b.cfg.Storage.ResumeThresholdPercent
	}
	full := fill.IsFull(
		b.cfg.Storage.HaltWhenGoldFull,
		b.cfg.Storage.HaltWhenElixirFull,
		b.cfg.Storage.HaltWhenDEFull,
		b.cfg.Storage.HaltRule,
		thresh,
	)

	if !full {
		if b.storageHalted.Swap(false) {
			b.logger.Info().
				Float64("gold", fill.GoldFillPercent).
				Float64("elixir", fill.ElixirFillPercent).
				Float64("de", fill.DEFillPercent).
				Float64("resume_threshold", b.cfg.Storage.ResumeThresholdPercent).
				Msg("storages drained below the resume threshold; attacks resume")
		}
		b.storageNoSpenderWarned.Store(false)
		return
	}

	if !storageHaltApplies(b.cfg) {
		// Nothing can spend the loot, so the halt would never end. Clear any
		// latch a previous config left behind and keep raiding: wasted overflow
		// is recoverable, a permanently idle account is not. Say it once per
		// episode, not on every storageGateInterval.
		b.storageHalted.Store(false)
		if !b.storageNoSpenderWarned.Swap(true) {
			b.logger.Warn().
				Float64("gold", fill.GoldFillPercent).
				Float64("elixir", fill.ElixirFillPercent).
				Float64("de", fill.DEFillPercent).
				Msg("storages are full and no spender is enabled (walls, builders, lab and pets are all off): attacks continue so the account keeps farming, and the overflow is wasted. Enable upgrade.auto_builders, or storage.dump_to_walls_before_halt + upgrade.upgrade_walls, to spend it down")
		}
		return
	}
	b.storageNoSpenderWarned.Store(false)

	if !b.storageHalted.Load() {
		b.logger.Warn().
			Float64("gold", fill.GoldFillPercent).
			Float64("elixir", fill.ElixirFillPercent).
			Float64("de", fill.DEFillPercent).
			Bool("spender_enabled", true).
			Msg("storages are full; attacks halt until loot is spent (walls/upgrades keep running)")
	}
	b.storageHalted.Store(true)

	// Spend the overflow into walls before giving up. This runs off the capture
	// loop because a wall dump is a minutes-long tap sequence.
	if b.cfg.Storage.DumpToWallsBeforeHalt && b.cfg.Upgrade.UpgradeWalls &&
		b.wallsDumping.CompareAndSwap(false, true) {
		b.logger.Info().Msg("dumping storage overflow into walls while the attack gate is closed")
		go func() {
			defer b.wallsDumping.Store(false)
			if b.ctx.Err() != nil {
				return
			}
			b.UpgradeWalls(gc)
			// Let the next interval read a fresh fill instead of waiting out the
			// full window on a village that just spent a few million.
			b.lastStorageCheck = time.Time{}
		}()
	}
}
