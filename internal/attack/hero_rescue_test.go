package attack

import (
	"image"
	"testing"
)

// TestHeroRescueWalksOutwardFromTheRefusedPoint is the 2026-11-15 rule: a
// refused hero drop moves further OUT on the next tap, not somewhere else in
// the candidate list. The pocket that refuses a tap hugs the base, so a tile at
// the same radius is just as likely to be refused again.
func TestHeroRescueWalksOutwardFromTheRefusedPoint(t *testing.T) {
	line := []image.Point{{X: 766, Y: 130}, {X: 850, Y: 160}, {X: 950, Y: 210}, {X: 1088, Y: 270}}
	hm := &HeroManager{
		deployLine: DeployLine{Points: line, Side: "TopRight"},
		w:          1280,
		h:          720,
	}
	fired := image.Point{X: 950, Y: 210}
	cands := []image.Point{fired, {X: 1000, Y: 120}, {X: 640, Y: 400}}

	out := hm.heroRescueGround(fired, cands)
	if len(out) < 2 {
		t.Fatalf("got %d rescue points, want outward steps after the refused one", len(out))
	}
	if out[0] != fired {
		t.Fatalf("rescue list starts at %v, want the refused point %v at index 0", out[0], fired)
	}
	mid := image.Pt(hm.w/2, hm.h/2)
	for i, p := range out[1:] {
		if p.Y < BandTop() || p.Y > UICutoff(hm.h) {
			t.Fatalf("rescue point %d = %v sits outside the deploy band", i+1, p)
		}
	}
	if dist2(out[1], mid) <= dist2(fired, mid) {
		t.Fatalf("first rescue step %v is not further out than the refused point %v", out[1], fired)
	}
	if dupes := len(out) - len(dedupePoints(out)); dupes != 0 {
		t.Fatalf("rescue ground carries %d duplicate taps", dupes)
	}
}

// TestHeroRetryAcceptsCardEmptyAsConfirmation: the card's own badge is the
// game's answer. A drop that leaves the card can hold the slot ratio almost
// flat mid-animation, and a ratio-only rule then fires another field tap at a
// hero that had already landed.
func TestHeroRetryAcceptsCardEmptyAsConfirmation(t *testing.T) {
	cands := []image.Point{{X: 10, Y: 10}, {X: 20, Y: 10}, {X: 30, Y: 10}, {X: 40, Y: 10}}
	fired := 0
	won := runHeroFieldRetries(
		cands, 0.7, true,
		func() bool { return true },
		func(image.Point) { fired++ },
		func() (float64, bool) { return 0.7, true }, // ratio never moves
		func() bool { return true },                 // but the card is empty
		func() bool { return false },
		func(int, image.Point, float64, float64, float64, bool) {},
	)
	if !won {
		t.Fatal("an empty hero card must confirm the drop")
	}
	if fired != 1 {
		t.Fatalf("fired %d field taps, want 1 (card state confirmed the first one)", fired)
	}
}

// TestHeroRetryStillRefusesOnFullCard keeps the other half honest: with a card
// that still holds the hero and a flat ratio, the drop stays unconfirmed and the
// walk outward continues.
func TestHeroRetryStillRefusesOnFullCard(t *testing.T) {
	cands := []image.Point{{X: 10, Y: 10}, {X: 20, Y: 10}, {X: 30, Y: 10}, {X: 40, Y: 10}}
	fired := 0
	won := runHeroFieldRetries(
		cands, 0.7, true,
		func() bool { return true },
		func(image.Point) { fired++ },
		func() (float64, bool) { return 0.7, true },
		func() bool { return false },
		func() bool { return false },
		func(int, image.Point, float64, float64, float64, bool) {},
	)
	if won {
		t.Fatal("a card that still holds the hero must not confirm the drop")
	}
	if fired != heroFieldRetries {
		t.Fatalf("fired %d field taps, want the full outward walk (%d)", fired, heroFieldRetries)
	}
}
