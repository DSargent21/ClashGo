package game

import (
	"image"
	"testing"
)

// TestButtonROIBounds covers the table contract: a window for every button the
// project has measured one for, inside the authored frame, and no window for a
// template it has not (a guess here silently costs a detection, so the table
// must stay honest).
func TestButtonROIBounds(t *testing.T) {
	known := []string{
		"btn_attack", "btn_find_match", "btn_battle", "btn_next",
		"btn_army_arrow", "btn_army_1", "btn_okay", "btn_return_home",
	}
	frame := ReferenceFrame()
	for _, name := range known {
		r, ok := ButtonROIBounds(name)
		if !ok {
			t.Errorf("%s: no window in the table", name)
			continue
		}
		if !r.In(frame) {
			t.Errorf("%s: window %v is not inside the authored frame %v", name, r, frame)
		}
		if r.Dx() < 2 || r.Dy() < 2 {
			t.Errorf("%s: window %v is degenerate", name, r)
		}
	}

	// hammer gates StateChestReward on its template alone (MinPass 0), so a
	// wrong window is a lost chest break, not a lost tie-break bonus. It has no
	// measured window and must keep the full-frame search.
	for _, name := range []string{"hammer", "btn_does_not_exist", ""} {
		if r, ok := ButtonROIBounds(name); ok {
			t.Errorf("%s: unexpected window %v; an unmeasured template must fall back to the frame", name, r)
		}
	}
}

// TestRuleSearchROI_ReferenceGeometry pins the identity: at the authored
// geometry every window maps to itself, so the reference frames classify
// exactly as they did before the windows existed.
func TestRuleSearchROI_ReferenceGeometry(t *testing.T) {
	cal := NewCalibration(RefWidth, RefHeight)
	if !cal.IsReferenceGeometry() {
		t.Fatal("the authored geometry must be the identity mapping")
	}
	frame := image.Rect(0, 0, RefWidth, RefHeight)
	for anchor := range map[Anchor]bool{AnchorEdge: true, AnchorCenter: true, AnchorCenterBottom: true} {
		for name := range map[string]bool{"btn_attack": true, "btn_okay": true, "btn_return_home": true, "btn_next": true} {
			want, _ := ButtonROIBounds(name)
			got := RuleSearchROI(cal, name, anchor, frame)
			if got != want {
				t.Errorf("%s anchor %v: mapped %v, want the authored window %v", name, anchor, got, want)
			}
		}
	}
}

// TestRuleSearchROI_Anchoring pins that a window follows the same anchoring its
// rule's probes use, because that is what keeps the window over the button on a
// non-reference geometry: HUD chrome keeps its distance to the screen edges,
// a centred dialog button keeps its offset from the centre.
//
// Whether the mapped window actually lands on the button (including the one way
// it can legitimately lose area — the frame ending before the window does) is
// asserted where the button positions live: internal/bot
// TestRuleWindowsContainPinpoints.
func TestRuleSearchROI_Anchoring(t *testing.T) {
	const (
		w = 1280
		h = 720
	)
	cal := NewCalibration(w, h)
	cal.SetDisplayScale(1.325)
	frame := image.Rect(0, 0, w, h)

	ref := NewCalibration(RefWidth, RefHeight)
	for _, tc := range []struct {
		name   string
		anchor Anchor
	}{
		{"btn_attack", AnchorEdge},
		{"btn_next", AnchorEdge},
		{"btn_battle", AnchorEdge},
		{"btn_okay", AnchorCenter},
		{"btn_return_home", AnchorCenter},
	} {
		authored, _ := ButtonROIBounds(tc.name)
		if identity := RuleSearchROI(ref, tc.name, tc.anchor, ReferenceFrame()); identity != authored {
			t.Errorf("%s: reference mapping gave %v, want %v", tc.name, identity, authored)
		}
		// On a wider frame than the reference, a HUD window keeps its
		// distance to the edge it hangs off, so it must grow with the screen
		// rather than stay the size it was authored at.
		wide := NewCalibration(1920, 1080)
		wide.SetDisplayScale(1.325)
		got := RuleSearchROI(wide, tc.name, tc.anchor, image.Rect(0, 0, 1920, 1080))
		if got.Dx() < authored.Dx() || got.Dy() < authored.Dy() {
			t.Errorf("%s: window %v on 1920x1080 is smaller than the authored %v", tc.name, got, authored)
		}
	}

	// Centre-anchored: btn_okay sits at the viewport centre horizontally, so
	// the window stays centred on x and drops below the centre on y.
	okay := RuleSearchROI(cal, "btn_okay", AnchorCenter, frame)
	if want := w / 2; absDiff(okay.Min.X+okay.Dx()/2, want) > 2 {
		t.Errorf("btn_okay window %v is not centred on x=%d", okay, want)
	}
	if okay.Min.Y <= h/2 {
		t.Errorf("btn_okay window %v must sit below the viewport centre", okay)
	}
}

// TestRuleSearchROI_FallsBackToFrame is the safety property: whenever the
// mapping cannot place a usable window, the search must widen to the whole
// frame. A window that shrank to nothing would return no matches at all — a
// lost detection — while a full-frame search only costs time.
func TestRuleSearchROI_FallsBackToFrame(t *testing.T) {
	cal := NewCalibration(RefWidth, RefHeight)

	// A template with no authored window.
	frame := image.Rect(0, 0, RefWidth, RefHeight)
	if got := RuleSearchROI(cal, "hammer", AnchorEdge, frame); got != frame {
		t.Errorf("an unmeasured template searched %v, want the whole frame %v", got, frame)
	}

	// A frame the authored window does not intersect at all.
	small := image.Rect(0, 0, 50, 50)
	if got := RuleSearchROI(cal, "btn_attack", AnchorEdge, small); got != small {
		t.Errorf("a window that maps off-frame searched %v, want the whole frame %v", got, small)
	}

	// No calibration: nothing to map with, so nothing to narrow with either.
	if got := RuleSearchROI(nil, "btn_attack", AnchorEdge, frame); got != frame {
		t.Errorf("a nil calibration searched %v, want the whole frame %v", got, frame)
	}
}

// TestRuleSearchROI_ReducesSearchedArea is the point of the exercise: on the
// live geometry the windowed rules must search strictly less of the frame than
// they used to, or the classifier pays the same cost for more risk.
func TestRuleSearchROI_ReducesSearchedArea(t *testing.T) {
	const (
		w = 1280
		h = 720
	)
	cal := NewCalibration(w, h)
	cal.SetDisplayScale(1.325)
	frame := image.Rect(0, 0, w, h)
	whole := frame.Dx() * frame.Dy()

	rule := func(name string, a Anchor) {
		t.Helper()
		got := RuleSearchROI(cal, name, a, frame)
		area := got.Dx() * got.Dy()
		if area >= whole {
			t.Errorf("%s: window %v covers the whole frame; there is nothing to save", name, got)
		}
		t.Logf("%-16s searches %6.1f%% of the frame (%v of %v)", name, 100*float64(area)/float64(whole), got, frame)
	}
	// The anchors are the ones the rules declare: the button rules are HUD
	// chrome, the two dialog rules are centred overlays.
	rule("btn_attack", AnchorEdge)
	rule("btn_next", AnchorEdge)
	rule("btn_find_match", AnchorEdge)
	rule("btn_battle", AnchorEdge)
	rule("btn_okay", AnchorCenter)
	rule("btn_return_home", AnchorCenter)
}
