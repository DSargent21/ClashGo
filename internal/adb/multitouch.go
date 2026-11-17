package adb

import (
	"fmt"
	"image"
	"math"
	"math/rand"
	"strconv"
	"strings"
	"time"
)

// Evdev codes for the virtual touch panel BlueStacks exposes. A single-finger
// drag needs the multi-touch slot protocol: selecting slot 0, giving it a
// tracking id, then streaming ABS_MT_POSITION_X/Y with BTN_TOUCH held down.
// `input swipe` cannot do this — it delivers one down/up pair, which Clash
// treats as one placement at release, so the game never sees the finger move
// across the field (measured live 2026-11-17: 1 of 11 troops landed).
const (
	evSyn = 0
	evKey = 1
	evAbs = 3

	synReport      = 0
	btnTouch       = 330
	absMTSlot      = 47
	absMTPositionX = 53
	absMTPositionY = 54
	absMTTracking  = 57
)

// TouchPanel is an evdev node that accepts injected multi-touch, with the
// ABS_MT coordinate range its driver reports.
type TouchPanel struct {
	Path       string
	MaxX, MaxY int
}

// StrokeSample is one position of a drag, in screen pixels, with the time it is
// meant to be reached measured from the stroke start.
type StrokeSample struct {
	Pos image.Point
	At  time.Duration
}

// parseTouchPanels extracts panels from `getevent -pl` output. Only devices
// that report both ABS_MT_POSITION_X and ABS_MT_POSITION_Y qualify: without
// them a held, moving contact cannot be expressed at all.
func parseTouchPanels(out string) []TouchPanel {
	var panels []TouchPanel
	var cur TouchPanel
	var haveX, haveY bool
	flush := func() {
		if cur.Path != "" && haveX && haveY && cur.MaxX > 0 && cur.MaxY > 0 {
			panels = append(panels, cur)
		}
		cur, haveX, haveY = TouchPanel{}, false, false
	}
	for _, raw := range strings.Split(out, "\n") {
		line := strings.TrimSpace(raw)
		if strings.HasPrefix(line, "add device ") {
			flush()
			if i := strings.Index(line, "/dev/input/"); i >= 0 {
				cur.Path = strings.TrimSpace(line[i:])
			}
			continue
		}
		if strings.Contains(line, "ABS_MT_POSITION_X") || strings.Contains(line, "ABS_MT_POSITION_Y") {
			minV, maxV, ok := parseAbsRange(line)
			if !ok {
				continue
			}
			if strings.Contains(line, "ABS_MT_POSITION_X") {
				cur.MaxX, haveX = maxV, true
				_ = minV
			} else {
				cur.MaxY, haveY = maxV, true
			}
		}
	}
	flush()
	return panels
}

// parseAbsRange reads "min 0, max 32767" (or "min 0, max 32767,") from a
// getevent capability line.
func parseAbsRange(line string) (int, int, bool) {
	minI := strings.Index(line, "min ")
	maxI := strings.Index(line, "max ")
	if minI < 0 || maxI < 0 {
		return 0, 0, false
	}
	readInt := func(s string) (int, bool) {
		end := 0
		for end < len(s) && (s[end] == '-' || (s[end] >= '0' && s[end] <= '9')) {
			end++
		}
		v, err := strconv.Atoi(s[:end])
		return v, err == nil
	}
	minV, okMin := readInt(line[minI+4:])
	maxV, okMax := readInt(line[maxI+4:])
	if !okMin || !okMax || maxV <= minV {
		return 0, 0, false
	}
	return minV, maxV, true
}

// BuildDragStroke lays out a human drag from `from` to `to`: ease-out velocity
// (fast commit, settling finish), a slight bow, bounded jitter and clamped to
// the screen box. Times are strictly increasing so a caller can pace them.
func BuildDragStroke(from, to image.Point, w, h int, duration time.Duration, samples, bowPx, jitterPx int) []StrokeSample {
	if samples < 2 {
		samples = 2
	}
	if duration <= 0 {
		duration = 300 * time.Millisecond
	}
	dx, dy := float64(to.X-from.X), float64(to.Y-from.Y)
	length := math.Hypot(dx, dy)
	// Unit normal for the bow.
	nx, ny := 0.0, 0.0
	if length > 0 {
		nx, ny = -dy/length, dx/length
	}
	bow := float64(bowPx)
	if rand.Intn(2) == 0 {
		bow = -bow
	}
	out := make([]StrokeSample, 0, samples)
	for i := 0; i < samples; i++ {
		t := float64(i) / float64(samples-1)
		// Ease-out cubic: sub-linear velocity, a human commit-then-settle drag.
		s := 1 - math.Pow(1-t, 3)
		along := s * length
		bend := bow * math.Sin(math.Pi*s)
		x := float64(from.X) + dx/lengthSafe(length)*along + nx*bend
		y := float64(from.Y) + dy/lengthSafe(length)*along + ny*bend
		if jitterPx > 0 {
			x += rand.NormFloat64() * float64(jitterPx) * 0.5
			y += rand.NormFloat64() * float64(jitterPx) * 0.5
		}
		out = append(out, StrokeSample{
			Pos: image.Pt(clampInt(int(math.Round(x)), 0, w-1), clampInt(int(math.Round(y)), 0, h-1)),
			At:  time.Duration(t * float64(duration)),
		})
	}
	// The press and release are exact: a drag that starts a few pixels off the
	// card can miss the slot entirely.
	out[0].Pos = image.Pt(clampInt(from.X, 0, w-1), clampInt(from.Y, 0, h-1))
	out[len(out)-1].Pos = image.Pt(clampInt(to.X, 0, w-1), clampInt(to.Y, 0, h-1))
	return out
}

// BuildHoldThenDrag is the game's own deployment gesture: hold a still finger
// on the field until a troop spawns, then sweep. Players describe it as "press
// until a troop spawns, then drag across" (Arqade 2015, CocLand slides); live
// 2026-11-17 a stroke that started moving immediately placed nothing, because
// the press has to register as a spawn first.
func BuildHoldThenDrag(from, to image.Point, w, h int, hold, duration time.Duration, samples, bowPx, jitterPx int) []StrokeSample {
	holdSamples := 4
	if hold < 120*time.Millisecond {
		holdSamples = 2
	}
	out := make([]StrokeSample, 0, holdSamples+samples)
	for i := 0; i < holdSamples; i++ {
		t := time.Duration(float64(i) / float64(holdSamples) * float64(hold))
		out = append(out, StrokeSample{
			Pos: image.Pt(clampInt(from.X, 0, w-1), clampInt(from.Y, 0, h-1)),
			At:  t,
		})
	}
	sweep := BuildDragStroke(from, to, w, h, duration, samples, bowPx, jitterPx)
	for _, s := range sweep {
		s.At += hold
		out = append(out, s)
	}
	// The duplicate press points and the sweep's first point share a time; keep
	// the stream strictly increasing so the sleeps are always positive.
	for i := 1; i < len(out); i++ {
		if out[i].At <= out[i-1].At {
			out[i].At = out[i-1].At + time.Millisecond
		}
	}
	return out
}

// BuildWaypointDrag presses at the first point, holds until `hold`, then moves
// through the remaining waypoints to the last one. Distance-weighted so a long
// leg gets proportionally more samples, ease-out so the drag commits fast and
// settles; only the interior points wobble, the press and release are exact.
// This is the real deployment gesture: the finger goes down on the card, is
// held there, then is dragged across the field.
func BuildWaypointDrag(waypoints []image.Point, w, h int, hold, duration time.Duration, samples, jitterPx int) []StrokeSample {
	if len(waypoints) < 2 {
		return nil
	}
	if samples < len(waypoints)*2 {
		samples = len(waypoints) * 2
	}
	if duration <= 0 {
		duration = 400 * time.Millisecond
	}
	lens := make([]float64, len(waypoints)-1)
	total := 0.0
	for i := 1; i < len(waypoints); i++ {
		dx, dy := float64(waypoints[i].X-waypoints[i-1].X), float64(waypoints[i].Y-waypoints[i-1].Y)
		lens[i-1] = math.Hypot(dx, dy)
		total += lens[i-1]
	}
	if total == 0 {
		return nil
	}
	pointAt := func(d float64) image.Point {
		if d <= 0 {
			return waypoints[0]
		}
		for i := 0; i < len(lens); i++ {
			if d <= lens[i] {
				t := d / lens[i]
				x := float64(waypoints[i].X) + float64(waypoints[i+1].X-waypoints[i].X)*t
				y := float64(waypoints[i].Y) + float64(waypoints[i+1].Y-waypoints[i].Y)*t
				return image.Pt(int(math.Round(x)), int(math.Round(y)))
			}
			d -= lens[i]
		}
		return waypoints[len(waypoints)-1]
	}

	out := make([]StrokeSample, 0, 4+samples)
	holdSamples := 3
	if hold < 120*time.Millisecond {
		holdSamples = 1
	}
	for i := 0; i < holdSamples; i++ {
		out = append(out, StrokeSample{
			Pos: image.Pt(clampInt(waypoints[0].X, 0, w-1), clampInt(waypoints[0].Y, 0, h-1)),
			At:  time.Duration(float64(i+1) / float64(holdSamples+1) * float64(hold)),
		})
	}
	for i := 1; i <= samples; i++ {
		t := float64(i) / float64(samples)
		s := 1 - math.Pow(1-t, 3)
		p := pointAt(s * total)
		if i != samples && jitterPx > 0 {
			p.X += int(math.Round(rand.NormFloat64() * float64(jitterPx) * 0.5))
			p.Y += int(math.Round(rand.NormFloat64() * float64(jitterPx) * 0.5))
		}
		out = append(out, StrokeSample{
			Pos: image.Pt(clampInt(p.X, 0, w-1), clampInt(p.Y, 0, h-1)),
			At:  hold + time.Duration(t*float64(duration)),
		})
	}
	for i := 1; i < len(out); i++ {
		if out[i].At <= out[i-1].At {
			out[i].At = out[i-1].At + time.Millisecond
		}
	}
	out[len(out)-1].Pos = image.Pt(clampInt(waypoints[len(waypoints)-1].X, 0, w-1), clampInt(waypoints[len(waypoints)-1].Y, 0, h-1))
	return out
}

func lengthSafe(l float64) float64 {
	if l == 0 {
		return 1
	}
	return l
}

// TouchPanel discovers (once per client) the evdev node that can take injected
// multi-touch. It is nil when the image has no ABS_MT device.
func (c *Client) TouchPanel() *TouchPanel {
	c.panelOnce.Do(func() {
		out, err := c.Shell("getevent -pl")
		if err != nil {
			c.panelErr = err
			return
		}
		panels := parseTouchPanels(out)
		if len(panels) > 0 {
			c.panel = &panels[0]
		}
	})
	return c.panel
}

// DragStroke replays a stroke as one shell line of sendevent calls: slot 0 is
// acquired, the contact is held for the whole path, and only the final sample
// releases it (ABS_MT_TRACKING_ID -1, BTN_TOUCH 0).
//
// The moves are paced with an explicit sleep between samples, sized from the
// stroke's own timings. Live 2026-11-17: without the sleeps the whole stream
// took ~50 ms for 200 px and the game read it as one gesture, placing nothing;
// a stroke that is actually held for ~350 ms is what the game streams along.
func (c *Client) DragStroke(panel *TouchPanel, samples []StrokeSample, w, h int) error {
	if panel == nil || len(samples) < 2 || w <= 0 || h <= 0 {
		return fmt.Errorf("drag stroke: no panel or too few samples")
	}
	xabs := func(x int) int { return x * panel.MaxX / (w - 1) }
	yabs := func(y int) int { return y * panel.MaxY / (h - 1) }

	var b strings.Builder
	emit := func(typ, code, val int) {
		fmt.Fprintf(&b, "sendevent %s %d %d %d; ", panel.Path, typ, code, val)
	}
	tracking := rand.Intn(60000) + 1
	emit(evAbs, absMTSlot, 0)
	emit(evAbs, absMTTracking, tracking)
	emit(evAbs, absMTPositionX, xabs(samples[0].Pos.X))
	emit(evAbs, absMTPositionY, yabs(samples[0].Pos.Y))
	emit(evKey, btnTouch, 1)
	emit(evSyn, synReport, 0)
	for i, s := range samples[1:] {
		// Hold the contact for the interval the builder asked for, so the game
		// samples a moving finger instead of a single instant jump.
		gap := s.At - samples[i].At
		if gap < 4*time.Millisecond {
			gap = 4 * time.Millisecond
		}
		if gap > 40*time.Millisecond {
			gap = 40 * time.Millisecond
		}
		fmt.Fprintf(&b, "sleep %.3f; ", gap.Seconds())
		emit(evAbs, absMTPositionX, xabs(s.Pos.X))
		emit(evAbs, absMTPositionY, yabs(s.Pos.Y))
		emit(evSyn, synReport, 0)
	}
	emit(evAbs, absMTTracking, -1)
	emit(evKey, btnTouch, 0)
	emit(evSyn, synReport, 0)

	c.markInput()
	_, err := c.Shell(strings.TrimSuffix(b.String(), " "))
	c.fireTapHook(TapEvent{
		Type: "drag", X: samples[0].Pos.X, Y: samples[0].Pos.Y,
		ActualX: samples[len(samples)-1].Pos.X, ActualY: samples[len(samples)-1].Pos.Y,
		Error: errStr(err),
	})
	return err
}
