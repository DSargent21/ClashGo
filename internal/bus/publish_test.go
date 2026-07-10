package bus

import (
	"strings"
	"testing"
	"time"
)

// TestPublishNewEvents_NilBusIsSafe verifies the disabled-bus path does not
// panic for any of the new Publish methods. When the bus has no NATS
// connection, publish() should return immediately (no error, no work).
func TestPublishNewEvents_NilBusIsSafe(t *testing.T) {
	w, err := NewEventBus("", "test-device", WithNoopOnFail(true))
	if err != nil {
		// NoopOnFail still returns a stub EventBus, so this shouldn't happen,
		// but be defensive.
		if w == nil {
			t.Skip("no bus available, skipping")
		}
	}
	if w.Enabled() {
		t.Skip("NATS available; this test only exercises the disabled path")
	}

	now := time.Now().UnixMilli()

	cases := []struct {
		name string
		fn   func() error
	}{
		{"PublishScreen", func() error { return w.PublishScreen(&ScreenEvent{TimestampMs: now, Width: 860, Height: 732}) }},
		{"PublishTap", func() error {
			return w.PublishTap(&TapEvent{TimestampMs: now, TargetName: "btn_attack", TierUsed: "pinpoint", X: 100, Y: 200, Success: true})
		}},
		{"PublishSequenceStep", func() error {
			return w.PublishSequenceStep(&SequenceStepEvent{TimestampMs: now, StepName: "Attack", Outcome: "ok"})
		}},
		{"PublishClassifier", func() error {
			return w.PublishClassifier(&ClassifierEvent{TimestampMs: now, State: GameState_MAIN_VILLAGE, TopScore: 87})
		}},
		{"PublishDeploy", func() error {
			return w.PublishDeploy(&DeployEvent{TimestampMs: now, Phase: "Balloons", Unit: "Balloon"})
		}},
		{"PublishSummary", func() error {
			return w.PublishSummary(&AttackSummaryEvent{TimestampMs: now, AttackId: "atk_x", Stars: 3})
		}},
		{"PublishRestart", func() error {
			return w.PublishRestart(&RestartEvent{TimestampMs: now, Trigger: RestartTrigger_RESTART_STUCK_TIMEOUT})
		}},
		{"PublishStuck", func() error {
			return w.PublishStuck(&StuckEvent{TimestampMs: now, LastState: "MAIN_VILLAGE", IdleMs: 30000})
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := c.fn(); err != nil {
				t.Errorf("%s: expected nil error on disabled bus, got %v", c.name, err)
			}
		})
	}
}

// TestPublishNewEvents_RingBufferReceivesEvents verifies that even with no
// NATS server, publishing events still feeds the in-process ring buffer so
// the in-process WebSocket/replay layer can serve them.
func TestPublishNewEvents_RingBufferReceivesEvents(t *testing.T) {
	ring := NewRingBuffer(time.Minute, 30, 256)
	w, err := NewEventBus("", "test-device", WithNoopOnFail(true), WithRingBuffer(ring))
	if err != nil && w == nil {
		t.Skip("no bus available")
	}
	if w.Enabled() {
		t.Skip("NATS available; this test only exercises the no-NATS path")
	}

	if err := w.PublishTap(&TapEvent{TimestampMs: 1, TargetName: "btn_next", TierUsed: "template", Success: true}); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if err := w.PublishDeploy(&DeployEvent{TimestampMs: 2, Unit: "Balloon", Phase: "Balloons"}); err != nil {
		t.Fatalf("publish: %v", err)
	}

	items := ring.Replay(time.Time{}, time.Time{})
	if len(items) < 2 {
		t.Fatalf("expected >= 2 ring buffer items, got %d", len(items))
	}
	var sawTap, sawDeploy bool
	for _, it := range items {
		if it.Type != "event" {
			continue
		}
		s := string(it.Data)
		if !sawTap && strings.Contains(s, "btn_next") && strings.Contains(s, "template") {
			sawTap = true
		}
		if !sawDeploy && strings.Contains(s, "Balloon") && strings.Contains(s, "Balloons") {
			sawDeploy = true
		}
	}
	if !sawTap {
		t.Errorf("ring buffer missing tap event: %+v", items)
	}
	if !sawDeploy {
		t.Errorf("ring buffer missing deploy event: %+v", items)
	}
}

// TestPublishRaw_NewSubjects verifies PublishRaw works for the new SubjectKinds.
func TestPublishRaw_NewSubjects(t *testing.T) {
	w, err := NewEventBus("", "test-device", WithNoopOnFail(true))
	if w == nil && err != nil {
		// Defensive: WithNoopOnFail should always return a stub; some nats
		// versions may not, so fall back to a noop test.
		t.Skip("bus unavailable for raw test")
	}
	if w.Enabled() {
		t.Skip("NATS available; this test only exercises the no-NATS path")
	}
	for _, k := range []SubjectKind{
		SubjectScreen, SubjectTap, SubjectSequence, SubjectClassifier,
		SubjectDeploy, SubjectSummary, SubjectRestart, SubjectStuck,
	} {
		t.Run(string(k), func(t *testing.T) {
			if err := w.PublishRaw(k, &ScreenEvent{}); err != nil {
				t.Errorf("publish raw %s: %v", k, err)
			}
		})
	}
}
