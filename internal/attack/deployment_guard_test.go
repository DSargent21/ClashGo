package attack

import (
	"context"
	"errors"
	"testing"

	"github.com/Ducky705/ClashGO/internal/game"
	"github.com/rs/zerolog"
	"gocv.io/x/gocv"
)

// The guard exists to stop mid-deploy tapping once the battle is over. The
// evidence threshold is the whole safety story: one misclassified frame must
// not cut a deploy short, and a real end must be seen twice before input is
// blocked. These tests pin that threshold without an emulator.

func TestDeploymentGuard_RequiresTwoConsecutiveEndFrames(t *testing.T) {
	g := newDeploymentGuard(context.Background())

	g.observe(game.StateBattle)
	if err := g.Err(); err != nil {
		t.Fatalf("battle frame armed the guard: %v", err)
	}

	g.observe(game.StateBattleEnd)
	if err := g.Err(); err != nil {
		t.Fatalf("one end frame confirmed battle end (%v); want at least %d", err, deployEndConfirmFrames)
	}

	g.observe(game.StateBattleEnd)
	if !errors.Is(g.Err(), ErrBattleFinished) {
		t.Fatalf("two consecutive end frames: Err() = %v, want ErrBattleFinished", g.Err())
	}
}

func TestDeploymentGuard_NonBattleFrameResetsStreak(t *testing.T) {
	g := newDeploymentGuard(context.Background())

	g.observe(game.StateBattleEnd) // streak 1
	g.observe(game.StateMainVillage)
	g.observe(game.StateBattleEnd) // streak restarts at 1
	if err := g.Err(); err != nil {
		t.Fatalf("streak not reset by the intervening non-battle frame: %v", err)
	}

	g.observe(game.StateBattleEnd) // streak 2
	if !errors.Is(g.Err(), ErrBattleFinished) {
		t.Fatalf("restarted streak never confirmed: Err() = %v", g.Err())
	}
}

func TestDeploymentGuard_ReturnHomeCountsAsBattleEnd(t *testing.T) {
	g := newDeploymentGuard(context.Background())
	g.observe(game.StateReturnHome)
	g.observe(game.StateReturnHome)
	if !errors.Is(g.Err(), ErrBattleFinished) {
		t.Fatalf("two ReturnHome frames: Err() = %v, want ErrBattleFinished", g.Err())
	}
}

func TestDeploymentGuard_ContextCancelBlocksInput(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	g := newDeploymentGuard(ctx)
	if err := g.Err(); err != nil {
		t.Fatalf("fresh guard: Err() = %v, want nil", err)
	}
	cancel()
	if !errors.Is(g.Err(), context.Canceled) {
		t.Fatalf("canceled ctx: Err() = %v, want context.Canceled", g.Err())
	}
}

func TestDeploymentGuard_NilReceiverIsSafe(t *testing.T) {
	var g *deploymentGuard
	if err := g.Err(); err != nil {
		t.Fatalf("nil guard: Err() = %v, want nil", err)
	}
	g.observe(game.StateBattleEnd) // must not panic
}

func TestConfirmedBattleEnd(t *testing.T) {
	cases := []struct {
		name   string
		states []game.GameState
		want   bool
	}{
		{"empty", nil, false},
		{"battle only", []game.GameState{game.StateBattle}, false},
		{"single end frame", []game.GameState{game.StateBattleEnd}, false},
		{"two end frames", []game.GameState{game.StateBattleEnd, game.StateBattleEnd}, true},
		{"streak broken by battle", []game.GameState{game.StateBattleEnd, game.StateBattle, game.StateBattleEnd}, false},
		{"streak restarts", []game.GameState{game.StateBattleEnd, game.StateBattle, game.StateBattleEnd, game.StateBattleEnd}, true},
		{"end then return home", []game.GameState{game.StateBattleEnd, game.StateReturnHome}, true},
		{"one end then stop", []game.GameState{game.StateBattle, game.StateBattleEnd}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := confirmedBattleEnd(tc.states); got != tc.want {
				t.Fatalf("confirmedBattleEnd(%v) = %v, want %v", tc.states, got, tc.want)
			}
		})
	}
}

// SetDeploymentContext arms the client input guard and a watcher goroutine;
// teardown must join the watcher and clear every field so the NEXT battle
// starts from a clean guard, and the client must stop rejecting input once
// teardown has run.
func TestSetDeploymentContext_TeardownClearsGuard(t *testing.T) {
	client := newClosedTestClient(t)
	cal := &game.Calibration{PhysicalW: 860, PhysicalH: 732, ScaleX: 1.0, ScaleY: 1.0}
	e := &Executor{
		client:   client,
		cal:      cal,
		logger:   zerolog.Nop(),
		classify: func(m gocv.Mat) (game.GameState, int) { return game.StateBattle, 0 },
	}
	taps := NewTapExecutor(client, cal, zerolog.Nop())

	ctx, cancel := context.WithCancel(context.Background())
	stop := e.SetDeploymentContext(ctx, taps)
	if e.deployGuard == nil || e.tapExec == nil || e.deployCtx == nil {
		stop()
		t.Fatal("SetDeploymentContext did not arm deploy state")
	}

	cancel()
	if err := e.deploymentErr(); !errors.Is(err, context.Canceled) {
		stop()
		t.Fatalf("deploymentErr() after cancel = %v, want context.Canceled", err)
	}
	stop()

	if e.deployGuard != nil || e.tapExec != nil || e.deployCtx != nil {
		t.Fatal("teardown left deploy state armed")
	}
	// With the guard cleared, the client's own failure ("client closed")
	// must surface — not the guard's context error.
	if err := client.Tap(1, 1); errors.Is(err, context.Canceled) {
		t.Fatalf("input guard still installed after teardown: %v", err)
	}
}
