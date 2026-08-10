// Command result_probe is a diagnostic harness that runs the bot's own
// battle-result OCR pipeline against a saved screenshot (defaults to
// last_battle_result.png in the config dir). It prints the raw pixel
// values of the star points and the battle-loot/bonus columns so a
// misparse can be traced to a wrong ROI, missing template, or theme
// shift — without touching the live bot.
package main

import (
	"flag"
	"fmt"
	"image"
	"os"

	"github.com/Ducky705/ClashGO/internal/game"
	"github.com/Ducky705/ClashGO/internal/paths"
	"github.com/rs/zerolog"
	"gocv.io/x/gocv"
)

func main() {
	imgPath := flag.String("img", paths.ResolveConfig("last_battle_result.png"), "screenshot to analyze")
	debug := flag.Bool("debug", false, "enable digit-level OCR logging (per-row detected digits + read rects)")
	flag.Parse()

	lvl := zerolog.InfoLevel
	if *debug {
		lvl = zerolog.DebugLevel
	}
	logger := zerolog.New(zerolog.ConsoleWriter{Out: os.Stderr, NoColor: true}).Level(lvl)

	img := gocv.IMRead(*imgPath, gocv.IMReadColor)
	if img.Empty() {
		fmt.Fprintf(os.Stderr, "cannot read %s\n", *imgPath)
		os.Exit(1)
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

	// Classify first so we know what state the bot sees in this frame.
	classifier := game.NewClassifier(cal, game.DefaultClassifierConfig(), logger)
	ts, err := game.NewTemplateStore(paths.Resolve("templates"))
	if err == nil {
		ts.LoadTemplates()
		classifier.SetTemplates(ts)
		defer ts.Close()
	}
	state, score := classifier.ClassifyState(img)
	fmt.Printf("classifier: state=%s score=%d\n\n", state.String(), score)

	gray := gocv.NewMat()
	gocv.CvtColor(img, &gray, gocv.ColorBGRToGray)
	defer gray.Close()

	// Star points used by ReadBattleResult (reference coords).
	starPoints := []image.Point{
		{X: 327, Y: 205}, // Left
		{X: 430, Y: 196}, // Middle
		{X: 535, Y: 210}, // Right
	}
	fmt.Println("star points (ref -> physical, gray mean over 5x5):")
	for _, pt := range starPoints {
		sx := int(float64(pt.X) * cal.ScaleX)
		sy := int(float64(pt.Y) * cal.ScaleY)
		r := image.Rect(sx-2, sy-2, sx+3, sy+3)
		if r.Min.X < 0 {
			r.Min.X = 0
		}
		if r.Min.Y < 0 {
			r.Min.Y = 0
		}
		if r.Max.X > gray.Cols() {
			r.Max.X = gray.Cols()
		}
		if r.Max.Y > gray.Rows() {
			r.Max.Y = gray.Rows()
		}
		sub := gray.Region(r)
		m := sub.Mean().Val1
		sub.Close()
		fmt.Printf("  (%d,%d) -> phys (%d,%d) gray_mean=%.1f (star if >100)\n", pt.X, pt.Y, sx, sy, m)
	}

	// Column ROIs from ReadBattleResult defaults.
	// (Declared for documentation; sampling loops below hardcode the ranges.)

	// Sample a horizontal strip of the battle-loot rows to see digit/icon layout.
	fmt.Println("\nbattle-loot column pixel sample (ref 311-501, rows 313/360/407):")
	for _, y := range []int{313, 360, 407} {
		sy := int(float64(y) * cal.ScaleY)
		fmt.Printf("  y=%d: ", y)
		for x := 311; x <= 501; x += 15 {
			sx := int(float64(x) * cal.ScaleX)
			if sx >= img.Cols() || sy >= img.Rows() {
				continue
			}
			b := img.GetUCharAt(sy, sx*3)
			g := img.GetUCharAt(sy, sx*3+1)
			r := img.GetUCharAt(sy, sx*3+2)
			fmt.Printf("[%d](%d,%d,%d) ", x, r, g, b)
		}
		fmt.Println()
	}

	fmt.Println("\nbonus-loot column pixel sample (ref 571-674, rows 366/409/452):")
	for _, y := range []int{366, 409, 452} {
		sy := int(float64(y) * cal.ScaleY)
		fmt.Printf("  y=%d: ", y)
		for x := 571; x <= 674; x += 12 {
			sx := int(float64(x) * cal.ScaleX)
			if sx >= img.Cols() || sy >= img.Rows() {
				continue
			}
			b := img.GetUCharAt(sy, sx*3)
			g := img.GetUCharAt(sy, sx*3+1)
			r := img.GetUCharAt(sy, sx*3+2)
			fmt.Printf("[%d](%d,%d,%d) ", x, r, g, b)
		}
		fmt.Println()
	}

	// Run the actual recognizer for comparison.
	lr := game.NewLootRecognizer(cal, ts, logger)
	defer lr.Close()
	lr.Debug = *debug
	res, err := lr.ReadBattleResult(img)
	if err != nil {
		fmt.Printf("\nReadBattleResult error: %v\n", err)
	} else {
		fmt.Printf("\nReadBattleResult: stars=%d loot=(g%d e%d de%d) bonus=(g%d e%d de%d)\n",
			res.Stars, res.Loot.Gold, res.Loot.Elixir, res.Loot.DarkElixir,
			res.Bonus.Gold, res.Bonus.Elixir, res.Bonus.DarkElixir)
	}
}
