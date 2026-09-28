// Package attacklog turns a bot run log into a per-unit account of what the
// deploy phase actually did.
//
// It exists because the aggregate line is not evidence. A real 1280x720 attack
// logged `all units successfully deployed` and `remaining=0` while the same log
// recorded, further up, that the Barbarian King and Grand Warden had failed to
// deploy, that the Balloon and Electro Dragon reconciles had exhausted and been
// marked deployed on an OCR reading of zero *against a slot that still looked
// non-empty*, and that five Ice Spells had been tapped inside a 42x29 px box.
//
// The gap was in how the run was checked, not in the parser: the geometry tools
// answer "are the taps inside the deployable band", which is necessary and
// nowhere near sufficient. This package answers "which units actually deployed,
// and were the spells placed somewhere they do work".
//
// It reads the log's *intent and outcome lines* — what the bot decided and what
// its own reconcile concluded. It cannot see the game. A unit it calls
// unverified is one whose completion the bot could not establish either, which
// is exactly the category worth reporting.
package attacklog

import (
	"bufio"
	"fmt"
	"io"
	"math"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Verdict is what the log lets us say about one unit.
type Verdict string

const (
	// ConfirmedDeploy means the bot observed the card leave the bar (an explicit
	// success signal, e.g. "sweep reconciled slot empty" or "hero deployed").
	ConfirmedDeploy Verdict = "deployed"
	// Failed means the bot said so ("will be marked failed").
	Failed Verdict = "FAILED"
	// Unverified means the bot believed it deployed but the evidence in the log
	// is a fallback or a contradiction: a zero-OCR "spent" verdict against a
	// slot its own visual check called non-empty, or a reconcile that exhausted
	// and was handed on. These are the ones that turn into "it didn't use all
	// the troops" with no error anywhere.
	Unverified Verdict = "UNVERIFIED"
	// NotInBar means the card was never in the army bar, so skipping it is
	// correct rather than a failure.
	NotInBar Verdict = "not in bar"
)

// Unit is one line of the report.
type Unit struct {
	Name    string
	Kind    string // troop | siege | hero | spell | event
	Verdict Verdict
	Reason  string

	// Taps are the coordinates the log says were fired for this unit.
	Taps []Point
	// MinSpacing is the smallest distance between any two of its taps, and Span
	// the largest. For a spell fired several times these are the whole question:
	// five freezes 23 px apart are one freeze.
	MinSpacing float64
	Span       float64
}

// Point is a tap coordinate in screen pixels.
type Point struct{ X, Y int }

// Placement is one spell unit's tap geometry, with the analysis applied.
type Placement struct {
	Unit       string
	Taps       []Point
	MinSpacing float64
	Span       float64
	// Overlapped is true when two casts landed within half a cast's reach of
	// each other, i.e. one of them was spent on ground the other already covers.
	Overlapped bool
	// Tight is the milder case: closer than a full reach, but not stacked.
	Tight bool
	// Stacked is how many taps have another tap within half a reach.
	Stacked int
}

// Report is the parsed run.
type Report struct {
	Path        string
	Edge        string
	Projection  string
	DisplayK    float64
	ClampedTop  int
	ClampedBtm  int
	ClampedPts  int
	BandKnown   bool
	Units       []Unit
	SpellRadius float64
	// CountFabricated names the units whose troop count could not be read, so
	// the deploy used a fallback number instead of what the card held.
	CountFabricated []string
	ZeroCountSlots  int
	TotalSlots      int
}

var (
	unitRe    = regexp.MustCompile(`\bunit=("[^"]*"|\S+)`)
	ptRe      = regexp.MustCompile(`\b(?:pt|p1|p2|p)=\{"X":(-?\d+),"Y":(-?\d+)\}`)
	kvIntRe   = regexp.MustCompile(`\b(\w+)=(-?\d+)\b`)
	kvFloatRe = regexp.MustCompile(`\b(\w+)=(-?[\d.]+)\b`)
	kvStrRe   = regexp.MustCompile(`\b(\w+)="([^"]*)"`)
	countRe   = regexp.MustCompile(`"(\d+)":(-?\d+)`)
)

func unquote(s string) string {
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		return s[1 : len(s)-1]
	}
	return s
}

func unitOf(line string) string {
	m := unitRe.FindStringSubmatch(line)
	if m == nil {
		return ""
	}
	return unquote(m[1])
}

func pointsOf(line string) []Point {
	var out []Point
	for _, m := range ptRe.FindAllStringSubmatch(line, -1) {
		x, _ := strconv.Atoi(m[1])
		y, _ := strconv.Atoi(m[2])
		out = append(out, Point{x, y})
	}
	return out
}

func intField(line, key string) (int, bool) {
	for _, m := range kvIntRe.FindAllStringSubmatch(line, -1) {
		if m[1] == key {
			v, _ := strconv.Atoi(m[2])
			return v, true
		}
	}
	return 0, false
}

func floatField(line, key string) (float64, bool) {
	for _, m := range kvFloatRe.FindAllStringSubmatch(line, -1) {
		if m[1] == key {
			v, _ := strconv.ParseFloat(m[2], 64)
			return v, true
		}
	}
	return 0, false
}

func strField(line, key string) (string, bool) {
	for _, m := range kvStrRe.FindAllStringSubmatch(line, -1) {
		if m[1] == key {
			return m[2], true
		}
	}
	return "", false
}

func classifyKind(line, name string) string {
	low := strings.ToLower(line + " " + name)
	switch {
	case strings.Contains(low, "spell"):
		return "spell"
	case strings.Contains(low, "stone slammer"):
		return "siege"
	case strings.Contains(low, "main hero") || strings.Contains(low, "king") ||
		strings.Contains(low, "queen") || strings.Contains(low, "warden") ||
		strings.Contains(low, "minion prince") || strings.Contains(low, "duke"):
		return "hero"
	case strings.Contains(low, "event troop"):
		return "event"
	default:
		return "troop"
	}
}

// DefaultSpellRadius is the reach a single spell placement is assumed to cover,
// in live pixels. It is the yardstick for "did these taps overlap": the village
// view spans roughly 44 tiles across 1280 px at the 720p device profile, so
// ~29 px per tile, and a freeze covers a handful of tiles. The number is a
// heuristic on purpose — the report prints the measured spacing beside it so a
// reader can judge for themselves instead of trusting a verdict.
const DefaultSpellRadius = 100.0

// stackFactor is the fraction of a spell's reach below which two casts are
// considered to be on the same spot rather than merely close. A binary flag at
// the full radius would call a 93 px rage pair as bad as an 11 px ice pair, and
// a report that cries wolf about the harmless case is one nobody reads.
const stackFactor = 0.5

// MinCastSpacing is the smallest live-pixel gap at which two casts of the same
// spell are working adjacent ground instead of the same ground. It is exported
// because it is not only a reporting threshold: the spell deployer enforces it
// as an invariant (see internal/attack's minCastSpacingRef), so that the tool
// which measures the defect and the code that would commit it agree on one
// number. Five Ice Spells tapped 11 px apart is how this was found.
const MinCastSpacing = DefaultSpellRadius * stackFactor

// Parse reads a run log and produces the report.
func Parse(path string) (*Report, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return parse(f, path)
}

func parse(r io.Reader, path string) (*Report, error) {
	rep := &Report{Path: path, SpellRadius: DefaultSpellRadius}
	units := map[string]*Unit{}
	notInBar := map[string]bool{}
	var order []string

	// Units are keyed case-insensitively: the same card is logged as "balloon"
	// by the slot manager, "Balloon" by the planner and "balloon" by the sweep,
	// and treating those as three units is how a report hides a failure in a
	// crowd. The display name is whatever the first line called it.
	get := func(name, line string) *Unit {
		key := strings.ToLower(name)
		if u, ok := units[key]; ok {
			return u
		}
		u := &Unit{Name: name, Kind: classifyKind(line, name), Verdict: Unverified}
		units[key] = u
		order = append(order, key)
		return u
	}

	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()

		// ---- session-level facts ------------------------------------------
		if edge, ok := strField(line, "edge"); ok && rep.Edge == "" {
			rep.Edge = edge
		} else if rep.Edge == "" && strings.Contains(line, "edge selected") {
			// That line logs the corner unquoted: `edge=BottomRight`.
			if m := regexp.MustCompile(`\bedge=(\w+)`).FindStringSubmatch(line); m != nil {
				rep.Edge = m[1]
			}
		}
		if proj, ok := strField(line, "projection"); ok {
			rep.Projection = proj
		}
		if k, ok := floatField(line, "display_scale"); ok && rep.DisplayK == 0 {
			rep.DisplayK = k
		}
		if strings.Contains(line, "projected outside the deployable band") {
			rep.BandKnown = true
			if v, ok := intField(line, "points_clamped"); ok {
				rep.ClampedPts = v
			}
			if v, ok := intField(line, "band_top"); ok {
				rep.ClampedTop = v
			}
			if v, ok := intField(line, "band_bottom"); ok {
				rep.ClampedBtm = v
			}
		}
		if strings.Contains(line, "detected troop counts") {
			if i := strings.Index(line, "counts="); i >= 0 {
				for _, m := range countRe.FindAllStringSubmatch(line[i:], -1) {
					rep.TotalSlots++
					if m[2] == "0" {
						rep.ZeroCountSlots++
					}
				}
			}
		}

		// ---- explicit outcomes --------------------------------------------
		switch {
		case strings.Contains(line, "unit not found in bar"):
			if n := unitOf(line); n != "" {
				// Registered, not just remembered: a unit the strategy lists and
				// the bar does not carry is part of the run's account, and leaving
				// it out is how a reader concludes the army was complete.
				get(n, line)
				notInBar[strings.ToLower(n)] = true
			}

		case strings.Contains(line, "reconcile exhausted"):
			if n := unitOf(line); n != "" {
				u := get(n, line)
				u.Verdict = Unverified
				u.Reason = "reconcile exhausted; handed to the sweep without confirming the card left"
			}

		case strings.Contains(line, "hero deployed (single card tap"), strings.Contains(line, "hero deployed (slot-selected"):
			if n := unitOf(line); n != "" {
				u := get(n, line)
				u.Verdict = ConfirmedDeploy
				u.Reason = "slot visibly transitioned to cooldown"
			}

		case strings.Contains(line, "hero deployed on displaced retry"):
			if n := unitOf(line); n != "" {
				u := get(n, line)
				u.Verdict = ConfirmedDeploy
				u.Reason = "displaced retry transitioned the slot to cooldown"
			}

		case strings.Contains(line, "hero sweep deploy succeeded"):
			if n := unitOf(line); n != "" {
				u := get(n, line)
				u.Verdict = ConfirmedDeploy
				u.Reason = "sweep's line retry transitioned the slot to cooldown"
			}

		case strings.Contains(line, "hero slot did not visibly transition"),
			strings.Contains(line, "hero drop not confirmed by the slot transition"):
			if n := unitOf(line); n != "" {
				u := get(n, line)
				if u.Verdict != Failed {
					u.Verdict = Unverified
					u.Reason = "slot did not visibly transition to cooldown"
				}
			}

		case strings.Contains(line, "did not visibly transition slot; will be marked failed"):
			if n := unitOf(line); n != "" {
				u := get(n, line)
				u.Verdict = Failed
				u.Reason = "sweep retry also saw no transition; marked failed"
			}

		case strings.Contains(line, "retry: reconciled slot empty"):
			if n := unitOf(line); n != "" {
				u := get(n, line)
				u.Verdict = ConfirmedDeploy
				u.Reason = "verifier retry reconciled the slot empty"
			}

		case strings.Contains(line, "reconcile: siege count label gone"):
			if n := unitOf(line); n != "" {
				u := get(n, line)
				u.Verdict = ConfirmedDeploy
				u.Reason = "siege count label gone after the deploy tap"
			}

		case strings.Contains(line, "reconcile: siege slot dimmed"):
			if n := unitOf(line); n != "" {
				u := get(n, line)
				u.Verdict = ConfirmedDeploy
				u.Reason = "siege card dimmed after the deploy tap"
			}

		case strings.Contains(line, "hero retry: visibly transitioned to cooldown"):
			if n := unitOf(line); n != "" {
				u := get(n, line)
				// The verifier's retry comes AFTER the sweep's "will be marked
				// failed": live 2026-09-26, run1 — the sweep wrote the Barbarian
				// King off, then the verifier's own retry visibly transitioned
				// the slot. A later confirmation must overwrite the earlier
				// failure, so this case sets ConfirmedDeploy unconditionally.
				u.Verdict = ConfirmedDeploy
				u.Reason = "verifier retry transitioned the slot to cooldown"
			}

		// Per-unit sweep drain: the sweep watched THIS card empty mid-batch.
		// Deliberately NOT keyed off "all units successfully deployed": the
		// aggregate line carries no per-unit evidence, and the fixture pins
		// the property that a log can carry it while units stay unproven.
		case strings.Contains(line, "genuine-empty bail; slot drained"):
			if n := unitOf(line); n != "" {
				u := get(n, line)
				u.Verdict = ConfirmedDeploy
				u.Reason = "sweep watched the card drain mid-batch"
			}

		case strings.Contains(line, "OCR reads zero for consecutive reconciles"):
			if n := unitOf(line); n != "" {
				u := get(n, line)
				u.Verdict = Unverified
				u.Reason = "marked deployed on a zero troop-count OCR reading, against a slot the same check called still non-empty"
			}

		case strings.Contains(line, "sweep reconciled slot empty"):
			if n := unitOf(line); n != "" {
				u := get(n, line)
				u.Verdict = ConfirmedDeploy
				u.Reason = "sweep saw the slot drain"
			}

		// The troop deploy phase's own success line. Without this case a unit
		// whose card the deploy phase watched drain still read UNVERIFIED
		// ("no completion check in the log") on live runs, which made the
		// report accuse a working deploy of being unproven.
		case strings.Contains(line, "reconcile confirmed slot empty"):
			if n := unitOf(line); n != "" {
				u := get(n, line)
				u.Verdict = ConfirmedDeploy
				u.Reason = "the deploy phase saw the card drain"
			}

		// The sweep writing a card off on a trusted zero while the visual check
		// still saw content. That is a judgement, not a drain, so it is
		// recorded as unverified rather than confirmed.
		case strings.Contains(line, "card shows no count while the rest of the bar is readable"):
			if n := unitOf(line); n != "" {
				u := get(n, line)
				u.Verdict = Unverified
				u.Reason = "written off on a zero count while the visual check still saw content"
			}

		case strings.Contains(line, "spell slot confirmed empty"):
			if n := unitOf(line); n != "" {
				u := get(n, line)
				u.Verdict = ConfirmedDeploy
				u.Reason = "spell card drained"
			}

		case strings.Contains(line, "visual-empty-but-OCR-zero"):
			if n := unitOf(line); n != "" {
				u := get(n, line)
				if u.Reason == "" {
					u.Reason = "visual check and count OCR disagree on the same slot"
				}
			}
		}

		// ---- taps ----------------------------------------------------------
		if pts := pointsOf(line); len(pts) > 0 && isDeployLine(line) {
			n := unitOf(line)
			if n == "" {
				n = unnamedDeploy(line)
			}
			if n != "" {
				u := get(n, line)
				u.Taps = append(u.Taps, pts...)
			}
		}

		// ---- fabricated counts ---------------------------------------------
		if strings.Contains(line, "formula-driven live-count") {
			if dc, ok := intField(line, "detected_count"); ok && dc == 0 {
				if n := unitOf(line); n != "" {
					rep.CountFabricated = appendUnique(rep.CountFabricated, n)
				}
			}
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}

	for _, key := range order {
		u := units[key]
		switch {
		case notInBar[key] && len(u.Taps) == 0 && u.Verdict == Unverified:
			u.Verdict = NotInBar
			u.Reason = "strategy lists it but this army's bar does not carry it"
		case u.Reason == "" && len(u.Taps) > 0:
			// Taps went out and the log never says what became of them. That is
			// not a success signal, and on a real run it is exactly how a unit
			// disappears: the ice spell's five taps are the last thing logged
			// before the sweep, with no reconcile line at all.
			u.Reason = "taps were sent but the log carries no completion check for it"
		case u.Reason == "" && u.Verdict == Unverified:
			u.Reason = "seen in the log but never deployed or explicitly skipped"
		}
		spaceTaps(u)
		rep.Units = append(rep.Units, *u)
	}
	sort.SliceStable(rep.Units, func(i, j int) bool {
		return kindOrder(rep.Units[i].Kind) < kindOrder(rep.Units[j].Kind)
	})
	return rep, nil
}

// IsDeployLine reports whether a log line describes a deploy tap carrying
// coordinates — the lines this package reads coordinates from.
func IsDeployLine(line string) bool { return isDeployLine(line) }

func isDeployLine(line string) bool {
	for _, m := range []string{
		"tapping spell formula point",
		"deploying troop (formula-driven live-count)",
		"formula-driven siege deploy",
		"sweep deploying",
		"deploying main hero",
	} {
		if strings.Contains(line, m) {
			return true
		}
	}
	return false
}

// unnamedDeploy names the log lines that carry coordinates but no unit — the
// sweep prints an empty `unit=` for a slot its template match could not label,
// and those are exactly the taps worth seeing.
func unnamedDeploy(line string) string {
	if strings.Contains(line, "sweep deploying") {
		if x, ok := intField(line, "x"); ok {
			return fmt.Sprintf("unlabelled slot x=%d", x)
		}
	}
	return ""
}

func appendUnique(list []string, s string) []string {
	for _, v := range list {
		if v == s {
			return list
		}
	}
	return append(list, s)
}

func kindOrder(k string) int {
	switch k {
	case "troop":
		return 0
	case "siege":
		return 1
	case "hero":
		return 2
	case "event":
		return 3
	case "spell":
		return 4
	default:
		return 5
	}
}

// spaceTaps fills MinSpacing and Span for a unit's taps.
func spaceTaps(u *Unit) {
	if len(u.Taps) < 2 {
		return
	}
	minD := math.Inf(1)
	maxD := 0.0
	for i := 0; i < len(u.Taps); i++ {
		for j := i + 1; j < len(u.Taps); j++ {
			d := math.Hypot(float64(u.Taps[i].X-u.Taps[j].X), float64(u.Taps[i].Y-u.Taps[j].Y))
			if d < minD {
				minD = d
			}
			if d > maxD {
				maxD = d
			}
		}
	}
	u.MinSpacing, u.Span = minD, maxD
}

// Placements returns the spell units with their spacing analysis, which is where
// the report's "did the spells overlap" answer comes from.
func (r *Report) Placements() []Placement {
	stack := r.SpellRadius * stackFactor
	var out []Placement
	for _, u := range r.Units {
		if u.Kind != "spell" || len(u.Taps) < 2 {
			continue
		}
		p := Placement{
			Unit:       u.Name,
			Taps:       u.Taps,
			MinSpacing: u.MinSpacing,
			Span:       u.Span,
			Overlapped: u.MinSpacing < stack,
			Tight:      u.MinSpacing < r.SpellRadius,
		}
		for i := range u.Taps {
			for j := range u.Taps {
				if i == j {
					continue
				}
				d := math.Hypot(float64(u.Taps[i].X-u.Taps[j].X), float64(u.Taps[i].Y-u.Taps[j].Y))
				if d < stack {
					p.Stacked++
					break
				}
			}
		}
		out = append(out, p)
	}
	return out
}

// Counts returns how many units landed in each verdict.
func (r *Report) Counts() map[Verdict]int {
	out := map[Verdict]int{}
	for _, u := range r.Units {
		out[u.Verdict]++
	}
	return out
}

// FailedUnits returns the units whose deployment the run did not establish,
// excluding the ones that were never in the bar.
func (r *Report) FailedUnits() []Unit {
	var out []Unit
	for _, u := range r.Units {
		if u.Verdict == Failed || u.Verdict == Unverified {
			out = append(out, u)
		}
	}
	return out
}

// OverlappedSpells returns the spell units whose taps are stacked within half
// a cast's reach, which means part of the cast landed on ground a previous cast
// already covers.
func (r *Report) OverlappedSpells() []Placement {
	var out []Placement
	for _, p := range r.Placements() {
		if p.Overlapped {
			out = append(out, p)
		}
	}
	return out
}

// Problem reports whether anything in the run must not be called a success: an
// unestablished deployment, an overlapped spell cast, or a fabricated troop
// count (which is what makes the other two possible).
func (r *Report) Problem() bool {
	return len(r.FailedUnits()) > 0 || len(r.OverlappedSpells()) > 0 || len(r.CountFabricated) > 0
}
