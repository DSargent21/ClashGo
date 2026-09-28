package attack

import (
	"image"
	"testing"

	"github.com/Ducky705/ClashGO/pkg/formula"
)

// The red outline is a closed loop; a point strictly inside it is a tap the game
// refuses. This is the shape test the deploy path now runs before tapping, and
// the bug it prevents is silent: the tap succeeds and the unit stays in the bar.
func TestRedZoneInside_ClosedOutline(t *testing.T) {
	// A square red line from (200,150) to (1080,560) — roughly a 720p village.
	zone := RedZone{
		Valid: true,
		BBox:  image.Rect(200, 150, 1080, 560),
		Polygon: []image.Point{
			{200, 150}, {1080, 150}, {1080, 560}, {200, 560},
		},
	}

	cases := []struct {
		name string
		x, y int
		want bool
	}{
		{"centre of the no-deploy area", 640, 350, true},
		{"just inside the left edge", 205, 350, true},
		{"the bottom-left deploy strip", 60, 500, false},
		{"outside the top edge", 640, 100, false},
		{"outside the right edge", 1200, 350, false},
	}
	for _, c := range cases {
		if got := zone.Inside(c.x, c.y); got != c.want {
			t.Errorf("Inside(%d,%d) = %v, want %v (%s)", c.x, c.y, got, c.want, c.name)
		}
	}

	// An unusable outline must answer false rather than guess, so an unknown
	// zone never moves a point the user pinned.
	empty := RedZone{Valid: true}
	if empty.Inside(640, 350) {
		t.Error("Inside() on a zone with no outline = true; want false (unknown must not move taps)")
	}
}

func TestRedZoneCentre(t *testing.T) {
	zone := RedZone{Valid: true, BBox: image.Rect(200, 150, 1080, 560)}
	if x, y := zone.Centre(); x != 640 || y != 355 {
		t.Fatalf("Centre() = (%d,%d), want (640,355)", x, y)
	}
}

// A pinned formula line inside the red area must be walked out of it, radially,
// in whole tiles — and a line that is already clear must not move at all.
func TestFormulaPushOutOfZone(t *testing.T) {
	f := &formula.Formula{
		Screen: formula.ScreenSize{W: 860, H: 732},
		Units: map[string]formula.UnitEntry{
			"balloon": {Type: "line", P1: &formula.Point{X: 300, Y: 400}, P2: &formula.Point{X: 400, Y: 520}},
		},
	}
	// A zone whose outline covers x 200..500, y 300..560 in reference space.
	blocked := func(x, y int) bool {
		return x >= 200 && x <= 500 && y >= 300 && y <= 560
	}

	moved := f.PushOutOfZone(blocked, 350, 430, 20, 24)
	if moved != 2 {
		t.Fatalf("PushOutOfZone moved %d points, want 2 (both endpoints of the blocked line)", moved)
	}
	e := f.Units["balloon"]
	for _, p := range []*formula.Point{e.P1, e.P2} {
		if blocked(p.X, p.Y) {
			t.Errorf("point (%d,%d) is still inside the zone", p.X, p.Y)
		}
	}

	// Already-clear geometry is left exactly alone.
	g := &formula.Formula{Units: map[string]formula.UnitEntry{
		"balloon": {Type: "line", P1: &formula.Point{X: 20, Y: 60}, P2: &formula.Point{X: 40, Y: 80}},
	}}
	if moved := g.PushOutOfZone(blocked, 350, 430, 20, 24); moved != 0 {
		t.Fatalf("PushOutOfZone moved %d clear points, want 0", moved)
	}
	p := g.Units["balloon"].P1
	if p.X != 20 || p.Y != 60 {
		t.Errorf("clear point moved to (%d,%d), want (20,60) untouched", p.X, p.Y)
	}
}

// A point that cannot escape within the step budget stays where it was: the
// honest failure is a refused tap the log shows, not a tap relocated somewhere
// the user never aimed at.
func TestFormulaPushOutOfZone_GivesUpRatherThanRelocating(t *testing.T) {
	f := &formula.Formula{Units: map[string]formula.UnitEntry{
		"balloon": {Type: "point", P: &formula.Point{X: 300, Y: 400}},
	}}
	alwaysBlocked := func(int, int) bool { return true }

	if moved := f.PushOutOfZone(alwaysBlocked, 300, 400, 20, 5); moved != 0 {
		t.Fatalf("moved %d points out of an unescapable zone, want 0", moved)
	}
	p := f.Units["balloon"].P
	if p.X != 300 || p.Y != 400 {
		t.Errorf("unescapable point moved to (%d,%d), want it left at (300,400)", p.X, p.Y)
	}
}
