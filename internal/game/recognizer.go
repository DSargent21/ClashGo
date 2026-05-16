package game

import (
	"fmt"
	"image"
	"math"
	"sync"

	"gocv.io/x/gocv"
)

type Recognizer struct {
	pool  sync.Pool
	width int
	height int
}

func NewRecognizer() *Recognizer {
	return &Recognizer{
		pool: sync.Pool{
			New: func() any {
				return make([]uint8, 1024*1024*4)
			},
		},
	}
}

func (r *Recognizer) ExtractPixels(mat gocv.Mat) []uint8 {
	n := mat.Total() * 4
	buf := r.pool.Get().([]uint8)
	if cap(buf) < n {
		buf = make([]uint8, n)
	}
	buf = buf[:n]

	for row := 0; row < mat.Rows(); row++ {
		for col := 0; col < mat.Cols(); col++ {
			i := (row*mat.Cols()+col)*3
			if i+2 < n {
				buf[i+2] = mat.GetUCharAt(row, col*3+2)
				buf[i+1] = mat.GetUCharAt(row, col*3+1)
				buf[i] = mat.GetUCharAt(row, col*3)
			}
		}
	}
	return buf
}

func (r *Recognizer) ReleasePixels(buf []uint8) {
	r.pool.Put(buf[:0])
}

func (r *Recognizer) GetPixel(data []uint8, stride int, x, y int) (R, G, B uint8) {
	i := y*stride + x*3
	if i+2 >= len(data) {
		return 0, 0, 0
	}
	return data[i+2], data[i+1], data[i]
}

func (r *Recognizer) CheckPixel(data []uint8, stride int, x, y int, expR, expG, expB uint8, tol int) bool {
	r2, g2, b2 := r.GetPixel(data, stride, x, y)
	return absDiff(int(r2), int(expR)) <= tol &&
		absDiff(int(g2), int(expG)) <= tol &&
		absDiff(int(b2), int(expB)) <= tol
}

func (r *Recognizer) RegionMeanColor(mat gocv.Mat, rgn image.Rectangle) (R, G, B uint8, count int) {
	if rgn.Min.X < 0 || rgn.Min.Y < 0 || rgn.Max.X > mat.Cols() || rgn.Max.Y > mat.Rows() {
		return 0, 0, 0, 0
	}

	var sumR, sumG, sumB int64
	count = rgn.Dx() * rgn.Dy()
	if count <= 0 {
		return 0, 0, 0, 0
	}

	region := mat.Region(rgn)
	defer region.Close()

	for row := 0; row < region.Rows(); row++ {
		for col := 0; col < region.Cols(); col++ {
			b := region.GetUCharAt(row, col*3)
			g := region.GetUCharAt(row, col*3+1)
			r_ := region.GetUCharAt(row, col*3+2)
			sumB += int64(b)
			sumG += int64(g)
			sumR += int64(r_)
		}
	}

	return uint8(sumR / int64(count)),
		uint8(sumG / int64(count)),
		uint8(sumB / int64(count)),
		count
}

func (r *Recognizer) ScanForColor(mat gocv.Mat, rgn image.Rectangle, expR, expG, expB uint8, tol int) []image.Point {
	var matches []image.Point

	if rgn.Min.X < 0 || rgn.Min.Y < 0 || rgn.Max.X > mat.Cols() || rgn.Max.Y > mat.Rows() {
		return matches
	}

	region := mat.Region(rgn)
	defer region.Close()

	tolF := float64(tol)
	dr := float64(expR)
	dg := float64(expG)
	db := float64(expB)

	for row := 0; row < region.Rows(); row++ {
		for col := 0; col < region.Cols(); col++ {
			b := region.GetUCharAt(row, col*3)
			g := region.GetUCharAt(row, col*3+1)
			r_ := region.GetUCharAt(row, col*3+2)

			dist := math.Sqrt(sqDiff(float64(r_), dr) + sqDiff(float64(g), dg) + sqDiff(float64(b), db))
			if dist <= tolF {
				matches = append(matches, image.Pt(rgn.Min.X+col, rgn.Min.Y+row))
			}
		}
	}

	return matches
}

func (r *Recognizer) FindButtonLikeRegions(mat gocv.Mat) []Rectangle {
	var regions []Rectangle

	w, h := mat.Cols(), mat.Rows()

	region := mat.Region(image.Rect(0, 0, w, int(float64(h)*0.90)))
	defer region.Close()

	gray := gocv.NewMat()
	defer gray.Close()
	gocv.CvtColor(region, &gray, gocv.ColorBGRToGray)

	edges := gocv.NewMat()
	defer edges.Close()
	gocv.Canny(gray, &edges, 50, 150)

	contours := gocv.FindContours(edges, gocv.RetrievalExternal, gocv.ChainApproxSimple)
	defer contours.Close()

	for i := 0; i < contours.Size(); i++ {
		rect := gocv.BoundingRect(contours.At(i))
		area := float64(rect.Dx() * rect.Dy())

		if area < 400 || area > 80000 {
			continue
		}

		aspect := float64(rect.Dx()) / float64(rect.Dy())
		if aspect < 0.5 || aspect > 5.0 {
			continue
		}

		regions = append(regions, Rectangle{
			X1: rect.Min.X, Y1: rect.Min.Y,
			X2: rect.Max.X, Y2: rect.Max.Y,
		})
	}

	return regions
}

func (r *Recognizer) ScreenHash(mat gocv.Mat) uint64 {
	if mat.Empty() {
		return 0
	}

	var hash uint64

	for y := 0; y < mat.Rows(); y += 8 {
		for x := 0; x < mat.Cols(); x += 8 {
			b := mat.GetUCharAt(y, x*3)
			g := mat.GetUCharAt(y, x*3+1)
			r := mat.GetUCharAt(y, x*3+2)
			hash ^= uint64(b)<<56 | uint64(g)<<48 | uint64(r)<<40 | uint64(x)<<32 | uint64(y)
		}
	}
	return hash
}

func (r *Recognizer) HistogramSimilarity(a, b gocv.Mat) float64 {
	return 0
}

func (r *Recognizer) DetectBlur(mat gocv.Mat) bool {
	if mat.Empty() {
		return true
	}

	gray := gocv.NewMat()
	defer gray.Close()
	gocv.CvtColor(mat, &gray, gocv.ColorBGRToGray)

	laplacian := gocv.NewMat()
	defer laplacian.Close()
	gocv.Laplacian(gray, &laplacian, gocv.MatTypeCV64F, 3, 1.0, 0, gocv.BorderDefault)

	var total float64
	for i := 0; i < laplacian.Rows(); i++ {
		for j := 0; j < laplacian.Cols(); j++ {
			total += math.Abs(laplacian.GetDoubleAt(i, j))
		}
	}
	pixels := float64(gray.Rows() * gray.Cols())
	return (total / pixels) < 100
}

func (r *Recognizer) UniquePixelCount(mat gocv.Mat, rgn image.Rectangle) int {
	if rgn.Min.X < 0 || rgn.Min.Y < 0 || rgn.Max.X > mat.Cols() || rgn.Max.Y > mat.Rows() {
		return 0
	}

	seen := make(map[string]bool)
	region := mat.Region(rgn)
	defer region.Close()

	for row := 0; row < region.Rows(); row++ {
		for col := 0; col < region.Cols(); col++ {
			b := region.GetUCharAt(row, col*3)
			g := region.GetUCharAt(row, col*3+1)
			r_ := region.GetUCharAt(row, col*3+2)
			key := fmt.Sprintf("%d,%d,%d", r_, g, b)
			seen[key] = true
		}
	}

	return len(seen)
}

func sqDiff(a, b float64) float64 {
	d := a - b
	return d * d
}