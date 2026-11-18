package game

// accountcapture.go installs the switch templates from the game's own screens,
// with no hand cropping.
//
// A cold start used to need an operator to screenshot, measure and crop four
// PNGs before the switcher would run. That manual step was the bug: the
// regions are derivable from the screens themselves — the player banner is the
// top-left text, the Supercell ID row is the text that says so, and the
// account rows are the player tags — so the helper detects them, derives the
// row geometry from the panel's real rows, writes everything into the directory
// the runtime reads, and proves each file matches back before it is kept.
//
// Detection needs OCR. Tests and offline runs inject SwitchTextReader instead
// (see accountcapture_test.go), which keeps this package runnable without
// macOS or a device.

import (
	"encoding/json"
	"fmt"
	"image"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Ducky705/ClashGO/internal/paths"
	"github.com/Ducky705/ClashGO/internal/vision"
	"gocv.io/x/gocv"
)

// SwitchFrames are the screens the switch walks through, captured in the order
// the runtime taps them. There is no village frame: the profile is addressed by
// geometry, and the banner carries the current player's name.
type SwitchFrames struct {
	Profile  gocv.Mat
	Settings gocv.Mat
	Panel    gocv.Mat
}

// Close releases the captured frames.
func (f SwitchFrames) Close() {
	for _, m := range []gocv.Mat{f.Profile, f.Settings, f.Panel} {
		if !m.Empty() {
			m.Close()
		}
	}
}

// SwitchTemplateRegions are the crops the runtime templates come from.
type SwitchTemplateRegions struct {
	Settings  image.Rectangle // Settings tab/gear, profile screen
	Supercell image.Rectangle // Supercell ID row or its Open button
	SlotList  image.Rectangle // first account row, panel screen
}

// SwitchTextReader returns the text on a frame. OCRSwitchText is the real one;
// tests inject fixture text so the install path never needs a device.
type SwitchTextReader func(frame string, screen gocv.Mat) ([]vision.TextRegion, error)

// OCRSwitchText reads text regions from a frame using the macOS Vision helper.
// Shared by identity verification and template capture so both see the same
// boxes.
func OCRSwitchText(screen gocv.Mat) ([]vision.TextRegion, error) {
	if screen.Empty() {
		return nil, fmt.Errorf("empty frame")
	}
	path := filepath.Join(os.TempDir(), fmt.Sprintf("clashgo_capture_%d.png", time.Now().UnixNano()))
	if !gocv.IMWrite(path, screen) {
		return nil, fmt.Errorf("write frame to %s", path)
	}
	defer func() { _ = os.Remove(path) }()
	return vision.OCRTextRegions(path)
}

// switchText calls the reader and returns regions top-to-bottom.
func switchText(reader SwitchTextReader, frame string, screen gocv.Mat) ([]vision.TextRegion, error) {
	if reader == nil {
		reader = func(_ string, m gocv.Mat) ([]vision.TextRegion, error) { return OCRSwitchText(m) }
	}
	regions, err := reader(frame, screen)
	if err != nil {
		return nil, fmt.Errorf("read the screen's text: %w", err)
	}
	return vision.SortedRegions(regions), nil
}

// DetectSettingsRegion finds the control that opens the Settings panel. Current
// builds show a "Settings" tab on the profile screen, so its text is the first
// choice; older icon-only layouts fall back to the HUD's top-right corner.
func DetectSettingsRegion(screen gocv.Mat, text SwitchTextReader) image.Rectangle {
	regions, err := switchText(text, "profile", screen)
	if err == nil {
		for _, region := range regions {
			flat := strings.ToLower(strings.Join(strings.Fields(region.Text), ""))
			if flat == "settings" || strings.HasPrefix(flat, "settings") {
				return clampRect(padRect(region.Rect(), region.H/2), screen.Cols(), screen.Rows())
			}
		}
	}
	return hudSettingsRegion(screen)
}

// hudSettingsRegion is the icon-only fallback: a corner control keeps its
// distance to the screen edge at any resolution.
func hudSettingsRegion(screen gocv.Mat) image.Rectangle {
	w, h := screen.Cols(), screen.Rows()
	return clampRect(image.Rect(int(0.90*float64(w)), int(0.005*float64(h)), int(0.995*float64(w)), int(0.090*float64(h))), w, h)
}

// DetectSupercellRegion finds the "Supercell ID" row in Settings and returns
// the text box padded into a button-shaped template.
func DetectSupercellRegion(screen gocv.Mat, text SwitchTextReader) (image.Rectangle, error) {
	w, h := screen.Cols(), screen.Rows()
	regions, err := switchText(text, "settings", screen)
	if err != nil {
		return image.Rectangle{}, err
	}
	var heading *vision.TextRegion
	for i := range regions {
		flat := strings.ToLower(strings.Join(strings.Fields(regions[i].Text), ""))
		if strings.Contains(flat, "supercell") {
			heading = &regions[i]
			break
		}
	}
	if heading == nil {
		return image.Rectangle{}, fmt.Errorf("no \"Supercell ID\" text on this screen — open the profile (top-left banner) or Settings and re-run")
	}
	// The row's tap target is the Open button beside it: the panel that lists
	// the signed-in villages opens from there.
	for _, region := range regions {
		flat := strings.ToLower(strings.Join(strings.Fields(region.Text), ""))
		if !strings.Contains(flat, "open") {
			continue
		}
		if region.Y >= heading.Y-region.H && region.Y <= heading.Y+3*heading.H && region.X > heading.X+heading.W {
			return clampRect(padRect(region.Rect(), region.H), w, h), nil
		}
	}
	return clampRect(padRect(heading.Rect(), heading.H), w, h), nil
}

// DetectSlotListRegion finds the account rows on the Supercell ID panel. The
// player tags are what makes a row a row: names repeat between villages, tags
// do not. The first tag becomes the anchor; every row centre feeds the derived
// geometry.
func DetectSlotListRegion(screen gocv.Mat, text SwitchTextReader) ([]image.Point, image.Rectangle, error) {
	w, h := screen.Cols(), screen.Rows()
	regions, err := switchText(text, "panel", screen)
	if err != nil {
		return nil, image.Rectangle{}, err
	}
	var tags []vision.TextRegion
	for _, region := range regions {
		flat := strings.ToUpper(strings.Join(strings.Fields(region.Text), ""))
		if profileTagRe.MatchString(flat) {
			tags = append(tags, region)
		}
	}
	if len(tags) < 2 {
		return nil, image.Rectangle{}, fmt.Errorf("found %d player tags on the Supercell ID panel; switching needs at least 2 signed-in villages — open Settings → Supercell ID and re-run", len(tags))
	}
	rows := make([]image.Point, 0, len(tags))
	for _, tag := range tags {
		rows = append(rows, tag.Center())
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Y < rows[j].Y })
	anchor := clampRect(padRect(tags[0].Rect(), tags[0].H/2), w, h)
	return rows, anchor, nil
}

// CaptureSwitchFrames drives the running game to each screen the installer
// crops and saves every frame under dir (so a refusal can be replayed with
// -source). It taps the profile corner, opens Settings only when Supercell ID is
// not already on the profile screen, and returns to the village afterwards.
// overrides replaces any crop the caller wants to force.
func CaptureSwitchFrames(dev Device, dir string, settle time.Duration, overrides SwitchTemplateRegions) (SwitchFrames, SwitchTemplateRegions, error) {
	var frames SwitchFrames
	var regions SwitchTemplateRegions

	wait := func() { time.Sleep(settle) }
	capture := func(name string) (gocv.Mat, error) {
		mat, err := dev.CaptureToMat()
		if err != nil {
			return gocv.NewMat(), fmt.Errorf("capture the %s screen: %w", name, err)
		}
		if dir != "" && !gocv.IMWrite(filepath.Join(dir, name+".png"), mat) {
			fmt.Fprintf(os.Stderr, "WARN: could not save %s.png in %s\n", name, dir)
		}
		return mat, nil
	}
	tap := func(r image.Rectangle) error {
		p := image.Pt(r.Min.X+r.Dx()/2, r.Min.Y+r.Dy()/2)
		if err := dev.TapRandomized(p.X, p.Y); err != nil {
			return fmt.Errorf("tap (%d,%d): %w", p.X, p.Y, err)
		}
		wait()
		return nil
	}

	var err error
	if frames.Profile, err = capture("profile"); err != nil {
		return frames, regions, err
	}
	// Back out exactly as deep as we went: Back on the village opens the exit
	// dialog, which would break the next switch.
	depth := 0
	defer func() {
		for i := 0; i < depth; i++ {
			_ = dev.Back()
			time.Sleep(500 * time.Millisecond)
		}
	}()

	if !overrides.Settings.Empty() {
		regions.Settings = overrides.Settings
	}

	// Current builds show the Supercell ID row on the profile screen itself;
	// older ones hide it behind Settings. Probe first, tap Settings only if the
	// row is not already there.
	if !overrides.Supercell.Empty() {
		regions.Supercell = overrides.Supercell
		frames.Settings = frames.Profile.Clone()
	} else if regions.Supercell, err = DetectSupercellRegion(frames.Profile, nil); err == nil {
		frames.Settings = frames.Profile.Clone()
	} else {
		if regions.Settings.Empty() {
			regions.Settings = DetectSettingsRegion(frames.Profile, nil)
		}
		if err := tap(regions.Settings); err != nil {
			return frames, regions, err
		}
		depth++
		if frames.Settings, err = capture("settings"); err != nil {
			return frames, regions, err
		}
		if regions.Supercell, err = DetectSupercellRegion(frames.Settings, nil); err != nil {
			return frames, regions, fmt.Errorf("%w (tapped (x,y)=(%d,%d); pass -settings x1,y1,x2,y2 to point at it, or open Supercell ID by hand and install from saved frames with -source)", err, regions.Settings.Min.X+regions.Settings.Dx()/2, regions.Settings.Min.Y+regions.Settings.Dy()/2)
		}
	}

	if err := tap(regions.Supercell); err != nil {
		return frames, regions, err
	}
	depth++
	if frames.Panel, err = capture("panel"); err != nil {
		return frames, regions, err
	}
	if _, anchor, derr := DetectSlotListRegion(frames.Panel, nil); derr != nil {
		return frames, regions, derr
	} else if regions.SlotList.Empty() {
		regions.SlotList = anchor
	}
	return frames, regions, nil
}

// SwitchInstallInput is everything the installer needs. Regions, Rows and
// Anchor are optional: whatever is left empty is detected from Frames.
type SwitchInstallInput struct {
	Frames  SwitchFrames
	Text    SwitchTextReader
	Config  SwitchConfig
	Regions SwitchTemplateRegions

	// TemplateDir and ConfigPath default to the assets paths the runtime reads.
	TemplateDir string
	ConfigPath  string

	// UseConfigGeometry validates the caller's slot_row1_offset/spacing against
	// the panel instead of deriving them from its rows.
	UseConfigGeometry bool
}

// SwitchInstallReport is what one install did, for the CLI and for tests.
type SwitchInstallReport struct {
	Files      []string
	Regions    SwitchTemplateRegions
	Rows       []image.Point
	SlotY      []int
	Config     SwitchConfig
	ConfigPath string
}

// InstallSwitchTemplates detects the four crop regions, writes the templates
// into the runtime's template directory, verifies each installed PNG against
// the frame it came from at the runtime's own threshold, derives and validates
// the account-row geometry, and writes the config the runtime loads. It
// refuses to keep a template or a geometry that would fail later.
func InstallSwitchTemplates(in SwitchInstallInput) (SwitchInstallReport, error) {
	var report SwitchInstallReport
	if err := framesPresent(in.Frames); err != nil {
		return report, err
	}
	cfg := in.Config
	cfg.normalize()
	if in.TemplateDir == "" {
		in.TemplateDir = paths.Resolve("templates")
	}
	cfg.TemplateDir = in.TemplateDir
	if in.ConfigPath == "" {
		in.ConfigPath = paths.Resolve("account_switch.json")
	}
	report.ConfigPath = in.ConfigPath

	reader := in.Text
	if reader == nil {
		reader = func(_ string, m gocv.Mat) ([]vision.TextRegion, error) { return OCRSwitchText(m) }
	}
	regions := in.Regions
	var err error
	if regions.Settings.Empty() {
		regions.Settings = DetectSettingsRegion(in.Frames.Profile, reader)
	}
	if regions.Supercell.Empty() {
		if regions.Supercell, err = DetectSupercellRegion(in.Frames.Settings, reader); err != nil {
			return report, err
		}
	}
	rows, anchor, err := DetectSlotListRegion(in.Frames.Panel, reader)
	if err != nil {
		return report, err
	}
	if regions.SlotList.Empty() {
		regions.SlotList = anchor
	}

	// The anchor has to be the row it claims to be, or every slot tap is off.
	if !rows[0].In(regions.SlotList) {
		return report, fmt.Errorf("%s at (%d,%d)-(%d,%d) does not contain the first account row at (%d,%d); adjust -slot-list (or the detected row) and re-run",
			AcctSlotListTemplate+".png", regions.SlotList.Min.X, regions.SlotList.Min.Y, regions.SlotList.Max.X, regions.SlotList.Max.Y, rows[0].X, rows[0].Y)
	}
	// The Supercell ID template has to be the row the switch taps.
	if err := supercellCoversTarget(in.Frames.Settings, reader, regions.Supercell); err != nil {
		return report, err
	}
	// ...and the Settings template has to be the control that opens it.
	if err := settingsCoversTarget(in.Frames.Profile, reader, regions.Settings); err != nil {
		return report, err
	}

	slotY, err := validateSlotGeometry(&cfg, in.Frames.Panel.Rows(), rows, in.UseConfigGeometry)
	if err != nil {
		return report, err
	}
	report.Rows, report.SlotY, report.Config, report.Regions = rows, slotY, cfg, regions

	if err := os.MkdirAll(in.TemplateDir, 0o755); err != nil {
		return report, fmt.Errorf("create template dir %s: %w", in.TemplateDir, err)
	}
	plan := []struct {
		name   string
		screen gocv.Mat
		rect   image.Rectangle
	}{
		{AcctSettingsTemplate, in.Frames.Profile, regions.Settings},
		{AcctSupercellTemplate, in.Frames.Settings, regions.Supercell},
		{AcctSlotListTemplate, in.Frames.Panel, regions.SlotList},
	}
	for _, item := range plan {
		path := filepath.Join(in.TemplateDir, item.name+".png")
		if err := installTemplate(item.name, item.screen, item.rect, path, cfg); err != nil {
			return report, err
		}
		report.Files = append(report.Files, path)
	}

	if err := writeSwitchConfigFile(in.ConfigPath, cfg); err != nil {
		return report, err
	}
	return report, nil
}

// settingsCoversTarget re-reads the profile frame and requires the crop to
// overlap the control the detector would tap. Without it a crop of empty
// background would verify perfectly against itself and then never match again.
func settingsCoversTarget(screen gocv.Mat, reader SwitchTextReader, region image.Rectangle) error {
	target := DetectSettingsRegion(screen, reader)
	if overlapArea(region, target) > 0 {
		return nil
	}
	return fmt.Errorf("%s at (%d,%d)-(%d,%d) does not cover the Settings control at (%d,%d)-(%d,%d); adjust -settings and re-run",
		AcctSettingsTemplate+".png", region.Min.X, region.Min.Y, region.Max.X, region.Max.Y,
		target.Min.X, target.Min.Y, target.Max.X, target.Max.Y)
}

// supercellCoversTarget re-reads the Settings frame and requires the crop to
// overlap what the detector calls the Supercell ID row (its heading or the Open
// button beside it). A blank patch that would happily match itself is refused.
func supercellCoversTarget(screen gocv.Mat, reader SwitchTextReader, region image.Rectangle) error {
	detected, err := DetectSupercellRegion(screen, reader)
	if err != nil {
		return fmt.Errorf("%s cannot be verified: %w; adjust -supercell rect and re-run", AcctSupercellTemplate+".png", err)
	}
	if overlapArea(region, detected) > 0 {
		return nil
	}
	return fmt.Errorf("%s at (%d,%d)-(%d,%d) does not cover the Supercell ID row at (%d,%d)-(%d,%d); adjust -supercell rect and re-run",
		AcctSupercellTemplate+".png", region.Min.X, region.Min.Y, region.Max.X, region.Max.Y,
		detected.Min.X, detected.Min.Y, detected.Max.X, detected.Max.Y)
}

// validateSlotGeometry either derives the row geometry from the panel's own
// rows or validates the caller's values against them, then returns the y each
// existing slot row should be tapped at.
func validateSlotGeometry(cfg *SwitchConfig, screenH int, rows []image.Point, useConfig bool) ([]int, error) {
	if len(rows) < 2 {
		return nil, fmt.Errorf("account-row geometry needs at least two rows on the Supercell ID panel, found %d — add a second signed-in village or pass -slot-row-spacing", len(rows))
	}
	pitch := float64(rows[len(rows)-1].Y-rows[0].Y) / float64(len(rows)-1)
	if pitch < 4 {
		return nil, fmt.Errorf("account rows are %dpx apart on the panel, too close to tap apart; adjust -slot-row-spacing", int(pitch))
	}
	panelTop := rows[0].Y - int(pitch/2)
	panelBottom := rows[len(rows)-1].Y + int(pitch/2)

	if !useConfig {
		cfg.SlotRow1Offset = 0 // the anchor is row 1 itself
		cfg.SlotRowSpacing = pitch / float64(screenH)
	}
	slotY := make([]int, 0, len(rows))
	tolerance := maxInt(4, int(pitch/2))
	for i, row := range rows {
		y := SlotRowY(rows[0].Y, screenH, i+1, *cfg)
		if y < panelTop || y > panelBottom {
			return nil, fmt.Errorf("slot %d lands at y=%d, outside the Supercell ID panel (rows %d..%d); adjust slot_row_spacing (currently %.3f) in account_switch.json and re-run",
				i+1, y, panelTop, panelBottom, cfg.SlotRowSpacing)
		}
		if intAbs(y-row.Y) > tolerance {
			return nil, fmt.Errorf("slot %d lands at y=%d but that row's player tag is at y=%d; adjust slot_row1_offset (%.3f) or slot_row_spacing (%.3f) and re-run",
				i+1, y, row.Y, cfg.SlotRow1Offset, cfg.SlotRowSpacing)
		}
		slotY = append(slotY, y)
	}
	return slotY, nil
}

// installTemplate crops rect out of screen, verifies the crop matches back at
// the runtime's threshold, and only then renames it over path (previous file
// kept as .bak). A template that would not match is never installed.
func installTemplate(name string, screen gocv.Mat, rect image.Rectangle, path string, cfg SwitchConfig) error {
	r := rect.Intersect(image.Rect(0, 0, screen.Cols(), screen.Rows()))
	if r.Dx() < 4 || r.Dy() < 4 {
		return fmt.Errorf("%s crop %v is outside the %dx%d screen; adjust %s and re-run",
			name+".png", rect, screen.Cols(), screen.Rows(), templateRectFlag(name))
	}
	crop := screen.Region(r)
	defer crop.Close()

	// A flat crop (empty background, a solid panel) matches itself perfectly and
	// then matches nothing else, so the switch would fail hours later. Refuse it
	// here with the flag that produced it.
	if std := grayStdDev(crop); std < 8 {
		return fmt.Errorf("%s crop at (%d,%d %dx%d) is a flat patch (std %.1f/255) — it would match anywhere; adjust %s and re-run",
			name+".png", rect.Min.X, rect.Min.Y, rect.Dx(), rect.Dy(), std, templateRectFlag(name))
	}

	tmp := path + ".tmp.png"
	if ok := gocv.IMWrite(tmp, crop); !ok {
		return fmt.Errorf("write %s", tmp)
	}
	if _, verr := VerifyInstalledSwitchTemplate(tmp, screen, cfg, r); verr != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("%s (crop %v): %w", name+".png", rect, verr)
	}
	if prev, rerr := os.ReadFile(path); rerr == nil {
		_ = os.WriteFile(path+".bak", prev, 0o644)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("install %s: %w", path, err)
	}
	return nil
}

// VerifyInstalledSwitchTemplate re-reads an installed PNG and matches it in
// screen with the runtime's matcher. It is exported so tests (and a future
// doctor command) can prove the file the switch will load is the file that was
// verified. The error names the file, the coordinates, and the flag to adjust.
func VerifyInstalledSwitchTemplate(path string, screen gocv.Mat, cfg SwitchConfig, expected image.Rectangle) (float64, error) {
	name := strings.TrimSuffix(filepath.Base(path), ".png")
	tpl := gocv.IMRead(path, gocv.IMReadColor)
	if tpl.Empty() {
		return 0, fmt.Errorf("%s cannot be read back as an image; re-run the capture", path)
	}
	defer tpl.Close()

	// Search exactly the region the crop came from: TM_CCOEFF_NORMED reports
	// ~1.0 on flat areas (zero variance), so letting the matcher slide would
	// let it "find" an empty patch somewhere else and pass a bad template.
	roi := expected.Intersect(image.Rect(0, 0, screen.Cols(), screen.Rows()))
	point, conf, found, err := MatchSwitchTemplateROI(screen, tpl, name, cfg, roi)
	if err != nil {
		return 0, fmt.Errorf("%s could not be matched (%v); adjust %s and re-run", path, err, templateRectFlag(name))
	}
	if !found {
		probe := cfg
		probe.Threshold = 0.01
		_, best, _, _ := MatchSwitchTemplateROI(screen, tpl, name, probe, roi)
		return best, fmt.Errorf("%s does not match its own screenshot region at (%d,%d %dx%d) at the runtime threshold %.2f (best %.2f); adjust %s and re-run",
			filepath.Base(path), expected.Min.X, expected.Min.Y, expected.Dx(), expected.Dy(), cfg.Threshold, best, templateRectFlag(name))
	}
	if !point.In(roi) {
		return conf, fmt.Errorf("%s matched at (%d,%d), outside its source region (%d,%d %dx%d) at threshold %.2f; adjust %s and re-run",
			filepath.Base(path), point.X, point.Y, expected.Min.X, expected.Min.Y, expected.Dx(), expected.Dy(), cfg.Threshold, templateRectFlag(name))
	}
	return conf, nil
}

// writeSwitchConfigFile persists the geometry next to the templates, merging
// whatever the operator already had.
func writeSwitchConfigFile(path string, cfg SwitchConfig) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal %s: %w", path, err)
	}
	data = append(data, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(path), err)
	}
	return paths.WriteFileAtomic(path, data, 0o644)
}

// LoadSwitchConfigFile is LoadSwitchConfig with the defaults already applied,
// for callers that only want the effective settings.
func LoadSwitchConfigFile(path string) SwitchConfig {
	cfg, err := LoadSwitchConfig(path)
	if err != nil {
		return DefaultSwitchConfig()
	}
	return cfg
}

// templateRectFlag maps a template to the capture flag that controls its crop,
// so every installed-template failure can tell the operator what to adjust.
func templateRectFlag(name string) string {
	switch name {
	case AcctSettingsTemplate:
		return "-settings"
	case AcctSupercellTemplate:
		return "-supercell"
	case AcctSlotListTemplate:
		return "-slot-list"
	default:
		return "-region"
	}
}

func framesPresent(f SwitchFrames) error {
	for name, mat := range map[string]gocv.Mat{
		"profile":  f.Profile,
		"settings": f.Settings,
		"panel":    f.Panel,
	} {
		if mat.Empty() || mat.Cols() < 4 || mat.Rows() < 4 {
			return fmt.Errorf("the %s screen was not captured; the installer needs all three screens", name)
		}
	}
	return nil
}

func padRect(r image.Rectangle, pad int) image.Rectangle {
	return image.Rect(r.Min.X-pad, r.Min.Y-pad, r.Max.X+pad, r.Max.Y+pad)
}

func clampRect(r image.Rectangle, w, h int) image.Rectangle {
	r = r.Canon()
	return r.Intersect(image.Rect(0, 0, w, h))
}

func overlapArea(a, b image.Rectangle) int {
	inter := a.Intersect(b)
	if inter.Empty() {
		return 0
	}
	return inter.Dx() * inter.Dy()
}

// grayStdDev is the pixel spread of a crop: near zero means nothing is drawn
// there, so matching it proves nothing.
func grayStdDev(m gocv.Mat) float64 {
	if m.Cols() < 1 || m.Rows() < 1 {
		return 0
	}
	gray := gocv.NewMat()
	defer gray.Close()
	gocv.CvtColor(m, &gray, gocv.ColorBGRToGray)
	mean, std := gocv.NewMat(), gocv.NewMat()
	defer mean.Close()
	defer std.Close()
	gocv.MeanStdDev(gray, &mean, &std)
	if std.Cols() < 1 || std.Rows() < 1 {
		return 0
	}
	return std.GetDoubleAt(0, 0)
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func intAbs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
