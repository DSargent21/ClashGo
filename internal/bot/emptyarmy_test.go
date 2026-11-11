package bot

import (
	"os"
	"testing"

	"gocv.io/x/gocv"
)

// TestEmptyArmyPanelOnLiveFrames pins the empty-army probe to two captured
// live frames: the Start Attack panel showing "Train an army before
// attacking!" (live2.png, must detect) and an ordinary village frame
// (live_now.png, must not). Skips when the captures are absent.
func TestEmptyArmyPanelOnLiveFrames(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{"../../tmp/live2.png", true},     // empty-army Start Attack panel
		{"../../tmp/live_now.png", false}, // village with clan chat open
	}
	for _, c := range cases {
		if _, err := os.Stat(c.path); err != nil {
			t.Logf("skip %s: %v", c.path, err)
			continue
		}
		img := gocv.IMRead(c.path, gocv.IMReadColor)
		if img.Empty() {
			t.Fatalf("cannot read %s", c.path)
		}
		got := emptyArmyPanel(img)
		img.Close()
		if got != c.want {
			t.Errorf("emptyArmyPanel(%s) = %v, want %v", c.path, got, c.want)
		}
	}
}

// TestArmySheetPointsStayOnTheSheet pins the empty-army recovery geometry.
//
// These four taps are the whole training path, and each was measured on a live
// 1024x768 frame (tmp/watch/recipes.png): EDIT ARMY on the empty-army panel at
// (327,561), the Saved Recipes tab at (535,176), recipe 1's USE at (896,256),
// and the sheet's close button at (943,168). A tap outside the frame is a
// silently dropped deploy-cycle; a tap in the wrong column presses EDIT or
// SHARE instead of USE and queues nothing, so the wait then burns the whole
// budget on an army that was never ordered.
func TestArmySheetPointsStayOnTheSheet(t *testing.T) {
	const w, h = 1024, 768
	for slot := 1; slot <= armyRecipeRowsVisible; slot++ {
		e, tab, use, c, ok := armySheetPoints(slot)
		if !ok {
			t.Fatalf("slot %d must be queueable", slot)
		}
		for _, p := range [][2]float64{e, tab, use, c} {
			if p[0] <= 0 || p[0] >= 1 || p[1] <= 0 || p[1] >= 1 {
				t.Fatalf("slot %d: point %v is outside the frame", slot, p)
			}
		}
		if y := int(use[1] * h); y < 200 || y > 650 {
			t.Errorf("slot %d: USE y = %d px, want inside the recipe list", slot, y)
		}
		if x := int(use[0] * w); x < w*2/3 {
			t.Errorf("slot %d: USE x = %d, want the action column (EDIT and SHARE sit beside it)", slot, x)
		}
	}

	use1, _ := armyRecipeRow(1)
	if y := int(use1[1] * h); y < 253 || y > 259 {
		t.Errorf("USE row 1 y = %d px, want the measured 256 (\u00b13 px)", y)
	}

	for slot := 2; slot <= armyRecipeRowsVisible; slot++ {
		prev, _ := armyRecipeRow(slot - 1)
		cur, _ := armyRecipeRow(slot)
		if d := cur[1] - prev[1]; d < 0.15 || d > 0.3 {
			t.Errorf("slot %d row spacing = %.3f, want ~0.222", slot, d)
		}
	}

	// Anything the list does not show must be refused, never clamped onto a
	// neighbouring row: pressing USE on the wrong recipe queues the wrong army.
	for _, bad := range []int{-1, 0, armyRecipeRowsVisible + 1, 4, 99} {
		if _, ok := armyRecipeRow(bad); ok {
			t.Errorf("slot %d reported queueable, want refused (list shows %d rows)", bad, armyRecipeRowsVisible)
		}
		if _, _, _, _, ok := armySheetPoints(bad); ok {
			t.Errorf("armySheetPoints(%d) reported a visible row, want refused", bad)
		}
	}
}
