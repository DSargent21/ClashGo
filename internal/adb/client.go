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
	health   Health
	mu       sync.Mutex
	closed   bool
}

type adbLogAdapter struct {
	log Logger
}

func (a *adbLogAdapter) Debug() bool { return a.log.Debug() }
func (a *adbLogAdapter) Debugf(format string, v ...any) {
	a.log.Debugf(format, v...)
}
func (a *adbLogAdapter) Info(msg string)  { a.log.Info(msg) }
func (a *adbLogAdapter) Warn(msg string)  { a.log.Warn(msg) }
func (a *adbLogAdapter) Error(msg string) { a.log.Error(msg) }
func (a *adbLogAdapter) WithFields(fields map[string]any) Logger {
	return &adbLogAdapter{log: a.log.WithFields(fields)}
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
	zl := &adbLogAdapter{log: c.log}

	t, err := NewTransport(c.DeviceID, c.host, c.port, c.timeout)
	if err != nil {
		return err
	}
	c.transport = t
	_ = zl
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
	t, err := NewTransport(c.DeviceID, c.host, c.port, c.timeout)
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

	resp, err := c.captureScreenRaw()
	if err != nil {
		return gocv.Mat{}, err
	}

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

func (c *Client) TapRandomized(x, y int) error {
	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	ox := r.Intn(11) - 5
	oy := r.Intn(11) - 5
	time.Sleep(time.Duration(50+r.Intn(151)) * time.Millisecond)
	return c.Tap(x+ox, y+oy)
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
	// Note: Coc launch usually needs the component name
	return c.transport.StartActivity(pkg + "/com.supercell.clashofclans.GameApp")
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