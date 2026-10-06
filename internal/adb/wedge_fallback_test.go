package adb

import "testing"

// The BlueStacks wedge makes adbd answer every bare shell command with FAIL
// "closed", and Transport.Exec recovers by re-running it behind
// "getprop ro.build.type; ". On this emulator the framework pinch ONLY works
// that way — verified live, the unprefixed form answers "error: closed" — so
// that recovery has to be observable, or a pinch that landed purely via the
// fallback looks identical to a native one and a break in the fallback leaves no
// trace.
func TestWedgeFallbacksStartsAtZeroAndIsNilSafe(t *testing.T) {
	var nilTransport *Transport
	if got := nilTransport.WedgeFallbacks(); got != 0 {
		t.Errorf("nil transport WedgeFallbacks() = %d, want 0", got)
	}
	tr := &Transport{}
	if got := tr.WedgeFallbacks(); got != 0 {
		t.Errorf("fresh transport WedgeFallbacks() = %d, want 0", got)
	}
}

// The counter is what Client.Shell diffs across one Exec to decide whether the
// wedge workaround carried the command, so it must only move when the fallback
// actually ran.
func TestWedgeFallbacksCountsOnlyFallbackDeliveries(t *testing.T) {
	tr := &Transport{}
	tr.wedgeFallbacks.Add(1)
	tr.wedgeFallbacks.Add(1)
	if got := tr.WedgeFallbacks(); got != 2 {
		t.Errorf("WedgeFallbacks() = %d, want 2", got)
	}
	// The property NAME is what identifies the workaround, and it must stay
	// spelled the same way the prefix actually reads it: the prefix emits the
	// property VALUE ("user" / "userdebug"), which is why detection counts
	// fallbacks instead of matching output text.
	if wedgePrefixProperty != "ro.build.type" {
		t.Errorf("wedgePrefixProperty = %q, want %q: a rename here would silently un-wedge every command",
			wedgePrefixProperty, "ro.build.type")
	}
	if bluestacksWedgePrefix != "getprop ro.build.type; " {
		t.Errorf("bluestacksWedgePrefix = %q, want %q", bluestacksWedgePrefix, "getprop ro.build.type; ")
	}
}

func TestFirstLineSplitsOnNewline(t *testing.T) {
	if got := firstLine("a\nb\nc"); got != "a" {
		t.Errorf("firstLine = %q, want %q", got, "a")
	}
	if got := firstLine("only"); got != "only" {
		t.Errorf("firstLine = %q, want %q", got, "only")
	}
	if got := firstLine(""); got != "" {
		t.Errorf("firstLine = %q, want empty", got)
	}
}
