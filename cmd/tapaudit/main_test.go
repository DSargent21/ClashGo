package main

import "testing"

// The audit contract: a stray double-fire and a hammered tile must be
// flagged, a clean jittered deploy must not.
func TestAudit_FlagsDoubleFireAndHammering(t *testing.T) {
	t0 := int64(1_000)
	mk := func(i int, x, y int) auditTap {
		return auditTap{TapEvent: TapEvent{
			Type: "tap_fast", X: x, Y: y,
			ActualX: x + i%3, ActualY: y + i%2,
			Ts: t0 + int64(i)*400,
		}}
	}

	clean := []auditTap{
		mk(0, 200, 400), mk(1, 320, 412), mk(2, 440, 424),
		mk(3, 560, 436), mk(4, 680, 448),
	}
	if got := audit(clean, 1280, 720); got != 0 {
		t.Fatalf("clean deploy flagged %d times, want 0", got)
	}

	// Same spot twice within 300ms -> double-fire.
	fast := []auditTap{
		{TapEvent: TapEvent{Type: "tap_fast", X: 500, Y: 300, ActualX: 500, ActualY: 300, Ts: t0}},
		{TapEvent: TapEvent{Type: "tap_fast", X: 502, Y: 301, ActualX: 502, ActualY: 301, Ts: t0 + 120}},
	}
	if got := audit(fast, 1280, 720); got < 1 {
		t.Fatalf("double-fire not flagged")
	}

	// One tile hammered 3x within 2s -> hammering.
	ham := []auditTap{
		{TapEvent: TapEvent{Type: "tap_fast", X: 500, Y: 300, ActualX: 500, ActualY: 300, Ts: t0}},
		{TapEvent: TapEvent{Type: "tap_fast", X: 501, Y: 302, ActualX: 501, ActualY: 302, Ts: t0 + 500}},
		{TapEvent: TapEvent{Type: "tap_fast", X: 502, Y: 301, ActualX: 502, ActualY: 301, Ts: t0 + 1000}},
	}
	if got := audit(ham, 1280, 720); got < 1 {
		t.Fatalf("hammering not flagged")
	}
}

// A tap under the top resource HUD is chrome, not battlefield.
func TestAudit_FlagsTopHUD(t *testing.T) {
	taps := []auditTap{
		{TapEvent: TapEvent{Type: "tap_fast", X: 600, Y: 40, ActualX: 600, ActualY: 40, Ts: 1}},
	}
	if got := audit(taps, 1280, 720); got != 1 {
		t.Fatalf("top-hud tap flagged %d times, want 1", got)
	}
	if taps[0].class != "top-hud" {
		t.Fatalf("class = %q, want top-hud", taps[0].class)
	}
}
