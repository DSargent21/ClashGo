package attack

import (
	"image"

	"github.com/rs/zerolog"
)

const (
	standoff = 80
	margin   = 30
	yTopMin  = 110
	yBotPad  = 80
	xMinPad  = 60
	// bandTopInset keeps a clamped point clear of the HUD's last row.
	bandTopInset = 20
	linePoints   = 15
	lineSpacing  = 35

	// safeGroundInsetFrac is how far in from each playfield edge the
	// conservative deployable box sits, used only when the red boundary could
	// not be established. See SafeGroundRect.
	safeGroundInsetFrac = 0.20
)

// YTopMin is the top of the deployable band: the deploy line never places a
// point above it, because the resource HUD occupies the rows above. Exported so
// the tools that render or verify a plan (cmd/attack_verify) and the formula
// clamp share one definition of the band instead of each keeping its own
// percentage of the screen.
func YTopMin() int { return yTopMin }

// SafeGroundRect is the box a deploy point is guaranteed to be inside when the
// red no-deploy boundary could NOT be established, and therefore nothing about
// the frame's outer ground has been verified.
//
// The red band is a property of the MAP, not of the screen: in Clash of Clans
// the ring between the village and the outer edge of the battle map is
// undeployable, so a point's risk of being refused is a function of how far it
// sits from the middle of the view. The middle of the frame is inside the base
// and is never inside that ring. So when the boundary is unknown, the frame's
// central box is the one region whose legality does not depend on having solved
// the detection problem, and it is what the deploy path falls back to.
//
// Live reason this exists: 2026-09-30, `Auto EDrag Rush`, target BottomRight.
// The detector rejected its only candidate (the whole-frame sheet the gold HUD
// and warm terrain fuse into) and reported no boundary, so the run deployed with
// no legality check at all onto the user's pinned line (897,557)->(1154,367) —
// which hugs the right edge of a 1280px frame — and the game answered every tap
// with "You cannot deploy troops on the Red area!". All five slots stayed
// undeployed and the deploy phase burned 17.5s against a 7s goal. Retrying
// helped nothing: the retry ladder moved points 8-38px, which stays inside the
// refused region by construction.
//
// This is a floor, not a target. The band clamp is still applied on top of it
// (the box is derived from the band, so it is a subset), and when the boundary
// IS found this is not consulted at all — a real outline beats a guess.
func SafeGroundRect(w, h, uiCutoff int) image.Rectangle {
	bandTop := BandTop()
	bandBot := uiCutoff
	if bandBot <= bandTop {
		bandBot = h
	}
	insetX := int(float64(w) * safeGroundInsetFrac)
	insetY := int(float64(bandBot-bandTop) * safeGroundInsetFrac)
	lo := image.Pt(insetX, bandTop+insetY)
	hi := image.Pt(w-insetX, bandBot-insetY)
	if hi.X <= lo.X {
		// A frame too narrow for the inset still has a legal middle; keep it
		// non-empty rather than collapsing every point onto one column.
		lo.X, hi.X = 0, w-1
	}
	if hi.Y <= lo.Y {
		lo.Y, hi.Y = bandTop, bandBot
	}
	return image.Rect(lo.X, lo.Y, hi.X, hi.Y)
}

// TranslateIntoSafeGround shifts a whole deploy line by the smallest offset
// that puts every one of its points inside safe, and reports the offset.
//
// safe is a half-open image.Rectangle: Max is exclusive, so the last legal
// column/row is Max-1. The offsets are computed against that last legal
// coordinate rather than against Max itself — landing a tap exactly on Max
// satisfies a `>=` comparison and still fails image.Rectangle.In, which is the
// check the deploy path uses to decide a point is on legal ground.
//
// A translation, not a per-point clamp, because clamping collapses a line onto
// the box edge and destroys the length and the direction the user pinned: the
// BottomRight line above would have folded into the corner of the box and
// deployed every unit on top of one tile. Shifting preserves both, so a
// corrected line is still the line that was asked for, just further in.
//
// The smallest offset is chosen per axis independently, which is exact for an
// axis-aligned bound: if the line overflows on the right, move it left by
// exactly that overflow, and no less will do.
func TranslateIntoSafeGround(points []image.Point, safe image.Rectangle) (dx, dy int, moved bool) {
	if len(points) == 0 || safe.Empty() {
		return 0, 0, false
	}
	minX, minY := points[0].X, points[0].Y
	maxX, maxY := points[0].X, points[0].Y
	for _, p := range points {
		if p.X < minX {
			minX = p.X
		}
		if p.X > maxX {
			maxX = p.X
		}
		if p.Y < minY {
			minY = p.Y
		}
		if p.Y > maxY {
			maxY = p.Y
		}
	}
	lastX, lastY := safe.Max.X-1, safe.Max.Y-1
	dx = axisOffset(minX, maxX, safe.Min.X, lastX)
	dy = axisOffset(minY, maxY, safe.Min.Y, lastY)
	if dx == 0 && dy == 0 {
		return 0, 0, false
	}
	for i := range points {
		points[i].X += dx
		points[i].Y += dy
	}
	return dx, dy, true
}

// axisOffset is the translation that brings the [min, max] span inside the
// INCLUSIVE range [lo, hi] (lo is a legal coordinate, hi is the last legal
// coordinate): none when it already fits, the exact overflow when it overhangs
// one end, and a centring when the span is wider than the box.
//
// That last case is a real loss: no translation can make an over-long line fit,
// so it is centred and still overhangs. The caller reports the offsets so the
// reader sees the line is no longer the pinned one rather than having a
// silently shortened line pass for it.
func axisOffset(min, max, lo, hi int) int {
	if max-min > hi-lo {
		return (lo+hi)/2 - (min+max)/2
	}
	switch {
	case min < lo:
		return lo - min
	case max > hi:
		return hi - max
	}
	return 0
}

// DeployLine represents a calculated deployment line.
type DeployLine struct {
	Points  []image.Point
	Side    string
	Anchor  image.Point
	Outside bool
}

// ClampToDeployBand pulls a coordinate back into the strip of field a tap can
// actually reach.
//
// The top resource HUD (loot counters, battle clock, settings) covers roughly
// the first yTopMin rows, and the troop bar begins at uiCutoff. A tap on either
// is consumed by the chrome instead of deploying, so a pinned point up there is
// never what the user meant: they aimed at the field and the picker recorded a
// spot under the overlay. Left alone it is silently thrown away, which is how a
// pinned side can produce a battle where nothing deploys.
//
// Only y is touched. The left/right pads are a placement preference rather than
// a UI boundary, and a line hugging the screen edge is a legitimate plan.
func ClampToDeployBand(p image.Point, uiCutoff int) image.Point {
	if uiCutoff > 0 && p.Y > uiCutoff {
		p.Y = uiCutoff
	}
	if top := BandTop(); p.Y < top {
		p.Y = top
	}
	return p
}

// UICutoff is the first row of the game's own chrome at the bottom of the frame
// (the troop bar). Everything above it is field; a tap below it is given to the
// bar instead of the battlefield. One definition, shared by the red-line
// detector, the band clamp and the hero ground ladder, because a disagreement
// here is what puts a tap on the UI.
func UICutoff(h int) int { return int(float64(h) * 0.85) }

// BandTop is the first row a tap reliably reaches. yTopMin is the codebase's
// definition of where the resource HUD ends, but the HUD's last row lands a few
// pixels below that, so the field really starts at yTopMin + bandTopInset. The
// picker warns against the same number, so what the tool calls safe is exactly
// what the bot leaves alone.
func BandTop() int { return yTopMin + bandTopInset }

// ClampEdgeToDeployBand applies ClampToDeployBand to both endpoints of a pin.
func ClampEdgeToDeployBand(e ManualEdge, uiCutoff int) ManualEdge {
	e.P1 = ClampToDeployBand(e.P1, uiCutoff)
	e.P2 = ClampToDeployBand(e.P2, uiCutoff)
	return e
}

// DeployLineCalculator computes deployment lines dynamically.
type DeployLineCalculator struct {
	logger zerolog.Logger
}

// NewDeployLineCalculator creates calculator.
func NewDeployLineCalculator(logger zerolog.Logger) *DeployLineCalculator {
	return &DeployLineCalculator{logger: logger.With().Str("component", "deploy_line").Logger()}
}

// Calculate returns a deployment line outside the red zone.
// Picks edge with most free space, places line 80px outside red zone.
func (d *DeployLineCalculator) Calculate(
	zone RedZone,
	screenW, screenH, uiCutoff int,
	preferSide string,
	count int,
) DeployLine {
	if count <= 0 {
		count = linePoints
	}

	if !zone.Valid {
		return d.fallbackLine(screenW, screenH, uiCutoff, count)
	}

	freeSpace := map[string]int{
		"left":   zone.BBox.Min.X,
		"right":  screenW - zone.BBox.Max.X,
		"top":    zone.BBox.Min.Y,
		"bottom": uiCutoff - zone.BBox.Max.Y,
	}

	side := d.pickSide(freeSpace, preferSide)

	if freeSpace[side] <= 0 {
		d.logger.Warn().Str("side", side).Msg("no space on preferred side, using fallback")
		return d.fallbackLine(screenW, screenH, uiCutoff, count)
	}

	xLo := xMinPad
	xHi := screenW - xMinPad
	yTop := yTopMin
	yBot := uiCutoff - yBotPad

	var points []image.Point

	switch side {
	case "left":
		x := zone.BBox.Min.X - standoff
		if x < xLo {
			x = xLo
		}
		yStart := zone.BBox.Min.Y + 30
		yEnd := zone.BBox.Max.Y - 30
		if yStart < yTop {
			yStart = yTop
		}
		if yEnd > yBot {
			yEnd = yBot
		}
		points = d.linspaceY(x, yStart, yEnd, count)

	case "right":
		x := zone.BBox.Max.X + standoff
		if x > xHi {
			x = xHi
		}
		yStart := zone.BBox.Min.Y + 30
		yEnd := zone.BBox.Max.Y - 30
		if yStart < yTop {
			yStart = yTop
		}
		if yEnd > yBot {
			yEnd = yBot
		}
		points = d.linspaceY(x, yStart, yEnd, count)

	case "top":
		y := zone.BBox.Min.Y - standoff
		if y < yTop {
			y = yTop
		}
		xStart := zone.BBox.Min.X + 30
		xEnd := zone.BBox.Max.X - 30
		if xStart < xLo {
			xStart = xLo
		}
		if xEnd > xHi {
			xEnd = xHi
		}
		points = d.linspaceX(xStart, xEnd, y, count)

	case "bottom":
		y := zone.BBox.Max.Y + standoff
		if y > yBot {
			y = yBot
		}
		xStart := zone.BBox.Min.X + 30
		xEnd := zone.BBox.Max.X - 30
		if xStart < xLo {
			xStart = xLo
		}
		if xEnd > xHi {
			xEnd = xHi
		}
		points = d.linspaceX(xStart, xEnd, y, count)
	}

	for i := range points {
		points[i].X = clamp(points[i].X, xLo, xHi)
		points[i].Y = clamp(points[i].Y, yTop, yBot)
	}

	anchor := points[len(points)/2]

	d.logger.Info().
		Str("side", side).
		Int("points", len(points)).
		Interface("anchor", anchor).
		Msg("calculated deployment line")

	return DeployLine{
		Points:  points,
		Side:    side,
		Anchor:  anchor,
		Outside: true,
	}
}

// pickSide selects edge with most free space.
func (d *DeployLineCalculator) pickSide(freeSpace map[string]int, prefer string) string {

	if prefer != "" && freeSpace[prefer] > 0 {
		return prefer
	}

	best := "left"
	bestSpace := 0
	for side, space := range freeSpace {
		if space > bestSpace {
			bestSpace = space
			best = side
		}
	}
	return best
}

// fallbackLine creates a line when no red zone is detected.
// Uses fixed positions near screen edges.
func (d *DeployLineCalculator) fallbackLine(screenW, screenH, uiCutoff, count int) DeployLine {

	x := xMinPad
	yStart := yTopMin
	yEnd := uiCutoff - yBotPad

	points := d.linspaceY(x, yStart, yEnd, count)
	anchor := points[len(points)/2]

	d.logger.Warn().Msg("using fallback deployment line (no red zone)")

	return DeployLine{
		Points:  points,
		Side:    "left",
		Anchor:  anchor,
		Outside: false,
	}
}

// linspaceY generates points with varying Y, fixed X.
func (d *DeployLineCalculator) linspaceY(x, yStart, yEnd, count int) []image.Point {
	points := make([]image.Point, count)
	for i := 0; i < count; i++ {
		t := float64(i) / float64(count-1)
		y := yStart + int(float64(yEnd-yStart)*t)
		points[i] = image.Pt(x, y)
	}
	return points
}

// linspaceX generates points with varying X, fixed Y.
func (d *DeployLineCalculator) linspaceX(xStart, xEnd, y, count int) []image.Point {
	points := make([]image.Point, count)
	for i := 0; i < count; i++ {
		t := float64(i) / float64(count-1)
		x := xStart + int(float64(xEnd-xStart)*t)
		points[i] = image.Pt(x, y)
	}
	return points
}

func clamp(v, min, max int) int {
	if v < min {
		return min
	}
	if v > max {
		return max
	}
	return v
}
