// Command wmresize changes a running device's display geometry.
//
// Why this exists: the host `adb` binary cannot drive `wm` on BlueStacks — the
// wedged WindowManager returns "error: closed" for shell commands while
// SurfaceFlinger keeps serving frames, which is the failure
// internal/adb.shellWedgeFallback works around (see cmd/wedge_probe). Anything
// that switches resolution at runtime therefore has to go through this repo's
// own ADB transport, and this is that tool.
//
// It is a development instrument: it changes the emulator's display without
// touching BlueStacks' saved preferences, so a cold boot reverts to whatever
// the multi-instance manager has configured. Pair it with
// docs/RESOLUTION.md — a new geometry also needs a measured display scale for
// the classifier to hold up.
//
// Usage:
//
//	go run ./cmd/wmresize -w 1280 -h 720          # override the geometry
//	go run ./cmd/wmresize -dpi 320                # density only
//	go run ./cmd/wmresize -reset                  # back to the physical size
//	go run ./cmd/wmresize                         # report the current geometry
package main

import (
	"encoding/binary"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/Ducky705/ClashGO/internal/adb"
)

func main() {
	device := flag.String("device", "localhost:5555", "ADB device ID")
	width := flag.Int("w", 0, "display width (0 = leave unchanged)")
	height := flag.Int("h", 0, "display height (0 = leave unchanged)")
	dpi := flag.Int("dpi", 0, "display density (0 = leave unchanged)")
	reset := flag.Bool("reset", false, "reset the size and density overrides")
	shellCmd := flag.String("shell", "", "run this one shell command through the working transport, then report and exit")
	settle := flag.Duration("settle", 2*time.Second, "wait after changing the geometry before reporting")
	flag.Parse()

	if (*width == 0) != (*height == 0) {
		fmt.Fprintln(os.Stderr, "-w and -h must be given together")
		os.Exit(2)
	}

	client := adb.NewClient(
		adb.WithHost("127.0.0.1"),
		adb.WithPort(5037),
		adb.WithTimeout(30*time.Second),
	)
	client.DeviceID = *device
	defer client.Close()

	if *shellCmd != "" {
		if err := run(client, *shellCmd); err != nil {
			fail(err)
		}
		time.Sleep(*settle)
		report(client, "after")
		return
	}

	fmt.Printf("device %s\n", *device)
	report(client, "before")

	switch {
	case *reset:
		if err := run(client, "wm size reset"); err != nil {
			fail(err)
		}
		if err := run(client, "wm density reset"); err != nil {
			fail(err)
		}
	case *width > 0:
		if err := run(client, fmt.Sprintf("wm size %dx%d", *width, *height)); err != nil {
			fail(err)
		}
		if *dpi > 0 {
			if err := run(client, fmt.Sprintf("wm density %d", *dpi)); err != nil {
				fail(err)
			}
		}
	case *dpi > 0:
		if err := run(client, fmt.Sprintf("wm density %d", *dpi)); err != nil {
			fail(err)
		}
	default:
		// Report only.
	}

	// The game recreates its surface a beat after the geometry changes, so the
	// screencap header is only authoritative once it has settled.
	time.Sleep(*settle)
	report(client, "after")
}

// run executes one shell command, preferring the persistent pipe (which is the
// path the bot itself uses) and falling back to the one-shot transport.
func run(c *adb.Client, cmd string) error {
	out, err := c.Shell(cmd)
	if err != nil {
		return fmt.Errorf("shell %q: %w", cmd, err)
	}
	if s := strings.TrimSpace(out); s != "" {
		fmt.Printf("  $ %s\n    %s\n", cmd, strings.ReplaceAll(s, "\n", "\n    "))
	}
	return nil
}

// report prints what the device believes its geometry is, both from the
// WindowManager and from the authoritative framebuffer header.
func report(c *adb.Client, label string) {
	fmt.Printf("%s:\n", label)

	if out, err := c.Shell("wm size"); err == nil {
		fmt.Printf("  wm size:    %s\n", strings.TrimSpace(out))
	} else {
		fmt.Printf("  wm size:    unavailable (%v)\n", err)
	}
	if out, err := c.Shell("wm density"); err == nil {
		fmt.Printf("  wm density: %s\n", strings.TrimSpace(out))
	} else {
		fmt.Printf("  wm density: unavailable (%v)\n", err)
	}

	w, h, err := screenSizeFromCapture(c)
	if err != nil {
		fmt.Printf("  screencap:  unavailable (%v)\n", err)
		return
	}
	fmt.Printf("  screencap:  %dx%d\n", w, h)
}

func screenSizeFromCapture(c *adb.Client) (int, int, error) {
	buf, err := c.CaptureScreen()
	if err != nil {
		return 0, 0, err
	}
	if len(buf) < 12 {
		return 0, 0, fmt.Errorf("short screencap header (%d bytes)", len(buf))
	}
	w := binary.LittleEndian.Uint32(buf[0:4])
	h := binary.LittleEndian.Uint32(buf[4:8])
	return int(w), int(h), nil
}

func fail(err error) {
	fmt.Fprintf(os.Stderr, "%v\n", err)
	os.Exit(1)
}
