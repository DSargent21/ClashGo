package game

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"gocv.io/x/gocv"
)

// Benchmarks for the classifier's two evaluation paths, on the real corpus
// frames with the real template store loaded — the same inputs the capture loop
// sees, because the cost that matters is the full-frame template match a rule
// runs after its pixel probes pass, not the pixel probes themselves.
//
// These are the numbers behind the "early exit" entry in docs/PERFORMANCE.md:
//
//	go test -run '^$' -bench Classify -benchtime 20x ./internal/game
//
// The gap between the two is what a frame with a decisive winner saves. It is
// not a constant: it is zero on a frame nothing matches (the loop has to see
// every rule before it can answer Unknown) and largest on a frame a
// top-priority state claims immediately.
func BenchmarkClassifyStateEarlyExit(b *testing.B) { benchmarkClassify(b, true) }
func BenchmarkEvaluateRulesFull(b *testing.B)      { benchmarkClassify(b, false) }

func benchmarkClassify(b *testing.B, early bool) {
	frames := benchCorpusFrames(b)
	b.ReportAllocs()
	b.ResetTimer()

	// Cycling the fixtures keeps one frame from being friendly to the caches.
	for i := 0; i < b.N; i++ {
		fr := frames[i%len(frames)]
		if early {
			fr.classifier.ClassifyState(fr.mat)
			continue
		}
		fr.classifier.EvaluateRules(fr.mat, false)
	}
}

// BenchmarkListBenchmarkCorpusFrames reports each fixture's rule counts under
// both paths plus a timed classify, so a saving can be attributed to specific
// rules and geometries rather than only measured in aggregate:
//
//	go test -run '^$' -bench BenchmarkListBenchmarkCorpusFrames -benchtime 1x -v ./internal/game
func BenchmarkListBenchmarkCorpusFrames(b *testing.B) {
	for _, fr := range benchCorpusFrames(b) {
		full := len(fr.classifier.GetRules())
		early := len(fr.classifier.evaluateRules(fr.mat, false, true))
		state, score := fr.classifier.ClassifyState(fr.mat)

		// One warm call per path, then the median of three: a classify is long
		// enough (tens to hundreds of ms) that scheduling noise matters.
		fr.classifier.ClassifyState(fr.mat)
		fr.classifier.EvaluateRules(fr.mat, false)
		var earlyRuns, fullRuns [3]time.Duration
		for i := range earlyRuns {
			start := time.Now()
			fr.classifier.ClassifyState(fr.mat)
			earlyRuns[i] = time.Since(start)

			start = time.Now()
			fr.classifier.EvaluateRules(fr.mat, false)
			fullRuns[i] = time.Since(start)
		}
		sort.Slice(earlyRuns[:], func(i, j int) bool { return earlyRuns[i] < earlyRuns[j] })
		sort.Slice(fullRuns[:], func(i, j int) bool { return fullRuns[i] < fullRuns[j] })
		earlyMs := float64(earlyRuns[1].Microseconds()) / 1000
		fullMs := float64(fullRuns[1].Microseconds()) / 1000

		b.Logf("%-30s %4dx%-4d %-16s(%4d) rules: %2d of %2d (skipped %2d)  early %6.1f ms  full %6.1f ms  (saved %5.1f ms)",
			filepath.Base(fr.file), fr.mat.Cols(), fr.mat.Rows(), state, score, early, full, full-early,
			earlyMs, fullMs, fullMs-earlyMs)
	}
}

type benchFrame struct {
	file       string
	mat        gocv.Mat
	classifier *Classifier
}

// benchCorpusFrames loads every fixture in the corpus at the geometry its
// display scale implies, skipping (not failing) when a checkout does not carry
// the corpus: the binaries are large and the benchmarks are a measuring tool,
// not a correctness gate.
func benchCorpusFrames(tb testing.TB) []benchFrame {
	tb.Helper()
	manifest := loadCorpusManifest(tb, filepath.Join("testdata", "corpus.json"))

	bySize := make(map[string]*Classifier, 2)
	var frames []benchFrame
	for _, fr := range manifest.Frames {
		path := filepath.Join("testdata", fr.File)
		if _, err := os.Stat(path); err != nil {
			continue
		}
		img := gocv.IMRead(path, gocv.IMReadColor)
		if img.Empty() {
			tb.Fatalf("cannot decode %s", path)
		}
		if !isCorpusResolution(img.Cols(), img.Rows()) {
			img.Close()
			tb.Fatalf("%s is %dx%d; the benchmarks only carry the geometries the probes are placed for",
				path, img.Cols(), img.Rows())
		}

		key := fmt.Sprintf("%dx%d", img.Cols(), img.Rows())
		c, ok := bySize[key]
		if !ok {
			scale := manifest.DisplayScale
			if img.Cols() == RefWidth && img.Rows() == RefHeight {
				scale = 0
			}
			c = newCorpusClassifier(tb, scale, img.Cols(), img.Rows())
			bySize[key] = c
		}
		frames = append(frames, benchFrame{file: path, mat: img, classifier: c})
	}

	if len(frames) == 0 {
		tb.Skipf("no corpus frames present under %s; restore internal/game/testdata/corpus/ to measure", "testdata/corpus")
	}
	tb.Cleanup(func() {
		for _, fr := range frames {
			if !fr.mat.Empty() {
				fr.mat.Close()
			}
		}
	})
	return frames
}
