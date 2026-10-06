// tapaudit reviews every touch the bot fired, straight from the NDJSON
// tap trace (CLASHGO_TAP_TRACE=<path>). The trace is the bot's raw output:
// what actually went to the device, in order, with timestamps.
//
// It answers, per battle:
//
//   - which taps landed on the game's chrome (top HUD, side pads, the
//     button corners) instead of the battlefield or the troop bar,
//   - which taps double-fired or hammered one tile (the "random taps"
//     a human sees as the bot twitching),
//   - whether the cadence is machine-perfect (constant gaps, zero jitter),
//     which is what makes a deploy look bot-like.
//
// Exit code is non-zero when any tap is flagged, so it can gate a run.
//
// Lightweight on purpose: stdlib only, no OpenCV, one pass over the file.
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"os"
	"sort"
	"strings"
	"time"
)

// TapEvent mirrors internal/adb.TapEvent's JSON encoding. Duplicated rather
// than imported so this tool stays free of the OpenCV-linked adb package.
type TapEvent struct {
	Seq     int64   `json:"seq"`
	Type    string  `json:"type"`
	X       int     `json:"x"`
	Y       int     `json:"y"`
	ActualX int     `json:"actual_x"`
	ActualY int     `json:"actual_y"`
	StdDev  float64 `json:"std_dev,omitempty"`
	Error   string  `json:"error,omitempty"`
	Ts      int64   `json:"ts_ms"`
}

const (
	// topHUDFraction: the top resource bar is chrome — a tap above it is
	// eaten by the UI. Mirrors the deploy band's yTopMin intent.
	topHUDFraction = 0.12
	// uiCutoffFraction mirrors attack.UICutoff: the troop bar starts here.
	uiCutoffFraction = 0.85
	// sidePadFraction: the outermost 3% of the width is edge chrome.
	sidePadFraction = 0.03
)

type flagReason string

const (
	flagTopHUD    flagReason = "top-hud"
	flagSidePad   flagReason = "side-pad"
	flagDoubleTap flagReason = "double-fire"
	flagHammering flagReason = "hammering"
	flagBotCadence flagReason = "bot-cadence"
	flagTapError  flagReason = "tap-error"
)

type auditTap struct {
	TapEvent
	class  string
	reason []flagReason
}

func main() {
	tracePath := flag.String("trace", "", "tap trace NDJSON (written when CLASHGO_TAP_TRACE is set)")
	w := flag.Int("w", 1280, "live screen width")
	h := flag.Int("h", 720, "live screen height")
	verbose := flag.Bool("v", false, "print every touch with its classification")
	flag.Parse()

	if *tracePath == "" {
		fmt.Fprintln(os.Stderr, "usage: tapaudit -trace <taps.ndjson> [-w 1280] [-h 720] [-v]")
		os.Exit(2)
	}

	taps, err := loadTrace(*tracePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "tapaudit: %v\n", err)
		os.Exit(2)
	}
	if len(taps) == 0 {
		fmt.Println("tapaudit: trace is empty — no touches recorded")
		return
	}

	flagged := audit(taps, *w, *h)

	if *verbose {
		for _, t := range taps {
			mark := " "
			if len(t.reason) > 0 {
				mark = "!"
			}
			when := time.UnixMilli(t.Ts).Format("15:04:05.000")
			fmt.Printf("%s %s %-16s (%4d,%4d) actual(%4d,%4d) jitter=%.1f %s%s\n",
				mark, when, t.Type, t.X, t.Y, t.ActualX, t.ActualY, t.StdDev,
				t.class, reasonSuffix(t.reason))
		}
		fmt.Println()
	}

	report(taps, flagged, *w, *h)
	if flagged > 0 {
		os.Exit(1)
	}
}

func reasonSuffix(reasons []flagReason) string {
	if len(reasons) == 0 {
		return ""
	}
	parts := make([]string, len(reasons))
	for i, r := range reasons {
		parts[i] = string(r)
	}
	return " [" + strings.Join(parts, ",") + "]"
}

func loadTrace(path string) ([]auditTap, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var taps []auditTap
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var ev TapEvent
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			return nil, fmt.Errorf("%s:%d: not valid TapEvent JSON: %w", path, lineNo, err)
		}
		taps = append(taps, auditTap{TapEvent: ev})
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return taps, nil
}

// audit classifies every tap in place and returns how many carry a flag.
func audit(taps []auditTap, w, h int) int {
	topHUD := int(float64(h) * topHUDFraction)
	uiCut := int(float64(h) * uiCutoffFraction)
	pad := int(float64(w) * sidePadFraction)

	flagged := 0
	for i := range taps {
		t := &taps[i]
		switch {
		case t.Y < topHUD:
			t.class = "top-hud"
			t.reason = append(t.reason, flagTopHUD)
		case t.Y > uiCut:
			t.class = "bar/ui"
		case t.X < pad || t.X > w-pad:
			t.class = "field(edge)"
			t.reason = append(t.reason, flagSidePad)
		default:
			t.class = "field"
		}
		if t.Error != "" {
			t.reason = append(t.reason, flagTapError)
		}

		// Double-fire: two touches on the same spot within 300ms. A human
		// rarely lands two taps inside one tile twice in a third of a second
		// unless one of them was a misfire.
		if i > 0 {
			prev := &taps[i-1]
			dt := t.Ts - prev.Ts
			if dt >= 0 && dt < 300 && dist(t.ActualX, t.ActualY, prev.ActualX, prev.ActualY) <= 25 {
				t.reason = append(t.reason, flagDoubleTap)
			}
		}

		// Hammering: the same tile 3+ times within 2s (window looks back).
		same := 0
		for j := i - 1; j >= 0 && t.Ts-taps[j].Ts <= 2000; j-- {
			if dist(t.ActualX, t.ActualY, taps[j].ActualX, taps[j].ActualY) <= 25 {
				same++
			}
		}
		if same >= 2 {
			t.reason = append(t.reason, flagHammering)
		}
		if len(t.reason) > 0 {
			flagged++
		}
	}

	// Bot cadence: a burst of 8+ taps whose inter-tap gaps are all within
	// 5ms of their mean (and mean below 400ms) is machine output, not a
	// hand. Flagged on the burst's first tap.
	const burstMin = 8
	for i := 0; i+burstMin <= len(taps); i++ {
		gaps := make([]float64, 0, burstMin-1)
		ok := true
		for j := i; j < i+burstMin-1; j++ {
			g := float64(taps[j+1].Ts - taps[j].Ts)
			if g < 0 {
				ok = false
				break
			}
			gaps = append(gaps, g)
		}
		if !ok {
			continue
		}
		mean, sd := meanSD(gaps)
		if mean < 400 && sd <= 5 {
			taps[i].reason = append(taps[i].reason, flagBotCadence)
			flagged++
			i += burstMin - 1 // one flag per burst
		}
	}
	return flagged
}

func report(taps []auditTap, flagged, w, h int) {
	byType := map[string]int{}
	byClass := map[string]int{}
	for _, t := range taps {
		byType[t.Type]++
		byClass[t.class]++
	}

	first := time.UnixMilli(taps[0].Ts)
	last := time.UnixMilli(taps[len(taps)-1].Ts)
	dur := last.Sub(first)
	rate := 0.0
	if dur > 0 {
		rate = float64(len(taps)) / dur.Seconds()
	}

	var gaps []float64
	for i := 1; i < len(taps); i++ {
		gaps = append(gaps, float64(taps[i].Ts-taps[i-1].Ts))
	}

	fmt.Printf("tapaudit: %d touches over %s (%.1f taps/s) at %dx%d\n",
		len(taps), dur.Round(time.Millisecond), rate, w, h)
	fmt.Printf("  window   %s -> %s\n", first.Format("15:04:05.000"), last.Format("15:04:05.000"))
	if len(gaps) > 0 {
		sort.Float64s(gaps)
		mean, sd := meanSD(gaps)
		fmt.Printf("  gaps     min=%dms median=%dms p90=%dms max=%dms mean=%.0fms sd=%.0fms\n",
			int(gaps[0]), int(gaps[len(gaps)/2]), int(gaps[len(gaps)*9/10]),
			int(gaps[len(gaps)-1]), mean, sd)
	}
	keys := make([]string, 0, len(byClass))
	for k := range byClass {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Printf("  %-11s %d\n", k, byClass[k])
	}
	keys = keys[:0]
	for k := range byType {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	fmt.Printf("  types      %s\n", joinCounts(byType, keys))

	if flagged == 0 {
		fmt.Println("  verdict    CLEAN — no stray, repetitive or machine-cadenced touches")
		return
	}
	fmt.Printf("  verdict    %d flagged touches\n", flagged)
	for _, t := range taps {
		if len(t.reason) > 0 {
			when := time.UnixMilli(t.Ts).Format("15:04:05.000")
			fmt.Printf("    %s %-16s (%4d,%4d) %s%s\n",
				when, t.Type, t.X, t.Y, t.class, reasonSuffix(t.reason))
		}
	}
}

func joinCounts(m map[string]int, keys []string) string {
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%d", k, m[k]))
	}
	return strings.Join(parts, " ")
}

func dist(x1, y1, x2, y2 int) float64 {
	return math.Hypot(float64(x1-x2), float64(y1-y2))
}

func meanSD(v []float64) (float64, float64) {
	if len(v) == 0 {
		return 0, 0
	}
	m := 0.0
	for _, x := range v {
		m += x
	}
	m /= float64(len(v))
	sd := 0.0
	for _, x := range v {
		sd += (x - m) * (x - m)
	}
	return m, math.Sqrt(sd / float64(len(v)))
}
