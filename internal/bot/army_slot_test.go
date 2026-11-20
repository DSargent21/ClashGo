package bot

import "testing"

// near reports whether a tap point lands on the measured control: the recipe
// rows are ~55 px tall, so a couple of pixels either way is still the same row.
func near(got, want int) bool {
	d := got - want
	return d >= -3 && d <= 3
}

// Live-measured 1024x768 sheet: dropdown arrow (604,174); recipe rows for
// slots 1-4 at y 240/308/377/445, x 603. A tap on the wrong row arms the wrong
// army, so every slot is pinned here.
func TestArmySlotTapPoints_AllFourSlots(t *testing.T) {
	rows := []int{240, 308, 377, 445}
	for slot := 1; slot <= 4; slot++ {
		arrowX, arrowY, slotX, slotY := armySlotTapPoints(1024, 768, slot, nil)
		if !near(arrowX, 604) || !near(arrowY, 174) {
			t.Errorf("slot %d arrow = (%d,%d), want ~(604,174)", slot, arrowX, arrowY)
		}
		if !near(slotX, 603) {
			t.Errorf("slot %d row x = %d, want ~603", slot, slotX)
		}
		if !near(slotY, rows[slot-1]) {
			t.Errorf("slot %d row y = %d, want ~%d", slot, slotY, rows[slot-1])
		}
	}
}

// The same layout at 1280x720, so the fractions are not tied to one geometry.
func TestArmySlotTapPoints_ScalesTo720p(t *testing.T) {
	want := []int{225, 289, 353, 417}
	for slot := 1; slot <= 4; slot++ {
		arrowX, arrowY, slotX, slotY := armySlotTapPoints(1280, 720, slot, nil)
		if !near(arrowX, 755) || !near(arrowY, 163) {
			t.Errorf("slot %d arrow = (%d,%d), want ~(755,163)", slot, arrowX, arrowY)
		}
		if !near(slotX, 755) || !near(slotY, want[slot-1]) {
			t.Errorf("slot %d row = (%d,%d), want ~(755,%d)", slot, slotX, slotY, want[slot-1])
		}
	}
}

// A calibrated army_slots.json still wins, scaled from its own frame.
func TestArmySlotTapPoints_HonoursCalibration(t *testing.T) {
	cfg := &armySlotsConfig{Width: 1280, Height: 720}
	cfg.DropdownArrow.X, cfg.DropdownArrow.Y = 700, 100
	cfg.Slots = map[string]armySlotRect{
		"3": {X: 800, Y: 400},
	}
	arrowX, arrowY, slotX, slotY := armySlotTapPoints(1280, 720, 3, cfg)
	if arrowX != 700 || arrowY != 100 {
		t.Errorf("arrow = (%d,%d), want (700,100)", arrowX, arrowY)
	}
	if slotX != 800 || slotY != 400 {
		t.Errorf("slot 3 = (%d,%d), want (800,400)", slotX, slotY)
	}
}

// Out-of-range values fall back to the slot 1 row instead of tapping off the
// list.
func TestArmySlotTapPoints_ClampsSlot(t *testing.T) {
	_, _, _, low := armySlotTapPoints(1024, 768, 0, nil)
	_, _, _, high := armySlotTapPoints(1024, 768, 5, nil)
	if !near(low, 240) || !near(high, 240) {
		t.Errorf("clamped rows = (%d,%d), want both ~240", low, high)
	}
}
