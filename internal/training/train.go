package training

import (
	"fmt"
	"image"
	"sync"
	"time"

	"gocv.io/x/gocv"

	"github.com/Ducky705/ClashGo/internal/config"
	"github.com/Ducky705/ClashGo/internal/game"
	"github.com/Ducky705/ClashGo/internal/vision"
	"github.com/rs/zerolog"
)

type TroopCount struct {
	Name  string
	Count int
	Max   int
}

type ArmyStatus struct {
	Full        bool
	TotalSlots  int
	UsedSlots   int
	Troops      []TroopCount
	CCFull      bool
	SpellsReady int
	LastUpdated time.Time
}

type QueueEntry struct {
	Troop   string
	Count   int
	Trained int
}

type Trainer struct {
	client    game.Device
	cal       *game.Calibration
	cfg       *config.TrainingConfig
	classify  func(gocv.Mat) (game.GameState, int)
	templates *game.TemplateStore
	logger    zerolog.Logger
	mu        sync.RWMutex
}

func NewTrainer(client game.Device, cal *game.Calibration, cfg *config.TrainingConfig, logger zerolog.Logger) *Trainer {
	return &Trainer{
		client: client,
		cal:    cal,
		cfg:    cfg,
		logger: logger.With().Str("component", "trainer").Logger(),
	}
}

func (t *Trainer) SetClassifier(fn func(gocv.Mat) (game.GameState, int)) {
	t.classify = fn
}

func (t *Trainer) SetTemplates(ts *game.TemplateStore) {
	t.templates = ts
}

func (t *Trainer) IdentifyTroops(screen gocv.Mat) ([]TroopCount, error) {
	if t.templates == nil {
		return nil, fmt.Errorf("templates not loaded")
	}

	// Army overview region in Army Camp tab
	// Typically horizontal icons from X=60 to X=800, Y=200 to Y=400
	// We'll scan a slightly larger area and use template matching
	armyRegion := t.cal.ScaleRefRect(60, 200, 800, 450)
	if !t.cal.IsRectInBounds(armyRegion) {
		return nil, fmt.Errorf("army region outside screen bounds")
	}

	regionMat := screen.Region(armyRegion)
	defer regionMat.Close()

	var detected []TroopCount
	
	// List all templates in the 'troops' category (assuming naming convention or category filter)
	// For now, we'll look for templates with "troop_" prefix or in a sub-store
	// To simplify, let's assume any template starting with "troop_" is a troop icon
	allNames := t.templates.List(game.StateArmyCamp)
	
	for _, name := range allNames {
		// Only process troop templates
		// For this implementation, we assume the user provides templates named "Barbarian", "Archer", etc.
		tpl, ok := t.templates.Get(name)
		if !ok {
			continue
		}

		// Match template in the army region
		matches, err := vision.MatchTemplate(regionMat, tpl, 0.8)
		if err == nil && len(matches) > 0 {
			// Found the troop!
			// In a real bot, we'd also read the number below the icon
			detected = append(detected, TroopCount{
				Name:  name,
				Count: len(matches), // This is a placeholder, should read text
			})
		}
	}

	return detected, nil
}

func (t *Trainer) VerifyArmyComposition(screen gocv.Mat, target []QueueEntry) (bool, string) {
	detected, err := t.IdentifyTroops(screen)
	if err != nil {
		return false, fmt.Sprintf("failed to identify troops: %v", err)
	}

	for _, req := range target {
		found := false
		for _, det := range detected {
			if det.Name == req.Troop {
				found = true
				break
			}
		}
		if !found {
			return false, fmt.Sprintf("missing required troop: %s", req.Troop)
		}
	}

	return true, "army composition verified"
}

func (t *Trainer) ReadArmyStatus(screen gocv.Mat) (ArmyStatus, error) {
	var status ArmyStatus
	status.Troops = []TroopCount{}

	barY := t.cal.ScaleYRef(680)
	barMinX := t.cal.ScaleXRef(30)
	barMaxX := t.cal.ScaleXRef(430)

	if barMinX < 0 || barMaxX > screen.Cols() || barY < 0 || barY >= screen.Rows() {
		return status, fmt.Errorf("army bar outside screen bounds")
	}

	filledWidth := t.measureFilledBar(screen, image.Rect(barMinX, barY, barMaxX, barY+30))
	totalWidth := barMaxX - barMinX
	if totalWidth > 0 {
		status.Full = float64(filledWidth)/float64(totalWidth) > 0.95
	}

	status.UsedSlots, status.TotalSlots = t.countArmySlots(screen)

	return status, nil
}

func (t *Trainer) measureFilledBar(screen gocv.Mat, r image.Rectangle) int {
	if r.Min.X < 0 || r.Max.X > screen.Cols() || r.Min.Y < 0 || r.Max.Y > screen.Rows() {
		return 0
	}

	region := screen.Region(r)
	defer region.Close()

	gray := gocv.NewMat()
	gocv.CvtColor(region, &gray, gocv.ColorBGRToGray)
	defer gray.Close()

	_, maxVal, maxLoc, _ := gocv.MinMaxLoc(gray)
	_ = maxVal
	return maxLoc.X
}

func (t *Trainer) countArmySlots(screen gocv.Mat) (used, total int) {
	armyBarY := t.cal.ScaleYRef(665)
	armyBarX1 := t.cal.ScaleXRef(40)
	armyBarX2 := t.cal.ScaleXRef(410)

	r := image.Rect(armyBarX1, armyBarY, armyBarX2, armyBarY+50)
	if r.Min.X < 0 || r.Max.X > screen.Cols() || r.Min.Y < 0 || r.Max.Y > screen.Rows() {
		return 0, 260
	}

	region := screen.Region(r)
	defer region.Close()

	gray := gocv.NewMat()
	gocv.CvtColor(region, &gray, gocv.ColorBGRToGray)
	defer gray.Close()

	thresh := gocv.NewMat()
	gocv.Threshold(gray, &thresh, 100, 255, gocv.ThresholdBinary)
	defer thresh.Close()

	row := make([]int, thresh.Cols())
	for y := 0; y < thresh.Rows(); y++ {
		for x := 0; x < thresh.Cols(); x++ {
			v := thresh.GetUCharAt(y, x)
			if v > 0 {
				row[x]++
			}
		}
	}

	transitions := 0
	for x := 1; x < len(row); x++ {
		if row[x] > 0 && row[x-1] == 0 {
			transitions++
		}
	}

	used = transitions / 2
	total = 260
	if used > total {
		used = total
	}

	return used, total
}

func (t *Trainer) IsArmyFull(screen gocv.Mat) bool {
	status, err := t.ReadArmyStatus(screen)
	if err != nil {
		return false
	}
	return status.Full
}

func (t *Trainer) IsArmyFullPercent(screen gocv.Mat) float64 {
	status, err := t.ReadArmyStatus(screen)
	if err != nil {
		return 0
	}
	if status.TotalSlots == 0 {
		return 0
	}
	return float64(status.UsedSlots) / float64(status.TotalSlots)
}

func (t *Trainer) OpenTrainArmy(screen gocv.Mat) (game.GameState, error) {
	state, _ := t.classify(screen)
	if state != game.StateMainVillage && state != game.StateArmyCamp {
		return state, fmt.Errorf("not on village or army camp screen (state=%s)", state)
	}

	if state == game.StateArmyCamp {
		return state, nil
	}

	btnX, btnY := t.cal.ScaleRef(40, 525)

	if err := t.client.Tap(btnX, btnY); err != nil {
		return state, fmt.Errorf("tap army button: %w", err)
	}

	time.Sleep(2 * time.Second)

	screen2, err := t.client.CaptureToMat()
	if err != nil {
		return state, err
	}
	defer screen2.Close()

	state, _ = t.classify(screen2)
	return state, nil
}

func (t *Trainer) SelectArmy1(screen gocv.Mat) error {
	// Replaced by step-by-step sequence in bot.go
	return nil
}

func (t *Trainer) ClickBattle(screen gocv.Mat) error {
	// Replaced by step-by-step sequence in bot.go
	return nil
}

func (t *Trainer) TrainTroop(troop string, count int) error {
	troopSlots := map[string]int{
		"Barbarian": 0, "Archer": 1, "Goblin": 2, "Giant": 3,
		"WallBreaker": 4, "Balloon": 5, "Wizard": 6, "Healer": 7,
		"Dragon": 8, "Pekka": 9, "Minion": 10, "HogRider": 11,
		"Valkyrie": 12, "Golem": 13, "Witch": 14, "LavaHound": 15,
	}

	slot, ok := troopSlots[troop]
	if !ok {
		return fmt.Errorf("unknown troop: %s", troop)
	}

	barX := t.cal.ScaleXRef(50 + slot*50)
	barY := t.cal.ScaleYRef(100)
	if err := t.client.Tap(barX, barY); err != nil {
		return fmt.Errorf("select troop %s: %w", troop, err)
	}

	time.Sleep(300 * time.Millisecond)

	for i := 0; i < count; i++ {
		addX, addY := t.cal.ScaleRef(280, 200)
		if err := t.client.Tap(addX, addY); err != nil {
			return fmt.Errorf("add troop count: %w", err)
		}
		time.Sleep(50 * time.Millisecond)
	}

	trainX, trainY := t.cal.ScaleRef(430, 550)
	return t.client.Tap(trainX, trainY)
}

func (t *Trainer) TrainFromQueue(queue []QueueEntry) error {
	for _, entry := range queue {
		if err := t.TrainTroop(entry.Troop, entry.Count); err != nil {
			return err
		}
		time.Sleep(t.cfg.SleepAfterTrain.Duration)
	}
	return nil
}

func (t *Trainer) WaitForFullArmy(screen gocv.Mat, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			s, err := t.client.CaptureToMat()
			if err != nil {
				continue
			}
			defer s.Close()

			if t.IsArmyFull(s) {
				return true
			}

			if time.Now().After(deadline) {
				return false
			}
		}
	}
}

func (t *Trainer) ReadResources(screen gocv.Mat) game.Resources {
	var r game.Resources

	goldRect := t.cal.ScaleRefRect(50, 15, 150, 45)
	if goldRect.Min.X >= 0 && goldRect.Max.X <= screen.Cols() {
		r.Gold = t.readNumber(screen, goldRect)
	}

	elixirRect := t.cal.ScaleRefRect(180, 15, 280, 45)
	if elixirRect.Min.X >= 0 && elixirRect.Max.X <= screen.Cols() {
		r.Elixir = t.readNumber(screen, elixirRect)
	}

	deRect := t.cal.ScaleRefRect(310, 15, 410, 45)
	if deRect.Min.X >= 0 && deRect.Max.X <= screen.Cols() {
		r.DarkElixir = t.readNumber(screen, deRect)
	}

	return r
}

func (t *Trainer) readNumber(screen gocv.Mat, r image.Rectangle) int {
	if r.Min.X < 0 || r.Max.X > screen.Cols() || r.Min.Y < 0 || r.Max.Y > screen.Rows() {
		return 0
	}

	region := screen.Region(r)
	defer region.Close()

	gray := gocv.NewMat()
	gocv.CvtColor(region, &gray, gocv.ColorBGRToGray)
	defer gray.Close()

	thresh := gocv.NewMat()
	gocv.Threshold(gray, &thresh, 120, 255, gocv.ThresholdBinary)
	defer thresh.Close()

	totalDark := 0
	for y := 0; y < thresh.Rows(); y++ {
		for x := 0; x < thresh.Cols(); x++ {
			if thresh.GetUCharAt(y, x) > 0 {
				totalDark++
			}
		}
	}

	avgDarkPerRow := float64(totalDark) / float64(thresh.Rows())
	avgCharWidth := 15.0
	chars := int(float64(thresh.Cols()) / avgCharWidth)

	estimated := int(avgDarkPerRow * float64(chars) * 2.5)
	return estimated
}

func (t *Trainer) GetTrainTime() time.Duration {
	return t.cfg.SleepAfterTrain.Duration
}

func DefaultTrainQueue() []QueueEntry {
	return []QueueEntry{
		{Troop: "Giant", Count: 6},
		{Troop: "Archer", Count: 80},
		{Troop: "WallBreaker", Count: 10},
		{Troop: "Dragon", Count: 4},
	}
}