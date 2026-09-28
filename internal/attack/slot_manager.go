package attack

import (
	"encoding/json"
	"image"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/Ducky705/ClashGO/internal/game"
	"github.com/Ducky705/ClashGO/internal/vision"
	"github.com/rs/zerolog"
	"gocv.io/x/gocv"
)

// SlotState tracks lifecycle of each detected slot.
type SlotState int

const (
	SlotDetected SlotState = iota
	SlotIdentified
	SlotAttempted
	SlotDeployed
	SlotFailed
)

func (s SlotState) String() string {
	switch s {
	case SlotDetected:
		return "Detected"
	case SlotIdentified:
		return "Identified"
	case SlotAttempted:
		return "Attempted"
	case SlotDeployed:
		return "Deployed"
	case SlotFailed:
		return "Failed"
	default:
		return "Unknown"
	}
}

// TrackedSlot extends TroopSlot with state tracking.
type TrackedSlot struct {
	TroopSlot
	State           SlotState `json:"state"`
	UnitName        string    `json:"unit_name"`
	Confidence      float64   `json:"confidence"`
	Attempts        int       `json:"attempts"`
	LastTapAt       time.Time `json:"last_tap_at"`
	IsEmpty         bool      `json:"is_empty"`
	FallbackLabeled bool      `json:"fallback_labeled"` // name came from stale manual_labels.json, not a template match

	// SpotTaps counts how many times the bot has tapped this slot's CARD
	// (not the field). A hero card is capped at maxHeroSpotTaps: one tap
	// selects/places the hero and one more activates its ability. Every
	// deploy path shares this one counter (main drop, sweep, verifier,
	// ability pass) so no combination of retries can ever exceed the cap.
	SpotTaps int `json:"spot_taps"`

	// SpotRearms counts REPLACEMENT card taps made after a placement the game
	// refused (which deselects the card). Bounded by maxHeroRearms; kept apart
	// from SpotTaps so a re-arm can never steal the ability's tap.
	SpotRearms int `json:"spot_rearms"`
}

// SlotManager handles slot detection, classification, identity resolution, and state tracking.
type SlotManager struct {
	slots     []*TrackedSlot
	unitIndex map[string]*TrackedSlot
	xIndex    map[int]*TrackedSlot
	w         int
	h         int
	slotY     int
	barY      int
	// fallbackAssigned records spell units routed through unnamed spell
	// slots (see GetSlot), so two strategy spells never share one card.
	fallbackAssigned map[int]string

	// scale converts reference pixel distances (slot spacing, margins) to live
	// pixels: the display scale, applied to both axes.
	scale  func(float64) float64
	logger zerolog.Logger
}

// NewSlotManager detects active slots, resolves identities via template matching + manual labels.
func NewSlotManager(
	screen gocv.Mat,
	cal *game.Calibration,
	pCfg PrecisionConfig,
	w, h, mBarY int,
	templates map[string]gocv.Mat,
	classify func(gocv.Mat) (game.GameState, int),
	logger zerolog.Logger,
) *SlotManager {
	scale := func(px float64) float64 { return px }
	if cal != nil {
		scale = cal.Length
	}
	sm := &SlotManager{
		unitIndex:        make(map[string]*TrackedSlot),
		xIndex:           make(map[int]*TrackedSlot),
		fallbackAssigned: make(map[int]string),
		w:                w,
		h:                h,
		barY:             mBarY,
		scale:            scale,
		logger:           logger.With().Str("component", "slot_manager").Logger(),
	}
	_ = screen

	sm.slotY = mBarY + int(sm.scale(38))
	if data, ok := readConfigJSON("manual_slots.json"); ok {
		var mConf struct {
			SlotY      int `json:"slot_y"`
			CardHeight int `json:"card_height"`
		}
		if json.Unmarshal(data, &mConf) == nil {
			if mConf.SlotY > 0 {
				sm.slotY = mConf.SlotY
			} else if mConf.CardHeight > 0 {
				sm.slotY = mBarY + mConf.CardHeight/2
			}
		}
	}

	activeXs := sm.detectActiveSlots(screen)
	if len(activeXs) == 0 {
		sm.logger.Warn().Msg("no active slots detected")
		return sm
	}

	barROI := image.Rect(0, mBarY, w, h)
	sm.classifySlots(screen, activeXs, templates, barROI)

	sm.applyManualLabelsFallback()

	for _, slot := range sm.slots {
		sm.xIndex[slot.X] = slot
		if slot.UnitName != "" {
			sm.unitIndex[strings.ToLower(slot.UnitName)] = slot
		}
	}

	sm.logger.Info().Int("total", len(sm.slots)).Msg("slot manager initialized")
	return sm
}

// detectActiveSlots finds all non-empty X positions on the troop bar.
//
// `manual_slots.json`'s `slot_xs` are LIVE-frame card CENTRES, one per card
// position left to right, consumed here verbatim — no scaling. That is worth
// stating because the values in the file are the whole geometry contract: the
// list it replaces was authored in the 860-wide reference frame (pitch 73 against
// a live pitch of ~100) and was then used as live pixels anyway, so its anchors
// drifted up to 45px off the cards and two of them landed on the same card. They
// are measured from real 1280x720 battle frames now: card bodies are 88px wide on
// a ~100px pitch, the first at x 93, and each entry below is a body centre
// cross-checked against that card's count badge (which the game draws
// right-aligned inside the card, 1-5px in from its right edge).
func (sm *SlotManager) detectActiveSlots(screen gocv.Mat) []int {

	if data, ok := readConfigJSON("manual_slots.json"); ok {
		var mConf struct {
			SlotXs []int `json:"slot_xs"`
			SlotY  int   `json:"slot_y"`
		}
		if json.Unmarshal(data, &mConf) == nil && len(mConf.SlotXs) > 0 {
			sm.logger.Info().Int("slots", len(mConf.SlotXs)).Msg("using precise manual slot mapping")
			var activeXs []int
			for _, x := range mConf.SlotXs {
				if !isSlotEmptyStatic(screen, x, sm.slotY, sm.w, sm.h) {
					activeXs = append(activeXs, x)
				}
			}
			return activeXs
		}
	}

	sm.logger.Info().Msg("manual calibration missing, falling back to grid detection")
	// Slot spacing and the first slot's offset are lengths: they grow with the
	// display scale, not with a per-axis framebuffer ratio (which is 1.49 x at
	// 1280x720 against a measured 1.325 and would drift a whole slot by the end
	// of the bar).
	step := int(sm.scale(75))
	startX := int(sm.scale(40))
	var activeXs []int
	for x := startX; x < sm.w-20; x += step {
		if !isSlotEmptyStatic(screen, x, sm.slotY, sm.w, sm.h) {
			activeXs = append(activeXs, x)
		}
	}
	return activeXs
}

// classifySlots runs template matching to identify units and assign categories.
func (sm *SlotManager) classifySlots(screen gocv.Mat, activeXs []int, templates map[string]gocv.Mat, barROI image.Rectangle) {

	for _, x := range activeXs {
		sm.slots = append(sm.slots, &TrackedSlot{
			TroopSlot: TroopSlot{X: x, Y: sm.slotY, Category: "Troop"},
			State:     SlotDetected,
		})
	}

	type templateResult struct {
		name  string
		match vision.Match
	}
	var results []templateResult

	for tplName, tpl := range templates {
		if tpl.Empty() {
			continue
		}
		matches, _ := vision.MatchMultiScaleROICached(screen, tpl, tplName, 0.2, 1.2, 20, 0.55, barROI)
		if len(matches) > 0 {
			sort.Slice(matches, func(i, j int) bool { return matches[i].Confidence > matches[j].Confidence })
			results = append(results, templateResult{name: tplName, match: matches[0]})
		}
	}

	sort.Slice(results, func(i, j int) bool { return results[i].match.Confidence > results[j].match.Confidence })

	for _, res := range results {
		cleanName := strings.ReplaceAll(res.name, "_", " ")
		bestSlot := sm.findClosestSlot(res.match.Point.X, 0.04)
		if bestSlot == nil {
			continue
		}

		if bestSlot.UnitName != "" && bestSlot.Confidence >= res.match.Confidence {
			sm.logger.Debug().
				Int("x", bestSlot.X).
				Str("existing", bestSlot.UnitName).
				Float64("existing_conf", bestSlot.Confidence).
				Str("skipping", cleanName).
				Float64("skip_conf", res.match.Confidence).
				Msg("slot identity already assigned at higher confidence")
			continue
		}

		bestSlot.UnitName = cleanName
		bestSlot.Confidence = res.match.Confidence
		bestSlot.State = SlotIdentified

		if isHeroStatic(cleanName) {
			bestSlot.Category = "Hero"
		} else if isSiegeStatic(cleanName) {
			bestSlot.Category = "Siege"
		} else if isSpellStatic(cleanName) {
			bestSlot.Category = "Spell"
		} else if strings.Contains(cleanName, "cc") || strings.Contains(cleanName, "castle") {
			bestSlot.Category = "CC"
		}

		sm.logger.Info().
			Str("unit", cleanName).
			Int("x", bestSlot.X).
			Float64("conf", res.match.Confidence).
			Msg("identified unit via template match")
	}

	sm.applyPositionalClassification(activeXs)
}

// applyPositionalClassification uses hero/spell anchors to classify unidentified slots.
func (sm *SlotManager) applyPositionalClassification(activeXs []int) {

	firstHeroX := 9999
	lastHeroX := -1
	firstSpellX := 9999

	for _, slot := range sm.slots {
		if slot.UnitName == "" {
			continue
		}
		if isHeroStatic(slot.UnitName) {
			if slot.X < firstHeroX {
				firstHeroX = slot.X
			}
			if slot.X > lastHeroX {
				lastHeroX = slot.X
			}
		}
		if isSpellStatic(slot.UnitName) {
			if slot.X < firstSpellX {
				firstSpellX = slot.X
			}
		}
	}

	if firstHeroX == 9999 {
		idx := len(activeXs) / 2
		if idx < len(activeXs) {
			firstHeroX = activeXs[idx]
			lastHeroX = firstHeroX
		}
	}
	if firstSpellX == 9999 {
		firstSpellX = lastHeroX + int(sm.scale(70))
	}

	heroMargin := int(sm.scale(30))
	spellMargin := int(sm.scale(30))

	for _, slot := range sm.slots {

		if slot.UnitName != "" {
			continue
		}

		isSiege := slot.Category == "Siege"

		if !isSiege && firstHeroX != 9999 && slot.X < firstHeroX {
			isLastBeforeHero := true
			for _, otherSlot := range sm.slots {
				if otherSlot.X > slot.X && otherSlot.X < firstHeroX {
					isLastBeforeHero = false
					break
				}
			}
			if isLastBeforeHero && slot.X != activeXs[0] {
				isSiege = true
			}
		}

		if slot.X >= firstSpellX-spellMargin {
			slot.Category = "Spell"
		} else if slot.X >= firstHeroX-heroMargin && slot.X <= lastHeroX+heroMargin {
			slot.Category = "Hero"
		} else if isSiege {
			slot.Category = "Siege"
		}
	}

	if len(sm.slots) > 0 {
		lastSlot := sm.slots[len(sm.slots)-1]
		if lastSlot.Category == "Spell" && lastSlot.X > sm.w-int(sm.scale(100)) {
			lastSlot.Category = "CC"
			sm.logger.Info().Int("x", lastSlot.X).Msg("classified last slot as CC")
		}
	}
}

// applyManualLabelsFallback fills unidentified slots with manual_labels.json data.
func (sm *SlotManager) applyManualLabelsFallback() {
	data, ok := readConfigJSON("manual_labels.json")
	if !ok {
		return
	}
	sm.applyManualLabels(data)
}

// applyManualLabels fills unidentified slots from manual_labels.json data.
// Split out from applyManualLabelsFallback so tests can feed crafted
// configs without touching the on-disk assets.
func (sm *SlotManager) applyManualLabels(data []byte) {
	var lConf struct {
		Slots []struct {
			X    int    `json:"x"`
			Name string `json:"name"`
		} `json:"slots"`
	}
	if json.Unmarshal(data, &lConf) != nil {
		return
	}

	manualMap := make(map[int]string)
	for _, s := range lConf.Slots {
		manualMap[s.X] = s.Name
	}

	for _, slot := range sm.slots {
		if slot.UnitName != "" {
			continue
		}
		label, ok := manualMap[slot.X]
		if !ok || label == "Empty" {
			continue
		}
		cleanName := strings.ToLower(strings.TrimSpace(label))

		// A template match on a DIFFERENT slot wins over this stale
		// manual label. manual_labels.json is calibrated on one army
		// layout; reusing a name that template matching already
		// assigned elsewhere would create a DUPLICATE identity. The
		// duplicate then hijacks the strategy's unit lookup
		// (unitIndex keeps the LAST assignment per name), so e.g.
		// "grand warden" could resolve to the wrong card while the
		// real hero slot never gets its main-phase deploy and the
		// wrongly-labeled card is never swept (it looks deployed).
		// Seen live with valk_spam: the stale "grand warden" label
		// on the unidentified Dragon Duke slot stole the warden's
		// deploy and left the Duke in the bar all battle.
		duplicate := false
		for _, other := range sm.slots {
			if other != slot && strings.EqualFold(other.UnitName, cleanName) {
				duplicate = true
				break
			}
		}
		if duplicate {
			sm.logger.Warn().
				Int("x", slot.X).
				Str("fallback", cleanName).
				Msg("skipping stale fallback label; template match already assigned this unit elsewhere")
			continue
		}

		slot.UnitName = cleanName
		slot.Confidence = 1.0
		slot.State = SlotIdentified
		// Mark as fallback-labeled so the sweep treats it as a probable
		// event/bonus troop rather than trusting the stale label. The
		// user's manual_labels.json reflects an old army; a slot that
		// template matching couldn't identify is more likely a seasonal
		// bonus card than whatever the label claims.
		slot.FallbackLabeled = true

		if isHeroStatic(cleanName) {
			slot.Category = "Hero"
		} else if isSiegeStatic(cleanName) {
			slot.Category = "Siege"
		} else if isSpellStatic(cleanName) {
			slot.Category = "Spell"
		} else if strings.Contains(cleanName, "cc") || strings.Contains(cleanName, "castle") {
			slot.Category = "CC"
		}

		sm.logger.Warn().
			Int("x", slot.X).
			Str("fallback", cleanName).
			Msg("using fallback manual label")
	}
}

// findClosestSlot finds the slot closest to a given X within tolerance.
func (sm *SlotManager) findClosestSlot(x int, tolerancePct float64) *TrackedSlot {
	tolerance := float64(sm.w) * tolerancePct
	var best *TrackedSlot
	bestDist := tolerance
	for _, slot := range sm.slots {
		dist := math.Abs(float64(x - slot.X))
		if dist < bestDist {
			bestDist = dist
			best = slot
		}
	}
	return best
}

// --- Public API ---

// GetSlot returns the tracked slot for a unit name (case-insensitive).
func (sm *SlotManager) GetSlot(unitName string) *TrackedSlot {
	n := strings.ToLower(strings.TrimSpace(unitName))
	if slot, ok := sm.unitIndex[n]; ok {
		return slot
	}
	return sm.fallbackSpellSlot(n)
}

// fallbackSpellSlot returns an unnamed spell-category slot for a strategy
// spell the classifier could not identify. Live failure this fixes
// (valk_run6): the earthquake card template-matched nothing, so the spell
// plan resolved no slot, the Earthquakes phase deployed NOTHING and the
// spells were cast only by the sweep — after the hero abilities, on the
// generic red-zone line, re-fired 5 times. A spell in the spell region of
// the bar can only be a spell, so the fallback is safe; troops/heroes are
// NOT eligible (a wrong fallback there double-deploys real units). Each
// fallback assignment is recorded so two spell units never share a card.
func (sm *SlotManager) fallbackSpellSlot(unitName string) *TrackedSlot {
	if !isSpellStatic(unitName) {
		return nil
	}
	for _, slot := range sm.slots {
		if slot.Category != "Spell" || slot.UnitName != "" {
			continue
		}
		if prev, taken := sm.fallbackAssigned[slot.X]; taken {
			if prev == unitName {
				return slot
			}
			continue
		}
		if sm.fallbackAssigned == nil {
			sm.fallbackAssigned = make(map[int]string)
		}
		sm.fallbackAssigned[slot.X] = unitName
		sm.logger.Info().
			Str("unit", unitName).
			Int("x", slot.X).
			Msg("spell not identified on bar; deploying through unnamed spell slot fallback")
		return slot
	}
	return nil
}

// GetAllSlots returns all tracked slots.
func (sm *SlotManager) GetAllSlots() []*TrackedSlot {
	return sm.slots
}

// GetSlotY returns the Y coordinate used for slot detection.
func (sm *SlotManager) GetSlotY() int {
	return sm.slotY
}

// GetBarY returns the Y coordinate of the troop-bar top (where deck
// counts are printed above each card). HeroManager / Sweeper / Verifier
// use this to live-OCR the per-card count above each slot.
func (sm *SlotManager) GetBarY() int {
	return sm.barY
}

// GetUndeployedSlots returns slots not in Deployed or Failed state.
func (sm *SlotManager) GetUndeployedSlots() []*TrackedSlot {
	var result []*TrackedSlot
	for _, slot := range sm.slots {
		if slot.State != SlotDeployed && slot.State != SlotFailed {
			result = append(result, slot)
		}
	}
	return result
}

// GetEventTroops returns slots with unit names not in the given strategy unit list.
//
// "Event" here means any Troop/Spell/CC slot that the strategy did NOT
// declare. Bonus/seasonal event troops change names every season and may
// template-match to nothing (empty UnitName), so we deliberately:
//   - include empty-UnitName slots (an unlabelled troop card is still a
//     card the user wants placed),
//   - include every Troop/Spell/CC slot whose name is not a strategy unit
//     (covers seasonal names that never appear in the YAML),
//   - include fallback-labeled slots REGARDLESS of category/name — the
//     label comes from manual_labels.json (an old army) and can be a
//     hero name ("archer queen") that collides with a strategy unit.
//     If template matching couldn't ID the card, it's probably a bonus
//     troop, so don't let a stale hero label hide it.
//
// Slots already Deployed/Failed are skipped so the sweep doesn't re-fire
// slots the strategy phase already drained. Siege is included because a
// non-strategy siege slot (e.g. a seasonal bonus troop that got
// fallback-labeled "siege machine") is still a card the user wants
// placed; the strategy's OWN siege is excluded by its declared name.
func (sm *SlotManager) GetEventTroops(strategyUnitNames []string) []*TrackedSlot {
	strategySet := make(map[string]bool)
	for _, name := range strategyUnitNames {
		strategySet[strings.ToLower(name)] = true
	}

	var result []*TrackedSlot
	for _, slot := range sm.slots {
		if slot.State == SlotDeployed || slot.State == SlotFailed {
			continue
		}
		if slot.FallbackLabeled {
			// Stale label — treat as event troop regardless of what the
			// label claims (hero/spell/siege all possible).
			result = append(result, slot)
			continue
		}
		if slot.Category != "Troop" && slot.Category != "Spell" && slot.Category != "CC" && slot.Category != "Siege" {
			continue
		}
		name := strings.ToLower(strings.TrimSpace(slot.UnitName))
		if name != "" && strategySet[name] {
			continue
		}
		result = append(result, slot)
	}
	return result
}

// RecordAttempt records a deployment attempt for a slot.
func (sm *SlotManager) RecordAttempt(unitName string, success bool) {
	slot := sm.GetSlot(unitName)
	if slot == nil {
		return
	}
	slot.Attempts++
	slot.LastTapAt = time.Now()
	slot.State = SlotAttempted

	if success {
		slot.State = SlotDeployed
	}
}

// MarkDeployed marks a slot as successfully deployed.
func (sm *SlotManager) MarkDeployed(unitName string) {
	slot := sm.GetSlot(unitName)
	if slot == nil {
		return
	}
	slot.State = SlotDeployed
	slot.IsEmpty = true
}

// MarkSlotDeployed marks a specific slot as successfully deployed. Used
// for fallback-labeled/event slots where the UnitName may collide with a
// template-matched slot (e.g. a bonus troop stale-labeled "archer queen"
// shares a name with the real hero), so name-based lookup is unsafe.
func (sm *SlotManager) MarkSlotDeployed(slot *TrackedSlot) {
	if slot == nil {
		return
	}
	slot.State = SlotDeployed
	slot.IsEmpty = true
}

// MarkFailed marks a slot as failed after exhausting retries.
func (sm *SlotManager) MarkFailed(unitName string) {
	slot := sm.GetSlot(unitName)
	if slot == nil {
		return
	}
	slot.State = SlotFailed
}

// MarkSlotFailed marks a specific slot as failed. Pointer-based variant
// for fallback-labeled/event slots whose UnitName may collide.
func (sm *SlotManager) MarkSlotFailed(slot *TrackedSlot) {
	if slot == nil {
		return
	}
	slot.State = SlotFailed
}

// --- Static helper functions (no Executor dependency) ---

func isHeroStatic(name string) bool {
	n := strings.ToLower(name)
	return strings.Contains(n, "king") || strings.Contains(n, "queen") || strings.Contains(n, "warden") ||
		strings.Contains(n, "prince") || strings.Contains(n, "duke") || strings.Contains(n, "champion")
}

func isSiegeStatic(name string) bool {
	n := strings.ToLower(name)
	return strings.Contains(n, "slammer") || strings.Contains(n, "siege") || strings.Contains(n, "blimp") ||
		strings.Contains(n, "wrecker") || strings.Contains(n, "launcher") || strings.Contains(n, "drill")
}

func isSpellStatic(name string) bool {
	return strings.Contains(strings.ToLower(name), "spell")
}

// slotActivity computes the active-content ratio for a slot region using a shared
// Mat pool, reusing a single HSV conversion and mask buffers across the computation.
// The single CvtColor + mask set previously ran 3 separate copies of this pipeline
// (isSlotEmpty, getSlotActivityRatio, GetSlotActivityRatioStatic); this consolidates
// them and avoids per-call NewMat allocations in the hot deploy loop.
func slotActivity(screen gocv.Mat, x, y, screenW, sizeHint int) float64 {
	if screen.Empty() || x < 0 || y < 0 || x >= screen.Cols() || y >= screen.Rows() {
		return 0
	}

	size := sizeHint
	if size <= 0 {
		size = SlotProbeSize(screenW)
		if size <= 0 {
			return 0
		}
	}
	region := image.Rect(x-size, y-size, x+size, y+size)
	if region.Min.X < 0 {
		region.Min.X = 0
	}
	if region.Min.Y < 0 {
		region.Min.Y = 0
	}
	if region.Max.X > screen.Cols() {
		region.Max.X = screen.Cols()
	}
	if region.Max.Y > screen.Rows() {
		region.Max.Y = screen.Rows()
	}
	sub := screen.Region(region)
	defer sub.Close()

	hsv := vision.GetMat(sub.Rows(), sub.Cols(), gocv.MatTypeCV8UC3)
	defer vision.PutMat(hsv)
	gocv.CvtColor(sub, &hsv, gocv.ColorBGRToHSV)

	maskMap1 := vision.GetMatFrom(hsv)
	defer vision.PutMat(maskMap1)
	gocv.InRangeWithScalar(hsv, gocv.NewScalar(35, 31, 0, 0), gocv.NewScalar(90, 255, 255, 0), &maskMap1)

	maskMap2 := vision.GetMatFrom(hsv)
	defer vision.PutMat(maskMap2)
	gocv.InRangeWithScalar(hsv, gocv.NewScalar(0, 0, 0, 0), gocv.NewScalar(29, 49, 79, 0), &maskMap2)

	isMapMask := vision.GetMatFrom(hsv)
	defer vision.PutMat(isMapMask)
	gocv.BitwiseOr(maskMap1, maskMap2, &isMapMask)

	notMapMask := vision.GetMatFrom(hsv)
	defer vision.PutMat(notMapMask)
	gocv.BitwiseNot(isMapMask, &notMapMask)

	maskActA := vision.GetMatFrom(hsv)
	defer vision.PutMat(maskActA)
	gocv.InRangeWithScalar(hsv, gocv.NewScalar(0, 56, 91, 0), gocv.NewScalar(180, 255, 255, 0), &maskActA)

	maskActB := vision.GetMatFrom(hsv)
	defer vision.PutMat(maskActB)
	gocv.InRangeWithScalar(hsv, gocv.NewScalar(0, 0, 221, 0), gocv.NewScalar(180, 29, 255, 0), &maskActB)

	activeContentMask := vision.GetMatFrom(hsv)
	defer vision.PutMat(activeContentMask)
	gocv.BitwiseOr(maskActA, maskActB, &activeContentMask)

	finalActiveMask := vision.GetMatFrom(hsv)
	defer vision.PutMat(finalActiveMask)
	gocv.BitwiseAnd(activeContentMask, notMapMask, &finalActiveMask)

	activePixels := gocv.CountNonZero(finalActiveMask)
	total := hsv.Rows() * hsv.Cols()
	if total <= 0 {
		return 0
	}
	return float64(activePixels) / float64(total)
}

// SlotProbeSize is the half-width in pixels of the square region slotActivity
// samples around a slot centre at a given screen width (25px at the 860px
// reference layout, scaled by the screen width otherwise).
//
// Exported so the hero/sweep/verify failure logs can report the exact geometry
// they measured. That matters because the hero "deployed?" test is a delta on
// this region's activity ratio: if the square does not actually cover the card
// (wrong centre, or a size that spills onto neighbouring slots and the bar
// chrome), the delta is noise and a hero that deployed fine fails its own
// check — which is what makes the bot fire an extra retry tap.
func SlotProbeSize(screenW int) int {
	if screenW <= 0 {
		return 0
	}
	return int(25.0 * float64(screenW) / 860.0)
}

// isSlotEmptyStatic checks if a slot region is empty (no active content).
func isSlotEmptyStatic(screen gocv.Mat, x, y, screenW, screenH int) bool {
	return slotActivity(screen, x, y, screenW, 0) < 0.08
}

// GetSlotActivityRatioStatic returns the ratio of active content pixels in a slot region.
func GetSlotActivityRatioStatic(screen gocv.Mat, x, y, screenW int) float64 {
	return slotActivity(screen, x, y, screenW, 0)
}
