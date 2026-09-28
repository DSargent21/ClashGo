// Command attack_report answers the two questions a finished attack actually
// raises, from the run's own log:
//
//	did every unit deploy, and were the spells placed somewhere useful?
//
// The other two harnesses answer neighbouring questions. `attack_verify` shows
// the *plan* before a battle is spent, and `see` shows what the bot *perceives*
// in a frame. Neither looks at what the run did, which is how a session that
// logged "all units successfully deployed" also logged, 20 lines earlier, two
// heroes failing and two troop cards being written off on a broken OCR reading.
//
// Usage:
//
//	attack_report -log tmp/run.log
//	attack_report -log tmp/run.log -json
//	attack_report -log tmp/run.log -spell-radius 90
//
// Exit code is 1 when the run has a problem worth looking at, so it can gate a
// script; 0 when nothing in the log contradicts a success.
//
// This is a log reader, not a game observer. A unit it calls UNVERIFIED is one
// whose completion the bot could not establish either — which is precisely the
// category that used to reach a human as "it didn't use all the troops".
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/Ducky705/ClashGO/internal/attacklog"
)

func main() {
	logPath := flag.String("log", "", "run log to analyse (required)")
	asJSON := flag.Bool("json", false, "emit the report as JSON instead of prose")
	radius := flag.Float64("spell-radius", attacklog.DefaultSpellRadius,
		"live px one spell placement is assumed to cover; taps closer than this are reported as overlapping")
	flag.Parse()

	if *logPath == "" {
		fmt.Fprintln(os.Stderr, "attack_report: -log is required")
		flag.PrintDefaults()
		os.Exit(2)
	}

	rep, err := attacklog.Parse(*logPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "attack_report: %v\n", err)
		os.Exit(2)
	}
	rep.SpellRadius = *radius

	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", " ")
		if err := enc.Encode(rep); err != nil {
			fmt.Fprintf(os.Stderr, "attack_report: %v\n", err)
			os.Exit(2)
		}
		if rep.Problem() {
			os.Exit(1)
		}
		return
	}

	printReport(rep)
	if rep.Problem() {
		os.Exit(1)
	}
}

func printReport(rep *attacklog.Report) {
	band := "band unknown"
	if rep.BandKnown {
		band = fmt.Sprintf("clamped %d into y %d..%d", rep.ClampedPts, rep.ClampedTop, rep.ClampedBtm)
	}
	proj := rep.Projection
	if proj == "" {
		proj = "(not logged)"
	}
	fmt.Printf("=== attack report %s ===\n", rep.Path)
	fmt.Printf("edge        %s\n", orDash(rep.Edge))
	fmt.Printf("geometry    k=%.4f  %s  %s\n", rep.DisplayK, proj, band)
	if rep.TotalSlots > 0 {
		fmt.Printf("army bar    %d slots, troop counts read 0 on %d of them\n",
			rep.TotalSlots, rep.ZeroCountSlots)
	}
	fmt.Println()

	fmt.Println("--- deployment, per unit ---")
	fmt.Printf("  %-24s %-11s %s\n", "UNIT", "VERDICT", "WHY")
	for _, u := range rep.Units {
		mark := ""
		switch u.Verdict {
		case attacklog.Failed:
			mark = " <<<"
		case attacklog.Unverified:
			mark = " <<<"
		}
		fmt.Printf("  %-24s %-11s %s%s\n", u.Name, u.Verdict, u.Reason, mark)
	}

	if lines := rep.Placements(); len(lines) > 0 {
		fmt.Println()
		fmt.Printf("--- spell placement (a cast is assumed to cover %.0f px) ---\n", rep.SpellRadius)
		sort.SliceStable(lines, func(i, j int) bool { return lines[i].Unit < lines[j].Unit })
		for _, p := range lines {
			verdict := "spread"
			switch {
			case p.Overlapped:
				verdict = "OVERLAPPED"
			case p.Tight:
				verdict = "tight"
			}
			fmt.Printf("  %-24s %d taps  closest pair %.0f px  span %.0f px  %s\n",
				p.Unit, len(p.Taps), p.MinSpacing, p.Span, verdict)
			fmt.Printf("  %-24s %s\n", "", formatPoints(p.Taps))
			if p.Overlapped {
				fmt.Printf("  %-24s %d of its %d casts are on ground another already covers, so that many are spent for nothing\n",
					"", p.Stacked, len(p.Taps))
			}
		}
	}

	fmt.Println()
	counts := rep.Counts()
	total := 0
	for _, n := range counts {
		total += n
	}
	parts := make([]string, 0, len(counts))
	for v, n := range counts {
		parts = append(parts, fmt.Sprintf("%s=%d", v, n))
	}
	sort.Strings(parts)
	fmt.Printf("--- totals ---\n  %d units: %s\n", total, strings.Join(parts, "  "))

	if bad := rep.FailedUnits(); len(bad) > 0 {
		names := make([]string, 0, len(bad))
		for _, u := range bad {
			names = append(names, fmt.Sprintf("%s (%s)", u.Name, u.Verdict))
		}
		fmt.Printf("  NOT established as deployed: %s\n", strings.Join(names, ", "))
	}
	if len(rep.CountFabricated) > 0 {
		fmt.Printf("  troop count unreadable, deploy used a fallback number for: %s\n",
			strings.Join(rep.CountFabricated, ", "))
	}
	if rep.Problem() {
		fmt.Println()
		fmt.Println("VERDICT: this run must NOT be called a successful deployment — see the lines above.")
	} else {
		fmt.Println()
		fmt.Println("VERDICT: nothing in the log contradicts a successful deployment.")
	}
}

func formatPoints(pts []attacklog.Point) string {
	parts := make([]string, 0, len(pts))
	for _, p := range pts {
		parts = append(parts, fmt.Sprintf("(%d,%d)", p.X, p.Y))
	}
	return strings.Join(parts, " ")
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
