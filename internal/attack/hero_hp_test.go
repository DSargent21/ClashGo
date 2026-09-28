package attack

import (
	"testing"
	"time"

	"gocv.io/x/gocv"
)

// paintStrip fills rows [top,bot) x [x0,x1) with BGR and returns the mat.
func paintStrip(w, h, top, bot, x0, x1 int, b, g, r uint8) gocv.Mat {
	m := gocv.NewMatWithSize(h, w, gocv.MatTypeCV8UC3)
	for y := top; y < bot; y++ {
		for x := x0; x < x1; x++ {
			m.SetUCharAt(y, x*3, b)
			m.SetUCharAt(y, x*3+1, g)
			m.SetUCharAt(y, x*3+2, r)
		}
	}
	return m
}

func TestHeroHPFraction_FullAndEmpty(t *testing.T) {
	const w, h, cx, slotY = 200, 200, 100, 150
	// nil cal → k=1: strip rows slotY-8..slotY-3, half-width 17.
	full := paintStrip(w, h, slotY-8, slotY-3, cx-17, cx+17, 40, 200, 60)
	defer full.Close()
	if got := HeroHPFraction(full, cx, slotY, nil); got < 0.99 {
		t.Fatalf("full HP bar reads %.2f, want ~1", got)
	}
	half := paintStrip(w, h, slotY-8, slotY-3, cx-17, cx, 40, 200, 60)
	defer half.Close()
	if got := HeroHPFraction(half, cx, slotY, nil); got < 0.45 || got > 0.55 {
		t.Fatalf("half HP bar reads %.2f, want ~0.5", got)
	}
	blank := gocv.NewMatWithSize(h, w, gocv.MatTypeCV8UC3)
	defer blank.Close()
	if got := HeroHPFraction(blank, cx, slotY, nil); got != 0 {
		t.Fatalf("blank frame reads %.2f, want 0", got)
	}
	if got := HeroHPFraction(gocv.NewMat(), cx, slotY, nil); got != 0 {
		t.Fatalf("empty mat reads %.2f, want 0", got)
	}
}

func TestHeroHPMonitor_FiresOnceOnLowHP(t *testing.T) {
	const w, h, cx, slotY = 200, 200, 100, 150
	low := paintStrip(w, h, slotY-8, slotY-3, cx-17, cx-12, 40, 200, 60) // ~15% fill
	defer low.Close()
	m := NewHeroHPMonitor([]*WatchedHero{{X: cx, SlotY: slotY, DeployedAt: time.Now().Add(-time.Minute)}})
	taps := 0
	m.Poll(low, nil, time.Now(), func(h *WatchedHero) { taps++ })
	m.Poll(low, nil, time.Now(), func(h *WatchedHero) { taps++ })
	if taps != 1 {
		t.Fatalf("low HP fired %d taps, want exactly 1", taps)
	}
	if !m.AllDone() {
		t.Fatal("monitor not done after firing")
	}
}

func TestHeroHPMonitor_DeadAfterTwoZeroReads(t *testing.T) {
	const w, h, cx, slotY = 200, 200, 100, 150
	blank := gocv.NewMatWithSize(h, w, gocv.MatTypeCV8UC3)
	defer blank.Close()
	m := NewHeroHPMonitor([]*WatchedHero{{X: cx, SlotY: slotY, DeployedAt: time.Now().Add(-time.Minute)}})
	taps := 0
	now := time.Now()
	m.Poll(blank, nil, now, func(h *WatchedHero) { taps++ })
	if m.AllDone() {
		t.Fatal("single zero read confirmed death; want 2 strikes")
	}
	m.Poll(blank, nil, now, func(h *WatchedHero) { taps++ })
	if !m.AllDone() || taps != 0 {
		t.Fatalf("dead hero: done=%v taps=%d, want done=true taps=0", m.AllDone(), taps)
	}
}

func TestHeroHPMonitor_WardenProactive(t *testing.T) {
	const w, h, cx, slotY = 200, 200, 100, 150
	full := paintStrip(w, h, slotY-8, slotY-3, cx-17, cx+17, 40, 200, 60)
	defer full.Close()
	m := NewHeroHPMonitor([]*WatchedHero{{X: cx, SlotY: slotY, Warden: true, DeployedAt: time.Now().Add(-time.Minute)}})
	taps := 0
	m.Poll(full, nil, time.Now(), func(h *WatchedHero) { taps++ })
	if taps != 1 {
		t.Fatalf("warden with full bar fired %d taps, want 1 proactive", taps)
	}
}
