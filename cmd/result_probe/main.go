// Command result_probe is a diagnostic harness that runs the bot's own
// battle-result OCR pipeline against a saved screenshot (defaults to
// last_battle_result.png in the config dir). It prints the detected loot
// text lines, star point samples, and the final parse so a misparse can
// be traced to a wrong zone, dropped line, or theme shift — without
// touching the live bot.
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

// starPointsRef are the star sample points used by ReadBattleResult,
// in reference coordinates (860x732). Printed per point: the strict
// star-pixel count in the sampled patch (bright white or gold-warm; the
// old gray>100 mean check read defeat placeholder rings as stars).
var starPointsRef = []image.Point{
	{X: 327, Y: 205}, // Left
	{X: 430, Y: 196}, // Middle
	{X: 535, Y: 210}, // Right
}

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

	fmt.Println("star points (ref -> physical, star-pixel count in 11x11 patch, >=5 counts):")
	for _, pt := range starPointsRef {
		sx := int(float64(pt.X) * cal.ScaleX)
		sy := int(float64(pt.Y) * cal.ScaleY)
		r := image.Rect(sx-5, sy-5, sx+6, sy+6)
		if r.Min.X < 0 {
			r.Min.X = 0
		}
		if r.Min.Y < 0 {
			r.Min.Y = 0
		}
		if r.Max.X > img.Cols() {
			r.Max.X = img.Cols()
		}
		if r.Max.Y > img.Rows() {
			r.Max.Y = img.Rows()
		}
		sub := img.Region(r)
		defer sub.Close()
		hsv := gocv.NewMat()
		gocv.CvtColor(sub, &hsv, gocv.ColorBGRToHSV)
		starPx := 0
		for row := 0; row < hsv.Rows(); row++ {
			for col := 0; col < hsv.Cols(); col++ {
				h := hsv.GetUCharAt(row, col*3)
				s := hsv.GetUCharAt(row, col*3+1)
				v := hsv.GetUCharAt(row, col*3+2)
				if v > 180 && (s < 70 || h < 50 || h > 160) {
					starPx++
				}
			}
		}
		hsv.Close()
		lit := starPx >= 5
		fmt.Printf("  (%d,%d) -> phys (%d,%d) star_px=%d lit=%v\n", pt.X, pt.Y, sx, sy, starPx, lit)
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
