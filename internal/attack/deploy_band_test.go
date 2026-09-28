package attack

import (
	"image"
	"testing"
)

// The clamp exists because a pinned point that lands under the game's chrome
// deploys nothing: the tap is eaten by the HUD or the troop bar, and the battle
// silently loses those units. These tests pin the two directions of that rule
// and, just as importantly, the fact that a legal point is left exactly alone.
func TestClampToDeployBand(t *testing.T) {
	const uiCutoff = 612 // 0.85 * 720, what DeployDynamicV2 uses

	cases := []struct {
		name   string
		in     image.Point
		cutoff int
		want   image.Point
	}{
		{
			name:   "point in the top HUD is pushed down onto the field",
			in:     image.Pt(444, 61),
			cutoff: uiCutoff,
			want:   image.Pt(444, BandTop()),
		},
		{
			name:   "point under the troop bar is pulled up",
			in:     image.Pt(700, 700),
			cutoff: uiCutoff,
			want:   image.Pt(700, uiCutoff),
		},
		{
			name:   "last row of the HUD is still not the field",
			in:     image.Pt(100, yTopMin),
			cutoff: uiCutoff,
			want:   image.Pt(100, BandTop()),
		},
		{
			name:   "a legal point is untouched, so pins stay exactly where they were drawn",
			in:     image.Pt(149, 283),
			cutoff: uiCutoff,
			want:   image.Pt(149, 283),
		},
		{
			name:   "being close to the edge is not a vertical problem",
			in:     image.Pt(6, 400),
			cutoff: uiCutoff,
			want:   image.Pt(6, 400),
		},
		{
			name:   "an unknown height leaves the bottom alone",
			in:     image.Pt(640, 719),
			cutoff: 0,
			want:   image.Pt(640, 719),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClampToDeployBand(tc.in, tc.cutoff); got != tc.want {
				t.Fatalf("ClampToDeployBand(%v, %d) = %v, want %v", tc.in, tc.cutoff, got, tc.want)
			}
		})
	}
}

func TestClampEdgeToDeployBand(t *testing.T) {
	const uiCutoff = 612

	// The user's real TopLeft pin: p2 sits in the HUD, p1 is fine. Clamping must
	// fix the bad end without disturbing the good one or the line's direction.
	in := ManualEdge{P1: image.Pt(149, 283), P2: image.Pt(444, 61)}
	got := ClampEdgeToDeployBand(in, uiCutoff)

	if got.P1 != in.P1 {
		t.Fatalf("p1 moved: got %v, want %v", got.P1, in.P1)
	}
	if got.P2 != image.Pt(444, BandTop()) {
		t.Fatalf("p2 = %v, want %v", got.P2, image.Pt(444, BandTop()))
	}
	if got.P2.Y <= in.P2.Y {
		t.Fatalf("clamping must move the point down the screen, got y=%d from y=%d", got.P2.Y, in.P2.Y)
	}
}

// BandTop must stay inside the region the detector will call field, otherwise a
// clamped point would be one the boundary gate rejects.
func TestBandTopIsBelowHudCutoff(t *testing.T) {
	if BandTop() <= yTopMin {
		t.Fatalf("BandTop() = %d must be below yTopMin = %d", BandTop(), yTopMin)
	}
	// 720p is the smallest surface the bot runs on at this project's scale; the
	// band has to leave real field between the HUD and the bottom pad there.
	if BandTop() >= int(float64(720)*0.85)-yBotPad {
		t.Fatalf("BandTop() = %d leaves no field at 720p (bottom of band = %d)", BandTop(), int(float64(720)*0.85)-yBotPad)
	}
}
