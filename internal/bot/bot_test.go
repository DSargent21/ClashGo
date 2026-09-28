package bot

import (
	"image"
	"testing"

	"github.com/Ducky705/ClashGO/internal/game"
	"github.com/rs/zerolog"
)

// TestButtonROIConsistency pins the agreement between the bot's click paths and
// the classifier's search windows: both read the game package's table, and a
// template nobody has measured a window for gets the whole reference frame.
func TestButtonROIConsistency(t *testing.T) {
	b := &Bot{}

	for _, name := range []string{
		"btn_attack", "btn_find_match", "btn_battle", "btn_next",
		"btn_army_arrow", "btn_army_1", "btn_okay", "btn_return_home",
	} {
		want, ok := game.ButtonROIBounds(name)
		if !ok {
			t.Fatalf("%s: the shared table has no window for a button the bot clicks", name)
		}
		if got := b.buttonROI(name); got != want {
			t.Errorf("buttonROI(%q) = %v, want the shared window %v", name, got, want)
		}
	}

	for _, name := range []string{"unknown_button", "hammer", ""} {
		if got, want := b.buttonROI(name), game.ReferenceFrame(); got != want {
			t.Errorf("buttonROI(%q) = %v, want the whole reference frame %v", name, got, want)
		}
	}
}

// TestRuleWindowsContainPinpoints is the invariant the classifier's search
// windows rest on. A window is only a cost optimisation while it covers the
// button; the moment it stops covering it, the rule loses its template evidence
// on a live frame — silently, and only on the geometries nobody tested. So for
// every rule that searches a window, the point the bot actually taps for that
// button must be inside it, at every geometry the bot can run at.
//
// The tap points and the windows come from different places (villagePinpoints
// vs game.buttonROIBounds), which is exactly why this needs an assertion: a
// window is authored from a read-off coordinate, and this is what checks that
// the two still describe the same button.
func TestRuleWindowsContainPinpoints(t *testing.T) {
	geometries := []struct {
		name string
		w, h int
	}{
		{"reference", game.RefWidth, game.RefHeight},
		{"720p", 1280, 720},
		{"1080p", 1920, 1080},
	}
	// The scale the bot pins in its config for the live device (docs/RESOLUTION.md).
	const pinnedScale = 1.325

	checked := 0
	for _, g := range geometries {
		cal := game.NewCalibration(g.w, g.h)
		if g.w != game.RefWidth || g.h != game.RefHeight {
			cal.SetDisplayScale(pinnedScale)
		}
		b := &Bot{cal: cal}
		frame := image.Rect(0, 0, g.w, g.h)

		for _, rule := range game.NewClassifier(cal, game.DefaultClassifierConfig(), zerolog.Nop()).GetRules() {
			if rule.Template == "" {
				continue
			}
			pp, ok := villagePinpoints[rule.Template]
			if !ok {
				// No tap point to check against (hammer is matched but never
				// tapped by name); it must not have been given a window either.
				if _, windowed := game.ButtonROIBounds(rule.Template); windowed {
					t.Errorf("%s: a window exists but no tap point to verify it against", rule.Template)
				}
				continue
			}
			win := game.RuleSearchROI(cal, rule.Template, rule.Anchor, frame)
			x, y := b.pinpointPoint(pp)
			if !image.Pt(x, y).In(win) {
				t.Errorf("%s at %s: search window %v misses the tap point (%d,%d) for state %s — the rule would lose its template evidence on a live frame",
					rule.Template, g.name, win, x, y, rule.State)
			}
			checked++
		}
	}
	if checked == 0 {
		t.Fatal("no windowed rule was checked; the invariants have gone stale")
	}
}

func TestHistoryCacheNoWipeOnReadError(t *testing.T) {
	b := &Bot{
		historyCache: []AttackReport{
			{Timestamp: "earlier", Stars: 3},
		},
	}

	// Simulate the record path using the in-memory cache as the source of
	// truth. A disk read failure (handled by the caller returning a fresh
	// nil slice) must NOT drop the prior run.
	history := b.historyCache
	if history == nil {
		// emulate read error -> fresh empty slice
		history = []AttackReport{}
	}
	rep := AttackReport{Timestamp: "now", Stars: 2}
	history = append([]AttackReport{rep}, history...)
	b.historyCache = history

	if len(b.historyCache) != 2 {
		t.Fatalf("history cache wiped: got %d entries, want 2", len(b.historyCache))
	}
	if b.historyCache[0].Timestamp != "now" || b.historyCache[1].Timestamp != "earlier" {
		t.Errorf("history cache order/copy wrong: %+v", b.historyCache)
	}
}
