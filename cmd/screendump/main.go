// Command screendump is the bot's "eyes on the emulator" observability
// tool. It captures the live screen (or reads a saved PNG), runs the
// SAME classifier the bot uses, renders a color-mapped ASCII view of
// the frame, probes key button pixels, and optionally invokes the
// Apple-Vision OCR helper (tools/ocr.swift) to read on-screen text.
//
// Usage:
//   go run ./cmd/screendump                    # live capture + classify + ascii
//   go run ./cmd/screendump -img /tmp/x.png    # analyze a saved frame
//   go run ./cmd/screendump -ocr               # also run Vision OCR
//   go run ./cmd/screendump -watch -ocr        # live loop, refresh every 3s
//   go run ./cmd/screendump -tap 64,666 -ocr   # tap, settle, then look
//   go run ./cmd/screendump -pinch out -ocr    # zoom out, settle, then look
//
// The whole point is text: a text-only agent (or a terminal user) can
// "see" what the emulator shows — state, layout, colors, and words.
//
// Live capture, -tap and -pinch go through internal/adb, the same transport
// the bot uses, rather than shelling out to the host adb binary. On BlueStacks
// the WindowManager wedges (host `adb shell` answers "error: closed") and a
// second attached device makes an unfiltered `adb exec-out` fail outright, so
// the host binary is exactly the wrong way to look at the screen.

package main

import (
	"flag"
	"fmt"
	"math"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/Ducky705/ClashGO/internal/adb"
	"github.com/Ducky705/ClashGO/internal/config"
	"github.com/Ducky705/ClashGO/internal/game"
	"github.com/Ducky705/ClashGO/internal/paths"
	"github.com/Ducky705/ClashGO/internal/vision"
	"github.com/rs/zerolog"
	"gocv.io/x/gocv"
)

func main() {
	imgPath := flag.String("img", "", "screenshot to analyze (default: live adb capture)")
	doOCR := flag.Bool("ocr", false, "run Apple Vision OCR on the frame")
	watch := flag.Bool("watch", false, "live loop: refresh every 3s until Ctrl-C")
	save := flag.String("save", "", "copy the analyzed frame to this path")
	dumpAnchors := flag.Bool("anchors", false, "dump every rule anchor under both geometry models (ref -> live coords + sampled RGB)")
	buttons := flag.Bool("buttons", false, "locate the primary action button in the frame (vision.FindActionButton)")
	buttonsFrac := flag.String("buttons-frac", "0.5,0.5,1.0,1.0", "-buttons search window as fx0,fy0,fx1,fy1 fractions of the frame")
	k := flag.Float64("k", 0, "override the display scale (0 = the scale the bot pins in device.display_scale, as the bot runs it); see docs/RESOLUTION.md for how to measure it")
	derive := flag.Bool("derive", false, "use the scale derived from the screen diagonal instead of the pinned one — the pre-pin behaviour, and what reproduces the derived-vs-pinned contrast in docs/RESOLUTION.md")
	tap := flag.String("tap", "", "tap X,Y on the live device before capturing (drives the game by hand without the host adb binary)")
	swipe := flag.String("swipe", "", "swipe X1,Y1,X2,Y2 on the live device before capturing (scrolls lists by hand)")
	speed := flag.Int("swipe-ms", 400, "duration of the -swipe gesture")
	pinch := flag.String("pinch", "", "run the bot's native pinch gesture (out|in) before capturing")
	settle := flag.Duration("settle", 1500*time.Millisecond, "wait after -tap/-pinch before capturing")
	device := flag.String("device", "localhost:5555", "ADB device used for live capture and -tap")
	flag.Parse()

	// This is the measurement instrument, but its classifier verdict still has
	// to describe the geometry the bot runs: the derived diagonal ratio is 2%
	// off the pinned value at 1280x720 (1.3004 against 1.325), which is the
	// difference between Battle and Unknown on real frames — see
	// internal/game/testdata/corpus.json. -derive restores the old derivation
	// for the comparison in docs/RESOLUTION.md.
	displayScale, scaleSource, departsFromBot := config.ResolveDisplayScale(*k)
	if *derive {
		displayScale = 0
		if pinned, _ := config.PinnedDisplayScale(); pinned > 0 {
			scaleSource = fmt.Sprintf("-derive: derived from the screen diagonal; the bot runs the pinned %.4f", pinned)
			departsFromBot = true
		} else {
			scaleSource = "-derive: derived from the screen diagonal, as the bot does too (nothing is pinned)"
			departsFromBot = false
		}
	}
	if departsFromBot {
		fmt.Printf("WARNING: the bot does not run this geometry — %s\n", scaleSource)
	}

	tapX, tapY, doTap, err := parseTap(*tap)
	if err != nil {
		fmt.Fprintf(os.Stderr, "-tap: %v\n", err)
		os.Exit(2)
	}

	swipeX1, swipeY1, swipeX2, swipeY2, doSwipe, err := parseSwipe(*swipe)
	if err != nil {
		fmt.Fprintf(os.Stderr, "-swipe: %v\n", err)
		os.Exit(2)
	}

	doPinch, err := parsePinch(*pinch)
	if err != nil {
		fmt.Fprintf(os.Stderr, "-pinch: %v\n", err)
		os.Exit(2)
	}

	var client *adb.Client
	if *imgPath == "" || doTap || doSwipe || doPinch != "" {
		client = adb.NewClient(
			adb.WithHost("127.0.0.1"),
			adb.WithPort(5037),
			adb.WithTimeout(30*time.Second),
		)
		client.DeviceID = *device
		defer client.Close()
	}

	// Scrolling a list by hand (e.g. the saved-recipe cards) needs the same
	// transport as -tap: the host adb binary's `input swipe` fails on BlueStacks.
	if doSwipe {
		if err := client.Swipe(swipeX1, swipeY1, swipeX2, swipeY2, *speed); err != nil {
			fmt.Fprintf(os.Stderr, "swipe %d,%d -> %d,%d: %v\n", swipeX1, swipeY1, swipeX2, swipeY2, err)
			os.Exit(1)
		}
		fmt.Printf("swiped %d,%d -> %d,%d (%dms); settling %s\n", swipeX1, swipeY1, swipeX2, swipeY2, *speed, *settle)
		time.Sleep(*settle)
	}

	// Drives the gesture the bot itself performs before an attack (the mandatory
	// zoom out), so a hand-run can reproduce the screens and side effects that
	// gesture produces — see docs/RESOLUTION.md.
	if doPinch != "" {
		switch doPinch {
		case "out":
			err = client.ZoomOut()
		case "in":
			err = client.ZoomIn()
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "pinch %s failed: %v\n", doPinch, err)
		} else {
			fmt.Printf("pinched %s; settling %s\n", doPinch, *settle)
		}
		time.Sleep(*settle)
	}

	for {
		runOnce(client, *imgPath, *doOCR, *save, *dumpAnchors, *buttons, *buttonsFrac, displayScale, scaleSource, doTap, tapX, tapY, *settle)
		if !*watch {
			return
		}
		time.Sleep(3 * time.Second)
	}
}

// parseFrac reads a "fx0,fy0,fx1,fy1" fraction window, falling back to the
// lower-right half so a typo degrades to the documented default instead of
// silently searching nothing.
func parseFrac(s string) (float64, float64, float64, float64) {
	const (
		defX0, defY0, defX1, defY1 = 0.5, 0.5, 1.0, 1.0
	)
	parts := strings.Split(s, ",")
	if len(parts) != 4 {
		return defX0, defY0, defX1, defY1
	}
	var v [4]float64
	for i, p := range parts {
		f, err := strconv.ParseFloat(strings.TrimSpace(p), 64)
		if err != nil {
			return defX0, defY0, defX1, defY1
		}
		v[i] = f
	}
	return v[0], v[1], v[2], v[3]
}

// parsePinch validates the -pinch flag. An empty flag means "do not pinch",
// which is distinct from a malformed one for the same reason as -tap: a typo
// must not silently degrade a scripted gesture-and-look into a plain look.
func parsePinch(s string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "":
		return "", nil
	case "out", "in":
		return strings.ToLower(strings.TrimSpace(s)), nil
	default:
		return "", fmt.Errorf("want out|in (got %q)", s)
	}
}

// parseSwipe splits the -swipe flag into X1,Y1,X2,Y2. An empty flag means "do
// not swipe"; a typo must fail loudly rather than degrade into a plain look,
// for the same reason as -tap.
func parseSwipe(s string) (int, int, int, int, bool, error) {
	if strings.TrimSpace(s) == "" {
		return 0, 0, 0, 0, false, nil
	}
	parts := strings.Split(s, ",")
	if len(parts) != 4 {
		return 0, 0, 0, 0, false, fmt.Errorf("want X1,Y1,X2,Y2 (got %q)", s)
	}
	var v [4]int
	for i, p := range parts {
		n, err := strconv.Atoi(strings.TrimSpace(p))
		if err != nil {
			return 0, 0, 0, 0, false, fmt.Errorf("bad coordinate in %q", s)
		}
		v[i] = n
	}
	return v[0], v[1], v[2], v[3], true, nil
}

// parseTap splits the -tap flag into coordinates. An empty flag means "do not
// tap", which is distinct from a malformed one: a typo must not silently turn a
// scripted tap-and-look into a plain look.
func parseTap(s string) (int, int, bool, error) {
	if strings.TrimSpace(s) == "" {
		return 0, 0, false, nil
	}
	parts := strings.Split(s, ",")
	if len(parts) != 2 {
		return 0, 0, false, fmt.Errorf("want X,Y (got %q)", s)
	}
	x, err := strconv.Atoi(strings.TrimSpace(parts[0]))
	if err != nil {
		return 0, 0, false, fmt.Errorf("bad X in %q", s)
	}
	y, err := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err != nil {
		return 0, 0, false, fmt.Errorf("bad Y in %q", s)
	}
	return x, y, true, nil
}

func runOnce(client *adb.Client, imgPath string, doOCR bool, savePath string, dumpAnchors, buttons bool, buttonsFrac string, displayScale float64, scaleSource string, doTap bool, tapX, tapY int, settle time.Duration) {
	img := gocv.Mat{}
	if imgPath != "" {
		img = gocv.IMRead(imgPath, gocv.IMReadColor)
		if img.Empty() {
			fmt.Fprintf(os.Stderr, "cannot read %s\n", imgPath)
			os.Exit(1)
		}
	} else {
		if doTap {
			if err := client.Tap(tapX, tapY); err != nil {
				fmt.Fprintf(os.Stderr, "tap %d,%d: %v\n", tapX, tapY, err)
				os.Exit(1)
			}
			fmt.Printf("tapped %d,%d; settling %s\n", tapX, tapY, settle)
			time.Sleep(settle)
		}
		var err error
		img, err = client.CaptureToMat()
		if err != nil {
			fmt.Fprintf(os.Stderr, "live capture: %v\n", err)
			os.Exit(1)
		}
		if img.Empty() {
			fmt.Fprintln(os.Stderr, "empty capture")
			os.Exit(1)
		}
	}
	defer img.Close()

	fmt.Printf("\n=== SCREEN %s (%dx%d) ===\n", time.Now().Format("15:04:05"), img.Cols(), img.Rows())
	if savePath != "" {
		gocv.IMWrite(savePath, img)
		fmt.Printf("saved frame -> %s\n", savePath)
	}

	cal := game.NewCalibration(img.Cols(), img.Rows())
	cal.Verified = true
	if displayScale > 0 {
		cal.SetDisplayScale(displayScale)
	}
	// Printed after the frame is read, because the effective scale is only
	// known once the screen size is: with nothing pinned, the calibration
	// derives it, and that derived value is what -derive exposes.
	fmt.Printf("display scale k=%.4f (%s)\n", cal.DisplayScale(), scaleSource)

	logger := zerolog.New(zerolog.ConsoleWriter{Out: os.Stderr, NoColor: true}).Level(zerolog.ErrorLevel)
	classifier := game.NewClassifier(cal, game.DefaultClassifierConfig(), logger)
	ts, err := game.NewTemplateStore(paths.Resolve("templates"))
	if err == nil {
		ts.LoadTemplates()
		classifier.SetTemplates(ts)
		defer ts.Close()
	}

	state, score := classifier.ClassifyState(img)
	fmt.Printf("CLASSIFIER: state=%s score=%d\n", state.String(), score)

	fmt.Println("--- state rules (pixel passes) ---")
	for _, rule := range classifier.GetRules() {
		passed := 0
		for _, chk := range rule.Checks {
			// Mirror ClassifyState exactly: reference coords mapped through
			// the rule's anchor, colour distance as a vector length.
			sx, sy := cal.MapX(chk.X, rule.Anchor), cal.MapY(chk.Y, rule.Anchor)
			if sx < 0 || sy < 0 || sx >= img.Cols() || sy >= img.Rows() {
				continue
			}
			b := img.GetUCharAt(sy, sx*3)
			g := img.GetUCharAt(sy, sx*3+1)
			r := img.GetUCharAt(sy, sx*3+2)
			if colorDistance(int(r), int(g), int(b), chk) <= float64(chk.Tolerance) {
				passed++
			}
		}
		marker := " "
		if rule.MinPass > 0 && passed >= rule.MinPass {
			marker = "*"
		}
		if rule.Template != "" {
			marker += "T"
		}
		anchor := anchorName(rule.Anchor)
		fmt.Printf("  %s %-18s pass=%d/%d minpass=%d (%s)\n", marker, rule.State.String(), passed, len(rule.Checks), rule.MinPass, anchor)
	}

	if buttons {
		// The default window is the lower-right half: that is where the army
		// sheet's action button sits at both measured geometries (ref
		// (727,536), 720p (1130,641)) even though the panel around it reflows.
		fx0, fy0, fx1, fy1 := parseFrac(buttonsFrac)
		region := vision.FractionRect(img.Cols(), img.Rows(), fx0, fy0, fx1, fy1)
		fmt.Printf("--- action button (region %v) ---\n", region)
		if got, ok := vision.FindActionButton(img, region, vision.DefaultActionButtonConfig()); ok {
			fmt.Printf("  %s\n", got.Describe())
		} else {
			fmt.Println("  none")
		}
	}

	if dumpAnchors {
		fmt.Println("--- anchors (ref -> live, both models) ---")
		for _, rule := range classifier.GetRules() {
			if len(rule.Checks) == 0 {
				continue
			}
			fmt.Printf("  %-18s anchor=%s\n", rule.State.String(), anchorName(rule.Anchor))
			for _, chk := range rule.Checks {
				// hud = the rule's widget anchoring, per axis (what ClassifyState
				// uses for a HUD rule); centre = the centred-overlay model. For an
				// overlay rule the two agree, so the hud column is the informative
				// one when they differ.
				hx, hy := cal.Hud(chk.X, chk.Y)
				cx, cy := cal.Centre(chk.X, chk.Y)
				fmt.Printf("    ref(%3d,%3d) want RGB(%3d,%3d,%3d)±%-3d hud(%4d,%4d)%s centre(%4d,%4d)%s\n",
					chk.X, chk.Y, chk.R, chk.G, chk.B, chk.Tolerance,
					hx, hy, sampleNote(img, hx, hy, chk), cx, cy, sampleNote(img, cx, cy, chk))
			}
		}
	}

	fmt.Println("--- color map (G=green O=orange R=red B=blue P=purple Y=yellow W=white .=dim #=bright) ---")
	renderColorMap(img)

	fmt.Println("--- key button pixels (ref coords -> live RGB per model) ---")
	probes := []struct {
		name   string
		x, y   int
		anchor game.Anchor
	}{
		{"attack", 64, 666, game.AnchorEdge},
		{"find_match", 158, 494, game.AnchorEdge},
		{"battle", 731, 537, game.AnchorEdge},
		{"army_arrow", 514, 192, game.AnchorCenter},
		{"army_1", 513, 230, game.AnchorCenter},
		{"next", 794, 577, game.AnchorEdge},
		{"return_home", 431, 581, game.AnchorCenter},
		{"okay", 430, 520, game.AnchorCenter},
		{"gold_icon", 35, 85, game.AnchorEdge},
		{"elixir_icon", 35, 115, game.AnchorEdge},
		{"de_icon", 35, 145, game.AnchorEdge},
	}
	for _, p := range probes {
		sx, sy := cal.MapX(p.x, p.anchor), cal.MapY(p.y, p.anchor)
		if sx < 0 || sy < 0 || sx >= img.Cols() || sy >= img.Rows() {
			fmt.Printf("  %-20s out of bounds\n", p.name)
			continue
		}
		b := img.GetUCharAt(sy, sx*3)
		g := img.GetUCharAt(sy, sx*3+1)
		r := img.GetUCharAt(sy, sx*3+2)
		fmt.Printf("  %-14s ref(%3d,%3d) %-6s -> (%4d,%4d) RGB(%3d,%3d,%3d) %s\n",
			p.name, p.x, p.y, anchorName(p.anchor), sx, sy, r, g, b, hueLabel(int(r), int(g), int(b)))
	}

	if doOCR {
		fmt.Println("--- OCR (Apple Vision) ---")
		runOCR(imgPath, img)
	}
}

// renderColorMap downsamples the frame into a color/letter grid. Hue
// classes win over brightness so buttons and icons stand out; otherwise
// density shading shows layout.
func renderColorMap(img gocv.Mat) {
	w, h := img.Cols(), img.Rows()
	cols := 90
	cellW := w / cols
	cellH := cellW * 2 // terminal chars are ~2x taller than wide
	if cellH < 1 {
		cellH = 1
	}
	rows := h / cellH

	var sb strings.Builder
	for ry := 0; ry < rows; ry++ {
		for rx := 0; rx < cols; rx++ {
			x0, y0 := rx*cellW, ry*cellH
			x1, y1 := x0+cellW, y0+cellH
			if x1 > w {
				x1 = w
			}
			if y1 > h {
				y1 = h
			}
			// Average a few samples.
			var sumR, sumG, sumB, n int
			for yy := y0; yy < y1; yy += 2 {
				for xx := x0; xx < x1; xx += 2 {
					b := img.GetUCharAt(yy, xx*3)
					g := img.GetUCharAt(yy, xx*3+1)
					r := img.GetUCharAt(yy, xx*3+2)
					sumR += int(r)
					sumG += int(g)
					sumB += int(b)
					n++
				}
			}
			if n == 0 {
				sb.WriteByte(' ')
				continue
			}
			r, g, b := sumR/n, sumG/n, sumB/n
			// Brightness check for background vs content.
			avg := (r + g + b) / 3
			if avg < 25 {
				sb.WriteByte(' ')
				continue
			}
			if ch, ok := dominantHue(r, g, b); ok && avg > 60 {
				sb.WriteByte(ch)
				continue
			}
			switch {
			case avg > 200:
				sb.WriteByte('#')
			case avg > 120:
				sb.WriteByte('+')
			case avg > 60:
				sb.WriteByte('.')
			default:
				sb.WriteByte('`')
			}
		}
		sb.WriteByte('\n')
	}
	fmt.Print(sb.String())
}

// dominantHue classifies saturated colors into single letters.
func dominantHue(r, g, b int) (byte, bool) {
	max, min := r, b
	if g > max {
		max = g
	}
	if b > max {
		max = b
	}
	if g < min {
		min = g
	}
	if b < min {
		min = b
	}
	sat := max - min
	if sat < 45 {
		return 0, false
	}
	switch max {
	case r:
		// Red vs orange: orange has notable green.
		if g > 90 && b < 80 {
			return 'O', true
		}
		return 'R', true
	case g:
		return 'G', true
	case b:
		return 'B', true
	}
	return 0, false
}

// colorDistance mirrors ClassifyState's colour comparison (vector length in
// RGB space), so this tool's pass counts and the classifier's agree.
func colorDistance(r, g, b int, chk game.PixelCheck) float64 {
	dr := r - int(chk.R)
	dg := g - int(chk.G)
	db := b - int(chk.B)
	return math.Sqrt(float64(dr*dr + dg*dg + db*db))
}

func anchorName(a game.Anchor) string {
	switch a {
	case game.AnchorCenter:
		return "centre"
	case game.AnchorCenterBottom:
		return "centre/bottom"
	default:
		return "edge"
	}
}

// sampleNote samples a mapped point and reports whether it satisfies the
// check, so an anchor dump shows at a glance which model lands on the element.
func sampleNote(img gocv.Mat, x, y int, chk game.PixelCheck) string {
	if x < 0 || y < 0 || x >= img.Cols() || y >= img.Rows() {
		return " oob"
	}
	b := img.GetUCharAt(y, x*3)
	g := img.GetUCharAt(y, x*3+1)
	r := img.GetUCharAt(y, x*3+2)
	if colorDistance(int(r), int(g), int(b), chk) <= float64(chk.Tolerance) {
		return " HIT"
	}
	return " miss"
}

func hueLabel(r, g, b int) string {
	if ch, ok := dominantHue(r, g, b); ok {
		return string(ch)
	}
	avg := (r + g + b) / 3
	if avg > 200 {
		return "W"
	}
	if avg > 120 {
		return "+"
	}
	return "dim"
}

func runOCR(imgPath string, img gocv.Mat) {
	// Prefer the explicit path; else write the live frame to a temp file.
	p := imgPath
	if p == "" {
		tmp := "/tmp/screendump_ocr.png"
		gocv.IMWrite(tmp, img)
		p = tmp
	}

	out, err := exec.Command("bash", "-lc",
		"swiftc -O tools/ocr.swift -o /tmp/clashgo_ocr 2>/dev/null && /tmp/clashgo_ocr "+p).
		CombinedOutput()
	if err != nil {
		fmt.Printf("  (ocr unavailable: %v)\n%s\n", err, strings.TrimSpace(string(out)))
		return
	}
	if strings.TrimSpace(string(out)) == "" {
		fmt.Println("  (no text recognized)")
		return
	}
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		fmt.Println("  " + l)
	}
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
