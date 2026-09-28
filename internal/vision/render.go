// Screenshot -> text rendering, built for a debugging loop where the reader
// is an agent that cannot decode a PNG.
//
// cmd/screendump already prints a coarse colour-letter map of a frame. That
// answers "roughly what is on screen"; it cannot answer "which button is lit",
// "did the popup close", "does the deploy line sit on the village or on the
// grass", because it has no coordinate frame and only one glyph of detail per
// cell. This file provides the primitives for a denser, ruler-annotated view:
//
//	Render      the frame as text on a Grid, one character per cell, with the
//	            screenshot pixel coordinates of every row and column labelled
//	            so a glyph can be converted straight back into a tap
//	RenderColor the same geometry in 24-bit ANSI half blocks (two pixels per
//	            character cell) for a human terminal
//	ChangeMap   what differs between two frames, drawn on the same Grid so the
//	            change map lines up with the frame render
//	Diff        the numbers behind ChangeMap: percent changed, bounding box,
//	            largest channel delta
//
// Everything here works on Frame, a plain BGR byte view of a gocv.Mat, so the
// renderers are byte arithmetic over a slice: no OpenCV calls, and testable
// without a screen.
package vision

import (
	"fmt"
	"image"
	"sort"
	"strconv"
	"strings"

	"gocv.io/x/gocv"
)

// DefaultCols is the render width when the caller does not choose one. At
// 1280x720 it yields ~32 text lines, which fits a terminal pane and still
// resolves UI elements tens of pixels wide.
const DefaultCols = 120

// Hard caps so a mistyped -cell cannot print megabytes of text.
const (
	MaxCols = 320
	MaxRows = 200
)

// DefaultDiffThreshold is the per-channel delta at which a pixel counts as
// changed. Above video-compression noise, below a real UI change.
const DefaultDiffThreshold = 12

// Ramp maps brightness (dark -> bright) onto characters, so bright game
// pixels — label text, button faces, health bars — come out as dense glyphs
// and dark scenery and panels fade to whitespace. The report prints the
// convention, but the short version is: ink where the game draws, blanks
// where it does not.
var Ramp = []byte(" .:-=+*#%@")

// Frame is a read-only BGR view of a screenshot in OpenCV's pixel layout:
// CV_8UC3, three bytes per pixel, Stride bytes per row (Stride >= W*3).
type Frame struct {
	W, H   int
	Stride int
	Pix    []byte
}

// FrameFromMat copies an OpenCV Mat into a Frame. Mat.ToBytes() returns the
// flattened pixel data, so the Frame owns its bytes and stays valid after the
// Mat is closed. A Mat whose row stride carries padding is only trusted when
// the flattened buffer is actually long enough for it; otherwise the frame is
// treated as contiguous, which is what every Mat this project creates is.
func FrameFromMat(m gocv.Mat) Frame {
	rows, cols := m.Rows(), m.Cols()
	if rows <= 0 || cols <= 0 {
		return Frame{}
	}
	pix := m.ToBytes()
	stride := cols * 3
	if s := m.Step(); s >= stride && len(pix) >= s*rows {
		stride = s
	}
	if len(pix) < stride*rows {
		return Frame{}
	}
	return Frame{W: cols, H: rows, Stride: stride, Pix: pix}
}

// Empty reports whether the frame holds no usable pixels.
func (f Frame) Empty() bool { return f.W <= 0 || f.H <= 0 || len(f.Pix) < f.Stride*f.H }

// At returns the colour at (x, y) in red, green, blue order. Out-of-bounds
// reads return black rather than panicking, because callers routinely probe
// mapped coordinates that a smaller screen pushed off-frame.
func (f Frame) At(x, y int) (r, g, b int) {
	if x < 0 || y < 0 || x >= f.W || y >= f.H || y*f.Stride+x*3+2 >= len(f.Pix) {
		return 0, 0, 0
	}
	o := y*f.Stride + x*3
	return int(f.Pix[o+2]), int(f.Pix[o+1]), int(f.Pix[o])
}

// Avg averages a rectangle of the frame, sampling at most `step` pixels apart
// so box-averaging a 10x20 cell stays cheap.
func (f Frame) Avg(r image.Rectangle, step int) (red, green, blue int) {
	r = r.Intersect(image.Rect(0, 0, f.W, f.H))
	if r.Empty() {
		return 0, 0, 0
	}
	if step < 1 {
		step = 1
	}
	var n int
	for y := r.Min.Y; y < r.Max.Y; y += step {
		for x := r.Min.X; x < r.Max.X; x += step {
			cr, cg, cb := f.At(x, y)
			red, green, blue = red+cr, green+cg, blue+cb
			n++
		}
	}
	if n == 0 {
		return 0, 0, 0
	}
	return red / n, green / n, blue / n
}

// Mark is an annotation drawn into the text render so the picture shows WHERE
// something was found, not merely that it was found. Either a rectangle (a
// detected element's bounds) or a single point (a planned tap).
type Mark struct {
	Rect  image.Rectangle
	Point image.Point
	Label rune
	Text  string
}

// MarkRect annotates a rectangle, drawing its border ring in the text render.
func MarkRect(r image.Rectangle, label rune, text string) Mark {
	return Mark{Rect: r, Label: label, Text: text}
}

// MarkPoint annotates a single pixel coordinate.
func MarkPoint(x, y int, label rune, text string) Mark {
	return Mark{Point: image.Pt(x, y), Label: label, Text: text}
}

// Bounds returns the pixels the mark covers.
func (m Mark) Bounds() image.Rectangle {
	if !m.Rect.Empty() {
		return m.Rect
	}
	return image.Rect(m.Point.X, m.Point.Y, m.Point.X+1, m.Point.Y+1)
}

// Describe renders one legend line: the label, where it is, and what it means.
func (m Mark) Describe() string {
	r := m.Bounds()
	txt := m.Text
	if txt == "" {
		txt = "point"
	}
	return fmt.Sprintf("%c %4d,%-4d %4dx%-4d %s", m.Label, r.Min.X, r.Min.Y, r.Dx(), r.Dy(), txt)
}

// MarkLegend renders every mark's description, one per line, so a reader can
// map the digits in the picture back to what was detected.
func MarkLegend(marks []Mark) string {
	if len(marks) == 0 {
		return ""
	}
	var sb strings.Builder
	for _, m := range marks {
		sb.WriteString(m.Describe())
		sb.WriteByte('\n')
	}
	return sb.String()
}

// RenderOptions controls the text render geometry and decoration.
type RenderOptions struct {
	// Rect is the region of the frame to render. The zero value renders the
	// whole frame.
	Rect image.Rectangle
	// Cols is the target width in characters; ignored when Cell is set or
	// when the region is too small to subdivide.
	Cols int
	// Cell is the number of source pixels covered by one character column.
	// A character row always covers twice that, which keeps the picture's
	// aspect ratio (terminal cells are about twice as tall as wide). Set it
	// to zoom: Cell=1 is one pixel per column, the finest a text render can
	// manage.
	Cell int
	// Marks are drawn into the render after the glyphs.
	Marks []Mark
	// Ruler labels the screenshot pixel coordinate of every rendered row and
	// of periodic columns. Leave it on: it is what turns the picture into
	// something whose coordinates can be tapped.
	Ruler bool
	// Color emits 24-bit ANSI half blocks, two source pixels per cell.
	Color bool
}

// Grid is the mapping between frame pixels and text cells. The same Grid is
// used for a frame render and its change map so the two line up column for
// column.
type Grid struct {
	Rect         image.Rectangle
	CellW, CellH int
	Cols, Rows   int
	// Truncated is set when MaxCols/MaxRows cut the region short, so the
	// render covers part of Rect rather than all of it.
	Truncated bool
}

// NewGrid resolves the cell size and character grid for a region.
func NewGrid(rect image.Rectangle, opts RenderOptions) Grid {
	if rect.Empty() {
		rect = image.Rect(0, 0, 1, 1)
	}
	cellW := opts.Cell
	if cellW <= 0 {
		cols := opts.Cols
		if cols <= 0 {
			cols = DefaultCols
		}
		cellW = (rect.Dx() + cols - 1) / cols
	}
	if cellW < 1 {
		cellW = 1
	}
	cellH := cellW * 2
	if cellH > rect.Dy() {
		// A region shorter than one character cell still gets one row.
		cellH = rect.Dy()
	}
	if cellH < 1 {
		cellH = 1
	}
	cols := rect.Dx() / cellW
	rows := rect.Dy() / cellH
	if cols < 1 {
		cols = 1
	}
	if rows < 1 {
		rows = 1
	}
	g := Grid{Rect: rect, CellW: cellW, CellH: cellH, Cols: cols, Rows: rows}
	if cols > MaxCols {
		g.Cols, g.Truncated = MaxCols, true
	}
	if rows > MaxRows {
		g.Rows, g.Truncated = MaxRows, true
	}
	return g
}

// CellRect is the frame region covered by one character cell.
func (g Grid) CellRect(col, row int) image.Rectangle {
	x0 := g.Rect.Min.X + col*g.CellW
	y0 := g.Rect.Min.Y + row*g.CellH
	return image.Rect(x0, y0, x0+g.CellW, y0+g.CellH)
}

// RulerX / RulerY give the screenshot pixel coordinate of a cell's top-left
// corner, which is what the ruler prints.
func (g Grid) RulerX(col int) int { return g.Rect.Min.X + col*g.CellW }
func (g Grid) RulerY(row int) int { return g.Rect.Min.Y + row*g.CellH }

// Describe summarises the geometry for a report header.
func (g Grid) Describe() string {
	s := fmt.Sprintf("%dx%d cells, 1 char = %dx%d px", g.Cols, g.Rows, g.CellW, g.CellH)
	if g.Truncated {
		s += " (TRUNCATED: region does not fit)"
	}
	return s
}

// HueClass names a saturated colour with one letter: R red, O orange/gold,
// Y yellow, G green, C cyan, B blue, P magenta/purple, W white. It returns
// false when the pixel is too grey to name, which is the caller's cue to fall
// back to a brightness ramp. Seven buckets is deliberately coarser than a
// colour wheel: the letters have to stay readable in a wall of text.
func HueClass(r, g, b int) (byte, bool) {
	max, min := r, r
	for _, v := range []int{g, b} {
		if v > max {
			max = v
		}
		if v < min {
			min = v
		}
	}
	if max < 60 || max-min < 45 {
		return 0, false
	}
	// A channel counts as "high" when it is close to the strongest one. The
	// window is generous (about 9% of the range) because these are compressed
	// screen captures: a cyan pixel often arrives as (60,220,230).
	hi := func(v int) bool { return v >= max-24 }
	switch {
	case hi(r) && hi(g) && hi(b):
		return 'W', true
	case hi(r) && hi(g):
		return 'Y', true
	case hi(r) && g*2 > max && b*2 < max:
		return 'O', true
	case hi(r) && hi(b):
		return 'P', true
	case hi(g) && hi(b):
		return 'C', true
	case hi(r):
		return 'R', true
	case hi(g):
		return 'G', true
	default:
		return 'B', true
	}
}

// cellGlyph picks the character for one averaged cell: the colour name when
// the cell is saturated enough to have one, otherwise a brightness ramp
// character.
func cellGlyph(r, g, b int) byte {
	if ch, ok := HueClass(r, g, b); ok {
		return ch
	}
	lum := (r*299 + g*587 + b*114) / 1000
	idx := lum * (len(Ramp) - 1) / 255
	return Ramp[idx]
}

// Render draws a frame as text. It returns the picture and the Grid it used,
// so a caller can convert a cell back to coordinates.
func Render(f Frame, opts RenderOptions) (string, Grid) {
	rect := opts.Rect
	if rect.Empty() {
		rect = image.Rect(0, 0, f.W, f.H)
	}
	g := NewGrid(rect, opts)
	cells := blank(g)
	if !f.Empty() {
		for row := 0; row < g.Rows; row++ {
			for col := 0; col < g.Cols; col++ {
				r, gr, b := f.Avg(g.CellRect(col, row), g.CellW/2+1)
				cells[row][col] = rune(cellGlyph(r, gr, b))
			}
		}
	}
	drawMarks(cells, g, opts.Marks)

	var sb strings.Builder
	if opts.Color {
		writeColorBody(&sb, f, g, cells)
		return sb.String(), g
	}
	writeRuler(&sb, g)
	for row := 0; row < g.Rows; row++ {
		fmt.Fprintf(&sb, "%4d |", g.RulerY(row))
		for col := 0; col < g.Cols; col++ {
			sb.WriteRune(cells[row][col])
		}
		sb.WriteByte('\n')
	}
	return sb.String(), g
}

// blank allocates an all-space cell grid.
func blank(g Grid) [][]rune {
	cells := make([][]rune, g.Rows)
	for i := range cells {
		cells[i] = make([]rune, g.Cols)
		for j := range cells[i] {
			cells[i][j] = ' '
		}
	}
	return cells
}

// drawMarks paints each mark's border into the cell grid. Borders are drawn
// rather than filled so the frame's own content stays visible inside a marked
// element — the question is usually "what is this thing", not "where is its
// middle".
func drawMarks(cells [][]rune, g Grid, marks []Mark) {
	for _, m := range marks {
		r := m.Bounds().Intersect(g.Rect)
		if r.Empty() {
			continue
		}
		c0 := (r.Min.X - g.Rect.Min.X) / g.CellW
		c1 := (r.Max.X - 1 - g.Rect.Min.X) / g.CellW
		r0 := (r.Min.Y - g.Rect.Min.Y) / g.CellH
		r1 := (r.Max.Y - 1 - g.Rect.Min.Y) / g.CellH
		for row := r0; row <= r1; row++ {
			if row < 0 || row >= g.Rows {
				continue
			}
			for col := c0; col <= c1; col++ {
				if col < 0 || col >= g.Cols {
					continue
				}
				if row == r0 || row == r1 || col == c0 || col == c1 {
					cells[row][col] = m.Label
				}
			}
		}
	}
}

// writeRuler emits the column coordinate header and its separator.
func writeRuler(sb *strings.Builder, g Grid) {
	const gutter = 5
	head := make([]byte, g.Cols)
	for i := range head {
		head[i] = ' '
	}
	step := niceStep(g.CellW * 6)
	for x := firstTick(g.Rect.Min.X, step); x < g.Rect.Max.X; x += step {
		col := (x - g.Rect.Min.X) / g.CellW
		label := strconv.Itoa(x)
		if col+len(label) <= len(head) {
			copy(head[col:], label)
		}
	}
	sb.WriteString(strings.Repeat(" ", gutter))
	sb.Write(head)
	sb.WriteByte('\n')
	sb.WriteString(strings.Repeat(" ", gutter))
	sb.WriteByte('+')
	for i := 0; i < g.Cols; i++ {
		sb.WriteByte('-')
	}
	sb.WriteByte('\n')
}

// niceStep rounds a raw tick spacing up to a human number (10/20/50/100/...),
// so ruler labels land on coordinates worth reading.
func niceStep(raw int) int {
	if raw < 10 {
		return 10
	}
	mult := 1
	for mult*10 <= raw {
		mult *= 10
	}
	for _, f := range []int{1, 2, 5, 10} {
		if mult*f >= raw {
			return mult * f
		}
	}
	return mult * 10
}

// firstTick returns the first multiple of step at or after x, so ticks read
// as round screenshot coordinates even on a cropped region.
func firstTick(x, step int) int {
	if step <= 0 {
		return x
	}
	if x < 0 {
		return 0
	}
	return ((x + step - 1) / step) * step
}

// writeColorBody emits the render as 24-bit ANSI half blocks: each character
// cell shows two source pixel rows (foreground = top, background = bottom),
// doubling the vertical resolution of the plain render. Marks keep their
// label characters so they survive the colouring.
func writeColorBody(sb *strings.Builder, f Frame, g Grid, cells [][]rune) {
	for row := 0; row < g.Rows; row++ {
		for col := 0; col < g.Cols; col++ {
			if cells[row][col] != ' ' {
				fmt.Fprintf(sb, "\x1b[0;97m%c", cells[row][col])
				continue
			}
			full := g.CellRect(col, row)
			top := image.Rect(full.Min.X, full.Min.Y, full.Max.X, full.Min.Y+full.Dy()/2)
			bot := image.Rect(full.Min.X, top.Max.Y, full.Max.X, full.Max.Y)
			tr, tg, tb := f.Avg(top, 1)
			br, bg, bb := f.Avg(bot, 1)
			fmt.Fprintf(sb, "\x1b[38;2;%d;%d;%dm\x1b[48;2;%d;%d;%dm\u2580", tr, tg, tb, br, bg, bb)
		}
		sb.WriteString("\x1b[0m\n")
	}
}

// DiffResult is the quantitative outcome of comparing two frames.
type DiffResult struct {
	Comparable bool
	W, H       int
	Changed    int
	Total      int
	Pct        float64
	BBox       image.Rectangle
	MaxDelta   int
}

// Describe renders the one-line summary a report prints.
func (d DiffResult) Describe() string {
	if !d.Comparable {
		return "frames not comparable (different dimensions)"
	}
	if d.Changed == 0 {
		return fmt.Sprintf("identical (%dx%d, %d pixels compared)", d.W, d.H, d.Total)
	}
	return fmt.Sprintf("%.2f%% changed (%d/%d px), bbox (%d,%d)-(%d,%d), max channel delta %d",
		d.Pct, d.Changed, d.Total, d.BBox.Min.X, d.BBox.Min.Y, d.BBox.Max.X, d.BBox.Max.Y, d.MaxDelta)
}

// Diff compares two frames pixel by pixel. A pixel counts as changed when any
// colour channel differs by more than threshold, which keeps video noise and
// gradient dithering out of the change percentage.
func Diff(a, b Frame, threshold int) DiffResult {
	if a.Empty() || b.Empty() || a.W != b.W || a.H != b.H {
		return DiffResult{Comparable: false, W: a.W, H: a.H}
	}
	if threshold < 0 {
		threshold = 0
	}
	res := DiffResult{Comparable: true, W: a.W, H: a.H, Total: a.W * a.H}
	// The box starts inverted so the first changed pixel seeds both corners.
	// It is built as a struct literal, not image.Rect(w, h, 0, 0): Rect
	// normalises its arguments and would silently hand back the whole frame
	// as the initial box, making every later comparison a no-op.
	box := image.Rectangle{Min: image.Pt(a.W, a.H), Max: image.Pt(0, 0)}
	for y := 0; y < a.H; y++ {
		ao := y * a.Stride
		bo := y * b.Stride
		for x := 0; x < a.W; x++ {
			d0 := absDiff(a.Pix[ao], b.Pix[bo])
			d1 := absDiff(a.Pix[ao+1], b.Pix[bo+1])
			d2 := absDiff(a.Pix[ao+2], b.Pix[bo+2])
			ao += 3
			bo += 3
			worst := d0
			if d1 > worst {
				worst = d1
			}
			if d2 > worst {
				worst = d2
			}
			if worst > res.MaxDelta {
				res.MaxDelta = worst
			}
			if worst > threshold {
				res.Changed++
				if x < box.Min.X {
					box.Min.X = x
				}
				if y < box.Min.Y {
					box.Min.Y = y
				}
				if x+1 > box.Max.X {
					box.Max.X = x + 1
				}
				if y+1 > box.Max.Y {
					box.Max.Y = y + 1
				}
			}
		}
	}
	if res.Changed > 0 {
		res.Pct = float64(res.Changed) * 100 / float64(res.Total)
		res.BBox = box
	}
	return res
}

func absDiff(a, b byte) int {
	if a > b {
		return int(a - b)
	}
	return int(b - a)
}

// ChangeMap draws which cells of the frame differ, on the same Grid a Render
// of b would use, so the two pictures line up. '#' marks a cell whose pixels
// changed by more than threshold, '.' a cell that differs at all, and ' ' a
// cell that is identical. Marks are drawn over the top.
func ChangeMap(a, b Frame, opts RenderOptions, threshold int) (string, Grid, DiffResult) {
	rect := opts.Rect
	if rect.Empty() {
		rect = image.Rect(0, 0, b.W, b.H)
	}
	g := NewGrid(rect, opts)
	cells := blank(g)
	if a.Empty() || b.Empty() || a.W != b.W || a.H != b.H {
		return "  (frames not comparable)\n", g, DiffResult{Comparable: false, W: a.W, H: a.H}
	}
	res := Diff(a, b, threshold)
	for row := 0; row < g.Rows; row++ {
		for col := 0; col < g.Cols; col++ {
			worst, any := 0, false
			r := g.CellRect(col, row).Intersect(image.Rect(0, 0, b.W, b.H))
			for y := r.Min.Y; y < r.Max.Y; y++ {
				ao := y * a.Stride
				bo := y * b.Stride
				for x := r.Min.X; x < r.Max.X; x++ {
					o := x * 3
					d := absDiff(a.Pix[ao+o], b.Pix[bo+o])
					if v := absDiff(a.Pix[ao+o+1], b.Pix[bo+o+1]); v > d {
						d = v
					}
					if v := absDiff(a.Pix[ao+o+2], b.Pix[bo+o+2]); v > d {
						d = v
					}
					if d > 0 {
						any = true
					}
					if d > worst {
						worst = d
					}
				}
			}
			switch {
			case worst > threshold:
				cells[row][col] = '#'
			case any:
				cells[row][col] = '.'
			}
		}
	}
	drawMarks(cells, g, opts.Marks)
	var sb strings.Builder
	writeRuler(&sb, g)
	for row := 0; row < g.Rows; row++ {
		fmt.Fprintf(&sb, "%4d |", g.RulerY(row))
		for col := 0; col < g.Cols; col++ {
			sb.WriteRune(cells[row][col])
		}
		sb.WriteByte('\n')
	}
	return sb.String(), g, res
}

// ParseRect reads an x,y,w,h region spec and clamps it to a w x h frame. An
// empty spec means the whole frame. A spec that would select nothing is an
// error rather than a silent empty render, because these flags are typed by
// hand.
func ParseRect(spec string, w, h int) (image.Rectangle, error) {
	if strings.TrimSpace(spec) == "" {
		return image.Rect(0, 0, w, h), nil
	}
	parts := strings.Split(spec, ",")
	if len(parts) != 4 {
		return image.Rectangle{}, fmt.Errorf("want x,y,w,h (got %q)", spec)
	}
	var v [4]int
	for i, p := range parts {
		n, err := strconv.Atoi(strings.TrimSpace(p))
		if err != nil {
			return image.Rectangle{}, fmt.Errorf("bad number %q in %q", p, spec)
		}
		v[i] = n
	}
	if v[2] <= 0 || v[3] <= 0 {
		return image.Rectangle{}, fmt.Errorf("w,h must be positive (got %q)", spec)
	}
	r := image.Rect(v[0], v[1], v[0]+v[2], v[1]+v[3]).Intersect(image.Rect(0, 0, w, h))
	if r.Empty() {
		return image.Rectangle{}, fmt.Errorf("region %q lies outside the %dx%d frame", spec, w, h)
	}
	return r, nil
}

// SortedRegions puts text regions in reading order (top to bottom, then left
// to right) so reports are stable between runs.
func SortedRegions(regions []TextRegion) []TextRegion {
	out := append([]TextRegion(nil), regions...)
	sort.SliceStable(out, func(i, j int) bool {
		if absInt(out[i].Y-out[j].Y) > 6 {
			return out[i].Y < out[j].Y
		}
		return out[i].X < out[j].X
	})
	return out
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
