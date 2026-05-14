package adb

import (
	"fmt"
	"io"
	"math/rand"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const MaxPayload = 256 * 1024 * 1024

type Transport struct {
	deviceID string
	host     string
	port     int
	timeout  time.Duration

	conn   net.Conn
	mu     sync.Mutex
	closed atomic.Bool
	seq    uint32
}

func NewTransport(deviceID, host string, port int, timeout time.Duration) (*Transport, error) {
	t := &Transport{
		deviceID: deviceID,
		host:     host,
		port:     port,
		timeout:  timeout,
	}
	if err := t.connect(); err != nil {
		return nil, err
	}
	return t, nil
}

func (t *Transport) connect() error {
	if t.closed.Load() {
		return ErrTransportGone
	}

	addr := fmt.Sprintf("%s:%d", t.host, t.port)
	conn, err := net.DialTimeout("tcp", addr, DialTimeout)
	if err != nil {
		return fmt.Errorf("adb dial %s: %w", addr, err)
	}

	t.conn = conn

	if err := t.setTransport(); err != nil {
		conn.Close()
		t.conn = nil
		return fmt.Errorf("set transport: %w", err)
	}

	return nil
}

func (t *Transport) sendService(service string) error {
	conn := t.conn
	if conn == nil {
		return ErrNotConnected
	}

	payload := fmt.Sprintf("%04x%s", len(service), service)

	conn.SetWriteDeadline(time.Now().Add(t.timeout))
	if _, err := conn.Write([]byte(payload)); err != nil {
		return fmt.Errorf("write service: %w", err)
	}

	conn.SetReadDeadline(time.Now().Add(t.timeout))
	status := make([]byte, 4)
	if _, err := io.ReadFull(conn, status); err != nil {
		return fmt.Errorf("read status: %w", err)
	}

	switch string(status) {
	case "OKAY":
		return nil
	case "FAIL":
		return t.readFailure()
	default:
		return fmt.Errorf("%w: status=%q", ErrInvalidResponse, string(status))
	}
}

func (t *Transport) readFailure() error {
	conn := t.conn
	if conn == nil {
		return ErrNotConnected
	}

	conn.SetReadDeadline(time.Now().Add(t.timeout))
	lenBytes := make([]byte, 4)
	if _, err := io.ReadFull(conn, lenBytes); err != nil {
		return fmt.Errorf("read failure len: %w", err)
	}

	msgLen := uint32(0)
	_, err := fmt.Sscanf(string(lenBytes), "%04x", &msgLen)
	if err != nil {
		return fmt.Errorf("parse failure len: %w", err)
	}

	if msgLen > 4096 {
		return fmt.Errorf("failure message too long: %d", msgLen)
	}

	msg := make([]byte, msgLen)
	if _, err := io.ReadFull(conn, msg); err != nil {
		return fmt.Errorf("read failure msg: %w", err)
	}

	return fmt.Errorf("ADB: %s", string(msg))
}

func (t *Transport) execService(service string) ([]byte, error) {
	if err := t.sendService(service); err != nil {
		return nil, err
	}
	return io.ReadAll(t.conn)
}

func (t *Transport) execRaw(service string) ([]byte, error) {
	return t.execService(service)
}

func (t *Transport) Reconnect() error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.closed.Load() {
		return ErrTransportGone
	}

	if t.conn != nil {
		t.conn.Close()
		t.conn = nil
	}

	return t.connect()
}

func (t *Transport) setTransport() error {
	if t.deviceID == "" {
		return nil
	}
	return t.sendService("host:transport:" + t.deviceID)
}

func (t *Transport) ensureConnected() error {
	if t.closed.Load() {
		return ErrTransportGone
	}
	// Always reconnect for services that take over the connection
	if t.conn != nil {
		t.conn.Close()
		t.conn = nil
	}
	return t.Reconnect()
}

func (t *Transport) Close() error {
	if t.closed.Swap(true) {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.conn != nil {
		t.conn.Close()
		t.conn = nil
	}
	return nil
}

func (t *Transport) IsConnected() bool {
	if t.closed.Load() || t.conn == nil {
		return false
	}
	return true
}

func (t *Transport) Exec(service string) ([]byte, error) {
	if err := t.ensureConnected(); err != nil {
		return nil, err
	}
	return t.execService(service)
}

func (t *Transport) ExecRaw(service string) ([]byte, error) {
	if err := t.ensureConnected(); err != nil {
		return nil, err
	}
	return t.execRaw(service)
}

func (t *Transport) CaptureScreen() ([]byte, error) {
	return t.ExecRaw("exec:/system/bin/screencap")
}

func (t *Transport) Tap(x, y int) error {
	_, err := t.Exec(fmt.Sprintf("shell:input tap %d %d", x, y))
	return err
}

func (t *Transport) TapRandomized(x, y int) error {
	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	ox := r.Intn(11) - 5
	oy := r.Intn(11) - 5
	time.Sleep(time.Duration(50+r.Intn(151)) * time.Millisecond)
	return t.Tap(x+ox, y+oy)
}

func (t *Transport) Swipe(x1, y1, x2, y2 int, ms int) error {
	_, err := t.Exec(fmt.Sprintf("shell:input swipe %d %d %d %d %d", x1, y1, x2, y2, ms))
	return err
}

func (t *Transport) Hold(x, y int, ms int) error {
	return t.Swipe(x, y, x, y, ms)
}

func (t *Transport) Text(text string) error {
	_, err := t.Exec("shell:input text " + text)
	return err
}

func (t *Transport) KeyEvent(code int) error {
	_, err := t.Exec(fmt.Sprintf("shell:input keyevent %d", code))
	return err
}

func (t *Transport) Back() error   { return t.KeyEvent(4) }
func (t *Transport) Home() error   { return t.KeyEvent(3) }
func (t *Transport) Enter() error  { return t.KeyEvent(66) }
func (t *Transport) Delete() error { return t.KeyEvent(67) }

func (t *Transport) Shell(cmd string) (string, error) {
	resp, err := t.Exec("shell:" + cmd)
	return strings.TrimSpace(string(resp)), err
}

func (t *Transport) ScreenSize() (int, int, error) {
	out, err := t.Shell("wm size")
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

func (t *Transport) ScreenCapPng(path string) error {
	_, err := t.Exec("shell:screencap -p /sdcard/screen.png")
	return err
}

func (t *Transport) StartActivity(component string) error {
	_, err := t.Exec("shell:am start -n " + component)
	return err
}

func (t *Transport) StopApp(packageName string) error {
	_, err := t.Exec("shell:am force-stop " + packageName)
	return err
}

func (t *Transport) GetFocusedWindow() (string, error) {
	return t.Shell("dumpsys window | grep mCurrentFocus")
}

func (t *Transport) ListPackages() ([]string, error) {
	out, err := t.Shell("pm list packages")
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

func (t *Transport) IsAppRunning(packageName string) (bool, error) {
	out, err := t.Shell("dumpsys activity activities | grep " + packageName)
	if err != nil {
		return false, nil
	}
	return strings.Contains(out, packageName), nil
}

func (t *Transport) WakeDevice() error {
	return t.KeyEvent(26)
}

func (t *Transport) PowerOff() error {
	return t.KeyEvent(223)
}

func (t *Transport) SendAstroBuddy(msg string) error {
	_, err := t.Exec("shell:am broadcast -a clashofclans.astro.BUDDY")
	return err
}