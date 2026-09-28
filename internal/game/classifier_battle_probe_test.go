package game

import (
	"math"
	"testing"

	"github.com/rs/zerolog"
	"gocv.io/x/gocv"
)

// The Battle rule's probes were re-checked against live 720p battle frames
// (2026-09-23, 10-attack soak). Two measurements drove the fix:
//
//   - The elixir loot icon (ref 35,115 → live 46,152) brightens as the loot
//     counter counts up: (214,26,255) empty → (252,74,255) full. At ±50 the
//     counted-up colour sits at distance 61 — 11 units outside — and because
//     the two loot icons are the rule's only strong 720p evidence otherwise,
//     the rule fell to 1 hit vs the escalated 2-probe bar and the classifier
//     reported Unknown through two whole battles (635 Unknown waits in the
//     soak log).
//   - The End Battle button face renders at live y 549-558 at 720p, colour
//     (206-213,13,14) across three independent live frames. The authored
//     probes map to y 529/505/416 (bottom-anchored) and land on the white
//     label text or the panel above, missing by d=105-341.
//
// The fix widens the elixir tolerance to ±65 and adds three reference probes
// chosen so the same bottom-anchored mapping lands them on the measured face.

// Colours measured on live 720p battle frames (see the numbers above).
var (
	// The loot icons at 720p, both observed states.
	battleGoldBright      = [3]uint8{255, 203, 9}  // ref (35,85), live (46,113)
	battleElixirEmpty     = [3]uint8{214, 26, 255} // ref (35,115), live (46,152)
	battleElixirCountedUp = [3]uint8{252, 74, 255} // the drift that stalled the classifier
	battleElixirMidFill   = [3]uint8{239, 51, 255} // observed mid-count
	// The End Battle button face at 720p, sampled on both flanks and both rows.
	battleEndBattleFace  = [3]uint8{213, 13, 14} // live (89,556) and (53,556)
	battleEndBattleFace2 = [3]uint8{206, 13, 14} // live (89,549)

	// A village frame's grass, for the negative test: village grass at the
	// same probe points must not satisfy the End Battle face probes.
	villageGrass = [3]uint8{74, 141, 62}
)

func newBattleClassifier(t *testing.T, w, h int) (*Classifier, gocv.Mat) {
	t.Helper()
	cal := NewCalibration(w, h)
	cal.SetDisplayScale(1.325)
	return NewClassifier(cal, DefaultClassifierConfig(), zerolog.Nop()), gocv.NewMatWithSize(h, w, gocv.MatTypeCV8UC3)
}

// battleRule returns the Battle rule from the classifier's rule list.
func battleRule(t *testing.T, c *Classifier) StateRule {
	t.Helper()
	for _, r := range c.GetRules() {
		if r.State == StateBattle {
			return r
		}
	}
	t.Fatal("no Battle rule")
	return StateRule{}
}

// The exact failure frame of the soak run: gold + counted-up elixir, with the
// End Battle face. The elixir colour sits at distance 61.2 from the old ±50
// band; the rule must still classify (now via 5 probes instead of 1).
func TestClassify_BattleFiresWhenElixirCountedUp(t *testing.T) {
	c, m := newBattleClassifier(t, 1280, 720)
	defer m.Close()

	rule := battleRule(t, c)
	gold, elixir := rule.Checks[1], rule.Checks[3] // bright variants

	// Paint at the MAPPED points, because this test pins live behaviour.
	mx, my := c.cal.AnchorPoint(elixir.X, elixir.Y, rule.Anchor)
	paint(m, mx, my, battleElixirCountedUp)
	gx, gy := c.cal.AnchorPoint(gold.X, gold.Y, rule.Anchor)
	paint(m, gx, gy, battleGoldBright)

	// The End Battle face at its measured live pixels: the three 720p probes
	// are the last three checks.
	for _, chk := range rule.Checks[len(rule.Checks)-3:] {
		sx, sy := c.cal.AnchorPoint(chk.X, chk.Y, rule.Anchor)
		paint(m, sx, sy, battleEndBattleFace)
	}

	state, score := c.ClassifyState(m)
	if state != StateBattle {
		t.Fatalf("elixir counted up + End Battle face: got %s (score %d), want Battle", state, score)
	}
}

// The elixir icon's observed colour range must stay inside the widened
// tolerance at the exact mapped point: empty, mid-count, and counted-up.
func TestBattleElixirProbeCoversMeasuredRange(t *testing.T) {
	c, m := newBattleClassifier(t, 1280, 720)
	defer m.Close()

	rule := battleRule(t, c)
	var elixir PixelCheck
	for _, chk := range rule.Checks {
		if chk.R == 0xD6 && chk.G == 0x1A && chk.B == 0xFF {
			elixir = chk
			break
		}
	}
	if elixir.Tolerance < 65 {
		t.Fatalf("elixir tolerance = %d, want ≥65 to cover the measured bright-fill drift", elixir.Tolerance)
	}

	mx, my := c.cal.AnchorPoint(elixir.X, elixir.Y, rule.Anchor)
	for name, col := range map[string][3]uint8{
		"empty":      battleElixirEmpty,
		"mid-fill":   battleElixirMidFill,
		"counted-up": battleElixirCountedUp,
	} {
		probe := gocv.NewMatWithSize(720, 1280, gocv.MatTypeCV8UC3)
		paint(probe, mx, my, col)
		b := probe.GetUCharAt(my, mx*3)
		g := probe.GetUCharAt(my, mx*3+1)
		r := probe.GetUCharAt(my, mx*3+2)
		probe.Close()
		dr, dg, db := absDiff(int(r), int(elixir.R)), absDiff(int(g), int(elixir.G)), absDiff(int(b), int(elixir.B))
		dist := dr*dr + dg*dg + db*db
		limit := elixir.Tolerance * elixir.Tolerance
		if dist > limit {
			t.Errorf("%s colour (%d,%d,%d): squared distance %d exceeds tolerance² %d", name, r, g, b, dist, limit)
		}
	}
}

// The three 720p End Battle probes must land on the button face (bottom
// band y≥468 → bottom-anchored): they must map inside the measured face rows
// (live y 549-558) at the live geometry, and the rule must fire on the face
// evidence ALONE — without the loot icons — so the classifier no longer
// depends on the elixir colour at all.
func TestBattleEndBattleFaceProbesFireAlone(t *testing.T) {
	c, m := newBattleClassifier(t, 1280, 720)
	defer m.Close()

	rule := battleRule(t, c)
	faceProbes := rule.Checks[len(rule.Checks)-3:]
	for _, chk := range faceProbes {
		sy := c.cal.MapY(chk.Y, rule.Anchor)
		if sy < 549 || sy > 558 {
			t.Errorf("End Battle probe ref(%d,%d) maps to live y=%d, want within the measured face rows 549-558", chk.X, chk.Y, sy)
		}
		sx := c.cal.MapX(chk.X, rule.Anchor)
		if sx < 34 || sx > 230 {
			t.Errorf("End Battle probe ref(%d,%d) maps to live x=%d, want inside the button span", chk.X, chk.Y, sx)
		}
		paint(m, sx, sy, battleEndBattleFace2)
	}

	// No loot icons painted: the face alone must carry the rule.
	state, score := c.ClassifyState(m)
	if state != StateBattle {
		t.Fatalf("End Battle face alone: got %s (score %d), want Battle", state, score)
	}
}

// Negative control: a village frame that only has grass where the End Battle
// probes land must not classify as Battle. The mapped probes sit at live
// y≈549-556, x≈53-89 — on the village that region is the bottom-left grass
// below the Attack! button, not a red button face.
func TestBattleFaceProbesRejectVillageGrass(t *testing.T) {
	c, m := newBattleClassifier(t, 1280, 720)
	defer m.Close()

	rule := battleRule(t, c)
	for _, chk := range rule.Checks[len(rule.Checks)-3:] {
		sx, sy := c.cal.AnchorPoint(chk.X, chk.Y, rule.Anchor)
		paint(m, sx, sy, villageGrass)
	}

	if state, _ := c.ClassifyState(m); state == StateBattle {
		t.Fatal("grass at the End Battle probe points satisfied the Battle rule")
	}
}

// --- 720p probe audit (2026-09-23): MainVillage, Logo, NewsSplash ---------
//
// The same audit that found the Battle rule's fragility found three more
// rules at or near zero margin at 720p:
//
//   - MainVillage was carried by the two bright storage icons alone (the
//     three Attack-button probes missed), and its dark-gold variant sat at
//     d=35 (vs ±40) on battle frames' dimmed top-right counter — one elixir
//     drift from firing MainVillage mid-battle.
//   - Logo's dark-corner probe read d=28 (vs ±35) on battle ground vs d=8.2
//     on the boot logo — one lucky second hit from misreading a battle as
//     the boot logo (priority 92 outranks MainVillage).
//   - NewsSplash's strongest probe (the green Continue button) measured
//     d=39.1 against ±40 on its own live fixture.

// Measured on the live 720p corpus frames (village_720p, village_alt_720p,
// battle_pre_deploy_720p, battle_mid_720p, boot_logo_720p,
// village_star_bonus_720p).
var (
	// The Attack! button face at live (54,617), identical on both village
	// captures; battle frames read (17-19,32-35,46-89) at the same point.
	villageAttackFace = [3]uint8{239, 165, 75}
	// The top-right counter area on battle frames (dimmed loot counter).
	battleGoldCounter  = [3]uint8{164, 112, 13}
	battleElixirGround = [3]uint8{57, 45, 57}
	// The logo rule's dark left background: fixture (49,34,14), battle
	// ground (43,38,39).
	logoDarkFixture = [3]uint8{49, 34, 14}
	logoDarkBattle  = [3]uint8{43, 38, 39}
	// The news splash's green Continue button: fixture (226,249,138),
	// result overlay (205,206,200).
	splashGreenLive   = [3]uint8{226, 249, 138}
	splashGreenResult = [3]uint8{205, 206, 200}
)

func villageRule(t *testing.T, c *Classifier) StateRule {
	t.Helper()
	for _, r := range c.GetRules() {
		if r.State == StateMainVillage {
			return r
		}
	}
	t.Fatal("no MainVillage rule")
	return StateRule{}
}

// The measured 720p Attack-face evidence must carry MainVillage at the live
// geometry on its own, so the rule no longer depends solely on the two
// storage icons (which are the exact pixels a battle screen dims). Two face
// hits are needed: the escalated non-reference bar is 2 (MinPass 1, 8 probes).
func TestClassify_MainVillage720pFiresOnAttackFace(t *testing.T) {
	c, m := newBattleClassifier(t, 1280, 720)
	defer m.Close()

	rule := villageRule(t, c)
	hits := 0
	for _, chk := range rule.Checks {
		sx, sy := c.cal.AnchorPoint(chk.X, chk.Y, rule.Anchor)
		// Both face probes land on the measured button face: the recolored
		// (40,640) on (53,598), the added (41,654) on (54,617).
		if sy >= 590 && sy <= 620 && sx <= 60 {
			paint(m, sx, sy, villageAttackFace)
			hits++
		}
	}
	if hits < 2 {
		t.Fatalf("only %d MainVillage probes land on the measured Attack face, want ≥2 (the 720p evidence bar)", hits)
	}

	state, score := c.ClassifyState(m)
	if state != StateMainVillage {
		t.Fatalf("Attack face alone: got %s (score %d), want MainVillage", state, score)
	}
}

// The dark storage variants must not fire on a battle frame's dimmed counter:
// with only the two counter areas painted in battle colours, MainVillage must
// stay silent (before the tightening this composition passed 1 hit at d=35
// against ±40, one elixir drift from the 2-probe bar).
func TestMainVillageDarkVariantsRejectBattleCounter(t *testing.T) {
	c, m := newBattleClassifier(t, 1280, 720)
	defer m.Close()

	rule := villageRule(t, c)
	painted := map[[2]int]bool{}
	for _, chk := range rule.Checks {
		sx, sy := c.cal.AnchorPoint(chk.X, chk.Y, rule.Anchor)
		key := [2]int{sx, sy}
		if painted[key] {
			continue
		}
		painted[key] = true
		switch {
		case sy <= 130 && sx > 1200: // storage icon column
			paint(m, sx, sy, battleGoldCounter)
		case sy <= 130 && sx > 1180:
			paint(m, sx, sy, battleElixirGround)
		default: // Attack-button probes: battle ground there too
			paint(m, sx, sy, battleElixirGround)
		}
	}

	if state, _ := c.ClassifyState(m); state == StateMainVillage {
		t.Fatal("battle-frame counter colours satisfied the MainVillage rule")
	}
}

// The logo dark probe must hold the fixture colour and exclude the battle
// ground colour it used to sit 7 units from.
func TestLogoDarkProbeSeparatesFixtureFromBattleGround(t *testing.T) {
	c, _ := newBattleClassifier(t, 1280, 720)
	var dark PixelCheck
	found := false
	for _, r := range c.GetRules() {
		if r.State != StateLogo {
			continue
		}
		for _, chk := range r.Checks {
			if chk.R == 0x2F && chk.G == 0x1A && chk.B == 0x0E {
				dark = chk
				found = true
			}
		}
	}
	if !found {
		t.Fatal("logo dark-background probe not found")
	}
	if dark.Tolerance > 22 {
		t.Fatalf("logo dark tolerance = %d, want ≤22 so the battle-ground read (d=28) stays excluded", dark.Tolerance)
	}
	fixtureDist := colorDistance(logoDarkFixture, [3]int{int(dark.R), int(dark.G), int(dark.B)})
	battleDist := colorDistance(logoDarkBattle, [3]int{int(dark.R), int(dark.G), int(dark.B)})
	if fixtureDist > float64(dark.Tolerance) {
		t.Errorf("fixture colour d=%.1f exceeds tolerance %d", fixtureDist, dark.Tolerance)
	}
	if battleDist <= float64(dark.Tolerance) {
		t.Errorf("battle ground d=%.1f still within tolerance %d", battleDist, dark.Tolerance)
	}
}

// The news splash green probe must hold the measured live colour with real
// margin while the result overlay's grey stays outside.
func TestNewsSplashGreenProbeCoversLiveColour(t *testing.T) {
	c, _ := newBattleClassifier(t, 1280, 720)
	var green PixelCheck
	found := false
	for _, r := range c.GetRules() {
		if r.State != StateNewsSplash {
			continue
		}
		for _, chk := range r.Checks {
			if chk.R == 0xBE && chk.G == 0xEA && chk.B == 0x8C {
				green = chk
				found = true
			}
		}
	}
	if !found {
		t.Fatal("news splash green-button probe not found")
	}
	if green.Tolerance < 55 {
		t.Fatalf("green tolerance = %d, want ≥55 to hold the measured live d=39.1 with margin", green.Tolerance)
	}
	liveDist := colorDistance(splashGreenLive, [3]int{int(green.R), int(green.G), int(green.B)})
	resultDist := colorDistance(splashGreenResult, [3]int{int(green.R), int(green.G), int(green.B)})
	if liveDist > float64(green.Tolerance) {
		t.Errorf("live splash colour d=%.1f exceeds tolerance %d", liveDist, green.Tolerance)
	}
	if resultDist <= float64(green.Tolerance) {
		t.Errorf("result-overlay grey d=%.1f falls inside tolerance %d", resultDist, green.Tolerance)
	}
}

func colorDistance(a [3]uint8, b [3]int) float64 {
	dr := absDiff(int(a[0]), b[0])
	dg := absDiff(int(a[1]), b[1])
	db := absDiff(int(a[2]), b[2])
	return math.Sqrt(float64(dr*dr + dg*dg + db*db))
}
