package game

import (
	"os"
	"testing"

	"github.com/rs/zerolog"
	"gocv.io/x/gocv"
)

// inactivityFramePath is a live 1280x720 capture of the "Anyone there? / You
// have been disconnected due to inactivity" prompt (gitignored like the other
// live captures; see testdata/live).
// Two captures of the same dialog on purpose: `adb exec-out screencap` and the
// bot's own capture pipe disagree by ~19 levels on the body text (anti-aliasing),
// which is what made a screenshot classify as Inactivity while the live bot read
// Unknown. Both must pass.
var inactivityFramePaths = []string{
	"testdata/live/inactivity_1280x720.png",
	"testdata/live/inactivity_pipe_1280x720.png",
}

func loadInactivityFrame(t *testing.T, path string) gocv.Mat {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
		t.Skipf("%s is not present (live captures are gitignored)", path)
	}
	frame := gocv.IMRead(path, gocv.IMReadColor)
	if frame.Empty() {
		t.Fatalf("could not read %s", path)
	}
	return frame
}

func inactivityClassifier(t *testing.T, frame gocv.Mat) *Classifier {
	t.Helper()
	cal := NewCalibration(frame.Cols(), frame.Rows())
	cal.SetDisplayScale(1.325)
	return NewClassifier(cal, DefaultClassifierConfig(), zerolog.Nop())
}

// TestInactivityPromptClassifiedOnTheLiveFrame: the prompt used to classify as
// something else (live: ChestReward, so the bot ran the chest dismissal), and
// the frame then sat untouched for minutes.
func TestInactivityPromptClassifiedOnTheLiveFrame(t *testing.T) {
	for _, path := range inactivityFramePaths {
		t.Run(path, func(t *testing.T) {
			frame := loadInactivityFrame(t, path)
			defer frame.Close()

			state, score := inactivityClassifier(t, frame).ClassifyState(frame)
			if state != StateInactivity {
				t.Fatalf("inactivity prompt (%s) classified as %s (score %d), want Inactivity", path, state, score)
			}
			t.Logf("%s -> %s score=%d", path, state, score)
		})
	}
}

// TestInactivityPromptTapIsTheReloadButton: the recovery has to land on RELOAD
// GAME, read off the same frame the classifier just confirmed. The expected
// point is the button's text stroke measured on the live capture.
func TestInactivityPromptTapIsTheReloadButton(t *testing.T) {
	frame := loadInactivityFrame(t, inactivityFramePaths[0])
	dev := newFrameDevice(frame, frame)
	defer dev.Close()
	cl := inactivityClassifier(t, dev.caps[0])

	if acted := DismissOverlay(dev, cl.cal, cl, StateInactivity, nil, zerolog.Nop()); !acted {
		t.Fatal("inactivity prompt was not dismissed from verified evidence")
	}
	if len(dev.recorded) != 1 {
		t.Fatalf("fired %d taps (%v), want exactly one on RELOAD GAME", len(dev.recorded), dev.recorded)
	}
	x, y := dev.recorded[0].X, dev.recorded[0].Y
	if x < 260 || x > 480 || y < 400 || y > 490 {
		t.Fatalf("tap (%d,%d) is not on the RELOAD GAME button (live ~294,436)", x, y)
	}
	if dev.wentBack {
		t.Error("Back was pressed; it would open the quit-confirm dialog instead of reloading")
	}
}
