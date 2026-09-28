package attack

import (
	"context"
	"errors"
	"sync/atomic"
	"time"

	"github.com/Ducky705/ClashGO/internal/game"
	"gocv.io/x/gocv"
)

var ErrBattleFinished = errors.New("battle ended during deployment")

const (
	deployGuardPollInterval = 300 * time.Millisecond
	deployEndConfirmFrames  = 2
)

// deploymentGuard watches fresh battle frames while deployment is running.
// Non-battle/unknown frames are deliberately inconclusive; two consecutive
// positive battle-end classifications are required before input is blocked.
type deploymentGuard struct {
	ctx           context.Context
	ended         atomic.Bool
	endRuns       atomic.Int32
	onBattleFrame func(gocv.Mat)
}

func newDeploymentGuard(ctx context.Context) *deploymentGuard {
	return &deploymentGuard{ctx: ctx}
}

func (g *deploymentGuard) observe(state game.GameState) {
	if g == nil || g.ended.Load() {
		return
	}
	if state == game.StateBattleEnd || state == game.StateReturnHome {
		if g.endRuns.Add(1) >= deployEndConfirmFrames {
			g.ended.Store(true)
		}
		return
	}
	g.endRuns.Store(0)
}

func (g *deploymentGuard) Err() error {
	if g == nil {
		return nil
	}
	if err := g.ctx.Err(); err != nil {
		return err
	}
	if g.ended.Load() {
		return ErrBattleFinished
	}
	return nil
}

func (e *Executor) watchDeployment(ctx context.Context, guard *deploymentGuard) {
	ticker := time.NewTicker(deployGuardPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		frame, err := e.client.CaptureToMat()
		if err != nil {
			continue
		}
		if frame.Empty() {
			frame.Close()
			continue
		}
		state, _ := e.classify(frame)
		guard.observe(state)
		if state == game.StateBattle && guard.onBattleFrame != nil && guard.Err() == nil {
			guard.onBattleFrame(frame)
		}
		frame.Close()
	}
}

// confirmedBattleEnd is kept separate from capture plumbing so the evidence
// threshold stays deterministic and can be exercised without an emulator.
func confirmedBattleEnd(states []game.GameState) bool {
	guard := newDeploymentGuard(context.Background())
	for _, state := range states {
		guard.observe(state)
		if guard.ended.Load() {
			return true
		}
	}
	return false
}
