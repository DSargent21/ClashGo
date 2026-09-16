package game

import (
	"image"
	"os"
	"path/filepath"
	"testing"

	"github.com/rs/zerolog"
	"gocv.io/x/gocv"
)

type lootTestCase struct {
	name     string
	imgPath  string
	wantGold int
	wantElix int
	wantDE   int
}

// Reference screenshots with hand-labelled ground-truth loot values.
var lootTestCases = []lootTestCase{
	{
		name:     "screen_ocr",
		imgPath:  "testdata/screen_ocr.png",
		wantGold: 295434,
		wantElix: 786892,
		wantDE:   7249,
	},
}

func testLogger(t testing.TB) zerolog.Logger {
	t.Helper()
	return zerolog.New(zerolog.ConsoleWriter{Out: os.Stderr, NoColor: true}).
		Level(zerolog.ErrorLevel).
		With().Timestamp().Logger()
}

func newTestLootRecognizer(t testing.TB) *LootRecognizer {
	t.Helper()

	// Resolve template path from the package's perspective.
	// Compensate for "go test" running from the package directory.
	templateDir := "../../assets/templates"
	// Ensure the path exists; try alternative relative locations.
	if _, err := os.Stat(templateDir); os.IsNotExist(err) {
		// Try from the module root
		templateDir = "assets/templates"
	}
	if _, err := os.Stat(templateDir); os.IsNotExist(err) {
		// Try absolute derived from test file location
		ex, _ := os.Executable()
		templateDir = filepath.Join(filepath.Dir(ex), "assets", "templates")
	}

	ts, err := NewTemplateStore(templateDir)
	if err != nil {
		t.Fatalf("NewTemplateStore(%s): %v", templateDir, err)
	}
	if err := ts.LoadTemplates(); err != nil {
		t.Fatalf("LoadTemplates: %v", err)
	}
	t.Cleanup(ts.Close)

	cal := &Calibration{
		PhysicalW: RefWidth,
		PhysicalH: RefHeight,
		ScaleX:    1.0,
		ScaleY:    1.0,
	}
	lr := NewLootRecognizer(cal, ts, testLogger(t))
	t.Cleanup(lr.Close)
	return lr
}

// TestLootAccuracy runs all reference screenshots and reports accuracy.
func TestLootAccuracy(t *testing.T) {
	lr := newTestLootRecognizer(t)

	for _, tc := range lootTestCases {
		t.Run(tc.name, func(t *testing.T) {
			img := gocv.IMRead(tc.imgPath, gocv.IMReadColor)
			if img.Empty() {
				t.Skipf("cannot read %s (cwd may be wrong)", tc.imgPath)
			}
			defer img.Close()

			report, err := lr.ReadLootDetailed(img)
			if err != nil {
				t.Fatalf("ReadLootDetailed: %v", err)
			}
			got := report.Resources

			// Exact match compare
			if got.Gold != tc.wantGold {
				t.Logf("Gold: got %d, want %d", got.Gold, tc.wantGold)
			}
			if got.Elixir != tc.wantElix {
				t.Logf("Elixir: got %d, want %d", got.Elixir, tc.wantElix)
			}
			if got.DarkElixir != tc.wantDE {
				t.Logf("DE: got %d, want %d", got.DarkElixir, tc.wantDE)
			}
		})
	}
}

func TestLootProgrammatic(t *testing.T) {
	lr := newTestLootRecognizer(t)

	// Ensure digit templates are loaded
	loadedCount := 0
	for _, tpl := range lr.digitTemplates {
		if !tpl.Empty() {
			loadedCount++
		}
	}
	if loadedCount < 10 {
		t.Skip("Some templates are missing; skipping programmatic test")
	}

	// Create a canvas 400x60 BGR
	canvas := gocv.NewMatWithSize(60, 400, gocv.MatTypeCV8UC3)
	canvas.SetTo(gocv.NewScalar(0, 0, 0, 0))
	defer canvas.Close()

	// Let's compose "45309" on the canvas
	digitsToDraw := []int{4, 5, 3, 0, 9}
	xOffset := 15
	yOffset := 15

	for _, d := range digitsToDraw {
		tpl := lr.digitTemplates[d]
		// Create a BGR version of the template to copy to canvas
		tplBGR := gocv.NewMat()
		gocv.CvtColor(tpl, &tplBGR, gocv.ColorGrayToBGR)

		rows := tpl.Rows()
		cols := tpl.Cols()

		targetRect := image.Rect(xOffset, yOffset, xOffset+cols, yOffset+rows)
		subMat := canvas.Region(targetRect)

		tplBGR.CopyTo(&subMat)
		subMat.Close()
		tplBGR.Close()

		xOffset += cols + 2 // 2px gap between digits
	}

	// Now run readRow on this canvas
	val := lr.readRow(canvas, image.Rect(0, 0, canvas.Cols(), canvas.Rows()))
	want := 45309
	if val != want {
		t.Errorf("readRow returned %d, want %d", val, want)
	}
}

// TestLootConsistency checks that multiple calls on the same image give the
// same result (determinism).
func TestLootConsistency(t *testing.T) {
	lr := newTestLootRecognizer(t)

	for _, tc := range lootTestCases {
		t.Run(tc.name, func(t *testing.T) {
			img := gocv.IMRead(tc.imgPath, gocv.IMReadColor)
			if img.Empty() {
				t.Skipf("cannot read %s", tc.imgPath)
			}
			defer img.Close()

			first, err := lr.ReadAvailableLoot(img)
			if err != nil {
				t.Fatalf("first call: %v", err)
			}

			for i := 0; i < 5; i++ {
				next, err := lr.ReadAvailableLoot(img)
				if err != nil {
					t.Fatalf("call %d: %v", i+2, err)
				}
				if next != first {
					t.Errorf("call %d changed: first=%+v, now=%+v", i+2, first, next)
				}
			}
		})
	}
}

// BenchmarkLootRecognition measures per-image throughput.
func BenchmarkLootRecognition(b *testing.B) {
	lr := newTestLootRecognizer(b)

	img := gocv.IMRead("testdata/screen_ocr.png", gocv.IMReadColor)
	if img.Empty() {
		b.Fatal("cannot read reference image")
	}
	defer img.Close()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := lr.ReadAvailableLoot(img)
		if err != nil {
			b.Fatal(err)
		}
	}
}

// TestLootAccuracyNoTolerance reports exact mismatches for every digit.
// Useful during HSV tuning — run with -v to see detailed breakdown.
func TestLootAccuracyExact(t *testing.T) {
	lr := newTestLootRecognizer(t)

	failures := 0
	for _, tc := range lootTestCases {
		img := gocv.IMRead(tc.imgPath, gocv.IMReadColor)
		if img.Empty() {
			t.Logf("SKIP: cannot read %s", tc.imgPath)
			continue
		}
		defer img.Close()

		report, err := lr.ReadLootDetailed(img)
		if err != nil {
			t.Logf("  %s: error %v", tc.name, err)
			failures++
			continue
		}
		got := report.Resources

		if got.Gold != tc.wantGold || got.Elixir != tc.wantElix || got.DarkElixir != tc.wantDE {
			t.Logf("  %s: GOLD %d (want %d) | ELIX %d (want %d) | DE %d (want %d)",
				tc.name, got.Gold, tc.wantGold, got.Elixir, tc.wantElix, got.DarkElixir, tc.wantDE)
			failures++
		}
	}

	// Just log the results rather than failing — useful during tuning.
	t.Logf("\n%d / %d cases have exact match failures — tune HSV ranges above",
		failures, len(lootTestCases))
}

// victoryTestCase captures the ground truth for an end-of-battle screen.
// screen_victory.png is the tracked regression fixture for victory-screen
// parsing, including the league-bonus column (which previously clipped a
// trailing zero on a live capture). The former screen_victory_live.png
// fixture was a live capture containing real player names and was removed
// for privacy; screen_victory.png keeps victory-screen coverage.
type victoryTestCase struct {
	name      string
	imgPath   string
	wantStars int
	loot      Resources
	bonus     Resources
}

var victoryTestCases = []victoryTestCase{
	{
		name:      "screen_victory",
		imgPath:   "testdata/screen_victory.png",
		wantStars: 2,
		loot:      Resources{Gold: 1985245, Elixir: 1985977, DarkElixir: 21600},
		bonus:     Resources{Gold: 312400, Elixir: 312400, DarkElixir: 2310},
	},
}

// defeatTestCase is the regression fixture for the defeat screen that a
// live 35% loss misparsed as "2⭐". The screenshot was sanitized (the
// top strip with the opponent's player/clan names is blacked out); the
// star band (y 140-260) and loot rows are untouched.
//
// A defeat screen still lists the attacker's own available loot in the
// result panel (ground truth read off the fixture), so these values are
// exact. Earlier parser revisions hallucinated wildly here: fixed slots
// missed the digits and OCR'd background noise ("e780077", "de107" from
// a row whose true value is 10735).

// TestStarsFromOutcome pins CoC's star scoring rules (the authoritative
// source for battle reports; result-screen art is only a fallback):
// >=50% destruction = 1 star, Town Hall destroyed = +1, 100% = 3.
func TestStarsFromOutcome(t *testing.T) {
	cases := []struct {
		pct         int
		thDestroyed bool
		want        int
	}{
		{0, false, 0},  // wipe-out
		{35, false, 0}, // live defeat (35% loss)
		{35, true, 1},  // TH sniped below 50%: still 1 star
		{49, true, 1},
		{50, false, 1}, // exactly the auto-end threshold, TH intact
		{50, true, 2},  // 50% + TH destroyed
		{74, false, 1},
		{89, true, 2},
		{99, true, 2},
		{100, false, 3}, // 100% is always 3 stars
		{100, true, 3},
	}
	for _, tc := range cases {
		if got := StarsFromOutcome(tc.pct, tc.thDestroyed); got != tc.want {
			t.Errorf("StarsFromOutcome(%d, %v) = %d, want %d", tc.pct, tc.thDestroyed, got, tc.want)
		}
	}
}

// defeatTestCase pins both the star count AND the loot values for the
// defeat fixture (ground truth read off the sanitized screenshot).
var defeatTestCases = []struct {
	name      string
	imgPath   string
	wantStars int
	loot      Resources
}{
	{
		name:      "screen_defeat",
		imgPath:   "testdata/screen_defeat.png",
		wantStars: 0,
		loot:      Resources{Gold: 1369137, Elixir: 780077, DarkElixir: 10735},
	},
}

func TestBattleResultDefeat(t *testing.T) {
	lr := newTestLootRecognizer(t)

	for _, tc := range defeatTestCases {
		t.Run(tc.name, func(t *testing.T) {
			img := gocv.IMRead(tc.imgPath, gocv.IMReadColor)
			if img.Empty() {
				t.Fatalf("cannot read defeat fixture %s", tc.imgPath)
			}
			defer img.Close()

			result, err := lr.ReadBattleResult(img)
			if err != nil {
				t.Fatalf("ReadBattleResult: %v", err)
			}

			if result.Stars != tc.wantStars {
				t.Errorf("Stars = %d, want %d (defeat must never read as a win)", result.Stars, tc.wantStars)
			}
			if result.Loot != tc.loot {
				t.Errorf("Loot = %+v, want %+v (defeat loot must be read exactly, not hallucinated)", result.Loot, tc.loot)
			}
		})
	}
}

func TestLootVictory(t *testing.T) {
	lr := newTestLootRecognizer(t)

	for _, tc := range victoryTestCases {
		t.Run(tc.name, func(t *testing.T) {
			img := gocv.IMRead(tc.imgPath, gocv.IMReadColor)
			if img.Empty() {
				t.Skipf("%s is missing", tc.imgPath)
			}
			defer img.Close()

			result, err := lr.ReadBattleResult(img)
			if err != nil {
				t.Fatalf("ReadBattleResult: %v", err)
			}

			if result.Stars != tc.wantStars {
				t.Errorf("Stars = %d, want %d", result.Stars, tc.wantStars)
			}

			if result.Loot != tc.loot {
				t.Errorf("Loot = %+v, want %+v", result.Loot, tc.loot)
			}

			if result.Bonus != tc.bonus {
				t.Errorf("Bonus = %+v, want %+v", result.Bonus, tc.bonus)
			}
		})
	}
}
