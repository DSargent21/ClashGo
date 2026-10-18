package bot

import (
	"fmt"
	"math/rand"
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

	// Throttles
	hourlyAttacks []time.Time
	dailyAttacks  int
	dailyReset    time.Time

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
		dailyReset:    time.Now().Add(24 * time.Hour),
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
	jitterMin := s.cfg.SessionJitterMinutes
	delta := 0
	if jitterMin > 0 {
		delta = rand.Intn(2*jitterMin+1) - jitterMin
	}
	sessionMins := durMin + delta
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
	jitterMin := s.cfg.BreakJitterMinutes
	delta := 0
	if jitterMin > 0 {
		delta = rand.Intn(2*jitterMin+1) - jitterMin
	}
	breakMins := durMin + delta
	if breakMins < 5 {
		breakMins = 5
	}
	s.breakEnd = now.Add(time.Duration(breakMins) * time.Minute)
	s.state = SchedulerBreak
	if s.onEnterBreak != nil {
		s.onEnterBreak()
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

	if now.After(s.dailyReset) {
		s.dailyAttacks = 0
		s.dailyReset = now.Add(24 * time.Hour)
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
	s.dailyAttacks++
}

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
		s.onEnterBreak()
	}
}

func (s *Scheduler) State() SchedulerState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state
}
