// plan.go computes the deployment plan for a strategy against a single
// screenshot WITHOUT issuing any taps. The math mirrors what the live
// Executor / HeroManager / SpellDeployer would do given the same
// (PrecisionConfig, RedZone, DeployLine, TargetEdge) inputs, so the
// output reveals what the bot WOULD have done — invaluable for
// debugging "attacks in the corner on 2 sides" regressions without
// burning live attacks.
//
// Pure compute, no I/O. The validator command reads a PNG, builds a
// Planner, calls Plan() once, and renders the result. No ADB, no
// goroutines, no time.Sleep.
package attack

import (
	"fmt"
	"image"
	"math"
	"strings"

	"github.com/Ducky705/ClashGO/pkg/strategy"
)

// PlanTap captures ONE planned tap point and its side-classification.
//
// Side is computed by SideOfPoint against the live screen dims so the
// validator can flag every tap that lands off the target edge's compass
// direction (e.g. a tap at (430, 700) classified "bottom" while the
// target is "TopLeft" — a real bug). Note records WHY this tap was
// planned ("troop-line", "hero-p1", "foursides", "duke-chosen") so
// surfacing the bug maps back to the deployUnit / FourSides / Hero
// branch that emitted it.
type PlanTap struct {
	Unit       string `json:"unit"`
	Phase      string `json:"phase"`
	TargetEdge string `json:"target_edge"`
	X          int    `json:"x"`
	Y          int    `json:"y"`
	Side       string `json:"side"`
	MatchSide  bool   `json:"match_side"`
	Note       string `json:"note"`
}

// PlanPhaseSummary rolls up the per-unit taps for one YAML phase.
type PlanPhaseSummary struct {
	Name       string    `json:"name"`
	TargetEdge string    `json:"target_edge"`
	Pattern    string    `json:"pattern"`
	Taps       []PlanTap `json:"taps"`
}

// PlanMismatch is a single off-side tap. The validator highlights these
// in red on the overlay so the user can see exactly which unit strayed.
type PlanMismatch struct {
	Unit          string   `json:"unit"`
	Phase         string   `json:"phase"`
	TargetEdge    string   `json:"target_edge"`
	TapSide       string   `json:"tap_side"`
	ExpectedSides []string `json:"expected_sides"`
	X             int      `json:"x"`
	Y             int      `json:"y"`
	Note          string   `json:"note"`
}

// PlanReport is the top-level validator output. Marshal to JSON for
// the plan.json artifact.
type PlanReport struct {
	Screen struct {
		W int `json:"w"`
		H int `json:"h"`
	} `json:"screen"`
	RedZoneValid     bool             `json:"red_zone_valid"`
	RedZoneBBox      image.Rectangle  `json:"red_zone_bbox"`
	DeploySide       string           `json:"deploy_side"`
	DecidedTargetEdge string          `json:"decided_target_edge"`
	Corners          map[string]image.Rectangle `json:"corners_after_override"`
	Phases           []PlanPhaseSummary        `json:"phases"`
	Mismatches       []PlanMismatch            `json:"mismatches"`

	// DiagonalCorners lists every pCfg.Edges key whose endpoint pair
	// spans MULTIPLE screen sides. This is the actual signal the user
	// is hunting — a "pinpointed" line like BottomLeft=(92,411)→(300,564)
	// is all-bottom-classified by SideOfPoint, so per-tap classification
	// alone misses the bug. DiagonalCorners surfaces the geometry
	// directly: P1 is at (92,411) on screen-side "bottom", P2 is at
	// (300,564) ALSO on screen-side "bottom". Both match semi-axial
	// half-screen classification, but the LINE ANGLE (atan2(208,153)
	// ≈ 53°) tells the user "this is a diagonal line, your troops
	// will visibly scatter across both left and bottom halves of the
	// screen even though per-tap classification says 'all green'".
	DiagonalCorners []DiagonalFlag `json:"diagonal_corners"`
}

// DiagonalFlag captures one pCfg.Edges entry whose endpoints sit on
// different screen halves. surfacing "EndpointA is on screen-side X,
// EndpointB is on screen-side Y" lets the user see "my pinned line is
// actually a diagonal — that's why troops scatter" without hand-tracing
// JSON coords.
type DiagonalFlag struct {
	Key         string `json:"key"`
	P1X         int    `json:"p1_x"`
	P1Y         int    `json:"p1_y"`
	P1Side      string `json:"p1_side"`
	P2X         int    `json:"p2_x"`
	P2Y         int    `json:"p2_y"`
	P2Side      string `json:"p2_side"`
	AngleDeg    int    `json:"angle_deg"`
	AngleReason string `json:"angle_reason"` // "diagonal", "horizontal", "vertical"
}

// SideOfPoint classifies a screen coordinate into ONE of the four
// compass directions ("top", "right", "bottom", "left") by a strict
// half-screen rule: the Y axis wins, X is the tiebreaker. For points
// in the bottom half of the screen, the result is "bottom" regardless
// of where on the X axis they land. For points exactly on the
// horizontal midline (rare), the X axis decides.
//
// Why strict half-screen rather than majority-dominance: it matches
// the user's intuition ("a tap at y=420 is on the BOTTOM half"). A
// diagonal corner line (92,411)→(300,564) is then ENTIRELY in the
// bottom half — taps classify as "bottom" — and all match
// SidesForCorner("BottomLeft"). The diagonality of the LINE itself
// is surfaced separately via DiagonalCorners in PlanReport below so
// configuration errors aren't hidden behind per-tap "all-green" runs.
func SideOfPoint(x, y, w, h int) string {
	midY := h / 2
	if y < midY {
		return "top"
	}
	if y > midY {
		return "bottom"
	}
	midX := w / 2
	if x < midX {
		return "left"
	}
	return "right"
}

// SidesForCorner maps a legacy corner key to its compass-direction
// envelope so MatchSide becomes a set-membership check. Corners are
// inherently ambiguous (TopLeft is BOTH top AND left); we surface
// both and the validator's diff count shows whether a tap landed
// off the chosen corner entirely.
//
// Strict-side keys (top/right/bottom/left) map to single-element
// envelopes; anything else maps to an empty envelope (the report
// flags ALL taps as mismatched — caught at the cfg loader).
func SidesForCorner(edge string) []string {
	switch strings.ToLower(strings.TrimSpace(edge)) {
	case "topleft":
		return []string{"top", "left"}
	case "topright":
		return []string{"top", "right"}
	case "bottomleft":
		return []string{"bottom", "left"}
	case "bottomright":
		return []string{"bottom", "right"}
	case "top":
		return []string{"top"}
	case "right":
		return []string{"right"}
	case "bottom":
		return []string{"bottom"}
	case "left":
		return []string{"left"}
	default:
		return []string{}
	}
}

// Planner holds the immutable inputs needed to plan a deployment.
type Planner struct {
	PCfg      PrecisionConfig
	Strategy  *strategy.DynamicStrategy
	RedZone   RedZone
	DeployLine DeployLine
	TargetEdge string
	W, H       int
}

// NewPlanner assembles the inputs. TargetEdge is the resolved edge
// ("Random" must already be picked by the caller), and PCfg must be
// post-orchestrator-override (DeployDynamicV2 writes the red-zone
// line into all 4 corner keys before deployment, so the validator
// does the same — see cmd/validate_strategy/main.go).
func NewPlanner(pCfg PrecisionConfig, s *strategy.DynamicStrategy, redZone RedZone, line DeployLine, targetEdge string, w, h int) *Planner {
	return &Planner{
		PCfg:       pCfg,
		Strategy:   s,
		RedZone:    redZone,
		DeployLine: line,
		TargetEdge: targetEdge,
		W:          w,
		H:          h,
	}
}

// Plan walks every (phase, unit) tuple, computes the planned taps,
// classifies each by SideOfPoint, and folds them into PlanReport.
// Taps whose Side is not in SidesForCorner(TargetEdge) are added
// to Mismatches AND kept in their phase's Taps (so the overlay can
// render every tap, color-coded).
func (p *Planner) Plan() PlanReport {
	var rep PlanReport
	rep.Screen.W = p.W
	rep.Screen.H = p.H
	rep.RedZoneValid = p.RedZone.Valid
	rep.RedZoneBBox = p.RedZone.BBox
	rep.DeploySide = p.DeployLine.Side
	rep.DecidedTargetEdge = p.TargetEdge
	rep.Corners = make(map[string]image.Rectangle)
	for k, e := range p.PCfg.Edges {
		r := image.Rect(e.P1.X, e.P1.Y, e.P2.X, e.P2.Y)
		rep.Corners[k] = r
		rep.DiagonalCorners = append(rep.DiagonalCorners, classifyLineAngle(k, e.P1, e.P2, p.W, p.H))
	}
	if p.Strategy == nil {
		return rep
	}
	for _, ph := range p.Strategy.Phases {
		ps := PlanPhaseSummary{
			Name:       ph.Name,
			TargetEdge: p.TargetEdge,
			Pattern:    ph.Pattern,
		}
		for _, unit := range ph.Units {
			taps := p.planUnit(unit, ph)
			for _, t := range taps {
				if !t.MatchSide {
					rep.Mismatches = append(rep.Mismatches, PlanMismatch{
						Unit:          t.Unit,
						Phase:         t.Phase,
						TargetEdge:    t.TargetEdge,
						TapSide:       t.Side,
						ExpectedSides: SidesForCorner(p.TargetEdge),
						X:             t.X,
						Y:             t.Y,
						Note:          t.Note,
					})
				}
			}
			ps.Taps = append(ps.Taps, taps...)
		}
		rep.Phases = append(rep.Phases, ps)
	}
	return rep
}

// classifyLineAngle returns a DiagonalFlag describing whether a corner
// config's two endpoints sit on the same screen-side (clean line) or
// different sides (diagnonal — the user's actual symptom). The
// ANGLE_BUCKET captures slope-as-intuition: 0°=horizontal (clean
// bottom/top line), 90°=vertical (clean left/right line), 45°=perfect
// diagonal, anywhere outside ±15° of those = "diagonal" flag.
func classifyLineAngle(key string, p1, p2 image.Point, w, h int) DiagonalFlag {
	dx := float64(p2.X - p1.X)
	dy := float64(p2.Y - p1.Y)
	angle := 0.0
	if dx != 0 {
		angle = absFloat(dy / dx)
	}
	// atan2 gives -π/2..π/2 for slope. Convert to degrees 0..90.
	deg := 0
	reason := "horizontal"
	if dx == 0 {
		deg = 90
		reason = "vertical"
	} else {
		// math.Atan2(dy, dx) is what we want; angle 0 = horizontal,
		// 90 = vertical, 45 = diagonal. degrees = |atan2(dy,dx)| * 180/π.
		fdeg := math.Abs(math.Atan2(dy, dx)) * 180.0 / math.Pi
		deg = int(fdeg)
		switch {
		case deg <= 15:
			reason = "horizontal"
		case deg >= 75:
			reason = "vertical"
		case deg >= 30 && deg <= 60:
			reason = "diagonal"
		default:
			reason = "off-axis"
		}
		_ = angle
	}
	return DiagonalFlag{
		Key:         key,
		P1X:         p1.X,
		P1Y:         p1.Y,
		P1Side:      SideOfPoint(p1.X, p1.Y, w, h),
		P2X:         p2.X,
		P2Y:         p2.Y,
		P2Side:      SideOfPoint(p2.X, p2.Y, w, h),
		AngleDeg:    deg,
		AngleReason: reason,
	}
}

func absFloat(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}

// makeTap wraps (x, y) into a PlanTap with side classification and
// MatchSide flag. Used by every planXyz helper below so the
// classification logic stays in one place.
func (p *Planner) makeTap(unit, phase string, x, y int, note string) PlanTap {
	t := PlanTap{
		Unit:       unit,
		Phase:      phase,
		TargetEdge: p.TargetEdge,
		X:          x,
		Y:          y,
		Note:       note,
	}
	t.Side = SideOfPoint(x, y, p.W, p.H)
	for _, s := range SidesForCorner(p.TargetEdge) {
		if t.Side == s {
			t.MatchSide = true
			break
		}
	}
	return t
}

// linePoints mirrors executor.calculateLinePoints: N evenly-spaced
// point along p1→p2 (count==1 collapses to p1).
func (p *Planner) linePoints(p1, p2 image.Point, count int) []image.Point {
	if count <= 0 {
		return nil
	}
	if count == 1 || p1 == p2 {
		return []image.Point{p1}
	}
	pts := make([]image.Point, count)
	for i := 0; i < count; i++ {
		t := float64(i) / float64(count-1)
		pts[i] = image.Pt(
			int(float64(p1.X)+float64(p2.X-p1.X)*t),
			int(float64(p1.Y)+float64(p2.Y-p1.Y)*t),
		)
	}
	return pts
}

// planUnit dispatches to one of the per-kind helpers below. The
// dispatch table mirrors attack.go's deployUnit branch order.
func (p *Planner) planUnit(unit strategy.Unit, ph strategy.Phase) []PlanTap {
	name := strings.ToLower(strings.TrimSpace(unit.Name))
	isAbility := unit.Pattern == "Ability" || ph.Pattern == "Ability"
	if isAbility {
		return nil
	}
	isSpell := strings.Contains(name, "spell")
	isHero := isHeroStatic(name)
	isDuke := strings.Contains(name, "duke")
	isSiege := isSiegeStatic(name)
	isFourSides := unit.Pattern == "FourSides" || ph.Pattern == "FourSides"

	if isSpell {
		if isFourSides {
			return p.planSpellFourSides(unit, ph)
		}
		isPoint := unit.Pattern == "Point" || ph.Pattern == "Point"
		if isPoint {
			return p.planSpellPoint(unit, ph)
		}
		return p.planSpellLine(unit, ph)
	}
	if isSiege {
		if isFourSides {
			return p.planTroopFourSides(unit, ph, "siege-foursides")
		}
		return p.planTroopLine(unit, ph, "siege-line")
	}
	if isHero {
		if isDuke {
			return p.planDukeLine(unit, ph)
		}
		return p.planHeroPoint(unit, ph)
	}
	if isFourSides {
		return p.planTroopFourSides(unit, ph, "troop-foursides")
	}
	return p.planTroopLine(unit, ph, "troop-line")
}

// planTroopLine mirrors attack.go's deployUnit troop branch: along
// the YAML-target edge with optional inward offset toward the center.
func (p *Planner) planTroopLine(unit strategy.Unit, ph strategy.Phase, note string) []PlanTap {
	edge, ok := p.PCfg.Edges[p.TargetEdge]
	if !ok {
		return nil
	}
	scaled := ScaleEdge(edge, p.PCfg.Width, p.PCfg.Height, p.W, p.H)
	p1, p2 := scaled.P1, scaled.P2

	// Inward offset mirror (matches attack.go deployUnit troop branch).
	offset := unit.Offset
	if offset == 0 {
		offset = ph.Offset
	}
	if offset > 0 {
		cx, cy := p.W/2, p.H/2
		pct := float64(offset) / 200.0
		p1 = image.Pt(int(float64(p1.X)+float64(cx-p1.X)*pct), int(float64(p1.Y)+float64(cy-p1.Y)*pct))
		p2 = image.Pt(int(float64(p2.X)+float64(cx-p2.X)*pct), int(float64(p2.Y)+float64(cy-p2.Y)*pct))
	}

	count := 12
	if unit.Amount != "All" && unit.Amount != "" {
		if v := parseAmount(unit.Amount); v > 0 {
			count = v
		}
	}
	pts := p.linePoints(p1, p2, count)
	out := make([]PlanTap, 0, len(pts))
	for _, pt := range pts {
		out = append(out, p.makeTap(unit.Name, ph.Name, pt.X, pt.Y, note))
	}
	return out
}

// planTroopFourSides mirrors attack.go's deployUnit FourSides branch
// for non-spell units: 12 taps per side, all 4 sides, total 48.
func (p *Planner) planTroopFourSides(unit strategy.Unit, ph strategy.Phase, note string) []PlanTap {
	var out []PlanTap
	for _, edgeName := range []string{"TopRight", "BottomRight", "BottomLeft", "TopLeft"} {
		edge, ok := p.PCfg.Edges[edgeName]
		if !ok {
			continue
		}
		scaled := ScaleEdge(edge, p.PCfg.Width, p.PCfg.Height, p.W, p.H)
		for _, pt := range p.linePoints(scaled.P1, scaled.P2, 12) {
			out = append(out, p.makeTap(unit.Name, ph.Name, pt.X, pt.Y, note))
		}
	}
	return out
}

// planHeroPoint mirrors attack.go's deployUnit non-Duke hero branch:
// single point at the EXTERIOR corner P1 of the chosen edge (the
// corner furthest from the base center). On a diagonal corner line,
// that endpoint is on a different SIDE than the corner's name
// implies — leading to the "attacks off-side" symptom.
func (p *Planner) planHeroPoint(unit strategy.Unit, ph strategy.Phase) []PlanTap {
	edge, ok := p.PCfg.Edges[p.TargetEdge]
	if !ok {
		return nil
	}
	scaled := ScaleEdge(edge, p.PCfg.Width, p.PCfg.Height, p.W, p.H)
	return []PlanTap{p.makeTap(unit.Name, ph.Name, scaled.P1.X, scaled.P1.Y, "hero-p1")}
}

// planDukeLine mirrors HeroManager.resolveHeroTarget's Duke handling
// (current behavior: Duke falls through to the chosen edge with a
// RANDOM point along it). We enumerate FIVE candidate pcts so the
// overlay shows the FULL range of Duke landing positions the live
// bot can produce — a deterministic midpoint hides the bug when the
// chosen-edge line is diagonal ("Duke's `random` pick on a diagonal
// chosen-edge WILL scatter across both screen sides, but only on
// half the random outcomes — dequeuing the overlay's 5 candidate
// positions exposes this").
func (p *Planner) planDukeLine(unit strategy.Unit, ph strategy.Phase) []PlanTap {
	edge, ok := p.PCfg.Edges[p.TargetEdge]
	if !ok {
		return nil
	}
	scaled := ScaleEdge(edge, p.PCfg.Width, p.PCfg.Height, p.W, p.H)
	out := make([]PlanTap, 0, 5)
	for i, pct := range []float64{0.0, 0.25, 0.5, 0.75, 1.0} {
		pt := image.Pt(
			int(float64(scaled.P1.X)+float64(scaled.P2.X-scaled.P1.X)*pct),
			int(float64(scaled.P1.Y)+float64(scaled.P2.Y-scaled.P1.Y)*pct),
		)
		out = append(out, p.makeTap(unit.Name, ph.Name, pt.X, pt.Y,
			fmt.Sprintf("duke-chosen-edge[%d]@pct=%.2f", i, pct)))
	}
	return out
}

// planSpellFourSides mirrors spell_deployer.deployFourSides: per
// edge, prefer SpellTargets[edge] (point-cluster of 4) else fall
// back to SpellEdgesB/A/Edges with 4 evenly-spaced taps.
func (p *Planner) planSpellFourSides(unit strategy.Unit, ph strategy.Phase) []PlanTap {
	var out []PlanTap
	for _, edgeName := range []string{"TopRight", "BottomRight", "BottomLeft", "TopLeft"} {
		if targetPt, ok := p.PCfg.SpellTargets[edgeName]; ok {
			for j := 0; j < 4; j++ {
				angle := float64(j) * 2.0 * math.Pi / 4.0
				radius := 18.0
				tx := targetPt.X + int(radius*math.Cos(angle))
				ty := targetPt.Y + int(radius*math.Sin(angle))
				out = append(out, p.makeTap(unit.Name, ph.Name, tx, ty, "spell-foursides-pt"))
			}
			continue
		}
		edge, ok := p.PCfg.SpellEdgesB[edgeName]
		if !ok {
			edge, ok = p.PCfg.SpellEdgesA[edgeName]
			if !ok {
				edge, _ = p.PCfg.Edges[edgeName]
			}
		}
		p1, p2 := edge.P1, edge.P2
		// Mirror the inward-offset push from spell_deployer.deployFourSides.
		off := unit.Offset
		if off > 0 {
			cx, cy := p.W/2, p.H/2
			pct := float64(off) / 300.0
			p1 = image.Pt(int(float64(p1.X)+float64(cx-p1.X)*pct), int(float64(p1.Y)+float64(cy-p1.Y)*pct))
			p2 = image.Pt(int(float64(p2.X)+float64(cx-p2.X)*pct), int(float64(p2.Y)+float64(cy-p2.Y)*pct))
		}
		for j := 0; j < 4; j++ {
			pct := float64(j) / 3.0
			tx := int(float64(p1.X) + float64(p2.X-p1.X)*pct)
			ty := int(float64(p1.Y) + float64(p2.Y-p1.Y)*pct)
			out = append(out, p.makeTap(unit.Name, ph.Name, tx, ty, "spell-foursides-line"))
		}
	}
	return out
}

// planSpellPoint mirrors spell_deployer.deployPointSpell: clustered
// ring of N spells around SpellTargets[edge].
func (p *Planner) planSpellPoint(unit strategy.Unit, ph strategy.Phase) []PlanTap {
	targetPt, ok := p.PCfg.SpellTargets[p.TargetEdge]
	if !ok {
		return nil
	}
	maxSpells := 5
	if unit.Amount != "All" && unit.Amount != "" {
		if v := parseAmount(unit.Amount); v > 0 {
			maxSpells = v
		}
	}
	var out []PlanTap
	for i := 0; i < maxSpells; i++ {
		var dx, dy float64
		if maxSpells > 1 {
			angle := float64(i) * 2.0 * math.Pi / float64(maxSpells)
			dx = 18 * math.Cos(angle)
			dy = 18 * math.Sin(angle)
		}
		out = append(out, p.makeTap(unit.Name, ph.Name, targetPt.X+int(dx), targetPt.Y+int(dy), "spell-cluster"))
	}
	return out
}

// planSpellLine mirrors spell_deployer.deployLineSpell. Rage →
// SpellEdgesA (Line A) first, with the 5-spell 3+2 special layout;
// other spells → SpellEdgesB (Line B). Falls back across the same
// chain the live deployer uses.
func (p *Planner) planSpellLine(unit strategy.Unit, ph strategy.Phase) []PlanTap {
	name := strings.ToLower(unit.Name)
	isRage := strings.Contains(name, "rage")
	var edge ManualEdge
	var ok bool
	var note string
	if isRage {
		edge, ok = p.PCfg.SpellEdgesA[p.TargetEdge]
		note = "rage-lineA"
		if !ok {
			edge, ok = p.PCfg.SpellEdgesB[p.TargetEdge]
			note = "rage-lineB-fallback"
		}
	} else {
		edge, ok = p.PCfg.SpellEdgesB[p.TargetEdge]
		note = "spell-lineB"
		if !ok {
			edge, ok = p.PCfg.SpellEdgesA[p.TargetEdge]
			note = "spell-lineA-fallback"
		}
	}
	if !ok {
		return nil
	}
	p1, p2 := edge.P1, edge.P2
	offset := unit.Offset
	if offset > 0 {
		cx, cy := p.W/2, p.H/2
		pct := float64(offset)/150.0 + 0.12
		p1 = image.Pt(int(float64(p1.X)+float64(cx-p1.X)*pct), int(float64(p1.Y)+float64(cy-p1.Y)*pct))
		p2 = image.Pt(int(float64(p2.X)+float64(cx-p2.X)*pct), int(float64(p2.Y)+float64(cy-p2.Y)*pct))
	}
	maxSpells := 5
	if unit.Amount != "All" && unit.Amount != "" {
		if v := parseAmount(unit.Amount); v > 0 {
			maxSpells = v
		}
	}
	var out []PlanTap
	if isRage && maxSpells == 5 {
		for idx := 0; idx < 3; idx++ {
			pct := 0.10 + float64(idx)*0.40
			tx := int(float64(p1.X) + float64(p2.X-p1.X)*pct)
			ty := int(float64(p1.Y) + float64(p2.Y-p1.Y)*pct)
			out = append(out, p.makeTap(unit.Name, ph.Name, tx, ty, fmt.Sprintf("rage-lineA[%d]", idx)))
		}
		edgeB, okB := p.PCfg.SpellEdgesB[p.TargetEdge]
		if !okB {
			return out
		}
		p1B, p2B := edgeB.P1, edgeB.P2
		for idx, pct := range []float64{0.10, 0.90} {
			tx := int(float64(p1B.X) + float64(p2B.X-p1B.X)*pct)
			ty := int(float64(p1B.Y) + float64(p2B.Y-p1B.Y)*pct)
			out = append(out, p.makeTap(unit.Name, ph.Name, tx, ty, fmt.Sprintf("rage-lineB[%d]", idx)))
		}
		return out
	}
	for i := 0; i < maxSpells; i++ {
		pct := 0.5
		if maxSpells > 1 {
			pct = 0.22 + float64(i)*0.56/float64(maxSpells-1)
		}
		tx := int(float64(p1.X) + float64(p2.X-p1.X)*pct)
		ty := int(float64(p1.Y) + float64(p2.Y-p1.Y)*pct)
		out = append(out, p.makeTap(unit.Name, ph.Name, tx, ty, note))
	}
	return out
}
