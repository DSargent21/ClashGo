// Package bot — builderbase.go
//
// The Builder Base battle: boat, Attack, match, one troop, surrender, home. It is
// a second, much simpler battle loop than the home-village attack sequence, and
// it exists for the reason every competitor's builder-base mode exists: Builder
// Base loot is paid by destruction on a base nobody is defending, so a single
// troop and a surrender is the cheapest income the account has.
//
// The reason this file is mostly preconditions is that none of it is visible from
// the code: the versus screen, the troop bar and the result panel are references
// that have to be proven on a live frame. Every step therefore proves its screen
// before the next tap and names the step that failed, so a run logs "the versus
// screen's Battle button was not located" instead of "builder base failed".
package bot

import (
	"errors"
	"fmt"
	"time"

	"github.com/Ducky705/ClashGO/internal/config"
	"github.com/Ducky705/ClashGO/internal/game"
	"github.com/Ducky705/ClashGO/internal/vision"
	"github.com/rs/zerolog"
)

// Wait budgets are variables so a test can shorten them instead of spending the
// real timeout on a screen that is scripted never to arrive.
var (
	bbOpenTimeout   = 25 * time.Second
	bbBattleTimeout = 60 * time.Second
	bbResultTimeout = 25 * time.Second
)

const bbPoll = 700 * time.Millisecond

// bbVersusActionRegion is the window the versus screen's Battle button sits in,
// as frame fractions. It is wide because the panel is centred and reflows, unlike
// the attack menu's narrow column; the window still excludes the top bar (the
// gem and gold counters are not action buttons) and the bottom edge.
var bbVersusActionRegion = [4]float64{0.15, 0.45, 0.85, 0.95}

// builderBasePlan decides whether a Builder Base battle may start now, and why
// not. Pure so the schedule is testable without a device: enabled, unlocked,
// mode supported, enough home attacks since the last one, daily cap.
func builderBasePlan(cfg config.BuilderBaseConfig, unlocked bool, attacksSinceLast, today int) (bool, string) {
	if !cfg.Enabled {
		return false, "builder_base.enabled is off"
	}
	if !unlocked {
		return false, "the imported village export says this account has no Builder Base"
	}
	if cfg.Mode != "elixir" {
		return false, fmt.Sprintf("builder_base.mode %q is not implemented (only \"elixir\": one troop and a surrender)", cfg.Mode)
	}
	every := cfg.EveryNHomeAttacks
	if every <= 0 {
		every = 1
	}
	if attacksSinceLast < every {
		return false, fmt.Sprintf("%d/%d home attacks since the last Builder Base battle", attacksSinceLast, every)
	}
	if cfg.MaxPerDay > 0 && today >= cfg.MaxPerDay {
		return false, fmt.Sprintf("daily Builder Base cap reached (%d/%d)", today, cfg.MaxPerDay)
	}
	return true, ""
}

// bbDevice is the device surface the sequence needs. Narrow on purpose: with it,
// the step order, the fail-closed behaviour and the timeouts are testable against
// a fake, which is the only way to check this code without an emulator.
type bbDevice interface {
	State() (game.GameState, int)
	TapRef(x, y int) error
	Click(template, step string) bool
	PanelAction(step string, region [4]float64, timeout time.Duration) bool
	EndBattle() error
	ReturnHome() error
	EmptyArmy() bool
	OnBuilderBase() bool
	Sleep(time.Duration)
}

// runBuilderBaseBattle walks one Builder Base raid. It never starts unless it can
// see the home village, and it never taps a control it did not locate: the cost
// of a blind tap here is a lost troop bar mid-run, not a wasted second.
func runBuilderBaseBattle(d bbDevice, cfg config.BuilderBaseConfig) error {
	if state, _ := d.State(); state != game.StateMainVillage {
		return fmt.Errorf("not in the home village (state=%s)", state)
	}

	if err := d.TapRef(cfg.BoatX, cfg.BoatY); err != nil {
		return fmt.Errorf("boat tap: %w", err)
	}
	d.Sleep(2 * time.Second)
	// Detected by the Builder Base's own crossed-axes button, NOT by the global
	// classifier: the StateBuilderBase state is a stale HUD fingerprint on the
	// current game, and a template-based rule for it matched other screens'
	// crossed swords (the attack panel among them), which breaks attacking.
	if !waitForBuilderBase(d, bbOpenTimeout) {
		return fmt.Errorf("the Builder Base screen never appeared after the boat tap (builder_base.boat_x/boat_y = %d,%d: check them with `see look`)", cfg.BoatX, cfg.BoatY)
	}

	// The Builder Base button is the crossed-axes one at the bottom-left.
	if !d.Click("btn_builder_battle", "Battle (Builder Base)") {
		return errors.New("the Battle button was not located on the Builder Base screen")
	}
	d.Sleep(900 * time.Millisecond)
	// The panel is the only place the troop bar can be inspected, and an empty
	// one is not worth a Find Now: the battle would start with nothing to drop,
	// pay nothing, and still cost the versus cooldown. The bot trains the home
	// recipe only, so this is the operator's one manual step per Builder Base
	// session.
	if d.EmptyArmy() {
		return errors.New("the Builder Base army is empty (\"Train an army before attacking!\"); train it in game and it will be used on the next raid")
	}
	if !d.PanelAction("Builder Base Battle", bbVersusActionRegion, 8*time.Second) {
		return errors.New("the Start Attack panel's Find Now button was not located")
	}
	if !waitForBBState(d, func(s game.GameState) bool { return s == game.StateBattle }, bbBattleTimeout) {
		return errors.New("no battle started after the versus-screen tap")
	}

	// elixir mode: one troop card, one drop, then let the raid pay out and end it.
	if err := d.TapRef(cfg.TroopSlotX, cfg.TroopSlotY); err != nil {
		return fmt.Errorf("troop card tap: %w", err)
	}
	d.Sleep(400 * time.Millisecond)
	if err := d.TapRef(cfg.DeployX, cfg.DeployY); err != nil {
		return fmt.Errorf("deploy tap: %w", err)
	}
	d.Sleep(400 * time.Millisecond)

	hold := time.Duration(cfg.SurrenderAfterSeconds) * time.Second
	if hold <= 0 {
		hold = 20 * time.Second
	}
	d.Sleep(hold)

	if err := d.EndBattle(); err != nil {
		return fmt.Errorf("end battle: %w", err)
	}
	if !waitForBBState(d, func(s game.GameState) bool {
		return s == game.StateBattleEnd || s == game.StateReturnHome
	}, bbResultTimeout) {
		return errors.New("the result screen never appeared after End Battle")
	}
	if err := d.ReturnHome(); err != nil {
		return fmt.Errorf("return home: %w", err)
	}
	d.Sleep(1500 * time.Millisecond)
	return nil
}

// waitForBuilderBase polls for the Builder Base's own button. It never taps: a
// screen that does not arrive is an error, not an invitation to guess.
func waitForBuilderBase(d bbDevice, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		if d.OnBuilderBase() {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		d.Sleep(bbPoll)
	}
}

// waitForBBState polls the classifier until match accepts the state or the
// timeout expires.
func waitForBBState(d bbDevice, match func(game.GameState) bool, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		if state, _ := d.State(); match(state) {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		d.Sleep(bbPoll)
	}
}

// bbBotDevice adapts the bot to bbDevice. All taps go through the calibration, so
// the authored reference anchors follow the display scale like every other
// anchored control.
type bbBotDevice struct{ b *Bot }

func (d bbBotDevice) State() (game.GameState, int) {
	screen, err := d.b.client.CaptureToMat()
	if err != nil || screen.Empty() {
		if err == nil {
			screen.Close()
		}
		return game.StateUnknown, 0
	}
	defer screen.Close()
	return d.b.classifyFrame(screen)
}

func (d bbBotDevice) TapRef(x, y int) (err error) {
	px, py := d.b.cal.Hud(x, y)
	return d.b.client.TapRandomized(px, py)
}

func (d bbBotDevice) Click(template, step string) bool {
	return d.b.findAndClick(template, step, 2)
}

func (d bbBotDevice) PanelAction(step string, region [4]float64, timeout time.Duration) bool {
	_, ok := d.b.pressPanelAction(step, region, timeout)
	return ok
}

func (d bbBotDevice) EndBattle() error  { return d.b.attackExec.EndBattle() }
func (d bbBotDevice) ReturnHome() error { return d.b.attackExec.ReturnHome() }

// OnBuilderBase reports the Builder Base by its crossed-axes Battle button,
// searched only in the bottom-left HUD window the button occupies. scoped on
// purpose: the same icon appears on the attack and army panels, and a full-frame
// search would call those the Builder Base.
func (d bbBotDevice) OnBuilderBase() bool {
	tpl, ok := d.b.templates.Get("btn_builder_battle")
	if !ok || tpl.Cols() < 2 || tpl.Rows() < 2 {
		return false
	}
	screen, err := d.b.client.CaptureToMat()
	if err != nil || screen.Empty() {
		if err == nil {
			screen.Close()
		}
		return false
	}
	defer screen.Close()
	roi := d.b.cal.HudRect(d.b.buttonROI("btn_builder_battle"))
	k := d.b.cal.DisplayScale()
	matches, err := vision.MatchMultiScaleROICached(
		screen, tpl, "btn_builder_battle", k*0.9, k*1.1, 3, 0.6, roi,
	)
	return err == nil && len(matches) > 0
}

func (d bbBotDevice) EmptyArmy() bool {
	screen, err := d.b.client.CaptureToMat()
	if err != nil || screen.Empty() {
		if err == nil {
			screen.Close()
		}
		return false
	}
	defer screen.Close()
	return emptyArmyPanel(screen)
}

func (d bbBotDevice) Sleep(dur time.Duration) { d.b.client.JitteredSleep(dur) }

// builderBaseDue reports the plan for the current moment, folding the day
// boundary into the counter so a cap from yesterday cannot block today.
func (b *Bot) builderBaseDue(now time.Time) (bool, string) {
	today := 0
	b.bbMu.Lock()
	if b.bbDay == localDay(now) {
		today = b.bbDayCount
	}
	b.bbMu.Unlock()

	since := int(b.attackCount.Load()) - int(b.bbAtAttack.Load())
	return builderBasePlan(b.cfg().BuilderBase, b.builderBaseUnlocked, since, today)
}

// maybeRunBuilderBase runs at most one Builder Base battle, synchronously, from
// the post-attack path: the bot is home, the sequence lock is already held, and
// the battle ends back in the home village where the wall pass continues.
func (b *Bot) maybeRunBuilderBase(logger zerolog.Logger) {
	due, why := b.builderBaseDue(time.Now())
	if !due {
		if b.cfg().BuilderBase.Enabled {
			logger.Debug().Str("reason", why).Msg("Builder Base battle not due")
		}
		return
	}
	if !b.bbRunning.CompareAndSwap(false, true) {
		return
	}
	defer b.bbRunning.Store(false)

	// The attempt is charged before it runs: a sequence that fails halfway still
	// spent the troop bar, so retrying it on the next attack would just fail in a
	// loop, and the daily cap has to see it.
	b.bbMu.Lock()
	b.bbAtAttack.Store(b.attackCount.Load())
	b.bbDay = localDay(time.Now())
	b.bbDayCount++
	today := b.bbDayCount
	b.bbMu.Unlock()

	logger.Info().
		Str("mode", b.cfg().BuilderBase.Mode).
		Int("attacks_today", today).
		Int("daily_cap", b.cfg().BuilderBase.MaxPerDay).
		Msg("starting the Builder Base battle")

	if err := runBuilderBaseBattle(bbBotDevice{b: b}, b.cfg().BuilderBase); err != nil {
		logger.Warn().Err(err).Msg("Builder Base battle did not complete; returning to the home village")
		b.notifyEvent("halt", "Builder Base battle failed", err.Error())
		b.returnToHomeVillage()
		return
	}
	logger.Info().Msg("Builder Base battle complete")
	b.returnToHomeVillage()
}

// returnToHomeVillage leaves the Builder Base. The boat is a one-way anchor so
// far — the Builder Base's own control back to the home village has not been
// measured live, and a guessed tap there is how a run ends up in the shop — so
// the recovery is a game restart, which always lands in the home village.
//
// ponytail: swap the restart for the return boat once its anchor is measured;
// the restart costs ~20s, the guess costs the session.
func (b *Bot) returnToHomeVillage() {
	screen, err := b.client.CaptureToMat()
	if err != nil {
		return
	}
	state, _ := b.classify(screen)
	screen.Close()
	if state != game.StateBuilderBase {
		return
	}
	b.logger.Info().Msg("still in the Builder Base; restarting the game to land back in the home village")
	b.restartGame()
}
