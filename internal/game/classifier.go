package game

import (
	"image"
	"math"
	"sort"
	"sync"
	"time"

	"github.com/Ducky705/ClashGO/internal/vision"
	"github.com/rs/zerolog"
	"gocv.io/x/gocv"
)

type Classifier struct {
	cfg       ClassifierConfig
	cal       *Calibration
	rules     []StateRule
	rec       *Recognizer
	templates *TemplateStore
	logger    zerolog.Logger

	pending GameState
	confirm int
	mu      sync.Mutex
}

func NewClassifier(cal *Calibration, cfg ClassifierConfig, logger zerolog.Logger) *Classifier {
	c := &Classifier{
		cfg:    cfg,
		cal:    cal,
		rec:    NewRecognizer(),
		logger: logger.With().Str("component", "classifier").Logger(),
	}
	c.buildRules()
	return c
}

func (c *Classifier) GetRules() []StateRule {
	return c.rules
}

func (c *Classifier) SetTemplates(ts *TemplateStore) {
	c.templates = ts
}

func (c *Classifier) ClassifyState(screen gocv.Mat) (GameState, int) {
	if screen.Cols() < 1 || screen.Rows() < 1 {
		return StateUnknown, 0
	}
	if screen.Cols() < 600 || screen.Rows() < 500 {
		return StateUnknown, 0
	}

	// evaluateRules may stop early: rules are stored highest-priority-first, so
	// once the best match outranks every rule still to come the answer cannot
	// change (the score tie-break only applies within one priority level). On a
	// live village — or any frame with a clear winner — the rest of the rules
	// are skipped, and a rule has to pass its pixel evidence before its template
	// is matched, so those are exactly the rules that would have paid for a
	// full-frame, 3-scale template match.
	var best scoredState
	found := false
	for _, v := range c.evaluateRules(screen, false, true) {
		if !v.Matched {
			continue
		}
		s := scoredState{State: v.State, Score: v.Score, Priority: v.Priority}
		if !found || betterState(s, best) {
			best, found = s, true
		}
	}

	if !found {
		c.logger.Trace().Msg("no states detected")
		return StateUnknown, 0
	}

	c.logger.Trace().
		Str("state", best.State.String()).
		Int("score", best.Score).
		Msg("top state detected")

	return best.State, best.Score
}

// betterState reports whether a should beat b under the classifier's ordering:
// higher priority wins, then higher score. This is the relation ClassifyState
// used to express with a sort of every matched state, kept in one place so the
// early-exit loop and the score comparison cannot drift apart.
//
// An exact tie keeps the incumbent, i.e. the rule that comes first in the
// priority-sorted rule list. The previous sort+pick had no tie rule at all and
// returned whichever of the equal states the sort happened to place first.
func betterState(a, b scoredState) bool {
	if a.Priority != b.Priority {
		return a.Priority > b.Priority
	}
	return a.Score > b.Score
}

// canStopEarly reports whether rule evaluation may stop after index i, given
// the priority of the best match seen so far. rules is sorted by descending
// priority (buildRules), so a leader that outranks rules[i+1] outranks every
// later rule too.
func canStopEarly(rules []StateRule, i int, bestPriority int) bool {
	return i+1 >= len(rules) || rules[i+1].Priority < bestPriority
}

// CheckResult is one colour probe of a rule: where it was authored, where the
// live geometry put it, what colour it wanted, what the frame actually has
// there, and whether that satisfies the tolerance.
//
// It exists so a debugging tool can say "this probe landed 30px above the
// button face on a panel that is dimmed" instead of "the classifier says
// Unknown".
type CheckResult struct {
	X, Y      int // probe in reference geometry
	SX, SY    int // probe after the rule's anchor mapping
	Want      [3]int
	Got       [3]int
	Tolerance int
	Distance  float64
	Passed    bool
	Skipped   bool // mapped off-screen, so it neither passed nor failed
}

// RuleVerdict is how one state rule scored against a frame.
//
// Matched, Score and Priority are exactly what ClassifyState uses to pick a
// state; Passed/Required are the evidence bar that was actually applied (the
// bar is not always the authored MinPass: at a non-reference geometry a
// multi-probe rule needs a second probe).
type RuleVerdict struct {
	State        GameState
	Matched      bool
	Score        int
	Priority     int
	PixelOK      bool
	Passed       int
	Required     int
	DeclaredMin  int
	Checks       int
	Skipped      int
	Template     string
	TemplateOK   bool
	TemplateConf float64
	Detail       []CheckResult
}

// EvaluateRules scores every state rule against a frame and returns one
// verdict per rule, in rule order. Pass detail to also collect the per-probe
// samples, which is what makes a misclassification explainable.
//
// It evaluates the FULL rule list, because that is what a diagnostic report
// needs. ClassifyState shares the per-rule scoring (evaluateRules) but stops
// as soon as one state is unbeatable, so a tool asking "what did the classifier
// consider" gets every rule while the capture loop pays least — and because
// both go through the same scorer, a tool cannot silently drift from the
// classifier it is trying to explain. The first version of `cmd/see -why`
// re-derived the rules and reported "the classifier should have matched" for
// frames the classifier was right to reject, because it did not know the
// effective evidence bar.
func (c *Classifier) EvaluateRules(screen gocv.Mat, detail bool) []RuleVerdict {
	return c.evaluateRules(screen, detail, false)
}

// evaluateRules scores the state rules against a frame, in rule order.
//
// ClassifyState and EvaluateRules both go through it, which is what keeps a
// diagnostic tool honest about the classifier it is explaining. With earlyExit
// set and only one winner possible, the returned slice may be SHORTER than the
// rule list: it stops at the rule that made the answer final, so a caller must
// not read it as a complete per-rule report (that is what detail + earlyExit
// false is for).
func (c *Classifier) evaluateRules(screen gocv.Mat, detail, earlyExit bool) []RuleVerdict {
	verdicts := make([]RuleVerdict, 0, len(c.rules))
	bestPriority := -1
	for i, rule := range c.rules {
		passed := 0
		skipped := 0
		var detailChecks []CheckResult
		for _, chk := range rule.Checks {
			// Reference coordinates -> live geometry. A HUD rule's probe sits on
			// a widget the game anchors to the screen, so each axis follows the
			// edge (or the viewport centre) that widget is anchored to; an
			// overlay/world rule's probe follows the viewport centre on both
			// axes. Both are the identity at the reference geometry (see
			// Calibration.Hud / Centre and docs/RESOLUTION.md).
			sx, sy := c.cal.AnchorPoint(chk.X, chk.Y, rule.Anchor)
			if sx < 0 || sy < 0 || sx >= screen.Cols() || sy >= screen.Rows() {
				skipped++
				if detail {
					detailChecks = append(detailChecks, CheckResult{
						X: chk.X, Y: chk.Y, SX: sx, SY: sy,
						Want:      [3]int{int(chk.R), int(chk.G), int(chk.B)},
						Tolerance: chk.Tolerance, Skipped: true,
					})
				}
				continue
			}

			b := screen.GetUCharAt(sy, sx*3)
			g := screen.GetUCharAt(sy, sx*3+1)
			r := screen.GetUCharAt(sy, sx*3+2)

			dr := absDiff(int(r), int(chk.R))
			dg := absDiff(int(g), int(chk.G))
			db := absDiff(int(b), int(chk.B))
			dist := math.Sqrt(float64(dr*dr + dg*dg + db*db))

			hit := dist <= float64(chk.Tolerance)
			if hit {
				passed++
			}
			if detail {
				detailChecks = append(detailChecks, CheckResult{
					X: chk.X, Y: chk.Y, SX: sx, SY: sy,
					Want:      [3]int{int(chk.R), int(chk.G), int(chk.B)},
					Got:       [3]int{int(r), int(g), int(b)},
					Tolerance: chk.Tolerance, Distance: dist, Passed: hit,
				})
			}
		}

		// Evidence bar. At the reference geometry MinPass is exact: the probes
		// were tuned there as a 1-pixel fingerprint. Elsewhere they are mapped
		// predictions, and a rule with several probes and MinPass 1 fires off a
		// single accidental colour hit on UI it was never authored against —
		// measured on live village frames, one ObstacleDialog probe passing at
		// 1920x1080 was enough to outrank the village. Requiring a second probe
		// can only remove false positives; a single-probe rule keeps its bar,
		// because those are full-screen states (loading) that must stay
		// detectable. See docs/RESOLUTION.md.
		minPass := rule.MinPass
		if minPass == 1 && len(rule.Checks) > 1 && !c.cal.IsReferenceGeometry() {
			minPass = 2
		}

		totalScore := 0
		pixelPassed := false
		if minPass > 0 {
			if passed >= minPass {
				totalScore = passed * 100
				pixelPassed = true
			}
		} else {
			// No pixel requirements
			pixelPassed = true
		}

		templatePassed := false
		bestConf := 0.0
		// Optimization: Only run template matching if pixel checks pass (if any)
		// Or if the rule has no pixel checks (pixelPassed will be true).
		if rule.Template != "" && c.templates != nil && pixelPassed {
			tpl, ok := c.templates.Get(rule.Template)
			if ok && tpl.Cols() > 1 && tpl.Rows() > 1 {
				// Match on the RAW frame with the templates scaled by the
				// display scale k: the reference templates were captured at
				// k == 1, and a non-reference geometry renders HUD chrome k
				// times larger. Sweeping ±10% around k is exactly the legacy
				// 0.9-1.1 sweep when k == 1, so the reference geometry keeps
				// its calibrated thresholds — and it removes the per-frame
				// ResizeToHeight(732) that used to precede this match.
				// The cached variant builds the scaled template Mats once per
				// (name, window) and reuses them for the rest of the session.
				// Search the button's authored window, not the whole frame: the
				// cost of this match is proportional to the searched area, and
				// the window is the one the tap paths already use for the same
				// button (see buttons.go). A template with no authored window
				// keeps the full-frame search.
				roi := RuleSearchROI(c.cal, rule.Template, rule.Anchor, image.Rect(0, 0, screen.Cols(), screen.Rows()))
				k := c.cal.DisplayScale()
				matches, err := vision.MatchMultiScaleROICached(
					screen, tpl, rule.Template,
					k*0.9, k*1.1, 3, c.cfg.TemplateThreshold,
					roi,
				)
				if err == nil && len(matches) > 0 {
					bestConf = matches[0].Confidence
					templatePassed = true
				}
			}
		}

		matched := false
		if pixelPassed && (passed > 0 || templatePassed) {
			totalScore += int(bestConf * 1000)
			totalScore += rule.Weight // Add weight exactly once
			matched = true
		}

		verdict := RuleVerdict{
			State:        rule.State,
			Matched:      matched,
			Score:        totalScore,
			Priority:     rule.Priority,
			PixelOK:      pixelPassed,
			Passed:       passed,
			Required:     minPass,
			DeclaredMin:  rule.MinPass,
			Checks:       len(rule.Checks),
			Skipped:      skipped,
			Template:     rule.Template,
			TemplateOK:   templatePassed,
			TemplateConf: bestConf,
			Detail:       detailChecks,
		}
		verdicts = append(verdicts, verdict)

		// The stop check runs after EVERY rule, not only a matching one: rules
		// that share the leader's priority still have to be scored (the score
		// decides between them), but a rule that does not match at all cannot
		// change the leader — so once the leader's priority group has been
		// passed, the rest of the list is dead weight.
		if earlyExit {
			if verdict.Matched && verdict.Priority > bestPriority {
				bestPriority = verdict.Priority
			}
			if bestPriority >= 0 && canStopEarly(c.rules, i, bestPriority) {
				break
			}
		}
	}
	return verdicts
}

func (c *Classifier) ConfirmState(state GameState) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	if state == c.pending {
		c.confirm++
	} else {
		c.pending = state
		c.confirm = 1
	}

	return c.confirm >= c.cfg.ConfirmFrames
}

func (c *Classifier) ResetConfirm() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pending = StateUnknown
	c.confirm = 0
}

func (c *Classifier) ForceState(state GameState) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pending = state
	c.confirm = c.cfg.ConfirmFrames
}

type scoredState struct {
	State    GameState
	Score    int
	Priority int
}

func (c *Classifier) buildRules() {
	// Rule anchors: AnchorEdge (the default, omitted below) is HUD chrome that
	// is laid out against the nearest screen edge — the village's top-right
	// storage icons and bottom-left Attack! button, the in-battle resource
	// icons and End Battle / Next buttons, the builder-base indicator. Those
	// anchors keep their authoring geometry at any display size.
	//
	// AnchorCenter is centred content: full-screen art (loading, splash,
	// search-map clouds), the battle-result panel and its Return Home button,
	// and every dialog/overlay — all of which are laid out around the
	// viewport centre, so their anchors follow the centre. Marker order and
	// the per-rule reasoning live in docs/RESOLUTION.md.
	baseRules := []StateRule{
		{
			State:    StateLoading,
			Priority: 99,
			Weight:   99,
			Desc:     "loading screen",
			Anchor:   AnchorCenter,
			MinPass:  1,
			Checks: []PixelCheck{
				{324, 499, 0xCB, 0xCD, 0xD3, 15},
			},
		},
		// StateChestReward is the post-attack event-reward chest screen.
		//
		// Detection is TEMPLATE-ONLY: the hammer icon + "TAP TO OPEN"
		// prompt vanish the moment the chest breaks, and never appear
		// on a normal village screen.  Pixel-based checks were tried
		// but the bottom-center dark band (the TAP TO OPEN shadow)
		// appears in several other CoC overlays and the village
		// itself, causing false positives.  Priority 94 sits just
		// below the obstruction modals (GemDialog=96,
		// ObstacleDialog=95).
		{
			State:    StateChestReward,
			Priority: 94,
			Weight:   94,
			Desc:     "post-attack reward chest screen (hammer template only)",
			MinPass:  0,
			Template: "hammer",
		},
		{
			State:    StateSearchMap,
			Priority: 98,
			Weight:   98,
			Desc:     "search map - clouds",
			Anchor:   AnchorCenter,
			MinPass:  1,
			Checks: []PixelCheck{
				{290, 366, 0xFF, 0xFF, 0xFF, 30},
				{135, 204, 0xEE, 0xF5, 0xFF, 30},
				{405, 509, 0xEE, 0xF5, 0xFF, 30},
				{38, 603, 0x0A, 0x22, 0x3F, 25},
			},
		},
		// StateTapToContinue is the post-boot "ТАР!" (tap-to-continue /
		// collect) splash that Clash shows right after the game relaunches.
		// It is a near-black screen with a large red/orange artwork and a
		// beige "ТАР!" prompt near the top; tapping the prompt text dismisses
		// it into the game. Previously the classifier misread the orange art
		// as StateBattle (1/9 pixels) which made the stuck-watchdog
		// force-stop the game in an endless restart loop.
		//
		// Signature: beige tap text at (450,195), dark near-black corners,
		// red/orange artwork at (430,300). Priority 97 sits above the
		// obstruction modals so the splash is never misread as Battle (90)
		// or MainVillage (60).
		{
			State:    StateTapToContinue,
			Priority: 97,
			Weight:   110,
			Desc:     "post-boot tap-to-continue / collect splash (ТАР!)",
			Anchor:   AnchorCenter,
			// NOTE: shares priority 97 with StateWelcomeBack. They cannot
			// co-fire in practice (WelcomeBack needs the red banner +
			// btn_okay template; the splash has neither), and a splash
			// winning the tie is the desired outcome.
			MinPass: 2,
			Checks: []PixelCheck{
				// Beige "ТАР!" prompt text (two points inside the glyphs,
				// sampled at 0 distance on the live splash; the strongest
				// discriminator — never present on the village)
				{414, 182, 0xE1, 0xCF, 0xBC, 45},
				{450, 185, 0xE2, 0xD0, 0xBC, 45},
				// Dark near-black top-left corner (no village HUD)
				{40, 40, 0x14, 0x0D, 0x1E, 30},
			},
		},
		// StateNewsSplash is the post-boot news/announcement screen (e.g. the
		// "Meteor Golem" troop intro) with a light-green Continue button at
		// bottom-center. Tapping Continue dismisses it into the village.
		// It previously read as StateUnknown, so the bot neither dismissed it
		// nor waited — the stuck-watchdog eventually force-restarted the game.
		// Priority 95 sits just below the obstruction modals and above Battle.
		{
			State:    StateNewsSplash,
			Priority: 95,
			Weight:   100,
			Desc:     "post-boot news splash with Continue button",
			Anchor:   AnchorCenter,
			// NOTE: shares priority 95 with StateObstacleDialog. They
			// cannot co-fire (ObstacleDialog needs the light-gray dialog
			// pixel at (324,499) + white corner + green button; the dark
			// news splash has none of those).
			MinPass: 2,
			Checks: []PixelCheck{
				// Light-green Continue button. Measured live at 720p:
				// (226,249,138) — d=39.1 against the authored ±40, one drift
				// from dropping the splash's strongest hit. ±55 covers it;
				// result overlays read d=67.9 at this point and battle frames
				// farther, so the widened band has no false-positive surface.
				{403, 535, 0xBE, 0xEA, 0x8C, 55},
				// Button top highlight (near-white)
				{403, 525, 0xFD, 0xFF, 0xF6, 25},
				// Pink/purple announcement art (center)
				{430, 200, 0xFE, 0xCA, 0xFF, 50},
			},
		},
		// StateLogo is the CoC castle logo / connecting splash shown while
		// the game establishes its session after the tap-to-continue screen.
		// It is static for 1-3 minutes (no progress indicator); classifying it
		// keeps the stuck-watchdog from force-restarting mid-boot.
		{
			State:    StateLogo,
			Priority: 92,
			Weight:   92,
			Desc:     "CoC castle logo / connecting splash (fallback; observed castle-like frames were actually the news splash)",
			Anchor:   AnchorCenter,
			MinPass:  2,
			// 720p probe audit (2026-09-23): the dark-corner probe read d=28
			// on battle frames (vs d=8.2 on the boot-logo fixture) — one drift
			// from pairing with a second hit and misreading a battle as the
			// boot logo. ±22 keeps the fixture hit (13.8 of margin) and pushes
			// the battle read 6 units clear.
			Checks: []PixelCheck{
				// Pink/red logo art (center)
				{430, 220, 0xFF, 0x74, 0xD0, 60},
				// Light logo band
				{430, 320, 0xFF, 0xE7, 0xE0, 50},
				// Dark left background. ±35 read d=28 on battle frames' dark
				// ground (vs d=8.2 on the boot-logo fixture) — one lucky second
				// hit from misreading a battle as the boot logo; ±22 keeps the
				// fixture hit 13.8 clear and pushes the battle read 6 clear.
				{60, 400, 0x2F, 0x1A, 0x0E, 22},
			},
		},
		{
			State:    StateWelcomeBack,
			Priority: 97,
			Weight:   110,
			Desc:     "welcome back chief popup",
			Anchor:   AnchorCenter,
			Template: "btn_okay",
			MinPass:  1,
			Checks: []PixelCheck{
				// Red banner top
				{430, 235, 0xCB, 0x14, 0x11, 30},
				{330, 235, 0xCB, 0x14, 0x11, 30},
				{530, 235, 0xCB, 0x14, 0x11, 30},
			},
		},
		{
			State:    StateGemDialog,
			Priority: 96,
			Weight:   100,
			Desc:     "gem purchase popup",
			Anchor:   AnchorCenter,
			MinPass:  3,
			Checks: []PixelCheck{
				// Original: 608,240 @ 1280x720 -> ref 860x732
				{410, 244, 0xEB, 0x16, 0x17, 15},
				{411, 250, 0xCD, 0x16, 0x1A, 15},
				{421, 250, 0xCE, 0x15, 0x19, 15},
			},
		},
		{
			State:    StateObstacleDialog,
			Priority: 95,
			Weight:   95,
			Desc:     "blocking dialog (obstacle sheet: title band, body, action row)",
			// A bottom sheet, not a centred overlay — see AnchorCenterBottom.
			// The previous anchors were a light-gray body pixel, a near-white
			// pixel at ref (272,11) that maps off the top of a wider frame, and a
			// green button face; with the centre anchor the rule never fired at
			// 1280x720, so the modal stayed open and swallowed every later tap.
			// These three were measured by opening the dialog and resizing the
			// device with it on screen, then keeping only points that are (a)
			// identical at both geometries under this anchor, (b) locally uniform
			// at both, and (c) at least 250 colour units from the same point on a
			// clean village frame.
			Anchor:  AnchorCenterBottom,
			MinPass: 2,
			Checks: []PixelCheck{
				// Pale-yellow glow strip behind the obstacle title
				// (ref (435,543) = (255,255,183) at both geometries, exactly)
				{435, 543, 0xFF, 0xFF, 0xB7, 40},
				// Cream sheet body above the action row
				// (ref (416,578) = (244,251,218) / 720p (238,237,219))
				{416, 578, 0xF4, 0xFB, 0xDA, 40},
				// Bright cyan accent on the sheet’s right side
				// (ref (452,574) = (24,206,255) / 720p (26,198,255))
				{452, 574, 0x18, 0xCE, 0xFF, 45},
			},
		},
		{
			State:    StateBattle,
			Priority: 90,
			Weight:   90,
			Desc:     "matchmaking or live battle",
			Template: "btn_next",
			MinPass:  1,
			//
			// Probe evidence at 720p (measured on three independent live
			// battle frames, 2026-09-23 soak + dev run): the two loot icons
			// are the only strong evidence at that geometry (gold d≈6,
			// elixir d≈35), and the End Battle button face renders at live
			// y 549-558 — the authored probes map to y 529/505/416 and miss
			// it by 20-113 px. The 720p block below restores that evidence.
			Checks: []PixelCheck{
				// Gold Icon Yellow (Top Left) - handles bright and dark gold colors
				{35, 85, 0xB2, 0x8D, 0x07, 50},
				{35, 85, 0xFF, 0xC5, 0x09, 50},

				// Elixir Icon Purple (Top Left) - handles bright and dark purple
				// colors. The fill brightens as the loot counter counts up:
				// measured live at (214,26,255) empty and (252,74,255) full —
				// a colour distance of 61 from the original ±50 band, which
				// left the rule at 1/9 during the count-up and stalled the
				// classifier through two whole battles. ±65 covers the range
				// (empty d=0, full d=35.4) with headroom for the counting
				// frames in between.
				{35, 115, 0x9C, 0x17, 0xB2, 50},
				{35, 115, 0xD6, 0x1A, 0xFF, 65},

				// End Battle (Red) - typical locations (including double-row/shifted)
				{34, 588, 0xAD, 0x09, 0x0F, 50},
				{67, 570, 0xCE, 0x0D, 0x0E, 50},
				{112, 408, 0xCE, 0x0D, 0x0E, 50},

				// Next Button (Orange/Yellow)
				{813, 509, 0xFC, 0xBA, 0x36, 50},
				{796, 564, 0xFC, 0xBA, 0x36, 50},

				// --- 1280x720 probes (measured 2026-09-23 on live battle
				// frames). At 720p the End Battle button renders its red face
				// at live y 549-558; the reference probes above are mapped
				// y≤529 (bottom-anchored: ref 588 → live 529) and land on the
				// white label text instead. These reference coords are chosen
				// so the same bottom-anchored mapping (live y = 720 − k·(732−ref_y),
				// live x = k·ref_x for the left-column band x<103, with the
				// pinned k=1.325) lands on the measured face: ref 608 → live
				// (89,556), ref 40 → (53,556), ref 603 → (89,549). The face
				// colour is (206-213,13,14) across all three frames.
				{67, 608, 0xCE, 0x0D, 0x0E, 40}, // live (89,556)
				{40, 608, 0xCE, 0x0D, 0x0E, 40}, // live (53,556)
				{67, 603, 0xCE, 0x0D, 0x0E, 40}, // live (89,549)
			},
		},
		// StateBattleEnd previously matched on the btn_return_home template
		// ALONE (MinPass 0). That was dangerously loose: the "Connection
		// lost" dialog also renders a RETURN HOME button, and dim village
		// frames can weakly match the template, so the bot believed it was
		// on a battle-result screen while actually sitting on a village or
		// a disconnect dialog — tapping dead coordinates forever (observed
		// live: 18:26 boot → 18:28 ReturnHome fallback → 18:33 emergency
		// restart loop). The pixel anchors below are the battle-result
		// panel's light-blue trophy band and orange star row — decorations
		// that exist only on a genuine result screen.
		{
			State:    StateBattleEnd,
			Priority: 88,
			Weight:   88,
			Desc:     "battle result stars (template + result-panel pixels)",
			Anchor:   AnchorCenter,
			Template: "btn_return_home",
			MinPass:  2,
			Checks: []PixelCheck{
				// Opaque gold band across the panel (the star/bonus row),
				// sampled live on both flanks because the centre column is
				// the damage banner. Panel colours are translucent over the
				// battlefield, so the probes sit on this band rather than on
				// the panel body — the old body probes (white header, light
				// blue sub-band) read dark grass on a defeat overlay and the
				// rule never fired, hanging the battle-end wait.
				{300, 240, 0xED, 0xCE, 0x5E, 45},
				{560, 240, 0xF0, 0xD4, 0x70, 45},
				// RETURN HOME button face (green). The connection-lost
				// dialog has this button too, so it can satisfy this probe
				// but not the gold band above — the 2-of-3 bar is what
				// keeps the two apart.
				{431, 600, 0x6C, 0xBB, 0x1F, 15},

				// --- 1280x720 probes (measured 2026-09-18 on a live defeat
				// overlay). The three probes above are reference-geometry
				// measurements; at 720p the centre mapping lands them off the
				// features they were authored against: the gold band renders
				// 30 px lower and darker (a translucent panel over a dimmed
				// battlefield) and the button face 24 px lower, so a finished
				// battle classified as Unknown and WaitForBattleEndCtx spun on
				// its 4-minute deadline logging "stall detected but End Battle
				// button not visible" — the battle never ended and the session
				// could not return home. These coordinates are the 720p
				// features expressed in reference space (the inverse of the
				// centre mapping at the pinned display scale k=1.325), so the
				// mapping lands them on the measured pixels.
				//
				// Deliberately NO second green probe: the post-battle Star
				// Bonus popup renders a green button in the same place, and a
				// second green would let that popup reach the 2-probe bar on
				// its own. With one green, the popup scores 1 of 6 and a real
				// result overlay scores 3 of 6 (two golds + the green).
				{249, 223, 0xFF, 0xE7, 0x63, 45}, // live (400,171) gold band, left flank
				{521, 265, 0xED, 0xCE, 0x6A, 45}, // live (761,226) gold band, right flank
				{400, 577, 0x6C, 0xBB, 0x1F, 15}, // live (600,640) RETURN HOME button face
			},
		},
		// StateConnectionLost is the game's disconnect dialog ("Connection
		// lost / You have lost connection with the server...") with TRY
		// AGAIN and RETURN HOME buttons. Previously it had NO rule and was
		// misread as StateBattleEnd (the RETURN HOME button satisfied that
		// rule's template-only match), leaving the bot tapping result-screen
		// coordinates forever. The dialog dims everything behind it, so its
		// dark-panel pixels double as the discriminator against a real
		// result screen.
		// StateConfirmExit is CoC's "Do you want to quit the game?" dialog
		// with Cancel (orange) and Okay (green) buttons. It appears when the
		// app's Back button is pressed on the main village — which a
		// misclassified ArmyCamp frame used to trigger (see the ArmyCamp
		// guard in processFrame). Previously undetected: the bot sat on the
		// dialog for the full boot-splash grace (5 min) then force-
		// restarted, every cycle. Panel pixel = the dialog's light-gray
		// body; both buttons must match (real villages have neither).
		{
			State:    StateConfirmExit,
			Priority: 99,
			Weight:   99,
			Desc:     "quit confirm dialog (Cancel / Okay)",
			Anchor:   AnchorCenter,
			MinPass:  2,
			Checks: []PixelCheck{
				// Green Okay button
				{497, 431, 0xD6, 0xF4, 0x76, 45},
				// Orange Cancel button
				{279, 429, 0xFE, 0xC3, 0x69, 45},
				// Light-gray dialog body between the texts
				{430, 340, 0xE8, 0xE8, 0xE0, 25},
			},
		},
		{
			State:    StateConnectionLost,
			Priority: 98,
			Weight:   98,
			Desc:     "connection lost dialog (TRY AGAIN / RETURN HOME)",
			Anchor:   AnchorCenter,
			MinPass:  2,
			Checks: []PixelCheck{
				// TRY AGAIN button text (light blue-gray)
				{300, 478, 0xCB, 0xE6, 0xFF, 40},
				// RETURN HOME button text (same light blue-gray)
				{431, 581, 0xCB, 0xE6, 0xFF, 45},
				// Dimmed dark panel between the two texts
				{430, 520, 0x1A, 0x1C, 0x1E, 20},

				// --- 1280x720 probes (measured 2026-09-21 on a live
				// lost-connection dialog, corpus/connection_lost_720p.png).
				//
				// This dialog does NOT scale with k: its panel is drawn at a
				// fixed pixel size, so the three reference probes above map
				// onto pixels the dialog never occupies at 720p (the dialog
				// is ~240 live px tall versus the ~348 the k map predicts,
				// so its TRY AGAIN button renders 27 px above where the
				// reference anchor maps). The rule scored 1 of 3 there — just
				// the dark panel — so a real disconnect read as Unknown and
				// WaitForBattleEndCtx sat out its whole deadline on a battle
				// whose result would never arrive, then restarted the game
				// (observed live: a 17:08 attack stalled from 17:12 to the
				// 18:03 deadline with the dialog on screen the entire time).
				//
				// Both probes are glyph strokes inside the panel, chosen
				// strictly inside a thick stroke (the eroded bright mask) so
				// they cannot land on inter-letter background. Coordinates
				// are the measured live pixels expressed in reference space
				// (inverse of the centre mapping at k=1.325), so the mapping
				// returns them to the measured pixels: live (472,473) for the
				// TRY AGAIN text, live (541,258) for the "Connection lost"
				// title.
				//
				// Verified across all sixteen 720p fixtures the corpus and
				// the online runs have produced: only the dialog itself
				// reaches the 2-probe bar (2 of 5); the battle frames, the
				// village, the boot logo, the result overlay (1 of 5, the
				// panel) and the Star Bonus popup all stay below it.
				{303, 451, 0xCB, 0xE6, 0xFF, 35}, // live (472,473) TRY AGAIN text
				{354, 289, 0xF0, 0xF0, 0xF3, 35}, // live (541,258) connection-lost title
			},
		},
		{
			State:    StateArmyCamp,
			Priority: 85,
			Weight:   85,
			Desc:     "army overview tab open",
			Anchor:   AnchorCenter,
			// MinPass was 1, but the brown pixel check at (479,149) also
			// passes on ordinary main-village frames (observed live:
			// village (479,149) = RGB(61,53,62), within tolerance of
			// 0x4D3E33), misclassifying the village as ArmyCamp. That
			// made processFrame press Back — which on the real village
			// opens the quit-confirm dialog — the stuck loop this
			// classifier change is part of fixing. Both anchors are the
			// army-overview tab header (red + brown side by side); a
			// genuine camp always shows both.
			MinPass: 2,
			Checks: []PixelCheck{
				{529, 149, 0xF1, 0x55, 0x4F, 25},
				{479, 149, 0x4D, 0x3E, 0x33, 25},
			},
		},
		{
			State:    StateShieldInfo,
			Priority: 80,
			Weight:   80,
			Desc:     "shield info overlay",
			Anchor:   AnchorCenter,
			MinPass:  1,
			Checks: []PixelCheck{
				{455, 158, 0xFF, 0x8D, 0x95, 15},
			},
		},
		{
			State:    StateChatOpen,
			Priority: 75,
			Weight:   75,
			Desc:     "chat tab visible",
			Anchor:   AnchorCenter,
			MinPass:  2,
			Checks: []PixelCheck{
				{264, 295, 0xF3, 0xAB, 0x28, 15},
				{264, 316, 0xFF, 0xFF, 0xFF, 15},
				{264, 341, 0xEA, 0x8A, 0x3B, 15},
			},
		},
		{
			State:    StateBuilderBase,
			Priority: 65,
			Weight:   65,
			Desc:     "builder base indicator",
			MinPass:  1,
			Checks: []PixelCheck{
				{565, 16, 0xFF, 0xFF, 0x47, 15},
			},
		},
		{
			State:    StateMainVillage,
			Priority: 60,
			Weight:   60,
			Desc:     "main village - builder info icon or attack button",
			Template: "btn_attack",
			MinPass:  1,
			// 720p probe audit (2026-09-23, live corpus frames): at 1280x720
			// this rule was carried by the two bright storage icons alone
			// (gold d=2.2, elixir d=17.6) — every Attack-button probe missed —
			// and the dark-gold variant sat at d=35 on BATTLE frames' dimmed
			// top-right counter, 5 units from firing MainVillage mid-battle.
			// The dark variants are therefore tightened (they hit nothing in
			// any corpus frame, at either geometry) and a measured 720p
			// Attack-face probe is added (live (54,617), d=0 on both village
			// fixtures, ~200 from any battle frame).
			Checks: []PixelCheck{
				// Gold storage icon (Top Right) - dark and bright versions.
				// Dark: ±40 read d=35 on battle screens; ±30 keeps the band
				// (no corpus frame, reference or 720p, sits between 30 and 40).
				{830, 35, 0xB2, 0x90, 0x0F, 30},
				{830, 35, 255, 208, 22, 40},

				// Elixir storage icon (Top Right) - dark and bright versions.
				// Dark: battle frames read d=46.4, only 6.4 outside ±40; ±35
				// widens the margin to 11.4.
				{830, 95, 0x54, 0x19, 0x59, 35},
				{830, 95, 125, 37, 127, 40},

				// Attack button orange/brown (Bottom Left) - supports Y=640 and Y=700 layouts.
				// The first probe maps exactly onto the measured face at 720p
				// (live (53,598)) — only its colour was stale: the face reads
				// (248,179,77) there, not (175,129,57), so it missed by 90.7
				// and left the two storage icons carrying the whole rule.
				{40, 640, 0xF8, 0xB3, 0x4D, 40},
				{40, 700, 0x91, 0x50, 0x2E, 40},
				{40, 558, 0xFF, 0xAF, 0x00, 40},

				// 1280x720 Attack-button face (measured 2026-09-23 on two
				// live village captures, identical colour both days). The
				// bottom-anchored mapping (live y = 720 − k·(732−ref_y), live
				// x = k·ref_x for the left column, pinned k=1.325) puts ref
				// (41,654) on the measured face at (54,617); the authored
				// probes map to y 489-678 and land on the frame edge or the
				// label text. Battle frames read (17-19,32-35,46-89) at this
				// point — 200+ away.
				{41, 654, 0xEF, 0xA5, 0x4B, 40}, // live (54,617) Attack! face
			},
		},
		{
			State:    StateReturnHome,
			Priority: 50,
			Weight:   50,
			Desc:     "return home button",
			Anchor:   AnchorCenter,
			MinPass:  1,
			Checks: []PixelCheck{
				// Solid green face of the result overlay's RETURN HOME
				// button, measured live (the button is horizontally centred,
				// so this survives the centre-anchored mapping). The probe
				// was authored 141 px to the left of where the button
				// actually renders, which is why this state never fired
				// either.
				{431, 600, 0x6C, 0xBB, 0x1F, 15},
				// 1280x720 result-overlay features (see the same block on
				// StateBattleEnd). This rule is the template-free half of the
				// battle-end signal, so it must fire on the mapped geometry
				// too: the button face at 720p sits 24 px below where the
				// reference probe maps, and BattleEnd alone is not enough to
				// end a battle when its template fails to match.
				{249, 223, 0xFF, 0xE7, 0x63, 45}, // live (400,171) gold band, left flank
				{521, 265, 0xED, 0xCE, 0x6A, 45}, // live (761,226) gold band, right flank
				{400, 577, 0x6C, 0xBB, 0x1F, 15}, // live (600,640) RETURN HOME button face
			},
		},
		{
			State:    StateSettings,
			Priority: 50,
			Weight:   50,
			Desc:     "settings page",
			Anchor:   AnchorCenter,
			MinPass:  1,
			Checks: []PixelCheck{
				{556, 565, 0xFF, 0xFF, 0xFF, 10},
			},
		},
		{
			State:    StateFindMatch,
			Priority: 50,
			Weight:   50,
			Desc:     "find match button",
			Template: "btn_find_match",
			MinPass:  1,
			Checks: []PixelCheck{
				{215, 563, 0xD8, 0xA4, 0x20, 25},
			},
		},
		{
			State:    StateArmySelection,
			Priority: 86,
			Weight:   100,
			Desc:     "army selection menu - white arrow or battle button",
			Template: "btn_battle",
			MinPass:  1,
			Checks: []PixelCheck{
				// White arrow in army bar expansion
				{512, 189, 0xFF, 0xFF, 0xFF, 30},
				// Green Battle button center (REF: 725, 535)
				{725, 535, 0x88, 0xD0, 0x39, 40},
			},
		},
	}

	// Rules are kept in reference coordinates: ClassifyState maps each check
	// through the live Calibration (see StateRule.Anchor), so the authored
	// numbers stay the single source of truth for every geometry.
	c.rules = append(c.rules, baseRules...)

	sort.Slice(c.rules, func(i, j int) bool {
		return c.rules[i].Priority > c.rules[j].Priority
	})
}

func (c *Classifier) DetectWithRedArea(screen gocv.Mat, minArea int) (GameState, []image.Point) {
	state, _ := c.ClassifyState(screen)

	if state == StateBattle || state == StateMainVillage {
		pts, _ := c.findRedArea(screen, minArea)
		if len(pts) > 10 {
			return StateBattle, pts
		}
	}

	return state, nil
}

func (c *Classifier) findRedArea(screen gocv.Mat, minArea int) ([]image.Point, error) {
	blurred := gocv.NewMat()
	defer blurred.Close()
	gocv.GaussianBlur(screen, &blurred, image.Point{X: 5, Y: 5}, 0, 0, gocv.BorderDefault)

	hsv := gocv.NewMat()
	defer hsv.Close()
	gocv.CvtColor(blurred, &hsv, gocv.ColorBGRToHSV)

	lowerRed1 := gocv.NewScalar(0, 100, 100, 0)
	upperRed1 := gocv.NewScalar(10, 255, 255, 0)
	lowerRed2 := gocv.NewScalar(160, 100, 100, 0)
	upperRed2 := gocv.NewScalar(180, 255, 255, 0)

	mask1 := gocv.NewMat()
	mask2 := gocv.NewMat()
	gocv.InRangeWithScalar(hsv, lowerRed1, upperRed1, &mask1)
	gocv.InRangeWithScalar(hsv, lowerRed2, upperRed2, &mask2)
	defer mask1.Close()
	defer mask2.Close()

	var mask gocv.Mat
	gocv.BitwiseOr(mask1, mask2, &mask)
	defer mask.Close()

	kernel := gocv.GetStructuringElement(gocv.MorphRect, image.Point{X: 3, Y: 3})
	defer kernel.Close()
	gocv.MorphologyEx(mask, &mask, gocv.MorphOpen, kernel)

	contours := gocv.FindContours(mask, gocv.RetrievalExternal, gocv.ChainApproxSimple)
	defer contours.Close()

	var points []image.Point
	for i := 0; i < contours.Size(); i++ {
		area := gocv.ContourArea(contours.At(i))
		if area < float64(minArea) {
			continue
		}
		rect := gocv.BoundingRect(contours.At(i))
		points = append(points, image.Pt(rect.Min.X+rect.Dx()/2, rect.Min.Y+rect.Dy()/2))
	}

	return points, nil
}

func (c *Classifier) SetCalibration(cal *Calibration) {
	c.cal = cal
	c.rules = nil
	c.buildRules()
}

type ClassifierStats struct {
	ConfirmFrames int
	PendingState  GameState
}

func (c *Classifier) Stats() ClassifierStats {
	c.mu.Lock()
	defer c.mu.Unlock()
	return ClassifierStats{
		ConfirmFrames: c.confirm,
		PendingState:  c.pending,
	}
}

type StateClassifier interface {
	ClassifyState(screen gocv.Mat) (GameState, int)
	ConfirmState(state GameState) bool
	ResetConfirm()
	Stats() ClassifierStats
}

var _ StateClassifier = (*Classifier)(nil)

type ClassifierResult struct {
	State      GameState
	Score      int
	Confirm    bool
	ClassifyMs time.Duration
	DetectedAt time.Time
}

func (c *Classifier) ClassifyWithTiming(screen gocv.Mat) ClassifierResult {
	start := time.Now()
	state, score := c.ClassifyState(screen)
	confirmed := c.ConfirmState(state)
	return ClassifierResult{
		State:      state,
		Score:      score,
		Confirm:    confirmed,
		ClassifyMs: time.Since(start),
		DetectedAt: time.Now(),
	}
}
