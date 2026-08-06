// Package bot — boot_report.go
//
// BootReport is the structured, persisted, Wails-emitted record of one
// boot attempt. It is built incrementally by the BootOrchestrator and
// surfaces three places: the in-memory status (returned to the React
// side via the bot_init_report Wails event), the JSON file under
// paths.ResolveConfig("logs/"), and the bundled boot profile (p95 /
// recommended timeout — see bootprofile.go).
//
// The intent is "never again should the user see a bare 'failed to
// initialize bot' line and have to grep app.log to find out why."
// Every step records its name, duration, result, and a short human
// detail. The SuggestedAction field is what the orchestrator believes
// the user should try next ("wait longer", "check adb", etc.) — the
// React panel surfaces this as a one-liner with optional buttons.
//
// The BootReport type holds a sync.Mutex so the orchestrator can
// append steps and recovery names from multiple goroutines safely.
// BootReport is therefore NOT safe to copy by value — copying a
// sync.Mutex is undefined behavior. The companion BootReportView
// type below is the value-copyable, JSON-marshalable, Wails-emittable
// projection: Snapshot() and SaveJSON() produce a BootReportView,
// and all read paths operate on it. This split keeps the mutex
// where it belongs (on the writer) and prevents the "go vet
// warning: return copies lock value" footgun.
package bot

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// BootResult is the terminal outcome of a single step in the boot
// pipeline. We keep it as a small enum-style string so it round-trips
// through JSON cleanly and is grep-friendly in app.log.
type BootResult string

const (
	BootResultOK      BootResult = "ok"
	BootResultTimeout BootResult = "timeout"
	BootResultError   BootResult = "error"
	BootResultSkipped BootResult = "skipped"
)

// BootStep is one discrete phase of the boot sequence. The orchestrator
// emits one per phase. Latency at the field level is the per-step
// duration; cumulative timing is recomputed by Summary().
type BootStep struct {
	Name      string        `json:"name"`
	StartedAt time.Time     `json:"started_at"`
	Duration  time.Duration `json:"duration_ns"`
	Result    BootResult    `json:"result"`
	Detail    string        `json:"detail,omitempty"`
}

// BootReport is the full per-attempt record. It is safe for concurrent
// writes via the embedded mutex; the orchestrator holds the lock while
// appending steps. DO NOT copy a BootReport by value — use Snapshot()
// to get a value-copyable BootReportView for reads, JSON marshaling,
// or Wails emission.
//
// The mutex is embedded (not a pointer) so the zero value is usable
// without explicit initialization. NewBootReport() handles the slice
// and map allocations.
type BootReport struct {
	mu sync.Mutex

	startedAt         time.Time
	completedAt       time.Time
	duration          time.Duration
	outcome           string
	finalError        string
	suggestedAction   string
	deviceID          string
	packageName       string
	expectedWidth     int
	expectedHeight    int
	blueStacksEnsured bool
	recoveryUsed      []string
	attempts          int
	steps             []BootStep
	metadata          map[string]string
}

// BootReportView is the JSON-serializable, Wails-emittable, copyable
// view of a BootReport. It has the same fields as BootReport minus
// the mutex, and is what callers actually read/serialize. The
// orchestrator hands out views via Snapshot() and the React side
// consumes them as plain JSON.
type BootReportView struct {
	StartedAt         time.Time         `json:"started_at"`
	CompletedAt       time.Time         `json:"completed_at,omitempty"`
	Duration          time.Duration     `json:"duration_ns"`
	Outcome           string            `json:"outcome"`
	FinalError        string            `json:"final_error,omitempty"`
	SuggestedAction   string            `json:"suggested_action,omitempty"`
	DeviceID          string            `json:"device_id,omitempty"`
	PackageName       string            `json:"package_name,omitempty"`
	ExpectedWidth     int               `json:"expected_width,omitempty"`
	ExpectedHeight    int               `json:"expected_height,omitempty"`
	BlueStacksEnsured bool              `json:"bluestacks_ensured"`
	RecoveryUsed      []string          `json:"recovery_used,omitempty"`
	Attempts          int               `json:"attempts"`
	Steps             []BootStep        `json:"steps"`
	Metadata          map[string]string `json:"metadata,omitempty"`
}

// NewBootReport constructs an empty report with the current wall clock
// as the start time. The caller is expected to fill DeviceID,
// PackageName, ExpectedWidth/Height, and Metadata before appending the
// first step (or, at minimum, before Snapshot() / Complete()).
func NewBootReport() *BootReport {
	return &BootReport{
		startedAt:    time.Now(),
		steps:        make([]BootStep, 0, 12),
		recoveryUsed: make([]string, 0, 4),
		metadata:     make(map[string]string),
	}
}

// AppendStep records a completed step. Safe for concurrent use. The
// passed-in startedAt is when the step began; Duration is recomputed
// here to keep callers from getting the wall-clock math wrong.
func (r *BootReport) AppendStep(name string, startedAt time.Time, result BootResult, detail string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.steps = append(r.steps, BootStep{
		Name:      name,
		StartedAt: startedAt,
		Duration:  time.Since(startedAt),
		Result:    result,
		Detail:    detail,
	})
}

// MarkRecovery appends a recovery-strategy name to the report. Called
// once per executed strategy (not per attempt) so the user can see
// "we had to SoftReset this time" in the UI.
func (r *BootReport) MarkRecovery(strategy string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.recoveryUsed = append(r.recoveryUsed, strategy)
}

// Complete finalizes the report. outcome should be "ok" or "failed".
// err may be nil for "ok". suggestedAction is a short imperative like
// "wait longer" or "relaunch BlueStacks manually" — surfaced in the UI
// and the JSON file.
func (r *BootReport) Complete(outcome string, err error, suggestedAction string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.completedAt = time.Now()
	r.duration = r.completedAt.Sub(r.startedAt)
	r.outcome = outcome
	if err != nil {
		r.finalError = err.Error()
	}
	if r.outcome != "ok" && suggestedAction == "" {
		suggestedAction = "check the boot report for details"
	}
	r.suggestedAction = suggestedAction
}

// SetDeviceContext sets the device/package/resolution fields. Called
// once at construction so Snapshot() can include them in the view.
// Safe to call before any concurrent writer exists.
func (r *BootReport) SetDeviceContext(deviceID, packageName string, width, height int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.deviceID = deviceID
	r.packageName = packageName
	r.expectedWidth = width
	r.expectedHeight = height
}

// SetMetadata merges key/value pairs into the report's free-form
// metadata. Safe for concurrent use. Existing keys are overwritten;
// new keys are added.
func (r *BootReport) SetMetadata(kv map[string]string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for k, v := range kv {
		r.metadata[k] = v
	}
}

// SetBlueStacksEnsured marks whether EnsureBlueStacksMac was called
// during this boot. Used by the UI to distinguish "cold-started
// BlueStacks" from "reused an already-running instance."
func (r *BootReport) SetBlueStacksEnsured(v bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.blueStacksEnsured = v
}

// SetAttempts records the number of probe attempts the orchestrator
// made. Called once at the end of the boot.
func (r *BootReport) SetAttempts(n int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.attempts = n
}

// Snapshot returns a value-copyable, JSON-marshalable view of the
// report. The mutex is held only long enough to clone the slices and
// map; the returned BootReportView is independent of the source
// BootReport and can be passed across goroutines, marshaled, or
// emitted via Wails without any lock-copy issues.
func (r *BootReport) Snapshot() BootReportView {
	r.mu.Lock()
	defer r.mu.Unlock()

	view := BootReportView{
		StartedAt:         r.startedAt,
		CompletedAt:       r.completedAt,
		Duration:          r.duration,
		Outcome:           r.outcome,
		FinalError:        r.finalError,
		SuggestedAction:   r.suggestedAction,
		DeviceID:          r.deviceID,
		PackageName:       r.packageName,
		ExpectedWidth:     r.expectedWidth,
		ExpectedHeight:    r.expectedHeight,
		BlueStacksEnsured: r.blueStacksEnsured,
		Attempts:          r.attempts,
	}
	if r.recoveryUsed != nil {
		view.RecoveryUsed = append([]string(nil), r.recoveryUsed...)
	}
	if r.steps != nil {
		view.Steps = append([]BootStep(nil), r.steps...)
	}
	if r.metadata != nil {
		view.Metadata = make(map[string]string, len(r.metadata))
		for k, v := range r.metadata {
			view.Metadata[k] = v
		}
	}
	return view
}

// Summary returns a one-line human description for log lines. Format:
//
//	"boot ok in 12.3s (4 steps, no recovery)"                   — success
//	"boot failed in 90.0s: android boot timeout (5 steps, 2 recovery)" — failure
func (r *BootReport) Summary() string {
	view := r.Snapshot()
	if view.Outcome == "ok" {
		return fmt.Sprintf("boot ok in %s (%d steps, %d recovery)",
			view.Duration.Round(time.Millisecond), len(view.Steps), len(view.RecoveryUsed))
	}
	errPart := view.FinalError
	if errPart == "" {
		errPart = "unknown error"
	}
	return fmt.Sprintf("boot failed in %s: %s (%d steps, %d recovery)",
		view.Duration.Round(time.Millisecond), errPart, len(view.Steps), len(view.RecoveryUsed))
}

// SaveJSON writes the report to path as pretty-printed JSON. Errors
// are returned to the caller but should typically be logged at debug
// level — a failed persistence must not itself become a boot failure.
func (r *BootReport) SaveJSON(path string) error {
	view := r.Snapshot()
	blob, err := json.MarshalIndent(view, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal boot report: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", filepath.Dir(path), err)
	}
	return os.WriteFile(path, blob, 0o644)
}

// StepSummary is a small printable summary for the React side / log
// line. One per step, joined with " | " by JoinedStepSummary.
func (s BootStep) StepSummary() string {
	dur := s.Duration.Round(time.Millisecond)
	detail := s.Detail
	if detail == "" {
		detail = string(s.Result)
	}
	return fmt.Sprintf("%s=%s/%s", s.Name, s.Result, dur)
}

// JoinedStepSummary returns "adb.connect=ok/2.1s | boot.probe=ok/87s | ..."
// suitable for one-line log emission and the Wails bot_init_report
// event payload. Truncates to maxLen characters with a trailing "…"
// so a 90-second probe doesn't blow up a UI card.
func (r *BootReport) JoinedStepSummary(maxLen int) string {
	view := r.Snapshot()
	parts := make([]string, 0, len(view.Steps))
	for _, s := range view.Steps {
		parts = append(parts, s.StepSummary())
	}
	joined := strings.Join(parts, " | ")
	if maxLen > 0 && len(joined) > maxLen {
		joined = joined[:maxLen-1] + "…"
	}
	return joined
}
