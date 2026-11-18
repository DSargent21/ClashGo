package game

import (
	"encoding/json"
	"fmt"
	"image"
	"math"
	"os"
	"sort"
	"strconv"
	"sync"

	"github.com/Ducky705/ClashGO/internal/paths"
	"github.com/Ducky705/ClashGO/internal/vision"
	"github.com/rs/zerolog"
	"gocv.io/x/gocv"
)

type LootRecognizer struct {
	cal       *Calibration
	templates *TemplateStore
	// scale is the display scale, used for every length threshold in the
	// result-panel OCR path (glyph heights/widths, line gaps, tolerances):
	// reference distances grow with the display scale on both axes, not with a
	// per-axis framebuffer ratio.
	scale float64
	// digitTemplates holds each digit template cropped to its own ink at file
	// resolution; digitCanon holds the same glyphs resized to the canonical
	// comparison box, which is what matchDigit compares against.
	digitTemplates []gocv.Mat
	digitCanon     []gocv.Mat
	logger         zerolog.Logger
	Debug          bool

	// The result-panel layout comes from two small JSON files. A recogniser is
	// reused for every read while one result panel settles (up to
	// resultSettleAttempts reads), and the file content cannot change mid-
	// settle, so resolve it once per recogniser instead of on each read. A new
	// battle builds a new recogniser, so an on-disk edit is still picked up on
	// the next battle rather than requiring a restart.
	resultLayoutOnce sync.Once
	battleZone       image.Rectangle
	bonusZone        image.Rectangle
	starPoints       [3]image.Point
}

// lootKernels are static structuring elements reused across every readRow
// morphology pass. Built once via sync.Once.
var (
	lootKernelOnce  sync.Once
	lootKernelOpen  gocv.Mat
	lootKernelClose gocv.Mat
)

func lootKernels() (gocv.Mat, gocv.Mat) {
	lootKernelOnce.Do(func() {
		lootKernelOpen = gocv.GetStructuringElement(gocv.MorphRect, image.Point{X: 3, Y: 3})
		lootKernelClose = gocv.GetStructuringElement(gocv.MorphEllipse, image.Point{X: 5, Y: 5})
	})
	return lootKernelOpen, lootKernelClose
}

type detectedDigit struct {
	rect  image.Rectangle
	digit int
	conf  float32
}

func NewLootRecognizer(cal *Calibration, ts *TemplateStore, logger zerolog.Logger) *LootRecognizer {
	lr := &LootRecognizer{
		cal:            cal,
		scale:          1.0,
		templates:      ts,
		digitTemplates: make([]gocv.Mat, 10),
		logger:         logger.With().Str("component", "loot_recognizer").Logger(),
	}
	if cal != nil {
		lr.scale = cal.DisplayScale()
	}
	lr.prepareDigitTemplates()
	return lr
}

func (lr *LootRecognizer) prepareDigitTemplates() {
	lr.digitTemplates = make([]gocv.Mat, 10)
	lr.digitCanon = make([]gocv.Mat, 10)
	for i := 0; i < 10; i++ {
		name := fmt.Sprintf("digit_%d", i)
		tpl, ok := lr.templates.Get(name)
		if !ok || tpl.Empty() {
			continue
		}
		gray := gocv.NewMat()
		if tpl.Channels() == 3 {
			gocv.CvtColor(tpl, &gray, gocv.ColorBGRToGray)
		} else {
			tpl.CopyTo(&gray)
		}
		bin := gocv.NewMat()
		gocv.Threshold(gray, &bin, 128, 255, gocv.ThresholdBinary)
		rect := tightBoundingBox(bin)
		blob := bin
		cropped := false
		if !rect.Empty() {
			blob = bin.Region(rect)
			cropped = true
		}
		lr.digitTemplates[i] = blob.Clone()
		// The canonical box is what the matcher actually compares against; see
		// internal/vision/digits.go for why resizing the template to the live
		// glyph's box instead scored correct digits at 0.05-0.45.
		lr.digitCanon[i] = vision.CanonicalGlyph(blob)
		if cropped {
			blob.Close()
		}
		bin.Close()
		gray.Close()
	}
}

func (lr *LootRecognizer) Close() {
	for _, tpl := range lr.digitTemplates {
		if !tpl.Empty() {
			tpl.Close()
		}
	}
	for _, canon := range lr.digitCanon {
		if !canon.Empty() {
			canon.Close()
		}
	}
}

type LootReport struct{ Resources Resources }

// IsZero reports whether a parsed result carries no value at all — every
// star count, loot row and bonus row read zero. Callers use it to tell a
// panel that has been emptied by the game from one that has not finished
// painting, and to decide when there is nothing worth keeping from a read.
func (r BattleResult) IsZero() bool {
	return r.Stars == 0 && r.Loot.IsZero() && r.Bonus.IsZero()
}

// IsZero reports whether every resource in the set read zero.
func (r Resources) IsZero() bool {
	return r.Gold == 0 && r.Elixir == 0 && r.DarkElixir == 0 && r.Trophies == 0
}

// Reference-resolution search zones for the two loot columns on the
// end-of-battle panel. They bound where text lines may be found; the lines
// themselves are located by content, and the three bottom-most are read as
// the column's loot rows.
//
// The bonus zone's right edge is the one that bit: the league-bonus rows
// render a "+" then six digits, and at 16:9 the column sits ~25 reference px
// further right than the reference-era screenshot these were authored from.
// A right edge of 676 clipped the last two digits, so a live "+288000"
// arrived as "288" — and because the clipped header line above it then made
// up the third row of the bottom-three anchoring, a real league bonus of
// 288000/288000/2160 was recorded as 90/288/288. See
// TestBonusSearchCoversObserved720pBonusRows.
var (
	battleLootSearch = image.Rect(300, 280, 520, 470) // Battle Loot column
	bonusLootSearch  = image.Rect(530, 330, 760, 470) // League Bonus column
)

type BattleResult struct {
	Loot  Resources
	Bonus Resources
	Stars int
}

func (lr *LootRecognizer) ReadAvailableLoot(screen gocv.Mat) (Resources, error) {
	report, _ := lr.ReadLootDetailed(screen)
	return report.Resources, nil
}

// battleHUDLootRows are the player's three running resource counters in the
// top-right corner of a battle frame. Geometry is authored at the calibrated
// 1280x720 frame and scaled to the actual capture, never hardcoded to one
// resolution: the HUD anchors to the top-right, so a 1024x768 capture puts the
// same rows at the same fractions of width/height. Measured on the 1280x720
// battle frames: gold digits at y 32-44, elixir at 72-87, DE at 114-128; the
// ROI deliberately stops before the resource icon (x≈1241) — including it
// makes readRow read the icon's ring as a digit (observed: 56940981 instead of
// 5694098).
const (
	battleHUDRefW = 1280
	battleHUDRefH = 720
)

var battleHUDLootRows = []struct {
	resource string
	y0, y1   int // reference pixels at 720p
}{
	{"gold", 22, 50},
	{"elixir", 65, 94},
	{"dark_elixir", 104, 137},
}

// ReadBattleLootHUD reads the player's running resource counters shown in the
// top-right HUD of a battle frame. The caller differences two reads (battle
// start vs now) to get the loot gained this raid; see attack.lootTargetReached.
// It returns an error when none of the three rows produced a plausible count,
// which is the signal that this HUD geometry is not the one the reader was
// measured on — better no early end than ending on a fabricated threshold.
func (lr *LootRecognizer) ReadBattleLootHUD(screen gocv.Mat) (Resources, error) {
	if screen.Empty() {
		return Resources{}, fmt.Errorf("battle loot HUD: empty frame")
	}
	w, h := screen.Cols(), screen.Rows()
	if w < 600 || h < 400 {
		return Resources{}, fmt.Errorf("battle loot HUD: frame %dx%d too small", w, h)
	}

	// Scale the authored 720p ROIs by the capture's own dimensions. This is
	// what makes a 1024x768 frame read the same counters as 1280x720: the HUD
	// is anchored top-right, not laid out in absolute pixels.
	sx, sy := float64(w)/battleHUDRefW, float64(h)/battleHUDRefH
	x0, x1 := int(0.76*float64(battleHUDRefW)*sx), int(0.97*float64(battleHUDRefW)*sx)
	values := make([]int, len(battleHUDLootRows))
	found := 0
	for i, row := range battleHUDLootRows {
		roi := image.Rect(x0, int(float64(row.y0)*sy), x1, int(float64(row.y1)*sy))
		values[i] = lr.readRow(screen, roi)
		if values[i] > 0 {
			found++
		}
	}
	if found == 0 {
		return Resources{}, fmt.Errorf("battle loot HUD: no readable counters at %dx%d", w, h)
	}
	return Resources{Gold: values[0], Elixir: values[1], DarkElixir: values[2]}, nil
}

func (lr *LootRecognizer) ReadDestructionPercentage(screen gocv.Mat, roi image.Rectangle) int {
	if roi.Empty() {
		return 0
	}
	val := lr.readRow(screen, roi)
	// Trailing '%' symbol guard: on HUD overlays the right edge of the ROI
	// can clip the '%' glyph's left vertical stroke, which matches digit_1.
	// That turns e.g. "36%" into 361, "50%" into 501. Truncating the phantom
	// digit recovers the true destruction percentage.
	if val > 100 && val <= 1009 && val%10 == 1 {
		val /= 10
	}
	return val
}

// ReadBattleResult reads the loot and star counts shown on the Clash of
// Clans end-of-battle screen.
//
// Loot rows are located by CONTENT, not by fixed slots: bright
// low-saturation connected components (the white/gold loot digits) are
// detected inside a generous search zone, grouped into horizontal text
// lines, and each line is OCR'd with the shared readRow pipeline. The
// game ships two result-screen layouts — the classic "You got / Bonus"
// card and the compact themed overlay — whose rows sit at different x/y
// positions, so no fixed per-row rectangle can serve both. Content
// detection handles both, plus the defeat layout, without theme-specific
// coordinates.
//
// Search zones only bound where rows may appear; they can be overridden
// by assets/battle_loot_rois.json (battleSearch / bonusSearch), written
// by tools/picker.py --preset battle-loot. Star points come from
// assets/star_points.json.
func (lr *LootRecognizer) ReadBattleResult(screen gocv.Mat) (BattleResult, error) {
	var result BattleResult

	lr.resultLayoutOnce.Do(lr.loadResultLayout)
	lr.captureBattleColumn(screen, lr.battleZone, &result.Loot)
	lr.captureBattleColumn(screen, lr.bonusZone, &result.Bonus)

	// Star detection. Two complementary passes share one goal: a defeat
	// (0 stars) must NEVER read as a victory, and a victory must read its
	// true count. The old single-pixel check (mean gray over a 5x5 patch
	// > 100) was calibrated on themes that render three separate gold
	// stars, and it read the ~110-luminance gray placeholder rings on a
	// DEFEAT screen as stars — a live 35% defeat was reported as "2⭐".
	// See readStars for the two-pass design.
	result.Stars = lr.readStars(screen, lr.starPoints[:])

	return result, nil
}

// loadResultLayout resolves the result-panel search zones and star points once
// per recogniser. Defaults are the authored reference geometry; the JSON files
// can override them exactly as before.
func (lr *LootRecognizer) loadResultLayout() {
	// Reference-resolution search zones (860x732). The battle zone's left
	// edge starts at 300: digit rows begin at x>=325 on every observed
	// layout, and the victory-ribbon tail bleeding into the panel's left
	// edge (x~240-272) otherwise forms a phantom "text line" that displaces
	// the gold row.
	lr.battleZone = battleLootSearch
	lr.bonusZone = bonusLootSearch
	lr.starPoints = [3]image.Point{
		{X: 327, Y: 205}, // Left
		{X: 430, Y: 196}, // Middle
		{X: 535, Y: 210}, // Right
	}

	if data, err := os.ReadFile(paths.Resolve("battle_loot_rois.json")); err == nil {
		var custom struct {
			BattleSearch struct{ X1, Y1, X2, Y2 int } `json:"battleSearch"`
			BonusSearch  struct{ X1, Y1, X2, Y2 int } `json:"bonusSearch"`
		}
		if json.Unmarshal(data, &custom) == nil {
			if custom.BattleSearch.X2 > custom.BattleSearch.X1 {
				lr.battleZone = image.Rect(custom.BattleSearch.X1, custom.BattleSearch.Y1, custom.BattleSearch.X2, custom.BattleSearch.Y2)
			}
			if custom.BonusSearch.X2 > custom.BonusSearch.X1 {
				lr.bonusZone = image.Rect(custom.BonusSearch.X1, custom.BonusSearch.Y1, custom.BonusSearch.X2, custom.BonusSearch.Y2)
			}
			lr.logger.Info().Msg("loaded custom battle loot ROIs")
		}
	}

	if data, err := os.ReadFile(paths.Resolve("star_points.json")); err == nil {
		var custom struct {
			Stars []struct{ X, Y int } `json:"stars"`
		}
		if json.Unmarshal(data, &custom) == nil && len(custom.Stars) == 3 {
			for i := 0; i < 3; i++ {
				lr.starPoints[i] = image.Pt(custom.Stars[i].X, custom.Stars[i].Y)
			}
			lr.logger.Info().Msg("loaded custom star points")
		}
	}
}

// ---------------------------------------------------------------------------
// Star detection.
//
// The end-of-battle screen renders earned stars in one of two layouts
// across themes, and BOTH must fail safe on defeats:
//
//   - Classic rows (three separate gold stars at fixed x positions) and
//   - the current themed emblem (stars merged into one big filled star,
//     hollow gold outline on a defeat).
//
// The old detector sampled a 5x5 gray mean at three configured points
// and counted >100 as "star lit". Defeat placeholder rings render at
// ~110 luminance, so a defeat could read as 1-2 stars (observed live:
// a 35% defeat reported as "2⭐"). The replacement is strict twice:
//
//   Pass 1 — per-point (legacy layouts). A configured star point counts
//   only when its patch contains genuinely BRIGHT pixels (luminance
//   > 180) that are white or gold-warm; neutral gray (~110) and the
//   gold banner both fail. Small patches prevent specks from counting.
//
//   Pass 2 — filled-star cluster (current themed emblem). The largest
//   connected blob of bright-white pixels inside starBand is measured
//   in reference pixels and mapped to 1-3 stars. Live calibration
//   (860x732): 2-star victories = 4145-5055 px; defeats = 0 px. The
//   mapping is anchored at ~2300 ref px per star, gated below 700 px
//   so decoration specks can never masquerade as an emblem.
//
//   The final count is the greater of the two passes, clamped to 3, so
//   a themed emblem (pass 2 = 2) still reports correctly when only one
//   legacy point happens to land on it (pass 1 = 1).
// ---------------------------------------------------------------------------

// starBand is the reference-resolution region that contains the star
// display on every observed end-of-battle layout (the "Total damage"
// line sits above it, the Victory/Defeat text below).
var starBand = image.Rect(240, 140, 460, 260)

// starClusterMinRef gates pass 2: filled-star clusters measure
// 4000+ ref px live, so 700 cleanly rejects decoration specks even on
// scaled-down or mid-animation frames.
const starClusterMinRef = 700

// starClusterPerRef is the reference-pixel area attributed to one star.
// Anchored on two live 2-star victories (4145 and 4895 px) and the
// tracked regression fixture (4709 px): 4145/2300 = 1.8 -> 2,
// 4895/2300 = 2.1 -> 2, 4709/2300 = 2.0 -> 2.
const starClusterPerRef = 2300

// isStarPixel reports whether an HSV pixel looks like earned-star
// material: genuinely bright AND either white (the filled themed emblem)
// or gold-warm (classic star icons). Neutral grays — the ~110-luminance
// placeholder rings that broke the old <=100 mean check — and saturated
// non-warm decorations (the league-bonus panel) are rejected.
func isStarPixel(h, s, v uint8) bool {
	if v < 181 {
		return false
	}
	if s < 70 {
		return true // bright white
	}
	return h < 50 || h > 160 // bright gold / warm hues (HSV hue: 0-179)
}

// readStars runs the two-pass star detector and returns an earned-star
// count in [0,3].
func (lr *LootRecognizer) readStars(screen gocv.Mat, starPoints []image.Point) int {
	legacy := 0
	for _, pt := range starPoints {
		if lr.starAtPoint(screen, pt) {
			legacy++
		}
	}

	if cluster := lr.starClusterCount(screen); cluster > legacy {
		legacy = cluster
	}
	if legacy > 3 {
		legacy = 3
	}
	return legacy
}

// starAtPoint counts genuine star pixels in a small patch around a
// configured point. Returns true when the patch holds enough bright
// white/gold pixels to be a lit star (>= 5 of a 10x10 ref patch; the
// classic star icons fill most of their footprint).
func (lr *LootRecognizer) starAtPoint(screen gocv.Mat, pt image.Point) bool {
	// Result-panel content is a centred overlay, so its points and ROIs are
	// mapped around the viewport centre (docs/RESOLUTION.md).
	sx, sy := lr.cal.Centre(pt.X, pt.Y)
	r := lr.safeRect(screen, image.Rect(sx-5, sy-5, sx+5, sy+5))
	if r.Empty() || r.Dx() < 4 || r.Dy() < 4 {
		return false
	}
	sub := screen.Region(r)
	defer sub.Close()

	hsv := gocv.NewMat()
	defer hsv.Close()
	gocv.CvtColor(sub, &hsv, gocv.ColorBGRToHSV)

	// Count qualifying pixels over the tiny patch (rows x cols, each
	// pixel packed H,S,V in the channel stride).
	ok := 0
	for row := 0; row < hsv.Rows(); row++ {
		for col := 0; col < hsv.Cols(); col++ {
			if isStarPixel(hsv.GetUCharAt(row, col*3), hsv.GetUCharAt(row, col*3+1), hsv.GetUCharAt(row, col*3+2)) {
				ok++
			}
		}
	}
	return ok >= 5
}

// starClusterCount measures the largest connected blob of bright-white
// pixels inside the star band and maps its reference-resolution area to
// 1-3 stars (0 when no filled emblem is present).
func (lr *LootRecognizer) starClusterCount(screen gocv.Mat) int {
	roi := lr.safeRect(screen, lr.cal.CentreRect(starBand))
	if roi.Empty() {
		return 0
	}
	sub := screen.Region(roi)
	defer sub.Close()

	hsv := gocv.NewMat()
	defer hsv.Close()
	gocv.CvtColor(sub, &hsv, gocv.ColorBGRToHSV)

	// Saturation < 70 AND Value > 200 -> binary white mask. The themed
	// emblem body is bright white; gold banner/decorations and gray
	// placeholders are excluded (see isStarPixel for the reasoning).
	hsvChans := gocv.Split(hsv)
	for i := range hsvChans {
		ch := hsvChans[i] // bind per-iteration: `defer c.Close()` would close the last channel 3x
		defer ch.Close()
	}
	sNotSat := gocv.NewMat()
	defer sNotSat.Close()
	gocv.Threshold(hsvChans[1], &sNotSat, 70, 255, gocv.ThresholdBinaryInv)
	vBright := gocv.NewMat()
	defer vBright.Close()
	gocv.Threshold(hsvChans[2], &vBright, 200, 255, gocv.ThresholdBinary)
	mask := gocv.NewMat()
	defer mask.Close()
	gocv.BitwiseAnd(sNotSat, vBright, &mask)

	// 3x3 open removes single-pixel speckle so FindContours sees only
	// real shapes.
	kernel := gocv.GetStructuringElement(gocv.MorphRect, image.Pt(3, 3))
	defer kernel.Close()
	gocv.MorphologyEx(mask, &mask, gocv.MorphOpen, kernel)

	contours := gocv.FindContours(mask, gocv.RetrievalExternal, gocv.ChainApproxSimple)
	defer contours.Close()

	largest := 0.0
	for i := 0; i < contours.Size(); i++ {
		if a := gocv.ContourArea(contours.At(i)); a > largest {
			largest = a
		}
	}

	// Normalize to reference resolution so the threshold/scale constants
	// hold across emulator sizes: an area scales with the display scale
	// squared, not with the product of two per-axis ratios.
	k := lr.cal.DisplayScale()
	refArea := largest / (k * k)
	if refArea < starClusterMinRef {
		return 0
	}
	if lr.Debug {
		lr.logger.Debug().Float64("refArea", refArea).Int("band", roi.Dx()*roi.Dy()).Msg("battle star cluster measured")
	}
	n := int(refArea/starClusterPerRef + 0.5) // round to nearest star
	if n < 1 {
		n = 1
	}
	if n > 3 {
		n = 3
	}
	return n
}

// StarsFromOutcome derives the earned star count from the battle's
// measured outcome using CoC's scoring rules (NOT from result-screen
// pixels):
//
//	>= 50% destruction          -> 1 star
//	Town Hall destroyed         -> +1 star
//	100% destruction            -> 3 stars
//
// thDestroyed should be false when the TH state was not observed; the
// function then reports 1 star for any 50-99% win, which is the
// conservative (never inflated) answer.
func StarsFromOutcome(destructionPercent int, thDestroyed bool) int {
	if destructionPercent >= 100 {
		return 3
	}
	stars := 0
	if destructionPercent >= 50 {
		stars = 1
	}
	if thDestroyed {
		stars++
	}
	if stars > 3 {
		stars = 3
	}
	return stars
}

// captureBattleColumn reads the three loot values (gold, elixir, DE) for
// a single column on the end-of-battle screen. `zoneRef` is the column's
// reference-resolution search zone; text lines are detected inside it by
// content (bright, low-saturation glyph blobs grouped into rows), the
// bottom-most three lines are treated as gold/elixir/DE, and each is OCR'd
// with the shared readRow pipeline.
//
// Writes gold/elixir/de into the supplied *Resources.
func (lr *LootRecognizer) captureBattleColumn(screen gocv.Mat, zoneRef image.Rectangle, dst *Resources) {
	lines := lr.detectBattleTextLines(screen, zoneRef)
	if lr.Debug {
		for i, ln := range lines {
			lr.logger.Debug().Int("line", i).Str("rect", ln.rect.String()).Int("glyphs", ln.glyphs).Msg("battle text line")
		}
	}

	// The bottom-most three lines are the loot values. Themes vary in how
	// many text rows the panel carries above them (e.g. a "LEAGUE BONUS"
	// header line in the same zone), so anchor on the bottom instead of
	// assuming the panel's internal layout.
	var rows [3]image.Rectangle
	for i := 0; i < 3; i++ {
		idx := len(lines) - 3 + i
		if idx >= 0 {
			rows[i] = lines[idx].rect
		}
	}

	values := [3]int{}
	for i := range rows {
		if rows[i].Empty() {
			continue
		}
		// Pad the detected line so descenders and anti-aliased glyph edges
		// survive readRow's binarization; readRow re-bounds the text itself.
		padY := int(lr.cal.Length(6))
		padX := int(lr.cal.Length(4))
		r := lr.safeRect(screen, image.Rect(rows[i].Min.X-padX, rows[i].Min.Y-padY, rows[i].Max.X+padX, rows[i].Max.Y+padY))
		values[i] = lr.readRow(screen, r)
		if lr.Debug {
			lr.logger.Debug().Int("row", i).Str("rect", r.String()).Int("value", values[i]).Msg("battle loot row read")
		}
	}

	dst.Gold = values[0]
	dst.Elixir = values[1]
	dst.DarkElixir = values[2]
}

// battleTextLine is one detected row of loot digits inside a search zone.
type battleTextLine struct {
	rect   image.Rectangle
	glyphs int
}

// Glyph geometry gates for loot digits, in reference pixels (860x732).
// Digit glyphs on every observed result-screen theme measure ~10-13 ref px
// tall and ~5-12 wide; the wide gates tolerate theme variation, and the
// line-glyph threshold (>=3) rejects lone decoration blobs. The digit '1'
// renders ~2 ref px wide at small sizes — hence minW=2.
const (
	battleGlyphMinH     = 9
	battleGlyphMaxH     = 30
	battleGlyphMinW     = 2
	battleGlyphMaxW     = 34
	battleGlyphMinArea  = 8
	battleGlyphMinFill  = 0.30
	battleLineMinGlyphs = 3
	battleLineTolY      = 12 // vertical center tolerance when grouping glyphs into lines; must exceed the within-row spread of adjacent decoration blobs (observed ~10) but stay well under the ~31 px row pitch
	battleRunGapX       = 16 // max horizontal gap inside one glyph run
)

// detectBattleTextLines finds horizontal rows of bright, low-saturation
// glyph blobs (loot digits) inside a reference-resolution zone. Blobs are
// grouped into lines by vertical center; per line, the longest glyph run
// (gap <= battleRunGapX) wins, so icon shine or separated panel-tab glyphs
// never displace the digit row itself.
func (lr *LootRecognizer) detectBattleTextLines(screen gocv.Mat, zoneRef image.Rectangle) []battleTextLine {
	if zoneRef.Empty() {
		return nil
	}
	// The battle-loot / bonus columns are on the centred result panel.
	zone := lr.safeRect(screen, lr.cal.CentreRect(zoneRef))
	if zone.Empty() || zone.Dx() < 10 || zone.Dy() < 10 {
		return nil
	}

	sub := screen.Region(zone)
	defer sub.Close()

	hsv := gocv.NewMat()
	defer hsv.Close()
	gocv.CvtColor(sub, &hsv, gocv.ColorBGRToHSV)

	// Loot digits render near-white (saturation < 70) with value > 170.
	// Icons, ribbons, and panel decorations are saturated or darker, so
	// they never enter the mask.
	mask := gocv.NewMat()
	defer mask.Close()
	gocv.InRangeWithScalar(hsv, gocv.NewScalar(0, 0, 170, 0), gocv.NewScalar(179, 70, 255, 0), &mask)

	stats := gocv.NewMat()
	centroids := gocv.NewMat()
	defer stats.Close()
	defer centroids.Close()
	labels := gocv.NewMat()
	defer labels.Close()
	n := gocv.ConnectedComponentsWithStats(mask, &labels, &stats, &centroids)

	type glyph struct{ x, y, w, h int }
	var glyphs []glyph
	for i := 1; i < n; i++ { // 0 is the background label
		// stats is MatTypeCV32S; GetIntAt (not GetFloatAt) reads the raw
		// int32 values — GetFloatAt would reinterpret the bits as floats.
		x := int(stats.GetIntAt(i, int(gocv.CC_STAT_LEFT)))
		y := int(stats.GetIntAt(i, int(gocv.CC_STAT_TOP)))
		w := int(stats.GetIntAt(i, int(gocv.CC_STAT_WIDTH)))
		h := int(stats.GetIntAt(i, int(gocv.CC_STAT_HEIGHT)))
		area := int(stats.GetIntAt(i, int(gocv.CC_STAT_AREA)))
		if h < int(battleGlyphMinH*lr.scale) || h > int(battleGlyphMaxH*lr.scale) {
			continue
		}
		if w < int(battleGlyphMinW*lr.scale) || w > int(battleGlyphMaxW*lr.scale) {
			continue
		}
		if area < battleGlyphMinArea || float64(area)/float64(w*h) < battleGlyphMinFill {
			continue
		}
		glyphs = append(glyphs, glyph{x, y, w, h})
	}
	if len(glyphs) == 0 {
		return nil
	}

	// Group glyph centers into lines: sort by vertical center, sweep, and
	// start a new line whenever the center jumps more than battleLineTolY
	// from the running line's mean center.
	sort.Slice(glyphs, func(i, j int) bool {
		return glyphs[i].y+glyphs[i].h/2 < glyphs[j].y+glyphs[j].h/2
	})
	type line struct {
		glyphs []glyph
		sumCY  float64
	}
	var lines []line
	for _, g := range glyphs {
		cy := float64(g.y + g.h/2)
		if len(lines) > 0 {
			last := &lines[len(lines)-1]
			if math.Abs(cy-last.sumCY/float64(len(last.glyphs))) <= float64(battleLineTolY)*lr.scale {
				last.glyphs = append(last.glyphs, g)
				last.sumCY += cy
				continue
			}
		}
		lines = append(lines, line{glyphs: []glyph{g}, sumCY: cy})
	}

	var out []battleTextLine
	for _, ln := range lines {
		if len(ln.glyphs) < battleLineMinGlyphs {
			continue
		}
		sort.Slice(ln.glyphs, func(i, j int) bool { return ln.glyphs[i].x < ln.glyphs[j].x })
		// Split into runs by horizontal gap; the longest run is the digit
		// row. Icon shine / panel-tab glyphs sitting near the text line end
		// up in their own short run and are dropped.
		maxGap := float64(battleRunGapX) * lr.scale
		runs := [][]glyph{{ln.glyphs[0]}}
		for _, g := range ln.glyphs[1:] {
			last := runs[len(runs)-1]
			tail := last[len(last)-1]
			if float64(g.x-(tail.x+tail.w)) <= maxGap {
				runs[len(runs)-1] = append(last, g)
			} else {
				runs = append(runs, []glyph{g})
			}
		}
		best := runs[0]
		for _, r := range runs[1:] {
			if len(r) > len(best) {
				best = r
			}
		}
		rect := image.Rect(best[0].x, best[0].y, best[0].x+best[0].w, best[0].y+best[0].h)
		for _, g := range best[1:] {
			rect = rect.Union(image.Rect(g.x, g.y, g.x+g.w, g.y+g.h))
		}
		// Back to absolute screen coordinates.
		out = append(out, battleTextLine{
			rect:   rect.Add(zone.Min),
			glyphs: len(best),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].rect.Min.Y < out[j].rect.Min.Y })
	return out
}

// safeRect clamps r to img bounds. Returns image.Rectangle{} when r collapses.
func (lr *LootRecognizer) safeRect(img gocv.Mat, r image.Rectangle) image.Rectangle {
	if r.Min.X < 0 {
		r.Min.X = 0
	}
	if r.Min.Y < 0 {
		r.Min.Y = 0
	}
	if r.Max.X > img.Cols() {
		r.Max.X = img.Cols()
	}
	if r.Max.Y > img.Rows() {
		r.Max.Y = img.Rows()
	}
	if r.Max.X < r.Min.X {
		r.Max.X = r.Min.X
	}
	if r.Max.Y < r.Min.Y {
		r.Max.Y = r.Min.Y
	}
	return r
}

func (lr *LootRecognizer) ReadLootDetailed(screen gocv.Mat) (LootReport, error) {
	// High-Precision ROIs for Scouting (Reference 860x732)
	// We use very inclusive X range (starting at 40) because digits start immediately after icons
	icons := []struct {
		name, tpl string
		y1, y2    int
	}{
		{"gold", "icon_gold", 66, 100},
		{"elixir", "icon_elixir", 95, 128},
		{"de", "icon_de", 124, 157},
	}

	var results [3]int
	for i, ic := range icons {
		tpl, ok := lr.templates.Get(ic.tpl)
		if ok && !tpl.Empty() {
			res := vision.GetMat(screen.Rows()-tpl.Rows()+1, screen.Cols()-tpl.Cols()+1, gocv.MatTypeCV32FC1)
			gocv.MatchTemplate(screen, tpl, &res, gocv.TmCcoeffNormed, vision.EmptyMask())
			_, maxConf, _, maxLoc := gocv.MinMaxLoc(res)
			vision.PutMat(res)

			if maxConf > 0.8 {
				// Anchor to icon. Starting ROI directly inside the icon area
				// because readRow uses Color/Saturation to skip the actual icon bits.
				// Digits span at most ~160px from the icon; wider spans scan into
				// base buildings, walls, and terrain, producing phantom digits.
				rect := image.Rect(
					maxLoc.X+int(4*lr.scale),
					maxLoc.Y-int(5*lr.scale),
					maxLoc.X+int(160*lr.scale),
					maxLoc.Y+tpl.Rows()+int(5*lr.scale),
				)
				results[i] = lr.readRow(screen, rect)
				continue
			}
		}
		// Fallback ROIs: Inclusive X1=40 to catch the very first digit, bounded at X2=220
		rect := image.Rect(int(40*lr.scale), int(float64(ic.y1)*lr.scale), int(220*lr.scale), int(float64(ic.y2)*lr.scale))
		results[i] = lr.readRow(screen, rect)
	}

	return LootReport{Resources: Resources{Gold: results[0], Elixir: results[1], DarkElixir: results[2]}}, nil
}

func (lr *LootRecognizer) readRow(screen gocv.Mat, roi image.Rectangle) int {
	roi = lr.safeRect(screen, roi)
	if roi.Empty() {
		return 0
	}

	sub := screen.Region(roi)
	defer sub.Close()

	gray := vision.GetMat(sub.Rows(), sub.Cols(), gocv.MatTypeCV8UC1)
	defer vision.PutMat(gray)
	gocv.CvtColor(sub, &gray, gocv.ColorBGRToGray)

	hsv := vision.GetMat(sub.Rows(), sub.Cols(), gocv.MatTypeCV8UC3)
	defer vision.PutMat(hsv)
	gocv.CvtColor(sub, &hsv, gocv.ColorBGRToHSV)

	// 1. Resize 5x first to ensure image dimensions are larger than kernel size (61x61)
	scaled := vision.GetMat(sub.Rows()*5, sub.Cols()*5, gocv.MatTypeCV8UC1)
	defer vision.PutMat(scaled)
	gocv.Resize(gray, &scaled, image.Point{X: 0, Y: 0}, 5.0, 5.0, gocv.InterpolationCubic)

	// 2. Estimate background brightness on scaled image with dynamic kernel size safety
	kSize := 61
	if scaled.Rows() < kSize {
		kSize = scaled.Rows()
	}
	if scaled.Cols() < kSize {
		kSize = scaled.Cols()
	}
	if kSize%2 == 0 {
		kSize--
	}
	if kSize < 3 {
		kSize = 3
	}

	bg := vision.GetMatFrom(scaled)
	defer vision.PutMat(bg)
	gocv.GaussianBlur(scaled, &bg, image.Point{X: kSize, Y: kSize}, 0, 0, gocv.BorderDefault)

	// 3. Remove slow illumination changes
	norm := vision.GetMatFrom(scaled)
	defer vision.PutMat(norm)
	gocv.Subtract(scaled, bg, &norm)

	// 4. Stretch contrast
	gocv.Normalize(norm, &norm, 0, 255, gocv.NormMinMax)

	// 5. Scaled Otsu thresholding to prevent hollow digits by keeping shaded inner text regions white
	binary := vision.GetMatFrom(norm)
	defer vision.PutMat(binary)
	dummy := vision.GetMatFrom(norm)
	defer vision.PutMat(dummy)
	otsuVal := gocv.Threshold(norm, &dummy, 0, 255, gocv.ThresholdBinary|gocv.ThresholdOtsu)
	gocv.Threshold(norm, &binary, otsuVal*0.60, 255, gocv.ThresholdBinary)

	// 6. Morphological Open (1x to clean noise)
	kernelOpen, kernelClose := lootKernels()
	gocv.MorphologyEx(binary, &binary, gocv.MorphOpen, kernelOpen)

	// 6.5 Morphological Close (5x5 ellipse) to fill hollow digit centers on victory screens
	gocv.MorphologyEx(binary, &binary, gocv.MorphClose, kernelClose)

	// 7. Invert colors if background is light (mostly white pixels)
	nonZero := gocv.CountNonZero(binary)
	total := binary.Rows() * binary.Cols()
	if float64(nonZero)/float64(total) > 0.5 {
		gocv.BitwiseNot(binary, &binary)
	}

	bestVal := 0
	roiCenterY := scaled.Rows() / 2

	contours := gocv.FindContours(binary, gocv.RetrievalExternal, gocv.ChainApproxSimple)
	var detected []detectedDigit
	for i := 0; i < contours.Size(); i++ {
		rect := gocv.BoundingRect(contours.At(i))
		minH := int(7*lr.scale) * 5
		maxH := int(40*lr.scale) * 5
		minW := int(2.5 * lr.scale * 5)
		maxW := int(40*lr.scale) * 5

		if rect.Dy() < minH || rect.Dy() > maxH || rect.Dx() < minW || rect.Dx() > maxW {
			lr.reject(rect, "size", minW, minH, maxW, maxH)
			continue
		}

		// Vertical alignment check
		blobCenterY := rect.Min.Y + rect.Dy()/2
		if math.Abs(float64(blobCenterY-roiCenterY)) > float64(scaled.Rows())/2.0 {
			lr.reject(rect, "row-offset", 0, 0, 0, 0)
			continue
		}

		// Color Filter: Digits are strictly white/grey (Low Saturation)
		// Sample from the original HSV region by scaling coordinates back down 5x
		origRect := image.Rect(rect.Min.X/5, rect.Min.Y/5, rect.Max.X/5, rect.Max.Y/5)
		origRect = lr.safeRect(sub, origRect)
		if !origRect.Empty() {
			blobHSV := hsv.Region(origRect)
			mean := blobHSV.Mean()
			blobHSV.Close()

			// Saturation check is our primary icon-rejection tool.
			// White text Saturation is usually < 40. Icons are > 100.
			// Increased to 105 to tolerate colorful/grass background bleeding into transparent text regions.
			if mean.Val2 > 105 {
				lr.reject(rect, "saturation", 0, 0, 0, 0)
				continue
			}
		}

		blob := binary.Region(rect)
		d := lr.matchDigit(blob)
		blob.Close()
		if d.digit < 0 {
			lr.reject(rect, "no-digit-match", 0, 0, 0, 0)
		}
		if d.digit >= 0 {
			d.rect = image.Rect(rect.Min.X/5, rect.Min.Y/5, rect.Max.X/5, rect.Max.Y/5)
			detected = append(detected, d)
		}
	}
	contours.Close()

	if len(detected) > 0 {
		sort.Slice(detected, func(i, j int) bool { return detected[i].rect.Min.X < detected[j].rect.Min.X })

		// Deduplicate overlaps
		cleaned := []detectedDigit{}
		for _, d := range detected {
			found := false
			for i, c := range cleaned {
				if d.rect.Min.X >= c.rect.Min.X-2 && d.rect.Min.X <= c.rect.Min.X+2 {
					found = true
					if d.conf > c.conf {
						cleaned[i] = d
					}
					break
				}
			}
			if !found {
				cleaned = append(cleaned, d)
			}
		}

		// Cluster Detection: Find the group of digits with small gaps
		var clusters [][]detectedDigit
		if len(cleaned) > 0 {
			current := []detectedDigit{cleaned[0]}
			maxGap := int(80 * lr.scale)
			for i := 1; i < len(cleaned); i++ {
				gap := cleaned[i].rect.Min.X - cleaned[i-1].rect.Max.X
				if gap <= maxGap {
					current = append(current, cleaned[i])
				} else {
					clusters = append(clusters, current)
					current = []detectedDigit{cleaned[i]}
				}
			}
			clusters = append(clusters, current)
		}

		// Select best cluster (most digits)
		var bestCluster []detectedDigit
		for _, c := range clusters {
			if len(c) > len(bestCluster) {
				bestCluster = c
			} else if len(c) == len(bestCluster) && len(c) > 0 {
				// Tie-breaker: prefer the more LEFT cluster (loot digits start immediately)
				if bestCluster == nil || c[0].rect.Min.X < bestCluster[0].rect.Min.X {
					bestCluster = c
				}
			}
		}
		cleaned = bestCluster

		s := ""
		details := ""
		for _, d := range cleaned {
			// Get mean intensity for logging
			origRect := lr.safeRect(gray, d.rect)
			if !origRect.Empty() {
				blobGray := gray.Region(origRect)
				mean := blobGray.Mean()
				blobGray.Close()
				s += strconv.Itoa(d.digit)
				details += fmt.Sprintf("[%d@%d-%d m%.0f]", d.digit, d.rect.Min.X, d.rect.Max.X, mean.Val1)
			}
		}
		if lr.Debug {
			lr.logger.Debug().Str("digits", s).Str("pos", details).Msg("row OCR pass")
		}
		val, _ := strconv.Atoi(s)
		if val < 100000000 {
			bestVal = val
		}
	}
	return bestVal
}

// reject records why a candidate blob was dropped, so a read that comes back
// short of digits is diagnosable from the run log instead of by re-running the
// pipeline under a debugger. rect is in the 5x-scaled ROI's coordinates; the
// logged box is scaled back to source pixels.
func (lr *LootRecognizer) reject(rect image.Rectangle, reason string, minW, minH, maxW, maxH int) {
	if !lr.Debug {
		return
	}
	e := lr.logger.Debug().
		Str("reason", reason).
		Interface("box", image.Rect(rect.Min.X/5, rect.Min.Y/5, rect.Max.X/5, rect.Max.Y/5)).
		Int("w", rect.Dx()/5).
		Int("h", rect.Dy()/5)
	if reason == "size" {
		e = e.Int("min_w", minW/5).Int("min_h", minH/5).Int("max_w", maxW/5).Int("max_h", maxH/5)
	}
	e.Msg("digit candidate rejected")
}

// matchDigit names one glyph-shaped blob as a digit.
//
// It compares SHAPES in a canonical box (vision.CanonicalGlyph / MatchCanonical):
// both the blob and every template are cropped to their own ink and resized to
// the same box before matching. The older approach — resize each template to
// the blob's box — scored correct digits 0.05-0.45 on the game's real font and
// almost always picked `digit_1`, so a damage panel showing "16%" read as "1"
// and the end-of-battle wait watched a percentage it could never see move. The
// only reason anything was read at all is the thin-blob shortcut below, which
// recognises a `1` without any template at all.
func (lr *LootRecognizer) matchDigit(bin gocv.Mat) detectedDigit {
	bw, bh := bin.Cols(), bin.Rows()
	if bw < 1 || bh < 1 {
		return detectedDigit{digit: -1}
	}

	canon := vision.CanonicalGlyph(bin)
	bestDigit, maxConf, margin := vision.MatchCanonical(canon, lr.digitCanon)
	canon.Close()

	// Thin vertical blobs are almost always '1'. Kept as a backstop for the
	// case the template set is missing entirely, but a real match no longer
	// needs it: at canonical size a `1` wins on its own merits.
	if bestDigit < 0 || maxConf < float64(digitConfFloor) {
		minH1 := int(12 * lr.scale)
		maxW1 := int(6 * lr.scale)
		if bw >= 1 && bw <= maxW1 && bh >= minH1 { // Narrower and taller
			fill := float64(gocv.CountNonZero(bin)) / float64(bw*bh)
			if fill > 0.65 {
				lr.logDigit(bin, 1, 0.6, 0, "thin-blob shortcut")
				return detectedDigit{digit: 1, conf: 0.6}
			}
		}
	}

	// A glyph that wins on score alone is accepted; so is one that is a little
	// weaker but has no near-neighbour. An 11-13 px `6` is genuinely close to
	// `8` and `9`, and reporting a confident wrong damage percentage is worse
	// than reporting that the panel could not be read.
	if bestDigit < 0 || maxConf < float64(digitConfFloor) ||
		(margin < float64(digitConfMargin) && maxConf < float64(digitConfStrong)) {
		lr.logDigit(bin, bestDigit, maxConf, margin, "below floor/margin")
		return detectedDigit{digit: -1}
	}
	lr.logDigit(bin, bestDigit, maxConf, margin, "accepted")
	return detectedDigit{digit: bestDigit, conf: float32(maxConf)}
}

// Digit acceptance thresholds, measured on this pipeline rather than copied
// from the troop counter's: the counter matches grayscale ink crops that keep
// their anti-aliasing and sees correct digits at 0.54-0.72, while a row read
// here matches binarised blobs and sees the same digits lower — the damage
// panel's `6` scores 0.50 with a 0.12 margin. Junk on this path measured 0.44
// with a 0.00 margin and 0.28 with 0.04, so the floor stays above the junk and
// the margin below the real glyph.
const (
	digitConfFloor  = 0.45
	digitConfMargin = 0.10
	digitConfStrong = 0.60
)

// logDigit records a classification decision under Debug, so a row that reads
// one digit short of what is on screen can be diagnosed from the log.
func (lr *LootRecognizer) logDigit(bin gocv.Mat, digit int, conf, margin float64, verdict string) {
	if !lr.Debug {
		return
	}
	lr.logger.Debug().
		Int("digit", digit).
		Float64("conf", math.Round(conf*100)/100).
		Float64("margin", math.Round(margin*100)/100).
		Int("w", bin.Cols()).
		Int("h", bin.Rows()).
		Str("verdict", verdict).
		Msg("digit classification")
}

func tightBoundingBox(bin gocv.Mat) image.Rectangle {
	rows, cols := bin.Rows(), bin.Cols()
	xMin, xMax, yMin, yMax := cols, 0, rows, 0
	found := false
	for y := 0; y < rows; y++ {
		for x := 0; x < cols; x++ {
			if bin.GetUCharAt(y, x) > 128 {
				if x < xMin {
					xMin = x
				}
				if x > xMax {
					xMax = x
				}
				if y < yMin {
					yMin = y
				}
				if y > yMax {
					yMax = y
				}
				found = true
			}
		}
	}
	if !found {
		return image.Rectangle{}
	}
	return image.Rect(xMin, yMin, xMax+1, yMax+1)
}

// VillageResources holds the measured fill levels of village resource storages.
type VillageResources struct {
	GoldFillPercent   float64 `json:"gold_fill_percent"`
	ElixirFillPercent float64 `json:"elixir_fill_percent"`
	DEFillPercent     float64 `json:"de_fill_percent"`
}

// IsFull reports whether the village storage is considered full under the given threshold.
func (vr VillageResources) IsFull(haltGold, haltElixir, haltDE bool, rule string, threshold float64) bool {
	if threshold <= 0 {
		threshold = 95.0
	}
	goldFull := vr.GoldFillPercent >= threshold
	elixirFull := vr.ElixirFillPercent >= threshold
	deFull := vr.DEFillPercent >= threshold

	if rule == "ALL_3" {
		return goldFull && elixirFull && deFull
	}

	if rule == "ALL" {
		countEnabled := 0
		countFull := 0
		if haltGold {
			countEnabled++
			if goldFull {
				countFull++
			}
		}
		if haltElixir {
			countEnabled++
			if elixirFull {
				countFull++
			}
		}
		if haltDE {
			countEnabled++
			if deFull {
				countFull++
			}
		}
		return countEnabled > 0 && countFull == countEnabled
	}
	// Default "ANY"
	return (haltGold && goldFull) || (haltElixir && elixirFull) || (haltDE && deFull)
}

// Village HUD storage-bar geometry, in reference px (860x732). Measured on two
// live 1024x768 frames (k=1.1334): the bars end 40 ref px from the right edge,
// run 180 ref px left from there, and their centres sit at y 29/68/116 — the row
// bands measured at y 18-48, 61-94 and 127-151 live. Earlier numbers
// (39/76/113, then 30/75/127) came from partial or older measurements: the
// elixir probe sat 8 px low and the DE probe 11 px low, close enough to the
// neighbour bar to read the wrong resource.
const (
	storageBarRightRef = 27
	storageBarLenRef   = 193
	storageBarHalfRef  = 12
)

// storageBarCentersRef is gold, elixir, dark elixir, top to bottom.
var storageBarCentersRef = [3]int{29, 68, 116}

// hudResourcePixel reports whether a BGR pixel is resource-bar fill chrome:
// warm gold/yellow or magenta/purple, at the saturation and brightness the HUD
// paints a FILLED bar.
//
// Hue decides, not channel order: the gold fill is saturated yellow whose green
// channel is nearly as high as its red (live pixel (129,119,45) RGB), so the old
// "green must be well below the max" test skipped the whole dull left end of the
// bar and under-read gold by ~10 points on a live frame. The empty-bar wash is
// the same hue, just dark and desaturated, and the scenery behind the HUD is
// green — both fail the saturation or the hue window.
func hudResourcePixel(b, g, r uint8) bool {
	mx := int(b)
	if int(g) > mx {
		mx = int(g)
	}
	if int(r) > mx {
		mx = int(r)
	}
	mn := int(b)
	if int(g) < mn {
		mn = int(g)
	}
	if int(r) < mn {
		mn = int(r)
	}
	if mx < 90 { // wash and shading are dark
		return false
	}
	delta := mx - mn
	if delta*100/mx < 45 { // wash is desaturated grey
		return false
	}
	var hue int
	switch mx {
	case int(r):
		hue = 60 * (int(g) - int(b)) / delta
	case int(g):
		hue = 120 + 60*(int(b)-int(r))/delta
	default:
		hue = 240 + 60*(int(r)-int(g))/delta
	}
	if hue < 0 {
		hue += 360
	}
	warm := hue >= 15 && hue <= 65
	magenta := hue >= 265 && hue <= 345
	return warm || magenta
}

// readStorageBar measures one bar: the fill grows from the resource icon at the
// right end leftwards, so the bar's fill is everything right of the leftmost
// fill-coloured column. Measuring the leftmost fill pixel rather than counting
// coloured pixels is what makes a full bar read as full: the amount text is
// white and sits over the fill, and counting colours around those glyphs capped
// a full bar at ~70%.
func readStorageBar(screen gocv.Mat, barLeft, barRight int, centerRef int, k float64) (pct float64, found bool) {
	if barRight <= barLeft {
		return 0, false
	}
	yc := int(float64(centerRef) * k)
	half := int(storageBarHalfRef * k)
	if half < 2 {
		half = 2
	}
	y0, y1 := yc-half, yc+half
	if y0 < 0 {
		y0 = 0
	}
	if y1 >= screen.Rows() {
		y1 = screen.Rows() - 1
	}
	if y0 > y1 {
		return 0, false
	}

	// One fill pixel per column is enough: the HUD paints the fill edge with a
	// hard boundary, and requiring a row majority instead dropped whole bars
	// (a DE bar the game showed at 100% read 0%) because the fill's top rows are
	// shaded darker there. The single-pixel rule's only known cost is at >99%,
	// where the leading glow reports 99.2% as full — high by 0.8 of a point, on
	// the side the halt gate wants.
	leftmost := -1
	for y := y0; y <= y1; y++ {
		for x := barLeft; x < barRight; x++ {
			if hudResourcePixel(screen.GetUCharAt(y, x*3), screen.GetUCharAt(y, x*3+1), screen.GetUCharAt(y, x*3+2)) {
				if leftmost < 0 || x < leftmost {
					leftmost = x
				}
				break
			}
		}
	}
	if leftmost < 0 {
		return 0, false
	}

	barLen := float64(barRight - barLeft)
	pct = 100 * float64(barRight-leftmost) / barLen
	if pct > 100 {
		pct = 100
	}
	return math.Round(pct*10) / 10, true
}

// ReadVillageStorageFill measures the storage fullness of Gold, Elixir, and Dark Elixir
// from the main village top-right HUD bars.
//
// The fill is measured from the bar's own pixels (see readStorageBar): a bar
// that is completely filled reads 100%, which is the only reading the storage
// halt needs — the bar being full IS the storage being full.
func (lr *LootRecognizer) ReadVillageStorageFill(screen gocv.Mat) VillageResources {
	if screen.Empty() {
		return VillageResources{}
	}
	return ReadVillageStorageFillMat(screen, lr.scale)
}

// ReadVillageStorageFillMat measures the storage fullness directly on a Mat
// given a display scale.
func ReadVillageStorageFillMat(screen gocv.Mat, scale float64) VillageResources {
	if screen.Empty() {
		return VillageResources{}
	}
	if scale <= 0 {
		scale = 1.0
	}

	w := screen.Cols()

	// Round, not truncate: a 1 px slip in the 100% end of an ~219 px bar is half
	// a percent, and the halt threshold is a percentage point away.
	barRight := w - int(math.Round(storageBarRightRef*scale))
	barLeft := barRight - int(math.Round(storageBarLenRef*scale))
	if barLeft < 0 {
		barLeft = 0
	}
	if barRight <= barLeft || barRight > w {
		return VillageResources{}
	}

	gold, _ := readStorageBar(screen, barLeft, barRight, storageBarCentersRef[0], scale)
	elixir, _ := readStorageBar(screen, barLeft, barRight, storageBarCentersRef[1], scale)
	de, _ := readStorageBar(screen, barLeft, barRight, storageBarCentersRef[2], scale)

	return VillageResources{
		GoldFillPercent:   gold,
		ElixirFillPercent: elixir,
		DEFillPercent:     de,
	}
}
