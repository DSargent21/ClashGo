// Command tplprobe locates one stored template on a frame across a scale sweep
// and prints the ranked result.
//
// It answers the question the geometry work keeps running into: "is this button
// where the mapping says it is, and at what scale is it drawn?" The templates in
// assets/templates were captured at the 860x732 reference geometry, so on any
// other geometry the button is rendered at the display scale k — and the tap
// points in internal/bot's villagePinpoints are a *prediction* that can be wrong
// by a whole widget. Pointing this at a live frame turns that prediction into a
// measurement.
//
// Usage:
//
//	go run ./cmd/tplprobe -name btn_find_match                       # live frame, coarse sweep
//	go run ./cmd/tplprobe -img shot.png -name btn_find_match -min 1.2 -max 1.45 -steps 6
//	go run ./cmd/tplprobe -name btn_battle -roi 900,380,1280,560      # limit the search window
package main

import (
	"flag"
	"fmt"
	"image"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"gocv.io/x/gocv"

	"github.com/Ducky705/ClashGO/internal/adb"
	"github.com/Ducky705/ClashGO/internal/vision"
)

func main() {
	imgPath := flag.String("img", "", "frame to search (default: live adb capture)")
	name := flag.String("name", "", "template name, with or without .png (required)")
	dir := flag.String("dir", "assets/templates", "template directory")
	minScale := flag.Float64("min", 0.2, "lowest scale in the sweep")
	maxScale := flag.Float64("max", 2.0, "highest scale in the sweep")
	steps := flag.Int("steps", 5, "number of scales to try")
	thresh := flag.Float64("thresh", 0.45, "minimum confidence to report")
	roiFlag := flag.String("roi", "", "search window as x1,y1,x2,y2 (default: whole frame)")
	device := flag.String("device", "localhost:5555", "ADB device ID for the live capture")
	flag.Parse()

	if *name == "" {
		fmt.Println("-name is required")
		flag.Usage()
		os.Exit(2)
	}

	screen, closeFn, err := loadFrame(*imgPath, *device)
	if err != nil {
		fmt.Fprintf(os.Stderr, "frame: %v\n", err)
		os.Exit(1)
	}
	defer closeFn()
	fmt.Printf("frame %dx%d\n", screen.Cols(), screen.Rows())

	tpl, err := loadTemplate(*dir, *name)
	if err != nil {
		fmt.Fprintf(os.Stderr, "template: %v\n", err)
		os.Exit(1)
	}
	defer tpl.Close()
	fmt.Printf("template %s %dx%d\n", *name, tpl.Cols(), tpl.Rows())

	roi := image.Rect(0, 0, screen.Cols(), screen.Rows())
	if *roiFlag != "" {
		r, err := parseRect(*roiFlag)
		if err != nil {
			fmt.Fprintf(os.Stderr, "roi: %v\n", err)
			os.Exit(1)
		}
		roi = r
	}
	fmt.Printf("roi %v  sweep %.2f..%.2f x%d  thresh %.2f\n", roi, *minScale, *maxScale, *steps, *thresh)

	start := time.Now()
	matches, err := vision.MatchMultiScaleROICached(screen, tpl, *name, *minScale, *maxScale, *steps, float32(*thresh), roi)
	if err != nil {
		fmt.Fprintf(os.Stderr, "match: %v\n", err)
		os.Exit(1)
	}
	if len(matches) == 0 {
		fmt.Printf("no match >= %.2f (%.0fms)\n", *thresh, float64(time.Since(start).Microseconds())/1000)
		os.Exit(1)
	}
	for i, m := range matches {
		w := int(float64(tpl.Cols()) * m.Scale)
		h := int(float64(tpl.Rows()) * m.Scale)
		fmt.Printf("  #%d conf=%.4f centre=(%d,%d) scale=%.3f size=%dx%d\n", i+1, m.Confidence, m.Point.X, m.Point.Y, m.Scale, w, h)
	}
	fmt.Printf("(%.0fms)\n", float64(time.Since(start).Microseconds())/1000)
}

func loadFrame(imgPath, device string) (gocv.Mat, func(), error) {
	if imgPath != "" {
		m := gocv.IMRead(imgPath, gocv.IMReadColor)
		if m.Empty() {
			return gocv.NewMat(), func() {}, fmt.Errorf("cannot read %s", imgPath)
		}
		return m, func() { m.Close() }, nil
	}
	client := adb.NewClient(
		adb.WithHost("127.0.0.1"),
		adb.WithPort(5037),
		adb.WithTimeout(30*time.Second),
	)
	client.DeviceID = device
	buf, err := client.CaptureScreen()
	if err != nil {
		client.Close()
		return gocv.NewMat(), func() {}, err
	}
	mat, err := decodeScreencap(buf)
	if err != nil {
		client.Close()
		return gocv.NewMat(), func() {}, err
	}
	return mat, func() { mat.Close(); client.Close() }, nil
}

// decodeScreencap turns a raw (non-PNG) screencap payload — 12-byte header of
// width, height, format followed by 4-byte pixels — into a BGR Mat.
func decodeScreencap(buf []byte) (gocv.Mat, error) {
	if len(buf) < 12 {
		return gocv.NewMat(), fmt.Errorf("short screencap payload (%d bytes)", len(buf))
	}
	w := int(uint32(buf[0]) | uint32(buf[1])<<8 | uint32(buf[2])<<16 | uint32(buf[3])<<24)
	h := int(uint32(buf[4]) | uint32(buf[5])<<8 | uint32(buf[6])<<16 | uint32(buf[7])<<24)
	if w <= 0 || h <= 0 || len(buf) < 12+w*h*4 {
		return gocv.NewMat(), fmt.Errorf("bad screencap header %dx%d for %d bytes", w, h, len(buf))
	}
	// The raw buffer is RGBA; wrap it as a 4-channel Mat then drop alpha.
	raw := make([]byte, w*h*4)
	copy(raw, buf[12:12+w*h*4])
	rgba, err := gocv.NewMatFromBytes(h, w, gocv.MatTypeCV8UC4, raw)
	if err != nil {
		return gocv.NewMat(), err
	}
	defer rgba.Close()
	out := gocv.NewMat()
	gocv.CvtColor(rgba, &out, gocv.ColorRGBAToBGR)
	return out, nil
}

func loadTemplate(dir, name string) (gocv.Mat, error) {
	if !strings.HasSuffix(name, ".png") {
		name += ".png"
	}
	path := filepath.Join(dir, name)
	m := gocv.IMRead(path, gocv.IMReadColor)
	if m.Empty() {
		return gocv.NewMat(), fmt.Errorf("cannot read %s", path)
	}
	return m, nil
}

func parseRect(s string) (image.Rectangle, error) {
	parts := strings.Split(s, ",")
	if len(parts) != 4 {
		return image.Rectangle{}, fmt.Errorf("want x1,y1,x2,y2, got %q", s)
	}
	var v [4]int
	for i, p := range parts {
		n, err := strconv.Atoi(strings.TrimSpace(p))
		if err != nil {
			return image.Rectangle{}, fmt.Errorf("bad number %q: %w", p, err)
		}
		v[i] = n
	}
	return image.Rect(v[0], v[1], v[2], v[3]), nil
}
