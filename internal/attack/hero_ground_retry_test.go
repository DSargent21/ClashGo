package attack

import (
	"image"
	"testing"
)

// ---------------------------------------------------------------------------
// The two defects the 2026-09-27 live runs (TopLeft and BottomRight, 720p)
// pinned down, both measured from recorded frames of the real battles:
//
//  1. A hero card that has LEFT the bar reads 0.094-0.132, while a resting or
//     merely SELECTED card reads 0.387-0.719. The delta test alone cannot tell
//     them apart (tapping BK moves its slot 0.7186 -> 0.6393, tapping Duke moves
//     its 0.3871 -> 0.7047), so a real drop was written off, the ability pass
//     skipped the hero, and the sweep spent the hero's second and last card tap
//     re-placing it. Zero abilities fired in both battles.
//
//  2. A refusal is a REGION, so retrying the neighbouring tiles along the same
//     deploy line is refused with it. Both heroes were refused at x=192 while
//     the sweep's later tap at the SAME point was accepted, only because the
//     no-deploy area had shrunk by then. Retries must step OUTWARD first.
// ---------------------------------------------------------------------------

// TestHeroPlaced_AcceptsConsumedCard pins defect 1 with the exact live numbers.
func TestHeroPlaced_AcceptsConsumedCard(t *testing.T) {
	for _, tc := range []struct {
		name           string
		pre, post      float64
		havePost, want bool
	}{
		// Live: hero phase after the field tap. The card had left the bar.
		{"king consumed", 0.7186, 0.1015, true, true},
		{"duke consumed", 0.3871, 0.1322, true, true},
		{"consumed right at the ceiling", 0.7, 0.25, true, true},
		// Live: the same reads a refused drop leaves behind (card still on the
		// bar, only selected). These must stay unconfirmed: the ability pass
		// firing here would be tapping a hero that was never placed.
		{"king selected, not deployed", 0.7186, 0.6393, true, false},
		{"duke selected, not deployed", 0.3871, 0.7047, true, false},
		{"no post read", 0.7186, 0, false, false},
		// The classic delta still works for cards whose resting value is high.
		{"classic delta", 0.72, 0.30, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := heroPlaced(tc.pre, tc.post, tc.havePost); got != tc.want {
				t.Fatalf("heroPlaced(%.4f, %.4f, %v) = %v, want %v", tc.pre, tc.post, tc.havePost, got, tc.want)
			}
		})
	}
}

// TestAxisCandidates_RetryStepsOutwardFirst pins defect 2: the first candidate is
// still the exact line point, but the retries leave the line toward the nearest
// screen edge instead of sliding along it.
func TestAxisCandidates_RetryStepsOutwardFirst(t *testing.T) {
	const h, w = 720, 1280
	intent := image.Pt(400, 504) // a hero pin inland of the line

	// Left-hand battle (TopLeft): the line is at x=192, outward is -x.
	got := axisCandidates(intent, 192, true, h, w)
	if len(got) < 3 {
		t.Fatalf("want at least 3 candidates, got %v", got)
	}
	if got[0] != image.Pt(192, 504) {
		t.Fatalf("candidate 0 must be the line point, got %v", got[0])
	}
	if got[1].X >= got[0].X {
		t.Fatalf("retry 1 must step OUTWARD (smaller x on a left-side line): %v after %v", got[1], got[0])
	}
	if got[2].X >= got[1].X {
		t.Fatalf("retry 2 must keep stepping outward: %v after %v", got[2], got[1])
	}
	if got[1].Y != got[0].Y {
		t.Fatalf("the outward step must not also move along the line: %v vs %v", got[1], got[0])
	}

	// Right-hand battle (BottomRight): outward is +x.
	got = axisCandidates(intent, 1088, true, h, w)
	if got[1].X <= got[0].X || got[2].X <= got[1].X {
		t.Fatalf("right-side line must retry toward the right edge, got %v", got[:3])
	}

	// Outward steps are clamped inside the screen margin.
	edge := axisCandidates(intent, 65, true, h, w)
	for _, p := range edge {
		if p.X < xMinPad || p.X > w-xMinPad {
			t.Fatalf("candidate %v escapes the margin [%d,%d]", p, xMinPad, w-xMinPad)
		}
	}
}

// TestAxisCandidates_HorizontalLineRetriesUpward keeps the same rule for a
// top/bottom deploy line, which spans y instead of x.
func TestAxisCandidates_HorizontalLineRetriesUpward(t *testing.T) {
	const h, w = 720, 1280
	got := axisCandidates(image.Pt(600, 400), 300, false, h, w) // top side
	if len(got) < 3 {
		t.Fatalf("want at least 3 candidates, got %v", got)
	}
	if got[0] != image.Pt(600, 300) {
		t.Fatalf("candidate 0 must be the line point, got %v", got[0])
	}
	if got[1].Y >= got[0].Y || got[2].Y >= got[1].Y {
		t.Fatalf("top-side line must retry upward, got %v", got[:3])
	}
	// Near the top margin only one outward step fits; the walk must not produce
	// duplicates or leave the band.
	nearEdge := axisCandidates(image.Pt(600, 400), 145, false, h, w)
	seen := map[image.Point]bool{}
	for _, p := range nearEdge {
		if seen[p] {
			t.Fatalf("duplicate candidate %v in %v", p, nearEdge)
		}
		seen[p] = true
		if p.Y < yTopMin {
			t.Fatalf("candidate %v escapes the band (yTopMin=%d)", p, yTopMin)
		}
	}
}
