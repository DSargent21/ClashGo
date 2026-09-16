package bot

import (
	"context"
	"encoding/binary"
	"errors"
	"testing"
	"time"

	"github.com/rs/zerolog"

	"github.com/Ducky705/ClashGO/internal/adb"
)

// fakeRunner scripts the Shell/CaptureScreen responses of the
// adb.ShellRunner interface without a live device.
type fakeRunner struct {
	shellOut   string
	shellErr   error
	capBuf     []byte
	capErr     error
	shellCalls int
	capCalls   int
}

func (f *fakeRunner) Shell(ctx context.Context, cmd string) (string, error) {
	f.shellCalls++
	return f.shellOut, f.shellErr
}

func (f *fakeRunner) CaptureScreen(ctx context.Context) ([]byte, error) {
	f.capCalls++
	return f.capBuf, f.capErr
}

func (f *fakeRunner) Exec(ctx context.Context, service string) ([]byte, error) {
	return nil, errors.New("not implemented")
}

func newOrchestratorWithRunner(r adb.ShellRunner) *BootOrchestrator {
	cfg := DefaultBootConfig()
	report := NewBootReport()
	return &BootOrchestrator{
		cfg:    cfg,
		runner: r,
		prober: adb.NewBootProber(r, adb.BootProbeConfig{}, "com.supercell.clashofclans"),
		report: report,
		logger: zerolog.Nop(),
	}
}

func screencapFrame(w, h int) []byte {
	buf := make([]byte, 12+w*h*4)
	binary.LittleEndian.PutUint32(buf[0:4], uint32(w))
	binary.LittleEndian.PutUint32(buf[4:8], uint32(h))
	for i := 12; i < len(buf); i += 4 {
		buf[i] = 200 // bright pixels, non-black luma
	}
	return buf
}

// Both wm size AND screencap fail on every attempt; the retry ladder
// must give up after maxAttempts with a joined error (not after one
// shot like before).
func TestScreenSizeRetriesBothSourcesBeforeFailing(t *testing.T) {
	r := &fakeRunner{shellErr: errors.New("ADB: closed"), capErr: errors.New("ADB: closed")}
	o := newOrchestratorWithRunner(r)
	recoverCalls := 0
	o.screenRecover = func(attempt int) { recoverCalls++ }

	_, _, err := o.screenSize(context.Background())
	if err == nil {
		t.Fatal("expected error when both sources always fail")
	}
	if r.shellCalls != 3 || r.capCalls != 3 {
		t.Fatalf("want 3 wm-size + 3 screencap attempts, got %d + %d", r.shellCalls, r.capCalls)
	}
	if recoverCalls != 2 {
		t.Fatalf("want 2 recovery injections between attempts, got %d", recoverCalls)
	}
}

// wm size fails with "ADB: closed" but screencap works — the
// existing screencap-fallback path must still succeed on the first
// attempt, with no recovery injected.
func TestScreenSizeScreencapFallbackStillWins(t *testing.T) {
	r := &fakeRunner{shellErr: errors.New("ADB: closed"), capBuf: screencapFrame(860, 732)}
	o := newOrchestratorWithRunner(r)
	o.screenRecover = func(int) { t.Fatal("recovery must not fire when screencap fallback succeeds") }

	w, h, err := o.screenSize(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if w != 860 || h != 732 {
		t.Fatalf("want 860x732, got %dx%d", w, h)
	}
}

// wm size works — happy path, no recovery.
func TestScreenSizeHappyPath(t *testing.T) {
	r := &fakeRunner{shellOut: "Physical size: 860x732"}
	o := newOrchestratorWithRunner(r)
	o.screenRecover = func(int) { t.Fatal("recovery must not fire on happy path") }

	w, h, err := o.screenSize(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if w != 860 || h != 732 {
		t.Fatalf("want 860x732, got %dx%d", w, h)
	}
}

// A runtime `wm size` override must win over the physical size. Reading the
// physical line first made the bot calibrate for 860x732 while the framebuffer
// rendered 1280x720, so every tap and probe was placed for the wrong geometry.
func TestScreenSizePrefersOverrideOverPhysical(t *testing.T) {
	r := &fakeRunner{shellOut: "Physical size: 860x732\nOverride size: 1280x720\n"}
	o := newOrchestratorWithRunner(r)
	o.screenRecover = func(int) { t.Fatal("recovery must not fire when the override parses") }

	w, h, err := o.screenSize(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if w != 1280 || h != 720 {
		t.Fatalf("want the override 1280x720, got %dx%d", w, h)
	}

	// The wedged-shell workaround prefixes the command, so the lines do not
	// have to start at byte 0 of the output.
	r = &fakeRunner{shellOut: "x\nPhysical size: 860x732\nOverride size: 1280x720\n"}
	o = newOrchestratorWithRunner(r)
	if w, h, err = o.screenSize(context.Background()); err != nil || w != 1280 || h != 720 {
		t.Fatalf("prefixed output: got %dx%d err=%v, want 1280x720", w, h, err)
	}
}

// Recovery injection passes the right attempt number (1-based) so the
// ladder can escalate cheap→destructive.
func TestScreenSizeRecoveryLadderEscalates(t *testing.T) {
	r := &fakeRunner{shellErr: errors.New("ADB: closed"), capErr: errors.New("ADB: closed")}
	o := newOrchestratorWithRunner(r)
	var attempts []int
	o.screenRecover = func(attempt int) { attempts = append(attempts, attempt) }

	_, _, _ = o.screenSize(context.Background())
	if len(attempts) != 2 || attempts[0] != 1 || attempts[1] != 2 {
		t.Fatalf("want recovery at attempts [1 2], got %v", attempts)
	}
}

// Guards against accidentally making the retry loop unbounded.
func TestScreenSizeRetryBoundedAndFast(t *testing.T) {
	r := &fakeRunner{shellErr: errors.New("ADB: closed"), capErr: errors.New("ADB: closed")}
	o := newOrchestratorWithRunner(r)
	o.screenRecover = func(int) {}

	start := time.Now()
	_, _, _ = o.screenSize(context.Background())
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("screen.size retry took %v; retry ladder must not sleep", elapsed)
	}
}
