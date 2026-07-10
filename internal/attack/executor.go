package attack

import (
	"image"
	"math/rand"
	"strings"
	"time"

	"github.com/Ducky705/ClashGO/internal/adb"
	"github.com/Ducky705/ClashGO/internal/game"
	"github.com/rs/zerolog"
	"gocv.io/x/gocv"
)

// TapExecutor handles all tap operations, screen capture, and timing.
type TapExecutor struct {
	client *adb.Client
	cal    *game.Calibration
	logger zerolog.Logger
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

// TapSlot selects a slot with jitter for human-like behavior.
func (t *TapExecutor) TapSlot(slot *TrackedSlot, jitterPx int) {
	// Grand Warden flight-mode toggle sits at the bottom of his icon on the
	// troop bar. Nudge the tap upward so the PORTRAIT (selection/cursor) is
	// hit, NOT the chip (which would silently flip his ground<->air mode).
	ptY := slot.Y
	if strings.Contains(strings.ToLower(slot.UnitName), "warden") {
		ptY -= int(25.0 * t.cal.ScaleY)
	}
	jPt := t.addJitter(image.Pt(slot.X, ptY), jitterPx)
	t.logger.Debug().
		Int("x", jPt.X).
		Int("y", jPt.Y).
		Str("unit", slot.UnitName).
		Msg("tapping slot")
	t.client.TapFast(jPt.X, jPt.Y, 2.0)
}

// TapSlotAt taps a specific coordinate with jitter.
func (t *TapExecutor) TapSlotAt(x, y, jitterPx int) {
	jPt := t.addJitter(image.Pt(x, y), jitterPx)
	t.client.TapFast(jPt.X, jPt.Y, 2.0)
}

// TapDeployLine distributes taps along a line from p1 to p2.
func (t *TapExecutor) TapDeployLine(p1, p2 image.Point, count int, jitterPx int) {
	points := t.calculateLinePoints(p1, p2, count)

	// Randomize direction 50% for human variability
	if rand.Float64() < 0.5 {
		for i, j := 0, len(points)-1; i < j; i, j = i+1, j-1 {
			points[i], points[j] = points[j], points[i]
		}
	}

	// Deploy in batches of 3
	for i := 0; i < len(points); {
		rem := len(points) - i
		if rem >= 3 {
			j1 := t.addJitter(points[i], jitterPx)
			j2 := t.addJitter(points[i+1], jitterPx)
			j3 := t.addJitter(points[i+2], jitterPx)
			t.client.TapTriple(j1.X, j1.Y, 15.0, j2.X, j2.Y, 15.0, j3.X, j3.Y, 15.0)
			i += 3
		} else if rem == 2 {
			j1 := t.addJitter(points[i], jitterPx)
			j2 := t.addJitter(points[i+1], jitterPx)
			t.client.TapDual(j1.X, j1.Y, 15.0, j2.X, j2.Y, 15.0)
			i += 2
		} else {
			j1 := t.addJitter(points[i], jitterPx)
			t.client.TapFast(j1.X, j1.Y, 15.0)
			i += 1
		}
		t.sleepBetweenBatches()
	}
}

// TapDeployPoint clusters taps around a single point.
func (t *TapExecutor) TapDeployPoint(pt image.Point, count int, jitterPx int) {
	for i := 0; i < count; {
		rem := count - i
		if rem >= 3 {
			j1 := t.addJitter(pt, jitterPx)
			j2 := t.addJitter(pt, jitterPx)
			j3 := t.addJitter(pt, jitterPx)
			t.client.TapTriple(j1.X, j1.Y, 12.0, j2.X, j2.Y, 12.0, j3.X, j3.Y, 12.0)
			i += 3
		} else if rem == 2 {
			j1 := t.addJitter(pt, jitterPx)
			j2 := t.addJitter(pt, jitterPx)
			t.client.TapDual(j1.X, j1.Y, 12.0, j2.X, j2.Y, 12.0)
			i += 2
		} else {
			j1 := t.addJitter(pt, jitterPx)
			t.client.TapFast(j1.X, j1.Y, 12.0)
			i += 1
		}
		t.sleepBetweenBatches()
	}
}

// TapDeployFourSides performs rapid 4-side spam deployment.
func (t *TapExecutor) TapDeployFourSides(pCfg PrecisionConfig, targetEdge string, countPerSide int, jitterPx int) {
	edges := []string{"TopRight", "BottomRight", "BottomLeft", "TopLeft"}
	for _, edgeName := range edges {
		edge, ok := pCfg.Edges[edgeName]
		if !ok {
			continue
		}
		p1, p2 := edge.P1, edge.P2

		t.logger.Info().Str("edge", edgeName).Msg("FourSides rapid spam")
		steps := countPerSide
		if steps < 4 {
			steps = 4
		}
		for i := 0; i < steps; i += 3 {
			rem := steps - i
			if rem >= 3 {
				pct1 := float64(i) / float64(steps-1)
				pct2 := float64(i+1) / float64(steps-1)
				pct3 := float64(i+2) / float64(steps-1)
				tx1, ty1 := intLerp(p1, p2, pct1)
				tx2, ty2 := intLerp(p1, p2, pct2)
				tx3, ty3 := intLerp(p1, p2, pct3)
				j1 := t.addJitter(image.Pt(tx1, ty1), jitterPx)
				j2 := t.addJitter(image.Pt(tx2, ty2), jitterPx)
				j3 := t.addJitter(image.Pt(tx3, ty3), jitterPx)
				t.client.TapTriple(j1.X, j1.Y, 12.0, j2.X, j2.Y, 12.0, j3.X, j3.Y, 12.0)
			} else if rem == 2 {
				pct1 := float64(i) / float64(steps-1)
				pct2 := float64(i+1) / float64(steps-1)
				tx1, ty1 := intLerp(p1, p2, pct1)
				tx2, ty2 := intLerp(p1, p2, pct2)
				j1 := t.addJitter(image.Pt(tx1, ty1), jitterPx)
				j2 := t.addJitter(image.Pt(tx2, ty2), jitterPx)
				t.client.TapDual(j1.X, j1.Y, 12.0, j2.X, j2.Y, 12.0)
			} else {
				pct := float64(i) / float64(steps-1)
				tx, ty := intLerp(p1, p2, pct)
				j1 := t.addJitter(image.Pt(tx, ty), jitterPx)
				t.client.TapFast(j1.X, j1.Y, 12.0)
			}
			time.Sleep(45 * time.Millisecond)
		}
	}
	time.Sleep(120 * time.Millisecond)
}

// TapHeroAbility taps a hero slot for ability activation.
func (t *TapExecutor) TapHeroAbility(slot *TrackedSlot) {
	// Same warden-portrait-vs-mode-chip offset as TapSlot: tap the upper
	// portion of the icon so the ABILITY button registers, not the small
	// ground<->air toggle below it.
	ptY := slot.Y
	if strings.Contains(strings.ToLower(slot.UnitName), "warden") {
		ptY -= int(25.0 * t.cal.ScaleY)
	}
	t.logger.Info().
		Int("x", slot.X).
		Int("y", ptY).
		Str("unit", slot.UnitName).
		Msg("tapping hero ability")
	t.client.TapFast(slot.X, ptY, 4.0)
}

// TapBulkAbilities activates abilities for multiple heroes with delays.
func (t *TapExecutor) TapBulkAbilities(slots []*TrackedSlot, delayMs int) {
	if len(slots) == 0 {
		return
	}
	t.logger.Info().Int("count", len(slots)).Msg("bulk activating hero abilities")
	// Short settle so heroes have a chance to spawn on the map before the
	// first ability fires; full deploy-time wait is in hero_manager.go.
	time.Sleep(120 * time.Millisecond)

	for _, slot := range slots {
		t.TapHeroAbility(slot)
		time.Sleep(time.Duration(delayMs) * time.Millisecond)
	}
}

// WaitForSlotEmpty polls until a slot is empty or timeout.
func (t *TapExecutor) WaitForSlotEmpty(slot *TrackedSlot, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		screen, err := t.CaptureFresh()
		if err != nil {
			time.Sleep(200 * time.Millisecond)
			continue
		}
		empty := isSlotEmptyStatic(screen, slot.X, slot.Y, t.cal.PhysicalW, t.cal.PhysicalH)
		screen.Close()
		if empty {
			return true
		}
		time.Sleep(200 * time.Millisecond)
	}
	return false
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
	jx := int(float64(maxPixels) * t.cal.ScaleX)
	jy := int(float64(maxPixels) * t.cal.ScaleY)
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
