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
	heroHPThreshold  = 0.30 // fire ability below 30% HP
	heroHPMinAlive   = 0.03 // below this the bar is gone: dead or undeployed
	heroHPDeadStrikes = 2   // confirm death over 2 ticks (one 0.0 is a stray frame)
	heroWardenDelay  = 4 * time.Second // warden HP reads full till death: fire proactively

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
func (m *HeroHPMonitor) Poll(screen gocv.Mat, cal *game.Calibration, now time.Time, tap func(h *WatchedHero)) {
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
			tap(h)
			h.activated, h.Done = true, true
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
		tap(h)
		h.activated, h.Done = true, true
	}
}

func isWardenName(name string) bool {
	return strings.Contains(strings.ToLower(name), "warden")
}

// snapshotHeroWatch collects deployed heroes that still hold their ability tap
// (SpotTaps < maxHeroSpotTaps: one tap places, one more fires the ability) so
// the battle-end wait can fire them on low HP. Heroes whose budget is spent,
// fallback-labeled bonus troops, and empty snapshots leave no watch.
func (e *Executor) snapshotHeroWatch(sm *SlotManager) {
	e.heroWatch = nil
	if sm == nil {
		return
	}
	var heroes []*WatchedHero
	for _, slot := range sm.slots {
		if slot == nil || slot.Category != "Hero" || slot.FallbackLabeled {
			continue
		}
		if slot.State != SlotDeployed || slot.SpotTaps >= maxHeroSpotTaps {
			continue
		}
		at := slot.DeployedAt
		if at.IsZero() {
			at = time.Now()
		}
		heroes = append(heroes, &WatchedHero{
			X:          slot.X,
			SlotY:      slot.Y,
			Warden:     isWardenName(slot.UnitName),
			DeployedAt: at,
		})
	}
	if len(heroes) > 0 {
		e.heroWatch = NewHeroHPMonitor(heroes)
		e.logger.Info().Int("watched_heroes", len(heroes)).Msg("hero HP watch armed for the battle-end wait")
	}
}
