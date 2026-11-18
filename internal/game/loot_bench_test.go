package game

import (
	"image"
	"path/filepath"
	"testing"

	"github.com/rs/zerolog"
	"gocv.io/x/gocv"
)

// The damage path's two costs are not the same shape: building a recogniser
// clones and canonicalises ten digit templates, while a read walks two loot
// columns and the star band. They are benchmarked separately so a change that
// only helps one cannot hide behind the other.
func BenchmarkNewLootRecognizer(b *testing.B) {
	base := newTestLootRecognizer(b)
	cal, ts, logger := base.cal, base.templates, zerolog.Nop()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		lr := NewLootRecognizer(cal, ts, logger)
		lr.Close()
	}
}

// BenchmarkLoadTemplateStore is the cost the battle wait pays when it builds
// its own store just to read a destruction percentage. Separate from
// NewLootRecognizer so the two costs are not conflated.
func BenchmarkLoadTemplateStore(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		ts, err := NewTemplateStore("../../assets/templates")
		if err != nil {
			b.Fatal(err)
		}
		if err := ts.LoadTemplates(); err != nil {
			ts.Close()
			b.Fatal(err)
		}
		ts.Close()
	}
}

// BenchmarkReplayFrameLoop is the offline stand-in for the capture loop: the
// committed corpus frames stand in for captures, each pass runs the real
// classifier, and the result frame also runs the damage parser. It is not a
// live measurement; it exists so a sustained multi-hundred-frame run can be
// sampled for RSS and CPU on a machine with no emulator.
func BenchmarkReplayFrameLoop(b *testing.B) {
	frames := benchCorpusFrames(b)
	var resultFrame *benchFrame
	for i := range frames {
		if filepath.Base(frames[i].file) == "result_overlay_720p.png" {
			resultFrame = &frames[i]
			break
		}
	}
	if resultFrame == nil {
		b.Skip("result_overlay_720p.png is missing")
	}
	loot := NewLootRecognizer(resultFrame.classifier.cal, resultFrame.classifier.templates, zerolog.Nop())
	defer loot.Close()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		fr := frames[i%len(frames)]
		fr.classifier.ClassifyState(fr.mat)
		// A result panel is parsed once per battle, not on every frame; every
		// 25th replayed frame stands in for that end-of-battle read.
		if i%25 == 0 {
			if _, err := loot.ReadBattleResult(resultFrame.mat); err != nil {
				b.Fatal(err)
			}
		}
	}
}

func BenchmarkReadBattleResult(b *testing.B) {
	lr := newTestLootRecognizer(b)
	img := gocv.IMRead("testdata/screen_victory.png", gocv.IMReadColor)
	if img.Empty() {
		b.Skip("testdata/screen_victory.png is missing")
	}
	defer img.Close()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := lr.ReadBattleResult(img); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkReadDestructionPercentage(b *testing.B) {
	img := gocv.IMRead("testdata/corpus/battle_mid_720p.png", gocv.IMReadColor)
	if img.Empty() {
		b.Skip("testdata/corpus/battle_mid_720p.png is missing")
	}
	defer img.Close()

	cal := &Calibration{PhysicalW: 1280, PhysicalH: 720, ScaleX: 1.325, ScaleY: 1.325}
	ts, err := NewTemplateStore("../../assets/templates")
	if err != nil {
		b.Fatal(err)
	}
	if err := ts.LoadTemplates(); err != nil {
		b.Fatal(err)
	}
	defer ts.Close()
	lr := NewLootRecognizer(cal, ts, zerolog.Nop())
	defer lr.Close()

	// The live stall ROI at 720p (docs: it was mapped 50 px low by the old
	// per-axis formula; HudRect puts it on the digits).
	roi := image.Rect(1166, 525, 1239, 557)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = lr.ReadDestructionPercentage(img, roi)
	}
}

// BenchmarkResultSettleLoopRebuilt replays the shape the bot used to use on
// the result panel: several reads while the panel animates, each constructing
// its own recogniser. Kept beside the reused version so the reason for the
// change stays measurable.
func BenchmarkResultSettleLoopRebuilt(b *testing.B) {
	base := newTestLootRecognizer(b)
	img := gocv.IMRead("testdata/screen_victory.png", gocv.IMReadColor)
	if img.Empty() {
		b.Skip("testdata/screen_victory.png is missing")
	}
	defer img.Close()
	cal, ts, logger := base.cal, base.templates, zerolog.Nop()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for attempt := 0; attempt < 5; attempt++ {
			lr := NewLootRecognizer(cal, ts, logger)
			if _, err := lr.ReadBattleResult(img); err != nil {
				lr.Close()
				b.Fatal(err)
			}
			lr.Close()
		}
	}
}

// BenchmarkResultSettleLoopReused is the same five-read settle window with one
// recogniser, which is what the bot now does.
func BenchmarkResultSettleLoopReused(b *testing.B) {
	lr := newTestLootRecognizer(b)
	img := gocv.IMRead("testdata/screen_victory.png", gocv.IMReadColor)
	if img.Empty() {
		b.Skip("testdata/screen_victory.png is missing")
	}
	defer img.Close()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for attempt := 0; attempt < 5; attempt++ {
			if _, err := lr.ReadBattleResult(img); err != nil {
				b.Fatal(err)
			}
		}
	}
}
