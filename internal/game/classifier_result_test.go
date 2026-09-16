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
