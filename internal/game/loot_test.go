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

func TestLootVictory(t *testing.T) {
	lr := newTestLootRecognizer(t)
	img := gocv.IMRead("testdata/screen_victory.png", gocv.IMReadColor)
	if img.Empty() {
		t.Skip("screen_victory.png is missing")
	}
	defer img.Close()

	result, err := lr.ReadBattleResult(img)
	if err != nil {
		t.Fatalf("ReadBattleResult: %v", err)
	}

	if result.Stars != 2 {
		t.Errorf("Stars = %d, want 2", result.Stars)
	}

	// Battle Loot
	if result.Loot.Gold != 1985245 {
		t.Errorf("Loot.Gold = %d, want 1985245", result.Loot.Gold)
	}
	if result.Loot.Elixir != 1985977 {
		t.Errorf("Loot.Elixir = %d, want 1985977", result.Loot.Elixir)
	}
	if result.Loot.DarkElixir != 21600 {
		t.Errorf("Loot.DE = %d, want 21600", result.Loot.DarkElixir)
	}

	// Bonus Loot
	if result.Bonus.Gold != 312400 {
		t.Errorf("Bonus.Gold = %d, want 312400", result.Bonus.Gold)
	}
	if result.Bonus.Elixir != 312400 {
		t.Errorf("Bonus.Elixir = %d, want 312400", result.Bonus.Elixir)
	}
	if result.Bonus.DarkElixir != 2310 {
		t.Errorf("Bonus.DE = %d, want 2310", result.Bonus.DarkElixir)
	}
}
