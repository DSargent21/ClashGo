package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Ducky705/ClashGo/internal/game"
	"github.com/rs/zerolog"
	"gocv.io/x/gocv"
)

// ANSI colors
const (
	colorReset  = "\033[0m"
	colorRed    = "\033[31m"
	colorGreen  = "\033[32m"
	colorYellow = "\033[33m"
	colorBlue   = "\033[34m"
	colorCyan   = "\033[36m"
	colorBold   = "\033[1m"
)

type ElementAssertion struct {
	Template string     `json:"template"`
	Pos      game.Point `json:"pos"`
	Found    bool       `json:"found"`
	Conf     float64    `json:"confidence,omitempty"`
}

type TestCase struct {
	Image         string             `json:"image"`
	ExpectedState string             `json:"expected_state"`
	ExpectedLoot  game.Resources     `json:"expected_loot"`
	Elements      []ElementAssertion `json:"elements,omitempty"`
}

type TestResult struct {
	Name      string
	Passed    bool
	Latency   map[string]time.Duration
	Errors    []string
	StateGot  string
	StateWant string
	LootGot   game.Resources
	LootWant  game.Resources
}

var (
	testDir    = flag.String("dir", "test_data/images", "Directory containing test images and snapshots")
	update     = flag.Bool("update", false, "Update snapshots with current vision results")
	verbose    = flag.Bool("v", false, "Verbose output")
	templates  = flag.String("templates", "assets/templates", "Template directory")
	concurrency = flag.Int("j", runtime.NumCPU(), "Number of parallel workers")
)

func main() {
	flag.Parse()

	logger := zerolog.New(zerolog.ConsoleWriter{Out: os.Stderr}).Level(zerolog.ErrorLevel)

	if *update {
		// Update is usually sequential to avoid file write contention, but could be parallel too.
		// For now, keep it simple.
		ts, _ := game.NewTemplateStore(*templates)
		ts.LoadTemplates()
		cal := &game.Calibration{PhysicalW: 1920, PhysicalH: 1080, ScaleX: 1.0, ScaleY: 1.0}
		runUpdate(*testDir, game.NewClassifier(cal, game.DefaultClassifierConfig(), logger), game.NewLootRecognizer(cal, ts, logger))
		return
	}

	runTestsParallel(*testDir, logger)
}

func runTestsParallel(dir string, logger zerolog.Logger) {
	fmt.Printf("%sRunning Vision Benchmarks (workers: %d)...%s\n\n", colorBold, *concurrency, colorReset)

	var snapshots []string
	filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() && strings.HasSuffix(path, ".vtest.json") {
			snapshots = append(snapshots, path)
		}
		return nil
	})

	if len(snapshots) == 0 {
		fmt.Println("No test snapshots found.")
		return
	}

	sort.Strings(snapshots)

	jobs := make(chan string, len(snapshots))
	results := make(chan TestResult, len(snapshots))
	var wg sync.WaitGroup

	// Start workers
	for w := 0; w < *concurrency; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			
			// Each worker needs its own vision components to avoid mutex contention
			ts, _ := game.NewTemplateStore(*templates)
			ts.LoadTemplates()
			cal := &game.Calibration{PhysicalW: 1920, PhysicalH: 1080, ScaleX: 1.0, ScaleY: 1.0}
			classifier := game.NewClassifier(cal, game.DefaultClassifierConfig(), logger)
			classifier.SetTemplates(ts)
			lootRec := game.NewLootRecognizer(cal, ts, logger)
			defer ts.Close()
			defer lootRec.Close()

			for path := range jobs {
				results <- runSingleTest(path, classifier, lootRec)
			}
		}()
	}

	for _, path := range snapshots {
		jobs <- path
	}
	close(jobs)

	go func() {
		wg.Wait()
		close(results)
	}()

	var allResults []TestResult
	start := time.Now()
	for res := range results {
		allResults = append(allResults, res)
		status := fmt.Sprintf("%sPASS%s", colorGreen, colorReset)
		if !res.Passed {
			status = fmt.Sprintf("%sFAIL%s", colorRed, colorReset)
		}
		
		totalLat := time.Duration(0)
		for _, d := range res.Latency {
			totalLat += d
		}

		fmt.Printf("%s %-40s %8s [S:%-4s L:%-4s]\n", 
			status, 
			res.Name, 
			totalLat.Round(time.Millisecond),
			res.Latency["state"].Round(time.Millisecond),
			res.Latency["loot"].Round(time.Millisecond))
	}

	// Sort results for consistent summary output
	sort.Slice(allResults, func(i, j int) bool {
		return allResults[i].Name < allResults[j].Name
	})

	printSummary(allResults, time.Since(start))
}

func runUpdate(dir string, classifier *game.Classifier, lootRec *game.LootRecognizer) {
	fmt.Printf("%sUpdating snapshots in %s...%s\n", colorCyan, dir, colorReset)

	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || filepath.Ext(path) != ".png" {
			return nil
		}

		// Skip if it looks like a template or system file
		if strings.Contains(path, "node_modules") || strings.Contains(path, "assets/templates") {
			return nil
		}

		fmt.Printf("Processing %s... ", path)
		img := gocv.IMRead(path, gocv.IMReadColor)
		if img.Empty() {
			fmt.Println("Failed to read image")
			return nil
		}
		defer img.Close()

		state, _ := classifier.ClassifyState(img)
		loot, _ := lootRec.ReadAvailableLoot(img)

		tc := TestCase{
			Image:         info.Name(),
			ExpectedState: state.String(),
			ExpectedLoot:  loot,
		}

		jsonPath := strings.TrimSuffix(path, ".png") + ".vtest.json"
		data, _ := json.MarshalIndent(tc, "", "  ")
		if err := os.WriteFile(jsonPath, data, 0644); err != nil {
			fmt.Printf("Error writing %s: %v\n", jsonPath, err)
		} else {
			fmt.Println("Done")
		}
		return nil
	})

	if err != nil {
		fmt.Printf("Walk error: %v\n", err)
	}
}

func runSingleTest(path string, classifier *game.Classifier, lootRec *game.LootRecognizer) TestResult {
	res := TestResult{
		Name:    filepath.Base(path),
		Passed:  true,
		Latency: make(map[string]time.Duration),
	}

	data, err := os.ReadFile(path)
	if err != nil {
		res.Passed = false
		res.Errors = append(res.Errors, fmt.Sprintf("Read JSON: %v", err))
		return res
	}

	var tc TestCase
	if err := json.Unmarshal(data, &tc); err != nil {
		res.Passed = false
		res.Errors = append(res.Errors, fmt.Sprintf("Parse JSON: %v", err))
		return res
	}

	imgPath := filepath.Join(filepath.Dir(path), tc.Image)
	img := gocv.IMRead(imgPath, gocv.IMReadColor)
	if img.Empty() {
		res.Passed = false
		res.Errors = append(res.Errors, fmt.Sprintf("Read Image %s: empty", imgPath))
		return res
	}
	defer img.Close()

	// 1. State Classification
	s1 := time.Now()
	state, _ := classifier.ClassifyState(img)
	res.Latency["state"] = time.Since(s1)
	res.StateGot = state.String()
	res.StateWant = tc.ExpectedState

	if res.StateGot != res.StateWant {
		res.Passed = false
		res.Errors = append(res.Errors, fmt.Sprintf("State mismatch: got %s, want %s", res.StateGot, res.StateWant))
	}

	// 2. Loot OCR
	s2 := time.Now()
	loot, _ := lootRec.ReadAvailableLoot(img)
	res.Latency["loot"] = time.Since(s2)
	res.LootGot = loot
	res.LootWant = tc.ExpectedLoot

	if loot.Gold != tc.ExpectedLoot.Gold || loot.Elixir != tc.ExpectedLoot.Elixir || loot.DarkElixir != tc.ExpectedLoot.DarkElixir {
		res.Passed = false
		res.Errors = append(res.Errors, fmt.Sprintf("Loot mismatch: got G:%d E:%d DE:%d, want G:%d E:%d DE:%d",
			loot.Gold, loot.Elixir, loot.DarkElixir,
			tc.ExpectedLoot.Gold, tc.ExpectedLoot.Elixir, tc.ExpectedLoot.DarkElixir))
	}

	return res
}

func printSummary(results []TestResult, totalTime time.Duration) {
	passed := 0
	failed := 0
	for _, r := range results {
		if r.Passed {
			passed++
		} else {
			failed++
		}
	}

	fmt.Printf("\n%s%sSummary:%s\n", colorBold, colorCyan, colorReset)
	fmt.Printf("  Total:  %d\n", len(results))
	fmt.Printf("  %sPassed: %d%s\n", colorGreen, passed, colorReset)
	if failed > 0 {
		fmt.Printf("  %sFailed: %d%s\n", colorRed, failed, colorReset)
	}
	fmt.Printf("  Time:   %s\n", totalTime.String())

	if failed > 0 {
		fmt.Printf("\n%sFailures:%s\n", colorRed, colorReset)
		for _, r := range results {
			if !r.Passed {
				fmt.Printf("  %s%s%s\n", colorBold, r.Name, colorReset)
				for _, e := range r.Errors {
					fmt.Printf("    - %s\n", e)
				}
			}
		}
		os.Exit(1)
	}
}
