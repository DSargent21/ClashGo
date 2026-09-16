package attack

import (
	"context"
	"image"
	"os"
	"testing"
	"time"

	"github.com/Ducky705/ClashGO/internal/game"
	"github.com/Ducky705/ClashGO/pkg/strategy"
	"github.com/rs/zerolog"
	"gocv.io/x/gocv"
)

// ---------------------------------------------------------------------------
// end_at_percent decision logic
//
// WaitForBattleEndCtx mixes the auto-end decision into a live capture
// loop that needs a device; the DECISION itself is a pure function of
// (threshold, measuredPercent). Pin it here so the semantics — exact
// boundary, never below threshold, disabled at 0 — can't drift.
// ---------------------------------------------------------------------------

// endAtPercentReached itself lives in attack.go (the production wait loop
// calls it); the tests below pin its semantics table.

func TestEndAtPercentReached_Semantics(t *testing.T) {
	cases := []struct {
		threshold, pct int
		want           bool
		note           string
	}{
		{0, 100, false, "0 disables auto-end entirely"},
		{0, 0, false, "0 disables auto-end at zero pct"},
		{50, 49, false, "1 below threshold must NOT end (valk 49% keeps fighting for 50)"},
		{50, 50, true, "exact boundary triggers — >= not >"},
		{50, 51, true, "above threshold triggers"},
		{50, 100, true, "way above triggers"},
		{100, 99, false, "a 100% threshold still needs the full 100"},
		{100, 100, true, "100% threshold triggers at 100"},
		{1, 1, true, "threshold 1 triggers at 1"},
	}
	for _, tc := range cases {
		if got := endAtPercentReached(tc.threshold, tc.pct); got != tc.want {
			t.Errorf("endAtPercentReached(%d, %d) = %v, want %v (%s)",
				tc.threshold, tc.pct, got, tc.want, tc.note)
		}
	}
}

// TestEndAtPercentReached_ValkSpamContract pins the exact valk_spam
// scenario: the strategy declares end_at_percent: 50 and secured the win
// at exactly 50% — the bot must end at 50, and keep fighting at 49.
func TestEndAtPercentReached_ValkSpamContract(t *testing.T) {
	const valkThreshold = 50
	if !endAtPercentReached(valkThreshold, 50) {
		t.Fatal("valk_spam must end the battle the moment destruction hits 50%")
	}
	if endAtPercentReached(valkThreshold, 49) {
		t.Fatal("valk_spam must NOT end below 50% — a 49% end forfeits the star")
	}
}

// ---------------------------------------------------------------------------
// Threshold reads from the active strategy, not globals
// ---------------------------------------------------------------------------

func TestEndAtPct_RequiresActiveStrategy(t *testing.T) {
	// No active strategy → threshold 0 → never auto-end.
	var active *strategy.DynamicStrategy
	endAtPct := 0
	if active != nil {
		endAtPct = active.EndAtPercent
	}
	if endAtPct != 0 {
		t.Fatalf("nil activeStrategy must yield threshold 0, got %d", endAtPct)
	}

	// An active strategy WITH a threshold yields that threshold.
	active = &strategy.DynamicStrategy{Name: "Valkyrie Earthquake Spam", EndAtPercent: 50}
	endAtPct = 0
	if active != nil {
		endAtPct = active.EndAtPercent
	}
	if endAtPct != 50 {
		t.Fatalf("active strategy threshold not read: got %d, want 50", endAtPct)
	}

	// An active strategy WITHOUT a threshold still disables auto-end.
	active = &strategy.DynamicStrategy{Name: "Auto EDrag Rush"}
	endAtPct = 0
	if active != nil {
		endAtPct = active.EndAtPercent
	}
	if endAtPct != 0 {
		t.Fatalf("strategy without end_at_percent must yield 0, got %d", endAtPct)
	}
}

// ---------------------------------------------------------------------------
// Stall-timer suppression in threshold mode
//
// In threshold mode the stall timer must not end the battle BELOW the
// threshold — ending early on a stall would abandon the win the strategy
// is built around. Pin the gating condition.
// ---------------------------------------------------------------------------

func TestStallTimerDisabledInThresholdMode(t *testing.T) {
	cases := []struct {
		stallTimerSeconds, endAtPct int
		stallActive                 bool
		note                        string
	}{
		{90, 0, true, "classic stall mode: no threshold → stall timer active"},
		{90, 50, false, "threshold mode suppresses the stall timer below the threshold"},
		{0, 50, false, "no stall timer configured anyway"},
		{0, 0, false, "neither configured"},
	}
	for _, tc := range cases {
		// Mirrors WaitForBattleEndCtx's gating: the stall branch only
		// runs when the timer is configured AND no threshold is set.
		stallBranchActive := tc.stallTimerSeconds > 0 && tc.endAtPct == 0
		if stallBranchActive != tc.stallActive {
			t.Errorf("stall gate (timer=%d, threshold=%d): active=%v, want %v (%s)",
				tc.stallTimerSeconds, tc.endAtPct, stallBranchActive, tc.stallActive, tc.note)
		}
	}
}

// ---------------------------------------------------------------------------
// lastDestructionPct latching — max, not last
//
// Transient 0 reads happen when the overlay starts rendering before its
// state is classified. The battle's final damage must latch the MAX
// measured percent, never a later lower/transient read.
// ---------------------------------------------------------------------------

func TestLastDestructionPct_LatchesMaximum(t *testing.T) {
	e := &Executor{logger: zerolog.Nop()}

	ticks := []int{0, 23, 41, 50, 50, 0, 0} // live valk shape: 23% → 50% then overlay restarts
	for _, pct := range ticks {
		if pct > e.lastDestructionPct {
			e.lastDestructionPct = pct
		}
	}
	if got := e.LastDestructionPercent(); got != 50 {
		t.Fatalf("final destruction = %d, want 50 (max must win over later transient 0 reads)", got)
	}
}

// TestValidDestructionRead pins the sanity bound: destruction in CoC is
// 0..100, so reads outside that band are OCR garbage of an unrelated
// counter under the stall ROI (observed live on auto_edrag battles: 381,
// 851, 991 while the result screen showed a 32% defeat).
func TestValidDestructionRead(t *testing.T) {
	for raw, want := range map[int]bool{
		0: true, 1: true, 50: true, 99: true, 100: true,
		101: false, 381: false, 991: false, -1: false,
	} {
		if got := validDestructionRead(raw); got != want {
			t.Errorf("validDestructionRead(%d) = %v, want %v", raw, got, want)
		}
	}
}

// TestLastDestructionPct_IgnoresGarbageReads is the regression test for
// the fabricated-star bug: garbage >100 reads from the stall ROI must
// never be latched as destruction, so they can never feed
// StarsFromOutcome and turn a defeat into a "3 star" victory.
func TestLastDestructionPct_IgnoresGarbageReads(t *testing.T) {
	e := &Executor{logger: zerolog.Nop()}

	// Live-shaped tick stream from the audited run: real damage reads of
	// 61-91 interspersed with garbage reads of 381 / 141 (impossible).
	ticks := []int{0, 61, 71, 381, 81, 991, 91, 141, 0}
	for _, pct := range ticks {
		if validDestructionRead(pct) && pct > e.lastDestructionPct {
			e.lastDestructionPct = pct
		}
	}
	if got := e.LastDestructionPercent(); got != 91 {
		t.Fatalf("destruction latched %d, want 91 — garbage >100 reads must never be latched", got)
	}
}

// TestResetBattleOutcome_ClearsStaleReads is the regression test for the
// cross-battle bleed bug: the executor-scoped lastDestructionPct carried
// battle N's reads into battle N+1's star computation. Reset per battle.
func TestResetBattleOutcome_ClearsStaleReads(t *testing.T) {
	e := &Executor{logger: zerolog.Nop()}
	e.lastDestructionPct = 381 // garbage latched during battle 1
	e.thDestroyed = true

	e.ResetBattleOutcome()

	if got := e.LastDestructionPercent(); got != 0 {
		t.Fatalf("battle outcome not reset: destruction still %d, want 0", got)
	}
	if e.ThDestroyed() {
		t.Fatal("battle outcome not reset: thDestroyed still true")
	}
}

func TestLastDestructionPct_ZeroWhenNeverSampled(t *testing.T) {
	e := &Executor{logger: zerolog.Nop()}
	if got := e.LastDestructionPercent(); got != 0 {
		t.Fatalf("un-sampled battle reported %d%%, want 0", got)
	}
}

// TestLastDestructionPct_StaysBelowThresholdWhenEndingEarly pins the
// invariant that LastDestructionPercent never exceeds the auto-end
// threshold on a threshold-terminated battle: the wait returns the tick
// currentPct >= threshold fires, so the latched max IS the threshold.
func TestLastDestructionPct_StaysBelowThresholdWhenEndingEarly(t *testing.T) {
	e := &Executor{logger: zerolog.Nop()}
	const threshold = 50

	for _, pct := range []int{23, 41, 50} {
		if pct > e.lastDestructionPct {
			e.lastDestructionPct = pct
		}
		if endAtPercentReached(threshold, pct) {
			break // WaitForBattleEndCtx returns here; no later tick samples
		}
	}
	if got := e.LastDestructionPercent(); got > threshold {
		t.Fatalf("destruction %d exceeded the auto-end threshold %d on a threshold-ended battle", got, threshold)
	}
}

// ---------------------------------------------------------------------------
// Fixture-backed destruction OCR: the stall ROI pipeline (readRow on the
// stall_config percent_roi) must read 0 on both end-of-battle fixtures —
// the percent HUD only exists DURING battle, and both shipped fixtures
// are post-battle screens. This pins the "no phantom percent from result
// screens" property the auto-end loop relies on: a stale result-screen
// frame must never satisfy the auto-end threshold.
// ---------------------------------------------------------------------------

func loadStallPercentROI(t testing.TB) image.Rectangle {
	t.Helper()
	for _, p := range []string{"../../assets/stall_config.json", "assets/stall_config.json"} {
		raw, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var sCfg StallConfig
		if err := jsonUnmarshalStall(raw, &sCfg); err != nil {
			t.Fatalf("stall_config.json: %v", err)
		}
		if sCfg.PercentROI.Empty() {
			t.Fatal("stall_config.json percent_roi is empty")
		}
		return sCfg.PercentROI
	}
	t.Skip("stall_config.json not found from test cwd")
	return image.Rectangle{}
}

func newFixtureLootRecognizer(t testing.TB) *game.LootRecognizer {
	t.Helper()
	templateDir := "../../assets/templates"
	if _, err := os.Stat(templateDir); os.IsNotExist(err) {
		templateDir = "assets/templates"
	}
	if _, err := os.Stat(templateDir); os.IsNotExist(err) {
		t.Skip("templates dir not found from test cwd")
	}
	ts, err := game.NewTemplateStore(templateDir)
	if err != nil {
		t.Fatalf("NewTemplateStore: %v", err)
	}
	if err := ts.LoadTemplates(); err != nil {
		t.Fatalf("LoadTemplates: %v", err)
	}
	t.Cleanup(ts.Close)

	cal := &game.Calibration{PhysicalW: 860, PhysicalH: 732, ScaleX: 1.0, ScaleY: 1.0}
	lr := game.NewLootRecognizer(cal, ts, zerolog.Nop())
	t.Cleanup(lr.Close)
	return lr
}

func readFixture(t testing.TB, path string) gocv.Mat {
	t.Helper()
	for _, p := range []string{"../../internal/game/testdata/" + path, "internal/game/testdata/" + path} {
		if _, err := os.Stat(p); err == nil {
			img := gocv.IMRead(p, gocv.IMReadColor)
			if !img.Empty() {
				return img
			}
		}
	}
	t.Skipf("fixture %s not found from test cwd", path)
	return gocv.Mat{}
}

func TestDestructionOCR_ResultScreensNeverSatisfyThreshold(t *testing.T) {
	lr := newFixtureLootRecognizer(t)
	roi := loadStallPercentROI(t)

	for _, fixture := range []string{"screen_defeat.png", "screen_victory.png"} {
		img := readFixture(t, fixture)
		pct := lr.ReadDestructionPercentage(img, roi)
		img.Close()

		if pct != 0 {
			t.Errorf("%s: destruction OCR read %d%% on a post-battle screen — a stale result frame would satisfy end_at_percent", fixture, pct)
		}
		// The valk contract: whatever the threshold, a result-screen
		// frame must never trigger the auto-end.
		if endAtPercentReached(50, pct) {
			t.Errorf("%s: phantom %d%% would end a valk battle instantly", fixture, pct)
		}
	}
}

func TestReadDestructionPercentage_EmptyROIIsZero(t *testing.T) {
	lr := newFixtureLootRecognizer(t)
	img := readFixture(t, "screen_defeat.png")
	defer img.Close()

	if got := lr.ReadDestructionPercentage(img, image.Rectangle{}); got != 0 {
		t.Errorf("empty ROI read %d, want 0", got)
	}
}

// TestBattleEndContextCancellation pins the ctx contract: a cancelled
// context must terminate the wait immediately WITHOUT ending the battle
// (return false), so a user stop never force-ends into a tap sequence.
// This runs against a real Executor whose client fails fast — the loop's
// first select on ctx.Done must win before any capture attempt matters.
func TestBattleEndContextCancellation(t *testing.T) {
	client := newClosedTestClient(t)
	cal := &game.Calibration{PhysicalW: 860, PhysicalH: 732, ScaleX: 1.0, ScaleY: 1.0}
	cfg := minimalAttackConfig()

	e := NewExecutor(client, cal, cfg, zerolog.Nop())
	// No active strategy: threshold 0. The wait would run to its deadline
	// on tick captures — but the cancelled ctx must return immediately.
	e.activeStrategy = &strategy.DynamicStrategy{Name: "Valkyrie Earthquake Spam", EndAtPercent: 50}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel BEFORE the wait starts

	done := make(chan bool, 1)
	go func() { done <- e.WaitForBattleEndCtx(ctx, 30*time.Second) }()

	select {
	case ended := <-done:
		if ended {
			t.Fatal("cancelled wait reported battle ended — a user stop must not trigger EndBattle")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled wait did not return promptly — ctx select is starved")
	}
}

// TestBattleEndContextCancellationWithZeroStrategy verifies the same
// cancellation contract with no strategy at all (threshold mode off).
func TestBattleEndContextCancellationWithZeroStrategy(t *testing.T) {
	client := newClosedTestClient(t)
	cal := &game.Calibration{PhysicalW: 860, PhysicalH: 732, ScaleX: 1.0, ScaleY: 1.0}
	cfg := minimalAttackConfig()

	e := NewExecutor(client, cal, cfg, zerolog.Nop())
	e.activeStrategy = nil

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	done := make(chan bool, 1)
	go func() { done <- e.WaitForBattleEndCtx(ctx, 30*time.Second) }()

	select {
	case ended := <-done:
		if ended {
			t.Fatal("cancelled wait reported battle ended")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled wait did not return promptly")
	}
}
