package adb

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
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

	// wedgeFallbacks counts commands delivered through shellWedgeFallback. See
	// WedgeFallbacks.
	wedgeFallbacks atomic.Uint64
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
	return t.execLocked(service)
}

func (t *Transport) execLocked(service string) ([]byte, error) {
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

// bluestacksWedgePrefix is the compound-command prefix used to work
// around a BlueStacks adbd bug. In the wedged state adbd answers every
// bare command with FAIL "closed" (adb CLI prints "error: closed")
// while compound commands — "getprop <anything>; <cmd>" — execute
// normally, 100% reliably, with unmodified binary output. The wedge
// survives adb kill-server/start-server and transport reconnects
// (it lives in the DEVICE's adbd, not the local adb-server), so the
// only durable fix at this layer is to rewrite the command line.
//
// getprop is used as the prefix because it is the cheapest whitelisted
// call (~30ms round trip observed) and its output lands on the first
// line where callers that parse `wm size` output can ignore it.
const bluestacksWedgePrefix = "getprop " + wedgePrefixProperty + "; "

// wedgePrefixProperty is the property the BlueStacks wedge workaround reads. It
// is named separately because the detector that spots an unstripped prefix line
// (client.go's isWedgedShellOutput) must match on the name, not on this
// device's value for it — ro.build.type reads "user" on a retail image and
// "userdebug" on a debuggable one.
const wedgePrefixProperty = "ro.build.type"

// isClosedFailure reports whether err is the adbd FAIL "closed"
// response that characterizes the BlueStacks wedge (raw protocol:
// "ADB: closed"; adb CLI rendering: "error: closed").
func isClosedFailure(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "ADB: closed") || strings.Contains(msg, "error: closed")
}

// shellWedgeFallback rewrites a service string into its compound
// (wedge-safe) equivalent. Returns the rewritten service and true when
// a fallback applies:
//
//   - "shell:cmd"          → "shell:getprop ...; cmd"
//     (skipped when the command is already prefixed)
//   - "exec:…/screencap"   → "shell:getprop ...; screencap"
//     (the exec service cannot run compound commands; the shell
//     service passes raw screencap bytes through unmodified on the
//     Android 10+ non-pty path BlueStacks implements)
//
// Everything else (host:*, root:, etc.) has no fallback — second
// return is false.
func shellWedgeFallback(service string) (string, bool) {
	if strings.HasPrefix(service, "shell:") {
		cmd := strings.TrimPrefix(service, "shell:")
		if strings.Contains(cmd, bluestacksWedgePrefix) {
			return service, false // already prefixed; don't double up
		}
		return "shell:" + bluestacksWedgePrefix + cmd, true
	}
	if strings.HasPrefix(service, "exec:") && strings.Contains(service, "screencap") {
		return "shell:" + bluestacksWedgePrefix + "screencap", true
	}
	return service, false
}

func (t *Transport) Exec(service string) ([]byte, error) {
	out, err := t.execService(service)
	if err == nil || !isClosedFailure(err) {
		return out, err
	}
	// BlueStacks adbd wedge: retry once with the compound prefix.
	fallback, ok := shellWedgeFallback(service)
	if !ok {
		return out, err
	}
	out2, err2 := t.execService(fallback)
	if err2 == nil {
		// Record that the fallback carried this command. The prefix line is
		// stripped from the result, so the caller cannot otherwise tell that
		// the command only worked because of the workaround — and on this
		// emulator the framework pinch is exactly such a command.
		t.wedgeFallbacks.Add(1)
		// The getprop prefix line precedes the payload; strip it so
		// callers see exactly what the original command would have
		// returned.
		return stripPrefixLine(out2), nil
	}
	return out, err
}

// WedgeFallbacks is how many commands have been delivered via the BlueStacks
// wedge workaround rather than on their first attempt. Callers compare it across
// one Exec to learn whether the workaround was needed, which the returned bytes
// cannot say because the prefix line is stripped.
func (t *Transport) WedgeFallbacks() uint64 {
	if t == nil {
		return 0
	}
	return t.wedgeFallbacks.Load()
}

// stripPrefixLine removes everything up to and including the first
// newline. Used to drop the "<prop value>\n" line produced by the
// BlueStacks wedge workaround prefix. Input shorter than one line is
// returned unchanged.
func stripPrefixLine(data []byte) []byte {
	idx := bytes.IndexByte(data, '\n')
	if idx < 0 {
		return data
	}
	return data[idx+1:]
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

func (t *Transport) CaptureScreenPooled() (*[]byte, int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.conn == nil {
		if err := t.connectLocked(); err != nil {
			return nil, 0, err
		}
	}
	if err := t.setTransportLocked(); err != nil {
		t.conn.Close()
		t.conn = nil
		return nil, 0, err
	}
	if err := t.sendServiceLocked("exec:/system/bin/screencap"); err != nil {
		t.conn.Close()
		t.conn = nil
		// BlueStacks adbd wedge: the exec service answers FAIL
		// "closed" for screencap while the shell service still runs
		// compound commands with intact binary output. Retry via the
		// shell service (we already hold t.mu, so reuse the locked
		// helpers instead of re-entering execService).
		if isClosedFailure(err) {
			if bufPtr2, n2, err2 := t.captureViaShellLocked(); err2 == nil {
				return bufPtr2, n2, nil
			}
		}
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
			t.conn.Close()
			t.conn = nil
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

// ReturnBuffer hands a pooled capture buffer back. The slice is restored to
// its full capacity first: a caller may be returning a resliced view (the
// wedge-path normalize does), while the capture loop treats a pooled buffer's
// LENGTH as its writable extent — handing back a short view would make the
// next capture read into a truncated buffer and then "grow" it from that
// smaller base.
func ReturnBuffer(bufPtr *[]byte) {
	if bufPtr == nil || cap(*bufPtr) == 0 {
		return
	}
	*bufPtr = (*bufPtr)[:cap(*bufPtr)]
	bufferPool.Put(bufPtr)
}

// captureViaShellLocked grabs a screencap through the shell service
// with the BlueStacks wedge workaround prefix, pooling the same 10MB
// buffers as CaptureScreenPooled. Caller must hold t.mu. On success
// t.conn is nil (the per-exec close happens inside); on failure the
// caller's original error is the one to propagate.
func (t *Transport) captureViaShellLocked() (*[]byte, int, error) {
	if err := t.connectLocked(); err != nil {
		return nil, 0, err
	}
	defer func() {
		if t.conn != nil {
			t.conn.Close()
			t.conn = nil
		}
	}()
	if err := t.sendServiceLocked("shell:" + bluestacksWedgePrefix + "screencap"); err != nil {
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
			ReturnBuffer(bufPtr)
			return nil, 0, err
		}
		if total == len(buf) {
			newBuf := make([]byte, len(buf)*2)
			copy(newBuf, buf)
			buf = newBuf
			*bufPtr = buf
		}
	}
	// The frame is normalized IN PLACE inside the buffer this capture already
	// read into, so ownership of that pooled buffer passes to the returned
	// frame: the caller releases it with ReturnBuffer, never here.
	return normalizeShellScreencap(bufPtr, total)
}

// normalizeShellScreencap rewrites the raw output of `shell:...screencap`
// (line-prefixed by the wedge workaround, 16-byte header: w, h, format,
// colorspace) into the 12-byte-header layout (w, h, format) that CaptureToMat
// and the boot probe expect.
//
// It normalizes IN the pooled buffer the capture already read into: the frame
// is moved down to the buffer's start, which costs one overlapping memmove and
// no allocation. The BlueStacks adbd wedge routes every capture down this path,
// and the previous implementation allocated a fresh frame buffer per capture
// (~2.5 MB at 860x732) purely to prepend a header — several megabytes of
// garbage per second on the capture loop, for the length of a session.
//
// On success the buffer now holds the frame and the caller owns it (release it
// with ReturnBuffer). Any layout that does not match the expected shape returns
// an error with the buffer untouched, rather than producing a corrupt frame.
func normalizeShellScreencap(bufPtr *[]byte, total int) (*[]byte, int, error) {
	if bufPtr == nil || total <= 0 || total > cap(*bufPtr) {
		return nil, 0, fmt.Errorf("screencap via shell: invalid capture buffer (total=%d)", total)
	}
	buf := (*bufPtr)[:total]

	// Drop the "<prop value>\n" prefix line.
	idx := bytes.IndexByte(buf, '\n')
	if idx < 0 {
		return nil, 0, errors.New("screencap via shell: missing prefix line")
	}
	payload := buf[idx+1:]
	if len(payload) < 16 {
		return nil, 0, fmt.Errorf("screencap via shell: payload too short (%d bytes)", len(payload))
	}
	w := int(binary.LittleEndian.Uint32(payload[0:4]))
	h := int(binary.LittleEndian.Uint32(payload[4:8]))
	if w <= 0 || h <= 0 || w > 8192 || h > 8192 {
		return nil, 0, fmt.Errorf("screencap via shell: invalid dimensions %dx%d", w, h)
	}
	pixels := payload[16:]
	if len(pixels) < w*h*4 {
		return nil, 0, fmt.Errorf("screencap via shell: incomplete frame: got %d, want %d", len(pixels), w*h*4)
	}
	pixels = pixels[:w*h*4]

	// The device's header is exactly 4 bytes longer than the one consumers
	// read (it carries a colorspace field), so dropping those 4 bytes and
	// moving the frame to the buffer start converts one layout into the other
	// in place. copy() is memmove — the ranges overlap.
	n := 12 + len(pixels)
	out := buf[:n]
	copy(out[12:], pixels)
	binary.LittleEndian.PutUint32(out[0:4], uint32(w))
	binary.LittleEndian.PutUint32(out[4:8], uint32(h))
	// out[8:12] is the format field. No consumer reads it, and the direct path
	// passes the device's raw value through, so zero it here to keep the two
	// capture paths handing callers identical headers.
	out[8], out[9], out[10], out[11] = 0, 0, 0, 0

	*bufPtr = out
	return bufPtr, n, nil
}

func (t *Transport) CaptureScreen() ([]byte, error) {
	return t.Exec("exec:/system/bin/screencap")
}

func (t *Transport) Tap(x, y int) error {
	_, err := t.Exec(fmt.Sprintf("shell:input tap %d %d", x, y))
	return err
}

// NOTE: this used to carry a TapRandomized that offset the point by a
// uniform ±5 px and slept a uniform 50-200ms before tapping. It had no callers
// (every tap goes through Client, whose TapHuman owns the jitter and the human
// reaction delay) and it was the only uniform-random tap left in the transport
// layer — a second, worse answer to "what is a human-like tap" sitting one
// interface away from the code that actually taps. Deleted rather than kept as
// an unused alternative (see internal/game/dismiss.go for the other half of this
// cleanup).

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

	// An override wins: after a runtime `wm size WxH` the command prints both
	// lines, and the override is what the framebuffer renders. Reading the
	// physical size first made the bot calibrate for the wrong geometry — see
	// docs/RESOLUTION.md.
	var w, h int
	for _, line := range strings.Split(out, "\n") {
		if _, err := fmt.Sscanf(strings.TrimSpace(line), "Override size: %dx%d", &w, &h); err == nil {
			return w, h, nil
		}
	}
	for _, line := range strings.Split(out, "\n") {
		if _, err := fmt.Sscanf(strings.TrimSpace(line), "Physical size: %dx%d", &w, &h); err == nil {
			return w, h, nil
		}
	}
	return 0, 0, fmt.Errorf("parse wm size: no size line in %q", strings.TrimSpace(out))
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
