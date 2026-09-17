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

	var scores []scoredState

	for _, rule := range c.rules {
		passed := 0
		for _, chk := range rule.Checks {
			// Reference coordinates -> live geometry. A HUD rule's probe sits on
			// a widget the game anchors to the screen, so each axis follows the
			// edge (or the viewport centre) that widget is anchored to; an
			// overlay/world rule's probe follows the viewport centre on both
			// axes. Both are the identity at the reference geometry (see
			// Calibration.Hud / Centre and docs/RESOLUTION.md).
			sx, sy := c.cal.AnchorPoint(chk.X, chk.Y, rule.Anchor)
			if sx < 0 || sy < 0 || sx >= screen.Cols() || sy >= screen.Rows() {
				continue
			}

			b := screen.GetUCharAt(sy, sx*3)
			g := screen.GetUCharAt(sy, sx*3+1)
			r := screen.GetUCharAt(sy, sx*3+2)

			dr := absDiff(int(r), int(chk.R))
			dg := absDiff(int(g), int(chk.G))
			db := absDiff(int(b), int(chk.B))

			if math.Sqrt(float64(dr*dr+dg*dg+db*db)) <= float64(chk.Tolerance) {
				passed++
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
				k := c.cal.DisplayScale()
				matches, err := vision.MatchMultiScaleROICached(
					screen, tpl, rule.Template,
					k*0.9, k*1.1, 3, c.cfg.TemplateThreshold,
					image.Rect(0, 0, screen.Cols(), screen.Rows()),
				)
				if err == nil && len(matches) > 0 {
					bestConf = matches[0].Confidence
					templatePassed = true
				}
			}
		}

		if pixelPassed && (passed > 0 || templatePassed) {
			totalScore += int(bestConf * 1000)
			totalScore += rule.Weight // Add weight exactly once

			scores = append(scores, scoredState{
				State:    rule.State,
				Score:    totalScore,
				Priority: rule.Priority,
			})
		}
	}

	if len(scores) == 0 {
		c.logger.Trace().Msg("no states detected")
		return StateUnknown, 0
	}

	sort.Slice(scores, func(i, j int) bool {
		if scores[i].Priority != scores[j].Priority {
			return scores[i].Priority > scores[j].Priority
		}
		return scores[i].Score > scores[j].Score
	})

	c.logger.Trace().
		Str("state", scores[0].State.String()).
		Int("score", scores[0].Score).
		Msg("top state detected")

	return scores[0].State, scores[0].Score
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
				// Light-green Continue button
				{403, 535, 0xBE, 0xEA, 0x8C, 40},
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
			Checks: []PixelCheck{
				// Pink/red logo art (center)
				{430, 220, 0xFF, 0x74, 0xD0, 60},
				// Light logo band
				{430, 320, 0xFF, 0xE7, 0xE0, 50},
				// Dark left background
				{60, 400, 0x2F, 0x1A, 0x0E, 35},
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
			Checks: []PixelCheck{
				// Gold Icon Yellow (Top Left) - handles bright and dark gold colors
				{35, 85, 0xB2, 0x8D, 0x07, 50},
				{35, 85, 0xFF, 0xC5, 0x09, 50},

				// Elixir Icon Purple (Top Left) - handles bright and dark purple colors
				{35, 115, 0x9C, 0x17, 0xB2, 50},
				{35, 115, 0xD6, 0x1A, 0xFF, 50},

				// End Battle (Red) - typical locations (including double-row/shifted)
				{34, 588, 0xAD, 0x09, 0x0F, 50},
				{67, 570, 0xCE, 0x0D, 0x0E, 50},
				{112, 408, 0xCE, 0x0D, 0x0E, 50},

				// Next Button (Orange/Yellow)
				{813, 509, 0xFC, 0xBA, 0x36, 50},
				{796, 564, 0xFC, 0xBA, 0x36, 50},
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
			Checks: []PixelCheck{
				// Gold storage icon (Top Right) - dark and bright versions
				{830, 35, 0xB2, 0x90, 0x0F, 40},
				{830, 35, 255, 208, 22, 40},

				// Elixir storage icon (Top Right) - dark and bright versions
				{830, 95, 0x54, 0x19, 0x59, 40},
				{830, 95, 125, 37, 127, 40},

				// Attack button orange/brown (Bottom Left) - supports Y=640 and Y=700 layouts
				{40, 640, 0xAF, 0x81, 0x39, 40},
				{40, 700, 0x91, 0x50, 0x2E, 40},
				{40, 558, 0xFF, 0xAF, 0x00, 40},
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
