package attack

import (
	"fmt"
	"sort"
	"time"

	"gocv.io/x/gocv"

	"github.com/diegosargent/coc-bot/internal/adb"
	"github.com/diegosargent/coc-bot/internal/config"
	"github.com/diegosargent/coc-bot/internal/game"
	"github.com/diegosargent/coc-bot/internal/vision"
	"github.com/diegosargent/coc-bot/pkg/strategy"
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
	Strategy strategy.AttackStrategy
	DropOrder []DeployEntry
}

type Executor struct {
	client   *adb.Client
	cal      *game.Calibration
	cfg      *config.AttackConfig
	classify func(gocv.Mat) (game.GameState, int)
}

func NewExecutor(client *adb.Client, cal *game.Calibration, cfg *config.AttackConfig) *Executor {
	return &Executor{
		client: client,
		cal:    cal,
		cfg:    cfg,
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
		"Bowler", "Miner",
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
	px := e.cal.ScaleXRef(820)
	py := e.cal.ScaleYRef(200)
	return e.client.Tap(px, py)
}

func (e *Executor) ActivateWarden(pct int) error {
	px := e.cal.ScaleXRef(865)
	py := e.cal.ScaleYRef(200)
	return e.client.Tap(px, py)
}

func (e *Executor) DeployClanCastle(x, y int) error {
	sx, sy := e.cal.ScaleRef(x, y)
	return e.client.Tap(sx, sy)
}

func (e *Executor) EndBattle() error {
	ex, ey := e.cal.ScaleRef(50, 548)
	if err := e.client.Tap(ex, ey); err != nil {
		return err
	}
	time.Sleep(3 * time.Second)
	return nil
}

func (e *Executor) ReturnHome() error {
	hx, hy := e.cal.ScaleRef(430, 566)
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

	starY := e.cal.ScaleYRef(538)
	starPositions := []int{714, 739, 763}

	for _, sx := range starPositions {
		px, py := e.cal.ScaleRef(sx, starY)
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