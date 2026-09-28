package game

import (
	"testing"

	"github.com/rs/zerolog"
	"gocv.io/x/gocv"
)

// The battle-result overlay probes were re-authored against the live panel
// (2026-09-16) because the previous ones had drifted: the rule's two body probes
// (a white header and a light blue-gray sub-band) read the dimmed battlefield on
// a defeat overlay, and the RETURN HOME probe sat 141 px left of the button. The
// result was that no result screen ever classified, so the battle-end wait never
// returned and a session could not finish a single attack at ANY geometry.
//
// These tests pin the measured geometry rather than the old authored one. Every
// colour below was sampled with PIL from a live 860x732 capture of the defeat
// overlay.

// The result overlay's opaque gold star/bonus band, sampled on both flanks of
// the damage banner.
var (
	battleEndGoldLeft  = [3]uint8{237, 206, 94}  // ref (300,240)
	battleEndGoldRight = [3]uint8{240, 212, 112} // ref (560,240)
	// Solid green face of the RETURN HOME button, ref (431,600). The button is
	// horizontally centred on the overlay, which is what lets the probe survive
	// the centre-anchored mapping at a non-reference geometry.
	battleEndReturnHome = [3]uint8{118, 195, 36}
	// The same three points on a live village frame: grass, shadow, and the
	// grey village surface behind where the button sits on the panel.
	villageGoldLeft  = [3]uint8{140, 196, 65}
	villageGoldRight = [3]uint8{20, 21, 17}
	villageReturn    = [3]uint8{169, 153, 150}
)

func newResultClassifier() *Classifier {
	cal := &Calibration{PhysicalW: RefWidth, PhysicalH: RefHeight, ScaleX: 1, ScaleY: 1}
	return NewClassifier(cal, DefaultClassifierConfig(), zerolog.Nop())
}

// paint sets a 3x3 block of the frame so a probe reads the intended colour even
// if the mapping lands a pixel off.
func paint(m gocv.Mat, x, y int, c [3]uint8) {
	for dy := -1; dy <= 1; dy++ {
		for dx := -1; dx <= 1; dx++ {
			px, py := x+dx, y+dy
			if px < 0 || py < 0 || px >= m.Cols() || py >= m.Rows() {
				continue
			}
			m.SetUCharAt(py, px*3, c[2])   // B
			m.SetUCharAt(py, px*3+1, c[1]) // G
			m.SetUCharAt(py, px*3+2, c[0]) // R
		}
	}
}

func newFrame() gocv.Mat {
	m := gocv.NewMatWithSize(RefHeight, RefWidth, gocv.MatTypeCV8UC3)
	m.SetTo(gocv.NewScalar(18, 22, 16, 0))
	return m
}

// A frame carrying the measured result-overlay features must classify as the
// battle result, because the battle-end wait returns on exactly this state.
func TestClassify_BattleEndFiresOnMeasuredResultPanel(t *testing.T) {
	c := newResultClassifier()
	m := newFrame()
	defer m.Close()

	paint(m, 300, 240, battleEndGoldLeft)
	paint(m, 560, 240, battleEndGoldRight)
	paint(m, 431, 600, battleEndReturnHome)

	state, score := c.ClassifyState(m)
	if state != StateBattleEnd {
		t.Fatalf("want StateBattleEnd on the measured result panel, got %s (score %d)", state, score)
	}
}

// The gold band is what separates the result overlay from the connection-lost
// dialog, which also renders a RETURN HOME button. With only the button present
// the result rule must not reach its 2-of-3 bar.
func TestClassify_BattleEndNeedsTheGoldBandNotJustReturnHome(t *testing.T) {
	c := newResultClassifier()
	m := newFrame()
	defer m.Close()

	paint(m, 431, 600, battleEndReturnHome)

	if state, _ := c.ClassifyState(m); state == StateBattleEnd {
		t.Fatal("RETURN HOME alone must not satisfy StateBattleEnd (the connection-lost dialog has that button too)")
	}
}

// A village frame must not be mistaken for a result overlay at any probe.
func TestClassify_BattleEndRejectsVillageFrame(t *testing.T) {
	c := newResultClassifier()
	m := newFrame()
	defer m.Close()

	paint(m, 300, 240, villageGoldLeft)
	paint(m, 560, 240, villageGoldRight)
	paint(m, 431, 600, villageReturn)

	if state, score := c.ClassifyState(m); state == StateBattleEnd {
		t.Fatalf("village frame classified as StateBattleEnd (score %d)", score)
	}
}

// The standalone ReturnHome rule is the fallback signal the battle-end wait
// accepts, so its single probe has to sit on the button the game actually draws.
func TestClassify_ReturnHomeProbeSitsOnTheButton(t *testing.T) {
	c := newResultClassifier()
	m := newFrame()
	defer m.Close()

	paint(m, 431, 600, battleEndReturnHome)

	state, _ := c.ClassifyState(m)
	if state != StateReturnHome {
		t.Fatalf("want StateReturnHome from the RETURN HOME button face, got %s", state)
	}
}

// ---------------------------------------------------------------------------
// 1280x720 (BlueStacks) result overlay.
//
// The reference-geometry probes above do not land on the features they were
// authored against once the centre mapping moves them: on a live 720p defeat
// overlay the gold band renders 30 px lower and darker (a translucent panel
// over a dimmed battlefield) and the button face 24 px lower. Measured live on
// 2026-09-18, a finished battle therefore classified as Unknown and the
// battle-end wait burned its whole 4-minute deadline while logging "stall
// detected but End Battle button not visible", never returning home.
//
// The coordinates below are those 720p pixels, sampled with PIL from the frame
// the bot actually wedged on.
const (
	liveResultGoldLeft  = 400 // -> ref (249,223)
	liveResultGoldRight = 761 // -> ref (521,265)
	liveResultButtonX   = 600 // -> ref (400,577)
)

var (
	liveResultGoldLeftRGB  = [3]uint8{255, 231, 99}
	liveResultGoldRightRGB = [3]uint8{237, 206, 106}
	liveResultGreenRGB     = [3]uint8{108, 187, 31}
)

// new720pResultClassifier pins the display scale the bot's config uses on this
// device (display_scale = 1.325); the derived diagonal ratio is 1.3004, and the
// probes are placed under the pinned value because that is what runs live.
func new720pResultClassifier() *Classifier {
	cal := NewCalibration(1280, 720)
	cal.SetDisplayScale(1.325)
	return NewClassifier(cal, DefaultClassifierConfig(), zerolog.Nop())
}

func new720pFrame() gocv.Mat {
	m := gocv.NewMatWithSize(720, 1280, gocv.MatTypeCV8UC3)
	m.SetTo(gocv.NewScalar(18, 22, 16, 0))
	return m
}

func TestClassify_BattleEndFiresOn720pResultOverlay(t *testing.T) {
	c := new720pResultClassifier()
	m := new720pFrame()
	defer m.Close()

	paint(m, liveResultGoldLeft, 171, liveResultGoldLeftRGB)
	paint(m, liveResultGoldRight, 226, liveResultGoldRightRGB)
	paint(m, liveResultButtonX, 640, liveResultGreenRGB)

	state, score := c.ClassifyState(m)
	if state != StateBattleEnd {
		t.Fatalf("want StateBattleEnd on the measured 720p result overlay, got %s (score %d)", state, score)
	}
}

// The battle-end wait also accepts StateReturnHome, and that rule carries no
// template, so it has to fire on the mapped geometry on its own.
func TestClassify_ReturnHomeFiresOn720pResultOverlay(t *testing.T) {
	c := new720pResultClassifier()
	m := new720pFrame()
	defer m.Close()

	paint(m, liveResultGoldLeft, 171, liveResultGoldLeftRGB)
	paint(m, liveResultGoldRight, 226, liveResultGoldRightRGB)
	paint(m, liveResultButtonX, 640, liveResultGreenRGB)

	state, _ := c.ClassifyState(m)
	if state != StateBattleEnd && state != StateReturnHome {
		t.Fatalf("want a terminal result state on the 720p overlay, got %s", state)
	}
}

// The post-battle Star Bonus popup draws a green button where the result
// overlay's RETURN HOME sits. It must never reach either rule's bar: a green
// button alone is not a result screen (this is why the 720p probe set has one
// green and two golds rather than two greens).
func TestClassify_StarBonusPopupIsNotABattleEnd(t *testing.T) {
	c := new720pResultClassifier()
	m := new720pFrame()
	defer m.Close()

	paint(m, liveResultButtonX, 640, liveResultGreenRGB)

	state, _ := c.ClassifyState(m)
	if state == StateBattleEnd || state == StateReturnHome {
		t.Fatalf("a lone green button (Star Bonus popup) classified as %s", state)
	}
}

// And a live 720p battle frame must stay a battle: the gold band probes sit on
// battlefield ground there, but the whole 2-probe set must not trip.
func TestClassify_720pBattleFramesStayBattle(t *testing.T) {
	c := new720pResultClassifier()
	m := new720pFrame()
	defer m.Close()

	// Measured battlefield ground under the two gold probes and the button
	// probe on a live 720p battle frame.
	paint(m, liveResultGoldLeft, 171, [3]uint8{40, 46, 28})
	paint(m, liveResultGoldRight, 226, [3]uint8{101, 119, 71})
	paint(m, liveResultButtonX, 640, [3]uint8{62, 62, 62})

	if state, _ := c.ClassifyState(m); state == StateBattleEnd || state == StateReturnHome {
		t.Fatalf("a live 720p battle frame classified as %s", state)
	}
}
