package attack

import (
	"image"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/Ducky705/ClashGO/internal/attacklog"
	"github.com/Ducky705/ClashGO/pkg/formula"
	"github.com/Ducky705/ClashGO/pkg/strategy"
	"github.com/rs/zerolog"
)

// minCastSpacingRef is how far apart two casts of the same spell must land
// before they are working adjacent ground instead of the same ground, in
// reference px. It is attacklog.MinCastSpacing (half a cast's reach, the same
// threshold the post-run report uses to call a pair OVERLAPPED) divided back by
// the 720p display scale, so the deployer and the tool that measures the
// deployer hold the same number. Reference px rather than live px because the
// game renders at one uniform display scale and cal.Length converts this to
// whatever the device actually needs.
//
// The defect this exists to prevent, measured from a real 1280x720 run: five
// Ice Spells tapped 11 px apart, all five inside a 51x42 px box, because
// distributeAlong divided whatever line it was handed into `count` equal taps —
// and the authored ice line is 91 live px long. One freeze landed five times.
var minCastSpacingRef = attacklog.MinCastSpacing / 1.325

// ringRoundPadPx widens the ring by one pixel-ish per cast so that the exact
// chord arithmetic survives being stored as integer tap coordinates. A ring
// sized for a chord of exactly minStep measures up to ~1.4 px short (each of a
// cast's two coordinates truncates by up to 1 px), which is the whole margin
// between "spread" and "stacked" by the yardstick this rule exists to satisfy.
const ringRoundPadPx = 2.0

// maxCastRingRadiusRef is the hard ceiling on how far a cast ring may push its
// casts from the point it was drawn on, in reference px (~4 village tiles at the
// reference geometry).
//
// It exists to stop a pathological cast count from flinging spells across the
// village — NOT to trade spacing for compactness, which is what the previous 45
// made it do. The radius a ring needs is minStep/(2·sin(π/n)); at 8 casts, which
// a real army carries, that is 63 reference px, so the 45 ceiling clamped a
// correctly-sized ring down into overlap and the deployer committed the very
// defect the spacing rule exists to prevent. The comment that stood here claimed
// the cap "only bites for a formula that asks for a dozen-odd casts"; the live
// run that measured a closest pair of 41 px against a 50 px bar was an Ice Spell
// ring of 8. It is raised so it no longer bites in practice (120 reference px
// carries 16 casts at 50 px spacing) and it now logs when it does.
const maxCastRingRadiusRef = 120.0

// SpellDeployer handles spell-specific deployment logic.
type SpellDeployer struct {
	executor *TapExecutor
	pCfg     PrecisionConfig
	formula  *formula.Formula
	w, h     int
	// slotCounts maps slot X -> live OCR'd spell count (from TroopCounter).
	// When non-nil and the slot has a positive count, it takes precedence
	// over the hardcoded default of 5 so an army carrying 2 EQ spells
	// doesn't get 5 taps and an army carrying 11 rage spells doesn't get
	// 5. Counts are best-effort: OCR may fail, in which case the legacy
	// default applies.
	slotCounts map[int]int
	// pinnedGround is true when this side's spell pins are the authoritative
	// geometry (see SetPinnedGround).
	pinnedGround bool
	// firePass counts re-fires of the same spell within one reconcile: the
	// first deployment is pass 0, each reconcile re-fire is one more. It is
	// deliberately per-unit and short-lived (incremented around the recursive
	// DeploySpell call), because it describes "this cast must not repeat the
	// last one", not a property of the deployer.
	firePass int
	// troopCounter live-OCRs the count above a spell card. When set,
	// VerifyAndReconcile can re-read the count after deployment and
	// re-fire the difference until the slot is empty or retries run out.
	troopCounter *TroopCounter
	barY         int
	logger       zerolog.Logger
}

// NewSpellDeployer creates a new spell deployer. formula may be nil;
// when non-nil, a formula unit entry completely replaces the legacy
// pCfg.SpellEdges{A,B} / pCfg.SpellTargets / isRage-special logic so
// the user-pinned geometry wins. slotCounts may be nil (legacy behavior:
// Amount=="All" resolves to the default 5 taps).
func NewSpellDeployer(executor *TapExecutor, pCfg PrecisionConfig, formula *formula.Formula, w, h int, logger zerolog.Logger) *SpellDeployer {
	return NewSpellDeployerWithCounts(executor, pCfg, formula, w, h, nil, logger)
}

// NewSpellDeployerWithCounts is the count-aware constructor. counts is
// the map returned by GetAllCounts(troopCounts): slot X -> detected
// count for that card.
func NewSpellDeployerWithCounts(executor *TapExecutor, pCfg PrecisionConfig, formula *formula.Formula, w, h int, counts map[int]int, logger zerolog.Logger) *SpellDeployer {
	return &SpellDeployer{
		executor:   executor,
		pCfg:       pCfg,
		formula:    formula,
		w:          w,
		h:          h,
		slotCounts: counts,
		logger:     logger.With().Str("component", "spell_deployer").Logger(),
	}
}

// SetCounter enables post-deploy verification. counter is the shared
// TroopCounter already used by HeroManager/Sweeper; barY is the troop-bar
// top so DetectCount samples the correct card-number ROI.
func (sd *SpellDeployer) SetCounter(counter *TroopCounter, barY int) {
	sd.troopCounter = counter
	sd.barY = barY
}

// liveCountAndEmpty reads the current state of the spell's card using the
// shared captureSlotLiveCount helper (one screencap → OCR count + visual
// empty check). Returns (count, trusted, visuallyEmpty).
//
// `trusted` is true only when the count is a real measurement — either a
// positive number read off the card, or a card that shows no number while
// other cards on the bar still do. Callers must treat an untrusted zero as
// "unknown", never as "the card is spent".
func (sd *SpellDeployer) liveCountAndEmpty(slot *TrackedSlot) (int, bool, bool) {
	if sd.troopCounter == nil || sd.executor == nil {
		return 0, false, false
	}
	return captureSlotLiveCount(sd.executor, sd.troopCounter, slot, sd.barY, sd.w, sd.h)
}

// VerifyAndReconcile re-reads the spell's slot state after deployment and
// re-fires the difference along the same geometry until the slot is empty
// or maxRounds is exhausted. Mirrors HeroManager's troop reconcile loop.
// A slot is only "confirmed empty" when BOTH the OCR count reads 0 and
// the visual empty check passes; a disagreement keeps reconciling.
//
// Returns (extra spells fired, slot confirmed empty). Only runs when a
// counter was registered via SetCounter; otherwise (0, false).
func (sd *SpellDeployer) VerifyAndReconcile(unit strategy.Unit, slot *TrackedSlot, targetEdge, phasePattern string, maxRounds int) (int, bool) {
	if sd.troopCounter == nil || slot == nil || maxRounds <= 0 {
		return 0, false
	}

	const reconcileSettleMs = 350 // CoC redraw of the card count after a drop

	totalFired := 0
	lastRemaining := -1
	for round := 0; round < maxRounds; round++ {
		// Battle-timer guard: a spent spell card whose cooldown reads as
		// "still has charges" must not keep eating re-fire taps once the
		// deploy budget is gone (observed live: rage/EQ reconcile loops).
		if sd.executor.DeployBudgetExhausted() {
			sd.logger.Warn().Str("unit", unit.Name).Msg("spell reconcile: deploy budget exhausted; stopping")
			return totalFired, false
		}
		sd.executor.client.HumanSleep(reconcileSettleMs, 50)
		remaining, trusted, empty := sd.liveCountAndEmpty(slot)
		// Confirmed empty needs the count read and the visual check to agree —
		// or the visual check alone when no card on the bar is readable (the
		// end-of-battle case, where every spent card has lost its number).
		if empty && (!trusted || remaining <= 0) {
			sd.logger.Info().
				Str("unit", unit.Name).
				Int("rounds", round+1).
				Int("extra_fired", totalFired).
				Bool("count_read_trusted", trusted).
				Msg("reconcile: spell slot confirmed empty")
			return totalFired, true
		}
		if !trusted {
			// The card shows no readable count and is not visually empty, so
			// there is no number to re-fire against. Stop rather than invent
			// one: blind taps into a live battle are worse than leaving the
			// remainder for the sweep.
			sd.logger.Warn().
				Str("unit", unit.Name).
				Int("round", round+1).
				Msg("reconcile: card count unreadable and card not visually empty; stopping spell reconcile")
			return totalFired, false
		}
		if remaining <= 0 {
			// OCR says 0 but the card still looks active (cursor
			// highlight artifact). One more settle, then trust OCR.
			sd.logger.Debug().
				Str("unit", unit.Name).
				Int("round", round+1).
				Msg("reconcile: OCR empty but card visually active; treating as deployed")
			return totalFired, true
		}
		// No-progress stop: a spell drop consumes its card almost
		// instantly, so a POSITIVE count that repeats after a full
		// fire+settle cycle means the drop isn't registering (or the
		// card is in cooldown and OCR reads a stale/dimmed badge) —
		// re-firing the same count again will read the same number and
		// just wastes taps. Live: a 1-charge rage card read "1" every
		// round and the old code re-fired it for the whole maxRounds
		// budget. One repeat is tolerated (the first re-fire may be a
		// genuine miss); a second identical read stops the loop.
		if lastRemaining == remaining {
			sd.logger.Warn().
				Str("unit", unit.Name).
				Int("round", round+1).
				Int("remaining", remaining).
				Int("extra_fired", totalFired).
				Msg("spell reconcile: OCR count did not drop after re-fire; treating card as spent — stopping")
			return totalFired, false
		}
		lastRemaining = remaining

		sd.logger.Warn().
			Str("unit", unit.Name).
			Int("round", round+1).
			Int("remaining", remaining).
			Msg("reconcile: spells still on card; re-firing remainder")

		// Re-select the slot so the cursor owns the spells again, then
		// re-deploy with the unit's Amount pinned to the remaining count.
		// The explicit count takes precedence over the OCR snapshot inside
		// resolveSpellCount, so exactly `remaining` spells are re-fired.
		sd.executor.TapSlot(slot, 8)
		sd.executor.client.HumanSleep(150, 30)

		re := unit
		re.Amount = strconv.Itoa(remaining)
		// A re-fire must land on ground the earlier pass did not already
		// cover. Reusing the identical geometry is what produced a live ice
		// spell with 13 taps whose closest pair was 4 px apart: the first
		// pass covered its ring, and two more passes cast onto the same ring.
		// The pass counter shifts each re-fire by one cast spacing, which the
		// placement code applies perpendicular to the authored line.
		sd.firePass++
		sd.DeploySpell(re, slot, targetEdge, phasePattern)
		sd.firePass--
		totalFired += remaining
	}

	sd.logger.Warn().
		Str("unit", unit.Name).
		Int("extra_fired", totalFired).
		Msg("reconcile exhausted; some spells may remain undeployed")
	return totalFired, false
}

// SetPinnedGround marks this side's spell pins as authoritative, so the
// strategy formula no longer replaces the pinned spell lines (see
// HeroManager.SetPinnedGround for the rationale).
func (sd *SpellDeployer) SetPinnedGround(v bool) { sd.pinnedGround = v }

// DeploySpell deploys a spell unit according to its pattern.
//
// Precedence:
//  1. Formula entry (if loaded). Replaces ALL legacy logic including
//     the isRage special-case so the user-pinned geometry wins.
//  2. FourSides legacy fallback.
//  3. Point pattern with a configured spell target.
//  4. Line pattern (or default): Line A for rage, Line B otherwise.
func (sd *SpellDeployer) DeploySpell(unit strategy.Unit, slot *TrackedSlot, targetEdge string, phasePattern string) bool {
	// USER PINS FIRST: when the side has pins in precision_config.json (placed
	// with cmd/pick_coords), the spell lines SpellEdgesA/SpellEdgesB ARE the
	// geometry and the strategy formula must not replace them — otherwise the
	// user pins a spell line and the spells still go to the formula's ground.
	if sd.pinnedGround {
		isPointPattern := unit.Pattern == "Point" || phasePattern == "Point"
		if spellTarget, ok := sd.pCfg.SpellTargets[targetEdge]; ok && isPointPattern {
			return sd.deployPointSpell(unit, slot, spellTarget)
		}
		if _, okA := sd.pCfg.SpellEdgesA[targetEdge]; okA {
			return sd.deployLineSpell(unit, slot, targetEdge)
		}
	}

	// Formula next — when the user authored a formula for this spell
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
				radius := sd.executor.cal.Length(18)
				tx := targetPt.X + int(radius*math.Cos(angle))
				ty := targetPt.Y + int(radius*math.Sin(angle))
				jPt := sd.executor.addJitter(image.Pt(tx, ty), 6)
				sd.executor.TapField(jPt.X, jPt.Y, 8.0)
				time.Sleep(50 * time.Millisecond)
			}
		} else {
			// Legacy fallback chain: SpellEdgesB → SpellEdgesA → Edges.
			// An edge with NO configured line at all is skipped — tapping
			// the zero-value edge (0,0)→(0,0) lands every tap in the
			// screen's top-left corner, which in CoC opens menus instead
			// of deploying spells.
			edge, ok := sd.pCfg.SpellEdgesB[edgeName]
			if !ok {
				edge, ok = sd.pCfg.SpellEdgesA[edgeName]
			}
			if !ok {
				edge, ok = sd.pCfg.Edges[edgeName]
			}
			if !ok {
				sd.logger.Warn().
					Str("unit", unit.Name).
					Str("edge", edgeName).
					Msg("FourSides spell: no line configured for edge; skipping")
				continue
			}

			p1, p2 := edge.P1, edge.P2

			// Apply inward offset. Per-unit offset wins; fall back to the
			// phase-level offset (valk_spam pins "offset: 130 # Deeper in for
			// EQs" on the PHASE, not the unit — dropping the fallback made
			// the EQ ring land on the outer edge instead of deeper in).
			off := unit.Offset
			if off == 0 {
				off = unit.PhaseOffset
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
				sd.executor.TapField(jPt.X, jPt.Y, 8.0)
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

	// The ring is sized by the spacing rule, not by a constant. A hardcoded 18
	// reference px radius puts 5 casts 28 px apart and 8 casts 18 px apart — all
	// inside one cast's reach, i.e. the same freeze landed 5 times.
	jitter := 6
	minStep := sd.ringChord(jitter)
	points := make([]image.Point, 0, maxSpells)
	for i := 0; i < maxSpells; i++ {
		var offset image.Point
		if radius := sd.castRingRadius(maxSpells, minStep); radius > 0 {
			angle := float64(i) * 2.0 * math.Pi / float64(maxSpells)
			offset = image.Pt(int(radius*math.Cos(angle)), int(radius*math.Sin(angle)))
		}
		pt := image.Pt(spellTarget.X+offset.X, spellTarget.Y+offset.Y)
		points = append(points, sd.executor.addJitter(pt, jitter))
	}

	for idx, pt := range points {
		sd.logger.Info().Str("unit", unit.Name).Int("idx", idx).Interface("pt", pt).Msg("tapping spell target point")
		sd.executor.TapField(pt.X, pt.Y, 8.0)
		if idx < len(points)-1 {
			sd.executor.client.HumanSleep(80, 20)
		}
	}
	return true
}

// fallbackOffset is the offset a unit would have used, for logging only.
func fallbackOffset(unit strategy.Unit) int {
	if unit.Offset != 0 {
		return unit.Offset
	}
	return unit.PhaseOffset
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

	// Apply offset. Per-unit offset wins; fall back to the phase-level
	// offset so valk_spam-style "deeper in for EQs" phase pins still work.
	//
	// A PINNED side never takes the offset. The offset is strategy intent —
	// "these spells belong deeper in than the troop line" — and it is expressed
	// as a percentage slide toward the screen centre, which on a line the user
	// drew by hand only moves the casts OFF their line. Live 2026-09-27 the Ice
	// Spell on pinned TopRight SpellEdgesB (982,295)->(1056,393) was cast at
	// (900,341): 82 px left of the segment, i.e. off the pin by more than two
	// tiles, exactly the "spells are all off" the user reported. The picker
	// writes edges, hero_targets AND spell_edges_a/b for the same side in one
	// sitting, so pinnedGround implies these spell lines are the user's too —
	// the same reason DeploySpell routes to them ahead of the formula. Pins win;
	// the strategy offset only shapes ground the user did not choose.
	if sd.pinnedGround {
		sd.logger.Debug().
			Str("unit", unit.Name).
			Int("strategy_offset", fallbackOffset(unit)).
			Msg("pinned spell line: keeping the casts ON the user's line and not applying the strategy's inward offset")
	} else {
		off := unit.Offset
		if off == 0 {
			off = unit.PhaseOffset
		}
		if off > 0 {
			centerX, centerY := sd.w/2, sd.h/2
			pct := float64(off)/150.0 + 0.12
			p1 = image.Pt(int(float64(p1.X)+float64(centerX-p1.X)*pct), int(float64(p1.Y)+float64(centerY-p1.Y)*pct))
			p2 = image.Pt(int(float64(p2.X)+float64(centerX-p2.X)*pct), int(float64(p2.Y)+float64(centerY-p2.Y)*pct))
			sd.logger.Debug().Interface("offset_p1", p1).Interface("offset_p2", p2).Msg("applied inward offset")
		}
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
		sd.executor.TapField(pt.X, pt.Y, 8.0)
		if idx < len(points)-1 {
			sd.executor.client.HumanSleep(80, 20)
		}
	}
	return true
}

// deployRageSpecial deploys the five rage spells across the user's two pinned
// spell lines: 3 on Line A (one on each END, one on the CENTRE) and 2 on Line B
// (one on each END).
//
// The end/centre/end layout is what the user asked for, and it is also the
// widest spread a fixed line can carry. The previous 0.10 / 0.50 / 0.90 left 10%
// of the line unused at each end, which on the live 720p TopLeft rage line
// (140 px at display_scale 1.325) put the three casts 56 px apart — inside one
// rage's own reach, so the post-run report called the line TIGHT and the five
// spells landed as one cluster. Using the whole line moves the same three casts
// to 70 px apart there and the two-cast line to its full 121 px.
//
// Histrionically bots have silently dropped the last Line B tap because the
// "if idx < len(pointsB)-1" sleep guard skipped the final inter-tap delay
// and the slot re-select fired before the second Line B tap registered.
// Make every tap loud (log + count), use a consistent 180ms delay between
// taps, and log the final count so a missed spell becomes visible in the
// tap log instead of vanishing.
func (sd *SpellDeployer) deployRageSpecial(slot *TrackedSlot, edgeA, edgeB ManualEdge) bool {
	sd.logger.Info().Msg("deploying rage spells: 3 on Line A (end/centre/end), 2 on Line B (end/end)")

	pointsA := sd.rageLinePoints("Line A", edgeA, []float64{0, 0.5, 1})
	pointsB := sd.rageLinePoints("Line B", edgeB, []float64{0, 1})
	sd.logRageSpread(append(append([]image.Point{}, pointsA...), pointsB...))

	deployed := 0
	for idx, pt := range pointsA {
		sd.logger.Info().Int("idx", idx).Interface("pt", pt).Msg("tapping rage spell Line A")
		sd.executor.TapField(pt.X, pt.Y, 8.0)
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
		sd.executor.TapField(pt.X, pt.Y, 8.0)
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

// rageLinePoints turns a pinned spell line plus a list of fractions along it
// (0 = P1, 1 = P2) into jittered cast points.
//
// Every point is pulled back onto ground a cast can actually land on first.
// Now that the casts sit on the line's ENDS, a pin whose end falls under the top
// HUD or on the troop bar would otherwise spend a rage on the chrome, where CoC
// deploys nothing at all — the same failure the deploy-band clamp exists to
// prevent in the formula path, and the reason distributeAlong insets its casts.
func (sd *SpellDeployer) rageLinePoints(name string, edge ManualEdge, fractions []float64) []image.Point {
	out := make([]image.Point, 0, len(fractions))
	for _, f := range fractions {
		pt := image.Pt(
			int(float64(edge.P1.X)+float64(edge.P2.X-edge.P1.X)*f),
			int(float64(edge.P1.Y)+float64(edge.P2.Y-edge.P1.Y)*f),
		)
		// Jitter first, clamp second: the clamp is 8 px wide, and a clamp
		// applied before the jitter leaves the jitter free to push the cast
		// straight back into the chrome the clamp just moved it out of.
		pt = sd.executor.addJitter(pt, 8)
		if clamped, moved := sd.castPointInField(pt); moved {
			sd.logger.Warn().
				Str("line", name).
				Float64("at", f).
				Interface("was", pt).
				Interface("clamped_to", clamped).
				Int("field_top", BandTop()).
				Int("bar_top", sd.spellBarTop()).
				Msg("rage cast point sat outside the deployable field (top HUD or troop bar); pulled it back onto the field")
			pt = clamped
		}
		out = append(out, pt)
	}
	return out
}

// castPointInField pulls a cast point out of the game's chrome: inside the
// screen, below the top HUD, above the troop bar. Returns the point and whether
// it moved. castPointDeployable answers the same question as a predicate, for
// the paths whose contract is to SKIP a cast that cannot land; the rage layout's
// contract is to still place the user's five casts, so it moves them instead.
func (sd *SpellDeployer) castPointInField(pt image.Point) (image.Point, bool) {
	moved := false
	if sd.w > 0 {
		if pt.X < 0 {
			pt.X, moved = 0, true
		}
		if pt.X >= sd.w {
			pt.X, moved = sd.w-1, true
		}
	}
	top := BandTop() + 1
	if pt.Y < top {
		pt.Y, moved = top, true
	}
	if bot := sd.spellBarTop() - 1; bot > top && pt.Y > bot {
		pt.Y, moved = bot, true
	}
	return pt, moved
}

// logRageSpread reports the closest pair of this deployment's rage casts, in the
// same currency the post-run report measures with (attacklog.MinCastSpacing: half
// a cast's reach — below it the two casts are working the same ground). The
// deployer states its own spread here instead of leaving it to the report,
// because "spread more on the line" is a request about a number, and the number
// belongs in the run's log beside the taps that produced it.
func (sd *SpellDeployer) logRageSpread(points []image.Point) {
	if len(points) < 2 {
		return
	}
	bar := sd.minCastSpacing()
	closest := math.Inf(1)
	closestI, closestJ := 0, 0
	for i := 0; i < len(points); i++ {
		for j := i + 1; j < len(points); j++ {
			d := math.Hypot(
				float64(points[i].X-points[j].X),
				float64(points[i].Y-points[j].Y),
			)
			if d < closest {
				closest, closestI, closestJ = d, i, j
			}
		}
	}
	ev := sd.logger.Info()
	if bar > 0 && closest < bar {
		ev = sd.logger.Warn()
	}
	ev.
		Int("casts", len(points)).
		Float64("closest_pair_px", math.Round(closest)).
		Float64("min_cast_spacing_px", math.Round(bar)).
		Interface("closest_a", points[closestI]).
		Interface("closest_b", points[closestJ]).
		Msg("rage cast spread: closest pair of the five casts")
}

// resolveSpellCount returns the number of spells to deploy.
//
// Precedence:
//  1. Numeric Amount ("2", "3", ...) — the user explicitly pinned a count,
//     clamped to a trusted measured count when the card carries fewer.
//  2. Live OCR count for the spell's slot (from TroopCounter). The map only
//     holds reads the multi-frame merge confirmed, so a PRESENT zero is
//     positive evidence the card is empty — never a blurry-frame artifact
//     (an unreadable card is absent from the map). A trusted zero fires no
//     taps; inventing casts for an empty card is exactly the over-fire this
//     count path exists to prevent.
//  3. Default 5 (legacy fallback when Amount is "All" and there is NO
//     measurement at all; the post-deploy reconcile still drives it).
func (sd *SpellDeployer) resolveSpellCount(unit strategy.Unit) int {
	measured, haveMeasured := 0, false
	if sd.slotCounts != nil && sd.executor != nil {
		if n, ok := sd.slotCounts[sd.executor.slotXFor(unit)]; ok {
			measured, haveMeasured = n, true
		}
	}

	authored := -1
	if unit.Amount != "All" && unit.Amount != "" {
		if val, err := strconv.Atoi(unit.Amount); err == nil {
			authored = val
		}
	}

	switch {
	case authored >= 0 && haveMeasured && authored > measured:
		sd.logger.Warn().
			Str("unit", unit.Name).
			Int("authored", authored).
			Int("measured", measured).
			Msg("authored spell count exceeds what the card measurably carries; clamping to the measured count")
		return measured
	case authored >= 0:
		return authored
	case haveMeasured:
		if measured == 0 {
			sd.logger.Warn().
				Str("unit", unit.Name).
				Msg("spell card measurably empty at deploy start; firing no taps")
		}
		return measured
	default:
		return 5 // legacy fallback: no measurement exists at all
	}
}

// clampToMeasured caps a formula-authored count at what the card measurably
// carries. Formula geometry entries (entry.Count) outrank resolveSpellCount,
// but no authored number may exceed the army actually present.
func (sd *SpellDeployer) clampToMeasured(unit strategy.Unit, count int) int {
	if sd.slotCounts == nil || sd.executor == nil {
		return count
	}
	measured, ok := sd.slotCounts[sd.executor.slotXFor(unit)]
	if !ok || count <= measured {
		return count
	}
	sd.logger.Warn().
		Str("unit", unit.Name).
		Int("requested", count).
		Int("measured", measured).
		Msg("formula spell count exceeds the measured card; clamping")
	return measured
}

// slotXFor resolves the slot X the planner assigned to this unit. It
// mirrors DeployPlanner.planUnit's lookup (lower-cased name -> slot) so
// the SpellDeployer can pair an Amount:"All" unit with its OCR count
// without threading the *TrackedSlot through every call site. Returns 0
// when the unit has no slot (count lookup then misses and the default
// applies).
func (te *TapExecutor) slotXFor(unit strategy.Unit) int {
	if te.slotResolver == nil {
		return 0
	}
	slot := te.slotResolver(strings.ToLower(strings.TrimSpace(unit.Name)))
	if slot == nil {
		return 0
	}
	return slot.X
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
		maxSpells = sd.clampToMeasured(unit, entry.Count)
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

	// Sized by the spacing rule for the same reason as the legacy point path:
	// a fixed radius shrinks the chord as the cast count grows, so a card
	// carrying more casts lands all of them on the same ground.
	minStep := sd.ringChord(jitter)
	points := make([]image.Point, 0, maxSpells)
	for i := 0; i < maxSpells; i++ {
		var offset image.Point
		if radius := sd.castRingRadius(maxSpells, minStep); radius > 0 {
			angle := float64(i) * 2.0 * math.Pi / float64(maxSpells)
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
		count = sd.clampToMeasured(unit, entry.Count)
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
			// The inward offset sets how far apart the two sub-lines run.
			// A fixed ~35px is ~2 tiles toward the base center, matching the
			// "outerline = entry, innerline = behind the entry wall"
			// canonical edrag-rush layout — but a live 720p run showed why
			// it cannot be the whole story: the two parallel lines were only
			// 35px apart while one cast's reach is 100px, so an outer cast
			// and an inner cast landed 31px apart and the report called the
			// Rage line OVERLAPPED (2 of 5 casts spent on ground another
			// already covered). The offset carries one cast spacing, the
			// per-axis jitter both sub-lines receive (2·√2·j between two
			// casts, not 2·j), and the integer-rounding pad — the same bar
			// the cast-level guard below enforces, so a parallel auto-derive
			// starts compliant and the guard has nothing to do.
			innerOffset := 35
			if want := int(math.Ceil(sd.ringChord(jitter) + ringRoundPadPx)); want > innerOffset {
				innerOffset = want
			}
			p1In, p2In = deriveInwardLine(p1, p2, sd.w, sd.h, innerOffset)
			innerSource = "auto_derive (deriveInwardLine)"
		}
		// Whatever supplied the inner line — the user's `_rage_inner` or the
		// auto-derive — the CASTS it fires must clear the casts the outer
		// line fires. Checking the lines' midpoints is not enough, and the
		// live run that proved it is run16 (2026-09-22): the pinned inner
		// line is rotated a few degrees against the outer one, the midpoint
		// guard pushed it to its 56 px bar, and the inner line's far END
		// still swung back to 17 px from the outer line's far end —
		// outer (505,517) vs inner (512,501) — so 2 of 5 Rage casts shared
		// ground and the report called the line OVERLAPPED. Midpoint
		// distance is only the cast distance when the lines are parallel.
		sepP1, sepP2, pushed := sd.ensureInnerCastsClearOuter(p1, p2, p1In, p2In, inner, jitter)
		if pushed {
			sd.logger.Warn().
				Str("unit", unit.Name).
				Interface("was_in", p1In).
				Interface("now_in", sepP1).
				Msg("rage inner line's casts sat inside one cast spacing of the outer line; pushed inward so the two sub-lines do not overlap")
			p1In, p2In = sepP1, sepP2
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

// ensureInnerCastsClearOuter is the guard that makes "the two Rage sub-lines
// never overlap" true at the level that matters: the CASTS each line fires.
//
// The inner line is shifted inward — perpendicular to the outer line, in the
// direction of the screen center — until every cast it would fire sits at least
// one cast spacing (ringChord) from every cast the outer line would fire.
// Returns the (possibly) shifted inner line and whether it moved.
//
// This replaces a midpoint-only check whose blind spot a live run measured: a
// pinned `_rage_inner` rotated a few degrees against the outer line cleared the
// midpoint bar by 56 px, yet its far-end cast sat 17 px from the outer line's
// far-end cast, because two non-parallel lines CONVERGE toward their ends and
// midpoint distance bounds the end distance only when the lines are parallel.
// Convergence is exactly what a user-pinned inner line has: it is authored free-
// hand in reference space, projected through ApplyScreenScale, and clamped into
// the live deploy band — none of which preserves parallelism.
//
// Measuring casts, not lines, is also what makes the check honest about counts:
// an inner line carrying 1 cast must clear the 3 the outer line fires, and a
// longer inner line must clear them at its own cast positions, not at its middle.
// rageOuterCount is the number of casts the Rage split fires on the outer
// line: the odd-rounding remainder, ceil(count/2). The inner casts must clear
// the outer ones, so the guard models the outer line at this density — the
// conservative choice, since clearing a denser outer field clears the real one.
func rageOuterCount(total int) int {
	if total <= 0 {
		return 1
	}
	return total - total/2
}

func (sd *SpellDeployer) ensureInnerCastsClearOuter(outer1, outer2, inner1, inner2 image.Point, innerCount, jitter int) (image.Point, image.Point, bool) {
	chord := sd.ringChord(jitter)
	if chord <= 0 {
		return inner1, inner2, false
	}
	dx := float64(outer2.X - outer1.X)
	dy := float64(outer2.Y - outer1.Y)
	length := math.Hypot(dx, dy)
	if length < 1 {
		return inner1, inner2, false
	}

	// Model the casts each line fires: the outer line at its full rage share
	// (the conservative count — the split actually fires ceil/2, but clearing a
	// denser outer line clears the real one), the inner at the count it will
	// fire. The cast positions are exactly what distributeAlong will tap, so
	// the check measures the deployment, not an abstraction of it.
	outerCasts := sd.distributeAlong(outer1, outer2, rageOuterCount(5), jitter)
	nInner := innerCount
	if nInner < 2 {
		nInner = 2
	}

	// Shift the inner line inward — perpendicular to the outer line, toward the
	// screen center, matching deriveInwardLine's convention — by the largest
	// shortfall any inner cast has against the chord, and iterate: after one
	// shift the cast positions move, and a rotated inner line can expose a
	// different worst pair. A handful of iterations converges because each
	// shift moves every inner cast the full shortfall of the worst one.
	px, py := -dy/length, dx/length
	midX, midY := float64(inner1.X+inner2.X)/2.0, float64(inner1.Y+inner2.Y)/2.0
	if (float64(sd.w)/2.0-midX)*px+(float64(sd.h)/2.0-midY)*py < 0 {
		px, py = -px, -py
	}

	total := 0.0
	pushed := false
	for iter := 0; iter < 4; iter++ {
		innerCasts := sd.distributeAlong(inner1, inner2, nInner, jitter)
		shift := 0.0
		for _, ic := range innerCasts {
			for _, oc := range outerCasts {
				if d := math.Hypot(float64(ic.X-oc.X), float64(ic.Y-oc.Y)); d < chord {
					// Shortfall measured perpendicular to the outer line:
					// shifting the whole inner line by that amount moves this
					// cast directly away from the outer line's cast field.
					// +1 px pad: the shift is rounded to integer endpoints, and
					// without the pad a sub-pixel remainder survives the last
					// iteration (measured live: a pair left 0.5 px short).
					if need := chord - d + 1.0; need > shift {
						shift = need
					}
				}
			}
		}
		if shift <= 0.5 {
			break
		}
		ox, oy := int(math.Round(px*shift)), int(math.Round(py*shift))
		inner1 = image.Pt(inner1.X+ox, inner1.Y+oy)
		inner2 = image.Pt(inner2.X+ox, inner2.Y+oy)
		total += shift
		pushed = true
	}
	if pushed {
		sd.logger.Debug().
			Float64("total_shift", math.Round(total)).
			Float64("chord", math.Round(chord)).
			Msg("rage inner cast clearance: total inward shift applied")
	}
	return inner1, inner2, pushed
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
			sd.executor.TapField(pt.X, pt.Y, 8.0)
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

// distributeAlong produces `count` jittered cast points between p1 and p2.
//
// Taps are spread over 0.22 → 0.78 of the authored line: the inset at each end
// is deliberate, so a cast aimed at a wall corner does not land on the wrong
// side of it. Casts are then spread evenly within that span — but ONLY while
// the span can carry them at minCastSpacingRef or more. A line shorter than
// count × spacing cannot hold count distinct casts, and dividing it anyway is
// what turned five Ice Spells into one freeze plus four wasted cards: 91 live
// px of line, 5 taps, 22.7 px apart by construction, ~11 px once the tap jitter
// was applied. When the line is too short the casts go on a ring around the
// line's midpoint instead — as tight as `count` casts can be while still
// covering distinct ground, and still anchored to the geometry the user drew.
func (sd *SpellDeployer) distributeAlong(p1, p2 image.Point, count, jitter int) []image.Point {
	if count <= 1 {
		// One cast goes to the line's CENTRE, not to its first endpoint.
		//
		// The endpoints are the least trustworthy points of a line: they are
		// what the projection clamp and the red-line push displace first, and a
		// displaced endpoint can end up on the troop bar. Live 2026-09-25 (run7,
		// BottomRight): the ice spell's line was clamped to p1=(751,611), the
		// single cast fired at (751,614) — on the card row of the bar, whose
		// cards start at y≈596 — so CoC treated it as a bar tap: the card counted
		// down nowhere, the reconcile re-fired the same endpoint at (750,610),
		// and the run ended with the ice spell still on the bar (badge x1,
		// OCR-confirmed) while Deploy Health said SUCCESS. The centre of the
		// authored line is inside the field by construction, and it is the point
		// a single cast is meant to cover anyway.
		centre := image.Pt((p1.X+p2.X)/2, (p1.Y+p2.Y)/2)
		// A re-fire must still land on fresh ground: the same perpendicular
		// displacement the multi-cast path gets, so pass 1 is one cast spacing
		// beside pass 0 rather than on the identical spot.
		centre = sd.shiftPointForPass(centre, p1, p2, sd.minCastSpacing())
		return []image.Point{sd.executor.addJitter(centre, jitter)}
	}

	span := 0.56 * math.Hypot(float64(p2.X-p1.X), float64(p2.Y-p1.Y))
	// Each cast is jittered after it is placed, so two neighbours that are
	// `minStep` apart on paper can end up 2×jitter closer: a live run's ice
	// line spaced its casts 53 px apart and the measured closest pair came out
	// at 47 px, under the 50 px "same ground" bar. Requiring the post-jitter
	// distance keeps the invariant that actually ships.
	minStep := sd.minCastSpacing() + 2*float64(jitter)

	if minStep > 0 && span < minStep*float64(count-1) {
		// Ring fallback: the line is too short to carry `count` casts at the
		// minimum spacing, so the casts ring the midpoint instead.
		//
		// A re-fire cannot be separated by one spacing here the way a line
		// can: two circles of the same radius whose centres are closer than
		// their combined radius share points, so shifting the centre by a
		// single spacing still lets a re-fire cast land on the previous
		// ring. The clearance that makes the two rings disjoint by exactly
		// one spacing is 2·radius + spacing; using it keeps the invariant
		// this whole branch exists to honour — a re-fire never freezes
		// ground the first pass already froze.
		chord := sd.ringChord(jitter)
		radius := sd.ringRadius(count, chord, span)
		center := image.Pt((p1.X+p2.X)/2, (p1.Y+p2.Y)/2)
		center = sd.shiftPointForPass(center, p1, p2, 2*radius+chord)
		return sd.spreadOnRing(center, count, jitter, chord, span)
	}

	// A line re-fire is displaced sideways by whole cast spacings, so pass N
	// lands on ground pass N-1 did not cover instead of restacking on it.
	p1, p2 = sd.shiftForPass(p1, p2, minStep)

	pts := make([]image.Point, 0, count)
	for i := 0; i < count; i++ {
		pct := 0.22 + float64(i)*0.56/float64(count-1)
		tx := int(float64(p1.X) + float64(p2.X-p1.X)*pct)
		ty := int(float64(p1.Y) + float64(p2.Y-p1.Y)*pct)
		pts = append(pts, sd.executor.addJitter(image.Pt(tx, ty), jitter))
	}
	return pts
}

// perpendicularOffsetForPass returns the signed perpendicular displacement the
// current re-fire pass needs to clear `clearance` px of already-covered ground.
// Zero on pass 0 (every deployment that is not a reconcile re-fire).
//
// The sign alternates by pass parity so pass 2 sits one step further out on the
// first side rather than crossing back over its own trail — pass 1 clears to
// +clearance, pass 2 to -clearance, pass 3 to +2·clearance, and so on.
func (sd *SpellDeployer) perpendicularOffsetForPass(clearance float64) float64 {
	if sd.firePass <= 0 || clearance <= 0 {
		return 0
	}
	side := 1.0
	steps := sd.firePass
	if sd.firePass%2 == 0 {
		side = -1.0
		steps = sd.firePass / 2
	} else {
		steps = (sd.firePass + 1) / 2
	}
	return side * clearance * float64(steps)
}

// shiftForPass displaces a line perpendicular to itself so the casts of a
// re-fire sit beside the previous pass's rather than on top of them. Returns the
// segment unchanged on the first pass, or when the segment is too short to have
// a perpendicular direction.
func (sd *SpellDeployer) shiftForPass(p1, p2 image.Point, clearance float64) (image.Point, image.Point) {
	off := sd.perpendicularOffsetForPass(clearance)
	if off == 0 {
		return p1, p2
	}
	return sd.shiftPoint(p1, p1, p2, off), sd.shiftPoint(p2, p1, p2, off)
}

// shiftPointForPass moves a single point perpendicular to segment p1→p2 by the
// offset the current re-fire pass needs. Used for the ring fallback, whose
// "line" is just the segment it was derived from.
func (sd *SpellDeployer) shiftPointForPass(pt, p1, p2 image.Point, clearance float64) image.Point {
	off := sd.perpendicularOffsetForPass(clearance)
	if off == 0 {
		return pt
	}
	return sd.shiftPoint(pt, p1, p2, off)
}

// shiftPoint applies a perpendicular offset to `pt`, where the perpendicular is
// taken from the segment a→b. Falls back to an unshifted point when a==b, which
// has no perpendicular direction.
func (sd *SpellDeployer) shiftPoint(pt, a, b image.Point, off float64) image.Point {
	dx := float64(b.X - a.X)
	dy := float64(b.Y - a.Y)
	length := math.Hypot(dx, dy)
	if length < 1 {
		return pt
	}
	return image.Pt(
		pt.X+int(math.Round(-dy/length*off)),
		pt.Y+int(math.Round(dx/length*off)),
	)
}

// minCastSpacing converts the shared cast-spacing yardstick into this device's
// live pixels. Returns 0 ("no constraint") when there is no calibration to
// convert with, which leaves the pre-existing even spread in place rather than
// guessing a scale.
func (sd *SpellDeployer) minCastSpacing() float64 {
	if sd.executor == nil || sd.executor.cal == nil {
		return 0
	}
	return sd.executor.cal.Length(minCastSpacingRef)
}

// spreadOnRing places `count` casts evenly on a circle centred on `center`,
// with the radius chosen so that neighbouring casts sit exactly `chord` apart
// (chord = 2r·sin(π/n)), and never below half the ring needed to reclaim the
// short line's own span — the ring should be at least as wide as the ground the
// user drew, or we would have made the placement tighter, not spread it.
//
// `chord` is ringChord(jitter): the pre-jitter distance needed to clear the
// minimum spacing after jitter. `span` is the usable span of the line that was
// too short, logged so a reader can see the arithmetic.
// ringRadius is the radius the ring fallback uses for `count` casts at
// `minStep` spacing: the chord = 2r·sin(π/n) relation, widened so neighbouring
// casts sit a hair over the minimum, never narrower than the short line's own
// usable span (the ring must at least cover the ground the user drew), and
// capped so a formula asking for a dozen-odd casts on a stub of a line stays
// near the authored target.
func (sd *SpellDeployer) ringRadius(count int, minStep, span float64) float64 {
	radius := sd.castRingRadius(count, minStep)
	if half := span / 2; radius < half {
		radius = half
	}
	return radius
}

// ringChord is the centre-to-centre distance a ring's neighbours must have
// BEFORE jitter, so that they still clear the minimum spacing after it.
//
// Jitter is applied per axis over [-j, j], so two neighbours can each move
// toward each other by up to √2·j — the distance they can lose between them is
// 2√2·j, not 2j. Sizing a ring for `spacing + 2j` therefore does not hold the
// invariant it looks like it holds: a live 8-cast Ice Spell ring sized that way
// measured a closest pair of 41 px against a 50 px bar.
func (sd *SpellDeployer) ringChord(jitter int) float64 {
	return sd.minCastSpacing() + 2*math.Sqrt2*float64(jitter)
}

// castRingRadius returns the radius at which `count` casts placed evenly on a
// circle sit at least `minStep` apart, from chord = 2r·sin(π/n), widened by a
// hair so integer tap rounding cannot eat the margin.
//
// This is the ONE place a ring radius is decided, and it is the fix for a defect
// that had three homes: two hardcoded 18 reference px rings and one chord
// relation capped at 45. All three could hand back a ring whose neighbours sat
// closer than a cast's own reach. The live case that exposed it is the 8-cast Ice
// Spell ring in run15: closest pair 41 px against a 50 px bar, with all eight
// casts on ground another already covered, because the relation asked for 83 live
// px and the ceiling cut it to 60. A ring of N casts must span enough ground
// that no two casts cover the same area — that is the invariant, and the ceiling
// may not silently break it.
func (sd *SpellDeployer) castRingRadius(count int, minStep float64) float64 {
	if count < 2 || minStep <= 0 {
		return 0
	}
	radius := (minStep + ringRoundPadPx) / (2 * math.Sin(math.Pi/float64(count)))
	if maxRadius := sd.executor.cal.Length(maxCastRingRadiusRef); radius > maxRadius {
		sd.logger.Warn().
			Int("count", count).
			Float64("needed_radius", math.Round(radius)).
			Float64("max_radius", math.Round(maxRadius)).
			Msg("cast ring clamped: this many casts need a wider ring to stay a minimum spacing apart, so neighbours are closer than that")
		radius = maxRadius
	}
	return radius
}

func (sd *SpellDeployer) spreadOnRing(center image.Point, count, jitter int, minStep, span float64) []image.Point {
	radius := sd.ringRadius(count, minStep, span)

	sd.logger.Info().
		Str("spread", "ring").
		Int("count", count).
		Float64("line_span", math.Round(span)).
		Float64("min_spacing", math.Round(sd.minCastSpacing())).
		Float64("ring_chord", math.Round(minStep)).
		Float64("even_spacing_implied", math.Round(span/math.Max(1, float64(count-1)))).
		Float64("ring_radius", math.Round(radius)).
		Interface("center", center).
		Msg("spell line too short for this many casts at minimum spacing; spreading on a ring instead of stacking")

	pts := make([]image.Point, 0, count)
	for i := 0; i < count; i++ {
		angle := 2 * math.Pi * float64(i) / float64(count)
		pt := image.Pt(
			center.X+int(radius*math.Cos(angle)),
			center.Y+int(radius*math.Sin(angle)),
		)
		pts = append(pts, sd.executor.addJitter(pt, jitter))
	}
	return pts
}

// tapSeries taps each point in `points` with 180ms +/- 40ms inter-tap
// sleeps. Always delays between taps (no "skip on last" bug from legacy
// deployLineSpell).
//
// Every cast goes through castPointDeployable first: a tap that leaves the
// screen deploys nothing and is pure noise in the trace (live 2026-09-25, a
// TopLeft run whose sweep line projected to x=-11 sent 18 taps off-screen), and
// a cast is not re-tried from the same place just because the tap went out.
func (sd *SpellDeployer) tapSeries(unit strategy.Unit, points []image.Point, _ int, kind string) {
	for idx, pt := range points {
		if !sd.castPointDeployable(pt) {
			sd.logger.Warn().
				Str("unit", unit.Name).
				Str("series", kind).
				Int("idx", idx).
				Interface("pt", pt).
				Int("screen_w", sd.w).
				Int("screen_h", sd.h).
				Msg("spell cast point is outside the deployable field; skipping the tap rather than tapping the HUD or the troop bar")
			continue
		}
		sd.logger.Info().
			Str("unit", unit.Name).
			Str("series", kind).
			Int("idx", idx).
			Interface("pt", pt).
			Msg("tapping spell formula point")
		sd.executor.TapField(pt.X, pt.Y, 8.0)
		sd.executor.client.HumanSleep(80, 20)
	}
}

// castPointDeployable reports whether a spell may be cast at `pt`: inside the
// screen, below the top HUD, and above the troop bar.
//
// The bar's boundary is the one place a spell tap silently does nothing: CoC
// hands it to the card row instead of casting it, so the card keeps its charge
// and the run looks like it "selected the spell but never placed it". The bar's
// top is taken from the counter's own bar_y when one is wired (the same number
// the count reader samples against), and from the deploy band's own bottom on a
// screen with no counter.
func (sd *SpellDeployer) castPointDeployable(pt image.Point) bool {
	if sd.w > 0 && (pt.X < 0 || pt.X >= sd.w) {
		return false
	}
	if sd.h > 0 && (pt.Y < 0 || pt.Y >= sd.h) {
		return false
	}
	if pt.Y < YTopMin() {
		return false
	}
	return pt.Y < sd.spellBarTop()
}

// spellBarTop is the first y the troop bar occupies. bar_y from the troop
// counter is the bar's own top edge; without a counter the fallback is the
// deploy band's top-side cutoff the orchestrator uses (0.85 of the height),
// which is the last y it will clamp a formula point to.
func (sd *SpellDeployer) spellBarTop() int {
	if sd.barY > 0 {
		return sd.barY
	}
	if sd.h > 0 {
		return int(float64(sd.h) * 0.85)
	}
	// No screen geometry to bound against: do not invent a bound and silently
	// drop every cast.
	return 1 << 30
}
