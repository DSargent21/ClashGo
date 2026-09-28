package game

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/rs/zerolog"
	"gocv.io/x/gocv"
)

// ClassifyState stops evaluating rules once the best match cannot be beaten.
// The rules are only useful if the shortcut is invisible: a caller must get the
// same state and score it would have got from scoring every rule. These tests
// pin the stop condition, prove the shortcut actually fires, and then check the
// two paths agree on real frames.

// canStopEarly is the whole optimization. It has to be exact in both
// directions: stopping too late wastes a template match per frame, stopping too
// soon returns a lower-priority state.
func TestCanStopEarly(t *testing.T) {
	// The priority order buildRules produces: descending, with a tie at the end
	// (the three priority-50 rules).
	rules := []StateRule{
		{State: StateLoading, Priority: 99},
		{State: StateBattle, Priority: 90},
		{State: StateReturnHome, Priority: 50},
		{State: StateSettings, Priority: 50},
	}

	cases := []struct {
		name         string
		i            int
		bestPriority int
		want         bool
		why          string
	}{
		{"leader outranks the next rule", 0, 99, true, "nothing after a 99 can outrank it"},
		{"leader does not outrank the next rule", 0, 50, false, "the 90-priority rule still has to be scored"},
		{"leader ties the next rule", 2, 50, false, "an equal priority is decided by score, so it must be evaluated"},
		{"leader ties, next rule is lower", 1, 90, true, "the remaining rules are all below the leader"},
		{"last rule", 3, 50, true, "nothing is left to evaluate"},
	}
	for _, tc := range cases {
		if got := canStopEarly(rules, tc.i, tc.bestPriority); got != tc.want {
			t.Errorf("%s: canStopEarly(i=%d, best=%d) = %v, want %v (%s)",
				tc.name, tc.i, tc.bestPriority, got, tc.want, tc.why)
		}
	}
}

// betterState is the ordering the classifier used to express by sorting every
// matched state; priority first, then score.
func TestBetterState(t *testing.T) {
	cases := []struct {
		name string
		a, b scoredState
		want bool
	}{
		{"higher priority wins", scoredState{State: StateLoading, Priority: 99, Score: 100}, scoredState{State: StateBattle, Priority: 90, Score: 9000}, true},
		{"lower priority loses", scoredState{State: StateBattle, Priority: 90, Score: 9000}, scoredState{State: StateLoading, Priority: 99, Score: 100}, false},
		{"same priority, higher score wins", scoredState{State: StateSettings, Priority: 50, Score: 150}, scoredState{State: StateFindMatch, Priority: 50, Score: 140}, true},
		{"same priority, lower score loses", scoredState{State: StateSettings, Priority: 50, Score: 140}, scoredState{State: StateFindMatch, Priority: 50, Score: 150}, false},
		{"exact tie keeps the incumbent", scoredState{State: StateSettings, Priority: 50, Score: 150}, scoredState{State: StateFindMatch, Priority: 50, Score: 150}, false},
	}
	for _, tc := range cases {
		if got := betterState(tc.a, tc.b); got != tc.want {
			t.Errorf("%s: betterState(%s/%d, %s/%d) = %v, want %v",
				tc.name, tc.a.State, tc.a.Score, tc.b.State, tc.b.Score, got, tc.want)
		}
	}
}

// The shortcut has to be more than a no-op. A loading screen is a
// top-priority, single-probe state, so one painted probe decides the frame
// immediately and the remaining rules are never scored: on a real capture loop
// that is where the full-frame template matches are skipped.
func TestEarlyExitStopsOnADecisiveFrame(t *testing.T) {
	cal := NewCalibration(RefWidth, RefHeight)
	c := NewClassifier(cal, DefaultClassifierConfig(), zerolog.Nop())

	frame := gocv.NewMatWithSize(RefHeight, RefWidth, gocv.MatTypeCV8UC3)
	defer frame.Close()

	rule := ruleFor(t, c, StateLoading)
	if len(rule.Checks) != 1 {
		t.Fatalf("the loading rule has %d probes; this test assumed a single-probe state", len(rule.Checks))
	}
	chk := rule.Checks[0]
	setRGB(frame, chk.X, chk.Y, chk.R, chk.G, chk.B)

	full := c.EvaluateRules(frame, false)
	early := c.evaluateRules(frame, false, true)
	if len(full) != len(c.GetRules()) {
		t.Fatalf("the reporting path scored %d of %d rules; it must score them all",
			len(full), len(c.GetRules()))
	}
	if len(early) >= len(full) {
		t.Errorf("early exit scored %d rules, the same as full evaluation; the shortcut never fired",
			len(early))
	}
	if len(early) > 3 {
		t.Errorf("early exit scored %d rules before stopping; a priority-99 match can only need the other 99s", len(early))
	}

	// And the shortcut must still produce the state the full pass produces.
	if st, sc := c.ClassifyState(frame); st != StateLoading {
		t.Errorf("decisive loading frame classified as %s (score %d), wantLoading", st, sc)
	}
}

// Rules that share a priority are decided by score, so the shortcut must score
// the WHOLE priority group before it stops — including the rules that lose, and
// including a later rule that beats an earlier one on score. Stopping at the
// first match in the group would silently hand the frame to whichever rule the
// sort happened to place first.
func TestEarlyExitScoresTheWholePriorityTie(t *testing.T) {
	c := NewClassifier(NewCalibration(RefWidth, RefHeight), DefaultClassifierConfig(), zerolog.Nop())

	// Three rules, priority-sorted the way buildRules leaves them: a tie at 50
	// with the higher-scoring rule SECOND, then a clearly-lower rule.
	first := PixelCheck{10, 10, 0x10, 0x20, 0x30, 5}
	second := PixelCheck{20, 20, 0x40, 0x50, 0x60, 5}
	lower := PixelCheck{30, 30, 0x70, 0x80, 0x90, 5}
	c.rules = []StateRule{
		{State: StateSettings, Priority: 50, Weight: 50, MinPass: 1, Checks: []PixelCheck{first}},
		{State: StateFindMatch, Priority: 50, Weight: 90, MinPass: 1, Checks: []PixelCheck{second}},
		{State: StateChatOpen, Priority: 5, Weight: 5, MinPass: 1, Checks: []PixelCheck{lower}},
	}

	frame := gocv.NewMatWithSize(RefHeight, RefWidth, gocv.MatTypeCV8UC3)
	defer frame.Close()
	for _, chk := range []PixelCheck{first, second} {
		setRGB(frame, chk.X, chk.Y, chk.R, chk.G, chk.B)
	}

	early := c.evaluateRules(frame, false, true)
	if len(early) != 2 {
		t.Fatalf("early exit scored %d rules, want both rules of the priority-50 group and nothing after it", len(early))
	}
	if !early[0].Matched || !early[1].Matched {
		t.Fatalf("the tie group's rules did not both match: %v", early)
	}
	if early[0].Score >= early[1].Score {
		t.Fatalf("test setup is not exercising a tie-break: scores are %d and %d", early[0].Score, early[1].Score)
	}
	if st, sc := c.ClassifyState(frame); st != StateFindMatch {
		t.Errorf("classify returned %s (score %d), want the higher-scoring tie-breaker %s", st, sc, StateFindMatch)
	}
}

// A frame that matches nothing must be unaffected by the shortcut: there is no
// leader to stop on, so every rule is scored and the frame stays Unknown.
func TestEarlyExitStillScansWhenNothingMatches(t *testing.T) {
	c := NewClassifier(NewCalibration(RefWidth, RefHeight), DefaultClassifierConfig(), zerolog.Nop())
	frame := gocv.NewMatWithSize(RefHeight, RefWidth, gocv.MatTypeCV8UC3)
	defer frame.Close()

	if got := len(c.evaluateRules(frame, false, true)); got != len(c.GetRules()) {
		t.Errorf("early exit scored %d of %d rules on a frame with no match; it must score them all",
			got, len(c.GetRules()))
	}
	if st, _ := c.ClassifyState(frame); st != StateUnknown {
		t.Errorf("blank frame classified as %s, want Unknown", st)
	}
}

// The equivalence that matters: on every real frame in the corpus, the
// early-exit classifier must return exactly what scoring all the rules returns.
// The fixtures are the only frames whose probe placement was measured against a
// live screen, so this is where a stop condition that is subtly wrong shows up.
func TestEarlyExitMatchesFullEvaluationOnRealFrames(t *testing.T) {
	manifest := loadCorpusManifest(t, filepath.Join("testdata", "corpus.json"))

	bySize := make(map[string]*Classifier, 2)
	classifierFor := func(img gocv.Mat) *Classifier {
		key := fmt.Sprintf("%dx%d", img.Cols(), img.Rows())
		if c, ok := bySize[key]; ok {
			return c
		}
		scale := manifest.DisplayScale
		if img.Cols() == RefWidth && img.Rows() == RefHeight {
			scale = 0
		}
		c := newCorpusClassifier(t, scale, img.Cols(), img.Rows())
		bySize[key] = c
		return c
	}

	checked := 0
	for _, fr := range manifest.Frames {
		path := filepath.Join("testdata", fr.File)
		if _, err := os.Stat(path); err != nil {
			continue // a checkout without the corpus degrades to a skip
		}
		img := gocv.IMRead(path, gocv.IMReadColor)
		if img.Empty() {
			t.Fatalf("cannot decode %s", path)
		}
		if !isCorpusResolution(img.Cols(), img.Rows()) {
			t.Fatalf("%s is %dx%d; this test only carries frames at the geometries the probes are placed for",
				path, img.Cols(), img.Rows())
		}

		c := classifierFor(img)
		wantState, wantScore := fullEvaluation(c, img)
		gotState, gotScore := c.ClassifyState(img)
		if gotState != wantState || gotScore != wantScore {
			t.Errorf("%s: early exit returned %s (score %d), full evaluation returns %s (score %d)",
				filepath.Base(path), gotState, gotScore, wantState, wantScore)
		}
		img.Close()
		checked++
	}

	if checked == 0 {
		t.Skipf("no corpus frames are present under %s; restore internal/game/testdata/corpus/ to run this gate", "testdata/corpus")
	}
	t.Logf("corpus: %d real frames agree between early exit and full evaluation", checked)
}

// fullEvaluation scores every rule and picks the winner the way the classifier
// did before the shortcut existed: highest priority, then highest score.
func fullEvaluation(c *Classifier, frame gocv.Mat) (GameState, int) {
	var best scoredState
	found := false
	for _, v := range c.EvaluateRules(frame, false) {
		if !v.Matched {
			continue
		}
		s := scoredState{State: v.State, Score: v.Score, Priority: v.Priority}
		if !found || betterState(s, best) {
			best, found = s, true
		}
	}
	if !found {
		return StateUnknown, 0
	}
	return best.State, best.Score
}

// ruleFor returns the classifier's rule for a state, failing the test when the
// rule is gone (a rule deletion would otherwise make a test here silently
// vacuous).
func ruleFor(t *testing.T, c *Classifier, state GameState) StateRule {
	t.Helper()
	for _, r := range c.GetRules() {
		if r.State == state {
			return r
		}
	}
	t.Fatalf("no rule for %s", state)
	return StateRule{}
}
