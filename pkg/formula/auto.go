package formula

import (
	"strings"

	"github.com/Ducky705/ClashGO/pkg/strategy"
	"github.com/rs/zerolog/log"
	"image"
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
// Callers should pass formulaPtr.Screen.W/H as the source dims (the
// dimensions the formula was authored against) and the live screen
// dims as the destination.
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
