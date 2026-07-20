package game

import (
	"encoding/json"
	"image"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"gocv.io/x/gocv"
)

// withFastChestAnimSettle shrinks BOTH chest-related settle sleeps to
// ~1ms for the duration of `t`. Without this the fallback tap-scan
// loop tests would take 3.6s and 18s respectively, and the new
// Skip→Confirm fast-path tests would each take ~1.6s. Both sleeps
// are TEST-MUTABLE package-level vars (see their declarations in
// chestdismiss.go); production paths read only.
//
// Tests still assert "exhausted MaxChestDismissLoops taps" /
// "exactly N Skip+Confirm attempts" which are TAP-COUNT driven, not
// time-driven, so the speed-up doesn't relax those assertions.
func withFastChestAnimSettle(t *testing.T) {
	t.Helper()
	origAnim := ChestAnimSettle
	origSkip := ChestSkipConfirmSettle
	origContinueTaps := chestContinueMaxTaps
	ChestAnimSettle = 1 * time.Millisecond
	ChestSkipConfirmSettle = 1 * time.Millisecond
	chestContinueMaxTaps = 1
	t.Cleanup(func() {
		ChestAnimSettle = origAnim
		ChestSkipConfirmSettle = origSkip
		chestContinueMaxTaps = origContinueTaps
	})
}

// fakeDevice satisfies the game.Device interface. It records every
// TapRandomized call and replays a pre-scripted sequence of Mats on
// each CaptureToMat. Every Mat it creates (initial script + overruns)
// is tracked in `allMats` so the test can release them all on Close.
type fakeDevice struct {
	caps     []gocv.Mat
	capIdx   int
	recorded []image.Point
	allMats  []gocv.Mat
}

func newFakeDevice(numFrames int) *fakeDevice {
	fd := &fakeDevice{}
	for i := 0; i < numFrames; i++ {
		// 1x1 BGR unit Mats — content doesn't matter; the classify
		// closure is mocked to ignore the Mat and return a scripted state.
		m := gocv.NewMatWithSize(1, 1, gocv.MatTypeCV8UC3)
		fd.caps = append(fd.caps, m)
		fd.allMats = append(fd.allMats, m)
	}
	return fd
}

// Close releases every Mat this device created. Tests MUST defer this —
// gocv's bindings don't finalize Mats and a leak here would floor the
// CI's RSS by ~2 MB per test run.
func (f *fakeDevice) Close() error {
	for _, m := range f.allMats {
		if !m.Empty() {
			m.Close()
		}
	}
	f.allMats = nil
	return nil
}

func (f *fakeDevice) Tap(x, y int) error           { f.recorded = append(f.recorded, image.Pt(x, y)); return nil }
func (f *fakeDevice) TapRandomized(x, y int) error { return f.Tap(x, y) }
func (f *fakeDevice) Swipe(x1, y1, x2, y2, ms int) error {
	return nil
}
func (f *fakeDevice) Pinch(x1, y1, x2, y2, x3, y3, x4, y4, ms int) error {
	return nil
}
func (f *fakeDevice) PinchZoom(zoomOut bool) error { return nil }
func (f *fakeDevice) ZoomOut() error               { return nil }
func (f *fakeDevice) ZoomIn() error                { return nil }
func (f *fakeDevice) Hold(x, y, ms int) error      { return nil }
func (f *fakeDevice) KeyEvent(code int) error      { return nil }
func (f *fakeDevice) Text(text string) error       { return nil }
func (f *fakeDevice) Back() error                  { return nil }
func (f *fakeDevice) CaptureToMat() (gocv.Mat, error) {
	if f.capIdx >= len(f.caps) {
		// Give the loop a stable "main village" return when it overshoots.
		// Track the new Mat so Close() can release it.
		m := gocv.NewMatWithSize(1, 1, gocv.MatTypeCV8UC3)
		f.allMats = append(f.allMats, m)
		return m, nil
	}
	m := f.caps[f.capIdx]
	f.capIdx++
	return m, nil
}

// scriptedClassifier returns GameStates from a fixed slice in order.
// On overrun it returns StateMainVillage (the "we're done" answer).
type scriptedClassifier struct {
	states []GameState
	idx    int
}

func (s *scriptedClassifier) classify(_ gocv.Mat) (GameState, int) {
	if s.idx >= len(s.states) {
		return StateMainVillage, 100
	}
	st := s.states[s.idx]
	s.idx++
	return st, 100
}

// makeTestNavigator wires a Navigator with a no-op graph + zero-scale
// cal + scripted classifier. Zero-scale is fine because the loop
// taps a uniformly-random point inside the loaded ROI; with sx=sy=1
// the result is identical, just easier to assert against.
func makeTestNavigator(dev Device, sc *scriptedClassifier) *Navigator {
	cal := &Calibration{PhysicalW: RefWidth, PhysicalH: RefHeight, ScaleX: 1, ScaleY: 1}
	g := NewStateGraph()
	return &Navigator{
		cfg:      DefaultNavigatorConfig(),
		cal:      cal,
		graph:    g,
		client:   dev,
		classify: sc.classify,
		logger:   zerolog.Nop(),
	}
}

func TestChestDismiss_HammerMultiTap(t *testing.T) {
	withFastChestAnimSettle(t)
	// A single tap per iteration never breaks the chest (state stays
	// ChestReward), but with hammer_taps=3 the chest breaks on the
	// first iteration's 3rd tap — so we expect 3 taps total, not the
	// old single-tap path which would loop forever / hit the cap.
	dev := newFakeDevice(5)
	defer dev.Close()
	sc := &scriptedClassifier{
		states: []GameState{StateMainVillage},
	}
	nav := makeTestNavigator(dev, sc)

	cfg := &ChestROISchema{
		TapROI:     &Rectangle{X1: 100, Y1: 200, X2: 300, Y2: 400},
		HammerTaps: 3,
	}

	if err := nav.dismissChestRewardWithCfg(cfg, nil); err != nil {
		t.Fatalf("hammer path expected nil, got: %v", err)
	}
	if got := len(dev.recorded); got != 3 {
		t.Errorf("expected 3 taps (hammer_taps=3 on first iter), got %d", got)
	}
}
func TestChestDismiss_HappyPath(t *testing.T) {
	withFastChestAnimSettle(t)
	dev := newFakeDevice(3)
	defer dev.Close()
	sc := &scriptedClassifier{
		states: []GameState{StateChestReward, StateChestReward, StateMainVillage},
	}
	nav := makeTestNavigator(dev, sc)

	// Drive the testable core directly with no Continue overlay so
	// the test is independent of whatever's on disk in
	// assets/continue_button.json. The public DismissChestReward()
	// would load it; the testable core accepts the rect explicitly.
	cfg := &ChestROISchema{
		TapROI: &Rectangle{X1: 100, Y1: 200, X2: 300, Y2: 400},
	}

	start := time.Now()
	if err := nav.dismissChestRewardWithCfg(cfg, nil); err != nil {
		t.Fatalf("happy path expected nil, got: %v", err)
	}
	elapsed := time.Since(start)

	if got := len(dev.recorded); got != 3 {
		t.Errorf("expected 3 taps, got %d", got)
	}
	// With ChestAnimSettle=1ms the happy path should finish in <500ms
	// — a generous bound that still fails loudly under any regression.
	if elapsed > 500*time.Millisecond {
		t.Errorf("happy path took %s, expected well under 500ms with fast settle", elapsed)
	}
}

func TestChestDismiss_CircuitBreaker(t *testing.T) {
	withFastChestAnimSettle(t)
	dev := newFakeDevice(MaxChestDismissLoops + 5)
	defer dev.Close()
	sc := &scriptedClassifier{}
	for i := 0; i < MaxChestDismissLoops+5; i++ {
		sc.states = append(sc.states, StateChestReward)
	}
	nav := makeTestNavigator(dev, sc)

	// No Continue overlay: the tap-scan loop's circuit-breaker is the
	// only thing that should fire here. Bypass the on-disk load.
	cfg := &ChestROISchema{
		TapROI: &Rectangle{X1: 100, Y1: 200, X2: 300, Y2: 400},
	}
	err := nav.dismissChestRewardWithCfg(cfg, nil)
	if err == nil {
		t.Fatalf("circuit breaker expected error, got nil")
	}
	if got := len(dev.recorded); got != MaxChestDismissLoops {
		t.Errorf("expected exactly %d taps (the cap), got %d",
			MaxChestDismissLoops, got)
	}
}

func TestChestDismiss_ConfigLoader_Missing(t *testing.T) {
	// The loader is hard-wired to assets/chest_dismiss_roi.json so the
	// test exercises "absent OR valid" — both are accepted runtime
	// states. Without this baseline the test would fail when the
	// file is committed (project ships a default ROI).
	cfg, err := LoadChestDismissConfig()
	if err != nil {
		t.Fatalf("loader error: %v", err)
	}
	if cfg != nil && cfg.TapROI == nil {
		t.Errorf("present config must have non-nil TapROI: %+v", cfg)
	}
}

func TestChestDismiss_RectValid(t *testing.T) {
	cases := []struct {
		r    *Rectangle
		want bool
	}{
		{nil, false},
		{&Rectangle{X1: -1, Y1: 0, X2: 10, Y2: 10}, false},
		{&Rectangle{X1: 0, Y1: 0, X2: 0, Y2: 10}, false},
		{&Rectangle{X1: 100, Y1: 100, X2: 50, Y2: 200}, false},
		{&Rectangle{X1: 100, Y1: 100, X2: 100, Y2: 200}, false},
		{&Rectangle{X1: 0, Y1: 0, X2: RefWidth, Y2: RefHeight}, true},
		{&Rectangle{X1: 300, Y1: 280, X2: 530, Y2: 480}, true},
	}
	for i, tc := range cases {
		if got := tc.r.isValid(); got != tc.want {
			t.Errorf("case %d: isValid(%v) = %v, want %v", i, tc.r, got, tc.want)
		}
	}
}

func TestChestDismiss_RandomPointInRect(t *testing.T) {
	r := Rectangle{X1: 100, Y1: 200, X2: 300, Y2: 400}
	for i := 0; i < 1000; i++ {
		x, y := randomPointInRect(r)
		if x < r.X1 || x > r.X2 || y < r.Y1 || y > r.Y2 {
			t.Fatalf("randomPointInRect produced (%d,%d) outside %v", x, y, r)
		}
	}
	// Degenerate rects must NOT panic; they should pin to the corner.
	for _, deg := range []Rectangle{
		{X1: 5, Y1: 5, X2: 5, Y2: 5},
		{X1: 9, Y1: 5, X2: 3, Y2: 5},
	} {
		x, y := randomPointInRect(deg)
		if x != deg.X1 || y != deg.Y1 {
			t.Errorf("degenerate %v produced (%d,%d), expected (%d,%d)",
				deg, x, y, deg.X1, deg.Y1)
		}
	}
}

func TestChestDismiss_SchemaRoundtrip(t *testing.T) {
	// Author a temp JSON matching the schema and verify the loader
	// parses it. Avoids touching real assets/ from the test. Covers
	// all four optional fields so a future addition (or removal)
	// that breaks JSON roundtrip will fail loudly here.
	dir := t.TempDir()
	p := filepath.Join(dir, "chest_dismiss_roi.json")
	payload := ChestROISchema{
		TapROI:           &Rectangle{X1: 10, Y1: 20, X2: 110, Y2: 220},
		TapROIAlt:        &Rectangle{X1: 30, Y1: 40, X2: 130, Y2: 240},
		SkipButton:       &Rectangle{X1: 200, Y1: 600, X2: 320, Y2: 660},
		ConfirmYesButton: &Rectangle{X1: 380, Y1: 480, X2: 480, Y2: 540},
	}
	blob, _ := json.MarshalIndent(payload, "", "  ")
	if err := os.WriteFile(p, blob, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var decoded ChestROISchema
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded.TapROI == nil || *decoded.TapROI != *payload.TapROI {
		t.Errorf("TapROI roundtrip mismatch: got %+v, want %+v",
			decoded.TapROI, payload.TapROI)
	}
	if decoded.TapROIAlt == nil || *decoded.TapROIAlt != *payload.TapROIAlt {
		t.Errorf("TapROIAlt roundtrip mismatch: got %+v, want %+v",
			decoded.TapROIAlt, payload.TapROIAlt)
	}
	if decoded.SkipButton == nil || *decoded.SkipButton != *payload.SkipButton {
		t.Errorf("SkipButton roundtrip mismatch: got %+v, want %+v",
			decoded.SkipButton, payload.SkipButton)
	}
	if decoded.ConfirmYesButton == nil || *decoded.ConfirmYesButton != *payload.ConfirmYesButton {
		t.Errorf("ConfirmYesButton roundtrip mismatch: got %+v, want %+v",
			decoded.ConfirmYesButton, payload.ConfirmYesButton)
	}
}

// TestChestDismiss_SkipConfirmHappyPath: when both SkipButton and
// ConfirmYesButton are configured AND the verify capture shows a
// non-chest state on the first attempt, the Skip→Confirm path runs
// exactly ONCE (2 tap calls total: skip + confirm-yes), no
// tap-scan fallback is entered, and the verify capture returns
// home.
//
// Drives the engine via dismissChestRewardWithCfg (unexported) so
// we don't have to write a config file to disk for each test.
func TestChestDismiss_SkipConfirmHappyPath(t *testing.T) {
	withFastChestAnimSettle(t)
	dev := newFakeDevice(2)
	defer dev.Close()
	// scripted: 1st capture (verify after skip+confirm) → home.
	sc := &scriptedClassifier{
		states: []GameState{StateMainVillage, StateMainVillage},
	}
	nav := makeTestNavigator(dev, sc)

	cfg := &ChestROISchema{
		TapROI:           &Rectangle{X1: 100, Y1: 200, X2: 300, Y2: 400},
		SkipButton:       &Rectangle{X1: 200, Y1: 600, X2: 280, Y2: 660},
		ConfirmYesButton: &Rectangle{X1: 380, Y1: 480, X2: 480, Y2: 540},
	}
	if err := nav.dismissChestRewardWithCfg(cfg, nil); err != nil {
		t.Fatalf("skip+confirm expected nil, got: %v", err)
	}
	// Exactly 2 taps: one Skip, one Confirm Yes. No fallback loop.
	if got := len(dev.recorded); got != 2 {
		t.Errorf("expected 2 taps (Skip + Confirm), got %d", got)
	}
	// Exactly 1 capture call: the verify capture after Skip+Confirm.
	if got := dev.capIdx; got != 1 {
		t.Errorf("expected 1 capture call (verify only), got %d", got)
	}
}

// TestChestDismiss_SkipConfirmFailsThenTapScan: when Skip→Confirm
// fails on its single attempt (state stays StateChestReward on
// verify), the engine falls back to the tap-scan loop with the
// trimmed post-Skip-fail budget (chestTapScanBudgetAfterSkipFail = 5).
//
// tryChestSkipFlow is single-attempt by design (a second Skip tap on
// a moving target misfires into other UI). Worst-case cost on failure
// is therefore 1 attempt × (2 taps + 1 capture) = 2 taps + 1 capture,
// plus the tap-scan loop driving to home.
//
// scripted sequence:
//
//	capture 1 = skip attempt 1 verify → StateChestReward (failed)
//	capture 2 = tap-scan iter 0 verify → StateChestReward
//	capture 3 = tap-scan iter 1 verify → StateChestReward
//	capture 4 = tap-scan iter 2 verify → StateMainVillage (success)
func TestChestDismiss_SkipConfirmFailsThenTapScan(t *testing.T) {
	withFastChestAnimSettle(t)
	dev := newFakeDevice(5)
	defer dev.Close()
	sc := &scriptedClassifier{
		states: []GameState{
			StateChestReward, // 1 = skip verify (failed)
			StateChestReward, // 2 = tap-scan iter 0 verify
			StateChestReward, // 3 = tap-scan iter 1 verify
			StateMainVillage, // 4 = tap-scan iter 2 verify (success)
		},
	}
	nav := makeTestNavigator(dev, sc)

	cfg := &ChestROISchema{
		TapROI:           &Rectangle{X1: 100, Y1: 200, X2: 300, Y2: 400},
		SkipButton:       &Rectangle{X1: 200, Y1: 600, X2: 280, Y2: 660},
		ConfirmYesButton: &Rectangle{X1: 380, Y1: 480, X2: 480, Y2: 540},
	}
	if err := nav.dismissChestRewardWithCfg(cfg, nil); err != nil {
		t.Fatalf("expected fall-back to succeed, got: %v", err)
	}
	// 2 (Skip + Confirm) + 3 (tap-scan × 3 iterations before home
	// is detected) = 5 taps.
	if got, want := len(dev.recorded), 5; got != want {
		t.Errorf("expected %d taps (2 skip-path + 3 tap-scan), got %d", want, got)
	}
	// 4 captures: 1 skip verify + 3 tap-scan verifies.
	if got, want := dev.capIdx, 4; got != want {
		t.Errorf("expected %d capture calls, got %d", want, got)
	}
}

// TestChestDismiss_NoButtonsSkipsSkipPath: when SkipButton and
// ConfirmYesButton are absent, the engine must NOT attempt the Skip
// fast path (no taps of skip/confirm geometry) and must go straight
// to the tap-scan fallback.
//
// Drives a no-buttons config inline; the engine should match the
// legacy tap-scan-only behavior.
func TestChestDismiss_NoButtonsSkipsSkipPath(t *testing.T) {
	withFastChestAnimSettle(t)
	dev := newFakeDevice(10)
	defer dev.Close()
	// scripted: chest, chest, chest, then home — 4 captures needed
	// (3 chest verifies + 1 home verify). The engine taps once per
	// capture-preceding call, so we end up with 4 taps (1 per iter)
	// before returning.
	sc := &scriptedClassifier{
		states: []GameState{StateChestReward, StateChestReward, StateChestReward, StateMainVillage},
	}
	nav := makeTestNavigator(dev, sc)

	cfg := &ChestROISchema{
		TapROI: &Rectangle{X1: 100, Y1: 200, X2: 300, Y2: 400},
		// SkipButton + ConfirmYesButton deliberately absent.
	}
	if err := nav.dismissChestRewardWithCfg(cfg, nil); err != nil {
		t.Fatalf("expected fall-back to succeed, got: %v", err)
	}
	// 4 taps total (1 per tap-scan iter × 4 iters before home).
	if got := len(dev.recorded); got != 4 {
		t.Errorf("expected 4 taps (tap-scan only, 4 iters to home), got %d", got)
	}
	// 4 captures (one per iter).
	if got, want := dev.capIdx, 4; got != want {
		t.Errorf("expected %d capture calls, got %d", want, got)
	}
}

// TestChestDismiss_DisableFlagNoOp: when the runtime kill-switch
// (SetDisableChestDismissal) is engaged, DismissChestReward must
// short-circuit BEFORE any config load, classifier roundtrip, or
// tap call. This guards against the kill-switch silently regressing
// into a no-op (which the user wouldn't notice until the chest
// recovery burned a wall-clock budget).
//
// Asserts: zero taps, zero captures, nil error.
func TestChestDismiss_DisableFlagNoOp(t *testing.T) {
	withFastChestAnimSettle(t)
	dev := newFakeDevice(2)
	defer dev.Close()
	// Even if the classifier would return chest repeatedly, the
	// kill-switch must bypass the entire flow — no captures should
	// be requested at all.
	sc := &scriptedClassifier{
		states: []GameState{StateChestReward, StateChestReward},
	}
	nav := makeTestNavigator(dev, sc)
	nav.SetDisableChestDismissal(true)

	if err := nav.DismissChestReward(); err != nil {
		t.Fatalf("disabled kill-switch should return nil, got: %v", err)
	}
	if got := len(dev.recorded); got != 0 {
		t.Errorf("expected 0 taps when disabled, got %d", got)
	}
	if got := dev.capIdx; got != 0 {
		t.Errorf("expected 0 captures when disabled, got %d", got)
	}

	// Roundtrip: re-enable the kill-switch on a fresh navigator and
	// verify the recovery flow runs normally. Guards against the
	// setter leaving the navigator in a stuck "always-disabled" state.
	dev2 := newFakeDevice(4)
	defer dev2.Close()
	sc2 := &scriptedClassifier{
		states: []GameState{
			StateChestReward,
			StateChestReward,
			StateChestReward,
			StateMainVillage,
		},
	}
	nav2 := makeTestNavigator(dev2, sc2)
	nav2.SetDisableChestDismissal(false)
	if err := nav2.DismissChestReward(); err != nil {
		t.Fatalf("re-enabled kill-switch expected happy path, got: %v", err)
	}
}

// TestChestDismiss_ContinueHappyPath: after the tap-scan loop
// succeeds (state changes away from chest), the chestContinueTap
// step fires, taps the configured Continue rect once, and verifies
// the classifier sees StateMainVillage.
//
// scripted sequence:
//
//	capture 1 = tap-scan iter 0 verify → StateMainVillage (chest dismissed)
//	capture 2 = Continue verify → StateMainVillage (overlay dismissed)
//
// Asserts: 1 (tap-scan) + 1 (continue) = 2 taps; 2 captures.
func TestChestDismiss_ContinueHappyPath(t *testing.T) {
	withFastChestAnimSettle(t)
	dev := newFakeDevice(3)
	defer dev.Close()
	sc := &scriptedClassifier{
		states: []GameState{
			StateMainVillage, // 1 = tap-scan iter 0 verify (chest dismissed)
			StateMainVillage, // 2 = Continue verify (overlay dismissed)
			StateMainVillage, // overrun cushion
		},
	}
	nav := makeTestNavigator(dev, sc)

	cfg := &ChestROISchema{
		TapROI: &Rectangle{X1: 100, Y1: 200, X2: 300, Y2: 400},
	}
	continueRect := &Rectangle{X1: 369, Y1: 502, X2: 492, Y2: 542}

	if err := nav.dismissChestRewardWithCfg(cfg, continueRect); err != nil {
		t.Fatalf("expected Continue happy path, got: %v", err)
	}
	// 1 (tap-scan) + 1 (continue) = 2 taps.
	if got := len(dev.recorded); got != 2 {
		t.Errorf("expected 2 taps (1 tap-scan + 1 continue), got %d", got)
	}
	// 2 captures: 1 tap-scan verify + 1 continue verify.
	if got, want := dev.capIdx, 2; got != want {
		t.Errorf("expected %d capture calls, got %d", want, got)
	}
}

// TestChestDismiss_ContinueNoConfig: when continueRect is nil, the
// chestContinueTap step is a no-op — the flow ends at the tap-scan
// loop's success.
//
// scripted sequence:
//
//	capture 1 = tap-scan iter 0 verify → StateMainVillage (chest dismissed)
//
// Asserts: 1 tap, 1 capture (continue step NOT executed).
func TestChestDismiss_ContinueNoConfig(t *testing.T) {
	withFastChestAnimSettle(t)
	dev := newFakeDevice(2)
	defer dev.Close()
	sc := &scriptedClassifier{
		states: []GameState{StateMainVillage, StateMainVillage},
	}
	nav := makeTestNavigator(dev, sc)

	cfg := &ChestROISchema{
		TapROI: &Rectangle{X1: 100, Y1: 200, X2: 300, Y2: 400},
	}
	if err := nav.dismissChestRewardWithCfg(cfg, nil); err != nil {
		t.Fatalf("expected happy path without Continue, got: %v", err)
	}
	// 1 tap (tap-scan only — no Continue step fired).
	if got := len(dev.recorded); got != 1 {
		t.Errorf("expected 1 tap (tap-scan only), got %d", got)
	}
	// 1 capture (tap-scan verify — no Continue verify).
	if got, want := dev.capIdx, 1; got != want {
		t.Errorf("expected %d capture calls, got %d", want, got)
	}
} // TestChestDismiss_ContinueFails: when the Continue tap fires but
// the classifier still sees a non-MainVillage state on verify, the
// engine returns an error so the Navigator's chestCascadeCount
// escalation kicks in.
//
// scripted sequence:
//
//	capture 1 = tap-scan iter 0 verify → StateMainVillage (chest dismissed, OK)
//	capture 2 = Continue verify → StateLoading (FAIL — not MainVillage)
//
// We reuse scriptedClassifier (which ignores score, returning 100)
// because chestContinueTap only branches on state, not score.
func TestChestDismiss_ContinueFails(t *testing.T) {
	withFastChestAnimSettle(t)
	dev := newFakeDevice(3)
	defer dev.Close()
	sc := &scriptedClassifier{
		states: []GameState{
			StateMainVillage, // 1 = tap-scan iter 0 verify (chest dismissed)
			StateLoading,     // 2 = Continue verify (FAIL — not MainVillage)
			StateLoading,     // overrun cushion
		},
	}
	nav := makeTestNavigator(dev, sc)

	cfg := &ChestROISchema{
		TapROI: &Rectangle{X1: 100, Y1: 200, X2: 300, Y2: 400},
	}
	continueRect := &Rectangle{X1: 369, Y1: 502, X2: 492, Y2: 542}

	err := nav.dismissChestRewardWithCfg(cfg, continueRect)
	if err == nil {
		t.Fatalf("expected Continue failure, got nil")
	}
	// 1 (tap-scan) + 1 (continue) = 2 taps.
	if got := len(dev.recorded); got != 2 {
		t.Errorf("expected 2 taps (1 tap-scan + 1 continue), got %d", got)
	}
	// 3 captures: 1 tap-scan verify + 1 continue verify + 1 failure dump.
	if got, want := dev.capIdx, 3; got != want {
		t.Errorf("expected %d capture calls, got %d", want, got)
	}
}
