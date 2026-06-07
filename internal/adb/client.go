package adb

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math/rand"
	"strings"
	"sync"
	"time"

	"gocv.io/x/gocv"
)

type Client struct {
	DeviceID string
	host     string
	port     int
	timeout  time.Duration
	log      Logger

	transport *Transport
	health    Health
	mu        sync.Mutex
	closed    bool
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

// Tap performs a direct ADB tap.
func (c *Client) Tap(x, y int) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.transport == nil {
		if err := c.connectTransport(); err != nil {
			return err
		}
	}

	_, err := c.transport.Exec(fmt.Sprintf("shell:input tap %d %d", x, y))
	return err
}

// TapFast performs a tap with minimal randomness and NO intentional delay.
func (c *Client) TapFast(x, y int, stdDev float64) error {
	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	ox := int(r.NormFloat64() * stdDev)
	oy := int(r.NormFloat64() * stdDev)
	return c.Tap(x+ox, y+oy)
}

// TapDual performs two taps simultaneously using background shell execution.
func (c *Client) TapDual(x1, y1 int, stdDev1 float64, x2, y2 int, stdDev2 float64) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.transport == nil {
		if err := c.connectTransport(); err != nil {
			return err
		}
	}

	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	ox1 := int(r.NormFloat64() * stdDev1)
	oy1 := int(r.NormFloat64() * stdDev1)

	r2 := rand.New(rand.NewSource(time.Now().UnixNano() + 71))
	ox2 := int(r2.NormFloat64() * stdDev2)
	oy2 := int(r2.NormFloat64() * stdDev2)

	cmd := fmt.Sprintf("shell:input tap %d %d & input tap %d %d & wait", x1+ox1, y1+oy1, x2+ox2, y2+oy2)
	_, err := c.transport.Exec(cmd)
	return err
}

// TapTriple performs three taps simultaneously using background shell execution.
func (c *Client) TapTriple(x1, y1 int, stdDev1 float64, x2, y2 int, stdDev2 float64, x3, y3 int, stdDev3 float64) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.transport == nil {
		if err := c.connectTransport(); err != nil {
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

	cmd := fmt.Sprintf("shell:input tap %d %d & input tap %d %d & input tap %d %d & wait", x1+ox1, y1+oy1, x2+ox2, y2+oy2, x3+ox3, y3+oy3)
	_, err := c.transport.Exec(cmd)
	return err
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

// ZoomOut sends the standard Android zoom out keyevent (169)
func (c *Client) ZoomOut() error { return c.KeyEvent(169) }

// ZoomIn sends the standard Android zoom in keyevent (168)
func (c *Client) ZoomIn() error { return c.KeyEvent(168) }

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

func (c *Client) Health() Health {
	return c.health
}