package adb

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math/rand"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"gocv.io/x/gocv"
)

type TapEvent struct {
	Seq     int64   `json:"seq"`
	Type    string  `json:"type"`
	X       int     `json:"x"`
	Y       int     `json:"y"`
	ActualX int     `json:"actual_x"`
	ActualY int     `json:"actual_y"`
	StdDev  float64 `json:"std_dev,omitempty"`
	Error   string  `json:"error,omitempty"`
	Ts      int64   `json:"ts_ms"`
}

type Client struct {
	DeviceID string
	host     string
	port     int
	timeout  time.Duration
	log      Logger

	zoomOutKey string
	zoomInKey  string

	transport *Transport
	health    Health
	mu        sync.Mutex
	closed    bool

	// Persistent shell pipe (lazy-init). When enabled (UseShellPipe == true)
	// and not broken, Tap/Swipe/KeyEvent/Text route through pipe.Send
	// (sync flush) or pipe.SendAsync (fire-and-forget). When disabled or
	// broken, all calls fall back to transport.Exec via tapLocked.
	pipe   *ShellPipe
	pipeMu sync.Mutex

	tapHookMu sync.RWMutex
	tapHook   func(TapEvent)
	tapSeq    int64
}

// EnablePersistentShell activates the persistent adb shell pipe for the
// duration of an attack cycle. Pass syncFlush=false to use fire-and-forget
// semantics for Tap (faster but no per-tap completion ack). Subsequent
// calls are no-ops; call ClosePersistentShell to tear down.
func (c *Client) EnablePersistentShell(syncFlush bool) {
	c.pipeMu.Lock()
	defer c.pipeMu.Unlock()
	if c.pipe != nil {
		return
	}
	c.pipe = NewShellPipe(c.DeviceID, c.host, c.port, c.timeout, c.log)
	if err := c.pipe.Start(); err != nil {
		c.log.Warn(fmt.Sprintf("persistent shell pipe failed to start, using legacy transport: %v", err))
		c.pipe = nil
		return
	}
	c.log.Info("persistent adb shell pipe enabled (sync_flush: " + fmt.Sprint(syncFlush) + ")")
}

// ClosePersistentShell tears down the pipe (typically called at end of
// executeAttackSequence). Safe to call when not enabled.
func (c *Client) ClosePersistentShell() {
	c.pipeMu.Lock()
	defer c.pipeMu.Unlock()
	if c.pipe == nil {
		return
	}
	_ = c.pipe.Close()
	c.pipe = nil
	c.log.Info("persistent adb shell pipe closed")
}

// hasPipe returns the current pipe if enabled and not broken. Caller must
// hold c.pipeMu only if it intends to use the pipe; for typical Tap callers
// they don't lock — pipe.IsBroken() is atomic and a torn read returns nil.
func (c *Client) currentPipe() *ShellPipe {
	c.pipeMu.Lock()
	defer c.pipeMu.Unlock()
	if c.pipe == nil {
		return nil
	}
	if c.pipe.IsBroken() {
		return nil
	}
	return c.pipe
}

func NewClient(opts ...Option) *Client {
	c := &Client{
		DeviceID: "",
		host:     DefaultHost,
		port:     DefaultPort,
		timeout:  DefaultTimeout,
		log:      nopLogger{},
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

func (c *Client) SetTapHook(fn func(TapEvent)) {
	c.tapHookMu.Lock()
	defer c.tapHookMu.Unlock()
	c.tapHook = fn
}

func (c *Client) fireTapHook(ev TapEvent) {
	c.tapHookMu.RLock()
	fn := c.tapHook
	c.tapHookMu.RUnlock()
	if fn != nil {
		ev.Seq = atomic.AddInt64(&c.tapSeq, 1)
		ev.Ts = time.Now().UnixMilli()
		fn(ev)
	}
}

func (c *Client) connectTransport() error {
	t, err := NewTransport(c.DeviceID, c.host, c.port, c.timeout)
	if err != nil {
		return err
	}
	c.transport = t
	return nil
}

func (c *Client) Connect() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if err := c.connectTransport(); err != nil {
		return fmt.Errorf("transport connect: %w", err)
	}
	c.log.Info("ADB device connected")
	return nil
}

func (c *Client) EnsureConnected() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed {
		return errors.New("client closed")
	}

	if c.transport != nil && c.transport.IsConnected() {
		return nil
	}

	return c.connectTransport()
}

func (c *Client) Devices() ([]string, error) {
	// Use empty device ID to talk to the ADB host directly for the device list
	t, err := NewTransport("", c.host, c.port, c.timeout)
	if err != nil {
		return nil, err
	}
	defer t.Close()

	resp, err := t.Exec("host:devices")
	if err != nil {
		return nil, fmt.Errorf("host:devices: %w", err)
	}

	if len(resp) < 4 {
		return nil, errors.New("invalid devices response")
	}

	// Skip the 4-byte hex length prefix
	data := string(resp[4:])
	var devs []string
	for _, line := range strings.Split(data, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) >= 1 && (strings.Contains(fields[len(fields)-1], "device") || strings.Contains(fields[len(fields)-1], "emulator")) {
			devs = append(devs, fields[0])
		}
	}
	return devs, nil
}

// AutoDetectDevice attempts to find a suitable ADB device if the current one is disconnected.
// It prioritizes common emulator addresses.
func (c *Client) AutoDetectDevice() error {
	devs, err := c.Devices()
	if err != nil {
		return fmt.Errorf("list devices: %w", err)
	}

	if len(devs) == 0 {
		return errors.New("no ADB devices found")
	}

	// 1. Check if current ID is still in the list
	for _, d := range devs {
		if d == c.DeviceID {
			return nil
		}
	}

	// 2. Look for real devices first (not emulator-like)
	var bestMatch string
	for _, d := range devs {
		low := strings.ToLower(d)
		isEmulator := strings.Contains(low, "127.0.0.1") || strings.Contains(low, "localhost") || strings.Contains(low, "emulator-")
		if !isEmulator {
			bestMatch = d
			break
		}
	}

	// 3. Fallback to emulator-like devices if no real device found
	if bestMatch == "" {
		for _, d := range devs {
			low := strings.ToLower(d)
			if strings.Contains(low, "127.0.0.1") || strings.Contains(low, "localhost") || strings.Contains(low, "emulator-") {
				bestMatch = d
				break
			}
		}
	}

	// 4. Fallback to first device in list
	if bestMatch == "" {
		bestMatch = devs[0]
	}

	if c.DeviceID != bestMatch {
		c.log.Warn(fmt.Sprintf("ADB device auto-switched: %s -> %s", c.DeviceID, bestMatch))
		c.DeviceID = bestMatch
	}

	return nil
}

func (c *Client) captureScreenRaw() ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.transport == nil {
		if err := c.connectTransport(); err != nil {
			c.health.RecordFailure(err)
			return nil, err
		}
	}

	resp, err := c.transport.CaptureScreen()
	if err != nil {
		if reconnErr := c.transport.Reconnect(); reconnErr == nil {
			resp, err = c.transport.CaptureScreen()
		}
	}
	if err != nil {
		c.health.RecordFailure(err)
		return nil, err
	}

	return resp, nil
}

func (c *Client) CaptureScreen() ([]byte, error) {
	return c.captureScreenRaw()
}

func (c *Client) CaptureToMat() (gocv.Mat, error) {
	start := time.Now()

	c.mu.Lock()
	if c.transport == nil {
		if err := c.connectTransport(); err != nil {
			c.mu.Unlock()
			c.health.RecordFailure(err)
			return gocv.Mat{}, err
		}
	}
	transport := c.transport
	c.mu.Unlock()

	bufPtr, n, err := transport.CaptureScreenPooled()
	if err != nil {
		if reconnErr := transport.Reconnect(); reconnErr == nil {
			bufPtr, n, err = transport.CaptureScreenPooled()
		}
	}
	if err != nil {
		c.health.RecordFailure(err)
		return gocv.Mat{}, err
	}
	defer ReturnBuffer(bufPtr)

	resp := (*bufPtr)[:n]

	if len(resp) < 12 {
		err := fmt.Errorf("screencap response too short: %d bytes", len(resp))
		c.health.RecordFailure(err)
		return gocv.Mat{}, err
	}

	width := int(binary.LittleEndian.Uint32(resp[0:4]))
	height := int(binary.LittleEndian.Uint32(resp[4:8]))

	if width <= 0 || height <= 0 || width > 4096 || height > 4096 {
		err := fmt.Errorf("invalid screencap dimensions: %dx%d", width, height)
		c.health.RecordFailure(err)
		return gocv.Mat{}, err
	}

	expected := width * height * 4
	if len(resp) < expected+12 {
		err := fmt.Errorf("incomplete screencap: got %d, want %d", len(resp), expected+12)
		c.health.RecordFailure(err)
		return gocv.Mat{}, err
	}

	pixels := resp[12 : expected+12]
	imgRGBA, err := gocv.NewMatFromBytes(height, width, gocv.MatTypeCV8UC4, pixels)
	if err != nil {
		c.health.RecordFailure(err)
		return gocv.Mat{}, fmt.Errorf("mat from bytes: %w", err)
	}

	imgBGR := gocv.NewMat()
	gocv.CvtColor(imgRGBA, &imgBGR, gocv.ColorRGBAToBGR)
	imgRGBA.Close()

	if imgBGR.Empty() {
		imgBGR.Close()
		err := errors.New("converted BGR mat is empty")
		c.health.RecordFailure(err)
		return gocv.Mat{}, err
	}

	c.health.RecordSuccess(time.Since(start))
	return imgBGR, nil
}

// Tap performs a direct ADB tap via the persistent shell pipe when enabled,
// or falls back to transport.Exec otherwise. The pipe path is sync-flush:
// returns once the bytes have been written to the socket (eliminating the
// `app_process` JVM spin-up latency from `shell:input tap`).
func (c *Client) Tap(x, y int) error {
	c.log.Debugf("ADB TAP: (%d, %d)", x, y)
	err := c.routeTap("input tap", x, y, false)
	c.fireTapHook(TapEvent{Type: "tap", X: x, Y: y, ActualX: x, ActualY: y, StdDev: 0, Error: errStr(err)})
	return err
}

// TapAsync is fire-and-forget: returns once the command is queued in the
// pipe channel. Use only in verified-safe hot loops (TapDeployFourSides,
// TapDeployLine, TapDeployPoint) where ordering relative to capture is
// preserved by an explicit HumanSleep/time.Sleep after the batch.
func (c *Client) TapAsync(x, y int) error {
	c.log.Debugf("ADB TAP-ASYNC: (%d, %d)", x, y)
	err := c.routeTap("input tap", x, y, true)
	c.fireTapHook(TapEvent{Type: "tap_async", X: x, Y: y, ActualX: x, ActualY: y, StdDev: 0, Error: errStr(err)})
	return err
}

// routeTap is the shared router for Tap/TapAsync/TapFast through either
// the persistent pipe (when alive) or the legacy transport.Exec fallback.
func (c *Client) routeTap(cmd string, x, y int, async bool) error {
	if p := c.currentPipe(); p != nil {
		full := fmt.Sprintf("%s %d %d", cmd, x, y)
		if async {
			if err := p.SendAsync(full); err == nil {
				return nil
			} else if err != ErrShellPipeBusy {
				// Broken: drop pipe so subsequent calls take legacy path
				c.markPipeBroken()
				return c.tapLocked(x, y)
			}
			// Busy: fall through to legacy
		} else {
			if err := p.Send(full); err == nil {
				return nil
			}
			// Broken: fall back
			c.markPipeBroken()
			return c.tapLocked(x, y)
		}
	}
	return c.tapLocked(x, y)
}

// markPipeBroken atomically closes the pipe so future calls fall back.
func (c *Client) markPipeBroken() {
	c.pipeMu.Lock()
	defer c.pipeMu.Unlock()
	if c.pipe != nil {
		_ = c.pipe.Close()
		c.pipe = nil
	}
}

func (c *Client) tapLocked(x, y int) error {
	if c.transport == nil {
		if err := c.connectTransport(); err != nil {
			return err
		}
	}
	_, err := c.transport.Exec(fmt.Sprintf("shell:input tap %d %d", x, y))
	return err
}

// TapFast performs a tap with minimal randomness and NO intentional delay.
// When the persistent shell pipe is alive and synced to the worker, this
// returns in ~1-5ms (socket flush) instead of ~200ms (`app_process` startup
// from per-call transport.Exec). Falls back to legacy transport.Exec if
// the pipe is disabled or broken.
func (c *Client) TapFast(x, y int, stdDev float64) error {
	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	ox := int(r.NormFloat64() * stdDev)
	oy := int(r.NormFloat64() * stdDev)
	c.mu.Lock()
	err := c.routeTap("input tap", x+ox, y+oy, false)
	c.mu.Unlock()
	c.fireTapHook(TapEvent{Type: "tap_fast", X: x, Y: y, ActualX: x + ox, ActualY: y + oy, StdDev: stdDev, Error: errStr(err)})
	return err
}

// TapFastAsync is fire-and-forget with jitter; preferred in hot loops.
func (c *Client) TapFastAsync(x, y int, stdDev float64) error {
	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	ox := int(r.NormFloat64() * stdDev)
	oy := int(r.NormFloat64() * stdDev)
	err := c.routeTap("input tap", x+ox, y+oy, true)
	c.fireTapHook(TapEvent{Type: "tap_fast_async", X: x, Y: y, ActualX: x + ox, ActualY: y + oy, StdDev: stdDev, Error: errStr(err)})
	return err
}

// TapDual performs two taps sequentially with a 50ms inter-tap settle so
// Clash's UI properly registers each as a separate gesture. When the
// persistent shell pipe is alive each tap round-trips in ~1-5ms instead of
// ~200ms; falls back to legacy transport.Exec per tap if the pipe is broken.
func (c *Client) TapDual(x1, y1 int, stdDev1 float64, x2, y2 int, stdDev2 float64) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.transport == nil {
		if err := c.connectTransport(); err != nil {
			c.fireTapHook(TapEvent{Type: "tap_dual", X: x1, Y: y1, ActualX: x1, ActualY: y1, StdDev: stdDev1, Error: errStr(err)})
			return err
		}
	}

	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	ox1 := int(r.NormFloat64() * stdDev1)
	oy1 := int(r.NormFloat64() * stdDev1)

	r2 := rand.New(rand.NewSource(time.Now().UnixNano() + 71))
	ox2 := int(r2.NormFloat64() * stdDev2)
	oy2 := int(r2.NormFloat64() * stdDev2)

	c.log.Debugf("ADB DUAL TAP (sequential): (%d, %d), (%d, %d)", x1+ox1, y1+oy1, x2+ox2, y2+oy2)
	err1 := c.routeTap("input tap", x1+ox1, y1+oy1, false)
	c.fireTapHook(TapEvent{Type: "tap_dual_1", X: x1, Y: y1, ActualX: x1 + ox1, ActualY: y1 + oy1, StdDev: stdDev1, Error: errStr(err1)})
	if err1 != nil {
		return err1
	}
	time.Sleep(50 * time.Millisecond)
	err2 := c.routeTap("input tap", x2+ox2, y2+oy2, false)
	c.fireTapHook(TapEvent{Type: "tap_dual_2", X: x2, Y: y2, ActualX: x2 + ox2, ActualY: y2 + oy2, StdDev: stdDev2, Error: errStr(err2)})
	return err2
}

// TapTriple performs three taps sequentially with 50ms inter-tap settles
// so Clash's UI properly registers each as a separate deploy gesture. When
// the persistent shell pipe is alive each tap round-trips in ~1-5ms
// instead of ~200ms; falls back to legacy transport.Exec per tap if the
// pipe is broken.
func (c *Client) TapTriple(x1, y1 int, stdDev1 float64, x2, y2 int, stdDev2 float64, x3, y3 int, stdDev3 float64) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.transport == nil {
		if err := c.connectTransport(); err != nil {
			c.fireTapHook(TapEvent{Type: "tap_triple", X: x1, Y: y1, ActualX: x1, ActualY: y1, StdDev: stdDev1, Error: errStr(err)})
			return err
		}
	}

	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	ox1 := int(r.NormFloat64() * stdDev1)
	oy1 := int(r.NormFloat64() * stdDev1)

	r2 := rand.New(rand.NewSource(time.Now().UnixNano() + 71))
	ox2 := int(r2.NormFloat64() * stdDev2)
	oy2 := int(r2.NormFloat64() * stdDev2)

	r3 := rand.New(rand.NewSource(time.Now().UnixNano() + 142))
	ox3 := int(r3.NormFloat64() * stdDev3)
	oy3 := int(r3.NormFloat64() * stdDev3)

	c.log.Debugf("ADB TRIPLE TAP (sequential): (%d, %d), (%d, %d), (%d, %d)", x1+ox1, y1+oy1, x2+ox2, y2+oy2, x3+ox3, y3+oy3)
	err1 := c.routeTap("input tap", x1+ox1, y1+oy1, false)
	c.fireTapHook(TapEvent{Type: "tap_triple_1", X: x1, Y: y1, ActualX: x1 + ox1, ActualY: y1 + oy1, StdDev: stdDev1, Error: errStr(err1)})
	if err1 != nil {
		return err1
	}
	time.Sleep(50 * time.Millisecond)
	err2 := c.routeTap("input tap", x2+ox2, y2+oy2, false)
	c.fireTapHook(TapEvent{Type: "tap_triple_2", X: x2, Y: y2, ActualX: x2 + ox2, ActualY: y2 + oy2, StdDev: stdDev2, Error: errStr(err2)})
	if err2 != nil {
		return err2
	}
	time.Sleep(50 * time.Millisecond)
	err3 := c.routeTap("input tap", x3+ox3, y3+oy3, false)
	c.fireTapHook(TapEvent{Type: "tap_triple_3", X: x3, Y: y3, ActualX: x3 + ox3, ActualY: y3 + oy3, StdDev: stdDev3, Error: errStr(err3)})
	return err3
}

// TapHuman performs a tap with Gaussian-distributed randomness and a small natural delay.
func (c *Client) TapHuman(x, y int, stdDev float64) error {
	// Small hesitation before tapping (50-150ms)
	c.HumanSleep(100, 25)

	return c.TapFast(x, y, stdDev)
}

func (c *Client) TapRandomized(x, y int) error {
	return c.TapHuman(x, y, 3.5)
}

func (c *Client) Swipe(x1, y1, x2, y2 int, ms int) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if p := c.currentPipe(); p != nil {
		full := fmt.Sprintf("input swipe %d %d %d %d %d", x1, y1, x2, y2, ms)
		if err := p.Send(full); err == nil {
			return nil
		}
		c.markPipeBroken()
	}
	if c.transport == nil {
		if err := c.connectTransport(); err != nil {
			return err
		}
	}
	_, err := c.transport.Exec(fmt.Sprintf("shell:input swipe %d %d %d %d %d", x1, y1, x2, y2, ms))
	return err
}

// SwipeHuman simulates a human swipe by adding a slight curve and variable speed.
func (c *Client) SwipeHuman(x1, y1, x2, y2, ms int) error {
	// For simplicity in standard ADB, we use the basic swipe but randomize the points and duration
	r := rand.New(rand.NewSource(time.Now().UnixNano()))

	// Randomize start and end points slightly
	ox1, oy1 := int(r.NormFloat64()*5), int(r.NormFloat64()*5)
	ox2, oy2 := int(r.NormFloat64()*5), int(r.NormFloat64()*5)

	// Randomize duration (+/- 15%)
	duration := int(float64(ms) * (0.85 + r.Float64()*0.3))

	return c.Swipe(x1+ox1, y1+oy1, x2+ox2, y2+oy2, duration)
}

// HumanSleep pauses execution for a duration based on a Gaussian distribution.
func (c *Client) HumanSleep(baseMs, stdDevMs int) {
	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	ms := baseMs + int(r.NormFloat64()*float64(stdDevMs))
	if ms < 10 {
		ms = 10 // Minimum floor
	}
	time.Sleep(time.Duration(ms) * time.Millisecond)
}

func (c *Client) Hold(x, y int, ms int) error {
	return c.Swipe(x, y, x, y, ms)
}

func (c *Client) Text(text string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if p := c.currentPipe(); p != nil {
		// Quote the text to defend against spaces, etc.
		full := "input text '" + strings.ReplaceAll(text, "'", `\'`) + "'"
		if err := p.Send(full); err == nil {
			return nil
		}
		c.markPipeBroken()
	}
	if c.transport == nil {
		if err := c.connectTransport(); err != nil {
			return err
		}
	}
	_, err := c.transport.Exec("shell:input text " + text)
	return err
}

func (c *Client) KeyEvent(code int) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if p := c.currentPipe(); p != nil {
		full := fmt.Sprintf("input keyevent %d", code)
		if err := p.Send(full); err == nil {
			return nil
		}
		c.markPipeBroken()
	}
	if c.transport == nil {
		if err := c.connectTransport(); err != nil {
			return err
		}
	}
	_, err := c.transport.Exec(fmt.Sprintf("shell:input keyevent %d", code))
	return err
}

func (c *Client) Back() error   { return c.KeyEvent(4) }
func (c *Client) Home() error   { return c.KeyEvent(3) }
func (c *Client) Enter() error  { return c.KeyEvent(66) }
func (c *Client) Delete() error { return c.KeyEvent(67) }

// ZoomOut performs a native multi-touch zoom out.
func (c *Client) ZoomOut() error { return c.PinchZoom(true) }

// ZoomIn performs a native multi-touch zoom in.
func (c *Client) ZoomIn() error { return c.PinchZoom(false) }

func (c *Client) Shell(cmd string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.transport == nil {
		if err := c.connectTransport(); err != nil {
			return "", err
		}
	}

	resp, err := c.transport.Exec("shell:" + cmd)
	return strings.TrimSpace(string(resp)), err
}

func (c *Client) ScreenSize() (int, int, error) {
	out, err := c.Shell("wm size")
	if err != nil {
		return 0, 0, err
	}

	var w, h int
	if _, err := fmt.Sscanf(out, "Physical size: %dx%d", &w, &h); err != nil {
		if _, err := fmt.Sscanf(out, "Override size: %dx%d", &w, &h); err != nil {
			return 0, 0, fmt.Errorf("parse wm size: %w", err)
		}
	}
	return w, h, nil
}

func (c *Client) ScreenCapPng(path string) error {
	_, err := c.Shell("screencap -p /sdcard/screen.png")
	return err
}

func (c *Client) StartActivity(component string) error {
	_, err := c.Shell("am start -n " + component)
	return err
}

func (c *Client) StopApp(packageName string) error {
	_, err := c.Shell("am force-stop " + packageName)
	return err
}

func (c *Client) GetFocusedWindow() (string, error) {
	return c.Shell("dumpsys window | grep mCurrentFocus")
}

func (c *Client) ListPackages() ([]string, error) {
	out, err := c.Shell("pm list packages")
	if err != nil {
		return nil, err
	}
	var pkgs []string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "package:") {
			pkgs = append(pkgs, strings.TrimPrefix(line, "package:"))
		}
	}
	return pkgs, nil
}

func (c *Client) IsAppRunning(packageName string) (bool, error) {
	out, err := c.Shell("dumpsys activity activities | grep " + packageName)
	if err != nil {
		return false, nil
	}
	return strings.Contains(out, packageName), nil
}

func (c *Client) WakeDevice() error {
	return c.KeyEvent(26)
}

func (c *Client) PowerOff() error {
	return c.KeyEvent(223)
}

func (c *Client) SendAstroBuddy(msg string) error {
	_, err := c.Shell("am broadcast -a clashofclans.astro.BUDDY")
	return err
}

func (c *Client) IsBooted() (bool, error) {
	out, err := c.Shell("getprop sys.boot_completed")
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(out) == "1", nil
}

func (c *Client) WaitForBoot(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		booted, err := c.IsBooted()
		if err == nil && booted {
			return nil
		}
		time.Sleep(2 * time.Second)
	}
	return fmt.Errorf("timeout waiting for boot after %v", timeout)
}

func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	if c.transport != nil {
		c.transport.Close()
		c.transport = nil
	}
	c.pipeMu.Lock()
	if c.pipe != nil {
		_ = c.pipe.Close()
		c.pipe = nil
	}
	c.pipeMu.Unlock()
	return nil
}

func (c *Client) IsConnected() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.transport != nil && c.transport.IsConnected()
}

func (c *Client) ForceStop(pkg string) error {
	if err := c.EnsureConnected(); err != nil {
		return err
	}
	return c.transport.StopApp(pkg)
}

func (c *Client) StartApp(pkg string) error {
	if err := c.EnsureConnected(); err != nil {
		return err
	}
	// Use monkey to reliably start the app by package name
	_, err := c.transport.Exec("shell:monkey -p " + pkg + " -c android.intent.category.LAUNCHER 1")
	return err
}

func (c *Client) Reconnect() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.transport != nil {
		c.transport.Close()
	}
	return c.connectTransport()
}

func (c *Client) GetArchitecture() (string, error) {
	return c.Shell("getprop ro.product.cpu.abi")
}

func (c *Client) DetectTouchDevice() (string, error) {
	out, err := c.Shell("getevent -pl")
	if err != nil {
		return "", err
	}

	lines := strings.Split(out, "\n")
	var currentDevice string
	for _, line := range lines {
		if strings.HasPrefix(line, "add device") {
			parts := strings.Split(line, ":")
			if len(parts) > 1 {
				currentDevice = strings.TrimSpace(parts[1])
			}
			continue
		}
		if strings.Contains(line, "ABS_MT_POSITION_X") {
			return currentDevice, nil
		}
	}

	// Fallback for some emulators
	out, err = c.Shell("getevent -p")
	if err != nil {
		return "", err
	}
	lines = strings.Split(out, "\n")
	for _, line := range lines {
		if strings.HasPrefix(line, "add device") {
			parts := strings.Split(line, ":")
			if len(parts) > 1 {
				currentDevice = strings.TrimSpace(parts[1])
			}
			continue
		}
		// 0035 is ABS_MT_POSITION_X
		if strings.Contains(line, "0035") {
			return currentDevice, nil
		}
	}

	return "", errors.New("touchscreen device not found")
}

func (c *Client) Health() Health {
	return c.health
}

func errStr(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
