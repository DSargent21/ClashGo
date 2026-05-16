package main

import (
	"fmt"
	"os"
	"time"

	"github.com/Ducky705/ClashGo/internal/game"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"gocv.io/x/gocv"
)

type testCase struct {
	name     string
	imgPath  string
	wantGold int
	wantElix int
	wantDE   int
}

var testCases = []testCase{
	{"215234", "screen_20260515_215234.png", 443168, 434311, 4822},
	{"215242", "screen_20260515_215242.png", 1347740, 527360, 339},
	{"215252", "screen_20260515_215252.png", 1413444, 1410840, 9698},
	{"215301", "screen_20260515_215301.png", 665136, 339251, 13048},
	{"215309", "screen_20260515_215309.png", 649714, 644436, 6825},
	{"215316", "screen_20260515_215316.png", 597833, 254860, 11114},
}

func main() {
	// Professional logging setup
	zerolog.TimeFieldFormat = time.RFC3339
	log.Logger = log.Output(zerolog.ConsoleWriter{
		Out:        os.Stderr,
		TimeFormat: "15:04:05",
	})
	zerolog.SetGlobalLevel(zerolog.InfoLevel)

	fmt.Println("=== Batch Loot Recognition Test (18/18 Goal) ===")
	fmt.Println()

	// 1. Initialize Template Store
	ts, err := game.NewTemplateStore("assets/templates")
	if err != nil {
		log.Fatal().Err(err).Msg("TemplateStore error")
	}
	if err := ts.LoadTemplates(); err != nil {
		log.Fatal().Err(err).Msg("LoadTemplates error")
	}
	defer ts.Close()

	score := 0

	for _, tc := range testCases {
		fmt.Printf("Testing Screenshot: %s (%s)\n", tc.name, tc.imgPath)

		img := gocv.IMRead(tc.imgPath, gocv.IMReadColor)
		if img.Empty() {
			fmt.Printf("  [ERROR] Could not read image: %s\n\n", tc.imgPath)
			continue
		}

		// 2. Calibrate based on image size
		cal := &game.Calibration{
			PhysicalW: img.Cols(),
			PhysicalH: img.Rows(),
			ScaleX:    float64(img.Cols()) / game.RefWidth,
			ScaleY:    float64(img.Rows()) / game.RefHeight,
		}

		// 3. Initialize Recognizer
		lr := game.NewLootRecognizer(cal, ts, log.Logger)
		// lr.Debug = true
		
		// 4. Perform Recognition
		loot, err := lr.ReadAvailableLoot(img)
		lr.Close()
		img.Close()

		if err != nil {
			fmt.Printf("  [ERROR] Recognition failed: %v\n", err)
			fmt.Println()
			continue
		}

		// 5. Compare Results
		results := []struct {
			name string
			got  int
			want int
		}{
			{"Gold",   loot.Gold,       tc.wantGold},
			{"Elixir", loot.Elixir,     tc.wantElix},
			{"DE",     loot.DarkElixir, tc.wantDE},
		}

		for _, r := range results {
			status := "FAIL"
			if r.got == r.want {
				status = "OK"
				score++
			}
			fmt.Printf("  %-6s: got %-10d want %-10d [%s]\n", r.name, r.got, r.want, status)
		}
		fmt.Println()
	}

	fmt.Printf("Final Score: %d/18 exact\n", score)
	if score == 18 {
		fmt.Println("SUCCESS: All 18 resource values matched exactly!")
	} else {
		fmt.Printf("FAILURE: %d/18 resource values matched. See details above.\n", score)
		os.Exit(1)
	}
}
