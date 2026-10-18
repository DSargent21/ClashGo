package bot

import (
	"os"
	"testing"
	"time"

	"github.com/Ducky705/ClashGO/internal/config"
	"github.com/rs/zerolog"
)

func newTestScheduler(cfg config.ScheduleConfig) *Scheduler {
	logger := zerolog.New(os.Stderr).Level(zerolog.Disabled)
	return NewScheduler(cfg, logger)
}

func TestScheduler_Disabled(t *testing.T) {
	s := newTestScheduler(config.ScheduleConfig{Enabled: false})
	allowed, reason := s.CanAttack(time.Now())
	if !allowed {
		t.Fatalf("expected allowed when disabled, got reason: %s", reason)
	}
}

func TestScheduler_ActiveHours(t *testing.T) {
	s := newTestScheduler(config.ScheduleConfig{
		Enabled:          true,
		ActiveHoursStart: "09:00",
		ActiveHoursEnd:   "17:00",
	})

	inWindow := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	allowed, _ := s.CanAttack(inWindow)
	if !allowed {
		t.Errorf("expected allowed at 12:00 in 09:00-17:00 window")
	}

	outWindow := time.Date(2026, 10, 17, 20, 0, 0, 0, time.UTC)
	allowed, _ = s.CanAttack(outWindow)
	if allowed {
		t.Errorf("expected rejected at 20:00 outside 09:00-17:00 window")
	}
}

func TestScheduler_HourlyCap(t *testing.T) {
	s := newTestScheduler(config.ScheduleConfig{
		Enabled:           true,
		MaxAttacksPerHour: 2,
	})

	now := time.Now()
	s.RecordAttack(now.Add(-40 * time.Minute))
	s.RecordAttack(now.Add(-20 * time.Minute))

	allowed, reason := s.CanAttack(now)
	if allowed {
		t.Errorf("expected rejected when hourly cap reached, reason: %s", reason)
	}

	// 70 minutes later: attacks pruned
	allowed, _ = s.CanAttack(now.Add(70 * time.Minute))
	if !allowed {
		t.Errorf("expected allowed after previous attacks expired")
	}
}

func TestScheduler_BreakCycle(t *testing.T) {
	breakHookCalled := false
	exitHookCalled := false

	s := newTestScheduler(config.ScheduleConfig{
		Enabled:                true,
		SessionDurationMinutes: 60,
		SessionJitterMinutes:   0,
		BreakDurationMinutes:   20,
		BreakJitterMinutes:     0,
	})
	s.SetBreakHooks(func() { breakHookCalled = true }, func() { exitHookCalled = true })

	now := time.Now()
	// Before session expiry: allowed
	allowed, _ := s.CanAttack(now.Add(30 * time.Minute))
	if !allowed {
		t.Errorf("expected allowed during active session")
	}

	// Session expires at +61 minutes: should enter break
	allowed, reason := s.CanAttack(now.Add(61 * time.Minute))
	if allowed {
		t.Errorf("expected break triggered at +61m")
	}
	if !breakHookCalled {
		t.Errorf("expected break hook called")
	}
	t.Logf("break reason: %s", reason)

	// In break at +70 minutes: still rejected
	allowed, _ = s.CanAttack(now.Add(70 * time.Minute))
	if allowed {
		t.Errorf("expected rejected while in break")
	}

	// Break finishes at +82 minutes: allowed again
	allowed, _ = s.CanAttack(now.Add(82 * time.Minute))
	if !allowed {
		t.Errorf("expected allowed after break finishes")
	}
	if !exitHookCalled {
		t.Errorf("expected exit hook called")
	}
}

func TestScheduler_PersonalBreak(t *testing.T) {
	s := newTestScheduler(config.ScheduleConfig{Enabled: true})
	s.TriggerPersonalBreak(15*time.Minute, "Supercell PB")

	now := time.Now()
	allowed, reason := s.CanAttack(now.Add(5 * time.Minute))
	if allowed {
		t.Errorf("expected rejected during emergency break")
	}
	t.Logf("pb rejection reason: %s", reason)

	allowed, _ = s.CanAttack(now.Add(16 * time.Minute))
	if !allowed {
		t.Errorf("expected allowed after emergency break")
	}
}
