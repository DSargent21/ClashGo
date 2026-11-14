// Package bot — runtime_state.go
//
// The bot has three reasons to be deliberately idle: the scheduler does not
// authorize an attack right now, the storages are full, or the army is empty.
// Before this file they were three unrelated booleans living in three
// different call sites, and none of them was visible to the UI — a halted bot
// looked exactly like a working one, and the stuck-watchdog treated a
// deliberate halt as a hang and force-restarted the game every 35 s.
//
// This file owns the single verdict the rest of the bot reads:
//
//	gate.allowed / gate.reason
//
// plus the small amount of state that must survive a process restart (the
// scheduler's hourly and daily attack counters, which used to be in-memory only
// — so every crash or keepalive respawn reset the daily cap that exists to keep
// the account inside a human-looking attack budget).
package bot

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/Ducky705/ClashGO/internal/paths"
)

// HaltReason is why the bot is not attacking. Empty means "nothing is holding
// it back". It is a string (not an int) because it travels to the React UI
// through BotStats and is read by humans in the log.
type HaltReason string

const (
	HaltNone        HaltReason = ""
	HaltScheduler   HaltReason = "scheduler"
	HaltStorageFull HaltReason = "storage_full"
	HaltArmyEmpty   HaltReason = "army_empty"
	HaltCap         HaltReason = "session_cap"
)

// gateEvaluation is how often the scheduler verdict is re-read. Scheduler
// transitions are minute-granular (session/break windows) or hourly-granular
// (rate caps), so one second is far finer than the thing it observes while
// keeping the capture loop from re-planning on every frame.
const gateEvaluationInterval = time.Second

// attackGate is the cached verdict the capture loop reads.
type attackGate struct {
	allowed  bool
	reason   string // human-readable scheduler reason, empty when allowed
	evaledAt time.Time
}

// gateMu serializes gate evaluation: both the capture loop (3-4 Hz) and the
// attack sequence goroutine read it, and Scheduler.CanAttack mutates session
// state (it plans the next break when a session expires).
type gateState struct {
	mu     sync.Mutex
	cached attackGate
}

// scheduleAllowed reports whether the scheduler authorizes an attack, with the
// verdict cached for gateEvaluationInterval. It also publishes the effective
// halt reason so Stats(), the stale-action watchdog, and the UI all agree.
func (b *Bot) scheduleAllowed() (bool, string) {
	b.gate.mu.Lock()
	defer b.gate.mu.Unlock()

	now := time.Now()
	if !b.gate.cached.evaledAt.IsZero() && now.Sub(b.gate.cached.evaledAt) < gateEvaluationInterval {
		return b.gate.cached.allowed, b.gate.cached.reason
	}

	allowed, reason := true, ""
	if b.scheduler != nil && b.cfg.Schedule.Enabled {
		allowed, reason = b.scheduler.CanAttack(now)
		wasAllowed := b.gate.cached.allowed || b.gate.cached.evaledAt.IsZero()
		if !allowed && wasAllowed {
			// Log the edge only. The capture loop calls this several times a
			// second; logging every verdict would bury the transcript. The first
			// evaluation counts as "was allowed" so a bot that boots inside a
			// closed window says so instead of going quiet.
			b.logger.Info().
				Str("reason", reason).
				Str("scheduler_state", string(b.scheduler.State())).
				Msg("scheduler paused the bot; no taps and no restarts until it resumes")
			b.zoomedOut.Store(false)
		}
		if allowed && !b.gate.cached.allowed && !b.gate.cached.evaledAt.IsZero() {
			b.logger.Info().Msg("scheduler resumed; attacks are authorized again")
		}
	}

	b.gate.cached = attackGate{
		allowed:  allowed,
		reason:   reason,
		evaledAt: now,
	}
	return allowed, reason
}

// haltLocked is the reason reported to the UI and used by the watchdog, in
// priority order. The scheduler wins because it is the only gate that also
// stops input entirely. The caller MUST hold gate.mu.
//
// It reads the cached scheduler verdict rather than asking the scheduler: this
// runs from Stats(), which the Wails bridge calls on a poll, and Scheduler.
// CanAttack both plans the next break and (through the break hooks) can stop
// the game over ADB. None of that belongs on a stats read.
func (b *Bot) haltLocked() HaltReason {
	if b.cfg.Schedule.Enabled && !b.gate.cached.evaledAt.IsZero() && !b.gate.cached.allowed {
		return HaltScheduler
	}
	if b.storageHalted.Load() {
		return HaltStorageFull
	}
	if b.armyEmpty.Load() {
		return HaltArmyEmpty
	}
	if sessionCapReached(b.cfg.Attack.MaxAttackPerSession, b.attackCount.Load()) {
		return HaltCap
	}
	return HaltNone
}

// HaltedReason returns the current halt verdict for the UI.
func (b *Bot) HaltedReason() HaltReason {
	b.gate.mu.Lock()
	defer b.gate.mu.Unlock()
	return b.haltLocked()
}

// intentionalIdle reports whether a stale lastAction is expected right now,
// i.e. whether the watchdog must not treat it as a hang.
//
// Only the village counts: every halt reason above describes a village the bot
// is deliberately not acting on. Anywhere else (a dialog, a dead capture, an
// unknown screen) a stale lastAction still means trouble and the ladder must
// run.
func (b *Bot) intentionalIdle(inVillage bool) bool {
	if !inVillage {
		return false
	}
	if b.storageHalted.Load() || b.armyEmpty.Load() {
		return true
	}
	if b.scheduler != nil && b.cfg.Schedule.Enabled {
		// Cached verdict: checkStuck runs every frame, and CanAttack plans the
		// next session/break as a side effect.
		if allowed, _ := b.scheduleAllowed(); !allowed {
			return true
		}
	}
	return false
}

// -- scheduler counter persistence ------------------------------------------

// sessionState is the slice of scheduler state that must survive a restart.
// It is deliberately tiny: a date, one counter, and the timestamps inside the
// rate-limit window.
type sessionState struct {
	UpdatedAt     string   `json:"updated_at"`
	DailyAttacks  int      `json:"daily_attacks"`
	DailyDate     string   `json:"daily_date"`     // local YYYY-MM-DD
	HourlyAttacks []string `json:"hourly_attacks"` // RFC3339Nano, trimmed to 1h
}

func sessionStatePath() string { return paths.ResolveConfig("session_state.json") }

// loadSessionState returns the last saved counters. A missing or unreadable
// file is not an error worth escalating: a fresh profile simply starts at zero.
func loadSessionState() (sessionState, error) {
	raw, err := os.ReadFile(sessionStatePath())
	if err != nil {
		return sessionState{}, err
	}
	var st sessionState
	if err := json.Unmarshal(raw, &st); err != nil {
		return sessionState{}, fmt.Errorf("parse session state: %w", err)
	}
	return st, nil
}

// saveSessionState writes the counters atomically, so a kill -9 mid-write can
// never leave a truncated JSON file that the next boot would silently ignore.
func saveSessionState(st sessionState) error {
	st.UpdatedAt = time.Now().Format(time.RFC3339)
	raw, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal session state: %w", err)
	}
	path := sessionStatePath()
	tmp := filepath.Join(filepath.Dir(path), "."+filepath.Base(path)+".tmp")
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return fmt.Errorf("write session state: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("replace session state: %w", err)
	}
	return nil
}

// persistSchedulerCounters snapshots the scheduler's rate-limit counters to
// disk. Called once per completed attack and on shutdown.
func (b *Bot) persistSchedulerCounters() {
	if b.scheduler == nil {
		return
	}
	daily, hourly, date := b.scheduler.Counters()
	st := sessionState{DailyAttacks: daily, DailyDate: date}
	for _, t := range hourly {
		st.HourlyAttacks = append(st.HourlyAttacks, t.Format(time.RFC3339Nano))
	}
	if err := saveSessionState(st); err != nil && !b.persistFailed.Swap(true) {
		b.logger.Warn().Err(err).Msg("could not persist scheduler counters; daily cap will restart with the process")
	}
}

// restoreSchedulerCounters loads the persisted counters into the scheduler. A
// stale day is dropped (the scheduler resets at local midnight anyway) and
// timestamps outside the rate window are ignored by CanAttack.
func (b *Bot) restoreSchedulerCounters() {
	if b.scheduler == nil {
		return
	}
	st, err := loadSessionState()
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			b.logger.Warn().Err(err).Msg("could not read session state; daily cap starts fresh")
		}
		return
	}
	hourly := make([]time.Time, 0, len(st.HourlyAttacks))
	for _, raw := range st.HourlyAttacks {
		if t, perr := time.Parse(time.RFC3339Nano, raw); perr == nil {
			hourly = append(hourly, t)
		}
	}
	b.scheduler.RestoreCounters(st.DailyAttacks, st.DailyDate, hourly)
	if st.DailyAttacks > 0 {
		b.logger.Info().
			Int("daily_attacks", st.DailyAttacks).
			Str("daily_date", st.DailyDate).
			Int("hourly_window", len(hourly)).
			Msg("restored scheduler counters from the last run")
	}
}
