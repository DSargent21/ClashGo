// Command redprobe shows what the red no-deploy boundary detector sees: the
// binary mask it builds, every contour it weighed, and the verdict on each.
//
// It exists because the detector's hardest job is to say "there is no boundary
// in view" without lying, and the only way to check that claim is to look at the
// same mask and the same per-candidate verdicts the bot used. `attack_verify`
// renders the mask but reports only the single largest rejection, which is how a
// mask covering 19.6% of the playfield (terrain, roofs, lava walls) looked like a
// measurement rather than a false positive.
//
// Usage:
//
//	go run ./cmd/redprobe -img tmp/live/battle.png
//	go run ./cmd/redprobe -shot tmp/live/battle.png -mask-out tmp/live/mask.png -overlay-out tmp/live/overlay.png
//
// With no -img it captures the live device through internal/adb (the host adb
// binary answers "error: closed" on this BlueStacks build).
package main

import (
	"flag"
	"fmt"
	"image"
	"image/color"
	"os"
	"time"

	"github.com/Ducky705/ClashGO/internal/adb"
	"github.com/Ducky705/ClashGO/internal/attack"
	"github.com/rs/zerolog"
	"gocv.io/x/gocv"
)

func main() {
	imgPath := flag.String("img", "", "frame to analyze (default: live capture)")
	shot := flag.String("shot", "", "write the analyzed frame here (live capture only)")
	maskOut := flag.String("mask-out", "", "write the detector's binary mask here")
	overlayOut := flag.String("overlay-out", "", "write the mask tinted magenta over the frame here")
	traceOut := flag.String("trace-out", "", "write the candidate boxes, the accepted outline and the verdict here")
	uiCutoff := flag.Int("ui-cutoff", 0, "playfield bottom row (default: 85% of the frame height)")
	flag.Parse()

	frame, err := loadFrame(*imgPath, *shot)
	if err != nil {
		fmt.Fprintln(os.Stderr, "redprobe:", err)
		os.Exit(1)
	}
	defer frame.Close()

	cutoff := *uiCutoff
	if cutoff <= 0 {
		cutoff = int(float64(frame.Rows()) * 0.85)
	}

	logger := zerolog.New(zerolog.ConsoleWriter{Out: os.Stderr, TimeFormat: "15:04:05"}).
		With().Timestamp().Logger()
	detector := attack.NewRedLineDetector(logger)

	zone := detector.Detect(frame, cutoff)
	mask := detector.Mask(frame, cutoff)
	defer mask.Close()

	px := gocv.CountNonZero(mask)
	playfield := float64(frame.Cols() * cutoff)

	fmt.Printf("frame: %dx%d, playfield bottom row %d\n", frame.Cols(), frame.Rows(), cutoff)
	fmt.Printf("mask: %d px (%.1f%% of the playfield)\n", px, 100*float64(px)/playfield)
	if zone.Valid {
		fmt.Printf("verdict: ACCEPTED %v (contours %d, outline points %d)\n",
			zone.BBox, zone.Contours, len(zone.Polygon))
	} else {
		fmt.Printf("verdict: no boundary in view\n")
	}

	cands := detector.Candidates(frame, cutoff)
	fmt.Printf("candidates (largest first): %d\n", len(cands))
	for i, c := range cands {
		status := "rejected: " + c.Reason
		if c.Accepted {
			status = "ACCEPTED"
		}
		fmt.Printf("  %d. %v fill %.2f enclosed %.0f px — %s\n", i+1, c.Rect, c.Fill, c.Enclosed, status)
	}

	if *maskOut != "" && !gocv.IMWrite(*maskOut, mask) {
		fmt.Fprintln(os.Stderr, "redprobe: could not write", *maskOut)
	}
	if *overlayOut != "" {
		overlay := tintMask(frame, mask)
		defer overlay.Close()
		if !gocv.IMWrite(*overlayOut, overlay) {
			fmt.Fprintln(os.Stderr, "redprobe: could not write", *overlayOut)
		}
	}
	if *traceOut != "" {
		traced := traceZone(frame, zone, cands)
		defer traced.Close()
		if !gocv.IMWrite(*traceOut, traced) {
			fmt.Fprintln(os.Stderr, "redprobe: could not write", *traceOut)
		}
	}
}

// loadFrame reads a saved frame, or captures the live device when none was given.
func loadFrame(imgPath, shot string) (gocv.Mat, error) {
	if imgPath != "" {
		frame := gocv.IMRead(imgPath, gocv.IMReadColor)
		if frame.Empty() {
			return frame, fmt.Errorf("could not read %s", imgPath)
		}
		return frame, nil
	}

	client := adb.NewClient(
		adb.WithHost("127.0.0.1"),
		adb.WithPort(5037),
		adb.WithTimeout(30*time.Second),
	)
	client.DeviceID = "localhost:5555"
	frame, err := client.CaptureToMat()
	if err != nil {
		return frame, fmt.Errorf("live capture: %w", err)
	}
	if shot != "" && !gocv.IMWrite(shot, frame) {
		return frame, fmt.Errorf("could not write %s", shot)
	}
	return frame, nil
}

// tintMask paints the mask magenta over the frame so a human can see which
// pixels the detector is calling red.
func tintMask(frame, mask gocv.Mat) gocv.Mat {
	out := gocv.NewMat()
	frame.CopyTo(&out)

	tint := gocv.NewMatWithSize(frame.Rows(), frame.Cols(), frame.Type())
	defer tint.Close()
	tint.SetTo(gocv.NewScalar(255, 0, 255, 0))

	colored := gocv.NewMat()
	defer colored.Close()
	tint.CopyToWithMask(&colored, mask)
	if !colored.Empty() {
		gocv.AddWeighted(out, 0.6, colored, 0.4, 0, &out)
	}
	return out
}

// traceZone draws the detector's actual decision on the frame: every candidate
// it weighed as an orange box labelled with its fill ratio, and the accepted
// boundary as a green outline. A box alone cannot answer "is this tap inside the
// no-deploy area", so the accepted case also traces the polygon — the same shape
// RedZone.Inside uses.
func traceZone(frame gocv.Mat, zone attack.RedZone, cands []attack.RedCandidate) gocv.Mat {
	out := gocv.NewMat()
	frame.CopyTo(&out)

	green := color.RGBA{R: 60, G: 220, B: 60, A: 255}
	orange := color.RGBA{R: 0, G: 150, B: 255, A: 255}
	white := color.RGBA{R: 255, G: 255, B: 255, A: 255}

	for _, c := range cands {
		if c.Accepted {
			continue
		}
		gocv.Rectangle(&out, c.Rect, orange, 1)
		label := fmt.Sprintf("fill %.2f", c.Fill)
		gocv.PutText(&out, label, image.Pt(c.Rect.Min.X+3, c.Rect.Min.Y+14),
			gocv.FontHersheySimplex, 0.45, orange, 1)
	}

	// A dark plate behind the verdict: the header sits on the game's own HUD
	// text, and white-on-light-grey is unreadable in a review image.
	plate := color.RGBA{R: 20, G: 20, B: 20, A: 255}
	gocv.Rectangle(&out, image.Rect(0, 0, 560, 38), plate, -1)

	if zone.Valid {
		gocv.Rectangle(&out, zone.BBox, green, 2)
		for i := 0; i < len(zone.Polygon); i++ {
			gocv.Line(&out, zone.Polygon[i], zone.Polygon[(i+1)%len(zone.Polygon)], green, 2)
		}
		gocv.PutText(&out, fmt.Sprintf("BOUNDARY %v  conf %.2f  outline %d pts", zone.BBox, zone.Confidence, len(zone.Polygon)),
			image.Pt(8, 26), gocv.FontHersheySimplex, 0.6, green, 2)
		return out
	}

	gocv.PutText(&out, "NO BOUNDARY IN VIEW (nothing plausible passed)",
		image.Pt(8, 26), gocv.FontHersheySimplex, 0.6, white, 2)
	return out
}
