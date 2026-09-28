package adb

import (
	"strings"
	"testing"
)

// TestGuestAnimationCommands pins the exact commands applied to the guest.
//
// This is the only part of the feature a test can hold without a device, and it
// is the part that fails silently if it is wrong: `settings put` writes any key
// in any namespace without complaint, so a typo'd key zeroes nothing, saves
// nothing, and reports success. The three keys and the `global` namespace are
// what Android's own Developer options screen writes.
func TestGuestAnimationCommands(t *testing.T) {
	want := []string{
		"settings put global window_animation_scale 0.0",
		"settings put global transition_animation_scale 0.0",
		"settings put global animator_duration_scale 0.0",
	}
	got := GuestAnimationCommands()
	if len(got) != len(want) {
		t.Fatalf("got %d commands, want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("command %d = %q, want %q", i, got[i], want[i])
		}
	}

	// Every command must (a) write the global namespace, (b) write a zero, and
	// (c) not be a read. A read here would look plausible and change nothing.
	for _, cmd := range got {
		if !strings.HasPrefix(cmd, "settings put global ") {
			t.Errorf("%q must write the global namespace with `settings put`", cmd)
		}
		if !strings.HasSuffix(cmd, " 0.0") {
			t.Errorf("%q must set the scale to 0.0", cmd)
		}
	}
}

func TestIsZeroScale(t *testing.T) {
	zeroed := []string{"0", "0.0", "0.00", " 0.0\n", "0.0\r\n"}
	for _, v := range zeroed {
		if !isZeroScale(v) {
			t.Errorf("isZeroScale(%q) = false, want true", v)
		}
	}
	// "null" is what `settings get` prints for an unset key, and a non-zero
	// scale is exactly what this feature exists to change: both must be treated
	// as "not zeroed" so the writes happen.
	notZeroed := []string{"", "null", "1.0", "0.5", "error: closed", "2"}
	for _, v := range notZeroed {
		if isZeroScale(v) {
			t.Errorf("isZeroScale(%q) = true, want false", v)
		}
	}
}
