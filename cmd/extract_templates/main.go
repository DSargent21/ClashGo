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

type srcDef struct {
	path, name string
	x1, y1, x2, y2 int
	value string
}

type sample struct {
	mat   gocv.Mat
	width int
	white int
	src   string
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	sources := []srcDef{
		{"assets/captures/scout.png",  "Gold",    15, 88,  300, 110, "408303"},
		{"assets/captures/scout.png",  "Elixir",  15, 114, 300, 136, "570948"},
		{"assets/captures/scout.png",  "DE",      15, 140, 300, 162, "2170"},
		{"assets/captures/scout2.png", "DE",      15, 190, 300, 212, "6362"},
	}

	samplesByDigit := make(map[int][]sample)

	for _, s := range sources {
		screen := gocv.IMRead(s.path, gocv.IMReadColor)
		if screen.Empty() {
			fmt.Printf("WARN: cannot load %s\n", s.path)
			continue
		}

		phyW, phyH := screen.Cols(), screen.Rows()
		sx := float64(phyW) / float64(refWidth)
		sy := float64(phyH) / float64(refHeight)

		r := image.Rect(
			int(float64(s.x1)*sx), int(float64(s.y1)*sy),
			int(float64(s.x2)*sx), int(float64(s.y2)*sy),
		)
		r = clamp(r, phyW, phyH)

		region := screen.Region(r)
		gray := gocv.NewMat()
		gocv.CvtColor(region, &gray, gocv.ColorBGRToGray)
		region.Close()

		thresh := gocv.NewMat()
		gocv.Threshold(gray, &thresh, 130, 255, gocv.ThresholdBinary)
		gray.Close()

		kernel := gocv.GetStructuringElement(gocv.MorphRect, image.Pt(3, 3))
		gocv.MorphologyEx(thresh, &thresh, gocv.MorphClose, kernel)
		kernel.Close()

		segs := segmentProjection(thresh, r.Dy())
		sort.Slice(segs, func(i, j int) bool {
			return segs[i].Min.X < segs[j].Min.X
		})

		var kept []image.Rectangle
		for _, seg := range segs {
			if seg.Dx() < 3 || seg.Dx() > 55 {
				continue
			}
			digRoi := thresh.Region(seg)
			wp := countWhitePixels(digRoi)
			digRoi.Close()
			if wp >= r.Dy()/4 {
				kept = append(kept, seg)
			}
		}

		fmt.Printf("%s/%s: %d segs -> %d kept (expected %d)\n", s.path, s.name, len(segs), len(kept), len(s.value))

		if len(kept) < len(s.value) {
			fmt.Printf("  WARNING: not enough segments, got %d need %d\n", len(kept), len(s.value))
			thresh.Close()
			screen.Close()
			continue
		}

		for i := 0; i < len(s.value); i++ {
			seg := kept[i]
			digit := int(s.value[i] - '0')

			pad := 1
			x1 := max(0, seg.Min.X-pad)
			x2 := min(thresh.Cols(), seg.Max.X+pad)
			padded := image.Rect(x1, seg.Min.Y, x2, seg.Max.Y)

			digRoi := thresh.Region(padded)
			mat := digRoi.Clone()
			digRoi.Close()

			wp := countWhitePixels(mat)
			samplesByDigit[digit] = append(samplesByDigit[digit], sample{
				mat: mat, width: mat.Cols(), white: wp,
				src: fmt.Sprintf("%s/%s[%d]", s.path, s.name, i),
			})
			fmt.Printf("  seg[%d] -> digit %d  %dx%d  %d white\n", i, digit, mat.Cols(), mat.Rows(), wp)
		}

		thresh.Close()
		screen.Close()
	}

	// Save all templates to common dir
	outDir := "assets/templates"
	os.MkdirAll(outDir, 0755)

	fmt.Println("\n--- Saving templates ---")
	for d := 0; d <= 9; d++ {
		samples, ok := samplesByDigit[d]
		if !ok || len(samples) == 0 {
			fmt.Printf("  digit %d: MISSING\n", d)
			continue
		}

		// Pick narrowest with reasonable white pixel count
		sort.Slice(samples, func(i, j int) bool {
			return samples[i].width < samples[j].width
		})
		best := samples[0]
		for _, s := range samples {
			if s.width >= 8 && s.white > best.white {
				best = s
			}
		}

		path := filepath.Join(outDir, fmt.Sprintf("digit_%d.png", d))
		if gocv.IMWrite(path, best.mat) {
			fmt.Printf("  digit %d  (%dx%d, %d white) from %s\n", d, best.mat.Cols(), best.mat.Rows(), best.white, best.src)
		}
	}

	// Cleanup - close all mat copies
	for _, samples := range samplesByDigit {
		for _, s := range samples {
			s.mat.Close()
		}
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

func max(a, b int) int { if a > b { return a }; return b }
func min(a, b int) int { if a < b { return a }; return b }
