package bot

import (
	"fmt"
	"sync"
	"time"

	"github.com/Ducky705/ClashGO/internal/config"
	"github.com/rs/zerolog"
)

type SchedulerState string

const (
	SchedulerActive SchedulerState = "ACTIVE"
	SchedulerBreak  SchedulerState = "BREAK"
	SchedulerSleep  SchedulerState = "SLEEP"
)

type Scheduler struct {
	cfg        config.ScheduleConfig
	logger     zerolog.Logger
	mu         sync.Mutex
	state      SchedulerState
	sessionEnd time.Time
	breakEnd   time.Time

	// Throttles. dailyDate is the local calendar day dailyAttacks belongs to;
	// comparing dates (rather than counting 24 h from construction) makes the
	// cap reset at midnight the way a player's day does, and lets a restored
	// counter know whether it is still current.
	hourlyAttacks []time.Time
	dailyAttacks  int
	dailyDate     string

	// Hooks on break transitions
	onEnterBreak func()
	onExitBreak  func()
}

func NewScheduler(cfg config.ScheduleConfig, logger zerolog.Logger) *Scheduler {
	s := &Scheduler{
		cfg:           cfg,
		logger:        logger.With().Str("component", "scheduler").Logger(),
		state:         SchedulerActive,
		hourlyAttacks: make([]time.Time, 0, 10),
		dailyDate:     localDay(time.Now()),
	}
	s.planNextSession(time.Now())
	return s
}

func (s *Scheduler) SetBreakHooks(onEnter, onExit func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onEnterBreak = onEnter
	s.onExitBreak = onExit
}

func (s *Scheduler) planNextSession(now time.Time) {
	durMin := s.cfg.SessionDurationMinutes
	if durMin <= 0 {
		s.sessionEnd = time.Time{}
		s.state = SchedulerActive
		return
	}
	// humanizedMinutes draws the per-session variation and, about one session
	// in six, a short check-in instead of a full farming block. A player's
	// session lengths are not uniformly distributed around one mean.
	sessionMins := humanizedMinutes(durMin, s.cfg.SessionJitterMinutes)
	if sessionMins < 10 {
		sessionMins = 10
	}
	s.sessionEnd = now.Add(time.Duration(sessionMins) * time.Minute)
	s.state = SchedulerActive
}

func (s *Scheduler) planNextBreak(now time.Time) {
	durMin := s.cfg.BreakDurationMinutes
	if durMin <= 0 {
		durMin = 20
	}
	// Same distribution for breaks, plus the occasional multi-hour absence
	// (humanizedBreakMinutes). Every break being 13-27 minutes for weeks is
	// its own pattern.
	breakMins := humanizedBreakMinutes(durMin, s.cfg.BreakJitterMinutes)
	if breakMins < 5 {
		breakMins = 5
	}
	s.breakEnd = now.Add(time.Duration(breakMins) * time.Minute)
	s.state = SchedulerBreak
	if s.onEnterBreak != nil {
		// Off the lock, on its own goroutine: the hook stops the game over ADB,
		// which takes seconds. Running it inline holds s.mu (and the caller's
		// gate mutex) for the duration, freezing the capture loop and the UI's
		// stats poll behind a device round trip.
		hook := s.onEnterBreak
		go hook()
	}
}

// CanAttack evaluates whether the bot is authorized to start an attack right now.
// Returns (allowed, reason).
func (s *Scheduler) CanAttack(now time.Time) (bool, string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.cfg.Enabled {
		return true, "scheduler disabled (unlimited)"
	}

	// 1. Check active hours window
	if s.cfg.ActiveHoursStart != "" && s.cfg.ActiveHoursEnd != "" {
		curStr := now.Format("15:04")
		start := s.cfg.ActiveHoursStart
		end := s.cfg.ActiveHoursEnd
		if start <= end {
			if curStr < start || curStr > end {
				s.state = SchedulerSleep
				return false, fmt.Sprintf("outside active hours (%s - %s; current is %s)", start, end, curStr)
			}
		} else {
			if curStr < start && curStr > end {
				s.state = SchedulerSleep
				return false, fmt.Sprintf("outside overnight active hours (%s - %s; current is %s)", start, end, curStr)
			}
		}
	}

	// If resuming from sleep, initialize session
	if s.state == SchedulerSleep {
		s.planNextSession(now)
	}

	// 2. Check if currently in break
	if s.state == SchedulerBreak {
		if now.Before(s.breakEnd) {
			return false, fmt.Sprintf("in break until %s (remaining: %v)", s.breakEnd.Format("15:04:05"), s.breakEnd.Sub(now).Round(time.Second))
		}
		s.logger.Info().Msg("scheduler break ended; transitioning back to active session")
		if s.onExitBreak != nil {
			s.onExitBreak()
		}
		s.planNextSession(now)
	}

	// 3. Check session duration expiry
	if s.state == SchedulerActive && !s.sessionEnd.IsZero() && now.After(s.sessionEnd) {
		s.logger.Info().Msg("scheduler session duration elapsed; entering organic break")
		s.planNextBreak(now)
		return false, fmt.Sprintf("session expired; entered break until %s", s.breakEnd.Format("15:04:05"))
	}

	// 4. Rate-limit checks (attacks per hour / per day)
	oneHourAgo := now.Add(-1 * time.Hour)
	valid := s.hourlyAttacks[:0]
	for _, t := range s.hourlyAttacks {
		if t.After(oneHourAgo) {
			valid = append(valid, t)
		}
	}
	s.hourlyAttacks = valid
	if s.cfg.MaxAttacksPerHour > 0 && len(s.hourlyAttacks) >= s.cfg.MaxAttacksPerHour {
		return false, fmt.Sprintf("hourly attack cap reached (%d/%d attacks in past hour)", len(s.hourlyAttacks), s.cfg.MaxAttacksPerHour)
	}

	if today := localDay(now); today != s.dailyDate {
		s.dailyAttacks = 0
		s.dailyDate = today
	}
	if s.cfg.MaxAttacksPerDay > 0 && s.dailyAttacks >= s.cfg.MaxAttacksPerDay {
		return false, fmt.Sprintf("daily attack cap reached (%d/%d attacks today)", s.dailyAttacks, s.cfg.MaxAttacksPerDay)
	}

	return true, "active"
}

// RecordAttack records an attack completion for rate limiting.
func (s *Scheduler) RecordAttack(now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hourlyAttacks = append(s.hourlyAttacks, now)
	if today := localDay(now); today != s.dailyDate {
		s.dailyAttacks = 0
		s.dailyDate = today
	}
	s.dailyAttacks++
}

// Counters returns the rate-limit state for persistence: today's count, the
// day it belongs to, and the timestamps inside the hourly window (pruned to
// one hour so the saved file cannot grow without bound).
func (s *Scheduler) Counters() (daily int, hourly []time.Time, date string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cutoff := time.Now().Add(-1 * time.Hour)
	kept := make([]time.Time, 0, len(s.hourlyAttacks))
	for _, t := range s.hourlyAttacks {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	return s.dailyAttacks, kept, s.dailyDate
}

// RestoreCounters seeds the rate-limit state from the previous process. A
// counter from an older day is dropped: it cannot apply to today's cap, and
// keeping it would only make the UI report a number that is not this day's.
func (s *Scheduler) RestoreCounters(daily int, date string, hourly []time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	today := localDay(time.Now())
	if date != today {
		s.dailyAttacks, s.dailyDate = 0, today
	} else {
		s.dailyAttacks, s.dailyDate = daily, date
	}
	cutoff := time.Now().Add(-1 * time.Hour)
	s.hourlyAttacks = s.hourlyAttacks[:0]
	for _, t := range hourly {
		if t.After(cutoff) {
			s.hourlyAttacks = append(s.hourlyAttacks, t)
		}
	}
}

// AttacksToday reports today's completed attacks against the configured cap,
// for the dashboard. A cap of 0 means unlimited.
func (s *Scheduler) AttacksToday() (attacks, cap int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if localDay(time.Now()) != s.dailyDate {
		return 0, s.cfg.MaxAttacksPerDay
	}
	return s.dailyAttacks, s.cfg.MaxAttacksPerDay
}

// NextChange is when the scheduler state changes by itself: the end of the
// current break, the end of the current session, or the next active-hours
// boundary. Zero means "no scheduled change" (scheduler disabled).
func (s *Scheduler) NextChange(now time.Time) time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.cfg.Enabled {
		return time.Time{}
	}
	switch s.state {
	case SchedulerBreak:
		if s.breakEnd.After(now) {
			return s.breakEnd
		}
	case SchedulerActive:
		if s.sessionEnd.After(now) {
			return s.sessionEnd
		}
	}
	return time.Time{}
}

// localDay is the local calendar day a counter belongs to.
func localDay(t time.Time) string { return t.Format("2006-01-02") }

// TriggerPersonalBreak triggers an immediate break when Supercell shows Personal Break modal.
func (s *Scheduler) TriggerPersonalBreak(duration time.Duration, reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if duration <= 0 {
		duration = 15 * time.Minute
	}
	s.logger.Warn().Str("reason", reason).Dur("duration", duration).Msg("triggering emergency break")
	s.breakEnd = time.Now().Add(duration)
	s.state = SchedulerBreak
	if s.onEnterBreak != nil {
		hook := s.onEnterBreak
		go hook()
	}
}

func (s *Scheduler) State() SchedulerState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state
}
