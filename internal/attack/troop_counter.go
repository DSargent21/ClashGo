package attack

import (
	"fmt"
	"image"
	"image/color"
	"os"
	"path/filepath"
	"sort"

	"github.com/Ducky705/ClashGO/internal/game"
	"github.com/Ducky705/ClashGO/internal/paths"
	"github.com/Ducky705/ClashGO/internal/vision"
	"github.com/rs/zerolog"
	"gocv.io/x/gocv"
)

// Troop-count reading, and the two facts that make it work at 720p.
//
//  1. Where the number is. CoC draws the per-card count as a small "x8"-style
//     label at the card's TOP edge, i.e. at or just BELOW the slot's identity
//     point (`slotY`), not above the card. Measured on real 1280x720 battle
//     frames with an army loaded, the glyphs of every count label occupy
//     y 683..694 for slotY=671 — that is slotY+12 .. slotY+23, and the band
//     below (countBandTopRef/countBandBotRef) is that measurement with slack
//     on both sides. This code previously sampled [barY-5, slotY-25] — y
//     614..638 at 720p, roughly 45px ABOVE the label — so it read an empty
//     patch of bar on every slot of every frame and reported count=0 forever.
//     Everything downstream inherited that: the reconciler could never confirm
//     a drain, and both the sweep's "OCR reads zero, so the card is spent"
//     write-off and HeroManager's "visual-empty-but-OCR-zero" bail fired on
//     cards that were still full.
//
//  2. How to read it. The label glyphs are ~11-13px tall at 720p. Comparing a
//     binary glyph crop against a template resized to whatever box the contour
//     produced scored 0.05-0.45 for CORRECT digits and picked `digit_1` for
//     essentially every input, because the correlation was dominated by how
//     much of the crop was background. Instead every candidate glyph and every
//     template is cropped to its own ink and resized to the same canonical box
//     (digitCanonW x digitCanonH); TM_CCOEFF_NORMED over that pair is a
//     shape comparison that is independent of glyph width, of how much
//     padding surrounded it, and of the absolute pixel size. Measured on the
//     same real frames: correct digits score 0.54-0.72, junk 0.12-0.45.
type TroopCounter struct {
	// digitTemplates holds the tight ink crop of each digit template at its
	// file resolution; digitCanon holds the same glyphs resized to the
	// canonical comparison box. Matching only ever uses digitCanon.
	digitTemplates [10]gocv.Mat
	digitCanon     [10]gocv.Mat

	// tplH and tplW are the tallest/ widest template ink boxes, used to size
	// the "is this component even digit-shaped?" filter relative to the
	// templates on disk rather than to a hardcoded pixel count.
	tplH, tplW int

	logger zerolog.Logger

	// scale converts reference-frame pixel distances to live pixels. It is
	// the live calibration's display scale, not a per-axis framebuffer ratio.
	scale func(float64) float64

	// rowTop and rowBottom are the y range the count labels occupy in the frame
	// being read, as measured from that frame by measureLabelRow. They are set by
	// DetectCounts — the one call that sees the whole bar — and reused by every
	// later per-slot read, so a frame whose slot anchor disagrees with the bar's
	// real geometry still reads. Zero means "not measured", and the reader then
	// falls back to the row the configured geometry implies.
	rowTop, rowBottom int

	// unreadableDumps counts the annotated bar bands this counter has written.
	// See dumpUnreadableBarBand: it is the budget for the one reader outcome that
	// costs battle time. Touched only from the deploy path, like rowTop/rowBottom.
	unreadableDumps int
}

// glyphAccepted reports whether one glyph's match is a confident read: either
// the winning digit has a decisive lead over the runner-up, or the winning score
// is strong enough on its own to make the lead irrelevant.
func glyphAccepted(digit int, conf, margin float64) bool {
	return levelThresholds.accepts(digit, conf, margin)
}

// glyphThresholds are the acceptance numbers for ONE font: how strong a match
// has to be, and how far ahead of the runner-up it has to be.
//
// They are per-font, not global, because the count badge and the unit level are
// drawn at different sizes, on different backgrounds, and against the same digit
// templates. A badge glyph is anti-aliased over the card's plate, so it matches
// its own digit well while a stroke-sharing neighbour stays close: measured on the
// live event bar, the badge `0` scored 0.712 with a lead of 0.186 and the badge
// `9` scored 0.650 with a lead of 0.115, both of which the level font's numbers
// reject. Rejecting them is not neutral — it makes `x40` and `x9` unreadable, so
// the event troops those badges count stay on the bar.
type glyphThresholds struct {
	floor, margin, strong float64
}

func (th glyphThresholds) accepts(digit int, conf, margin float64) bool {
	if digit < 0 || conf < th.floor {
		return false
	}
	return margin >= th.margin || conf >= th.strong
}

// looksLikeCountDigit reports whether a component is recognisably one glyph of a
// count label, whether or not the digit itself could be settled. This is the
// weaker question of the two, and it is what separates "this card has a number I
// could not read" from "this card has no number".
func looksLikeCountDigit(digit int, conf float64) bool {
	return digit >= 0 && conf >= digitConfCount
}

// TroopCount is one slot's read.
//
// OK is the field callers must branch on. It reports that this read is a
// MEASUREMENT, not that a count was found: a card whose label is absent
// because the card is spent is read as Count=0, OK=true (the reader looked in
// the right place, and there is demonstrably nothing there). OK=false means
// the read is unknown — the reader found no digits anywhere on the bar, so a
// zero here carries no information and must not be treated as "the card is
// empty".
type TroopCount struct {
	X          int
	Count      int
	Confidence float64
	Digits     []int
	OK         bool
}

// Canonical glyph box for template comparison, and the acceptance floor.
const (
	digitCanonW = vision.DigitCanonW
	digitCanonH = vision.DigitCanonH

	// digitConfFloor sits between the measured wrong-answer scores (<=0.45)
	// and the measured correct-answer scores (>=0.54). `3` is the known weak
	// case (a real 3 can reach 0.65 as an 8 on a very clean glyph), so the
	// floor is a filter for junk, not a guarantee of a perfect digit.
	digitConfFloor = 0.50

	// digitConfMargin is the lead the winning digit must have over the
	// runner-up before the read counts as a measurement.
	//
	// This is the honest half of the reader, and the reason to prefer
	// "unreadable" over a guess. At 720p a count glyph is 11-13px tall, which
	// is small enough that `2`, `3` and `8` genuinely overlap: measured on the
	// corpus frames a real `2` scores 0.59-0.72 while a real `8` scores
	// 0.60-0.66, so a clear winner exists most of the time and a coin-flip
	// exists exactly when the glyph is ambiguous. Reporting unreadable there
	// costs one fallen-back count (the caller uses the strategy amount, or
	// reconciles against the visual check); reporting 8 instead of 3 costs
	// five taps fired into empty ground or five troops left on the bar.
	digitConfMargin = 0.20

	// digitConfCount is the score above which a component is treated as part of a
	// count label rather than as card art. It is BELOW digitConfFloor: a glyph
	// this good is recognisably a digit even when the digit cannot be settled.
	//
	// The bar is set between the two populations that were measured. Artwork that
	// merely resembles a digit — the blobs on spent cards — tops out at 0.53,
	// while a real glyph that could not be resolved scores 0.63 and up (a `3`
	// matched as an `8` at 0.63, a glyph Vision reads as `6` matched as a `9` at
	// 0.69). Telling those apart is what lets a spent card be recognised as spent
	// while a full card whose number is unclear is reported as unknown.
	digitConfCount = 0.58

	// badgeConf* are the same three numbers for the count badge, tightened
	// against the junk that shares its row and loosened where the badge font
	// genuinely is ambiguous. Measured on the live event bar: correct badge
	// digits scored 0.650-0.809 (weakest `9`), while every piece of card art and
	// every `x` prefix glyph in the same windows scored at most 0.480. The floor
	// therefore sits at 0.60 — above all measured junk, below the weakest
	// measured digit — and the margin at 0.10, the lead the `9` actually had.
	badgeConfFloor  = 0.60
	badgeConfMargin = 0.10
	badgeConfStrong = 0.72

	// digitConfStrong is the score at which a glyph is believed on its own
	// strength, with no decisive margin required.
	//
	// Some digits are built from strokes that another digit shares, so the
	// runner-up stays close no matter how clean the glyph is: the `7` of a
	// verified `x7` label scores 0.81 against the `7` template but only 0.11
	// clear of its runner-up, and a margin gate alone would throw that read away.
	//
	// The cut sits between the two ends that were actually measured: every digit
	// whose reading turned out to be ambiguous (a `3` matched as an `8` at 0.63;
	// a glyph independent OCR reads as `6` matched as a `9` at 0.69) scores below
	// 0.75, and the verified clean glyphs that lack a decisive margin score above
	// it. Below the cut the margin decides and an unclear glyph is declined.
	digitConfStrong = 0.75

	// Mask geometry for finding the glyphs. The adaptive window is a
	// reference-pixel size scaled to the live frame; the contrast delta is a
	// luminance offset (resolution-independent).
	glyphBlockRef      = 11.0
	glyphContrastDelta = 10.0

	// brightGlyphCut is the global cut used as the second extraction
	// hypothesis. See glyphBoxes for why both are needed.
	brightGlyphCut = 160.0

	// labelFillCut is the global cut used as the fourth extraction hypothesis:
	// the near-white FILL of a count label, separated from card art that is
	// merely bright.
	//
	// CoC draws the count in near-white over a dark outline, so a cut just below
	// white isolates the label from the sprite behind it. Measured on the saved
	// live 720p bar (tmp/live_check/run14_bar.png): the Electro Dragon and Balloon
	// label fills sit at 220-255 while the art touching them sits at 160-219, and
	// the existing brightGlyphCut of 160 therefore welded glyph and art into one
	// whole-band blob that every size filter rejected. The two cards came back as
	// measured zeros and the sweep wrote them off as deployed.
	//
	// 200 is chosen from the measured gap, not guessed: 180 still merges the two
	// populations, while 220 loses the dimmer glyphs of a label whose fill is not
	// saturated (the `1` of a verified `21` disappears at 220). Everywhere
	// between the two the problem cards read and the known-good cards keep their
	// existing, higher-confidence detections.
	labelFillCut = 200.0

	// countDigitGapRef is the largest horizontal gap that can separate two
	// glyphs of the same number label, in reference pixels.
	countDigitGapRef = 4.0

	// The count BADGE band, in reference px from the troop bar's top edge, and
	// the slack the search window allows around the row it is measured onto.
	//
	// CoC draws the per-card count as an `xN` beside the card's TOP edge — above
	// the slot's identity point and in a font about a third larger than the
	// unit-LEVEL number. The level is drawn at the card's bottom-left, on the row
	// this reader used to sample (slotY+9..+17 reference px), and that is the bug
	// this band replaces: a level is a STATIC number, so every count the old
	// reader reported stopped describing the card the moment the first troop
	// left it — and wrong in the worst direction. Live 2026-09-24, the event
	// card's badge read `x40` while its level read `16` and the reader reported
	// 18; the sweep fired 25 taps at a 40-troop card, left ~29 troops on the bar,
	// then wrote the card off as spent ("100% Deployed").
	//
	// Measured at 720p on that bar, whose top edge is bar_y=621: the badge glyphs
	// occupy y 596..614, i.e. bar_y-25 .. bar_y-7. The band below is that
	// measurement with slack on both sides.
	badgeBandAboveRef = 38.0
	badgeBandBelowRef = 2.0
	countBandSlackRef = 9.0

	// The badge font's height window, as a fraction of expectGlyphH (the level
	// font's template height scaled to this geometry). Measured live: badge
	// glyphs are 13-16px tall against 12px level glyphs, so the level window
	// (0.68..1.0) rejects the badge outright — which is the second half of why
	// the old reader only ever saw the level. The window's floor stays above the
	// level font on purpose: if a future layout moves the badge onto the level's
	// row, this reader reports unreadable instead of silently reporting levels
	// as counts again.
	badgeGlyphMinFactor = 0.85
	badgeGlyphMaxFactor = 1.75

	// badgeSearchHalfRef is half the width of the badge search window, in
	// reference px. It has to reach a badge belonging to a card most of a card
	// width away from the slot anchor (manual_slots.json's slot_xs are stale by up
	// to ~45px live), and the run's own right edge then picks the card.
	badgeSearchHalfRef = 62.0

	// badgeRightToCentreRef converts a badge run's right edge into the x of the
	// card that carries it: measured live, the badge text ends 1-5px inside the
	// card's right edge and the card is ~88px wide, so the centre sits ~41px left
	// of the run's right edge.
	badgeRightToCentreRef = 31.0

	// badgeAnchorToleranceRef is the cap described on glyphReadOpts. It is wide
	// enough for the stale anchors and narrower than the gap to the next card.
	badgeAnchorToleranceRef = 20.0

	// badgeRowHalfRef is half the measured badge row's height in reference px.
	badgeRowHalfRef = 6.0

	// labelRowSupport is how many distinct cards must contribute a count-like
	// glyph to the same row before the frame itself is trusted as the row's
	// location.
	labelRowSupport = 2

	// A count label is at most three digits; more glyphs than this in one band
	// means we are looking at artwork, not a number.
	maxCountDigits = 3

	// barProbeWindowRef is the width of one horizontal window of the
	// frame-level legibility probe.
	barProbeWindowRef = 160.0
)

// NewTroopCounter creates a new troop counter with digit templates.
func NewTroopCounter(cal *game.Calibration, refW, refH int, logger zerolog.Logger) *TroopCounter {
	tc := &TroopCounter{
		logger: logger.With().Str("component", "troop_counter").Logger(),
		scale:  func(px float64) float64 { return px },
	}
	if cal != nil {
		tc.scale = cal.Length
	}
	tc.loadDigitTemplates()
	return tc
}

// loadDigitTemplates loads digit_0..digit_9 templates from the templates
// directory and precomputes their canonical form.
func (tc *TroopCounter) loadDigitTemplates() {
	digitDir := paths.Resolve("templates/digits")
	if _, err := os.Stat(digitDir); os.IsNotExist(err) {
		digitDir = paths.Resolve("templates")
	}

	loaded := 0
	for i := 0; i < 10; i++ {
		name := fmt.Sprintf("digit_%d", i)
		path := filepath.Join(digitDir, name+".png")
		gray := gocv.IMRead(path, gocv.IMReadGrayScale)
		if gray.Empty() {
			tc.logger.Debug().Str("name", name).Msg("digit template not found")
			continue
		}

		// The ink mask locates the glyph; the crop fed to the matcher is the
		// grayscale ink box, so the template keeps the same anti-aliasing the
		// live glyph will have.
		bin := gocv.NewMat()
		gocv.Threshold(gray, &bin, 128, 255, gocv.ThresholdBinary)
		rect := tightBoundingBox(bin)
		bin.Close()

		crop := gray
		cropped := false
		if !rect.Empty() {
			crop = gray.Region(rect)
			cropped = true
		}
		tight := crop.Clone()
		if cropped {
			crop.Close()
		}
		tc.digitTemplates[i] = tight

		tc.digitCanon[i] = vision.CanonicalGlyph(tight)

		if h := tight.Rows(); h > tc.tplH {
			tc.tplH = h
		}
		if w := tight.Cols(); w > tc.tplW {
			tc.tplW = w
		}
		gray.Close()
		loaded++
	}

	tc.logger.Info().
		Int("loaded", loaded).
		Int("tpl_h", tc.tplH).
		Int("tpl_w", tc.tplW).
		Msg("digit templates loaded")
}

// expectGlyphH is the glyph height the reader expects at this geometry: the
// template ink height scaled the way the rest of the HUD scales. The measured
// 720p glyphs (11-13px) sit inside the accepted range but UNDER this figure,
// because CoC's HUD text grows more slowly than the display scale — which is
// exactly why the filter is a range and not an equality.
func (tc *TroopCounter) expectGlyphH() float64 {
	h := float64(tc.tplH)
	if h <= 0 {
		h = 11
	}
	return h * tc.scale(1)
}

// DetectCounts detects troop counts for all slots on the bar.
//
// This is the one call that sees the whole bar, so it is also where the label
// row is located: it measures the row from the frame (measureLabelRow) and
// remembers it, and every later per-slot read — the deploy reconcile, the sweep,
// the verifier — reuses that row. That matters because the row cannot be derived
// from the slot point alone: the live run's slot point comes from
// manual_slots.json, whose `slot_y` is a reference-space value used verbatim, and
// it sat 11px away from the row the geometry implies. Anchored blindly to it, the
// search window started below the glyphs, every label was clipped by the window
// edge, and the bar read as nine unreadable cards plus one lucky hit from the
// wide fallback band.
func (tc *TroopCounter) DetectCounts(screen gocv.Mat, slots []*TrackedSlot, barY int) []TroopCount {
	if row, ok := tc.measureCountRow(screen, slots, barY); ok {
		tc.rowTop, tc.rowBottom = row.top, row.bottom
	} else {
		tc.rowTop, tc.rowBottom = 0, 0
	}

	var results []TroopCount

	readable := 0
	for _, slot := range slots {
		count := tc.detectSlotCount(screen, slot.X, slot.Y, barY)
		if count.OK {
			readable++
		}
		results = append(results, count)
	}

	event := tc.logger.Info().
		Int("slots", len(slots)).
		Int("readable", readable)
	if tc.rowBottom > tc.rowTop {
		event = event.Int("label_row_top", tc.rowTop).Int("label_row_bottom", tc.rowBottom)
	} else {
		event = event.Bool("label_row_measured", false)
	}
	event.Msg("troop count read")
	return results
}

// frameRead is one frame's read of the whole bar: the per-slot verdicts, which
// cards showed a label, and whether the frame let the reader see the bar at all.
type frameRead struct {
	counts      []TroopCount
	labelSeen   []bool
	labelCards  int
	measuredAny bool
}

// readFrame measures the label row from one frame and reads every slot on it.
// Splitting this out is what lets DetectCounts (one frame) and
// DetectCountsMerged (many) share one definition of a single read.
func (tc *TroopCounter) readFrame(screen gocv.Mat, slots []*TrackedSlot, barY int) frameRead {
	if row, ok := tc.measureCountRow(screen, slots, barY); ok {
		tc.rowTop, tc.rowBottom = row.top, row.bottom
	} else {
		tc.rowTop, tc.rowBottom = 0, 0
	}

	fr := frameRead{
		counts:    make([]TroopCount, 0, len(slots)),
		labelSeen: make([]bool, 0, len(slots)),
	}
	for _, slot := range slots {
		res, seen := tc.detectSlotCountDetailed(screen, slot.X, slot.Y, barY)
		fr.counts = append(fr.counts, res)
		fr.labelSeen = append(fr.labelSeen, seen)
		if seen {
			fr.labelCards++
		}
		if res.OK {
			fr.measuredAny = true
		}
	}
	return fr
}

// DetectCountsMerged reads the bar across several frames and reports the count a
// card's frames agree on.
//
// A single capture is the wrong unit for this read. The troop bar animates in at
// the start of a battle, and a card caught mid-animation can carry no glyph the
// reader can find at all: on one live 720p deploy frame the reader found count
// labels on three of ten cards and read one number, while the SAME bar read four
// of six labels clearly on a saved frame a second later — and the corpus already
// documents a card "drawn over-exposed" mid-deployment with no glyph on its label
// row. The count is a stable property of the card until the first drop, so the
// frame is the noise and the number is the signal.
//
// The merge keeps the reader's promise that it never reports a number a card does
// not carry:
//
//   - positive reads that AGREE are taken;
//   - positive reads that DISAGREE mean the bar cannot be trusted, so the slot is
//     reported unknown rather than guessed;
//   - no positive read but a label seen on some frame means the card has a number
//     the reader could not settle: unknown, never a zero;
//   - no positive read, no label anywhere, and at least one frame where the reader
//     could see the bar means the card is spent: a measured zero.
func (tc *TroopCounter) DetectCountsMerged(frames []gocv.Mat, slots []*TrackedSlot, barY int) []TroopCount {
	type vote struct {
		positives    map[int]int
		labelSeen    bool
		measuredZero bool
	}
	votes := make([]vote, len(slots))
	frameVerbose := false
	framesUsed := 0

	for _, frame := range frames {
		if frame.Empty() || frame.Cols() < 2 || frame.Rows() < 2 {
			continue
		}
		framesUsed++
		fr := tc.readFrame(frame, slots, barY)
		if fr.measuredAny || fr.labelCards > 0 {
			frameVerbose = true
		}
		for i, res := range fr.counts {
			v := &votes[i]
			if fr.labelSeen[i] {
				v.labelSeen = true
			}
			if !res.OK {
				continue
			}
			if res.Count > 0 {
				if v.positives == nil {
					v.positives = make(map[int]int)
				}
				v.positives[res.Count]++
			} else {
				v.measuredZero = true
			}
		}
	}

	results := make([]TroopCount, 0, len(slots))
	agreed, zeroed, undecided := 0, 0, 0
	for i, slot := range slots {
		res := TroopCount{X: slot.X, Count: 0}
		v := votes[i]
		switch {
		case len(v.positives) == 1:
			for count := range v.positives {
				res.Count = count
			}
			res.OK = true
			agreed++
		case len(v.positives) > 1:
			// Frames read different numbers off one card. The gate makes a wrong
			// positive read rare, not impossible, and there is no way to pick
			// between them from here — so this card is unknown and the caller
			// falls back to its strategy amount and visual reconcile.
			undecided++
			tc.logger.Warn().
				Int("x", slot.X).
				Interface("counts", v.positives).
				Msg("frames disagree on this card's count; reporting it unknown rather than guessing")
		case v.labelSeen:
			undecided++
		case v.measuredZero && frameVerbose:
			res.OK = true
			zeroed++
		default:
			undecided++
		}
		results = append(results, res)
	}

	tc.logger.Info().
		Int("frames", framesUsed).
		Int("bar_y", barY).
		Int("slots", len(slots)).
		Int("counts_agreed", agreed).
		Int("measured_zero", zeroed).
		Int("undecided", undecided).
		Int("label_row_top", tc.rowTop).
		Int("label_row_bottom", tc.rowBottom).
		Msg("troop counts merged across frames")
	return results
}

// detectSlotCount reads the count above one slot's card.
func (tc *TroopCounter) detectSlotCount(screen gocv.Mat, slotX, slotY, barY int) TroopCount {
	res, _ := tc.detectSlotCountDetailed(screen, slotX, slotY, barY)
	return res
}

// detectSlotCountDetailed is detectSlotCount plus the third outcome the merge
// needs: whether a count LABEL was seen on this card even though its digits could
// not be settled. "No label" and "a label I could not read" are both OK=false, and
// a merge that cannot tell them apart would let a card with an unreadable number
// be written off as spent on a frame where the number happened to be invisible.
// SlotHasCountLabel reports whether a count badge is drawn on the card at
// (slotX, slotY).
//
// It is the "there is a card here holding units" signal: a card with troops
// always carries its `xN` badge, while an EMPTY position in the troop bar
// carries nothing at all. That is the difference between a seasonal/bonus card
// whose count could not be read — which the sweep must still try to place — and
// empty bar space, which the sweep must not tap ~24 times (live 2026-09-25,
// run11: the positions at x=1048/x=1148 took blind sweeps from both the troop
// pass and the event pass).
//
// A hero card has no `xN` badge either (it shows a level), so callers must not
// use this as a general "is there a card" test: identify by name first.
func (tc *TroopCounter) SlotHasCountLabel(screen gocv.Mat, slotX, slotY, barY int) bool {
	if tc == nil || screen.Empty() || !tc.HasDigitTemplates() {
		return false
	}
	_, labelSeen := tc.detectSlotCountDetailed(screen, slotX, slotY, barY)
	return labelSeen
}

func (tc *TroopCounter) detectSlotCountDetailed(screen gocv.Mat, slotX, slotY, barY int) (TroopCount, bool) {
	result := TroopCount{X: slotX, Count: 0}

	if screen.Empty() {
		return result, false
	}
	if !tc.HasDigitTemplates() {
		return result, false
	}

	// The count is read on the row the frame itself shows (measured once per bar
	// by DetectCounts/readFrame), falling back to the band the configured
	// geometry implies when no row could be measured. The unit LEVEL row is never
	// read — see the badgeBandAboveRef note for what reading it cost.
	labelPresent := false
	slack := int(tc.scale(countBandSlackRef))
	rows := tc.countRowCandidates(barY)
	frameRowMeasured := rows[0].source == "frame"
	for i, row := range rows {
		res, read, present := tc.readBadgeRow(screen, slotX, row, slack)
		if read {
			res.X = slotX
			if i > 0 && frameRowMeasured {
				// This frame showed the reader where its badges are, and this card's
				// window holds nothing on that row. The geometry band is the fallback
				// for frames that measured no row at all (a bar caught mid-animation),
				// NOT a second chance for one card: on the corpus pre-deploy frame it
				// turned a 17px-tall piece of card art at y 587..603 into a `2` on a
				// card whose badge row reads nothing, and the deployer fires the taps
				// a fabricated count asks for. Its glyphs still count as a label seen,
				// so the card is reported unknown rather than spent.
				tc.logger.Debug().
					Int("x", slotX).
					Int("row_top", row.top).
					Int("count", res.Count).
					Msg("ignoring a count found off the badge row this frame measured")
				labelPresent = labelPresent || present
				continue
			}
			if i > 0 {
				tc.logger.Warn().
					Int("x", slotX).
					Int("bar_y", barY).
					Int("row_top", row.top).
					Int("count", res.Count).
					Msg("count read on the geometry-derived badge band, not the row measured from this frame; the frame's badge row was not measured")
			}
			return res, false
		}
		labelPresent = labelPresent || present
	}

	// A label is on this card but could not be read: part of a number matched
	// while the whole could not be assembled (an ambiguous `3` next to a clear
	// `1`, say). Reporting a zero here would be the original bug in miniature —
	// the deployer would treat a full card as spent — so the read stays unknown
	// and the caller falls back to its visual reconcile.
	if labelPresent {
		tc.logger.Warn().
			Int("x", slotX).
			Int("slot_y", slotY).
			Msg("count label present but not readable; reporting the count as unknown")
		return result, true
	}

	// Nothing label-shaped on either row. Whether that means "card is spent" or
	// "the reader is blind" is a frame-level question, so BarLegible answers it.
	if tc.BarLegible(screen, barY, slotY) {
		result.OK = true
		return result, false
	}
	return result, false
}

// labelRow is one candidate y range for the count label in the frame being read.
type labelRow struct {
	top, bottom int
	source      string
}

// countRowCandidates returns the rows the count badge could be on in this
// frame, most trustworthy first: the row measured from the frame itself, then
// the band implied by the configured geometry. A row that was not measured is
// omitted.
func (tc *TroopCounter) countRowCandidates(barY int) []labelRow {
	var rows []labelRow
	if tc.rowBottom > tc.rowTop {
		rows = append(rows, labelRow{tc.rowTop, tc.rowBottom, "frame"})
	}
	rows = append(rows, tc.badgeGeometryRow(barY))
	return rows
}

// badgeGeometryRow is the badge band implied by the configured geometry. The
// badge is drawn against the troop bar's top edge, not against the slot point,
// so barY is the anchor: that survives a stale manual_slots.json `slot_y`, and
// it cannot drift onto the unit-level row at the card's bottom.
func (tc *TroopCounter) badgeGeometryRow(barY int) labelRow {
	return labelRow{
		top:    barY - int(tc.scale(badgeBandAboveRef)),
		bottom: barY - int(tc.scale(badgeBandBelowRef)),
		source: "geometry",
	}
}

// measureCountRow finds the y range the count badges occupy by looking at the
// frame rather than trusting a slot anchor.
//
// The badge row is a property of the BAR, not of the derived slot point: at
// 1280x720 the badges sit at y 596..614 while manual_slots.json's `slot_y` (682)
// describes the unit-level row far below it, which is how every previous read
// ended up being a level. Anchoring to barY instead survives a stale slot anchor.
//
// The row is found by consensus: every card contributes the badge-shaped glyphs
// it shows, and the row is the y range that the most DISTINCT cards agree on. A
// single card's artwork cannot define the row, and a frame where fewer than
// labelRowSupport cards agree is reported as unmeasured so the caller falls back
// to the band the configured geometry implies.
func (tc *TroopCounter) measureCountRow(screen gocv.Mat, slots []*TrackedSlot, barY int) (labelRow, bool) {
	if screen.Empty() || !tc.HasDigitTemplates() || len(slots) == 0 {
		return labelRow{}, false
	}

	band := tc.badgeGeometryRow(barY)
	yTop, yBottom := clampRange(band.top, band.bottom, 0, screen.Rows())
	if yBottom-yTop < int(tc.scale(16)) {
		return labelRow{}, false
	}

	half := int(tc.scale(badgeRowHalfRef))
	cardWidth := int(tc.scale(60))

	// centres[v] = the set of cards that showed a count-like glyph centred at y.
	centres := map[int]map[int]bool{}
	for i, slot := range slots {
		x1, x2 := clampRange(slot.X-cardWidth/2, slot.X+cardWidth/2, 0, screen.Cols())
		if x2-x1 < 4 {
			continue
		}
		roi := screen.Region(image.Rect(x1, yTop, x2, yBottom))
		gray := gocv.NewMat()
		if roi.Channels() == 3 {
			gocv.CvtColor(roi, &gray, gocv.ColorBGRToGray)
		} else {
			roi.CopyTo(&gray)
		}
		roi.Close()

		for _, b := range tc.badgeGlyphBoxes(gray) {
			digit, conf, _ := tc.matchGlyphEitherPolarity(gray, b)
			if !looksLikeCountDigit(digit, conf) {
				continue
			}
			centre := yTop + b.Min.Y + b.Dy()/2
			if centres[centre] == nil {
				centres[centre] = map[int]bool{}
			}
			centres[centre][i] = true
		}
		gray.Close()
	}
	if len(centres) == 0 {
		return labelRow{}, false
	}

	// Score each centre by how many distinct cards have a glyph on the row it
	// implies, breaking ties towards the row the configured geometry expects.
	expectedCentre := (band.top + band.bottom) / 2
	bestSupport, bestCentre, bestDistance := 0, 0, 0
	for centre := range centres {
		support := len(cardsOnRow(centres, centre, half))
		distance := centre - expectedCentre
		if distance < 0 {
			distance = -distance
		}
		if support > bestSupport || (support == bestSupport && support > 0 && distance < bestDistance) {
			bestSupport, bestCentre, bestDistance = support, centre, distance
		}
	}
	if bestSupport < labelRowSupport {
		return labelRow{}, false
	}
	return labelRow{top: bestCentre - half, bottom: bestCentre + half, source: "frame"}, true
}

// cardsOnRow reports which cards have a count-like glyph centred within half of
// the given centre, i.e. sharing that row.
func cardsOnRow(centres map[int]map[int]bool, centre, half int) map[int]bool {
	on := map[int]bool{}
	for c, cards := range centres {
		if c < centre-half || c > centre+half {
			continue
		}
		for card := range cards {
			on[card] = true
		}
	}
	return on
}

// readBadgeRow runs the badge glyph pipeline over one card's column of the given
// badge row, with a little slack, and assembles a count from whatever it finds.
// The row is given in absolute frame coordinates.
//
// The window is a slice of one card, so "a component touching the window's own
// edge" is a real signal: the window starts above the row and ends below it, and
// a glyph cut in half by either edge cannot be matched against a whole-glyph
// template.
func (tc *TroopCounter) readBadgeRow(screen gocv.Mat, slotX int, row labelRow, slack int) (TroopCount, bool, bool) {
	if slack <= 0 {
		slack = int(tc.scale(countBandSlackRef))
	}
	// The window is a search area, not a card: it must reach a badge that sits
	// most of a card to either side of a stale slot anchor, and the run's own
	// right edge then decides which card it belongs to (anchorRightToCentreRef).
	half := int(tc.scale(badgeSearchHalfRef))
	x1 := slotX - half
	x2 := slotX + half
	y1 := row.top - slack
	y2 := row.bottom + slack

	y1, y2 = clampRange(y1, y2, 0, screen.Rows())
	x1, x2 = clampRange(x1, x2, 0, screen.Cols())
	if x2-x1 < int(tc.scale(8)) || y2-y1 < int(tc.scale(6)) {
		return TroopCount{}, false, false
	}

	opts := badgeGlyphOpts
	opts.anchorX = slotX

	roi := screen.Region(image.Rect(x1, y1, x2, y2))
	defer roi.Close()

	return tc.readGlyphsWithOpts(roi, image.Pt(x1, y1), row.top, row.bottom, opts)
}

// matchGlyphEitherPolarity matches one glyph crop against the digit templates in
// both polarities and keeps the stronger reading.
//
// CoC draws the count badge in dark ink on a bright plate on some cards (the
// event card measured at 720p) and in bright ink on the card art on others (the
// rage spell card) — a reader fixed to one polarity reads half the bar. The
// templates are bright-ink crops and TM_CCOEFF_NORMED is signed, so an inverted
// crop scores its true digit well below zero: taking the better of the two can
// only rescue inversions, it cannot turn a real glyph into a wrong digit.
func (tc *TroopCounter) matchGlyphEitherPolarity(gray gocv.Mat, box image.Rectangle) (int, float64, float64) {
	sub := gray.Region(box)
	defer sub.Close()
	digit, conf, margin := tc.matchDigitGlyph(sub)

	inv := gocv.NewMat()
	defer inv.Close()
	gocv.BitwiseNot(sub, &inv)
	digitInv, confInv, marginInv := tc.matchDigitGlyph(inv)
	if confInv > conf {
		return digitInv, confInv, marginInv
	}
	return digit, conf, margin
}

// glyphReadOpts tunes one glyph read: which glyph heights to consider, whether to
// try the inverted polarity, whether a leading non-digit glyph may be skipped, and
// whether the run should be chosen by where its card is rather than by confidence.
type glyphReadOpts struct {
	minFactor, maxFactor float64
	invert               bool
	skipLeadingJunk      bool

	// thresholds are the acceptance numbers for the font being read; the zero
	// value means the unit-level font's.
	thresholds glyphThresholds

	// anchorX is the slot's x in frame coordinates. When non-zero, the read
	// prefers the glyph run whose implied CARD is nearest that anchor over the run
	// with the highest confidence — the run to the right of a slot belongs to the
	// next card, and firing that card's count on this one is an under- or
	// over-deploy of a whole unit batch.
	anchorX int

	// anchorToleranceRef is how far a run's implied card centre may sit from the
	// slot anchor and still be accepted as that slot's badge. On a bar whose
	// anchors match the cards, the run's implied centre lands within ~10px of its
	// own slot, so a cap of 20 reference px (26 live) accepts every measured card
	// and refuses the neighbour — the next card's badge implies a centre a full
	// ~100px pitch away. Before the anchors were re-authored to the measured card
	// centres this had to be 42 reference px (56 live), because the stale list sat
	// up to 47px off the cards, and at that looseness a slot halfway between two
	// cards could claim the further one's count.
	anchorToleranceRef float64

	// anchorRightToCentreRef turns a run's right edge into the x of the card the
	// run belongs to: CoC right-aligns the badge inside the card, so the badge's
	// right edge sits about half a card width right of the card's centre. This is
	// what makes the read self-anchoring: with manual_slots.json's slot_xs up to
	// ~45px off the live card centres, the badge itself is the better source for
	// "which card is this".
	anchorRightToCentreRef float64
}

// levelThresholds reads the unit-level font; badgeThresholds reads the count
// badge. Both compare against the same templates.
var (
	levelThresholds = glyphThresholds{floor: digitConfFloor, margin: digitConfMargin, strong: digitConfStrong}
	badgeThresholds = glyphThresholds{floor: badgeConfFloor, margin: badgeConfMargin, strong: badgeConfStrong}
)

func (o glyphReadOpts) thresholdsOrDefault() glyphThresholds {
	if o.thresholds.floor == 0 {
		return levelThresholds
	}
	return o.thresholds
}

// levelGlyphOpts reads the unit-level font: the size window the templates were
// measured at, bright ink, no prefix glyph.
var levelGlyphOpts = glyphReadOpts{minFactor: 0.68, maxFactor: 1.00}

// badgeGlyphOpts reads the count badge: a larger font, either polarity, and the
// `x` prefix the badge carries in front of its digits.
var badgeGlyphOpts = glyphReadOpts{
	minFactor:              badgeGlyphMinFactor,
	maxFactor:              badgeGlyphMaxFactor,
	invert:                 true,
	skipLeadingJunk:        true,
	thresholds:             badgeThresholds,
	anchorRightToCentreRef: badgeRightToCentreRef,
	anchorToleranceRef:     badgeAnchorToleranceRef,
}

// glyphsOnRow keeps only the candidates that sit on the count-label row, given
// as rowTop..rowBottom in the ROI's own coordinates. A glyph must overlap the row
// by most of its height: the label font is the same size on every card, so a
// component that hangs above or below the row is the card's artwork.
func glyphsOnRow(boxes []image.Rectangle, rowTop, rowBottom int) []image.Rectangle {
	kept := boxes[:0]
	for _, b := range boxes {
		top, bottom := b.Min.Y, b.Max.Y
		if top < rowTop {
			top = rowTop
		}
		if bottom > rowBottom {
			bottom = rowBottom
		}
		if overlap := bottom - top; overlap > 0 && float64(overlap) >= 0.6*float64(b.Dy()) {
			kept = append(kept, b)
		}
	}
	return kept
}

// readGlyphsIn finds the count label inside a region and reads it as a number.
//
// A number label is a run of vertically-aligned, horizontally adjacent glyphs,
// so the reader searches for exactly that shape instead of reading whatever the
// masks happened to produce. The distinction matters: card artwork falls inside
// the band too, and the previous "drop every box shorter than 72% of the tallest"
// rule both discarded real digits (whenever one piece of art was taller) and
// kept art as digits.
func (tc *TroopCounter) readGlyphsWithOpts(roi gocv.Mat, origin image.Point, rowTop, rowBottom int, opts glyphReadOpts) (TroopCount, bool, bool) {
	gray := gocv.NewMat()
	defer gray.Close()
	if roi.Channels() == 3 {
		gocv.CvtColor(roi, &gray, gocv.ColorBGRToGray)
	} else {
		roi.CopyTo(&gray)
	}

	boxes := tc.glyphBoxesSized(gray, opts.minFactor, opts.maxFactor)
	if rowBottom > rowTop {
		boxes = glyphsOnRow(boxes, rowTop-origin.Y, rowBottom-origin.Y)
	}
	if len(boxes) == 0 {
		return TroopCount{}, false, false
	}

	type glyph struct {
		rect   image.Rectangle
		digit  int
		conf   float64
		margin float64
	}
	glyphs := make([]glyph, 0, len(boxes))
	for _, b := range boxes {
		var d int
		var conf, margin float64
		if opts.invert {
			d, conf, margin = tc.matchGlyphEitherPolarity(gray, b)
		} else {
			sub := gray.Region(b)
			d, conf, margin = tc.matchDigitGlyph(sub)
			sub.Close()
		}
		glyphs = append(glyphs, glyph{rect: b, digit: d, conf: conf, margin: margin})
	}

	thresholds := opts.thresholdsOrDefault()

	gapMax := int(tc.scale(countDigitGapRef))
	bestConf := 0.0
	bestDistance := 0
	var bestValues []int

	// Each glyph belongs to exactly one label. Confirming a run consumes its
	// glyphs so a later start cannot re-read them as a shorter number: without
	// this, the lone `1` of a `21` scores the same as the pair and overwrites it,
	// turning 21 into 1. A run that cannot be read confidently is poisoned
	// instead — including every shorter run inside it — because reporting a
	// prefix of a label is an under-read, and an under-read leaves troops on the
	// bar.
	consumed := make([]bool, len(glyphs))
	for i := range glyphs {
		if consumed[i] {
			continue
		}
		if opts.skipLeadingJunk && !looksLikeCountDigit(glyphs[i].digit, glyphs[i].conf) {
			// A count badge is written `xN`: the prefix glyph matches no digit.
			// Skipping it, rather than poisoning the run it heads, is what lets
			// `x40` read as 40. Poisoning it would discard the digits with it and
			// leave the troops on the bar — the under-deploy this path exists to
			// prevent.
			continue
		}
		run := []int{i}
		sum := glyphs[i].conf
		for j := i + 1; j < len(glyphs); j++ {
			if len(run) >= maxCountDigits {
				break
			}
			last := glyphs[run[len(run)-1]]
			if gap := glyphs[j].rect.Min.X - last.rect.Max.X; gap > gapMax {
				break
			}
			if !verticallyAligned(last.rect, glyphs[j].rect) {
				continue
			}
			run = append(run, j)
			sum += glyphs[j].conf
		}

		values := make([]int, 0, len(run))
		readable := true
		for _, idx := range run {
			g := glyphs[idx]
			if !thresholds.accepts(g.digit, g.conf, g.margin) {
				readable = false
				break
			}
			values = append(values, g.digit)
		}
		if !readable {
			tc.logger.Debug().
				Int("x", origin.X+glyphs[i].rect.Min.X).
				Int("glyphs", len(run)).
				Msg("glyph run not read confidently; reporting the card count as unreadable")
			for _, idx := range run {
				consumed[idx] = true
			}
			continue
		}

		mean := sum / float64(len(run))
		chosen := mean > bestConf
		if opts.anchorX > 0 {
			// Choose by where the run's card is, not by how clean the glyphs are:
			// a neighbouring card's badge is usually just as clean as this one's.
			right := origin.X + glyphs[run[len(run)-1]].rect.Max.X
			centre := right - int(tc.scale(opts.anchorRightToCentreRef))
			distance := centre - opts.anchorX
			if distance < 0 {
				distance = -distance
			}
			if tol := int(tc.scale(opts.anchorToleranceRef)); tol > 0 && distance > tol {
				// The nearest readable badge is further off than any card's badge can
				// be from its own slot, so this run belongs to a card this slot is not
				// looking at. Firing that card's count here spends a unit batch on the
				// wrong card and leaves this one's troops on the bar, so the read is
				// declined rather than moved to the neighbour.
				for _, idx := range run {
					consumed[idx] = true
				}
				continue
			}
			chosen = bestValues == nil || distance < bestDistance ||
				(distance == bestDistance && mean > bestConf)
			bestDistance = distance
		}
		if chosen {
			bestConf = mean
			bestValues = values
		}
		for _, idx := range run {
			consumed[idx] = true
		}
	}

	if bestValues == nil {
		// No number could be assembled. Whether that means the card is spent or
		// that a label is present but unreadable is the question every caller's
		// behaviour hangs on, so answer it here: a glyph good enough to be a count
		// digit in its own right means a label IS on this card. Its neighbours
		// then put the number out of reach, and reporting a zero would say the
		// card is empty when it is not.
		//
		// The glyph must belong to THIS slot's card, by the same card-centre test
		// the run selection uses. The window reaches a full card to either side, so
		// a spent card's neighbours are always inside it — counting their badges as
		// this card's evidence would make every spent card in the middle of a bar
		// permanently "unreadable" instead of spent, and the sweep would never be
		// able to stop tapping it.
		present := false
		for _, g := range glyphs {
			if !looksLikeCountDigit(g.digit, g.conf) {
				continue
			}
			if opts.anchorX > 0 {
				centre := origin.X + g.rect.Max.X - int(tc.scale(opts.anchorRightToCentreRef))
				distance := centre - opts.anchorX
				if distance < 0 {
					distance = -distance
				}
				if tol := int(tc.scale(opts.anchorToleranceRef)); tol > 0 && distance > tol {
					continue
				}
			}
			present = true
			break
		}
		return TroopCount{}, false, present
	}
	count := 0
	for _, d := range bestValues {
		count = count*10 + d
	}
	if count <= 0 {
		// A count label never reads zero: `0` here would be an artefact (a glyph
		// that happened to match a `0`), and reporting it as a measurement would
		// make a full card look empty.
		return TroopCount{}, false, true
	}

	tc.logger.Debug().
		Int("x", origin.X).
		Int("count", count).
		Interface("digits", bestValues).
		Float64("conf", bestConf).
		Msg("detected troop count")

	return TroopCount{Count: count, Digits: bestValues, Confidence: bestConf, OK: true}, true, true
}

// verticallyAligned reports whether two glyph boxes share a text row: their
// vertical extents overlap by most of the shorter one.
//
// Only the vertical axis is compared, and deliberately so. Digits of one label
// sit side by side, so their x ranges are disjoint by construction — using the
// rectangle intersection here (as this once did) reports "not aligned" for every
// pair of digits in a multi-digit label and truncates `16` to `1`.
func verticallyAligned(a, b image.Rectangle) bool {
	top, bot := a.Min.Y, a.Max.Y
	if b.Min.Y > top {
		top = b.Min.Y
	}
	if b.Max.Y < bot {
		bot = b.Max.Y
	}
	overlap := bot - top
	if overlap <= 0 {
		return false
	}
	short := a.Dy()
	if b.Dy() < short {
		short = b.Dy()
	}
	return float64(overlap) >= 0.7*float64(short)
}

// glyphBoxes returns the bounding boxes of components in gray that are sized
// like a count digit at this geometry.
//
// Two masks are combined, because neither alone sees every card. CoC renders
// the same label bright on one card and dim on the next — the corpus frames
// measure strokes at 110-150 on some cards and 150-255 on others:
//
//   - an ADAPTIVE threshold (a negative C, i.e. "C above your neighbourhood")
//     picks up the dim labels, which a global cut misses entirely: with a fixed
//     160 the reader returns nothing at all for the dim cards' numbers;
//   - a global BRIGHT cut picks up the labels on bright card art, where a
//     local-mean threshold saturates and the glyph merges into its background;
//   - a much HIGHER global cut picks up the near-white fill of a label drawn on
//     a bright sprite, where even the 160 cut welds glyph and art together.
//
// All of them are just candidate generators; the digit matcher and its
// confidence floor decide which candidates are numbers. Overlapping candidates
// are deduplicated so a glyph found by several masks cannot be counted twice.
//
// The size window is derived from the loaded templates rather than hardcoded: a
// glyph is accepted between 0.68x and 1.0x the template height scaled to this
// geometry. That contains the measured 11-13px 720p label glyphs and rejects the
// card artwork, which is what stops a decorative blob from being read as a
// count. The ceiling is the font's own height: nothing the HUD draws as a count
// digit is taller than the digit it is drawn from, and the blobs that are taller
// are the ones that used to be read as a confident `4` on a card whose real
// label sat unread elsewhere in the band.
func (tc *TroopCounter) badgeGlyphBoxes(gray gocv.Mat) []image.Rectangle {
	return tc.glyphBoxesSized(gray, badgeGlyphMinFactor, badgeGlyphMaxFactor)
}

// glyphBoxesSized is glyphBoxes with the accepted glyph height given as fractions
// of expectGlyphH. The count badge is drawn in a larger font than the unit level,
// and the legend the filters use — "a component this tall could be a digit" — is
// font-specific, so it is a parameter rather than a constant.
func (tc *TroopCounter) glyphBoxesSized(gray gocv.Mat, minFactor, maxFactor float64) []image.Rectangle {
	expectH := tc.expectGlyphH()
	minH := int(minFactor * expectH)
	maxH := int(maxFactor * expectH)
	if minH < 6 {
		minH = 6
	}
	if maxH <= minH {
		maxH = minH + 6
	}
	maxW := int(1.2 * maxFactor * expectH)
	if maxW < 6 {
		maxW = 6
	}

	block := int(tc.scale(glyphBlockRef))
	if block%2 == 0 {
		block++
	}
	if block < 5 {
		block = 5
	}

	var boxes []image.Rectangle
	addFrom := func(bin *gocv.Mat) {
		contours := gocv.FindContours(*bin, gocv.RetrievalExternal, gocv.ChainApproxSimple)
		defer contours.Close()
		for i := 0; i < contours.Size(); i++ {
			rect := gocv.BoundingRect(contours.At(i))
			w, h := rect.Dx(), rect.Dy()
			if w < 1 || w > maxW || h < minH || h > maxH {
				continue
			}
			if aspect := float64(w) / float64(h); aspect < 0.12 || aspect > 1.6 {
				continue
			}
			// A component touching the region's own edge is a glyph cut in
			// half by the band: its true height is unknown, so it cannot be
			// matched against a whole-glyph template.
			if rect.Min.Y <= 0 || rect.Max.Y >= gray.Rows()-1 {
				continue
			}
			// Ink density guard: reject near-empty and near-solid components,
			// the two shapes that score spuriously under a normalized
			// correlation.
			sub := bin.Region(rect)
			ink := float64(gocv.CountNonZero(sub)) / float64(w*h)
			sub.Close()
			if ink < 0.10 || ink > 0.92 {
				continue
			}
			boxes = append(boxes, rect)
		}
	}

	// Hypothesis 1: ink brighter than its own neighbourhood.
	adaptive := gocv.NewMat()
	if err := gocv.AdaptiveThreshold(gray, &adaptive, 255, gocv.AdaptiveThresholdMean,
		gocv.ThresholdBinary, block, -glyphContrastDelta); err == nil {
		addFrom(&adaptive)
	}
	adaptive.Close()

	// Hypothesis 2: ink brighter than a global cut, which is what finds the
	// labels on bright card art where a local-mean threshold saturates.
	bright := gocv.NewMat()
	gocv.Threshold(gray, &bright, brightGlyphCut, 255, gocv.ThresholdBinary)
	addFrom(&bright)
	bright.Close()

	// Hypothesis 3: ink DARKER than its own neighbourhood. Some cards draw the
	// count in dark ink over a bright sprite (the corpus documents slot 134 as
	// "dark ink on a bright card sprite"), and both masks above look only for ink
	// brighter than its surroundings, so those labels were invisible: the card
	// came back as a measured zero, the deployer read that as "the card is spent",
	// and the troops were never placed. Measured on the saved live deploy frame,
	// a dark-ink label on the Electro Dragon and Balloon cards left the whole
	// sweep writing both cards off as deployed while their art was pixel-identical
	// to the start of the battle — the "it didn't use all the troops" report.
	//
	// The inverse is a LOCAL mean, not a fixed cut: a global dark threshold turns
	// an entire card sprite into one ink blob whose size the filters reject (and
	// which is how an earlier attempt at this recovered nothing). What makes a
	// glyph is being darker than its neighbourhood by a contrast delta, which is
	// the same rule the bright masks use, reversed.
	dark := gocv.NewMat()
	if err := gocv.AdaptiveThreshold(gray, &dark, 255, gocv.AdaptiveThresholdMean,
		gocv.ThresholdBinaryInv, block, glyphContrastDelta); err == nil {
		addFrom(&dark)
	}
	dark.Close()

	// Hypothesis 4: the label's near-white FILL, cut above the level that card
	// art reaches.
	//
	// This is the case the three masks above all miss, and it is the one that
	// cost the battle its troops: a label drawn over a BRIGHT sprite. The
	// adaptive mask saturates on such a card (its neighbourhood is bright too),
	// the 160 cut welds glyph and art together into a band-sized blob, and the
	// dark mask isolates the glyph's outline but reports it several px taller than
	// the glyph because the outline extends past the fill — so every candidate is
	// rejected and the card reads as a measured zero.
	//
	// On the saved live bar this recovers the Electro Dragon's `9` and the
	// Balloon's two-glyph run where nothing at all was found before. It is added
	// LAST and deliberately so: the dedupe below keeps the first candidate seen
	// for a given glyph, so the existing, higher-confidence detections on the
	// cards that already read are preserved verbatim and this mask can only ever
	// contribute candidates that no other mask found.
	fill := gocv.NewMat()
	gocv.Threshold(gray, &fill, labelFillCut, 255, gocv.ThresholdBinary)
	addFrom(&fill)
	fill.Close()

	sort.Slice(boxes, func(i, j int) bool { return boxes[i].Min.X < boxes[j].Min.X })

	// Deduplicate: the same glyph found by both masks must count once, or a
	// single digit would be assembled into the number twice.
	var out []image.Rectangle
	for _, r := range boxes {
		skip := false
		for _, k := range out {
			if overlapRatio(r, k) > 0.5 {
				skip = true
				break
			}
		}
		if !skip {
			out = append(out, r)
		}
	}
	return out
}

// overlapRatio reports the intersection of a and b as a fraction of the smaller
// of the two, so a box contained inside another scores 1.0.
func overlapRatio(a, b image.Rectangle) float64 {
	inter := a.Intersect(b)
	if inter.Empty() {
		return 0
	}
	small := a.Dx() * a.Dy()
	if x := b.Dx() * b.Dy(); x < small {
		small = x
	}
	if small == 0 {
		return 0
	}
	return float64(inter.Dx()*inter.Dy()) / float64(small)
}

// matchDigitGlyph reads a single grayscale glyph crop: it predicts the digit,
// the winning score, and the margin over the runner-up.
//
// The crop is resized to the canonical box first, so the comparison is a shape
// comparison at a fixed scale: it does not care how wide the glyph happened to
// be or how much of the crop was padding, which is what made the old
// resize-template-to-contour-box version score 0.05-0.45 for correct digits.
func (tc *TroopCounter) matchDigitGlyph(glyph gocv.Mat) (int, float64, float64) {
	// The canonical-box shape match itself lives in internal/vision, shared with
	// the loot/damage reader, which had converged on the opposite (broken)
	// approach of resizing each template to the glyph's box. The acceptance
	// thresholds below stay here: they are tuned to this reader's grayscale ink
	// crops, not to the matcher.
	canon := vision.CanonicalGlyph(glyph)
	defer canon.Close()
	scores := vision.CanonicalScores(canon, tc.digitCanon[:])
	for i := range scores {
		if !tc.templateAspectCompatible(i, glyph) {
			scores[i] = vision.UnusableScore
		}
	}
	return vision.PickBest(scores)
}

// glyphAspectTolerance is how far a template's ink proportions may differ from a
// glyph's before the template is ruled out as a possible reading of it. 1.5 is
// deliberately loose: it is there to remove templates that are nothing like the
// glyph's shape, not to make close calls, and the `1` this was written for sits
// at 1.44 (a 4x11 template against a 3x12 glyph).
const glyphAspectTolerance = 1.5

// templateAspectCompatible reports whether a digit template's ink could be the
// glyph in front of it, judged by proportion alone.
//
// The matcher compares shapes after BOTH sides are resized into one canonical box,
// which is what makes it immune to glyph size — and also what throws away the most
// discriminative fact about a small glyph: a `1` is a bare vertical stroke.
// Measured on a live 720p bar, a real `1` (3px wide, 12px tall) scored 0.548
// against the `1` template (ink 4x11) with the `3` template 0.364 behind it — a
// margin of 0.184, just under the 0.20 bar, so every label containing a `1` (21,
// 12, 11, 13...) was declined and the deployer fired a blind batch instead. A
// 3px-wide glyph cannot be a digit drawn 7-9px wide at the same font height, so
// those templates are not eligible to be its runner-up either.
//
// This removes nothing from the genuinely ambiguous cases: a 9px `6` and a 9px `8`
// are both eligible for a 9px glyph, so the margin still decides between them.
func (tc *TroopCounter) templateAspectCompatible(digit int, glyph gocv.Mat) bool {
	if digit < 0 || digit >= len(tc.digitTemplates) || glyph.Empty() {
		return false
	}
	tpl := tc.digitTemplates[digit]
	if tpl.Empty() || tpl.Rows() == 0 || glyph.Rows() == 0 {
		return false
	}
	glyphAspect := float64(glyph.Cols()) / float64(glyph.Rows())
	tplAspect := float64(tpl.Cols()) / float64(tpl.Rows())
	if glyphAspect <= 0 || tplAspect <= 0 {
		return false
	}
	ratio := tplAspect / glyphAspect
	return ratio <= glyphAspectTolerance && ratio >= 1/glyphAspectTolerance
}

// BarLegible reports whether the count row of the troop bar is readable at
// all in this frame.
//
// This is what separates the two meanings of "no digits above this card": a
// spent card (the label is gone because the card is empty) versus a frame the
// reader cannot read (dimmed by a dialog, a resize mid-render, a theme whose
// labels do not match the templates). Callers must only trust a zero count
// when the reader is demonstrably working on this frame — otherwise they are
// re-deriving the original bug, where a permanently-blind reader was
// indistinguishable from a bar full of empty cards.
//
// One readable digit is the bar: it says the reader's thresholds work at this
// frame's brightness, scale and theme. (A stricter rule — two digits a card
// apart — was tried and reverted: it breaks the deliberate contract that a bar
// carrying a single readable label is legible, which is the normal state of a
// nearly-spent bar. The residual risk it was aimed at, a hero card's LEVEL
// number being read as a count and legitimising zeros elsewhere, is real in the
// logs but unproven in the pixels; see the note in docs/SEEING.md.)
func (tc *TroopCounter) BarLegible(screen gocv.Mat, barY, slotY int) bool {
	if screen.Empty() || !tc.HasDigitTemplates() {
		return false
	}

	slack := int(tc.scale(countBandSlackRef))
	window := int(tc.scale(barProbeWindowRef))
	if window < 40 {
		window = 40
	}

	// Same rows the per-slot read uses: the frame's measured badge row when there
	// is one, then the band the geometry implies. A legibility verdict anchored to
	// a row the reader does not read from would answer a different question than
	// the one asked.
	for _, row := range tc.countRowCandidates(barY) {
		y1, y2 := clampRange(row.top-slack, row.bottom+slack, 0, screen.Rows())
		if y2-y1 < 4 {
			continue
		}
		for x := 0; x < screen.Cols(); x += window {
			x2 := x + window
			if x2 > screen.Cols() {
				x2 = screen.Cols()
			}
			if x2-x <= 0 {
				continue
			}
			roi := screen.Region(image.Rect(x, y1, x2, y2))
			legible := tc.regionHasReadableDigit(roi, row.top-y1, row.bottom-y1)
			roi.Close()
			if legible {
				return true
			}
		}
	}
	return false
}

// regionHasReadableDigit reports whether a region contains a glyph that matches
// a digit above the acceptance floor.
func (tc *TroopCounter) regionHasReadableDigit(roi gocv.Mat, rowTop, rowBottom int) bool {
	gray := gocv.NewMat()
	defer gray.Close()
	if roi.Channels() == 3 {
		gocv.CvtColor(roi, &gray, gocv.ColorBGRToGray)
	} else {
		roi.CopyTo(&gray)
	}

	boxes := tc.badgeGlyphBoxes(gray)
	if rowBottom > rowTop {
		boxes = glyphsOnRow(boxes, rowTop, rowBottom)
	}
	for _, b := range boxes {
		digit, conf, margin := tc.matchGlyphEitherPolarity(gray, b)
		if glyphAccepted(digit, conf, margin) {
			return true
		}
	}
	return false
}

// clampRange orders and clamps a [lo,hi) interval to [min,max].
func clampRange(lo, hi, min, max int) (int, int) {
	if lo > hi {
		lo, hi = hi, lo
	}
	if lo < min {
		lo = min
	}
	if hi > max {
		hi = max
	}
	if hi < lo {
		hi = lo
	}
	return lo, hi
}

// tightBoundingBox finds the tight bounding box of white pixels in a binary image.
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

// GetCountForSlot returns the detected count for a specific slot X coordinate.
// A slot whose read was not trustworthy reports 0 — callers that need to tell
// "empty" from "unreadable" must use DetectCounts and inspect TroopCount.OK.
func GetCountForSlot(counts []TroopCount, slotX int) int {
	for _, c := range counts {
		if c.X == slotX {
			return c.Count
		}
	}
	return 0
}

// GetCountForSlotMeasured returns the slot's battle-start count together with
// whether the reader actually measured it. The count alone cannot say: an
// unreadable card and an empty one both come back 0. Callers that use the
// number to decide a tap count need the difference (see
// HeroManager.DeployTroops's detectedTrusted argument).
func GetCountForSlotMeasured(counts []TroopCount, slotX int) (int, bool) {
	for _, c := range counts {
		if c.X == slotX {
			return c.Count, c.OK
		}
	}
	return 0, false
}

// GetAllCounts returns a map of slot X -> count for the slots whose read was a
// trustworthy measurement. Slots with no readable count are omitted rather
// than reported as 0, so a blind read can never be mistaken downstream for a
// measured empty card.
func GetAllCounts(counts []TroopCount) map[int]int {
	result := make(map[int]int)
	for _, c := range counts {
		if c.OK {
			result[c.X] = c.Count
		}
	}
	return result
}

// ReadableCounts reports how many of these reads were trustworthy measurements
// out of the slots present.
func ReadableCounts(counts []TroopCount) (readable, total int) {
	for _, c := range counts {
		if c.OK {
			readable++
		}
	}
	return readable, len(counts)
}

// HasDigitTemplates returns true if digit templates are loaded.
func (tc *TroopCounter) HasDigitTemplates() bool {
	for _, tpl := range tc.digitTemplates {
		if !tpl.Empty() {
			return true
		}
	}
	return false
}

// unreadableDumpBudget is how many bar bands one battle may keep. The budget is
// small on purpose: six frames from the first unreadable read answer "what does
// an unreadable badge look like at this geometry" as well as six hundred would,
// and six hundred would cost more than the deploy they were recording.
const unreadableDumpBudget = 6

// dumpUnreadableBarBand saves the troop bar as the reader saw it when a card's
// count label was present but its digits could not be settled, with the windows
// the reader searched drawn on top.
//
// # Why
//
// "count label present but not readable" is the one reader outcome that costs
// real battle time: the caller has no number to fire and no proof the card is
// spent, so it fires a blind top-up batch and reconciles again. Measured live
// 2026-09-27, Balloon and Electro Dragon each burned three reconcile rounds that
// way (~9 extra taps each) and the battle deployed in 26s against a <7s target.
//
// The log line alone cannot say which of the two causes that was:
//
//	(a) the badge is drawn outside the window the reader searched (geometry), or
//	(b) the digits are inside it but their shape does not match the templates.
//
// Those need opposite fixes — one moves a window, the other changes matching —
// and no amount of log reading separates them, because the reader reports the
// same sentence for both. The frame separates them, so the frame is kept: the
// band is drawn with every badge-row candidate it tried (red for the row
// measured from this frame, orange for the geometry fallback), the slot anchor
// (yellow) and the numbers that produced them, so the artifact explains itself
// without the log beside it.
func (tc *TroopCounter) dumpUnreadableBarBand(screen gocv.Mat, slotX, slotY, barY int) {
	if tc == nil || screen.Empty() {
		return
	}
	if tc.unreadableDumps >= unreadableDumpBudget {
		return
	}

	slack := int(tc.scale(countBandSlackRef))
	rows := tc.countRowCandidates(barY)
	if len(rows) == 0 {
		return
	}
	top, bottom := rows[0].top-slack, rows[len(rows)-1].bottom+slack
	top, bottom = clampRange(top, bottom, 0, screen.Rows())
	if bottom-top < int(tc.scale(12)) {
		return
	}

	tc.unreadableDumps++

	band := screen.Region(image.Rect(0, top, screen.Cols(), bottom))
	defer band.Close()

	// 2x so an ~11px-tall glyph is legible in an image viewer. The band is a few
	// hundred KB at 720p and the budget caps the total.
	annotated := gocv.NewMat()
	defer annotated.Close()
	gocv.Resize(band, &annotated, image.Pt(0, 0), 2, 2, gocv.InterpolationNearestNeighbor)

	half := int(tc.scale(badgeSearchHalfRef))
	for i, row := range rows {
		x1, x2 := clampRange(slotX-half, slotX+half, 0, screen.Cols())
		y1, y2 := clampRange(row.top-slack, row.bottom+slack, 0, screen.Rows())
		colour := color.RGBA{R: 255, G: 60, B: 60, A: 255}
		if i > 0 {
			colour = color.RGBA{R: 255, G: 170, B: 0, A: 255}
		}
		gocv.Rectangle(&annotated,
			image.Rect(x1*2, (y1-top)*2, x2*2, (y2-top)*2), colour, 1)
	}
	gocv.Line(&annotated,
		image.Pt(slotX*2, 0), image.Pt(slotX*2, annotated.Rows()-1),
		color.RGBA{R: 255, G: 255, B: 0, A: 255}, 1)
	gocv.PutText(&annotated,
		fmt.Sprintf("x=%d slot_y=%d bar_y=%d band=%d..%d rows=%d scale=%.4f",
			slotX, slotY, barY, top, bottom, len(rows), tc.scale(1)),
		image.Pt(6, 18), gocv.FontHersheySimplex, 0.5,
		color.RGBA{R: 0, G: 255, B: 255, A: 255}, 1)

	path := paths.ResolveConfig(fmt.Sprintf("debug_bar_unreadable_%02d.png", tc.unreadableDumps))
	if ok := gocv.IMWrite(path, annotated); !ok {
		tc.unreadableDumps--
		tc.logger.Warn().
			Str("path", path).
			Msg("could not save the bar band for an unreadable count label; the next one will try again")
		return
	}
	tc.logger.Info().
		Str("path", path).
		Int("x", slotX).
		Int("bar_y", barY).
		Int("dumps", tc.unreadableDumps).
		Int("budget", unreadableDumpBudget).
		Msg("saved the troop bar as the reader saw it, with the searched badge windows drawn; the unreadable count label is in this frame")
}

// DetectCount reads the count above a single slot's card in the provided
// screen, and reports whether the read is a trustworthy measurement.
//
// `ok=false` means "unknown", not "zero": the caller must not conclude the
// card is spent. `ok=true, count=0` means the card shows no count label while
// other cards in the bar still do — i.e. the card is spent or locked.
func (tc *TroopCounter) DetectCount(screen gocv.Mat, slot *TrackedSlot, barY int) (int, bool) {
	if screen.Empty() || slot == nil {
		return 0, false
	}
	res := tc.detectSlotCount(screen, slot.X, slot.Y, barY)
	return res.Count, res.OK
}
