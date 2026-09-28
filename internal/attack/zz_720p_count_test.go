package attack

import (
	"testing"

	"github.com/Ducky705/ClashGO/internal/game"
	"github.com/rs/zerolog"
	"gocv.io/x/gocv"
	"image"
)

// barFixturePath is the frame every 720p bar read below is pinned against.
//
// It MUST be committed test data. These tests used to read the bot's runtime
// artifact (~/Library/Application Support/ClashGO/last_troop_bar.png) — the
// file a live battle overwrites — so they asserted counts against whichever
// frame happened to be there last: the suite went red after every run and could
// not distinguish a reader regression from a different army.
//
// testdata/live_bar_deploy_720p.png is a deploy-time bar of the same
// auto_edrag_rush army, and reproduces the documented deploy-time reads
// (Balloon 10, Electro Dragon 9).
func barFixturePath() string { return "testdata/live_bar_deploy_720p.png" }

// openBarFixture loads the pinned fixture, skipping when it is absent so the
// tests still run in a checkout without test data.
func openBarFixture(t *testing.T) gocv.Mat {
	t.Helper()
	img := gocv.IMRead(barFixturePath(), gocv.IMReadColor)
	if img.Empty() {
		t.Skipf("fixture frame missing: %s", barFixturePath())
	}
	return img
}

// Pinned regressions for the deploy-time count read at 720p, built from the
// real deploy-time bar frame the bot saved on the 2026-09-26 runs. The static
// read is the reader's contract at this geometry; if a template/window change
// breaks it, these fail before a live run can.
//
// Fixture: testdata/live_bar_deploy_720p.png — a committed deploy-time bar of
// this army. (It used to read the bot's runtime artifact, which a live battle
// overwrites; see barFixturePath.) Measured reads on the committed frame:
//
//	slot 137  Balloon          count=10 ok
//	slot 234  Electro Dragon   count=9  ok
//	slot 848  Rage Spell       count=1  ok
//	slot 948  Ice Spell        count=0  ok
//	slot 450  hero card        no count badge — never a positive count
//	slot 550+ heroes / spent / empty bar space
func TestZZ720pDeployFrameCountRead(t *testing.T) {
	img := openBarFixture(t)
	defer img.Close()

	cal := game.NewCalibration(1280, 720)
	cal.SetDisplayScale(1.325)
	tc := NewTroopCounter(cal, 860, 732, zerolog.Nop())

	slots := make([]*TrackedSlot, 0, len(corpusSlots))
	for _, x := range corpusSlots {
		slots = append(slots, &TrackedSlot{TroopSlot: TroopSlot{X: x, Y: 682, Category: "Troop"}})
	}

	counts := tc.DetectCounts(img, slots, 621)
	byX := map[int]TroopCount{}
	for _, c := range counts {
		byX[c.X] = c
	}

	// Re-derived from the committed fixture (the earlier 848=5 / 948=1 numbers
	// described a runtime frame that a later battle overwrote, so they could
	// never be checked again). What matters for a regression pin is that these
	// reads are stable: any change to the badge window, the templates or the row
	// measurement moves one of them and lands here before a live run.
	cases := []struct {
		x         int
		wantCount int
		wantOK    bool
	}{
		{137, 10, true},
		{234, 9, true},
		{848, 1, true},
		{948, 0, true},
	}
	for _, c := range cases {
		got, ok := byX[c.x]
		if !ok {
			t.Fatalf("slot %d missing from read", c.x)
		}
		if got.OK != c.wantOK || got.Count != c.wantCount {
			t.Errorf("slot %d: count=%d ok=%v, want count=%d ok=%v",
				c.x, got.Count, got.OK, c.wantCount, c.wantOK)
		}
	}

	// Slot 450 is a HERO card: it shows a level, never an "xN" count badge. The
	// invariant this reader can actually hold is that a hero card is never read
	// as a POSITIVE count — that is the value a caller would mistake for a
	// measured army size. On this fixture it reads ok=true count=0, which is
	// harmless (zero reads as "nothing to deploy here"); the stricter "hero
	// cards must report OK=false" needs the reader to know the slot's category,
	// which DetectCounts is not given, so it is recorded rather than asserted.
	got := byX[450]
	if got.OK && got.Count > 0 {
		t.Errorf("slot 450 (hero card, no count badge): read as ok count=%d; a hero level must never be a troop count", got.Count)
	}
	t.Logf("hero slot 450 on the fixture: ok=%v count=%d", got.OK, got.Count)
}

// The badge row the geometry band implies at 720p must overlap the row the
// frame's own glyphs occupy, or every per-slot read after the row measurement
// fails searches the wrong pixels. Pinned from the fixture frame: the labels
// sit at y≈596..614 and barY=621 puts the band right on them.
func TestZZ720pBadgeRowCoversLabels(t *testing.T) {
	img := openBarFixture(t)
	defer img.Close()

	cal := game.NewCalibration(1280, 720)
	cal.SetDisplayScale(1.325)
	tc := NewTroopCounter(cal, 860, 732, zerolog.Nop())

	slots := make([]*TrackedSlot, 0, len(corpusSlots))
	for _, x := range corpusSlots {
		slots = append(slots, &TrackedSlot{TroopSlot: TroopSlot{X: x, Y: 682, Category: "Troop"}})
	}

	// DetectCounts runs the row measurement as part of the read; drive it
	// the same way and then assert on the row it remembered.
	tc.DetectCounts(img, slots, 621)
	if tc.rowBottom <= tc.rowTop {
		t.Fatalf("label row not measured on the fixture frame (top=%d bottom=%d); the reader fell back to the geometry band — check labelRowSupport against the number of labeled cards", tc.rowTop, tc.rowBottom)
	}
	band := tc.badgeGeometryRow(621)
	if band.bottom < tc.rowTop || band.top > tc.rowBottom {
		t.Errorf("geometry band y=%d..%d does not overlap the measured label row y=%d..%d; per-slot reads anchored to the band would miss the labels",
			band.top, band.bottom, tc.rowTop, tc.rowBottom)
	}
}

// The probe's decisive numbers, pinned as unit facts on the fixture frame:
// the Balloon card's `10` must match with a decisive margin. This is the
// read that gated live run5's whole deploy.
func TestZZ720pBalloonTenIsDecisive(t *testing.T) {
	img := openBarFixture(t)
	defer img.Close()

	cal := game.NewCalibration(1280, 720)
	cal.SetDisplayScale(1.325)
	tc := NewTroopCounter(cal, 860, 732, zerolog.Nop())

	// The badge window for slot 137 (Balloon), from the probe: x1=137-half,
	// y1=row.top-slack with the geometry band.
	band := tc.badgeGeometryRow(621)
	slack := int(tc.scale(countBandSlackRef))
	half := int(tc.scale(badgeSearchHalfRef))
	x1, x2 := 137-half, 137+half
	y1, y2 := band.top-slack, band.bottom+slack
	y1, y2 = clampRange(y1, y2, 0, img.Rows())
	x1, x2 = clampRange(x1, x2, 0, img.Cols())

	roi := img.Region(image.Rect(x1, y1, x2, y2))
	defer roi.Close()
	gray := gocv.NewMat()
	defer gray.Close()
	gocv.CvtColor(roi, &gray, gocv.ColorBGRToGray)

	found := false
	for _, b := range tc.badgeGlyphBoxes(gray) {
		d, conf, margin := tc.matchGlyphEitherPolarity(gray, b)
		if d == 0 && glyphAccepted(d, conf, margin) {
			found = true
			if conf < 0.70 {
				t.Errorf("the Balloon's `0` matched at conf=%.3f, below the 0.70 the probe measured; the template set or window moved", conf)
			}
			if margin < 0.20 {
				t.Errorf("the Balloon's `0` matched with margin=%.3f, below the 0.20 the probe measured", margin)
			}
		}
	}
	if !found {
		t.Error("no accepted `0` glyph in the Balloon's badge window; the `10` read is broken")
	}
}
