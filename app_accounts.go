package main

// app_accounts.go — the Wails surface of the multi-account switcher.
//
// The bot engine is intentionally single-session: one capture loop, one
// device, one set of counters. "Multiple accounts" therefore means a fleet
// that is handed the device in turn, not several bots racing the same ADB
// server. A switch is:
//
//  1. stop the running bot and flush its stats to the OLD profile,
//  2. select the NEW profile (paths.SetProfile) and mark it active,
//  3. load that profile's counters/history so the UI is immediately correct,
//  4. optionally start a fresh bot on the account's bound device.
//
// Every account owns <configDir>/accounts/<id>/{config,stats,attack_history},
// so no subsystem needed to learn about accounts: config.Load, saveStats and
// GetAttackHistory all resolve through paths.ResolveConfig.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/Ducky705/ClashGO/internal/accounts"
	"github.com/Ducky705/ClashGO/internal/bot"
	"github.com/Ducky705/ClashGO/internal/config"
	"github.com/Ducky705/ClashGO/internal/paths"
	"github.com/rs/zerolog/log"
	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// AccountView is one account plus the live state the switcher renders.
type AccountView struct {
	accounts.Account
	Active       bool         `json:"active"`
	Running      bool         `json:"running"`
	Stats        bot.BotStats `json:"stats"`
	TodayAttacks int          `json:"today_attacks"`
	Status       string       `json:"status"`
}

// AccountsState is the whole switcher document, fetched in one IPC call.
type AccountsState struct {
	Accounts []AccountView     `json:"accounts"`
	ActiveID string            `json:"active_id"`
	Rotation accounts.Rotation `json:"rotation"`
}

// SwitchResult reports what a switch actually did.
type SwitchResult struct {
	Account AccountView `json:"account"`
	Started bool        `json:"started"`
	Message string      `json:"message"`
}

// ensureAccounts loads the registry once. A corrupt accounts.json is
// surfaced rather than replaced, so the operator can fix or restore the
// .bak instead of silently losing the fleet.
func (a *App) ensureAccounts() (*accounts.Store, error) {
	a.accOnce.Do(func() {
		st, err := accounts.Open(paths.GetConfigDir())
		a.accMu.Lock()
		a.accounts, a.accErr = st, err
		a.accMu.Unlock()
	})
	a.accMu.RLock()
	defer a.accMu.RUnlock()
	return a.accounts, a.accErr
}

// loadedAccounts returns the registry only if it has already been opened.
// Passive paths (recording usage on Stop) must not bootstrap or touch disk
// just because a test stopped a bot.
func (a *App) loadedAccounts() *accounts.Store {
	a.accMu.RLock()
	defer a.accMu.RUnlock()
	return a.accounts
}

// GetAccounts returns every account with live stats, plus the rotation
// policy. Stats for non-active accounts come from their profile's stats.json;
// the active account's stats include the running bot's live counters.
func (a *App) GetAccounts() (AccountsState, error) {
	st, err := a.ensureAccounts()
	if err != nil {
		return AccountsState{}, err
	}

	now := time.Now()
	activeID := st.ActiveID()

	a.mu.Lock()
	running := a.bot != nil
	a.mu.Unlock()
	live := a.GetStats()

	state := AccountsState{
		ActiveID: activeID,
		Rotation: st.Rotation(),
		Accounts: make([]AccountView, 0, len(st.List())),
	}
	for _, acct := range st.List() {
		stats := readAccountStats(acct.ID)
		isActive := acct.ID == activeID
		if isActive {
			stats = live
		}
		today := 0
		if acct.Usage.Date == now.Format("2006-01-02") {
			today = acct.Usage.Attacks
		}
		state.Accounts = append(state.Accounts, AccountView{
			Account:      acct,
			Active:       isActive,
			Running:      isActive && running,
			Stats:        stats,
			TodayAttacks: today,
			Status:       acct.Status(now, isActive, running),
		})
	}
	return state, nil
}

// CreateAccount adds a profile. New accounts inherit the active account's
// tuning (strategy slots, loot filters, debug flags) and then diverge.
func (a *App) CreateAccount(in accounts.Input) (AccountView, error) {
	st, err := a.ensureAccounts()
	if err != nil {
		return AccountView{}, err
	}
	acct, err := st.Create(in)
	if err != nil {
		return AccountView{}, err
	}
	a.emitAccountsChanged()
	return a.viewFor(st, acct), nil
}

// UpdateAccount edits metadata, device binding and rotation limits. The ID is
// immutable and a running bot keeps its booted config until the next Start.
func (a *App) UpdateAccount(id string, in accounts.Input) (AccountView, error) {
	st, err := a.ensureAccounts()
	if err != nil {
		return AccountView{}, err
	}
	acct, err := st.Update(id, in)
	if err != nil {
		return AccountView{}, err
	}
	a.emitAccountsChanged()
	return a.viewFor(st, acct), nil
}

// DeleteAccount archives the profile under accounts/.trash and removes the
// account from the registry. The active account must be switched away first.
func (a *App) DeleteAccount(id string) error {
	st, err := a.ensureAccounts()
	if err != nil {
		return err
	}
	if err := st.Delete(id); err != nil {
		return err
	}
	a.emitAccountsChanged()
	return nil
}

// ResetAccountUsage clears an account's daily counters (attacks + switches),
// the escape hatch for a rotation blocked by "daily_limit".
func (a *App) ResetAccountUsage(id string) error {
	st, err := a.ensureAccounts()
	if err != nil {
		return err
	}
	if err := st.ResetUsage(id); err != nil {
		return err
	}
	a.emitAccountsChanged()
	return nil
}

// SetRotation persists the fleet policy. Enabling rotation makes each
// session finite (rotation.attacks_per_account) so the fleet actually cycles.
func (a *App) SetRotation(r accounts.Rotation) error {
	st, err := a.ensureAccounts()
	if err != nil {
		return err
	}
	if err := st.SetRotation(r); err != nil {
		return err
	}
	a.emitAccountsChanged()
	return nil
}

// SwitchAccount performs a safe hand-off to id. When start is true it also
// boots the bot on that account's device. Switching to the already-active
// account is a no-op unless it is stopped and start is true.
func (a *App) SwitchAccount(id string, start bool) (SwitchResult, error) {
	st, err := a.ensureAccounts()
	if err != nil {
		return SwitchResult{}, err
	}
	acct, ok := st.Get(id)
	if !ok {
		return SwitchResult{}, fmt.Errorf("account %q not found", id)
	}

	a.switchMu.Lock()
	defer a.switchMu.Unlock()

	if st.ActiveID() == id {
		if start && !a.IsRunning() {
			res := a.startBotWithConfig(config.LoadOrDefault("config.json"))
			a.emitAccountsChanged()
			return SwitchResult{Account: a.viewFor(st, acct), Started: res.Running, Message: res.Message}, nil
		}
		return SwitchResult{
			Account: a.viewFor(st, acct),
			Started: a.IsRunning(),
			Message: "Account already active",
		}, nil
	}

	if err := a.stopForProfileSwitch(); err != nil {
		return SwitchResult{}, err
	}
	if err := paths.SetProfile(id); err != nil {
		return SwitchResult{}, err
	}
	acct, err = st.SetActive(id)
	if err != nil {
		return SwitchResult{}, err
	}
	a.resetSessionStats(id)

	msg := fmt.Sprintf("Switched to %s on %s", acct.Name, acct.Device.DeviceID)
	started := false
	if start {
		res := a.startBotWithConfig(config.LoadOrDefault("config.json"))
		started = res.Running
		if !started {
			msg = res.Message
		} else {
			msg += " — bot starting"
		}
	}
	log.Info().
		Str("account", acct.ID).
		Str("device", acct.Device.DeviceID).
		Bool("started", started).
		Msg("account switched")

	a.emitAccountsChanged()
	return SwitchResult{Account: a.viewFor(st, acct), Started: started, Message: msg}, nil
}

// stopForProfileSwitch stops any running bot and waits for its teardown
// before the profile changes. saveStats runs while the OLD profile is still
// selected, so the final counters land in the correct account.
func (a *App) stopForProfileSwitch() error {
	a.mu.Lock()
	if a.cancel != nil {
		a.cancel()
	}
	b := a.bot
	booting := b == nil && a.cancel != nil
	if b != nil {
		a.lastStats = mergeStats(a.lastStats, b.Stats())
		a.recordAccountAttacks(b.Stats())
		b.Cancel()
		a.bot = nil
		a.cancel = nil
		a.botCtx = nil
	}
	a.mu.Unlock()

	if b != nil {
		select {
		case <-b.Done():
		case <-time.After(30 * time.Second):
			return fmt.Errorf("bot did not stop within 30s; account not switched")
		}
		b.Stop()
	} else if booting {
		// A boot was in flight. Its goroutine observes the cancelled
		// context and clears the placeholder itself; wait for that so a
		// new bot on the same device cannot collide with a dying boot.
		deadline := time.Now().Add(30 * time.Second)
		for {
			a.mu.Lock()
			booting = a.cancel != nil
			a.mu.Unlock()
			if !booting {
				break
			}
			if time.Now().After(deadline) {
				return fmt.Errorf("bot boot did not abort within 30s; account not switched")
			}
			time.Sleep(100 * time.Millisecond)
		}
	}

	a.saveStats()
	return nil
}

// resetSessionStats makes the newly selected profile's persisted counters
// visible immediately instead of carrying the previous account's totals.
func (a *App) resetSessionStats(id string) {
	a.mu.Lock()
	a.lastStats = readAccountStats(id)
	a.mu.Unlock()

	a.cachedHistoryMu.Lock()
	a.cachedHistory = nil
	a.cachedHistoryMu.Unlock()
}

// applyAccountConfig stamps the active account's device binding onto the
// config the bot is about to boot with. The config file stays the user's
// tuning; the binding is a property of the account, so editing either one
// cannot silently send traffic to the wrong emulator.
func (a *App) applyAccountConfig(cfg *config.BotConfig) {
	st, err := a.ensureAccounts()
	if err != nil {
		log.Warn().Err(err).Msg("account registry unavailable; starting with the config file's device")
		return
	}
	acct, ok := st.Active()
	if !ok {
		return
	}
	cfg.Device.ADBHost = acct.Device.ADBHost
	cfg.Device.ADBPort = acct.Device.ADBPort
	cfg.Device.DeviceID = acct.Device.DeviceID
	if acct.MaxAttacksPerSession > 0 {
		cfg.Attack.MaxAttackPerSession = acct.MaxAttacksPerSession
	} else if rot := st.Rotation(); rot.Enabled {
		cfg.Attack.MaxAttackPerSession = rot.AttacksPerAccount
	}
}

// recordAccountAttacks books a finished session against the active account's
// daily usage. Bot counters are zeroed on every boot, so the argument is the
// session total, not a lifetime total.
func (a *App) recordAccountAttacks(s bot.BotStats) {
	st := a.loadedAccounts()
	if st == nil {
		return
	}
	st.RecordAttacks(st.ActiveID(), int(s.AttacksCompleted))
}

// stopAtDailyCap ends a session that has reached the account's daily cap so
// rotation can move on. It applies whether or not rotation is enabled: the
// field is presented as a cap, so silently exceeding it would be worse than
// stopping.
func (a *App) stopAtDailyCap(b *bot.Bot) {
	st := a.loadedAccounts()
	if st == nil {
		return
	}
	acct, ok := st.Active()
	if !ok || acct.MaxAttacksPerDay <= 0 {
		return
	}
	used := 0
	if acct.Usage.Date == time.Now().Format("2006-01-02") {
		used = acct.Usage.Attacks
	}
	session := int(b.Stats().AttacksCompleted)
	if used+session < acct.MaxAttacksPerDay {
		return
	}
	log.Info().
		Str("account", acct.ID).
		Int("daily_cap", acct.MaxAttacksPerDay).
		Int("used", used).
		Int("session", session).
		Msg("daily attack cap reached; ending session for rotation")
	b.Cancel()
}

// notifySessionEnd wakes the rotation loop. Buffered + non-blocking: multiple
// session ends before the loop runs collapse into one hand-off.
func (a *App) notifySessionEnd() {
	select {
	case a.rotateCh <- struct{}{}:
	default:
	}
}

// rotationLoop is the fleet coordinator. It only reacts to a session that
// ended on its own, so stopping a bot by hand never starts another account.
func (a *App) rotationLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-a.rotateCh:
			a.runRotation()
		}
	}
}

func (a *App) runRotation() {
	st, err := a.ensureAccounts()
	if err != nil {
		log.Error().Err(err).Msg("rotation skipped: account registry unavailable")
		return
	}
	rot := st.Rotation()
	if !rot.Enabled || !rot.AutoStart {
		return
	}
	current := st.ActiveID()
	next, ok := st.Next(current, time.Now())
	if !ok {
		log.Info().Str("account", current).Msg("rotation: no eligible account (cooldown, daily cap or disabled)")
		return
	}
	log.Info().Str("from", current).Str("to", next.ID).Str("order", rot.Order).Msg("rotation: switching account")
	res, err := a.SwitchAccount(next.ID, true)
	if err != nil {
		log.Error().Err(err).Str("account", next.ID).Msg("rotation switch failed")
		return
	}
	if !res.Started {
		log.Warn().Str("account", next.ID).Str("message", res.Message).Msg("rotation switch did not start the bot")
	}
}

// viewFor builds one AccountView for a registry account.
func (a *App) viewFor(st *accounts.Store, acct accounts.Account) AccountView {
	now := time.Now()
	activeID := st.ActiveID()
	a.mu.Lock()
	running := a.bot != nil
	a.mu.Unlock()

	isActive := acct.ID == activeID
	stats := readAccountStats(acct.ID)
	if isActive {
		stats = a.GetStats()
	}
	today := 0
	if acct.Usage.Date == now.Format("2006-01-02") {
		today = acct.Usage.Attacks
	}
	return AccountView{
		Account:      acct,
		Active:       isActive,
		Running:      isActive && running,
		Stats:        stats,
		TodayAttacks: today,
		Status:       acct.Status(now, isActive, running),
	}
}

// emitAccountsChanged pushes the fresh state so every view (sidebar chip,
// Accounts page, future tray UI) converges without polling races.
func (a *App) emitAccountsChanged() {
	if a.ctx == nil {
		return
	}
	if state, err := a.GetAccounts(); err == nil {
		runtime.EventsEmit(a.ctx, "accounts_changed", state)
	}
}

// readAccountStats loads one profile's persisted counters. Missing or
// malformed files read as zero, matching the legacy GetStats behaviour.
func readAccountStats(id string) bot.BotStats {
	var s bot.BotStats
	data, err := os.ReadFile(paths.ResolveProfileConfig(id, "stats.json"))
	if err != nil {
		return s
	}
	if err := json.Unmarshal(data, &s); err != nil {
		return bot.BotStats{}
	}
	return s
}
