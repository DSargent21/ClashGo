// cmd/account_templates installs everything the in-game account switcher
// needs, in one command, with no image editing.
//
//	go run ./cmd/account_templates              # drive the running game
//	go run ./cmd/account_templates -source DIR  # install from saved screens
//
// Live mode walks the same path the switch uses — profile corner → Settings (if
// the build needs it) → Supercell ID → account list — saving each screen and
// detecting each button from that screen's own text. Installed PNGs are matched
// back against the screenshot they came from at the runtime's threshold; a crop
// or a row geometry that would fail later is refused here.
//
// `-source DIR` reads `profile.png`, `settings.png`, `panel.png` (plus an
// optional `text.json` standing in for OCR) from DIR. That is how a machine
// without the game reproduces an install, and how a live run's saved frames are
// tuned offline.
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
	rect image.Rectangle
}

func (r *rectFlag) String() string {
	if r.rect.Empty() {
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
		settings   = &rectFlag{}
		supercell  = &rectFlag{}
		slotList   = &rectFlag{}
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
	flag.Var(settings, "settings", "override: rect of the Settings control on the profile screen, x1,y1,x2,y2")
	flag.Var(supercell, "supercell", "override: rect of the Supercell ID row, x1,y1,x2,y2")
	flag.Var(slotList, "slot-list", "override: rect of the first account row on the panel, x1,y1,x2,y2")
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
		UseConfigGeometry: row1Offset > 0 || rowSpacing > 0,
		Regions: game.SwitchTemplateRegions{
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
		defer frames.Close()
		in.Frames, in.Text = frames, reader
	} else {
		dev, err := connect(device, host, port)
		if err != nil {
			fatalf("%v", err)
		}
		defer func() { _ = dev.Close() }()
		frames, regions, err := game.CaptureSwitchFrames(dev, framesDir, time.Duration(settleMS)*time.Millisecond, in.Regions)
		if err != nil {
			fatalf("%v\nScreens saved in %s for a -source re-run.", err, framesDir)
		}
		defer frames.Close()
		in.Frames = frames
		// The driver tapped exactly these regions, so hand them to the installer
		// instead of detecting again.
		in.Regions = regions
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
	fmt.Printf("✓ switch will tap slots at y=%s\n", intList(report.SlotY))
	fmt.Println()
	fmt.Println("Next: press Switch on an account (or Switch & start). No cropping, no further setup.")
}

// connect builds the ADB client for the device the game runs on. ConnectConfigured
// keeps the helper on that one serial instead of picking whichever answers first.
func connect(device, host string, port int) (*adb.Client, error) {
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
		return nil, fmt.Errorf("connect to %s: %w (start the emulator, or install from saved screens with -source DIR)", client.DeviceID, err)
	}
	return client, nil
}

// loadSavedFrames reads the three screens a -source install needs, plus the
// optional OCR fixture.
func loadSavedFrames(dir string) (game.SwitchFrames, game.SwitchTextReader, error) {
	var frames game.SwitchFrames
	targets := map[string]*gocv.Mat{
		"profile":  &frames.Profile,
		"settings": &frames.Settings,
		"panel":    &frames.Panel,
	}
	for name, dst := range targets {
		mat := gocv.IMRead(filepath.Join(dir, name+".png"), gocv.IMReadColor)
		if mat.Empty() {
			return frames, nil, fmt.Errorf("read %s: %s.png missing (a -source dir holds profile.png, settings.png, panel.png)", dir, name)
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
		return frames, nil, fmt.Errorf("parse %s: %w", textPath, err)
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
