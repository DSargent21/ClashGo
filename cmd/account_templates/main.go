// cmd/account_templates captures the four PNGs the in-game account switcher
// needs, straight from a running emulator.
//
// ClashGO switches villages by tapping the game's own Supercell ID list
// (profile → Settings → Supercell ID → row). Five near-identical rows cannot be
// told apart by a template, so only the navigation buttons and the list anchor
// are captured here; the row itself is derived from the anchor's geometry
// (account_switch.json). One screenshot, four crops.
//
// Two-step workflow, both read-only until the crop is written:
//
//	go run ./cmd/account_templates
//	  # saves output/account_templates/<ts>/screen.png and prints the crop lines
//
//	go run ./cmd/account_templates \
//	  -profile 18,24,150,110 -settings 1196,44,64,64 \
//	  -supercell 900,318,320,74 -slot-list 640,170,520,80
//	  # crops each rect out of a fresh capture, verifies the crop matches the
//	  # frame, writes assets/templates/acct_*.png (+ .bak) and, when the row
//	  # geometry flags are given, assets/account_switch.json
//
// Coordinates are physical pixels of the captured frame; run with
// -source screen.png to crop an already-saved frame without a device.
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
		shotDir    string
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
	flag.StringVar(&source, "source", "", "crop from this PNG instead of capturing the device")
	flag.StringVar(&outDir, "out", "", "template directory (default: <assets>/templates)")
	flag.StringVar(&configPath, "config", "", "switch config file (default: <assets>/account_switch.json)")
	flag.StringVar(&shotDir, "screenshot-dir", "", "where the full frame is saved (default: output/account_templates/<ts>)")
	flag.Float64Var(&row1Offset, "slot-row1-offset", 0, "where account row 1 sits below the list anchor, as a fraction of screen height (0 = leave unchanged)")
	flag.Float64Var(&rowSpacing, "slot-row-spacing", 0, "distance between account rows, as a fraction of screen height (0 = leave unchanged)")
	flag.Var(profile, "profile", "rect of the top-left player banner, x1,y1,x2,y2")
	flag.Var(settings, "settings", "rect of the Settings gear on the profile screen")
	flag.Var(supercell, "supercell", "rect of the Supercell ID row in Settings")
	flag.Var(slotList, "slot-list", "rect of the account-list panel header that opens from it")
	flag.Parse()

	if outDir == "" {
		outDir = paths.Resolve("templates")
	}
	if configPath == "" {
		configPath = paths.Resolve("account_switch.json")
	}
	if shotDir == "" {
		shotDir = filepath.Join("output", "account_templates", time.Now().Format("20060102_150405"))
	}

	screen, cleanup, err := loadFrame(device, host, port, source)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: %v\n", err)
		os.Exit(1)
	}
	defer screen.Close()
	defer cleanup()

	w, h := screen.Cols(), screen.Rows()
	if err := os.MkdirAll(shotDir, 0o755); err != nil {
		fatalf("mkdir %s: %v", shotDir, err)
	}
	shotPath := filepath.Join(shotDir, "screen.png")
	if ok := gocv.IMWrite(shotPath, screen); !ok {
		fatalf("write screenshot to %s", shotPath)
	}
	fmt.Printf("✓ captured %dx%d → %s\n", w, h, shotPath)

	crops := []*rectFlag{profile, settings, supercell, slotList}
	any := false
	for _, c := range crops {
		any = any || c.set
	}
	if !any {
		fmt.Println()
		fmt.Println("Next: open the screenshot, measure each region, then run:")
		fmt.Printf("  go run ./cmd/account_templates -source %s \\\n", shotPath)
		fmt.Println("    -profile x1,y1,x2,y2 \\")
		fmt.Println("    -settings x1,y1,x2,y2 \\")
		fmt.Println("    -supercell x1,y1,x2,y2 \\")
		fmt.Println("    -slot-list x1,y1,x2,y2 \\")
		fmt.Println("    -slot-row1-offset 0.16 -slot-row-spacing 0.075")
		fmt.Println()
		fmt.Println("  profile     player banner button, top left of the main village")
		fmt.Println("  settings    gear that opens Settings, on the profile screen")
		fmt.Println("  supercell   the \"Supercell ID\" row in Settings")
		fmt.Println("  slot-list   the account list panel that opens from it (header is enough)")
		fmt.Println("  row flags   optional; only needed when the default row pitch misses")
		return
	}

	if err := os.MkdirAll(outDir, 0o755); err != nil {
		fatalf("mkdir %s: %v", outDir, err)
	}
	failed := 0
	for _, c := range crops {
		if !c.set {
			continue
		}
		r := c.rect.Intersect(image.Rect(0, 0, w, h))
		if r.Dx() < 4 || r.Dy() < 4 {
			fmt.Printf("✗ %s: rect %v is outside the %dx%d frame\n", c.name, c.rect, w, h)
			failed++
			continue
		}
		crop := screen.Region(r)
		if err := writeVerifiedTemplate(c.name, crop, screen, outDir); err != nil {
			crop.Close()
			fmt.Printf("✗ %s: %v\n", c.name, err)
			failed++
			continue
		}
		crop.Close()
		fmt.Printf("✓ %s → %s (%dx%d at %d,%d)\n", c.name, filepath.Join(outDir, c.name+".png"), r.Dx(), r.Dy(), r.Min.X, r.Min.Y)
	}

	if row1Offset > 0 || rowSpacing > 0 {
		if err := writeSwitchConfig(configPath, row1Offset, rowSpacing); err != nil {
			fmt.Printf("✗ account_switch.json: %v\n", err)
			failed++
		} else {
			fmt.Printf("✓ row geometry written to %s\n", configPath)
		}
	}

	missing := missingTemplates(outDir)
	if len(missing) > 0 {
		fmt.Printf("\nStill missing: %s\n", strings.Join(missing, ", "))
	}
	if failed > 0 {
		os.Exit(1)
	}
	fmt.Println("\nAll requested templates written and verified against the captured frame.")
}

// loadFrame captures the live screen (or reads -source) and returns it with the
// cleanup for whatever had to be opened.
func loadFrame(device, host string, port int, source string) (gocv.Mat, func(), error) {
	if source != "" {
		mat := gocv.IMRead(source, gocv.IMReadColor)
		if mat.Empty() {
			return gocv.NewMat(), func() {}, fmt.Errorf("read source PNG %s", source)
		}
		return mat, func() {}, nil
	}
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
	// Capture from the requested serial only: auto-detection may pick another
	// running instance and silently save templates from the wrong emulator.
	if err := client.ConnectConfigured(); err != nil {
		_ = client.Close()
		return gocv.NewMat(), func() {}, fmt.Errorf("connect to %s: %w", client.DeviceID, err)
	}
	mat, err := client.CaptureToMat()
	if err != nil {
		_ = client.Close()
		return gocv.NewMat(), func() {}, fmt.Errorf("capture %s: %w", client.DeviceID, err)
	}
	return mat, func() { _ = client.Close() }, nil
}

// writeVerifiedTemplate refuses to leave a crop the matcher cannot find again:
// the file is written to .tmp, matched against the source frame, and only then
// renamed over the live name (previous file kept as .bak).
func writeVerifiedTemplate(name string, crop, screen gocv.Mat, outDir string) error {
	tmpPath := filepath.Join(outDir, name+".tmp.png")
	if ok := gocv.IMWrite(tmpPath, crop); !ok {
		return fmt.Errorf("write %s", tmpPath)
	}
	defer func() { _ = os.Remove(tmpPath) }()

	tpl := gocv.IMRead(tmpPath, gocv.IMReadColor)
	if tpl.Empty() {
		return fmt.Errorf("re-read %s", tmpPath)
	}
	defer tpl.Close()
	matches, err := vision.MatchMultiScaleROICached(screen, tpl, name, 0.9, 1.1, 3, 0.7,
		image.Rect(0, 0, screen.Cols(), screen.Rows()))
	if err != nil {
		return err
	}
	if len(matches) == 0 {
		return fmt.Errorf("crop does not match the frame — re-measure the rect (tight around the target)")
	}

	dst := filepath.Join(outDir, name+".png")
	if prev, err := os.ReadFile(dst); err == nil {
		_ = os.WriteFile(dst+".bak", prev, 0o644)
	}
	if err := os.Rename(tmpPath, dst); err != nil {
		return fmt.Errorf("rename into %s: %w", dst, err)
	}
	return nil
}

// writeSwitchConfig merges the row geometry into account_switch.json without
// dropping the other tunables.
func writeSwitchConfig(path string, row1Offset, rowSpacing float64) error {
	cfg, err := game.LoadSwitchConfig(path)
	if err != nil {
		cfg = game.DefaultSwitchConfig()
	}
	if row1Offset > 0 {
		cfg.SlotRow1Offset = row1Offset
	}
	if rowSpacing > 0 {
		cfg.SlotRowSpacing = rowSpacing
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return paths.WriteFileAtomic(path, data, 0o644)
}

func missingTemplates(dir string) []string {
	var missing []string
	for _, name := range []string{
		game.AcctProfileTemplate,
		game.AcctSettingsTemplate,
		game.AcctSupercellTemplate,
		game.AcctSlotListTemplate,
	} {
		if _, err := os.Stat(filepath.Join(dir, name+".png")); err != nil {
			missing = append(missing, name)
		}
	}
	return missing
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "ERROR: "+format+"\n", args...)
	os.Exit(1)
}
