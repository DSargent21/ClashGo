package main

import (
	"fmt"
	"os"

	"github.com/Ducky705/ClashGo/internal/game"
	"github.com/rs/zerolog"
	"gocv.io/x/gocv"
)

func main() {
	logger := zerolog.New(os.Stdout).With().Timestamp().Logger()
	screen := gocv.IMRead("loot_debug.png", gocv.IMReadColor)
	if screen.Empty() {
		fmt.Println("❌ Error: loot_debug.png not found")
		os.Exit(1)
	}
	defer screen.Close()

	cal := &game.Calibration{
		PhysicalW: screen.Cols(),
		PhysicalH: screen.Rows(),
		ScaleX:    float64(screen.Cols()) / 860.0,
		ScaleY:    float64(screen.Rows()) / 732.0,
	}

	ts, _ := game.NewTemplateStore("assets/templates")
	ts.LoadTemplates()
	defer ts.Close()

	lr := game.NewLootRecognizer(cal, ts, logger)
	lr.Debug = true
	defer lr.Close()

	report, _ := lr.ReadLootDetailed(screen)
	res := report.Resources

	fmt.Printf("\nGround Truth Comparison:\n")
	fmt.Printf("Gold:   %-8d | Expected: 498911 | %s\n", res.Gold, check(res.Gold, 498911))
	fmt.Printf("Elixir: %-8d | Expected: 572452 | %s\n", res.Elixir, check(res.Elixir, 572452))
	fmt.Printf("DE:     %-8d | Expected: 6959   | %s\n", res.DarkElixir, check(res.DarkElixir, 6959))

	if res.Gold == 498911 && res.Elixir == 572452 && res.DarkElixir == 6959 {
		fmt.Println("\n✅ ALL TESTS PASSED")
	} else {
		fmt.Println("\n❌ TESTS FAILED")
		os.Exit(1)
	}
}

func check(got, expected int) string {
	if got == expected {
		return "PASS"
	}
	return "FAIL"
}
