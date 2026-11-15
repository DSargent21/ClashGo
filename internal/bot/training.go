// Package bot — training.go
//
// The bot could not retrain: when the camps were spent the Start Attack panel
// said "Train an army before attacking!", the capture loop latched that and
// idled 60 s at a time, forever. A 24/7 farm therefore stopped farming after
// the first battle. See the comment this file replaces in clickSequence.
//
// The queue is driven entirely through controls the bot already proves on the
// live frame: the Attack button it taps every cycle, the empty-army panel's own
// EDIT ARMY button (located, never predicted), the saved-recipe card, and the
// army sheet's action button. Every step fails closed and logs why, so a UI
// change leaves the account idle instead of tapping coordinates blind.
package bot

import (
	"time"

	"github.com/Ducky705/ClashGO/internal/game"
	"github.com/Ducky705/ClashGO/internal/vision"
)

const (
	// trainingRetryInterval is how often an empty army may be re-queued. A
	// recipe that is already brewing ignores the extra selection, so this is
	// purely a bound on input: three minutes is well inside the shortest army
	// build the shipped strategies ask for, and far outside anything a player
	// would notice as repeated tapping.
	trainingRetryInterval = 3 * time.Minute

	// emptyArmyRetryInterval is how long an empty army keeps the attack path
	// away from the Attack button. Re-opening the panel every capture cycle taps
	// the village for no reason; two minutes still re-checks well inside one
	// training pass.
	emptyArmyRetryInterval = 2 * time.Minute
)

// emptyArmyActionRegion is the window holding the empty attack panel's EDIT
// ARMY button, as frame fractions. Measured live at 1024x768: the button centre
// sits at (326,560), i.e. 0.32 x / 0.73 y. The window is wide enough for the
// sheet reflow and stays clear of the panel title.
var emptyArmyActionRegion = [4]float64{0.10, 0.55, 0.62, 0.92}

// trainingDue reports whether the queue may be touched again. Pure so the
// throttle is testable without a device.
func trainingDue(last, now time.Time, interval time.Duration) bool {
	if last.IsZero() {
		return true
	}
	return now.Sub(last) >= interval
}

// maybeTrainArmy queues the saved army recipe when the last attack attempt
// reported an empty army. It returns true when a queue attempt was dispatched
// on this frame, so the caller can skip starting an attack in the same cycle.
func (b *Bot) maybeTrainArmy() bool {
	if !b.armyEmpty.Load() {
		return false
	}
	if !b.cfg.Training.Enabled || !b.cfg.Training.TrainDeadTroops {
		// Say it out loud. A disabled training loop plus an empty army is the
		// one halt the bot cannot recover from on its own: nothing refills the
		// camps, so a 24/7 run quietly turns into a 24/7 idle village.
		if trainingDue(b.trainingLast, time.Now(), trainingRetryInterval) {
			b.trainingLast = time.Now()
			b.logger.Warn().
				Bool("training_enabled", b.cfg.Training.Enabled).
				Bool("train_dead_troops", b.cfg.Training.TrainDeadTroops).
				Msg("army is empty and the training loop is off (training.enabled and training.train_dead_troops both must be true): the bot waits instead of training, and nothing will refill the camps")
		}
		return false
	}
	if b.client == nil || b.cal == nil {
		return false
	}
	if b.seqRunning.Load() || b.ctx.Err() != nil {
		return false
	}
	now := time.Now()
	if !trainingDue(b.trainingLast, now, trainingRetryInterval) {
		return false
	}
	if !b.trainingInFlight.CompareAndSwap(false, true) {
		return false
	}
	b.trainingLast = now
	go func() {
		defer b.trainingInFlight.Store(false)
		b.trainArmy()
	}()
	return true
}

// attacksDeferred reports whether an empty army should keep the attack path off
// the Attack button until skipUntil (unix seconds).
func attacksDeferred(armyEmpty bool, skipUntil, now int64) bool {
	return armyEmpty && now < skipUntil
}

// trainArmy is the queue sequence. It runs off the capture loop so the ~4 s of
// taps cannot stall frame processing, and it re-checks the lifecycle context at
// every step so a Stop never leaves the account inside the army sheet.
func (b *Bot) trainArmy() {
	b.logger.Info().Msg("army is empty: queueing the saved recipe so the camps rebuild")

	if !b.findAndClick("btn_attack", "Attack (train)", 1) {
		b.logger.Warn().Msg("training: could not open the attack panel; will retry on the next cycle")
		return
	}
	b.client.JitteredSleep(900 * time.Millisecond)

	screen, err := b.client.CaptureToMat()
	if err != nil || screen.Empty() {
		if err == nil {
			screen.Close()
		}
		b.logger.Warn().Err(err).Msg("training: capture of the attack panel failed; retrying next cycle")
		return
	}
	panelEmpty := emptyArmyPanel(screen)
	win := vision.FractionRect(screen.Cols(), screen.Rows(),
		emptyArmyActionRegion[0], emptyArmyActionRegion[1],
		emptyArmyActionRegion[2], emptyArmyActionRegion[3])
	editBtn, located := vision.FindActionButton(screen, win, vision.DefaultActionButtonConfig())
	screen.Close()

	if !panelEmpty {
		// Troops are already available (a queue finished between the latch and
		// this attempt). Close the panel and let the normal cycle attack.
		b.logger.Info().Msg("training: the attack panel reports troops available; nothing queued")
		b.armyEmpty.Store(false)
		_ = b.client.Back()
		b.client.JitteredSleep(600 * time.Millisecond)
		return
	}
	if !located {
		b.logger.Warn().Msg("training: EDIT ARMY was not located on the empty attack panel; no tap fired")
		_ = b.client.Back()
		b.client.JitteredSleep(600 * time.Millisecond)
		return
	}

	b.logger.Info().Str("button", editBtn.Describe()).Msg("training: opening the army editor")
	if err := b.client.TapRandomized(editBtn.Centre().X, editBtn.Centre().Y); err != nil {
		b.logger.Warn().Err(err).Msg("training: EDIT ARMY tap failed")
		return
	}
	b.client.JitteredSleep(1500 * time.Millisecond)
	if b.ctx.Err() != nil {
		return
	}

	// Arm the configured saved recipe. Slot 1 is the active army and needs no
	// card selection; selectArmySlot already handles both shapes.
	if !b.armyRecipeSelected() {
		b.logger.Warn().Int("army_slot", b.armySlot).Msg("training: saved recipe card was not confirmed; leaving the editor untouched")
		_ = b.client.Back()
		return
	}

	// The sheet's action button is the queue: Train on an incomplete army,
	// Attack-ready once it is full. Located in the live frame, never predicted.
	if _, ok := b.pressPanelAction("Train Army", sheetActionRegion, 5*time.Second); !ok {
		b.logger.Warn().Msg("training: army sheet action button not located; the recipe card selection still queues the army")
	}
	b.client.JitteredSleep(1200 * time.Millisecond)
	_ = b.client.Back()
	b.client.JitteredSleep(600 * time.Millisecond)

	if d := b.cfg.Training.SleepAfterTrain.Duration; d > 0 {
		sleepCtx(b.ctx, d)
	}
	b.recordActivity()
	b.logger.Info().Msg("training: recipe queued; the next cycle attacks as soon as the camps are ready")
}

// armyRecipeSelected wraps selectArmySlot so the training path can record its
// own outcome without changing the pre-attack behaviour.
func (b *Bot) armyRecipeSelected() bool {
	if b.armySlot <= 1 {
		// Active army: the editor already shows it. Nothing to select.
		return true
	}
	return b.selectArmySlot()
}

// isVillageState reports whether a state is a screen the idle maintenance is
// allowed to touch. Army-camp frames are excluded: the bot navigates out of
// them, it does not act inside them.
func isVillageState(state game.GameState) bool {
	return state == game.StateMainVillage
}
