package bus

import (
	"encoding/base64"
	"encoding/json"
	"sort"
	"strings"
	"sync"
	"time"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

type RingBuffer struct {
	mu          sync.RWMutex
	frames      []FrameEntry
	events      []EventEntry
	maxDuration time.Duration
	maxFrames   int
	maxEvents   int
}

type FrameEntry struct {
	Timestamp time.Time `json:"timestamp"`
	JPEG      []byte    `json:"-"`
	Width     int       `json:"width"`
	Height    int       `json:"height"`
	State     GameState `json:"state"`
}

type EventEntry struct {
	Timestamp time.Time `json:"timestamp"`
	Subject   string    `json:"subject"`
	Data      []byte    `json:"-"`
}

type ReplayItem struct {
	Timestamp  time.Time       `json:"timestamp"`
	Type       string          `json:"type"`
	Subject    string          `json:"subject,omitempty"`
	Data       json.RawMessage `json:"data,omitempty"`
	Frame      string          `json:"frame,omitempty"`
	FrameState GameState       `json:"frame_state,omitempty"`
}

func NewRingBuffer(maxDuration time.Duration, maxFrames, maxEvents int) *RingBuffer {
	if maxDuration <= 0 {
		maxDuration = 30 * time.Second
	}
	if maxFrames <= 0 {
		maxFrames = 300
	}
	if maxEvents <= 0 {
		maxEvents = 4096
	}
	return &RingBuffer{
		maxDuration: maxDuration,
		maxFrames:   maxFrames,
		maxEvents:   maxEvents,
	}
}

func (r *RingBuffer) PushFrame(frame FrameEntry) {
	if r == nil || len(frame.JPEG) == 0 {
		return
	}
	now := frame.Timestamp
	if now.IsZero() {
		now = time.Now()
	}
	cp := make([]byte, len(frame.JPEG))
	copy(cp, frame.JPEG)
	frame.JPEG = cp
	frame.Timestamp = now

	r.mu.Lock()
	defer r.mu.Unlock()
	r.frames = append(r.frames, frame)
	r.pruneLocked(now)
}

func (r *RingBuffer) PushEvent(ts time.Time, subject string, data []byte) {
	if r == nil || subject == "" || len(data) == 0 {
		return
	}
	if ts.IsZero() {
		ts = time.Now()
	}
	cp := make([]byte, len(data))
	copy(cp, data)

	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, EventEntry{Timestamp: ts, Subject: subject, Data: cp})
	r.pruneLocked(ts)
}

func (r *RingBuffer) LatestFrame() *FrameEntry {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	if len(r.frames) == 0 {
		return nil
	}
	frame := r.frames[len(r.frames)-1]
	return &FrameEntry{
		Timestamp: frame.Timestamp,
		JPEG:      append([]byte(nil), frame.JPEG...),
		Width:     frame.Width,
		Height:    frame.Height,
		State:     frame.State,
	}
}

func (r *RingBuffer) Replay(from, to time.Time) []ReplayItem {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()

	items := make([]ReplayItem, 0, len(r.frames)+len(r.events))
	for _, frame := range r.frames {
		if !frame.Timestamp.IsZero() && !from.IsZero() && frame.Timestamp.Before(from) {
			continue
		}
		if !to.IsZero() && frame.Timestamp.After(to) {
			continue
		}
		items = append(items, ReplayItem{
			Timestamp:  frame.Timestamp,
			Type:       "frame",
			Frame:      base64.StdEncoding.EncodeToString(frame.JPEG),
			FrameState: frame.State,
		})
	}
	for _, ev := range r.events {
		if !ev.Timestamp.IsZero() && !from.IsZero() && ev.Timestamp.Before(from) {
			continue
		}
		if !to.IsZero() && ev.Timestamp.After(to) {
			continue
		}
		items = append(items, ReplayItem{
			Timestamp: ev.Timestamp,
			Type:      "event",
			Subject:   ev.Subject,
			Data:      protoToJSON(ev.Subject, ev.Data),
		})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Timestamp.Before(items[j].Timestamp) })
	return items
}

func (r *RingBuffer) pruneLocked(now time.Time) {
	cutoff := now.Add(-r.maxDuration)
	for len(r.frames) > 0 && r.frames[0].Timestamp.Before(cutoff) {
		r.frames = r.frames[1:]
	}
	for len(r.events) > 0 && r.events[0].Timestamp.Before(cutoff) {
		r.events = r.events[1:]
	}
	if len(r.frames) > r.maxFrames {
		r.frames = r.frames[len(r.frames)-r.maxFrames:]
	}
	if len(r.events) > r.maxEvents {
		r.events = r.events[len(r.events)-r.maxEvents:]
	}
}

func ProtoToJSON(subject string, data []byte) json.RawMessage {
	return protoToJSON(subject, data)
}

// protoToJSON decodes a raw proto payload into JSON. If the subject's kind is
// known, we unmarshal into the typed message and emit protojson so field
// tags use proto names (snake_case) rather than Go struct names. Unknown kinds
// fall back to the raw bytes if they are already valid JSON, or to a
// fallback envelope with the base64 blob otherwise.
//
// Performance: this is called on every replay from the ring buffer. The
// protojson marshaler is reasonable but not free — the AI replay path keeps
// the snapshot footprint small by using a 30s buffer.
func protoToJSON(subject string, data []byte) json.RawMessage {
	if len(data) == 0 {
		return nil
	}
	var m proto.Message
	switch SubjectKindFromSubject(subject) {
	case string(SubjectState):
		m = &GameStateEvent{}
	case string(SubjectLoot):
		m = &LootEvent{}
	case string(SubjectAction):
		m = &ActionEvent{}
	case string(SubjectDiagnostic):
		m = &DiagnosticEvent{}
	case string(SubjectEnriched):
		m = &EnrichedStateEvent{}
	case string(SubjectCommandAck):
		m = &CommandResult{}
	case string(SubjectScreen):
		m = &ScreenEvent{}
	case string(SubjectTap):
		m = &TapEvent{}
	case string(SubjectSequence):
		m = &SequenceStepEvent{}
	case string(SubjectClassifier):
		m = &ClassifierEvent{}
	case string(SubjectDeploy):
		m = &DeployEvent{}
	case string(SubjectSummary):
		m = &AttackSummaryEvent{}
	case string(SubjectRestart):
		m = &RestartEvent{}
	case string(SubjectStuck):
		m = &StuckEvent{}
	}
	if m != nil {
		if err := proto.Unmarshal(data, m); err == nil {
			b, err := protojson.MarshalOptions{UseProtoNames: true, EmitUnpopulated: true}.Marshal(m)
			if err == nil {
				return b
			}
		}
	}
	if json.Valid(data) {
		return append(json.RawMessage(nil), data...)
	}
	return json.RawMessage(`{"error":"unknown_event","base64":"` + base64.StdEncoding.EncodeToString(data) + `"`)
}

func SubjectKindFromSubject(subject string) string {
	parts := strings.Split(subject, ".")
	if len(parts) >= 3 {
		return parts[len(parts)-2]
	}
	return "unknown"
}
