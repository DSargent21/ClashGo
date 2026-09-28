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
)

// YTopMin is the top of the deployable band: the deploy line never places a
// point above it, because the resource HUD occupies the rows above. Exported so
// the tools that render or verify a plan (cmd/attack_verify) and the formula
// clamp share one definition of the band instead of each keeping its own
// percentage of the screen.
func YTopMin() int { return yTopMin }

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
