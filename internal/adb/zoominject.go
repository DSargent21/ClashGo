package adb

import (
	_ "embed"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
)

// zoomInjectJar is the prebuilt helper that injects a real multi-pointer
// MotionEvent through InputManager. Source and build recipe live in
// tools/zoominject/.
//
//go:embed zoominject.jar
var zoomInjectJar []byte

const (
	zoomInjectDevicePath = "/data/local/tmp/ClashGO-zoominject.jar"
	zoomInjectClass      = "ZoomInject"
	zoomInjectSteps      = 25
	zoomInjectDurationMs = 600
)

// PinchGesture is the geometry of one symmetric two-finger pinch in physical
// pixels: both fingers start and end at the same y, and the pair is symmetric
// about the frame centre so the camera zooms instead of panning.
type PinchGesture struct {
	X1Start, X2Start int
	X1End, X2End     int
	Y                int
}

// PinchPoints returns the four x coordinates and the shared y for a pinch at
// the given screen size. Modelled on the reference implementation: fingers at
// 28% and 72% of the width moving to 48% and 52% for a zoom-out (and the
// reverse for a zoom-in).
func PinchPoints(w, h int, zoomOut bool) PinchGesture {
	// Built outward from the centre so the pair is exactly symmetric: an odd
	// width cannot split evenly if the two x values are computed independently,
	// and a one-pixel midpoint drift is what turns a zoom into a pan.
	cx := w / 2
	nearOff := w * 44 / 200 // 22% either side of centre -> the 28%/72% pair
	farOff := w * 4 / 200   // 2% either side -> the 48%/52% pair
	if zoomOut {
		return PinchGesture{X1Start: cx + nearOff, X2Start: cx - nearOff, X1End: cx + farOff, X2End: cx - farOff, Y: h / 2}
	}
	return PinchGesture{X1Start: cx + farOff, X2Start: cx - farOff, X1End: cx + nearOff, X2End: cx - nearOff, Y: h / 2}
}

// pinchCommand builds the on-device invocation. Kept separate so the argument
// shape is unit-testable without a device.
func pinchCommand(path string, g PinchGesture) string {
	args := []string{
		strconv.Itoa(g.X1Start), strconv.Itoa(g.Y),
		strconv.Itoa(g.X2Start), strconv.Itoa(g.Y),
		strconv.Itoa(g.X1End), strconv.Itoa(g.Y),
		strconv.Itoa(g.X2End), strconv.Itoa(g.Y),
		strconv.Itoa(zoomInjectSteps), strconv.Itoa(zoomInjectDurationMs),
	}
	return "CLASSPATH=" + path + " app_process /system/bin " + zoomInjectClass +
		" pinch " + strings.Join(args, " ")
}

// ensureZoomInjector makes sure the device carries the exact helper build,
// transferring it over the shell (file sync is unreliable on this emulator)
// when the size differs.
func (c *Client) ensureZoomInjector() error {
	want := strconv.Itoa(len(zoomInjectJar))
	out, err := c.Shell("wc -c < " + zoomInjectDevicePath)
	if err == nil && strings.TrimSpace(out) == want {
		return nil
	}

	b64 := base64.StdEncoding.EncodeToString(zoomInjectJar)
	if _, err := c.Shell("echo " + b64 + " | base64 -d > " + zoomInjectDevicePath); err != nil {
		return fmt.Errorf("transfer zoom injector: %w", err)
	}

	out, err = c.Shell("wc -c < " + zoomInjectDevicePath)
	if err != nil {
		return fmt.Errorf("verify zoom injector: %w", err)
	}
	if got := strings.TrimSpace(out); got != want {
		return fmt.Errorf("zoom injector size mismatch: device has %q, want %s", got, want)
	}
	return nil
}

// PinchZoom performs a real multi-touch pinch through the Android framework.
//
// It deliberately does NOT use sendevent. The guest input pipeline on
// BlueStacks forwards only a single pointer from raw evdev writes: the kernel
// reports both slots, but the framework sees one contact, so every sendevent
// "pinch" collapses to a tap on whatever sits under the fingers and the camera
// never moves. Injecting the MotionEvent through InputManager (the same path
// the `input` command uses) delivers both pointers.
func (c *Client) PinchZoom(zoomOut bool) error {
	if err := c.checkInputContext(); err != nil {
		return err
	}
	c.markInput()

	if err := c.ensureZoomInjector(); err != nil {
		c.log.Debugf("framework pinch unavailable (%v); falling back to sendevent", err)
		return c.pinchZoomSendevent(zoomOut)
	}

	w, h, err := c.ScreenSize()
	if err != nil {
		c.log.Debugf("framework pinch: screen size unavailable (%v); falling back to sendevent", err)
		return c.pinchZoomSendevent(zoomOut)
	}

	cmd := pinchCommand(zoomInjectDevicePath, PinchPoints(w, h, zoomOut))
	out, err := c.Shell(cmd)
	if err != nil {
		c.log.Debugf("framework pinch failed (%v); falling back to sendevent", err)
		return c.pinchZoomSendevent(zoomOut)
	}
	if !strings.Contains(out, "pinch injected") {
		c.log.Debugf("framework pinch produced no confirmation (%q); falling back to sendevent", strings.TrimSpace(out))
		return c.pinchZoomSendevent(zoomOut)
	}
	c.log.Debugf("framework pinch injected (zoomOut: %v)", zoomOut)
	return nil
}

// pinchZoomSendevent is the legacy raw-evdev pinch. It is kept only as a
// fallback for devices whose framework injection is unavailable; on the
// BlueStacks guest it is a no-op that taps a building.
func (c *Client) pinchZoomSendevent(zoomOut bool) error {
	c.log.Debugf("PinchZoom executing via sendevent (zoomOut: %v)", zoomOut)

	w, h, err := c.ScreenSize()
	if err != nil {
		return fmt.Errorf("get screen size: %w", err)
	}

	device, err := c.DetectTouchDevice()
	if err != nil {
		return fmt.Errorf("detect touch device: %w", err)
	}

	// BlueStacks Virtual Touch uses 0-32767
	const touchMax = 32767
	scale := func(pixel, size int) int {
		if size == 0 {
			return 0
		}
		return (pixel * touchMax) / size
	}

	centerX, centerY := w/2, h/2
	offsetX, offsetY := w/4, h/4

	var f1Start, f1End, f2Start, f2End [2]int
	if zoomOut {
		f1Start = [2]int{centerX - offsetX, centerY - offsetY}
		f1End = [2]int{centerX - 50, centerY - 50}
		f2Start = [2]int{centerX + offsetX, centerY + offsetY}
		f2End = [2]int{centerX + 50, centerY + 50}
	} else {
		f1Start = [2]int{centerX - 50, centerY - 50}
		f1End = [2]int{centerX - offsetX, centerY - offsetY}
		f2Start = [2]int{centerX + 50, centerY + 50}
		f2End = [2]int{centerX + offsetX, centerY + offsetY}
	}

	var batch strings.Builder
	add := func(typ, code, value int) {
		batch.WriteString(fmt.Sprintf("sendevent %s %d %d %d && ", device, typ, code, value))
	}

	// 1. LANDING: Both fingers down in one frame
	add(3, 57, 1)  // ID 1
	add(1, 330, 1) // BTN_TOUCH DOWN
	add(3, 53, scale(f1Start[0], w))
	add(3, 54, scale(f1Start[1], h))
	add(0, 2, 0) // SYN_MT_REPORT

	add(3, 57, 2) // ID 2
	add(3, 53, scale(f2Start[0], w))
	add(3, 54, scale(f2Start[1], h))
	add(0, 2, 0) // SYN_MT_REPORT
	add(0, 0, 0) // SYN_REPORT

	// 2. MOVEMENT: 10 steps (enough for game engine to track)
	steps := 10
	for i := 1; i <= steps; i++ {
		add(3, 53, scale(f1Start[0]+(f1End[0]-f1Start[0])*i/steps, w))
		add(3, 54, scale(f1Start[1]+(f1End[1]-f1Start[1])*i/steps, h))
		add(0, 2, 0)
		add(3, 53, scale(f2Start[0]+(f2End[0]-f2Start[0])*i/steps, w))
		add(3, 54, scale(f2Start[1]+(f2End[1]-f2Start[1])*i/steps, h))
		add(0, 2, 0)
		add(0, 0, 0)
	}

	// 3. LIFT: Clean release
	add(3, 57, -1) // Release ID 1
	add(0, 2, 0)
	add(3, 57, -1) // Release ID 2
	add(1, 330, 0) // BTN_TOUCH UP
	add(0, 2, 0)
	batch.WriteString(fmt.Sprintf("sendevent %s 0 0 0", device))

	_, err = c.Shell(batch.String())
	return err
}
