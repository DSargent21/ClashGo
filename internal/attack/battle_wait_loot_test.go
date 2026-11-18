package attack

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Ducky705/ClashGO/internal/config"
	"github.com/Ducky705/ClashGO/internal/game"
	"github.com/rs/zerolog"
	"gocv.io/x/gocv"
)

// fakeBattleWait drives WaitForBattleEndCtx from channels and a fixed clock:
// no real ticker and no device. Every hook signals `events`, so the test can
// wait for the loop to consume a tick instead of sleeping.
type fakeBattleWait struct {
	captures    int32
	hudCalls    int32
	buttonCalls int32
	hud         func(int) (game.Resources, error)
	button      bool
	events      chan struct{}
	started     chan struct{}
	startOnce   int32
}

func (f *fakeBattleWait) signal(ch chan struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}

func (f *fakeBattleWait) hooks(ticks <-chan time.Time) *battleWaitHooks {
	return &battleWaitHooks{
		now: func() time.Time {
			if atomic.CompareAndSwapInt32(&f.startOnce, 0, 1) {
				close(f.started)
			}
			return time.Unix(1_700_000_000, 0)
		},
		tick: ticks,
		capture: func() (gocv.Mat, error) {
			atomic.AddInt32(&f.captures, 1)
			f.signal(f.events)
			return gocv.NewMat(), nil
		},
		buttonVisible: func(gocv.Mat, StallConfig) bool {
			atomic.AddInt32(&f.buttonCalls, 1)
			f.signal(f.events)
			return f.button
		},
		readLootHUD: func(gocv.Mat) (game.Resources, error) {
			n := int(atomic.AddInt32(&f.hudCalls, 1))
			f.signal(f.events)
			return f.hud(n)
		},
	}
}

func newWaitTestExecutor(t *testing.T, cfg *config.AttackConfig, hooks *battleWaitHooks) *Executor {
	t.Helper()
	ts, err := game.NewTemplateStore("../../assets/templates")
	if err != nil {
		t.Fatalf("NewTemplateStore: %v", err)
	}
	if err := ts.LoadTemplates(); err != nil {
		t.Fatalf("LoadTemplates: %v", err)
	}
	t.Cleanup(ts.Close)
	e := &Executor{
		client:        newClosedTestClient(t),
		cal:           game.NewCalibration(1280, 720),
		logger:        zerolog.Nop(),
		classify:      func(gocv.Mat) (game.GameState, int) { return game.StateBattle, 0 },
		templateStore: ts,
		waitHooks:     hooks,
	}
	e.cfgPtr.Store(cfg)
	return e
}

// waitForEvents blocks until n hook signals arrive. It is a guard against a
// hung loop, not the thing that decides when the test proceeds: n is derived
// from the exact hooks the case expects.
func waitForEvents(t *testing.T, events <-chan struct{}, n int) {
	t.Helper()
	timeout := time.After(2 * time.Second)
	for i := 0; i < n; i++ {
		select {
		case <-events:
		case <-timeout:
			t.Fatalf("timed out after %d/%d hook calls", i, n)
		}
	}
}

func TestWaitForBattleEnd_LootTargetFakeClock(t *testing.T) {
	const goldTarget = 500_000
	base := game.Resources{Gold: 1_000_000, Elixir: 2_000_000, DarkElixir: 50_000}
	above := game.Resources{Gold: 1_500_000, Elixir: 2_000_000, DarkElixir: 50_000}
	low := game.Resources{Gold: 1_000_001, Elixir: 2_000_000, DarkElixir: 50_000}

	enabled := &config.AttackConfig{EndOnLootTarget: true, LootTargetGold: goldTarget}
	disabled := &config.AttackConfig{EndOnLootTarget: false, LootTargetGold: goldTarget}

	cases := []struct {
		name         string
		cfg          *config.AttackConfig
		button       bool
		hud          func(int) (game.Resources, error)
		ticks        int
		wantEnd      bool
		wantHUD      int32
		wantCaptures int32
		wantButtons  int32
		wantNoGain   bool
	}{
		{
			name:   "below target, button hidden: keeps waiting",
			cfg:    enabled,
			button: false,
			hud: func(n int) (game.Resources, error) {
				if n == 1 {
					return base, nil
				}
				return low, nil
			},
			ticks:        3,
			wantHUD:      3,
			wantCaptures: 3,
			wantButtons:  0,
		},
		{
			name:   "target reached, button hidden: keeps waiting",
			cfg:    enabled,
			button: false,
			hud: func(n int) (game.Resources, error) {
				if n == 1 {
					return base, nil
				}
				return above, nil
			},
			ticks:        3,
			wantHUD:      3,
			wantCaptures: 3,
			wantButtons:  2, // every tick after the baseline reached the button probe
		},
		{
			name:   "target reached, button visible: ends once",
			cfg:    enabled,
			button: true,
			hud: func(n int) (game.Resources, error) {
				return map[bool]game.Resources{true: above, false: base}[n > 1], nil
			},
			ticks:        2,
			wantEnd:      true,
			wantHUD:      2,
			wantCaptures: 2,
			wantButtons:  1,
		},
		{
			name:   "HUD runs backwards after baseline: gain clamps to zero",
			cfg:    enabled,
			button: true,
			hud: func(n int) (game.Resources, error) {
				if n == 1 {
					return base, nil
				}
				return game.Resources{Gold: 1, Elixir: 1, DarkElixir: 1}, nil
			},
			ticks:        3,
			wantHUD:      3,
			wantCaptures: 3,
			wantButtons:  0,
			wantNoGain:   true,
		},
		{
			name:         "feature disabled: never consults the HUD or ends",
			cfg:          disabled,
			button:       true,
			hud:          func(int) (game.Resources, error) { return above, nil },
			ticks:        3,
			wantHUD:      0,
			wantCaptures: 3,
			wantButtons:  0,
		},
		{
			name:   "HUD unreadable: no end, no panic",
			cfg:    enabled,
			button: true,
			hud: func(int) (game.Resources, error) {
				return game.Resources{}, errors.New("no readable counters")
			},
			ticks:        3,
			wantHUD:      3,
			wantCaptures: 3,
			wantButtons:  0,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ticks := make(chan time.Time, tc.ticks)
			f := &fakeBattleWait{
				hud:     tc.hud,
				button:  tc.button,
				events:  make(chan struct{}, 32),
				started: make(chan struct{}),
			}
			e := newWaitTestExecutor(t, tc.cfg, f.hooks(ticks))
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			done := make(chan bool, 1)
			go func() { done <- e.WaitForBattleEndCtx(ctx, time.Minute) }()
			<-f.started

			for i := 0; i < tc.ticks; i++ {
				ticks <- time.Unix(1_700_000_000, 0)
			}

			if tc.wantEnd {
				select {
				case ended := <-done:
					if !ended {
						t.Fatal("WaitForBattleEndCtx returned false after the loot target was reached")
					}
				case <-time.After(2 * time.Second):
					t.Fatal("WaitForBattleEndCtx did not end after the loot target")
				}
				if got := e.BattleEndReason(); got != "loot target" {
					t.Fatalf("BattleEndReason = %q, want %q", got, "loot target")
				}
			} else {
				// Every expected hook call must actually happen before cancel;
				// otherwise a loop that skipped the HUD/button would pass.
				waitForEvents(t, f.events, int(tc.wantHUD+tc.wantCaptures+tc.wantButtons))
				cancel()
				select {
				case ended := <-done:
					if ended {
						t.Fatal("WaitForBattleEndCtx ended a battle it should have kept alive")
					}
				case <-time.After(2 * time.Second):
					t.Fatal("WaitForBattleEndCtx did not return after cancellation")
				}
				if got := e.BattleEndReason(); got != "" {
					t.Fatalf("BattleEndReason = %q, want empty (no early end)", got)
				}
				if got := atomic.LoadInt32(&f.hudCalls); got != tc.wantHUD {
					t.Fatalf("HUD reads = %d, want %d", got, tc.wantHUD)
				}
				if got := atomic.LoadInt32(&f.captures); got != tc.wantCaptures {
					t.Fatalf("captures = %d, want %d", got, tc.wantCaptures)
				}
				if got := atomic.LoadInt32(&f.buttonCalls); got != tc.wantButtons {
					t.Fatalf("button probes = %d, want %d", got, tc.wantButtons)
				}
				if tc.wantNoGain && lootTargetReached(tc.cfg, base, game.Resources{Gold: 1, Elixir: 1, DarkElixir: 1}) {
					t.Fatal("backwards HUD was treated as a gain")
				}
			}

			// The end case must not have read the HUD or probed the button
			// extra times after it returned.
			if tc.wantEnd {
				if got := atomic.LoadInt32(&f.hudCalls); got != tc.wantHUD {
					t.Fatalf("HUD reads = %d, want %d", got, tc.wantHUD)
				}
				if got := atomic.LoadInt32(&f.buttonCalls); got != tc.wantButtons {
					t.Fatalf("button probes = %d, want %d", got, tc.wantButtons)
				}
			}
		})
	}
}

// No tick ever arrives: cancellation must return false without ending, and
// without a real ticker moving the loop. This is the "ticker unavailable" path.
func TestWaitForBattleEnd_NoTicks(t *testing.T) {
	ticks := make(chan time.Time) // never sent to
	f := &fakeBattleWait{
		hud:     func(int) (game.Resources, error) { return game.Resources{}, nil },
		events:  make(chan struct{}, 1),
		started: make(chan struct{}),
	}
	e := newWaitTestExecutor(t, &config.AttackConfig{EndOnLootTarget: true, LootTargetGold: 1}, f.hooks(ticks))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan bool, 1)
	go func() { done <- e.WaitForBattleEndCtx(ctx, time.Minute) }()
	<-f.started
	cancel()
	select {
	case ended := <-done:
		if ended {
			t.Fatal("a missing ticker ended the battle")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancellation did not stop the wait")
	}
}
