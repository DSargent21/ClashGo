// Package world maintains a single, always-writable, atomic, queryable JSON
// snapshot of the bot's current world view at ~/.clashgo/current_state.json.
//
// Design goals:
//   - Always-on: works even when NATS bus is disabled.
//   - Bounded size: snapshot is capped at ~50 KB; never embeds screen bytes.
//   - Hot-path safe: Update() never blocks; a single coalescing goroutine writes
//     to disk at most every MinWriteInterval.
//   - Audit-friendly: writes are atomic via tmp + rename, so concurrent `jq`
//     readers never see a half-written file.
//
// Pattern: callers do world.Update("classifier", ClassifierEvent{...}).
// The world packs fields into a Section, which is merged into the JSON snapshot
// by a single writer goroutine.
//
// The world is intentionally tiny and dependency-free. It does not depend on
// gocv, adb, or any game package — only on the bus package for protobuf events
// and standard library for JSON + atomic file rename.
package world

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
)

// Snapshot is the on-disk JSON structure written to current_state.json.
// Field tags are stable; new fields may be added but existing field names
// must not change without a schema_version bump.
type Snapshot struct {
	SchemaVersion int       `json:"schema_version"`
	UpdatedMS     int64     `json:"updated_ms"`
	BootID        string    `json:"boot_id,omitempty"`
	SessionID     string    `json:"session_id,omitempty"`
	AttackID      string    `json:"attack_id,omitempty"`

	CurrentState string `json:"current_state,omitempty"`
	PrevState    string `json:"prev_state,omitempty"`
	StateAgeMS   int64  `json:"state_age_ms,omitempty"`

	Classifier *ClassifierSnapshot `json:"classifier,omitempty"`
	ADB        *ADBSnapshot        `json:"adb,omitempty"`
	Sequence   *SequenceSnapshot   `json:"sequence,omitempty"`
	Deployment *DeploymentSnapshot `json:"deployment,omitempty"`
	Slots      []*SlotSnapshot     `json:"slots,omitempty"`
	Stats      *StatsSnapshot      `json:"stats,omitempty"`
	FSM        *FSMSnapshot        `json:"fsm,omitempty"`
	Config     *ConfigSnapshot     `json:"config,omitempty"`

	LastFrame *FrameSnapshot   `json:"last_frame,omitempty"`
	Taps      []*TapSnapshot   `json:"taps_last_30s,omitempty"`
	Custom    *json.RawMessage `json:"custom,omitempty"`
}

type ClassifierSnapshot struct {
	TopScore       int            `json:"top_score"`
	Alternatives   map[string]int `json:"alternatives,omitempty"`
	ConfirmFrames  int            `json:"confirm_frames"`
	UpdatedMS      int64          `json:"updated_ms"`
}

type ADBSnapshot struct {
	Connected       bool    `json:"connected"`
	Device          string  `json:"device,omitempty"`
	Resolution      string  `json:"resolution,omitempty"`
	LastCaptureMS   int64   `json:"last_capture_ms"`
	AvgCaptureMS    float64 `json:"avg_capture_ms"`
	ConsecutiveFails int32  `json:"consecutive_fails"`
	UptimeS         int64   `json:"uptime_s"`
}

type SequenceSnapshot struct {
	Running       bool   `json:"running"`
	CurrentStep   string `json:"current_step,omitempty"`
	StepStartedMS int64  `json:"step_started_ms,omitempty"`
	StepRetries   int    `json:"step_retries"`
	AttackID      string `json:"attack_id,omitempty"`
}

type DeploymentSnapshot struct {
	RedZoneValid bool          `json:"red_zone_valid"`
	RedZone      *BoxSnapshot  `json:"red_zone,omitempty"`
	DeploySide   string        `json:"deploy_side,omitempty"`
	DeployPoints []PointSnapshot `json:"deploy_points,omitempty"`
	Anchor       *PointSnapshot `json:"anchor,omitempty"`
	BarY         int           `json:"bar_y,omitempty"`
}

type BoxSnapshot struct {
	X int `json:"x"`
	Y int `json:"y"`
	W int `json:"w"`
	H int `json:"h"`
}

type PointSnapshot struct {
	X int `json:"x"`
	Y int `json:"y"`
}

type SlotSnapshot struct {
	Idx       int    `json:"idx"`
	X         int    `json:"x"`
	Y         int    `json:"y,omitempty"`
	Category  string `json:"category"`
	UnitHint  string `json:"unit_hint,omitempty"`
	Count     int    `json:"count,omitempty"`
	Deployed  bool   `json:"deployed"`
}

type StatsSnapshot struct {
	AttacksCompleted      int32 `json:"attacks_completed"`
	Skips                 int32 `json:"skips"`
	Stars                 int32 `json:"stars"`
	Stars0                int32 `json:"stars_0"`
	Stars1                int32 `json:"stars_1"`
	Stars2                int32 `json:"stars_2"`
	Stars3                int32 `json:"stars_3"`
	Gold                  int64 `json:"gold"`
	Elixir                int64 `json:"elixir"`
	DarkElixir            int64 `json:"dark_elixir"`
	SessionAvgAttackMS    int64 `json:"session_avg_attack_ms,omitempty"`
	LastAttackID          string `json:"last_attack_id,omitempty"`
}

type FSMSnapshot struct {
	Node              string   `json:"node,omitempty"`
	History           []string `json:"history,omitempty"`
	TransitionsLast10 int      `json:"transitions_last_10s"`
}

type ConfigSnapshot struct {
	Strategy    string             `json:"strategy,omitempty"`
	TargetEdge  string             `json:"target_edge,omitempty"`
	Thresholds  map[string]float32 `json:"thresholds,omitempty"`
}

type FrameSnapshot struct {
	TSMillis  int64  `json:"ts_ms"`
	PHash     string `json:"phash,omitempty"`
	Ref       string `json:"ref,omitempty"`
}

type TapSnapshot struct {
	Millis int64  `json:"ms"`
	Name   string `json:"name"`
	Tier   string `json:"tier"`
	X      int    `json:"x,omitempty"`
	Y      int    `json:"y,omitempty"`
	OK     bool   `json:"ok"`
	Reason string `json:"reason,omitempty"`
}

// World is the in-memory aggregation of everything the bot knows. It is
// goroutine-safe: all Update methods may be called from any goroutine.
//
// One World instance per bot process. The pointer is globally accessible via
// the package-level Default() / SetDefault() helpers.
//
// Concurrency notes:
//   - closed is an atomic.Bool so Update() and Stop() can race safely without
//     acquiring the slowpath mutex.
//   - patches is the hot-path channel: Update() must NEVER block on it.
//   - dropped counts patches dropped due to channel pressure (callers can
//     introspect via Dropped() to detect lost snapshots — important for the
//     terminal-AI observability story).
//   - All writes to snap are serialized through the writer goroutine OR through
//     UpdateImmediate (which takes the write lock).
type World struct {
	mu     sync.RWMutex
	snap   Snapshot
	path   string
	bootID string
	lastWrittenAt time.Time
	dirty bool
	closed atomic.Bool
	dropped atomic.Int64

	patches chan patch
	stop    chan struct{}
	wg      sync.WaitGroup

	// configurable
	MinWriteInterval time.Duration
	MaxTapsRetained  int
}

type patch struct {
	section string
	apply   func(*Snapshot)
}

// New creates a world backed by path (typically ~/.clashgo/current_state.json).
// The directory is created if missing. The returned World must have Start()
// called to begin the writer goroutine, and Close() on shutdown.
func New(path string) *World {
	return &World{
		path:             path,
		bootID:           fmt.Sprintf("boot_%s", time.Now().Format("20060102_150405")),
		MinWriteInterval: 250 * time.Millisecond,
		MaxTapsRetained:  64,
		patches:          make(chan patch, 1024),
		stop:             make(chan struct{}),
	}
}

// Start launches the writer goroutine. Call once after New.
func (w *World) Start() {
	if w == nil {
		return
	}
	// Ensure parent directory exists.
	if dir := filepath.Dir(w.path); dir != "" {
		_ = os.MkdirAll(dir, 0o755)
	}
	// Write a fresh boot file immediately so any tool can detect a new run.
	w.mu.Lock()
	w.snap.SchemaVersion = SchemaVersion
	w.snap.BootID = w.bootID
	w.snap.SessionID = w.bootID
	w.snap.UpdatedMS = time.Now().UnixMilli()
	w.mu.Unlock()
	w.flushNow()

	w.wg.Add(1)
	go w.run()
}

// Stop flushes and joins the writer goroutine.
func (w *World) Stop() {
	if w == nil {
		return
	}
	if !w.closed.CompareAndSwap(false, true) {
		return
	}
	close(w.stop)
	w.wg.Wait()
	w.flushNow()
}

// Path returns the on-disk JSON path.
func (w *World) Path() string {
	if w == nil {
		return ""
	}
	return w.path
}

// BootID returns the boot identifier for this world instance.
func (w *World) BootID() string {
	if w == nil {
		return ""
	}
	return w.bootID
}

// Update enqueues a snapshot mutation. The apply function runs on the writer
// goroutine, so it must be pure (no blocking I/O).
//
// Returns false if the world has been Stopped or the patches channel is full
// (the latter increments Dropped() so callers can detect lost snapshots).
func (w *World) Update(section string, apply func(*Snapshot)) bool {
	if w == nil {
		return false
	}
	if w.closed.Load() {
		return false
	}
	select {
	case w.patches <- patch{section: section, apply: apply}:
		return true
	default:
		// Drop on backpressure rather than block the hot path. The boot
		// snapshot will still get written frequently.
		w.dropped.Add(1)
		return false
	}
}

// Dropped returns the count of patches silently dropped due to channel
// backpressure. A persistently-rising count means the writer goroutine is
// falling behind; consider increasing the patches channel capacity or
// reducing update frequency.
func (w *World) Dropped() int64 {
	if w == nil {
		return 0
	}
	return w.dropped.Load()
}

// UpdateImmediate is like Update but writes to disk synchronously. Use only
// for rare milestones (attack start/end, restart).
func (w *World) UpdateImmediate(section string, apply func(*Snapshot)) {
	if w == nil {
		return
	}
	w.mu.Lock()
	if apply != nil {
		apply(&w.snap)
	}
	w.snap.UpdatedMS = time.Now().UnixMilli()
	w.mu.Unlock()
	w.flushNow()
}

// Snapshot returns a deep-copied snapshot safe to read. The caller must not
// mutate the returned struct.
func (w *World) Snapshot() Snapshot {
	if w == nil {
		return Snapshot{}
	}
	w.mu.RLock()
	defer w.mu.RUnlock()
	// Marshal + unmarshal for a deep copy. Cheap enough at <50KB.
	b, _ := json.Marshal(w.snap)
	var out Snapshot
	_ = json.Unmarshal(b, &out)
	return out
}

// ReadSnapshot reads the on-disk current_state.json directly. Useful for
// external observers (a terminal AI) that want to bypass the in-process
// snapshot and confirm what was last flushed.
func (w *World) ReadSnapshot() (Snapshot, error) {
	if w == nil || w.path == "" {
		return Snapshot{}, fmt.Errorf("world: nil")
	}
	data, err := os.ReadFile(w.path)
	if err != nil {
		return Snapshot{}, err
	}
	var s Snapshot
	if err := json.Unmarshal(data, &s); err != nil {
		return s, fmt.Errorf("world: parse snapshot: %w", err)
	}
	return s, nil
}

// run is the single-writer goroutine.
func (w *World) run() {
	defer w.wg.Done()
	ticker := time.NewTicker(w.MinWriteInterval)
	defer ticker.Stop()
	for {
		select {
		case p, ok := <-w.patches:
			if !ok {
				return
			}
			w.applyPatch(p)
		case <-ticker.C:
			w.flushIfDirty()
		case <-w.stop:
			// Drain pending patches FULLY before returning, so a graceful
			// Stop() doesn't lose buffered updates. Each iteration consumes
			// one item; the inner `default` exits the loop once the channel
			// is empty. Worst case N+1 iterations where N = buffered patches.
			for {
				select {
				case p := <-w.patches:
					w.applyPatch(p)
				default:
					return
				}
			}
		}
	}
}

func (w *World) applyPatch(p patch) {
	w.mu.Lock()
	if p.apply != nil {
		p.apply(&w.snap)
	}
	w.snap.UpdatedMS = time.Now().UnixMilli()
	w.dirty = true
	w.mu.Unlock()
}

func (w *World) flushIfDirty() {
	w.mu.RLock()
	dirty := w.dirty
	w.mu.RUnlock()
	if !dirty {
		return
	}
	w.flushNow()
}

func (w *World) flushNow() {
	w.mu.RLock()
	data, err := json.MarshalIndent(&w.snap, "", "  ")
	w.mu.RUnlock()
	if err != nil {
		return
	}
	dir := filepath.Dir(w.path)
	tmp := filepath.Join(dir, fmt.Sprintf(".current_state.%d.tmp", time.Now().UnixNano()))
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return
	}
	if err := os.Rename(tmp, w.path); err != nil {
		_ = os.Remove(tmp)
		return
	}
	w.mu.Lock()
	w.dirty = false
	w.lastWrittenAt = time.Now()
	w.mu.Unlock()
}

// SchemaVersion is bumped when the Snapshot JSON structure changes.
const SchemaVersion = 1

// ---- section helpers (convenience for callers) ---------------------------

// SetState updates CurrentState (and PrevState when it changes). The
// comparison runs INSIDE the writer goroutine's closure so it always
// observes the latest prior value rather than a stale call-site snapshot.
func (w *World) SetState(current string) {
	w.Update("state", func(s *Snapshot) {
		if current != s.CurrentState {
			s.PrevState = s.CurrentState
		}
		s.CurrentState = current
		s.StateAgeMS = 0 // re-derived on the next Snapshot read if needed
	})
}

// SetADB updates the ADB/health section.
func (w *World) SetADB(info ADBSnapshot) {
	cp := info
	w.Update("adb", func(s *Snapshot) { s.ADB = &cp })
}

// SetClassifier updates the classifier verdict section.
func (w *World) SetClassifier(topScore int, alternatives map[string]int, confirm int) {
	w.Update("classifier", func(s *Snapshot) {
		s.Classifier = &ClassifierSnapshot{
			TopScore:      topScore,
			Alternatives:  alternatives,
			ConfirmFrames: confirm,
			UpdatedMS:     time.Now().UnixMilli(),
		}
	})
}

// SetSequence updates the clickSequence section.
func (w *World) SetSequence(running bool, step string, retries int, attackID string) {
	w.Update("sequence", func(s *Snapshot) {
		s.Sequence = &SequenceSnapshot{
			Running:     running,
			CurrentStep: step,
			StepRetries: retries,
			AttackID:    attackID,
		}
	})
}

// SetDeployment updates the red-zone + deploy-line section.
func (w *World) SetDeployment(d DeploymentSnapshot) {
	cp := d
	w.Update("deployment", func(s *Snapshot) { s.Deployment = &cp })
}

// SetSlots replaces the slot list.
func (w *World) SetSlots(slots []*SlotSnapshot) {
	cp := slots
	w.Update("slots", func(s *Snapshot) { s.Slots = cp })
}

// SetStats updates session stats. Uses Update() so the patch is serialized
// through the single writer goroutine — no additional locking needed.
func (w *World) SetStats(s StatsSnapshot) {
	cp := s
	w.Update("stats", func(snap *Snapshot) { snap.Stats = &cp })
}

// RecordTap appends a tap to the rolling window. Older taps are dropped at
// MaxTapsRetained entries.
func (w *World) RecordTap(t TapSnapshot) {
	w.Update("taps", func(s *Snapshot) {
		s.Taps = append(s.Taps, &t)
		if len(s.Taps) > w.MaxTapsRetained {
			drop := len(s.Taps) - w.MaxTapsRetained
			s.Taps = s.Taps[drop:]
		}
	})
}

// SetLastFrame records the latest frame pointer.
func (w *World) SetLastFrame(tsMillis int64, phash, ref string) {
	w.Update("frame", func(s *Snapshot) {
		s.LastFrame = &FrameSnapshot{TSMillis: tsMillis, PHash: phash, Ref: ref}
	})
}

// SetConfig records the active strategy and thresholds.
func (w *World) SetConfig(c ConfigSnapshot) {
	w.Update("config", func(s *Snapshot) { s.Config = &c })
}

// ---- process-wide default ------------------------------------------------

var (
	defaultMu sync.RWMutex
	defaultW  *World
)

// SetDefault registers a process-wide World accessible via Default().
// Pass nil to clear.
func SetDefault(w *World) {
	defaultMu.Lock()
	defaultW = w
	defaultMu.Unlock()
}

// Default returns the process-wide World, or nil if none has been set.
func Default() *World {
	defaultMu.RLock()
	defer defaultMu.RUnlock()
	return defaultW
}
