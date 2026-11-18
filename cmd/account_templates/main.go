// cmd/account_templates installs everything the in-game account switcher
// needs, in one command, with no image editing.
//
//	go run ./cmd/account_templates                 # drive the running game
//	go run ./cmd/account_templates -source DIR      # install from saved screens
//
// Live mode walks the same path the switch uses — player banner → Settings →
// Supercell ID — captures each screen, detects the button to tap from that
// screen's own text, and writes the four templates plus the derived row
// geometry into the assets directory the runtime reads. Every installed PNG is
// matched back against the screenshot it came from at the runtime's threshold;
// a crop or a row geometry that would fail later is refused here.
//
// `-source DIR` reads `village.png`, `profile.png`, `settings.png`,
// `panel.png` (plus an optional `text.json` standing in for OCR) from DIR.
// That is how a machine without the game can reproduce an install, and how a
// live run's saved frames can be tuned offline.
package main

import (
	"encoding/json"
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
	"github.com/Ducky705/ClashGO/internal/config"
	"github.com/Ducky705/ClashGO/internal/game"
	"github.com/Ducky705/ClashGO/internal/paths"
	"github.com/Ducky705/ClashGO/internal/vision"
)

type rectFlag struct {
	name string
	rect image.Rectangle
	set  bool
}

func (r *rectFlag) String() string {
	if !r.set {
		return ""
	}
	return fmt.Sprintf("%d,%d,%d,%d", r.rect.Min.X, r.rect.Min.Y, r.rect.Max.X, r.rect.Max.Y)
}

func (r *rectFlag) Set(value string) error {
	parts := strings.Split(strings.TrimSpace(value), ",")
	if len(parts) != 4 {
		return fmt.Errorf("want x1,y1,x2,y2 (got %q)", value)
	}
	var nums [4]int
	for i, p := range parts {
		n, err := strconv.Atoi(strings.TrimSpace(p))
		if err != nil {
			return fmt.Errorf("coordinate %q is not a number: %w", p, err)
		}
		nums[i] = n
	}
	r.rect = image.Rect(nums[0], nums[1], nums[2], nums[3]).Canon()
	if r.rect.Dx() < 4 || r.rect.Dy() < 4 {
		return fmt.Errorf("rect %dx%d is too small (need at least 4x4)", r.rect.Dx(), r.rect.Dy())
	}
	r.set = true
	return nil
}

func main() {
	var (
		device     string
		host       string
		port       int
		source     string
		outDir     string
		configPath string
		framesDir  string
		settleMS   int
		row1Offset float64
		rowSpacing float64
		profile    = &rectFlag{name: game.AcctProfileTemplate}
		settings   = &rectFlag{name: game.AcctSettingsTemplate}
		supercell  = &rectFlag{name: game.AcctSupercellTemplate}
		slotList   = &rectFlag{name: game.AcctSlotListTemplate}
	)
	flag.StringVar(&device, "device", "", "adb serial to capture from (default: config.json's device)")
	flag.StringVar(&host, "host", "", "adb server host (default: config.json, else 127.0.0.1)")
	flag.IntVar(&port, "port", 0, "adb server port (default: config.json, else 5037)")
	flag.StringVar(&source, "source", "", "install from saved screens in this dir instead of driving the game")
	flag.StringVar(&outDir, "out", "", "template directory the runtime reads (default: <assets>/templates)")
	flag.StringVar(&configPath, "config", "", "geometry file the runtime reads (default: <assets>/account_switch.json)")
	flag.StringVar(&framesDir, "frames", "", "where captured screens are saved (default: output/account_templates/<ts>)")
	flag.IntVar(&settleMS, "settle-ms", 1500, "wait after each tap for the screen to change")
	flag.Float64Var(&row1Offset, "slot-row1-offset", 0, "override: row 1 below the anchor, as a fraction of screen height")
	flag.Float64Var(&rowSpacing, "slot-row-spacing", 0, "override: distance between account rows, as a fraction of screen height")
	flag.Var(profile, "profile", "override: rect of the top-left player banner, x1,y1,x2,y2")
	flag.Var(settings, "settings", "override: rect of the Settings gear on the profile screen")
	flag.Var(supercell, "supercell", "override: rect of the Supercell ID row in Settings")
	flag.Var(slotList, "slot-list", "override: rect of the first account row on the panel")
	flag.Parse()

	if outDir == "" {
		outDir = paths.Resolve("templates")
	}
	if configPath == "" {
		configPath = paths.Resolve("account_switch.json")
	}
	if framesDir == "" {
		framesDir = filepath.Join("output", "account_templates", time.Now().Format("20060102_150405"))
	}
	if err := os.MkdirAll(framesDir, 0o755); err != nil {
		fatalf("mkdir %s: %v", framesDir, err)
	}

	cfg := game.LoadSwitchConfigFile(configPath)
	useConfigGeometry := row1Offset > 0 || rowSpacing > 0
	if row1Offset > 0 {
		cfg.SlotRow1Offset = row1Offset
	}
	if rowSpacing > 0 {
		cfg.SlotRowSpacing = rowSpacing
	}

	in := game.SwitchInstallInput{
		Config:            cfg,
		TemplateDir:       outDir,
		ConfigPath:        configPath,
		UseConfigGeometry: useConfigGeometry,
		Regions: game.SwitchTemplateRegions{
			Profile:   profile.rect,
			Settings:  settings.rect,
			Supercell: supercell.rect,
			SlotList:  slotList.rect,
		},
	}

	if source != "" {
		frames, reader, err := loadSavedFrames(source)
		if err != nil {
			fatalf("%v", err)
		}
		defer frames.close()
		in.Frames, in.Text = frames.mats(), reader
	} else {
		frames, regions, rows, anchor, err := driveGame(device, host, port, settleMS, framesDir, in.Regions)
		if err != nil {
			fatalf("%v", err)
		}
		defer frames.close()
		in.Frames = frames.mats()
		// Live detection already picked the regions to tap; hand them to the
		// installer so the crops are exactly what was tapped.
		in.Regions = regions
		in.Rows, in.Anchor = rows, anchor
	}

	report, err := game.InstallSwitchTemplates(in)
	if err != nil {
		fmt.Fprintf(os.Stderr, "\nERROR: %v\n", err)
		fmt.Fprintf(os.Stderr, "Screens kept in %s for a -source re-run.\n", framesDir)
		fmt.Fprintln(os.Stderr, "Nothing was installed: the switch keeps refusing until every template verifies.")
		os.Exit(1)
	}

	fmt.Println()
	for _, file := range report.Files {
		fmt.Printf("✓ %s\n", file)
	}
	fmt.Printf("✓ row geometry: slot_row1_offset=%.4f slot_row_spacing=%.4f → %s\n",
		report.Config.SlotRow1Offset, report.Config.SlotRowSpacing, report.ConfigPath)
	fmt.Printf("✓ rows detected at y=%s\n", rowList(report.Rows))
	fmt.Printf("✓ switch will tap slots at y=%s\n", intList(report.SlotY))
	fmt.Println()
	fmt.Println("Next: press Switch on an account (or Switch & start). No cropping, no further setup.")
}

// savedFrames is the four screens a -source install reads.
type savedFrames struct {
	Village, Profile, Settings, Panel gocv.Mat
}

func (f savedFrames) mats() game.SwitchFrames {
	return game.SwitchFrames{Village: f.Village, Profile: f.Profile, Settings: f.Settings, Panel: f.Panel}
}

func (f savedFrames) close() {
	for _, m := range []gocv.Mat{f.Village, f.Profile, f.Settings, f.Panel} {
		if !m.Empty() {
			m.Close()
		}
	}
}

func loadSavedFrames(dir string) (savedFrames, game.SwitchTextReader, error) {
	var frames savedFrames
	targets := map[string]*gocv.Mat{
		"village":  &frames.Village,
		"profile":  &frames.Profile,
		"settings": &frames.Settings,
		"panel":    &frames.Panel,
	}
	for name, dst := range targets {
		path := filepath.Join(dir, name+".png")
		mat := gocv.IMRead(path, gocv.IMReadColor)
		if mat.Empty() {
			return savedFrames{}, nil, fmt.Errorf("read %s: %s missing (a -source dir holds village.png, profile.png, settings.png, panel.png)", path, name+".png")
		}
		*dst = mat
	}

	textPath := filepath.Join(dir, "text.json")
	data, err := os.ReadFile(textPath)
	if err != nil {
		return frames, nil, nil // no fixture text: use the real OCR helper
	}
	var texts map[string][]vision.TextRegion
	if err := json.Unmarshal(data, &texts); err != nil {
		return savedFrames{}, nil, fmt.Errorf("parse %s: %w", textPath, err)
	}
	reader := func(frame string, _ gocv.Mat) ([]vision.TextRegion, error) {
		regions, ok := texts[frame]
		if !ok {
			return nil, fmt.Errorf("text.json has no %q entries", frame)
		}
		return regions, nil
	}
	return frames, reader, nil
}

// driveGame walks the switch path on the running emulator, returning the four
// screens plus the regions and rows it detected. It leaves the game back on the
// main village.
func driveGame(device, host string, port, settleMS int, saveDir string, overrides game.SwitchTemplateRegions) (savedFrames, game.SwitchTemplateRegions, []image.Point, image.Rectangle, error) {
	cfg := config.LoadOrDefault("config.json")
	if host == "" {
		host = cfg.Device.ADBHost
	}
	if port == 0 {
		port = cfg.Device.ADBPort
	}
	client := adb.NewClient(
		adb.WithHost(host),
		adb.WithPort(port),
		adb.WithTimeout(30*time.Second),
	)
	client.DeviceID = device
	if client.DeviceID == "" {
		client.DeviceID = cfg.Device.DeviceID
	}
	if err := client.ConnectConfigured(); err != nil {
		return savedFrames{}, game.SwitchTemplateRegions{}, nil, image.Rectangle{}, fmt.Errorf("connect to %s: %w (start the emulator, or install from saved screens with -source DIR)", client.DeviceID, err)
	}
	defer func() { _ = client.Close() }()

	settle := func() { time.Sleep(time.Duration(settleMS) * time.Millisecond) }
	capture := func() (gocv.Mat, error) {
		mat, err := client.CaptureToMat()
		if err != nil {
			return gocv.NewMat(), fmt.Errorf("capture %s: %w", client.DeviceID, err)
		}
		return mat, nil
	}
	tap := func(p image.Point) error {
		if err := client.TapRandomized(p.X, p.Y); err != nil {
			return fmt.Errorf("tap (%d,%d): %w", p.X, p.Y, err)
		}
		settle()
		return nil
	}
	centre := func(r image.Rectangle) image.Point {
		return image.Pt(r.Min.X+r.Dx()/2, r.Min.Y+r.Dy()/2)
	}

	var frames savedFrames
	var err error
	if frames.Village, err = capture(); err != nil {
		return savedFrames{}, game.SwitchTemplateRegions{}, nil, image.Rectangle{}, err
	}
	saveFrame(saveDir, "village", frames.Village)
	saved := 0
	defer func() {
		if saved > 0 {
			// panel → settings → profile → village. Back from the village
			// itself would open the exit dialog, so the count is exact.
			for i := 0; i < saved; i++ {
				_ = client.Back()
				time.Sleep(500 * time.Millisecond)
			}
		}
	}()

	var regions game.SwitchTemplateRegions
	var rows []image.Point

	if overrides.Profile.Empty() {
		if regions.Profile, err = game.DetectBannerRegion(frames.Village, nil); err != nil {
			return savedFrames{}, regions, nil, image.Rectangle{}, err
		}
	} else {
		regions.Profile = overrides.Profile
	}
	// The banner carries the current village's name, so the profile is opened by
	// geometry (the level badge) exactly as the runtime does it.
	if err := tap(game.ProfileTapPoint(frames.Village.Cols(), frames.Village.Rows())); err != nil {
		return savedFrames{}, regions, nil, image.Rectangle{}, err
	}
	saved++
	if frames.Profile, err = capture(); err != nil {
		return savedFrames{}, regions, nil, image.Rectangle{}, err
	}
	saveFrame(saveDir, "profile", frames.Profile)

	if overrides.Settings.Empty() {
		regions.Settings = game.DetectSettingsRegion(frames.Profile, nil)
	} else {
		regions.Settings = overrides.Settings
	}

	// Current builds put the Supercell ID row straight on the profile screen;
	// older ones hide it behind a Settings tab. Only tap Settings when it is not
	// already visible, and save whichever screen actually shows the row.
	if !overrides.Supercell.Empty() {
		regions.Supercell = overrides.Supercell
		frames.Settings = frames.Profile.Clone()
	} else if regions.Supercell, err = game.DetectSupercellRegion(frames.Profile, nil); err == nil {
		frames.Settings = frames.Profile.Clone()
	} else {
		if err := tap(centre(regions.Settings)); err != nil {
			return savedFrames{}, regions, nil, image.Rectangle{}, err
		}
		saved++
		if frames.Settings, err = capture(); err != nil {
			return savedFrames{}, regions, nil, image.Rectangle{}, err
		}
		if regions.Supercell, err = game.DetectSupercellRegion(frames.Settings, nil); err != nil {
			return savedFrames{}, regions, nil, image.Rectangle{}, fmt.Errorf("%w (Settings opened at (%d,%d); pass -settings x1,y1,x2,y2 to point at it, or open Supercell ID by hand and use -source)", err, centre(regions.Settings).X, centre(regions.Settings).Y)
		}
	}
	saveFrame(saveDir, "settings", frames.Settings)
	if err := tap(centre(regions.Supercell)); err != nil {
		return savedFrames{}, regions, nil, image.Rectangle{}, err
	}
	saved++
	if frames.Panel, err = capture(); err != nil {
		return savedFrames{}, regions, nil, image.Rectangle{}, err
	}
	saveFrame(saveDir, "panel", frames.Panel)

	// Rows always come from the panel's own tags; only the crop can be overridden.
	detected, anchor, err := game.DetectSlotListRegion(frames.Panel, nil)
	if err != nil {
		return savedFrames{}, regions, nil, image.Rectangle{}, err
	}
	rows = detected
	regions.SlotList = anchor
	if !overrides.SlotList.Empty() {
		regions.SlotList = overrides.SlotList
	}

	return frames, regions, rows, regions.SlotList, nil
}

// saveFrame keeps every captured screen, including after a failed step, so the
// operator (and `-source`) can inspect or reuse it.
func saveFrame(dir, name string, mat gocv.Mat) {
	if mat.Empty() {
		return
	}
	if ok := gocv.IMWrite(filepath.Join(dir, name+".png"), mat); !ok {
		fmt.Fprintf(os.Stderr, "WARN: could not save %s.png in %s\n", name, dir)
	}
}

func rowList(rows []image.Point) string {
	parts := make([]string, 0, len(rows))
	for _, row := range rows {
		parts = append(parts, strconv.Itoa(row.Y))
	}
	return strings.Join(parts, ", ")
}

func intList(values []int) string {
	parts := make([]string, 0, len(values))
	for _, v := range values {
		parts = append(parts, strconv.Itoa(v))
	}
	return strings.Join(parts, ", ")
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "ERROR: "+format+"\n", args...)
	os.Exit(1)
}
