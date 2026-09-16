package game

import (
	"image"
	"math"
	"sync/atomic"
)

// Reference frame. Every authored coordinate in this package and in the
// attack package (pixel anchors, ROIs, deploy geometry, strategy points) is
// expressed in this 860x732 space, measured on the BlueStacks instance the
// bot was calibrated against.
const (
	RefWidth  = 860
	RefHeight = 732
)

// refDiagonal is |(RefWidth, RefHeight)|, the denominator of the display
// scale. Precomputed so DisplayScale is branch-free on the hot path.
var refDiagonal = math.Hypot(RefWidth, RefHeight)

// Anchor says how a reference-frame coordinate follows the device geometry.
//
// This exists because Clash of Clans does NOT lay a non-reference geometry out
// by the per-axis ratios (physW/RefWidth, physH/RefHeight). Measured live with
// cmd/resprobe (see docs/RESOLUTION.md):
//
//   - the game world is scaled uniformly about the viewport centre by the
//     display scale k, so a world point keeps its offset from the centre;
//   - HUD chrome (buttons, bars, icons, dialogs) is scaled by the same k but
//     anchored to the nearest screen edge, so its distance to that edge — not
//     its distance to the centre — is what survives.
//
// k tracks the screen-diagonal ratio: at 1280x720 the measured world scale was
// 1.325 against 1.3006 predicted, at 1600x900 1.68 against 1.626, at
// 1920x1080 2.00 against 1.951. The prediction is used because it is
// geometry-derived and within a few percent; template matching and pixel
// checks both tolerate that, and the reference geometry (where every anchor
// was measured) is exact by construction.
type Anchor uint8

const (
	// AnchorEdge is HUD chrome: the point keeps its distance to the nearest
	// screen edge and scales by k. This is the zero value because most
	// authored anchors live on buttons, bars and dialogs.
	AnchorEdge Anchor = iota
	// AnchorCenter is centred content: overlays (battle-result panel, dialogs,
	// splash art) and anything authored against world/village coordinates.
	// The point keeps its offset from the viewport centre and scales by k.
	AnchorCenter
)

type Calibration struct {
	PhysicalW  int
	PhysicalH  int
	ScaleX     float64
	ScaleY     float64
	MidOffsetY int
	BottomOffY int
	Verified   bool

	// K is the display scale of the live geometry relative to the reference
	// frame: HUD elements and world content are rendered K times larger than
	// at 860x732. 1.0 at the reference geometry. NewCalibration derives it
	// from the screen diagonal; SetDisplayScale can replace it with a value
	// measured on the running device — see docs/RESOLUTION.md.
	K float64

	// override holds math.Float64bits of a caller-supplied display scale, or 0
	// when the derived K is in use. An atomic rather than a plain float so the
	// scale can be replaced while the bot's goroutines are running: DisplayScale
	// is read on hot paths (per frame, per pixel probe), where a lock would be a
	// tax on every sample.
	override atomic.Uint64
}

// NewCalibration builds a calibration for a live frame geometry.
//
// At the reference geometry (860x732) every mapping in this file is the
// identity, which is what keeps the authored dataset — and the whole existing
// test suite — valid without changes.
func NewCalibration(physW, physH int) *Calibration {
	k := 1.0
	if physW > 0 && physH > 0 {
		k = math.Hypot(float64(physW), float64(physH)) / refDiagonal
	}
	return &Calibration{
		PhysicalW:  physW,
		PhysicalH:  physH,
		ScaleX:     float64(physW) / float64(RefWidth),
		ScaleY:     float64(physH) / float64(RefHeight),
		MidOffsetY: (physH - RefHeight) / 2,
		BottomOffY: physH - RefHeight,
		K:          k,
	}
}

// DisplayScale returns the scale the game renders HUD chrome and world
// content at for this geometry (1.0 when unset, so a zero Calibration behaves
// like the reference). A caller-supplied scale wins over the derived value.
func (c *Calibration) DisplayScale() float64 {
	if c == nil {
		return 1.0
	}
	if bits := c.override.Load(); bits != 0 {
		return math.Float64frombits(bits)
	}
	if c.K <= 0 {
		return 1.0
	}
	return c.K
}

// SetDisplayScale pins the display scale, superseding the diagonal-ratio
// derivation. Callers use it with a scale measured on the actual device
// (`cmd/resprobe`, see docs/RESOLUTION.md); a non-positive or non-finite value
// is ignored so a zero config field means "derive it". Safe to call while other
// goroutines use the calibration: the value is read back by DisplayScale and
// therefore by every mapping.
func (c *Calibration) SetDisplayScale(k float64) {
	if c == nil || k <= 0 || math.IsNaN(k) || math.IsInf(k, 0) {
		return
	}
	c.override.Store(math.Float64bits(k))
}

// DisplayScaleOverride reports the pinned display scale, if one was set.
func (c *Calibration) DisplayScaleOverride() (float64, bool) {
	if c == nil {
		return 0, false
	}
	bits := c.override.Load()
	if bits == 0 {
		return 0, false
	}
	return math.Float64frombits(bits), true
}

// IsReferenceGeometry reports whether this calibration is the authored
// geometry, i.e. every mapping is the identity. Callers that must not pay for
// (or depend on) the mapping use it to take the legacy path.
func (c *Calibration) IsReferenceGeometry() bool {
	if c == nil {
		return true
	}
	// An unset geometry carries no mapping information (struct literals that
	// only pin ScaleX/ScaleY, test doubles), so it must keep the identity
	// behaviour rather than map against a zero-sized screen.
	if c.PhysicalW <= 0 || c.PhysicalH <= 0 {
		return true
	}
	return c.PhysicalW == RefWidth && c.PhysicalH == RefHeight
}

func (c *Calibration) centerX() float64 { return float64(c.PhysicalW) / 2 }
func (c *Calibration) centerY() float64 { return float64(c.PhysicalH) / 2 }

// MapX maps a reference x for content anchored to the left or right edge
// (AnchorEdge) or to the viewport centre (AnchorCenter).
func (c *Calibration) MapX(x int, a Anchor) int {
	if c.IsReferenceGeometry() {
		return x
	}
	k := c.DisplayScale()
	fx := float64(x)
	if a == AnchorCenter {
		return clampInt(int(math.Round(c.centerX()+(fx-RefWidth/2.0)*k)), 0, c.PhysicalW-1)
	}
	if fx < RefWidth/2.0 {
		return clampInt(int(math.Round(fx*k)), 0, c.PhysicalW-1)
	}
	// Right-anchored: keep the distance to the right edge, scaled.
	return clampInt(int(math.Round(float64(c.PhysicalW)-(RefWidth-fx)*k)), 0, c.PhysicalW-1)
}

// MapY maps a reference y for content anchored to the top or bottom edge
// (AnchorEdge) or to the viewport centre (AnchorCenter).
func (c *Calibration) MapY(y int, a Anchor) int {
	if c.IsReferenceGeometry() {
		return y
	}
	k := c.DisplayScale()
	fy := float64(y)
	if a == AnchorCenter {
		return clampInt(int(math.Round(c.centerY()+(fy-RefHeight/2.0)*k)), 0, c.PhysicalH-1)
	}
	if fy < RefHeight/2.0 {
		return clampInt(int(math.Round(fy*k)), 0, c.PhysicalH-1)
	}
	return clampInt(int(math.Round(float64(c.PhysicalH)-(RefHeight-fy)*k)), 0, c.PhysicalH-1)
}

// MapPoint maps a reference point using one anchor for both axes.
func (c *Calibration) MapPoint(pt Point, a Anchor) Point {
	return Point{X: c.MapX(pt.X, a), Y: c.MapY(pt.Y, a)}
}

// MapRect maps a reference rectangle. The anchor decides whether the rect's
// edges are measured from the screen edges (AnchorEdge) or from the centre
// (AnchorCenter); the rectangle keeps its K-scaled size either way.
func (c *Calibration) MapRect(r image.Rectangle, a Anchor) image.Rectangle {
	x1, x2 := c.MapX(r.Min.X, a), c.MapX(r.Max.X, a)
	y1, y2 := c.MapY(r.Min.Y, a), c.MapY(r.Max.Y, a)
	if x1 > x2 {
		x1, x2 = x2, x1
	}
	if y1 > y2 {
		y1, y2 = y2, y1
	}
	return image.Rect(x1, y1, x2, y2)
}

// MapPixelCheck maps a rule's colour probe with the rule's anchor. Probes are
// 1-3 px, so the anchor semantics matter far more than the size: a HUD probe
// keeps its edge offset, an overlay/world probe keeps its centre offset.
func (c *Calibration) MapPixelCheck(chk PixelCheck, a Anchor) PixelCheck {
	return PixelCheck{
		X:         c.MapX(chk.X, a),
		Y:         c.MapY(chk.Y, a),
		R:         chk.R,
		G:         chk.G,
		B:         chk.B,
		Tolerance: chk.Tolerance,
	}
}

// Calibration is normally built by the bot from the live screen size (see
// internal/bot/bot.go); the struct literal keeps all scaling math local to
// this package and resolution-independent.
//
// ScaleRef is the LEGACY per-axis map: x by physW/RefWidth, y by
// physH/RefHeight. It is correct at the reference geometry and for geometries
// with the reference aspect ratio (where k == ScaleX == ScaleY), and is kept
// for call sites that are genuinely resolution-proportional. Content that
// follows the game's HUD/world layout (see Anchor) must use MapX/MapY/MapRect
// instead.
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
		Anchor:   r.Anchor,
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

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
