package main

// tapfind locates a button by template matching with the project's own
// templates and taps it, so UI driving does not depend on hand-typed
// coordinates. Coordinates are fragile here: the village HUD is dense and a
// 20px miss opens a building panel instead of the intended screen.
//
// Usage: tapfind <template-name> [threshold] [--dry]
//
//	tapfind btn_attack 0.75
//	tapfind btn_army_arrow 0.72 --dry
import (
	"fmt"
	"image"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"

	"github.com/Ducky705/ClashGO/internal/game"
	"github.com/Ducky705/ClashGO/internal/paths"
	"gocv.io/x/gocv"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: tapfind <template> [threshold] [--dry]")
		os.Exit(2)
	}
	name := os.Args[1]
	threshold := 0.70
	dry := false
	for _, a := range os.Args[2:] {
		if a == "--dry" {
			dry = true
			continue
		}
		if v, err := strconv.ParseFloat(a, 64); err == nil {
			threshold = v
		}
	}

	framePath := "/tmp/_tapfind.png"
	cmd := exec.Command("adb", "-s", "127.0.0.1:5555", "exec-out", "screencap", "-p")
	out, err := cmd.Output()
	if err != nil || len(out) < 1000 {
		fmt.Fprintf(os.Stderr, "screencap failed: %v (%d bytes)\n", err, len(out))
		os.Exit(1)
	}
	if err := os.WriteFile(framePath, out, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "write frame:", err)
		os.Exit(1)
	}

	frame := gocv.IMRead(framePath, gocv.IMReadColor)
	if frame.Empty() {
		fmt.Fprintln(os.Stderr, "cannot read frame")
		os.Exit(1)
	}
	defer frame.Close()

	tplPath := filepath.Join(paths.Resolve("templates"), name+".png")
	tpl := gocv.IMRead(tplPath, gocv.IMReadColor)
	if tpl.Empty() {
		fmt.Fprintf(os.Stderr, "cannot read template %s\n", tplPath)
		os.Exit(1)
	}
	defer tpl.Close()

	cal := game.NewCalibration(frame.Cols(), frame.Rows())
	scale := cal.DisplayScale()

	best := struct {
		score float32
		x, y  int
		w, h  int
	}{score: -1}
	for _, s := range []float64{1.0, scale} {
		w := int(float64(tpl.Cols()) * s)
		h := int(float64(tpl.Rows()) * s)
		if w < 8 || h < 8 || w > frame.Cols() || h > frame.Rows() {
			continue
		}
		resized := gocv.NewMat()
		gocv.Resize(tpl, &resized, image.Pt(w, h), 0, 0, gocv.InterpolationArea)
		res := gocv.NewMat()
		gocv.MatchTemplate(frame, resized, &res, gocv.TmCcoeffNormed, gocv.NewMat())
		_, maxV, _, maxLoc := gocv.MinMaxLoc(res)
		if maxV > best.score {
			best.score = maxV
			best.x = maxLoc.X + w/2
			best.y = maxLoc.Y + h/2
			best.w = w
			best.h = h
		}
		res.Close()
		resized.Close()
	}

	fmt.Printf("template=%s score=%.3f centre=(%d,%d) size=%dx%d frame=%dx%d\n",
		name, best.score, best.x, best.y, best.w, best.h, frame.Cols(), frame.Rows())
	if best.score < float32(threshold) {
		fmt.Printf("below threshold %.2f; not tapping\n", threshold)
		os.Exit(3)
	}
	if dry {
		fmt.Println("dry run; not tapping")
		return
	}
	if err := exec.Command("/tmp/uiact", "sh",
		fmt.Sprintf("input tap %d %d", best.x, best.y)).Run(); err != nil {
		fmt.Fprintln(os.Stderr, "tap failed:", err)
		os.Exit(1)
	}
	fmt.Printf("tapped %d,%d\n", best.x, best.y)
}
