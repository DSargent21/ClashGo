package geo

import (
	"fmt"
	"math"
)

type Point struct {
	X, Y int
}

func (p Point) String() string {
	return fmt.Sprintf("(%d,%d)", p.X, p.Y)
}

func (p Point) Add(q Point) Point {
	return Point{p.X + q.X, p.Y + q.Y}
}

func (p Point) Sub(q Point) Point {
	return Point{p.X - q.X, p.Y - q.Y}
}

func (p Point) Mul(s int) Point {
	return Point{p.X * s, p.Y * s}
}

func (p Point) Distance(q Point) float64 {
	dx := float64(p.X - q.X)
	dy := float64(p.Y - q.Y)
	return math.Sqrt(dx*dx + dy*dy)
}

func (p Point) Equals(q Point) bool {
	return p.X == q.X && p.Y == q.Y
}

func (p Point) InRect(r Rect) bool {
	return r.Contains(p)
}

func (p Point) Offset(dx, dy int) Point {
	return Point{p.X + dx, p.Y + dy}
}

type Rect struct {
	Min, Max Point
}

func (r Rect) String() string {
	return fmt.Sprintf("Rect(%s→%s)", r.Min, r.Max)
}

func (r Rect) Width() int  { return r.Max.X - r.Min.X }
func (r Rect) Height() int { return r.Max.Y - r.Min.Y }
func (r Rect) Area() int   { return r.Width() * r.Height() }

func (r Rect) Contains(p Point) bool {
	return p.X >= r.Min.X && p.X <= r.Max.X && p.Y >= r.Min.Y && p.Y <= r.Max.Y
}

func (r Rect) Intersects(s Rect) bool {
	return r.Min.X <= s.Max.X && r.Max.X >= s.Min.X && r.Min.Y <= s.Max.Y && r.Max.Y >= s.Min.Y
}

func (r Rect) Center() Point {
	return Point{(r.Min.X + r.Max.X) / 2, (r.Min.Y + r.Max.Y) / 2}
}

func (r Rect) TopLeft()     Point { return r.Min }
func (r Rect) TopRight()    Point { return Point{r.Max.X, r.Min.Y} }
func (r Rect) BottomLeft()  Point { return Point{r.Min.X, r.Max.Y} }
func (r Rect) BottomRight() Point { return r.Max }

func (r Rect) Shrink(margin int) Rect {
	return Rect{
		Min: Point{r.Min.X + margin, r.Min.Y + margin},
		Max: Point{r.Max.X - margin, r.Max.Y - margin},
	}
}

func (r Rect) Expand(margin int) Rect {
	return Rect{
		Min: Point{r.Min.X - margin, r.Min.Y - margin},
		Max: Point{r.Max.X + margin, r.Max.Y + margin},
	}
}

type Diamond struct {
	Center Point
	Left, Right, Top, Bottom int
}

func (d Diamond) String() string {
	return fmt.Sprintf("Diamond(C=%s L=%d R=%d T=%d B=%d)", d.Center, d.Left, d.Right, d.Top, d.Bottom)
}

func (d Diamond) Contains(p Point) bool {
	dx := p.X - d.Center.X
	dy := p.Y - d.Center.Y

	if dx == 0 {
		return math.Abs(float64(dy)) <= float64(d.Top)
	}

	slope := float64(d.Top) / float64(d.Left)
	edgeY := math.Abs(float64(dx)) * slope

	return math.Abs(float64(dy)) <= edgeY
}

func (d Diamond) InnerMargin(m int) Diamond {
	d.Left -= m
	d.Right -= m
	d.Top -= m
	d.Bottom -= m
	if d.Left < 0 {
		d.Left = 0
	}
	if d.Right < 0 {
		d.Right = 0
	}
	if d.Top < 0 {
		d.Top = 0
	}
	if d.Bottom < 0 {
		d.Bottom = 0
	}
	return d
}

func (d Diamond) Points() [4]Point {
	return [4]Point{
		{d.Center.X, d.Center.Y - d.Top},
		{d.Center.X + d.Right, d.Center.Y},
		{d.Center.X, d.Center.Y + d.Bottom},
		{d.Center.X - d.Left, d.Center.Y},
	}
}

func (d Diamond) MinX() int { return d.Center.X - d.Left }
func (d Diamond) MaxX() int { return d.Center.X + d.Right }
func (d Diamond) MinY() int { return d.Center.Y - d.Top }
func (d Diamond) MaxY() int { return d.Center.Y + d.Bottom }

func (d Diamond) Bounds() Rect {
	return Rect{
		Min: Point{d.MinX(), d.MinY()},
		Max: Point{d.MaxX(), d.MaxY()},
	}
}

func (d Diamond) CenterLine() (topLeft, topRight, bottomLeft, bottomRight, midLeft, midRight, midTop, midBottom Point) {
	cx := d.Center.X
	cy := d.Center.Y
	hw := d.Left / 2
	hh := d.Top / 2

	topLeft = Point{cx - hw, cy - d.Top + hh}
	topRight = Point{cx + hw, cy - d.Top + hh}
	bottomLeft = Point{cx - hw, cy + d.Bottom - hh}
	bottomRight = Point{cx + hw, cy + d.Bottom - hh}
	midLeft = Point{cx - d.Left + hw, cy}
	midRight = Point{cx + d.Right - hw, cy}
	midTop = Point{cx, cy - d.Top + hh}
	midBottom = Point{cx, cy + d.Bottom - hh}

	return
}
