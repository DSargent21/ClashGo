// Command swipe_probe is a standalone live-verification harness for the
// human-gesture input layer (Phase 1 of the autonomous hardening loop):
// it connects to the emulator like the bot does and exercises the
// sendevent bezier-swipe primitives against the live device.
//
// Modes:
//   -mode launcher  connect, snap, SwipeBezier twice, snap (fast; tests the
//                   sendevent path on the BlueStacks home screen)
//   -mode game      start Clash of Clans, wait for settle, snap, pan twice,
//                   snap
//   -mode idlepan   build a real game.Navigator and run IdlePan (the
//                   production idle-camera wander) twice, snapping around
//                   it to prove the gesture moves the camera and returns.
//
// Snapshots land in /tmp/probe_<mode>_{before,mid,after}.png; diff them
// (e.g. via sips -> BMP pixel comparison) to confirm screen movement.
package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/Ducky705/ClashGO/internal/adb"
	"github.com/Ducky705/ClashGO/internal/game"
	"github.com/rs/zerolog"
	"gocv.io/x/gocv"
)

type probeLogger struct{}

func (probeLogger) Debug() bool                  { return true }
func (probeLogger) Debugf(f string, v ...any)    { fmt.Printf("DBG "+f+"\n", v...) }
func (probeLogger) Info(msg string)              { fmt.Println("INF", msg) }
func (probeLogger) Warn(msg string)              { fmt.Println("WRN", msg) }
func (probeLogger) Error(msg string)             { fmt.Println("ERR", msg) }
func (probeLogger) WithFields(map[string]any) adb.Logger { return probeLogger{} }

func snap(c *adb.Client, path string) {
	fmt.Printf("[%s] capturing...\n", time.Now().Format("15:04:05"))
	m, err := c.CaptureToMat()
	if err != nil || m.Empty() {
		fmt.Printf("snap %s failed: %v\n", path, err)
		if !m.Empty() {
			m.Close()
		}
		return
	}
	defer m.Close()
	if !gocv.IMWrite(path, m) {
		fmt.Printf("snap %s: IMWrite returned false\n", path)
		return
	}
	fmt.Printf("snap saved %s (%dx%d)\n", path, m.Cols(), m.Rows())
}

func main() {
	mode := flag.String("mode", "launcher", "launcher | game")
	flag.Parse()

	c := adb.NewClient(
		adb.WithHost("127.0.0.1"),
		adb.WithPort(5037),
		adb.WithLogger(probeLogger{}),
		adb.WithTimeout(30*time.Second),
	)
	c.DeviceID = "localhost:5555"
	defer c.Close()

	if err := c.Connect(); err != nil {
		fmt.Println("connect error:", err)
		os.Exit(1)
	}

	w, h, err := c.ScreenSize()
	fmt.Printf("screen %dx%d err=%v\n", w, h, err)
	dev, err := c.DetectTouchDevice()
	fmt.Printf("touch device=%q err=%v\n", dev, err)

	if *mode == "game" {
		if err := c.StartApp("com.supercell.clashofclans"); err != nil {
		fmt.Println("StartApp error:", err)
		}
		fmt.Println("waiting 70s for game settle...")
		time.Sleep(70 * time.Second)
	}

	prefix := *mode
	snap(c, fmt.Sprintf("/tmp/probe_%s_before.png", prefix))

	if *mode == "idlepan" {
		// Exercise the REAL production method through a Navigator built
		// exactly like the bot's (minus templates/classifier).
		cal := &game.Calibration{PhysicalW: w, PhysicalH: h, ScaleX: float64(w) / float64(game.RefWidth), ScaleY: float64(h) / float64(game.RefHeight), Verified: true}
		g := game.NewStateGraph()
		nav := game.NewNavigator(c, cal, g, func(gocv.Mat) (game.GameState, int) { return game.StateMainVillage, 100 }, zerolog.Nop())
		for i := 1; i <= 2; i++ {
			t0 := time.Now()
			nav.IdlePan()
			fmt.Printf("IdlePan #%d done in %s\n", i, time.Since(t0))
			time.Sleep(2 * time.Second)
		}
		snap(c, fmt.Sprintf("/tmp/probe_%s_after.png", prefix))
		fmt.Println("PROBE COMPLETE")
		return
	}

	// Pan right, then back left.
	t0 := time.Now()
	if err := c.SwipeBezier(w*3/4, h/2, w/4, h/2, 350); err != nil {
		fmt.Println("SwipeBezier #1 error:", err)
	}
	fmt.Printf("SwipeBezier #1 done in %s\n", time.Since(t0))
	time.Sleep(2 * time.Second)

	snap(c, fmt.Sprintf("/tmp/probe_%s_mid.png", prefix))

	if err := c.SwipeBezier(w/4, h/2, w*3/4, h/2, 350); err != nil {
		fmt.Println("SwipeBezier #2 error:", err)
	}
	fmt.Println("SwipeBezier #2 done")
	time.Sleep(2 * time.Second)

	snap(c, fmt.Sprintf("/tmp/probe_%s_after.png", prefix))
	fmt.Println("PROBE COMPLETE")
}
