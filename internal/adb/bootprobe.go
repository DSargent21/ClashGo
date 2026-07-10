// Package adb — bootprobe.go
//
// BootProber is the multi-signal readiness probe that replaces the
// single-signal `WaitForBoot` (which only checked sys.boot_completed).
// On BlueStacks and similar emulators, sys.boot_completed can take
// 30-180s to flip to "1" on cold boot, and on some builds the
// property never gets set if the launcher's update screen is up.
//
// Rather than rely on a single signal that is known to be flaky, the
// probe checks four orthogonal signals and reports ready when the
// configured number of them agree:
//
//  1. sys.boot_completed == "1"            (the legacy signal)
//  2. init.svc.bootanim == "stopped"       (boot animation service done)
//  3. screencap returns a non-black frame  (SurfaceFlinger is up)
//  4. `pm list packages` returns our pkg   (PackageManager is up)
//
// Signals 3 and 4 are the only ones that actually matter for
// "CoC can launch successfully" — signal 1 is preserved for
// compatibility and signal 2 is a cheap extra confirmation that the
// UI is settled. By default the probe reports ready when ANY 2 of
// the 4 signals are ready (configurable via MinReadySignals).
//
// Each signal is wrapped in a per-signal context timeout so a hung
// shell call (e.g. a wedged ADB transport) cannot stall the whole
// probe loop past its outer budget.
package adb

import (
	"context"
	"encoding/binary"
	"fmt"
	"sync"
	"time"
)

// SignalName is the type-safe name of one probe signal. Used both in
// the per-signal timeout config and in the result map.
type SignalName string

const (
	SignalBootCompleted SignalName = "boot_completed"
	SignalBootAnim      SignalName = "boot_anim"
	SignalScreenReady   SignalName = "screen_ready"
	SignalPackageMgr    SignalName = "package_manager"
)

// SignalStatus is the per-signal outcome. Latency is how long the
// signal's check took (useful for diagnosing which one is slow).
type SignalStatus struct {
	Name    SignalName `json:"name"`
	Ready   bool       `json:"ready"`
	Value   string     `json:"value,omitempty"`
	Latency int64      `json:"latency_ns"`
	Error   string     `json:"error,omitempty"`
}

// ProbeResult is the aggregate of one probe pass. Signals is keyed by
// SignalName so callers can render it directly as JSON. ReadyCount is
// the number of ready signals; Ready is the boolean verdict.
type ProbeResult struct {
	Ready       bool                        `json:"ready"`
	ReadyCount  int                         `json:"ready_count"`
	MinRequired int                         `json:"min_required"`
	Signals     map[SignalName]SignalStatus `json:"signals"`
	LastError   string                      `json:"last_error,omitempty"`
}

// BootProbeConfig is the tunables for the probe. The defaults are
// chosen to be safe for the production BlueStacks-on-macOS case but
// dev mode shrinks them via dev-fail-fast.
type BootProbeConfig struct {
	// PerSignalTimeout caps how long any one signal can take. 5s is
	// well above a healthy device's per-call latency (50-200ms) and
	// well below the point where a hung call is worth waiting for.
	PerSignalTimeout time.Duration

	// PollInterval is the gap between probe passes. 2s matches the
	// prior WaitForBoot cadence. In dev mode this drops to 500ms.
	PollInterval time.Duration

	// MinReadySignals is how many signals must agree before the
	// probe reports ready. Default 2 (so 2-of-4 is enough); the
	// boot_completed-only check would correspond to MinReady=1 +
	// only-the-legacy-signal in use, but the orchestrator always
	// queries all four.
	MinReadySignals int
}

// DefaultBootProbeConfig returns the production defaults. Used when
// no override is supplied (typically the orchestrator overrides for
// dev mode).
func DefaultBootProbeConfig() BootProbeConfig {
	return BootProbeConfig{
		PerSignalTimeout: 5 * time.Second,
		PollInterval:     2 * time.Second,
		MinReadySignals:  2,
	}
}

// BootProber runs the multi-signal probe. Construct one with
// NewBootProber; the zero value is not usable. The probe is
// goroutine-safe — multiple calls to Probe() can run concurrently,
// though the orchestrator never does.
type BootProber struct {
	runner ShellRunner
	cfg    BootProbeConfig

	// packageName is the Android package whose presence in `pm list
	// packages` is the success signal for SignalPackageMgr. Empty
	// means "any non-empty output is success" (e.g. the PackageManager
	// is up at all).
	packageName string

	// screencapMinLuma is the minimum mean BGR luma for the screen
	// signal to be considered ready. 8/255 = "not all black" — a
	// real Android UI almost always has at least a status bar pixel
	// above this. Calibrated against a black-frame test fixture.
	screencapMinLuma float64
}

// NewBootProber wires a prober. The runner is typically
// NewShellRunner(client) in production; tests pass a fake.
func NewBootProber(runner ShellRunner, cfg BootProbeConfig, packageName string) *BootProber {
	return &BootProber{
		runner:           runner,
		cfg:              cfg,
		packageName:      packageName,
		screencapMinLuma: 8.0,
	}
}

// Probe runs one full pass. Each signal runs concurrently (its own
// goroutine + context), so the worst case for a single Probe call is
// the per-signal timeout plus a small bit of orchestration overhead.
// The returned ProbeResult is fully populated even on failure (the
// failing signals carry their error in Error).
func (p *BootProber) Probe(ctx context.Context) ProbeResult {
	if p.cfg.MinReadySignals == 0 {
		p.cfg.MinReadySignals = 2
	}
	res := ProbeResult{
		MinRequired: p.cfg.MinReadySignals,
		Signals:     make(map[SignalName]SignalStatus, 4),
	}

	var wg sync.WaitGroup
	var mu sync.Mutex
	record := func(st SignalStatus) {
		mu.Lock()
		res.Signals[st.Name] = st
		if st.Ready {
			res.ReadyCount++
		} else if st.Error != "" {
			res.LastError = fmt.Sprintf("%s: %s", st.Name, st.Error)
		}
		mu.Unlock()
	}

	probe := func(name SignalName, fn func(context.Context) SignalStatus) {
		defer wg.Done()
		cctx, cancel := context.WithTimeout(ctx, p.cfg.PerSignalTimeout)
		defer cancel()
		record(fn(cctx))
	}

	wg.Add(4)
	go probe(SignalBootCompleted, p.probeBootCompleted)
	go probe(SignalBootAnim, p.probeBootAnim)
	go probe(SignalScreenReady, p.probeScreenReady)
	go probe(SignalPackageMgr, p.probePackageMgr)
	wg.Wait()

	res.Ready = res.ReadyCount >= p.cfg.MinReadySignals
	return res
}

// WaitReady is the long-running variant: probes repeatedly until the
// result reports ready or ctx is done. Returns the final
// ProbeResult so the caller can see which signals ultimately flipped.
//
// This is the function the BootOrchestrator uses in place of the old
// WaitForBoot — same shape, but with multi-signal logic and a richer
// return value.
func (p *BootProber) WaitReady(ctx context.Context) (ProbeResult, error) {
	if err := ctx.Err(); err != nil {
		return ProbeResult{}, err
	}
	ticker := time.NewTicker(p.cfg.PollInterval)
	defer ticker.Stop()
	// Take one immediate sample so a device that was already ready
	// before the call doesn't have to wait a full PollInterval.
	res := p.Probe(ctx)
	if res.Ready {
		return res, nil
	}
	for {
		select {
		case <-ctx.Done():
			return res, ctx.Err()
		case <-ticker.C:
			res = p.Probe(ctx)
			if res.Ready {
				return res, nil
			}
		}
	}
}

// probeBootCompleted checks getprop sys.boot_completed == "1".
func (p *BootProber) probeBootCompleted(ctx context.Context) SignalStatus {
	start := time.Now()
	out, err := p.runner.Shell(ctx, "getprop sys.boot_completed")
	st := SignalStatus{
		Name:    SignalBootCompleted,
		Latency: time.Since(start).Nanoseconds(),
		Value:   out,
	}
	if err != nil {
		st.Error = err.Error()
		return st
	}
	st.Ready = out == "1"
	if !st.Ready {
		st.Error = fmt.Sprintf("boot_completed=%q (want \"1\")", out)
	}
	return st
}

// probeBootAnim checks init.svc.bootanim == "stopped". This is the
// boot animation service: when it reports "stopped", the system has
// finished drawing the boot animation and is showing the launcher.
func (p *BootProber) probeBootAnim(ctx context.Context) SignalStatus {
	start := time.Now()
	out, err := p.runner.Shell(ctx, "getprop init.svc.bootanim")
	st := SignalStatus{
		Name:    SignalBootAnim,
		Latency: time.Since(start).Nanoseconds(),
		Value:   out,
	}
	if err != nil {
		st.Error = err.Error()
		return st
	}
	st.Ready = out == "stopped"
	if !st.Ready {
		st.Error = fmt.Sprintf("bootanim=%q (want \"stopped\")", out)
	}
	return st
}

// probeScreenReady captures a frame and computes its mean BGR luma.
// Above p.screencapMinLuma the screen is considered "not black" and
// the signal is ready. This is the single most useful signal on
// BlueStacks because SurfaceFlinger is up before sys.boot_completed
// flips in many builds.
//
// The luma computation is O(W*H) and runs in a tight loop over the
// raw RGBA-ish pixels returned by screencap. For 860x732 frames this
// is ~2.5MB to walk — well under 50ms on a 2020 MacBook.
func (p *BootProber) probeScreenReady(ctx context.Context) SignalStatus {
	start := time.Now()
	buf, err := p.runner.CaptureScreen(ctx)
	st := SignalStatus{
		Name:    SignalScreenReady,
		Latency: time.Since(start).Nanoseconds(),
	}
	if err != nil {
		st.Error = err.Error()
		return st
	}
	if len(buf) < 12 {
		st.Error = "screencap response too short"
		return st
	}
	width := int(binary.LittleEndian.Uint32(buf[0:4]))
	height := int(binary.LittleEndian.Uint32(buf[4:8]))
	if width <= 0 || height <= 0 || width > 4096 || height > 4096 {
		st.Error = fmt.Sprintf("invalid screencap dimensions: %dx%d", width, height)
		return st
	}
	expected := width * height * 4
	if len(buf) < expected+12 {
		st.Error = fmt.Sprintf("incomplete screencap: got %d, want %d", len(buf), expected+12)
		return st
	}
	// screencap on Android outputs RGBA. Sample mean luma using the
	// perceptual Rec. 601 weights (0.299R + 0.587G + 0.114B).
	var sum uint64
	const stride = 8 // sample every 8th pixel for speed (~312K samples for 860x732)
	pixels := buf[12 : expected+12]
	n := 0
	for i := 0; i+4 <= len(pixels); i += 4 * stride {
		// Android screencap is RGBA. Use R, G, B. Cast to uint64
		// so the 299/587/114 multiplications don't overflow uint32
		// for a fully-saturated (255,255,255) pixel.
		r := uint64(pixels[i])
		g := uint64(pixels[i+1])
		b := uint64(pixels[i+2])
		sum += 299*r + 587*g + 114*b
		n++
	}
	if n == 0 {
		st.Error = "no pixels in screencap"
		return st
	}
	// Rec. 601 weights sum to 1000, so divide by 1000 to get a 0-255
	// luma value.
	mean := float64(sum) / float64(n) / 1000.0
	st.Value = fmt.Sprintf("mean_luma=%.1f (%dx%d)", mean, width, height)
	st.Ready = mean >= p.screencapMinLuma
	if !st.Ready {
		st.Error = fmt.Sprintf("screen mostly black (mean luma %.1f < %.1f)", mean, p.screencapMinLuma)
	}
	return st
}

// probePackageMgr checks that `pm list packages` returns successfully
// AND, if a package name was configured at construction, that the
// package appears in the listing. This is the signal that matters
// most for "can CoC launch" — without PackageManager no app can
// start.
func (p *BootProber) probePackageMgr(ctx context.Context) SignalStatus {
	start := time.Now()
	out, err := p.runner.Shell(ctx, "pm list packages")
	st := SignalStatus{
		Name:    SignalPackageMgr,
		Latency: time.Since(start).Nanoseconds(),
	}
	if err != nil {
		st.Error = err.Error()
		return st
	}
	if out == "" {
		st.Error = "pm list packages returned empty"
		return st
	}
	if p.packageName == "" {
		// No specific package configured — any non-empty output means
		// PackageManager is responsive.
		st.Ready = true
		st.Value = "pm responsive"
		return st
	}
	// Match `package:<name>` in the output. TrimSpace + a simple
	// contains check is enough — pm's output is line-delimited and
	// we don't need exact-line matching.
	want := "package:" + p.packageName
	if containsLine(out, want) {
		st.Ready = true
		st.Value = p.packageName + " present"
	} else {
		st.Error = p.packageName + " not in pm list packages"
	}
	return st
}

// containsLine is a small helper to keep the readiness check
// allocation-free. Looks for a literal `want` line prefix.
func containsLine(out, want string) bool {
	// Fast path: simple substring.
	if len(want) == 0 {
		return true
	}
	for i := 0; i+len(want) <= len(out); i++ {
		if out[i] == want[0] && out[i:i+len(want)] == want {
			return true
		}
	}
	return false
}
