package main

import (
	"os"
	"testing"
	"time"

	"github.com/Ducky705/ClashGO/internal/paths"
)

func TestSessionRecordsRoundTrip(t *testing.T) {
	t.Setenv("CLASHGO_CONFIG_DIR", t.TempDir())

	start := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	end := start.Add(90 * time.Minute)
	appendSessionRecord(start, end)

	records := loadSessionRecords()
	if len(records) != 1 {
		t.Fatalf("loaded %d session records, want 1", len(records))
	}
	gotStart, gotEnd, ok := records[0].parse()
	if !ok {
		t.Fatal("written session record does not parse")
	}
	if !gotStart.Equal(start) || !gotEnd.Equal(end) {
		t.Errorf("session = %v..%v, want %v..%v", gotStart, gotEnd, start, end)
	}
}

func TestSessionRecordsSkipEmptyRuns(t *testing.T) {
	t.Setenv("CLASHGO_CONFIG_DIR", t.TempDir())

	start := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	appendSessionRecord(time.Time{}, start)
	appendSessionRecord(start, start)
	appendSessionRecord(start, start.Add(-time.Minute))

	if records := loadSessionRecords(); len(records) != 0 {
		t.Errorf("wrote %d records for empty runs, want 0", len(records))
	}
	if _, err := os.Stat(paths.ResolveConfig("sessions.json")); !os.IsNotExist(err) {
		t.Errorf("sessions.json exists after only empty runs (err=%v)", err)
	}
}

func TestSessionRecordsKeepLatestHundred(t *testing.T) {
	t.Setenv("CLASHGO_CONFIG_DIR", t.TempDir())

	start := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	for i := 0; i < sessionHistoryLimit+5; i++ {
		at := start.Add(time.Duration(i) * time.Hour)
		appendSessionRecord(at, at.Add(30*time.Minute))
	}

	records := loadSessionRecords()
	if len(records) != sessionHistoryLimit {
		t.Fatalf("kept %d session records, want %d", len(records), sessionHistoryLimit)
	}
	gotStart, _, ok := records[len(records)-1].parse()
	if !ok {
		t.Fatal("newest session record does not parse")
	}
	if want := start.Add(time.Duration(sessionHistoryLimit+4) * time.Hour); !gotStart.Equal(want) {
		t.Errorf("last session starts %v, want newest %v", gotStart, want)
	}
}
