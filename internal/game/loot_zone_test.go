package game

import (
	"image"
	"testing"
)

// TestBonusSearchCoversObserved720pBonusRows pins the geometry the 720p
// layout exposed on 2026-09-23 (recorded in tmp/live_check/anim2).
//
// The three league-bonus rows render as "+288000", "+288000", "+2160" and
// reach live x=1001 at 1280x720, i.e. reference x ~702 — past the 676 right
// edge the zone carried. The old zone cut the last two digits of every row
// and also cut the third row out entirely, so a real bonus of
// 288000/288000/2160 was recorded as 90/288/288 (the "90" coming from the
// clipped "90% BONUS" header, which the bottom-three anchoring then treated
// as the gold row).
func TestBonusSearchCoversObserved720pBonusRows(t *testing.T) {
	// Row rects as the parser found them in the recorded victory frame.
	observed := []struct {
		name string
		live image.Rectangle
	}{
		{"bonus gold +288000", image.Rect(916, 349, 1006, 378)},
		{"bonus elixir +288000", image.Rect(916, 385, 1006, 414)},
		{"bonus dark elixir +2160", image.Rect(951, 422, 1006, 451)},
	}

	cal := NewCalibration(1280, 720)
	cal.SetDisplayScale(1.325) // pinned by device.display_scale, as the bot runs it
	zone := cal.CentreRect(bonusLootSearch)

	for _, row := range observed {
		if !row.live.In(zone) {
			t.Errorf("%s: live row %v is not inside the mapped bonus search zone %v "+
				"(a clipped row reads a short number and lands in the wrong resource)",
				row.name, row.live, zone)
		}
	}
}

// TestBattleLootSearchCoversObserved720pLootRows is the same guard for the
// battle-loot column, which read correctly in the same recordings.
func TestBattleLootSearchCoversObserved720pLootRows(t *testing.T) {
	observed := []struct {
		name string
		live image.Rectangle
	}{
		{"battle gold", image.Rect(557, 321, 664, 342)},
		{"battle elixir", image.Rect(549, 368, 665, 390)},
		{"battle dark elixir", image.Rect(590, 416, 664, 437)},
	}

	cal := NewCalibration(1280, 720)
	cal.SetDisplayScale(1.325)
	zone := cal.CentreRect(battleLootSearch)

	for _, row := range observed {
		if !row.live.In(zone) {
			t.Errorf("%s: live row %v is not inside the mapped battle search zone %v", row.name, row.live, zone)
		}
	}
}

// TestSearchZonesCoverReferenceFixtureRows keeps the reference-resolution
// fixture inside the same zones: the two geometries disagree about where the
// panel columns sit (the 16:9 layout is wider), and one pair of zones has to
// serve both.
func TestSearchZonesCoverReferenceFixtureRows(t *testing.T) {
	// Rows detected in internal/game/testdata/screen_victory.png.
	battle := []image.Rectangle{
		image.Rect(325, 320, 438, 338),
		image.Rect(327, 359, 437, 377),
		image.Rect(357, 397, 438, 415),
	}
	bonus := []image.Rectangle{
		image.Rect(609, 340, 676, 353),
		image.Rect(606, 371, 670, 384),
		image.Rect(606, 403, 670, 416),
		image.Rect(631, 434, 670, 447),
	}

	for _, r := range battle {
		if !r.In(battleLootSearch) {
			t.Errorf("reference battle row %v is outside the battle search zone %v", r, battleLootSearch)
		}
	}
	// The header line above the rows is allowed to be outside, but every
	// value row must be inside.
	for _, r := range bonus[1:] {
		if !r.In(bonusLootSearch) {
			t.Errorf("reference bonus row %v is outside the bonus search zone %v", r, bonusLootSearch)
		}
	}
}
