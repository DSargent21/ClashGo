package attack

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/Ducky705/ClashGO/internal/paths"
)

// rotationOrder is the cycle of target edges for the "Rotate" YAML mode.
// Top → Right → Bottom → Left so consecutive attacks alternate top/bottom
// (visually distinct on a Battle Log timeline).
var rotationOrder = []string{"TopLeft", "TopRight", "BottomRight", "BottomLeft"}

// RotationState is the on-disk schema for the persistent rotation index.
// Survives process restarts so the bot distributes attacks evenly across
// the 4 sides over time (not "always start at TopLeft on launch").
type RotationState struct {
	LastIndex int `json:"last_index"`
}

const rotationStateFile = "rotation_state.json"

// rotationMu serializes read+write to rotation_state.json across the
// orchestrator's many parallel goroutines (multi-attack sessions run
// in a goroutine each). The in-process mutex is sufficient: if the user
// ever runs two ClashGO instances against the same assets dir, worst
// case is that one process double-advances the counter (last-write-wins)
// — both instances still cycle through 4 sides, just in a different
// order. Cross-process locking via flock(2) would block CI smoke runs
// and add platform-specific code for no real win.
var rotationMu sync.Mutex

// lastIndexInMem is the package-level in-memory rotation index. It
// starts at -1 (uninitialized) and is advanced on every NextEdgeIndex
// call regardless of whether the on-disk write succeeds. This guards
// against a read-only config dir (e.g. CI mounts, frozen sandboxes):
// without it, every call would re-read the stale disk value and
// compute the same `next=0`, freezing the bot on TopLeft forever.
// The in-memory value is read on every subsequent call within the
// same process; on process restart we re-read from disk.
var lastIndexInMem int = -1

// NextEdgeIndex atomically advances the persistent rotation counter and
// returns the next corner name in the cycle.
//
// Failure modes (all degraded gracefully, never panic, never block):
//   - File missing: first call returns index 0 (TopLeft) and persists
//     the new state.
//   - File empty: same as missing.
//   - File corrupted (invalid JSON): same as missing; the next call
//     overwrites with valid JSON.
//   - File with out-of-range LastIndex (e.g. -1, 99): defensively reset
//     to 0, then advance normally — no crash on weird persistent state.
//   - File write error (disk full, permission denied): log to stderr,
//     still return the computed next index. Cross-restart continuity
//     is lost in this case but the bot keeps cycling.
//
// The call is wrapped in rotationMu.Lock/Unlock so concurrent
// invocations from the bot's parallel attack goroutines don't drop
// increments or interleave read/write.
func NextEdgeIndex() string {
	rotationMu.Lock()
	defer rotationMu.Unlock()

	// Use the in-memory index as the source of truth within a process
	// run. On the first call (-1 sentinel) we hydrate from disk; on
	// every subsequent call we already know the latest position. This
	// means a write failure does NOT freeze the rotation — the next
	// call still advances from the in-memory value.
	last := lastIndexInMem
	if last < 0 {
		path := filepath.Join(paths.GetConfigDir(), rotationStateFile)
		// Default: -1 so first call advances to 0.
		last = -1
		data, err := os.ReadFile(path)
		if err == nil && len(data) > 0 {
			var st RotationState
			if jerr := json.Unmarshal(data, &st); jerr == nil {
				// Defensive: only accept indices inside [0, len-1].
				if st.LastIndex >= 0 && st.LastIndex < len(rotationOrder) {
					last = st.LastIndex
				}
			}
			// else: corrupted JSON; fall through with last=-1.
		}
	}

	next := (last + 1) % len(rotationOrder)
	// ALWAYS advance the in-memory value, even if the on-disk write
	// below fails. This is the read-only-mount / disk-full safety net.
	lastIndexInMem = next

	out, _ := json.Marshal(RotationState{LastIndex: next})
	path := filepath.Join(paths.GetConfigDir(), rotationStateFile)
	if werr := os.WriteFile(path, out, 0644); werr != nil {
		// Degrade gracefully: still return the computed value.
		// In-memory state has already advanced, so subsequent calls
		// in this process keep cycling. Cross-restart continuity is
		// lost but the bot still makes forward progress.
		fmt.Fprintf(os.Stderr, "rotation state write failed: %v (path=%s)\n", werr, path)
	}
	return rotationOrder[next]
}
