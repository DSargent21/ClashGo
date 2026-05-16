package attack

import (
	"fmt"
	"image"
	"math"
	"sort"
	"strings"
	"time"

	"gocv.io/x/gocv"

	"github.com/Ducky705/ClashGo/internal/adb"
	"github.com/Ducky705/ClashGo/internal/config"
	"github.com/Ducky705/ClashGo/internal/game"
	"github.com/Ducky705/ClashGo/internal/vision"
	"github.com/Ducky705/ClashGo/pkg/strategy"
	"github.com/rs/zerolog"
)

type AttackResult struct {
	Stars       int
	PercentDest float64
	GoldLoot    int
	ElixirLoot  int
	DELoot      int
	Trophies    int
	Duration    time.Duration
}

type DeployEntry struct {
	Slot   int
	Count  int
	X, Y   int
	Delay  time.Duration
}

type AttackPlan struct {
	Strategy  strategy.AttackStrategy
	DropOrder []DeployEntry
}

type Executor struct {
	client   *adb.Client
	cal      *game.Calibration
	cfg      *config.AttackConfig
	classify func(gocv.Mat) (game.GameState, int)
	logger   zerolog.Logger
}

func NewExecutor(client *adb.Client, cal *game.Calibration, cfg *config.AttackConfig, logger zerolog.Logger) *Executor {
	return &Executor{
		client: client,
		cal:    cal,
		cfg:    cfg,
		logger: logger.With().Str("component", "attack_executor").Logger(),
	}
}

func (e *Executor) SetClassifier(fn func(gocv.Mat) (game.GameState, int)) {
	e.classify = fn
}

func (e *Executor) LoadStrategy(path string) (*AttackPlan, error) {
	s, err := strategy.ParseCSV(path)
	if err != nil {
		return nil, fmt.Errorf("parse strategy: %w", err)
	}

	plan := &AttackPlan{
		Strategy:  *s,
		DropOrder: e.buildDropOrder(s),
	}

	return plan, nil
}

func (e *Executor) buildDropOrder(s *strategy.AttackStrategy) []DeployEntry {
	var entries []DeployEntry

	troopOrder := e.troopSlotOrder()
	slotIdx := 0

	for _, drop := range s.DropOrders {
		if drop.Troop == "" || drop.Quantity <= 0 {
			continue
		}

		slot := slotIdx
		for i, t := range troopOrder {
			if t == drop.Troop {
				slot = i
				break
			}
		}

		entries = append(entries, DeployEntry{
			Slot:   slot,
			Count:  drop.Quantity,
			X:      drop.X,
			Y:      drop.Y,
			Delay:  e.cfg.DropDelay.Duration,
		})

		slotIdx++
	}

	return entries
}

func (e *Executor) troopSlotOrder() []string {
	return []string{
		"Barbarian", "Archer", "Goblin", "Giant",
		"WallBreaker", "Balloon", "Wizard", "Healer",
		"Dragon", "Pekka", "Minion", "HogRider",
		"Valkyrie", "Golem", "Witch", "LavaHound",
		"Bowler", "Miner", "ElectroDragon", "Yeti",
		"DragonRider", "ElectroTitan", "RootRider",
	}
}

func (e *Executor) SelectTroop(slot int) error {
	sx, sy := e.slotPosition(slot)
	return e.client.Tap(sx, sy)
}

func (e *Executor) slotPosition(slot int) (int, int) {
	baseX := e.cal.ScaleXRef(50)
	baseY := e.cal.ScaleYRef(660)
	slotW := e.cal.ScaleXRef(50)
	slotH := e.cal.ScaleYRef(60)

	x := baseX + (slot % 5) * slotW
	y := baseY
	if slot >= 5 {
		y += slotH
	}

	return x, y
}

func (e *Executor) DropTroop(x, y int, count int) error {
	for i := 0; i < count; i++ {
		sx, sy := e.cal.ScaleRef(x, y)
		if err := e.client.Tap(sx, sy); err != nil {
			return err
		}

		dropDelay := e.cfg.DropDelay.Duration
		if i < count-1 {
			time.Sleep(dropDelay)
		}
	}
	return nil
}

func (e *Executor) DeploySquad(slot int, entries []DeployEntry) error {
	if err := e.SelectTroop(slot); err != nil {
		return fmt.Errorf("select troop slot %d: %w", slot, err)
	}

	time.Sleep(200 * time.Millisecond)

	for _, entry := range entries {
		if entry.Slot != slot {
			continue
		}
		if err := e.DropTroop(entry.X, entry.Y, entry.Count); err != nil {
			return fmt.Errorf("drop troop %d at (%d,%d): %w", slot, entry.X, entry.Y, err)
		}
		time.Sleep(entry.Delay)
	}

	return nil
}

func (e *Executor) CastSpell(spellSlot int, x, y int) error {
	sx, sy := e.spellPosition(spellSlot)
	if err := e.client.Tap(sx, sy); err != nil {
		return err
	}
	time.Sleep(300 * time.Millisecond)

	sx2, sy2 := e.cal.ScaleRef(x, y)
	return e.client.Tap(sx2, sy2)
}

func (e *Executor) spellPosition(slot int) (int, int) {
	x := e.cal.ScaleXRef(300 + slot*45)
	y := e.cal.ScaleYRef(660)
	return x, y
}

func (e *Executor) ActivateQueen(pct int) error {
	px := e.cal.ScaleXRef(553)
	py := e.cal.ScaleYRef(204)
	return e.client.Tap(px, py)
}

func (e *Executor) ActivateWarden(pct int) error {
	px := e.cal.ScaleXRef(583)
	py := e.cal.ScaleYRef(204)
	return e.client.Tap(px, py)
}

func (e *Executor) DeployClanCastle(x, y int) error {
	sx, sy := e.cal.ScaleRef(x, y)
	return e.client.Tap(sx, sy)
}

func (e *Executor) EndBattle() error {
	ex, ey := e.cal.ScaleRef(34, 558)
	if err := e.client.Tap(ex, ey); err != nil {
		return err
	}
	time.Sleep(3 * time.Second)
	return nil
}

func (e *Executor) ReturnHome() error {
	hx, hy := e.cal.ScaleRef(290, 576)
	if err := e.client.Tap(hx, hy); err != nil {
		return err
	}
	time.Sleep(5 * time.Second)

	screen, err := e.client.CaptureToMat()
	if err != nil {
		return err
	}
	defer screen.Close()

	state, _ := e.classify(screen)
	if state != game.StateMainVillage {
		return fmt.Errorf("did not return to main village (state=%s)", state)
	}

	return nil
}

func (e *Executor) IsArmyFull(screen gocv.Mat) bool {
	fullBar := e.cal.ScaleRefRect(30, 650, 430, 680)
	if fullBar.Min.X < 0 || fullBar.Max.X > screen.Cols() {
		return false
	}

	region := screen.Region(fullBar)
	defer region.Close()

	gray := gocv.NewMat()
	gocv.CvtColor(region, &gray, gocv.ColorBGRToGray)
	defer gray.Close()

	_, maxVal, _, _ := gocv.MinMaxLoc(gray)
	return maxVal > 200
}

func (e *Executor) AnalyzeRedArea(screen gocv.Mat) ([]DropPoint, error) {
	minArea := int(float64(screen.Cols()*screen.Rows()) * 0.001)
	pts, err := vision.FindRedArea(screen, minArea)
	if err != nil {
		return nil, err
	}

	var drops []DropPoint
	for _, pt := range pts {
		sx, sy := e.cal.ScaleRef(pt.X, pt.Y)
		drops = append(drops, DropPoint{X: sx, Y: sy, Weight: 1})
	}

	return drops, nil
}

type DropPoint struct {
	X, Y   int
	Weight float64
}

func (e *Executor) ReadBattleResult(screen gocv.Mat) (AttackResult, error) {
	var result AttackResult

	starY := e.cal.ScaleYRef(548)
	// Star positions in 860x732 reference coordinates, scaled to physical via ScaleXRef
	starPositions := []int{481, 498, 514}

	for _, sx := range starPositions {
		px := e.cal.ScaleXRef(sx)
		py := starY
		if px < 0 || py < 0 || px >= screen.Cols() || py >= screen.Rows() {
			continue
		}

		b := screen.GetUCharAt(py, px*3)
		g := screen.GetUCharAt(py, px*3+1)
		r := screen.GetUCharAt(py, px*3+2)

		if int(r) > 150 && int(g) > 150 && int(b) < 100 {
			result.Stars++
		}
	}

	return result, nil
}

func (e *Executor) WaitForBattleEnd(timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			screen, err := e.client.CaptureToMat()
			if err != nil {
				continue
			}
			defer screen.Close()

			state, _ := e.classify(screen)
			if state == game.StateBattleEnd {
				return true
			}

			if time.Now().After(deadline) {
				return false
			}
		}
	}
}

func (e *Executor) DeployAll(plan *AttackPlan, screen gocv.Mat, redPts []DropPoint) error {
	grouped := make(map[int][]DeployEntry)
	for _, entry := range plan.DropOrder {
		grouped[entry.Slot] = append(grouped[entry.Slot], entry)
	}

	var slots []int
	for slot := range grouped {
		slots = append(slots, slot)
	}
	sort.Ints(slots)

	for _, slot := range slots {
		entries := grouped[slot]
		if err := e.DeploySquad(slot, entries); err != nil {
			return err
		}
		time.Sleep(500 * time.Millisecond)
	}

	return nil
}

func (e *Executor) DeployDynamic(s *strategy.DynamicStrategy, screen gocv.Mat) error {
	w, h := screen.Cols(), screen.Rows()

	for _, phase := range s.Phases {
		e.logger.Info().Str("phase", phase.Name).Msg("starting attack phase")

		for _, unit := range phase.Units {
			// Find the unit on the bar dynamically
			fileName := strings.ToLower(strings.ReplaceAll(unit.Name, " ", "_"))
			tplPath := fmt.Sprintf("assets/templates/attack/%s.png", fileName)
			tpl := gocv.IMRead(tplPath, gocv.IMReadColor)
			if tpl.Empty() {
				e.logger.Warn().Str("unit", unit.Name).Str("path", tplPath).Msg("unit template not found")
				continue
			}
			defer tpl.Close()

			matches, _ := vision.MatchTemplate(screen, tpl, 0.7)
			if len(matches) == 0 {
				e.logger.Warn().Str("unit", unit.Name).Msg("unit not found on deployment bar")
				continue
			}

			// Select the unit
			uPt := matches[0].Point
			e.logger.Debug().Str("unit", unit.Name).Interface("point", uPt).Msg("selecting unit")
			if err := e.client.Tap(uPt.X, uPt.Y); err != nil {
				return err
			}
			time.Sleep(200 * time.Millisecond)

			// Calculate deployment points/lines using Screen Edges
			p1, p2 := e.calculateDeploymentAreaEdge(s.TargetEdge, phase.Offset, w, h)

			switch phase.Pattern {
			case "Line":
				duration := 600
				e.logger.Debug().Str("unit", unit.Name).Interface("from", p1).Interface("to", p2).Msg("deploying in line")
				if err := e.client.Swipe(p1.X, p1.Y, p2.X, p2.Y, duration); err != nil {
					return err
				}
			case "Point":
				mid := image.Point{X: (p1.X + p2.X) / 2, Y: (p1.Y + p2.Y) / 2}
				e.logger.Debug().Str("unit", unit.Name).Interface("point", mid).Msg("deploying at point")
				if err := e.client.Tap(mid.X, mid.Y); err != nil {
					return err
				}
			}
		}

		if phase.DelayAfterMS > 0 {
			time.Sleep(time.Duration(phase.DelayAfterMS) * time.Millisecond)
		}
	}

	return nil
}

func (e *Executor) calculateDeploymentAreaEdge(edge string, offset int, w, h int) (image.Point, image.Point) {
	// Professional Margin: Use 2% of screen width/height for safety
	safeX := int(float64(w) * 0.02)
	safeY := int(float64(h) * 0.02)
	
	// Deployment Bar Margin: Use 15% of screen height
	barY := int(float64(h) * 0.85)

	var p1, p2 image.Point

	switch edge {
	case "Top":
		p1 = image.Pt(safeX, safeY+offset)
		p2 = image.Pt(w-safeX, safeY+offset)
	case "Bottom":
		p1 = image.Pt(safeX, barY-offset)
		p2 = image.Pt(w-safeX, barY-offset)
	case "Left":
		p1 = image.Pt(safeX+offset, safeY)
		p2 = image.Pt(safeX+offset, barY)
	case "Right":
		p1 = image.Pt(w-safeX-offset, safeY)
		p2 = image.Pt(w-safeX-offset, barY)
	case "TopRight":
		// From top-middle to right-middle
		p1 = image.Pt(w/2, safeY+offset)
		p2 = image.Pt(w-safeX-offset, h/2)
	case "TopLeft":
		p1 = image.Pt(safeX+offset, h/2)
		p2 = image.Pt(w/2, safeY+offset)
	case "BottomRight":
		p1 = image.Pt(w/2, barY-offset)
		p2 = image.Pt(w-safeX-offset, h/2)
	case "BottomLeft":
		p1 = image.Pt(safeX+offset, h/2)
		p2 = image.Pt(w/2, barY-offset)
	default:
		p1 = image.Pt(w/2, safeY+offset)
		p2 = image.Pt(w-safeX-offset, h/2)
	}

	return p1, p2
}


func (e *Executor) findTroopSlot(name string) int {
	order := e.troopSlotOrder()
	for i, t := range order {
		if strings.EqualFold(t, name) {
			return i
		}
	}
	// For heroes, spells, etc., we'll need a more robust mapping later
	// but for now, let's just use simple mapping
	if strings.Contains(strings.ToLower(name), "king") { return 10 } // placeholder
	if strings.Contains(strings.ToLower(name), "queen") { return 11 }
	if strings.Contains(strings.ToLower(name), "warden") { return 12 }
	
	return -1
}

func (e *Executor) calculateDeploymentArea(edge string, offset int, top, bottom, left, right image.Point) (image.Point, image.Point) {
	var p1, p2 image.Point
	var center image.Point

	// Calculate a rough center of the base to determine "outwards" direction
	center = image.Point{
		X: (left.X + right.X) / 2,
		Y: (top.Y + bottom.Y) / 2,
	}

	switch edge {
	case "TopRight":
		p1, p2 = top, right
	case "BottomRight":
		p1, p2 = right, bottom
	case "BottomLeft":
		p1, p2 = bottom, left
	case "TopLeft":
		p1, p2 = left, top
	default:
		p1, p2 = top, right
	}

	// Calculate direction vector along the edge
	dx := float64(p2.X - p1.X)
	dy := float64(p2.Y - p1.Y)
	mag := math.Sqrt(dx*dx + dy*dy)
	
	if mag == 0 {
		return p1, p2
	}

	// Normal vector (perpendicular to the edge)
	nx := dy / mag
	ny := -dx / mag

	// IMPORTANT: Ensure the normal points AWAY from the center of the base
	// Test a point shifted by the normal
	testPt := image.Point{
		X: (p1.X+p2.X)/2 + int(nx*10),
		Y: (p1.Y+p2.Y)/2 + int(ny*10),
	}

	distToCenterOrig := math.Sqrt(math.Pow(float64((p1.X+p2.X)/2-center.X), 2) + math.Pow(float64((p1.Y+p2.Y)/2-center.Y), 2))
	distToCenterNew := math.Sqrt(math.Pow(float64(testPt.X-center.X), 2) + math.Pow(float64(testPt.Y-center.Y), 2))

	// If the new point is closer to center, flip the normal
	if distToCenterNew < distToCenterOrig {
		nx = -nx
		ny = -ny
	}

	// Apply offset
	p1.X += int(nx * float64(offset))
	p1.Y += int(ny * float64(offset))
	p2.X += int(nx * float64(offset))
	p2.Y += int(ny * float64(offset))

	return p1, p2
}

func (e *Executor) logPhase(phase strategy.Phase) {
	e.logger.Info().Str("phase", phase.Name).Msg("attack phase")
}