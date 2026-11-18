package game

// accountswitch_test.go drives the in-game switch state machine from synthetic
// screen fixtures: each frame contains the template the next step has to find,
// so the real matcher runs and the fake device records where the taps land. No
// emulator, no templates on disk, no OCR toolchain.

import (
	"errors"
	"image"
	"image/color"
	"strings"
	"sync"
	"testing"

	"github.com/rs/zerolog"
	"gocv.io/x/gocv"
)

const (
	testScreenW = 640
	testScreenH = 360
)

// fillMat paints a solid frame.
func fillMat(t *testing.T, w, h int, c color.RGBA) gocv.Mat {
	t.Helper()
	m := gocv.NewMatWithSize(h, w, gocv.MatTypeCV8UC3)
	gocv.Rectangle(&m, image.Rect(0, 0, w, h), c, -1)
	return m
}

// fixtureTemplate is a small two-tone patch: distinctive enough that the
// matcher finds it and nothing else in a synthetic frame.
func fixtureTemplate(t *testing.T, w, h int, first, second color.RGBA) gocv.Mat {
	t.Helper()
	m := fillMat(t, w, h, first)
	gocv.Rectangle(&m, image.Rect(w/4, h/4, w-w/4, h-h/4), second, -1)
	return m
}

func paste(t *testing.T, dst gocv.Mat, at image.Rectangle, tpl gocv.Mat) {
	t.Helper()
	roi := dst.Region(at)
	defer roi.Close()
	tpl.CopyTo(&roi)
}

// switchFixture is one synthetic screen set for the five stages of a switch.
type switchFixture struct {
	templates   map[string]gocv.Mat
	screens     []gocv.Mat
	settingsAt  image.Rectangle
	supercellAt image.Rectangle
}

func newSwitchFixture(t *testing.T, screenH int, slotListAnchor image.Rectangle) *switchFixture {
	t.Helper()
	f := &switchFixture{templates: map[string]gocv.Mat{
		AcctSettingsTemplate:  fixtureTemplate(t, 32, 32, color.RGBA{60, 60, 200, 255}, color.RGBA{230, 230, 230, 255}),
		AcctSupercellTemplate: fixtureTemplate(t, 120, 26, color.RGBA{30, 120, 60, 255}, color.RGBA{250, 250, 250, 255}),
		AcctSlotListTemplate:  fixtureTemplate(t, 150, 30, color.RGBA{90, 30, 120, 255}, color.RGBA{200, 200, 90, 255}),
	}}
	bg := color.RGBA{18, 20, 24, 255}
	f.settingsAt = image.Rect(testScreenW-80, 40, testScreenW-48, 72)
	// Keep the Supercell ID row inside the frame for short test screens.
	f.supercellAt = image.Rect(300, screenH-160, 420, screenH-134)

	village := fillMat(t, testScreenW, screenH, bg)

	profile := fillMat(t, testScreenW, screenH, bg)
	paste(t, profile, f.settingsAt, f.templates[AcctSettingsTemplate])

	settings := fillMat(t, testScreenW, screenH, bg)
	paste(t, settings, f.supercellAt, f.templates[AcctSupercellTemplate])

	list := fillMat(t, testScreenW, screenH, bg)
	paste(t, list, slotListAnchor, f.templates[AcctSlotListTemplate])

	f.screens = []gocv.Mat{
		village,         // before any tap
		profile,         // after the banner tap
		settings,        // after the Settings tap
		list,            // after the Supercell ID tap
		village.Clone(), // after the slot tap: back at a village
		village.Clone(), // profile opened for the identity read
	}
	t.Cleanup(func() {
		for _, m := range f.templates {
			m.Close()
		}
		for _, m := range f.screens {
			m.Close()
		}
	})
	return f
}

func (f *switchFixture) store(t *testing.T) *TemplateStore {
	t.Helper()
	ts := NewEmptyTemplateStore()
	for name, mat := range f.templates {
		ts.mu.Lock()
		ts.templates[name] = mat.Clone()
		ts.registry[name] = TemplateMeta{Name: name}
		ts.mu.Unlock()
	}
	t.Cleanup(ts.Close)
	return ts
}

// fakeSwitchDevice replays fixture frames in step order and records input.
type fakeSwitchDevice struct {
	mu      sync.Mutex
	screens []gocv.Mat
	taps    []image.Point
	backs   int
}

func (d *fakeSwitchDevice) frame() gocv.Mat {
	d.mu.Lock()
	defer d.mu.Unlock()
	idx := len(d.taps)
	if idx >= len(d.screens) {
		idx = len(d.screens) - 1
	}
	return d.screens[idx].Clone()
}

func (d *fakeSwitchDevice) Tap(x, y int) error { return d.TapRandomized(x, y) }
func (d *fakeSwitchDevice) TapRandomized(x, y int) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.taps = append(d.taps, image.Pt(x, y))
	return nil
}
func (d *fakeSwitchDevice) Swipe(x1, y1, x2, y2, ms int) error       { return nil }
func (d *fakeSwitchDevice) SwipeBezier(x1, y1, x2, y2, ms int) error { return nil }
func (d *fakeSwitchDevice) Pinch(x1, y1, x2, y2, x3, y3, x4, y4, ms int) error {
	return nil
}
func (d *fakeSwitchDevice) PinchZoom(zoomOut bool) error { return nil }
func (d *fakeSwitchDevice) ZoomOut() error               { return nil }
func (d *fakeSwitchDevice) ZoomIn() error                { return nil }
func (d *fakeSwitchDevice) Hold(x, y, ms int) error      { return nil }
func (d *fakeSwitchDevice) KeyEvent(code int) error      { return nil }
func (d *fakeSwitchDevice) Text(text string) error       { return nil }
func (d *fakeSwitchDevice) Back() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.backs++
	return nil
}
func (d *fakeSwitchDevice) CaptureToMat() (gocv.Mat, error) { return d.frame(), nil }
func (d *fakeSwitchDevice) Close() error                    { return nil }

func testSwitchConfig() SwitchConfig {
	cfg := DefaultSwitchConfig()
	cfg.SettleMS = 0
	cfg.PollMS = 1
	cfg.TimeoutMS = 2000
	return cfg
}

// TestSwitchAccountTapsProfileSettingsSupercellSlot is the core contract:
// banner → Settings → Supercell ID → the row for the slot, in that order, and
// the row's Y comes from the list anchor and the configured pitch.
func TestSwitchAccountTapsProfileSettingsSupercellSlot(t *testing.T) {
	anchor := image.Rect(240, 120, 390, 150) // centre (315,135)
	fixture := newSwitchFixture(t, testScreenH, anchor)
	dev := &fakeSwitchDevice{screens: fixture.screens}
	sw := NewAccountSwitcher(dev, fixture.store(t), testSwitchConfig(), zerolog.Nop())
	sw.SetIdentityReader(func(gocv.Mat) (Identity, error) {
		return Identity{Name: "Fleet Two", Tag: "#2P0Y8LQGR"}, nil
	})

	got, err := sw.Switch(2, Identity{Name: "Fleet Two", Tag: "#2P0Y8LQGR"})
	if err != nil {
		t.Fatalf("Switch: %v", err)
	}
	if got.Tag != "#2P0Y8LQGR" {
		t.Fatalf("identity = %+v, want the fixture tag", got)
	}

	cfg := sw.Config()
	wantRowY := anchor.Min.Y + anchor.Dy()/2 +
		int((cfg.SlotRow1Offset+float64(2-1)*cfg.SlotRowSpacing)*float64(testScreenH)+0.5)
	centre := func(r image.Rectangle) image.Point {
		return image.Pt(r.Min.X+r.Dx()/2, r.Min.Y+r.Dy()/2)
	}
	want := []image.Point{
		ProfileTapPoint(testScreenW, testScreenH),      // profile corner, not a template
		centre(fixture.settingsAt),                     // Settings tab
		centre(fixture.supercellAt),                    // Supercell ID row
		image.Pt(anchor.Min.X+anchor.Dx()/2, wantRowY), // slot 2 row
		ProfileTapPoint(testScreenW, testScreenH),      // profile again, for the identity read
	}
	if len(dev.taps) != len(want) {
		t.Fatalf("taps = %v, want %d taps %v", dev.taps, len(want), want)
	}
	for i := range want {
		if dev.taps[i] != want[i] {
			t.Errorf("tap %d = %v, want %v (all taps: %v)", i, dev.taps[i], want[i], dev.taps)
		}
	}
	if dev.backs != 1 {
		t.Errorf("profile closed %d times, want 1", dev.backs)
	}
}

// TestSwitchAccountMismatchAbortsWithBothIdentities proves a slot that lands on
// the wrong village fails instead of returning success.
func TestSwitchAccountMismatchAbortsWithBothIdentities(t *testing.T) {
	fixture := newSwitchFixture(t, testScreenH, image.Rect(240, 120, 390, 150))
	dev := &fakeSwitchDevice{screens: fixture.screens}
	sw := NewAccountSwitcher(dev, fixture.store(t), testSwitchConfig(), zerolog.Nop())
	sw.SetIdentityReader(func(gocv.Mat) (Identity, error) {
		return Identity{Name: "Somebody Else", Tag: "#9XYZ0000"}, nil
	})

	_, err := sw.Switch(3, Identity{Name: "Fleet Two", Tag: "#2P0Y8LQGR"})
	if err == nil {
		t.Fatal("mismatch reported success")
	}
	var mismatch *IdentityMismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("error = %T %v, want IdentityMismatchError", err, err)
	}
	if mismatch.Slot != 3 {
		t.Errorf("mismatch slot = %d, want 3", mismatch.Slot)
	}
	msg := err.Error()
	for _, needle := range []string{"#9XYZ0000", "#2P0Y8LQGR", "nothing was started"} {
		if !strings.Contains(msg, needle) {
			t.Errorf("mismatch message %q is missing %q", msg, needle)
		}
	}
}

// TestSwitchAccountMissingTemplatesIsActionable pins the first-run failure: it
// must name every PNG and the capture command.
func TestSwitchAccountMissingTemplatesIsActionable(t *testing.T) {
	fixture := newSwitchFixture(t, testScreenH, image.Rect(240, 120, 390, 150))
	dev := &fakeSwitchDevice{screens: fixture.screens}
	sw := NewAccountSwitcher(dev, NewEmptyTemplateStore(), testSwitchConfig(), zerolog.Nop())

	_, err := sw.Switch(1, Identity{Name: "Main", Tag: "#2P0Y8LQGR"})
	if err == nil {
		t.Fatal("switch with no templates succeeded")
	}
	var missing *MissingTemplatesError
	if !errors.As(err, &missing) {
		t.Fatalf("error = %T %v, want MissingTemplatesError", err, err)
	}
	msg := err.Error()
	for _, needle := range []string{
		AcctSettingsTemplate, AcctSupercellTemplate, AcctSlotListTemplate,
		"go run ./cmd/account_templates",
	} {
		if !strings.Contains(msg, needle) {
			t.Errorf("missing-template message is missing %q:\n%s", needle, msg)
		}
	}
	if len(dev.taps) != 0 {
		t.Errorf("switch tapped %v before checking templates", dev.taps)
	}
}

// TestSwitchAccountSlotBeyondScreenNamesTheTunable keeps a bad slot pitch
// debuggable: the error has to name the config keys, not just "failed".
func TestSwitchAccountSlotBeyondScreenNamesTheTunable(t *testing.T) {
	fixture := newSwitchFixture(t, 200, image.Rect(240, 125, 390, 155)) // centre y=140
	dev := &fakeSwitchDevice{screens: fixture.screens}
	sw := NewAccountSwitcher(dev, fixture.store(t), testSwitchConfig(), zerolog.Nop())
	sw.SetIdentityReader(func(gocv.Mat) (Identity, error) { return Identity{Tag: "#2P0Y8LQGR"}, nil })

	_, err := sw.Switch(5, Identity{Tag: "#2P0Y8LQGR"})
	if err == nil {
		t.Fatal("slot outside the screen was accepted")
	}
	for _, needle := range []string{"slot 5", "slot_row1_offset", "640x200"} {
		if !strings.Contains(err.Error(), needle) {
			t.Errorf("error %q is missing %q", err, needle)
		}
	}
}
