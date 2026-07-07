package attack

import (
	"image"
	"math"

	"github.com/rs/zerolog"
)

const (
	standoff     = 80  // min distance from red zone
	margin       = 30  // distance from screen edge
	yTopMin      = 110 // below top HUD
	yBotPad      = 80  // above troop bar
	xMinPad      = 60  // left screen edge padding
	linePoints   = 15  // points per deployment line
	lineSpacing  = 35  // spacing between points
)

// DeployLine represents a calculated deployment line.
type DeployLine struct {
	Points  []image.Point // Tap coordinates
	Side    string        // "left", "right", "top", "bottom"
	Anchor  image.Point   // Center of line (for spells)
	Outside bool          // Whether line is outside red zone
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

	// If no red zone detected, use fallback
	if !zone.Valid {
		return d.fallbackLine(screenW, screenH, uiCutoff, count)
	}

	// Get free space on each edge
	freeSpace := map[string]int{
		"left":   zone.BBox.Min.X,
		"right":  screenW - zone.BBox.Max.X,
		"top":    zone.BBox.Min.Y,
		"bottom": uiCutoff - zone.BBox.Max.Y,
	}

	// Pick side
	side := d.pickSide(freeSpace, preferSide)

	// Check if we have space
	if freeSpace[side] <= 0 {
		d.logger.Warn().Str("side", side).Msg("no space on preferred side, using fallback")
		return d.fallbackLine(screenW, screenH, uiCutoff, count)
	}

	// Clamp values
	xLo := xMinPad
	xHi := screenW - xMinPad
	yTop := yTopMin
	yBot := uiCutoff - yBotPad

	// Calculate line points
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

	// Clamp all points to safe zones
	for i := range points {
		points[i].X = clamp(points[i].X, xLo, xHi)
		points[i].Y = clamp(points[i].Y, yTop, yBot)
	}

	// Calculate anchor (center of line)
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
	// Use preferred side if it has space
	if prefer != "" && freeSpace[prefer] > 0 {
		return prefer
	}

	// Pick side with most free space
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
	// Default to left edge
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

// SpellLine calculates spell deployment points along a line into the base.
// Spells go from anchor point TOWARD the base center, offset left/right.
func (d *DeployLineCalculator) SpellLine(
	anchor image.Point,
	screenW, uiCutoff int,
	count int,
	depthPct float64,
) []image.Point {
	cx, cy := screenW/2, uiCutoff/2
	dx := float64(cx - anchor.X)
	dy := float64(cy - anchor.Y)
	norm := math.Sqrt(dx*dx + dy*dy)
	if norm < 1 {
		norm = 1
	}
	ux, uy := dx/norm, dy/norm
	// Perpendicular
	px, py := -uy, ux

	points := make([]image.Point, count)
	for i := 0; i < count; i++ {
		depth := depthPct + 0.10*float64(i/2)
		depth = clampF(depth, 0.30, 0.95)

		baseX := float64(anchor.X) + ux*norm*depth
		baseY := float64(anchor.Y) + uy*norm*depth

		offset := 90.0 + 35.0*float64(i/2)
		sign := 1.0
		if i%2 != 0 {
			sign = -1.0
		}

		sx := int(baseX + px*offset*sign)
		sy := int(baseY + py*offset*sign)
		sx = clamp(sx, 60, screenW-60)
		sy = clamp(sy, 60, uiCutoff-60)
		points[i] = image.Pt(sx, sy)
	}
	return points
}

func clamp(v, min, max int) int {
	if v < min { return min }
	if v > max { return max }
	return v
}

func clampF(v, min, max float64) float64 {
	if v < min { return min }
	if v > max { return max }
	return v
}
