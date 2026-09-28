package attack

import (
	"strings"
	"time"

	"github.com/Ducky705/ClashGO/internal/game"
	"gocv.io/x/gocv"
)

// Hero HP-triggered ability, ported from mybot-py's monitor_hero_hp.
//
// ClashGo fires abilities on strategy timing only. A hero that tanks at low HP
// for the rest of a 3-minute battle never gets its ability (which also heals).
// The battle-end wait already captures a frame every tick, so polling the HP
// strip there costs zero extra captures: one narrow strip scan per watched
// hero per tick.
//
// Geometry, measured on internal/attack/testdata/live_bar_deploy_720p.png
// (1280x720, slot_y=682): a deployed hero's HP bar is a solid green band
// ~44px wide centred on the card, rows slotY-8..slotY-3. Ref offsets below
// reproduce that band through cal.Length (K=1.325 at 1280x720 → 8x5 live px).
const (
	heroHPThreshold   = 0.30            // fire ability below 30% HP
	heroHPMinAlive    = 0.03            // below this the bar is gone: dead or undeployed
	heroHPDeadStrikes = 2               // confirm death over 2 ticks (one 0.0 is a stray frame)
	heroWardenDelay   = 4 * time.Second // warden HP reads full till death: fire proactively

	heroHPStripTopRef = 8.0 // strip rows above the card centre, ref px
	heroHPStripBotRef = 3.0
	heroHPHalfWRef    = 17.0 // half strip width, ref px
)

// HeroHPFraction returns the fill fraction (0..1) of a deployed hero's HP bar:
// the share of strip columns containing green pixels. Green = g>110 and
// dominant over r/b by 25, same rule as the reference bot.
func HeroHPFraction(screen gocv.Mat, cardCX, slotY int, cal *game.Calibration) float64 {
	if screen.Empty() || screen.Cols() < 1 || screen.Rows() < 1 {
		return 0
	}
	k := 1.0
	if cal != nil {
		k = cal.Length(1)
	}
	top := slotY - int(heroHPStripTopRef*k+0.5)
	bot := slotY - int(heroHPStripBotRef*k+0.5)
	half := int(heroHPHalfWRef*k + 0.5)
	if half < 4 {
		half = 4
	}
	if top < 0 {
		top = 0
	}
	if bot > screen.Rows() {
		bot = screen.Rows()
	}
	if bot-top < 2 {
		return 0
	}
	x0 := cardCX - half
	if x0 < 0 {
		x0 = 0
	}
	x1 := cardCX + half
	if x1 > screen.Cols() {
		x1 = screen.Cols()
	}
	if x1-x0 < 4 {
		return 0
	}
	greenCols := 0
	for x := x0; x < x1; x++ {
		for y := top; y < bot; y++ {
			b := screen.GetUCharAt(y, x*3)
			g := screen.GetUCharAt(y, x*3+1)
			r := screen.GetUCharAt(y, x*3+2)
			if g > 110 && int(g)-int(r) > 25 && int(g)-int(b) > 25 {
				greenCols++
				break
			}
		}
	}
	return float64(greenCols) / float64(x1-x0)
}

// WatchedHero is one deployed hero whose ability tap is still unspent.
type WatchedHero struct {
	X          int // card centre x (live px)
	SlotY      int // card centre y (live px)
	Slot       *TrackedSlot
	Warden     bool
	DeployedAt time.Time
	activated  bool
	dead       int // consecutive ~zero reads
	Done       bool
}

// HeroHPMonitor polls watched heroes' HP strips and fires each ability once:
// on low HP, or proactively for the warden shortly after deploy.
type HeroHPMonitor struct {
	heroes []*WatchedHero
}

func NewHeroHPMonitor(heroes []*WatchedHero) *HeroHPMonitor {
	return &HeroHPMonitor{heroes: heroes}
}

// AllDone reports every watched hero resolved (ability fired or confirmed dead).
func (m *HeroHPMonitor) AllDone() bool {
	if m == nil {
		return true
	}
	for _, h := range m.heroes {
		if !h.Done {
			return false
		}
	}
	return true
}

// Poll reads each unresolved hero once against screen and taps for the
// ability when due. tap receives the hero (X/SlotY card point, Warden flag
// for the card's Y offset); the caller owns transport.
func (m *HeroHPMonitor) Poll(screen gocv.Mat, cal *game.Calibration, now time.Time, tap func(h *WatchedHero) bool) {
	if m == nil || m.AllDone() {
		return
	}
	for _, h := range m.heroes {
		if h.Done || h.activated {
			h.Done = h.Done || h.activated
			continue
		}
		// Proactive warden: its bar reads full until death, so fire once
		// shortly after deploy instead of waiting for a low read.
		if h.Warden && !now.Before(h.DeployedAt.Add(heroWardenDelay)) {
			if tap != nil && tap(h) {
				h.activated, h.Done = true, true
			}
			continue
		}
		frac := HeroHPFraction(screen, h.X, h.SlotY, cal)
		if frac < heroHPMinAlive {
			h.dead++
			if h.dead >= heroHPDeadStrikes {
				h.Done = true
			}
			continue
		}
		h.dead = 0
		if frac >= heroHPThreshold {
			continue
		}
		if tap != nil && tap(h) {
			h.activated, h.Done = true, true
		}
	}
}

func isWardenName(name string) bool {
	return strings.Contains(strings.ToLower(name), "warden")
}

// Track adds a deployed hero that still holds its ability tap (SpotTaps <
// maxHeroSpotTaps: one tap places, one more fires the ability) so the battle
// watcher can fire it on low HP. Heroes whose budget is spent, fallback-
// labeled bonus troops, non-hero slots, and already-tracked (X, SlotY)
// positions are ignored.
func (m *HeroHPMonitor) Track(slot *TrackedSlot) {
	if m == nil || slot == nil || slot.Category != "Hero" || slot.FallbackLabeled || slot.State != SlotDeployed || slot.SpotTaps >= maxHeroSpotTaps {
		return
	}
	for _, h := range m.heroes {
		if h.X == slot.X && h.SlotY == slot.Y {
			return
		}
	}
	at := slot.DeployedAt
	if at.IsZero() {
		at = time.Now()
	}
	m.heroes = append(m.heroes, &WatchedHero{
		X: slot.X, SlotY: slot.Y, Slot: slot, Warden: isWardenName(slot.UnitName), DeployedAt: at,
	})
}

// clearHeroWatch drops any watch from a previous battle. Called at deploy start.
func (e *Executor) clearHeroWatch() {
	e.heroWatchMu.Lock()
	e.heroWatch = nil
	e.heroWatchMu.Unlock()
}

// armHeroWatch tracks every hero the slot manager has confirmed deployed so
// far, so an ability becomes eligible the moment the hero lands instead of at
// the end of the whole deploy. Safe to call repeatedly.
func (e *Executor) armHeroWatch(sm *SlotManager) int {
	if sm == nil {
		return 0
	}
	e.heroWatchMu.Lock()
	defer e.heroWatchMu.Unlock()
	if e.heroWatch == nil {
		e.heroWatch = NewHeroHPMonitor(nil)
	}
	for _, slot := range sm.slots {
		e.heroWatch.Track(slot)
	}
	watched := 0
	if e.heroWatch != nil {
		watched = len(e.heroWatch.heroes)
	}
	if watched == 0 {
		e.heroWatch = nil
	}
	return watched
}

// pollHeroWatch runs one HP check on an already-captured frame. Costs no extra
// capture: callers pass the frame they already own (the battle-end tick, or
// the deployment watcher's fresh frame).
func (e *Executor) pollHeroWatch(frame gocv.Mat) {
	e.heroWatchMu.Lock()
	defer e.heroWatchMu.Unlock()
	if e.heroWatch == nil {
		return
	}
	e.heroWatch.Poll(frame, e.cal, time.Now(), e.fireHeroAbility)
	if e.heroWatch.AllDone() {
		e.heroWatch = nil
	}
}

// fireHeroAbility fires exactly one hero ability tap for the HP monitor.
// Returns true only when the tap actually reached the device, so an
// interrupted or failed tap is retried on the next frame instead of being
// marked done.
func (e *Executor) fireHeroAbility(h *WatchedHero) bool {
	if h == nil {
		return false
	}
	// Stop or confirmed battle end: the client guard would reject the tap
	// anyway; skipping here avoids logging a guaranteed failure.
	if err := e.deploymentErr(); err != nil {
		return false
	}
	if h.Slot != nil {
		if h.Slot.SpotTaps >= maxHeroSpotTaps {
			// The ability already fired through another path (explicit
			// ability phase or the sweep); never tap a third time.
			return false
		}
		h.Slot.SpotTaps++
	}
	y := h.SlotY
	if h.Warden {
		// Same card-tap point as the deploy path (fireSlotTap): the warden's
		// icon sits above the card centre.
		y -= int(e.cal.Length(25))
	}
	if err := e.client.TapFast(h.X, y, 4.0); err != nil {
		e.logger.Warn().Err(err).Int("x", h.X).Msg("hero ability tap failed; will retry next frame")
		if h.Slot != nil {
			h.Slot.SpotTaps-- // nothing was delivered; keep the budget honest
		}
		return false
	}
	if e.deployCtxIsActive() {
		// Mid-deploy: the ability's card tap re-selects the hero card. Put
		// the deploy's own troop/spell selection back so the next field tap
		// does not fire with a hero selected (valk_run6 failure mode).
		if e.tapExec != nil {
			e.client.HumanSleep(120, 25)
			if !e.tapExec.RestoreSelection() {
				e.logger.Warn().Msg("could not restore troop-bar selection after a mid-deploy hero ability")
				return false
			}
		}
		e.logger.Info().Int("x", h.X).Msg("hero ability fired mid-deploy; prior bar selection restored")
	} else {
		e.logger.Info().Int("x", h.X).Msg("hero ability fired on low HP")
	}
	return true
}
