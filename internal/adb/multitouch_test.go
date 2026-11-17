package adb

import (
	"image"
	"math"
	"testing"
	"time"
)

const liveGetevent = `add device 1: /dev/input/event2
  name:     "BlueStacks Virtual Touch"
  events:
    KEY (0001): BTN_MOUSE             BTN_RIGHT             BTN_MIDDLE            BTN_TOUCH
    ABS (0003): ABS_MT_SLOT           : value 15, min 0, max 15, fuzz 0, flat 0, resolution 0
                ABS_MT_POSITION_X     : value 0, min 0, max 32767, fuzz 0, flat 0, resolution 0
                ABS_MT_POSITION_Y     : value 0, min 0, max 32767, fuzz 0, flat 0, resolution 0
                ABS_MT_TRACKING_ID    : value 0, min 0, max 65535, fuzz 0, flat 0, resolution 0
  input props:
    <none>
add device 2: /dev/input/event0
  name:     "gpio-keys"
  events:
    KEY (0001): KEY_POWER
`

func TestParseTouchPanelsFindsTheVirtualTouchNode(t *testing.T) {
	panels := parseTouchPanels(liveGetevent)
	if len(panels) != 1 {
		t.Fatalf("panels = %d, want 1 (gpio-keys has no ABS_MT)", len(panels))
	}
	p := panels[0]
	if p.Path != "/dev/input/event2" || p.MaxX != 32767 || p.MaxY != 32767 {
		t.Fatalf("panel = %+v, want event2 0..32767 on both axes", p)
	}
}

func TestBuildDragStrokeStaysOnScreenAndInOrder(t *testing.T) {
	const w, h = 1024, 768
	from, to := image.Pt(67, 705), image.Pt(922, 232)
	dur := 420 * time.Millisecond
	samples := BuildDragStroke(from, to, w, h, dur, 24, 10, 4)
	if len(samples) != 24 {
		t.Fatalf("samples = %d, want 24", len(samples))
	}
	var prev time.Duration
	for i, s := range samples {
		if s.Pos.X < 0 || s.Pos.X >= w || s.Pos.Y < 0 || s.Pos.Y >= h {
			t.Fatalf("sample %d off screen: %v", i, s.Pos)
		}
		if s.At <= prev && i > 0 {
			t.Fatalf("sample %d time %v not after %v", i, s.At, prev)
		}
		prev = s.At
	}
	if samples[0].Pos != from {
		t.Fatalf("first sample %v, want the press point %v", samples[0].Pos, from)
	}
	if samples[len(samples)-1].Pos != to {
		t.Fatalf("last sample %v, want the release point %v", samples[len(samples)-1].Pos, to)
	}
}

// The stroke must move sub-linearly: a human commits, then settles. Ease-out
// means the first third of the time covers more than a third of the distance.
func TestBuildDragStrokeIsEaseOutAndBowed(t *testing.T) {
	const w, h = 1024, 768
	from, to := image.Pt(200, 600), image.Pt(800, 200)
	samples := BuildDragStroke(from, to, w, h, 500*time.Millisecond, 21, 18, 0)
	third := samples[7].Pos // t = 7/20 = 0.35
	// Straight-line fraction covered by 35% of the time under ease-out cubic.
	want := 1 - math.Pow(1-0.35, 3)
	got := math.Hypot(float64(third.X-from.X), float64(third.Y-from.Y)) /
		math.Hypot(float64(to.X-from.X), float64(to.Y-from.Y))
	if math.Abs(got-want) > 0.12 {
		t.Fatalf("progress at 35%% of the stroke = %.3f, want ~%.3f (ease-out)", got, want)
	}
	// A bowed path leaves the straight chord by roughly the bow amplitude.
	mid := samples[10].Pos
	straight := image.Pt((from.X+to.X)/2, (from.Y+to.Y)/2)
	dev := math.Hypot(float64(mid.X-straight.X), float64(mid.Y-straight.Y))
	if dev < 5 {
		t.Fatalf("midpoint deviates %.1f px from the chord, want a bowed path", dev)
	}
}

func TestBuildHoldThenDragHoldsStillThenSweeps(t *testing.T) {
	from, to := image.Pt(400, 600), image.Pt(700, 300)
	hold := 400 * time.Millisecond
	samples := BuildHoldThenDrag(from, to, 1024, 768, hold, 400*time.Millisecond, 16, 10, 4)
	if len(samples) < 10 {
		t.Fatalf("only %d samples", len(samples))
	}
	var movedAt time.Duration
	for i, s := range samples {
		if i > 0 && s.At <= samples[i-1].At {
			t.Fatalf("sample %d at %v is not after %v", i, s.At, samples[i-1].At)
		}
		if s.At <= hold && s.Pos != from {
			t.Fatalf("sample %d moved to %v during the hold", i, s.Pos)
		}
		if s.At > hold && movedAt == 0 {
			movedAt = s.At
		}
	}
	if movedAt == 0 {
		t.Fatal("no sweep samples after the hold")
	}
	if last := samples[len(samples)-1].Pos; last != to {
		t.Fatalf("release at %v, want %v", last, to)
	}
}

func TestBuildWaypointDragStartsOnCardHoldsThenSweeps(t *testing.T) {
	card, lineStart, lineEnd := image.Pt(153, 705), image.Pt(613, 130), image.Pt(870, 288)
	hold := 320 * time.Millisecond
	samples := BuildWaypointDrag([]image.Point{card, lineStart, lineEnd}, 1024, 768, hold, 500*time.Millisecond, 24, 4)
	if len(samples) < 20 {
		t.Fatalf("only %d samples", len(samples))
	}
	for i, s := range samples {
		if i > 0 && s.At <= samples[i-1].At {
			t.Fatalf("sample %d not strictly increasing", i)
		}
		if s.At <= hold && s.Pos != card {
			t.Fatalf("sample %d moved off the card during the hold: %v", i, s.Pos)
		}
	}
	if last := samples[len(samples)-1].Pos; last != lineEnd {
		t.Fatalf("release at %v, want line end %v", last, lineEnd)
	}
}
