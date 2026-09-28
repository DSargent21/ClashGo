// Digit classification against canonical-size templates.
//
// This file exists because the same mistake was made twice in two different
// readers, and the second time it cost a live battle.
//
// The mistake: compare a glyph crop against a digit template resized to fit
// *that crop's* box. The correlation is then dominated by how much of the box
// is background rather than by the shape of the digit, so on the game's real
// font a CORRECT digit scored 0.05-0.45 and the winner was essentially always
// `digit_1` — the narrowest template, which resizes to the least background.
//
// One reader hid the problem behind a special case (a thin bright blob with
// >0.65 fill is called a `1`, no template needed), which is why a damage panel
// reading "16%" came back as "1": the `1` was accepted by the shortcut, the `6`
// scored under the floor and was dropped. That read is the destruction
// percentage, and it is what left the bot's end-of-battle wait stuck on 0%
// while it watched the damage panel.
//
// The fix, measured on real 720p frames with an army loaded: crop each glyph and
// each template to its own ink, resize both to one canonical box, and compare
// shapes with TM_CCOEFF_NORMED. Correct digits then score 0.54-0.72 and junk
// 0.12-0.45, so a confidence floor plus a confidence *margin* over the runner-up
// separates "this is a 6" from "this is a glyph I cannot name".
package vision

import (
	"image"
	"math"

	"gocv.io/x/gocv"
)

// DigitCanonW and DigitCanonH are the canonical comparison box. It is the shape
// of a digit at roughly 1.5x its 720p size: big enough that the resize keeps the
// stroke topology that distinguishes 6/8/9 and 5/3, small enough that the whole
// match is a few hundred comparisons.
const (
	DigitCanonW = 24
	DigitCanonH = 32
)

// CanonicalGlyph resizes one glyph (or template) crop into the canonical
// comparison box. The caller owns the returned Mat and must Close it.
func CanonicalGlyph(glyph gocv.Mat) gocv.Mat {
	canon := gocv.NewMat()
	if glyph.Empty() {
		return canon
	}
	gocv.Resize(glyph, &canon, image.Point{X: DigitCanonW, Y: DigitCanonH}, 0, 0, gocv.InterpolationLinear)
	return canon
}

// UnusableScore is the score CanonicalScores records for a template it could not
// compare (empty, or a different canonical size). It is used instead of zero
// because TM_CCOEFF_NORMED can return negative values, so a zero could otherwise
// beat every real candidate on a glyph that matches nothing.
var UnusableScore = math.Inf(-1)

// CanonicalScores scores one canonical glyph against every template, in template
// order, and returns the raw scores. A caller that can rule digits out before
// comparing — the count reader rules out every digit whose ink is far wider than
// the glyph it is looking at — can set those entries to UnusableScore and hand
// the slice to PickBest, which is how a 3px-wide stroke stops being compared
// against digits drawn 8px wide.
func CanonicalScores(canon gocv.Mat, templates []gocv.Mat) []float64 {
	scores := make([]float64, len(templates))
	for i := range scores {
		scores[i] = UnusableScore
	}
	if canon.Empty() {
		return scores
	}
	for i, tpl := range templates {
		if tpl.Empty() || tpl.Rows() != canon.Rows() || tpl.Cols() != canon.Cols() {
			continue
		}
		res := GetMat(canon.Rows()-tpl.Rows()+1, canon.Cols()-tpl.Cols()+1, gocv.MatTypeCV32FC1)
		gocv.MatchTemplate(canon, tpl, &res, gocv.TmCcoeffNormed, EmptyMask())
		_, conf, _, _ := gocv.MinMaxLoc(res)
		PutMat(res)
		scores[i] = float64(conf)
	}
	return scores
}

// PickBest returns the winner of a score slice with its confidence and its
// margin over the runner-up. Scores equal to UnusableScore are skipped.
//
// The margin is returned rather than folded into the score because the two
// answer different questions: a high confidence says "this matches a digit
// well", a high margin says "and it is not nearly the same shape as another
// digit". An 11-13 px `6` is genuinely ambiguous with `8` and `9`, and a reader
// that reports confident-but-ambiguous damage percentages is worse than one that
// reports it could not read the panel.
//
// Returns (-1, 0, 0) when no template has a usable score.
func PickBest(scores []float64) (int, float64, float64) {
	bestDigit := -1
	bestConf := UnusableScore
	secondConf := UnusableScore
	for i, v := range scores {
		if v == UnusableScore {
			continue
		}
		switch {
		case v > bestConf:
			secondConf = bestConf
			bestConf = v
			bestDigit = i
		case v > secondConf:
			secondConf = v
		}
	}
	if bestDigit == -1 {
		return -1, 0.0, 0.0
	}
	margin := bestConf
	if secondConf != UnusableScore {
		margin = bestConf - secondConf
	}
	return bestDigit, bestConf, margin
}

// MatchCanonical scores one canonical glyph against a set of canonical digit
// templates and returns the winning digit with its confidence and its margin
// over the runner-up.
//
// Returns (-1, 0, 0) when the glyph is empty or no template is usable.
func MatchCanonical(canon gocv.Mat, templates []gocv.Mat) (int, float64, float64) {
	if canon.Empty() {
		return -1, 0.0, 0.0
	}
	return PickBest(CanonicalScores(canon, templates))
}
