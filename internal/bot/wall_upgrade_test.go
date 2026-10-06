package bot

import (
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gocv.io/x/gocv"

	"github.com/Ducky705/ClashGO/internal/game"
	"github.com/rs/zerolog"
)

// The wall-upgrade sequence is the only part of the bot that taps the screen
// with no affordability evidence in front of it, and until this file existed it
// had no tests at all. These drive the REAL RunWallUpgradeLoop against a fake
// device and a temp asset tree, so every claim below is about the shipped code
// path — including the vision-gated first half (the loop only reaches the
// upgrade buttons after `text_wall` matches, so the fake paints a real template
// hit instead of stubbing the matcher out).
//
// The synthetic geometry is deliberately small (a 200x480 "screen" with a tight
// builder-menu ROI) because template matching runs for real here. The loop
// derives every ROI from the captured frame and the calibration, so a small frame
// exercises the same arithmetic as a real device at a fraction of the
// matchTemplate cost. Sleeps are injected away (WallUpgradeHooks.Sleep), so the
// whole file runs in ~2s despite a production timing budget of ~20s per
// iteration.

// ---------------------------------------------------------------------------
// Synthetic screen furniture
// ---------------------------------------------------------------------------

// testFrame is the fake device's screen size. It must be taller than the legacy
// flow's fixed bottom ROI (y >= 400) for that path to have search area.
const (
	testFrameW = 200
	testFrameH = 480

	// testMenuROI is the builder-menu region the loop is told to search (a temp
	// builder_menu_roi.json overrides the 860x732 default, which a 200x480 frame
	// cannot contain).
	testMenuROIX1 = 20
	testMenuROIY1 = 90
	testMenuROIX2 = 140
	testMenuROIY2 = 130

	// wallLabelX/Y place the `text_wall` template hit inside testMenuROI.
	wallLabelX = 30
	wallLabelY = 100
	wallLabelW = 24
	wallLabelH = 10
)

// wallLabelRects is the coarse pattern stamped as `text_wall` — the shape the
// loop must recognize in the builder menu. The blocks are large on purpose: a
// low-frequency pattern survives the 0.3..1.5 scale sweep without dropping under
// the loop's 0.78 threshold.
var wallLabelRects = []image.Rectangle{
	image.Rect(1, 1, 9, 4),
	image.Rect(12, 1, 23, 4),
	image.Rect(2, 6, 13, 9),
	image.Rect(15, 6, 22, 9),
}

// The wall-upgrade rects the tests write into their temp assets. They mirror the
// shipped assets/wall_upgrade_*.json schema and are scaled down to testFrame,
// keeping every rect inside the frame (hasModalInRect clamps) and mutually
// disjoint (the fake can tell which rect a tap landed on).
var (
	testGoldRect    = Rect{X1: 20, Y1: 300, X2: 60, Y2: 340}
	testElixirRect  = Rect{X1: 70, Y1: 300, X2: 110, Y2: 340}
	testConfirmRect = Rect{X1: 120, Y1: 300, X2: 180, Y2: 340}
	testXRect       = Rect{X1: 150, Y1: 20, X2: 190, Y2: 60}
	testXAltRect    = Rect{X1: 5, Y1: 5, X2: 40, Y2: 40}

	// testLegacyCandidate is where the legacy flow's btn_upgrade_wall match is
	// painted (inside the y >= 400 bottom ROI).
	testLegacyCandidate = image.Rect(60, 430, 80, 450)

	// testBuilderButtonRect is the box a picking session would drag around the
	// builder-head button. It is disjoint from every other rect here, so a tap
	// inside it can only have come from assets/builder_button.json.
	testBuilderButtonRect = Rect{X1: 150, Y1: 130, X2: 190, Y2: 150}

	// goldChipFace / elixirChipFace are the painted faces the tray gate looks
	// for: saturated (a button face) rather than the near-white, zero-saturation
	// glyphs a builder-menu row is drawn with. The values are CoC's measured
	// palettes — warm gold (247,177,54) and the elixir-family purple.
	goldChipFace   = color.RGBA{247, 177, 54, 255}
	elixirChipFace = color.RGBA{206, 92, 222, 255}

	// confirmFaceGreen is the CONFIRM control's face (green with a white
	// caption); popupRedFace is the buy-with-gems popup's close control (a red
	// disc with a white glyph). Both are the palettes the two pixel signatures
	// in wall_upgrade.go were measured against.
	confirmFaceGreen = color.RGBA{70, 190, 70, 255}
	popupRedFace     = color.RGBA{235, 40, 40, 255}
)

func wallLabelTemplate() gocv.Mat {
	m := gocv.NewMatWithSize(wallLabelH, wallLabelW, gocv.MatTypeCV8UC3)
	m.SetTo(gocv.NewScalar(255, 255, 255, 0))
	for _, r := range wallLabelRects {
		gocv.Rectangle(&m, r, color.RGBA{20, 20, 20, 255}, -1)
	}
	return m
}

// stampWallLabel paints the template hit into a frame at wallLabelX/y.
func stampWallLabel(frame *gocv.Mat, y int) {
	gocv.Rectangle(frame,
		image.Rect(wallLabelX, y, wallLabelX+wallLabelW, y+wallLabelH),
		color.RGBA{255, 255, 255, 255}, -1)
	for _, r := range wallLabelRects {
		gocv.Rectangle(frame,
			image.Rect(wallLabelX+r.Min.X, y+r.Min.Y, wallLabelX+r.Max.X, y+r.Max.Y),
			color.RGBA{20, 20, 20, 255}, -1)
	}
}

// stampUpgradeButton paints the btn_upgrade_wall candidate the legacy flow has to
// find in the bottom ROI. Pattern identical to the template PNG.
func stampUpgradeButton(frame *gocv.Mat) {
	gocv.Rectangle(frame, testLegacyCandidate, color.RGBA{240, 240, 240, 255}, -1)
	gocv.Rectangle(frame, testLegacyCandidate.Inset(4), color.RGBA{30, 30, 30, 255}, 2)
}

// ---------------------------------------------------------------------------
// Fake device
// ---------------------------------------------------------------------------

// wallFakeDevice is a scripted Clash of Clans. It paints a frame per capture from
// its current UI state and mutates that state in response to taps, which is what
// makes the assertions below about observable UI consequences rather than about a
// hardcoded tap list.
type wallFakeDevice struct {
	// wallVisible: the "Wall" row is in the builder menu (the loop's entry gate).
	wallVisible bool
	// primaryUp / altUp: the buy-with-gems popup(s) are on screen.
	primaryUp bool
	altUp     bool
	// dialogUp: the confirm dialog is open. Its CONFIRM control is painted at
	// testConfirmRect (a green face with a white caption — the pattern
	// confirmButtonDrawn reads), the tray chips are behind it, and tapping a tray
	// chip does nothing until Back() closes it. That last part is what makes the
	// flow's leaveConfirmDialog load-bearing: without it the elixir chip tap
	// lands on the dialog and the resource never switches.
	dialogUp bool
	// confirmControlAbsent: the dialog is up but its CONFIRM control is not
	// drawn where the asset maps it — the live state the confirmButtonDrawn
	// pre-tap guard exists for.
	confirmControlAbsent bool
	// affordable: tapping the resource button leads to an upgrade instead of the
	// gem-buy popup.
	affordable bool
	// ignoreXTaps: simulates a mis-picked X rect (taps fire, popup stays up).
	ignoreXTaps bool
	// trayAbsent: the wall-upgrade tray is NOT on screen — the rects the assets
	// predict hold builder-menu furniture instead of painted chips. This is the
	// live 1280x720 state the tray gate exists for.
	trayAbsent bool
	// altAlwaysLit: the alternate X rect is covered by permanent screen chrome
	// (a resource readout on the live 720p run), so it reads bright in every
	// frame — before any tap as well as after one.
	altAlwaysLit bool
	// upgradeButtonVisible: the legacy flow's btn_upgrade_wall candidate is on
	// screen.
	upgradeButtonVisible bool

	// labelY / labelStep model a builder menu that is still gliding: the wall row
	// sits at labelY (default wallLabelY) and each capture moves it by labelStep.
	// A negative step is a menu scrolling up, which is the direction the live run
	// moved between the match and the tap.
	labelY    int
	labelStep int

	taps    []image.Point
	swipes  int
	backs   int
	caps    int
	failAt  map[int]bool
	failAll bool
}

func (f *wallFakeDevice) Tap(x, y int) error {
	f.taps = append(f.taps, image.Pt(x, y))
	p := image.Pt(x, y)
	switch {
	case p.In(testConfirmRect.ImageRect()):
		if f.affordable {
			// CoC spawns the upgrade and closes the dialog; the row stops
			// being upgradable, which is what ends a real sequence.
			f.wallVisible = false
			f.dialogUp = false
		} else {
			// The buy-with-gems popup, on top of the still-open dialog.
			f.primaryUp = true
		}
	case p.In(testGoldRect.ImageRect()) || p.In(testElixirRect.ImageRect()):
		// A tray resource chip opens the confirm dialog. The tray is covered
		// while the dialog is up, so the chip is only reachable after Back().
		if !f.dialogUp {
			f.dialogUp = true
		}
	case p.In(testLegacyCandidate):
		// The legacy flow's own btn_upgrade_wall candidate.
		if f.affordable {
			f.wallVisible = false
		} else {
			f.primaryUp = true
		}
	case p.In(testXRect.ImageRect()):
		if !f.ignoreXTaps {
			f.primaryUp = false
			// The chained second popup is revealed by closing the first.
			f.altUp = true
		}
	case p.In(testXAltRect.ImageRect()):
		if !f.ignoreXTaps {
			f.altUp = false
		}
	}
	return nil
}

func (f *wallFakeDevice) TapRandomized(x, y int) error { return f.Tap(x, y) }
func (f *wallFakeDevice) Swipe(int, int, int, int, int) error {
	f.swipes++
	return nil
}
func (f *wallFakeDevice) Back() error {
	f.backs++
	// Leaving the confirm dialog puts the tray chips back on screen.
	f.dialogUp = false
	return nil
}

var errWallFakeCapture = errors.New("fake capture failed")

func (f *wallFakeDevice) CaptureToMat() (gocv.Mat, error) {
	f.caps++
	if f.failAll || f.failAt[f.caps] {
		return gocv.NewMat(), errWallFakeCapture
	}
	m := gocv.NewMatWithSize(testFrameH, testFrameW, gocv.MatTypeCV8UC3)
	m.SetTo(gocv.NewScalar(60, 60, 60, 0))
	if f.dialogUp && !f.primaryUp && !f.altUp && !f.confirmControlAbsent {
		// The confirm dialog covers the tray, and its CONFIRM control is a
		// saturated green face with a near-white caption.
		gocv.Rectangle(&m, testConfirmRect.ImageRect(), confirmFaceGreen, -1)
		gocv.Rectangle(&m, testConfirmRect.ImageRect().Inset(4), color.RGBA{255, 255, 255, 255}, -1)
	} else if !f.trayAbsent && !f.dialogUp {
		// The tray's two upgrade chips, painted as saturated faces. The tray
		// gate reads the bright-SATURATED mask, so a grey or white fill would be
		// rejected exactly like the builder-menu label glyphs it has to tell a
		// real tray apart from.
		gocv.Rectangle(&m, testGoldRect.ImageRect(), goldChipFace, -1)
		gocv.Rectangle(&m, testElixirRect.ImageRect(), elixirChipFace, -1)
	}
	if f.wallVisible && !f.dialogUp {
		// The builder menu's row is replaced by the confirm dialog. Hiding it
		// here is what makes the loop's exit on a vanished row real: without it
		// a flow that declares "silent spawn" success would re-select the same
		// still-visible row for ever.
		f.labelY += f.labelStep
		stampWallLabel(&m, f.labelY)
	}
	if f.upgradeButtonVisible {
		stampUpgradeButton(&m)
	}
	if f.primaryUp {
		// The buy-with-gems popup's close control: a red face with a white
		// glyph. popupRedPixelsMin was measured against this palette.
		gocv.Rectangle(&m, testXRect.ImageRect(), popupRedFace, -1)
		gocv.Rectangle(&m, testXRect.ImageRect().Inset(8), color.RGBA{255, 255, 255, 255}, -1)
	}
	if f.altUp || f.altAlwaysLit {
		gocv.Rectangle(&m, testXAltRect.ImageRect().Inset(-2), popupRedFace, -1)
	}
	return m, nil
}

// ---------------------------------------------------------------------------
// Harness
// ---------------------------------------------------------------------------

// wallHarness is one wired-up run of the loop plus everything the test asserts
// against: the step trace, the dismiss count and the taps.
type wallHarness struct {
	h       *WallUpgradeHooks
	dev     *wallFakeDevice
	steps   []string
	taps    []image.Point
	payload map[string]map[string]any
	dismiss int
	stopped bool
}

// newWallHarness builds the hooks struct against a temp asset tree.
//
// rectOpts selects which rect assets exist, mirroring the flow-selection gate in
// RunWallUpgradeLoop ("all" = asset-driven, "buttons" = probe-and-discard,
// "none" = legacy template flow).
func newWallHarness(t *testing.T, dev *wallFakeDevice, rectOpts string, templates ...string) *wallHarness {
	t.Helper()

	dir := t.TempDir()
	tplDir := filepath.Join(dir, "templates")
	if err := os.MkdirAll(tplDir, 0o755); err != nil {
		t.Fatalf("mkdir templates: %v", err)
	}
	for _, name := range templates {
		writeWallTemplate(t, tplDir, name)
	}
	// The builder-menu ROI has to be overridden for this frame size; without it
	// the loop's 860x732 default lands outside a 200x480 capture and every search
	// returns no area.
	writeJSON(t, filepath.Join(dir, "builder_menu_roi.json"), map[string]any{
		"physical": map[string]int{
			"x1": testMenuROIX1, "y1": testMenuROIY1,
			"x2": testMenuROIX2, "y2": testMenuROIY2,
		},
	})
	switch rectOpts {
	case "all":
		writeWallButtonsAsset(t, dir)
		writeJSON(t, filepath.Join(dir, "wall_upgrade_confirm.json"), map[string]any{
			"confirm_button": testConfirmRect,
		})
		writeJSON(t, filepath.Join(dir, "wall_upgrade_x_roi.json"), map[string]any{
			"x_popup_roi":     testXRect,
			"x_popup_roi_alt": testXAltRect,
		})
	case "buttons":
		writeWallButtonsAsset(t, dir)
	case "none":
	default:
		t.Fatalf("newWallHarness: unknown rectOpts %q", rectOpts)
	}
	t.Setenv("CLASHGO_ASSETS_DIR", dir)

	ts, err := game.NewTemplateStore(tplDir)
	if err != nil {
		t.Fatalf("NewTemplateStore: %v", err)
	}
	if err := ts.LoadTemplates(); err != nil {
		t.Fatalf("LoadTemplates: %v", err)
	}
	t.Cleanup(ts.Close)

	if dev.labelY == 0 {
		dev.labelY = wallLabelY
	}
	hr := &wallHarness{dev: dev, payload: map[string]map[string]any{}}
	hr.h = &WallUpgradeHooks{
		Logger:    zerolog.Nop(),
		Client:    dev,
		Cal:       testIdentityCal(),
		Templates: ts,
		// Classify nil = "the user is already in MainVillage", which keeps the
		// 30s classifier poll out of the way (the diagnostic tool's manual mode
		// does the same).
		Classify: nil,
		Sleep:    func(time.Duration) {},
		Dismiss:  func() { hr.dismiss++ },
		StopCheck: func() bool {
			return hr.stopped
		},
		OnStep: func(step string, data map[string]any) {
			hr.steps = append(hr.steps, step)
			hr.payload[step] = data
		},
	}
	return hr
}

func (hr *wallHarness) run() {
	RunWallUpgradeLoop(hr.h)
	hr.taps = append(hr.taps, hr.dev.taps...)
}

func (hr *wallHarness) stepsContain(name string) bool {
	for _, s := range hr.steps {
		if s == name {
			return true
		}
	}
	return false
}

// tapped reports whether the loop tapped the centre of rect.
func (hr *wallHarness) tapped(r Rect) bool {
	cx, cy := r.Center()
	for _, p := range hr.taps {
		if p.X == cx && p.Y == cy {
			return true
		}
	}
	return false
}

func (hr *wallHarness) tapsInRect(r image.Rectangle) int {
	n := 0
	for _, p := range hr.taps {
		if p.In(r) {
			n++
		}
	}
	return n
}

func writeJSON(t *testing.T, path string, v any) {
	t.Helper()
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatalf("marshal %s: %v", path, err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func writeWallButtonsAsset(t *testing.T, dir string) {
	t.Helper()
	writeJSON(t, filepath.Join(dir, "wall_upgrade_buttons.json"), map[string]any{
		"gold":   testGoldRect,
		"elixir": testElixirRect,
	})
}

// writeWallTemplate writes a PNG named <name>.png that the loop can match:
// text_wall gets the synthetic label; btn_upgrade_wall gets the candidate stamp
// the legacy flow must find.
func writeWallTemplate(t *testing.T, dir, name string) {
	t.Helper()
	var m gocv.Mat
	switch name {
	case "text_wall":
		m = wallLabelTemplate()
	case "btn_upgrade_wall":
		m = gocv.NewMatWithSize(20, 20, gocv.MatTypeCV8UC3)
		m.SetTo(gocv.NewScalar(240, 240, 240, 0))
		gocv.Rectangle(&m, image.Rect(4, 4, 16, 16), color.RGBA{30, 30, 30, 255}, 2)
	case "btn_confirm_upgrade":
		m = gocv.NewMatWithSize(30, 60, gocv.MatTypeCV8UC3)
		m.SetTo(gocv.NewScalar(210, 210, 210, 0))
		gocv.Rectangle(&m, image.Rect(2, 2, 58, 28), color.RGBA{40, 40, 40, 255}, 2)
	default:
		t.Fatalf("writeWallTemplate: unknown template %q", name)
	}
	defer m.Close()
	if ok := gocv.IMWrite(filepath.Join(dir, name+".png"), m); !ok {
		t.Fatalf("IMWrite %s failed", name)
	}
}

// ---------------------------------------------------------------------------
// Unit tests: geometry + loaders
// ---------------------------------------------------------------------------

func TestWallRectGeometry(t *testing.T) {
	r := Rect{X1: 10, Y1: 20, X2: 30, Y2: 60}
	if cx, cy := r.Center(); cx != 20 || cy != 40 {
		t.Fatalf("Center = (%d,%d), want (20,40)", cx, cy)
	}
	if r.Empty() {
		t.Fatal("non-degenerate rect reported Empty")
	}
	for name, degenerate := range map[string]Rect{
		"zero":      {},
		"zero wide": {X1: 5, X2: 5, Y1: 0, Y2: 10},
		"zero tall": {X1: 0, X2: 10, Y1: 5, Y2: 5},
	} {
		if !degenerate.Empty() {
			t.Errorf("%s: degenerate rect not reported Empty", name)
		}
	}
	// A reversed drag must still produce a usable rectangle: image.Rect
	// normalises, so the loaders' Empty() gate is the only rejection.
	rev := Rect{X1: 30, Y1: 60, X2: 10, Y2: 20}
	if got := rev.ImageRect(); got != image.Rect(10, 20, 30, 60) {
		t.Fatalf("reversed ImageRect = %v", got)
	}
	if cx, cy := rev.Center(); cx != 20 || cy != 40 {
		t.Fatalf("reversed Center = (%d,%d), want (20,40)", cx, cy)
	}
}

func TestWallUpgradeAssetLoaders(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLASHGO_ASSETS_DIR", dir)
	log := zerolog.Nop()
	cal := testIdentityCal()

	// Nothing on disk: every loader must refuse rather than invent a rect.
	if _, _, ok := loadWallUpgradeButtons(cal, log); ok {
		t.Fatal("buttons loader accepted a missing file")
	}
	if _, ok := loadWallUpgradeConfirmRect(cal, log); ok {
		t.Fatal("confirm loader accepted a missing file")
	}
	if _, _, ok := loadWallUpgradeXPopupRect(cal, log); ok {
		t.Fatal("x-popup loader accepted a missing file")
	}

	// Degenerate rects must be rejected too: a zero-area confirm rect would
	// silently tap the screen origin.
	writeJSON(t, filepath.Join(dir, "wall_upgrade_buttons.json"), map[string]any{
		"gold":   Rect{X1: 0, Y1: 0, X2: 0, Y2: 0},
		"elixir": testElixirRect,
	})
	if _, _, ok := loadWallUpgradeButtons(cal, log); ok {
		t.Fatal("buttons loader accepted a degenerate gold rect")
	}

	writeWallButtonsAsset(t, dir)
	gold, elixir, ok := loadWallUpgradeButtons(cal, log)
	if !ok || gold != testGoldRect || elixir != testElixirRect {
		t.Fatalf("buttons loader = %+v/%+v ok=%v", gold, elixir, ok)
	}

	writeJSON(t, filepath.Join(dir, "wall_upgrade_confirm.json"), map[string]any{"confirm_button": testConfirmRect})
	if got, ok := loadWallUpgradeConfirmRect(cal, log); !ok || got != testConfirmRect {
		t.Fatalf("confirm loader = %+v ok=%v", got, ok)
	}

	// A degenerate alt rect must be demoted to nil, not kept as a zero-area rect
	// that would mask a real chained popup as absent.
	writeJSON(t, filepath.Join(dir, "wall_upgrade_x_roi.json"), map[string]any{
		"x_popup_roi":     testXRect,
		"x_popup_roi_alt": Rect{},
	})
	primary, alt, ok := loadWallUpgradeXPopupRect(cal, log)
	if !ok || primary != testXRect {
		t.Fatalf("x-popup loader = %+v ok=%v", primary, ok)
	}
	if alt != nil {
		t.Fatalf("degenerate alt kept as %+v, want nil", *alt)
	}

}

// TestWallUpgradeLoadersMapShippedAssetsToLiveGeometry drives the loaders with
// the exact numbers assets/wall_upgrade_*.json hold today, at the geometry the
// device runs (1280x720, k pinned 1.325), and asserts the LIVE rects. Consuming
// those numbers raw — which is what the loop did before Rect.Live existed — put
// the resource buttons ~210 px and the gem-buy X ~110 px from their widgets, so
// the blind-tap flow fired into the village instead of the tray.
func TestWallUpgradeLoadersMapShippedAssetsToLiveGeometry(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLASHGO_ASSETS_DIR", dir)
	log := zerolog.Nop()
	live := testLive1280x720Cal()

	writeJSON(t, filepath.Join(dir, "wall_upgrade_buttons.json"), map[string]any{
		"gold":   Rect{X1: 393, Y1: 568, X2: 469, Y2: 642},
		"elixir": Rect{X1: 488, Y1: 568, X2: 563, Y2: 642},
	})
	writeJSON(t, filepath.Join(dir, "wall_upgrade_confirm.json"), map[string]any{
		"confirm_button": Rect{X1: 548, Y1: 631, X2: 700, Y2: 692},
	})
	writeJSON(t, filepath.Join(dir, "wall_upgrade_x_roi.json"), map[string]any{
		"x_popup_roi":     Rect{X1: 602, Y1: 239, X2: 647, Y2: 279},
		"x_popup_roi_alt": Rect{X1: 789, Y1: 115, X2: 824, Y2: 149},
	})

	gold, elixir, ok := loadWallUpgradeButtons(live, log)
	if !ok {
		t.Fatal("buttons loader refused the shipped rects at 1280x720")
	}
	if want := (Rect{X1: 591, Y1: 503, X2: 692, Y2: 601}); gold != want {
		t.Errorf("gold = %+v, want %+v", gold, want)
	}
	if want := (Rect{X1: 717, Y1: 503, X2: 816, Y2: 601}); elixir != want {
		t.Errorf("elixir = %+v, want %+v", elixir, want)
	}
	confirm, ok := loadWallUpgradeConfirmRect(live, log)
	if !ok {
		t.Fatal("confirm loader refused the shipped rect at 1280x720")
	}
	if want := (Rect{X1: 796, Y1: 586, X2: 998, Y2: 667}); confirm != want {
		t.Errorf("confirm = %+v, want %+v", confirm, want)
	}
	primary, alt, ok := loadWallUpgradeXPopupRect(live, log)
	if !ok {
		t.Fatal("x-popup loader refused the shipped rect at 1280x720")
	}
	if want := (Rect{X1: 868, Y1: 192, X2: 928, Y2: 245}); primary != want {
		t.Errorf("x_popup_roi = %+v, want %+v", primary, want)
	}
	if alt == nil {
		t.Fatal("x_popup_roi_alt was dropped")
	}
	if want := (Rect{X1: 1186, Y1: 152, X2: 1232, Y2: 197}); *alt != want {
		t.Errorf("x_popup_roi_alt = %+v, want %+v", *alt, want)
	}

	// The shipped file's x_button point must land on the modal, too: the
	// probe-and-discard flow taps it as the close button.
	writeJSON(t, filepath.Join(dir, "wall_upgrade_modal.json"), map[string]any{
		"x_button": map[string]int{"x": 807, "y": 133},
	})
	if x, y := loadWallUpgradeXButton(live, log); x != 1210 || y != 176 {
		t.Errorf("modal X at 1280x720 = (%d,%d), want (1210,176)", x, y)
	}
}

func TestWallXButtonDefaultAndOverride(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLASHGO_ASSETS_DIR", dir)
	log := zerolog.Nop()
	cal := testIdentityCal()

	x, y := loadWallUpgradeXButton(cal, log)
	if x != 800 || y != 100 {
		t.Fatalf("default X button = (%d,%d), want (800,100) at the reference geometry", x, y)
	}
	writeJSON(t, filepath.Join(dir, "wall_upgrade_modal.json"), map[string]any{
		"x_button": map[string]int{"x": 807, "y": 133},
	})
	if x, y := loadWallUpgradeXButton(cal, log); x != 807 || y != 133 {
		t.Fatalf("picked X button = (%d,%d), want (807,133) at the reference geometry", x, y)
	}

	// 1280x720: the point is reference-frame, so it is mapped (right-anchored x,
	// top-anchored y) rather than tapped at 807,133 — which on the live device is
	// the middle of the gem-buy dialog, not its close button.
	live := testLive1280x720Cal()
	if x, y := loadWallUpgradeXButton(live, log); x != 1210 || y != 176 {
		t.Fatalf("picked X button at 1280x720 = (%d,%d), want (1210,176)", x, y)
	}
}

// testIdentityCal is the reported-geometry calibration: a zero PhysicalW/H is
// what Calibration.IsReferenceGeometry treats as "no mapping information", so
// every mapping is the identity — which is what the small-frame fixtures below
// and the authored 860x732 dataset both rely on.
func testIdentityCal() *game.Calibration {
	return &game.Calibration{ScaleX: 1, ScaleY: 1}
}

// testLive1280x720Cal is the geometry the device actually runs (config.json:
// 1280x720 + display_scale pinned to 1.325, the value measured with
// cmd/resprobe — see docs/RESOLUTION.md).
func testLive1280x720Cal() *game.Calibration {
	cal := game.NewCalibration(1280, 720)
	cal.SetDisplayScale(1.325)
	return cal
}

// TestWallRectLiveMapping pins the reference->live law with the SHIPPED asset
// numbers, so a change to either the assets or the mapping has to be deliberate.
// These four rects are what assets/wall_upgrade_*.json hold today, and every one
// of them was picked at 860x732 (their pickers write `reference = physical/scale`
// and the files carry physical == reference).
func TestWallRectLiveMapping(t *testing.T) {
	live := testLive1280x720Cal()
	for _, tc := range []struct {
		name     string
		ref      Rect
		wantRect Rect
		wantCX   int
		wantCY   int
	}{
		{"gold", Rect{X1: 393, Y1: 568, X2: 469, Y2: 642}, Rect{X1: 591, Y1: 503, X2: 692, Y2: 601}, 641, 552},
		{"elixir", Rect{X1: 488, Y1: 568, X2: 563, Y2: 642}, Rect{X1: 717, Y1: 503, X2: 816, Y2: 601}, 766, 552},
		{"confirm", Rect{X1: 548, Y1: 631, X2: 700, Y2: 692}, Rect{X1: 796, Y1: 586, X2: 998, Y2: 667}, 897, 626},
		{"x_popup", Rect{X1: 602, Y1: 239, X2: 647, Y2: 279}, Rect{X1: 868, Y1: 192, X2: 928, Y2: 245}, 898, 218},
		{"x_popup_alt", Rect{X1: 789, Y1: 115, X2: 824, Y2: 149}, Rect{X1: 1186, Y1: 152, X2: 1232, Y2: 197}, 1209, 174},
	} {
		got := tc.ref.Live(live)
		if got != tc.wantRect {
			t.Errorf("%s: Live = %+v, want %+v", tc.name, got, tc.wantRect)
		}
		if cx, cy := got.Center(); cx != tc.wantCX || cy != tc.wantCY {
			t.Errorf("%s: live centre = (%d,%d), want (%d,%d)", tc.name, cx, cy, tc.wantCX, tc.wantCY)
		}
		if got.Empty() {
			t.Errorf("%s: mapped rect is degenerate", tc.name)
		}
		// The mapped rect has to stay a legal ROI on the live frame, or the
		// popup probe and the tap land outside the screen.
		if !image.Rect(0, 0, 1280, 720).Union(got.ImageRect()).Eq(image.Rect(0, 0, 1280, 720)) {
			t.Errorf("%s: mapped rect %v is outside 1280x720", tc.name, got)
		}
	}

	// Identity at the reference geometry, for nil, and for a zero-value
	// Calibration — the three cases the authored dataset and the tests rely on.
	ref := NewTestReferenceCal()
	for _, cal := range []*game.Calibration{nil, testIdentityCal(), ref} {
		r := Rect{X1: 393, Y1: 568, X2: 469, Y2: 642}
		if got := r.Live(cal); got != r {
			t.Errorf("Live(%v) = %+v, want identity %+v", cal, got, r)
		}
	}
}

// NewTestReferenceCal is the authored 860x732 geometry, where every mapping is
// the identity by construction.
func NewTestReferenceCal() *game.Calibration {
	return game.NewCalibration(game.RefWidth, game.RefHeight)
}

// ---------------------------------------------------------------------------
// Unit tests: pixel checks (including the inverted-ROI crash guard)
// ---------------------------------------------------------------------------

func TestHasModalInRect(t *testing.T) {
	frame := gocv.NewMatWithSize(testFrameH, testFrameW, gocv.MatTypeCV8UC3)
	defer frame.Close()
	frame.SetTo(gocv.NewScalar(60, 60, 60, 0))

	if up, px := hasModalInRect(frame, testXRect); up || px != 0 {
		t.Fatalf("dark ROI reported modal up (px=%d)", px)
	}
	gocv.Rectangle(&frame, testXRect.ImageRect(), color.RGBA{255, 255, 255, 255}, -1)
	up, px := hasModalInRect(frame, testXRect)
	if !up {
		t.Fatalf("white X-button ROI not detected as modal (px=%d)", px)
	}
	// Off-frame and degenerate rects must answer "down", never touch OpenCV with
	// an illegal ROI.
	if up, _ := hasModalInRect(frame, Rect{X1: 5000, Y1: 5000, X2: 5100, Y2: 5100}); up {
		t.Fatal("off-frame rect reported modal up")
	}
	if up, _ := hasModalInRect(frame, Rect{}); up {
		t.Fatal("zero rect reported modal up")
	}
}

// TestCheckRedRefusesInvertedROI pins the regression where a cost band below a
// candidate in the bottom rows clamped to Min > Max and was handed to
// Mat.Region, which aborts the process rather than returning an error. Both
// checks must answer "not red" instead.
func TestCheckRedRefusesInvertedROI(t *testing.T) {
	frame := gocv.NewMatWithSize(testFrameH, testFrameW, gocv.MatTypeCV8UC3)
	defer frame.Close()
	frame.SetTo(gocv.NewScalar(0, 0, 255, 0)) // saturated red (BGR) everywhere

	// A band wholly below the frame: clamping pulls Max.Y back to the frame edge
	// while Min.Y stays past it, i.e. the inverted rect.
	below := image.Rect(20, testFrameH+10, 120, testFrameH+90)
	if checkConfirmRed(frame, below, zerolog.Nop()) {
		t.Fatal("checkConfirmRed reported red for an inverted rect")
	}
	if red, px := checkUpgradeTrayRedWithCount(frame, below, zerolog.Nop()); red || px != 0 {
		t.Fatalf("checkUpgradeTrayRedWithCount = (%v,%d) for an inverted rect, want (false,0)", red, px)
	}

	// A valid red band still reads red, so the guard did not disable the check.
	inFrame := image.Rect(20, 100, 120, 160)
	if !checkConfirmRed(frame, inFrame, zerolog.Nop()) {
		t.Fatal("checkConfirmRed lost a valid in-frame red band")
	}
	if red, px := checkUpgradeTrayRedWithCount(frame, inFrame, zerolog.Nop()); !red || px == 0 {
		t.Fatalf("checkUpgradeTrayRedWithCount = (%v,%d) for a valid red band", red, px)
	}
}

// TestStepScreenOwnership proves the frame-carrying instrumentation path still
// hands a live clone to a wired receiver, and hands nothing at all when no
// receiver is wired (the leak the no-receiver path used to create).
func TestStepScreenOwnership(t *testing.T) {
	frame := gocv.NewMatWithSize(testFrameH, testFrameW, gocv.MatTypeCV8UC3)
	defer frame.Close()

	// No receiver: no clone is made, and the payload keeps no reference.
	h := &WallUpgradeHooks{}
	payload := map[string]any{"x": 1}
	h.stepScreen("phase", frame, payload)
	if _, ok := payload["screen"]; ok {
		t.Fatal("no-receiver step retained a screen clone")
	}
	if len(payload) != 1 {
		t.Fatalf("no-receiver step mutated payload: %v", payload)
	}

	// Wired receiver owns the clone.
	var got gocv.Mat
	h.OnStep = func(_ string, data map[string]any) {
		if m, ok := data["screen"].(gocv.Mat); ok {
			got = m
		}
	}
	h.stepScreen("phase", frame, nil)
	if got.Empty() || got.Closed() {
		t.Fatal("receiver got no usable clone")
	}
	if got.Cols() != testFrameW || got.Rows() != testFrameH {
		t.Fatalf("clone is %dx%d", got.Cols(), got.Rows())
	}
	got.Close()
	if frame.Empty() || frame.Closed() {
		t.Fatal("closing the clone invalidated the source frame")
	}
}

// ---------------------------------------------------------------------------
// Full-loop tests (asset-driven blind flow)
// ---------------------------------------------------------------------------

func TestWallLoopAffordableUpgradeThenNoMoreOptions(t *testing.T) {
	dev := &wallFakeDevice{wallVisible: true, affordable: true}
	hr := newWallHarness(t, dev, "all", "text_wall")
	hr.run()

	if !hr.stepsContain("upgrade_success") {
		t.Fatalf("no upgrade_success in %v", hr.steps)
	}
	if hr.steps[len(hr.steps)-1] != "sequence_end" {
		t.Fatalf("last step = %q, want sequence_end (%v)", hr.steps[len(hr.steps)-1], hr.steps)
	}
	// The upgrade path: builder head, wall row, gold, blind confirm.
	if !hr.tapped(testGoldRect) {
		t.Fatalf("gold button never tapped: %v", hr.taps)
	}
	if !hr.tapped(testConfirmRect) {
		t.Fatalf("confirm never tapped: %v", hr.taps)
	}
	if hr.tapped(testElixirRect) {
		t.Fatalf("elixir tapped after gold already succeeded: %v", hr.taps)
	}
	if hr.stepsContain("all_unaffordable") {
		t.Fatal("affordable run reported all_unaffordable")
	}
	// Once the row is gone the loop must stop rather than spin.
	if !hr.stepsContain("wall_text_not_found") {
		t.Fatalf("loop did not exit on a vanished wall row: %v", hr.steps)
	}
}

// TestWallLoopDismissedPopupIsUnaffordable is the regression test for the
// unbounded loop: an unaffordable wall raises the gem-buy popup, so dismissing
// it must mean "this resource cannot pay", NOT "the upgrade succeeded". Before
// the fix the dismissed popup was reported as a silent spawn, which made the
// outer loop re-select the same wall for ever — every iteration raised the same
// popup, closed it and declared success, so the sequence only ever ended on a
// mis-picked X rect or a user Stop.
func TestWallLoopDismissedPopupIsUnaffordable(t *testing.T) {
	// Unaffordable: the gem-buy popup appears on both resources. Closing the
	// primary X reveals the chained second popup, whose X is the alt rect.
	dev := &wallFakeDevice{wallVisible: true, affordable: false}
	hr := newWallHarness(t, dev, "all", "text_wall")
	hr.run()

	if !hr.stepsContain("asset_driven_modal_checked") {
		t.Fatalf("popup never observed: %v", hr.steps)
	}
	checked := hr.payload["asset_driven_modal_checked"]
	if up, _ := checked["primary_up"].(bool); !up {
		t.Fatalf("primary popup not detected: %v", checked)
	}
	if !hr.tapped(testXRect) {
		t.Fatalf("primary X never tapped: %v", hr.taps)
	}
	if !hr.tapped(testXAltRect) {
		t.Fatalf("chained alt X never tapped: %v", hr.taps)
	}
	verified := hr.payload["asset_driven_modal_verified"]
	if up, _ := verified["all_up"].(bool); up {
		t.Fatalf("verify phase still reports a popup up: %v", verified)
	}
	if hr.stepsContain("asset_driven_silent_spawn") {
		t.Fatalf("a dismissed gem popup was reported as a successful upgrade: %v", hr.steps)
	}
	if !hr.stepsContain("asset_driven_unaffordable_dismissed") {
		t.Fatalf("no unaffordable marker for the dismissed popup: %v", hr.steps)
	}
	// Both resources were unaffordable, so the sequence must END rather than
	// loop back onto the same wall.
	if !hr.stepsContain("all_unaffordable") {
		t.Fatalf("dismissed popups did not end the sequence: %v", hr.steps)
	}
	if !hr.tapped(testGoldRect) || !hr.tapped(testElixirRect) {
		t.Fatalf("gold -> elixir fallthrough missing: %v", hr.taps)
	}
	if hr.dismiss != 1 {
		t.Fatalf("dismissals = %d, want exactly 1 at sequence end", hr.dismiss)
	}
	if hr.steps[len(hr.steps)-1] != "sequence_end" {
		t.Fatalf("sequence did not close: %v", hr.steps)
	}
}

func TestWallLoopBothResourcesUnaffordable(t *testing.T) {
	// X taps that do not clear the popup = mis-picked X rect. The loop must try
	// the next resource (gold -> elixir), then end with all_unaffordable and run
	// exactly one dismissal.
	dev := &wallFakeDevice{wallVisible: true, affordable: false, ignoreXTaps: true}
	hr := newWallHarness(t, dev, "all", "text_wall")
	hr.run()

	if !hr.tapped(testGoldRect) || !hr.tapped(testElixirRect) {
		t.Fatalf("both resources must be tried: %v", hr.taps)
	}
	if !hr.stepsContain("asset_driven_modal_close_failed") {
		t.Fatalf("close failure not surfaced for a mis-picked X: %v", hr.steps)
	}
	if !hr.stepsContain("all_unaffordable") {
		t.Fatalf("no all_unaffordable exit: %v", hr.steps)
	}
	if hr.dismiss != 1 {
		t.Fatalf("dismissals = %d, want exactly 1 at sequence end", hr.dismiss)
	}
	if hr.stepsContain("upgrade_success") {
		t.Fatalf("reported success on an unaffordable wall: %v", hr.steps)
	}
}

func TestWallLoopStopSignalEndsSequenceWithoutTapping(t *testing.T) {
	dev := &wallFakeDevice{wallVisible: true, affordable: true}
	hr := newWallHarness(t, dev, "all", "text_wall")
	hr.stopped = true
	hr.run()

	if hr.stepsContain("builder_tapped") || len(dev.taps) != 0 {
		t.Fatalf("stop signal was not honoured before the first tap: %v / %v", hr.steps, dev.taps)
	}
	if !hr.stepsContain("stopped") || hr.steps[len(hr.steps)-1] != "sequence_end" {
		t.Fatalf("stop trace = %v", hr.steps)
	}
}

func TestWallLoopCaptureFailureAbortsDefensively(t *testing.T) {
	dev := &wallFakeDevice{wallVisible: true, affordable: false}
	hr := newWallHarness(t, dev, "all", "text_wall")
	// Capture 1 = waitForMainVillage, 2 = wall-text search, 3 = the fresh frame the
	// row tap is made from, 4 = the pre-tap baseline, 5 = the confirm page,
	// 6 = the post-confirm modal check. Failing #6 is the transport-hiccup case
	// with the popup state unknown, so the loop must tap both X centres and stop.
	// (#4 failing is the baseline capture and #5 the confirm-page capture, both
	// covered by their own tests.)
	dev.failAt = map[int]bool{6: true}
	hr.run()

	if !hr.stepsContain("aborted_capture_defensive") {
		t.Fatalf("no abort on a dead modal capture: %v", hr.steps)
	}
	if !hr.tapped(testXRect) || !hr.tapped(testXAltRect) {
		t.Fatalf("defensive double X tap missing: %v", hr.taps)
	}
	if hr.tapped(testElixirRect) {
		t.Fatalf("loop kept tapping the next resource after an abort: %v", hr.taps)
	}
	if hr.steps[len(hr.steps)-1] != "sequence_end" {
		t.Fatalf("abort did not end the sequence: %v", hr.steps)
	}
}

func TestWallLoopMissingWallTemplateStopsEarly(t *testing.T) {
	dev := &wallFakeDevice{wallVisible: true, affordable: true}
	hr := newWallHarness(t, dev, "all" /* no text_wall template installed */)
	hr.run()

	if !hr.stepsContain("wall_template_missing") {
		t.Fatalf("missing text_wall not reported: %v", hr.steps)
	}
	if dev.backs == 0 {
		t.Fatal("builder menu was not backed out of")
	}
	if hr.stepsContain("upgrade_success") {
		t.Fatal("loop upgraded without its navigation template")
	}
}

// ---------------------------------------------------------------------------
// Full-loop tests (fallback flows)
// ---------------------------------------------------------------------------

func TestWallLoopProbeFallbackNeedsConfirmTemplate(t *testing.T) {
	// Buttons asset present, confirm/x-popup assets absent: the probe-and-discard
	// flow is selected, and with no btn_confirm_upgrade template it must bail out
	// instead of matching against a nil template (which panics in gocv).
	dev := &wallFakeDevice{wallVisible: true, affordable: true}
	hr := newWallHarness(t, dev, "buttons", "text_wall")
	hr.run()

	if !hr.stepsContain("confirm_template_missing") {
		t.Fatalf("probe flow ran without its confirm template: %v", hr.steps)
	}
	if hr.dismiss != 1 {
		t.Fatalf("dismissals = %d, want 1", hr.dismiss)
	}
	if hr.tapped(testGoldRect) {
		t.Fatalf("tapped a button with no confirm template loaded: %v", hr.taps)
	}
}

// TestWallLoopBlindFlowNeedsAllThreeAssets pins the flow-selection gate: a
// buttons+confirm pair without the x-popup ROI must NOT take the blind path,
// because the blind path detects success by observing that popup.
func TestWallLoopBlindFlowNeedsAllThreeAssets(t *testing.T) {
	dev := &wallFakeDevice{wallVisible: true, affordable: true}
	hr := newWallHarness(t, dev, "buttons", "text_wall", "btn_confirm_upgrade")
	writeJSON(t, filepath.Join(os.Getenv("CLASHGO_ASSETS_DIR"), "wall_upgrade_confirm.json"), map[string]any{
		"confirm_button": testConfirmRect,
	})
	// (no wall_upgrade_x_roi.json)
	hr.run()

	if hr.stepsContain("asset_driven_tap_upgrade") {
		t.Fatalf("blind flow ran without the x-popup ROI: %v", hr.steps)
	}
	if !hr.stepsContain("hardcoded_tap_upgrade") {
		t.Fatalf("probe-and-discard flow not selected: %v", hr.steps)
	}
}

func TestWallLoopLegacyTemplateFlow(t *testing.T) {
	// No rect assets at all: the loop must fall all the way back to matching
	// btn_upgrade_wall, reading the in-tray cost colour, and treating a missing
	// confirm dialog as CoC's silent upgrade spawn.
	dev := &wallFakeDevice{wallVisible: true, affordable: true, upgradeButtonVisible: true}
	hr := newWallHarness(t, dev, "none", "text_wall", "btn_upgrade_wall", "btn_confirm_upgrade")
	hr.run()

	if !hr.stepsContain("upgrade_buttons_found") {
		t.Fatalf("legacy flow found no candidate: %v", hr.steps)
	}
	if !hr.stepsContain("confirm_silent_spawn") {
		t.Fatalf("silent spawn not treated as success: %v", hr.steps)
	}
	if !hr.stepsContain("upgrade_success") {
		t.Fatalf("legacy flow reported no success: %v", hr.steps)
	}
	if got := hr.tapsInRect(testLegacyCandidate); got != 1 {
		t.Fatalf("taps on the upgrade candidate = %d, want 1: %v", got, hr.taps)
	}
}

// TestWallLoopNoAssetsNoTemplates confirms the sequence still terminates cleanly
// on a profile that ships neither rect assets nor templates — the state a fresh
// clone starts in.
func TestWallLoopNoAssetsNoTemplates(t *testing.T) {
	dev := &wallFakeDevice{wallVisible: true}
	hr := newWallHarness(t, dev, "none")
	hr.run()

	if !hr.stepsContain("wall_template_missing") {
		t.Fatalf("expected a clean early exit: %v", hr.steps)
	}
	if hr.steps[len(hr.steps)-1] != "sequence_end" {
		t.Fatalf("sequence did not close: %v", hr.steps)
	}
}

// ---------------------------------------------------------------------------
// Tray gate and popup-baseline regressions (live 1280x720 findings)
// ---------------------------------------------------------------------------

// TestWallLoopRefusesToTapWithoutTray is the regression test for the live
// 1280x720 run's central failure: with the assets' rects landing on builder-menu
// rows rather than a wall tray, the blind flow tapped the mapped centres anyway
// and opened unrelated panels ("Upgrade Hero Hunter to Level 4?", a storage
// detail sheet). The gate must now stop the iteration with NO input fired.
func TestWallLoopRefusesToTapWithoutTray(t *testing.T) {
	dev := &wallFakeDevice{wallVisible: true, affordable: true, trayAbsent: true}
	hr := newWallHarness(t, dev, "all", "text_wall")
	hr.run()

	if !hr.stepsContain("wall_tray_absent") {
		t.Fatalf("tray-less frame did not stop the iteration: %v", hr.steps)
	}
	if hr.stepsContain("asset_driven_tap_upgrade") || hr.stepsContain("asset_driven_tap_confirm") {
		t.Fatalf("blind taps fired without tray evidence: %v", hr.steps)
	}
	for _, r := range []Rect{testGoldRect, testElixirRect} {
		if hr.tapped(r) {
			t.Fatalf("resource button %+v tapped with no tray on screen: %v", r, hr.taps)
		}
	}
	if hr.tapped(testConfirmRect) {
		t.Fatalf("confirm tapped with no tray on screen: %v", hr.taps)
	}
	if hr.stepsContain("upgrade_success") {
		t.Fatalf("reported success with no tray: %v", hr.steps)
	}
	if hr.dismiss != 1 || hr.steps[len(hr.steps)-1] != "sequence_end" {
		t.Fatalf("sequence did not close cleanly: dismiss=%d steps=%v", hr.dismiss, hr.steps)
	}
	// The gate's evidence must be in the payload so a user can see WHY it refused.
	gate := hr.payload["asset_driven_tray_gate"]
	if gate == nil {
		t.Fatal("no tray-gate evidence emitted")
	}
	for _, key := range []string{"gold_face_px", "elixir_face_px", "gold_found", "elixir_found"} {
		if _, ok := gate[key]; !ok {
			t.Fatalf("tray-gate evidence missing %q: %v", key, gate)
		}
	}
	if found, _ := gate["gold_found"].(bool); found {
		t.Fatalf("gate claimed a chip on a frame that has none: %v", gate)
	}
}

// TestXPopupPresentNeedsRedAndGrowth pins the popup detector's two gates, and
// the regression it exists for: a BRIGHT panel parked on x_popup_roi is not the
// buy-with-gems popup. On the live 1280x720 frames the confirm dialog's own
// panel reads 3172 bright px in that rect (against 580 for the popup itself), so
// the old brightness test called every affordable wall unaffordable.
func TestXPopupPresentNeedsRedAndGrowth(t *testing.T) {
	dark := gocv.NewMatWithSize(testFrameH, testFrameW, gocv.MatTypeCV8UC3)
	defer dark.Close()
	dark.SetTo(gocv.NewScalar(60, 60, 60, 0))
	if up, red := xPopupPresent(dark, testXRect, 0); up || red != 0 {
		t.Fatalf("empty screen read as a popup (up=%v red=%d)", up, red)
	}

	// A bright (near-white) panel where the popup's control would be: the
	// confirm dialog's chrome. Not a popup.
	bright := dark.Clone()
	defer bright.Close()
	gocv.Rectangle(&bright, testXRect.ImageRect().Inset(-2), color.RGBA{255, 255, 255, 255}, -1)
	if up, red := xPopupPresent(bright, testXRect, 0); up || red != 0 {
		t.Fatalf("a bright panel read as the buy-with-gems popup (up=%v red=%d)", up, red)
	}

	// The popup's red close control does, and the same red already present on
	// the baseline is chrome rather than a popup.
	red := dark.Clone()
	defer red.Close()
	gocv.Rectangle(&red, testXRect.ImageRect().Inset(-2), popupRedFace, -1)
	up, count := xPopupPresent(red, testXRect, 0)
	if !up {
		t.Fatalf("the red close control was not read as a popup (red=%d)", count)
	}
	if up, _ := xPopupPresent(red, testXRect, count); up {
		t.Fatal("red chrome that was already on the baseline read as a popup (no growth)")
	}

	// An off-frame or degenerate rect answers "down" instead of touching OpenCV
	// with an illegal ROI.
	if up, _ := xPopupPresent(red, Rect{X1: 5000, Y1: 5000, X2: 5100, Y2: 5100}, 0); up {
		t.Fatal("off-frame rect read as a popup")
	}
	if up, _ := xPopupPresent(red, Rect{}, 0); up {
		t.Fatal("zero rect read as a popup")
	}
}

// TestConfirmButtonDrawnNeedsGreenFaceAndCaption pins the pre-tap guard's shape:
// the CONFIRM control is a green face WITH a caption, so neither half alone is
// enough. The village's grass (green, no caption) and a dark panel's white
// caption (bright, no green face) both have to be refused — both were measured
// on the live frames inside the mapped confirm rect.
func TestConfirmButtonDrawnNeedsGreenFaceAndCaption(t *testing.T) {
	frame := gocv.NewMatWithSize(testFrameH, testFrameW, gocv.MatTypeCV8UC3)
	defer frame.Close()
	frame.SetTo(gocv.NewScalar(60, 60, 60, 0))
	if drawn, bright, green := confirmButtonDrawn(frame, testConfirmRect); drawn {
		t.Fatalf("empty rect reported the confirm control (bright=%d green=%d)", bright, green)
	}

	// Green face, no caption (the grass case).
	gocv.Rectangle(&frame, testConfirmRect.ImageRect(), confirmFaceGreen, -1)
	if drawn, bright, green := confirmButtonDrawn(frame, testConfirmRect); drawn {
		t.Fatalf("a caption-less green face reported the confirm control (bright=%d green=%d)", bright, green)
	}

	// Caption on the green face: the control.
	gocv.Rectangle(&frame, testConfirmRect.ImageRect().Inset(4), color.RGBA{255, 255, 255, 255}, -1)
	if drawn, bright, green := confirmButtonDrawn(frame, testConfirmRect); !drawn {
		t.Fatalf("the confirm control was not recognised (bright=%d green=%d)", bright, green)
	}

	// Bright caption, no green (the dark-panel case).
	plain := gocv.NewMatWithSize(testFrameH, testFrameW, gocv.MatTypeCV8UC3)
	defer plain.Close()
	plain.SetTo(gocv.NewScalar(60, 60, 60, 0))
	gocv.Rectangle(&plain, testConfirmRect.ImageRect(), color.RGBA{255, 255, 255, 255}, -1)
	if drawn, bright, green := confirmButtonDrawn(plain, testConfirmRect); drawn {
		t.Fatalf("a caption without a green face reported the confirm control (bright=%d green=%d)", bright, green)
	}
}

// TestWallLoopChromeAltRectIsNotAPopup is the regression test for the other half
// of the live failure: x_popup_roi_alt maps onto this client's resource HUD, so
// brightness alone read alt_up with alt_px=151 on every verify and turned BOTH
// resources into asset_driven_modal_close_failed — the sequence could never
// report a real outcome. With a baseline comparison the rect stays quiet, the
// alt tap is skipped, and the run reaches a real verdict.
func TestWallLoopChromeAltRectIsNotAPopup(t *testing.T) {
	dev := &wallFakeDevice{wallVisible: true, affordable: true, altAlwaysLit: true}
	hr := newWallHarness(t, dev, "all", "text_wall")
	hr.run()

	if hr.stepsContain("asset_driven_modal_close_failed") {
		t.Fatalf("a rect parked on screen chrome was read as a popup: %v", hr.steps)
	}
	if hr.tapped(testXAltRect) {
		t.Fatalf("blind alt tap fired on permanently-lit chrome: %v", hr.taps)
	}
	if !hr.stepsContain("asset_driven_silent_spawn") {
		t.Fatalf("affordable upgrade not recognised: %v", hr.steps)
	}
	if !hr.stepsContain("upgrade_success") {
		t.Fatalf("no upgrade_success: %v", hr.steps)
	}
	gate := hr.payload["asset_driven_tray_gate"]
	if red, _ := gate["base_alt_red"].(int); red <= 0 {
		t.Fatalf("baseline did not record the lit alt rect (base_alt_red=%v)", gate["base_alt_red"])
	}
}

// TestWallLoopBaselineCaptureFailureFiresNoInput covers the capture that exists
// only for the gate: if the baseline frame cannot be read there is no tray
// evidence and no popup reference, so the iteration must end without input.
func TestWallLoopBaselineCaptureFailureFiresNoInput(t *testing.T) {
	dev := &wallFakeDevice{wallVisible: true, affordable: true}
	hr := newWallHarness(t, dev, "all", "text_wall")
	// 1 = waitForMainVillage, 2 = wall-text search, 3 = the fresh frame the row
	// tap is made from, 4 = the pre-tap baseline.
	dev.failAt = map[int]bool{4: true}
	hr.run()

	if !hr.stepsContain("asset_driven_baseline_capture_failed") {
		t.Fatalf("no abort on a dead baseline capture: %v", hr.steps)
	}
	for _, r := range []Rect{testGoldRect, testElixirRect, testConfirmRect, testXRect} {
		if hr.tapped(r) {
			t.Fatalf("input fired without a baseline frame: %v", hr.taps)
		}
	}
}

// TestLocateWallTrayChipFollowsReflow proves the gate doubles as a locator: a
// chip painted off its mapped rect is still found (and its LIVE position is what
// the loop taps), which is what keeps a modest panel reflow from costing the
// sequence — the failure mode docs/RESOLUTION.md measures for every panel.
func TestLocateWallTrayChipFollowsReflow(t *testing.T) {
	frame := gocv.NewMatWithSize(testFrameH, testFrameW, gocv.MatTypeCV8UC3)
	defer frame.Close()
	frame.SetTo(gocv.NewScalar(60, 60, 60, 0))

	// Painted 10 px right and 6 px down of where the asset predicts.
	shifted := testGoldRect.ImageRect().Add(image.Pt(10, 6))
	gocv.Rectangle(&frame, shifted, goldChipFace, -1)

	located, facePx, found := locateWallTrayChip(frame, testGoldRect)
	if !found {
		t.Fatalf("reflowed chip not located (face_px=%d)", facePx)
	}
	// The mask's closing kernel can add a pixel of border, so the claim is that
	// the located centre lands ON the painted chip, not that the bounding box is
	// bit-identical.
	lcx, lcy := located.Center()
	if dx, dy := lcx-shifted.Min.X-20, lcy-shifted.Min.Y-20; dx > 2 || dx < -2 || dy > 2 || dy < -2 {
		t.Fatalf("located centre (%d,%d) is %d,%d px off the painted chip %v", lcx, lcy, dx, dy, shifted)
	}
	// The diagnostic still reports paint where the mapped rect overlaps the
	// shifted chip, which is what lets a user read a partial reflow off the log.
	if facePx == 0 {
		t.Fatal("face_px = 0 although the shifted chip still overlaps the mapped rect")
	}

	// A builder-menu row in the same place is white text on a dark panel: bright
	// but with no saturation, so the gate must not mistake it for a chip.
	menu := gocv.NewMatWithSize(testFrameH, testFrameW, gocv.MatTypeCV8UC3)
	defer menu.Close()
	menu.SetTo(gocv.NewScalar(40, 40, 40, 0))
	gocv.PutText(&menu, "Wall x128", image.Pt(testGoldRect.X1, testGoldRect.Y2-6),
		gocv.FontHersheySimplex, 0.5, color.RGBA{255, 255, 255, 255}, 1)
	if _, _, found := locateWallTrayChip(menu, testGoldRect); found {
		t.Fatal("a menu-row label was accepted as a tray chip")
	}
}

// ---------------------------------------------------------------------------
// Loop-level safety properties
// ---------------------------------------------------------------------------

// TestWallLoopDismissalCountIsBounded guards the user-reported "it does one click
// too much when exiting out": the wall loop must not fire its neutral dismiss tap
// more than once per sequence, however many buttons failed. That tap lands on
// empty village ground, so an extra one is an unexplained click on the user's
// base.
func TestWallLoopDismissalCountIsBounded(t *testing.T) {
	dev := &wallFakeDevice{wallVisible: true, affordable: false, ignoreXTaps: true}
	hr := newWallHarness(t, dev, "all", "text_wall")
	hr.run()
	if hr.dismiss > 1 {
		t.Fatalf("sequence fired %d dismiss taps, want <= 1", hr.dismiss)
	}
}

// TestWallLoopStopCheckIsConsultedEveryIteration makes sure a user Stop can end a
// run that keeps finding affordable walls: the stop request lands during the
// first iteration, and the loop must honour it at the next iteration boundary
// instead of running the (otherwise unbounded) sequence to completion.
func TestWallLoopStopCheckIsConsultedEveryIteration(t *testing.T) {
	dev := &wallFakeDevice{wallVisible: true, affordable: true}
	hr := newWallHarness(t, dev, "all", "text_wall")
	// Request the stop the moment the first iteration reports success.
	hr.h.OnStep = func(step string, _ map[string]any) {
		hr.steps = append(hr.steps, step)
		if step == "upgrade_success" {
			hr.stopped = true
		}
	}
	hr.run()

	if !hr.stepsContain("upgrade_success") {
		t.Fatalf("no successful iteration: %v", hr.steps)
	}
	if !hr.stepsContain("stopped") {
		t.Fatalf("stop was never observed by the loop: %v", hr.steps)
	}
	if got := strings.Count(strings.Join(hr.steps, ","), "upgrade_success"); got != 1 {
		t.Fatalf("loop ran %d successful iterations after the stop request: %v", got, hr.steps)
	}
	// The stop, not the natural exit, ended the sequence: the same fake would
	// otherwise have reported the wall row as gone.
	if hr.stepsContain("wall_text_not_found") {
		t.Fatalf("loop exited naturally instead of on the stop request: %v", hr.steps)
	}
	if hr.steps[len(hr.steps)-1] != "sequence_end" {
		t.Fatalf("sequence did not close: %v", hr.steps)
	}
}

// ---------------------------------------------------------------------------
// Asset shapes: the two-block output of cmd/refmap, and the builder-head button
// ---------------------------------------------------------------------------

// blockMap renders a rect the way cmd/refmap writes it: a physical block and the
// reference block the bot's HUD law maps back onto it.
func blockMap(physical, reference image.Rectangle) map[string]any {
	block := func(r image.Rectangle) map[string]int {
		return map[string]int{"x1": r.Min.X, "y1": r.Min.Y, "x2": r.Max.X, "y2": r.Max.Y}
	}
	return map[string]any{"physical": block(physical), "reference": block(reference)}
}

// TestWallUpgradeLoadersRoundTripRefmapBlocks is the join between the picking
// tool and the bot: a box measured on a live frame, recorded as physical +
// reference, must come back out of the loader as that same live box. It is the
// assertion that was missing while the picker wrote raw physical pixels into a
// file the loader read as reference coordinates.
func TestWallUpgradeLoadersRoundTripRefmapBlocks(t *testing.T) {
	cal := testLive1280x720Cal()

	// The live boxes a picking session would capture.
	liveTargets := map[string]image.Rectangle{
		"gold":           image.Rect(591, 503, 692, 601),
		"elixir":         image.Rect(717, 503, 816, 601),
		"confirm_button": image.Rect(800, 466, 1003, 549),
		"x_popup_roi":    image.Rect(868, 192, 928, 245),
	}
	refFor := func(r image.Rectangle) image.Rectangle {
		ref, _, ok := cal.HudRectReferenceSnap(r, 4)
		if !ok {
			t.Fatalf("HudRectReferenceSnap(%v) failed", r)
		}
		return ref
	}

	dir := t.TempDir()
	t.Setenv("CLASHGO_ASSETS_DIR", dir)
	writeJSON(t, filepath.Join(dir, "wall_upgrade_buttons.json"), map[string]any{
		"gold":   blockMap(liveTargets["gold"], refFor(liveTargets["gold"])),
		"elixir": blockMap(liveTargets["elixir"], refFor(liveTargets["elixir"])),
	})
	writeJSON(t, filepath.Join(dir, "wall_upgrade_confirm.json"), map[string]any{
		"confirm_button": blockMap(liveTargets["confirm_button"], refFor(liveTargets["confirm_button"])),
	})
	writeJSON(t, filepath.Join(dir, "wall_upgrade_x_roi.json"), map[string]any{
		"x_popup_roi": blockMap(liveTargets["x_popup_roi"], refFor(liveTargets["x_popup_roi"])),
	})

	gold, elixir, ok := loadWallUpgradeButtons(cal, zerolog.Nop())
	if !ok {
		t.Fatal("buttons asset with reference blocks was rejected")
	}
	if got := gold.ImageRect(); got != liveTargets["gold"] {
		t.Errorf("gold loaded as %v, want the picked live box %v", got, liveTargets["gold"])
	}
	if got := elixir.ImageRect(); got != liveTargets["elixir"] {
		t.Errorf("elixir loaded as %v, want the picked live box %v", got, liveTargets["elixir"])
	}
	confirm, ok := loadWallUpgradeConfirmRect(cal, zerolog.Nop())
	if !ok {
		t.Fatal("confirm asset with reference blocks was rejected")
	}
	if got := confirm.ImageRect(); got != liveTargets["confirm_button"] {
		t.Errorf("confirm loaded as %v, want %v", got, liveTargets["confirm_button"])
	}
	xr, alt, ok := loadWallUpgradeXPopupRect(cal, zerolog.Nop())
	if !ok {
		t.Fatal("x-popup asset with reference blocks was rejected")
	}
	if alt != nil {
		t.Errorf("alt = %v, want nil when the file has no alt key", alt)
	}
	if got := xr.ImageRect(); got != liveTargets["x_popup_roi"] {
		t.Errorf("x_popup_roi loaded as %v, want %v", got, liveTargets["x_popup_roi"])
	}
}

// A physical-only asset is the exact mistake that broke the live run, so it must
// be refused rather than mapped: those numbers are live-device pixels and reading
// them as reference coordinates is what moved every recorded box.
func TestWallUpgradeLoadersRefusePhysicalOnlyBlocks(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLASHGO_ASSETS_DIR", dir)
	box := map[string]any{"physical": map[string]int{"x1": 591, "y1": 503, "x2": 692, "y2": 601}}

	writeJSON(t, filepath.Join(dir, "wall_upgrade_buttons.json"), map[string]any{"gold": box, "elixir": box})
	if _, _, ok := loadWallUpgradeButtons(testIdentityCal(), zerolog.Nop()); ok {
		t.Error("physical-only buttons asset was accepted")
	}
	writeJSON(t, filepath.Join(dir, "wall_upgrade_confirm.json"), map[string]any{"confirm_button": box})
	if _, ok := loadWallUpgradeConfirmRect(testIdentityCal(), zerolog.Nop()); ok {
		t.Error("physical-only confirm asset was accepted")
	}
	writeJSON(t, filepath.Join(dir, "wall_upgrade_x_roi.json"), map[string]any{"x_popup_roi": box})
	if _, _, ok := loadWallUpgradeXPopupRect(testIdentityCal(), zerolog.Nop()); ok {
		t.Error("physical-only x-popup asset was accepted")
	}
}

// The legacy point shape tools/picker.py's confirm preset writes must keep
// working: a point and a degenerate rect identify the same tap.
func TestWallUpgradeConfirmAcceptsLegacyPointShape(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLASHGO_ASSETS_DIR", dir)
	writeJSON(t, filepath.Join(dir, "wall_upgrade_confirm.json"), map[string]any{
		"confirm_button": map[string]int{"x": 900, "y": 507},
	})
	r, ok := loadWallUpgradeConfirmRect(testIdentityCal(), zerolog.Nop())
	if !ok {
		t.Fatal("legacy point shape was rejected")
	}
	if cx, cy := r.Center(); cx != 900 || cy != 507 {
		t.Errorf("point loaded as centre (%d,%d), want (900,507)", cx, cy)
	}
}

// The picking session's first step is the builder-head button, so the loop has to
// consult it: an asset that moves the tap must move it.
func TestWallLoopUsesPickedBuilderButton(t *testing.T) {
	dev := &wallFakeDevice{wallVisible: true, affordable: true}
	hr := newWallHarness(t, dev, "all", "text_wall")
	writeJSON(t, filepath.Join(os.Getenv("CLASHGO_ASSETS_DIR"), "builder_button.json"), map[string]any{
		"builder_button": testBuilderButtonRect,
	})
	hr.run()

	if got := hr.payload["builder_tapped"]["source"]; got != "assets/builder_button.json" {
		t.Fatalf("builder tap source = %v, want the picked asset", got)
	}
	bx, by := testBuilderButtonRect.Center()
	if hr.payload["builder_tapped"]["x"] != bx || hr.payload["builder_tapped"]["y"] != by {
		t.Fatalf("builder tap = (%v,%v), want the picked box centre (%d,%d)",
			hr.payload["builder_tapped"]["x"], hr.payload["builder_tapped"]["y"], bx, by)
	}
	if !hr.tapped(testBuilderButtonRect) {
		t.Fatalf("no tap inside the picked builder box: %v", hr.taps)
	}
}

// Without the asset the loop keeps the authored reference point, so a profile
// that never picked a builder button behaves exactly as before.
func TestWallLoopBuilderTapFallsBackToAuthoredPoint(t *testing.T) {
	dev := &wallFakeDevice{wallVisible: true, affordable: true}
	hr := newWallHarness(t, dev, "all", "text_wall")
	hr.run()

	if got := hr.payload["builder_tapped"]["source"]; got != "authored-reference" {
		t.Fatalf("builder tap source = %v, want the authored fallback", got)
	}
}

// The row tap is made from the frame the row was matched in, but the menu does
// not hold still while the tap is in flight. This is the live 1280x720
// regression: "Wall x127" matched at (504,251) and the tap, fired ~100 ms later,
// landed on the "Hero Hunter" row that had glided under it, opening that unit's
// confirm dialog instead of the wall tray. The tap must follow the row on a fresh
// frame rather than the coordinates the match was computed from.
func TestWallLoopWallRowTapFollowsAGlidingMenu(t *testing.T) {
	// Every capture sees the row 10 px higher than the one before it.
	dev := &wallFakeDevice{
		wallVisible: true, affordable: true,
		labelY: wallLabelY + 30, labelStep: -10,
	}
	hr := newWallHarness(t, dev, "all", "text_wall")
	hr.run()

	tap := hr.payload["wall_row_tap"]
	if tap == nil {
		t.Fatalf("no row-tap evidence emitted: %v", hr.steps)
	}
	if fresh, _ := tap["fresh"].(bool); !fresh {
		t.Fatalf("the tap was not re-matched on a fresh frame: %v", tap)
	}
	seed, _ := tap["seed"].([]int)
	at, _ := tap["tap"].([]int)
	if len(seed) != 2 || len(at) != 2 {
		t.Fatalf("row-tap evidence lacks its points: %v", tap)
	}
	if at[1] >= seed[1] {
		t.Fatalf("tap y=%d did not follow the row up from the seed y=%d: %v", at[1], seed[1], tap)
	}
	if !hr.stepsContain("wall_clicked") {
		t.Fatalf("the row was never clicked: %v", hr.steps)
	}
}

// The confirm-page guard: the dialog is up but its CONFIRM control is not drawn
// where the asset predicts. On the live 1280x720 run that was the shipped asset's
// state (it mapped 94 live px above the button), the blind tap pressed the
// dialog's flavour text, and the flow reported a successful upgrade that never
// happened. No input may fire, and no success may be claimed.
func TestWallLoopConfirmControlAbsentSkipsTheTap(t *testing.T) {
	dev := &wallFakeDevice{wallVisible: true, affordable: true, confirmControlAbsent: true}
	hr := newWallHarness(t, dev, "all", "text_wall")
	hr.run()

	if !hr.stepsContain("asset_driven_confirm_button_absent") {
		t.Fatalf("a missing CONFIRM control was not surfaced: %v", hr.steps)
	}
	if hr.stepsContain("asset_driven_tap_confirm") {
		t.Fatalf("the confirm tap fired with no control on screen: %v", hr.steps)
	}
	if hr.tapped(testConfirmRect) {
		t.Fatalf("confirm centre tapped with no control drawn there: %v", hr.taps)
	}
	if hr.stepsContain("upgrade_success") {
		t.Fatalf("a skipped confirm tap still reported an upgrade: %v", hr.steps)
	}
	page := hr.payload["asset_driven_confirm_page"]
	if drawn, _ := page["confirm_drawn"].(bool); drawn {
		t.Fatalf("the guard claimed the control was drawn: %v", page)
	}
	if _, ok := page["confirm_bright"]; !ok {
		t.Fatalf("no pixel evidence for the refusal: %v", page)
	}
}

// The "switch to elixir" half of the user's spec, at loop level: gold's
// unaffordable popup is closed, the confirm dialog it revealed is left (so the
// tray is reachable again), and only THEN is the elixir chip tapped — the order
// that makes the second resource a real second attempt instead of the same gold
// prompt raised a second time.
func TestWallLoopDismissedGoldFallsThroughToElixir(t *testing.T) {
	dev := &wallFakeDevice{wallVisible: true, affordable: false}
	hr := newWallHarness(t, dev, "all", "text_wall")
	hr.run()

	goldStep, elixirStep, closeStep := -1, -1, -1
	for i, s := range hr.steps {
		switch s {
		case "asset_driven_leave_confirm_dialog":
			if closeStep < 0 {
				closeStep = i
			}
		case "asset_driven_tap_upgrade":
			if goldStep < 0 {
				goldStep = i
			} else if elixirStep < 0 {
				elixirStep = i
			}
		}
	}
	if goldStep < 0 || elixirStep < 0 {
		t.Fatalf("both resources were not attempted: %v", hr.steps)
	}
	if closeStep < 0 || closeStep > elixirStep {
		t.Fatalf("the confirm dialog was not left before the elixir attempt (leave=%d elixir=%d): %v", closeStep, elixirStep, hr.steps)
	}
	if dev.backs == 0 {
		t.Fatal("no Back was sent to leave the confirm dialog")
	}
	// The elixir chip is only reachable once the dialog is gone, so the fake
	// drops the tap while the dialog is up: reaching the elixir attempt proves
	// the recovery ran.
	if !hr.tapped(testElixirRect) {
		t.Fatalf("the elixir chip was never tapped: %v", hr.taps)
	}
	if hr.stepsContain("upgrade_success") {
		t.Fatalf("two gem popups were reported as an upgrade: %v", hr.steps)
	}
	if !hr.stepsContain("all_unaffordable") || hr.dismiss != 1 {
		t.Fatalf("sequence did not close on all_unaffordable (dismiss=%d): %v", hr.dismiss, hr.steps)
	}
}

// ---------------------------------------------------------------------------
// Pixel signatures, pinned against the live 1280x720 captures
// ---------------------------------------------------------------------------

// The crops in testdata were cut out of the picking session's live frames
// (output/wall_pick/step{1,5,6}.png) around the two controls the asset-driven
// flow has to tell apart, so the two signatures stay pinned to real device
// pixels. The brightness test they replaced was written against synthetic frames,
// and on the device it read the confirm page's own bright panel as the popup
// (3172 bright px, against the popup's 580) — an affordable wall, sat on its
// confirm dialog, kept being called unaffordable.
var (
	// The popup's X roi, (868,192)-(928,245) in the captured frame.
	popupCloseCropRect = Rect{X1: 28, Y1: 28, X2: 88, Y2: 81}
	// The confirm rect, (796,586)-(998,667) in the captured frame.
	confirmButtonCropRect = Rect{X1: 26, Y1: 26, X2: 228, Y2: 107}
)

func TestLiveCropsPinThePopupSignature(t *testing.T) {
	for _, tc := range []struct {
		name  string
		crop  string
		isPop bool
	}{
		{"buy-with-gems popup up", "testdata/popup_close_control.png", true},
		{"confirm dialog page", "testdata/confirm_page_close_control.png", false},
		{"confirm page's CONFIRM band", "testdata/confirm_page_button.png", false},
		{"village grass", "testdata/village_confirm_box.png", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			img := gocv.IMRead(tc.crop, gocv.IMReadColor)
			if img.Empty() {
				t.Fatalf("cannot read %s", tc.crop)
			}
			defer img.Close()
			up, red := xPopupPresent(img, popupCloseCropRect, 0)
			if up != tc.isPop {
				t.Errorf("xPopupPresent(%s) = %v (red=%d), want %v", tc.crop, up, red, tc.isPop)
			}
			if tc.isPop && red < popupRedPixelsMin {
				t.Errorf("popup crop only read %d red px, under the floor %d", red, popupRedPixelsMin)
			}
		})
	}
}

func TestLiveCropsPinTheConfirmSignature(t *testing.T) {
	for _, tc := range []struct {
		name  string
		crop  string
		isBtn bool
	}{
		{"confirm page's CONFIRM control", "testdata/confirm_page_button.png", true},
		{"village grass where the box would be", "testdata/village_confirm_box.png", false},
		{"the popup covering the dialog", "testdata/popup_close_control.png", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			img := gocv.IMRead(tc.crop, gocv.IMReadColor)
			if img.Empty() {
				t.Fatalf("cannot read %s", tc.crop)
			}
			defer img.Close()
			drawn, bright, green := confirmButtonDrawn(img, confirmButtonCropRect)
			if drawn != tc.isBtn {
				t.Errorf("confirmButtonDrawn(%s) = %v (bright=%d green=%d), want %v", tc.crop, drawn, bright, green, tc.isBtn)
			}
		})
	}
}
