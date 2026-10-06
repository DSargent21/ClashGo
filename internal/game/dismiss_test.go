package game

import (
	"image"
	"testing"

	"github.com/rs/zerolog"
	"gocv.io/x/gocv"
)

// newDismissClassifier returns the reference-geometry classifier the dismissal
// tests drive. Probe coordinates in overlayControlProbes are reference-space, so
// these tests run at the reference geometry where the mapping is the identity
// and an assertion can name a pixel.
func newDismissClassifier() *Classifier {
	return NewClassifier(NewCalibration(RefWidth, RefHeight), DefaultClassifierConfig(), zerolog.Nop())
}

// newFrameDevice wires a fake device whose every capture is one of the given
// frames. The device takes ownership: its Close releases them.
func newFrameDevice(frames ...gocv.Mat) *fakeDevice {
	return &fakeDevice{caps: frames, allMats: append([]gocv.Mat{}, frames...)}
}

// overlayEvidenceFrame builds the frame a state's own rule describes: every
// colour probe of that rule, painted at its authored point. The dismissal path
// refuses to act unless the classifier still sees the state on the frame it just
// captured, so a test frame has to be real evidence rather than a state name.
func overlayEvidenceFrame(t *testing.T, cl *Classifier, state GameState) gocv.Mat {
	t.Helper()
	m := gocv.NewMatWithSize(RefHeight, RefWidth, gocv.MatTypeCV8UC3)
	m.SetTo(gocv.NewScalar(0, 0, 0, 0))
	for _, rule := range cl.GetRules() {
		if rule.State != state {
			continue
		}
		if len(rule.Checks) == 0 {
			t.Fatalf("%s has no colour probes; this helper only builds probe evidence", state)
		}
		for _, chk := range rule.Checks {
			setRGB(m, chk.X, chk.Y, chk.R, chk.G, chk.B)
		}
		return m
	}
	t.Fatalf("no rule for %s", state)
	return gocv.Mat{}
}

// blankFrame is a frame with no evidence on it at all.
func blankFrame() gocv.Mat {
	m := gocv.NewMatWithSize(RefHeight, RefWidth, gocv.MatTypeCV8UC3)
	m.SetTo(gocv.NewScalar(0, 0, 0, 0))
	return m
}

// TestDismissOverlayControlProbesAreClassifierProbes is the invariant the whole
// dismissal path rests on: every point it is willing to tap is one of the
// classifier's OWN probe points for that state. That is what makes the tap
// evidence-backed instead of predicted — the pixel is the one that made the
// overlay fire — and it is why the probe table must never grow a coordinate the
// classifier has not been taught. A predicted coordinate is exactly what a run
// reads as random tapping, and (docs/RESOLUTION.md) a tap at a predicted point
// on a panel that is still animating lands on whatever is behind it.
func TestDismissOverlayControlProbesAreClassifierProbes(t *testing.T) {
	cl := newDismissClassifier()

	checked := 0
	for state, probes := range overlayControlProbes {
		rules := cl.GetRules()
		var rule *StateRule
		for i := range rules {
			if rules[i].State == state {
				rule = &rules[i]
				break
			}
		}
		if rule == nil {
			t.Errorf("%s: no classifier rule, so no probe of it can have been verified", state)
			continue
		}
		if len(probes) == 0 {
			t.Errorf("%s: listed with no probe points", state)
		}
		for _, p := range probes {
			found := false
			for _, chk := range rule.Checks {
				if chk.X == p.X && chk.Y == p.Y {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("%s: tap point (%d,%d) is not one of the rule's probes — a tap nobody verified is a random tap", state, p.X, p.Y)
			}
			checked++
		}
	}
	if checked == 0 {
		t.Fatal("no control probe was checked; the invariant has gone stale")
	}
}

// TestDismissOverlayVillageTapsNothing: a verified main village must produce no
// tap and no Back. The village is where the capture loop spends most of a run,
// so a dismissal that fires on it is a tap on a screen with nothing to dismiss —
// which is the "random tapping" failure this path exists to remove. Back is just
// as bad here: on the real village it opens CoC's quit-confirm dialog.
func TestDismissOverlayVillageTapsNothing(t *testing.T) {
	cl := newDismissClassifier()
	dev := newFrameDevice(overlayEvidenceFrame(t, cl, StateMainVillage))
	defer dev.Close()

	if acted := DismissOverlay(dev, cl.cal, cl, StateMainVillage, nil, zerolog.Nop()); acted {
		t.Error("dismissing on a verified village reported that it acted")
	}
	if len(dev.recorded) != 0 {
		t.Errorf("verified village took %d taps: %v", len(dev.recorded), dev.recorded)
	}
	if dev.wentBack {
		t.Error("verified village took a Back press; that opens the quit-confirm dialog")
	}
}

// TestDismissOverlayEvidenceGateHolds: the state must still be on the frame the
// tap is aimed at. A caller that classified an older frame, or a screen that
// moved on, gets no tap at all — nothing is tapped on a frame with no matching
// evidence.
func TestDismissOverlayEvidenceGateHolds(t *testing.T) {
	cl := newDismissClassifier()
	dev := newFrameDevice(blankFrame())
	defer dev.Close()

	// This is the state whose probe the old code tapped blind whenever it was
	// reported, whatever the frame actually held.
	if acted := DismissOverlay(dev, cl.cal, cl, StateTapToContinue, nil, zerolog.Nop()); acted {
		t.Error("acted on a frame that carries none of the state's evidence")
	}
	if len(dev.recorded) != 0 || dev.wentBack {
		t.Errorf("an unverified frame produced input: taps=%v back=%v", dev.recorded, dev.wentBack)
	}
}

// TestDismissOverlayBackStates: the overlays whose only safe exit is the cancel
// action are dismissed with the device's Back key — no tap at all. The obstacle
// sheet used to get four blind taps across the middle of the screen before this
// Back, which is the most literal form of the random tapping this change removes.
func TestDismissOverlayBackStates(t *testing.T) {
	cl := newDismissClassifier()

	for _, tc := range []struct {
		state GameState
		why   string
	}{
		{StateObstacleDialog, "the sheet is a modal: Back cancels it, and the old path swept the screen first"},
		{StateGemDialog, "the popup's primary button BUYS gems; the cancel action must not be a tap"},
		{StateShieldInfo, "same overlay family as the gem popup: cancel, never a predicted X coordinate"},
		{StateChatOpen, "already Back-dismissed before this change; kept so the table stays the whole surface"},
		{StateOfferPopup, "the offer modal has no X and its one button leaves for the store; Back is the cancel action"},
	} {
		dev := newFrameDevice(overlayEvidenceFrame(t, cl, tc.state))
		acted := DismissOverlay(dev, cl.cal, cl, tc.state, nil, zerolog.Nop())
		if !acted {
			t.Errorf("%s: expected the overlay to be dismissed (%s)", tc.state, tc.why)
		}
		if len(dev.recorded) != 0 {
			t.Errorf("%s: %d taps fired (%v); %s", tc.state, len(dev.recorded), dev.recorded, tc.why)
		}
		if !dev.wentBack {
			t.Errorf("%s: no Back press went out, so the overlay is still up", tc.state)
		}
		dev.Close()
	}
}

// TestDismissOverlayTapsTheVerifiedProbeOnly: for the overlays that do advance on
// a tap, the tap has to land exactly on the probe the classifier verified — the
// prompt text on the "ТАР!" splash and the Continue button on the news splash.
// Anything else on those screens is artwork.
func TestDismissOverlayTapsTheVerifiedProbeOnly(t *testing.T) {
	cl := newDismissClassifier()

	for _, tc := range []struct {
		state GameState
		want  image.Point
	}{
		{StateTapToContinue, image.Pt(450, 185)},
		{StateNewsSplash, image.Pt(403, 535)},
	} {
		dev := newFrameDevice(overlayEvidenceFrame(t, cl, tc.state))
		if acted := DismissOverlay(dev, cl.cal, cl, tc.state, nil, zerolog.Nop()); !acted {
			t.Errorf("%s: no tap fired", tc.state)
		}
		if len(dev.recorded) != 1 {
			t.Errorf("%s: want exactly 1 tap, got %d (%v)", tc.state, len(dev.recorded), dev.recorded)
		} else if got := dev.recorded[0]; got != tc.want {
			t.Errorf("%s: tapped %v, want the verified probe %v", tc.state, got, tc.want)
		}
		dev.Close()
	}
}

// TestDismissOverlayConfirmExitTapsCancelNotOkay: the quit-confirm dialog has two
// button faces in its own probe set, and they mean opposite things. The dismissal
// must take the orange Cancel, never the green Okay, which quits the game.
func TestDismissOverlayConfirmExitTapsCancelNotOkay(t *testing.T) {
	cl := newDismissClassifier()
	dev := newFrameDevice(overlayEvidenceFrame(t, cl, StateConfirmExit))
	defer dev.Close()

	if acted := DismissOverlay(dev, cl.cal, cl, StateConfirmExit, nil, zerolog.Nop()); !acted {
		t.Fatal("quit-confirm dialog was not dismissed")
	}
	if len(dev.recorded) != 1 {
		t.Fatalf("want exactly 1 tap, got %d (%v)", len(dev.recorded), dev.recorded)
	}
	if got, want := dev.recorded[0], image.Pt(279, 429); got != want {
		t.Errorf("tapped %v, want the Cancel face %v", got, want)
	}
	if got := dev.recorded[0]; got == image.Pt(497, 431) {
		t.Error("tapped the green Okay face; that quits the game")
	}
}

// TestDismissOverlayConnectionLostPicksTheProbeThatIsLive is the geometry law of
// the lost-connection dialog, measured on a live frame: the dialog's panel is
// drawn at a fixed pixel size rather than scaling with the display scale, so the
// reference probe maps 34 px BELOW the TRY AGAIN label at 1280x720. The dialog's
// rule therefore carries both probes, and the dismissal has to pick the one that
// actually reads as the button on the frame in front of it — the reference probe
// at the reference geometry, the measured 720p probe at 720p.
func TestDismissOverlayConnectionLostPicksTheProbeThatIsLive(t *testing.T) {
	const pinnedScale = 1.325

	t.Run("reference geometry", func(t *testing.T) {
		cl := newDismissClassifier()
		// Only the reference probes are painted, as they are on a live dialog at
		// this geometry; the 720p probe's point carries no button art here.
		m := blankFrame()
		setRGB(m, 300, 478, 0xCB, 0xE6, 0xFF) // TRY AGAIN text
		setRGB(m, 431, 581, 0xCB, 0xE6, 0xFF) // RETURN HOME text
		setRGB(m, 430, 520, 0x1A, 0x1C, 0x1E) // dimmed panel between them
		dev := newFrameDevice(m)
		defer dev.Close()

		if acted := DismissOverlay(dev, cl.cal, cl, StateConnectionLost, nil, zerolog.Nop()); !acted {
			t.Fatal("reference-geometry dialog not dismissed")
		}
		if len(dev.recorded) != 1 {
			t.Fatalf("want exactly 1 tap, got %d (%v)", len(dev.recorded), dev.recorded)
		}
		if got, want := dev.recorded[0], image.Pt(300, 478); got != want {
			t.Errorf("tapped %v, want the reference TRY AGAIN point %v", got, want)
		}
	})

	t.Run("1280x720", func(t *testing.T) {
		cal := NewCalibration(1280, 720)
		cal.SetDisplayScale(pinnedScale)
		cl := NewClassifier(cal, DefaultClassifierConfig(), zerolog.Nop())

		m := gocv.NewMatWithSize(720, 1280, gocv.MatTypeCV8UC3)
		m.SetTo(gocv.NewScalar(0, 0, 0, 0))
		// Paint every probe of the rule where its own anchor mapping puts it, the
		// way the live dialog renders. The measured 720p probe lands on the TRY
		// AGAIN label; the reference probe lands 34 px below it.
		rules := cl.GetRules()
		var rule *StateRule
		for i := range rules {
			if rules[i].State == StateConnectionLost {
				rule = &rules[i]
				break
			}
		}
		for _, chk := range rule.Checks {
			sx, sy := cal.AnchorPoint(chk.X, chk.Y, rule.Anchor)
			if sx < 0 || sy < 0 || sx >= 1280 || sy >= 720 {
				continue
			}
			setRGB(m, sx, sy, chk.R, chk.G, chk.B)
		}
		dev := newFrameDevice(m)
		defer dev.Close()

		if acted := DismissOverlay(dev, cal, cl, StateConnectionLost, nil, zerolog.Nop()); !acted {
			t.Fatal("720p dialog not dismissed")
		}
		wantX, wantY := cal.AnchorPoint(303, 451, AnchorCenter)
		if len(dev.recorded) != 1 {
			t.Fatalf("want exactly 1 tap, got %d (%v)", len(dev.recorded), dev.recorded)
		}
		if got, want := dev.recorded[0], image.Pt(wantX, wantY); got != want {
			t.Errorf("tapped %v, want the measured 720p TRY AGAIN point %v (the reference probe maps 34px below the label)", got, want)
		}
	})
}

// TestDismissOverlayWelcomeBackNeedsTheOkayTemplate: the Welcome Back popup is
// dismissed by a located template, never by the old blind tap at its centre. With
// no template store there is nothing to locate and the answer is "no tap" — the
// stuck-watchdog owns that case, which is the point: a tap nobody could aim is
// not progress.
func TestDismissOverlayWelcomeBackNeedsTheOkayTemplate(t *testing.T) {
	cl := newDismissClassifier()
	dev := newFrameDevice(overlayEvidenceFrame(t, cl, StateWelcomeBack))
	defer dev.Close()

	if acted := DismissOverlay(dev, cl.cal, cl, StateWelcomeBack, nil, zerolog.Nop()); acted {
		t.Error("reported acting with no template to locate the Okay button")
	}
	if len(dev.recorded) != 0 {
		t.Errorf("blind-tapped %v; the popup's Okay button was never located", dev.recorded)
	}
}

// TestDismissOverlayStatesWithoutAnOverlayTapsNothing: every other state the
// capture loop can hand over (battle, search, loading) is not an overlay and must
// never produce input from this path. Unknown has no rule at all — it is the
// classifier's "nothing matched" answer, which is the state with the least
// evidence a run can be in — so it gets a blank frame and the same expectation.
func TestDismissOverlayStatesWithoutAnOverlayTapsNothing(t *testing.T) {
	cl := newDismissClassifier()

	for _, state := range []GameState{StateBattle, StateSearchMap, StateLoading, StateBattleEnd} {
		m := overlayEvidenceFrame(t, cl, state)
		dev := newFrameDevice(m)
		if acted := DismissOverlay(dev, cl.cal, cl, state, nil, zerolog.Nop()); acted {
			t.Errorf("%s: reported acting on a non-overlay state", state)
		}
		if len(dev.recorded) != 0 || dev.wentBack {
			t.Errorf("%s: produced input: taps=%v back=%v", state, dev.recorded, dev.wentBack)
		}
		dev.Close()
	}

	dev := newFrameDevice(blankFrame())
	defer dev.Close()
	if acted := DismissOverlay(dev, cl.cal, cl, StateUnknown, nil, zerolog.Nop()); acted {
		t.Error("reported acting on StateUnknown")
	}
	if len(dev.recorded) != 0 || dev.wentBack {
		t.Errorf("StateUnknown produced input: taps=%v back=%v", dev.recorded, dev.wentBack)
	}
}
