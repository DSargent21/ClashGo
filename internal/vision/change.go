package vision

import (
	"image"

	"gocv.io/x/gocv"
)

// Change detection: has the screen moved since the last frame?
//
// The classifier is a pure function of the pixels — the same frame always
// produces the same verdict — so a frame that has not changed cannot need
// classifying. That matters because a classify costs 200-490 ms on a 720p
// device (docs/PERFORMANCE.md, "Measured") while the capture loop runs one every
// 300 ms, so recognising "nothing moved" is the cheapest CPU saving available.
//
// Comparing full frames directly would cost a megabyte of copying per frame,
// which is most of what the classify is worth. Instead both frames are reduced
// to a small thumbnail (`Signature`) and compared there. A thumbnail also
// suppresses exactly the noise that would otherwise make the answer useless:
// single-pixel animation on water, gradients and anti-aliasing shimmer average
// away, while a dialog, a button or a state transition changes whole blocks.

const (
	// SignatureHeight is the thumbnail height. 90 rows keeps a 16:9 frame at
	// 160x90 and the 860x732 reference at 106x90: small enough that resizing
	// and comparing cost well under a millisecond together, large enough that a
	// 90x90 px button still covers a 7x7 block of thumbnail pixels.
	SignatureHeight = 90

	// DefaultChangeDelta is the per-channel difference at which a thumbnail
	// pixel counts as changed. It deliberately reuses the frame store's noise
	// floor (DefaultDiffThreshold) rather than inventing a second one: the
	// artifact tooling and the bot must agree on what "changed" means.
	DefaultChangeDelta = DefaultDiffThreshold
)

// Signature reduces a frame to a small BGR thumbnail for change detection. The
// returned Mat comes from the shared Mat pool — release it with PutMat. An
// unusable input (too small to compare) returns an empty Mat, which makes
// ChangedPercent report "changed" rather than silently claiming stability.
func Signature(src gocv.Mat) gocv.Mat {
	if src.Cols() < 2 || src.Rows() < 2 {
		return gocv.NewMat()
	}

	h := SignatureHeight
	if src.Rows() < h {
		h = src.Rows()
	}
	w := src.Cols() * h / src.Rows()
	if w < 2 {
		w = 2
	}

	dst := GetMat(h, w, gocv.MatTypeCV8UC3)
	if dst.Empty() {
		return gocv.NewMat()
	}
	// Resize into the pooled destination: dsize is explicit, so OpenCV writes in
	// place instead of reallocating.
	//
	// Linear, not Area: this is a ~8x downscale and the question is "did whole
	// blocks of the screen change", so per-pixel block averaging buys nothing
	// here. Measured on an M5, Area cost 5.5 ms per 860x732 frame against 0.05 ms
	// for Linear — 1-2 % of a classify's 390 ms budget for no answer at all.
	gocv.Resize(src, &dst, image.Point{X: w, Y: h}, 0, 0, gocv.InterpolationLinear)
	return dst
}

// ChangedPercent reports the percentage of thumbnail pixels that moved by more
// than perChannelDelta between two signatures.
//
// Fail-safe by construction: anything it cannot compare — an empty or
// differently shaped signature, an OpenCV error — reports 100 (changed), so a
// caller that gates work on "under threshold" can only ever do MORE work than it
// needed, never less.
func ChangedPercent(prev, cur gocv.Mat, perChannelDelta float64) float64 {
	if prev.Cols() < 2 || prev.Rows() < 2 || cur.Cols() < 2 || cur.Rows() < 2 {
		return 100
	}
	if prev.Rows() != cur.Rows() || prev.Cols() != cur.Cols() {
		return 100
	}

	diff := GetMatFrom(cur)
	defer PutMat(diff)
	if err := gocv.AbsDiff(prev, cur, &diff); err != nil {
		return 100
	}

	gray := GetMat(diff.Rows(), diff.Cols(), gocv.MatTypeCV8UC1)
	defer PutMat(gray)
	gocv.CvtColor(diff, &gray, gocv.ColorBGRToGray)
	gocv.Threshold(gray, &gray, float32(perChannelDelta), 255, gocv.ThresholdBinary)

	changed := gocv.CountNonZero(gray)
	if changed <= 0 {
		return 0
	}
	return float64(changed) * 100 / float64(cur.Rows()*cur.Cols())
}
