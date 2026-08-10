package game

import (
	"testing"
	"time"
)

// TestNavigator_IdlePanMirrors: an idle pan is exactly two bezier
// swipes (out + return) with mirrored endpoints, no taps, and no
// captures. The return leg MUST mirror the out leg so the camera ends
// where it started — otherwise the wander would permanently shift the
// map and break subsequent template matches.
func TestNavigator_IdlePanMirrors(t *testing.T) {
	dev := newFakeDevice(0)
	defer dev.Close()
	nav := makeTestNavigator(dev, &scriptedClassifier{})

	nav.IdlePan()

	if got := len(dev.swipes); got != 2 {
		t.Fatalf("expected exactly 2 bezier swipes (out + back), got %d", got)
	}
	out, back := dev.swipes[0], dev.swipes[1]
	if out.x1 != back.x2 || out.y1 != back.y2 || out.x2 != back.x1 || out.y2 != back.y1 {
		t.Errorf("return leg must mirror the out leg: out=%+v back=%+v", out, back)
	}
	if out.ms <= 0 || back.ms <= 0 {
		t.Errorf("swipe durations must be positive: out=%+v back=%+v", out, back)
	}
	if len(dev.recorded) != 0 {
		t.Errorf("idle pan must not tap; got %d taps", len(dev.recorded))
	}
}

// TestNavigator_IdlePanStaysOnScreen: every swipe endpoint must stay
// within the physical screen, and the pan distance must be sane
// (nonzero, less than half the screen width). Guards against a future
// edit sending the camera off into negative / overshoot coordinates.
func TestNavigator_IdlePanStaysOnScreen(t *testing.T) {
	for i := 0; i < 25; i++ {
		dev := newFakeDevice(0)
		nav := makeTestNavigator(dev, &scriptedClassifier{})
		nav.IdlePan()
		for _, s := range dev.swipes {
			for _, c := range []struct{ x, y int }{{s.x1, s.y1}, {s.x2, s.y2}} {
				if c.x < 0 || c.x > RefWidth || c.y < 0 || c.y > RefHeight {
					t.Fatalf("iteration %d: endpoint (%d,%d) off screen", i, c.x, c.y)
				}
			}
			dx := absDiff(s.x2, s.x1)
			if dx == 0 && absDiff(s.y2, s.y1) == 0 {
				t.Fatalf("iteration %d: zero-length pan %+v", i, s)
			}
			if dx > RefWidth/2 {
				t.Fatalf("iteration %d: pan too wide: %+v", i, s)
			}
		}
		dev.Close()
	}
}

// TestNavigator_IdlePanBoundedTime: the whole gesture (two legs + the
// far-end micro-pause) must complete within ~3s so a throttled call
// from the bot's capture loop never stalls frame processing. The real
// worst case is 400ms*2 + 300ms = 1.1s; 3s leaves headroom for slow
// CI machines without ever flaking.
func TestNavigator_IdlePanBoundedTime(t *testing.T) {
	dev := newFakeDevice(0)
	defer dev.Close()
	nav := makeTestNavigator(dev, &scriptedClassifier{})

	start := time.Now()
	nav.IdlePan()
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("IdlePan took %s, expected < 3s", elapsed)
	}
}
