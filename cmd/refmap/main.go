// refmap converts rectangles a human dragged on a live frame into the reference
// coordinates the bot's asset files are written in.
//
// The wall-flow assets are consumed through game.Calibration's HUD law (HudRect):
// a stored value is an 860x732 reference coordinate that the bot re-maps onto
// whatever device it is running on. The picking tools capture PHYSICAL pixels, so
// something has to invert the law — and inverting it is not a division.
// tools/select_wall_upgrade_buttons.py used to write its drags straight into
// assets/wall_upgrade_buttons.json, which the loader then read as reference
// values and re-mapped: the recorded boxes moved on every run, and the live
// 1280x720 session proved it (the boxes landed on builder-menu rows, and the gate
// in internal/bot/wall_upgrade.go now refuses to tap when they do).
//
// Usage, for every rect a picking session captured (one per line, physical
// pixels; the key may be omitted for a single-rect schema):
//
//	refmap -w 1280 -h 720 -k 1.325 -schema buttons <<'EOF'
//	gold   591 503 692 601
//	elixir 717 503 816 601
//	EOF
//
// Output is the asset file's exact contents, so a picker writes stdout to disk
// unchanged. Diagnostics — how far the recorded box sits from the drag, and
// whether the drag fell in a band seam the reference frame cannot express — go to
// stderr, because a JSON asset must not carry a comment.
//
// -schema wall accepts a whole session at once and, with -write, saves each key
// to the asset file that consumes it — stdout printing would have to interleave
// several JSON documents, so a multi-file set demands -write.
//
// Run with -describe to list the schemas.
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"image"
	"io"
	"os"
	"slices"
	"strconv"
	"strings"

	"github.com/Ducky705/ClashGO/internal/config"
	"github.com/Ducky705/ClashGO/internal/game"
	"github.com/Ducky705/ClashGO/internal/paths"
)

// schemas maps a schema name to its ordered keys. Each schema is one asset file:
// the key list is both the accepted input labels (in order, so a picker that
// captured boxes in sequence can omit them) and the JSON layout.
//
// Every schema here is rect-shaped. The legacy assets/wall_upgrade_modal.json is
// a bare point consumed through Hud rather than HudRect, and nothing in the
// asset-driven wall flow reads it any more.
var schemas = map[string][]string{
	"wall":    wallKeys,
	"buttons": {"gold", "elixir"},
	"x_roi":   {"x_popup_roi", "x_popup_roi_alt"},
	"confirm": {"confirm_button"},
	"menu":    {"physical"},
	"builder": {"builder_button"},
}

// wallKeys is the whole wall-upgrade flow as one schema, in the order the
// sequence reads its controls. A picking session captures all of them in one
// sitting because a box only means something against the frame it was drawn on,
// and every frame has to come from the same parked game.
var wallKeys = []string{
	"builder_button", "physical", "gold", "elixir",
	"confirm_button", "x_popup_roi", "x_popup_roi_alt",
}

// keyFile routes a schema key to the asset file the bot reads it from. Several
// keys share a file (both upgrade chips live in wall_upgrade_buttons.json),
// which is why -write groups by file rather than writing per schema.
// topLevelEntry names keys whose asset file holds one rect block pair at its top
// level instead of a key per entry (see writeFiles).
var topLevelEntry = map[string]bool{"physical": true}

var keyFile = map[string]string{
	"builder_button":  "builder_button.json",
	"physical":        "builder_menu_roi.json",
	"gold":            "wall_upgrade_buttons.json",
	"elixir":          "wall_upgrade_buttons.json",
	"confirm_button":  "wall_upgrade_confirm.json",
	"x_popup_roi":     "wall_upgrade_x_roi.json",
	"x_popup_roi_alt": "wall_upgrade_x_roi.json",
}

// tolLadder is tried in order: a drag whose rect cannot be reproduced takes the
// first tolerance that works. seamTol is where the tool stops calling the result
// precision and starts calling it a band seam — the smallest offset a seam miss
// produces is ~80 px, so anything past a few pixels is that.
var (
	tolLadder = []int{1, 4, 16, 96}
	seamTol   = 4
)

type rectBlock struct {
	X1 int `json:"x1"`
	Y1 int `json:"y1"`
	X2 int `json:"x2"`
	Y2 int `json:"y2"`
}

func (b rectBlock) rect() image.Rectangle {
	return image.Rect(min(b.X1, b.X2), min(b.Y1, b.Y2), max(b.X1, b.X2), max(b.Y1, b.Y2))
}

func fromRect(r image.Rectangle) rectBlock {
	return rectBlock{X1: r.Min.X, Y1: r.Min.Y, X2: r.Max.X, Y2: r.Max.Y}
}

// refEntry is one key's record in an asset file: the drag in physical pixels and
// the reference rect that maps back onto it. The loaders prefer `reference`;
// keeping `physical` beside it is what lets a later tool show the user what they
// actually dragged.
type refEntry struct {
	Physical  rectBlock `json:"physical"`
	Reference rectBlock `json:"reference"`
}

func main() {
	os.Exit(run())
}

func run() int {
	var (
		schema   = flag.String("schema", "", "asset schema: "+schemaList())
		w        = flag.Int("w", 0, "live frame width in pixels (the screencap size)")
		h        = flag.Int("h", 0, "live frame height in pixels")
		k        = flag.Float64("k", 0, "override the display scale (0 = the scale the bot pins for this device, else the frame diagonal)")
		optional = flag.String("optional", "", "comma-separated keys that may be absent (a chained-popup alt the user skipped)")
		write    = flag.Bool("write", false, "write the asset files in place instead of printing them to stdout")
		describe = flag.Bool("describe", false, "list the schemas and their keys, then exit")
	)
	flag.Parse()

	if *describe {
		for _, name := range sortedSchemas() {
			fmt.Printf("%s: %s\n", name, strings.Join(schemas[name], ", "))
		}
		return 0
	}
	keys, ok := schemas[*schema]
	if !ok {
		fmt.Fprintf(os.Stderr, "refmap: unknown -schema %q (want one of: %s)\n", *schema, schemaList())
		return 2
	}
	if *w <= 0 || *h <= 0 {
		fmt.Fprintln(os.Stderr, "refmap: -w and -h are required (the device's screenshot size)")
		return 2
	}

	// The scale defaults to the one the bot will read this asset back with. The
	// assets are re-mapped through the bot's Calibration, which is pinned from the
	// device config, so a picker that used the frame diagonal (a different number
	// whenever device.display_scale is set) would record boxes against a mapping
	// the bot never applies.
	cal := game.NewCalibration(*w, *h)
	scale, source, departs := config.ResolveDisplayScale(*k)
	if scale > 0 {
		cal.SetDisplayScale(scale)
	}
	full := fmt.Sprintf("%dx%d k=%.4f (%s)", *w, *h, cal.DisplayScale(), source)
	if departs {
		fmt.Fprintf(os.Stderr, "refmap: WARNING %s — the recorded boxes will not describe what the bot does\n", full)
	} else {
		fmt.Fprintf(os.Stderr, "refmap: %s, schema=%s\n", full, *schema)
	}

	drags, err := readDrags(os.Stdin, keys)
	if err != nil {
		fmt.Fprintf(os.Stderr, "refmap: %v\n", err)
		return 2
	}

	opt := map[string]bool{}
	for _, name := range strings.Split(*optional, ",") {
		if name = strings.TrimSpace(name); name != "" {
			if !slices.Contains(keys, name) {
				fmt.Fprintf(os.Stderr, "refmap: -optional %q is not part of schema %s\n", name, *schema)
				return 2
			}
			opt[name] = true
		}
	}

	out := make(map[string]refEntry, len(keys))
	worst := 0
	emitted := 0
	for _, key := range keys {
		target, ok := drags[key]
		if !ok {
			if opt[key] {
				fmt.Fprintf(os.Stderr, "  %-16s skipped (optional, not given)\n", key)
				continue
			}
			fmt.Fprintf(os.Stderr, "refmap: no rect given for key %q\n", key)
			return 2
		}
		emitted++
		ref, mapped, delta, ok := snap(cal, key, target)
		if !ok {
			fmt.Fprintf(os.Stderr,
				"refmap: %s %v cannot be recorded: its centre falls in a band seam no reference rect reaches. Move the box ~80 px and drag again.\n",
				key, target)
			return 3
		}
		if delta > worst {
			worst = delta
		}
		note := "exact"
		if delta > 0 {
			note = fmt.Sprintf("%d px off", delta)
		}
		fmt.Fprintf(os.Stderr, "  %-16s drag %v -> live %v (%s), ref %v\n", key, target, mapped, note, ref)
		out[key] = refEntry{Physical: fromRect(target), Reference: fromRect(ref)}
	}

	if emitted == 0 {
		fmt.Fprintln(os.Stderr, "refmap: no rects given, nothing to write")
		return 2
	}

	// Report the fit before acting on it: a drag the reference frame can only
	// express ~80 px away is a band seam, and in -write mode this warning is the
	// last moment it can be seen before the asset file is overwritten.
	fmt.Fprintf(os.Stderr, "refmap: worst recorded offset %d px\n", worst)
	if worst > seamTol {
		fmt.Fprintln(os.Stderr,
			"refmap: WARNING a recorded box sits further than a few pixels from the drag — that is a band seam, not precision. Check the numbers above before using this asset.")
	}

	if *write {
		return writeFiles(out)
	}
	if files := targetFiles(out); len(files) > 1 {
		fmt.Fprintf(os.Stderr,
			"refmap: schema %s spans %d asset files (%s); re-run with -write so each file gets its own keys\n",
			*schema, len(files), strings.Join(files, ", "))
		return 2
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(out); err != nil {
		fmt.Fprintf(os.Stderr, "refmap: encode: %v\n", err)
		return 1
	}
	return 0
}

// writeFiles saves each recorded key to the asset file that consumes it. One
// session therefore updates every file the flow reads, which is the only way the
// boxes stay consistent with each other: they were all dragged on frames of the
// same parked game, and one of them written against a different sitting is a
// box that lands somewhere else at run time.
func writeFiles(out map[string]refEntry) int {
	byFile := map[string]map[string]refEntry{}
	for key, entry := range out {
		file := keyFile[key]
		if file == "" {
			fmt.Fprintf(os.Stderr, "refmap: no asset file is known for key %q\n", key)
			return 2
		}
		if byFile[file] == nil {
			byFile[file] = map[string]refEntry{}
		}
		byFile[file][key] = entry
	}
	for _, file := range targetFiles(out) {
		entries := byFile[file]
		// A file whose top level IS the block pair takes the entry's own fields,
		// not an entry named after its key: nesting it would produce
		// {"physical":{"physical":…,"reference":…}}, which the loader's
		// map[string]int cannot decode — and it would then silently keep its
		// default ROI rather than reporting a malformed asset.
		var payload any = entries
		if len(entries) == 1 {
			for key, entry := range entries {
				if topLevelEntry[key] {
					payload = entry
				}
			}
		}
		blob, err := json.MarshalIndent(payload, "", "  ")
		if err != nil {
			fmt.Fprintf(os.Stderr, "refmap: encode %s: %v\n", file, err)
			return 1
		}
		path := paths.Resolve(file)
		if err := os.WriteFile(path, append(blob, '\n'), 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "refmap: write %s: %v\n", path, err)
			return 1
		}
		fmt.Printf("%s\n", path)
	}
	return 0
}

// targetFiles lists the asset files a recorded set spans, sorted so the output
// order of a -write run is stable.
func targetFiles(out map[string]refEntry) []string {
	seen := map[string]bool{}
	var files []string
	for key := range out {
		file := keyFile[key]
		if file != "" && !seen[file] {
			seen[file] = true
			files = append(files, file)
		}
	}
	slices.Sort(files)
	return files
}

// cornerKeys are the keys whose consumer maps its rect corner by corner through
// Hud. Every wall asset but one is read as an assetRect, and that reader goes
// through Rect.Live -> HudRect, which anchors both edges from the rect's centre;
// the builder-menu ROI loader maps each corner with its own band instead. A
// session has to invert the law its own consumer uses: the two agree on the
// device the drag came from and disagree on every other, so inverting the wrong
// one writes a plausible file that moves the box on a resize.
var cornerKeys = map[string]bool{"physical": true}

// snap walks the tolerance ladder, so a drag only a looser fit can express still
// produces an asset — and still reports how far it had to move.
func snap(cal *game.Calibration, key string, target image.Rectangle) (ref, mapped image.Rectangle, delta int, ok bool) {
	for _, tol := range tolLadder {
		var r, m image.Rectangle
		var found bool
		if cornerKeys[key] {
			r, m, found = cal.HudCornersReferenceSnap(target, tol)
		} else {
			r, m, found = cal.HudRectReferenceSnap(target, tol)
		}
		if !found {
			continue
		}
		return r, m, game.RectDelta(m, target), true
	}
	return image.Rectangle{}, image.Rectangle{}, 0, false
}

// readDrags parses `key x1 y1 x2 y2` lines, or bare `x1 y1 x2 y2` when the schema
// has a single key (a picker that captured one box has no name to give). Blank
// lines and # comments are ignored; commas are accepted as separators so a line
// can be pasted straight out of a log.
func readDrags(r io.Reader, keys []string) (map[string]image.Rectangle, error) {
	out := make(map[string]image.Rectangle)
	single := len(keys) == 1
	sc := bufio.NewScanner(r)
	for line := 1; sc.Scan(); line++ {
		text := strings.TrimSpace(sc.Text())
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		fields := strings.FieldsFunc(text, func(c rune) bool {
			return c == ' ' || c == '\t' || c == ','
		})
		key := ""
		switch {
		case len(fields) == 5:
			key, fields = fields[0], fields[1:]
		case len(fields) == 4 && single:
			key = keys[0]
		default:
			return nil, fmt.Errorf("line %d: want 'key x1 y1 x2 y2' or 'x1 y1 x2 y2', got %q", line, text)
		}
		if !slices.Contains(keys, key) {
			return nil, fmt.Errorf("line %d: key %q is not part of this schema (%s)", line, key, strings.Join(keys, ", "))
		}
		nums := make([]int, 4)
		for i, f := range fields {
			n, err := strconv.Atoi(f)
			if err != nil {
				return nil, fmt.Errorf("line %d: %q is not an integer", line, f)
			}
			nums[i] = n
		}
		if _, dup := out[key]; dup {
			return nil, fmt.Errorf("line %d: key %q given twice", line, key)
		}
		out[key] = image.Rect(
			min(nums[0], nums[2]), min(nums[1], nums[3]),
			max(nums[0], nums[2]), max(nums[1], nums[3]),
		)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func schemaList() string { return strings.Join(sortedSchemas(), ", ") }

func sortedSchemas() []string {
	out := make([]string, 0, len(schemas))
	for name := range schemas {
		out = append(out, name)
	}
	slices.Sort(out)
	return out
}
