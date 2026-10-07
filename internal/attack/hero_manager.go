package attack

import (
	"fmt"
	"image"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Ducky705/ClashGO/pkg/formula"
	"github.com/Ducky705/ClashGO/pkg/strategy"
	"github.com/rs/zerolog"
	"gocv.io/x/gocv"
)

// HeroManager handles hero-specific deployment logic.
type HeroManager struct {
	executor     *TapExecutor
	slotManager  *SlotManager
	pCfg         PrecisionConfig
	formula      *formula.Formula
	troopCounter *TroopCounter
	targetEdge   string
	w, h         int
	logger       zerolog.Logger

	// deployLine is the battle's validated deploy line — red-zone derived,
	// clamped into the deploy band, the same ground the sweep retries on.
	// deploySingleHero uses it for the displaced retry: a hero drop rejected
	// on the pinned tile (a base whose footprint covers the pin, or a pin on
	// top of an already-dropped siege) is not evidence the hero is undeployable,
	// it is evidence about ONE tile. The sweep's live successes came exclusively
	// from random points along this line.
	deployLine DeployLine

	// zone is the battle's detected red no-deploy line. Hero drops are
	// filtered against it (see hero_ground.go): ground INSIDE the line is
	// refused by the game, and the pinned reference-geometry edge line the
	// orchestrator hands the hero path is never checked against it. Live
	// run10 (2026-09-27): both heroes were dropped at the pinned midpoint
	// (935,485), both were refused, and the SAME point was accepted seven
	// seconds later once the base had been destroyed back off it — the tap
	// was fine, the ground was not. A zero-value zone (no usable outline)
	// keeps the legacy behaviour.
	zone RedZone

	// troopGround records where this battle's troop/siege passes just deployed.
	// Those taps were accepted by the game seconds before the hero phase runs, so
	// the axis they share is the strongest ground evidence available for a hero
	// drop — and taking it drops the orchestrator's radial red-zone push, which
	// live run13 moved two hero pins 74-147px sideways (x=118 and x=45) onto
	// ground the game refused while the troop line at x=192 was accepted.
	troopGround []image.Point

	// pinnedGround is true when the bearer of this battle's deploy line is the
	// USER's pin from precision_config.json (see SetPinnedGround).
	pinnedGround bool

	// ratioHook, when non-nil, replaces the screen capture inside
	// captureSlotRatio. Test seam only — nil in production.
	ratioHook func() (float64, bool)

	// activateAbility gates the post-deploy auto-activation loop inside
	// activateAbilities. The orchestrator keeps it false — abilities are
	// owned by the dedicated "Abilities" phase, which runs AFTER the spell
	// phases (tapping the hero icon again re-selects the hero's card, so an
	// ability fired mid-deploy sequence makes the next slot-selection tap
	// land on the hero icon instead of the intended unit). Legacy/hero-only
	// paths set it true to keep the bulk activation behavior.
	activateAbility bool

	OnDukeDeployed func(targetEdge string)
}

// NewHeroManager creates a new hero manager. formula may be nil; when
// non-nil, per-unit formula entries override the legacy pCfg.Edges /
// dynamic red-zone deploy coordinates so the user can pin exact side
// positions via cmd/design_attack. troopCounter may also be nil; when
// non-nil, DeployTroops uses it to live-OCR the slot's per-card count
// at deploy time AND after the main tap pass — so balloons/EDs (and any
// "amount: All" troop) always reach a true empty state before being
// marked deployed.
func NewHeroManager(
	executor *TapExecutor,
	slotManager *SlotManager,
	pCfg PrecisionConfig,
	targetEdge string,
	w, h int,
	formula *formula.Formula,
	troopCounter *TroopCounter,
	deployLine DeployLine,
	logger zerolog.Logger,
) *HeroManager {
	return &HeroManager{
		executor:     executor,
		slotManager:  slotManager,
		pCfg:         pCfg,
		formula:      formula,
		troopCounter: troopCounter,
		targetEdge:   targetEdge,
		w:            w,
		h:            h,
		deployLine:   deployLine,
		logger:       logger.With().Str("component", "hero_manager").Logger(),
	}
}

// SetActivateAbility enables the legacy post-deploy auto-activation loop.
// The dynamic V2 orchestrator leaves it off: abilities run in the dedicated
// "Abilities" phase, after spells. Only the hero-phase bulk-activation path
// (attack.go) opts in.
func (hm *HeroManager) SetActivateAbility(v bool) {
	hm.activateAbility = v
}

// SetRedZone wires the battle's detected red no-deploy line so hero drops can
// avoid ground the game refuses to accept (see hero_ground.go). Called once per
// battle by the orchestrator; a zero-value zone is legal and means "unchecked".
func (hm *HeroManager) SetRedZone(z RedZone) {
	hm.zone = z
}

// HeroDeployment represents a resolved hero deployment.
type HeroDeployment struct {
	Unit      strategy.Unit
	Slot      *TrackedSlot
	IsAbility bool
}

// DeployHeroes deploys all heroes and activates abilities.
// Returns list of deployed hero slots for ability tracking.
func (hm *HeroManager) DeployHeroes(heroUnits []strategy.Unit, screen gocv.Mat) []*TrackedSlot {

	var deployments []HeroDeployment
	for _, unit := range heroUnits {
		unitName := strings.ToLower(strings.TrimSpace(unit.Name))
		isAbility := unit.Pattern == "Ability"

		if isAbility {
			continue
		}

		slot := hm.slotManager.GetSlot(unitName)
		if slot == nil {
			hm.logger.Warn().Str("unit", unit.Name).Msg("hero not found in bar")
			continue
		}

		deployments = append(deployments, HeroDeployment{
			Unit:      unit,
			Slot:      slot,
			IsAbility: false,
		})
	}

	var mainDeployments []HeroDeployment
	var bonusDeployments []HeroDeployment

	for _, d := range deployments {
		if isHeroStatic(d.Unit.Name) {
			mainDeployments = append(mainDeployments, d)
		} else {
			bonusDeployments = append(bonusDeployments, d)
		}
	}

	sort.Slice(mainDeployments, func(i, j int) bool {
		return mainDeployments[i].Slot.Confidence > mainDeployments[j].Slot.Confidence
	})

	all := make([]HeroDeployment, 0, len(mainDeployments)+len(bonusDeployments))
	all = append(all, mainDeployments...)
	all = append(all, bonusDeployments...)
	// One batch for every hero: a rapid drop wave, then confirmation on shared
	// frames, then evidence-gated rescues. See deployHeroBatch.
	deployedSlots := hm.deployHeroBatch(all)

	// Ability activation is owned by the dedicated "Abilities" phase in
	// the orchestrator (see ExecuteDynamicV2's late-ability block), which
	// runs AFTER the spell phases. Auto-activating here fired the
	// abilities ~60ms after the heroes dropped and BEFORE the spell phase
	// could run — the tap re-selected the hero's card, and the spell
	// phase's slot-selection tap then landed on the still-highlighted
	// hero icon, burning the ability AND (live, valk_run6) tapping the
	// siege machine's slot instead of the earthquake card.
	hm.activateAbilities(deployedSlots)

	return deployedSlots
}

// SetPinnedGround marks the battle's deploy line as the USER'S pin from
// precision_config.json (written by cmd/pick_coords), which makes it
// authoritative for this hero manager: the strategy formula no longer supplies
// the troop ground and the strategy `offset` no longer applies to it.
//
// Both matter. The formula override sent every tap to the formula's line even
// when the user had pinned a different one, and `offset` (the strategy's
// "push the line this far toward the middle") moved a pinned line up to 40% of
// the way to the screen centre — straight into the base's no-deploy area, which
// is the ground the game refuses with "You cannot deploy troops on the Red
// area!" (captured live 2026-09-27).
func (hm *HeroManager) SetPinnedGround(v bool) { hm.pinnedGround = v }

// heroBatchGapMs is the pause between consecutive hero drops in the wave.
// Short on purpose: real players drop heroes back to back, and the game's own
// card-selection animation (heroArmSettleMs) is already paid per hero.
const heroBatchGapMs = 60

// heroConfirmSettleMs / heroConfirmFollowMs are the two fixed beats of a
// confirmation wave (see confirmHeroWave). Two deterministic reads beat the old
// settle-poll, which could lock onto a stable PRE-transition frame and report a
// drop that had actually landed — live 2026-10-01 the Dragon Duke measured
// "not placed" at every read, then showed consumed before the sweep's first tap.
const (
	heroConfirmSettleMs = 300
	heroConfirmFollowMs = 400
	heroConfirmLateMs   = 1200
)

// heroDebugDir returns the CLASHGO_HERO_DEBUG directory (empty when unset).
// Diagnostic seam only: when set, confirmHeroWave takes a third, late beat and
// dumps a frame per beat, so a live run shows WHEN each hero card empties — a
// batch drop that registers late (queued device taps) vs a drop the game
// genuinely refused. Production runs leave the variable unset and take the
// normal two beats.
func heroDebugDir() string {
	return os.Getenv("CLASHGO_HERO_DEBUG")
}

// heroBatchEntry tracks one hero through the batch drop wave and the shared
// confirmation wave that follows.
type heroBatchEntry struct {
	d          HeroDeployment
	cands      []image.Point
	firstIndex int
	fired      bool    // the placement field tap went out
	last       float64 // most recent slot-activity reading, carried into the next read
	reads      int     // successful confirm reads (a rescue needs this evidence)
	placed     bool
}

// deploySingleHero deploys one hero through the same batch path the hero phase
// uses (see deployHeroBatch). Kept as the named entry point for single-hero
// callers.
func (hm *HeroManager) deploySingleHero(d HeroDeployment, _ gocv.Mat) bool {
	return len(hm.deployHeroBatch([]HeroDeployment{d})) == 1
}

// deployHeroBatch drops every hero in one rapid wave, then confirms them on
// SHARED frames and re-aims only the heroes the game provably refused.
//
// Why batch: the per-hero path paid a settled pre-read, a settled post-read and
// a fixed drop settle FOR EACH hero — measured live at 11-12.7s for five heroes
// (2026-10-01, three attacks), which alone blew the 7s deploy window. The wave
// costs one arm settle per hero; the confirmation wave reads every pending hero
// off the same capture, so five heroes cost two captures, not ten.
//
// Anti-"random tap" contract: a rescue field tap fires only on evidence — a
// successful confirm read showed the hero is still on its card — and only on
// ground the first tap did not use. When nothing can be proven (capture failed)
// or there is no new ground, the hero is left to the sweep, which arms the card
// itself. Card-tap budgets are untouched: one placement tap per hero here, the
// re-arm at most once, the ability's tap always kept.
func (hm *HeroManager) deployHeroBatch(deps []HeroDeployment) []*TrackedSlot {
	batch := make([]*heroBatchEntry, 0, len(deps))
	for _, d := range deps {
		batch = append(batch, &heroBatchEntry{d: d, cands: hm.heroDropCandidates(d.Slot, d.Unit.Name)})
	}

	// Wave 0: the drops, back to back. No captures between them.
	for i, p := range batch {
		hm.logger.Info().
			Str("unit", p.d.Unit.Name).
			Int("x", p.d.Slot.X).
			Float64("conf", p.d.Slot.Confidence).
			Msg("deploying hero (batch drop)")
		// Card tap #1 of at most 2: select the hero for placement. When the
		// budget is already gone the hero cannot be placed and must not be
		// re-tapped — see maxHeroSpotTaps.
		if !hm.executor.TapSlot(p.d.Slot, heroCardTapJitter) {
			hm.logger.Warn().
				Str("unit", p.d.Unit.Name).
				Int("slot_x", p.d.Slot.X).
				Msg("hero card tap budget exhausted before placement; skipping hero")
			continue
		}
		hm.executor.client.HumanSleep(heroArmSettleMs, 30)
		hm.debugDump(fmt.Sprintf("wave%d_pre", i))
		// Spread the heroes over the candidate ground and widen the drop jitter
		// per hero: five heroes on one tile is both bot-like and the first thing
		// a refused pocket kills. When the pattern is explicitly "Point", all heroes
		// drop on the primary candidate point (same spot).
		if p.d.Unit.Pattern == "Point" {
			p.firstIndex = 0
		} else {
			p.firstIndex = i % len(p.cands)
		}
		ground := p.cands[p.firstIndex]
		jitter := 3 + 2*i
		if jitter > 10 {
			jitter = 10
		}
		hm.fireHeroDrop(p.d.Unit.Name, ground, true, jitter)
		p.fired = true
		hm.debugDump(fmt.Sprintf("wave%d_post", i))
		hm.executor.client.HumanSleep(heroBatchGapMs, 20)
	}

	// Shared confirmation: two reads per pending hero off ONE frame per read.
	hm.confirmHeroWave(batch)

	// Rescues: only heroes whose card is PROVEN to still hold them, and only
	// on ground the first tap did not use.
	for _, p := range batch {
		if p.placed || !p.fired || p.reads == 0 {
			continue // placed, dropped-but-unverifiable, or never dropped
		}
		if len(p.cands) <= 1 {
			hm.logger.Warn().
				Str("unit", p.d.Unit.Name).
				Int("slot_x", p.d.Slot.X).
				Msg("hero drop unconfirmed but there is no new ground to re-aim at; leaving it to the sweep (no stray re-arm)")
			continue
		}
		// Rotate candidate slice so the point that was actually fired is at index 0,
		// and index 1..len are untested retry points.
		retryCands := make([]image.Point, 0, len(p.cands))
		retryCands = append(retryCands, p.cands[p.firstIndex])
		for idx, pt := range p.cands {
			if idx != p.firstIndex {
				retryCands = append(retryCands, pt)
			}
		}

		// A refused drop DESELECTS the card, so the field can only be re-aimed
		// after the card is re-selected (see maxHeroRearms). The re-arm is not a
		// placement and spends no placement budget — the hero keeps its one
		// placement and its one ability.
		if !hm.executor.RearmSlot(p.d.Slot) {
			continue
		}
		hm.executor.client.HumanSleep(heroArmSettleMs, 30)
		hm.debugDump("rescue_" + p.d.Unit.Name + "_pre")
		if hm.retryHeroFieldTaps(p.d.Slot, p.d.Unit.Name, retryCands, p.last, true, "batch-confirm") {
			p.placed = true
		}
	}

	// Verdicts, and the log lines attack_report reads.
	var deployed []*TrackedSlot
	for _, p := range batch {
		if p.placed {
			deployed = append(deployed, p.d.Slot)
			if hm.slotManager != nil {
				hm.slotManager.MarkDeployed(p.d.Unit.Name)
			}
			continue
		}
		hm.logger.Warn().
			Str("unit", p.d.Unit.Name).
			Int("slot_x", p.d.Slot.X).
			Int("slot_y", p.d.Slot.Y).
			Msg("hero drop not confirmed by the slot transition; leaving it for the sweep's one remaining tap (no re-tap)")
		if hm.slotManager != nil {
			hm.slotManager.RecordAttempt(p.d.Unit.Name, false)
		}
	}
	return deployed
}

// confirmHeroWave reads every pending hero's card twice — one settled beat and
// one follow-up beat for the cards that register late — off a single shared
// capture per beat. heroPlaced's absolute ceiling (see hero_ground.go) decides
// on the first beat; the second doubles as the delta baseline. A hero whose
// reads all failed stays unproven and is never rescued blind.
//
// CLASHGO_HERO_DEBUG=<dir> adds a third, late beat plus one frame dump per beat
// (see heroDebugDir): the ratio curve across beats is the evidence for whether
// a failed batch drop ever registers on its own.
//
// The wave exits as soon as every fired hero is confirmed — the later beats
// exist only for cards that have not registered, and skipping them saves their
// sleep plus a capture of the deploy window (2026-10-05).
func (hm *HeroManager) confirmHeroWave(batch []*heroBatchEntry) {
	dbg := heroDebugDir()
	beats := 2
	if dbg != "" {
		beats = 3
		_ = os.MkdirAll(dbg, 0o755)
	}
	for beat := 0; beat < beats; beat++ {
		switch beat {
		case 0:
			hm.executor.client.HumanSleep(heroConfirmSettleMs, 30)
		case 1:
			hm.executor.client.HumanSleep(heroConfirmFollowMs, 40)
		default:
			hm.executor.client.HumanSleep(heroConfirmLateMs, 50)
		}
		if hm.executor.DeployBudgetExhausted() {
			return
		}
		frame, err := hm.executor.CaptureFresh()
		haveFrame := err == nil && !frame.Empty()
		for _, p := range batch {
			if p.placed || !p.fired {
				continue
			}
			r, ok := hm.readSlotRatio(frame, p.d.Slot)
			if !ok {
				continue
			}
			p.reads++
			placed := heroPlaced(p.last, r, true)
			if placed {
				p.placed = true
			}
			if dbg != "" {
				hm.logger.Info().
					Str("unit", p.d.Unit.Name).
					Int("beat", beat).
					Int("slot_x", p.d.Slot.X).
					Float64("ratio", r).
					Bool("placed", placed).
					Msg("hero confirm read (debug beat)")
			}
			p.last = r
		}
		if dbg != "" && haveFrame {
			path := filepath.Join(dbg, fmt.Sprintf("beat%d.png", beat))
			if !gocv.IMWrite(path, frame) {
				hm.logger.Warn().Str("path", path).Msg("failed to save hero confirm debug frame")
			}
		}
		if haveFrame {
			frame.Close()
		}
		// Every fired hero is confirmed: the remaining beats exist only to wait
		// for cards that have NOT registered, so re-reading confirmed cards just
		// spends the deploy window (heroConfirmFollowMs plus a capture per beat,
		// and heroConfirmLateMs too under CLASHGO_HERO_DEBUG). Entries that never
		// fired cannot be rescued anyway, so they do not keep the wave alive.
		if !batchPending(batch) {
			return
		}
	}
}

// batchPending reports whether any hero in the wave still needs a confirm read:
// fired but not yet proven placed.
func batchPending(batch []*heroBatchEntry) bool {
	for _, p := range batch {
		if p.fired && !p.placed {
			return true
		}
	}
	return false
}

// debugDump saves one diagnostic frame into the CLASHGO_HERO_DEBUG directory.
// It is the only way to see the card's ARMED state between the card tap and the
// field tap that spends it: a refused drop disarms the card, so after the fact
// "card never armed" and "ground refused" look identical. The wave's *_pre
// frames (captured after the arm sleep) versus the rescue's *_pre frames are
// what separates them.
func (hm *HeroManager) debugDump(name string) {
	if heroDebugDir() == "" {
		return
	}
	frame, err := hm.executor.CaptureFresh()
	if err != nil || frame.Empty() {
		return
	}
	defer frame.Close()
	_ = gocv.IMWrite(filepath.Join(heroDebugDir(), name+".png"), frame)
}

// readSlotRatio reads one hero card's activity ratio from the shared wave
// frame (or the scripted hook in tests).
func (hm *HeroManager) readSlotRatio(frame gocv.Mat, slot *TrackedSlot) (float64, bool) {
	if hm.ratioHook != nil {
		return hm.ratioHook()
	}
	if frame.Empty() {
		return 0, false
	}
	return GetSlotActivityRatioStatic(frame, slot.X, slot.Y, hm.w), true
}

// recordGround remembers field points a troop pass just deployed on. Capped so
// the set always describes the current pass rather than accumulating a whole
// battle's worth of unrelated lines.
func (hm *HeroManager) recordGround(points ...image.Point) {
	const maxRecordedGround = 8
	for _, p := range points {
		if p == (image.Point{}) {
			continue
		}
		if len(hm.troopGround) >= maxRecordedGround {
			hm.troopGround = hm.troopGround[1:]
		}
		hm.troopGround = append(hm.troopGround, p)
	}
}

// fireHeroDrop sends one field tap at pt (jittered by jitterPx). first marks
// the main drop, the only one that reports the Duke pick to the observer.
func (hm *HeroManager) fireHeroDrop(unitName string, pt image.Point, first bool, jitterPx int) {
	// Jitter first, then TapUnitField: the siege-clearance check has to be the
	// last thing between the aim point and the wire, or the jitter puts the tap
	// straight back on the machine.
	j := hm.executor.addJitter(pt, jitterPx)
	hm.executor.TapUnitField(j, 12.0)
	if first && hm.OnDukeDeployed != nil && strings.Contains(strings.ToLower(unitName), "duke") {
		hm.OnDukeDeployed(hm.targetEdge)
	}
}

// retryHeroFieldTaps re-aims the FIELD tap at the next hero ground candidates
// after an unconfirmed drop. It fires NO card tap, so the hero's 2-tap ceiling
// (place + ability) is preserved no matter how many retries run; each retry is
// verified with the same pre/post delta the first drop used, carrying the
// previous reading forward as the next baseline so no extra capture is needed
// for the pre.
func (hm *HeroManager) retryHeroFieldTaps(
	slot *TrackedSlot,
	unitName string,
	cands []image.Point,
	baseline float64,
	baselineOK bool,
	preSrc string,
) bool {
	return runHeroFieldRetries(
		cands, baseline, baselineOK,
		func(pt image.Point) { hm.fireHeroDrop(unitName, pt, false, 3) },
		func() (float64, bool) {
			hm.executor.client.HumanSleep(300, 40)
			return hm.captureSlotRatio(slot)
		},
		func() bool { return hm.executor.DeployBudgetExhausted() },
		func(attempt int, pt image.Point, pre, post, delta float64, won bool) {
			hm.logger.Info().
				Str("unit", unitName).
				Int("slot_x", slot.X).
				Int("attempt", attempt).
				Interface("target", pt).
				Float64("pre_ratio", pre).
				Float64("post_ratio", post).
				Float64("delta", delta).
				Bool("confirmed", won).
				Msg("hero field-tap retry on new ground (no extra card tap)")
			if won {
				hm.logger.Info().
					Str("unit", unitName).
					Int("slot_x", slot.X).
					Int("slot_y", slot.Y).
					Int("field_retry", attempt).
					Interface("target", pt).
					Str("pre_src", preSrc).
					Float64("pre_ratio", pre).
					Float64("post_ratio", post).
					Float64("delta", delta).
					Msg("hero deployed (single card tap + field-tap retry on new ground)")
			}
		},
	)
}

// containsPoint reports whether p is one of pts. Used by heroDropCandidates to
// name the ground that actually leads its list after re-ordering.
func containsPoint(pts []image.Point, p image.Point) bool {
	for _, q := range pts {
		if q == p {
			return true
		}
	}
	return false
}

// heroDropCandidates returns the ground points a hero may drop on, best first.
//
// Order of evidence:
//  1. The ground this battle's troop pass just used (the axis those accepted
//     taps share) — proven in THIS battle, so it leads whenever it exists.
//  2. The user's pinned hero point / the strategy formula's point.
//  3. The outward ladder from that intent, then the deploy line's band points.
//
// With no red outline to check against, the whole list is then ordered
// outward-first — the pin included. The pin's protected lead was exactly the
// live refusal pattern: 2026-10-04/05 (run1 + probe3) logged
// ground_source=pinned for every wave and the game refused that first tap
// 11/12, while the outward ladder points beside it landed on the rescue taps
// seconds later. The log's ground_source names whichever ground actually leads
// after all of it.
func (hm *HeroManager) heroDropCandidates(slot *TrackedSlot, unitName string) []image.Point {
	entry, hasFormula := hm.formulaEntry(unitName)
	var cands []image.Point
	source := "deploy_line"

	// A side the user pinned by hand outranks everything below. Live TopLeft run
	// 2026-09-27: the troop line correctly used the pin (src=pin) while both
	// heroes still dropped at the FORMULA's mirrored point {192,288}, because the
	// formula's hero entry supplied the intent here and the pinned hero point was
	// only reachable through the axis-snap fallback — which a diagonal pinned
	// line never satisfies. A first tap on the user's own point is also what
	// leaves the second card tap free for the ability.
	pinnedPt, havePin := hm.pinnedHeroPoint()

	// The ground the troop pass just used wins: snap this hero's intent onto the
	// axis those accepted taps share.
	var intent image.Point
	haveIntent := false
	switch {
	case havePin:
		intent, haveIntent = pinnedPt, true
	case hasFormula:
		if fc := heroFormulaCandidates(entry, hm.h); len(fc) > 0 {
			intent, haveIntent = fc[0], true
		}
	}
	// The troop pass's ground leads whenever this battle left some: those taps
	// were accepted by the game seconds ago. Before this fix the pin was
	// prepended OVER the axis and the source string overwritten with "pinned",
	// so the axis path was dead in every pinned battle (and a diagonal pin line
	// never matches groundAxis anyway — see troopGround).
	var axisCands, formulaPts []image.Point
	if axis, vertical, ok := groundAxis(hm.troopGround); ok {
		if !haveIntent {
			intent, _ = hm.resolveHeroTarget(slot)
			haveIntent = true
		}
		if haveIntent {
			axisCands = axisCandidates(intent, axis, vertical, hm.h, hm.w)
		}
	}
	if len(axisCands) > 0 {
		cands = append(cands, axisCands...)
		source = "troop_ground_axis"
	}
	if havePin {
		if len(axisCands) > 0 {
			// Battle evidence leads; the pin rides as an alternate instead of
			// being deleted — the user's choice stays reachable, it just no
			// longer spends the first tap on ground the game keeps refusing.
			cands = append(cands, pinnedPt)
		} else {
			cands = append([]image.Point{pinnedPt}, cands...)
			source = "pinned"
		}
	} else if hasFormula {
		if fc := heroFormulaCandidates(entry, hm.h); len(fc) > 0 {
			formulaPts = fc
			cands = append(cands, fc...)
			if source == "deploy_line" {
				source = "formula"
			}
		}
	}
	// A refused drop has to move OUTWARD from the point that was refused. Without
	// this the only alternative a missing red zone offers is the line's midpoint,
	// which is just as far inside the base's pocket as the point that failed —
	// live 2026-09-27 that made every hero drop on all four sides fail, cost the
	// ability pass, and hand the placement to the sweep seconds later. See
	// outwardHeroLadder (hero_ground.go).
	if haveIntent {
		cands = append(cands, outwardHeroLadder(intent, hm.deployLine.Points, hm.w, hm.h, maxHeroGroundCandidates-1)...)
	}
	cands = append(cands, heroGroundCandidates(hm.deployLine.Points, hm.h, hm.zone)...)

	// The machine deliberately stands on the heroes' ground (it screens them), so
	// that point is NOT hero ground any more: the game refuses a placement tap on
	// the machine, and the tap is what the user sees as "it taps the siege twice".
	// Live 2026-09-27 pinned TopRight: the machine landed on the pinned hero point
	// (954,183) and all three heroes fired there — two refused, each costing a
	// re-arm plus a retry 50 px away, ~1.5 s of the hero phase per hero. Dropping
	// the tile from the candidate list puts the first tap on accept-ground beside
	// the machine, where the heroes still tank for it.
	// A hand-built HeroManager (the unit tests) has no executor, so the machine's
	// tile is only known when one actually ran.
	var siegeGround image.Point
	siegeOK := false
	if hm.executor != nil {
		siegeGround, siegeOK = hm.executor.SiegeGround()
	}
	if siegeOK {
		clear := make([]image.Point, 0, len(cands))
		for _, p := range cands {
			if hm.executor.clearsSiege(p) {
				clear = append(clear, p)
			}
		}
		if dropped := len(cands) - len(clear); dropped > 0 {
			hm.logger.Info().
				Str("unit", unitName).
				Interface("siege_ground", siegeGround).
				Int("dropped", dropped).
				Msg("dropped hero ground that sat on the siege machine's tile; the drop goes beside the machine instead")
		}
		if len(clear) > 0 {
			cands = clear
		}
	}

	// First-tap ordering when this battle's red line is UNKNOWN. The no-deploy
	// pocket hugs the middle of the map, so the farther a candidate sits from
	// the screen centre the more likely the game accepts it. Live 2026-10-01
	// (BottomLeft, three attacks): with the pinned point consumed by the
	// siege-clearance filter above, the ladder's NEAREST point led every drop
	// and was refused four times out of five while the outward point beside it
	// landed every time — each refusal then cost a re-arm plus a retry tap.
	// The pin used to keep its lead here when it survived the filter; run1 and
	// probe3 (2026-10-04/05) then refused that lead 11/12, so now the pin is
	// re-ordered with everything else. With a red outline present the
	// zone-clearance order (heroGroundCandidates) is strictly better and is
	// left alone.
	reordered := false
	if len(hm.zone.Polygon) < 3 && len(cands) > 1 {
		// Outward-first over the WHOLE list, pin included: the pin's protected
		// lead was the refusal pattern (its first tap lost ~11/12 live), and the
		// outward points around it are the ones that landed. The comment above
		// this block used to keep the pin at [0]; the evidence says the opposite.
		orderOutwardFirst(cands, hm.w, hm.h)
		reordered = true
	}

	if len(cands) == 0 {
		pt, _ := hm.resolveHeroTarget(slot)
		return []image.Point{pt}
	}
	if len(cands) > maxHeroGroundCandidates {
		cands = cands[:maxHeroGroundCandidates]
	}

	// ground_source must describe what actually LEADS the list: the siege
	// filter and the outward order both move the pin, and this field is how the
	// next live run judges whether the ordering fix worked.
	switch {
	case containsPoint(axisCands, cands[0]):
		source = "troop_ground_axis"
	case havePin && cands[0] == pinnedPt:
		source = "pinned"
	case containsPoint(formulaPts, cands[0]):
		source = "formula"
	case reordered:
		source = "outward_first"
	}
	hm.logger.Info().
		Str("unit", unitName).
		Str("ground_source", source).
		Interface("target", cands[0]).
		Int("alt_points", len(cands)-1).
		Int("troop_ground_points", len(hm.troopGround)).
		Bool("red_zone_checked", len(hm.zone.Polygon) >= 3).
		Msg("hero drop ground chosen")
	return cands
}

// captureSlotRatio reads the slot's activity ratio at (slot.X, slot.Y) once the
// card has stopped animating. Returns (ratio, capturedOK).
//
// The settle matters: a hero drop starts a ~1s deploy/cooldown animation, and a
// single fixed-delay read measures a mid-animation frame, which is why deployed
// heroes used to fail their own delta check and collect a retry tap (see
// slot_settle.go for the live numbers). The ratioHook seam short-circuits all of
// that to one scripted read for tests.
func (hm *HeroManager) captureSlotRatio(slot *TrackedSlot) (float64, bool) {
	if hm.ratioHook != nil {
		return hm.ratioHook()
	}
	return hm.executor.CaptureSettledSlotRatio(hm.w, slot)
}

// activateAbilities activates abilities for deployed heroes.
//
// Gated on hm.activateAbility (set by the orchestrator): the orchestrator
// owns ability activation in the dedicated "Abilities" phase, which runs
// AFTER the spell phases. The old always-on auto-activation here fired the
// abilities ~60ms after the heroes dropped and BEFORE the spell phase —
// re-selecting the hero card mid-deploy, then making the very next
// slot-selection tap land on the still-highlighted hero icon (live, this
// burned an ability and tapped the siege slot instead of the EQ card).
func (hm *HeroManager) activateAbilities(deployedSlots []*TrackedSlot) {
	if !hm.activateAbility {
		hm.logger.Debug().Int("count", len(deployedSlots)).Msg("skipping auto ability activation (deferred to Abilities phase)")
		return
	}
	if len(deployedSlots) == 0 {
		return
	}

	hm.logger.Info().Int("count", len(deployedSlots)).Msg("activating hero abilities")

	time.Sleep(100 * time.Millisecond)

	for _, slot := range deployedSlots {

		hm.executor.TapHeroAbility(slot)
		hm.logger.Info().
			Str("unit", slot.UnitName).
			Int("x", slot.X).
			Msg("hero ability activated")
		time.Sleep(250 * time.Millisecond)
	}
}

// pinnedHeroPoint returns the user's pinned hero point for this battle's side.
//
// Precision config is authored per side, with the four corner keys first and a
// physical-side fallback (top/bottom/left/right), so this mirrors the lookup
// resolveHeroTarget has always done. It only reports a point for a side the
// user actually pinned (SetPinnedGround), so an unpinned battle keeps whatever
// the strategy authored.
func (hm *HeroManager) pinnedHeroPoint() (image.Point, bool) {
	if !hm.pinnedGround {
		return image.Point{}, false
	}
	if pt, ok := hm.pCfg.HeroTargets[hm.targetEdge]; ok && (pt.X != 0 || pt.Y != 0) {
		return pt, true
	}
	if side := cornerToSide(hm.targetEdge); side != "" {
		if pt, ok := hm.pCfg.HeroTargets[side]; ok && (pt.X != 0 || pt.Y != 0) {
			return pt, true
		}
	}
	return image.Point{}, false
}

// resolveHeroTarget returns the deployment point for a hero.
//
// Order of precedence:
//  1. Formula entry (the user pinned a specific point or line) — wins
//     whenever a point OR a line is present, so users with both
//     formula.json AND pCfg.HeroTargets keep the formula's per-unit
//     pins and do not silently get demoted to the corner pin.
//  2. Per-corner pin from precision_config.json (pCfg.HeroTargets) —
//     looked up first by exact targetEdge (TopLeft/TopRight/...), then
//     by the physical side via cornerToSide (so users who pin
//     hero_targets.top get the same point for both top corners).
//  3. Grand Warden drops in the screen center (Eternal Tome radius).
//  4. Random interpolation along the chosen edge (legacy fallback).
func (hm *HeroManager) resolveHeroTarget(slot *TrackedSlot) (image.Point, image.Point) {
	unitName := strings.ToLower(slot.UnitName)

	// A side the user pinned by hand is authoritative: the formula's per-unit
	// hero points were authored for the formula's own side, so letting them win
	// is how a "user-pinned" battle ended up dropping heroes at the formula's
	// x while the log claimed the pin was in use. Same precedence as the pinned
	// deploy line; see SetPinnedGround.
	if pt, ok := hm.pinnedHeroPoint(); ok {
		hm.logger.Info().
			Str("unit", unitName).
			Str("corner", hm.targetEdge).
			Interface("target", pt).
			Msg("hero target from the USER's pinned hero point (outranks the strategy formula on this side)")
		return pt, pt
	}

	if entry, ok := hm.formulaEntry(unitName); ok {
		switch {
		case entry.IsPoint() && entry.P != nil:
			pt := entry.P.Image()
			return pt, pt
		case entry.IsLine() && entry.P1 != nil && entry.P2 != nil:
			p1 := entry.P1.Image()
			p2 := entry.P2.Image()
			t := rand.Float64()
			px := p1.X + int(float64(p2.X-p1.X)*t)
			py := p1.Y + int(float64(p2.Y-p1.Y)*t)
			return image.Pt(px, py), image.Pt(px, py)
		}
	}

	if pt, ok := hm.pCfg.HeroTargets[hm.targetEdge]; ok && (pt.X != 0 || pt.Y != 0) {
		hm.logger.Debug().
			Str("unit", unitName).
			Str("corner", hm.targetEdge).
			Interface("target", pt).
			Msg("hero target from precision_config.json")
		return pt, pt
	}
	if side := cornerToSide(hm.targetEdge); side != "" {
		if pt, ok := hm.pCfg.HeroTargets[side]; ok && (pt.X != 0 || pt.Y != 0) {
			hm.logger.Debug().
				Str("unit", unitName).
				Str("side", side).
				Interface("target", pt).
				Msg("hero target from precision_config.json (side fallback)")
			return pt, pt
		}
	}

	if strings.Contains(unitName, "warden") {
		pt := image.Pt(hm.w/2, hm.h/2)
		return pt, pt
	}

	edge, ok := hm.pCfg.Edges[hm.targetEdge]
	if !ok {
		return image.Pt(hm.w/2, hm.h/2), image.Pt(hm.w/2, hm.h/2)
	}
	scaled := ScaleEdge(edge, hm.pCfg.Width, hm.pCfg.Height, hm.w, hm.h)
	t := rand.Float64()
	px := scaled.P1.X + int(float64(scaled.P2.X-scaled.P1.X)*t)
	py := scaled.P1.Y + int(float64(scaled.P2.Y-scaled.P1.Y)*t)
	pt := image.Pt(px, py)
	return pt, pt
}

// DeployTroops deploys a group of regular troops (non-hero, non-spell).
//
// Root-cause fix for the "balloons/EDs sometimes don't all get placed"
// user-reported bug: the previous path fired `count` taps and either
// returned success (formula-driven) or did a single visual-empty check,
// then marked the slot SlotDeployed regardless of whether troop icons
// remained. When the cached detectedCount was wrong (template-OCR is
// brittle across themes / emulator sizes), troops were left behind
// without ever being re-counted.
//
// The new path:
//  1. Live-OCR the slot's per-card count BEFORE the main tap pass.
//     Live count wins over detectedCount/YAML when > 0. When OCR fails
//     AND the slot is visually empty, the deploy is a true no-op and
//     we mark deployed without firing taps.
//  2. Main pass fires exactly `count` taps on the formula/pinned or
//     legacy edge line.
//  3. Reconcile loop: up to reconcileRounds rounds, each one captures
//     a fresh screen, live-OCRs + visual-empty checks, and re-selects
//     + fires compensating taps if there's still count > 0. The slot
//     is only MarkDeployed after a (live OCR == 0 AND visual-empty)
//     confirmation — or after the reconcile budget runs out, in which
//     case we record SlotAttempted so the sweep phase retries with its
//     own reconcile loop.
func (hm *HeroManager) DeployTroops(
	unit strategy.Unit,
	slot *TrackedSlot,
	pattern string,
	offset int,
	phasePattern string,
	screen gocv.Mat,
	detectedCount int,
	detectedTrusted bool,
) bool {
	unitName := strings.ToLower(strings.TrimSpace(unit.Name))
	isFourSides := pattern == "FourSides" || phasePattern == "FourSides"

	if isFourSides {

		cfg := hm.pCfg
		if offset > 0 {
			cfg.Edges = make(map[string]ManualEdge)
			cx, cy := hm.w/2, hm.h/2
			pct := float64(offset) / 200.0
			for k, e := range hm.pCfg.Edges {
				p1 := image.Pt(
					int(float64(e.P1.X)+float64(cx-e.P1.X)*pct),
					int(float64(e.P1.Y)+float64(cy-e.P1.Y)*pct),
				)
				p2 := image.Pt(
					int(float64(e.P2.X)+float64(cx-e.P2.X)*pct),
					int(float64(e.P2.Y)+float64(cy-e.P2.Y)*pct),
				)
				cfg.Edges[k] = ManualEdge{P1: p1, P2: p2}
			}
		}
		count := 12
		if detectedCount > 0 {
			count = (detectedCount + 3) / 4
			if count < 4 {
				count = 4
			}
		}
		hm.executor.TapDeployFourSides(cfg, hm.targetEdge, count, 8)

		hm.slotManager.MarkDeployed(unitName)
		return true
	}

	var p1, p2 image.Point
	// geometry sourcing: the user's pins, else the strategy formula, else the
	// legacy edge pin. See SetPinnedGround for why the pins outrank the formula.
	src := "edge"
	hasFormula := false
	if hm.pinnedGround && len(hm.deployLine.Points) >= 2 {
		p1 = hm.deployLine.Points[0]
		p2 = hm.deployLine.Points[len(hm.deployLine.Points)-1]
		src = "pin"
	} else if entry, ok := hm.formulaEntry(unit.Name); ok {
		switch {
		case entry.IsLine() && entry.P1 != nil && entry.P2 != nil:
			p1 = entry.P1.Image()
			p2 = entry.P2.Image()
			hasFormula = true
		case entry.IsPoint() && entry.P != nil:
			p := entry.P.Image()
			p1, p2 = p, p
			hasFormula = true
		default:

			hasFormula = false
		}
	}
	if hasFormula {
		src = "formula"
	}
	if src == "edge" {
		p1, p2 = hm.resolveTroopTarget(slot, offset)
	}

	// Remember this pass's ground for the hero phase that follows: these taps
	// were accepted by the game moments ago (see hero_manager.go's troopGround).
	hm.recordGround(p1, p2)

	// The fresh pre-deploy read is only worth its ~300ms when nothing
	// trustworthy already measured this card. The orchestrator reads the whole
	// bar over several settled frames before the first drop
	// (DetectCountsMerged, whose OK flag is `detectedTrusted`), and that reading
	// is strictly better than one frame captured mid-animation here. So the
	// fresh read stays as the fallback and is skipped when the battle-start read
	// is a real measurement of a card holding units — in which case the
	// already-empty branch below could not have fired anyway.
	preCount, preTrusted, preVisualEmpty := 0, false, false
	if !(detectedTrusted && detectedCount > 0) {
		preCount, preTrusted, preVisualEmpty = hm.liveCountAndEmpty(slot)
	}
	if preVisualEmpty && preTrusted && preCount <= 0 {
		hm.logger.Info().
			Str("unit", unit.Name).
			Bool("formula_pinned", hasFormula).
			Msg("slot already empty at deploy start; marking deployed (no taps fired)")
		hm.slotManager.MarkDeployed(unitName)
		return true
	}

	tapCount, tapCountMeasured := hm.resolveLiveTapCountMeasured(unit, slot, preCount, preTrusted, detectedCount)
	if entry, ok := hm.formulaEntry(unit.Name); ok && entry.Count > tapCount {
		tapCount = entry.Count
		tapCountMeasured = true
	}
	if strings.EqualFold(unit.Amount, "All") && p1 != p2 {
		minTaps := 16
		if strings.Contains(unitName, "dragon") {
			minTaps = 12
		}
		if tapCount < minTaps {
			tapCount = minTaps
			tapCountMeasured = true
		}
	}

	var deployed func(int, int)
	if p1 == p2 {
		deployed = func(c, j int) { hm.executor.TapDeployPoint(p1, c, j) }
	} else {
		deployed = func(c, j int) { hm.executor.TapDeployLine(p1, p2, c, j) }
	}
	if src != "edge" {
		hm.logger.Info().
			Str("unit", unit.Name).
			Str("src", src).
			Int("count", tapCount).
			Bool("count_measured", tapCountMeasured).
			Int("live_count", preCount).
			Bool("live_count_trusted", preTrusted).
			Int("detected_count", detectedCount).
			Interface("p1", p1).
			Interface("p2", p2).
			Msg("deploying troop (geometry-sourced live-count)")
	} else {
		hm.logger.Info().
			Str("unit", unit.Name).
			Str("src", "edge").
			Int("count", tapCount).
			Bool("count_measured", tapCountMeasured).
			Int("live_count", preCount).
			Bool("live_count_trusted", preTrusted).
			Int("detected_count", detectedCount).
			Msg("deploying troop (live-count-driven)")
	}
	deployed(tapCount, hm.lineJitter(hasFormula))

	const reconcileRounds = 3
	const reconcileSettleMs = 150
	// unreadableReads counts the read-only retries below, taken when the card's
	// count label will not come back after its own deployment. See the stop rule
	// in the loop.
	unreadableReads := 0
	for round := 0; round < reconcileRounds; round++ {
		// Battle-timer guard: the reconcile top-ups exist to catch genuine
		// drops CoC swallowed, not to re-fire a spent card forever. Once
		// the deploy budget is gone the slot is left for the (also
		// budget-guarded) sweep.
		if hm.executor.DeployBudgetExhausted() {
			hm.logger.Warn().Str("unit", unit.Name).Msg("troop reconcile: deploy budget exhausted; stopping")
			hm.slotManager.RecordAttempt(unitName, false)
			return false
		}
		hm.executor.HumanSleep(reconcileSettleMs, 30)

		live, trusted, visualEmpty := hm.liveCountAndEmpty(slot)
		// "Done" needs the count read and the visual check to agree — or, when
		// the reader cannot read this frame at all, the visual check alone,
		// which is the same signal this loop had before OCR existed. A blind
		// zero must never be able to confirm a deploy.
		if visualEmpty && (!trusted || live <= 0) {
			hm.slotManager.MarkDeployed(unitName)
			hm.logger.Info().
				Str("unit", unit.Name).
				Int("round", round+1).
				Int("fired_total", tapCount).
				Bool("count_read_trusted", trusted).
				Msg("reconcile confirmed slot empty; deploy complete")
			return true
		}
		if trusted && live <= 0 {
			// A real zero on a card that is not visually empty. Previously this
			// branch fired for EVERY slot on every round, because the reader was
			// sampling the wrong part of the bar and could not return anything
			// but zero — so the log read "transient ghost" three times and the
			// slot went back to the sweep without a single top-up tap.
			hm.logger.Warn().
				Str("unit", unit.Name).
				Int("round", round+1).
				Bool("visual_empty", visualEmpty).
				Msg("reconcile: card shows no count but the slot is not visually empty; treating as transient ghost")
			continue
		}

		// No readable count, and the main pass already fired a MEASURED count.
		//
		// A blind batch is a guess about a card nobody could measure, and when the
		// main pass measured the card there is nothing left to guess: the taps that
		// place those units have already gone out. Firing three more at a card
		// that is merely mid-animation (a selected card's count label animates
		// away for the length of the drop sequence) is what a live 720p run did
		// three rounds in a row: 9 extra taps at a 10-Balloon card the deploy had
		// already drained, ~2s per troop unit, no measurable gain. So re-read
		// instead — the label comes back when the animation ends — and if it
		// never does, stop here and let the sweep decide from a fresh frame.
		//
		// The blind batch stays for the case it was written for: a main pass that
		// was itself blind, where no measurement exists and a bounded guess with a
		// visual-check reconcile is strictly better than firing nothing.
		if !trusted && tapCountMeasured {
			unreadableReads++
			if unreadableReads >= maxUnreadableReconcileReads {
				if strings.EqualFold(unit.Amount, "All") && tapCount >= 12 && p1 != p2 {
					hm.logger.Info().
						Str("unit", unit.Name).
						Int("reads", unreadableReads).
						Int("fired_total", tapCount).
						Msg("reconcile: saturated 'All' line pass fired; marking slot deployed so sweeper does not circle back")
					hm.slotManager.MarkDeployed(unitName)
					return true
				}
				hm.logger.Warn().
					Str("unit", unit.Name).
					Int("reads", unreadableReads).
					Int("fired_total", tapCount).
					Msg("reconcile: count label still unreadable and the main pass already fired the measured count; stopping without blind top-up taps (the sweep re-checks on a fresh frame)")
				hm.slotManager.RecordAttempt(unitName, false)
				return false
			}
			hm.logger.Info().
				Str("unit", unit.Name).
				Int("round", round+1).
				Int("reads", unreadableReads).
				Msg("reconcile: count label unreadable after a measured main pass; re-reading instead of firing blind top-up taps")
			continue
		}

		// Fire what is left. When the count is untrusted there is no number to
		// fire, so use the bounded blind batch and let the visual check on the
		// next round decide — never invent a count from nothing.
		fire := live
		if !trusted || fire <= 0 {
			fire = blindBatchTaps
		}
		hm.executor.TapSlot(slot, 4)
		hm.executor.HumanSleep(reconcileSettleMs, 30)
		deployed(fire, hm.lineJitter(hasFormula))
		hm.logger.Info().
			Str("unit", unit.Name).
			Int("round", round+1).
			Int("remaining", live).
			Bool("count_read_trusted", trusted).
			Int("fired", fire).
			Msg("reconcile: re-selected slot and fired top-up taps")
	}

	hm.logger.Warn().
		Str("unit", unit.Name).
		Int("reconcile_rounds", reconcileRounds).
		Int("initial_count", tapCount).
		Msg("reconcile exhausted; leaving slot in SlotAttempted for sweep to retry")
	hm.slotManager.RecordAttempt(unitName, false)
	return false
}

// DeploySiege deploys a siege machine.
//
// Formula takes precedence: when the user authored a "point" or "line"
// entry for this siege (e.g. stone_slammer → {"type":"point","p":{...}}),
// the user-pinned geometry wins and the legacy pCfg.Edges path is
// skipped. Falls back to the dynamic red-zone edge line otherwise.
//
// Live-test fix: ON SUCCESS the slot is marked deployed via
// slot.UnitName (canonical key in unitIndex), NOT via unit.Name.
// Template matching may identify the slot under a slightly different
// spelling than the strategy YAML uses, so the
// strategy-derived `unit.Name` key often fails GetSlot() lookup and
// MarkDeployed becomes a silent no-op. That left the slot in a
// non-terminal state and the sweeper picked it up — 3 deploySlot
// retries × ~12 taps each = ~36 wasted taps per attack.
//
// Live-test fix #2: the legacy path no longer polls
// isSlotEmptyStatic after the drop. Siege machines in CoC NEVER
// transition the troop-bar slot back to a clean "empty" state on
// success — the slot visually persists as the next queued icon
// (often a CC-troop icon) or a skeleton silhouette until the next
// production cycle. Trusting the tap and marking deployed directly
// is the only safe path.
// confirmSiegeDrop looks back at the siege card after the deploy tap.
//
// Live run1 2026-09-26 left the siege with "taps were sent but the log carries
// no completion check for it" — attack_report read the whole run as UNVERIFIED
// because nothing ever looked back.
//
// The signal is a PRE/POST DELTA on the card's activity ratio, not an absolute
// empty check: a spent siege card keeps a dimmed cooldown silhouette (like a
// hero card), so visuallyEmpty stays false after a SUCCESSFUL drop — runs 3/4
// read exactly that and warned "OCR and the visual check disagree" against a
// slot the verifier cleared seconds later. Same settle policy as the hero drop
// check (the drop animates ~1s). NO blind re-tap: a second tap while the machine
// is dropping can hit the deployed machine (the retap-kills-siege bug the
// single-tap path exists to avoid).
func (hm *HeroManager) confirmSiegeDrop(unit strategy.Unit, slot *TrackedSlot) {
	// One settled look, not two. A spent siege card keeps a dimmed silhouette
	// and the next queued icon can sit on the same slot, so the old pre/post
	// delta pair proved no more than a single settled read plus the count label
	// — while costing a pre-capture + 400ms + post-capture (~1.2s) of the 7s
	// deploy window watching a card this function only reports on. NO blind
	// re-tap either way: a second tap while the machine is dropping can hit the
	// deployed machine (see PlaceSiege).
	time.Sleep(250 * time.Millisecond)
	ratio, ratioOK := hm.executor.CaptureSettledSlotRatio(hm.w, slot)
	count, trusted, _ := hm.liveCountAndEmpty(slot)
	switch {
	case trusted && count == 0:
		hm.logger.Info().Str("unit", unit.Name).Msg("reconcile: siege count label gone; slot drained")
	case ratioOK && ratio <= heroConsumedRatio:
		hm.logger.Info().Str("unit", unit.Name).
			Float64("post_ratio", ratio).
			Msg("reconcile: siege slot dimmed after deploy tap")
	case ratioOK:
		hm.logger.Warn().Str("unit", unit.Name).
			Float64("post_ratio", ratio).
			Int("count", count).
			Bool("count_read_trusted", trusted).
			Msg("siege card still bright after deploy tap; the machine may not have dropped (no re-tap — a second tap would hit it)")
	default:
		hm.logger.Warn().Str("unit", unit.Name).Msg("siege settle check could not capture the screen; deployment unconfirmed")
	}
}

func (hm *HeroManager) DeploySiege(unit strategy.Unit, slot *TrackedSlot) bool {
	// The user pinned a hero point for this side and asked for the siege to land
	// on the same spot: the machine screens the heroes, so it belongs on their
	// ground rather than on the strategy's "Center" — the middle of the base,
	// where the game refuses the tap. It outranks the formula for the same
	// reason the pinned deploy line does; see SetPinnedGround.
	if pt, ok := hm.pinnedHeroPoint(); ok {
		tap := hm.executor.addJitter(pt, 4)
		hm.logger.Info().
			Str("unit", unit.Name).
			Interface("hero_point", pt).
			Interface("tap", tap).
			Msg("siege placed on the user's pinned hero point (the one and only placement tap)")
		hm.executor.PlaceSiege(slot, tap, 12.0)
		hm.confirmSiegeDrop(unit, slot)
		hm.slotManager.MarkDeployed(slot.UnitName)
		return true
	}

	if entry, ok := hm.formulaEntry(unit.Name); ok {
		if hm.deploySiegeFromFormula(unit, slot, entry) {
			hm.confirmSiegeDrop(unit, slot)
			hm.slotManager.MarkDeployed(slot.UnitName)
			return true
		}
	}

	edge, ok := hm.pCfg.Edges[hm.targetEdge]
	if !ok {
		hm.logger.Warn().Msg("no edge configured for siege deployment")
		return false
	}
	scaled := ScaleEdge(edge, hm.pCfg.Width, hm.pCfg.Height, hm.w, hm.h)
	p1, p2 := scaled.P1, scaled.P2

	hm.logger.Info().Str("unit", unit.Name).Msg("deploying siege machine (single tap)")
	mid := image.Pt((p1.X+p2.X)/2, (p1.Y+p2.Y)/2)
	j := hm.executor.addJitter(mid, 10)
	hm.executor.PlaceSiege(slot, j, 12.0)

	time.Sleep(80 * time.Millisecond)
	hm.slotManager.MarkDeployed(slot.UnitName)
	return true
}

// resolveTroopTarget returns the deployment line for a troop.
func (hm *HeroManager) resolveTroopTarget(slot *TrackedSlot, offset int) (image.Point, image.Point) {
	edge, ok := hm.pCfg.Edges[hm.targetEdge]
	if !ok {
		return image.Pt(hm.w/2, hm.h/2), image.Pt(hm.w/2, hm.h/2)
	}
	scaled := ScaleEdge(edge, hm.pCfg.Width, hm.pCfg.Height, hm.w, hm.h)
	p1, p2 := scaled.P1, scaled.P2

	if offset > 0 {
		centerX, centerY := hm.w/2, hm.h/2
		pct := float64(offset) / 200.0
		p1 = image.Pt(int(float64(p1.X)+float64(centerX-p1.X)*pct), int(float64(p1.Y)+float64(centerY-p1.Y)*pct))
		p2 = image.Pt(int(float64(p2.X)+float64(centerX-p2.X)*pct), int(float64(p2.Y)+float64(centerY-p2.Y)*pct))
	}

	return p1, p2
}

// parseAmount parses an amount string to int.
func parseAmount(s string) int {
	if s == "All" || s == "" {
		return 0
	}
	val := 0
	for _, c := range s {
		if c >= '0' && c <= '9' {
			val = val*10 + int(c-'0')
		}
	}
	return val
}

// formulaEntry is a nil-safe wrapper around Formula.LookUp for use by the
// deploy path. When the bot didn't load a formula (file missing or
// invalid) this returns (_, false) and the caller falls back to pCfg
// coordinates / dynamic red zone.
func (hm *HeroManager) formulaEntry(unitName string) (formula.UnitEntry, bool) {
	if hm.formula == nil {
		return formula.UnitEntry{}, false
	}
	return hm.formula.LookUp(unitName)
}

// (deployFromFormula — REMOVED. DeployTroops now owns the formula-driven
// path inline, with the live-OCR reconcile loop. The previous standalone
// function returned true unconditionally after firing its taps and marked
// the slot SlotDeployed without verifying — the silent under-deployment
// bug for balloons/EDs. The SpellDeployer has its own private
// deployFromFormula for spell-specific deployment and is unrelated.)

// deploySiegeFromFormula drops the siege at the user-pinned point (or
// along a pinned line for wrecker-style multi-tap drops). When the
// formula has no entry the caller falls back to the pCfg edge path.
func (hm *HeroManager) deploySiegeFromFormula(unit strategy.Unit, slot *TrackedSlot, entry formula.UnitEntry) bool {
	jitter := entry.Jitter
	if jitter <= 0 {
		jitter = 6
	}
	// EXACTLY ONE field tap: a siege machine deploys on the first tap and
	// every further tap hits the DEPLOYED machine on the field — live,
	// that retapping killed it instantly (user-reported: "it retapped siege
	// destroying it"). The old 12-tap drop was copied from the troop
	// spam-batch shape and is flatly wrong for a single-unit deploy.
	switch {
	case entry.IsPoint() && entry.P != nil:
		p := entry.P.Image()
		hm.logger.Info().Str("unit", unit.Name).Interface("p", p).
			Msg("formula-driven siege placement (single tap)")
		j := hm.executor.addJitter(p, jitter)
		hm.executor.PlaceSiege(slot, j, 12.0)
		return true
	case entry.IsLine() && entry.P1 != nil && entry.P2 != nil:
		p1 := entry.P1.Image()
		p2 := entry.P2.Image()
		hm.logger.Info().Str("unit", unit.Name).Interface("p1", p1).Interface("p2", p2).
			Msg("formula-driven siege placement (single tap on line midpoint)")
		mid := image.Pt((p1.X+p2.X)/2, (p1.Y+p2.Y)/2)
		j := hm.executor.addJitter(mid, jitter)
		hm.executor.PlaceSiege(slot, j, 12.0)
		return true
	}
	return false
}

// maxUnreadableReconcileReads bounds the READ-ONLY retries the troop reconcile
// loop spends when a card's count label does not come back after its own
// deployment. The label animates away while a card is selected and mid-drop, so
// an unreadable label is a timing fact about the frame, not evidence about the
// card — the answer is to look again, briefly, not to fire taps nobody measured.
// Two reads is about one second; after that the sweep decides from a fresh
// frame. See the stop rule in DeployTroops.
const maxUnreadableReconcileReads = 2

// resolveFormulaCount is removed: DeployTroops uses resolveLiveTapCount
// (which threads live OCR + heuristic fallbacks) directly, including the
// "+1 when count >= 6" safety pad for OCR under-reads. The dedicated
// formula-only helper is no longer reachable.

// liveCountAndEmpty is a thin shim to the shared captureSlotLiveCount
// helper in live_count.go. Kept as a method to keep call-site code
// readable inside HeroManager.DeployTroops's reconcile loop.
func (hm *HeroManager) liveCountAndEmpty(slot *TrackedSlot) (int, bool, bool) {
	return captureSlotLiveCount(
		hm.executor,
		hm.troopCounter,
		slot,
		hm.slotManager.GetBarY(),
		hm.w, hm.h,
	)
}

// resolveLiveTapCount chooses the canonical tap count for the main pass.
// Order of precedence:
//
//  1. Live OCR count from the pre-deploy screen (most authoritative —
//     captures whatever CoC currently shows). Padded by +1 when ≥ 6 to
//     absorb single-digit OCR under-reads. Used only when liveTrusted:
//     an untrusted zero is "unknown", not "no troops".
//  2. detectedCount (the once-cached orchestrator-start OCR). Same +1
//     pad. Fallback when live OCR is unavailable (e.g. troopCounter
//     not threaded through).
//  3. YAML amount (parseAmount with the "All" → 0 → default path). This is
//     the army the user configured, so it is a real number rather than a
//     guess, and it is the preferred stand-in whenever OCR comes back
//     unreadable.
//  4. Bounded blind batch (blindBatchTaps) plus the reconcile loop — the
//     last resort when neither the screen nor the strategy states a count.
//     It is deliberately small and labelled `count_read_trusted=false` in
//     the log so it can never be mistaken for a measurement; the reconcile
//     loop is what guarantees the card actually drains.
// resolveLiveTapCount is the measurement-blind entry point: the count alone.
// Kept for callers (and tests) that do not care how the number was obtained.
func (hm *HeroManager) resolveLiveTapCount(unit strategy.Unit, slot *TrackedSlot, liveCount int, liveTrusted bool, detectedCount int) int {
	n, _ := hm.resolveLiveTapCountMeasured(unit, slot, liveCount, liveTrusted, detectedCount)
	return n
}

// resolveLiveTapCountMeasured is resolveLiveTapCount plus the fact the reconcile
// loop needs to decide whether a blind top-up batch is admissible: whether the
// number is a MEASUREMENT (live OCR, the orchestrator's settled battle-start
// read, or the strategy's own amount) or the bounded blind batch that exists
// only because nothing could be measured at all. The distinction is invisible
// from the number itself — blindBatchTaps is a plausible troop count — and it is
// exactly the distinction that decides whether more taps can mean anything.
func (hm *HeroManager) resolveLiveTapCountMeasured(unit strategy.Unit, slot *TrackedSlot, liveCount int, liveTrusted bool, detectedCount int) (int, bool) {
	const padFloor = 6
	pad := func(n int) int {
		if n >= padFloor {
			return n + 1
		}
		if n > 0 {
			return n
		}
		return 0
	}
	if liveTrusted {
		if v := pad(liveCount); v > 0 {
			hm.logger.Debug().
				Str("unit", unit.Name).
				Int("live_count", liveCount).
				Int("padded", v).
				Msg("tap count from live OCR")
			return v, true
		}
	}
	if v := pad(detectedCount); v > 0 {
		hm.logger.Debug().
			Str("unit", unit.Name).
			Int("detected_count", detectedCount).
			Int("padded", v).
			Msg("tap count from orchestrator-cached detected count")
		return v, true
	}
	if unit.Amount != "" && unit.Amount != "All" {
		if v := parseAmount(unit.Amount); v > 0 {
			hm.logger.Debug().
				Str("unit", unit.Name).
				Str("amount", unit.Amount).
				Int("count", v).
				Msg("tap count from YAML amount")
			return v, true
		}
	}

	hm.logger.Warn().
		Str("unit", unit.Name).
		Bool("live_count_trusted", liveTrusted).
		Int("detected_count", detectedCount).
		Str("amount", unit.Amount).
		Int("blind_batch", blindBatchTaps).
		Msg("tap count unresolved (no readable card count and no strategy amount); firing a blind batch and reconciling")
	return blindBatchTaps, false
}

// lineJitter returns the per-tap jitter for the main/reconcile tap
// sequence. Formula-pinned units have authored jitter (default 3); the
// legacy edge path uses 10 for lines / 8 for points (matching the
// pre-refactor values).
func (hm *HeroManager) lineJitter(formulaPinned bool) int {
	if formulaPinned {
		return 3
	}
	return 10
}
