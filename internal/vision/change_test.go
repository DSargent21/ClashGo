package vision

import (
	"image"
	"runtime"
	"testing"

	"gocv.io/x/gocv"
)

// uniformMat builds a solid BGR frame.
func uniformMat(w, h int, b, g, r uint8) gocv.Mat {
	m := gocv.NewMatWithSize(h, w, gocv.MatTypeCV8UC3)
	m.SetTo(gocv.NewScalar(float64(b), float64(g), float64(r), 0))
	return m
}

// paint fills a rectangle with a colour, one pixel at a time (tests are allowed
// to be slow; the point is to know exactly what changed).
func paint(m gocv.Mat, rect image.Rectangle, b, g, r uint8) {
	for y := rect.Min.Y; y < rect.Max.Y; y++ {
		for x := rect.Min.X; x < rect.Max.X; x++ {
			m.SetUCharAt(y, x*3, b)
			m.SetUCharAt(y, x*3+1, g)
			m.SetUCharAt(y, x*3+2, r)
		}
	}
}

func TestSignatureScalesToAThumbnail(t *testing.T) {
	cases := []struct {
		w, h         int
		wantW, wantH int
	}{
		{1280, 720, 160, 90},
		{860, 732, 105, 90},
		{720, 1280, 50, 90},
	}
	for _, tc := range cases {
		m := uniformMat(tc.w, tc.h, 10, 20, 30)
		sig := Signature(m)
		if sig.Empty() {
			t.Fatalf("%dx%d produced an empty signature", tc.w, tc.h)
		}
		if sig.Cols() != tc.wantW || sig.Rows() != tc.wantH {
			t.Errorf("%dx%d signature = %dx%d, want %dx%d", tc.w, tc.h, sig.Cols(), sig.Rows(), tc.wantW, tc.wantH)
		}
		PutMat(sig)
		m.Close()
	}
}

// A frame too small to compare must produce an empty signature, which
// ChangedPercent then reports as changed — never as "stable".
func TestSignatureRejectsUnusableFrames(t *testing.T) {
	for _, m := range []gocv.Mat{gocv.NewMat(), uniformMat(1, 1, 0, 0, 0)} {
		sig := Signature(m)
		if !sig.Empty() {
			t.Errorf("signature of an unusable frame is %dx%d, want empty", sig.Cols(), sig.Rows())
		}
		if got := ChangedPercent(sig, sig, DefaultChangeDelta); got != 100 {
			t.Errorf("ChangedPercent on an empty signature = %v, want 100 (fail safe)", got)
		}
		PutMat(sig)
		m.Close()
	}
}

func TestChangedPercentZeroForIdenticalFrames(t *testing.T) {
	a := uniformMat(1280, 720, 30, 60, 90)
	b := uniformMat(1280, 720, 30, 60, 90)
	defer a.Close()
	defer b.Close()

	sa, sb := Signature(a), Signature(b)
	defer PutMat(sa)
	defer PutMat(sb)

	if got := ChangedPercent(sa, sb, DefaultChangeDelta); got != 0 {
		t.Errorf("identical frames report %v %% changed, want 0", got)
	}
}

// The percentage has to be an area measure, because the gate's threshold is one:
// a quarter of the frame changing must read as roughly 25 %.
func TestChangedPercentMeasuresChangedArea(t *testing.T) {
	a := uniformMat(1000, 1000, 0, 0, 0)
	b := uniformMat(1000, 1000, 0, 0, 0)
	defer a.Close()
	defer b.Close()
	paint(b, image.Rect(0, 0, 500, 500), 255, 255, 255) // one quarter

	sa, sb := Signature(a), Signature(b)
	defer PutMat(sa)
	defer PutMat(sb)

	got := ChangedPercent(sa, sb, DefaultChangeDelta)
	if got < 15 || got > 35 {
		t.Errorf("a quarter of the frame changed but the signature reports %v %%", got)
	}
}

// Sub-threshold movement is the animation floor the whole optimization rests on:
// it must not count as a change.
func TestChangedPercentIgnoresSubThresholdNoise(t *testing.T) {
	a := uniformMat(640, 480, 100, 100, 100)
	b := uniformMat(640, 480, 105, 105, 105) // below DefaultChangeDelta (12)
	defer a.Close()
	defer b.Close()

	sa, sb := Signature(a), Signature(b)
	defer PutMat(sa)
	defer PutMat(sb)

	if got := ChangedPercent(sa, sb, DefaultChangeDelta); got != 0 {
		t.Errorf("a 5-unit shift reads as %v %% changed, want 0 (below the %d noise floor)", got, DefaultChangeDelta)
	}
}

// Anything the comparison cannot do reports 100 (changed), so a caller gating
// work on "under threshold" can only ever do more work than it needed.
func TestChangedPercentFailsSafe(t *testing.T) {
	// Different aspect ratios, so the thumbnails really are different shapes
	// (320x240 and 640x480 would both reduce to 120x90 and compare "fine").
	small := Signature(uniformMat(320, 240, 0, 0, 0))
	defer PutMat(small)
	large := Signature(uniformMat(1280, 720, 0, 0, 0))
	defer PutMat(large)
	if small.Cols() == large.Cols() && small.Rows() == large.Rows() {
		t.Fatalf("test setup: both frames produced a %dx%d signature", small.Cols(), small.Rows())
	}

	if got := ChangedPercent(small, large, DefaultChangeDelta); got != 100 {
		t.Errorf("differently shaped signatures report %v %%, want 100", got)
	}
	if got := ChangedPercent(gocv.NewMat(), large, DefaultChangeDelta); got != 100 {
		t.Errorf("an empty signature reports %v %%, want 100", got)
	}
}

// Both functions run on every captured frame, so neither may allocate a buffer:
// the Mats come from the shared pool, and a per-frame thumbnail (43 KB here) or
// frame-sized temporary would be exactly the churn this path exists to avoid.
//
// The assertion is on BYTES, not object count: gocv's C bindings do mint a
// couple of small headers per call, and the property that matters is that no
// pixel buffer is allocated. A regression that stopped pooling shows up here as
// more than a kilobyte per call.
func TestChangeCompareDoesNotAllocateBuffers(t *testing.T) {
	a := uniformMat(1280, 720, 20, 20, 20)
	b := uniformMat(1280, 720, 20, 20, 20)
	defer a.Close()
	defer b.Close()

	sa, sb := Signature(a), Signature(b)
	defer PutMat(sa)
	defer PutMat(sb)

	// Warm the pools so the measurement is of steady state, not first use.
	ChangedPercent(sa, sb, DefaultChangeDelta)
	PutMat(Signature(a))

	const perCallBudget = 2048

	if perCall := allocBytesPerCall(500, func() { ChangedPercent(sa, sb, DefaultChangeDelta) }); perCall > perCallBudget {
		t.Errorf("ChangedPercent allocates %.0f bytes per call, want under %d (thumbnail is %d bytes)",
			perCall, perCallBudget, sb.Rows()*sb.Cols()*3)
	}

	if perCall := allocBytesPerCall(200, func() { PutMat(Signature(a)) }); perCall > perCallBudget {
		t.Errorf("Signature allocates %.0f bytes per call, want under %d", perCall, perCallBudget)
	}
}

// allocBytesPerCall returns the bytes the runtime attributed to f, per call.
// TotalAlloc is cumulative and unaffected by collection, which is what makes it
// the right counter for "did this allocate a buffer".
func allocBytesPerCall(n int, f func()) float64 {
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	for i := 0; i < n; i++ {
		f()
	}
	runtime.ReadMemStats(&after)
	return float64(after.TotalAlloc-before.TotalAlloc) / float64(n)
}
