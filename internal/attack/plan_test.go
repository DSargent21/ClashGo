// plan_test.go verifies the Planner's side-classification logic
// against (a) the user's actual symptom — diagonal precision_config
// corners that drop troops on 2 sides when target is BottomLeft —
// and (b) the simpler SideOfPoint / SidesForCorner unit cases.
//
// All tests are pure-compute: no PNG files, no ADB, no gocv windows.
package attack

import (
	"image"
	"testing"

	"github.com/Ducky705/ClashGO/pkg/strategy"
)

// TestSideOfPoint covers the strict half-screen rule:
// Y axis beats X axis, X is the tiebreaker. Concretely:
//
//	(50, 50)        → "top"  (y < 366)
//	(800, 50)       → "top"  (y < 366)
//	(800, 700)      → "bottom"
//	(50, 700)       → "bottom"
//	(100, 366)      → "left"  (exactly on midY, falls to X)
//	(800, 366)      → "right"
//	(430, 100)      → "top"
//	(430, 600)      → "bottom"
func TestSideOfPoint(t *testing.T) {
	w, h := 860, 732
	cases := []struct {
		name string
		x, y int
		want string
	}{
		{"top-left quad y-dominant", 50, 50, "top"},
		{"top-right quad y-dominant", 800, 50, "top"},
		{"bottom-right quad", 800, 700, "bottom"},
		{"bottom-left quad", 50, 700, "bottom"},
		{"exactly on midY → x-axis decides left", 100, 366, "left"},
		{"exactly on midY → x-axis decides right", 800, 366, "right"},
		{"mid-col top", 430, 100, "top"},
		{"mid-col bottom", 430, 600, "bottom"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := SideOfPoint(tc.x, tc.y, w, h); got != tc.want {
				t.Fatalf("SideOfPoint(%d,%d) = %q, want %q", tc.x, tc.y, got, tc.want)
			}
		})
	}
}

func TestSidesForCorner(t *testing.T) {
	cases := []struct {
		edge string
		want []string
	}{
		{"TopLeft", []string{"top", "left"}},
		{"topleft", []string{"top", "left"}},
		{" topleft ", []string{"top", "left"}},
		{"TopRight", []string{"top", "right"}},
		{"BottomLeft", []string{"bottom", "left"}},
		{"BottomRight", []string{"bottom", "right"}},
		{"top", []string{"top"}},
		{"right", []string{"right"}},
		{"bottom", []string{"bottom"}},
		{"left", []string{"left"}},
		{"", []string{}},
		{"Random", []string{}},
	}
	for _, tc := range cases {
		t.Run(tc.edge, func(t *testing.T) {
			got := SidesForCorner(tc.edge)
			if len(got) != len(tc.want) {
				t.Fatalf("SidesForCorner(%q) = %v, want %v", tc.edge, got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("SidesForCorner(%q)[%d] = %q, want %q", tc.edge, i, got[i], tc.want[i])
				}
			}
		})
	}
}

// TestPlannerDiagonalCornerReported reproduces the user's actual
// symptom: precision_config.json defines BottomLeft as the diagonal
// line P1=(92,411)→P2=(300,564). With strict half-screen classification
// the per-tap side IS "bottom" all the way down — every tap matches
// the BottomLeft envelope [bottom, left]. Per-tap Mismatches will be
// 0. The bug is therefore NOT in per-tap classification but in the
// LINE GEOMETRY itself, surfaced via PlanReport.DiagonalCorners.
//
// We assert:
//   - Mismatches is empty (per-tap is fine — strict algo)
//   - DiagonalCorners has the BottomLeft entry
//   - The entry's AngleReason is "diagonal"
func TestPlannerDiagonalCornerReported(t *testing.T) {
	pCfg := PrecisionConfig{
		Width:  860,
		Height: 732,
		Edges: map[string]ManualEdge{
			"BottomLeft": {P1: image.Pt(92, 411), P2: image.Pt(300, 564)},
		},
	}
	s := &strategy.DynamicStrategy{
		TargetEdge: "BottomLeft",
		Phases: []strategy.Phase{
			{
				Name:    "Balloons",
				Pattern: "Line",
				Units:   []strategy.Unit{{Name: "Balloon", Amount: "All", Pattern: "Line"}},
			},
		},
	}
	p := NewPlanner(pCfg, s, RedZone{}, DeployLine{}, "BottomLeft", 860, 732)
	rep := p.Plan()

	if len(rep.Mismatches) != 0 {
		t.Fatalf("strict half-screen algorithm says no per-tap mismatches; got %d: %+v", len(rep.Mismatches), rep.Mismatches)
	}

	var bottomLeft DiagonalFlag
	for _, d := range rep.DiagonalCorners {
		if d.Key == "BottomLeft" {
			bottomLeft = d
			break
		}
	}
	if bottomLeft.Key == "" {
		t.Fatalf("expected DiagonalCorners to include BottomLeft, got %+v", rep.DiagonalCorners)
	}
	if bottomLeft.AngleReason != "diagonal" {
		t.Fatalf("BottomLeft angle reason = %q, want 'diagonal' (atan2(line) is ~53°)", bottomLeft.AngleReason)
	}
	// atan2(|564-411|, |300-92|) = atan2(153, 208) ≈ 36° — borderline
	// "diagonal". The flag reason comes from our bucket: 30-60 is
	// diagonal. We assert the value lands inside the accepted range.
	if bottomLeft.AngleDeg < 25 || bottomLeft.AngleDeg > 65 {
		t.Fatalf("BottomLeft angle = %d°, want 25-65 (36° expected)", bottomLeft.AngleDeg)
	}
}

// TestPlannerStrictSideAllMatch is the inverse sanity check: when
// corners are truly on a SINGLE SIDE (top/bottom/left/right lines
// with matching target_edge) every tap should classify as a match.
func TestPlannerStrictSideAllMatch(t *testing.T) {
	w, h := 860, 732
	for _, k := range []string{"top", "bottom", "left", "right"} {
		t.Run("strict-side/"+k, func(t *testing.T) {
			var p1, p2 image.Point
			switch k {
			case "top":
				p1 = image.Pt(60, 110)
				p2 = image.Pt(w-60, 110)
			case "bottom":
				p1 = image.Pt(60, h-110)
				p2 = image.Pt(w-60, h-110)
			case "left":
				p1 = image.Pt(60, h/4)
				p2 = image.Pt(60, 3*h/4)
			case "right":
				p1 = image.Pt(w-60, h/4)
				p2 = image.Pt(w-60, 3*h/4)
			}
			pCfg := PrecisionConfig{
				Width:  w,
				Height: h,
				Edges: map[string]ManualEdge{
					"TopLeft":     {P1: p1, P2: p2},
					"TopRight":    {P1: p1, P2: p2},
					"BottomLeft":  {P1: p1, P2: p2},
					"BottomRight": {P1: p1, P2: p2},
				},
			}
			s := &strategy.DynamicStrategy{
				TargetEdge: k, // strict side target — must match the strict line
				Phases: []strategy.Phase{
					{
						Name:    "Balloons",
						Pattern: "Line",
						Units:   []strategy.Unit{{Name: "Balloon", Amount: "All", Pattern: "Line"}},
					},
				},
			}
			p := NewPlanner(pCfg, s, RedZone{}, DeployLine{}, k, w, h)
			rep := p.Plan()
			if len(rep.Mismatches) != 0 {
				t.Fatalf("expected ZERO mismatches when corner is strict side, got %d", len(rep.Mismatches))
			}
			for _, d := range rep.DiagonalCorners {
				// Strict lines are horizontal/vertical → not diagonal.
				if d.AngleReason == "diagonal" {
					t.Fatalf("%s strict-side line flagged as diagonal: angle=%d°", d.Key, d.AngleDeg)
				}
			}
		})
	}
}
