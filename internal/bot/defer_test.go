package bot

import "testing"

// An empty army must keep the attack path off the Attack button until the
// training retry comes round; otherwise every capture cycle taps Attack, opens
// the panel and closes it, which reads as the bot touching the village at random.
func TestAttacksDeferred(t *testing.T) {
	now := int64(1_800_000_000)
	tests := []struct {
		name      string
		empty     bool
		skipUntil int64
		want      bool
	}{
		{"empty army inside the skip window", true, now + 60, true},
		{"empty army past the skip window", true, now - 1, false},
		{"army present ignores the window", false, now + 60, false},
		{"no window set", true, 0, false},
	}
	for _, tt := range tests {
		if got := attacksDeferred(tt.empty, tt.skipUntil, now); got != tt.want {
			t.Errorf("%s: attacksDeferred = %v, want %v", tt.name, got, tt.want)
		}
	}
}
