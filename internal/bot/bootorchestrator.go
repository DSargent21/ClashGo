// Package bot — bootorchestrator.go
//
// BootOrchestrator is the single entry point that brings the bot
// from "user clicked Start" to "ready to capture frames". It
// replaces the inline boot sequence that used to live in
// NewBot() in bot.go, and turns the multi-step hand-rolled
// procedure into a state machine with named steps, layered recovery,
// structured reporting, and learned timing.
//
// The orchestrator does NOT own the capture loop, the classifier,
// the attack executor, or any of the runtime subsystems. It only
// owns the BOOT phase. NewBot() now reads:
//
//	bctx, err := NewBootOrchestrator(cfg, client).Boot(ctx)
//	if err != nil { ... }
//	// bctx.ScreenW/H, bctx.Client, bctx.BlueStacksRestarted are ready
//
// BootConfig carries the tunables. The defaults are production-safe;
// in dev mode NewBot shrinks them via WithDevFastFail so a failed
// boot surfaces in 25s instead of 180s+.
//
// Threading: the orchestrator is single-use. Construct one per
// NewBot call, call Boot() once, and discard. The Client it
// receives is mutated (its transport is connected, the pipe is
// not enabled) — that's the intended hand-off.
package bot

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/rs/zerolog"

	"github.com/Ducky705/ClashGO/internal/adb"
	"github.com/Ducky705/ClashGO/internal/config"
	"github.com/Ducky705/ClashGO/internal/paths"
)

// BootConfig is the per-call configuration for a single boot
// attempt. Constructed by NewBootConfigFromBotConfig; tune via the
// WithDevFastFail and WithAllowNuclear helpers.
type BootConfig struct {
	// ADB timeouts.
	AdbConnectTimeout time.Duration // overall budget for the transport connect loop
	AdbConnectPoll    time.Duration // gap between connect attempts
	AdbPerCallTimeout time.Duration // passed to transport.Exec (one Shell call)

	// Boot-probe timeouts (multi-signal).
	BootProbeTimeout    time.Duration // overall budget for the probe loop
	BootProbePoll       time.Duration // gap between probe passes
	BootProbeMinSignals int           // required # of ready signals (2 = 2-of-4)
	BootProbePerSignal  time.Duration // per-signal timeout inside one probe pass

	// Recovery knobs.
	MaxRecoveryAttempts    int
	AllowNuclear           bool
	InitialRecoveryBackoff time.Duration
	MaxRecoveryBackoff     time.Duration

	// Misc.
	WaitForGameSettle time.Duration // post-StartApp sleep
	PackageName       string
	DeviceID          string
	ExpectedWidth     int
	ExpectedHeight    int
	ExpectedDPI       int

	// DevFastFail is true when wails dev is running. Shrinks all
	// the timeouts above; the orchestrator does NOT touch this
	// field directly (NewBootConfigFromBotConfig reads the env).
	DevFastFail bool
}

// DefaultBootConfig returns the production defaults. 90s on the
// boot probe, 90s on the ADB connect loop, 2-of-4 signals, 5
// recovery attempts with a 0.5s..4s exponential backoff.
func DefaultBootConfig() BootConfig {
	return BootConfig{
		AdbConnectTimeout:      90 * time.Second,
		AdbConnectPoll:         3 * time.Second,
		AdbPerCallTimeout:      30 * time.Second,
		BootProbeTimeout:       90 * time.Second,
		BootProbePoll:          2 * time.Second,
		BootProbeMinSignals:    2,
		BootProbePerSignal:     5 * time.Second,
		MaxRecoveryAttempts:    5,
		AllowNuclear:           true,
		InitialRecoveryBackoff: 500 * time.Millisecond,
		MaxRecoveryBackoff:     4 * time.Second,
		WaitForGameSettle:      15 * time.Second,
		PackageName:            "com.supercell.clashofclans",
	}
}

// WithDevFastFail returns a copy of cfg with shrunken timeouts
// suitable for `wails dev`. 10s for the ADB loop, 15s for the boot
// probe, 500ms poll cadence, no nuclear option. A coding error in
// dev cycles through the failure modes in ~25s instead of 180s+.
func (c BootConfig) WithDevFastFail() BootConfig {
	c.AdbConnectTimeout = 10 * time.Second
	c.AdbConnectPoll = 1 * time.Second
	c.BootProbeTimeout = 15 * time.Second
	c.BootProbePoll = 500 * time.Millisecond
	c.BootProbePerSignal = 3 * time.Second
	c.MaxRecoveryAttempts = 3
	c.AllowNuclear = false
	c.InitialRecoveryBackoff = 200 * time.Millisecond
	c.MaxRecoveryBackoff = 1 * time.Second
	c.WaitForGameSettle = 5 * time.Second
	c.DevFastFail = true
	return c
}

// NewBootConfigFromBotConfig merges the BotConfig defaults with the
// orchestrator's own defaults. The DeviceID, PackageName, and
// resolution come from cfg.Device. The recovery policy reads cfg
// flags for future tunability (currently none — the orchestrator
// has its own opinion).
func NewBootConfigFromBotConfig(cfg *config.BotConfig) BootConfig {
	bc := DefaultBootConfig()
	if cfg != nil {
		bc.DeviceID = cfg.Device.DeviceID
		bc.PackageName = cfg.Device.PackageName
		bc.ExpectedWidth = cfg.Device.Width
		bc.ExpectedHeight = cfg.Device.Height
		bc.ExpectedDPI = cfg.Device.DPI
		if bc.PackageName == "" {
			bc.PackageName = "com.supercell.clashofclans"
		}
	}
	return bc
}

// BootContext is what the orchestrator returns on success. NewBot
// reads ScreenW/H to build the Calibration; everything else is
// passed through to the Bot struct for runtime use.
//
// Report is a value-copyable BootReportView (no mutex), safe to
// pass across goroutines, serialize, or emit via Wails.
type BootContext struct {
	Client              *adb.Client
	DeviceID            string
	PackageName         string
	ScreenW             int
	ScreenH             int
	ExpectedScreenW     int
	ExpectedScreenH     int
	BlueStacksRestarted bool
	Report              BootReportView
	BootDuration        time.Duration
	RecoveryUsed        []string
}

// BootOrchestrator is the live state of a single boot attempt. One
// per NewBot call. Use NewBootOrchestrator to construct.
type BootOrchestrator struct {
	cfg    BootConfig
	client *adb.Client
	runner adb.ShellRunner
	prober *adb.BootProber
	policy *RecoveryPolicy
	report *BootReport
	logger zerolog.Logger
}

// NewBootOrchestrator wires the orchestrator. The client is the
// already-constructed adb.Client from NewBot (no transport connect
// has happened yet — the orchestrator does that itself).
func NewBootOrchestrator(cfg BootConfig, client *adb.Client, logger zerolog.Logger) *BootOrchestrator {
	runner := adb.NewShellRunner(client)
	prober := adb.NewBootProber(runner, adb.BootProbeConfig{
		PerSignalTimeout: cfg.BootProbePerSignal,
		PollInterval:     cfg.BootProbePoll,
		MinReadySignals:  cfg.BootProbeMinSignals,
	}, cfg.PackageName)
	policy := NewRecoveryPolicy(RecoveryConfig{
		MaxAttempts:    cfg.MaxRecoveryAttempts,
		AllowNuclear:   cfg.AllowNuclear,
		InitialBackoff: cfg.InitialRecoveryBackoff,
		MaxBackoff:     cfg.MaxRecoveryBackoff,
	}, client)
	r := NewBootReport()
	r.SetDeviceContext(cfg.DeviceID, cfg.PackageName, cfg.ExpectedWidth, cfg.ExpectedHeight)
	buildType := "release"
	if cfg.DevFastFail {
		buildType = "dev"
	}
	r.SetMetadata(map[string]string{
		"build_type": buildType,
		"dev_mode":   strconv.FormatBool(cfg.DevFastFail),
	})
	return &BootOrchestrator{
		cfg:    cfg,
		client: client,
		runner: runner,
		prober: prober,
		policy: policy,
		report: r,
		logger: logger,
	}
}

// devFastFail reports whether the bot should use the shortened boot
// timeouts intended for wails dev. The orchestrator's WithDevFastFail
// shrinks the boot budget from ~180s to ~25s so a coding error cycles
// fast and the developer can iterate.
//
// Detected via env vars — Wails v2 does not set WAILS_DEV on the
// spawned GUI process, so a runtime check on the env is the only
// reliable signal. CLASHGO_DEV_FAST_FAIL=1 is the canonical knob; the
// legacy CLASHGO_DEV alias is honored for backward compatibility.
//
// Production builds leave both unset; the orchestrator falls back to
// the 90s defaults.
func devFastFail() bool {
	return os.Getenv("CLASHGO_DEV_FAST_FAIL") == "1" ||
		os.Getenv("CLASHGO_DEV") == "1"
}

// Boot runs the full sequence. The returned BootContext is non-nil
// iff err is nil. On failure the BootContext is nil; use
// orchestrator.Report().Snapshot() to get the structured report.
func (o *BootOrchestrator) Boot(ctx context.Context) (*BootContext, error) {
	// The boot has its own derived context so a caller-passed cancel
	// (the user clicking Stop) tears the whole sequence down. The
	// outer budget is the larger of AdbConnectTimeout +
	// BootProbeTimeout + WaitForGameSettle + recovery headroom.
	budget := o.cfg.AdbConnectTimeout + o.cfg.BootProbeTimeout +
		o.cfg.WaitForGameSettle + time.Duration(o.cfg.MaxRecoveryAttempts)*o.cfg.MaxRecoveryBackoff
	bctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()

	o.logger.Info().
		Str("device_id", o.cfg.DeviceID).
		Str("package", o.cfg.PackageName).
		Bool("dev_mode", o.cfg.DevFastFail).
		Dur("budget", budget).
		Msg("boot orchestrator starting")

	// Phase 1: macOS-only BlueStacks ensure. On non-darwin this is
	// a no-op. CRITICAL: this MUST run before the ADB connect loop.
	// EnsureBlueStacksMac force-kills the running BlueStacks process
	// when it's at the wrong resolution and relaunches it, which
	// invalidates any in-flight ADB transport. Doing it after the
	// connect loop forced the probe to fight a half-dead device for
	// 90s and emit the "boot probe failed" warnings seen in dev runs.
	//
	// Deliberately NON-terminal: on a cold boot the internal VM wait
	// can expire before qemu-system-aarch64 / hd-adb appear (a 2020
	// MacBook Air takes 45-70s+, the wait used to be 45s and the
	// FIRST Start click always failed while the SECOND one — with
	// BlueStacks already up — succeeded in under a second). We log
	// the failure and fall through to connectADB, which has its own
	// 90s budget + mid-budget adb-server reset to absorb the
	// remaining lag. The boot only fails if BOTH ensure AND the ADB
	// connect loop give up.
	var ensureErr error
	if o.cfg.ExpectedDPI > 0 { // heuristic: only call when DPI is configured
		start := time.Now()
		if err := o.client.EnsureBlueStacksMacCtx(bctx, o.cfg.ExpectedWidth, o.cfg.ExpectedHeight, o.cfg.ExpectedDPI); err != nil {
			ensureErr = err
			o.report.AppendStep("bluestacks.ensure", start, BootResultError, err.Error())
			o.logger.Warn().Err(err).Msg("BlueStacks ensure reported an error; continuing to ADB connect loop to absorb slow cold boot")
		} else {
			o.report.AppendStep("bluestacks.ensure", start, BootResultOK, fmt.Sprintf("ensured %dx%d@%d", o.cfg.ExpectedWidth, o.cfg.ExpectedHeight, o.cfg.ExpectedDPI))
			o.report.SetBlueStacksEnsured(true)
		}
	}

	// Phase 2: ADB connect loop. Polls Reconnect until success or
	// budget exhausted. Runs after BlueStacks is settled so the
	// transport we open is the one the rest of the boot will use.
	if err := o.connectADB(bctx); err != nil {
		o.report.Complete("failed", err, SuggestedAction("adb.connect", "", err.Error()))
		o.persist()
		if ensureErr != nil {
			// Surface the more actionable ensure error ("BlueStacks
			// VM never became reachable") alongside the connect
			// failure — the latter is often just a symptom.
			return nil, fmt.Errorf("bluestacks: %v; adb connect: %w", ensureErr, err)
		}
		return nil, fmt.Errorf("adb connect: %w", err)
	}

	// Phase 3: multi-signal boot probe. The recovery loop around
	// this phase is where most of the action happens — the probe
	// is the thing that was timing out before.
	probeResult, err := o.probeWithRecovery(bctx)
	if err != nil {
		o.report.Complete("failed", err, SuggestedAction("boot.probe", last(o.report), err.Error()))
		o.persist()
		return nil, fmt.Errorf("boot probe: %w", err)
	}
	_ = probeResult // currently only used for logging; the report's Steps already capture per-signal outcomes

	// Phase 4: get the screen size. The probe is ready, but we
	// still need a verified (w, h) before Calibrate. The screen
	// size doubles as a sanity check: if it doesn't match the
	// expected, we re-run EnsureBlueStacksMac and try again.
	screenW, screenH, err := o.screenSize(bctx)
	if err != nil {
		o.report.Complete("failed", err, SuggestedAction("screen.size", "", err.Error()))
		o.persist()
		return nil, fmt.Errorf("screen size: %w", err)
	}

	// Phase 5: optional game launch. Only if RestartOnStartup is
	// set in the bot config. We don't take the config here — the
	// orchestrator's job is "is the device ready", not "is the
	// game open". NewBot handles the game launch decision.
	// Skipped in this orchestrator on purpose.

	// Done. Compose the BootContext, mark the report ok, persist.
	view := o.report.Snapshot()
	o.report.Complete("ok", nil, "")
	// Refresh the view so Duration and CompletedAt are populated
	// for the logger line below and the returned BootContext.
	view = o.report.Snapshot()
	o.persist()

	o.logger.Info().
		Str("device_id", o.cfg.DeviceID).
		Int("screen_w", screenW).
		Int("screen_h", screenH).
		Int("steps", len(view.Steps)).
		Int("recovery", len(view.RecoveryUsed)).
		Dur("duration", view.Duration).
		Msg("boot orchestrator complete")

	return &BootContext{
		Client:              o.client,
		DeviceID:            o.cfg.DeviceID,
		PackageName:         o.cfg.PackageName,
		ScreenW:             screenW,
		ScreenH:             screenH,
		ExpectedScreenW:     o.cfg.ExpectedWidth,
		ExpectedScreenH:     o.cfg.ExpectedHeight,
		BlueStacksRestarted: containsStr(view.RecoveryUsed, "RestartBlueStacks"),
		Report:              view,
		BootDuration:        view.Duration,
		RecoveryUsed:        append([]string(nil), view.RecoveryUsed...),
	}, nil
}

// connectADB loops Reconnect() with a 3s poll until success or
// AdbConnectTimeout. The per-call timeout inside transport.Exec (30s
// default) bounds each individual attempt.
//
// Each iteration first calls AutoDetectDevice, which verifies that
// the configured DeviceID is actually a reachable BlueStacks
// emulator (or picks one from the adb device list). The verification
// error is captured and surfaced if the loop times out — without
// this, the user would see a generic "timeout waiting for ADB
// connection" even when the real problem is "no BlueStacks device
// found", which is the more actionable diagnosis.
//
// Mid-budget recovery: at the 1/3 mark of the total budget, if we
// still haven't connected, we inject a single ResetAdbServer. This
// is the common failure mode that hit the user's bot — the local
// adb-server's stale device list points at localhost:5555 as
// "offline" even though the BlueStacks process is up. Without this
// injection, the loop would burn the full 90s budget on retries
// against bad state. After the reset, we keep polling for the
// remaining 2/3 of the budget — typically enough for the blueStacks
// adb daemon to re-register with the fresh adb-server.
func (o *BootOrchestrator) connectADB(ctx context.Context) error {
	deadline := time.Now().Add(o.cfg.AdbConnectTimeout)
	start := time.Now()
	// Inject at 1/3 of the budget. Producers fail fast on the
	// first third if recovery was needed, leaving 2/3 of the
	// budget for the post-reset adb-server settle + the BlueStacks
	// daemon to register. With AdbConnectTimeout=90s, midpoint
	// fires at 30s; with dev-fast-fail=10s, midpoint fires at 3s
	midpoint := start.Add(o.cfg.AdbConnectTimeout / 3)
	var lastAutoErr, lastConnErr error
	midRecoveryAttempted := false
	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("context cancelled: %w", ctx.Err())
		default:
		}
		// AutoDetectDevice now actively rejects wrong devices (e.g.
		// Android Studio AVDs) and stale adb-server ghost entries.
		// We log the error at debug (it's expected while BlueStacks
		// is starting) and remember it for the final error report.
		if err := o.client.AutoDetectDevice(); err != nil {
			lastAutoErr = err
			o.logger.Debug().Err(err).Msg("auto-detect failed; will retry on next poll")
		} else {
			lastAutoErr = nil
		}
		if err := o.client.Reconnect(); err == nil {
			o.report.AppendStep("adb.connect", start, BootResultOK, "transport connected")
			return nil
		} else {
			lastConnErr = err
		}
		// Mid-budget recovery injection. We only fire once per
		// connectADB call (midRecoveryAttempted guards the
		// idempotent retry). The marker "midpoint" is 1/3 of
		// AdbConnectTimeout so the recovery always has two-thirds
		// of the budget left to settle the new adb-server +
		// re-register the device.
		if !midRecoveryAttempted && time.Now().After(midpoint) {
			o.logger.Warn().Msg("adb connect: 1/3 budget exhausted; injecting adb-server reset")
			o.report.MarkRecovery("ResetAdbServer")
			sStart := time.Now()
			if rErr := o.client.ResetAdbServer(); rErr != nil {
				o.report.AppendStep("recovery.ResetAdbServer", sStart, BootResultError, rErr.Error())
				o.logger.Warn().Err(rErr).Msg("mid-budget ResetAdbServer failed; continuing poll")
			} else {
				o.report.AppendStep("recovery.ResetAdbServer", sStart, BootResultOK, "adb-server reset, polling for device")
			}
			midRecoveryAttempted = true
		}
		if time.Now().After(deadline) {
			o.report.AppendStep("adb.connect", start, BootResultTimeout, "no transport within budget")
			// Prefer the verification error (more actionable: "no
			// BlueStacks device found") over the generic connection
			// timeout ("waited 90s for ADB"). They typically carry
			// the same root cause but the verification wording is
			// easier to act on.
			if lastAutoErr != nil {
				return fmt.Errorf("auto-detect: %w", lastAutoErr)
			}
			// If mid-budget ResetAdbServer fired, surface that in
			// the error so SuggestedAction can pick the right hint
			// for the UI ("local adb-server was reset").
			if midRecoveryAttempted {
				return fmt.Errorf("timeout after ResetAdbServer (mid-budget): %w", lastConnErr)
			}
			return fmt.Errorf("timeout waiting for ADB connection: %w", lastConnErr)
		}
		time.Sleep(o.cfg.AdbConnectPoll)
	}
}

// probeWithRecovery runs the multi-signal boot probe, applying the
// recovery ladder between attempts. Each attempt that fails tries
// the next strategy in the ladder; the function returns when the
// probe reports ready, the ladder is exhausted, or the context
// cancels.
//
// Returns the final ProbeResult on success (so the caller can see
// which signals flipped), or an error wrapping the last attempt's
// reason on failure.
func (o *BootOrchestrator) probeWithRecovery(ctx context.Context) (adb.ProbeResult, error) {
	strats := o.policy.Strategies(o.cfg.PackageName, o.cfg.ExpectedWidth, o.cfg.ExpectedHeight, o.cfg.ExpectedDPI)
	for attempt := 1; attempt <= o.cfg.MaxRecoveryAttempts; attempt++ {
		start := time.Now()
		pctx, cancel := context.WithTimeout(ctx, o.cfg.BootProbeTimeout)
		res, err := o.prober.WaitReady(pctx)
		cancel()
		if err == nil && res.Ready {
			o.report.AppendStep("boot.probe", start, BootResultOK, summarizeProbe(res))
			o.report.SetAttempts(attempt)
			return res, nil
		}
		// Failed. Log and escalate.
		reason := "probe not ready"
		if err != nil {
			reason = err.Error()
		}
		o.report.AppendStep("boot.probe", start, BootResultTimeout, reason)

		// Choose the next strategy. If we've exhausted the ladder,
		// bail with a synthesized error.
		idx := o.policy.Escalate(strats, attempt)
		if idx < 0 {
			o.report.SetAttempts(attempt)
			return res, fmt.Errorf("boot probe failed after %d attempts; last result: %s", attempt, summarizeProbe(res))
		}
		s := strats[idx]
		o.logger.Warn().
			Str("strategy", s.Name).
			Int("attempt", attempt).
			Bool("destructive", s.Destructive).
			Msg("boot probe failed, applying recovery strategy")
		o.report.MarkRecovery(s.Name)
		sStart := time.Now()
		if sErr := s.Apply(ctx); sErr != nil {
			o.report.AppendStep("recovery."+s.Name, sStart, BootResultError, sErr.Error())
			o.logger.Error().Err(sErr).Str("strategy", s.Name).Msg("recovery strategy failed")
		} else {
			o.report.AppendStep("recovery."+s.Name, sStart, BootResultOK, "")
		}
		// Backoff before the next attempt. The wait is bounded by
		// the outer context so a Stop click can still cancel.
		wait := o.policy.Backoff(attempt)
		select {
		case <-ctx.Done():
			o.report.SetAttempts(attempt)
			return res, ctx.Err()
		case <-time.After(wait):
		}
	}
	o.report.SetAttempts(o.cfg.MaxRecoveryAttempts)
	return adb.ProbeResult{}, errors.New("boot probe exhausted recovery attempts")
}

// screenSize fetches wm size. The shell call goes through the
// context-aware wrapper with the per-call timeout so a hung device
// can't stall this step past its budget.
func (o *BootOrchestrator) screenSize(ctx context.Context) (int, int, error) {
	start := time.Now()
	pctx, cancel := context.WithTimeout(ctx, o.cfg.AdbPerCallTimeout)
	defer cancel()
	out, err := o.runner.Shell(pctx, "wm size")
	if err != nil {
		o.report.AppendStep("screen.size", start, BootResultError, err.Error())
		return 0, 0, err
	}
	var w, h int
	if _, err := fmt.Sscanf(out, "Physical size: %dx%d", &w, &h); err != nil {
		if _, err := fmt.Sscanf(out, "Override size: %dx%d", &w, &h); err != nil {
			o.report.AppendStep("screen.size", start, BootResultError, fmt.Sprintf("parse %q: %v", out, err))
			return 0, 0, fmt.Errorf("parse wm size %q: %w", out, err)
		}
	}
	o.report.AppendStep("screen.size", start, BootResultOK, fmt.Sprintf("%dx%d", w, h))
	return w, h, nil
}

// Report returns the live (not snapshot) report. Callers that need
// to serialize should use Report().Snapshot() instead.
func (o *BootOrchestrator) Report() *BootReport { return o.report }

// persist writes the report to the standard log directory. Errors
// are silently dropped — a failed persistence must not turn into a
// boot failure (which would create infinite retry loops on a
// read-only filesystem).
func (o *BootOrchestrator) persist() {
	path := paths.ResolveConfig("logs/last_boot_report.json")
	if err := o.report.SaveJSON(path); err != nil {
		o.logger.Debug().Err(err).Str("path", path).Msg("failed to persist boot report")
	}
}

// summarizeProbe turns a ProbeResult into a short "ready_count/min,
// ready_signals=[…]" string for the step detail.
func summarizeProbe(r adb.ProbeResult) string {
	if r.Ready {
		return fmt.Sprintf("ready (%d/%d signals)", r.ReadyCount, r.MinRequired)
	}
	// List the signals that WERE ready so a dev reading the report
	// can see which ones are still flaky.
	ready := make([]string, 0, 4)
	for name, st := range r.Signals {
		if st.Ready {
			ready = append(ready, string(name))
		}
	}
	if len(ready) == 0 {
		return fmt.Sprintf("not ready (%d/%d signals, none passed)", r.ReadyCount, r.MinRequired)
	}
	return fmt.Sprintf("not ready (%d/%d signals; passed: %v)", r.ReadyCount, r.MinRequired, ready)
}

// last returns the most-recent step's name, or "" if the report has
// none. Used by the suggested-action heuristics.
func last(r *BootReport) string {
	snap := r.Snapshot()
	if len(snap.Steps) == 0 {
		return ""
	}
	return snap.Steps[len(snap.Steps)-1].Name
}

// containsStr is a tiny helper used by the BlueStacks-restarted
// detection. Returns true if needle is in haystack. Empty needle
// returns false (callers test specifically).
func containsStr(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}
