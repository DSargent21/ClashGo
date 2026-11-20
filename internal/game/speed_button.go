package game

import (
	"image"

	"github.com/Ducky705/ClashGO/internal/vision"
	"gocv.io/x/gocv"
)

// The battle HUD shows a green speed pill at the right edge once a raid is in
// progress: it reads "1x" at normal playback and "4x" after one tap. Tapping it
// is the difference between a two-minute battle and a thirty-second one, but
// the pill's position is HUD chrome (it moves with the viewport), so the tap
// point is located from the frame rather than authored.
//
// Two templates, not one: the rounded square is identical in both states, and
// a 1x template alone matches a 4x pill well enough to be dangerous — tapping
// a pill that already reads 4x cycles it back down. The caller only taps when
// the 1x match beats the 4x match on the same frame.
const (
	speedButton1xTemplate = "speed_1x"
	speedButton4xTemplate = "speed_4x"
	speedButtonMinConf    = 0.80
)

// speedButtonRegion is the search strip for the pill as fractions of the frame.
// Measured on live 1024x768 battles: the pill's face occupies (950-1010,
// 450-510), i.e. x 0.93-0.986 W and y 0.586-0.664 H. The strip keeps margin on
// every side so a viewport change moves the pill inside it rather than out.
func speedButtonRegion(w, h int) image.Rectangle {
	return image.Rect(
		int(0.88*float64(w)), int(0.52*float64(h)),
		int(0.995*float64(w)), int(0.70*float64(h)),
	)
}

// SpeedButtonPoint returns the centre of the green "1x" speed pill when the
// frame shows it, false otherwise. templates may be nil (callers without a
// template store simply do not speed up).
func SpeedButtonPoint(screen gocv.Mat, templates *TemplateStore) (image.Point, bool) {
	if screen.Empty() || templates == nil {
		return image.Point{}, false
	}
	tpl1x, ok := templates.Get(speedButton1xTemplate)
	if !ok || tpl1x.Cols() < 8 || tpl1x.Rows() < 8 {
		return image.Point{}, false
	}
	region := speedButtonRegion(screen.Cols(), screen.Rows())
	if region.Dx() < tpl1x.Cols() || region.Dy() < tpl1x.Rows() {
		return image.Point{}, false
	}
	pt, conf1x, err := vision.MatchTemplateRegion(screen, tpl1x, region, 0)
	if err != nil || conf1x < speedButtonMinConf {
		return image.Point{}, false
	}
	// The 4x pill is the same widget with a different glyph. It must not be
	// mistaken for the 1x state: one more tap there would slow the battle back
	// down. Requiring the 1x match to win the comparison also keeps a frame
	// captured mid-relabelling from firing twice.
	if tpl4x, ok := templates.Get(speedButton4xTemplate); ok && tpl4x.Cols() == tpl1x.Cols() && tpl4x.Rows() == tpl1x.Rows() {
		if _, conf4x, err := vision.MatchTemplateRegion(screen, tpl4x, region, 0); err == nil && conf4x > conf1x {
			return image.Point{}, false
		}
	}
	return pt, true
}
