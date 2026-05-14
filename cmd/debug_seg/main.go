package main

import (
	"fmt"
	"image"
	"os"
	"path/filepath"
	"sort"

	"gocv.io/x/gocv"
)

const (
	refWidth  = 860
	refHeight = 732
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	screen := gocv.IMRead("assets/captures/scout.png", gocv.IMReadColor)
	if screen.Empty() {
		return fmt.Errorf("failed to load")
	}
	defer screen.Close()

	phyW, phyH := screen.Cols(), screen.Rows()
	scaleX := float64(phyW) / float64(refWidth)
	scaleY := float64(phyH) / float64(refHeight)

	type testCase struct {
		name   string
		x1, y1, x2, y2 int
	}

	tests := []testCase{
		{"Gold",   15, 88,  300, 110},
		{"Elixir", 15, 114, 300, 136},
		{"DE",     15, 140, 300, 162},
	}

	outDir := "assets/debug_seg"
	os.MkdirAll(outDir, 0755)

	for _, tc := range tests {
		r := image.Rect(
			int(float64(tc.x1)*scaleX),
			int(float64(tc.y1)*scaleY),
			int(float64(tc.x2)*scaleX),
			int(float64(tc.y2)*scaleY),
		)
		r = clamp(r, phyW, phyH)

		region := screen.Region(r)
		gocv.IMWrite(filepath.Join(outDir, tc.name+"_color.png"), region)

		gray := gocv.NewMat()
		gocv.CvtColor(region, &gray, gocv.ColorBGRToGray)
		region.Close()

		for _, threshVal := range []int{130, 140, 150, 160, 170} {
			for _, kSz := range []int{1, 2, 3} {
				thresh := gocv.NewMat()
				gocv.Threshold(gray, &thresh, float32(threshVal), 255, gocv.ThresholdBinary)

				if kSz > 1 {
					kernel := gocv.GetStructuringElement(gocv.MorphRect, image.Pt(kSz, kSz))
					gocv.MorphologyEx(thresh, &thresh, gocv.MorphClose, kernel)
					kernel.Close()
				}

				gocv.IMWrite(filepath.Join(outDir, fmt.Sprintf("%s_t%d_k%d.png", tc.name, threshVal, kSz)), thresh)

				segs := segmentProjection(thresh, r.Dy())
				sort.Slice(segs, func(i, j int) bool {
					return segs[i].Min.X < segs[j].Min.X
				})

				var kept int
				for _, s := range segs {
					digRoi := thresh.Region(s)
					if countWhitePixels(digRoi) >= r.Dy() {
						kept++
					}
					digRoi.Close()
				}

				fmt.Printf("%s t=%d k=%d: %d segments (%d kept)\n", tc.name, threshVal, kSz, len(segs), kept)

				thresh.Close()
			}
		}
		gray.Close()
	}

	return nil
}

func segmentProjection(thresh gocv.Mat, charH int) []image.Rectangle {
	counts := make([]int, thresh.Cols())
	for x := 0; x < thresh.Cols(); x++ {
		for y := 0; y < thresh.Rows(); y++ {
			if thresh.GetUCharAt(y, x) > 128 {
				counts[x]++
			}
		}
	}

	minCount := charH / 4
	if minCount < 3 {
		minCount = 3
	}
	marked := make([]bool, thresh.Cols())
	for x := 0; x < thresh.Cols(); x++ {
		marked[x] = counts[x] >= minCount
	}

	var segs []image.Rectangle
	i := 0
	for i < thresh.Cols() {
		if !marked[i] {
			i++
			continue
		}
		start := i
		for i < thresh.Cols() && marked[i] {
			i++
		}
		segs = append(segs, image.Rect(start, 0, i, thresh.Rows()))
	}

	if len(segs) == 0 {
		return segs
	}
	merged := []image.Rectangle{segs[0]}
	for _, s := range segs[1:] {
		last := &merged[len(merged)-1]
		if s.Min.X-last.Max.X < 3 {
			last.Max.X = s.Max.X
		} else {
			merged = append(merged, s)
		}
	}

	var filtered []image.Rectangle
	for _, s := range merged {
		if s.Dx() >= 1 {
			filtered = append(filtered, s)
		}
	}
	return filtered
}

func countWhitePixels(mat gocv.Mat) int {
	n := 0
	for y := 0; y < mat.Rows(); y++ {
		for x := 0; x < mat.Cols(); x++ {
			if mat.GetUCharAt(y, x) > 128 {
				n++
			}
		}
	}
	return n
}

func clamp(r image.Rectangle, w, h int) image.Rectangle {
	if r.Min.X < 0 { r.Min.X = 0 }
	if r.Min.Y < 0 { r.Min.Y = 0 }
	if r.Max.X > w { r.Max.X = w }
	if r.Max.Y > h { r.Max.Y = h }
	return r
}
