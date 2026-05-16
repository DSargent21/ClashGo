package game

import (
	"fmt"
	"image"
	"math"

	"github.com/Ducky705/ClashGo/internal/adb"
)

const (
	RefWidth  = 860
	RefHeight = 732
)

type Calibration struct {
	PhysicalW   int
	PhysicalH   int
	ScaleX      float64
	ScaleY      float64
	MidOffsetY  int
	BottomOffY  int
	Verified    bool
}

func NewCalibrator(client *adb.Client) *Calibrator {
	return &Calibrator{client: client}
}

type Calibrator struct {
	client *adb.Client
}

func (c *Calibrator) Calibrate() (*Calibration, error) {
	mat, err := c.client.CaptureToMat()
	if err != nil {
		return nil, fmt.Errorf("calibration capture: %w", err)
	}
	defer mat.Close()

	w, h := mat.Cols(), mat.Rows()

	cal := &Calibration{
		PhysicalW:   w,
		PhysicalH:   h,
		ScaleX:      float64(w) / RefWidth,
		ScaleY:      float64(h) / RefHeight,
		MidOffsetY:  (h - RefHeight) / 2,
		BottomOffY:  h - RefHeight,
		Verified:    false,
	}

	cal.Verified = true
	return cal, nil
}

func (c *Calibration) ScaleRef(x, y int) (int, int) {
	return int(float64(x) * c.ScaleX), int(float64(y) * c.ScaleY)
}

func (c *Calibration) Unscale(sx, sy int) (int, int) {
	return int(float64(sx) / c.ScaleX), int(float64(sy) / c.ScaleY)
}

func (c *Calibration) ScaleRefRect(x1, y1, x2, y2 int) image.Rectangle {
	sx1, sy1 := c.ScaleRef(x1, y1)
	sx2, sy2 := c.ScaleRef(x2, y2)
	return image.Rect(sx1, sy1, sx2, sy2)
}

func (c *Calibration) ScalePoint(pt Point) Point {
	sx, sy := c.ScaleRef(pt.X, pt.Y)
	return Point{X: sx, Y: sy}
}

func (c *Calibration) ScalePixelCheck(chk PixelCheck) PixelCheck {
	sx, sy := c.ScaleRef(chk.X, chk.Y)
	return PixelCheck{X: sx, Y: sy, R: chk.R, G: chk.G, B: chk.B, Tolerance: chk.Tolerance}
}

func (c *Calibration) ScaleRule(r StateRule) StateRule {
	scaled := StateRule{
		State:    r.State,
		Template: r.Template,
		MinPass:  r.MinPass,
		Weight:   r.Weight,
		Priority: r.Priority,
		Desc:     r.Desc,
	}
	for _, chk := range r.Checks {
		scaled.Checks = append(scaled.Checks, c.ScalePixelCheck(chk))
	}
	return scaled
}

func (c *Calibration) MidY() int {
	return c.MidOffsetY
}

func (c *Calibration) BottomY() int {
	return c.BottomOffY
}

func (c *Calibration) ScaleYRef(y int) int {
	return int(float64(y) * c.ScaleY)
}

func (c *Calibration) ScaleXRef(x int) int {
	return int(float64(x) * c.ScaleX)
}

func (c *Calibration) ApplyOffset(baseY int) int {
	if c.PhysicalH > RefHeight {
		return baseY + c.MidOffsetY
	}
	return baseY
}

func (c *Calibration) Distance(a, b Point) float64 {
	dx := float64(a.X - b.X)
	dy := float64(a.Y - b.Y)
	return math.Sqrt(dx*dx + dy*dy)
}

func (c *Calibration) IsRectInBounds(r image.Rectangle) bool {
	return r.Min.X >= 0 && r.Min.Y >= 0 &&
		r.Max.X <= c.PhysicalW && r.Max.Y <= c.PhysicalH
}