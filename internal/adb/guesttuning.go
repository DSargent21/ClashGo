package adb

import (
	"fmt"
	"strings"
)

// guestAnimationScaleKeys are Android's framework animation scales. They are the
// same three the Developer options screen writes: window (the animation a window
// plays when it opens or closes), transition (activity and task switches), and
// animator (the scale factor apps multiply their own durations by).
//
// They live in the `global` settings namespace on every Android the bot supports
// (API 17+; this project runs Android 13 on BlueStacks Air). Zeroing them is the
// one lever in docs/PERFORMANCE.md's BlueStacks table that the bot can apply
// through its own transport rather than asking the user to open a settings UI:
// animated transitions are real per-frame guest GPU and CPU work, and the bot
// reads the screen at 4 Hz and taps at human pace, so it cannot use a single one
// of those frames. Zeroing them also shortens the window during which a screen
// is half-drawn, which is the state that makes a classifier guess.
var guestAnimationScaleKeys = []string{
	"window_animation_scale",
	"transition_animation_scale",
	"animator_duration_scale",
}

// GuestAnimationCommands returns the `settings put` commands that zero Android's
// animation scales, in the order they should be applied.
//
// Exported (rather than inlined into TuneGuestAnimations) so the exact commands
// are pinned by a test without a device: a typo in a settings key or namespace
// fails silently on the device — `settings put` happily writes a key nobody
// reads — so this contract is worth an assertion.
func GuestAnimationCommands() []string {
	cmds := make([]string, 0, len(guestAnimationScaleKeys))
	for _, k := range guestAnimationScaleKeys {
		cmds = append(cmds, fmt.Sprintf("settings put global %s 0.0", k))
	}
	return cmds
}

// TuneGuestAnimations zeroes the guest's animation scales, best effort.
//
// Deliberately tolerant and non-fatal: this is an optimisation, not a
// precondition. A guest that refuses the writes (a wedged shell — this
// BlueStacks returns "error: closed" for exactly these commands while
// SurfaceFlinger still serves frames) boots and runs exactly as it would have,
// just without the saving, so the caller logs it and carries on.
//
// It returns how many of the commands succeeded plus the first error, and it is
// idempotent: re-applying the same values is what the boot path does.
func (c *Client) TuneGuestAnimations() (applied int, err error) {
	var firstErr error
	for _, cmd := range GuestAnimationCommands() {
		if _, serr := c.Shell(cmd); serr != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("%s: %w", cmd, serr)
			}
			continue
		}
		applied++
	}
	return applied, firstErr
}

// AnimationsAlreadyZeroed reports whether every animation scale already reads as
// zero, so a caller can skip the writes (and the three shell round trips) when a
// previous session already applied them. An unreadable value is reported as
// false: not knowing means doing the write again, which is harmless.
func (c *Client) AnimationsAlreadyZeroed() bool {
	for _, k := range guestAnimationScaleKeys {
		out, err := c.Shell("settings get global " + k)
		if err != nil {
			return false
		}
		if !isZeroScale(out) {
			return false
		}
	}
	return true
}

// isZeroScale parses a `settings get` reply for an animation scale. Android
// writes these as floats ("0.0"), but a device that was set through some other
// path can report "0", and a device where the setting is unset or null must not
// count as zeroed.
func isZeroScale(out string) bool {
	v := strings.TrimSpace(out)
	switch v {
	case "0", "0.0", "0.00":
		return true
	}
	return false
}
