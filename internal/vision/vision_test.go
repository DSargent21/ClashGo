package vision

import (
	"image"
	"testing"

	"gocv.io/x/gocv"
)

func TestDrawOverlay(t *testing.T) {
	src := gocv.NewMatWithSize(100, 100, gocv.MatTypeCV8UC3)
	defer src.Close()

	rois := []image.Rectangle{image.Rect(10, 10, 50, 50)}
	matches := []Match{{Point: image.Pt(30, 30), Confidence: 0.85}}
	labels := []string{"test_lbl"}

	dst := DrawOverlay(src, rois, matches, labels)
	defer dst.Close()

	if dst.Empty() {
		t.Error("DrawOverlay returned empty Mat")
	}
	if dst.Rows() != src.Rows() || dst.Cols() != src.Cols() {
		t.Errorf("DrawOverlay dimensions mismatch: got %dx%d, want %dx%d", dst.Rows(), dst.Cols(), src.Rows(), src.Cols())
	}
}

func TestGenerateFilterPipelineImage(t *testing.T) {
	src := gocv.NewMatWithSize(100, 100, gocv.MatTypeCV8UC3)
	defer src.Close()

	gray := gocv.NewMatWithSize(100, 100, gocv.MatTypeCV8U)
	defer gray.Close()

	thresh := gocv.NewMatWithSize(100, 100, gocv.MatTypeCV8U)
	defer thresh.Close()

	canvas := GenerateFilterPipelineImage(src, gray, thresh)
	defer canvas.Close()

	if canvas.Empty() {
		t.Error("GenerateFilterPipelineImage returned empty Mat")
	}
	expectedWidth := src.Cols() * 3
	if canvas.Cols() != expectedWidth {
		t.Errorf("GenerateFilterPipelineImage width mismatch: got %d, want %d", canvas.Cols(), expectedWidth)
	}
}
