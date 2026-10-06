package main

import (
	"fmt"
	"os"
	"time"

	"github.com/Ducky705/ClashGO/internal/attack"
	"github.com/Ducky705/ClashGO/internal/game"
	"github.com/rs/zerolog"
	"gocv.io/x/gocv"
)

// Usage: countprobe <frame.png> [barY] — runs the count reader on one frame and
// prints every slot's read plus the reader's debug rejections.
func main() {
	path := os.Args[1]
	barY := 621
	if len(os.Args) > 2 {
		fmt.Sscanf(os.Args[2], "%d", &barY)
	}
	img := gocv.IMRead(path, gocv.IMReadColor)
	if img.Empty() {
		fmt.Fprintln(os.Stderr, "cannot read", path)
		os.Exit(1)
	}
	defer img.Close()

	cal := game.NewCalibration(1280, 720)
	cal.SetDisplayScale(1.325)
	logger := zerolog.New(zerolog.ConsoleWriter{Out: os.Stdout, TimeFormat: time.TimeOnly}).Level(zerolog.DebugLevel)
	tc := attack.NewTroopCounter(cal, 860, 732, logger)

	xs := []int{137, 234, 450, 550, 643, 751, 848, 948}
	var slots []*attack.TrackedSlot
	for _, x := range xs {
		slots = append(slots, &attack.TrackedSlot{TroopSlot: attack.TroopSlot{X: x, Y: 682, Category: "Troop"}})
	}
	counts := tc.DetectCounts(img, slots, barY)
	for _, c := range counts {
		fmt.Printf("slot x=%d count=%d ok=%v conf=%.3f digits=%v\n", c.X, c.Count, c.OK, c.Confidence, c.Digits)
	}
}
