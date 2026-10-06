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

	"github.com/Ducky705/ClashGO/internal/config"
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

	// The calibration must be the one the bot itself runs with (same pinned
	// display scale), or the probe mapping describes a different device than
	// the one that misclassified — this tool used to derive its own scale and
	// then disagreed with the bot on every centre-anchored rule.
	cal := game.NewCalibration(img.Cols(), img.Rows())
	cal.Verified = true
	scale, scaleSource, _ := config.ResolveDisplayScale(0)
	if scale > 0 {
		cal.SetDisplayScale(scale)
	}
	fmt.Printf("scale:   k=%.4f (%s)\n", cal.DisplayScale(), scaleSource)

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

	// Dump every rule's verdict so a near-miss is visible. Deliberately through
	// EvaluateRules — the SAME scorer ClassifyState uses. This listing used to
	// re-implement probe mapping and hit testing (first with cal.Hud for every
	// rule, then a hand-rolled colour check), and both copies drifted from the
	// classifier: a rule the classifier fired on showed "pass=1/3" here. A
	// tool that cannot silently disagree with the bot is the point.
	for _, v := range classifier.EvaluateRules(img, true) {
		marker := " "
		if v.Matched {
			marker = "*"
		}
		if v.Template != "" {
			marker += "T"
		}
		fmt.Printf("  %s %-18s pass=%d/%d minpass=%d score=%d\n",
			marker, v.State.String(), v.Passed, v.Checks, v.Required, v.Score)
	}
}
