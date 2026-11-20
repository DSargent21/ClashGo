package bot

import (
	"testing"

	"github.com/Ducky705/ClashGO/internal/game"
)

// TestReconnectRecovered covers the watchdog's post-reconnect decision. A
// classified village with the Attack button hidden is still blocked (live
// 2026-11-19: clan chat + Clan War modal), so the reconnect alone must not
// cancel the restart ladder.
func TestReconnectRecovered(t *testing.T) {
	cases := []struct {
		name          string
		state         game.GameState
		attackVisible bool
		want          bool
	}{
		{"unknown", game.StateUnknown, false, false},
		{"village with attack button", game.StateMainVillage, true, true},
		{"village blocked by panel", game.StateMainVillage, false, false},
		{"army camp with attack button", game.StateArmyCamp, true, true},
		{"army camp has its own navigation", game.StateArmyCamp, false, true},
		{"battle is actionable", game.StateBattle, false, true},
		{"connection lost is actionable", game.StateConnectionLost, false, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := reconnectRecovered(c.state, c.attackVisible); got != c.want {
				t.Fatalf("reconnectRecovered(%v, %v) = %v, want %v", c.state, c.attackVisible, got, c.want)
			}
		})
	}
}
