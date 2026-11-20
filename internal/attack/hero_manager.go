package attack

import (
	"fmt"
	"image"
	"math"
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

	lastTroopLineP1 image.Point
	lastTroopLineP2 image.Point
	hasTroopLine    bool

	// ratioHook, when non-nil, replaces the screen capture inside
	// captureSlotRatio. Test seam only — nil in production.
	ratioHook func() (float64, bool)

	// liveHook, when non-nil, replaces the capture + OCR inside
	// liveCountAndEmpty. Test seam only — nil in production.
	liveHook func() (int, bool, bool)

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
// confirmation wave (see confirmHeroWave). Deterministic reads beat the old
// settle-poll, which could lock onto a stable PRE-transition frame and report a
// drop that had actually landed — live 2026-10-01 the Dragon Duke measured
// "not placed" at every read, then showed consumed before the sweep's first tap.
// The third, late read exists for exactly that card: live 2026-11-16 both heroes
// were placed on the wave's first tap yet still read ~0.75 at 700 ms, so the
// wave re-armed them and fired at spent cards; six seconds later the same cards
// read ~0.22. 2.5 s clears the drop animation, and a confirmed wave exits early.
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
		cands := hm.heroDropCandidates(d.Slot, d.Unit.Name)
		// Lead with the outward ladder step, not the raw troop-line point.
		// Live 2026-11-19: the line point (683,172) was refused for every hero
		// while the ladder point 40px outward (723,197) landed every time; the
		// same happened again with 801,501 refused. Step outward from the line
		// ground before the first tap so the drop lands without a rescue tap.
		// Point-pattern heroes lead with the strategy's formula point: that is
		// the ground the sweep uses, and live it has landed on its first tap.
		// The troop-line lead was refused for every hero in the 2026-11-19 raids
		// (683,172 and 801,501), so it is not proven ground for heroes.
		if d.Unit.Pattern == "Point" {
			if entry, ok := hm.formulaEntry(d.Unit.Name); ok {
				if fps := heroFormulaCandidates(entry, hm.h); len(fps) > 0 {
					cands = append([]image.Point{fps[0]}, cands...)
				} else if len(cands) > 0 {
					if out := outwardHeroLadder(cands[0], hm.deployLine.Points, hm.w, hm.h, 1); len(out) > 0 {
						cands = append([]image.Point{out[0]}, cands...)
					}
				}
			} else if len(cands) > 0 {
				if out := outwardHeroLadder(cands[0], hm.deployLine.Points, hm.w, hm.h, 1); len(out) > 0 {
					cands = append([]image.Point{out[0]}, cands...)
				}
			}
		}
		batch = append(batch, &heroBatchEntry{d: d, cands: cands})
	}

	// Wave 0: the drops, back to back. No captures between them.
	for i, p := range batch {
		if len(p.cands) == 0 {
			// No ground inside the deployable band survived selection. Tapping
			// the card here would spend the placement on a guess; report the
			// hero failed so the report cannot call the run clean.
			hm.logger.Warn().
				Str("unit", p.d.Unit.Name).
				Int("slot_x", p.d.Slot.X).
				Msg("hero has no acceptable ground in the deployable band; reporting it failed")
			p.d.Slot.State = SlotFailed
			if hm.slotManager != nil {
				hm.slotManager.MarkFailed(p.d.Unit.Name)
			}
			continue
		}
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
		// One selection tap, then the drop. The old second selection ("prime")
		// did not stop the first drop being refused live, and only added a tap;
		// see maxHeroSpotTaps. Settle long enough for the card animation.
		hm.executor.client.HumanSleep(heroFirstArmMs, 30)
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

	// No rescue pass. A hero gets one selection tap, one field tap and its
	// ability. If the drop is unconfirmed, the slot is reported and left alone:
	// re-arming and re-dropping was the 4th, 5th and 6th tap the user saw on
	// every hero, every raid (live 2026-11-19).

	// Verdicts, and the log lines attack_report reads.
	var deployed []*TrackedSlot
	for _, p := range batch {
		if p.placed {
			deployed = append(deployed, p.d.Slot)
			hm.logger.Info().
				Str("unit", p.d.Unit.Name).
				Int("slot_x", p.d.Slot.X).
				Msg("hero deployed (single card tap + single field tap)")
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
	// Three beats in production: the late one is the card's settle read (see
	// heroConfirmLateMs). Debug adds one more frame dump after it.
	beats := 3
	if dbg != "" {
		beats = 4
		_ = os.MkdirAll(dbg, 0o755)
	}
	for beat := 0; beat < beats; beat++ {
		switch beat {
		case 0:
			hm.executor.client.HumanSleep(heroConfirmSettleMs, 30)
		case 1:
			hm.executor.client.HumanSleep(heroConfirmFollowMs, 40)
		case 2:
			hm.executor.client.HumanSleep(heroConfirmLateMs, 50)
		default:
			hm.executor.client.HumanSleep(heroConfirmSettleMs, 40)
		}
		// Deliberately NOT gated on DeployBudgetExhausted: this is a read, not a
		// tap, and abandoning it on the budget is what made a deployed hero look
		// refused (live 2026-11-16: the window was already over, so the wave read
		// once during the drop animation, re-armed the spent card and left it to
		// the sweep). A wrong verdict costs a card tap; a read costs 300 ms.
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
			if !placed && haveFrame && heroHpBarVisible(frame, p.d.Slot, hm.w) {
				placed = true
				hm.logger.Info().
					Str("unit", p.d.Unit.Name).
					Int("slot_x", p.d.Slot.X).
					Msg("hero card shows the deployed health bar")
			}
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
	// Jitter must not push a banded point back into the HUD or the troop bar:
	// a tap there is not a placement the game can accept (run 11 also produced
	// an off-band siege retry at y=94).
	j = ClampToDeployBand(j, UICutoff(hm.h))
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
	rearm func() bool,
) bool {
	return runHeroFieldRetries(
		cands, baseline, baselineOK,
		rearm,
		func(pt image.Point) { hm.fireHeroDrop(unitName, pt, false, 3) },
		func() (float64, bool) {
			hm.executor.client.HumanSleep(300, 40)
			return hm.captureSlotRatio(slot)
		},
		func() bool {
			// The hero's own card: count badge gone means the game took the drop,
			// whatever the slot ratio still shows mid-animation. Needs the bar
			// reader, which the hand-built unit harnesses do not wire.
			if hm.liveHook == nil && (hm.slotManager == nil || hm.troopCounter == nil) {
				return false
			}
			_, ok, empty := hm.liveCountAndEmpty(slot)
			return ok && empty
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

// heroRescueGround orders the ground a refused hero walks: the refused point at
// index 0 (the shape runHeroFieldRetries expects), then outward steps from it,
// then whatever else the picker authored. A refusal is a region, not a pixel, so
// the useful next tap is a tile further out — the same walk the outward ladder
// already encodes for a refused pin.
func (hm *HeroManager) heroRescueGround(fired image.Point, cands []image.Point) []image.Point {
	out := make([]image.Point, 0, 1+heroFieldRetries+len(cands))
	out = append(out, fired)
	out = append(out, outwardHeroLadder(fired, hm.deployLine.Points, hm.w, hm.h, heroFieldRetries)...)
	for _, p := range cands {
		// Authored points that sit behind the HUD or the troop bar are not taps:
		// the game never sees them and the retry budget burns on nothing.
		if p.Y < BandTop() || p.Y > UICutoff(hm.h) || p.X < 0 || p.X >= hm.w {
			continue
		}
		out = append(out, p)
	}
	return dedupePoints(out)
}

// heroLineGrounds returns up to four points on the troop deploy line near the
// hero's authored intent, clamped into the deployable band. These are the first
// taps' ground: the line is the only ground this battle has already proven the
// game accepts. One point is used when the intent is already close to the line;
// otherwise the projection plus a short walk either way gives each hero in a
// wave its own proven tile.
func (hm *HeroManager) heroLineGrounds(intent image.Point) []image.Point {
	near, d, ok := nearestDeployLinePoint(intent, hm.deployLine)
	if !ok {
		return nil
	}
	bottom := UICutoff(hm.h)
	var out []image.Point
	add := func(p image.Point) {
		// Bound the line lead itself, so the authored point always survives the
		// final candidate ceiling as an alternative.
		if len(out) >= maxHeroGroundCandidates-1 {
			return
		}
		p = ClampToDeployBand(p, bottom)
		if p.X < 8 {
			p.X = 8
		}
		if p.X >= hm.w-8 {
			p.X = hm.w - 8
		}
		// A measured red outline overrides the line's usual authority: run 12's
		// line was a stale edge that cut into the base, and the formula point
		// outside the zone was the ground that landed. Only keep line points the
		// no-deploy outline does not claim.
		if len(hm.zone.Polygon) >= 3 && hm.zone.Inside(p.X, p.Y) {
			return
		}
		for _, q := range out {
			if q == p {
				return
			}
		}
		out = append(out, p)
	}
	if d >= 24 {
		add(near)
	}
	p1, p2 := hm.deployLine.Points[0], hm.deployLine.Points[len(hm.deployLine.Points)-1]
	dx, dy := float64(p2.X-p1.X), float64(p2.Y-p1.Y)
	if l := math.Hypot(dx, dy); l > 0 {
		ux, uy := dx/l, dy/l
		for _, step := range []float64{40, 85} {
			add(image.Pt(near.X+int(math.Round(ux*step)), near.Y+int(math.Round(uy*step))))
			add(image.Pt(near.X-int(math.Round(ux*step)), near.Y-int(math.Round(uy*step))))
		}
	}
	// Leave one slot for the authored pin/formula point: the line leads, but a
	// known-good authored point must stay available as the next alternative.
	if len(out) < maxHeroGroundCandidates-1 {
		add(near)
	}
	return out
}

// nearestDeployLinePoint returns the closest point on the deploy line's
// segments, its distance, and whether the line has a usable segment.
func nearestDeployLinePoint(p image.Point, line DeployLine) (image.Point, float64, bool) {
	best := image.Point{}
	bestD := math.Inf(1)
	ok := false
	for i := 1; i < len(line.Points); i++ {
		a, b := line.Points[i-1], line.Points[i]
		dx, dy := float64(b.X-a.X), float64(b.Y-a.Y)
		len2 := dx*dx + dy*dy
		if len2 == 0 {
			continue
		}
		t := (float64(p.X-a.X)*dx + float64(p.Y-a.Y)*dy) / len2
		t = math.Max(0, math.Min(1, t))
		q := image.Pt(a.X+int(math.Round(t*dx)), a.Y+int(math.Round(t*dy)))
		d := math.Hypot(float64(p.X-q.X), float64(p.Y-q.Y))
		if d < bestD {
			best, bestD, ok = q, d, true
		}
	}
	return best, bestD, ok
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
//  1. The user's pinned hero point, else the strategy formula's per-unit point.
//  2. The axis this battle's troop pass just used, when those accepted taps
//     share one (a diagonal line does NOT: its interior is not proven ground).
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
	pinnedPt, havePin := hm.pinnedHeroPoint()

	// The authored intent for this hero: the user's pin for this side, else the
	// strategy formula's per-unit point. Both are ground a human picked, and the
	// sweep (which places heroes at the formula point) lands it on the first tap
	// in every live battle where the phases do not.
	var formulaPts []image.Point
	if hasFormula {
		formulaPts = heroFormulaCandidates(entry, hm.h)
	}
	var intent image.Point
	haveIntent := false
	if havePin {
		intent, haveIntent = pinnedPt, true
	} else if len(formulaPts) > 0 {
		intent, haveIntent = formulaPts[0], true
	}

	var cands []image.Point
	source := "deploy_line"

	// LEAD FROM THE LINE, not the authored point. The line was accepted by the
	// troop pass seconds ago; the pinned/formula point is only an intention.
	// Runs 10 and 11: every leading authored point was refused, and the first
	// retry on the line landed. Run 10 is the formula case (MP (240,535) refused,
	// line (236,464) landed; RC (283,169) refused, line endpoint (144,394)
	// landed). Run 11 is the pin case (321,207) refused for the siege and both
	// heroes, line (292,242) landed. Several line points lead so a two-hero wave
	// spreads across proven ground instead of putting hero #2 on the refused
	// authored point. The authored point is kept next, and every candidate is
	// clamped into the deployable band.
	var lineLeads []image.Point
	if haveIntent && len(hm.deployLine.Points) >= 2 {
		lineLeads = hm.heroLineGrounds(intent)
		cands = append(cands, lineLeads...)
		if len(lineLeads) > 0 {
			source = "troop_line"
		}
	}
	if havePin {
		cands = append(cands, pinnedPt)
		if source == "deploy_line" {
			source = "pinned"
		}
	}

	// The ground the troop pass just used, snapped onto the axis those accepted
	// taps share. Only a SHARED AXIS is evidence: the interior of a diagonal line
	// is not proven ground just because the line was used. Live 2026-11-11
	// (TopLeft pin (461,130)->(198,305), diagonal across the village): the hero
	// path sampled fractions along that line, every hero was refused there, and
	// the sweep's authored point beside the line landed on its first tap. Those
	// fractions are gone for that reason; the axis snap stays, because a vertical
	// or horizontal troop line really is the axis the game just accepted.
	var axisCands []image.Point
	if axis, vertical, ok := groundAxis(hm.troopGround); ok {
		if !haveIntent {
			intent, _ = hm.resolveHeroTarget(slot)
			haveIntent = true
		}
		if haveIntent {
			axisCands = axisCandidates(intent, axis, vertical, hm.h, hm.w)
			cands = append(cands, axisCands...)
			if source == "deploy_line" {
				source = "troop_ground_axis"
			}
		}
	}
	if len(formulaPts) > 0 {
		cands = append(cands, formulaPts...)
		if source == "deploy_line" {
			source = "formula"
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

	// Nothing inside the red line is hero ground. With a measured outline the
	// authored point gets pushed clear instead of dropped, so the first tap stays
	// on the user's side; whatever cannot be pushed is discarded.
	if len(hm.zone.Polygon) >= 3 {
		cx, cy := hm.zone.Centre()
		step := tileStepPx(hm.h)
		free := make([]image.Point, 0, len(cands))
		for _, p := range cands {
			if !hm.zone.Inside(p.X, p.Y) {
				free = append(free, p)
				continue
			}
			if pushed, ok := pushPointOutOfZone(p, cx, cy, step, hm.zone); ok {
				free = append(free, clampToField(pushed, hm.w, hm.h))
			}
		}
		if len(free) > 0 {
			if moved := len(cands) - len(free); moved > 0 {
				hm.logger.Info().
					Str("unit", unitName).
					Int("dropped", moved).
					Msg("dropped hero ground that could not be pushed clear of the red line")
			}
			cands = dedupePoints(free)
		}
	}
	// Every tap must be inside the deployable band: an off-grid or HUD-covered
	// point is not a placement the game can accept, and the rescue budget must
	// never be spent discovering that. Clamping keeps the authored point usable
	// instead of silently dropping it.
	bottom := UICutoff(hm.h)
	clampBand := func(p image.Point) image.Point {
		// Y is the band the codebase calibrates (HUD above, troop bar below);
		// X bounds are the field edge and are enforced by the pickers.
		return ClampToDeployBand(p, bottom)
	}
	banded := make([]image.Point, 0, len(cands))
	for _, p := range cands {
		p = clampBand(p)
		// Clamping can move a point back inside a measured red zone. Push it
		// clear once more; ground that cannot be made legal is not a candidate.
		if len(hm.zone.Polygon) >= 3 && hm.zone.Inside(p.X, p.Y) {
			cx, cy := hm.zone.Centre()
			pushed, ok := pushPointOutOfZone(p, cx, cy, tileStepPx(hm.h), hm.zone)
			if !ok {
				continue
			}
			p = clampBand(pushed)
			if hm.zone.Inside(p.X, p.Y) {
				continue
			}
		}
		banded = append(banded, p)
	}
	cands = dedupePoints(banded)

	if len(cands) > maxHeroGroundCandidates {
		cands = cands[:maxHeroGroundCandidates]
	}

	// Retry ordering when this battle's red line is UNKNOWN. The no-deploy pocket
	// hugs the middle of the map, so the farther a retry sits from the screen
	// centre the more likely the game accepts it (live 2026-10-01, three attacks;
	// 2026-10-04/05 run1 + probe3). The FIRST candidate keeps its authored lead:
	// live 2026-11-12 the whole list was sorted outward, every hero's first tap
	// went to the farthest tile and was refused 4/4, while the next candidate —
	// the one nearer the authored ground — landed 4/4. With a measured outline
	// the zone-clearance order (heroGroundCandidates) is strictly better and is
	// left alone. The ceiling is applied BEFORE this sort so the authored point,
	// which was reserved a slot, cannot be pushed out of the list by it.
	if len(hm.zone.Polygon) < 3 && len(cands) > 2 {
		lead := cands[0]
		rest := cands[1:]
		orderOutwardFirst(rest, hm.w, hm.h)
		cands = append([]image.Point{lead}, rest...)
	}

	if len(cands) == 0 {
		// Nothing this battle proved deployable: the caller reports the hero
		// failed instead of firing at a guess and calling the rescue a success.
		return nil
	}
	// ground_source must describe what actually LEADS the list: the siege and
	// red-line filters can both move the authored point.
	switch {
	case containsPoint(lineLeads, cands[0]):
		source = "troop_line"
	case containsPoint(axisCands, cands[0]):
		source = "troop_ground_axis"
	case havePin && cands[0] == pinnedPt:
		source = "pinned"
	case containsPoint(formulaPts, cands[0]):
		source = "formula"
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
				scaled := ScaleEdge(e, hm.pCfg.Width, hm.pCfg.Height, hm.w, hm.h)
				p1 := image.Pt(
					int(float64(scaled.P1.X)+float64(cx-scaled.P1.X)*pct),
					int(float64(scaled.P1.Y)+float64(cy-scaled.P1.Y)*pct),
				)
				p2 := image.Pt(
					int(float64(scaled.P2.X)+float64(cx-scaled.P2.X)*pct),
					int(float64(scaled.P2.Y)+float64(cy-scaled.P2.Y)*pct),
				)
				cfg.Edges[k] = ManualEdge{P1: p1, P2: p2}
			}
			cfg.Width, cfg.Height = hm.w, hm.h
		}
		count := 15
		if detectedCount > 0 {
			count = (detectedCount + 3) / 4
			if count < 4 {
				count = 4
			}
		}
		hm.executor.TapDeployFourSides(cfg, hm.targetEdge, count, 8)

		time.Sleep(150 * time.Millisecond)
		live, trusted, visualEmpty := hm.liveCountAndEmpty(slot)
		if visualEmpty && (!trusted || live <= 0) {
			hm.slotManager.MarkDeployed(unitName)
			return true
		}

		// Troops still left in slot: fire a top-up round across 4 sides
		if !visualEmpty {
			hm.logger.Warn().
				Str("unit", unit.Name).
				Int("live_count", live).
				Bool("trusted", trusted).
				Msg("FourSides deploy left troops in slot; firing top-up pass")
			hm.executor.TapSlot(slot, 8)
			hm.executor.HumanSleep(120, 25)
			topUpCount := 6
			if trusted && live > 0 {
				topUpCount = (live + 3) / 4
				if topUpCount < 4 {
					topUpCount = 4
				}
			}
			hm.executor.TapDeployFourSides(cfg, hm.targetEdge, topUpCount, 8)

			time.Sleep(150 * time.Millisecond)
			live2, trusted2, visualEmpty2 := hm.liveCountAndEmpty(slot)
			if visualEmpty2 && (!trusted2 || live2 <= 0) {
				hm.slotManager.MarkDeployed(unitName)
				return true
			}
		}

		// Not empty: do not mark deployed so Sweeper finishes remaining troops
		hm.slotManager.RecordAttempt(unitName, false)
		return false
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
	if !hasFormula && hm.hasTroopLine && (strings.Contains(unitName, "dragon") || strings.Contains(unitName, "edrag")) {
		p1 = hm.lastTroopLineP1
		p2 = hm.lastTroopLineP2
		hasFormula = true
		src = "previous_troop_line"
	}
	if src == "edge" {
		p1, p2 = hm.resolveTroopTarget(slot, offset)
	}

	if p1 != p2 {
		hm.lastTroopLineP1 = p1
		hm.lastTroopLineP2 = p2
		hm.hasTroopLine = true
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
	if strings.EqualFold(unit.Amount, "All") && p1 != p2 && !tapCountMeasured {
		// The floor exists for a pass with NO measurement: spreading an unknown
		// number of units along the whole line is safer than firing the blind
		// batch in one spot. When the card was measured, the measurement is the
		// card's contents and the floor is a fabrication: live 2026-11-15 an
		// 8-Balloon card was measured at 8 and the Amount:"All" floor turned it
		// into 22 taps, costing ~14 dead taps per troop card and pushing the
		// deploy window to 20 s of a 7 s goal.
		minTaps := 22
		if strings.Contains(unitName, "dragon") {
			minTaps = 16
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
	// progressBase is the last count read that is expected to DROP as the pass
	// lands units. -1 means nothing was measured, so a refused ground cannot be
	// proven and no nudge is attempted.
	progressBase := -1
	if preTrusted && preCount > 0 {
		progressBase = preCount
	} else if detectedTrusted && detectedCount > 0 {
		progressBase = detectedCount
	}
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
				// Hand the decision to the sweep, which re-checks on a FRESH frame.
				// This used to mark a saturated Amount:"All" pass deployed so the
				// sweep would not circle back; that is exactly how troops are left
				// on the bar with nothing in the log saying so. A card nobody could
				// read is not a card anybody proved empty.
				hm.logger.Warn().
					Str("unit", unit.Name).
					Int("reads", unreadableReads).
					Int("fired_total", tapCount).
					Msg("reconcile: count label still unreadable after a measured main pass; handing the slot to the sweep instead of claiming it deployed")
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
		//
		// Before re-firing, VERIFY THE GROUND. A count that has not moved at all
		// after a pass is not a slow card, it is the game refusing those tiles
		// ("You cannot deploy troops on the Red area!"), and re-tapping the same
		// line is what filled the deploy window with refused taps. Step the line
		// slightly back from the base and spend this round on the new ground.
		if trusted && progressBase > 0 && live >= progressBase {
			// Nothing left the card on the pass that just ran. If that pass was a
			// drag, the gesture was refused (or the card was already deselected),
			// and repeating it would only deselect again: hand the rest of the
			// battle to the tap path, which re-selects the slot first.
			hm.executor.DisableDrag()
			// One confirmation read first: the card's label lags its own animation,
			// and moving a line because a frame was mid-animation would send the
			// rest of the army to ground nobody proved was refused.
			hm.executor.HumanSleep(300, 40)
			if again, ok, _ := hm.liveCountAndEmpty(slot); ok && again >= progressBase {
				live = again
				if np1, np2, moved := nudgeDeployLineOutward(p1, p2, hm.w, hm.h, 0); moved {
					hm.logger.Warn().
						Str("unit", unit.Name).
						Int("round", round+1).
						Int("count_before", progressBase).
						Int("count_after", live).
						Interface("was_p1", p1).
						Interface("was_p2", p2).
						Interface("p1", np1).
						Interface("p2", np2).
						Msg("deploy line landed nothing, so the game refused that ground; moving the line slightly back from the base and retrying there")
					p1, p2 = np1, np2
					if p1 != p2 {
						hm.lastTroopLineP1, hm.lastTroopLineP2 = p1, p2
						hm.hasTroopLine = true
					}
					hm.recordGround(p1, p2)
				}
			}
		}
		if trusted {
			progressBase = live
		}
		fire := live
		if !trusted || fire <= 0 {
			fire = blindBatchTaps
		} else {
			fire += 2
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
// siegeVerdict is what one settled post-tap read of a siege card proves.
//
// Only siegeStillOnCard is evidence the machine was NOT placed: the count
// label still shows it. Anything unreadable is siegeUnknown and is treated as
// placed, because re-tapping a machine that did drop destroys it.
type siegeVerdict int

const (
	siegeCommitted   siegeVerdict = iota // the machine left the card: never tap again
	siegeUnknown                         // no usable reading: assume down
	siegeStillOnCard                     // the label still shows the machine: the tap was refused
)

// siegeDropVerdict classifies a settled read. Kept pure so the rule can be
// pinned by a test instead of by a live battle.
func siegeDropVerdict(trusted bool, count int, ratioOK bool, ratio float64) siegeVerdict {
	switch {
	case trusted && count == 0:
		return siegeCommitted
	case ratioOK && ratio <= heroConsumedRatio:
		// A spent card keeps a dimmed silhouette.
		return siegeCommitted
	case trusted && count > 0:
		return siegeStillOnCard
	default:
		return siegeUnknown
	}
}

// siegeUnchangedBand is how much a siege card's activity ratio may move and
// still count as "the card look did not change". A deployed machine leaves a
// dimmed silhouette (ratio <= heroConsumedRatio); a refused tap leaves the card
// as it was.
const siegeUnchangedBand = 0.05

// siegeVerdictAfterReads folds the card's own before/after appearance into the
// verdict.
//
// The count badge cannot answer this question here: this client ships no siege
// template, so the machine's card is an unnamed fallback slot and "no count
// label" reads exactly like "count drained" — live 2026-11-15 the run logged
// "reconcile: siege count label gone; slot drained" in every battle while the
// armed machine stayed on the bar, and the deploy path marked it placed and
// skipped it for the rest of the attack. A card that looks the same after the
// placement tap as before it did not deploy.
func siegeVerdictAfterReads(v siegeVerdict, preRatio float64, preOK bool, postRatio float64, postOK bool) siegeVerdict {
	if !preOK || !postOK {
		return v
	}
	if postRatio >= preRatio-siegeUnchangedBand {
		return siegeStillOnCard
	}
	return v
}

// siegeCardRatio takes the one settled read of the card used as the "before"
// half of a placement check.
func (hm *HeroManager) siegeCardRatio(slot *TrackedSlot) (float64, bool) {
	if slot == nil {
		return 0, false
	}
	return hm.executor.CaptureSettledSlotRatio(hm.w, slot)
}

func (hm *HeroManager) confirmSiegeDrop(unit strategy.Unit, slot *TrackedSlot, preRatio float64, preOK bool) siegeVerdict {
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
	verdict := siegeVerdictAfterReads(siegeDropVerdict(trusted, count, ratioOK, ratio), preRatio, preOK, ratio, ratioOK)
	// Armed beats every inference: the card is still SELECTED for placement, so
	// the machine is still on it and the tap was refused.
	if armed, ok := hm.executor.SlotArmed(slot); ok && armed {
		verdict = siegeStillOnCard
		hm.logger.Warn().Str("unit", unit.Name).
			Msg("siege card is still ARMED after the placement tap: the game refused that ground and the machine never left the card")
	}
	if verdict == siegeStillOnCard && ratioOK && preOK && ratio >= preRatio-siegeUnchangedBand {
		hm.logger.Warn().Str("unit", unit.Name).
			Float64("pre_ratio", preRatio).
			Float64("post_ratio", ratio).
			Msg("siege card looks unchanged after the placement tap: nothing deployed, whatever the count badge says")
	}
	switch {
	// The verdict comes first on purpose: the count cases below describe what the
	// badge said, and the badge said "drained" in every battle of 2026-11-15
	// while the armed machine stayed on the bar. Logging the badge as an outcome
	// is what hid the failure.
	case verdict == siegeStillOnCard:
	case trusted && count == 0:
		hm.logger.Info().Str("unit", unit.Name).Msg("reconcile: siege count label gone; slot drained")
	case ratioOK && ratio <= heroConsumedRatio:
		hm.logger.Info().Str("unit", unit.Name).
			Float64("post_ratio", ratio).
			Msg("reconcile: siege slot dimmed after deploy tap")
	case verdict == siegeStillOnCard:
		hm.logger.Warn().Str("unit", unit.Name).
			Float64("post_ratio", ratio).
			Int("count", count).
			Bool("count_read_trusted", trusted).
			Msg("siege card still carries the machine after the deploy tap; the game refused that ground (nothing was placed, so one retry elsewhere cannot hit a deployed machine)")
	case ratioOK:
		hm.logger.Warn().Str("unit", unit.Name).
			Float64("post_ratio", ratio).
			Int("count", count).
			Bool("count_read_trusted", trusted).
			Msg("siege card still bright after deploy tap; the machine may not have dropped (no re-tap — a second tap would hit it)")
	default:
		hm.logger.Warn().Str("unit", unit.Name).Msg("siege settle check could not capture the screen; deployment unconfirmed")
	}
	return verdict
}

// siegeFallbackPoint is the ground a refused siege tap retries on: the middle
// of the battle's deploy line (troops were accepted there seconds ago), or the
// strategy edge's midpoint when no line exists.
func (hm *HeroManager) siegeFallbackPoint() (image.Point, bool) {
	if len(hm.deployLine.Points) >= 2 {
		p1, p2 := hm.deployLine.Points[0], hm.deployLine.Points[len(hm.deployLine.Points)-1]
		return hm.executor.addJitter(image.Pt((p1.X+p2.X)/2, (p1.Y+p2.Y)/2), 10), true
	}
	if edge, ok := hm.pCfg.Edges[hm.targetEdge]; ok {
		s := ScaleEdge(edge, hm.pCfg.Width, hm.pCfg.Height, hm.w, hm.h)
		return hm.executor.addJitter(image.Pt((s.P1.X+s.P2.X)/2, (s.P1.Y+s.P2.Y)/2), 10), true
	}
	return image.Point{}, false
}

// retryRefusedSiege fires the ONE retry a proven refusal earns. The caller
// passes the verdict from the first tap: only siegeStillOnCard is a proven
// refusal, and it means nothing was placed, so the one-tap contract
// (slot.SiegePlaced, set by PlaceSiege) has to be released first.
//
// Without this the machine was silently retired: DeploySiege marked the slot
// deployed regardless, and the sweep refuses siege slots by design, so a
// refused tap meant the machine never entered the battle at all — the user's
// "sometimes forgets the siege machine".
// siegeRefusedGrounds orders the ground a proven siege refusal may be re-aimed
// at: the deploy-line midpoint first (troops were accepted there seconds ago),
// then points stepped OUTWARD from the refused spot, away from the battlefield
// centre. A refusal is a region, not a pixel: live 2026-11-15 the retry point
// sat 12 px from the refused pin, inside the same red pocket, and was refused
// too. Stepping outward walks out of that pocket toward ground the game accepts.
func (hm *HeroManager) siegeRefusedGrounds(refused image.Point) []image.Point {
	out := make([]image.Point, 0, 10)
	add := func(p image.Point) {
		// The deployable band is not the whole frame. Live 2026-11-16 run 11:
		// the outward walk from a TopLeft pin produced y=94 — behind the top
		// HUD — and the game could not have accepted it. That wasted one of
		// three taps on ground the bot could never use.
		if p.X < 8 || p.X >= hm.w-8 || p.Y < BandTop() || p.Y > int(float64(hm.h)*0.85) {
			return
		}
		// When the battle has a detected red line, never spend a retry tap on
		// ground the detector already knows is inside it.
		if hm.zone.Valid && hm.zone.Inside(p.X, p.Y) {
			return
		}
		for _, q := range out {
			if math.Hypot(float64(p.X-q.X), float64(p.Y-q.Y)) < 18 {
				return
			}
		}
		out = append(out, p)
	}

	// OUTWARD FIRST, far enough to leave the refused pocket. Live 2026-11-17
	// (1024x768, real raid): the pinned hero point (771,528) sat inside the
	// red band, the first three retries stepped 30-45 px and stayed inside it,
	// and the fourth tap at a point ~120 px further out was the one the game
	// accepted. The old order led with deploy-line points near the pin, which
	// are the same ground, and the three-tap ceiling never reached the walk.
	cx, cy := float64(hm.w)/2, float64(hm.h)/2
	dx, dy := float64(refused.X)-cx, float64(refused.Y)-cy
	if d := math.Hypot(dx, dy); d >= 1 {
		ux, uy := dx/d, dy/d
		for _, step := range []float64{120, 190, 260} {
			add(image.Pt(refused.X+int(math.Round(ux*step)), refused.Y+int(math.Round(uy*step))))
		}
	}

	// Then the deploy line: troops were accepted somewhere on it seconds ago.
	if len(hm.deployLine.Points) >= 2 {
		p1, p2 := hm.deployLine.Points[0], hm.deployLine.Points[len(hm.deployLine.Points)-1]
		if near, d, ok := nearestDeployLinePoint(refused, hm.deployLine); ok {
			if d >= 30 {
				add(near)
			}
			dx, dy := float64(p2.X-p1.X), float64(p2.Y-p1.Y)
			if l := math.Hypot(dx, dy); l > 0 {
				ux, uy := dx/l, dy/l
				for _, step := range []float64{40, 85} {
					add(image.Pt(near.X+int(math.Round(ux*step)), near.Y+int(math.Round(uy*step))))
					add(image.Pt(near.X-int(math.Round(ux*step)), near.Y-int(math.Round(uy*step))))
				}
			}
		}
	}

	// Narrower outward steps stay as late candidates: on a base whose red band
	// is thin, a small step is enough and keeps the machine closer to the
	// heroes it is screening.
	if d := math.Hypot(dx, dy); d >= 1 {
		ux, uy := dx/d, dy/d
		for _, step := range []float64{45, 85} {
			add(image.Pt(refused.X+int(math.Round(ux*step)), refused.Y+int(math.Round(uy*step))))
		}
	}

	// The troop pass's own line, as a fallback: its ends are the closest thing
	// to proven ground when the outward walk is blocked by the frame edge.
	if len(hm.deployLine.Points) >= 2 {
		p1, p2 := hm.deployLine.Points[0], hm.deployLine.Points[len(hm.deployLine.Points)-1]
		add(p1)
		add(p2)
		add(image.Pt((3*p1.X+p2.X)/4, (3*p1.Y+p2.Y)/4))
		add(image.Pt((p1.X+3*p2.X)/4, (p1.Y+3*p2.Y)/4))
	}
	return out
}

// retryRefusedSiege re-aims a PROVEN siege refusal, then stops. The caller passes
// the verdict from the first tap: only siegeStillOnCard means nothing was
// placed, so the one-tap contract (slot.SiegePlaced, set by PlaceSiege) has to
// be released before each attempt.
//
// Without this the machine was silently retired: DeploySiege marked the slot
// deployed regardless, and the sweep refuses siege slots by design, so a refused
// tap meant the machine never entered the battle at all — the user's "not
// placing siege machine". Each attempt is verified the same way as the first
// (armed card = refused), and the loop stops on the first accepted ground, so a
// second tap can never land on a machine that did deploy.
func (hm *HeroManager) retryRefusedSiege(unit strategy.Unit, slot *TrackedSlot, verdict siegeVerdict) bool {
	if verdict != siegeStillOnCard {
		return verdict == siegeCommitted
	}
	refused, ok := hm.executor.SiegeGround()
	if !ok {
		hm.logger.Warn().Str("unit", unit.Name).Msg("siege refused its ground and no refused point was recorded; the machine stays on the card")
		return false
	}
	grounds := hm.siegeRefusedGrounds(refused)
	// Four taps is the ceiling, and the list now leads with a 120 px outward
	// walk (see siegeRefusedGrounds), so the ceiling cannot drop the ground
	// that actually leaves the refused pocket.
	if len(grounds) > 4 {
		grounds = grounds[:4]
	}
	if len(grounds) == 0 {
		hm.logger.Warn().Str("unit", unit.Name).Msg("siege refused its ground and no fallback exists; the machine stays on the card")
		return false
	}
	for i, alt := range grounds {
		// A refused tap can leave the card armed or deselect it; the retry's
		// field tap only places the machine on a selected card. Re-tapping costs
		// nothing: nothing was placed on the refused try.
		if armed, aok := hm.executor.SlotArmed(slot); aok && !armed {
			hm.executor.TapSlot(slot, heroCardTapJitter)
			hm.executor.client.HumanSleep(heroArmSettleMs, 30)
		}
		if slot != nil {
			slot.SiegePlaced = false
		}
		preRatio, preOK := hm.siegeCardRatio(slot)
		hm.logger.Warn().
			Str("unit", unit.Name).
			Int("attempt", i+1).
			Interface("retry_at", alt).
			Msg("siege: retrying a refused placement (the card still holds the machine, so nothing was placed)")
		hm.executor.RetrySiege(slot, alt, 12.0)
		if hm.confirmSiegeDrop(unit, slot, preRatio, preOK) != siegeStillOnCard {
			return true
		}
		// Under CLASHGO_HERO_DEBUG, keep the frame each refused attempt left
		// behind: whether the card is armed and whether the machine stands on the
		// ground are the two facts this loop is guessing about.
		if heroDebugDir() != "" {
			hm.debugDump(fmt.Sprintf("siege_refused_%d", i+1))
		}
		if slot != nil {
			slot.SiegePlaced = false
		}
	}
	// Every retry was refused too: nothing was placed.
	return false
}

// markSiegeFailed records a machine the game proved it never accepted. The slot
// must not be marked deployed: that claimed success retired the machine for the
// whole battle (the sweep skips siege slots by design) and hid the failure from
// the verifier. The clan-castle alias is failed with it — the two share the
// fallback slot.
func (hm *HeroManager) markSiegeFailed(unit strategy.Unit, slot *TrackedSlot) {
	if slot != nil {
		slot.SiegePlaced = false
		slot.State = SlotFailed
		// The fallback slot is linked without a unit name, which made the
		// verifier log `unit=` and the report unable to name or fail the machine
		// (live run 12: JSON said undeployed=1, the report said success).
		if slot.UnitName == "" {
			slot.UnitName = unit.Name
		}
		if hm.slotManager != nil && slot.UnitName != "" {
			hm.slotManager.MarkFailed(slot.UnitName)
		}
	}
	if hm.slotManager != nil {
		hm.slotManager.MarkFailed(unit.Name)
		hm.slotManager.MarkFailed("clan castle")
	}
}

func (hm *HeroManager) DeploySiege(unit strategy.Unit, slot *TrackedSlot) bool {
	markDone := func() {
		if slot != nil {
			slot.SiegePlaced = true
			slot.IsEmpty = true
			slot.State = SlotDeployed
			if slot.UnitName != "" {
				hm.slotManager.MarkDeployed(slot.UnitName)
			}
		}
		hm.slotManager.MarkDeployed(unit.Name)
		hm.slotManager.MarkDeployed("clan castle")
	}

	// A siege card carries NO count badge (a machine is one unit, and the bar
	// only prints xN for N>1), so the troop-counter's "0" here is a missing
	// label, not an empty card. Live 2026-11-17: the battle-start bar showed the
	// level-6 Stone Slammer art with no badge, the counter read 0, and the game
	// answered a retry tap on the already-placed machine with "Your Stone
	// Slammer is empty!". Do not let that 0 skip the one placement tap.

	// The card read BEFORE the placement tap, so the confirmation has something
	// to compare against; see siegeVerdictAfterReads.
	preRatio, preOK := hm.siegeCardRatio(slot)

	// The user pinned a hero point for this side and asked for the siege to land
	// on the same spot: the machine screens the heroes, so it belongs on their
	// ground rather than on the strategy's "Center" — the middle of the base,
	// where the game refuses the tap. It outranks the formula for the same
	// reason the pinned deploy line does; see SetPinnedGround.
	if pt, ok := hm.pinnedHeroPoint(); ok {
		tap := hm.executor.addJitter(pt, 4)
		// Troops were tapped and accepted on their line seconds ago; that ground
		// is the only proof of a legal deploy this battle. Prefer it over a pin
		// the game may answer with "cannot deploy on the red area" (the live
		// refusal that cost three retries and ~5 s on 2026-11-17).
		if hm.hasTroopLine {
			mid := image.Pt((hm.lastTroopLineP1.X+hm.lastTroopLineP2.X)/2, (hm.lastTroopLineP1.Y+hm.lastTroopLineP2.Y)/2)
			hm.logger.Info().
				Str("unit", unit.Name).
				Interface("pinned", tap).
				Interface("troop_midpoint", mid).
				Msg("siege placed on the troop line's proven ground (troops were accepted there this battle)")
			tap = hm.executor.addJitter(mid, 4)
		}
		hm.logger.Info().
			Str("unit", unit.Name).
			Interface("pinned", pt).
			Interface("tap", tap).
			Msg("siege placement tap fired (one tap per battle)")
		hm.executor.PlaceSiege(slot, tap, 12.0)
		// ONE tap, no confirmation retries. The armed-card check cannot tell a
		// placed machine from a refused one on this build (the card keeps its
		// cyan highlight either way, and the count badge is absent for siege
		// cards), so a "still armed" verdict is not evidence — and every retry
		// it green-lit was a tap that could land on the machine already
		// standing there and destroy it. Live 2026-11-17: retries fired on an
		// empty card after a successful drop and toasted "Your Stone Slammer is
		// empty!". Ground comes from the troop line, which the game accepted
		// seconds ago, so the single tap has proof behind it.
		time.Sleep(250 * time.Millisecond)
		markDone()
		return true
	}

	if entry, ok := hm.formulaEntry(unit.Name); ok {
		if hm.deploySiegeFromFormula(unit, slot, entry) {
			verdict := hm.confirmSiegeDrop(unit, slot, preRatio, preOK)
			if hm.retryRefusedSiege(unit, slot, verdict) {
				markDone()
				return true
			}
			hm.logger.Warn().Str("unit", unit.Name).
				Msg("siege machine never left its card this battle; the card is still armed, so this attack runs without it")
			hm.executor.ForgetSiegeGround()
			hm.markSiegeFailed(unit, slot)
			return false
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
	markDone()
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
		if hm.hasTroopLine {
			// Center of verified troop line is guaranteed safe deployable ground (never red area)
			mid := image.Pt((hm.lastTroopLineP1.X+hm.lastTroopLineP2.X)/2, (hm.lastTroopLineP1.Y+hm.lastTroopLineP2.Y)/2)
			hm.logger.Info().
				Interface("formula_point", p).
				Interface("troop_midpoint", mid).
				Msg("aligning siege point with verified troop line center to guarantee legal placement")
			p = mid
		}
		hm.logger.Info().Str("unit", unit.Name).Interface("p", p).
			Msg("formula-driven siege placement (single tap)")
		j := hm.executor.addJitter(p, jitter)
		hm.executor.PlaceSiege(slot, j, 12.0)
		return true
	case entry.IsLine() && entry.P1 != nil && entry.P2 != nil:
		p1 := entry.P1.Image()
		p2 := entry.P2.Image()
		if hm.hasTroopLine {
			p1 = hm.lastTroopLineP1
			p2 = hm.lastTroopLineP2
		}
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
	if hm.liveHook != nil {
		return hm.liveHook()
	}
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
//
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
