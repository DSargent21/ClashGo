package game

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/rs/zerolog"
	"gocv.io/x/gocv"
)

// A corpus of real screenshots, with the state the bot must report for each.
//
// Every other classifier test builds its frame synthetically: it paints the
// probe colours onto a blank canvas and asserts the rule fires. That proves the
// probes agree with themselves and nothing else — a rule whose prototypes were
// measured off the wrong pixel, or whose coordinates were authored against a
// stale geometry, passes all of them and still fails on a live screen. These
// fixtures are real captures, so this is the only test that can catch that
// class of bug. It has already earned its keep: it showed that the derived
// display scale (1.3004) and the scale the bot pins in its config (1.325) give
// opposite verdicts on three of these five frames, which is why every fixture
// states the scale it must be evaluated at.
//
// Lifecycle: a mismatch is a hard failure. Do not add a fixture whose correct
// state is unknown — either the bot's behaviour on that screen is a bug worth
// fixing, in which case fix it and then add the frame, or the frame does not
// belong here. If you deliberately change a rule, update the manifest in the
// same commit and say in the entry's note what changed.
func TestClassifierOnRealFrames(t *testing.T) {
	manifest := loadCorpusManifest(t, filepath.Join("testdata", "corpus.json"))
	if len(manifest.Frames) == 0 {
		t.Fatal("the corpus manifest lists no frames; a manifest nobody asserts on is dead weight")
	}
	// One classifier per geometry. The reference frame is the identity mapping
	// and must NOT have the device scale pinned onto it: the template sweep uses
	// k to resize the reference template PNGs, so pinning 1.325 on an 860x732
	// frame would resize them by 1.19-1.46 and break every template-gated rule.
	bySize := make(map[string]*Classifier, 2)
	classifierFor := func(w, h int) *Classifier {
		key := fmt.Sprintf("%dx%d", w, h)
		if c, ok := bySize[key]; ok {
			return c
		}
		scale := manifest.DisplayScale
		if w == RefWidth && h == RefHeight {
			scale = 0
		}
		c := newCorpusClassifier(t, scale, w, h)
		bySize[key] = c
		return c
	}

	outcomes := runCorpus(manifest.Frames, func(fr corpusExpectation) (string, int, error) {
		return classifyFixture(classifierFor, fr)
	})

	var passed, failed, skipped int
	for _, o := range outcomes {
		switch o.State {
		case outcomePassed:
			passed++
			t.Logf("ok     %-40s -> %-12s score=%d", filepath.Base(o.Expect.File), o.Got, o.Score)
		case outcomeSkipped:
			skipped++
			t.Logf("skip   %-40s (fixture not present)", o.Expect.File)
		default:
			failed++
			if o.Err != nil {
				t.Errorf("%s: %v", o.Expect.File, o.Err)
				continue
			}
			t.Errorf("%s: want %s, got %s (score %d)\n        note: %s\n        explain it with: build/bin/see look -img %s -why %s -k %g",
				filepath.Base(o.Expect.File), o.Expect.State, o.Got, o.Score, o.Expect.Note,
				filepath.Join("internal/game/testdata", o.Expect.File), o.Expect.State, manifest.DisplayScale)
		}
	}

	if skipped == len(outcomes) {
		t.Skipf("no corpus frames are present in %s; restore internal/game/testdata/corpus/ to run this gate", "testdata/corpus")
	}
	if failed > 0 {
		t.Fatalf("%d of %d real frames classify wrongly (see the failures above)", failed, len(outcomes))
	}
	t.Logf("corpus: %d passed, %d skipped", passed, skipped)
}

// corpusExpectation is one fixture's requirement.
type corpusExpectation struct {
	File  string `json:"file"`
	State string `json:"state"`
	Note  string `json:"note,omitempty"`
}

// corpusManifest is the on-disk list of fixtures and the default scale.
type corpusManifest struct {
	Comment      string              `json:"comment"`
	DisplayScale float64             `json:"display_scale"`
	Frames       []corpusExpectation `json:"frames"`
}

// corpusState classifies what happened to one fixture.
type corpusState int

const (
	outcomePassed corpusState = iota
	outcomeFailed
	outcomeSkipped
)

// corpusOutcome records the verdict for one fixture.
type corpusOutcome struct {
	Expect corpusExpectation
	Got    string
	Score  int
	Err    error
	State  corpusState
}

// runCorpus evaluates every expectation. It takes the classifier as a function
// so the harness's own behaviour — pass, fail, and "fixture missing is a skip
// rather than a failure" — is unit-testable without needing OpenCV fixtures or
// a corpus on disk.
func runCorpus(frames []corpusExpectation, classify func(corpusExpectation) (string, int, error)) []corpusOutcome {
	out := make([]corpusOutcome, 0, len(frames))
	for _, fr := range frames {
		state, score, err := classify(fr)
		o := corpusOutcome{Expect: fr, Got: state, Score: score, Err: err}
		switch {
		case err != nil && errors.Is(err, fs.ErrNotExist):
			// A missing fixture is not a wrong answer: the corpus is
			// large binaries, so it must degrade to a skip when a checkout
			// does not carry it.
			o.State = outcomeSkipped
		case err != nil:
			o.State = outcomeFailed
		case state != fr.State:
			o.State = outcomeFailed
		default:
			o.State = outcomePassed
		}
		out = append(out, o)
	}
	return out
}

// loadCorpusManifest reads the manifest. A manifest that exists but cannot be
// parsed is a failure, not a skip: silently testing nothing is worse than not
// testing.
func loadCorpusManifest(t testing.TB, path string) corpusManifest {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read corpus manifest: %v", err)
	}
	var m corpusManifest
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return m
}

// newCorpusClassifier builds the classifier the bot runs: the same config
// defaults, the fixture's display scale, and the real template store when it is
// present (several rules are gated on a template match, so a corpus run without
// templates would be measuring a different classifier).
func newCorpusClassifier(t testing.TB, scale float64, w, h int) *Classifier {
	t.Helper()
	// The calibration MUST be built from the fixture's own dimensions. Building
	// it from the live device size instead maps an 860x732 reference frame
	// through the 720p law — k=1.3004 about the 1280x720 centre — and every
	// probe lands somewhere else entirely, which reads as "the reference path is
	// broken" when only the harness is.
	cal := NewCalibration(w, h)
	cal.Verified = true
	if scale > 0 {
		cal.SetDisplayScale(scale)
	}
	c := NewClassifier(cal, DefaultClassifierConfig(), zerolog.Nop())

	for _, dir := range []string{"../../assets/templates", "assets/templates"} {
		if _, err := os.Stat(dir); err != nil {
			continue
		}
		ts, err := NewTemplateStore(dir)
		if err != nil {
			t.Logf("template store at %s unusable (%v); template-gated rules will not match", dir, err)
			continue
		}
		if err := ts.LoadTemplates(); err != nil {
			t.Logf("loading templates from %s: %v", dir, err)
			continue
		}
		t.Cleanup(ts.Close)
		c.SetTemplates(ts)
		break
	}
	return c
}

// classifyFixture loads one fixture and classifies it at the manifest's scale.
// The frame size is asserted because the probes are placed for 720p: swapping
// in a different resolution would silently change what the fixture tests.
func classifyFixture(classifierFor func(w, h int) *Classifier, fr corpusExpectation) (string, int, error) {
	path := filepath.Join("testdata", fr.File)
	if _, err := os.Stat(path); err != nil {
		return "", 0, fmt.Errorf("%w: %s", fs.ErrNotExist, path)
	}
	img := gocv.IMRead(path, gocv.IMReadColor)
	if img.Empty() {
		return "", 0, fmt.Errorf("cannot decode %s", path)
	}
	defer img.Close()
	if !isCorpusResolution(img.Cols(), img.Rows()) {
		return "", 0, fmt.Errorf("%s is %dx%d; this corpus carries only the reference 860x732 and the 720p device geometry, because the probes are placed for those",
			path, img.Cols(), img.Rows())
	}
	state, score := classifierFor(img.Cols(), img.Rows()).ClassifyState(img)
	return state.String(), score, nil
}

// isCorpusResolution accepts the authored reference geometry and the live device
// geometry. A fixture at any other size would silently change what is being
// tested, so it is rejected rather than mapped.
func isCorpusResolution(w, h int) bool {
	return (w == RefWidth && h == RefHeight) || (w == 1280 && h == 720)
}

// TestCorpusHarness_SortsPassFailAndSkip pins the harness itself: a fixture
// that disagrees with its expectation must be reported as a failure with the
// observed state attached, and a fixture that is simply absent must be a skip.
// Without this, a harness bug could turn the whole corpus green.
func TestCorpusHarness_SortsPassFailAndSkip(t *testing.T) {
	frames := []corpusExpectation{
		{File: "matching.png", State: "Battle"},
		{File: "absent.png", State: "MainVillage"},
		{File: "wrong.png", State: "BattleEnd"},
		{File: "broken.png", State: "Battle"},
	}
	classify := func(fr corpusExpectation) (string, int, error) {
		switch fr.File {
		case "matching.png":
			return "Battle", 290, nil
		case "absent.png":
			return "", 0, fmt.Errorf("%w: %s", fs.ErrNotExist, fr.File)
		case "wrong.png":
			return "Unknown", 0, nil
		default:
			return "", 0, fmt.Errorf("cannot decode %s", fr.File)
		}
	}

	out := runCorpus(frames, classify)
	got := make([]corpusState, len(out))
	for i, o := range out {
		got[i] = o.State
	}
	want := []corpusState{outcomePassed, outcomeSkipped, outcomeFailed, outcomeFailed}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("%s: state = %v, want %v", out[i].Expect.File, got[i], want[i])
		}
	}
	if out[2].Got != "Unknown" {
		t.Errorf("a mismatch must record the observed state, got %q", out[2].Got)
	}
	if out[2].Err != nil {
		t.Errorf("a state mismatch is not an error: %v", out[2].Err)
	}
	if !errors.Is(out[1].Err, fs.ErrNotExist) {
		t.Errorf("a missing fixture must be recognisable as not-exist, got %v", out[1].Err)
	}
}

// TestCorpusFramesAreWiredToTheManifest guards the other half of the contract:
// every PNG in the corpus directory is asserted on somewhere. An unasserted
// fixture is 2MB of dead weight that looks like coverage.
func TestCorpusFramesAreWiredToTheManifest(t *testing.T) {
	manifest := loadCorpusManifest(t, filepath.Join("testdata", "corpus.json"))
	listed := make(map[string]bool, len(manifest.Frames))
	for _, fr := range manifest.Frames {
		listed[fr.File] = true
		if fr.Note == "" {
			t.Errorf("%s has no note; a fixture without its story cannot be maintained", fr.File)
		}
	}
	matches, err := filepath.Glob(filepath.Join("testdata", "corpus", "*.png"))
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range matches {
		rel := filepath.ToSlash(filepath.Join("corpus", filepath.Base(m)))
		if !listed[rel] {
			t.Errorf("%s is in the corpus but not in the manifest: add an expectation or delete the file", rel)
		}
	}
}
