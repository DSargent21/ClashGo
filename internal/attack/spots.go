// spots.go computes deployment coordinates for the 4 strict sides of
// the attack (top / right / bottom / left). The math is fully
// symmetric — the same SpotsForSide call works regardless of which
// side is being attacked, which IS the "invert" property the caller
// asked for:
//
//	define a placement once on one axis, apply to any side via SpotsForSide.
//
// Sides are stored in precision_config.json under "sides" as up to 4
// straight line segments, one per side. Endpoints in JSON are at the
// calibrated reference (860x732 by default); runtime callers pass live
// screenW / screenH so the helper scales correctly.
package attack

import (
	"image"
	"strings"
)

// Side name constants. Mirroring across an axis (top↔bottom,
// left↔right) is the canonical "invert" transform — see MirrorForSide.
const (
	SideTop    = "top"
	SideRight  = "right"
	SideBottom = "bottom"
	SideLeft   = "left"
)

// DefaultSpotsCount is the default number of even-spaced tap points to
// emit per side. Matches linePoints in deploy_line.go so dots line up
// with the existing red-zone-aware executor's expectations.
const DefaultSpotsCount = 15

// ClassifySide names a line segment by orientation + position so users
// (or pick_coords -mode=four) can click sides in any order. Returns
// one of SideTop / SideRight / SideBottom / SideLeft.
//
//	Mostly horizontal → "top" if avgY < midY else "bottom"
//	Mostly vertical   → "left" if avgX < midX else "right"
//	Diagonal          → by whichever centroid axis is farther from screen center
func ClassifySide(p1, p2 image.Point, screenW, screenH int) string {
	if p1.X == p2.X && p1.Y == p2.Y {
		return SideTop
	}
	midX, midY := screenW/2, screenH/2
	dx := p2.X - p1.X
	dy := p2.Y - p1.Y
	adx, ady := absInt(dx), absInt(dy)

	if adx > ady*2 {
		avgY := (p1.Y + p2.Y) / 2
		if avgY < midY {
			return SideTop
		}
		return SideBottom
	}
	if ady > adx*2 {
		avgX := (p1.X + p2.X) / 2
		if avgX < midX {
			return SideLeft
		}
		return SideRight
	}
	cx := (p1.X + p2.X) / 2
	cy := (p1.Y + p2.Y) / 2
	avx, avy := absInt(cx-midX), absInt(cy-midY)
	if avx > avy {
		if cx < midX {
			return SideLeft
		}
		return SideRight
	}
	if cy < midY {
		return SideTop
	}
	return SideBottom
}

func absInt(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

// SpotsForSide returns `count` evenly-distributed tap points along the
// side's deployment line. The line endpoints come from pCfg.Sides[side]
// (a key precision_config.json gained to make the 4 strict sides
// first-class).
//
// CONTRACT: pCfg.Sides endpoints must already be in LIVE screen
// coordinates. Both DeployDynamic (legacy JSON-load) and the
// orchestrator's redZone override pre-scale Sides to live screen dims,
// so this function returns live-coord tap points directly. screenW /
// screenH are accepted for API symmetry with potential future callers
// that need clamping to the screen rect, but the math currently does
// not use them.
//
// Returns nil on:
//   - count <= 0
//   - pCfg.Sides nil or missing key for `side`
func SpotsForSide(pCfg PrecisionConfig, side string, count, screenW, screenH int) []image.Point {
	_ = screenW
	_ = screenH
	if count <= 0 {
		return nil
	}
	sideK := strings.ToLower(strings.TrimSpace(side))
	if pCfg.Sides == nil {
		return nil
	}
	edge, ok := pCfg.Sides[sideK]
	if !ok {
		return nil
	}

	points := make([]image.Point, count)
	for i := 0; i < count; i++ {
		t := 0.5
		if count > 1 {
			t = float64(i) / float64(count-1)
		}
		points[i] = image.Pt(
			edge.P1.X+int(float64(edge.P2.X-edge.P1.X)*t),
			edge.P1.Y+int(float64(edge.P2.Y-edge.P1.Y)*t),
		)
	}
	return points
}

// MirrorForSide returns the symmetric tap point of p across the screen
// center for the requested target side. This is the explicit "invert"
// helper — define a placement on one axis, receive the equivalent
// placement on the opposite axis:
//
//	top    ↔ bottom : (x, y) → (x, screenH - y)
//	left   ↔ right  : (x, y) → (screenW - x, y)
//	same side       : (x, y) → (x, y)         (identity short-circuit)
//	cross axis      : (x, y) → (x, y)         (no-op; 90° rotation out of scope)
//
// Same-side identity short-circuits before any flip so callers passing
// `MirrorForSide(p, "top", "top", ...)` get p back unchanged. Cross-axis
// requests (top↔left) deliberately no-op since that would require a
// 90° rotation, which this helper does not implement.
func MirrorForSide(p image.Point, fromSide, toSide string, screenW, screenH int) image.Point {
	from := strings.ToLower(strings.TrimSpace(fromSide))
	to := strings.ToLower(strings.TrimSpace(toSide))

	if from == to {
		return p
	}

	horiz := func(s string) bool { return s == SideTop || s == SideBottom }
	vert := func(s string) bool { return s == SideLeft || s == SideRight }

	if horiz(from) && horiz(to) {
		return image.Pt(p.X, screenH-p.Y)
	}
	if vert(from) && vert(to) {
		return image.Pt(screenW-p.X, p.Y)
	}
	return p
}
