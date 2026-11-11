package attack

import (
	"image"
	"testing"
)

// TestHasPinnedForTarget_RealPin verifies a non-zero edge counts as pinned.
func TestHasPinnedForTarget_RealPin(t *testing.T) {
	pCfg := PrecisionConfig{
		Edges: map[string]ManualEdge{
			"BottomLeft": {
				P1: image.Pt(92, 411),
				P2: image.Pt(300, 564),
			},
		},
	}
	if !hasPinnedForTarget(pCfg, "BottomLeft") {
		t.Errorf("expected BottomLeft to be detected as pinned with diagonal coords (92,411)->(300,564)")
	}
}

// TestHasPinnedForTarget_NoPin verifies a missing key is NOT pinned.
func TestHasPinnedForTarget_NoPin(t *testing.T) {
	pCfg := PrecisionConfig{
		Edges: map[string]ManualEdge{},
	}
	if hasPinnedForTarget(pCfg, "BottomLeft") {
		t.Errorf("expected empty Edges map -> not pinned")
	}
}

// TestHasPinnedForTarget_ZeroPin verifies a (0,0)->(0,0) zero-default is NOT pinned.
// This is the Go default for absent JSON keys, so it must be excluded — otherwise
// every "missing" pin would look "pinned" and the override would never fire.
func TestHasPinnedForTarget_ZeroPin(t *testing.T) {
	pCfg := PrecisionConfig{
		Edges: map[string]ManualEdge{
			"BottomLeft": {}, // everywhere 0 (unmarshaled-from-missing-key)
		},
	}
	if hasPinnedForTarget(pCfg, "BottomLeft") {
		t.Errorf("expected (0,0)->(0,0) zero-default to NOT count as pinned")
	}
}

// TestHasPinnedForTarget_SidesMap confirms a sides entry also counts as a pin.
func TestHasPinnedForTarget_SidesMap(t *testing.T) {
	pCfg := PrecisionConfig{
		Sides: map[string]ManualEdge{
			"bottom": {
				P1: image.Pt(60, 542),
				P2: image.Pt(60, 110),
			},
		},
	}
	if !hasPinnedForTarget(pCfg, "BottomLeft") {
		t.Errorf("expected BottomLeft to be pinned via Sides[bottom]")
	}
	// Top side is not pinned; TopLeft should report false.
	if hasPinnedForTarget(pCfg, "TopLeft") {
		t.Errorf("expected TopLeft to NOT be pinned (only bottom side is set)")
	}
}

// TestHasPinnedForTarget_NilMaps confirms nil Edges/Sides maps don't panic.
func TestHasPinnedForTarget_NilMaps(t *testing.T) {
	pCfg := PrecisionConfig{}
	if hasPinnedForTarget(pCfg, "BottomLeft") {
		t.Errorf("expected nil maps -> not pinned")
	}
}

// TestCornerToSide verifies the corner->physical-side mapping.
func TestCornerToSide(t *testing.T) {
	cases := []struct {
		corner string
		want   string
	}{
		{"TopLeft", "top"},
		{"TopRight", "top"},
		{"BottomLeft", "bottom"},
		{"BottomRight", "bottom"},
		{"left", "left"},
		{"right", "right"},
		{"Random", ""},
		{"", ""},
		{"unknown", ""},
	}
	for _, c := range cases {
		if got := cornerToSide(c.corner); got != c.want {
			t.Errorf("cornerToSide(%q) = %q, want %q", c.corner, got, c.want)
		}
	}
}

// TestManualEdgeToDeployLine_PreservesAxis verifies axis-aligned pins stay axis-aligned.
func TestManualEdgeToDeployLine_PreservesAxis(t *testing.T) {
	vertical := ManualEdge{
		P1: image.Pt(60, 110),
		P2: image.Pt(60, 542),
	}
	line := manualEdgeToDeployLine(vertical, "BottomLeft", 15)
	if len(line.Points) != 15 {
		t.Fatalf("expected 15 points, got %d", len(line.Points))
	}
	for i, p := range line.Points {
		if p.X != 60 {
			t.Errorf("vertical pin: point %d X=%d != 60 (axis broken)", i, p.X)
		}
	}
	if line.Side != "BottomLeft" {
		t.Errorf("expected Side=BottomLeft, got %q", line.Side)
	}
	if line.Anchor.X != 60 || line.Anchor.Y < 110 || line.Anchor.Y > 542 {
		t.Errorf("expected anchor on the line, got %v", line.Anchor)
	}
}

// TestManualEdgeToDeployLine_NDefaultsToFifteen verifies n<2 falls back to 15.
func TestManualEdgeToDeployLine_NDefaultsToFifteen(t *testing.T) {
	edge := ManualEdge{P1: image.Pt(0, 0), P2: image.Pt(100, 100)}
	if got := manualEdgeToDeployLine(edge, "BottomLeft", 0); len(got.Points) != 15 {
		t.Errorf("expected n=0 -> 15 points, got %d", len(got.Points))
	}
	if got := manualEdgeToDeployLine(edge, "BottomLeft", 1); len(got.Points) != 15 {
		t.Errorf("expected n=1 -> 15 points (min n=2 guard), got %d", len(got.Points))
	}
}

// TestIsZeroManualEdge verifies the zero-default detector.
func TestIsZeroManualEdge(t *testing.T) {
	if !isZeroManualEdge(ManualEdge{}) {
		t.Error("empty ManualEdge should be zero")
	}
	if !isZeroManualEdge(ManualEdge{
		P1: image.Pt(0, 0),
		P2: image.Pt(0, 0),
	}) {
		t.Error("(0,0)->(0,0) should be zero")
	}
	// Any non-zero coord breaks zero-ness.
	if isZeroManualEdge(ManualEdge{P1: image.Pt(0, 110), P2: image.Pt(0, 542)}) {
		t.Error("vertical line on left edge should NOT be zero (real pin)")
	}
	if isZeroManualEdge(ManualEdge{P1: image.Pt(60, 0), P2: image.Pt(60, 542)}) {
		t.Error("vertical line at x=60 should NOT be zero (real pin)")
	}
}

// TestApplyCornerOverride_PinnedTargetPreserved verifies the fix: when
// the user pinned the chosen target, the pinned corner is left
// untouched while unpinned corners inherit the SAME PIN so Duke's
// adjacent-corner random pick stays on the user's line.
func TestApplyCornerOverride_PinnedTargetPreserved(t *testing.T) {
	pinnedVertical := ManualEdge{P1: image.Pt(60, 110), P2: image.Pt(60, 652)}
	pCfg := PrecisionConfig{
		Edges: map[string]ManualEdge{
			"BottomLeft": pinnedVertical,
		},
	}
	applyCornerOverride(&pCfg, "BottomLeft")

	// The pinned target stays untouched.
	if got, want := pCfg.Edges["BottomLeft"], pinnedVertical; got != want {
		t.Errorf("pinned target clobbered: got %+v, want %+v", got, want)
	}
	// Duke's adjacent-corner picks (TopLeft, TopRight, BottomRight)
	// inherit the user's pin — not the dynamic line — so Duke stays
	// coherent on the chosen side.
	for _, c := range []string{"TopLeft", "TopRight", "BottomRight"} {
		if got, want := pCfg.Edges[c], pinnedVertical; got != want {
			t.Errorf("%s should mirror pinned target (Duke-coherence fix): got %+v, want %+v", c, got, want)
		}
	}
	// Sides map also mirrors the pin.
	for _, s := range []string{"top", "right", "bottom", "left"} {
		if got, want := pCfg.Sides[s], pinnedVertical; got != want {
			t.Errorf("Sides[%q] should mirror pinned target: got %+v, want %+v", s, got, want)
		}
	}
}

// With no pin for the target there is nothing to mirror, and the override must
// stay out of the config entirely: writing a line there is how a run ended up
// attacking on ground nobody authored.
func TestApplyCornerOverride_UnpinnedIsANoOp(t *testing.T) {
	pCfg := PrecisionConfig{
		Edges: map[string]ManualEdge{},
	}
	applyCornerOverride(&pCfg, "BottomLeft")

	if len(pCfg.Edges) != 0 {
		t.Errorf("unpinned target wrote %d corners, want none: %+v", len(pCfg.Edges), pCfg.Edges)
	}
	if len(pCfg.Sides) != 0 {
		t.Errorf("unpinned target wrote %d sides, want none: %+v", len(pCfg.Sides), pCfg.Sides)
	}
}

// An existing pin is never overwritten by the mirror, and a target pinned through
// Sides works the same as one pinned through Edges.
func TestApplyCornerOverride_KeepsExistingPins(t *testing.T) {
	existing := ManualEdge{P1: image.Pt(50, 100), P2: image.Pt(50, 600)}
	chosen := ManualEdge{P1: image.Pt(70, 120), P2: image.Pt(70, 610)}
	pCfg := PrecisionConfig{
		Edges: map[string]ManualEdge{
			"BottomLeft": chosen,
			"TopLeft":    existing,
		},
	}
	applyCornerOverride(&pCfg, "BottomLeft")

	if got := pCfg.Edges["TopLeft"]; got != existing {
		t.Errorf("existing pin overwritten: got %+v, want %+v", got, existing)
	}
	if got := pCfg.Edges["TopRight"]; got != chosen {
		t.Errorf("empty corner not mirrored: got %+v, want %+v", got, chosen)
	}
	if got := pCfg.Sides["left"]; got != chosen {
		t.Errorf("empty side not mirrored: got %+v, want %+v", got, chosen)
	}
}

// TestApplyCornerOverride_PinnedViaSides verifies users who pinned via
// Sides (the new strict-mode output) also get Duke-coherence mirroring.
func TestApplyCornerOverride_PinnedViaSides(t *testing.T) {
	pinned := ManualEdge{P1: image.Pt(60, 110), P2: image.Pt(60, 650)}
	pCfg := PrecisionConfig{
		Sides: map[string]ManualEdge{
			"bottom": pinned,
		},
	}
	applyCornerOverride(&pCfg, "BottomLeft")
	// Sides[bottom] preserved.
	if got, want := pCfg.Sides["bottom"], pinned; got != want {
		t.Errorf("Sides[bottom] should be preserved: got %+v, want %+v", got, want)
	}
	// Sides[top/right/left] mirror the pin.
	for _, s := range []string{"top", "right", "left"} {
		if got, want := pCfg.Sides[s], pinned; got != want {
			t.Errorf("Sides[%q] should mirror pin: got %+v, want %+v", s, got, want)
		}
	}
}
