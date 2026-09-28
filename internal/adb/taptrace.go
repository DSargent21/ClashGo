package adb

import (
	"encoding/json"
	"os"
	"sync"
)

// TapTrace appends every tap the client fires to a file as NDJSON, one
// TapEvent per line, via the client's tap hook (client.SetTapHook).
//
// Why this exists: the tap stream is the bot's raw output, and the failure
// modes that matter operationally live in it and nowhere else — "did the hero
// slot take one field tap or three?", "did the siege machine get retapped
// after it already deployed?", "is a reconcile loop re-tapping a card that is
// already drained?". The structured attack logs describe intent (which unit
// the bot meant to drop); this records what actually went to the device,
// in order, with timestamps.
//
// It is deliberately off by default and attaches only when
// CLASHGO_TAP_TRACE names a file (see internal/bot). When the env var is
// unset, not a single line is written and no hook is installed, so normal
// runs pay nothing.
type TapTrace struct {
	mu  sync.Mutex
	f   *os.File
	enc *json.Encoder
}

// NewTapTrace creates (truncating) path and returns a trace ready for
// SetTapHook. The caller owns Close.
func NewTapTrace(path string) (*TapTrace, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return nil, err
	}
	return &TapTrace{f: f, enc: json.NewEncoder(f)}, nil
}

// Record is the SetTapHook callback. One encoded line into the OS page cache
// is cheap enough to run on the tap path, and an I/O error is swallowed
// rather than propagated into the attack: a debug trace must never be able to
// break a deploy.
func (t *TapTrace) Record(ev TapEvent) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.enc == nil {
		return
	}
	_ = t.enc.Encode(ev)
}

// Close flushes and closes the underlying file. Safe to call more than once.
func (t *TapTrace) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.f == nil {
		return nil
	}
	f := t.f
	t.f, t.enc = nil, nil
	return f.Close()
}
