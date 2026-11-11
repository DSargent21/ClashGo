package bot

import (
	"context"
	"errors"
	"testing"

	"github.com/Ducky705/ClashGO/internal/game"
	"github.com/rs/zerolog"
)

type fakeZoomer struct {
	errs     []error // one per attempt; a nil entry means "confirmed"
	attempts int
}

func (f *fakeZoomer) ZoomOutMax() error {
	f.attempts++
	if f.attempts-1 < len(f.errs) {
		return f.errs[f.attempts-1]
	}
	return nil
}

// One confirmed zoom is enough, and it must not retry past the success: a retry
// after a confirmed max zoom-out spends two more seconds of an attack budget to
// learn nothing.
func TestEnsureBattleZoom_StopsOnFirstConfirmation(t *testing.T) {
	z := &fakeZoomer{}
	if err := ensureBattleZoom(z, battleZoomAttempts, zerolog.Nop()); err != nil {
		t.Fatalf("ensureBattleZoom() = %v, want nil", err)
	}
	if z.attempts != 1 {
		t.Errorf("attempts = %d, want 1", z.attempts)
	}
}

// A transient failure that a second attempt fixes must not abort the attack.
func TestEnsureBattleZoom_RetriesATransientFailure(t *testing.T) {
	z := &fakeZoomer{errs: []error{context.DeadlineExceeded}}
	if err := ensureBattleZoom(z, battleZoomAttempts, zerolog.Nop()); err != nil {
		t.Fatalf("ensureBattleZoom() = %v, want nil after the retry", err)
	}
	if z.attempts != 2 {
		t.Errorf("attempts = %d, want 2", z.attempts)
	}
}

// The failure this guard exists for: the camera never confirms. The error must
// say so and must carry the reason, because the caller refuses to deploy and the
// log is the only account of why the attack was abandoned.
func TestEnsureBattleZoom_ReportsAnUnconfirmedCamera(t *testing.T) {
	inert := game.ErrZoomInert
	z := &fakeZoomer{errs: []error{inert, inert}}
	err := ensureBattleZoom(z, battleZoomAttempts, zerolog.Nop())
	if !errors.Is(err, errBattleZoomUnconfirmed) {
		t.Fatalf("err = %v, want errBattleZoomUnconfirmed", err)
	}
	if !errors.Is(err, inert) {
		t.Errorf("err = %v, want it to wrap %v so the cause survives", err, inert)
	}
	if z.attempts != battleZoomAttempts {
		t.Errorf("attempts = %d, want %d", z.attempts, battleZoomAttempts)
	}
}

// A zero or negative budget still makes one attempt: "no attempts" would report a
// zoom failure without ever having pinched, which is a lie in the log.
func TestEnsureBattleZoom_AlwaysAttemptsOnce(t *testing.T) {
	z := &fakeZoomer{}
	if err := ensureBattleZoom(z, 0, zerolog.Nop()); err != nil {
		t.Fatalf("ensureBattleZoom() = %v, want nil", err)
	}
	if z.attempts != 1 {
		t.Errorf("attempts = %d, want 1", z.attempts)
	}
}
