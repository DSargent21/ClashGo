package attack

import (
	"image"
	"testing"
)

// TestClassifySide locks the orientation/centroid rules used by both
// pick_coords's auto-label and any future runtime caller. These cases
// caught several off-by-midX bugs during initial authoring.
func TestClassifySide(t *testing.T) {
	const refW, refH = 860, 732
	cases := []struct {
		name   string
		p1, p2 image.Point
		want   string
	}{
		{"horizontal at top", image.Pt(100, 110), image.Pt(760, 110), SideTop},
		{"horizontal at bottom", image.Pt(100, 590), image.Pt(760, 590), SideBottom},
		{"vertical at left", image.Pt(80, 110), image.Pt(80, 590), SideLeft},
		{"vertical at right", image.Pt(780, 110), image.Pt(780, 590), SideRight},
		{"horizontal at exact midline goes bottom (avgY > midY)",
			image.Pt(100, 367), image.Pt(760, 367), SideBottom},
		{"degenerate same-point", image.Pt(100, 100), image.Pt(100, 100), SideTop},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ClassifySide(c.p1, c.p2, refW, refH)
			if got != c.want {
				t.Errorf("got %q, want %q (p1=%v p2=%v)", got, c.want, c.p1, c.p2)
			}
		})
	}
}

// TestSpotsForSideEndpoints forces the linspace to end exactly on p1
// (defaultSpotsCount[0]) and p2 (defaultSpotsCount[last]) so any
// future refactor that drops an inclusive bound is caught.
func TestSpotsForSideEndpoints(t *testing.T) {
	pCfg := PrecisionConfig{
		Width: 860, Height: 732,
		Sides: map[string]ManualEdge{
			"top": {P1: image.Pt(100, 110), P2: image.Pt(760, 110)},
		},
	}
	pts := SpotsForSide(pCfg, "top", 5, 860, 732)
	if len(pts) != 5 {
		t.Fatalf("got %d spots, want 5", len(pts))
	}
	if pts[0] != pCfg.Sides["top"].P1 {
		t.Errorf("first = %v, want %v", pts[0], pCfg.Sides["top"].P1)
	}
	if pts[len(pts)-1] != pCfg.Sides["top"].P2 {
		t.Errorf("last  = %v, want %v", pts[len(pts)-1], pCfg.Sides["top"].P2)
	}
	// middle spot — equal-distribution sanity
	if pts[2] != image.Pt(430, 110) {
		t.Errorf("mid = %v, want {430,110}", pts[2])
	}
}

// TestMirrorForSide locks the axis-pair swap behavior. Different-axis
// requests MUST fall through unchanged (the documented no-op) so a
// future caller doing top→left doesn't silently get a diagonal flip.
func TestMirrorForSide(t *testing.T) {
	p := image.Pt(100, 200)
	cases := []struct {
		name, from, to string
		want           image.Point
	}{
		{"top→bottom flips Y", "top", "bottom", image.Pt(100, 732-200)},
		{"bottom→top flips Y", "bottom", "top", image.Pt(100, 732-200)},
		{"left→right flips X", "left", "right", image.Pt(860-100, 200)},
		{"right→left flips X", "right", "left", image.Pt(860-100, 200)},
		{"top→top unchanged", "top", "top", p},
		{"top→left no-op on axis mismatch", "top", "left", p},
		{"bottom→right no-op on axis mismatch", "bottom", "right", p},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := MirrorForSide(p, c.from, c.to, 860, 732)
			if got != c.want {
				t.Errorf("got %v, want %v", got, c.want)
			}
		})
	}
}
