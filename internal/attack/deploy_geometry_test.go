package attack

import (
	"fmt"
	"sort"
	"testing"

	"github.com/Ducky705/ClashGO/pkg/formula"
)

// The deploy path is the only part of the bot that spends resources, and its
// failure mode is silent: a tap that lands behind the top HUD or under the
// troop bar hits chrome instead of the village, the unit never deploys, and no
// log line is wrong. The classifier got a real-frame corpus for exactly this
// kind of gate (internal/game/corpus_test.go); the deploy geometry had nothing,
// so a projection regression could only be found by spending an attack.
//
// These tests project the shipped formula the way the orchestrator does at the
// live 720p geometry and assert the taps land where a tap can work. They found
// real breakage on the first run: at 1280x720 the shipped formula puts three of
// its units outside the band the deploy code will tap — two under the troop bar
// (balloon, electro dragon) and one behind the top resource HUD (dragon duke) —
// and a Top-corner mirror pushes more up there.
const (
	liveW, liveH = 1280, 720
	// device.display_scale, measured with cmd/resprobe at this geometry.
	liveK = 1.325
	// The bot's own band: DeployLineCalculator never places a point above
	// yTopMin, and the orchestrator's uiCutoff is int(h*0.85) ("above troop
	// bar"). Everything outside is chrome.
	liveUICutoff = int(float64(liveH) * 0.85) // 612
)

// tappableBand is the range of y a deploy tap can work in, in the bot's own
// terms. Using the package constants rather than numbers here is deliberate: if
// someone moves the band, this gate moves with it and cannot disagree.
func tappableBand() (int, int) { return YTopMin(), liveUICutoff }

// deployedPoints returns the tapped coordinates of a formula, excluding the
// underscore-prefixed helper entries (e.g. _rage_inner), which are reference
// shapes for other units rather than taps of their own.
func deployedPoints(f *formula.Formula) map[string][][2]int {
	out := map[string][][2]int{}
	for name, entry := range f.Units {
		if len(name) > 0 && name[0] == '_' {
			continue
		}
		var pts [][2]int
		switch {
		case entry.P != nil:
			pts = append(pts, [2]int{entry.P.X, entry.P.Y})
		case entry.P1 != nil && entry.P2 != nil:
			// A line is deployed along, so its endpoints bound where it can
			// reach: both must be tappable.
			pts = append(pts, [2]int{entry.P1.X, entry.P1.Y}, [2]int{entry.P2.X, entry.P2.Y})
		default:
			for _, l := range entry.Lines {
				pts = append(pts, [2]int{l.P1.X, l.P1.Y}, [2]int{l.P2.X, l.P2.Y})
			}
		}
		if len(pts) > 0 {
			out[name] = pts
		}
	}
	return out
}

// projectShipped loads, resolves and projects the shipped formula exactly as
// the orchestrator does for one corner, and returns the raw (unclamped) result
// plus the clamped one — the clamp being part of the same code path the bot
// runs.
//
// The corner resolution goes through applyCornerUnits rather than calling
// MirrorForCorner directly. That distinction is load-bearing and this gate got
// it wrong first: the shipped formula carries authored overrides for all four
// corners, so the orchestrator never mirrors any of them, while a gate that
// mirrors anyway measures coordinates the bot will not tap. Reusing the
// orchestrator's own function means the two cannot disagree.
func projectShipped(t *testing.T, corner string) (raw, clamped *formula.Formula, usedOverride bool) {
	t.Helper()
	resolve := func() (*formula.Formula, bool) {
		f := loadShippedFormula(t)
		used := applyCornerUnits(f, corner)
		f.ProjectUniform(liveK, f.Screen.W, f.Screen.H, liveW, liveH)
		return f, used
	}

	raw, usedOverride = resolve()
	clamped, _ = resolve()
	minY, maxY := tappableBand()
	clamped.ClampY(minY, maxY)
	return raw, clamped, usedOverride
}

func outsideBand(f *formula.Formula) (offScreen, above, below []string) {
	minY, maxY := tappableBand()
	for name, pts := range deployedPoints(f) {
		for _, p := range pts {
			x, y := p[0], p[1]
			switch {
			case x < 0 || x >= liveW || y < 0 || y >= liveH:
				offScreen = append(offScreen, fmt.Sprintf("%s(%d,%d)", name, x, y))
			case y < minY:
				above = append(above, fmt.Sprintf("%s y=%d", name, y))
			case y > maxY:
				below = append(below, fmt.Sprintf("%s y=%d", name, y))
			}
		}
	}
	sort.Strings(offScreen)
	sort.Strings(above)
	sort.Strings(below)
	return
}

// TestShippedFormulaClampsIntoTheTappableBand is the deploy-geometry gate: what
// the orchestrator ends up tapping, for every corner, must be inside the band.
func TestShippedFormulaClampsIntoTheTappableBand(t *testing.T) {
	minY, maxY := tappableBand()
	for _, corner := range []string{"BottomRight", "BottomLeft", "TopRight", "TopLeft"} {
		t.Run(corner, func(t *testing.T) {
			raw, clamped, usedOverride := projectShipped(t, corner)
			t.Logf("corner resolution: authored override used = %v (false means the BR default was mirrored)", usedOverride)

			total := 0
			for _, pts := range deployedPoints(clamped) {
				total += len(pts)
			}
			if total == 0 {
				t.Fatal("no deploy points found in the shipped formula — the gate would pass vacuously")
			}

			if off, above, below := outsideBand(raw); len(off)+len(above)+len(below) > 0 {
				// Not a failure: the clamp exists for exactly this, and the
				// durable fix (re-authoring the formula for 16:9) is the
				// author's call. Logged so the debt is visible and cannot grow
				// without someone seeing it here.
				t.Logf("NOTE: %d of %d points project outside the band before clamping (band y %d..%d): %d off-screen %v, %d above %v, %d below %v",
					len(off)+len(above)+len(below), total, minY, maxY, len(off), off, len(above), above, len(below), below)
			}

			if off, above, below := outsideBand(clamped); len(off)+len(above)+len(below) > 0 {
				t.Errorf("after clamping, %d point(s) still outside the tappable band (y %d..%d) — these tap chrome and the unit never deploys: %d off-screen %v, %d above %v, %d below %v",
					len(off)+len(above)+len(below), minY, maxY, len(off), off, len(above), above, len(below), below)
			}
		})
	}
}

// TestClampBandCoversTheCornerThatOnlyTheMirrorReaches guards the case that
// motivated clamping the top edge at all: a Top-corner line reaches up the
// frame toward the resource HUD, where the old bottom-only clamp left it alone —
// observed live, on the shipped TopRight override, as balloon p2 and electro
// dragon p2 projecting to y 26 and 25 while the bot tapped them anyway. If the
// clamp ever loses its top bound, those taps land in the chrome again.
func TestClampBandCoversTheCornerThatOnlyTheMirrorReaches(t *testing.T) {
	_, clamped, _ := projectShipped(t, "TopRight")
	minY, maxY := tappableBand()
	if _, above, below := outsideBand(clamped); len(above) > 0 || len(below) > 0 {
		t.Errorf("the clamped TopRight plan still leaves points outside y %d..%d (above %v, below %v)", minY, maxY, above, below)
	}
}
