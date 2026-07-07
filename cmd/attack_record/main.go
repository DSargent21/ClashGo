// cmd/attack_record is a MACRO RECORDER + REPLAYER for ClashGO.
//
// Record mode (-mode=record -out macro.json):
//
//   Captures the device screen into a GoCV window. Every left-click on
//   the window is BOTH forwarded to the device via adb shell input tap
//   AND appended to an in-memory tap list with a relative timestamp.
//   Press 's' to save the tap list to disk and quit, 'q' to discard.
//   The user drives the attack; the tool watches. ~200ms round-trip
//   per tap is normal (PC click → adb → emulator).
//
// Replay mode (-mode=replay -in macro.json):
//
//   Reads the JSON tap list and emits device taps at the recorded
//   cadence. Use relative timestamps (ms_since_start) so pauses,
//   pauses-to-zoom, etc. are preserved.
//
// Why this exists: lets the user "teach by demonstration". Play an
// attack once manually, save the macro, replay N times on N different
// bases. No code-level coordination, no YAML strategy authoring —
// record writes a flat time-ordered tap list, replay is purely
// procedural.
//
// Output schema (macro.json):
//   {
//     "meta": {"width": <int>, "height": <int>},    // device dim at record time
//     "taps": [{"t": <ms_since_start>, "x": <px>, "y": <px>}]
//   }
//
// Caveats (intentional, not bugs):
//   - Coordinates are device-pixel, recorded from whatever screen the
//     user recorded on. Macros recorded on 1280×720 are NOT portable
//     to 860×732. Same device + same resolution assumed.
//   - Single tap only. Drags/swipes for siege targeting are out of
//     scope for v1; siege with pre-set target = single tap works.
//   - The cursor position when the user clicks on the PC window is
//     what gets sent. Misses happen if the user clicks off-image.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"image"
	"image/color"
	"os"
	"runtime"
	"sync"
	"time"

	"github.com/Ducky705/ClashGO/internal/adb"
	"github.com/Ducky705/ClashGO/internal/logger"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"gocv.io/x/gocv"
)

// Tap is one recorded tap. T is millis since the record session started.
// X/Y are device-pixel coordinates (window-click coords go straight to adb).
// (No class field stored: replay re-derives via heuristic at runtime so
// old macros still get troop-aware behavior.)
type Tap struct {
	T int `json:"t"`
	X int `json:"x"`
	Y int `json:"y"`
}

// slotY is the troop-bar divider: taps with y > slotY are slot-bar clicks
// (unit selection), taps with y <= slotY are deploy-area clicks (drops).
// Picked from assets/precision_config.json (bar_y=632) with a 52-px fudge
// to catch taps slightly above the bar.
const slotY = 580

// classifyTap returns "slot-XYZ" if y > slotY (bar region), else "deploy".
// Slot subclasses are heuristic on x:
//   - 287..511         → slot-hero (the four-hero middle block)
//   - 62..250          → slot-troop (left)
//   - 560..620         → slot-troop (right-of-hero)
//   - 630..740         → slot-spell (rightmost)
//   - anything else    → slot-other (siege, clan castle, hero+offset, etc.)
//
// The exact ranges are tied to the standard 10-slot CoC bar geometry on
// 860×732 reference layout. Devices with a different bar position
// will misclassify some taps; replace with a LearnOnce-on-start flow
// later if that becomes a problem.
func classifyTap(x, y int) string {
	if y <= slotY {
		return "deploy"
	}
	switch {
	case x >= 287 && x <= 511:
		return "slot-hero"
	case x >= 62 && x <= 250:
		return "slot-troop"
	case x >= 560 && x <= 620:
		return "slot-troop"
	case x >= 630 && x <= 740:
		return "slot-spell"
	default:
		return "slot-other"
	}
}

// Macro is the on-disk JSON file format.
type Macro struct {
	Meta struct {
		Width  int `json:"width"`
		Height int `json:"height"`
	} `json:"meta"`
	Taps []Tap `json:"taps"`
}

func main() {
	// macOS Cocoa needs SetMouseHandler on the main OS thread.
	runtime.LockOSThread()
	logger.Init(os.Getenv("DEBUG") != "")
	log.Logger = log.Output(
		zerolog.ConsoleWriter{Out: os.Stderr, TimeFormat: "15:04:05"},
	).With().Timestamp().Logger()

	mode := flag.String("mode", "", "record | replay")
	device := flag.String("device", "", "ADB device ID (auto-detected if empty)")
	inPath := flag.String("in", "", "macro JSON for replay mode")
	outPath := flag.String("out", "", "macro JSON for record mode")
	extraTaps := flag.Int("extra-tap-count", 1, "extra taps per NON-hero (troop/spell) drop. Heroes always 0. Default 1 fixes BlueStacks single-tap drops.")
	extraDelay := flag.Int("extra-tap-delay", 50, "ms between extra taps (default 50).")
	dryRun := flag.Bool("dry-run", false, "replay mode only: classify taps and log them WITHOUT firing device taps. Use to preview behavior.")
	flag.Parse()

	if *mode != "record" && *mode != "replay" {
		log.Fatal().Msg("--mode must be 'record' or 'replay'")
	}

	client := adb.NewClient()
	if *device != "" {
		client.DeviceID = *device
	}
	if err := client.AutoDetectDevice(); err != nil {
		log.Fatal().Err(err).Msg("device auto-detect failed")
	}
	if err := client.Connect(); err != nil {
		log.Fatal().Err(err).Msg("connect failed")
	}
	defer client.Close()

	switch *mode {
	case "record":
		runRecord(client, *outPath)
	case "replay":
		runReplay(client, *inPath, *extraTaps, *extraDelay, *dryRun)
	}
}

// runRecord opens a live window on top of the device screen. Each
// left-click event is dispatched asynchronously as an adb tap and
// appended to the in-memory tap list. The window redraws with a
// growing trail of green pebbles so the user can see what they've
// queued. Save with 's', clear with 'c', quit without save with 'q'.
func runRecord(client *adb.Client, outPath string) {
	if outPath == "" {
		log.Fatal().Msg("--out required for record mode")
	}

	screen, err := client.CaptureToMat()
	if err != nil {
		log.Fatal().Err(err).Msg("capture failed")
	}
	defer screen.Close()
	w, h := screen.Cols(), screen.Rows()
	log.Info().Int("w", w).Int("h", h).Str("device", client.DeviceID).Msg("recording macro")

	overlay := screen.Clone()
	defer overlay.Close()
	gocv.Rectangle(&overlay, image.Rect(8, 8, 280, 38), color.RGBA{0, 0, 0, 220}, -1)
	gocv.PutText(&overlay,
		"RECORDING (0 taps)",
		image.Pt(18, 28), gocv.FontHersheySimplex, 0.5,
		color.RGBA{255, 80, 80, 255}, 1)

	win := gocv.NewWindow(fmt.Sprintf("RECORD MACRO (%dx%d) - click to record  s=save  c=clear  q=quit", w, h))
	defer win.Close()
	win.IMShow(overlay)

	var (
		mu    sync.Mutex
		taps  []Tap
		start = time.Now()
	)

	win.SetMouseHandler(func(event, x, y, flags int, userdata interface{}) {
		if event != 1 { // 1 == LButtonUp
			return
		}
		now := int(time.Since(start).Milliseconds())
		mu.Lock()
		taps = append(taps, Tap{T: now, X: x, Y: y})
		mu.Unlock()

		// Forward click to device in goroutine so UI doesn't stall on
		// adb round-trip (~30-200ms). Drop the error; adb's internal
		// TapEvent hook already logs each tap.
		go func() { _ = client.Tap(x, y) }()

		// Lightweight refresh: a new pebble. We don't redraw the full
		// frame each click (CaptureToMat is ~50ms); instead overlay
		// gets marker-only updates and the user accepts slight lag.
		gocv.Circle(&overlay, image.Pt(x, y), 6, color.RGBA{0, 255, 0, 220}, 2)
		gocv.PutText(&overlay, fmt.Sprintf("%d", len(taps)),
			image.Pt(x+8, y-5), gocv.FontHersheySimplex, 0.32,
			color.RGBA{255, 255, 255, 255}, 1)
		gocv.Rectangle(&overlay, image.Rect(8, 8, 280, 38), color.RGBA{0, 0, 0, 220}, -1)
		gocv.PutText(&overlay,
			fmt.Sprintf("RECORDING (%d taps)", len(taps)),
			image.Pt(18, 28), gocv.FontHersheySimplex, 0.5,
			color.RGBA{255, 80, 80, 255}, 1)
		win.IMShow(overlay)
	}, nil)

	log.Info().Msg("ready. click on the window to record+forward; press 's' to save, 'q' to quit")
	for {
		key := win.WaitKey(0)
		switch key {
		case 's', 'S':
			saveMacro(outPath, w, h, taps, &mu)
			return
		case 'c', 'C':
			mu.Lock()
			taps = nil
			start = time.Now()
			mu.Unlock()
			log.Info().Msg("cleared current recording; counter reset to 0")
		case 'q', 'Q', 27:
			log.Info().Msg("discarding recording without saving")
			return
		}
	}
}

// runReplay reads macro.json and emits device taps respecting the
// recorded relative timestamps. We re-record the device's current
// screen size in the meta header at save time, but emit device-pixel
// coords as recorded (no rescale).
//
// Troop/spell classification runs at replay time:
//
//   taps are classified by x/y on each iteration. A slot-tap updates
//   lastSelected ("hero"|"troop"|"spell"|"other"); subsequent deploy
//   taps inherit that suffix ("deploy-hero"/"deploy-troop"/...). When
//   the inherited class is NOT hero AND the suffix is a known troop/
//   spell/other (i.e. classifyTap ranged the slot tap confidently),
//   --extra-tap-count extra fires right after the primary tap, spaced
//   by --extra-tap-delay ms. This matches CoC's tolerance for repeated
//   deploy-position taps on the same spot (single tap sometimes fails
//   to register on BlueStacks).
//
// dryRun=true logs classification + extras planning but never fires a
// device tap. Use it via `make attack-classify` to preview behavior.
func runReplay(client *adb.Client, inPath string, extraTaps, extraDelay int, dryRun bool) {
	if inPath == "" {
		log.Fatal().Msg("--in required for replay mode")
	}
	raw, err := os.ReadFile(inPath)
	if err != nil {
		log.Fatal().Err(err).Str("path", inPath).Msg("read macro failed")
	}
	var m Macro
	if err := json.Unmarshal(raw, &m); err != nil {
		log.Fatal().Err(err).Str("path", inPath).Msg("parse macro failed")
	}
	log.Info().
		Int("taps", len(m.Taps)).
		Int("ref_w", m.Meta.Width).Int("ref_h", m.Meta.Height).
		Int("extra_taps", extraTaps).Int("extra_delay_ms", extraDelay).
		Bool("dry_run", dryRun).
		Str("device", client.DeviceID).
		Msg("replaying macro")

	start := time.Now()
	lastSelected := ""
	dispatch := func(x, y int) error {
		if dryRun {
			return nil
		}
		return client.TapFast(x, y, 2.0)
	}
	for i, t := range m.Taps {
		target := time.Duration(t.T) * time.Millisecond
		elapsed := time.Since(start)
		if target > elapsed {
			time.Sleep(target - elapsed)
		}
		class := classifyTap(t.X, t.Y)
		switch {
		case len(class) > 5 && class[:5] == "slot-":
			lastSelected = class[5:]
			log.Info().Int("seq", i+1).Str("class", class).Int("x", t.X).Int("y", t.Y).Msg("slot selection")
		case class == "deploy" && lastSelected != "":
			class = "deploy-" + lastSelected
		}
		// Heroes NEVER get extras. Extras only fire when deploy suffix
		// names a non-hero unit (troop/spell/other) so a misclassified
		// deploy (no suffix at all → "deploy") doesn't accidentally
		// extra-tap a stale selection.
		isHero := class == "slot-hero" || class == "deploy-hero"
		extras := 0
		switch {
		case isHero:
			extras = 0
		case class == "deploy-troop", class == "deploy-spell", class == "deploy-other":
			if extraTaps > 0 {
				extras = extraTaps
			}
		}
		if err := dispatch(t.X, t.Y); err != nil {
			log.Warn().Err(err).Int("seq", i+1).Msg("primary tap failed")
		}
		log.Info().Int("seq", i+1).Int("x", t.X).Int("y", t.Y).Int("t_ms", t.T).
			Str("class", class).Int("extras", extras).Bool("dry_run", dryRun).Msg("tap dispatched")
		for j := 0; j < extras; j++ {
			time.Sleep(time.Duration(extraDelay) * time.Millisecond)
			if dryRun {
				log.Info().Int("seq", i+1).Int("extra", j+1).Msg("extra tap (dry-run, no device fire)")
				continue
			}
			if err := client.TapFast(t.X, t.Y, 1.0); err != nil {
				log.Warn().Err(err).Int("seq", i+1).Int("extra", j+1).Msg("extra tap failed")
				continue
			}
			log.Info().Int("seq", i+1).Int("extra", j+1).Msg("extra tap")
		}
	}
	log.Info().Int("total", len(m.Taps)).Dur("elapsed", time.Since(start)).Msg("replay complete")
}

func saveMacro(path string, w, h int, taps []Tap, mu *sync.Mutex) {
	// Snapshot under lock so the mouse-handler goroutine can't append
	// while we're ranging over the slice. Same OS thread today, but
	// pinning the contract now means a future gocv version bump that
	// shifts SetMouseHandler onto a worker thread won't corrupt the file.
	if mu != nil {
		mu.Lock()
	}
	out := Macro{}
	out.Meta.Width = w
	out.Meta.Height = h
	out.Taps = append(out.Taps, taps...)
	if mu != nil {
		mu.Unlock()
	}
	raw, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		log.Fatal().Err(err).Msg("marshal failed")
	}
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		log.Fatal().Err(err).Msg("write failed")
	}
	log.Info().Str("path", path).Int("count", len(out.Taps)).
		Int("w", w).Int("h", h).Msg("macro saved")
}

