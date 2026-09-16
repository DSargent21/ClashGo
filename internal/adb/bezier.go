package adb

import (
	"fmt"
	"image"
	"math"
	"math/rand"
	"strings"
	"time"
)

// swipePath returns `steps` points tracing a quadratic bezier curve from
// p0 to p2 with control point p1. With p1 == the straight-line midpoint
// the path degenerates to the p0→p2 segment (allowing unit tests to assert
// both the degenerate and the curved cases).
func swipePath(p0, p1, p2 image.Point, steps int) []image.Point {
	if steps < 2 {
		steps = 2
	}
	pts := make([]image.Point, steps)
	for i := 0; i < steps; i++ {
		t := float64(i) / float64(steps-1)
		inv := 1 - t
		x := inv*inv*float64(p0.X) + 2*inv*t*float64(p1.X) + t*t*float64(p2.X)
		y := inv*inv*float64(p0.Y) + 2*inv*t*float64(p1.Y) + t*t*float64(p2.Y)
		pts[i] = image.Point{X: int(math.Round(x)), Y: int(math.Round(y))}
	}
	return pts
}

// easedStepSleeps distributes `totalMs` across `steps` per-step pause
// durations using a smoothstep velocity profile: long pauses while the
// finger is slow (start/end of the gesture), short pauses at peak
// velocity (middle). Each path sample is equidistant, so the pause is
// proportional to 1/velocity — the derivative of smoothstep, 6t(1-t),
// clamped so the near-zero velocities at the endpoints stay finite.
// The pauses sum to approximately totalMs (each floored at 4ms so the
// device shell never hammers).
func easedStepSleeps(totalMs, steps int) []int {
	if steps < 1 {
		return nil
	}
	if totalMs <= 0 {
		totalMs = 300
	}
	w := make([]float64, steps)
	var sum float64
	for i := 0; i < steps; i++ {
		// Smoothstep velocity at the midpoint of this interval.
		t := (float64(i) + 0.5) / float64(steps)
		v := 6 * t * (1 - t)
		if v < 0.15 {
			v = 0.15
		}
		w[i] = 1 / v
		sum += w[i]
	}
	if sum <= 0 {
		sum = 1
	}
	sleeps := make([]int, steps)
	for i := range w {
		ms := int(math.Round(w[i] / sum * float64(totalMs)))
		if ms < 4 {
			ms = 4
		}
		sleeps[i] = ms
	}
	return sleeps
}

func clampInt(v, min, max int) int {
	if v < min {
		return min
	}
	if v > max {
		return max
	}
	return v
}

// SwipeBezier performs a single-finger swipe along a quadratic bezier arc
// with ease-in-out velocity (accelerate → coast → decelerate). It is
// emitted as a low-level sendevent stream — the same Protocol A technique
// PinchZoom uses for genuine multi-touch — so the game receives a real
// continuous multi-point gesture instead of a straight-line `input swipe`.
//
// The control point is offset perpendicular to the start→end axis by a
// random 10–20% of the segment length, so every gesture has a natural,
// unpredictable arc (humans rarely drag in perfectly straight lines).
//
// Robustness: if the emulator's touch device cannot be resolved, the
// screen size is unknown, or the sendevent stream fails, it degrades to
// SwipeHuman (the existing randomized linear swipe) so navigation never
// breaks on a device that doesn't support raw input injection.
func (c *Client) SwipeBezier(x1, y1, x2, y2, ms int) error {
	if x1 == x2 && y1 == y2 {
		// Zero-length gesture is a press-and-release at that point.
		return c.Hold(x1, y1, ms)
	}

	device, err := c.DetectTouchDevice()
	if err != nil {
		c.log.Debugf("SwipeBezier: touch device unavailable (%v); falling back to linear swipe", err)
		return c.SwipeHuman(x1, y1, x2, y2, ms)
	}

	w, h, err := c.ScreenSize()
	if err != nil || w <= 0 || h <= 0 {
		c.log.Debugf("SwipeBezier: screen size unavailable (%v); falling back to linear swipe", err)
		return c.SwipeHuman(x1, y1, x2, y2, ms)
	}

	// Local seeded source (matches the codebase's rand convention) so the
	// arc offset and sign vary per gesture on every toolchain.
	r := rand.New(rand.NewSource(time.Now().UnixNano()))

	// BlueStacks Virtual Touch uses 0-32767 (same range PinchZoom relies on).
	const touchMax = 32767
	scaleX := func(v int) int { return clampInt(v*touchMax/w, 0, touchMax) }
	scaleY := func(v int) int { return clampInt(v*touchMax/h, 0, touchMax) }

	// Quadratic bezier control point: midpoint offset perpendicular to the
	// gesture axis by a random 10-20% of its length.
	p0 := image.Point{X: x1, Y: y1}
	p2 := image.Point{X: x2, Y: y2}
	mid := image.Point{X: (x1 + x2) / 2, Y: (y1 + y2) / 2}
	dx, dy := float64(x2-x1), float64(y2-y1)
	l := math.Hypot(dx, dy)
	sign := 1.0
	if r.Float64() < 0.5 {
		sign = -1
	}
	curve := 0.10 + r.Float64()*0.10
	// Perpendicular unit vector is (-dy/l, dx/l).
	p1 := image.Point{
		X: int(math.Round(float64(mid.X) + sign*curve*(-dy/l)*l)),
		Y: int(math.Round(float64(mid.Y) + sign*curve*(dx/l)*l)),
	}

	// 12 steps keeps the gesture smooth without ballooning the sendevent
	// batch past what adb can deliver in one shell invocation.
	steps := 12
	pts := swipePath(p0, p1, p2, steps)
	sleeps := easedStepSleeps(ms, steps)

	var batch strings.Builder
	add := func(typ, code, value int) {
		batch.WriteString(fmt.Sprintf("sendevent %s %d %d %d && ", device, typ, code, value))
	}

	// 1. LANDING: finger down at the gesture start.
	add(3, 57, 1)  // tracking ID 1
	add(1, 330, 1) // BTN_TOUCH DOWN
	add(3, 53, scaleX(pts[0].X))
	add(3, 54, scaleY(pts[0].Y))
	add(0, 2, 0) // SYN_MT_REPORT
	add(0, 0, 0) // SYN_REPORT

	// 2. MOVEMENT: eased cadence along the bezier path.
	for i := 1; i < steps; i++ {
		batch.WriteString(fmt.Sprintf("sleep %.2f && ", float64(sleeps[i-1])/1000.0))
		add(3, 53, scaleX(pts[i].X))
		add(3, 54, scaleY(pts[i].Y))
		add(0, 2, 0)
		add(0, 0, 0)
	}

	// 3. LIFT: clean release.
	add(3, 57, -1) // release ID 1
	add(1, 330, 0) // BTN_TOUCH UP
	add(0, 2, 0)
	batch.WriteString(fmt.Sprintf("sendevent %s 0 0 0", device))

	if _, err := c.Shell(batch.String()); err != nil {
		// Never fail navigation on an input-injection quirk; degrade to the
		// well-trodden linear path.
		c.log.Debugf("SwipeBezier: sendevent stream failed (%v); falling back to linear swipe", err)
		return c.SwipeHuman(x1, y1, x2, y2, ms)
	}
	return nil
}
