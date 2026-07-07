package world

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWorld_StartWritesInitialSnapshot(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "current_state.json")

	w := New(path)
	w.Start()
	t.Cleanup(w.Stop)

	// Allow one tick to flush.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("expected current_state.json: %v", err)
	}
	var s Snapshot
	if err := json.Unmarshal(data, &s); err != nil {
		t.Fatalf("parse snapshot: %v", err)
	}
	if s.SchemaVersion != SchemaVersion {
		t.Errorf("schema_version: got %d, want %d", s.SchemaVersion, SchemaVersion)
	}
	if s.BootID == "" {
		t.Errorf("expected non-empty boot_id")
	}
	if s.UpdatedMS == 0 {
		t.Errorf("expected updated_ms to be set")
	}
}

func TestWorld_UpdateCoalescesAndFlushes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "current_state.json")

	w := New(path)
	w.MinWriteInterval = 50 * time.Millisecond
	w.Start()
	t.Cleanup(w.Stop)

	w.SetState("MAIN_VILLAGE")
	w.SetClassifier(87, map[string]int{"MAIN_VILLAGE": 87, "BATTLE": 4}, 2)
	w.SetADB(ADBSnapshot{Connected: true, Device: "localhost:5555", Resolution: "860x732"})

	// Wait for at least one flush.
	time.Sleep(200 * time.Millisecond)
	w.mu.RLock()
	written := !w.dirty
	w.mu.RUnlock()
	if !written {
		t.Errorf("expected world to have flushed by now")
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read snapshot: %v", err)
	}
	if !strings.Contains(string(data), "MAIN_VILLAGE") {
		t.Errorf("snapshot missing state: %s", string(data))
	}
	if !strings.Contains(string(data), "127.0.0.1") && !strings.Contains(string(data), "localhost:5555") {
		t.Errorf("snapshot missing adb device id: %s", string(data))
	}
	if !strings.Contains(string(data), "top_score") {
		t.Errorf("snapshot missing classifier top_score: %s", string(data))
	}
}

func TestWorld_UpdateDropsOnOverflowAndIsNonBlocking(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "current_state.json")
	w := New(path)
	// Force overflow: tiny patch buffer + long flush interval.
	w.patches = make(chan patch, 4)
	w.MinWriteInterval = 10 * time.Second
	w.Start()
	t.Cleanup(w.Stop)

	// First 4 should all succeed (within the buffer).
	successes := 0
	for i := 0; i < 4; i++ {
		if w.Update("stats", func(s *Snapshot) {
			s.Stats = &StatsSnapshot{Stars: int32(i)}
		}) {
			successes++
		}
	}
	if successes != 4 {
		t.Errorf("first 4 updates should succeed (cap=4); got %d", successes)
	}

	// Now flood past the buffer; Update must remain non-blocking.
	start := time.Now()
	for i := 0; i < 2000; i++ {
		w.Update("stats", func(s *Snapshot) {
			s.Stats = &StatsSnapshot{Stars: int32(i)}
		})
	}
	elapsed := time.Since(start)
	if elapsed > 250*time.Millisecond {
		t.Errorf("Update() blocked under overflow: 2000 calls took %v", elapsed)
	}
}

func TestWorld_RecordTapRetainsRollingWindow(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "current_state.json")

	w := New(path)
	w.MaxTapsRetained = 8
	w.Start()
	t.Cleanup(w.Stop)

	for i := 0; i < 50; i++ {
		w.RecordTap(TapSnapshot{
			Millis: int64(i),
			Name:   "btn_attack",
			Tier:   "pinpoint",
			OK:     true,
		})
	}

	// Force a flush via Stop.
	w.Stop()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var s Snapshot
	if err := json.Unmarshal(data, &s); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(s.Taps) != 8 {
		t.Errorf("taps retained: got %d, want %d", len(s.Taps), 8)
	}
	// The most recent taps must be 49..42 (rolling window).
	for i, tap := range s.Taps {
		want := int64(42 + i)
		if tap.Millis != want {
			t.Errorf("tap[%d].ms = %d, want %d", i, tap.Millis, want)
		}
	}
}

func TestWorld_UpdateImmediateBypassesCoalescing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "current_state.json")
	w := New(path)
	// Do NOT call Start; UpdateImmediate should still write to disk.
	t.Cleanup(w.Stop)

	w.UpdateImmediate("stats", func(s *Snapshot) {
		s.Stats = &StatsSnapshot{AttacksCompleted: 14}
	})

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("expected immediate flush: %v", err)
	}
	var s Snapshot
	if err := json.Unmarshal(data, &s); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if s.Stats == nil || s.Stats.AttacksCompleted != 14 {
		t.Errorf("stats.attacks_completed = %v, want 14", s.Stats)
	}
}

func TestWorld_SnapshotIsDeepCopy(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "current_state.json")
	w := New(path)
	w.Start()
	t.Cleanup(w.Stop)

	w.UpdateImmediate("slots", func(s *Snapshot) {
		s.Slots = []*SlotSnapshot{{Idx: 0, X: 100, Category: "Troop", UnitHint: "Balloon"}}
	})

	a := w.Snapshot()
	a.Slots[0].X = 999

	b := w.Snapshot()
	if b.Slots[0].X == 999 {
		t.Errorf("Snapshot() returned aliased slice — must deep-copy")
	}
}

func TestWorld_DefaultGlobal(t *testing.T) {
	// Save and restore global.
	old := Default()
	SetDefault(nil)
	if Default() != nil {
		t.Errorf("expected nil default after SetDefault(nil)")
	}
	SetDefault(old)
	if Default() != old {
		t.Errorf("expected to restore previous default")
	}
}
