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
	// AnchorCenterBottom is a bottom-sheet modal: horizontally centred, but
	// vertically anchored to the bottom edge, so the sheet keeps its distance
	// to the bottom of the screen and grows toward the top.
	//
	// Measured live by opening one obstacle dialog and resizing the device with
	// it still on screen, which yields the same dialog at both geometries:
	// its title band at ref (435,543) renders at 1280x720 (647,470), and
	// x = 640 + (435-430)*k, y = 720 - (732-543)*k explains the whole sheet
	// (title, REMOVE and Move buttons) while the centre model is 130 px off in
	// y and the per-axis edge model 250 px. Modal dialogs that hang off the
	// bottom edge are built this way; the centred overlays (result panel,
	// splash chain, dialogs that sit mid-screen) are not.
	AnchorCenterBottom
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

// AxisAnchor selects which screen edge — or the centre — a reference
// coordinate keeps its distance to, on one axis. The game anchors each widget
// per axis, and cmd/resprobe measures which: at 1280x720 the village builder
// head (ref 430,30) lands at (640,40) — x centred, y top — while the army-bar
// battle button (ref 525,247) lands at (767,200) — both axes centred, and the
// Attack! button (ref 60,695) at (79,669) — left and bottom. A single anchor
// for both axes therefore cannot express the layout, and the per-axis ratios
// ScaleRef used (1.49 x, 0.98 y at 1280x720) match none of them.
type AxisAnchor int

const (
	// AnchorLow keeps the distance to the low edge: left for x, top for y.
	AnchorLow AxisAnchor = iota
	// AnchorHigh keeps the distance to the high edge: right for x, bottom for y.
	AnchorHigh
	// AnchorMid keeps the distance to the axis centre.
	AnchorMid
)

// The HUD is a small number of anchored widget groups, not one uniform layout,
// so Hud picks each axis from the group a reference coordinate falls in. The
// bands below are fractions of the reference axis and were chosen to fit every
// live measurement (docs/RESOLUTION.md):
//
//	left column     x <  0.12 * 860 = 103   Attack!, shop, End Battle (x)
//	right column    x >  0.88 * 860 = 757   resource icons (830), SHOP (770)
//	top bar         y <= 0.232 * 732 = 170  resource column (y 16-145), builder head
//	bottom stack    y >= 0.64 * 732 = 468   troop bar, Next, End Battle, attack buttons
//	everything else                        centre-anchored widgets: the army bar
//	                                       and its expansion (y 190-250), the
//	                                       builder-head x, recipe cards, army arrow
//
// The top bar has to reach y 170 rather than a symmetric 12%: the resource
// column runs from y 16 to y 145, and a probe at y 95 is still top-anchored
// (measured: it lands at 124, not at the centre model's y 1).
const (
	hudXLowBand  = 0.12
	hudXHighBand = 0.88
	hudYLowBand  = 0.232
	hudYHighBand = 0.64
)

// MapAxis maps one reference axis coordinate. The reference geometry and a
// zero-value calibration are the identity, so every existing caller and test is
// unaffected.
func (c *Calibration) MapAxis(p, refSize, size int, a AxisAnchor) int {
	if c.IsReferenceGeometry() || refSize <= 0 || size <= 0 {
		return p
	}
	k := c.DisplayScale()
	fp := float64(p)
	switch a {
	case AnchorHigh:
		return clampInt(int(math.Round(float64(size)-(float64(refSize)-fp)*k)), 0, size-1)
	case AnchorMid:
		return clampInt(int(math.Round(float64(size)/2+(fp-float64(refSize)/2)*k)), 0, size-1)
	default:
		return clampInt(int(math.Round(fp*k)), 0, size-1)
	}
}

// X maps a reference x with an explicit horizontal anchor.
func (c *Calibration) X(x int, a AxisAnchor) int { return c.MapAxis(x, RefWidth, c.PhysicalW, a) }

// Y maps a reference y with an explicit vertical anchor.
func (c *Calibration) Y(y int, a AxisAnchor) int { return c.MapAxis(y, RefHeight, c.PhysicalH, a) }

// Point2 maps a reference point with one anchor per axis.
func (c *Calibration) Point2(x, y int, xa, ya AxisAnchor) (int, int) {
	return c.X(x, xa), c.Y(y, ya)
}

// Rect2 maps a reference rectangle with one anchor per axis; the rectangle
// keeps its K-scaled size whichever anchors are used, because both edges are
// mapped with the same one.
func (c *Calibration) Rect2(r image.Rectangle, xa, ya AxisAnchor) image.Rectangle {
	x1, x2 := c.X(r.Min.X, xa), c.X(r.Max.X, xa)
	y1, y2 := c.Y(r.Min.Y, ya), c.Y(r.Max.Y, ya)
	if x1 > x2 {
		x1, x2 = x2, x1
	}
	if y1 > y2 {
		y1, y2 = y2, y1
	}
	return image.Rect(x1, y1, x2, y2)
}

// Hud maps a reference point on HUD chrome: each axis keeps its distance to the
// edge it sits against, or to the viewport centre when it is not near an edge.
// This is the mapping for buttons and bars — the widgets the game anchors to the
// screen, not to the world.
func (c *Calibration) Hud(x, y int) (int, int) {
	return c.Point2(x, y, c.hudAxis(x, RefWidth), c.hudAxis(y, RefHeight))
}

// AnchorPoint maps a reference point declared with the classifier's semantic
// anchor: AnchorEdge for HUD chrome (per-axis edge or centre anchoring) and
// AnchorCenter for centred overlays and world content. Call sites that carry an
// anchor declaration use this; call sites that know their context use Hud or
// Centre directly.
func (c *Calibration) AnchorPoint(x, y int, a Anchor) (int, int) {
	switch a {
	case AnchorCenter:
		return c.Centre(x, y)
	case AnchorCenterBottom:
		// Centred horizontally, measured from the bottom edge vertically.
		return c.X(x, AnchorMid), c.Y(y, AnchorHigh)
	default:
		return c.Hud(x, y)
	}
}

// HudRect maps a reference rectangle on HUD chrome; the anchors are chosen from
// the rectangle's centre point, so a rect that spans a whole bar (the troop
// slot row, the troop counter) keeps that bar's anchoring.
func (c *Calibration) HudRect(r image.Rectangle) image.Rectangle {
	return c.Rect2(r, c.hudAxis((r.Min.X+r.Max.X)/2, RefWidth), c.hudAxis((r.Min.Y+r.Max.Y)/2, RefHeight))
}

// Centre maps a reference point on centred content: the game world (village,
// battle field, base) and every full-screen overlay and dialog, which are laid
// out around the viewport centre. Measured at 1280x720: a village tap at ref
// (50,450) lands at (131,472) — the centre model — not at the left-edge offset
// (66,346).
func (c *Calibration) Centre(x, y int) (int, int) {
	return c.Point2(x, y, AnchorMid, AnchorMid)
}

// CentreRect maps a rectangle on centred content (an overlay ROI, a world
// zone).
func (c *Calibration) CentreRect(r image.Rectangle) image.Rectangle {
	return c.Rect2(r, AnchorMid, AnchorMid)
}

// hudAxis picks the anchor for one HUD axis from the reference coordinate's
// widget group (see the band table above).
func (c *Calibration) hudAxis(p, refSize int) AxisAnchor {
	fp := float64(p)
	switch refSize {
	case RefWidth:
		switch {
		case fp < float64(RefWidth)*hudXLowBand:
			return AnchorLow
		case fp > float64(RefWidth)*hudXHighBand:
			return AnchorHigh
		}
	case RefHeight:
		switch {
		case fp <= float64(RefHeight)*hudYLowBand:
			return AnchorLow
		case fp >= float64(RefHeight)*hudYHighBand:
			return AnchorHigh
		}
	}
	return AnchorMid
}

// ---------------------------------------------------------------------------
// The inverse: recovering the reference coordinate for a point or rect already
// measured on a live frame.
//
// Everything above answers "where does this authored coordinate land on this
// device". The picker tools need the other direction: a human drags a box on the
// running device and the asset file has to record a reference rect that maps
// BACK onto the box they drew. Without an inverse, a picker either records the
// raw drag and the loaders re-map it (which moves the box), or it divides by a
// per-axis ratio — the deprecated law this file exists to replace — and lands
// somewhere else again. tools/select_wall_upgrade_buttons.py did the former:
// assets/wall_upgrade_buttons.json holds raw drag coordinates while
// internal/bot's loader reads the same numbers as 860x732 reference values, so
// the picked boxes were re-mapped on every run.
//
// The law is piecewise (the anchor is chosen from the source coordinate's band),
// so the inverse is solved by trying each anchor and keeping a candidate that
// maps back exactly. That also makes it a verification: a coordinate the law
// cannot reach answers false instead of returning a near miss, and the picker can
// tell the user to nudge the box rather than writing an asset that does not fit.

// axisPreimageNearest scans the reference axis for the value MapAxis maps
// closest onto target, returning that value and what it actually maps to.
//
// A scan rather than the closed form, because the law is NOT surjective: every
// anchor multiplies the reference coordinate by k, so as the reference steps by
// one the physical value steps by k > 1 and skips roughly a quarter of the
// integers. At 1280x720 about 45% of sampled physical points have no exact
// preimage at all (TestHudReferenceInvertsHudExactly measures it), which is a
// property of the mapping, not a rounding bug — so the tools need the nearest
// reachable coordinate and an honest report of how far the drag had to move.
//
// found is false only for a degenerate axis or a target outside the frame.
func (c *Calibration) axisPreimageNearest(target, refSize, size int, a AxisAnchor) (ref, mapped int, found bool) {
	if refSize <= 0 || size <= 0 || target < 0 || target >= size {
		return 0, 0, false
	}
	bestDelta := -1
	for p := 0; p <= refSize; p++ {
		m := c.MapAxis(p, refSize, size, a)
		d := m - target
		if d < 0 {
			d = -d
		}
		if bestDelta >= 0 && d >= bestDelta {
			continue
		}
		bestDelta, ref, mapped, found = d, p, m, true
		if d == 0 {
			break
		}
	}
	return ref, mapped, found
}

// HudPointReference returns a reference point that Hud maps exactly onto (x, y),
// or false when no reference point does (see axisPreimageNearest for why that
// happens). Hud re-derives each axis anchor from the candidate itself, so the
// only sound test is the round trip through the real mapping.
func (c *Calibration) HudPointReference(x, y int) (rx, ry int, ok bool) {
	for _, a := range []AxisAnchor{AnchorLow, AnchorHigh, AnchorMid} {
		candX, mappedX, okX := c.axisPreimageNearest(x, RefWidth, c.PhysicalW, a)
		if !okX || mappedX != x {
			continue
		}
		for _, b := range []AxisAnchor{AnchorLow, AnchorHigh, AnchorMid} {
			candY, mappedY, okY := c.axisPreimageNearest(y, RefHeight, c.PhysicalH, b)
			if !okY || mappedY != y {
				continue
			}
			if gx, gy := c.Hud(candX, candY); gx == x && gy == y {
				return candX, candY, true
			}
		}
	}
	return 0, 0, false
}

// HudRectReferenceSnap returns a reference rect that HudRect maps onto a
// rectangle within tol pixels of target, together with what it actually maps to.
//
// tol is how far the recorded box is allowed to sit from the user's drag; 1 is
// the useful value, because the mapping skips individual pixels (see
// axisPreimageNearest) and a picker cannot ask a human to drag to an exact
// pixel anyway. The caller compares mapped against target to tell the user where
// the recorded box landed.
//
// HudRect picks its anchor from the rectangle's centre and then maps BOTH edges
// with it, so both edges are inverted with one anchor per axis and every
// combination is verified through HudRect itself. That verification is what
// rejects the anchor choices whose edges would move independently.
func (c *Calibration) HudRectReferenceSnap(target image.Rectangle, tol int) (ref, mapped image.Rectangle, ok bool) {
	if target.Empty() || tol < 0 {
		return image.Rectangle{}, image.Rectangle{}, false
	}
	best := tol + 1
	for _, ax := range []AxisAnchor{AnchorLow, AnchorHigh, AnchorMid} {
		x1, _, ok1 := c.axisPreimageNearest(target.Min.X, RefWidth, c.PhysicalW, ax)
		x2, _, ok2 := c.axisPreimageNearest(target.Max.X, RefWidth, c.PhysicalW, ax)
		if !ok1 || !ok2 {
			continue
		}
		for _, ay := range []AxisAnchor{AnchorLow, AnchorHigh, AnchorMid} {
			y1, _, ok3 := c.axisPreimageNearest(target.Min.Y, RefHeight, c.PhysicalH, ay)
			y2, _, ok4 := c.axisPreimageNearest(target.Max.Y, RefHeight, c.PhysicalH, ay)
			if !ok3 || !ok4 {
				continue
			}
			cand := image.Rect(min(x1, x2), min(y1, y2), max(x1, x2), max(y1, y2))
			got := c.HudRect(cand)
			// The rect as a whole is what has to fit: HudRect anchors both edges
			// off the centre, so edges that each land within tol can still produce
			// a rect that overlaps the drag by more than tol.
			delta := RectDelta(got, target)
			if delta > tol || delta >= best {
				continue
			}
			best, ref, mapped, ok = delta, cand, got, true
		}
	}
	return ref, mapped, ok
}

// HudCornersReferenceSnap is the inverse for a rect its consumer maps CORNER BY
// CORNER through Hud instead of through HudRect, which is how the builder-menu
// ROI is read (see internal/bot's builder_menu_roi.json loader).
//
// The two laws are not interchangeable, and a picker has to invert the one its
// consumer applies. HudRect picks a single anchor per axis from the rectangle's
// centre, so a rect spanning a band boundary is unexpressible under it: the menu
// panel's top edge belongs to the top band (it is pinned under the top bar) while
// its bottom edge is mid-screen. Inverting the wrong law produces the same live
// rect on the device it was dragged on — both laws agree there — and then diverges
// on any other geometry, which is why the mismatch is only visible in the file.
func (c *Calibration) HudCornersReferenceSnap(target image.Rectangle, tol int) (ref, mapped image.Rectangle, ok bool) {
	if target.Empty() || tol < 0 {
		return image.Rectangle{}, image.Rectangle{}, false
	}
	x1, mx1, ok1 := c.cornerPreimage(target.Min.X, RefWidth, c.PhysicalW, tol)
	y1, my1, ok2 := c.cornerPreimage(target.Min.Y, RefHeight, c.PhysicalH, tol)
	x2, mx2, ok3 := c.cornerPreimage(target.Max.X, RefWidth, c.PhysicalW, tol)
	y2, my2, ok4 := c.cornerPreimage(target.Max.Y, RefHeight, c.PhysicalH, tol)
	if !ok1 || !ok2 || !ok3 || !ok4 {
		return image.Rectangle{}, image.Rectangle{}, false
	}
	// Ordered the way the loader orders its two mapped corners, so `mapped` is
	// comparable to the drag it came from.
	ref = image.Rect(min(x1, x2), min(y1, y2), max(x1, x2), max(y1, y2))
	mapped = image.Rect(min(mx1, mx2), min(my1, my2), max(mx1, mx2), max(my1, my2))
	if RectDelta(mapped, target) > tol {
		return image.Rectangle{}, image.Rectangle{}, false
	}
	return ref, mapped, true
}

// cornerPreimage scans one axis for the reference coordinate whose OWN-band
// mapping lands closest to target, within tol.
//
// It maps through hudAxis(p, refSize) rather than taking an anchor as a
// parameter, so every candidate is a fixed point of the same function Hud uses:
// a coordinate whose band disagrees with the anchor it was derived under does not
// survive, which is what stops the inverse returning an edge-anchored value for
// something that sits near the middle of the screen (both map to the same live
// pixel on one device, and to different ones on the next).
//
// The scan starts at reference 0 because the low-band candidate is the smaller
// of two coordinates that collide on a live pixel, and it is the one the asset
// files were authored with.
func (c *Calibration) cornerPreimage(target, refSize, size, tol int) (ref, mapped int, ok bool) {
	if refSize <= 0 || size <= 0 || target < 0 || target >= size {
		return 0, 0, false
	}
	best := tol + 1
	for p := 0; p <= refSize; p++ {
		m := c.MapAxis(p, refSize, size, c.hudAxis(p, refSize))
		d := m - target
		if d < 0 {
			d = -d
		}
		if d >= best {
			continue
		}
		best, ref, mapped, ok = d, p, m, true
		if d == 0 {
			break
		}
	}
	return ref, mapped, ok
}

// RectDelta is the largest per-edge distance between two rects, which is the
// distance a tap point derived from either one can move. Exported because the
// picking tools report it: it is the same number that decides whether a snap is
// acceptable, so it must not have a second implementation on the tool side.
func RectDelta(a, b image.Rectangle) int {
	d := func(x, y int) int {
		if x > y {
			return x - y
		}
		return y - x
	}
	return max(max(d(a.Min.X, b.Min.X), d(a.Min.Y, b.Min.Y)), max(d(a.Max.X, b.Max.X), d(a.Max.Y, b.Max.Y)))
}

// Length scales a pixel distance (a radius, a spacing, a jitter bound) by the
// display scale. Distances are not edge-anchored: they grow with K on both axes
// whatever they measure.
func (c *Calibration) Length(px float64) float64 {
	if c == nil {
		return px
	}
	return px * c.DisplayScale()
}

// Calibration is normally built by the bot from the live screen size (see
// internal/bot/bot.go); the struct literal keeps all scaling math local to
// this package and resolution-independent.
//
// DEPRECATED: ScaleRef is the per-axis ratio map (x by physW/RefWidth, y by
// physH/RefHeight). It is right only at the reference geometry or where K
// happens to equal both ratios, which no real geometry does at another aspect
// (1.49 x, 0.98 y at 1280x720 against a measured K of 1.325). New call sites
// must use Hud / Centre / X / Y / Length; the remaining callers are being
// migrated in docs/RESOLUTION.md's plan.
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
