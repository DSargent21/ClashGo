package attack

import (
	"image"
	"math"
	"testing"

	"github.com/Ducky705/ClashGO/internal/adb"
	"github.com/Ducky705/ClashGO/internal/game"
	"github.com/rs/zerolog"
)

// newSweepTestExecutor builds a TapExecutor with a 1:1 calibration, which is all
// shiftLineForAttempt needs to turn a reference spacing into pixels.
func newSweepTestExecutor(t *testing.T) *TapExecutor {
	t.Helper()
	client := adb.NewClient(
		adb.WithJitterTaps(false),
		adb.WithJitterDelays(false),
		adb.WithTimeout(1),
	)
	client.Close() // transport calls fail fast; nothing here taps
	cal := &game.Calibration{PhysicalW: 860, PhysicalH: 732, ScaleX: 1.0, ScaleY: 1.0}
	return NewTapExecutor(client, cal, zerolog.Nop())
}

// spellLikeSlot decides whether a slot's retry may reuse ground. Getting it wrong
// on a spell is what let a live earthquake line re-fire onto its own two points:
// the slot was labelled "earthquake spell" but carried Category "Troop", so both
// the retry displacement and the spread-the-batch taps were skipped.
func TestSpellLikeSlot(t *testing.T) {
	cases := []struct {
		name string
		slot *TrackedSlot
		want bool
	}{
		{"nil", nil, false},
		{"category spell", &TrackedSlot{TroopSlot: TroopSlot{Category: "Spell"}}, true},
		{"named spell in a troop slot", &TrackedSlot{TroopSlot: TroopSlot{Category: "Troop"}, UnitName: "earthquake spell"}, true},
		{"capitalised name", &TrackedSlot{TroopSlot: TroopSlot{Category: "Troop"}, UnitName: "Rage Spell"}, true},
		{"event spell", &TrackedSlot{TroopSlot: TroopSlot{Category: "CC"}, UnitName: "ice spell"}, true},
		{"plain troop", &TrackedSlot{TroopSlot: TroopSlot{Category: "Troop"}, UnitName: "Balloon"}, false},
		{"hero", &TrackedSlot{TroopSlot: TroopSlot{Category: "Hero"}, UnitName: "Archer Queen"}, false},
		{"empty", &TrackedSlot{}, false},
	}
	for _, c := range cases {
		if got := spellLikeSlot(c.slot); got != c.want {
			t.Errorf("spellLikeSlot(%s) = %v, want %v", c.name, got, c.want)
		}
	}
}

// A spell retry must land beside the previous attempt, and successive retries
// must alternate sides so they do not walk off in one direction. Attempt 0 is
// the first pass and must not move at all.
func TestShiftLineForAttempt(t *testing.T) {
	sw := &Sweeper{executor: newSweepTestExecutor(t)}
	p1, p2 := image.Pt(400, 400), image.Pt(600, 300)

	if a, b := sw.shiftLineForAttempt(p1, p2, 0); a != p1 || b != p2 {
		t.Errorf("attempt 0 moved the line to %v/%v; the first pass must fire where it was aimed", a, b)
	}

	spacing := sw.executor.cal.Length(spellBatchSpacingRef)
	if spacing <= 0 {
		t.Fatalf("test spacing = %v, want > 0", spacing)
	}

	one1, one2 := sw.shiftLineForAttempt(p1, p2, 1)
	d1 := perpendicularDistance(p1, p2, one1)
	if d1 < spacing-1 {
		t.Errorf("attempt 1 moved %.1f px perpendicular, want >= %.1f", d1, spacing-1)
	}

	two1, _ := sw.shiftLineForAttempt(p1, p2, 2)
	side1 := signedPerpendicular(p1, p2, one1)
	side2 := signedPerpendicular(p1, p2, two1)
	if side1 == 0 || side2 == 0 || sameSign(side1, side2) {
		t.Errorf("attempts 1 and 2 sit on the same side (%v, %v); a retry must not keep walking one way", side1, side2)
	}
	_ = one2
}

func perpendicularDistance(a, b, pt image.Point) float64 {
	return math.Abs(signedPerpendicular(a, b, pt))
}

func signedPerpendicular(a, b, pt image.Point) float64 {
	dx := float64(b.X - a.X)
	dy := float64(b.Y - a.Y)
	length := math.Hypot(dx, dy)
	if length == 0 {
		return 0
	}
	return (float64(pt.X-a.X)*(-dy) + float64(pt.Y-a.Y)*dx) / length
}

func sameSign(a, b float64) bool {
	return (a > 0 && b > 0) || (a < 0 && b < 0)
}

// Consecutive spell retries must clear each other, not just the first pass:
// the report reconstructs taps from the p1/p2 the sweep logs, so two attempts
// whose lines are one spacing apart are two attempts whose casts cannot share
// ground.
func TestShiftLineForAttempt_ConsecutiveAttemptsClearEachOther(t *testing.T) {
	sw := &Sweeper{executor: newSweepTestExecutor(t)}
	p1, p2 := image.Pt(400, 400), image.Pt(600, 300)
	spacing := sw.executor.cal.Length(spellBatchSpacingRef)

	one1, _ := sw.shiftLineForAttempt(p1, p2, 1)
	two1, _ := sw.shiftLineForAttempt(p1, p2, 2)
	if d := distance(one1, two1); d < spacing-1 {
		t.Errorf("attempts 1 and 2 start %.1f px apart, want >= %.1f — consecutive retries must clear each other", d, spacing-1)
	}
}

func distance(a, b image.Point) float64 {
	dx := float64(a.X - b.X)
	dy := float64(a.Y - b.Y)
	return math.Hypot(dx, dy)
}
