package attack

import (
	"image"
	"math"
	"math/rand"
	"strings"
	"sync"
	"time"

	"github.com/Ducky705/ClashGO/internal/adb"
	"github.com/Ducky705/ClashGO/internal/game"
	"github.com/rs/zerolog"
	"gocv.io/x/gocv"
)

// DeployBudget bounds the entire deploy phase (strategy phases + spell /
// hero reconcile + sweep + verify) from the moment DeployDynamicV2 starts.
// A CoC battle lasts 3 minutes from the first drop; every reconcile/sweep
// loop below consults DeployBudgetExhausted so a slot whose "empty" check
// keeps failing (observed live: a spent earthquake/spell card stuck in its
// cooldown read as visually non-empty forever) can never keep firing taps
// past the battle timer — the stuck-sweep symptom that burned minutes and
// hundreds of taps after the battle was already over. The budget is ~2x the
// longest legitimately-observed full deployment (~100s) so real attacks are
// never cut short.
const DeployBudget = 170 * time.Second

// DeployGoal is the wall-clock target for the placement phases themselves
// (troops, siege, heroes, spells — from the first phase's first tap to the last
// spell cast), as opposed to DeployBudget, which is the hard stop that keeps a
// stuck slot from tapping past the battle timer. It exists so the run states its
// own number: every phase logs its elapsed time against this goal, and the
// window is reported whether or not it is met, because the only way to move a
// wall clock is to measure it.
const DeployGoal = 7 * time.Second

// siegeClearanceRef is the radius, in reference px, of the no-tap zone around a
// siege machine this battle already placed. Reference geometry is 860x732 across
// a field a bit over 40 tiles wide, so ~1 tile is ~20 ref px and 44 ref px is a
// little over two tiles: far enough that no later placement tap can land on the
// machine, close enough that the heroes still drop beside it and tank for it.
const siegeClearanceRef = 44

// TapExecutor handles all tap operations, screen capture, and timing.
type TapExecutor struct {
	client      *adb.Client
	cal         *game.Calibration
	logger      zerolog.Logger
	lineForward bool

	// siegeGround is the screen point of the siege machine this battle has
	// already placed, recorded by PlaceSiege. Every later UNIT placement tap is
	// moved clear of it (see avoidSiegeGround): the machine stands on the ground
	// the heroes are told to use, and tapping that tile again destroys it. This
	// is the state behind the "it taps the siege machine twice" report — live
	// 2026-09-27 the machine landed on the pinned hero point (954,183) and all
	// three heroes then fired their field taps at that exact tile, two of them
	// refused by the game and retried 50 px away.
	siegeGround    image.Point
	siegeGroundSet bool

	// siegeNudges counts how many placement taps this battle had to move off
	// the machine's tile, so a run can report the protection working.
	siegeNudges int

	// siegePlacements is how many siege machines this battle has placed. It
	// must be 0 or 1 for a normal run; the deploy summary reports it so a
	// second placement can never hide in a 40 MB log.
	siegePlacements int

	// deployDeadline is when this battle's deploy budget runs out. Zero
	// when no deploy is in progress (or the budget was never started).
	// Set by StartDeployBudget on the attack goroutine; read by the same
	// goroutine's reconcile loops, so no locking is needed.
	deployDeadline time.Time

	// slotResolver maps a lower-cased unit name to its TrackedSlot.
	// The orchestrator wires it to SlotManager.GetSlot after the slot
	// manager is built; deployers use it to pair a unit with its OCR'd
	// count. nil is legal (lookup just returns no slot).
	slotResolver func(unitName string) *TrackedSlot

	// lastNonHeroSlot lets deployment-time HP-triggered abilities restore the
	// current troop selection before the next field tap. inputErr is the sticky
	// first definite tap transport failure for this battle. Both are shared with
	// the deployment watcher goroutine (which fires HP abilities), hence the
	// mutex.
	inputMu         sync.Mutex
	lastNonHeroSlot *TrackedSlot
	inputErr        error
}

// SetSlotResolver wires the unit-name -> slot lookup used by deployers
// that need to correlate a unit with its slot (e.g. live OCR counts).
func (t *TapExecutor) SetSlotResolver(fn func(unitName string) *TrackedSlot) {
	t.slotResolver = fn
}

// NewTapExecutor creates a new tap executor.
func NewTapExecutor(client *adb.Client, cal *game.Calibration, logger zerolog.Logger) *TapExecutor {
	return &TapExecutor{
		client: client,
		cal:    cal,
		logger: logger.With().Str("component", "tap_executor").Logger(),
	}
}

// CaptureFresh captures a fresh screen from the device.
func (t *TapExecutor) CaptureFresh() (gocv.Mat, error) {
	return t.client.CaptureToMat()
}

// StartDeployBudget arms the deploy deadline at DeployBudget from now.
// Call once per battle before any phase runs (DeployDynamicV2).
func (t *TapExecutor) StartDeployBudget() {
	t.deployDeadline = time.Now().Add(DeployBudget)
	// The siege ledger is per-battle: last battle's machine is not on this
	// field, and a stale point would quietly bend this battle's hero drops.
	t.siegeGround, t.siegeGroundSet, t.siegeNudges, t.siegePlacements = image.Point{}, false, 0, 0
	// Fresh battle, fresh transport verdict and selection state.
	t.inputMu.Lock()
	t.inputErr, t.lastNonHeroSlot = nil, nil
	t.inputMu.Unlock()
}

// noteInput records a definite tap transport failure once per battle. It is
// deliberately sticky: when a synchronous tap reports an error the deploy
// cannot know whether a queued copy landed, so retrying means either blind
// duplicate taps or silently skipping the unit — the orchestrator stops at
// the next checkpoint instead.
func (t *TapExecutor) noteInput(err error, what string) {
	if err == nil {
		return
	}
	t.inputMu.Lock()
	first := t.inputErr == nil
	if first {
		t.inputErr = err
	}
	t.inputMu.Unlock()
	if first {
		t.logger.Error().Err(err).Str("tap", what).
			Msg("definite tap transport failure; deploy stops at the next checkpoint (delivery uncertain, no blind retry)")
	}
}

// InputErr returns the first definite tap transport failure observed this
// battle, or nil.
func (t *TapExecutor) InputErr() error {
	t.inputMu.Lock()
	defer t.inputMu.Unlock()
	return t.inputErr
}

// TapField fires one raw field tap through the recording choke point, so
// deployers that call the client directly (spell casts) feed the same
// transport verdict as every other path.
func (t *TapExecutor) TapField(x, y int, stdDev float64) error {
	err := t.client.TapFast(x, y, stdDev)
	t.noteInput(err, "field tap")
	return err
}

// RestoreSelection re-selects the troop/spell card the deploy most recently
// chose. Used after a hero-ability tap so the ability's card tap cannot leave
// a hero selected while the next phase aims field taps at troop ground.
func (t *TapExecutor) RestoreSelection() bool {
	t.inputMu.Lock()
	slot := t.lastNonHeroSlot
	t.inputMu.Unlock()
	if slot == nil {
		return false
	}
	return t.TapFast_ok(slot.X, slot.Y)
}

func (t *TapExecutor) TapFast_ok(x, y int) bool {
	err := t.client.TapFast(x, y, 2.0)
	t.noteInput(err, "selection restore")
	return err == nil
}

// StartDeployBudgetAt arms the deploy deadline at an explicit instant
// (used by tests to simulate an already-expired budget).
func (t *TapExecutor) StartDeployBudgetAt(deadline time.Time) {
	t.deployDeadline = deadline
}

// DeployBudgetExhausted reports whether the deploy phase may keep firing
// taps. It is the single abort check every reconcile/sweep/verify loop
// consults between rounds: once true, remaining units are reported
// undeployed and the bot moves on to the battle-end wait instead of
// tapping into (or past) the battle timer.
func (t *TapExecutor) DeployBudgetExhausted() bool {
	return !t.deployDeadline.IsZero() && time.Now().After(t.deployDeadline)
}

// DeployBudgetRemaining returns the time left in the deploy budget (0
// when exhausted or never armed).
func (t *TapExecutor) DeployBudgetRemaining() time.Duration {
	if t.deployDeadline.IsZero() {
		return 0
	}
	rem := time.Until(t.deployDeadline)
	if rem < 0 {
		return 0
	}
	return rem
}

// maxHeroSpotTaps is the hard ceiling on how many times the bot may tap a
// hero's CARD per battle to PLACE it: the initial drop, and at most one rescue.
// Never more. A tapped card is the only way to drop a hero, so an uncapped
// retry/sweep/verify pass is exactly the "it keeps tapping the Archer Queen"
// symptom.
//
// The ability is no longer part of this budget. One shared counter meant a
// hero whose drop the sweep had to rescue had spent both taps before the
// ability pass ran, so the pass skipped the activation — a deployed hero, with
// its ability unused. Live 2026-10-01 (TopLeft): exactly that happened to the
// Grand Warden. The ability now carries its own one-shot flag
// (TrackedSlot.AbilityFired), which still permits exactly one activation per
// hero per battle.
const maxHeroSpotTaps = 2

// The single hero placement sequence, shared by the hero phase and the sweep.
// They had drifted into two near-identical copies (card-tap jitter 8 vs 4,
// post-drop settle 350ms vs 300ms); the sweep's copy placed both heroes on its
// first candidate in every battle while the hero phase's copy failed on every
// ground it was given, so one of them was demonstrably wrong and nothing said
// which. Both callers now use these.
const (
	heroCardTapJitter = 4
	// heroArmSettleMs is the pause between a hero card tap and the field tap
	// that spends it. Measured live 2026-10-04 (tmp/audit4, four attacks + a
	// heroes-only probe): a card's FIRST selection in a battle needs longer
	// than 155ms to arm — every batch wave drop at card→field 155-205ms was
	// refused (12/12), while the same fireHeroDrop at the same field points
	// landed every time when it followed a RE-arm tap (12/14), and the
	// hero phase's identical arm+drop has failed while the sweep's landed
	// since 2026-09-27 for the same reason (the sweep re-selects a card the
	// hero phase already touched). 400ms covers the first-selection arm; the
	// wave costs 250ms per hero more than before and drops the rescue path
	// entirely when it works.
	heroArmSettleMs   = 400
	heroDropSettleMs  = 300
)

// maxHeroRearms is how many REPLACEMENT arm taps a hero may take when the game
// did not register the previous placement attempt.
//
// A refused placement DESELECTS the card. Measured live 2026-09-27 (BottomLeft,
// recorder frames): the card arms at ~48/255 brightness, the refused field tap
// returns it to ~86, and the sweep's identical arm+drop 8 s later arms it again
// (94) and places the hero. The comment this replaces claimed the game keeps the
// card selected after a refusal, so the retry ladder fired its field taps at an
// UNSELECTED card — which is why all eight hero drops on the four pinned sides
// failed at every ground they were given, including points that bracketed the
// sweep's successful one.
//
// A re-arm is not a second placement tap: the game never received the first one
// as a card selection. It therefore does not consume the slot's placement
// budget, which is what keeps the 2-tap contract (one placement, one ability,
// never spam) intact. The physical ceiling per hero is
// maxHeroSpotTaps + maxHeroRearms.
const maxHeroRearms = 1

// isHeroCardSlot reports whether a slot is a real hero card the cap applies
// to. Fallback-labeled "heroes" are bonus troops wearing a stale manual
// label and are tapped like troops.
func isHeroCardSlot(slot *TrackedSlot) bool {
	return slot != nil && slot.Category == "Hero" && !slot.FallbackLabeled
}

// consumeHeroSpotTap returns true when the hero slot still has a PLACEMENT card
// tap left and burns one. Non-hero slots are always allowed and never counted.
// The ability tap does not come through here — see TapHeroAbility.
func (t *TapExecutor) consumeHeroSpotTap(slot *TrackedSlot, what string) bool {
	if !isHeroCardSlot(slot) {
		return true
	}
	// A CONFIRMED deployment ends the placement budget. The second placement
	// tap exists for one purpose only: rescuing a drop the game refused, which
	// leaves the slot short of SlotDeployed. A hero already standing on the
	// field must never be re-placed — that is a second army unit the user did
	// not ask for.
	if slot.State == SlotDeployed && slot.SpotTaps >= 1 {
		t.logger.Warn().
			Str("unit", slot.UnitName).
			Int("x", slot.X).
			Str("attempted", what).
			Msg("hero is already deployed; refusing a second placement tap")
		return false
	}
	if slot.SpotTaps >= maxHeroSpotTaps {
		t.logger.Warn().
			Str("unit", slot.UnitName).
			Int("x", slot.X).
			Int("spot_taps", slot.SpotTaps).
			Str("attempted", what).
			Msg("hero placement card tap budget exhausted (max 2: drop + one rescue); refusing to tap the hero again")
		return false
	}
	slot.SpotTaps++
	return true
}

// HeroSpotTapBudgetLeft reports whether the hero slot may still take a
// PLACEMENT card tap. Non-hero slots always report true. It mirrors
// consumeHeroSpotTap exactly, including the rule that a confirmed deployment
// has no placement taps left. A hero that is out of placement taps is still
// eligible for its ability — see AbilityFired.
func (t *TapExecutor) HeroSpotTapBudgetLeft(slot *TrackedSlot) bool {
	if !isHeroCardSlot(slot) {
		return true
	}
	if slot.State == SlotDeployed && slot.SpotTaps >= 1 {
		return false
	}
	return slot.SpotTaps < maxHeroSpotTaps
}

// TapSlot selects a slot with jitter for human-like behavior. It returns
// false (and fires nothing) when the slot is a hero card whose 2-tap budget
// is already spent, so callers can skip their follow-up field tap instead of
// firing at empty ground.
//
// A siege slot that has already placed its machine is refused for the same
// reason: re-selecting its card is the first half of a re-deploy, and the field
// tap that would follow destroys the machine (see TrackedSlot.SiegePlaced).
func (t *TapExecutor) TapSlot(slot *TrackedSlot, jitterPx int) bool {
	if slot != nil && slot.SiegePlaced {
		t.logger.Warn().
			Str("unit", slot.UnitName).
			Int("x", slot.X).
			Msg("refusing to tap the siege card again: its one placement tap is spent and re-selecting it is how a second field tap gets fired")
		return false
	}
	if !t.consumeHeroSpotTap(slot, "select") {
		return false
	}
	t.noteInput(t.fireSlotTap(slot, jitterPx), "slot select")
	if !isHeroCardSlot(slot) {
		t.inputMu.Lock()
		t.lastNonHeroSlot = slot
		t.inputMu.Unlock()
	}
	return true
}

// PlaceSiege fires the ONE field tap that drops a siege machine, or refuses to
// fire at all.
//
// This is the only function any deploy path may use to place a siege machine.
// A siege leaves the bar on its first field tap; a second tap lands on the
// machine that is already standing there and destroys it, so the count of
// placement taps per battle must be exactly one no matter how many phases,
// sweeps and verifier retries look at the slot afterwards. Putting the tap and
// the bookkeeping in one function is what makes that checkable: every caller
// gets the same answer, and a path that tries again is refused rather than
// trusted to remember.
//
// Returns true when the placement tap was fired.
func (t *TapExecutor) PlaceSiege(slot *TrackedSlot, pt image.Point, stdDev float64) bool {
	if slot != nil && slot.SiegePlaced {
		t.logger.Warn().
			Str("unit", slot.UnitName).
			Int("x", slot.X).
			Interface("pt", pt).
			Msg("refusing a second siege placement tap: the machine is already on the field and a second tap destroys it")
		return false
	}
	t.noteInput(t.client.TapFast(pt.X, pt.Y, stdDev), "siege placement")
	// Record the machine's tile before anything else can tap the field, so every
	// later placement path has the point it must stay clear of.
	t.siegeGround, t.siegeGroundSet = pt, true
	t.siegePlacements++
	if slot != nil {
		slot.SiegePlaced = true
	}
	return true
}

// SiegeGround returns the tile of the siege machine this battle has placed.
func (t *TapExecutor) SiegeGround() (image.Point, bool) {
	return t.siegeGround, t.siegeGroundSet
}

// SiegeNudges reports how many placement taps were moved off the machine's tile.
func (t *TapExecutor) SiegeNudges() int { return t.siegeNudges }

// SiegePlacements reports how many siege machines were placed this battle. Any
// value above 1 is a bug the run must be able to see.
func (t *TapExecutor) SiegePlacements() int { return t.siegePlacements }

// avoidSiegeGround moves a placement point off the tile the siege machine
// occupies, and reports whether it had to move it.
//
// This is the siege-safety invariant, not a policy that each deploy path
// remembers to apply: a siege machine that is already on the field is destroyed
// by a second placement tap on its tile, so NO field tap that follows the siege
// may land there — not the heroes on the shared hero point, not a sweep retry,
// not a verifier pass. The point is moved along the direction it already lies in,
// so a hero that wanted the machine's tile lands the nearest clear ground beside
// it and still tanks for it.
func (t *TapExecutor) avoidSiegeGround(pt image.Point) (image.Point, bool) {
	if !t.siegeGroundSet {
		return pt, false
	}
	clearance := int(t.cal.Length(siegeClearanceRef))
	if clearance < 1 {
		clearance = siegeClearanceRef
	}
	dx, dy := pt.X-t.siegeGround.X, pt.Y-t.siegeGround.Y
	if d2 := dx*dx + dy*dy; d2 >= clearance*clearance {
		return pt, false
	}

	d := math.Sqrt(float64(dx*dx + dy*dy))
	ux, uy := 1.0, 0.0
	if d >= 1 {
		ux, uy = float64(dx)/d, float64(dy)/d
	}
	step := float64(clearance) * 1.15
	moved := image.Pt(
		t.siegeGround.X+int(math.Round(ux*step)),
		t.siegeGround.Y+int(math.Round(uy*step)),
	)
	t.siegeNudges++
	t.logger.Warn().
		Interface("was", pt).
		Interface("siege_ground", t.siegeGround).
		Interface("moved_to", moved).
		Int("clearance_px", clearance).
		Int("nudges", t.siegeNudges).
		Msg("placement tap was aimed at the tile the siege machine already occupies; moved clear of it (tapping it again destroys the machine)")
	return moved, true
}

// clearsSiege reports whether pt is far enough from the placed siege machine to
// be usable placement ground. Selection code uses it to keep the machine's tile
// out of a candidate list; avoidSiegeGround is the tap-time backstop for anything
// that slips through.
func (t *TapExecutor) clearsSiege(pt image.Point) bool {
	if !t.siegeGroundSet {
		return true
	}
	clearance := int(t.cal.Length(siegeClearanceRef))
	if clearance < 1 {
		clearance = siegeClearanceRef
	}
	dx, dy := pt.X-t.siegeGround.X, pt.Y-t.siegeGround.Y
	return dx*dx+dy*dy >= clearance*clearance
}

// avoidSiegeGroundXY is avoidSiegeGround for the batched tap loops, which work
// in raw coordinates rather than points.
func (t *TapExecutor) avoidSiegeGroundXY(x, y int) (int, int) {
	moved, _ := t.avoidSiegeGround(image.Pt(x, y))
	return moved.X, moved.Y
}

// TapUnitField fires ONE unit placement tap on the field, moved clear of any
// siege machine already standing there.
//
// Every single-tap placement path goes through here (hero drops, hero field-tap
// retries, sweep hero taps), so the siege tile is protected in one place rather
// than in each caller. Jitter belongs OUTSIDE this call: the clearance check has
// to be the last thing between the caller's aim and the wire.
func (t *TapExecutor) TapUnitField(pt image.Point, stdDev float64) {
	pt, _ = t.avoidSiegeGround(pt)
	t.noteInput(t.client.TapFast(pt.X, pt.Y, stdDev), "unit field tap")
}

// RearmSlot re-selects a hero's card after a placement the game did not
// register, so the next field tap is aimed at a SELECTED card. See maxHeroRearms
// for the measurement that makes this necessary. It deliberately does not consume
// the placement budget: nothing was placed, so the hero still has one placement
// and one ability to spend.
func (t *TapExecutor) RearmSlot(slot *TrackedSlot) bool {
	if !isHeroCardSlot(slot) {
		return false
	}
	if slot.SpotRearms >= maxHeroRearms {
		t.logger.Debug().
			Str("unit", slot.UnitName).
			Int("x", slot.X).
			Int("rearms", slot.SpotRearms).
			Msg("hero re-arm budget spent; not tapping the card again")
		return false
	}
	slot.SpotRearms++
	t.logger.Info().
		Str("unit", slot.UnitName).
		Int("x", slot.X).
		Int("rearms", slot.SpotRearms).
		Msg("re-arming the hero card: the previous placement was refused, which deselects the card, so re-tapping it is the only way the next field tap can land (no placement budget spent)")
	t.noteInput(t.fireSlotTap(slot, heroCardTapJitter), "hero re-arm")
	return true
}

// fireSlotTap sends the card tap itself. Shared by TapSlot and RearmSlot so the
// two can never drift apart in jitter or coordinates. Returns the transport
// error so callers can feed the sticky per-battle input verdict.
func (t *TapExecutor) fireSlotTap(slot *TrackedSlot, jitterPx int) error {
	ptY := slot.Y
	if strings.Contains(strings.ToLower(slot.UnitName), "warden") {
		ptY -= int(t.cal.Length(25))
	}
	jPt := t.addJitter(image.Pt(slot.X, ptY), jitterPx)
	t.logger.Debug().
		Int("x", jPt.X).
		Int("y", jPt.Y).
		Str("unit", slot.UnitName).
		Msg("tapping slot")
	return t.client.TapFast(jPt.X, jPt.Y, 2.0)
}

// TapDeployLine distributes taps along a line from p1 to p2.
// Direction alternates per call (boustrophedon): down the line, then
// back up the next call, so consecutive passes never restart at the top.
func (t *TapExecutor) TapDeployLine(p1, p2 image.Point, count int, jitterPx int) {
	points := t.calculateLinePoints(p1, p2, count)

	t.lineForward = !t.lineForward
	if !t.lineForward {
		for i, j := 0, len(points)-1; i < j; i, j = i+1, j-1 {
			points[i], points[j] = points[j], points[i]
		}
	}

	// A sweep re-firing troops after the siege phase walks the same line, and the
	// pinned line can run straight through the machine's tile. Move any point that
	// would land on it before batching, so the protection costs no extra tap.
	for i, pt := range points {
		if moved, ok := t.avoidSiegeGround(pt); ok {
			points[i] = moved
		}
	}

	for i := 0; i < len(points); {
		rem := len(points) - i
		if rem >= 3 {
			j1 := t.addJitter(points[i], jitterPx)
			j2 := t.addJitter(points[i+1], jitterPx)
			j3 := t.addJitter(points[i+2], jitterPx)
			t.noteInput(t.client.TapTriple(j1.X, j1.Y, 15.0, j2.X, j2.Y, 15.0, j3.X, j3.Y, 15.0), "deploy triple")
			i += 3
		} else if rem == 2 {
			j1 := t.addJitter(points[i], jitterPx)
			j2 := t.addJitter(points[i+1], jitterPx)
			t.noteInput(t.client.TapDual(j1.X, j1.Y, 15.0, j2.X, j2.Y, 15.0), "deploy dual")
			i += 2
		} else {
			j1 := t.addJitter(points[i], jitterPx)
			t.noteInput(t.client.TapFast(j1.X, j1.Y, 15.0), "deploy single")
			i += 1
		}
		t.sleepBetweenBatches()
	}
}

// TapDeployPoint clusters taps around a single point.
func (t *TapExecutor) TapDeployPoint(pt image.Point, count int, jitterPx int) {
	pt, _ = t.avoidSiegeGround(pt)
	for i := 0; i < count; {
		rem := count - i
		if rem >= 3 {
			j1 := t.addJitter(pt, jitterPx)
			j2 := t.addJitter(pt, jitterPx)
			j3 := t.addJitter(pt, jitterPx)
			t.noteInput(t.client.TapTriple(j1.X, j1.Y, 12.0, j2.X, j2.Y, 12.0, j3.X, j3.Y, 12.0), "point triple")
			i += 3
		} else if rem == 2 {
			j1 := t.addJitter(pt, jitterPx)
			j2 := t.addJitter(pt, jitterPx)
			t.noteInput(t.client.TapDual(j1.X, j1.Y, 12.0, j2.X, j2.Y, 12.0), "point dual")
			i += 2
		} else {
			j1 := t.addJitter(pt, jitterPx)
			t.noteInput(t.client.TapFast(j1.X, j1.Y, 12.0), "point single")
			i += 1
		}
		t.sleepBetweenBatches()
	}
}

// TapDeployFourSides performs rapid simultaneous 4-side spam deployment across all 4 sides at once.
func (t *TapExecutor) TapDeployFourSides(pCfg PrecisionConfig, targetEdge string, countPerSide int, jitterPx int) {
	edgeNames := []string{"TopRight", "BottomRight", "BottomLeft", "TopLeft"}
	type edgeLine struct {
		name   string
		p1, p2 image.Point
	}
	cx, cy := 640, 360
	pushOutward := func(pt image.Point, dist float64) image.Point {
		dx := float64(pt.X - cx)
		dy := float64(pt.Y - cy)
		h := math.Hypot(dx, dy)
		if h == 0 {
			return pt
		}
		return image.Pt(int(float64(pt.X)+dx/h*dist), int(float64(pt.Y)+dy/h*dist))
	}

	var validEdges []edgeLine
	for _, name := range edgeNames {
		if edge, ok := pCfg.Edges[name]; ok {
			// Push outward into grass to avoid red deploy boundary
			p1 := pushOutward(edge.P1, 15)
			p2 := pushOutward(edge.P2, 15)
			validEdges = append(validEdges, edgeLine{name: name, p1: p1, p2: p2})
		}
	}
	if len(validEdges) == 0 {
		return
	}

	steps := countPerSide
	if steps < 14 {
		steps = 14 // At least 14 per side = 56 taps, drops all 52 Valkyries in ~1.5s
	}

	t.logger.Info().Int("steps_per_side", steps).Int("total_taps", steps*len(validEdges)).Msg("FourSides simultaneous rapid spam across all 4 sides")

	for i := 0; i < steps; i++ {
		pct := float64(i) / float64(steps-1)
		for _, e := range validEdges {
			tx, ty := intLerp(e.p1, e.p2, pct)
			j := t.addJitter(image.Pt(tx, ty), 4)
			t.client.TapFast(j.X, j.Y, 3.0)
			time.Sleep(18 * time.Millisecond)
		}
	}
	time.Sleep(100 * time.Millisecond)
}

// TapHeroAbility taps a hero slot to activate its ability.
//
// The activation is gated on its OWN one-shot flag, not on the placement
// budget: placement and activation are different intents. What the 2-tap rule
// protects is "never two placements and never a repeated activation", and
// that is still enforced — placement is capped by maxHeroSpotTaps and the
// ability by AbilityFired, so a hero can be activated exactly once per battle
// however many paths race to do it. Returns false when the ability already
// fired and no tap was sent.
func (t *TapExecutor) TapHeroAbility(slot *TrackedSlot) bool {
	if slot != nil && slot.AbilityFired {
		t.logger.Warn().
			Str("unit", slot.UnitName).
			Int("x", slot.X).
			Msg("hero ability already fired this battle; refusing to tap the hero again")
		return false
	}
	if slot != nil {
		slot.AbilityFired = true
	}

	ptY := slot.Y
	if strings.Contains(strings.ToLower(slot.UnitName), "warden") {
		ptY -= int(t.cal.Length(25))
	}
	t.logger.Info().
		Int("x", slot.X).
		Int("y", ptY).
		Str("unit", slot.UnitName).
		Msg("tapping hero ability")
	t.noteInput(t.client.TapFast(slot.X, ptY, 4.0), "hero ability")
	return true
}

// WaitForSettle waits for deployment to settle.
func (t *TapExecutor) WaitForSettle(duration time.Duration) {
	time.Sleep(duration)
}

// sleepBetweenBatches waits between tap batches.
// Tightened for speed: 60±20ms is the empirical floor below which CoC
// stacks consecutive tap triples as a single deploy gesture. The 10%
// "long pause" branch preserves a touch of variability for the anti-cheat
// heuristic but stays under 100ms.
func (t *TapExecutor) sleepBetweenBatches() {
	sleepBase := 60
	sleepDev := 20
	if rand.Float64() < 0.10 {
		sleepBase = 90
		sleepDev = 25
	}
	t.client.HumanSleep(sleepBase, sleepDev)
}

// HumanSleep wraps client HumanSleep.
func (t *TapExecutor) HumanSleep(baseMs, stdDevMs int) {
	t.client.HumanSleep(baseMs, stdDevMs)
}

// addJitter adds random pixel offset for human-like tap positions.
func (t *TapExecutor) addJitter(pt image.Point, maxPixels int) image.Point {
	if maxPixels <= 0 {
		return pt
	}
	jx := int(t.cal.Length(float64(maxPixels)))
	jy := jx
	if jx <= 0 {
		jx = 1
	}
	if jy <= 0 {
		jy = 1
	}
	return image.Pt(
		pt.X+rand.Intn(jx*2+1)-jx,
		pt.Y+rand.Intn(jy*2+1)-jy,
	)
}

// calculateLinePoints distributes points along a line.
func (t *TapExecutor) calculateLinePoints(p1, p2 image.Point, count int) []image.Point {
	points := make([]image.Point, 0, count)
	for i := 0; i < count; i++ {
		pct := 0.5
		if count > 1 {
			pct = float64(i) / float64(count-1)
		}
		tx, ty := intLerp(p1, p2, pct)
		points = append(points, image.Pt(tx, ty))
	}
	return points
}

// intLerp interpolates between two points.
func intLerp(p1, p2 image.Point, pct float64) (int, int) {
	return int(float64(p1.X) + float64(p2.X-p1.X)*pct),
		int(float64(p1.Y) + float64(p2.Y-p1.Y)*pct)
}
