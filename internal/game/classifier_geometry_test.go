package game

import (
	"testing"

	"github.com/rs/zerolog"
	"gocv.io/x/gocv"
)

// At a non-reference geometry the probes are mapped predictions, so a rule with
// several probes must not be able to win a state decision off a single mapped
// hit: on live village frames one accidental ObstacleDialog probe at 1920x1080
// outranked the village. The reference geometry keeps its calibrated 1-probe
// bar, and single-probe rules keep theirs at every geometry (loading is a
// full-screen state that has only one anchor).
func TestNonReferenceGeometryRequiresASecondProbe(t *testing.T) {
	logger := zerolog.Nop()
	rule := mainVillageRule(t, NewClassifier(NewCalibration(RefWidth, RefHeight), DefaultClassifierConfig(), logger))
	gold, elixir := rule.Checks[1], rule.Checks[2] // bright gold icon, elixir icon

	// Reference geometry: one probe is exactly what was calibrated, and the
	// live village scores 160 (one pass + weight) on a village with the icons.
	refCal := NewCalibration(RefWidth, RefHeight)
	ref := NewClassifier(refCal, DefaultClassifierConfig(), logger)
	refFrame := gocv.NewMatWithSize(RefHeight, RefWidth, gocv.MatTypeCV8UC3)
	defer refFrame.Close()
	setRGB(refFrame, elixir.X, elixir.Y, elixir.R, elixir.G, elixir.B)
	if st, sc := ref.ClassifyState(refFrame); st != StateMainVillage || sc != 160 {
		t.Fatalf("reference geometry: one probe -> %s (score %d), want MainVillage (160)", st, sc)
	}

	// 1280x720: the same single probe, painted where the mapped anchor lands
	// (the elixir icon measured live at (1241,124), docs/RESOLUTION.md).
	cal := NewCalibration(1280, 720)
	cl := NewClassifier(cal, DefaultClassifierConfig(), logger)
	frame := gocv.NewMatWithSize(720, 1280, gocv.MatTypeCV8UC3)
	defer frame.Close()
	ex, ey := cal.MapX(elixir.X, rule.Anchor), cal.MapY(elixir.Y, rule.Anchor)
	if ex != 1241 || ey != 124 {
		t.Fatalf("elixir probe maps to (%d,%d), want the measured (1241,124)", ex, ey)
	}
	setRGB(frame, ex, ey, elixir.R, elixir.G, elixir.B)
	if st, sc := cl.ClassifyState(frame); st == StateMainVillage {
		t.Errorf("non-reference geometry fired MainVillage off one mapped probe (score %d)", sc)
	}

	// Two probes on the same icon column: enough evidence, as at the reference.
	gx, gy := cal.MapX(gold.X, rule.Anchor), cal.MapY(gold.Y, rule.Anchor)
	setRGB(frame, gx, gy, gold.R, gold.G, gold.B)
	if st, sc := cl.ClassifyState(frame); st != StateMainVillage || sc != 260 {
		t.Errorf("non-reference geometry with two probes -> %s (score %d), want MainVillage (260)", st, sc)
	}
}

// Centred overlays are laid out around the viewport centre, so at a geometry
// wider than the reference aspect they render clipped: their authored anchor can
// fall outside the frame and clamp to the edge. That is why a wide-aspect
// geometry needs centred states re-authored rather than just re-mapped.
func TestCentredOverlayClipsAtWiderAspect(t *testing.T) {
	cal := NewCalibration(1280, 720)
	// ObstacleDialog's white dialog corner, ref (272,11): 355 px above the
	// reference centre, so 355*1.3006 = 462 px above the 720p centre — past the
	// top of the frame.
	if got := cal.MapY(11, AnchorCenter); got != 0 {
		t.Errorf("centred ref y=11 at 1280x720 maps to %d, want it clamped to 0 (clipped)", got)
	}
	// HUD chrome at the same reference row is edge-anchored and stays visible.
	if got := cal.MapY(11, AnchorEdge); got < 1 || got > 20 {
		t.Errorf("edge ref y=11 at 1280x720 maps to %d, want it near the top edge", got)
	}
}

func mainVillageRule(t *testing.T, c *Classifier) StateRule {
	t.Helper()
	for _, rule := range c.GetRules() {
		if rule.State == StateMainVillage {
			return rule
		}
	}
	t.Fatal("no MainVillage rule")
	return StateRule{}
}
