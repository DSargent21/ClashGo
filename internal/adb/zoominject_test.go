package adb

import (
	"archive/zip"
	"bytes"
	"strings"
	"testing"
)

// The embedded helper has to be a real dex jar, otherwise app_process starts,
// fails to find the class and the pinch silently degrades to the sendevent
// fallback — which is exactly the no-op this file exists to replace.
func TestZoomInjectJarIsADexArchive(t *testing.T) {
	zr, err := zip.NewReader(bytes.NewReader(zoomInjectJar), int64(len(zoomInjectJar)))
	if err != nil {
		t.Fatalf("embedded zoominject.jar is not a zip: %v", err)
	}
	for _, f := range zr.File {
		if f.Name == "classes.dex" {
			return
		}
	}
	t.Fatal("embedded zoominject.jar has no classes.dex")
}

func mid(a, b int) int { return (a + b) / 2 }

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

func TestPinchPointsAreSymmetricAboutTheCentre(t *testing.T) {
	const w, h = 1280, 720

	for _, zoomOut := range []bool{true, false} {
		g := PinchPoints(w, h, zoomOut)

		// The pinch must straddle the centre line, and both fingers share one
		// y: any vertical offset lets the camera read it as a pan.
		if g.Y != h/2 {
			t.Fatalf("zoomOut=%v: y=%d, want %d", zoomOut, g.Y, h/2)
		}
		// Symmetric about the centre: the midpoint never drifts, so the camera
		// cannot read the gesture as a two-finger pan.
		if got := mid(g.X1Start, g.X2Start); got != w/2 {
			t.Fatalf("zoomOut=%v: start midpoint %d, want %d", zoomOut, got, w/2)
		}
		if got := mid(g.X1End, g.X2End); got != w/2 {
			t.Fatalf("zoomOut=%v: end midpoint %d, want %d", zoomOut, got, w/2)
		}

		startSpan := abs(g.X1Start - g.X2Start)
		endSpan := abs(g.X1End - g.X2End)
		// Zoom out = camera pulls back = the fingers close together.
		if zoomOut && endSpan >= startSpan {
			t.Fatalf("zoom out must close the fingers: start span %d, end span %d", startSpan, endSpan)
		}
		if !zoomOut && endSpan <= startSpan {
			t.Fatalf("zoom in must open the fingers: start span %d, end span %d", startSpan, endSpan)
		}
	}
}

func TestPinchCommandCarriesTheFrameworkClasspath(t *testing.T) {
	g := PinchPoints(1280, 720, true)
	cmd := pinchCommand("/data/local/tmp/x.jar", g)

	if !strings.HasPrefix(cmd, "CLASSPATH=/data/local/tmp/x.jar app_process ") {
		t.Fatalf("command must run the helper through app_process: %q", cmd)
	}
	if !strings.Contains(cmd, zoomInjectClass+" pinch ") {
		t.Fatalf("command must call the pinch entry point: %q", cmd)
	}
	// 921/359 are the two landing x values, 615/665 the two landing ends.
	for _, x := range []string{"921", "359", "615", "665"} {
		if !strings.Contains(cmd, x) {
			t.Fatalf("command %q is missing coordinate %s", cmd, x)
		}
	}
	// One shared y per finger per frame.
	if got := strings.Count(cmd, " 360"); got != 4 {
		t.Fatalf("expected 4 shared-y coordinates, got %d in %q", got, cmd)
	}
}
