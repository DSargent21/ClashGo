package adb

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestTapTraceWritesNDJSONPerTap locks the audit contract: one line per tap,
// in order, carrying the fields a tap-count audit needs (type, intended and
// actual coordinates, std dev). If this format drifts, every post-run tap
// audit script drifts with it.
func TestTapTraceWritesNDJSONPerTap(t *testing.T) {
	path := filepath.Join(t.TempDir(), "taps.ndjson")
	tr, err := NewTapTrace(path)
	if err != nil {
		t.Fatalf("NewTapTrace: %v", err)
	}

	tr.Record(TapEvent{Type: "tap_triple_1", X: 100, Y: 200, ActualX: 101, ActualY: 199, StdDev: 12})
	tr.Record(TapEvent{Type: "tap_fast", X: 300, Y: 400, ActualX: 302, ActualY: 401, StdDev: 2})
	if err := tr.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open trace: %v", err)
	}
	defer f.Close()

	var got []TapEvent
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var ev TapEvent
		if err := json.Unmarshal(sc.Bytes(), &ev); err != nil {
			t.Fatalf("line %q is not valid TapEvent JSON: %v", sc.Text(), err)
		}
		got = append(got, ev)
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scan: %v", err)
	}

	if len(got) != 2 {
		t.Fatalf("want 2 recorded taps, got %d", len(got))
	}
	if got[0].Type != "tap_triple_1" || got[0].X != 100 || got[0].Y != 200 || got[0].ActualX != 101 {
		t.Errorf("first event mismatch: %+v", got[0])
	}
	if got[1].Type != "tap_fast" || got[1].StdDev != 2 {
		t.Errorf("second event mismatch: %+v", got[1])
	}
}

// TestTapTraceCloseIsIdempotentAndRecordSafeAfterClose: the trace is closed on
// the bot teardown path, which can run more than once (Stop then a deferred
// close). Recording must degrade to a no-op rather than panic on a nil file.
func TestTapTraceCloseIsIdempotentAndRecordSafeAfterClose(t *testing.T) {
	tr, err := NewTapTrace(filepath.Join(t.TempDir(), "taps.ndjson"))
	if err != nil {
		t.Fatalf("NewTapTrace: %v", err)
	}
	if err := tr.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := tr.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	tr.Record(TapEvent{Type: "tap_fast", X: 1, Y: 2}) // must not panic
}
