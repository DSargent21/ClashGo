package attack

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Ducky705/ClashGO/internal/game"
	"github.com/rs/zerolog"
	"gocv.io/x/gocv"
)

func TestZZVerifyPlacement(t *testing.T) {
	home, _ := os.UserHomeDir()
	dir := filepath.Join(home, "Library/Application Support/ClashGO/dev")

	// CLASHGO_PROBE_FRAMES (comma-separated paths) reads frames that were copied
	// out of the config dir — a finished run's own post-deploy evidence. Defaults
	// to the config dir's live pair so the probe still works unparameterised.
	type frame struct{ name, path string }
	var frames []frame
	if env := os.Getenv("CLASHGO_PROBE_FRAMES"); env != "" {
		for _, p := range strings.Split(env, ",") {
			if p = strings.TrimSpace(p); p == "" {
				continue
			}
			frames = append(frames, frame{filepath.Base(p), p})
		}
	} else {
		frames = []frame{
			{"DEPLOY-TIME BAR", filepath.Join(dir, "last_troop_bar.png")},
			{"POST-DEPLOY BAR", filepath.Join(dir, "last_attack_overview.png")},
		}
	}

	cal := game.NewCalibration(1280, 720)
	cal.SetDisplayScale(1.325)

	for _, f := range frames {
		if _, err := os.Stat(f.path); err != nil {
			t.Skipf("%s missing: %v", f.name, err)
		}
		img := gocv.IMRead(f.path, gocv.IMReadColor)
		if img.Empty() {
			t.Fatalf("cannot read %s", f.path)
		}
		tc := NewTroopCounter(cal, 860, 732, zerolog.Nop())
		slots := make([]*TrackedSlot, 0, len(corpusSlots))
		for _, x := range corpusSlots {
			slots = append(slots, &TrackedSlot{TroopSlot: TroopSlot{X: x, Y: 682, Category: "Troop"}})
		}
		counts := tc.DetectCounts(img, slots, 621)
		t.Logf("--- %s", f.name)
		for _, c := range counts {
			t.Logf("    slot %3d count=%2d ok=%-5v", c.X, c.Count, c.OK)
		}
		img.Close()
	}
}
