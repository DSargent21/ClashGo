//go:build livegate

package bot

// Live, read-only check of the inputs the runtime gates depend on. It fires no
// input at the device: it captures one frame, classifies it, reads the storage
// fill (the storage gate's verdict source) and runs the Apple Vision OCR path
// (the Personal Break detector's source).
//
// Run explicitly with the game sitting in the main village:
//
//	go test -tags livegate -run TestLiveGateInputs -v ./internal/bot \
//	  -ldflags="-extldflags=-Wl,-rpath,$(pkg-config --variable=libdir opencv4)"
//
// CLASHGO_LIVE_DEVICE overrides the ADB device (default: config.json's device).

import (
	"os"
	"testing"
	"time"

	"github.com/Ducky705/ClashGO/internal/adb"
	"github.com/Ducky705/ClashGO/internal/config"
	"github.com/Ducky705/ClashGO/internal/game"
	"github.com/Ducky705/ClashGO/internal/paths"
	"github.com/Ducky705/ClashGO/internal/vision"
	"github.com/rs/zerolog"
	"gocv.io/x/gocv"
)

func TestLiveGateInputs(t *testing.T) {
	cfg := config.LoadOrDefault("config.json")
	device := os.Getenv("CLASHGO_LIVE_DEVICE")
	if device == "" {
		device = cfg.Device.DeviceID
	}
	if device == "" {
		t.Skip("no ADB device configured; set CLASHGO_LIVE_DEVICE")
	}

	client := adb.NewClient(
		adb.WithHost(cfg.Device.ADBHost),
		adb.WithPort(cfg.Device.ADBPort),
		adb.WithTimeout(30*time.Second),
		adb.WithJitterTaps(false),
		adb.WithJitterDelays(false),
	)
	client.DeviceID = device
	defer client.Close()

	w, h, err := client.ScreenSize()
	if err != nil {
		t.Fatalf("screen size: %v", err)
	}
	cal := game.NewCalibration(w, h)
	cal.Verified = true
	if cfg.Device.DisplayScale > 0 {
		cal.SetDisplayScale(cfg.Device.DisplayScale)
	}
	t.Logf("device=%s screen=%dx%d display_scale=%.4f", device, w, h, cal.DisplayScale())

	screen, err := client.CaptureToMat()
	if err != nil || screen.Empty() {
		t.Fatalf("capture: %v", err)
	}
	defer screen.Close()

	classifier := game.NewClassifier(cal, game.DefaultClassifierConfig(), zerolog.Nop())
	tpl, terr := game.NewTemplateStore(paths.Resolve("templates"))
	if terr != nil {
		t.Logf("template store unavailable (%v); classifier still reports colour evidence", terr)
	} else {
		tpl.LoadTemplates()
		classifier.SetTemplates(tpl)
	}
	state, score := classifier.ClassifyState(screen)
	t.Logf("classifier: state=%s score=%d", state, score)

	// The storage gate's input only exists on a village frame. Percentages must
	// be real numbers inside 0..100; a stuck 0/0/0 would silently disable the
	// gate, and out-of-range values mean the ROI moved.
	if state == game.StateMainVillage {
		fill := game.ReadVillageStorageFillMat(screen, cal.DisplayScale())
		t.Logf("village storage fill: gold=%.1f%% elixir=%.1f%% de=%.1f%%",
			fill.GoldFillPercent, fill.ElixirFillPercent, fill.DEFillPercent)
		for name, pct := range map[string]float64{
			"gold":   fill.GoldFillPercent,
			"elixir": fill.ElixirFillPercent,
			"de":     fill.DEFillPercent,
		} {
			if pct < 0 || pct > 100 {
				t.Fatalf("%s fill %.2f%% is outside 0..100 — the storage gate would misread", name, pct)
			}
		}
	} else {
		t.Logf("device is on %s, not the village: storage-fill check not applicable on this frame", state)
	}

	// The Personal Break detector's input.
	path := t.TempDir() + "/gate.png"
	if !gocv.IMWrite(path, screen) {
		t.Fatal("could not write the frame for OCR")
	}
	regions, err := vision.OCRTextRegions(path)
	if err != nil {
		t.Fatalf("OCR: %v", err)
	}
	if len(regions) == 0 {
		t.Fatal("OCR returned no text regions on a live village frame — the Personal Break detector would never fire")
	}
	t.Logf("OCR: %d regions (first: %q)", len(regions), regions[0].Text)
}
