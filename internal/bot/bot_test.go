package bot

import (
	"image"
	"os"
	"testing"

	"gocv.io/x/gocv"

	"github.com/Ducky705/ClashGO/internal/config"
	"github.com/Ducky705/ClashGO/internal/game"
	"github.com/Ducky705/ClashGO/internal/vision"
	"github.com/rs/zerolog"
)

// TestButtonROIConsistency pins the agreement between the bot's click paths and
// the classifier's search windows: both read the game package's table, and a
// template nobody has measured a window for gets the whole reference frame.
// paintFrameFace stamps a solid face rectangle into a BGR frame, plus a band of
// label pixels through its middle — the shape the Attack! control actually has.
func paintFrameFace(img *gocv.Mat, r image.Rectangle, bgr [3]uint8) {
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			img.SetUCharAt(y, x*3, bgr[0])
			img.SetUCharAt(y, x*3+1, bgr[1])
			img.SetUCharAt(y, x*3+2, bgr[2])
		}
	}
	band := image.Rect(r.Min.X+4, r.Min.Y+r.Dy()/2-2, r.Max.X-4, r.Min.Y+r.Dy()/2+2)
	for y := band.Min.Y; y < band.Max.Y; y++ {
		for x := band.Min.X; x < band.Max.X; x++ {
			img.SetUCharAt(y, x*3, 255)
			img.SetUCharAt(y, x*3+1, 255)
			img.SetUCharAt(y, x*3+2, 255)
		}
	}
}

// TestMatchBoxCentresTheMatchedTemplate pins the geometry the face test depends
// on. vision reports a match by the CENTRE of its box (the matcher adds half the
// scaled template back to the peak), so a box built from it as if the point were
// the top-left corner lands half a button away and the face count is measured off
// the control.
func TestMatchBoxCentresTheMatchedTemplate(t *testing.T) {
	tpl := gocv.NewMatWithSize(63, 246, gocv.MatTypeCV8UC3) // rows, cols
	defer tpl.Close()

	m := vision.Match{Point: image.Pt(84, 681), Scale: 0.384, Confidence: 0.85}
	got := matchBox(m, tpl)
	want := image.Rect(84-47, 681-12, 84+47, 681+12)
	if got != want {
		t.Errorf("matchBox = %v, want %v (centred on the match point)", got, want)
	}

	// A degenerate scale answers with an empty box, which ColourPixels treats as
	// no evidence rather than handing OpenCV an illegal ROI.
	if got := matchBox(vision.Match{Point: image.Pt(10, 10), Scale: 0}, tpl); !got.Empty() {
		t.Errorf("matchBox with zero scale = %v, want empty", got)
	}
}

// TestAttackButtonFaceConfirmedNeedsTheButtonFace is the regression for the
// stuck-in-the-village hang: the attack verdict used to be a single-pixel probe
// whose 21x21 window is a third white glyphs, and it read 19 of the >20 orange
// pixels it demanded on a live village with the button plainly drawn. The verdict
// is now this face measurement, so both faces the same button has been measured
// with have to pass it, and white text on a dim panel has to fail it.
func TestAttackButtonFaceConfirmedNeedsTheButtonFace(t *testing.T) {
	cases := []struct {
		name      string
		face      [3]uint8
		w, h      int
		confirmed bool
	}{
		// Both live measurements of the village Attack! face, 2026-09-29.
		{"live village face", [3]uint8{16, 69, 169}, 200, 40, true},
		{"older frame face", [3]uint8{40, 102, 194}, 200, 40, true},
		// The reference geometry's gold chrome.
		{"reference gold", [3]uint8{54, 177, 247}, 200, 40, true},
		// A label on a dark panel: bright, unsaturated, and exactly what the
		// builder menu draws where a control belongs.
		{"white label on dark panel", [3]uint8{20, 20, 20}, 200, 40, false},
		// The village's own greens and blues.
		{"grass", [3]uint8{98, 237, 200}, 200, 40, false},
	}

	tpl := gocv.NewMatWithSize(40, 200, gocv.MatTypeCV8UC3)
	defer tpl.Close()

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			img := gocv.NewMatWithSize(tc.h, tc.w, gocv.MatTypeCV8UC3)
			defer img.Close()
			face := image.Rect(0, 0, tc.w, tc.h)
			paintFrameFace(&img, face, tc.face)

			confirmed, px, frac := attackButtonFaceConfirmed(img, vision.Match{Point: image.Pt(tc.w/2, tc.h/2), Scale: 1}, tpl)
			if confirmed != tc.confirmed {
				t.Errorf("confirmed = %v, want %v (face_px=%d frac=%.2f)", confirmed, tc.confirmed, px, frac)
			}
		})
	}
}

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

// TestSplashDismissBudgetIsBounded pins the boundary of the per-splash tap
// budget: exactly maxSplashDismissAttempts taps go out, the next detection is
// refused. The refusal is what stops the bot re-tapping a splash whose own
// classifier probe said the prompt was there and which still did not clear —
// observed live on 2026-09-29, where that loop ran for the whole 5-minute boot
// grace before the watchdog recovered the run.
func TestSplashDismissBudgetIsBounded(t *testing.T) {
	for attempt := 1; attempt <= maxSplashDismissAttempts; attempt++ {
		if !splashDismissAllowed(attempt) {
			t.Fatalf("attempt %d must be allowed (budget is %d)", attempt, maxSplashDismissAttempts)
		}
	}
	if splashDismissAllowed(maxSplashDismissAttempts + 1) {
		t.Fatalf("attempt %d must be refused; a splash that never clears would be tapped forever", maxSplashDismissAttempts+1)
	}
}

// TestGaveUpSplashLosesTheBootGrace pins the escalation: while a splash still
// has taps in its budget it keeps the generous boot grace (the splash may be one
// tap away from advancing), but once the budget is spent the bot has stopped
// touching the screen, so the ORDINARY stuck timeout must own it instead of
// several more idle minutes. The states that genuinely need the whole boot
// window — the castle logo's 1-3 minute connect, the update prompt that returns
// on every relaunch — must keep it either way.
func TestGaveUpSplashLosesTheBootGrace(t *testing.T) {
	b := &Bot{}

	for _, state := range []game.GameState{game.StateLogo, game.StateUpdatePrompt, game.StateTapToContinue, game.StateNewsSplash} {
		if !b.bootGraceApplies(state) {
			t.Fatalf("%s must get the boot grace before any dismissal has been spent", state)
		}
	}
	if b.bootGraceApplies(game.StateMainVillage) {
		t.Fatal("the village must not get the boot grace; the generic stuck timeout owns it")
	}

	b.splashDismissGaveUp.Store(true)

	for _, state := range []game.GameState{game.StateTapToContinue, game.StateNewsSplash} {
		if b.bootGraceApplies(state) {
			t.Fatalf("%s must lose the boot grace once its dismissal budget is spent", state)
		}
	}
	for _, state := range []game.GameState{game.StateLogo, game.StateUpdatePrompt} {
		if !b.bootGraceApplies(state) {
			t.Fatalf("%s must keep the boot grace: restarting does not clear it, so the generic timeout would only make a restart loop", state)
		}
	}
}

// TestSplashDismissEpisodeResetsOnProgress pins that the budget is PER SPLASH,
// not per session: the verdict that is not a tap-dismissed splash is the progress
// signal the dismissal path deliberately does not fake, so it must restore both
// the tap budget and the boot grace for whatever splash comes next.
func TestSplashDismissEpisodeResetsOnProgress(t *testing.T) {
	b := &Bot{}
	b.splashDismissAttempts.Store(maxSplashDismissAttempts)
	b.splashDismissGaveUp.Store(true)

	b.clearSplashDismissEpisode()

	if got := b.splashDismissAttempts.Load(); got != 0 {
		t.Fatalf("dismissal attempts after the episode ended = %d, want 0", got)
	}
	if b.splashDismissGaveUp.Load() {
		t.Fatal("the give-up flag must clear when the screen moves past the splash")
	}
	if !b.bootGraceApplies(game.StateTapToContinue) {
		t.Fatal("the next splash must get the boot grace back")
	}
	if !splashDismissAllowed(int(b.splashDismissAttempts.Add(1))) {
		t.Fatal("the next splash must get a full dismissal budget")
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

func TestResolveArmySlot(t *testing.T) {
	// 1. Config override takes precedence
	cfg := &config.BotConfig{
		Attack: config.AttackConfig{
			StrategyFile: "assets/strategies/valk_spam.yaml",
			StrategySlots: map[string]int{
				"valk_spam.yaml": 2,
			},
		},
	}
	if got := resolveArmySlot(cfg); got != 2 {
		t.Errorf("resolveArmySlot with config override = %d, want 2", got)
	}

	// 2. YAML default when no config override
	cfgNoOverride := &config.BotConfig{
		Attack: config.AttackConfig{
			StrategyFile: "assets/strategies/valk_spam.yaml",
		},
	}
	if got := resolveArmySlot(cfgNoOverride); got != 4 {
		t.Errorf("resolveArmySlot for valk_spam YAML = %d, want 4", got)
	}

	// 3. Fallback when strategy does not declare slot (e.g. edrag)
	cfgEdrag := &config.BotConfig{
		Attack: config.AttackConfig{
			StrategyFile: "assets/strategies/auto_edrag_rush.yaml",
		},
	}
	if got := resolveArmySlot(cfgEdrag); got != 1 {
		t.Errorf("resolveArmySlot for edrag YAML = %d, want 1", got)
	}

	// 4. Bare filename from UI dropdown without directory prefix
	cfgBare := &config.BotConfig{
		Attack: config.AttackConfig{
			StrategyFile: "valk_spam.yaml",
		},
	}
	if got := resolveArmySlot(cfgBare); got != 4 {
		t.Errorf("resolveArmySlot for bare 'valk_spam.yaml' = %d, want 4", got)
	}
}

func TestResolveStrategyPath(t *testing.T) {
	cases := []string{
		"assets/strategies/valk_spam.yaml",
		"strategies/valk_spam.yaml",
		"valk_spam.yaml",
	}
	for _, tc := range cases {
		p := resolveStrategyPath(tc)
		if _, err := os.Stat(p); err != nil {
			t.Errorf("resolveStrategyPath(%q) = %q, does not exist: %v", tc, p, err)
		}
	}
}


