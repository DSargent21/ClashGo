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
//
// The whole point is text: a text-only agent (or a terminal user) can
// "see" what the emulator shows — state, layout, colors, and words.

package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/Ducky705/ClashGO/internal/game"
	"github.com/Ducky705/ClashGO/internal/paths"
	"github.com/rs/zerolog"
	"gocv.io/x/gocv"
)

func main() {
	imgPath := flag.String("img", "", "screenshot to analyze (default: live adb capture)")
	doOCR := flag.Bool("ocr", false, "run Apple Vision OCR on the frame")
	watch := flag.Bool("watch", false, "live loop: refresh every 3s until Ctrl-C")
	save := flag.String("save", "", "copy the analyzed frame to this path")
	flag.Parse()

	for {
		runOnce(*imgPath, *doOCR, *save)
		if !*watch {
			return
		}
		time.Sleep(3 * time.Second)
	}
}

func runOnce(imgPath string, doOCR bool, savePath string) {
	img := gocv.Mat{}
	if imgPath != "" {
		img = gocv.IMRead(imgPath, gocv.IMReadColor)
		if img.Empty() {
			fmt.Fprintf(os.Stderr, "cannot read %s\n", imgPath)
			os.Exit(1)
		}
	} else {
		out, err := exec.Command("adb", "exec-out", "screencap", "-p").Output()
		if err != nil {
			fmt.Fprintf(os.Stderr, "adb screencap: %v\n", err)
			os.Exit(1)
		}
		tmp := "/tmp/screendump_live.png"
		if err := os.WriteFile(tmp, out, 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "write tmp: %v\n", err)
			os.Exit(1)
		}
		img = gocv.IMRead(tmp, gocv.IMReadColor)
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

	cal := &game.Calibration{
		PhysicalW:  img.Cols(),
		PhysicalH:  img.Rows(),
		ScaleX:     float64(img.Cols()) / float64(game.RefWidth),
		ScaleY:     float64(img.Rows()) / float64(game.RefHeight),
		MidOffsetY: (img.Rows() - game.RefHeight) / 2,
		BottomOffY: img.Rows() - game.RefHeight,
		Verified:   true,
	}

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
			sx, sy := cal.ScaleRef(chk.X, chk.Y)
			if sx < 0 || sy < 0 || sx >= img.Cols() || sy >= img.Rows() {
				continue
			}
			b := img.GetUCharAt(sy, sx*3)
			g := img.GetUCharAt(sy, sx*3+1)
			r := img.GetUCharAt(sy, sx*3+2)
			if abs(int(r)-int(chk.R)) <= chk.Tolerance &&
				abs(int(g)-int(chk.G)) <= chk.Tolerance &&
				abs(int(b)-int(chk.B)) <= chk.Tolerance {
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
		fmt.Printf("  %s %-18s pass=%d/%d minpass=%d\n", marker, rule.State.String(), passed, len(rule.Checks), rule.MinPass)
	}

	fmt.Println("--- color map (G=green O=orange R=red B=blue P=purple Y=yellow W=white .=dim #=bright) ---")
	renderColorMap(img)

	fmt.Println("--- key button pixels (ref coords -> live RGB) ---")
	probes := []struct {
		name string
		x, y int
	}{
		{"attack(64,666)", 64, 666},
		{"find_match(158,494)", 158, 494},
		{"battle(731,537)", 731, 537},
		{"army_arrow(514,192)", 514, 192},
		{"army_1(513,230)", 513, 230},
		{"next(794,577)", 794, 577},
		{"return_home(431,581)", 431, 581},
		{"okay(430,520)", 430, 520},
		{"gold_icon(35,85)", 35, 85},
		{"elixir_icon(35,115)", 35, 115},
		{"de_icon(35,145)", 35, 145},
	}
	for _, p := range probes {
		sx, sy := cal.ScaleRef(p.x, p.y)
		if sx < 0 || sy < 0 || sx >= img.Cols() || sy >= img.Rows() {
			fmt.Printf("  %-20s out of bounds\n", p.name)
			continue
		}
		b := img.GetUCharAt(sy, sx*3)
		g := img.GetUCharAt(sy, sx*3+1)
		r := img.GetUCharAt(sy, sx*3+2)
		fmt.Printf("  %-20s (%3d,%3d) RGB(%3d,%3d,%3d) %s\n", p.name, sx, sy, r, g, b, hueLabel(int(r), int(g), int(b)))
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
