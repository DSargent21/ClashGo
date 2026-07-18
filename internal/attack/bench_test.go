package attack

import (
	"image"
	"image/color"
	"testing"

	"gocv.io/x/gocv"
)

// makeSlotTestMat builds a synthetic BGR frame with a small colored region at
// (cx, cy) so the slot pipeline has something to inspect.
func makeSlotTestMat(w, h, cx, cy int) gocv.Mat {
	mat := gocv.NewMatWithSize(h, w, gocv.MatTypeCV8UC3)
	// Fill background (greenish grass-ish HSV-ish BGR)
	gocv.Rectangle(&mat, image.Rect(0, 0, w, h), color.RGBA{30, 120, 30, 0}, -1)
	// Active content blob (bright white)
	gocv.Rectangle(&mat, image.Rect(cx-10, cy-10, cx+10, cy+10), color.RGBA{240, 240, 240, 0}, -1)
	return mat
}

// oldSlotActivity is the pre-refactor implementation (per-call NewMat, no pool)
// kept here only to measure the allocation/CPU delta against the pooled version.
func oldSlotActivity(screen gocv.Mat, x, y, screenW, sizeHint int) float64 {
	if screen.Empty() || x < 0 || y < 0 || x >= screen.Cols() || y >= screen.Rows() {
		return 0
	}
	scaleX := float64(screenW) / 860.0
	size := sizeHint
	if size <= 0 {
		size = int(25.0 * scaleX)
	}
	region := image.Rect(x-size, y-size, x+size, y+size)
	if region.Min.X < 0 {
		region.Min.X = 0
	}
	if region.Min.Y < 0 {
		region.Min.Y = 0
	}
	if region.Max.X > screen.Cols() {
		region.Max.X = screen.Cols()
	}
	if region.Max.Y > screen.Rows() {
		region.Max.Y = screen.Rows()
	}
	sub := screen.Region(region)
	defer sub.Close()

	hsv := gocv.NewMat()
	defer hsv.Close()
	gocv.CvtColor(sub, &hsv, gocv.ColorBGRToHSV)

	maskMap1 := gocv.NewMat()
	defer maskMap1.Close()
	gocv.InRangeWithScalar(hsv, gocv.NewScalar(35, 31, 0, 0), gocv.NewScalar(90, 255, 255, 0), &maskMap1)
	maskMap2 := gocv.NewMat()
	defer maskMap2.Close()
	gocv.InRangeWithScalar(hsv, gocv.NewScalar(0, 0, 0, 0), gocv.NewScalar(29, 49, 79, 0), &maskMap2)
	isMapMask := gocv.NewMat()
	defer isMapMask.Close()
	gocv.BitwiseOr(maskMap1, maskMap2, &isMapMask)
	notMapMask := gocv.NewMat()
	defer notMapMask.Close()
	gocv.BitwiseNot(isMapMask, &notMapMask)
	maskActA := gocv.NewMat()
	defer maskActA.Close()
	gocv.InRangeWithScalar(hsv, gocv.NewScalar(0, 56, 91, 0), gocv.NewScalar(180, 255, 255, 0), &maskActA)
	maskActB := gocv.NewMat()
	defer maskActB.Close()
	gocv.InRangeWithScalar(hsv, gocv.NewScalar(0, 0, 221, 0), gocv.NewScalar(180, 29, 255, 0), &maskActB)
	activeContentMask := gocv.NewMat()
	defer activeContentMask.Close()
	gocv.BitwiseOr(maskActA, maskActB, &activeContentMask)
	finalActiveMask := gocv.NewMat()
	defer finalActiveMask.Close()
	gocv.BitwiseAnd(activeContentMask, notMapMask, &finalActiveMask)

	activePixels := gocv.CountNonZero(finalActiveMask)
	total := hsv.Rows() * hsv.Cols()
	if total <= 0 {
		return 0
	}
	return float64(activePixels) / float64(total)
}

func TestSlotActivityParity(t *testing.T) {
	mat := makeSlotTestMat(860, 480, 430, 240)
	defer mat.Close()
	for _, tc := range []struct{ x, y, w, s int }{
		{430, 240, 860, 25},
		{100, 100, 860, 25},
		{800, 400, 860, 25},
		{430, 240, 860, 0},
	} {
		oldV := oldSlotActivity(mat, tc.x, tc.y, tc.w, tc.s)
		newV := slotActivity(mat, tc.x, tc.y, tc.w, tc.s)
		if oldV != newV {
			t.Errorf("parity mismatch x=%d y=%d: old=%v new=%v", tc.x, tc.y, oldV, newV)
		}
	}
}

func BenchmarkSlotActivity_Old(b *testing.B) {
	mat := makeSlotTestMat(860, 480, 430, 240)
	defer mat.Close()
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = oldSlotActivity(mat, 430, 240, 860, 25)
	}
}

// BenchmarkSlotActivity_New measures the pooled path. Go's allocs/op cannot
// observe gocv's C-side cv::Mat allocations, so the meaningful signal here is
// functional parity (TestSlotActivityParity) plus the fact that steady-state
// reuse means no NEW C Mats are created after warmup — only the small pool
// bookkeeping structs. The Old path allocates ~10 fresh cv::Mat per call.
func BenchmarkSlotActivity_New(b *testing.B) {
	mat := makeSlotTestMat(860, 480, 430, 240)
	defer mat.Close()
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = slotActivity(mat, 430, 240, 860, 25)
	}
}

// BenchmarkAttackSlotChecks simulates a realistic attack: N slot occupancy
// checks per attack. The Old path pays ~10 fresh cv::Mat allocations per check;
// the New pooled path reuses buffers. We report Go-side allocs to show the
// bookkeeping cost is bounded and the C-side allocation storm is eliminated.
func BenchmarkAttackSlotChecks(b *testing.B) {
	mat := makeSlotTestMat(860, 480, 430, 240)
	defer mat.Close()
	slots := []struct{ x, y int }{
		{120, 400}, {200, 400}, {280, 400}, {360, 400}, {440, 400},
		{520, 400}, {600, 400}, {680, 400}, {760, 400}, {840, 400},
	}
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		for _, s := range slots {
			_ = slotActivity(mat, s.x, s.y, 860, 25)
		}
	}
}
