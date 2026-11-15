package bot

import (
	"strings"
	"testing"
	"time"

	"github.com/Ducky705/ClashGO/internal/config"
	"github.com/Ducky705/ClashGO/internal/game"
)

func bbConfig() config.BuilderBaseConfig {
	cfg := config.DefaultConfig().BuilderBase
	cfg.Enabled = true
	return cfg
}

// The schedule is the whole feature without a device: it decides enabled,
// unlocked, mode, spacing and the daily cap, and it must say which one stopped it
// so a silent no-op cannot look like a broken bot.
func TestBuilderBasePlan(t *testing.T) {
	cfg := bbConfig()

	tests := []struct {
		name       string
		mutate     func(*config.BuilderBaseConfig)
		unlocked   bool
		since, day int
		want       bool
		wantReason string
	}{
		{name: "due after the spacing", since: 3, day: 0, unlocked: true, want: true},
		{name: "waits for the spacing", since: 2, day: 0, unlocked: true, want: false, wantReason: "2/3 home attacks"},
		{name: "disabled", mutate: func(c *config.BuilderBaseConfig) { c.Enabled = false }, since: 9, unlocked: true, want: false, wantReason: "enabled is off"},
		{name: "export says no builder base", since: 9, unlocked: false, want: false, wantReason: "no Builder Base"},
		{name: "gold mode is not implemented", mutate: func(c *config.BuilderBaseConfig) { c.Mode = "gold" }, since: 9, unlocked: true, want: false, wantReason: "not implemented"},
		{name: "daily cap", since: 9, day: 20, unlocked: true, want: false, wantReason: "daily Builder Base cap"},
		{name: "cap disabled", mutate: func(c *config.BuilderBaseConfig) { c.MaxPerDay = 0 }, since: 9, day: 500, unlocked: true, want: true},
		{name: "spacing zero means every attack", mutate: func(c *config.BuilderBaseConfig) { c.EveryNHomeAttacks = 0 }, since: 1, unlocked: true, want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := cfg
			if tt.mutate != nil {
				tt.mutate(&c)
			}
			got, why := builderBasePlan(c, tt.unlocked, tt.since, tt.day)
			if got != tt.want {
				t.Fatalf("due = %v, want %v (%s)", got, tt.want, why)
			}
			if tt.wantReason != "" && !strings.Contains(why, tt.wantReason) {
				t.Fatalf("reason = %q, want it to mention %q", why, tt.wantReason)
			}
		})
	}
}

// fakeBBDevice scripts the screens and records every tap, so the step order and
// the fail-closed behaviour are checked without adb or a game.
type fakeBBDevice struct {
	states  []game.GameState // consumed one per State() call; last one repeats
	i       int
	taps    []string
	clickOK bool
	panelOK bool
	empty   bool
	onBB    bool
	endErr  error
}

func (f *fakeBBDevice) State() (game.GameState, int) {
	if f.i >= len(f.states) {
		return f.states[len(f.states)-1], 0
	}
	s := f.states[f.i]
	f.i++
	return s, 0
}

func (f *fakeBBDevice) TapRef(x, y int) error {
	f.taps = append(f.taps, "tap")
	return nil
}
func (f *fakeBBDevice) Click(string, string) bool { f.taps = append(f.taps, "click"); return f.clickOK }
func (f *fakeBBDevice) PanelAction(string, [4]float64, time.Duration) bool {
	f.taps = append(f.taps, "panel")
	return f.panelOK
}
func (f *fakeBBDevice) EndBattle() error {
	f.taps = append(f.taps, "end")
	return f.endErr
}
func (f *fakeBBDevice) ReturnHome() error { f.taps = append(f.taps, "home"); return nil }
func (f *fakeBBDevice) EmptyArmy() bool   { return f.empty }

// OnBuilderBase becomes true once the boat tap has happened, mirroring the game
// switching villages after the boat.
func (f *fakeBBDevice) OnBuilderBase() bool { return f.onBB && len(f.taps) > 0 }
func (f *fakeBBDevice) Sleep(time.Duration) {}

// A complete happy path: boat, Attack, versus, battle, card, drop, end, home, in
// that order, and the anchors scaled by the device adapter rather than here.
func TestRunBuilderBaseBattleHappyPath(t *testing.T) {
	d := &fakeBBDevice{
		states:  []game.GameState{game.StateMainVillage, game.StateBattle, game.StateBattleEnd},
		clickOK: true,
		panelOK: true,
		onBB:    true,
	}
	if err := runBuilderBaseBattle(d, bbConfig()); err != nil {
		t.Fatalf("sequence failed: %v", err)
	}
	want := []string{"tap", "click", "panel", "tap", "tap", "end", "home"}
	if strings.Join(d.taps, ",") != strings.Join(want, ",") {
		t.Fatalf("taps = %v, want %v", d.taps, want)
	}
}

// Nothing happens from a screen the sequence cannot identify as the home village:
// the boat anchor means "top-right of the home village", and on a battle frame it
// is a tap on the enemy base.
func TestRunBuilderBaseBattleRefusesOffVillage(t *testing.T) {
	d := &fakeBBDevice{states: []game.GameState{game.StateBattle}, clickOK: true, panelOK: true}
	err := runBuilderBaseBattle(d, bbConfig())
	if err == nil || !strings.Contains(err.Error(), "not in the home village") {
		t.Fatalf("err = %v, want the off-village refusal", err)
	}
	if len(d.taps) != 0 {
		t.Fatalf("taps = %v, want none", d.taps)
	}
}

// A Boat screen that never arrives aborts before any other control is touched,
// and the error names the step, which is what makes a live failure diagnosable.
func TestRunBuilderBaseBattleStopsWhenBuilderBaseNeverOpens(t *testing.T) {
	was := bbOpenTimeout
	bbOpenTimeout = 50 * time.Millisecond
	t.Cleanup(func() { bbOpenTimeout = was })

	d := &fakeBBDevice{states: []game.GameState{game.StateMainVillage}, clickOK: true, panelOK: true}
	err := runBuilderBaseBattle(d, bbConfig())
	if err == nil || !strings.Contains(err.Error(), "Builder Base screen never appeared") {
		t.Fatalf("err = %v, want the boat failure", err)
	}
	if len(d.taps) != 1 {
		t.Fatalf("taps = %v, want only the boat tap", d.taps)
	}
}

// An empty Builder Base army is refused at the panel instead of starting a
// battle with nothing to drop: the raid would pay nothing and still burn the
// versus cooldown.
func TestRunBuilderBaseBattleRefusesEmptyArmy(t *testing.T) {
	d := &fakeBBDevice{
		states:  []game.GameState{game.StateMainVillage},
		clickOK: true,
		panelOK: true,
		empty:   true,
		onBB:    true,
	}
	err := runBuilderBaseBattle(d, bbConfig())
	if err == nil || !strings.Contains(err.Error(), "army is empty") {
		t.Fatalf("err = %v, want the empty-army refusal", err)
	}
	for _, tap := range d.taps {
		if tap == "panel" || tap == "end" {
			t.Fatalf("taps = %v, want no Find Now and no battle with an empty army", d.taps)
		}
	}
}

// No Attack button found means no further tap, and the error says which control
// was missing rather than leaving the run to a generic failure.
func TestRunBuilderBaseBattleStopsWithoutAttackButton(t *testing.T) {
	d := &fakeBBDevice{
		states:  []game.GameState{game.StateMainVillage},
		clickOK: false,
		panelOK: true,
		onBB:    true,
	}
	err := runBuilderBaseBattle(d, bbConfig())
	if err == nil || !strings.Contains(err.Error(), "Battle button was not located") {
		t.Fatalf("err = %v, want the Battle-button failure", err)
	}
	for _, tap := range d.taps {
		if tap == "panel" || tap == "end" {
			t.Fatalf("taps = %v, want nothing after the missing Attack button", d.taps)
		}
	}
}
