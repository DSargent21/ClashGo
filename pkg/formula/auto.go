package formula

import (
	"image"
	"math"
	"strings"

	"github.com/Ducky705/ClashGO/pkg/strategy"
	"github.com/rs/zerolog/log"
)

// AutoPickFor builds a *Formula for every unit in the strategy using
// per-class pinned geometry. No manual clicks required - the side is
// chosen from targetEdge (Random falls back to Left for determinism).
//
// Per-class rules (4-side aware):
//   - Hero     (Point pattern, phase name contains "hero")   → midpoint of the chosen side's deploy line, jitter 5.
//   - Siege    (Point pattern, phase name contains "siege")  → dead center of map, jitter 6.
//   - Line troop   (Line pattern, "balloon"/"dragon"/etc)    → full edge line endpoints, count=10, jitter 3.
//   - Rage spell   (Line pattern, name contains "rage")      → 2 sub-lines: first 15%-60% (3 taps), second 40%-85% (2 taps).
//   - Other spell  (Line pattern, name contains "spell")     → single edge line pulled slightly inward, count=5, jitter 3.
//   - FourSides    → point cluster at map center, jitter 5.
//
// The 20-80 percent rule guarantees auto-pick geometry mathematically
// never lands in a screen corner (the regression that motivated this
// helper).
func AutoPickFor(s *strategy.DynamicStrategy, screenW, screenH int, targetEdge string) *Formula {
	if s == nil || screenW <= 0 || screenH <= 0 {
		return nil
	}
	if targetEdge == "" || strings.EqualFold(targetEdge, "Random") {
		targetEdge = "Left"
	}

	lineP1, lineP2 := autoEdgeLine(targetEdge, screenW, screenH)

	f := &Formula{Name: "auto_picked_for_" + s.Name}
	f.Screen.W = screenW
	f.Screen.H = screenH
	f.Units = make(map[string]UnitEntry)

	seen := make(map[string]bool)
	// Inside autoUnit we need a per-phase hero index to spread heroes
	// along the deploy line (otherwise they all collapse on the
	// midpoint and recoil the bug the auto-mode exists to fix).
	phaseHeroIdx := make(map[int]int)
	phaseHeroTotal := make(map[int]int)
	phaseIdx := 0
	for _, ph := range s.Phases {
		phaseName := strings.ToLower(ph.Name)
		if strings.Contains(phaseName, "hero") {
			phaseHeroTotal[phaseIdx] = countDeployableHeroes(ph)
		}
		phaseIdx++
	}

	phaseIdx = 0
	for _, ph := range s.Phases {
		pattern := strings.ToLower(strings.TrimSpace(ph.Pattern))
		phaseName := strings.ToLower(ph.Name)
		for _, u := range ph.Units {
			if strings.EqualFold(u.Pattern, "Ability") {
				continue
			}
			name := strings.ToLower(strings.TrimSpace(u.Name))
			if name == "" || seen[name] {
				continue
			}
			seen[name] = true
			hIdx := -1
			hTot := 0
			if strings.Contains(phaseName, "hero") {
				hIdx = phaseHeroIdx[phaseIdx]
				hTot = phaseHeroTotal[phaseIdx]
				phaseHeroIdx[phaseIdx] = hIdx + 1
			}
			f.Units[name] = autoUnit(name, pattern, phaseName, lineP1, lineP2, screenW, screenH, hIdx, hTot)
		}
		phaseIdx++
	}
	return f
}

// countDeployableHeroes returns how many distinct deployable heroes
// (non-Ability) exist in a phase. Used to pre-compute the per-phase
// hero total so the hero-spread formula knows the denominator.
func countDeployableHeroes(ph strategy.Phase) int {
	seen := make(map[string]bool)
	n := 0
	for _, u := range ph.Units {
		if strings.EqualFold(u.Pattern, "Ability") {
			continue
		}
		name := strings.ToLower(strings.TrimSpace(u.Name))
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		n++
	}
	return n
}

// autoEdgeLine returns the GEOMETRY endpoints for the chosen side.
// All endpoints are clamped within the 20-80 percent band so the
// auto-pick cannot land in a screen corner.
//
//	Top/Bottom variants      → horizontal line, 20-80% horizontal extent.
//	Left  variant            → vertical line at 15% from left, 20-80% vertical extent.
//	Right variant            → vertical line at 85% (1-0.15) from right, 20-80% vertical extent.
//	TopLeft / TopRight       → horizontal near-top line (treat as "Top").
//	BottomLeft / BottomRight → horizontal near-bottom line (treat as "Bottom").
//
// Side matching is CASE-INSENSITIVE and tolerant of surrounding
// whitespace. The CLI passes lowercase ("right"); the YAML holds
// canonical CamelCase ("TopLeft"); both must route to the right
// geometry. Without this normalization a lowercase "right" CLI arg
// silently falls through to the default (Left), so the bot would
// attack from the opposite side without complaining.
func autoEdgeLine(side string, w, h int) (image.Point, image.Point) {
	pct := func(p float64) int { return int(float64(w) * p) }
	pctH := func(p float64) int { return int(float64(h) * p) }
	side = strings.ToLower(strings.TrimSpace(side))
	switch side {
	case "top", "topleft", "topright":
		return image.Pt(pct(0.20), pctH(0.30)), image.Pt(pct(0.80), pctH(0.30))
	case "bottom", "bottomleft", "bottomright":
		return image.Pt(pct(0.20), pctH(0.70)), image.Pt(pct(0.80), pctH(0.70))
	case "right":
		return image.Pt(pct(0.85), pctH(0.20)), image.Pt(pct(0.85), pctH(0.80))
	case "left":
		return image.Pt(pct(0.15), pctH(0.20)), image.Pt(pct(0.15), pctH(0.80))
	default:
		// Unrecognized side — typically a typo ("rihgt", "rigt"). Surface
		// it loud rather than letting the bot silently attack from the
		// opposite flank. The fallback is still Left (preserves prior
		// behaviour) but the warning gives the user a chance to abort.
		// Listing the recognised keys inline turns a typo into a
		// one-glance fix instead of guessing from a JSON error.
		log.Warn().
			Str("requested_side", side).
			Strs("recognized", []string{"top", "bottom", "left", "right", "topleft", "topright", "bottomleft", "bottomright"}).
			Msg("autoEdgeLine: unrecognized target_edge; falling back to Left")
		return image.Pt(pct(0.15), pctH(0.20)), image.Pt(pct(0.15), pctH(0.80))
	}
}

// autoUnit picks one UnitEntry for an (already lowercased + trimmed)
// unit name, given the strategy's phase pattern/name and the chosen
// side's deploy line endpoints.
//
// heroIdx / heroTotal drive the per-phase hero spread: with 5 heroes
// on a single side, hero #1 lands at t=1/6, #2 at t=2/6, ..., #5 at
// t=5/6 along the chosen side's deploy line, instead of all five
// collapsing onto the same midpoint (which is the bug this auto mode
// exists to fix).
func autoUnit(name, pattern, phaseName string, p1, p2 image.Point, w, h int, heroIdx, heroTotal int) UnitEntry {
	e := UnitEntry{}
	isFourSides := pattern == "foursides"
	isPoint := pattern == "point" || pattern == ""
	isLine := pattern == "line"

	switch {
	case isFourSides:
		e.Type = "point"
		e.P = &Point{X: w / 2, Y: h / 2}
		e.Jitter = 5
	case isPoint && strings.Contains(phaseName, "siege"):
		e.Type = "point"
		e.P = &Point{X: w / 2, Y: h / 2}
		e.Jitter = 6
	case isPoint && strings.Contains(phaseName, "hero"):
		e.Type = "point"
		t := 0.5
		if heroTotal > 1 {
			t = float64(heroIdx+1) / float64(heroTotal+1)
		}
		mid := lerpPoint(p1, p2, t)
		e.P = &Point{X: mid.X, Y: mid.Y}
		// Slightly wider jitter to keep the CoC drop recognisable.
		e.Jitter = 6
	case isPoint:
		e.Type = "point"
		mid := lerpPoint(p1, p2, 0.5)
		e.P = &Point{X: mid.X, Y: mid.Y}
		e.Jitter = 5
	case isLine && strings.Contains(name, "rage"):
		// Rage 3+2 split: linear sub-lines overlapping slightly so the
		// player perceives a coherent rage "fan" rather than two
		// disjoint groups.
		e.Type = "lines"
		a := lerpPoint(p1, p2, 0.15)
		b := lerpPoint(p1, p2, 0.60)
		c := lerpPoint(p1, p2, 0.40)
		d := lerpPoint(p1, p2, 0.85)
		e.Lines = []LinePoint{
			{P1: Point{X: a.X, Y: a.Y}, P2: Point{X: b.X, Y: b.Y}, Count: 3, Jitter: 3},
			{P1: Point{X: c.X, Y: c.Y}, P2: Point{X: d.X, Y: d.Y}, Count: 2, Jitter: 3},
		}
	case isLine:
		e.Type = "line"
		e.P1 = &Point{X: p1.X, Y: p1.Y}
		e.P2 = &Point{X: p2.X, Y: p2.Y}
		e.Count = 10
		e.Jitter = 3
		if strings.Contains(name, "spell") {
			e.Count = 5
		}
	}
	return e
}

func lerpPoint(a, b image.Point, t float64) image.Point {
	return image.Pt(
		int(float64(a.X)*(1-t)+float64(b.X)*t),
		int(float64(a.Y)*(1-t)+float64(b.Y)*t),
	)
}

// ApplyScreenScale multiplies every coordinate in the formula by
// (dstW/srcW, dstH/srcH) so a JSON calibrated on one screen size still
// lands correctly on a different live resolution. Does NOT scale
// Count, Jitter, or Type. No-op when src dims are zero.
//
// Deprecated: this is the legacy "stretch to fill the framebuffer" model, and
// it is wrong for the game these formulas are authored against. CoC renders at
// a single uniform display scale about the viewport centre (docs/RESOLUTION.md
// item 1, verified live for HUD chrome at k=1.325 with 720p icon positions
// matching mapped predictions within 3 px). Between 860x732 and 1280x720 this
// function's factors are 1.4884 horizontally and 0.9836 vertically — a 34%
// anisotropy that moves an authored deploy point by 19-76 px, one to two
// village tiles, onto the wrong building. Use ProjectUniform with the
// calibration's display scale for anything that lands on the game world; this
// entry point survives only for the corner-mirror path in cmd/design_attack,
// which scales between two buffers of the same device.
func (f *Formula) ApplyScreenScale(srcW, srcH, dstW, dstH int) {
	if f == nil || srcW <= 0 || srcH <= 0 || dstW <= 0 || dstH <= 0 {
		return
	}
	sX := float64(dstW) / float64(srcW)
	sY := float64(dstH) / float64(srcH)
	for name, e := range f.Units {
		scalePoint(e.P, sX, sY)
		scalePoint(e.P1, sX, sY)
		scalePoint(e.P2, sX, sY)
		for i := range e.Lines {
			scalePoint(&e.Lines[i].P1, sX, sY)
			scalePoint(&e.Lines[i].P2, sX, sY)
		}
		f.Units[name] = e
	}
}

func scalePoint(p *Point, sX, sY float64) {
	if p == nil {
		return
	}
	p.X = int(float64(p.X) * sX)
	p.Y = int(float64(p.Y) * sY)
}

// ProjectUniform projects every coordinate from a formula authored on the
// src frame onto a live dst frame using a single display scale k about the two
// frames' centres: dst_centre + (p - src_centre) * k.
//
// This is the model the game actually renders with, which is why it takes k as
// an argument instead of deriving a ratio from the frame sizes: the display
// scale is a property of the device (device.display_scale, measured with
// cmd/resprobe), not of how many pixels the framebuffer happens to have. The
// same uniform scale applies on both axes, so a shape keeps its aspect, which
// is exactly what ApplyScreenScale's per-axis factors break.
//
// Coordinates are NOT clamped: a point that projects off-screen is a deploy
// point the bot cannot tap, and the caller is expected to report that rather
// than silently pin it to the frame edge.
//
// No-op when the formula is already in live coordinates (src == dst) or when
// any dimension or k is non-positive. The src == dst guard matters: formulas
// written by cmd/design_attack carry the live screen size, and re-scaling them
// by the device's k would multiply them for a second time.
func (f *Formula) ProjectUniform(k float64, srcW, srcH, dstW, dstH int) {
	if f == nil || k <= 0 || math.IsNaN(k) || math.IsInf(k, 0) {
		return
	}
	if srcW <= 0 || srcH <= 0 || dstW <= 0 || dstH <= 0 {
		return
	}
	if srcW == dstW && srcH == dstH {
		return
	}
	for name, e := range f.Units {
		projectPoint(e.P, k, srcW, srcH, dstW, dstH)
		projectPoint(e.P1, k, srcW, srcH, dstW, dstH)
		projectPoint(e.P2, k, srcW, srcH, dstW, dstH)
		for i := range e.Lines {
			projectPoint(&e.Lines[i].P1, k, srcW, srcH, dstW, dstH)
			projectPoint(&e.Lines[i].P2, k, srcW, srcH, dstW, dstH)
		}
		f.Units[name] = e
	}
}

// PushOutOfZone walks every coordinate radially away from (cx, cy) while
// `blocked` reports it sits in an area that cannot take a tap, and returns how
// many coordinates moved.
//
// It exists for the red deployment line. The band clamp above answers "is this
// point under the HUD", which is a frame-edge question; the red line is a
// shape in the middle of the frame — the game refuses a tap inside it with
// "You cannot deploy troops on the Red area!" and the unit stays in the bar.
// A user-pinned formula bypasses the red-zone-aware dynamic deploy line
// entirely, so a pinned line drawn inside the red boundary deploys nothing, and
// nothing in the log says why: the taps succeed, the game ignores them.
//
// Direction is radial from the zone's centre because that is the direction of
// the deployable strip on every edge, and stepping by whole tiles keeps the
// moved point on the geometry the user was aiming at rather than nudging it a
// few pixels inside the same rejected area. A point that cannot escape within
// maxSteps is left alone and counted as unmoved — silently relocating it across
// the village would be a worse lie than the refused tap.
func (f *Formula) PushOutOfZone(blocked func(x, y int) bool, cx, cy, step, maxSteps int) int {
	if f == nil || blocked == nil || step <= 0 || maxSteps <= 0 {
		return 0
	}
	moved := 0
	push := func(p *Point) {
		if p == nil || !blocked(p.X, p.Y) {
			return
		}
		dx := float64(p.X - cx)
		dy := float64(p.Y - cy)
		norm := math.Hypot(dx, dy)
		if norm < 1 {
			// A point on the centre has no radial direction; the safe
			// convention is straight outward along +x toward the right
			// edge, matching the dominant deploy side.
			dx, dy, norm = 1, 0, 1
		}
		ux, uy := dx/norm, dy/norm
		x, y := p.X, p.Y
		for i := 1; i <= maxSteps; i++ {
			nx := p.X + int(ux*float64(step*i))
			ny := p.Y + int(uy*float64(step*i))
			if nx == x && ny == y {
				continue
			}
			if !blocked(nx, ny) {
				p.X, p.Y = nx, ny
				moved++
				return
			}
		}
	}
	for name, e := range f.Units {
		push(e.P)
		push(e.P1)
		push(e.P2)
		for i := range e.Lines {
			push(&e.Lines[i].P1)
			push(&e.Lines[i].P2)
		}
		f.Units[name] = e
	}
	return moved
}

// ClampY pulls every coordinate's Y into [minY, maxY] and reports how many
// points moved. It exists because a formula authored on the reference geometry
// can project below the live deployable band even when the projection itself is
// correct: at 1280x720 the village view is shorter in reference terms, so an
// authored y of 580 lands at 645 on a 720px screen — under the troop bar at 624.
// Tapping there hits the HUD and the unit never deploys, which is the
// "it didn't use all the troops" failure; clamping keeps the unit in play at a
// slightly different spot instead. The count is returned so the caller can say
// so out loud, because the durable fix is re-authoring the point.
//
// A degenerate band (maxY <= minY) is ignored rather than collapsing every
// point onto one row.
func (f *Formula) ClampY(minY, maxY int) int {
	if f == nil || maxY <= minY {
		return 0
	}
	clamped := 0
	clampPoint := func(p *Point) {
		if p == nil {
			return
		}
		switch {
		case p.Y < minY:
			p.Y = minY
			clamped++
		case p.Y > maxY:
			p.Y = maxY
			clamped++
		}
	}
	for name, e := range f.Units {
		clampPoint(e.P)
		clampPoint(e.P1)
		clampPoint(e.P2)
		for i := range e.Lines {
			clampPoint(&e.Lines[i].P1)
			clampPoint(&e.Lines[i].P2)
		}
		f.Units[name] = e
	}
	return clamped
}

func projectPoint(p *Point, k float64, srcW, srcH, dstW, dstH int) {
	if p == nil {
		return
	}
	p.X = int(math.Round(float64(dstW)/2 + (float64(p.X)-float64(srcW)/2)*k))
	p.Y = int(math.Round(float64(dstH)/2 + (float64(p.Y)-float64(srcH)/2)*k))
}
