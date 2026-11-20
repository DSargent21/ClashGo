package attack

import (
	"context"
	"image"
	"testing"
	"time"

	"github.com/Ducky705/ClashGO/internal/adb"
	"github.com/Ducky705/ClashGO/internal/game"
	"github.com/rs/zerolog"
	"gocv.io/x/gocv"
)

// speedPillExecutor drives WaitForBattleEndCtx from a fixture frame and records
// every tap the executor would send. The ADB client is closed, so no tap can
// reach a device; the tap hook still sees each attempt.
func speedPillExecutor(t *testing.T, framePath string, ticks <-chan time.Time, taps chan<- adb.TapEvent, captures chan<- struct{}) *Executor {
	t.Helper()
	ts, err := game.NewTemplateStore("../../assets/templates")
	if err != nil {
		t.Fatalf("NewTemplateStore: %v", err)
	}
	if err := ts.LoadTemplates(); err != nil {
		t.Fatalf("LoadTemplates: %v", err)
	}
	t.Cleanup(ts.Close)

	client := adb.NewClient(adb.WithJitterTaps(false), adb.WithJitterDelays(false), adb.WithTimeout(1))
	client.Close()
	t.Cleanup(func() { _ = client.Close() })
	client.SetTapHook(func(ev adb.TapEvent) {
		select {
		case taps <- ev:
		default:
		}
	})

	hooks := &battleWaitHooks{
		now:  func() time.Time { return time.Unix(1_700_000_000, 0) },
		tick: ticks,
		capture: func() (gocv.Mat, error) {
			select {
			case captures <- struct{}{}:
			default:
			}
			return gocv.IMRead(framePath, gocv.IMReadColor), nil
		},
	}

	e := &Executor{
		client:        client,
		cal:           game.NewCalibration(1024, 768),
		logger:        zerolog.Nop(),
		classify:      func(gocv.Mat) (game.GameState, int) { return game.StateBattle, 0 },
		templateStore: ts,
		waitHooks:     hooks,
	}
	e.cfgPtr.Store(minimalAttackConfig())
	return e
}

func runBattleWaitOnce(t *testing.T, e *Executor, ticks chan time.Time) (cancel func()) {
	t.Helper()
	ctx, cancelCtx := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		e.WaitForBattleEndCtx(ctx, 10*time.Minute)
	}()
	ticks <- time.Unix(1_700_000_000, 0)
	return func() {
		cancelCtx()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("battle wait did not stop after cancel")
		}
	}
}

// A "1x" pill on the battle frame must produce exactly one tap, on the pill.
func TestWaitForBattleEnd_Taps1xSpeedPill(t *testing.T) {
	ticks := make(chan time.Time)
	taps := make(chan adb.TapEvent, 4)
	captures := make(chan struct{}, 4)
	e := speedPillExecutor(t, "../game/testdata/live/battle_speed_1x_1024x768.png", ticks, taps, captures)
	cancel := runBattleWaitOnce(t, e, ticks)
	defer cancel()

	select {
	case ev := <-taps:
		if d := image.Pt(ev.X, ev.Y).Sub(image.Pt(978, 478)); d.X < -8 || d.X > 8 || d.Y < -8 || d.Y > 8 {
			t.Fatalf("speed tap at (%d,%d), want (978,478) ±8", ev.X, ev.Y)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("1x speed pill was on the frame but no tap was sent")
	}
}

// The same pill after a tap reads "4x"; tapping it again would slow the battle
// back down, so the wait loop must leave it alone.
func TestWaitForBattleEnd_Leaves4xSpeedPillAlone(t *testing.T) {
	ticks := make(chan time.Time)
	taps := make(chan adb.TapEvent, 4)
	captures := make(chan struct{}, 4)
	e := speedPillExecutor(t, "../game/testdata/live/battle_speed_4x_1024x768.png", ticks, taps, captures)
	cancel := runBattleWaitOnce(t, e, ticks)
	defer cancel()

	// Wait until the loop has consumed the frame, then check no tap followed.
	select {
	case <-captures:
	case <-time.After(3 * time.Second):
		t.Fatal("battle wait did not capture a frame")
	}
	select {
	case ev := <-taps:
		t.Fatalf("tapped a 4x speed pill at (%d,%d)", ev.X, ev.Y)
	case <-time.After(300 * time.Millisecond):
	}
}
