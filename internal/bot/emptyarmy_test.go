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
