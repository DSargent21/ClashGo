package vision

import (
	"image"
	"strings"
	"testing"
)

// solid builds a uniform frame in BGR byte order.
func solid(w, h int, r, g, b byte) Frame {
	f := Frame{W: w, H: h, Stride: w * 3, Pix: make([]byte, w*3*h)}
	for i := 0; i < len(f.Pix); i += 3 {
		f.Pix[i], f.Pix[i+1], f.Pix[i+2] = b, g, r
	}
	return f
}

// fill paints a rectangle into a COPY of a frame. Frame.Pix is a slice, so
// painting in place would silently mutate the caller's frame and make two
// "different" fixtures byte-identical.
func fill(f Frame, r image.Rectangle, red, green, blue byte) Frame {
	f = f.Copy()
	r = r.Intersect(image.Rect(0, 0, f.W, f.H))
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			o := y*f.Stride + x*3
			f.Pix[o], f.Pix[o+1], f.Pix[o+2] = blue, green, red
		}
	}
	return f
}

func TestFrameAt_IsBGR(t *testing.T) {
	f := solid(4, 4, 10, 20, 30)
	r, g, b := f.At(1, 2)
	if r != 10 || g != 20 || b != 30 {
		t.Fatalf("At = (%d,%d,%d), want (10,20,30)", r, g, b)
	}
	// Out-of-bounds reads must report black, not panic: callers probe mapped
	// coordinates that a smaller screen can push off-frame.
	if r, g, b := f.At(-1, 0); r != 0 || g != 0 || b != 0 {
		t.Fatalf("At(-1,0) = (%d,%d,%d), want black", r, g, b)
	}
	if r, g, b := f.At(99, 99); r != 0 || g != 0 || b != 0 {
		t.Fatalf("At(99,99) = (%d,%d,%d), want black", r, g, b)
	}
}

func TestNewGrid_FitsRegionAt720p(t *testing.T) {
	g := NewGrid(image.Rect(0, 0, 1280, 720), RenderOptions{Cols: 120})
	if g.CellW != 11 || g.CellH != 22 {
		t.Fatalf("cell = %dx%d, want 11x22", g.CellW, g.CellH)
	}
	if g.Cols != 116 || g.Rows != 32 {
		t.Fatalf("grid = %dx%d, want 116x32", g.Cols, g.Rows)
	}
	if g.Truncated {
		t.Fatal("grid unexpectedly truncated")
	}
	if got := g.RulerY(1); got != 22 {
		t.Fatalf("RulerY(1) = %d, want 22", got)
	}
	if got := g.RulerX(2); got != 22 {
		t.Fatalf("RulerX(2) = %d, want 22", got)
	}
	if r := g.CellRect(0, 0); r != image.Rect(0, 0, 11, 22) {
		t.Fatalf("CellRect = %v, want 0,0-11,22", r)
	}
	// The grid must not claim to cover pixels it does not draw.
	if g.Cols*g.CellW > g.Rect.Dx() || g.Rows*g.CellH > g.Rect.Dy() {
		t.Fatal("grid covers more than the region")
	}
}

func TestNewGrid_CellZooms(t *testing.T) {
	// Cell=1 is the finest a text render can go: one source pixel per column.
	g := NewGrid(image.Rect(100, 50, 300, 150), RenderOptions{Cell: 1})
	if g.CellW != 1 || g.CellH != 2 {
		t.Fatalf("cell = %dx%d, want 1x2", g.CellW, g.CellH)
	}
	if g.Cols != 200 || g.Rows != 50 {
		t.Fatalf("grid = %dx%d, want 200x50", g.Cols, g.Rows)
	}
	if g.RulerX(0) != 100 || g.RulerY(0) != 50 {
		t.Fatalf("origin = (%d,%d), want (100,50)", g.RulerX(0), g.RulerY(0))
	}
}

func TestNewGrid_TruncationIsReported(t *testing.T) {
	g := NewGrid(image.Rect(0, 0, 1280, 720), RenderOptions{Cell: 1})
	if g.Cols != MaxCols || g.Rows != MaxRows {
		t.Fatalf("grid = %dx%d, want caps %dx%d", g.Cols, g.Rows, MaxCols, MaxRows)
	}
	if !g.Truncated {
		t.Fatal("a capped grid must report Truncated")
	}
	if !strings.Contains(g.Describe(), "TRUNCATED") {
		t.Fatalf("Describe = %q, want a truncation warning", g.Describe())
	}
}

func TestRender_RulerLinesAndInk(t *testing.T) {
	f := solid(200, 100, 0, 0, 0)
	f = fill(f, image.Rect(40, 40, 60, 60), 255, 255, 255) // a bright patch
	out, g := Render(f, RenderOptions{Cols: 100, Ruler: true})
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != g.Rows+2 {
		t.Fatalf("render has %d lines, want %d rows + ruler + separator", len(lines), g.Rows)
	}
	if !strings.HasPrefix(lines[1], "     +") {
		t.Fatalf("separator line = %q, want the ruler underline", lines[1])
	}
	if !strings.Contains(lines[0], "100") {
		t.Fatalf("ruler header = %q, want a 100 tick", lines[0])
	}
	if !strings.Contains(out, "@") {
		t.Fatal("a white patch must render as ink")
	}
	// The bright patch must appear at the cell the ruler says it does. Each
	// body line is a 6-character gutter ("%4d |") then one rune per column.
	const gutter = 6
	row := 40 / g.CellH
	col := 40 / g.CellW
	bodyLines := strings.Split(strings.SplitN(out, "\n", 3)[2], "\n")
	runes := []rune(bodyLines[row])
	if len(runes) <= gutter+col || runes[gutter+col] != '@' {
		t.Fatalf("cell (%d,%d) is not '@' (white patch):\n%s", col, row, bodyLines[row])
	}
	if strings.Contains(string(runes[:gutter+col]), "@") {
		t.Fatalf("ink leaked left of the patch:\n%s", bodyLines[row])
	}
}

func TestRender_MarksDrawBorders(t *testing.T) {
	f := solid(200, 100, 0, 0, 0)
	marks := []Mark{MarkRect(image.Rect(20, 20, 80, 60), '7', "button")}
	out, _ := Render(f, RenderOptions{Cols: 100, Ruler: true, Marks: marks})
	if strings.Count(out, "7") < 4 {
		t.Fatalf("mark border missing from render:\n%s", out)
	}
	if legend := MarkLegend(marks); !strings.Contains(legend, "button") ||
		!strings.Contains(legend, "20,20") {
		t.Fatalf("legend = %q, want the box and its text", legend)
	}
}

func TestDiff_FindsBoxAndPercent(t *testing.T) {
	a := solid(100, 100, 0, 0, 0)
	b := fill(a, image.Rect(10, 20, 20, 30), 255, 255, 255)
	res := Diff(a, b, DefaultDiffThreshold)
	if !res.Comparable {
		t.Fatal("same-size frames must be comparable")
	}
	if res.Changed != 100 {
		t.Fatalf("changed = %d, want 100", res.Changed)
	}
	if res.Pct != 1 {
		t.Fatalf("pct = %v, want 1", res.Pct)
	}
	if res.BBox != image.Rect(10, 20, 20, 30) {
		t.Fatalf("bbox = %v, want 10,20-20,30", res.BBox)
	}
	if res.MaxDelta != 255 {
		t.Fatalf("max delta = %d, want 255", res.MaxDelta)
	}
}

func TestDiff_ThresholdSuppressesNoise(t *testing.T) {
	a := solid(50, 50, 100, 100, 100)
	b := solid(50, 50, 105, 105, 105) // below the default threshold
	res := Diff(a, b, DefaultDiffThreshold)
	if res.Changed != 0 {
		t.Fatalf("changed = %d, want 0 for sub-threshold noise", res.Changed)
	}
	if !strings.Contains(res.Describe(), "identical") {
		t.Fatalf("Describe = %q, want an identical verdict", res.Describe())
	}
	if res.MaxDelta != 5 {
		t.Fatalf("max delta = %d, want 5", res.MaxDelta)
	}
}

func TestDiff_MismatchedSizesAreNotComparable(t *testing.T) {
	res := Diff(solid(10, 10, 0, 0, 0), solid(20, 10, 0, 0, 0), 0)
	if res.Comparable {
		t.Fatal("different dimensions must not compare")
	}
	if !strings.Contains(res.Describe(), "not comparable") {
		t.Fatalf("Describe = %q", res.Describe())
	}
}

func TestChangeMap_UsesTheSameGridAsRender(t *testing.T) {
	a := solid(200, 100, 0, 0, 0)
	b := fill(a, image.Rect(40, 40, 80, 60), 255, 255, 255)
	opts := RenderOptions{Cols: 100, Ruler: true}
	out, g, res := ChangeMap(a, b, opts, DefaultDiffThreshold)
	_, rendered := Render(b, opts)
	if g.Cols != rendered.Cols || g.Rows != rendered.Rows {
		t.Fatalf("change map grid %dx%d != render grid %dx%d", g.Cols, g.Rows, rendered.Cols, rendered.Rows)
	}
	if !res.Comparable || res.Changed == 0 {
		t.Fatalf("expected a change, got %+v", res)
	}
	if !strings.Contains(out, "#") {
		t.Fatalf("change map has no '#':\n%s", out)
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	row := 2 + 40/g.CellH
	if !strings.ContainsRune(lines[row], '#') {
		t.Fatalf("no change marked on the row the changed pixels live in:\n%s", lines[row])
	}
}

func TestParseRect(t *testing.T) {
	r, err := ParseRect("10,20,30,40", 1280, 720)
	if err != nil || r != image.Rect(10, 20, 40, 60) {
		t.Fatalf("ParseRect = %v, %v", r, err)
	}
	if r, err := ParseRect("", 1280, 720); err != nil || r != image.Rect(0, 0, 1280, 720) {
		t.Fatalf("empty spec = %v, %v; want the whole frame", r, err)
	}
	if r, err := ParseRect("1200,700,200,100", 1280, 720); err != nil || r != image.Rect(1200, 700, 1280, 720) {
		t.Fatalf("clamped spec = %v, %v", r, err)
	}
	for _, bad := range []string{"1,2,3", "a,b,c,d", "0,0,0,10", "5000,5000,10,10"} {
		if _, err := ParseRect(bad, 1280, 720); err == nil {
			t.Fatalf("ParseRect(%q) accepted a bad spec", bad)
		}
	}
}

func TestHueClass_NamesTheObviousColours(t *testing.T) {
	cases := []struct {
		r, g, b int
		want    byte
	}{
		{230, 40, 40, 'R'},
		{240, 180, 40, 'O'},
		{240, 240, 60, 'Y'},
		{60, 220, 80, 'G'},
		{60, 220, 230, 'C'},
		{60, 90, 230, 'B'},
		{220, 60, 220, 'P'},
	}
	for _, c := range cases {
		got, ok := HueClass(c.r, c.g, c.b)
		if !ok || got != c.want {
			t.Errorf("HueClass(%d,%d,%d) = %c,%v; want %c", c.r, c.g, c.b, got, ok, c.want)
		}
	}
	// Greys must not be named, so the brightness ramp handles them.
	if _, ok := HueClass(90, 92, 91); ok {
		t.Error("a grey pixel must not get a colour name")
	}
}
