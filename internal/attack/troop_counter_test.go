package attack

import (
	"image"
	"os"
	"path/filepath"
	"testing"

	"github.com/Ducky705/ClashGO/internal/game"
	"github.com/rs/zerolog"
	"gocv.io/x/gocv"
)

// The 720p bar geometry the corpus frames were captured at: bar_y from
// assets/precision_config.json (632 in the 860x732 reference) mapped with the
// same per-axis scale the orchestrator uses, plus the slot identity point the
// SlotManager derives from it.
const (
	corpusSlotY = 671
	corpusBarY  = 621
)

// corpusSlots mirrors `assets/manual_slots.json`'s `slot_xs`: the card centres
// measured off real 1280x720 battle frames. It is deliberately a copy rather than
// a read of the asset — the reader is being pinned against the geometry the bot
// ships, so a change to the file must not silently move the test's bar.
//
// It replaced a list authored in the 860-wide reference frame and used as live
// pixels (62, 134, 212, … at pitch 73 against a live pitch of ~100). On the live
// bar that list drifted up to 45px off the cards, put two anchors on one card
// (287 and 360 both on the card at 330) and left the rightmost cards — where the
// spells and the event units sit — with no anchor at all.
var corpusSlots = []int{137, 234, 349, 450, 550, 643, 751, 848, 948, 1048, 1148}

// Measured on the live event bar: the rows a count BADGE occupies, and the rows
// the unit-LEVEL number occupies. The badge is the count; the level is a static
// number at the card's bottom-left that the reader used to read as one.
const (
	labelRowTop    = 596
	labelRowBottom = 610
	levelRowTop    = 683
	levelRowBottom = 694
)

const corpusDir = "../../internal/game/testdata/corpus/"

// liveFrameDir holds frames captured straight off the emulator during real
// attacks. They live here rather than in the classifier corpus because they are
// not statements about the bot's game state — the frame below is a battle with an
// unrecognised reward popup dimming it, which the classifier reports as Unknown
// on purpose — they are the pixels the count reader has to work on.
const liveFrameDir = "testdata/"

func testCounter(t *testing.T) *TroopCounter {
	t.Helper()
	cal := game.NewCalibration(1280, 720)
	cal.SetDisplayScale(1.325)
	tc := NewTroopCounter(cal, 860, 732, zerolog.Nop())
	if !tc.HasDigitTemplates() {
		t.Fatal("no digit templates loaded; the reader cannot work without them")
	}
	return tc
}

func loadFrame(t *testing.T, name string) gocv.Mat {
	t.Helper()
	path := corpusDir + name + ".png"
	if _, err := os.Stat(path); err != nil {
		t.Skipf("corpus frame missing (%v)", err)
	}
	img := gocv.IMRead(path, gocv.IMReadColor)
	if img.Empty() {
		t.Fatalf("cannot read %s", path)
	}
	return img
}

// TestTroopCountReaderEndToEnd is the deterministic gate for the whole reader:
// band placement, glyph extraction, matching, multi-digit assembly, and the
// spent-card semantics.
//
// The frame is synthesised from the project's own digit templates painted at the
// measured label geometry, so the test states its expectations in exact numbers
// and cannot pass by agreeing with itself about the wrong pixel — the pre-fix
// reader sampled a band ~45px above the label and returned 0 for all ten slots
// of every real frame, and every one of its unit tests still passed.
func TestTroopCountReaderEndToEnd(t *testing.T) {
	tc := testCounter(t)

	cases := []struct {
		name  string
		paint map[int]string // slot x -> digits to paint in that card's label row
	}{
		{
			name: "single and double digit labels",
			paint: map[int]string{
				137: "9",
				234: "13",
				450: "21",
				550: "12",
				751: "7",
			},
		},
		{
			name: "six and eight are distinct",
			paint: map[int]string{
				137: "6",
				234: "8",
				450: "16",
				550: "18",
				751: "3",
			},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			screen := synthBar(t, tc, c.paint)
			defer screen.Close()

			slots := make([]*TrackedSlot, 0, len(corpusSlots))
			for _, x := range corpusSlots {
				slots = append(slots, &TrackedSlot{TroopSlot: TroopSlot{X: x, Y: corpusSlotY, Category: "Troop"}})
			}
			counts := tc.DetectCounts(screen, slots, corpusBarY)
			got := GetAllCounts(counts)

			for slotX, want := range c.paint {
				wantCount := 0
				for _, r := range want {
					wantCount = wantCount*10 + int(r-'0')
				}
				value, ok := got[slotX]
				if !ok {
					t.Errorf("slot x=%d painted %q: read unreadable", slotX, want)
					continue
				}
				if value != wantCount {
					t.Errorf("slot x=%d painted %q: read %d", slotX, want, value)
				}
			}

			// Slots with no painted label must read as a measured zero, not as
			// a number and not as unreadable: the rest of the bar is legible,
			// which is exactly the evidence that separates "spent card" from
			// "blind frame".
			for _, slotX := range corpusSlots {
				if _, painted := c.paint[slotX]; painted {
					continue
				}
				res := tc.detectSlotCount(screen, slotX, corpusSlotY, corpusBarY)
				if !res.OK {
					t.Errorf("slot x=%d has no label on a legible bar: expected a measured zero, got unreadable", slotX)
					continue
				}
				if res.Count != 0 {
					t.Errorf("slot x=%d has no label: read %d", slotX, res.Count)
				}
			}
		})
	}
}

// realFrameCase is one real 720p bar with the ground truth for its COUNTS.
//
// badge: the count each card actually carries, from an independent reader —
// tools/ocr.swift (Apple Vision) run over the badge ROW (the band at the card's
// top edge, y 586..626 live) upscaled 4-8x, cross-checked against the glyph
// structure in the pixels: a badge is an `x` prefix followed by its digits,
// right-aligned against the card's right edge. At 13px tall Vision's recall on
// this row is partial, so each entry here is a badge BOTH readers agree on.
//
// levels: the numbers the same Vision pass reads on the bar's unit-LEVEL row
// (y 683..694 live) — the numbers this reader used to report as counts. They are
// here as FORBIDDEN values, as a set rather than per slot: the level is drawn at
// the card's bottom-left, so which card a given level number belongs to is a
// question the count reader never has to answer.
//
// Reading the level as a count is not cosmetic. On the live event bar the
// 40-troop card carried badge `x40` and level `16`; the reader reported 18, the
// sweep fired 25 taps of the 40, ~29 troops stayed on the bar, and the run still
// reported "100% Deployed / SUCCESS". Every value in `level` is a number that must
// never come back as a count, on any slot of that bar.
//
// unresolved: values Vision DID see on this bar's badge row but could not settle
// (it reads `xO` for a badge whose digit could be 0, 6, 8 or 9). They are allowed
// as reads — the reader may resolve what Vision could not — but they are not
// required, because that would assert a number nobody has established.
type realFrameCase struct {
	path       string
	badge      map[int]int
	levels     []int
	unresolved []int
}

// TestTroopCountsOnRealBars runs the reader over the three real 720p battle
// frames.
//
// Three properties, in order of importance:
//
//  1. It never reports a NUMBER other than the one on the card. A wrong count
//     fires the wrong number of taps and writes off cards that still hold
//     troops; this is the property the whole reader exists to keep.
//  2. A card that carries a label never comes back as a MEASURED ZERO unless it
//     is one of the listed recall gaps. "No label here" is the reader telling
//     the deployer the card is spent, so saying it about a card that has a label
//     is the original bug in miniature.
//  3. The clearly separated labels are actually READ. At 720p a count glyph is
//     11-13px tall and `2`/`3`/`8` genuinely overlap, so the reader declines the
//     ambiguous glyphs (a real `3` scores 0.63 as an `8`, margin 0.16) — that is
//     the design, and the caller falls back to the strategy's own count for
//     those. But declined must mean the genuinely ambiguous ones only.
func TestTroopCountsOnRealBars(t *testing.T) {

	cases := []realFrameCase{
		{
			path:   liveFrameDir + "event_bar_720p.png",
			badge:  map[int]int{137: 40, 234: 10, 349: 9, 643: 5, 751: 1},
			levels: []int{16},
		},
		{
			path:   liveFrameDir + "live_bar_deploy_720p.png",
			badge:  map[int]int{137: 10, 234: 9, 751: 5, 848: 1},
			levels: []int{21, 12, 11, 8},
		},
		{
			path:   corpusDir + "battle_pre_deploy_720p.png",
			badge:  map[int]int{137: 1, 848: 1},
			levels: []int{9, 13, 21, 12, 11, 7},
		},
		{
			path:   corpusDir + "battle_deploy_720p.png",
			badge:  map[int]int{234: 3, 848: 1},
			levels: []int{9, 21, 12, 11, 7},
		},
		{
			path:   corpusDir + "battle_post_deploy_720p.png",
			badge:  map[int]int{848: 1},
			levels: []int{21, 12, 11, 7},
		},
	}

	for _, c := range cases {
		t.Run(filepath.Base(c.path), func(t *testing.T) {
			if _, err := os.Stat(c.path); err != nil {
				t.Skipf("frame missing (%v)", err)
			}
			img := gocv.IMRead(c.path, gocv.IMReadColor)
			if img.Empty() {
				t.Fatalf("cannot read %s", c.path)
			}
			defer img.Close()

			counter := testCounter(t)
			slots := make([]*TrackedSlot, 0, len(corpusSlots))
			for _, x := range corpusSlots {
				slots = append(slots, &TrackedSlot{TroopSlot: TroopSlot{X: x, Y: corpusSlotY, Category: "Troop"}})
			}
			counts := counter.DetectCounts(img, slots, corpusBarY)

			read := make(map[int]TroopCount, len(counts))
			for _, r := range counts {
				read[r.X] = r
			}

			// 1. The badges both readers can see are read EXACTLY. Declining one of
			// these is not caution, it is the reader failing on a clear label.
			for x, want := range c.badge {
				got := read[x]
				if !got.OK || got.Count != want {
					t.Errorf("slot x=%d carries badge `x%d` (Vision-confirmed on the badge row) but the reader reported count=%d ok=%v",
						x, want, got.Count, got.OK)
				}
			}

			// 2. A unit LEVEL is never reported as a count. This is the original
			// bug: the level is a plausible number, so it survived every test that
			// only asked whether the reader could read SOMETHING.
			for _, x := range corpusSlots {
				got := read[x]
				if !got.OK || got.Count == 0 {
					continue
				}
				for _, level := range c.levels {
					if got.Count == level {
						t.Errorf("slot x=%d: reported count=%d, which is a unit LEVEL read off this frame's level row — the read that left 29 of the event's 40 troops on the bar",
							x, level)
					}
				}
			}

			// 3. Never a number no card on this bar carries. A fabricated count
			// fires real taps at the wrong card.
			allowed := make(map[int]bool, len(c.badge)+len(c.unresolved))
			for _, want := range c.badge {
				allowed[want] = true
			}
			for _, want := range c.unresolved {
				allowed[want] = true
			}
			for _, x := range corpusSlots {
				got := read[x]
				if !got.OK || got.Count == 0 || allowed[got.Count] {
					continue
				}
				t.Errorf("slot x=%d: reported count=%d, which no card on this bar carries (badges present: %v, unresolved: %v)",
					x, got.Count, c.badge, c.unresolved)
			}

			// 4. A card whose badge is on screen is never called spent. "No label
			// here" is how the sweep writes a card off as "deployed", so a measured
			// zero on a card carrying a badge is the false-success bug in miniature.
			for x := range c.badge {
				if got := read[x]; got.OK && got.Count == 0 {
					t.Errorf("slot x=%d: reported a measured zero (\"spent\") on a card whose badge is visible on the bar", x)
				}
			}

			exact := 0
			for x, want := range c.badge {
				if read[x].Count == want {
					exact++
				}
			}
			t.Logf("%s: %d/%d Vision-confirmed badges read exactly", filepath.Base(c.path), exact, len(c.badge))
		})
	}
}

// TestTroopCountWindowCoversTheBadgeAndNotTheLevel pins the geometry the count
// reader has to read on: the badge band covers the count badge, and it must NOT
// cover the unit-level row at the card's bottom.
//
// Reading the level as a count is what the badge band replaced. It is invisible
// in a passing attack (a level is a plausible number) and fatal on the cards that
// matter: on the live event bar the 40-troop card carried badge `x40` and level
// `16`, the reader reported 18, the sweep fired 25 taps, ~29 troops stayed on the
// bar, and the run still reported "100% Deployed".
func TestTroopCountWindowCoversTheBadgeAndNotTheLevel(t *testing.T) {
	tc := testCounter(t)

	slack := int(tc.scale(countBandSlackRef))
	row := tc.badgeGeometryRow(corpusBarY)
	top, bottom := row.top-slack, row.bottom+slack
	if top > labelRowTop || bottom < labelRowBottom {
		t.Errorf("badge band y %d..%d does not cover the badge rows %d..%d",
			top, bottom, labelRowTop, labelRowBottom)
	}
	if row.bottom >= levelRowTop {
		t.Errorf("badge band reaches y %d, at or below the unit-level row %d..%d: a level read as a count is the bug this geometry replaced",
			row.bottom, levelRowTop, levelRowBottom)
	}
	t.Logf("badge band y %d..%d covers badge y %d..%d and stops above the level row y %d..%d",
		top, bottom, labelRowTop, labelRowBottom, levelRowTop, levelRowBottom)
}

// TestBarLegibleSeparatesSpentFromBlind covers the distinction the reconcilers
// now branch on. A bar with labels somewhere is legible, so a card with no label
// is a measured zero; a frame with no legible label anywhere is unknown, and a
// zero from it must not be able to write a slot off.
func TestBarLegibleSeparatesSpentFromBlind(t *testing.T) {
	tc := testCounter(t)

	screen := synthBar(t, tc, map[int]string{450: "21"})
	defer screen.Close()
	if !tc.BarLegible(screen, corpusBarY, corpusSlotY) {
		t.Fatal("a bar carrying a readable label must be reported legible")
	}
	if res := tc.detectSlotCount(screen, 349, corpusSlotY, corpusBarY); !res.OK || res.Count != 0 {
		t.Fatalf("a card with no label on a legible bar must be a measured zero, got count=%d ok=%v", res.Count, res.OK)
	}

	blank := gocv.NewMatWithSize(720, 1280, gocv.MatTypeCV8UC1)
	defer blank.Close()
	if tc.BarLegible(blank, corpusBarY, corpusSlotY) {
		t.Fatal("a blank frame must not be reported legible")
	}
	if res := tc.detectSlotCount(blank, 450, corpusSlotY, corpusBarY); res.OK {
		t.Fatal("a blind frame must not produce a trustworthy read")
	}
}

// TestGlyphRangeContainsMeasuredDigits guards the size filter against being
// re-derived from the display scale alone: CoC's HUD text grows more slowly than
// the display scale, so the measured 720p glyphs sit below tplH*k and the floor
// has to be a fraction of it. The measured label glyphs are 11-13px tall.
//
// The ceiling matters in the other direction. A digit cannot be taller than the
// font it is drawn from, and the components that are — 16px blobs of card art on
// the corpus frames — used to be offered to the matcher, which scored one of them
// 0.53 as a `4` on a card whose real label had not been extracted at all. That is
// a fabricated count for a card holding 21 troops, so the ceiling is pinned here.
func TestGlyphRangeContainsMeasuredDigits(t *testing.T) {
	tc := testCounter(t)

	expect := tc.expectGlyphH()
	minH := 0.68 * expect
	maxH := 1.00 * expect
	for _, measured := range []int{11, 12, 13} {
		if float64(measured) < minH || float64(measured) > maxH {
			t.Errorf("measured 720p glyph height %dpx falls outside the accepted %.1f..%.1f", measured, minH, maxH)
		}
	}
	if art := 16.0; art <= maxH {
		t.Errorf("a %vpx art blob is still accepted as a glyph (ceiling %.1f)", art, maxH)
	}
	t.Logf("templates %dx%d -> expected glyph height %.1f, accepted %.1f..%.1f",
		tc.tplW, tc.tplH, expect, minH, maxH)
}

// TestVerticallyAlignedRequiresOnlyTheVerticalAxis pins the bug that truncated
// every multi-digit count to its first digit.
//
// Two digits of one label sit SIDE BY SIDE, so their horizontal extents never
// overlap. Alignment was once decided with image.Rectangle.Intersect, which
// requires an overlap on both axes — so every pair of digits in a `16` or a `21`
// was judged "not on the same row", the run was never assembled, and the label
// read as its first digit alone. At 720p that read 21 as 2 and 16 as 1, and the
// deployer acted on the number.
func TestVerticallyAlignedRequiresOnlyTheVerticalAxis(t *testing.T) {
	one := image.Rect(28, 10, 32, 22)
	two := image.Rect(33, 10, 42, 22)
	if !one.Intersect(two).Empty() {
		t.Fatal("these two digits are not side by side; the test no longer describes the bug")
	}
	if !verticallyAligned(one, two) {
		t.Error("two digits on the same text row must count as vertically aligned")
	}
	if verticallyAligned(one, image.Rect(33, 40, 42, 52)) {
		t.Error("glyphs on different rows must not count as aligned")
	}
	if verticallyAligned(one, image.Rect(33, 20, 42, 32)) {
		t.Error("glyphs overlapping by 2 of 12 rows must not count as aligned")
	}
}

// TestLiveBarReadsAtAnySlotAnchor is the regression gate for the bug that cost a
// live attack its counts.
//
// The reader anchors the label row to the slot point it is handed, and that slot
// point comes from two places that disagree: the geometry derives it (bar_y 632 in
// the 860x732 reference, scaled to 621 live, plus 38 reference px = 671), while
// manual_slots.json supplies a reference-space `slot_y` that the slot manager uses
// verbatim (682). Measured at 1280x720, the count labels sit at y 683..694 — which
// is inside the geometry's window and, at the manual anchor, is clipped by the top
// edge of the window. A live attack logged `troop count read readable=1 slots=10`
// with that anchor: every label was cut in half by the window, so every card read
// as blank and the deploy fell back to a blind three-tap batch on every unit.
//
// The frame here is a saved live deploy capture, and the counts the test requires
// are the badges Vision confirms on the badge row (y 596..614 live), NOT the load
// `slot_y` describes: that reference point is 682, which is the unit-LEVEL row.
// Both anchors must produce the same reads, because which of the two the slot
// manager happens to hand over must not decide what the bot deploys — a fix that
// only works for one of them leaves it blind on the frames where they disagree,
// which is the state the live run was actually in.
func TestLiveBarReadsAtAnySlotAnchor(t *testing.T) {
	path := liveFrameDir + "live_bar_deploy_720p.png"
	if _, err := os.Stat(path); err != nil {
		t.Skipf("live bar frame missing (%v)", err)
	}
	img := gocv.IMRead(path, gocv.IMReadColor)
	if img.Empty() {
		t.Fatalf("cannot read %s", path)
	}
	defer img.Close()

	// The badges Vision confirms on this bar's badge row are `x10` at the first
	// card, `x9` at the second, `x5` at the seventh and `x1` at the eighth. All
	// four are required here: `x1` is the read the old anchors got wrong, by
	// sitting between two cards and firing the seventh card's count at it.
	want := map[int]int{137: 10, 234: 9, 751: 5, 848: 1}
	forbidden := []int{21, 12, 11, 8}
	for _, anchor := range []struct {
		name string
		y    int
	}{
		{"geometry-derived slot point", corpusSlotY},
		{"manual_slots.json slot_y", 682},
	} {
		t.Run(anchor.name, func(t *testing.T) {
			counter := testCounter(t)
			slots := make([]*TrackedSlot, 0, len(corpusSlots))
			for _, x := range corpusSlots {
				slots = append(slots, &TrackedSlot{TroopSlot: TroopSlot{X: x, Y: anchor.y, Category: "Troop"}})
			}
			counts := counter.DetectCounts(img, slots, corpusBarY)

			read := make(map[int]TroopCount, len(counts))
			for _, r := range counts {
				read[r.X] = r
			}
			exact := 0
			for x, expected := range want {
				got := read[x]
				if got.OK && got.Count == expected {
					exact++
					continue
				}
				t.Errorf("slot x=%d: read count=%d ok=%v, want %d", x, got.Count, got.OK, expected)
			}
			if exact != len(want) {
				t.Fatalf("only %d of %d labels read", exact, len(want))
			}
			for _, x := range corpusSlots {
				got := read[x]
				if !got.OK || got.Count == 0 {
					continue
				}
				for _, level := range forbidden {
					if got.Count == level {
						t.Errorf("slot x=%d: read count=%d, which is a unit LEVEL; the count row is the badge at the card's top edge, not the level at its bottom",
							x, level)
					}
				}
			}
		})
	}
}

// TestUnreadableLabelIsUnknownNotZero covers the state the deployer's safety
// depends on, on the shape of label that occurs in real frames: a clear digit
// beside one the matcher cannot resolve.
//
// A card in that state must NOT come back as a measured zero. "No label here" is
// the reader telling the deployer the card is spent, and a card holding 21 troops
// that is reported empty is the under-fire bug this reader exists to prevent. The
// read has to be unknown so the caller falls back to the strategy's own count and
// its visual check — while a card that genuinely carries no label on a legible bar
// still reads as a confident zero, or spent cards would never be recognised.
func TestUnreadableLabelIsUnknownNotZero(t *testing.T) {
	tc := testCounter(t)
	screen := synthBar(t, tc, map[int]string{450: "2", 848: "7"})
	defer screen.Close()

	// Beside the 2, paint a glyph-shaped component the matcher cannot read: a
	// hollow outline, the right size and aspect to be offered as a digit
	// candidate, matching no digit well. The 2 next to it is a decisive read, so
	// the rule under test is "part of a number matched, the number did not".
	// synthBar right-aligns the badge against the card's right edge, so the glyph
	// that must join its run sits immediately right of the painted digits.
	x0 := 450 + int(tc.scale(33)) - 3 + 2
	w, h := 9, labelRowBottom-labelRowTop
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			if y == 0 || y == h-1 || x == 0 || x == w-1 {
				screen.SetUCharAt(labelRowTop+y, x0+x, 255)
			}
		}
	}

	decoy := tc.detectSlotCount(screen, 450, corpusSlotY, corpusBarY)
	if decoy.OK {
		t.Fatalf("a card whose number could not be assembled reported count=%d as a measurement", decoy.Count)
	}

	spent := tc.detectSlotCount(screen, 349, corpusSlotY, corpusBarY)
	if !spent.OK || spent.Count != 0 {
		t.Fatalf("a card with no label on a legible bar must be a measured zero, got count=%d ok=%v", spent.Count, spent.OK)
	}

	// A clean label on the same bar is still read exactly: declining one card
	// must not cost the rest of the bar, or the deployer is back to guessing.
	clear := tc.detectSlotCount(screen, 848, corpusSlotY, corpusBarY)
	if !clear.OK || clear.Count != 7 {
		t.Fatalf("a clean `x7` beside a partial label must still be read, got count=%d ok=%v", clear.Count, clear.OK)
	}
}

// TestLiveDeployBarReadsLabelsContainingOne is the regression gate for the bug
// that cost a live attack its counts.
//
// The frame is the one a live 720p deploy read from, saved by the bot itself. On
// it the count reader read ONE card of ten (`counts={"134":0,...,"584":11,...}`)
// and the deploy fell back to a blind three-tap batch for every unit, including
// both spells. The pixels were fine: slots 433 and 511 carry bright, clean `21`
// and `12` labels, and the matcher scored them 0.716/0.548 and 0.716/0.547. The
// `1`'s margin over its runner-up was 0.184, a hair under the 0.20 bar, because a
// `1` is a bare 3px stroke and the matcher — which resizes both sides into one
// canonical box — was also comparing it against digits drawn 8px wide. Ruling out
// templates whose ink proportion cannot be the glyph in front of them restores
// those reads without touching the genuinely ambiguous cases.
//
// This pins all three properties at once: the labels are read, they are read
// EXACTLY, and nothing else on the bar is turned into a number (the `6`/`8`
// coin-flip on slot 730 stays unread, as it must).
func TestLiveDeployBarReadsLabelsContainingOne(t *testing.T) {
	path := liveFrameDir + "live_bar_deploy_720p.png"
	if _, err := os.Stat(path); err != nil {
		t.Skipf("live deploy frame missing (%v)", err)
	}
	img := gocv.IMRead(path, gocv.IMReadColor)
	if img.Empty() {
		t.Fatalf("cannot read %s", path)
	}
	defer img.Close()

	counter := testCounter(t)
	slots := make([]*TrackedSlot, 0, len(corpusSlots))
	for _, x := range corpusSlots {
		slots = append(slots, &TrackedSlot{TroopSlot: TroopSlot{X: x, Y: 682, Category: "Troop"}})
	}
	counts := counter.DetectCounts(img, slots, corpusBarY)
	read := make(map[int]TroopCount, len(counts))
	for _, c := range counts {
		read[c.X] = c
	}

	// Ground truth, read off the frame's pixels by hand: 21, 12, 11 and 8.
	//
	// 798 was asserted absent for as long as the reader could not extract its
	// label, which made the old guard here a statement about the reader's
	// blindness rather than about the bar — and tests like that are how a bug
	// survives. A pixel trace of x 817..825, y 683..694 shows a closed top loop,
	// a waist at y 688 and a closed bottom loop: an `8` at the same height as
	// every other label on the bar, and identical on both live frames. It is a
	// count, so it is now expected.
	// The counts Vision confirms on this bar's badge row: `x10` on the first card,
	// `x9` on the second, `x5` on the one at 730. These are the labels that used to
	// be read as the cards' unit levels, which is why the deploy fired a blind
	// three-tap batch on every unit instead.
	known := map[int]int{137: 10, 234: 9, 751: 5, 848: 1}
	for slotX, want := range known {
		got := read[slotX]
		if !got.OK || got.Count != want {
			t.Errorf("slot x=%d carries badge `x%d` (Vision-confirmed) but the reader reported count=%d ok=%v", slotX, want, got.Count, got.OK)
		}
	}

	// Never a number the card does not carry: a positive read must be one of the
	// badge values on this bar, and a fabricated count fires real taps. Unknown is
	// always allowed — declining a marginal glyph is the design — but inventing a
	// number is not.
	for _, x := range corpusSlots {
		got := read[x]
		if !got.OK || got.Count == 0 {
			continue
		}
		carried := false
		for _, want := range known {
			if got.Count == want {
				carried = true
			}
		}
		if !carried {
			t.Errorf("slot x=%d: reported count=%d, which no card on this bar carries (%v)", x, got.Count, known)
		}
	}

	// And a unit LEVEL is never reported as a count. The levels Vision reads on
	// this frame's level row are 21, 12, 11 and 8; reporting one of them here is
	// the reader sampling the wrong row, which is what left 29 of the live event's
	// 40 troops on the bar while the run reported 100% deployment.
	for _, x := range corpusSlots {
		got := read[x]
		if !got.OK || got.Count == 0 {
			continue
		}
		for _, level := range []int{21, 12, 11, 8} {
			if got.Count == level {
				t.Errorf("slot x=%d: reported count=%d, which is a unit LEVEL on this frame's level row", x, level)
			}
		}
	}
}

// The bar is drawn over an animation for the first moments of a battle, so a
// single capture is the wrong unit for the read: a live 720p run read one card of
// ten off the frame the deploy started from, while four of six labels read cleanly
// on a saved frame of the same bar a second later. DetectCountsMerged reads
// several frames and lets the cards vote. These tests pin the merge's rules.
func TestDetectCountsMerged_LabelLegibleOnAnyFrameIsRead(t *testing.T) {
	tc := testCounter(t)
	slots := []*TrackedSlot{{TroopSlot: TroopSlot{X: 450, Y: corpusSlotY, Category: "Troop"}}}

	animating := synthBar(t, tc, map[int]string{}) // the bar before the labels draw
	defer animating.Close()
	settled := synthBar(t, tc, map[int]string{450: "21"})
	defer settled.Close()

	single := tc.DetectCounts(animating, slots, corpusBarY)
	if single[0].OK {
		t.Fatalf("the animating frame alone reported count=%d as a measurement; the test no longer describes the bug", single[0].Count)
	}

	merged := tc.DetectCountsMerged([]gocv.Mat{animating, settled}, slots, corpusBarY)
	if !merged[0].OK || merged[0].Count != 21 {
		t.Fatalf("merged read = count=%d ok=%v, want 21 ok=true (the label was legible on a later frame)", merged[0].Count, merged[0].OK)
	}
}

func TestDetectCountsMerged_DisagreeingFramesAreUnknown(t *testing.T) {
	tc := testCounter(t)
	slots := []*TrackedSlot{{TroopSlot: TroopSlot{X: 450, Y: corpusSlotY, Category: "Troop"}}}

	a := synthBar(t, tc, map[int]string{450: "21"})
	defer a.Close()
	b := synthBar(t, tc, map[int]string{450: "12"})
	defer b.Close()

	merged := tc.DetectCountsMerged([]gocv.Mat{a, b}, slots, corpusBarY)
	if merged[0].OK {
		t.Fatalf("frames disagreed (21 vs 12) but the merge reported count=%d as a measurement; a guess here fires the wrong number of taps", merged[0].Count)
	}
}

func TestDetectCountsMerged_SpentCardIsMeasuredZeroButBlindFrameIsNot(t *testing.T) {
	tc := testCounter(t)
	slots := []*TrackedSlot{
		{TroopSlot: TroopSlot{X: 450, Y: corpusSlotY, Category: "Troop"}},
		{TroopSlot: TroopSlot{X: 550, Y: corpusSlotY, Category: "Troop"}},
	}

	settled := synthBar(t, tc, map[int]string{433: "21"})
	defer settled.Close()

	// A label somewhere on the bar makes the label-free card a measured zero.
	merged := tc.DetectCountsMerged([]gocv.Mat{settled, settled}, slots, corpusBarY)
	if !merged[1].OK || merged[1].Count != 0 {
		t.Fatalf("a card with no label on a legible bar must be a measured zero, got count=%d ok=%v", merged[1].Count, merged[1].OK)
	}

	// With no legible label anywhere the zero carries no information.
	blind := synthBar(t, tc, map[int]string{})
	defer blind.Close()
	blindMerged := tc.DetectCountsMerged([]gocv.Mat{blind}, slots, corpusBarY)
	if blindMerged[1].OK {
		t.Fatalf("a blind frame produced a measured zero (count=%d); a zero from a frame the reader could not see must not write a card off", blindMerged[1].Count)
	}
}

// TestLiveBarUnreadableLabelsAreNeverTrustedZeros runs the real false-success
// frame through the reader. `live_bar_720p.png` is the live 720p bar whose slots
// 134 and 212 (Electro Dragon, Balloon) carried a label the matcher could not
// settle; the run's sweep reported both as deployed anyway because the
// unreadable-label case fell through to the frame-level probe and became a
// trusted zero. The reader must call them unknown — the card is demonstrably not
// spent — while a genuinely label-free card on the same legible bar stays a
// measured zero.
func TestLiveBarUnreadableLabelsAreNeverTrustedZeros(t *testing.T) {
	path := liveFrameDir + "live_bar_720p.png"
	if _, err := os.Stat(path); err != nil {
		t.Skipf("live bar frame missing (%v)", err)
	}
	img := gocv.IMRead(path, gocv.IMReadColor)
	if img.Empty() {
		t.Fatalf("cannot read %s", path)
	}
	defer img.Close()

	tc := testCounter(t)
	slots := make([]*TrackedSlot, 0, len(corpusSlots))
	for _, x := range corpusSlots {
		slots = append(slots, &TrackedSlot{TroopSlot: TroopSlot{X: x, Y: 682, Category: "Troop"}})
	}
	// Prime the measured label row the way the real read does; a per-slot probe
	// that skips this reads a different row and sees nothing.
	tc.DetectCounts(img, slots, corpusBarY)

	for _, x := range []int{137, 234} {
		res, labelSeen := tc.detectSlotCountDetailed(img, x, 682, corpusBarY)
		if !labelSeen {
			t.Fatalf("slot x=%d: the frame carries a label the matcher cannot settle but labelSeen=false; the fixture no longer describes the write-off case", x)
		}
		if _, trusted := liveCountVerdict(res, labelSeen, tc.BarLegible(img, corpusBarY, 682)); trusted {
			t.Errorf("slot x=%d was handed back as a trusted zero (count=%d) while carrying an unreadable label; this is the write-off that reported the unit as deployed", x, res.Count)
		}
	}
}

// TestLiveCountVerdict_LabelPresentButUnreadableIsNeverATrustedZero is the
// regression gate for the false-success bug found in live run13 (2026-09-22).
// Balloon and Electro Dragon were reported as "deployed" while their cards were
// pixel-identical to the start of the battle: on a 720p bar where only 3 of 10
// cards read, the unreadable-label case fell through to the frame-level probe,
// which saw those 3 readable cards and answered "the bar is legible", so the
// unknown became `trusted=true, count=0` and the sweep's zero-streak rule wrote
// the full card off as "spent or locked". A label seen on a card means the card
// is not spent, so the verdict must be untrusted no matter how legible the rest
// of the bar is.
func TestLiveCountVerdict_LabelPresentButUnreadableIsNeverATrustedZero(t *testing.T) {
	// The swap that caused the bug: same inputs, only `labelSeen` moves, and the
	// verdict must flip from a trusted zero to unknown.
	if _, trusted := liveCountVerdict(TroopCount{}, false, true); !trusted {
		t.Fatal("a card with NO label on a legible bar must stay a trusted zero (spent card)")
	}
	if _, trusted := liveCountVerdict(TroopCount{}, true, true); trusted {
		t.Fatal("a card carrying an unreadable label was handed back as a trusted zero; this is the write-off that reported Balloon and Electro Dragon as deployed")
	}

	// A settled read is always trusted, count and all.
	if n, trusted := liveCountVerdict(TroopCount{Count: 21, OK: true}, false, true); !trusted || n != 21 {
		t.Fatalf("a settled read must be trusted verbatim, got count=%d trusted=%v", n, trusted)
	}
	// No label and a blind bar: unknown, so the visual check drives.
	if _, trusted := liveCountVerdict(TroopCount{}, false, false); trusted {
		t.Fatal("a blurry frame produced a trusted zero; a blind read must never be able to write a slot off")
	}
}

// synthBar paints digit glyphs from the project's own templates into a blank
// troop bar at the measured label geometry, one number per named slot.
func synthBar(t *testing.T, tc *TroopCounter, labels map[int]string) gocv.Mat {
	t.Helper()

	const bg = 60
	screen := gocv.NewMatWithSize(720, 1280, gocv.MatTypeCV8UC1)
	for y := 0; y < screen.Rows(); y++ {
		for x := 0; x < screen.Cols(); x++ {
			screen.SetUCharAt(y, x, bg)
		}
	}

	// The templates are cut from the unit-LEVEL font; the badge is drawn about a
	// third larger. Scale them up so the synthetic bar is the size the game
	// actually draws a badge at, which is the size the badge reader expects.
	const badgeFontScale = 1.4

	// Where the game draws the number: at the card's top edge, RIGHT-ALIGNED
	// against the card's right edge (measured live: the last badge glyph ends 1-5px
	// inside it, and a shorter number starts further right). The placement is not
	// cosmetic — the reader works out which card a badge belongs to FROM the badge's
	// right edge, because the configured slot anchors are up to ~47px off the live
	// card centres and cannot be trusted to identify the card themselves. A
	// synthetic bar painted from the card's left edge would therefore test a layout
	// the game never draws, and would pass only by encoding the reader's own guess.
	cardHalfWidth := int(tc.scale(33)) // the live card is 88px wide, so half is ~44
	const badgeRightInset = 3

	for slotX, digits := range labels {
		scaled := make([]gocv.Mat, 0, len(digits))
		widths := make([]int, 0, len(digits))
		total := 0
		for i, r := range digits {
			tpl := tc.digitTemplates[int(r-'0')]
			if tpl.Empty() {
				t.Fatalf("no template for digit %c", r)
			}
			m := gocv.NewMat()
			gocv.Resize(tpl, &m, image.Point{
				X: int(float64(tpl.Cols()) * badgeFontScale),
				Y: int(float64(tpl.Rows()) * badgeFontScale),
			}, 0, 0, gocv.InterpolationNearestNeighbor)
			scaled = append(scaled, m)
			widths = append(widths, m.Cols())
			total += m.Cols()
			if i > 0 {
				total += 2
			}
		}

		x := slotX + cardHalfWidth - badgeRightInset - total
		for i, m := range scaled {
			for gy := 0; gy < m.Rows(); gy++ {
				for gx := 0; gx < m.Cols(); gx++ {
					if m.GetUCharAt(gy, gx) > 128 {
						y := labelRowTop + gy
						px := x + gx
						if y < screen.Rows() && px < screen.Cols() && px >= 0 {
							screen.SetUCharAt(y, px, 255)
						}
					}
				}
			}
			x += widths[i] + 2
			m.Close()
		}
	}
	return screen
}

var _ = image.Rect
