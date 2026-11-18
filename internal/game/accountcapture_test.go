package game

// accountcapture_test.go proves the cold-start install path: four synthetic
// game screens go through the same detection + install code the CLI runs, the
// four PNGs and the geometry file land in a real directory, and the runtime
// switch then loads those exact files, passes the missing-template gate and
// taps the derived row. No emulator, no OCR toolchain, no hand cropping.

import (
	"encoding/json"
	"image"
	"image/color"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Ducky705/ClashGO/internal/vision"
	"github.com/rs/zerolog"
	"gocv.io/x/gocv"
)

const (
	captureW = 640
	captureH = 360
)

// captureFixture is a set of four screens plus the text the operator's game
// would show on each one.
type captureFixture struct {
	frames  SwitchFrames
	texts   map[string][]vision.TextRegion
	screens []gocv.Mat
}

func (f *captureFixture) reader() SwitchTextReader {
	return func(frame string, _ gocv.Mat) ([]vision.TextRegion, error) {
		return f.texts[frame], nil
	}
}

func (f *captureFixture) close() {
	for _, m := range f.screens {
		m.Close()
	}
}

// newCaptureFixture builds the four screens a switch walks through. Every
// target region gets a distinctive two-tone patch, so the installed crop is a
// template the matcher can find again.
func newCaptureFixture(t *testing.T) *captureFixture {
	t.Helper()
	bg := color.RGBA{20, 24, 30, 255}
	red := color.RGBA{190, 50, 50, 255}
	yellow := color.RGBA{245, 220, 70, 255}
	blue := color.RGBA{40, 90, 210, 255}
	white := color.RGBA{235, 235, 235, 255}
	green := color.RGBA{30, 140, 70, 255}

	f := &captureFixture{}
	village := fillMat(t, captureW, captureH, bg)
	bannerArt := fixtureTemplate(t, 60, 28, red, yellow)
	paste(t, village, image.Rect(90, 16, 150, 44), bannerArt)
	bannerArt.Close()

	profile := fillMat(t, captureW, captureH, bg)
	// The Settings tab, as current builds show it on the profile screen.
	settingsTab := image.Rect(512, 32, 612, 72)
	gearArt := fixtureTemplate(t, settingsTab.Dx(), settingsTab.Dy(), blue, white)
	paste(t, profile, settingsTab, gearArt)
	gearArt.Close()

	settings := fillMat(t, captureW, captureH, bg)
	supercell := image.Rect(300, 200, 440, 224) // the OCR box for "Supercell ID"
	supercellArt := fixtureTemplate(t, 188, 72, green, white)
	paste(t, settings, image.Rect(276, 176, 464, 248), supercellArt)
	supercellArt.Close()

	panel := fillMat(t, captureW, captureH, bg)
	row1 := fixtureTemplate(t, 140, 40, yellow, blue)
	paste(t, panel, image.Rect(230, 180, 370, 220), row1)
	row1.Close()

	f.texts = map[string][]vision.TextRegion{
		"village":  {{X: 80, Y: 20, W: 120, H: 20, Text: "MAIN VILLAGE"}},
		"profile":  {{X: settingsTab.Min.X, Y: settingsTab.Min.Y, W: settingsTab.Dx(), H: settingsTab.Dy(), Text: "Settings"}},
		"settings": {{X: supercell.Min.X, Y: supercell.Min.Y, W: supercell.Dx(), H: supercell.Dy(), Text: "Supercell ID"}},
		"panel": {
			{X: 240, Y: 190, W: 120, H: 20, Text: "#ABC123"},
			{X: 240, Y: 250, W: 120, H: 20, Text: "#DEF456"},
			{X: 240, Y: 310, W: 120, H: 20, Text: "#GHI789"},
		},
	}
	f.frames = SwitchFrames{Village: village, Profile: profile, Settings: settings, Panel: panel}
	f.screens = []gocv.Mat{village, profile, settings, panel}
	t.Cleanup(f.close)
	return f
}

// unrelatedScreen is deterministic pseudo-random noise: a structured template
// cannot correlate with it the way it can with stripes or a flat background.
func unrelatedScreen(t *testing.T) gocv.Mat {
	t.Helper()
	m := fillMat(t, captureW, captureH, color.RGBA{10, 10, 10, 255})
	seed := uint32(0x9e3779b9)
	next := func() uint8 {
		seed = seed*1664525 + 1013904223
		return uint8(seed >> 24)
	}
	for y := 0; y < captureH; y += 8 {
		for x := 0; x < captureW; x += 8 {
			c := color.RGBA{next(), next(), next(), 255}
			gocv.Rectangle(&m, image.Rect(x, y, x+8, y+8), c, -1)
		}
	}
	return m
}

// installForTest runs the real installer into a temp assets tree.
func installForTest(t *testing.T, f *captureFixture, mutate func(*SwitchInstallInput)) (SwitchInstallReport, string, string) {
	t.Helper()
	root := t.TempDir()
	tplDir := filepath.Join(root, "templates")
	cfgPath := filepath.Join(root, "account_switch.json")
	in := SwitchInstallInput{
		Frames:      f.frames,
		Text:        f.reader(),
		TemplateDir: tplDir,
		ConfigPath:  cfgPath,
	}
	if mutate != nil {
		mutate(&in)
	}
	report, err := InstallSwitchTemplates(in)
	if err != nil {
		t.Fatalf("InstallSwitchTemplates: %v", err)
	}
	return report, tplDir, cfgPath
}

// TestInstalledTemplatesSatisfyTheRuntimeSwitch is the cold-start contract:
// after one install the runtime loads the installed files, the missing-template
// gate passes, and the switch taps the row the geometry points at.
func TestInstalledTemplatesSatisfyTheRuntimeSwitch(t *testing.T) {
	f := newCaptureFixture(t)
	report, tplDir, cfgPath := installForTest(t, f, nil)

	if len(report.Files) != 4 {
		t.Fatalf("installed %d files (%v), want 4", len(report.Files), report.Files)
	}
	for _, name := range []string{
		AcctProfileTemplate, AcctSettingsTemplate, AcctSupercellTemplate, AcctSlotListTemplate,
	} {
		if _, err := os.Stat(filepath.Join(tplDir, name+".png")); err != nil {
			t.Fatalf("%s was not installed into the runtime directory: %v", name, err)
		}
	}
	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read installed geometry: %v", err)
	}
	var cfg SwitchConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("parse installed geometry: %v", err)
	}
	// Rows are 60px apart on a 360px screen; the first row is the anchor.
	if wantPitch := 60.0 / captureH; cfg.SlotRowSpacing < wantPitch-0.005 || cfg.SlotRowSpacing > wantPitch+0.005 {
		t.Fatalf("installed slot_row_spacing = %.4f, want %.4f", cfg.SlotRowSpacing, wantPitch)
	}
	if cfg.SlotRow1Offset != 0 {
		t.Fatalf("installed slot_row1_offset = %.4f, want 0 (anchor is row 1)", cfg.SlotRow1Offset)
	}

	// The runtime, using only what is on disk.
	store, err := NewTemplateStore(tplDir)
	if err != nil {
		t.Fatalf("open runtime template store: %v", err)
	}
	defer store.Close()
	if err := store.LoadTemplates(); err != nil {
		t.Fatalf("load runtime templates: %v", err)
	}
	loadedCfg, err := LoadSwitchConfig(cfgPath)
	if err != nil {
		t.Fatalf("load runtime config: %v", err)
	}

	screens := []gocv.Mat{
		f.frames.Village.Clone(),  // before any tap: player banner
		f.frames.Profile.Clone(),  // profile screen: gear
		f.frames.Settings.Clone(), // settings: Supercell ID row
		f.frames.Panel.Clone(),    // account list: row 1 anchor
		f.frames.Village.Clone(),  // after the slot tap
		f.frames.Profile.Clone(),  // profile, for the identity read
	}
	defer func() {
		for _, m := range screens {
			m.Close()
		}
	}()
	dev := &fakeSwitchDevice{screens: screens}
	sw := NewAccountSwitcher(dev, store, loadedCfg, zerolog.Nop())
	if err := sw.CheckTemplates(); err != nil {
		t.Fatalf("runtime still refuses the installed templates: %v", err)
	}
	sw.SetIdentityReader(func(gocv.Mat) (Identity, error) {
		return Identity{Name: "Fleet Two", Tag: "#DEF456"}, nil
	})
	got, err := sw.Switch(2, Identity{Name: "Fleet Two", Tag: "#DEF456"})
	if err != nil {
		t.Fatalf("switch with installed templates: %v", err)
	}
	if got.Tag != "#DEF456" {
		t.Fatalf("identity = %+v", got)
	}
	if len(dev.taps) != 5 {
		t.Fatalf("taps = %v, want 5", dev.taps)
	}
	// Row 2's tag centre: 640x360 fixture with the derived pitch.
	if want := image.Pt(300, 260); dev.taps[3] != want {
		t.Fatalf("slot 2 tap = %v, want %v (all taps %v)", dev.taps[3], want, dev.taps)
	}
}

// TestInstallRejectsWrongRowGeometry keeps a bad pitch from installing: the
// computed slot row misses the panel's own rows and names the tunable.
func TestInstallRejectsWrongRowGeometry(t *testing.T) {
	f := newCaptureFixture(t)
	root := t.TempDir()
	in := SwitchInstallInput{
		Frames:            f.frames,
		Text:              f.reader(),
		TemplateDir:       filepath.Join(root, "templates"),
		ConfigPath:        filepath.Join(root, "account_switch.json"),
		UseConfigGeometry: true,
		Config: SwitchConfig{
			SlotRow1Offset: 0,
			SlotRowSpacing: 0.60, // 216px: slot 2 lands far below the panel
			Threshold:      0.80,
		},
	}
	_, err := InstallSwitchTemplates(in)
	if err == nil {
		t.Fatal("a geometry that misses every row was installed")
	}
	for _, needle := range []string{"slot 2", "slot_row_spacing", "y=416"} {
		if !strings.Contains(err.Error(), needle) {
			t.Errorf("error %q is missing %q", err, needle)
		}
	}
	if _, statErr := os.Stat(filepath.Join(root, "templates", AcctProfileTemplate+".png")); statErr == nil {
		t.Fatal("templates were installed despite the geometry failure")
	}
}

// TestInstallRejectsSupercellRectAwayFromTarget proves a crop that is not the
// Supercell ID row is refused even though it would match itself perfectly.
func TestInstallRejectsSupercellRectAwayFromTarget(t *testing.T) {
	f := newCaptureFixture(t)
	_, err := InstallSwitchTemplates(SwitchInstallInput{
		Frames:      f.frames,
		Text:        f.reader(),
		TemplateDir: filepath.Join(t.TempDir(), "templates"),
		ConfigPath:  filepath.Join(t.TempDir(), "account_switch.json"),
		Regions: SwitchTemplateRegions{
			Supercell: image.Rect(10, 10, 70, 40),
		},
	})
	if err == nil {
		t.Fatal("a Supercell ID crop over empty background was installed")
	}
	for _, needle := range []string{AcctSupercellTemplate + ".png", "-supercell", "Supercell ID"} {
		if !strings.Contains(err.Error(), needle) {
			t.Errorf("error %q is missing %q", err, needle)
		}
	}
}

// TestInstallRejectsSettingsCropOffTheControl proves an override that crops
// empty background is refused: it would match itself perfectly and then never
// match the real screen, which is exactly the silent failure this helper exists
// to prevent.
func TestInstallRejectsSettingsCropOffTheControl(t *testing.T) {
	f := newCaptureFixture(t)
	_, err := InstallSwitchTemplates(SwitchInstallInput{
		Frames:      f.frames,
		Text:        f.reader(),
		TemplateDir: filepath.Join(t.TempDir(), "templates"),
		ConfigPath:  filepath.Join(t.TempDir(), "account_switch.json"),
		Regions: SwitchTemplateRegions{
			Settings: image.Rect(10, 10, 120, 60),
		},
	})
	if err == nil {
		t.Fatal("a Settings crop of empty background was installed")
	}
	for _, needle := range []string{AcctSettingsTemplate + ".png", "-settings", "Settings control", "(10,10)"} {
		if !strings.Contains(err.Error(), needle) {
			t.Errorf("error %q is missing %q", err, needle)
		}
	}
}

// TestInstalledTemplateThatDoesNotMatchIsRejected pins the verification of an
// installed file: matching it against the wrong screen fails with the file name
// and the flag that controls its crop.
func TestInstalledTemplateThatDoesNotMatchIsRejected(t *testing.T) {
	f := newCaptureFixture(t)
	_, tplDir, cfgPath := installForTest(t, f, nil)
	cfg, err := LoadSwitchConfig(cfgPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	noise := unrelatedScreen(t)
	defer noise.Close()
	_, err = VerifyInstalledSwitchTemplate(filepath.Join(tplDir, AcctSettingsTemplate+".png"), noise, cfg,
		DetectSettingsRegion(f.frames.Profile, f.reader()))
	if err == nil {
		t.Fatal("the Settings template matched an unrelated screen")
	}
	for _, needle := range []string{AcctSettingsTemplate + ".png", "-settings", "runtime threshold"} {
		if !strings.Contains(err.Error(), needle) {
			t.Errorf("error %q is missing %q", err, needle)
		}
	}
}

// TestDetectSlotListRegionNeedsTwoVillages keeps the one-account case honest:
// there is nothing to switch to, so the installer says so instead of deriving a
// pitch from a single row.
func TestDetectSlotListRegionNeedsTwoVillages(t *testing.T) {
	screen := fillMat(t, captureW, captureH, color.RGBA{20, 20, 20, 255})
	defer screen.Close()
	reader := func(string, gocv.Mat) ([]vision.TextRegion, error) {
		return []vision.TextRegion{{X: 240, Y: 190, W: 120, H: 20, Text: "#ABC123"}}, nil
	}
	_, _, err := DetectSlotListRegion(screen, reader)
	if err == nil || !strings.Contains(err.Error(), "at least 2") {
		t.Fatalf("error = %v, want a message about needing two villages", err)
	}
}
