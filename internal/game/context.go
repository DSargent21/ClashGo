package game

import (
	"fmt"
	"sync"
	"time"

	"gocv.io/x/gocv"
)

// GameContext holds all shared game state. All fields are protected by
// the embedded RWMutex. Callers must hold the read lock when reading
// and the write lock when mutating.
//
// For screen capture, use ReadScreen() which returns the mat and an
// unlock func. NEVER close the returned mat.
//
// Context lifecycle: created once at startup, passed to all subsystems
// as a read-only reference. Only the main loop writes to it.
type GameContext struct {
	sync.RWMutex

	State          GameState
	prevState      GameState
	StateSince     time.Time
	StateDuration  time.Duration
	StateChanges   int

	pending       GameState
	confirmFrames int

	Resources     Resources
	TownHallLevel int
	BuilderCount  int
	BuilderTotal  int

	ArmyFull      bool
	CCStatus      string

	BattleStars   int
	EnemyTHLevel  int
	EnemyResources Resources
	IsWeakBase    bool

	Health        SystemHealth

	screen        safeScreen

	StateChange chan StateChange
}

func NewGameContext() *GameContext {
	return &GameContext{
		State:       StateUnknown,
		StateSince:  time.Now(),
		StateChange: make(chan StateChange, 5),
	}
}

func (gc *GameContext) Close() error {
	gc.Lock()
	if !gc.screen.mat.Empty() {
		gc.screen.mat.Close()
	}
	gc.Unlock()
	return nil
}

func (gc *GameContext) UpdateState(newState GameState, now time.Time) {
	gc.Lock()
	defer gc.Unlock()

	if gc.State != newState {
		gc.prevState = gc.State
		prevDuration := now.Sub(gc.StateSince)

		gc.State = newState
		gc.StateSince = now
		gc.StateDuration = prevDuration
		gc.StateChanges++

		select {
		case gc.StateChange <- StateChange{
			From:     gc.prevState,
			To:       newState,
			At:       now,
			Duration: prevDuration,
		}:
		default:
		}
	}
}

func (gc *GameContext) ConfirmState(state GameState) bool {
	gc.Lock()
	defer gc.Unlock()

	if state == gc.pending {
		gc.confirmFrames++
	} else {
		gc.pending = state
		gc.confirmFrames = 1
	}

	return gc.confirmFrames >= 2
}

func (gc *GameContext) PrevState() GameState {
	gc.RLock()
	defer gc.RUnlock()
	return gc.prevState
}

func (gc *GameContext) ResetConfirm() {
	gc.Lock()
	defer gc.Unlock()
	gc.pending = StateUnknown
	gc.confirmFrames = 0
}

func (gc *GameContext) UpdateResources(r Resources) {
	gc.Lock()
	defer gc.Unlock()
	gc.Resources = r
}

func (gc *GameContext) UpdateScreen(mat gocv.Mat, captureMs time.Duration) {
	gc.Lock()
	gc.screen.Store(mat)
	gc.Health.LastCapture = time.Now()
	gc.Health.AvgCaptureMs = gc.Health.AvgCaptureMs*0.9 + captureMs.Seconds()*1000*0.1
	gc.Health.ConsecutiveFails = 0
	gc.Unlock()
}

func (gc *GameContext) ReadScreen() (gocv.Mat, time.Time, func()) {
	gc.screen.mu.RLock()
	mat := gc.screen.mat
	at := gc.screen.at
	return mat, at, gc.screen.mu.RUnlock
}

func (gc *GameContext) ReadState() (GameState, GameState, time.Time) {
	gc.RLock()
	defer gc.RUnlock()
	return gc.State, gc.prevState, gc.StateSince
}

func (gc *GameContext) ReadHealth() SystemHealth {
	gc.RLock()
	defer gc.RUnlock()
	return gc.Health
}

func (gc *GameContext) RecordCaptureError() {
	gc.Lock()
	defer gc.Unlock()
	gc.Health.ConsecutiveFails++
}

func (gc *GameContext) SetADBConnected(v bool) {
	gc.Lock()
	defer gc.Unlock()
	gc.Health.ADBConnected = v
}

func (gc *GameContext) IsHealthy() bool {
	gc.RLock()
	defer gc.RUnlock()
	return gc.Health.IsHealthy()
}

func (gc *GameContext) String() string {
	gc.RLock()
	defer gc.RUnlock()
	return fmt.Sprintf("GameContext{state=%s since=%v resources={G:%d E:%d DE:%d T:%d} th=%d builder=%d/%d}",
		gc.State, gc.StateSince.Format("15:04:05"),
		gc.Resources.Gold, gc.Resources.Elixir, gc.Resources.DarkElixir, gc.Resources.Trophies,
		gc.TownHallLevel, gc.BuilderCount, gc.BuilderTotal)
}