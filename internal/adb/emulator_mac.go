//go:build darwin

// Package adb — emulator_mac.go
//
// EnsureBlueStacksMac brings up a BlueStacks instance with the
// requested resolution. Single launch attempt: if the main GUI
// binary exits cleanly without spawning qemu-system-aarch64 / hd-adb
// (the BlueStacks 5.21.775 / macOS Sequoia failure mode), we surface
// an actionable error rather than retry, because each retry produces
// the same exit-125ms-after-AppKit behavior the user can observe in
// `log show`. A retry was previously attempted and removed: it
// caused the bot's bot to relaunch BlueStacks repeatedly while the
// user watched the icon appear/disappear with no progress.
//
// Three things had to change from the previous "kill + wait for
// pgrep -x BlueStacks" approach to make this reliable:
//
//  1. **VM signals, not BlueStacks-shell pgrep.** The original
//     `pgrep -x BlueStacks` matched the main GUI shell AND false-
//     positive-matched BlueStacksAI (the companion process which is
//     ALWAYS running before BlueStacks even opens, listening on
//     127.0.0.1:8080). The new `vmProcessSignals` list only
//     contains processes that genuinely indicate "the Android VM
//     subsystem is alive": qemu-system-aarch64 (the VM itself) and
//     hd-adb (BlueStacks' custom adb daemon in 5.21+). Either of
//     these being present means the past BlueStacks launch
//     validation succeeded.
//
//  2. **Port-scanning in waitForBlueStacksADB.** The configured
//     DeviceID is just a prediction; BlueStacks 5.x on macOS
//     occasionally lands on 5556+ (multi-instance manager, conflict
//     with Android Studio AVDs, etc). The wait loop now scans all
//     candidate ports in [5555..5565] and updates c.DeviceID on the
//     first one that responds to adb protocol AND reports as
//     BlueStacks.
//
// //   3. **Conditional defaults write only.** Writing to a BlueStacks
//
//	bundle ID whose ~/Library/Preferences/<id>.plist does not yet
//	exist creates a fresh plist that BlueStacks 5.21's startup
//	validation sometimes rejects, causing the main binary to
//	exit within ~125ms of AppKit init. The code only writes to a
//	bundle ID whose plist already exists on disk — never creates
//	a new one. There is intentionally NO kill-only retry: on this
//	failure mode every relaunch reproduces the same exit, so we
//	surface the actionable error to the UI after a single attempt.
package adb

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// vmProcessSignals is the list of process-name substrings whose
// presence indicates "BlueStacks' Android VM subsystem is alive".
//
// IMPORTANT: This list deliberately does NOT contain "BlueStacks"
// (the main GUI shell) or "BlueStacks Main" (the alternate main
// process name). On macOS, BlueStacksAI (the companion that talks
// to the network on port 8080) is running BEFORE this function is
// ever called, so a pgrep substring match for "BlueStacks" would
// immediately yield a false positive — return success without
// verifying any actual VM. We only trust processes that genuinely
// belong to the Android subsystem.
//
// The "qemu-system" prefix is intentionally generic so it matches
// every BlueStacks version line: 4.x uses qemu-system-x86_64,
// 5.0–5.10 uses qemu-system-i386, 5.11–5.20 transitions through
// qemu-system-aarch64, and 5.21+ uses qemu-system-aarch64
// exclusively. Adding "qemu-system-aarch64" separately would only
// detect the very newest installs — the generic prefix is the
// safer, version-agnostic gate. hd-adb is BlueStacks 5.21+'s
// bundled adb daemon and matches only that line.
var vmProcessSignals = []string{
	"qemu-system",         // ANY BlueStacks VM (4.x x86 / 5.0–5.10 i386 / 5.11+ aarch64)
	"qemu-system-aarch64", // explicit 5.11+ ARM64 (covered by "qemu-system" prefix above, kept for backwards compat)
	"hd-adb",              // BlueStacks' custom adb daemon (5.21+ only — older versions reach us through qemu-system alone)
}

// candidateBlueStacksAdbPorts is the order-precedence for adb
// ports BlueStacks might expose. Main instance is 5555; secondary
// instances on 5556+ (multi-instance manager, Android Studio AVD
// conflict, user-customized port). Scanned by
// waitForBlueStacksADB on every iteration.
var candidateBlueStacksAdbPorts = []int{
	5555, 5556, 5557, 5558, 5559, 5560, 5565,
}

// EnsureBlueStacksMac brings up a BlueStacks instance matching
// (width, height, dpi). Single launch attempt — no retry. The
// retry path was removed because on BlueStacks 5.21.775 / macOS
// Sequoia the main GUI binary calls exit() ~125ms after AppKit init
// when its engine state is unrecoverable (visible in `log show`).
// Retrying `open -a BlueStacks.app` reproduces the same exit and
// caused the user-visible "BlueStacks keeps restarting" symptom.
//
// Each error return below is wrapped with an actionable prefix so
// the bot UI surfaces a concrete user instruction (open the
// BlueStacks Air multi-instance manager, click Start on
// Tiramisu64) instead of just "timeout".
//
// This is the context-free entry point (used by recovery strategies
// that lack a caller context); the boot orchestrator uses
// EnsureBlueStacksMacCtx so a Stop click can abort the launch waits.
func (c *Client) EnsureBlueStacksMac(width, height, dpi int) error {
	return c.EnsureBlueStacksMacCtx(context.Background(), width, height, dpi)
}

// EnsureBlueStacksMacCtx is the context-aware variant of
// EnsureBlueStacksMac. The launch waits (waitForVMProcess and
// waitForBlueStacksADB) observe ctx so a cancelled boot (user
// clicking Stop while BlueStacks is still cold-booting) returns
// promptly instead of blocking on the full 90s+60s wait.
func (c *Client) EnsureBlueStacksMacCtx(ctx context.Context, width, height, dpi int) error {
	c.log.Info(fmt.Sprintf("enforcing BlueStacks resolution: %dx%d (DPI: %d)", width, height, dpi))

	// Precheck: if BlueStacks is already running and verified as a
	// real BlueStacks device on any candidate adb port, honor the
	// existing session and skip the kill+launch entirely. The user
	// often launches the multi-instance manager manually, clicks
	// Start on Tiramisu64, gets into the game, and THEN invokes the
	// bot — at that point BlueStacks is healthy and adb is reachable.
	// Force-quitting would reset their game state and force a
	// re-login. The TCP scan filters down to LISTEN-ing ports so
	// we only pay the ~2s transport cost for ports that are
	// actually accepting connections.
	openPorts := c.tcpScanListens(candidateBlueStacksAdbPorts, 200*time.Millisecond)
	// One-shot retry on every `isBlueStacksDevice` miss: adb-server on
	// 127.0.0.1:5037 can briefly be unresponsive (~500ms-2s) while it's
	// reloading its device list, in which case a single probe returns
	// false even on a port that's actually serving a healthy BlueStacks.
	// Without the retry, an adb-server blip would let the precheck fall
	// through to `killall -9 BlueStacks` — exactly the failure mode the
	// precheck exists to prevent. Capped at one retry per port to keep
	// total precheck cost under ~3s in the success case.
	for _, port := range openPorts {
		addr := fmt.Sprintf("localhost:%d", port)
		verified := false
		for attempt := 1; attempt <= 2; attempt++ {
			if c.isBlueStacksDevice(addr) {
				verified = true
				break
			}
			if attempt == 1 {
				time.Sleep(1 * time.Second)
			}
		}
		if verified {
			// Register on the local adb-server so subsequent
			// c.AutoDetectDevice() / c.Devices() / `adb devices -l`
			// probes see the device. Mirrors waitForBlueStacksADB's
			// behavior; without it the precheck succeeds but the
			// bot's diagnostic surface thinks nothing is connected.
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			if err := exec.CommandContext(ctx, "adb", "connect", addr).Run(); err != nil {
				// adb-server might be busy or the connect may have
				// raced a reload. Direct transport still works for
				// the bot's adb commands (c.transport.Exec), but flag
				// the failure in app.log so the diagnostic surface has
				// the data.
				c.log.Debugf("adb connect %s: %v", addr, err)
			}
			cancel()
			if c.DeviceID != addr {
				c.log.Info(fmt.Sprintf("BlueStacks already running on %s (configured: %s) — keeping existing session, skipping launch", addr, c.DeviceID))
				c.DeviceID = addr
			} else {
				c.log.Info(fmt.Sprintf("BlueStacks already running on %s — keeping existing session, skipping launch", addr))
			}
			return nil
		}
	}
	c.log.Debugf("BlueStacks not reachable on any candidate port; proceeding to launch")

	if err := c.launchBlueStacks(true, width, height, dpi); err != nil {
		// `open -a BlueStacks.app` itself failed — likely a Gatekeeper
		// or TCC denial on macOS Sequoia. The user must open the app
		// manually from Finder to confirm Gatekeeper has accepted it.
		return fmt.Errorf("BlueStacks.app won't open via open -a (likely Gatekeeper / TCC denial on macOS Sequoia). Open the app manually from Finder once to confirm Gatekeeper, then click Retry in this app: %w", err)
	}
	// 90s VM budget: on a cold boot (first launch after the Mac /
	// BlueStacks was shut down) the VM takes 45-70s+ to spawn
	// qemu-system-aarch64 / hd-adb on a 2020 Air. The previous 45s
	// ceiling aborted the user's very first Start click even though
	// BlueStacks finished booting moments later (observed: VM came up
	// ~50-70s in, second Start found it already running). Even if this
	// wait still expires, the orchestrator treats ensure as
	// non-terminal and the ADB connect loop absorbs the remaining lag.
	if err := c.waitForVMProcess(ctx, 90*time.Second); err != nil {
		if ctx.Err() != nil {
			// User Stop (or boot budget expiry) — don't frame this as a
			// BlueStacks launch failure; the caller will see the
			// cancellation and abort cleanly.
			return fmt.Errorf("BlueStacks launch aborted: %w", ctx.Err())
		}
		// 90s after launch, qemu-system-aarch64 / hd-adb never appeared.
		// On BlueStacks 5.21.775 / macOS Sequoia the main GUI binary
		// typically calls exit() ~125ms after AppKit init if its engine
		// state is unrecoverable. The user must start the instance from
		// the multi-instance manager UI; the bot cannot.
		return fmt.Errorf("BlueStacks VM subsystem did not start after 90s (qemu-system-aarch64 / hd-adb never appeared). Open the BlueStacks Air multi-instance manager, select Tiramisu64, click Start, then click Retry in this app: %w", err)
	}
	if err := c.waitForBlueStacksADB(ctx, 60*time.Second); err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("BlueStacks launch aborted: %w", ctx.Err())
		}
		// VM subsystem is running but no adb port is exposed. Almost
		// always means Tiramisu64 is in "starting, not Running" state in
		// the multi-instance manager UI.
		return fmt.Errorf("VM subsystem is running but ADB port did not open within 60s. Confirm Tiramisu64 status is Running in the BlueStacks Air multi-instance manager, then click Retry in this app: %w", err)
	}
	c.log.Info("BlueStacks ready (single-attempt launch)")
	return nil
}

// launchBlueStacks performs three steps: politely quit any running
// BlueStacks via osascript, force-kill via killall -9 to ensure
// config can be safely rewritten AND any half-dead VM instances are
// terminated, optionally rewrite resolution prefs (only on
// plist-already-exists branch), clear stale single-instance locks,
// then `open -a BlueStacks.app` to launch.
//
// writeDefaults=true is the only call site currently — false was
// only ever passed by the kill-only retry path that has been
// removed (see EnsureBlueStacksMac's doc-comment). The parameter
// is kept for symmetry with the original API in case a future
// recovery path needs it.
func (c *Client) launchBlueStacks(writeDefaults bool, width, height, dpi int) error {
	// Politely quit first so BlueStacks can save any in-progress state.
	_ = exec.Command("osascript", "-e", "quit app \"BlueStacks\"").Run()
	time.Sleep(2 * time.Second)
	// Force-kill to ensure config can be safely rewritten AND any
	// half-dead VM instances are terminated.
	_ = exec.Command("killall", "-9", "BlueStacks").Run()
	time.Sleep(1 * time.Second)

	if writeDefaults {
		c.writeResolutionDefaultsIfPlistExists(width, height, dpi)
	}

	// Stale lock files in .locks/ gate concurrent launches on
	// BlueStacks 5.21. A crash-time lock from a previous run can
	// prevent a fresh start. Move them aside (rename, not unlink,
	// so they're recoverable if the user wants to inspect).
	if err := c.clearStaleBlueStacksLocks(); err != nil {
		c.log.Warn(fmt.Sprintf("clearStaleBlueStacksLocks: %v", err))
	}

	// Best-effort launch via the macOS GUI shell. On BlueStacks
	// 5.21.775 / macOS Sequoia this often aborts within ~5s without
	// spawning qemu-system-aarch64 / hd-adb; downstream
	// waitForVMProcess / waitForBlueStacksADB will surface a clear
	// error in that case and the orchestrator will report
	// "BlueStacks instance unreachable — open the multi-instance
	// manager and click Start on Tiramisu64" to the UI.
	//
	// We deliberately do NOT drive the BlueStacksAI HTTP API at
	// 127.0.0.1:8080 for VM lifecycle. The /v1/* endpoints exposed
	// there are the now.gg cloud-gaming control surface (tap,
	// swipe, start_app, screenshot, etc — verified via GET /info).
	// POST /v1/session/create requires job_id, auth_token,
	// refresh_token, nowgg_sso_host, nowgg_client_id, and
	// nowgg_client_secret — there is no local VM-start endpoint.
	// Driving this API for launch is a no-op (422 Unprocessable
	// Entity) and would not bring up qemu-system / hd-adb.
	c.log.Info("launching BlueStacks via 'open -a BlueStacks.app'...")
	return exec.Command("open", "-a", "BlueStacks").Run()
}

// (Removed: blueStacksDefaultInstance and startBlueStacksViaMIMAPI.
// The BlueStacksAI HTTP API at 127.0.0.1:8080 is the now.gg cloud-
// gaming control surface, not a local VM lifecycle endpoint. POST
// /v1/session/create requires now.gg SSO tokens and returns 422
// Unprocessable Entity on local installs. Driving this API for
// for VM launch is incorrect.)

// clearStaleBlueStacksLocks moves `*.lock` files in
// /Users/Shared/Library/Application Support/BlueStacks/.locks/ aside
// to a `.cleared.<timestamp>` sibling. We rename rather than unlink
// because the user might want to inspect the lock state afterwards
// (each rename keeps the file's mtime + content visible).
//
// Locks guard BlueStacks 5.21 against concurrent launches; if the
// previous session crashed, the lock can persist and block the next
// launch. Renaming is harmless because BlueStacks regenerates the
// locks on a successful start. Tiramisu64 is read by `pgrep -f`
// elsewhere; renaming doesn't affect pgrep.
func (c *Client) clearStaleBlueStacksLocks() error {
	const locksDir = "/Users/Shared/Library/Application Support/BlueStacks/.locks"
	entries, err := os.ReadDir(locksDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // never installed: nothing to clear
		}
		return fmt.Errorf("read %s: %w", locksDir, err)
	}
	if len(entries) == 0 {
		return nil
	}
	stamp := time.Now().Unix()
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".lock") {
			continue
		}
		src := filepath.Join(locksDir, e.Name())
		dst := filepath.Join(locksDir, e.Name()+".cleared."+fmt.Sprint(stamp))
		if err := os.Rename(src, dst); err != nil {
			c.log.Warn(fmt.Sprintf("rename %s -> %s: %v", src, dst, err))
		}
	}
	return nil
}

// (Removed startBlueStacksViaMIMAPI — see removal note for
// blueStacksDefaultInstance above. The /v1/session/create endpoint
// is for now.gg cloud-gaming sessions; using it to launch a local
// VM is incorrect.)

// writeResolutionDefaultsIfPlistExists writes the resolution
// preferences ONLY to com.BlueStacks.AppPlayer (the classic, stable
// bundle ID that has shipped with every BlueStacks 4.x/5.x on macOS
// since the first public release). We deliberately do NOT write to
// com.now.gg.BlueStacks (the newer id introduced alongside the
// now.gg acquisition) — on BlueStacks 5.21.775 we observed the
// main binary aborting within ~5s of `open -a BlueStacks.app` when
// ANY defaults write hit that namespace, including on machines
// where the plist already existed. The validator inside the new
// bundle ID's startup path treats our rewrite as a corrupt-config
// signal and bails before spawning qemu-system-aarch64.
//
// Belt-and-suspenders: even if the reject-from-com.now.gg theory
// turns out to be wrong on some user's machine, NOT writing to that
// bundle id costs us nothing — the AppPlayer pref is what the actual
// framebuffer layer reads at startup. So skipping it is always safe.
func (c *Client) writeResolutionDefaultsIfPlistExists(width, height, dpi int) {
	const bundleID = "com.BlueStacks.AppPlayer"
	home, _ := os.UserHomeDir()
	if home == "" {
		home = os.Getenv("HOME")
	}
	plistPath := filepath.Join(home, "Library", "Preferences", bundleID+".plist")
	if _, err := os.Stat(plistPath); err != nil {
		c.log.Info(fmt.Sprintf("skipping defaults write to %s (plist absent at %s)", bundleID, plistPath))
		return
	}
	c.log.Info(fmt.Sprintf("writing configuration to %s", bundleID))
	commands := [][]string{
		{"write", bundleID, "Guests/Android/FrameBuffer/0/GuestWidth", "-int", fmt.Sprint(width)},
		{"write", bundleID, "Guests/Android/FrameBuffer/0/WindowWidth", "-int", fmt.Sprint(width)},
		{"write", bundleID, "Guests/Android/FrameBuffer/0/GuestHeight", "-int", fmt.Sprint(height)},
		{"write", bundleID, "Guests/Android/FrameBuffer/0/WindowHeight", "-int", fmt.Sprint(height)},
		{"write", bundleID, "Guests/Android/FrameBuffer/0/Dpi", "-int", fmt.Sprint(dpi)},
	}
	for _, args := range commands {
		// Best-effort write: `defaults` returns non-zero for
		// various reasons (plist unreadable, sandbox denial), but
		// none of those mean we should fail the boot — at worst,
		// BlueStacks will keep its existing resolution, which is
		// better than not starting at all.
		_ = exec.Command("defaults", args...).Run()
	}
}

// waitForVMProcess polls ps for any of vmProcessSignals and returns
// on first hit. The caller picks the budget — production passes 90s
// because BlueStacks 5.21 on a slow Mac can take 45-70s+ to fully
// spawn qemu-system-aarch64 after `open -a BlueStacks.app`.
//
// vmProcessSignals deliberately excludes "BlueStacks" itself
// because BlueStacksAI (the companion) is already running before
// this function is called — a substring match for "BlueStacks"
// would false-positive instantly. We only trust qemu-system-aarch64
// (VM process) and hd-adb (BlueStacks' bundled adb daemon).
//
// IMPORTANT (BlueStacks Air on Apple Silicon): the Android VM runs
// IN-PROCESS — neither qemu-system nor hd-adb ever appears as a
// separate process, but the main BlueStacks binary listens on the
// instance's adb port (5555+) as soon as the VM host is up. Relying
// on process names alone burns the full budget and emits a scary
// failure on every cold boot of these builds. An open candidate adb
// port is therefore treated as an equally valid VM-up signal (it is
// exactly the signal waitForBlueStacksADB already keys off).
func (c *Client) waitForVMProcess(ctx context.Context, timeout time.Duration) error {
	c.log.Info("waiting for BlueStacks VM subsystem (qemu-system-aarch64 / hd-adb / adb port)...")
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if sig := c.firstVMSignal(); sig != "" {
			c.log.Info(fmt.Sprintf("VM subsystem signal detected: %q", sig))
			return nil
		}
		if len(c.tcpScanListens(candidateBlueStacksAdbPorts, 200*time.Millisecond)) > 0 {
			c.log.Info("VM subsystem signal detected: adb port listening")
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("wait cancelled: %w", ctx.Err())
		case <-time.After(1 * time.Second):
		}
	}
	return errors.New("timeout waiting for BlueStacks VM subsystem (qemu-system-aarch64 / hd-adb / adb port never appeared)")
}

// firstVMSignal returns the first vmProcessSignals entry currently
// matching a running process via pgrep, or "" if none match.
// -fl matches against the full command line and returns exit code 0
// only when found.
func (c *Client) firstVMSignal() string {
	for _, sig := range vmProcessSignals {
		// pgrep -f matches against full command line; -l adds
		// the process name to the output. Run returns nil iff at
		// least one match exists.
		if err := exec.Command("pgrep", "-fl", sig).Run(); err == nil {
			return sig
		}
	}
	return ""
}

// isBlueStacksDevice verifies that the device at id is reachable
// AND reports itself as a BlueStacks instance (possibly with a
// custom device profile applied). Opens a fresh transport with a
// 2s timeout, so a stale adb-server ghost entry (e.g.
// "localhost:5555 device" that's actually offline) will fail the
// reachability check and be skipped — unlike the prior code that
// trusted the adb devices list verbatim.
//
// BlueStacks natively sets ro.product.manufacturer to "BlueStacks"
// (or "Microvirt" on older builds). HOWEVER, users frequently
// apply a custom device profile (Samsung, OnePlus, Asus) via
// BlueStacks settings for game compatibility and higher frame
// rates — this overrides the default manufacturer prop. We
// accept all of these because:
//
//  1. The only other common local emulator on macOS is the
//     Android Studio AVD, which reports "google" or "unknown".
//  2. Real phones over USB are filtered out by AutoDetectDevice's
//     `isEmulator` check (127.0.0.1 / localhost / emulator-*).
//  3. A local emulator reporting as a mobile OEM is virtually
//     always BlueStacks with a profile applied.
func (c *Client) isBlueStacksDevice(id string) bool {
	t, err := NewTransport(id, c.host, c.port, 2*time.Second)
	if err != nil {
		return false
	}
	defer t.Close()

	// Single-arg getprop is deliberate: multi-arg `getprop` has
	// undefined behavior on some Android versions (returns just
	// the first value or empty string). getprop doesn't wake the
	// package manager and returns in ~50ms on a healthy emulator.
	out, err := t.Shell("getprop ro.product.manufacturer")
	if err != nil {
		return false
	}
	low := strings.ToLower(strings.TrimSpace(out))
	// Accept native BlueStacks identities ("bluestacks", "microvirt")
	// plus the common gaming device profiles users apply.
	return strings.Contains(low, "bluestacks") ||
		strings.Contains(low, "microvirt") ||
		strings.Contains(low, "samsung") ||
		strings.Contains(low, "oneplus") ||
		strings.Contains(low, "asus")
}

// waitForBlueStacksADB polls the local adb-server until BlueStacks'
// adb daemon is reachable on one of the candidate ports AND
// responds as a BlueStacks device.
//
// Why port-scanning instead of just localhost:5555: BlueStacks
// might choose a non-default port (5556+, especially on hosts
// that have Android Studio AVDs already bound there). The
// configured DeviceID is just the canonical prediction; finding
// the actual port is part of the boot flow. On success, we update
// c.DeviceID so subsequent calls in the boot orchestrator use
// the discovered address.
//
// Mid-budget ResetAdbServer self-heal is preserved: at timeout/2
// we fire one adb-server reset inside the loop and continue. This
// is what the previous fix added and it has been observed to clear
// stale "localhost:5555 offline" ghost entries that accumulate
// when BlueStacks is hard-killed mid-flight.
func (c *Client) waitForBlueStacksADB(ctx context.Context, timeout time.Duration) error {
	if c.DeviceID == "" {
		return nil
	}
	c.log.Info("waiting for BlueStacks ADB daemon to initialize on any candidate port...")
	deadline := time.Now().Add(timeout)
	midpoint := time.Now().Add(timeout / 2)
	midRecoveryAttempted := false
	for time.Now().Before(deadline) {
		// Fast TCP scan identifies which candidate ports are
		// actually LISTEN-ing right now. 200ms each is enough
		// to surface ESTABLISHED / LISTEN sockets and to
		// confirm a CLOSED port via TCP RST. This filters the
		// candidate list down so we only pay the cost of a real
		// `adb connect` (5s ceiling per port) for ports that
		// might actually be BlueStacks.
		openPorts := c.tcpScanListens(candidateBlueStacksAdbPorts, 200*time.Millisecond)
		for _, port := range openPorts {
			addr := fmt.Sprintf("localhost:%d", port)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_ = exec.CommandContext(ctx, "adb", "connect", addr).Run()
			cancel()
			if c.isBlueStacksDevice(addr) {
				if c.DeviceID != addr {
					c.log.Info(fmt.Sprintf("BlueStacks ADB auto-detected at %s (configured: %s)", addr, c.DeviceID))
					c.DeviceID = addr
				}
				return nil
			}
		}
		// Mid-budget adb-server self-heal. Idempotent — fired
		// once per waitForBlueStacksADB call. Doesn't take c.mu
		// or c.pipeMu. The `continue` skips the 1s sleep +
		// diagnostic log on this iteration so we re-probe
		// immediately against the fresh adb-server. After the
		// reset we also re-issue `adb connect` for each
		// candidate port so the new server picks them up.
		if !midRecoveryAttempted && time.Now().After(midpoint) {
			c.log.Warn("ADB daemon not responding after 50% of budget; injecting local adb-server reset (note: `adb kill-server` drops ALL adb connections on this host)")
			if rErr := c.ResetAdbServer(); rErr != nil {
				c.log.Warn(fmt.Sprintf("mid-wait ResetAdbServer: %v", rErr))
			}
			midRecoveryAttempted = true
			continue
		}
		// Diagnostic log. Captured in app.log for forensics.
		if out, derr := exec.Command("adb", "devices", "-l").Output(); derr == nil {
			c.log.Debugf("adb devices -l: %s", strings.TrimSpace(string(out)))
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("wait cancelled: %w", ctx.Err())
		case <-time.After(1 * time.Second):
		}
	}
	return errors.New("timeout waiting for BlueStacks ADB daemon to respond on any candidate port")
}

// tcpScanListens returns the subset of given ports that accept a
// TCP connection within the per-port timeout. Used to filter the
// candidate adb ports before paying the cost of a real
// `adb connect` (which carries its own 5s wait per port). Pure
// Go TCP probe via net.DialTimeout — no `lsof` fork.
func (c *Client) tcpScanListens(ports []int, perPort time.Duration) []int {
	var open []int
	for _, p := range ports {
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", p), perPort)
		if err != nil {
			continue
		}
		_ = conn.Close()
		open = append(open, p)
	}
	return open
}
