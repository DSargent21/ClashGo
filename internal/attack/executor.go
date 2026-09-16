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

// TapExecutor handles all tap operations, screen capture, and timing.
type TapExecutor struct {
	client      *adb.Client
	cal         *game.Calibration
	logger      zerolog.Logger
	lineForward bool

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

// TapSlot selects a slot with jitter for human-like behavior.
func (t *TapExecutor) TapSlot(slot *TrackedSlot, jitterPx int) {

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
