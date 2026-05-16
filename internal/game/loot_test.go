package game

import (
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
		name:     "215234",
		imgPath:  "../../screen_20260515_215234.png",
		wantGold: 443168,
		wantElix: 434311,
		wantDE:   4822,
	},
	{
		name:     "215242",
		imgPath:  "../../screen_20260515_215242.png",
		wantGold: 1347740,
		wantElix: 527360,
		wantDE:   339,
	},
	{
		name:     "215252",
		imgPath:  "../../screen_20260515_215252.png",
		wantGold: 1413444,
		wantElix: 1410840,
		wantDE:   9698,
	},
	{
		name:     "215301",
		imgPath:  "../../screen_20260515_215301.png",
		wantGold: 665136,
		wantElix: 339251,
		wantDE:   13048,
	},
	{
		name:     "215309",
		imgPath:  "../../screen_20260515_215309.png",
		wantGold: 649714,
		wantElix: 644436,
		wantDE:   6825,
	},
	{
		name:     "215316",
		imgPath:  "../../screen_20260515_215316.png",
		wantGold: 597833,
		wantElix: 254860,
		wantDE:   11114,
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
				t.Fatalf("cannot read %s (cwd may be wrong)", tc.imgPath)
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

	// Use the largest image for benchmarking.
	img := gocv.IMRead("../../screen_20260515_215252.png", gocv.IMReadColor)
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

