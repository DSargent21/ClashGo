// Command classify_probe classifies a screenshot (default: live adb
// capture) with the repo's own classifier and prints the score for every
// state rule plus a few key pixel reads, so a stuck bot can be diagnosed
// from what the vision layer actually sees.
package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"

	"github.com/Ducky705/ClashGO/internal/game"
	"github.com/Ducky705/ClashGO/internal/paths"
	"github.com/rs/zerolog"
	"gocv.io/x/gocv"
)

func main() {
	imgPath := flag.String("img", "", "screenshot to analyze (default: capture live via adb)")
	flag.Parse()

	img := gocv.Mat{}
	if *imgPath != "" {
		img = gocv.IMRead(*imgPath, gocv.IMReadColor)
		if img.Empty() {
			fmt.Fprintf(os.Stderr, "cannot read %s\n", *imgPath)
			os.Exit(1)
		}
	} else {
		out, err := exec.Command("adb", "exec-out", "screencap", "-p").Output()
		if err != nil {
			fmt.Fprintf(os.Stderr, "adb screencap: %v\n", err)
			os.Exit(1)
		}
		tmp := "/tmp/classify_live.png"
		if err := os.WriteFile(tmp, out, 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "write tmp: %v\n", err)
			os.Exit(1)
		}
		img = gocv.IMRead(tmp, gocv.IMReadColor)
		if img.Empty() {
			fmt.Fprintln(os.Stderr, "empty capture")
			os.Exit(1)
		}
	}
	defer img.Close()

	fmt.Printf("image: %dx%d\n", img.Cols(), img.Rows())

	cal := &game.Calibration{
		PhysicalW:  img.Cols(),
		PhysicalH:  img.Rows(),
		ScaleX:     float64(img.Cols()) / float64(game.RefWidth),
		ScaleY:     float64(img.Rows()) / float64(game.RefHeight),
		MidOffsetY: (img.Rows() - game.RefHeight) / 2,
		BottomOffY: img.Rows() - game.RefHeight,
		Verified:   true,
	}

	logger := zerolog.New(zerolog.ConsoleWriter{Out: os.Stderr, NoColor: true}).Level(zerolog.ErrorLevel)
	classifier := game.NewClassifier(cal, game.DefaultClassifierConfig(), logger)
	ts, err := game.NewTemplateStore(paths.Resolve("templates"))
	if err == nil {
		ts.LoadTemplates()
		classifier.SetTemplates(ts)
		defer ts.Close()
	}

	state, score := classifier.ClassifyState(img)
	fmt.Printf("classifier: state=%s score=%d\n", state.String(), score)

	// Dump every rule's pass/fail so a near-miss is visible.
	for _, rule := range classifier.GetRules() {
		passed := 0
		for _, chk := range rule.Checks {
			sx, sy := cal.ScaleRef(chk.X, chk.Y)
			if sx < 0 || sy < 0 || sx >= img.Cols() || sy >= img.Rows() {
				continue
			}
			b := img.GetUCharAt(sy, sx*3)
			g := img.GetUCharAt(sy, sx*3+1)
			r := img.GetUCharAt(sy, sx*3+2)
			if abs(int(r)-int(chk.R)) <= chk.Tolerance && abs(int(g)-int(chk.G)) <= chk.Tolerance && abs(int(b)-int(chk.B)) <= chk.Tolerance {
				passed++
			}
		}
		marker := " "
		if rule.MinPass > 0 && passed >= rule.MinPass {
			marker = "*"
		}
		if rule.Template != "" {
			marker += "T"
		}
		fmt.Printf("  %s %-18s pass=%d/%d minpass=%d\n", marker, rule.State.String(), passed, len(rule.Checks), rule.MinPass)
	}
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
