package bus

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"
)

// TestProtoToJSON_AllKinds exercises protoToJSON over every (existing + new)
// event kind via the in-process ring buffer. Each event is published into a
// ring buffer without NATS, then replayed + asserted that the JSON contains
// the event-specific fields. This guarantees that protoToJSON's type switch
// deserialises each kind correctly so NDJSON consumers never see the
// fallback `{"error":"unknown_event",...}`.
//
// Subtests assert against a parsed JSON map (not strings), so protojson
// formatting variations (spaces, ordering) don't break the test, but a bug
// that puts the value in the wrong key gets caught.
func TestProtoToJSON_AllKinds(t *testing.T) {
	const attackID = "atk_test_42"

	now := time.Now().UnixMilli()

	cases := []struct {
		kind SubjectKind
		data []byte
		must map[string]any // field name → expected JSON value (parsed)
		miss []string       // substrings that MUST NOT appear (sanity vs fallback)
	}{
		{
			kind: SubjectScreen,
			data: mustMarshal(t, &ScreenEvent{
				TimestampMs:      now,
				Width:            860,
				Height:           732,
				CaptureLatencyMs: 47,
				FrameHash:        "9f3a02b1",
				PreviousHash:     "9f3a02b0",
				DiffPercent:      9,
				AttackId:         attackID,
			}),
			must: map[string]any{
				"timestamp_ms": float64(now),
				"width":        float64(860),
				"height":       float64(732),
				"frame_hash":   "9f3a02b1",
				"attack_id":    attackID,
			},
			miss: []string{`unknown_event`},
		},
		{
			kind: SubjectTap,
			data: mustMarshal(t, &TapEvent{
				TimestampMs: now,
				TargetName:  "btn_attack",
				TierUsed:    "pinpoint",
				X:           100,
				Y:           200,
				Attempt:     1,
				Confidence:  0.92,
				Success:     true,
				AttackId:    attackID,
			}),
			must: map[string]any{
				"timestamp_ms": float64(now),
				"target_name":  "btn_attack",
				"tier_used":    "pinpoint",
				"x":            float64(100),
				"y":            float64(200),
				"attack_id":    attackID,
			},
			miss: []string{`unknown_event`},
		},
		{
			kind: SubjectSequence,
			data: mustMarshal(t, &SequenceStepEvent{
				TimestampMs: now,
				StepName:    "Attack",
				Outcome:     "ok",
				RetryCount:  0,
			}),
			must: map[string]any{
				"timestamp_ms": float64(now),
				"step_name":    "Attack",
				"outcome":      "ok",
				"retry_count":  float64(0),
			},
			miss: []string{`unknown_event`},
		},
		{
			kind: SubjectClassifier,
			data: mustMarshal(t, &ClassifierEvent{
				TimestampMs:   now,
				State:         GameState_MAIN_VILLAGE,
				PrevState:     GameState_BATTLE_END,
				TopScore:      87,
				ConfirmFrames: 2,
				AttackId:      attackID,
			}),
			must: map[string]any{
				"timestamp_ms":   float64(now),
				"top_score":      float64(87),
				"state":          "MAIN_VILLAGE",
				"prev_state":     "BATTLE_END",
				"confirm_frames": float64(2),
			},
			miss: []string{`unknown_event`},
		},
		{
			kind: SubjectDeploy,
			data: mustMarshal(t, &DeployEvent{
				TimestampMs:  now,
				AttackId:     attackID,
				Phase:        "Balloons",
				Unit:         "Balloon",
				SlotX:        120,
				RedZoneValid: true,
				RedZoneX:     92,
				RedZoneW:     208,
				RedZoneH:     153,
				DeployPoints: []int32{92, 411, 150, 470, 220, 511, 300, 564},
			}),
			must: map[string]any{
				"timestamp_ms":   float64(now),
				"phase":          "Balloons",
				"unit":           "Balloon",
				"slot_x":         float64(120),
				"red_zone_x":     float64(92),
				"red_zone_w":     float64(208),
				"red_zone_h":     float64(153),
				"red_zone_valid": true,
				"deploy_points":  []any{float64(92), float64(411), float64(150), float64(470), float64(220), float64(511), float64(300), float64(564)},
			},
			miss: []string{`unknown_event`},
		},
		{
			kind: SubjectSummary,
			data: mustMarshal(t, &AttackSummaryEvent{
				TimestampMs:   now,
				AttackId:      attackID,
				Strategy:      "auto_edrag_rush",
				TargetEdge:    "BottomLeft",
				DeploySuccess: true,
				Stars:         3,
				LootGold:      600000,
				BonusGold:     250000,
			}),
			must: map[string]any{
				"timestamp_ms":   float64(now),
				"stars":          float64(3),
				"strategy":       "auto_edrag_rush",
				"target_edge":    "BottomLeft",
				"deploy_success": true,
				"loot_gold":      float64(600000),
				"bonus_gold":     float64(250000),
				"attack_id":      attackID,
			},
			miss: []string{`unknown_event`},
		},
		{
			kind: SubjectRestart,
			data: mustMarshal(t, &RestartEvent{
				TimestampMs:     now,
				Trigger:         RestartTrigger_RESTART_STUCK_TIMEOUT,
				Reason:          "no state change for 30s",
				LastActionAgeMs: 30000,
				LastState:       "MAIN_VILLAGE",
			}),
			must: map[string]any{
				"timestamp_ms":       float64(now),
				"trigger":            "RESTART_STUCK_TIMEOUT",
				"reason":             "no state change for 30s",
				"last_state":         "MAIN_VILLAGE",
				"last_action_age_ms": float64(30000),
			},
			miss: []string{`unknown_event`},
		},
		{
			kind: SubjectStuck,
			data: mustMarshal(t, &StuckEvent{
				TimestampMs:      now,
				LastState:        "MAIN_VILLAGE",
				IdleMs:           30000,
				ConsecutiveFails: 3,
			}),
			must: map[string]any{
				"timestamp_ms":      float64(now),
				"last_state":        "MAIN_VILLAGE",
				"idle_ms":           float64(30000),
				"consecutive_fails": float64(3),
			},
			miss: []string{`unknown_event`},
		},
	}

	for _, c := range cases {
		t.Run(string(c.kind), func(t *testing.T) {
			// Fresh ring for each subtest so prior subjects don't pollute.
			ring := NewRingBuffer(time.Minute, 4, 32)
			subject := Subject("test-device", c.kind)

			// Push directly into the ring; protoToJSON is the same code path
			// the bus uses on Replay.
			ring.PushEvent(time.Now(), subject, c.data)
			items := ring.Replay(time.Time{}, time.Time{})

			if len(items) == 0 {
				t.Fatalf("ring buffer returned 0 items for %s", c.kind)
			}
			found := false
			for _, it := range items {
				if it.Type != "event" || it.Subject != subject {
					continue
				}
				found = true
				js := string(it.Data)
				for _, deny := range c.miss {
					if strings.Contains(js, deny) {
						t.Errorf("JSON unexpectedly contains %q (full=%s)", deny, js)
					}
				}
				var parsed map[string]any
				if err := json.Unmarshal(it.Data, &parsed); err != nil {
					t.Fatalf("JSON not parseable: %v (full=%s)", err, js)
				}
				for field, want := range c.must {
					got, ok := parsed[field]
					if !ok {
						t.Errorf("JSON missing field %q (full=%s)", field, js)
						continue
					}
					if !valueApproxEqual(got, want) {
						t.Errorf("field %q = %v (%T), want %v (%T)", field, got, got, want, want)
					}
				}
			}
			if !found {
				t.Fatalf("ring did not retain subject %s", subject)
			}
		})
	}
}

func mustMarshal(t *testing.T, m proto.Message) []byte {
	t.Helper()
	b, err := proto.Marshal(m)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

// valueApproxEqual compares JSON-decoded values against the expected value.
// JSON-decoded numbers come back as float64 by default, but protojson's
// precision-protection mode emits int64 values as quoted strings (so they
// survive round-trips beyond float64's 53-bit mantissa). This comparator
// tolerates both representations: when the test expects a numeric value but
// got a string, it parses the string as float64 (and as int64 as a fallback).
// Slices and maps are walked recursively.
func valueApproxEqual(got, want any) bool {
	if got == nil || want == nil {
		return got == want
	}
	switch w := want.(type) {
	case float64:
		switch g := got.(type) {
		case float64:
			return g == w
		case string:
			if f, err := strconv.ParseFloat(g, 64); err == nil {
				return f == w
			}
			if i, err := strconv.ParseInt(g, 10, 64); err == nil {
				return float64(i) == w
			}
			return false
		default:
			return false
		}
	case string:
		s, ok := got.(string)
		return ok && s == w
	case bool:
		b, ok := got.(bool)
		return ok && b == w
	case []any:
		g, ok := got.([]any)
		if !ok || len(g) != len(w) {
			return false
		}
		for i := range w {
			if !valueApproxEqual(g[i], w[i]) {
				return false
			}
		}
		return true
	case map[string]any:
		g, ok := got.(map[string]any)
		if !ok || len(g) != len(w) {
			return false
		}
		for k, v := range w {
			gv, present := g[k]
			if !present {
				return false
			}
			if !valueApproxEqual(gv, v) {
				return false
			}
		}
		return true
	default:
		return got == want
	}
}
