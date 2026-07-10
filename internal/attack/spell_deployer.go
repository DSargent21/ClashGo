package attack

import (
	"image"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/Ducky705/ClashGO/pkg/formula"
	"github.com/Ducky705/ClashGO/pkg/strategy"
	"github.com/rs/zerolog"
)

// SpellDeployer handles spell-specific deployment logic.
type SpellDeployer struct {
	executor *TapExecutor
	pCfg     PrecisionConfig
	formula  *formula.Formula
	w, h     int
	logger   zerolog.Logger
}

// NewSpellDeployer creates a new spell deployer. formula may be nil;
// when non-nil, a formula unit entry completely replaces the legacy
// pCfg.SpellEdges{A,B} / pCfg.SpellTargets / isRage-special logic so
// the user-pinned geometry wins.
func NewSpellDeployer(executor *TapExecutor, pCfg PrecisionConfig, formula *formula.Formula, w, h int, logger zerolog.Logger) *SpellDeployer {
	return &SpellDeployer{
		executor: executor,
		pCfg:     pCfg,
		formula:  formula,
		w:        w,
		h:        h,
		logger:   logger.With().Str("component", "spell_deployer").Logger(),
	}
}

// DeploySpell deploys a spell unit according to its pattern.
//
// Precedence:
//  1. Formula entry (if loaded). Replaces ALL legacy logic including
//     the isRage special-case so the user-pinned geometry wins.
//  2. FourSides legacy fallback.
//  3. Point pattern with a configured spell target.
//  4. Line pattern (or default): Line A for rage, Line B otherwise.
func (sd *SpellDeployer) DeploySpell(unit strategy.Unit, slot *TrackedSlot, targetEdge string, phasePattern string) bool {
	// Formula FIRST — when the user authored a formula for this spell
	// (e.g. "rage spell" → {"type":"lines","lines":[A,B]}) their
	// geometry completely replaces Line A/B pCfg and the isRage special.
	if entry, ok := sd.formulaEntry(unit.Name); ok {
		return sd.deployFromFormula(unit, slot, entry)
	}

	isFourSides := unit.Pattern == "FourSides" || phasePattern == "FourSides"

	if isFourSides {
		return sd.deployFourSides(unit, slot, targetEdge)
	}

	isPointPattern := unit.Pattern == "Point" || phasePattern == "Point"
	spellTarget, hasTarget := sd.pCfg.SpellTargets[targetEdge]

	if isPointPattern && hasTarget {
		return sd.deployPointSpell(unit, slot, spellTarget)
	}

	return sd.deployLineSpell(unit, slot, targetEdge)
}

// deployFourSides deploys spells around all 4 edges.
func (sd *SpellDeployer) deployFourSides(unit strategy.Unit, _ *TrackedSlot, targetEdge string) bool {
	edges := []string{"TopRight", "BottomRight", "BottomLeft", "TopLeft"}

	for _, edgeName := range edges {
		sd.logger.Info().Str("unit", unit.Name).Str("edge", edgeName).Msg("FourSides spell deployment")

		if targetPt, okT := sd.pCfg.SpellTargets[edgeName]; okT {
			// Deploy around spell target point
			for j := 0; j < 4; j++ {
				angle := float64(j) * 2.0 * math.Pi / 4.0
				radius := 18.0 * sd.executor.cal.ScaleX
				tx := targetPt.X + int(radius*math.Cos(angle))
				ty := targetPt.Y + int(radius*math.Sin(angle))
				jPt := sd.executor.addJitter(image.Pt(tx, ty), 6)
				sd.executor.client.TapFast(jPt.X, jPt.Y, 8.0)
				time.Sleep(50 * time.Millisecond)
			}
		} else {
			// Legacy fallback to SpellEdgesB
			edge, ok := sd.pCfg.SpellEdgesB[edgeName]
			if !ok {
				edge, ok = sd.pCfg.SpellEdgesA[edgeName]
				if !ok {
					edge, _ = sd.pCfg.Edges[edgeName]
				}
			}

			p1, p2 := edge.P1, edge.P2

			// Apply inward offset
			off := unit.Offset
			if off == 0 {
				off = 0 // Phase offset handled elsewhere
			}
			if off > 0 {
				centerX, centerY := sd.w/2, sd.h/2
				pct := float64(off) / 300.0
				p1 = image.Pt(int(float64(p1.X)+float64(centerX-p1.X)*pct), int(float64(p1.Y)+float64(centerY-p1.Y)*pct))
				p2 = image.Pt(int(float64(p2.X)+float64(centerX-p2.X)*pct), int(float64(p2.Y)+float64(centerY-p2.Y)*pct))
			}
			sd.logger.Debug().Interface("p1", p1).Interface("p2", p2).Msg("FourSides spell edge coords")

			for j := 0; j < 4; j++ {
				pct := float64(j) / 3.0
				tx := int(float64(p1.X) + float64(p2.X-p1.X)*pct)
				ty := int(float64(p1.Y) + float64(p2.Y-p1.Y)*pct)
				jPt := sd.executor.addJitter(image.Pt(tx, ty), 6)
				sd.executor.client.TapFast(jPt.X, jPt.Y, 8.0)
				time.Sleep(50 * time.Millisecond)
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	time.Sleep(200 * time.Millisecond)
	return true
}

// deployPointSpell deploys spells clustered around a point.
func (sd *SpellDeployer) deployPointSpell(unit strategy.Unit, slot *TrackedSlot, spellTarget image.Point) bool {
	maxSpells := sd.resolveSpellCount(unit)

	sd.logger.Info().
		Str("unit", unit.Name).
		Interface("point", spellTarget).
		Int("count", maxSpells).
		Msg("deploying spells clustered around point target")

	points := make([]image.Point, 0, maxSpells)
	for i := 0; i < maxSpells; i++ {
		var offset image.Point
		if maxSpells > 1 {
			angle := float64(i) * 2.0 * math.Pi / float64(maxSpells)
			radius := 18.0 * sd.executor.cal.ScaleX
			offset = image.Pt(int(radius*math.Cos(angle)), int(radius*math.Sin(angle)))
		}
		pt := image.Pt(spellTarget.X+offset.X, spellTarget.Y+offset.Y)
		points = append(points, sd.executor.addJitter(pt, 6))
	}

	for idx, pt := range points {
		sd.logger.Info().Str("unit", unit.Name).Int("idx", idx).Interface("pt", pt).Msg("tapping spell target point")
		sd.executor.client.TapFast(pt.X, pt.Y, 8.0)
		if idx < len(points)-1 {
			sd.executor.client.HumanSleep(80, 20)
		}
	}
	return true
}

// deployLineSpell deploys spells along a line (Line A for rage, Line B for others).
func (sd *SpellDeployer) deployLineSpell(unit strategy.Unit, slot *TrackedSlot, targetEdge string) bool {
	unitName := strings.ToLower(strings.TrimSpace(unit.Name))
	isRage := strings.Contains(unitName, "rage")

	var edge ManualEdge
	var ok bool
	var selectedLine string

	if isRage {
		edge, ok = sd.pCfg.SpellEdgesA[targetEdge]
		selectedLine = "SpellEdgesA (Line A)"
		if !ok {
			edge, ok = sd.pCfg.SpellEdgesB[targetEdge]
			selectedLine = "SpellEdgesB (Line B fallback)"
		}
	} else {
		edge, ok = sd.pCfg.SpellEdgesB[targetEdge]
		selectedLine = "SpellEdgesB (Line B)"
		if !ok {
			edge, ok = sd.pCfg.SpellEdgesA[targetEdge]
			selectedLine = "SpellEdgesA (Line A fallback)"
		}
	}

	sd.logger.Info().
		Str("unit", unit.Name).
		Str("line", selectedLine).
		Bool("found", ok).
		Interface("coords", edge).
		Msg("routing spell to deployment line")

	if !ok {
		sd.logger.Warn().Str("unit", unit.Name).Msg("no spell deployment line configured for edge")
		return false
	}

	maxSpells := sd.resolveSpellCount(unit)

	// Special layout for 5 rage spells: 3 on Line A, 2 on Line B
	if isRage && maxSpells == 5 {
		edgeA, okA := sd.pCfg.SpellEdgesA[targetEdge]
		edgeB, okB := sd.pCfg.SpellEdgesB[targetEdge]
		if okA && okB {
			return sd.deployRageSpecial(slot, edgeA, edgeB)
		}
	}

	p1, p2 := edge.P1, edge.P2

	// Apply offset
	off := unit.Offset
	if off > 0 {
		centerX, centerY := sd.w/2, sd.h/2
		pct := float64(off)/150.0 + 0.12
		p1 = image.Pt(int(float64(p1.X)+float64(centerX-p1.X)*pct), int(float64(p1.Y)+float64(centerY-p1.Y)*pct))
		p2 = image.Pt(int(float64(p2.X)+float64(centerX-p2.X)*pct), int(float64(p2.Y)+float64(centerY-p2.Y)*pct))
		sd.logger.Debug().Interface("offset_p1", p1).Interface("offset_p2", p2).Msg("applied inward offset")
	}

	// Distribute spells along line
	points := make([]image.Point, 0, maxSpells)
	for i := 0; i < maxSpells; i++ {
		pct := 0.5
		if maxSpells > 1 {
			pct = 0.22 + float64(i)*0.56/float64(maxSpells-1)
		}
		tx := int(float64(p1.X) + float64(p2.X-p1.X)*pct)
		ty := int(float64(p1.Y) + float64(p2.Y-p1.Y)*pct)
		points = append(points, sd.executor.addJitter(image.Pt(tx, ty), 8))
	}

	for idx, pt := range points {
		sd.logger.Info().Str("unit", unit.Name).Int("idx", idx).Interface("pt", pt).Msg("tapping spell target line")
		sd.executor.client.TapFast(pt.X, pt.Y, 8.0)
		if idx < len(points)-1 {
			sd.executor.client.HumanSleep(80, 20)
		}
	}
	return true
}

// deployRageSpecial deploys 5 rage spells: 3 on Line A, 2 on Line B.
//
// Histrionically bots have silently dropped the last Line B tap because the
// "if idx < len(pointsB)-1" sleep guard skipped the final inter-tap delay
// and the slot re-select fired before the second Line B tap registered.
// Make every tap loud (log + count), use a consistent 180ms delay between
// taps, and log the final count so a missed spell becomes visible in the
// tap log instead of vanishing.
func (sd *SpellDeployer) deployRageSpecial(slot *TrackedSlot, edgeA, edgeB ManualEdge) bool {
	sd.logger.Info().Msg("deploying rage spells: 3 on Line A, 2 on Line B")

	p1A, p2A := edgeA.P1, edgeA.P2
	pointsA := []image.Point{
		sd.executor.addJitter(image.Pt(int(float64(p1A.X)+float64(p2A.X-p1A.X)*0.10), int(float64(p1A.Y)+float64(p2A.Y-p1A.Y)*0.10)), 8),
		sd.executor.addJitter(image.Pt(int(float64(p1A.X)+float64(p2A.X-p1A.X)*0.50), int(float64(p1A.Y)+float64(p2A.Y-p1A.Y)*0.50)), 8),
		sd.executor.addJitter(image.Pt(int(float64(p1A.X)+float64(p2A.X-p1A.X)*0.90), int(float64(p1A.Y)+float64(p2A.Y-p1A.Y)*0.90)), 8),
	}

	p1B, p2B := edgeB.P1, edgeB.P2
	pointsB := []image.Point{
		sd.executor.addJitter(image.Pt(int(float64(p1B.X)+float64(p2B.X-p1B.X)*0.10), int(float64(p1B.Y)+float64(p2B.Y-p1B.Y)*0.10)), 8),
		sd.executor.addJitter(image.Pt(int(float64(p1B.X)+float64(p2B.X-p1B.X)*0.90), int(float64(p1B.Y)+float64(p2B.Y-p1B.Y)*0.90)), 8),
	}

	deployed := 0
	for idx, pt := range pointsA {
		sd.logger.Info().Int("idx", idx).Interface("pt", pt).Msg("tapping rage spell Line A")
		sd.executor.client.TapFast(pt.X, pt.Y, 8.0)
		sd.executor.client.HumanSleep(80, 20)
		deployed++
	}

	// Re-select the rage spell slot before the Line B drop. CoC needs a
	// real re-select or the second drop falls on an empty cursor.
	sd.executor.client.HumanSleep(80, 20)
	sd.executor.TapSlot(slot, 8)
	sd.executor.client.HumanSleep(80, 20)
	sd.logger.Debug().
		Str("unit", slot.UnitName).
		Int("slot_x", slot.X).
		Int("slot_y", slot.Y).
		Msg("re-selected slot for Line B rage drops")

	// Always-delaying inter-tap loop. No conditional sleep on the last
	// iteration — that race condition was what dropped Line B tap #2.
	for idx, pt := range pointsB {
		sd.logger.Info().Int("idx", idx+3).Interface("pt", pt).Msg("tapping rage spell Line B")
		sd.executor.client.TapFast(pt.X, pt.Y, 8.0)
		deployed++
		sd.executor.client.HumanSleep(80, 20)
	}

	sd.logger.Info().Int("wanted", len(pointsA)+len(pointsB)).Int("delivered", deployed).Msg("rage spells deployment complete")

	if deployed < len(pointsA)+len(pointsB) {
		sd.logger.Warn().
			Int("wanted", len(pointsA)+len(pointsB)).
			Int("delivered", deployed).
			Msg("RAGE SPECIAL: deployed fewer spells than requested")
	}
	return true
}

// resolveSpellCount returns the number of spells to deploy.
func (sd *SpellDeployer) resolveSpellCount(unit strategy.Unit) int {
	if unit.Amount != "All" && unit.Amount != "" {
		if val, err := strconv.Atoi(unit.Amount); err == nil {
			return val
		}
	}
	return 5 // Default fallback
}

// formulaEntry is a nil-safe wrapper for the deploy path. Returns
// (_, false) when no formula was loaded, so the caller falls back to
// pCfg.SpellEdges{A,B} / pCfg.SpellTargets.
func (sd *SpellDeployer) formulaEntry(unitName string) (formula.UnitEntry, bool) {
	if sd.formula == nil {
		return formula.UnitEntry{}, false
	}
	return sd.formula.LookUp(unitName)
}

// deployFromFormula uses the user-pinned line/point/lines coordinates
// from the formula. This completely replaces the legacy isRage special
// handling — the user owns the geometry now.
//
// Supported entry shapes:
//
//	"point" — clusters maxSpells around P (mirrors deployPointSpell ring).
//	"line"  — distributes (entry.Count or maxSpells) along P1→P2.
//	"lines" — for each LinePoint taps Count taps along P1→P2 with jitter.
func (sd *SpellDeployer) deployFromFormula(unit strategy.Unit, slot *TrackedSlot, entry formula.UnitEntry) bool {
	switch {
	case entry.IsLines() && len(entry.Lines) > 0:
		return sd.deployFormulaLines(unit, slot, entry.Lines)
	case entry.IsPoint() && entry.P != nil:
		return sd.deployFormulaPoint(unit, slot, entry)
	case entry.IsLine() && entry.P1 != nil && entry.P2 != nil:
		return sd.deployFormulaLine(unit, slot, entry)
	}
	sd.logger.Warn().
		Str("unit", unit.Name).
		Str("type", entry.Type).
		Msg("formula entry present but has no usable coordinates; falling back to legacy pCfg path")
	return false
}

// deployFormulaPoint clusters maxSpells taps in a ring around the
// user-pinned point, same as the legacy deployPointSpell ring logic.
func (sd *SpellDeployer) deployFormulaPoint(unit strategy.Unit, _ *TrackedSlot, entry formula.UnitEntry) bool {
	maxSpells := sd.resolveSpellCount(unit)
	if entry.Count > 0 {
		maxSpells = entry.Count
	}
	center := entry.P.Image()
	jitter := entry.Jitter
	if jitter <= 0 {
		jitter = 6
	}

	sd.logger.Info().
		Str("unit", unit.Name).
		Str("formula_kind", "point").
		Int("count", maxSpells).
		Interface("p", center).
		Msg("formula-driven spell point deploy")

	points := make([]image.Point, 0, maxSpells)
	for i := 0; i < maxSpells; i++ {
		var offset image.Point
		if maxSpells > 1 {
			angle := float64(i) * 2.0 * math.Pi / float64(maxSpells)
			radius := 18.0 * sd.executor.cal.ScaleX
			offset = image.Pt(int(radius*math.Cos(angle)), int(radius*math.Sin(angle)))
		}
		pt := image.Pt(center.X+offset.X, center.Y+offset.Y)
		points = append(points, sd.executor.addJitter(pt, jitter))
	}

	sd.tapSeries(unit, points, jitter, "formula point")
	return true
}

// deployFormulaLine distributes taps along the user-pinned line.
// Count precedence: entry.Count → resolveSpellCount(unit).
//
// Rage auto-split: when the unit is a Rage spell and count >= 2, the
// function fires half the taps on the user's outer line and the rest
// on a parallel line shifted INWARD toward the screen center. This
// matches the canonical edrag-rush formula (3 rage on the entry line
// + 2 rage deeper in) without forcing the user to author an explicit
// "lines" entry. Non-rage spells still draw all taps on the single
// pinned line, preserving prior behavior for freeze / invisibility /
// heal / jump / etc. Override for rage by authoring a "lines" entry
// in the formula — when entry.Type == "lines" the path takes the
// explicit branches above and bypasses this auto-split.
func (sd *SpellDeployer) deployFormulaLine(unit strategy.Unit, _ *TrackedSlot, entry formula.UnitEntry) bool {
	p1 := entry.P1.Image()
	p2 := entry.P2.Image()
	count := sd.resolveSpellCount(unit)
	if entry.Count > 0 {
		count = entry.Count
	}
	jitter := entry.Jitter
	if jitter <= 0 {
		jitter = 8
	}

	unitName := strings.ToLower(strings.TrimSpace(unit.Name))
	isRage := strings.Contains(unitName, "rage")

	sd.logger.Info().
		Str("unit", unit.Name).
		Str("formula_kind", "line").
		Bool("auto_split_rage", isRage && count >= 2).
		Int("count", count).
		Interface("p1", p1).
		Interface("p2", p2).
		Msg("formula-driven spell line deploy")

	if isRage && count >= 2 {
		// Split: outer line gets any odd-rounding remainder; inner
		// line gets count/2.
		outer := count - count/2
		inner := count / 2

		// The inner line comes from EITHER:
		//  (a) the user's `_rage_inner` formula entry from the
		//      picker's 13th step — user explicitly pinned it, OR
		//  (b) auto-derived offset of the user's outer line.
		// Option (a) wins so the user can override the auto-derive.
		var p1In, p2In image.Point
		var innerSource string
		if sd.formula != nil {
			if innerEntry, ok := sd.formula.LookUp("_rage_inner"); ok && innerEntry.IsLine() && innerEntry.P1 != nil && innerEntry.P2 != nil {
				p1In = innerEntry.P1.Image()
				p2In = innerEntry.P2.Image()
				innerSource = "user_pinned (_rage_inner)"
			}
		}
		if !innerSourceSet(innerSource) {
			// Default inward offset ~35px. On an 860x732 screen this is
			// ~2 tiles deeper toward the base center, matching the
			// "outerline = entry, innerline = behind the entry wall"
			// canonical edrag-rush layout.
			p1In, p2In = deriveInwardLine(p1, p2, sd.w, sd.h, 35)
			innerSource = "auto_derive (deriveInwardLine)"
		}

		sd.logger.Info().
			Str("unit", unit.Name).
			Int("outer", outer).
			Int("inner", inner).
			Str("inner_source", innerSource).
			Interface("p1_in", p1In).
			Interface("p2_in", p2In).
			Msg("rage split: outer line + inner line")

		outerPts := sd.distributeAlong(p1, p2, outer, jitter)
		sd.tapSeries(unit, outerPts, jitter, "formula line outer")

		// Brief settle between sub-lines gives CoC time to recognize
		// the cursor hasn't desynced before the second drop. Slot
		// re-select is handled upstream by the DeploySpell wrapper.
		sd.executor.client.HumanSleep(80, 20)
		innerPts := sd.distributeAlong(p1In, p2In, inner, jitter)
		sd.tapSeries(unit, innerPts, jitter, "formula line inner")
		return true
	}

	points := sd.distributeAlong(p1, p2, count, jitter)
	sd.tapSeries(unit, points, jitter, "formula line")
	return true
}

// innerSourceSet is a tiny sentinel-style helper. Avoids importing
// `unicode` just to check non-empty. Used only by the rage branch in
// deployFormulaLine above to detect whether the `_rage_inner` formula
// entry was found before falling through to auto-derive.
func innerSourceSet(s string) bool { return s != "" }

// deriveInwardLine returns a parallel-shifted copy of (p1,p2) displaced
// INWARD — perpendicular to the line, in the direction of the screen
// center. Used by the Rage auto-split to compute the "second line
// further in" the user picked when authoring a single Rage line.
//
// Math: take the line's perpendicular (-dy, dx), project the
// center-to-midpoint vector onto it, and shift both endpoints by the
// projection. If the projection near-zero (the line passes through the
// center exactly), we fall back to a pure perpendicular shift in the
// arbitrary +perp direction (still a real, parallel line; just chosen
// as a stable default rather than re-deriving an arbitrary tangent).
func deriveInwardLine(p1, p2 image.Point, screenW, screenH, offsetPx int) (image.Point, image.Point) {
	dx := float64(p2.X - p1.X)
	dy := float64(p2.Y - p1.Y)
	length := math.Hypot(dx, dy)
	if length < 1.0 {
		// Degenerate (P1 == P2): just push straight down toward the
		// base center.
		midX, midY := float64(p1.X), float64(p1.Y)
		dcx := float64(screenW)/2.0 - midX
		dcy := float64(screenH)/2.0 - midY
		n := math.Hypot(dcx, dcy)
		if n < 1.0 {
			return p1, p2
		}
		shX := dcx / n * float64(offsetPx)
		shY := dcy / n * float64(offsetPx)
		return image.Pt(p1.X+int(math.Round(shX)), p1.Y+int(math.Round(shY))),
			image.Pt(p2.X+int(math.Round(shX)), p2.Y+int(math.Round(shY)))
	}

	// Perpendicular to the line direction.
	px, py := -dy, dx

	// Vector from line midpoint to screen center.
	midX, midY := float64(p1.X+p2.X)/2.0, float64(p1.Y+p2.Y)/2.0
	dcx := float64(screenW)/2.0 - midX
	dcy := float64(screenH)/2.0 - midY

	// Sign the perpendicular so it points inward. Dot<0 means the
	// natural perp points AWAY from the center.
	dot := dcx*px + dcy*py
	if dot < 0 {
		px, py = -px, -py
	}

	// Normalize and scale by offset.
	shX := px / length * float64(offsetPx)
	shY := py / length * float64(offsetPx)

	p1Out := image.Pt(p1.X+int(math.Round(shX)), p1.Y+int(math.Round(shY)))
	p2Out := image.Pt(p2.X+int(math.Round(shX)), p2.Y+int(math.Round(shY)))
	return p1Out, p2Out
}

// deployFormulaLines is the rage-style 3+2 path. Each LinePoint owns
// its own per-line tap count, jitter, and geometry — the user controls
// everything.
//
// Re-selects the slot between sub-lines so each LinePoint drop has a
// fresh "the slot owns the cursor" window (same bug-fix as the legacy
// deployRageSpecial re-select). Sub-lines with Count==0 are skipped
// (lets the user opt-in to e.g. only the Line A portion).
func (sd *SpellDeployer) deployFormulaLines(unit strategy.Unit, slot *TrackedSlot, lines []formula.LinePoint) bool {
	sd.logger.Info().
		Str("unit", unit.Name).
		Str("formula_kind", "lines").
		Int("sub_lines", len(lines)).
		Msg("formula-driven spell lines deploy")

	deployed := 0
	wanted := 0
	for li, lp := range lines {
		if lp.Count <= 0 {
			sd.logger.Info().Int("idx", li).Msg("formula sub-line count=0; skipping")
			continue
		}
		wanted += lp.Count
		jitter := lp.Jitter
		if jitter <= 0 {
			jitter = 8
		}
		p1 := lp.P1.Image()
		p2 := lp.P2.Image()
		points := sd.distributeAlong(p1, p2, lp.Count, jitter)

		if li > 0 {
			// Re-select between sub-lines (mirrors deployRageSpecial).
			sd.executor.client.HumanSleep(80, 20)
			sd.executor.TapSlot(slot, 8)
			sd.executor.client.HumanSleep(80, 20)
		}
		for idx, pt := range points {
			sd.logger.Info().
				Int("sub_idx", li).
				Int("idx", idx+deployed).
				Interface("pt", pt).
				Msg("tapping formula lines spell")
			sd.executor.client.TapFast(pt.X, pt.Y, 8.0)
			deployed++
			sd.executor.client.HumanSleep(80, 20)
		}
	}

	sd.logger.Info().
		Int("wanted", wanted).
		Int("delivered", deployed).
		Msg("formula lines: spell deployment complete")

	if deployed < wanted {
		sd.logger.Warn().
			Int("wanted", wanted).
			Int("delivered", deployed).
			Msg("FORMULA LINES: deployed fewer spells than requested")
	}
	return true
}

// distributeAlong produces `count` evenly-spaced jittered points
// between p1 and p2 (with end-anchored distribution: pct goes 0.1 → 0.9
// when count==2, similar to the legacy deployLineSpell).
func (sd *SpellDeployer) distributeAlong(p1, p2 image.Point, count, jitter int) []image.Point {
	if count <= 1 {
		return []image.Point{sd.executor.addJitter(p1, jitter)}
	}
	pts := make([]image.Point, 0, count)
	for i := 0; i < count; i++ {
		pct := 0.22 + float64(i)*0.56/float64(count-1)
		tx := int(float64(p1.X) + float64(p2.X-p1.X)*pct)
		ty := int(float64(p1.Y) + float64(p2.Y-p1.Y)*pct)
		pts = append(pts, sd.executor.addJitter(image.Pt(tx, ty), jitter))
	}
	return pts
}

// tapSeries taps each point in `points` with 180ms +/- 40ms inter-tap
// sleeps. Always delays between taps (no "skip on last" bug from legacy
// deployLineSpell).
func (sd *SpellDeployer) tapSeries(unit strategy.Unit, points []image.Point, _ int, kind string) {
	for idx, pt := range points {
		sd.logger.Info().
			Str("unit", unit.Name).
			Str("series", kind).
			Int("idx", idx).
			Interface("pt", pt).
			Msg("tapping spell formula point")
		sd.executor.client.TapFast(pt.X, pt.Y, 8.0)
		sd.executor.client.HumanSleep(80, 20)
	}
}
