// Package bot — recovery.go
//
// RecoveryPolicy is the layered escalation engine that decides what
// to try when a boot step fails. The whole point of replacing the
// hard-coded "kill BlueStacks" with named strategies is that the
// bot tries the cheapest fixes first and only escalates to
// destructive actions as a last resort.
//
// Strategy ladder (cheapest to most-destructive):
//
//  1. RetryTransport  — close + reopen the ADB transport socket.
//     Fast (~50ms). Handles the common case of
//     a stale connection from BlueStacks
//     restart.
//
//  2. SoftReset       — `adb shell stop && adb shell start`.
//     Restarts the Android runtime without
//     killing BlueStacks. ~5-10s. Handles
//     the "boot_completed never appeared"
//     case where Android is up but wedged.
//
//  3. RelaunchGame    — force-stop + restart CoC via monkey.
//     Doesn't touch BlueStacks or the Android
//     runtime. ~3-5s. Handles the
//     "PackageManager up but CoC crashed"
//     case.
//
//  4. RestartBlueStacks — osascript quit + open -a BlueStacks.
//     ~10-15s for the launch. The last
//     reasonable option before…
//
//  5. NuclearOption   — kill -9 BlueStacks + rewrite config
//     from scratch. ~20s. Only used when
//     EnsureBlueStacksMac itself failed
//     (config write rejected, etc).
//
// Each strategy has metadata: the action function, a "what went
// wrong" predicate that decides when to try it, and a budget. The
// orchestrator walks the ladder, applying backoff between attempts,
// and surfaces a "SuggestedAction" string for the UI.
package bot

import (
	"context"
	"fmt"
	"sort"
	"time"
)

// RecoveryStrategy is one named escalation step.
type RecoveryStrategy struct {
	Name string

	Apply func(ctx context.Context) error

	Cost time.Duration

	Destructive bool
}

// RecoveryConfig is the orchestration knobs the policy consults.
type RecoveryConfig struct {
	MaxAttempts int

	AllowNuclear bool

	InitialBackoff time.Duration
	MaxBackoff     time.Duration
}

// RecoveryPolicy wires the strategy ladder to the live Client. It is
// constructed once by the orchestrator and consulted at most once
// per attempt. Stateless after construction.
type RecoveryPolicy struct {
	cfg    RecoveryConfig
	client adbClient
}

// adbClient is the narrow subset of *adb.Client that the recovery
// actions need. Keeping the dependency small makes the policy
// trivially mockable in unit tests (pass a fake that implements the
// methods below).
type adbClient interface {
	Reconnect() error
	ResetAdbServer() error
	SoftResetAndroid() error
	ForceStop(pkg string) error
	StartApp(pkg string) error
	EnsureBlueStacksMac(w, h, dpi int) error
}

// NewRecoveryPolicy constructs a policy. The client is the live ADB
// client (or a fake in tests); the cfg carries the budgets.
func NewRecoveryPolicy(cfg RecoveryConfig, client adbClient) *RecoveryPolicy {
	return &RecoveryPolicy{cfg: cfg, client: client}
}

// Strategies returns the ordered ladder for the current config. The
// returned slice is a fresh copy — callers can mutate without
// affecting future ladders.
//
// Order rationale (cheapest to most-destructive, with the diagnostics-
// first principle applied):
//
//  1. RetryTransport   — close + reopen the ADB transport socket.
//     Fast (~50ms). Handles a stale connection
//     from a fresh BlueStacks start.
//  2. ResetAdbServer   — `adb kill-server` + `adb start-server`.
//     ~3s. Handles the "localhost:5555 listed
//     as offline in `adb devices` even though
//     the device is up" failure mode that hits
//     after a hard kill or a wails-dev session.
//     Non-destructive of the emulator state but
//     does drop ALL adb connections globally —
//     noted in the BootReport.
//  3. SoftReset        — `adb shell stop && adb shell start`.
//     Restarts the Android runtime without
//     killing BlueStacks. ~8s. Handles Android
//     wedged at the runtime layer.
//  4. RelaunchGame     — force-stop + restart CoC via monkey.
//     Doesn't touch BlueStacks or Android.
//     ~5s. Useless if ADB isn't yet talking
//     to a device, so in practice only
//     invoked from the boot-probe ladder.
//  5. RestartBlueStacks — osascript quit + open -a BlueStacks.
//     ~15s. The nuclear option, only used when
//     AllowNuclear is true and everything
//     above has failed.
func (p *RecoveryPolicy) Strategies(packageName string, w, h, dpi int) []RecoveryStrategy {
	strats := []RecoveryStrategy{
		{
			Name: "RetryTransport",
			Apply: func(ctx context.Context) error {
				return p.client.Reconnect()
			},
			Cost: 50 * time.Millisecond,
		},
		{
			Name: "ResetAdbServer",
			Apply: func(ctx context.Context) error {
				return p.client.ResetAdbServer()
			},
			Cost: 3 * time.Second,
		},
		{
			Name: "SoftReset",
			Apply: func(ctx context.Context) error {
				return p.client.SoftResetAndroid()
			},
			Cost: 8 * time.Second,
		},
		{
			Name: "RelaunchGame",
			Apply: func(ctx context.Context) error {
				if err := p.client.ForceStop(packageName); err != nil {
					return err
				}
				time.Sleep(2 * time.Second)
				return p.client.StartApp(packageName)
			},
			Cost: 5 * time.Second,
		},
	}
	if p.cfg.AllowNuclear {
		strats = append(strats,
			RecoveryStrategy{
				Name: "RestartBlueStacks",
				Apply: func(ctx context.Context) error {
					return p.client.EnsureBlueStacksMac(w, h, dpi)
				},
				Cost:        15 * time.Second,
				Destructive: true,
			},
		)
	}
	return strats
}

// Escalate decides which strategy (by index in Strategies()) to try
// next. failureContext tells the policy what step failed; the return
// value is the index into Strategies() to try, or -1 if the ladder
// is exhausted.
//
// The current implementation is a simple linear walk: it always
// tries the next strategy on each escalation. A future enhancement
// could skip strategies whose "predicate" doesn't match the failure
// (e.g. don't try RelaunchGame if the failure is in adb.connect
// itself).
func (p *RecoveryPolicy) Escalate(strats []RecoveryStrategy, attempt int) int {

	if attempt < 1 {
		return 0
	}
	if attempt > len(strats) {
		return -1
	}
	return attempt - 1
}

// Backoff returns the wait time before the next attempt. Exponential
// with a cap, starting from cfg.InitialBackoff. attempt is 1-based.
func (p *RecoveryPolicy) Backoff(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	d := p.cfg.InitialBackoff
	for i := 1; i < attempt; i++ {
		d *= 2
		if d >= p.cfg.MaxBackoff {
			return p.cfg.MaxBackoff
		}
	}
	return d
}

// SuggestedAction is the human-readable "what to try next" string
// surfaced in the BootReport and the React UI. It takes the most
// recent failed step + the last strategy attempted and produces a
// one-liner. Kept short (1 sentence) so it fits a UI card.
func SuggestedAction(lastStep, lastStrategy, lastErr string) string {
	if lastErr == "" {
		lastErr = "unknown"
	}

	hints := []struct {
		match  string
		action string
	}{
		{"ResetAdbServer", "local adb-server was reset to clear stale device registrations; if you have other adb tools connected, they will need to reconnect"},
		{"transport connect", "check that BlueStacks is running and ADB is enabled in BlueStacks settings"},
		{"no ADB devices", "open BlueStacks and confirm the instance is started (the multi-instance manager)"},
		{"wm size", "BlueStacks may still be initializing; wait 30s and try again"},
		{"boot_completed", "BlueStacks is taking unusually long to boot — close other heavy apps and try again"},
		{"bootanim", "BlueStacks boot animation is stuck; try Settings → Reset in BlueStacks"},
		{"screen", "screen is black/blank — confirm the BlueStacks window isn't minimized or covered"},
		{"pm list", "PackageManager is unresponsive — relaunch BlueStacks (the bot will do this automatically on the next retry)"},
		{"timeout", "the operation timed out — your Mac or BlueStacks may be under load; the bot will retry with a longer budget"},
		{"calibrate", "screen capture failed; check that the BlueStacks window is visible and not occluded"},
		{"StartApp", "could not start Clash of Clans — confirm it's installed: adb shell pm list packages | grep clashofclans"},
	}

	sort.SliceStable(hints, func(i, j int) bool {
		return len(hints[i].match) > len(hints[j].match)
	})
	for _, h := range hints {
		if containsFold(lastStep, h.match) || containsFold(lastErr, h.match) {
			return h.action
		}
	}

	switch lastStrategy {
	case "RetryTransport":
		return "could not reconnect to ADB; check that BlueStacks is still running"
	case "ResetAdbServer":
		return "local adb-server was reset to clear stale device registrations; if you have other adb tools connected, they will need to reconnect"
	case "SoftReset":
		return "Android soft reset did not help; BlueStacks may need to be restarted manually"
	case "RelaunchGame":
		return "could not restart Clash of Clans; try launching it manually to clear any crash"
	case "RestartBlueStacks":
		return "BlueStacks relaunch did not help — check ~/Library/Application Support/ClashGO/logs/app.log for details"
	case "NuclearOption":
		return "BlueStacks could not be reconfigured; check that you have permission to write to ~/Library/Preferences/com.BlueStacks.AppPlayer.plist"
	}
	return fmt.Sprintf("last error: %s", lastErr)
}

// containsFold is a tiny ASCII-case-insensitive substring test so
// the SuggestedAction heuristics work on "Boot_Completed" or
// "boot_completed" or "BOOT_COMPLETED" equally. We avoid strings.ToLower
// to keep the heap pressure zero (this is called once per failure).
func containsFold(haystack, needle string) bool {
	if len(needle) == 0 {
		return true
	}
	if len(needle) > len(haystack) {
		return false
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		match := true
		for j := 0; j < len(needle); j++ {
			h := haystack[i+j]
			n := needle[j]

			if h >= 'A' && h <= 'Z' {
				h += 'a' - 'A'
			}
			if n >= 'A' && n <= 'Z' {
				n += 'a' - 'A'
			}
			if h != n {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}
