// Command resprobe measures how the CoC HUD scales between two framebuffer
// geometries.
//
// The bot's geometry layer maps reference-frame coordinates onto the live
// frame with `ScaleRef(x,y) = (x*physW/RefWidth, y*physH/RefHeight)` — a
// model that is only correct when the emulator's UI scale factor is the
// same on both axes. Deciding whether a candidate device resolution can be
// adopted therefore needs one number per axis, not a guess: this probe takes
// a patch from a known reference capture (default 860x732), then searches
// the second capture over a grid of (scaleX, scaleY) pairs for the patch
// position that maximizes TM_CCOEFF_NORMED.
//
// A best pair near (1,1) means the HUD kept its pixel geometry (resolution
// change is layout-neutral). A pair like (1.49, 0.98) means the layout
// follows the framebuffer ratio and the existing reference frame still
// models it. Anything else — e.g. (1.3, 1.15) — is an anisotropic stretch
// the reference-frame model cannot express, and the authored reference
// coordinates for that resolution must be re-measured.
//
// Usage:
//
//	go run ./cmd/resprobe -a before.png -b after.png -x 20 -y 690 -w 90 -h 30
package main

import (
	"flag"
	"fmt"
	"image"
	"os"
	"sort"

	"gocv.io/x/gocv"
)

type hit struct {
	sx, sy float64
	conf   float64
	cx, cy int
}

func main() {
	pathA := flag.String("a", "", "reference capture (e.g. the 860x732 frame)")
	pathB := flag.String("b", "", "candidate capture (e.g. the 1280x720 frame)")
	rx := flag.Int("x", 0, "patch X in capture A")
	ry := flag.Int("y", 0, "patch Y in capture A")
	rw := flag.Int("w", 64, "patch width in capture A")
	rh := flag.Int("h", 64, "patch height in capture A")
	sx0 := flag.Float64("sx0", 0.85, "lowest horizontal scale to try")
	sx1 := flag.Float64("sx1", 1.60, "highest horizontal scale to try")
	sy0 := flag.Float64("sy0", 0.85, "lowest vertical scale to try")
	sy1 := flag.Float64("sy1", 1.60, "highest vertical scale to try")
	steps := flag.Int("steps", 41, "scale samples per axis")
	margin := flag.Int("margin", 120, "search margin (px) around the projected patch in B")
	flag.Parse()

	if *pathA == "" || *pathB == "" {
		flag.Usage()
		os.Exit(2)
	}

	a := gocv.IMRead(*pathA, gocv.IMReadColor)
	defer a.Close()
	b := gocv.IMRead(*pathB, gocv.IMReadColor)
	defer b.Close()
	if a.Empty() || b.Empty() {
		fmt.Fprintln(os.Stderr, "cannot read one of the captures")
		os.Exit(1)
	}

	patch := a.Region(image.Rect(*rx, *ry, *rx+*rw, *ry+*rh))
	defer patch.Close()
	if patch.Empty() {
		fmt.Fprintln(os.Stderr, "patch is empty (out of bounds?)")
		os.Exit(1)
	}
	fmt.Printf("patch %dx%d at (%d,%d) from %s [%dx%d]\n",
		patch.Cols(), patch.Rows(), *rx, *ry, *pathA, a.Cols(), a.Rows())
	fmt.Printf("searching %s [%dx%d]  scales x[%.2f..%.2f] y[%.2f..%.2f] %d steps\n",
		*pathB, b.Cols(), b.Rows(), *sx0, *sx1, *sy0, *sy1, *steps)

	var hits []hit
	for i := 0; i < *steps; i++ {
		sx := *sx0 + (*sx1-*sx0)*float64(i)/float64(*steps-1)
		for j := 0; j < *steps; j++ {
			sy := *sy0 + (*sy1-*sy0)*float64(j)/float64(*steps-1)
			if h, ok := matchScaled(patch, b, sx, sy, float64(*rx), float64(*ry), *margin); ok {
				hits = append(hits, h)
			}
		}
	}
	if len(hits) == 0 {
		fmt.Println("no placement fit inside capture B — widen -margin")
		os.Exit(1)
	}
	sort.Slice(hits, func(i, j int) bool { return hits[i].conf > hits[j].conf })

	fmt.Println("top 5 (scaleX, scaleY, confidence, center-in-B):")
	for i := 0; i < len(hits) && i < 5; i++ {
		h := hits[i]
		fmt.Printf("  %.3f  %.3f  conf=%.4f  center=(%d,%d)\n", h.sx, h.sy, h.conf, h.cx, h.cy)
	}
	best := hits[0]
	// Ratio of the two axes: 1.0 means a uniform scale (reference-frame
	// model works), anything else is an anisotropic stretch.
	fmt.Printf("best (%.3f, %.3f): anisotropy=%.3f\n", best.sx, best.sy, best.sx/best.sy)
}

// matchScaled resizes patch to (sx, sy) and returns the best placement of it
// inside a margin-limited window around the patch's projected position.
func matchScaled(patch, screen gocv.Mat, sx, sy, px, py float64, margin int) (hit, bool) {
	scaled := gocv.NewMat()
	defer scaled.Close()
	w, h := int(float64(patch.Cols())*sx), int(float64(patch.Rows())*sy)
	if w < 4 || h < 4 {
		return hit{}, false
	}
	gocv.Resize(patch, &scaled, image.Point{X: w, Y: h}, 0, 0, gocv.InterpolationLinear)
	if scaled.Empty() {
		return hit{}, false
	}

	cx, cy := int(px*sx), int(py*sy)
	win := image.Rect(cx-margin, cy-margin, cx+w+margin, cy+h+margin).
		Intersect(image.Rect(0, 0, screen.Cols(), screen.Rows()))
	if win.Dx() <= w || win.Dy() <= h {
		return hit{}, false
	}

	search := screen.Region(win)
	defer search.Close()

	res := gocv.NewMat()
	defer res.Close()
	if err := gocv.MatchTemplate(search, scaled, &res, gocv.TmCcoeffNormed, gocv.NewMat()); err != nil {
		return hit{}, false
	}
	if res.Empty() {
		return hit{}, false
	}
	_, maxVal, _, maxLoc := gocv.MinMaxLoc(res)
	return hit{
		sx:   sx,
		sy:   sy,
		conf: float64(maxVal),
		cx:   win.Min.X + maxLoc.X + w/2,
		cy:   win.Min.Y + maxLoc.Y + h/2,
	}, true
}
