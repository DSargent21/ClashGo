// Command see is the debugging agent's eyes on the emulator.
//
// The problem it solves: an agent can read text, not pixels, and the loop
// "tap something, look at the screen, decide" is where debugging bot vision
// actually happens. Without a way to look, every visual question costs a
// round trip through a human, and bugs like "the popup never got dismissed"
// or "the spells landed in the corner" take hours to find.
//
// see turns a screenshot into three things a text reader can use:
//
//	what the bot perceives   the classifier's state, the action button it
//	                         found, the red zone, every recognized string
//	                         with its bounding box
//	what it looks like       a coordinate-ruled character render, so the
//	                         layout is visible and any glyph maps back to a
//	                         tappable pixel (see internal/vision.Render)
//	what changed             a diff against an earlier frame, which answers
//	                         "did my tap land" without guessing
//
// plus a rolling store of recorded frames, so a run that already happened can
// still be reviewed ("timeline", "show").
//
// Usage:
//
//	see look [-img F] [-rect x,y,w,h] [-cell N] [-grep TEXT] [-rules]
//	see diff A B [-threshold N] [-render]
//	see rec [-every 2s] [-n 0] [-keep 400] [-label NAME] [-stop-on STATE]
//	see shots [-n 20]
//	see timeline [-n 40]
//	see show last|<id> [-rect x,y,w,h] [-cell N]
//
// A/B in diff is a PNG path, "last" for the newest recorded frame, or "live"
// for a fresh capture.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"image"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Ducky705/ClashGO/internal/adb"
	"github.com/Ducky705/ClashGO/internal/attack"
	"github.com/Ducky705/ClashGO/internal/config"
	"github.com/Ducky705/ClashGO/internal/game"
	"github.com/Ducky705/ClashGO/internal/paths"
	"github.com/Ducky705/ClashGO/internal/vision"
	"github.com/rs/zerolog"
	"gocv.io/x/gocv"
)

const usageText = `see - screenshot perception reports for debugging

commands:
  look      capture (or read) a frame, report what the bot perceives, render it
  diff      compare two frames and map what changed
  rec       record frames into the ring buffer, one line per frame
  shots     list recorded frames
  timeline  summarize recorded frames as state transitions
  show      render a recorded frame by id (or "last")

common flags: -device -k -dir -cols -cell -color -json
run "see <command> -h" for the flags of one command
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usageText)
		os.Exit(2)
	}
	cmd, args := os.Args[1], os.Args[2:]
	switch cmd {
	case "look":
		cmdLook(args)
	case "diff":
		cmdDiff(args)
	case "rec":
		cmdRec(args)
	case "shots":
		cmdShots(args)
	case "timeline":
		cmdTimeline(args)
	case "show":
		cmdShow(args)
	case "help", "-h", "--help":
		fmt.Print(usageText)
	default:
		fmt.Fprintf(os.Stderr, "see: unknown command %q\n\n%s", cmd, usageText)
		os.Exit(2)
	}
}

// ---------------------------------------------------------------- capture

// shot is a captured screen plus everything needed to report on it: the
// OpenCV Mat for the vision helpers, the byte view for rendering, and a file
// path for the OCR helper.
type shot struct {
	mat    gocv.Mat
	frame  vision.Frame
	path   string
	source string
}

// takeShot reads a saved PNG, or captures the live device through the same
// adb transport the bot uses (never the host adb binary: on BlueStacks the
// host binary reports "error: closed" and a second attached device makes an
// unfiltered exec-out fail outright).
func takeShot(imgPath, device, dir string) (*shot, error) {
	if imgPath != "" {
		m := gocv.IMRead(imgPath, gocv.IMReadColor)
		if m.Empty() {
			return nil, fmt.Errorf("cannot read %s", imgPath)
		}
		return &shot{mat: m, frame: vision.FrameFromMat(m), path: imgPath, source: imgPath}, nil
	}
	client := adb.NewClient(
		adb.WithHost("127.0.0.1"),
		adb.WithPort(5037),
		adb.WithTimeout(30*time.Second),
	)
	client.DeviceID = device
	defer client.Close()
	m, err := client.CaptureToMat()
	if err != nil {
		return nil, fmt.Errorf("live capture on %s: %w", device, err)
	}
	if m.Empty() {
		return nil, fmt.Errorf("empty capture on %s", device)
	}
	path := storePath(dir, "live.png")
	f := vision.FrameFromMat(m)
	if err := vision.WritePNG(path, f); err != nil {
		m.Close()
		return nil, err
	}
	return &shot{mat: m, frame: f, path: path, source: "live " + device}, nil
}

func (s *shot) Close() {
	if s != nil && !s.mat.Empty() {
		s.mat.Close()
	}
}

// perception is everything the bot's vision layer reports about a frame.
type perception struct {
	Time        string              `json:"time"`
	Source      string              `json:"source"`
	Width       int                 `json:"width"`
	Height      int                 `json:"height"`
	DisplayK    float64             `json:"display_scale"`
	ScaleSource string              `json:"display_scale_source"`
	// DepartsBot is true when DisplayK is not the geometry the bot runs
	// (a -k override that contradicts device.display_scale), so a machine
	// reader can distrust the verdict without parsing ScaleSource.
	DepartsBot bool `json:"departs_from_bot_geometry"`
	PinnedK     bool                `json:"display_scale_pinned"`
	State       string              `json:"state"`
	Score       int                 `json:"state_score"`
	Button      *buttonInfo         `json:"action_button,omitempty"`
	RedZone     *zoneInfo           `json:"red_zone,omitempty"`
	Text        []vision.TextRegion `json:"text,omitempty"`
	Marks       []string            `json:"marks,omitempty"`
	Grid        string              `json:"grid,omitempty"`
	RulesPassed []string            `json:"rules_passed,omitempty"`
	Changed     *vision.DiffResult  `json:"changed_vs,omitempty"`
	Zoom        *zoomInfo           `json:"zoom,omitempty"`
}

type buttonInfo struct {
	Found  bool    `json:"found"`
	X      int     `json:"x"`
	Y      int     `json:"y"`
	Rect   [4]int  `json:"rect"`
	Color  string  `json:"color,omitempty"`
	Fill   float64 `json:"fill,omitempty"`
	Region string  `json:"search_region"`
}

type zoneInfo struct {
	Valid    bool   `json:"valid"`
	BBox     [4]int `json:"bbox"`
	Contours int    `json:"contours"`
	Note     string `json:"note,omitempty"`
}

type zoomInfo struct {
	Rect string `json:"rect"`
	Cell int    `json:"cell"`
}

// parseInterspersed parses a command line whose flags may appear before, after
// or between positional arguments. Go's flag package stops at the first
// non-flag token, so `see diff a.png b.png -cols 100` would otherwise treat
// "-cols 100" as two positional arguments and report a usage error — the
// opposite of what the line looks like it does.
func parseInterspersed(fs *flag.FlagSet, args []string) ([]string, error) {
	var positional []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		rest := fs.Args()
		if len(rest) == 0 {
			return positional, nil
		}
		positional = append(positional, rest[0])
		args = rest[1:]
	}
}

// ---------------------------------------------------------------- look

func cmdLook(args []string) {
	fs := flag.NewFlagSet("look", flag.ExitOnError)
	imgPath := fs.String("img", "", "screenshot to read (default: live capture)")
	device := fs.String("device", "localhost:5555", "ADB device for live capture")
	dir := fs.String("dir", vision.DefaultStoreDir, "recording directory")
	k := fs.Float64("k", 0, "override the display scale (0 = derive from the screen size)")
	cols := fs.Int("cols", vision.DefaultCols, "render width in characters")
	cell := fs.Int("cell", 0, "source pixels per character column (0 = fit -cols)")
	rectSpec := fs.String("rect", "", "region to zoom into as x,y,w,h (rendered in addition to the overview)")
	color := fs.Bool("color", false, "render in 24-bit ANSI half blocks")
	jsonOut := fs.Bool("json", false, "emit the perception report as JSON only")
	noRender := fs.Bool("no-render", false, "skip the character render")
	doOCR := fs.Bool("ocr", true, "recognize on-screen text")
	grep := fs.String("grep", "", "only show OCR regions whose text contains this (case-insensitive)")
	rules := fs.Bool("rules", false, "print every state rule's pixel-pass score")
	why := fs.String("why", "", "explain one state rule check by check, e.g. -why Battle")
	buttons := fs.Bool("buttons", true, "locate the primary action button")
	buttonsFrac := fs.String("buttons-frac", "0.5,0.5,1.0,1.0", "action-button search window, fx0,fy0,fx1,fy1")
	record := fs.Bool("rec", false, "also store this frame in the ring buffer")
	label := fs.String("label", "", "label for the recorded frame")
	diffRef := fs.String("diff", "", `compare against a frame: a PNG path, "last", or "live"`)
	threshold := fs.Int("threshold", vision.DefaultDiffThreshold, "per-channel delta that counts as a change")
	var marks flagsList
	fs.Var(&marks, "mark", "annotate a point as X,Y[:LABEL] (repeatable)")
	fs.Parse(args)

	sh, err := takeShot(*imgPath, *device, *dir)
	if err != nil {
		fatal(err)
	}
	defer sh.Close()

	// The report must use the scale the bot itself runs with, or it describes
	// a geometry that never happens live.
	scale, scaleSource, departs := resolveDisplayScale(*k)
	cal, classifier := setupClassifier(sh.frame, scale)
	state, score := classifier.ClassifyState(sh.mat)

	rep := &perception{
		Time:        time.Now().Format("15:04:05"),
		Source:      sh.source,
		Width:       sh.frame.W,
		Height:      sh.frame.H,
		DisplayK:    cal.DisplayScale(),
		ScaleSource: scaleSource,
		DepartsBot:  departs,
		State:       state.String(),
		Score:       score,
	}
	if _, pinned := cal.DisplayScaleOverride(); pinned {
		rep.PinnedK = true
	}

	if btn, ok := vision.FindActionButton(sh.mat, buttonRegion(sh.frame, *buttonsFrac), vision.DefaultActionButtonConfig()); ok && *buttons {
		c := btn.Centre()
		rep.Button = &buttonInfo{
			Found: true, X: c.X, Y: c.Y,
			Rect:   [4]int{btn.Rect.Min.X, btn.Rect.Min.Y, btn.Rect.Dx(), btn.Rect.Dy()},
			Color:  btn.Color.String(),
			Fill:   btn.Fill,
			Region: *buttonsFrac,
		}
	}

	// The red line is the deployable-area boundary. It is only meaningful in a
	// battle: outside one the heuristic latches onto terrain and returns a
	// zone covering most of the screen, so the report says so instead of
	// letting a reader mistake it for a real deployable area.
	uiCutoff := int(float64(sh.frame.H) * 0.85)
	zone := attack.NewRedLineDetector(quietLogger()).Detect(sh.mat, uiCutoff)
	zi := &zoneInfo{Valid: zone.Valid, Contours: zone.Contours,
		BBox: [4]int{zone.BBox.Min.X, zone.BBox.Min.Y, zone.BBox.Dx(), zone.BBox.Dy()}}
	if state != game.StateBattle {
		zi.Note = "state is not Battle: treat this zone as terrain noise, not a deployable area"
	}
	rep.RedZone = zi

	if *doOCR {
		regions, err := vision.OCRTextRegions(sh.path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "see: ocr unavailable: %v\n", err)
		}
		rep.Text = regions
	}

	var ruleLines, whyLines []string
	// The rules are scored by the classifier itself, so this table cannot
	// disagree with the STATE line above it.
	if *rules {
		for _, v := range classifier.EvaluateRules(sh.mat, false) {
			mark := " "
			if v.Matched {
				mark = "*"
				rep.RulesPassed = append(rep.RulesPassed, v.State.String())
			}
			ruleLines = append(ruleLines, fmt.Sprintf("%s %-16s %2d/%-2d authored=%d prio=%-3d %s",
				mark, v.State.String(), v.Passed, v.Required, v.DeclaredMin, v.Priority, templateNote(v)))
		}
	}
	if *why != "" {
		whyLines = explainRule(classifier.EvaluateRules(sh.mat, true), *why)
	}
	if sh.frame.W < 600 || sh.frame.H < 500 {
		rep.Score = 0
		whyLines = append(whyLines,
			fmt.Sprintf("NOTE  this frame is %dx%d; ClassifyState returns Unknown for anything under 600x500 before it looks at a single rule",
				sh.frame.W, sh.frame.H))
	}

	// Marks: OCR boxes, the action button, the red zone, and any -mark points.
	// Digits then letters label them; the legend below the picture spells out
	// what each one is.
	var legend []string
	var textLines []string
	var markList []vision.Mark
	nextMark := 0
	markLabel := func() rune {
		if nextMark >= 35 {
			return 0
		}
		i := nextMark
		nextMark++
		if i < 9 {
			return rune('1' + i)
		}
		return rune('a' + (i - 9))
	}
	visible := filterRegions(rep.Text, *grep)
	for _, r := range visible {
		ch := markLabel()
		if ch == 0 {
			// Past the label alphabet: still list the region, just unmarked.
			textLines = append(textLines, "  "+r.Describe())
			continue
		}
		m := vision.MarkRect(r.Rect(), ch, r.Text)
		markList = append(markList, m)
		rep.Marks = append(rep.Marks, m.Describe())
		// The label leads the line so a reader can go straight from a glyph
		// in the picture to what it is and where it sits.
		textLines = append(textLines, fmt.Sprintf("%c %s", ch, r.Describe()))
	}
	if rep.Button != nil {
		b := rep.Button
		// Ring the button face the finder settled on, so the picture shows
		// which element the bot considers the primary action.
		box := image.Rect(b.Rect[0], b.Rect[1], b.Rect[0]+b.Rect[2], b.Rect[1]+b.Rect[3])
		m := vision.MarkRect(box, 'B', "action button "+b.Color)
		markList = append(markList, m)
		rep.Marks = append(rep.Marks, m.Describe())
		legend = append(legend, m.Describe())
	}
	if zi.Valid {
		box := image.Rect(zi.BBox[0], zi.BBox[1], zi.BBox[0]+zi.BBox[2], zi.BBox[1]+zi.BBox[3])
		m := vision.MarkRect(box, 'Z', "red zone (deployable area)")
		markList = append(markList, m)
		rep.Marks = append(rep.Marks, m.Describe())
		legend = append(legend, m.Describe())
	}
	for _, spec := range marks {
		pt, text, err := parsePoint(spec)
		if err != nil {
			fatal(err)
		}
		m := vision.MarkPoint(pt.X, pt.Y, 'T', text)
		markList = append(markList, m)
		rep.Marks = append(rep.Marks, m.Describe())
		legend = append(legend, m.Describe())
	}

	var changed *vision.DiffResult
	if *diffRef != "" {
		ref, refName, err := resolveFrame(*diffRef, *dir, *device)
		if err != nil {
			fatal(err)
		}
		d := vision.Diff(ref, sh.frame, *threshold)
		changed = &d
		rep.Changed = changed
		_ = refName
	}

	opts := vision.RenderOptions{Rect: image.Rect(0, 0, sh.frame.W, sh.frame.H), Cols: *cols, Ruler: true, Marks: markList, Color: *color}
	picture, grid := vision.Render(sh.frame, opts)
	rep.Grid = grid.Describe()

	var zoomPic string
	var zoomGrid vision.Grid
	if *rectSpec != "" {
		r, err := vision.ParseRect(*rectSpec, sh.frame.W, sh.frame.H)
		if err != nil {
			fatal(err)
		}
		zCell := *cell
		if zCell <= 0 {
			zCell = 1 // max detail: the point of a zoom
		}
		zoomPic, zoomGrid = vision.Render(sh.frame, vision.RenderOptions{
			Rect: r, Cell: zCell, Ruler: true, Marks: markList, Color: *color,
		})
		rep.Zoom = &zoomInfo{Rect: fmt.Sprintf("%d,%d,%d,%d", r.Min.X, r.Min.Y, r.Dx(), r.Dy()), Cell: zCell}
	}

	if *record {
		store, err := vision.OpenFrameStore(*dir)
		if err != nil {
			fatal(err)
		}
		entry, err := store.Add(sh.frame, vision.FrameMeta{State: state.String(), Score: score, Label: *label})
		if err != nil {
			fatal(err)
		}
		fmt.Printf("recorded frame #%d -> %s\n", entry.ID, entry.File)
	}

	if *jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(rep); err != nil {
			fatal(err)
		}
		return
	}

	fmt.Printf("=== SEE %s | %s | %dx%d | k=%.4f (%s) ===\n",
		rep.Time, rep.Source, rep.Width, rep.Height, rep.DisplayK, rep.ScaleSource)
	if rep.DepartsBot {
		fmt.Println("WARNING   the bot does not run this geometry — this report does NOT describe live behaviour")
	}
	fmt.Printf("STATE     %s score=%d\n", rep.State, rep.Score)
	if len(rep.RulesPassed) > 0 {
		fmt.Printf("RULES     pass: %s\n", strings.Join(rep.RulesPassed, ", "))
	}
	for i, line := range ruleLines {
		head := "          "
		if i == 0 {
			head = "RULES     "
		}
		fmt.Printf("%s%s\n", head, line)
	}
	for _, line := range whyLines {
		fmt.Println(line)
	}
	if rep.Button != nil {
		fmt.Printf("BUTTON    %s face %dx%d at (%d,%d) fill=%.2f  [searched %s]\n",
			rep.Button.Color, rep.Button.Rect[2], rep.Button.Rect[3],
			rep.Button.X, rep.Button.Y, rep.Button.Fill, rep.Button.Region)
	} else {
		fmt.Printf("BUTTON    none found [searched %s]\n", *buttonsFrac)
	}
	fmt.Printf("REDZONE   valid=%v bbox=%d,%d %dx%d contours=%d\n",
		zi.Valid, zi.BBox[0], zi.BBox[1], zi.BBox[2], zi.BBox[3], zi.Contours)
	if zi.Note != "" {
		fmt.Printf("          NOTE: %s\n", zi.Note)
	}
	if *doOCR {
		fmt.Printf("TEXT      %d regions", len(rep.Text))
		if *grep != "" {
			fmt.Printf(" (%d matching %q)", len(visible), *grep)
		}
		fmt.Println()
		for _, line := range textLines {
			fmt.Printf("          %s\n", line)
		}
	}
	if changed != nil {
		fmt.Printf("CHANGED   %s\n", changed.Describe())
	}
	if !*noRender {
		if len(legend) > 0 {
			fmt.Printf("MARKED    %s\n", strings.Join(legend, " | "))
		}
		fmt.Printf("GRID      %s\n", grid.Describe())
		fmt.Print(picture)
	}
	// A requested zoom prints even under -no-render: the flag suppresses the
	// whole-frame picture, not an explicit request for detail.
	if zoomPic != "" {
		fmt.Printf("ZOOM      rect=%s cell=%d (%s)\n", rep.Zoom.Rect, rep.Zoom.Cell, zoomGrid.Describe())
		fmt.Print(zoomPic)
	}
}

// ---------------------------------------------------------------- diff

func cmdDiff(args []string) {
	fs := flag.NewFlagSet("diff", flag.ExitOnError)
	device := fs.String("device", "localhost:5555", "ADB device for live capture")
	dir := fs.String("dir", vision.DefaultStoreDir, "recording directory")
	cols := fs.Int("cols", vision.DefaultCols, "render width in characters")
	threshold := fs.Int("threshold", vision.DefaultDiffThreshold, "per-channel delta that counts as a change")
	render := fs.Bool("render", false, "also render the second frame for reference")
	color := fs.Bool("color", false, "render in 24-bit ANSI half blocks")
	fs.Parse(args)

	pos, err := parseInterspersed(fs, args)
	if err != nil {
		fatal(err)
	}
	if len(pos) != 2 {
		fatal(fmt.Errorf("want two frames: see diff A B (A/B = PNG path, \"last\" or \"live\")"))
	}
	fa, nameA, err := resolveFrame(pos[0], *dir, *device)
	if err != nil {
		fatal(err)
	}
	fb, nameB, err := resolveFrame(pos[1], *dir, *device)
	if err != nil {
		fatal(err)
	}
	res := vision.Diff(fa, fb, *threshold)
	fmt.Printf("=== SEE DIFF ===\n")
	fmt.Printf("A  %s  %dx%d\n", nameA, fa.W, fa.H)
	fmt.Printf("B  %s  %dx%d\n", nameB, fb.W, fb.H)
	fmt.Printf("RESULT  %s\n", res.Describe())
	m, grid, res2 := vision.ChangeMap(fa, fb, vision.RenderOptions{
		Rect: image.Rect(0, 0, fb.W, fb.H), Cols: *cols, Ruler: true, Color: *color,
	}, *threshold)
	if res2.Comparable {
		fmt.Printf("LEGEND  '#' = changed pixels, '.' = tiny difference, ' ' = identical\n")
	}
	fmt.Printf("GRID    %s\n", grid.Describe())
	fmt.Print(m)
	if *render {
		pic, g := vision.Render(fb, vision.RenderOptions{
			Rect: image.Rect(0, 0, fb.W, fb.H), Cols: *cols, Ruler: true, Color: *color,
		})
		fmt.Printf("--- frame B ---\nGRID    %s\n", g.Describe())
		fmt.Print(pic)
	}
}

// resolveFrame turns a diff operand into a frame: a PNG path, "last" for the
// newest recorded frame, or "live" for a fresh capture.
func resolveFrame(spec, dir, device string) (vision.Frame, string, error) {
	switch strings.ToLower(strings.TrimSpace(spec)) {
	case "last":
		store, err := vision.OpenFrameStore(dir)
		if err != nil {
			return vision.Frame{}, "", err
		}
		e, err := store.Last()
		if err != nil {
			return vision.Frame{}, "", err
		}
		if e == nil {
			return vision.Frame{}, "", fmt.Errorf("no recorded frames in %s yet", dir)
		}
		f, err := store.Load(*e)
		if err != nil {
			return vision.Frame{}, "", err
		}
		return f, fmt.Sprintf("#%d %s %s", e.ID, e.Time, e.State), nil
	case "live":
		sh, err := takeShot("", device, dir)
		if err != nil {
			return vision.Frame{}, "", err
		}
		defer sh.Close()
		return sh.frame.Copy(), "live " + device, nil
	default:
		f, err := vision.LoadPNG(spec)
		return f, spec, err
	}
}

// ---------------------------------------------------------------- record

// pruneEvery is the default number of frames the recorder writes between
// rolling prunes. The store therefore never holds more than keep+pruneEvery
// frames (keep+keep when the operator asked to keep fewer than this).
const pruneEvery = 25

func cmdRec(args []string) {
	fs := flag.NewFlagSet("rec", flag.ExitOnError)
	device := fs.String("device", "localhost:5555", "ADB device to capture")
	dir := fs.String("dir", vision.DefaultStoreDir, "recording directory")
	every := fs.Duration("every", 2*time.Second, "interval between frames")
	limit := fs.Int("n", 0, "stop after this many frames (0 = until interrupted)")
	keep := fs.Int("keep", 400, "keep only the newest N frames on disk")
	minFreeMB := fs.Int("min-free-mb", int(vision.DefaultMinFreeBytes>>20), "stop recording when the volume has less than this much free (0 = no floor)")
	label := fs.String("label", "", "label every frame with this")
	stopOn := fs.String("stop-on", "", "stop when the classifier reports this state")
	printState := fs.Bool("state", true, "run the classifier on every frame")
	k := fs.Float64("k", 0, "override the display scale")
	fs.Parse(args)

	store, err := vision.OpenFrameStore(*dir)
	if err != nil {
		fatal(err)
	}
	store.SetMinFreeBytes(uint64(*minFreeMB) << 20)
	if free, err := store.FreeBytes(); err == nil {
		fmt.Printf("recording to %s every %s (ctrl-c to stop); %d MiB free, floor %d MiB\n",
			store.Dir(), *every, free>>20, store.MinFreeBytes()>>20)
		if store.MinFreeBytes() > 0 && free < store.MinFreeBytes() {
			fatal(fmt.Errorf("only %d MiB free in %s, below the %d MiB floor — free space or pass -min-free-mb 0 to override",
				free>>20, store.Dir(), store.MinFreeBytes()>>20))
		}
	} else {
		fmt.Printf("recording to %s every %s (ctrl-c to stop)\n", store.Dir(), *every)
	}
	if pruned, err := store.Prune(*keep); err == nil && pruned > 0 {
		fmt.Printf("pruned %d old frames (keeping %d)\n", pruned, *keep)
	}
	pruneInterval := pruneEvery
	if *keep > 0 && *keep < pruneInterval {
		pruneInterval = *keep
	}

	client := adb.NewClient(
		adb.WithHost("127.0.0.1"),
		adb.WithPort(5037),
		adb.WithTimeout(30*time.Second),
	)
	client.DeviceID = *device
	defer client.Close()

	var classifier *game.Classifier
	var n int
	for {
		m, err := client.CaptureToMat()
		if err != nil {
			fmt.Fprintf(os.Stderr, "capture: %v\n", err)
			time.Sleep(*every)
			continue
		}
		if m.Empty() {
			m.Close()
			time.Sleep(*every)
			continue
		}
		f := vision.FrameFromMat(m).Copy()
		stateName, score := "?", 0
		if *printState {
			if classifier == nil {
				scale, source, departs := resolveDisplayScale(*k)
				cal, c := setupClassifier(f, scale)
				classifier = c
				fmt.Printf("classifying at k=%.4f (%s)\n", cal.DisplayScale(), source)
				if departs {
					fmt.Printf("WARNING: the bot does not run this geometry — recorded states will not match live behaviour\n")
				}
			}
			st, sc := classifier.ClassifyState(m)
			stateName, score = st.String(), sc
		}
		m.Close()
		entry, err := store.Add(f, vision.FrameMeta{State: stateName, Score: score, Label: *label})
		if err != nil {
			// A full volume is not a transient error: stop instead of spinning
			// on it forever, which is what turns a runaway recording into a full
			// machine disk.
			if errors.Is(err, vision.ErrLowDisk) {
				fmt.Fprintf(os.Stderr, "stopping recording: %v\n", err)
				return
			}
			fmt.Fprintf(os.Stderr, "record: %v\n", err)
			continue
		}
		n++
		fmt.Println(entry.Line())
		// Prune on a rolling basis, not only at startup: an -every 2s run left
		// alone for a day writes ~43k frames, and a store that only prunes once
		// up front never bounds that. The interval follows keep so the store
		// never holds more than keep+interval frames (~600 MB at the defaults).
		if *keep > 0 && n%pruneInterval == 0 {
			if pruned, err := store.Prune(*keep); err == nil && pruned > 0 {
				fmt.Printf("pruned %d old frames (keeping %d)\n", pruned, *keep)
			}
		}
		if *stopOn != "" && strings.EqualFold(stateName, *stopOn) {
			fmt.Printf("stopping: reached state %s\n", stateName)
			return
		}
		if *limit > 0 && n >= *limit {
			return
		}
		time.Sleep(*every)
	}
}

// ---------------------------------------------------------------- history

func cmdShots(args []string) {
	fs := flag.NewFlagSet("shots", flag.ExitOnError)
	dir := fs.String("dir", vision.DefaultStoreDir, "recording directory")
	n := fs.Int("n", 20, "how many frames to list, newest last")
	fs.Parse(args)
	entries, err := recentFrames(*dir, *n)
	if err != nil {
		fatal(err)
	}
	fmt.Print(vision.Timeline(entries))
}

func cmdTimeline(args []string) {
	fs := flag.NewFlagSet("timeline", flag.ExitOnError)
	dir := fs.String("dir", vision.DefaultStoreDir, "recording directory")
	n := fs.Int("n", 60, "how many frames to summarize")
	fs.Parse(args)
	entries, err := recentFrames(*dir, *n)
	if err != nil {
		fatal(err)
	}
	if len(entries) == 0 {
		fmt.Println("(no recorded frames)")
		return
	}
	fmt.Print(vision.Timeline(entries))
	fmt.Println()
	fmt.Println("--- state transitions ---")
	prev := ""
	for _, e := range entries {
		if e.State != prev {
			fmt.Printf("  %5d  %s  -> %s\n", e.ID, e.Time, e.State)
			prev = e.State
		}
	}
}

func cmdShow(args []string) {
	fs := flag.NewFlagSet("show", flag.ExitOnError)
	dir := fs.String("dir", vision.DefaultStoreDir, "recording directory")
	cols := fs.Int("cols", vision.DefaultCols, "render width in characters")
	cell := fs.Int("cell", 0, "source pixels per character column (0 = fit -cols)")
	rectSpec := fs.String("rect", "", "region to zoom into as x,y,w,h")
	color := fs.Bool("color", false, "render in 24-bit ANSI half blocks")
	doOCR := fs.Bool("ocr", false, "recognize on-screen text")
	pos, err := parseInterspersed(fs, args)
	if err != nil {
		fatal(err)
	}
	if len(pos) != 1 {
		fatal(fmt.Errorf(`want one frame: see show last|<id>`))
	}
	store, err := vision.OpenFrameStore(*dir)
	if err != nil {
		fatal(err)
	}
	spec := strings.ToLower(strings.TrimSpace(pos[0]))
	var entry *vision.FrameEntry
	if spec == "last" {
		entry, err = store.Last()
	} else {
		id, convErr := strconv.Atoi(spec)
		if convErr != nil {
			fatal(fmt.Errorf("want an id or \"last\" (got %q)", spec))
		}
		entry, err = store.ByID(id)
	}
	if err != nil {
		fatal(err)
	}
	if entry == nil {
		fatal(fmt.Errorf("no recorded frames in %s", store.Dir()))
	}
	f, err := store.Load(*entry)
	if err != nil {
		fatal(err)
	}
	fmt.Printf("=== SEE SHOW #%d | %s | %s %d | %dx%d | changed %.2f%% | label %q ===\n",
		entry.ID, entry.Time, entry.State, entry.Score, entry.W, entry.H, entry.ChangedPct, entry.Label)
	if entry.Note != "" {
		fmt.Printf("NOTE      %s\n", entry.Note)
	}
	if *doOCR {
		regions, err := vision.OCRTextRegions(store.Dir() + "/" + entry.File)
		if err != nil {
			fmt.Fprintf(os.Stderr, "see: ocr unavailable: %v\n", err)
		}
		fmt.Printf("TEXT      %d regions\n", len(regions))
		for _, r := range regions {
			fmt.Printf("          %s\n", r.Describe())
		}
	}
	pic, grid := vision.Render(f, vision.RenderOptions{
		Rect: image.Rect(0, 0, f.W, f.H), Cols: *cols, Ruler: true, Color: *color,
	})
	fmt.Printf("GRID      %s\n", grid.Describe())
	fmt.Print(pic)
	if *rectSpec != "" {
		r, err := vision.ParseRect(*rectSpec, f.W, f.H)
		if err != nil {
			fatal(err)
		}
		zCell := *cell
		if zCell <= 0 {
			zCell = 1
		}
		zoom, zg := vision.Render(f, vision.RenderOptions{Rect: r, Cell: zCell, Ruler: true, Color: *color})
		fmt.Printf("ZOOM      rect=%s cell=%d (%s)\n", *rectSpec, zCell, zg.Describe())
		fmt.Print(zoom)
	}
}

// ---------------------------------------------------------------- helpers

// resolveDisplayScale decides which display scale a report should be produced
// with: an explicit -k, else the scale the device config pins for the bot,
// else 0 (derive from the screen diagonal).
//
// The distinction is not cosmetic. On the 1280x720 device this project is
// debugged on, the derived ratio is 1.3004 while the config pins 1.325, and
// that 2% moves a mapped probe by up to 30 px — enough to flip real battle and
// result frames between Battle/BattleEnd and Unknown. A report produced with
// the wrong scale is worse than no report: it looks like a classifier bug.
// The returned source string is printed in the header so a reader always knows
// which geometry a verdict describes, and departs says whether that geometry is
// one the bot actually runs.
func resolveDisplayScale(k float64) (float64, string, bool) {
	return config.ResolveDisplayScale(k)
}

// setupClassifier builds the calibration and classifier the bot itself uses,
// so a report reflects the bot's perception rather than a parallel one.
func setupClassifier(f vision.Frame, k float64) (*game.Calibration, *game.Classifier) {
	cal := game.NewCalibration(f.W, f.H)
	cal.Verified = true
	if k > 0 {
		cal.SetDisplayScale(k)
	}
	classifier := game.NewClassifier(cal, game.DefaultClassifierConfig(), quietLogger())
	if ts, err := game.NewTemplateStore(paths.Resolve("templates")); err == nil {
		ts.LoadTemplates()
		classifier.SetTemplates(ts)
	}
	return cal, classifier
}

// explainRule prints one state rule probe by probe: where the probe was
// authored, where the live geometry put it, what colour it wanted, what the
// frame actually has there, and whether that satisfies the tolerance. When a
// state misclassifies, this is the difference between "the classifier says
// Unknown" and "every probe landed on a panel that is dimmed to half
// brightness".
func explainRule(verdicts []game.RuleVerdict, state string) []string {
	for _, v := range verdicts {
		if !strings.EqualFold(v.State.String(), state) {
			continue
		}
		lines := []string{fmt.Sprintf("WHY  %s  passed=%d required=%d (authored %d) checks=%d skipped=%d %s",
			v.State.String(), v.Passed, v.Required, v.DeclaredMin, v.Checks, v.Skipped, templateNote(v))}
		var ratios []float64
		for _, c := range v.Detail {
			if c.Skipped {
				lines = append(lines, fmt.Sprintf("     ref(%4d,%4d) -> (%4d,%4d) off-screen: not counted", c.X, c.Y, c.SX, c.SY))
				continue
			}
			verdict := "miss"
			if c.Passed {
				verdict = "HIT"
			} else if wantLum, gotLum := luminance(c.Want), luminance(c.Got); wantLum > 60 {
				ratios = append(ratios, float64(gotLum)/float64(wantLum))
			}
			lines = append(lines, fmt.Sprintf("     ref(%4d,%4d) -> (%4d,%4d) want RGB(%3d,%3d,%3d)±%-3d got RGB(%3d,%3d,%3d) d=%-6.1f %s",
				c.X, c.Y, c.SX, c.SY, c.Want[0], c.Want[1], c.Want[2], c.Tolerance,
				c.Got[0], c.Got[1], c.Got[2], c.Distance, verdict))
		}
		if note := dimmingNote(ratios); note != "" {
			lines = append(lines, note)
		}
		lines = append(lines, fmt.Sprintf("     -> %s", verdictWord(v)))
		return lines
	}
	return []string{fmt.Sprintf("WHY  no rule named %q", state)}
}

// templateNote describes a rule's optional template requirement. Template
// matching only runs once the pixel bar is met, so "not attempted" is a
// distinct fact from "matching ran and missed".
func templateNote(v game.RuleVerdict) string {
	if v.Template == "" {
		return "no template"
	}
	if !v.PixelOK {
		return fmt.Sprintf("template %s not attempted (pixel bar unmet)", v.Template)
	}
	return fmt.Sprintf("template %s ok=%v conf=%.2f", v.Template, v.TemplateOK, v.TemplateConf)
}

// verdictWord states whether the rule cleared its evidence bar and why. Every
// condition that can reject a rule is named, because "does not match" alone is
// what makes a misclassification expensive to chase.
func verdictWord(v game.RuleVerdict) string {
	if v.Matched {
		return fmt.Sprintf("MATCHES (score %d); this is what the STATE line reports", v.Score)
	}
	var reasons []string
	if v.Skipped == v.Checks && v.Checks > 0 {
		return "does NOT match: no probe landed on screen"
	}
	if v.Checks > 0 && v.Passed < v.Required {
		reasons = append(reasons, fmt.Sprintf("only %d of %d probes hit (bar is %d)", v.Passed, v.Checks, v.Required))
	}
	if v.Template != "" && !v.TemplateOK {
		if v.PixelOK {
			reasons = append(reasons, fmt.Sprintf("the %s template did not match", v.Template))
		} else {
			reasons = append(reasons, fmt.Sprintf("the %s template was never tried", v.Template))
		}
	}
	if len(reasons) == 0 {
		return "does NOT match"
	}
	return "does NOT match: " + strings.Join(reasons, "; ")
}

// dimmingNote reports a uniform brightness shortfall across the failed probes.
//
// This is the signature of a translucent overlay (a reward popup, a
// disconnected banner) laid over the screen: every probe finds its feature in
// roughly the right place, just multiplied down. Without this note the symptom
// reads like a geometry error and sends you looking at the wrong thing.
func dimmingNote(ratios []float64) string {
	if len(ratios) < 3 {
		return ""
	}
	sort.Float64s(ratios)
	median := ratios[len(ratios)/2]
	if median < 0.30 || median > 0.85 {
		return ""
	}
	return fmt.Sprintf("     NOTE  the failed probes read uniformly %.0f%% as bright as their targets across %d samples: "+
		"that is a translucent overlay dimming the screen (a popup), not a geometry error", median*100, len(ratios))
}

func luminance(c [3]int) int { return (c[0]*299 + c[1]*587 + c[2]*114) / 1000 }

func buttonRegion(f vision.Frame, frac string) image.Rectangle {
	parts := strings.Split(frac, ",")
	if len(parts) != 4 {
		return vision.FractionRect(f.W, f.H, 0.5, 0.5, 1, 1)
	}
	var v [4]float64
	for i, p := range parts {
		x, err := strconv.ParseFloat(strings.TrimSpace(p), 64)
		if err != nil {
			return vision.FractionRect(f.W, f.H, 0.5, 0.5, 1, 1)
		}
		v[i] = x
	}
	return vision.FractionRect(f.W, f.H, v[0], v[1], v[2], v[3])
}

func filterRegions(regions []vision.TextRegion, grep string) []vision.TextRegion {
	if strings.TrimSpace(grep) == "" {
		out := make([]vision.TextRegion, len(regions))
		copy(out, regions)
		return out
	}
	needle := strings.ToLower(grep)
	var out []vision.TextRegion
	for _, r := range regions {
		if strings.Contains(strings.ToLower(r.Text), needle) {
			out = append(out, r)
		}
	}
	return out
}

func recentFrames(dir string, n int) ([]vision.FrameEntry, error) {
	store, err := vision.OpenFrameStore(dir)
	if err != nil {
		return nil, err
	}
	return store.Recent(n)
}

// storePath is the scratch file a live frame is written to when a helper
// needs a path rather than a buffer (the OCR helper reads files).
func storePath(dir, name string) string {
	full := dir + "/" + name
	os.MkdirAll(dir, 0o755)
	return full
}

func parsePoint(spec string) (image.Point, string, error) {
	coord, label, _ := strings.Cut(spec, ":")
	parts := strings.Split(coord, ",")
	if len(parts) != 2 {
		return image.Point{}, "", fmt.Errorf("want X,Y[:LABEL] (got %q)", spec)
	}
	x, err := strconv.Atoi(strings.TrimSpace(parts[0]))
	if err != nil {
		return image.Point{}, "", fmt.Errorf("bad X in %q", spec)
	}
	y, err := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err != nil {
		return image.Point{}, "", fmt.Errorf("bad Y in %q", spec)
	}
	if label == "" {
		label = "tap"
	}
	return image.Pt(x, y), label, nil
}

func quietLogger() zerolog.Logger {
	return zerolog.New(zerolog.ConsoleWriter{Out: os.Stderr, NoColor: true}).Level(zerolog.ErrorLevel)
}

func fatal(err error) {
	fmt.Fprintf(os.Stderr, "see: %v\n", err)
	os.Exit(1)
}

// flagsList collects a repeatable string flag.
type flagsList []string

func (f *flagsList) String() string { return strings.Join(*f, ",") }

func (f *flagsList) Set(v string) error {
	*f = append(*f, v)
	return nil
}
