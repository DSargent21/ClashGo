package adb

import (
	"fmt"
	"io"
	"math/rand"
	"net"
	"os/exec"
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
	if err := t.Reconnect(); err != nil {
		return nil, err
	}
	return t, nil
}

func (t *Transport) execService(service string) ([]byte, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if err := t.connectLocked(); err != nil {
		return nil, err
	}
	defer func() {
		if t.conn != nil {
			t.conn.Close()
			t.conn = nil
		}
	}()

	if err := t.sendServiceLocked(service); err != nil {
		return nil, err
	}
	return io.ReadAll(t.conn)
}

func (t *Transport) connectDeviceLocked() error {
	addr := net.JoinHostPort(t.host, fmt.Sprintf("%d", t.port))
	conn, err := net.DialTimeout("tcp", addr, DialTimeout)
	if err != nil {
		return err
	}
	defer conn.Close()

	service := "host:connect:" + t.deviceID
	payload := fmt.Sprintf("%04x%s", len(service), service)
	conn.SetWriteDeadline(time.Now().Add(t.timeout))
	if _, err := conn.Write([]byte(payload)); err != nil {
		return err
	}

	conn.SetReadDeadline(time.Now().Add(t.timeout))
	status := make([]byte, 4)
	if _, err := io.ReadFull(conn, status); err != nil {
		return err
	}

	if string(status) != "OKAY" {
		return fmt.Errorf("connect failed with status %s", string(status))
	}

	lenBytes := make([]byte, 4)
	if _, err := io.ReadFull(conn, lenBytes); err != nil {
		return err
	}
	var msgLen uint32
	if _, err := fmt.Sscanf(string(lenBytes), "%04x", &msgLen); err == nil && msgLen < 4096 {
		msg := make([]byte, msgLen)
		_, _ = io.ReadFull(conn, msg)
	}
	return nil
}

func (t *Transport) connectLocked() error {
	if t.closed.Load() {
		return ErrTransportGone
	}

	if t.conn != nil {
		t.conn.Close()
		t.conn = nil
	}

	addr := net.JoinHostPort(t.host, fmt.Sprintf("%d", t.port))
	conn, err := net.DialTimeout("tcp", addr, DialTimeout)
	if err != nil {
		// Auto-start ADB server if connection is refused
		_ = exec.Command("adb", "start-server").Run()
		time.Sleep(500 * time.Millisecond) // brief wait for startup
		conn, err = net.DialTimeout("tcp", addr, DialTimeout)
		if err != nil {
			return fmt.Errorf("adb dial %s (after start-server): %w", addr, err)
		}
	}

	t.conn = conn

	if err := t.setTransportLocked(); err != nil {
		conn.Close()
		t.conn = nil

		// If it's a TCP device, try connecting it to the ADB server and retry once
		if strings.Contains(t.deviceID, ":") {
			_ = t.connectDeviceLocked()
			
			// Retry connect to the target transport
			conn2, err2 := net.DialTimeout("tcp", addr, DialTimeout)
			if err2 == nil {
				t.conn = conn2
				if err := t.setTransportLocked(); err == nil {
					return nil
				}
				conn2.Close()
				t.conn = nil
			}
		}

		return fmt.Errorf("set transport: %w", err)
	}

	return nil
}

func (t *Transport) sendServiceLocked(service string) error {
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
		return t.readFailureLocked()
	default:
		return fmt.Errorf("%w: status=%q", ErrInvalidResponse, string(status))
	}
}

func (t *Transport) readFailureLocked() error {
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

func (t *Transport) Reconnect() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.connectLocked()
}

func (t *Transport) setTransportLocked() error {
	if t.deviceID == "" {
		return nil
	}
	return t.sendServiceLocked("host:transport:" + t.deviceID)
}

func (t *Transport) Exec(service string) ([]byte, error) {
	return t.execService(service)
}

func (t *Transport) ExecRaw(service string) ([]byte, error) {
	return t.execService(service)
}

func (t *Transport) CaptureScreenPooled() (*[]byte, int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if err := t.connectLocked(); err != nil {
		return nil, 0, err
	}
	defer func() {
		if t.conn != nil {
			t.conn.Close()
			t.conn = nil
		}
	}()

	if err := t.sendServiceLocked("exec:/system/bin/screencap"); err != nil {
		return nil, 0, err
	}

	bufPtr := bufferPool.Get().(*[]byte)
	buf := *bufPtr
	total := 0

	for {
		n, err := t.conn.Read(buf[total:])
		total += n
		if err != nil {
			if err == io.EOF {
				break
			}
			bufferPool.Put(bufPtr)
			return nil, 0, err
		}
		if total == len(buf) {
			// Grow buffer if needed
			newBuf := make([]byte, len(buf)*2)
			copy(newBuf, buf)
			buf = newBuf
			*bufPtr = buf
		}
	}

	return bufPtr, total, nil
}

func ReturnBuffer(bufPtr *[]byte) {
	bufferPool.Put(bufPtr)
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
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed.Load() || t.conn == nil {
		return false
	}
	return true
}

var bufferPool = sync.Pool{
	New: func() interface{} {
		// 10MB buffer to handle uncompressed screen captures
		b := make([]byte, 10*1024*1024)
		return &b
	},
}