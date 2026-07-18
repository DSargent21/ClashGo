package bus

import (
	"fmt"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
	"google.golang.org/protobuf/proto"
)

type EventBus struct {
	nc       *nats.Conn
	js       nats.JetStreamContext
	deviceID string
	stream   string
	subjects []string
	ring     *RingBuffer
	mu       sync.RWMutex
	enabled  bool
	err      error
}

type Option func(*options)

func WithURL(url string) Option {
	return func(o *options) { o.URL = url }
}

func WithDeviceID(id string) Option {
	return func(o *options) { o.DeviceID = id }
}

func WithStreamName(name string) Option {
	return func(o *options) { o.StreamName = name }
}

func WithRetention(retention time.Duration) Option {
	return func(o *options) { o.Retention = retention }
}

func WithLogger(warn func(string, ...interface{}), info func(string, ...interface{})) Option {
	return func(o *options) {
		o.LogWarn = warn
		o.LogInfo = info
	}
}

func WithNoopOnFail(v bool) Option {
	return func(o *options) { o.NoopOnFail = v }
}

func WithRingBuffer(r *RingBuffer) Option {
	return func(o *options) { o.Ring = r }
}

type options struct {
	URL             string
	DeviceID        string
	StreamName      string
	ConnectTimeout  time.Duration
	Retention       time.Duration
	Ring            *RingBuffer
	LogWarn         func(string, ...interface{})
	LogInfo         func(string, ...interface{})
	NoopOnFail      bool
	AsyncBufferSize int
}

func defaultOptions() options {
	return options{
		URL:             DefaultNATSURL,
		DeviceID:        "default",
		StreamName:      DefaultStreamName,
		ConnectTimeout:  2 * time.Second,
		Retention:       24 * time.Hour,
		NoopOnFail:      true,
		AsyncBufferSize: 8192,
	}
}

func NewEventBus(url, deviceID string, opts ...Option) (*EventBus, error) {
	cfg := defaultOptions()
	for _, opt := range opts {
		opt(&cfg)
	}
	if url != "" {
		cfg.URL = url
	}
	if deviceID != "" {
		cfg.DeviceID = deviceID
	}
	if cfg.DeviceID == "" {
		cfg.DeviceID = "default"
	}

	nc, err := nats.Connect(cfg.URL,
		nats.Name("clashgo-eventbus-"+cfg.DeviceID),
		nats.Timeout(cfg.ConnectTimeout),
		nats.NoReconnect(),
		nats.MaxReconnects(0),
	)
	if err != nil {
		if cfg.NoopOnFail {
			return &EventBus{
				deviceID: cfg.DeviceID,
				stream:   cfg.StreamName,
				subjects: allSubjects(cfg.DeviceID),
				ring:     cfg.Ring,
				enabled:  false,
				err:      err,
			}, err
		}
		return nil, err
	}

	js, err := nc.JetStream(nats.PublishAsyncMaxPending(cfg.AsyncBufferSize))
	if err != nil {
		nc.Drain()
		if cfg.NoopOnFail {
			return &EventBus{
				deviceID: cfg.DeviceID,
				stream:   cfg.StreamName,
				subjects: allSubjects(cfg.DeviceID),
				ring:     cfg.Ring,
				enabled:  false,
				err:      err,
			}, err
		}
		return nil, err
	}

	subjects := allSubjects(cfg.DeviceID)
	if _, err = js.StreamInfo(cfg.StreamName); err != nil {
		_, err = js.AddStream(&nats.StreamConfig{
			Name:      cfg.StreamName,
			Subjects:  subjects,
			Retention: nats.LimitsPolicy,
			Storage:   nats.MemoryStorage,
			MaxAge:    cfg.Retention,
			MaxMsgs:   int64(cfg.AsyncBufferSize * 8),
		})
		if err != nil {
			nc.Drain()
			if cfg.NoopOnFail {
				return &EventBus{
					deviceID: cfg.DeviceID,
					stream:   cfg.StreamName,
					subjects: subjects,
					ring:     cfg.Ring,
					enabled:  false,
					err:      err,
				}, err
			}
			return nil, err
		}
	}

	e := &EventBus{
		nc:       nc,
		js:       js,
		deviceID: cfg.DeviceID,
		stream:   cfg.StreamName,
		subjects: subjects,
		ring:     cfg.Ring,
		enabled:  true,
	}

	if cfg.LogInfo != nil {
		cfg.LogInfo("event bus connected", "url", cfg.URL, "device", cfg.DeviceID, "stream", cfg.StreamName)
	}
	return e, nil
}

func (e *EventBus) Enabled() bool {
	if e == nil {
		return false
	}
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.enabled
}

func (e *EventBus) Error() error {
	if e == nil {
		return nil
	}
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.err
}

func (e *EventBus) DeviceID() string {
	if e == nil {
		return ""
	}
	return e.deviceID
}

func (e *EventBus) PublishState(ev *GameStateEvent) error {
	return e.publish(Subject(e.deviceID, SubjectState), ev)
}

func (e *EventBus) PublishLoot(ev *LootEvent) error {
	return e.publish(Subject(e.deviceID, SubjectLoot), ev)
}

func (e *EventBus) PublishAction(ev *ActionEvent) error {
	return e.publish(Subject(e.deviceID, SubjectAction), ev)
}

func (e *EventBus) PublishDiagnostic(ev *DiagnosticEvent) error {
	return e.publish(Subject(e.deviceID, SubjectDiagnostic), ev)
}

func (e *EventBus) PublishEnrichedState(ev *EnrichedStateEvent) error {
	return e.publish(Subject(e.deviceID, SubjectEnriched), ev)
}

func (e *EventBus) PublishCommandResult(ev *CommandResult) error {
	return e.publish(Subject(e.deviceID, SubjectCommandAck), ev)
}

// PublishScreen emits a ScreenEvent capturing a single screencap attempt.
// Always safe to call; if the bus has no NATS connection the event still
// reaches the in-process ring buffer.
func (e *EventBus) PublishScreen(ev *ScreenEvent) error {
	return e.publish(Subject(e.deviceID, SubjectScreen), ev)
}

// PublishTap emits a TapEvent for every input that produces a tap path
// (pinpoint, color, template, blind fallback).
func (e *EventBus) PublishTap(ev *TapEvent) error {
	return e.publish(Subject(e.deviceID, SubjectTap), ev)
}

// PublishSequenceStep emits a SequenceStepEvent for each clickSequence step.
func (e *EventBus) PublishSequenceStep(ev *SequenceStepEvent) error {
	return e.publish(Subject(e.deviceID, SubjectSequence), ev)
}

// PublishClassifier emits a ClassifierEvent with the structured verdict and
// alternative score map.
func (e *EventBus) PublishClassifier(ev *ClassifierEvent) error {
	return e.publish(Subject(e.deviceID, SubjectClassifier), ev)
}

// PublishDeploy emits a DeployEvent per unit deployed (or attempted).
func (e *EventBus) PublishDeploy(ev *DeployEvent) error {
	return e.publish(Subject(e.deviceID, SubjectDeploy), ev)
}

// PublishSummary emits the final AttackSummaryEvent at the end of an attack.
func (e *EventBus) PublishSummary(ev *AttackSummaryEvent) error {
	return e.publish(Subject(e.deviceID, SubjectSummary), ev)
}

// PublishRestart emits a RestartEvent whenever the bot force-stops the game
// in response to a recoverable failure.
func (e *EventBus) PublishRestart(ev *RestartEvent) error {
	return e.publish(Subject(e.deviceID, SubjectRestart), ev)
}

// PublishStuck emits a StuckEvent when checkStuck is about to trigger.
func (e *EventBus) PublishStuck(ev *StuckEvent) error {
	return e.publish(Subject(e.deviceID, SubjectStuck), ev)
}

func (e *EventBus) PublishRaw(kind SubjectKind, msg proto.Message) error {
	return e.publish(Subject(e.deviceID, kind), msg)
}

func (e *EventBus) Flush(timeout time.Duration) error {
	if !e.Enabled() {
		return nil
	}
	e.mu.RLock()
	nc, js := e.nc, e.js
	e.mu.RUnlock()
	if nc == nil || js == nil {
		return nil
	}
	if timeout <= 0 {
		timeout = 2 * time.Second
	}
	select {
	case <-js.PublishAsyncComplete():
	case <-time.After(timeout):
		return fmt.Errorf("timeout waiting for pending jetstream publishes")
	}
	return nc.FlushTimeout(timeout)
}

func (e *EventBus) Close() error {
	if e == nil || !e.Enabled() {
		return nil
	}
	_ = e.Flush(time.Second)
	e.mu.Lock()
	nc := e.nc
	e.nc, e.js = nil, nil
	e.enabled = false
	e.mu.Unlock()
	if nc != nil {
		return nc.Drain()
	}
	return nil
}

func (e *EventBus) SubscribeCommands(handler func(*Command)) (*nats.Subscription, error) {
	if !e.Enabled() {
		return nil, nil
	}
	e.mu.RLock()
	js := e.js
	e.mu.RUnlock()
	if js == nil {
		return nil, nil
	}
	sub, err := js.Subscribe(Subject(e.deviceID, SubjectCommand), func(m *nats.Msg) {
		var cmd Command
		if err := proto.Unmarshal(m.Data, &cmd); err != nil {
			_ = m.Nak()
			return
		}
		handler(&cmd)
		if err := m.Ack(); err != nil {
			_ = m.Nak()
		}
	},
		nats.Durable("commands-"+e.deviceID),
		nats.ManualAck(),
		nats.AckWait(5*time.Second),
		nats.MaxAckPending(64),
	)
	if err != nil {
		return nil, err
	}
	return sub, nil
}

func (e *EventBus) SubscribeState(handler func(*GameStateEvent)) (*nats.Subscription, error) {
	if !e.Enabled() {
		return nil, nil
	}
	e.mu.RLock()
	js := e.js
	e.mu.RUnlock()
	if js == nil {
		return nil, nil
	}
	sub, err := js.Subscribe(Subject(e.deviceID, SubjectState), func(m *nats.Msg) {
		var ev GameStateEvent
		if err := proto.Unmarshal(m.Data, &ev); err != nil {
			_ = m.Nak()
			return
		}
		handler(&ev)
		_ = m.Ack()
	}, nats.ManualAck(), nats.MaxAckPending(128))
	return sub, err
}

func (e *EventBus) SubscribeAll(handler func(string, []byte)) (*nats.Subscription, error) {
	if !e.Enabled() {
		return nil, nil
	}
	e.mu.RLock()
	js := e.js
	e.mu.RUnlock()
	if js == nil {
		return nil, nil
	}
	sub, err := js.Subscribe(Subject(e.deviceID, "*"), func(m *nats.Msg) {
		handler(m.Subject, append([]byte(nil), m.Data...))
		_ = m.Ack()
	}, nats.ManualAck(), nats.MaxAckPending(256))
	return sub, err
}

func (e *EventBus) publish(subject string, msg proto.Message) error {
	data, err := proto.Marshal(msg)
	if err != nil {
		return err
	}
	// Always feed the in-process ring buffer so the AI can replay recent events
	// even when NATS is unavailable. The ring buffer is the local fallback
	// path — it MUST receive every event regardless of Enabled().
	if e.ring != nil {
		e.ring.PushEvent(time.Now(), subject, data)
	}
	if !e.Enabled() {
		return nil
	}
	e.mu.RLock()
	js := e.js
	e.mu.RUnlock()
	if js == nil {
		return nil
	}
	_, err = js.PublishAsync(subject, data)
	return err
}

func allSubjects(deviceID string) []string {
	kinds := []SubjectKind{
		SubjectState, SubjectLoot, SubjectAction, SubjectDiagnostic,
		SubjectEnriched, SubjectCommand, SubjectCommandAck, SubjectHealth,
		SubjectFrame,
		SubjectScreen, SubjectTap, SubjectSequence, SubjectClassifier,
		SubjectDeploy, SubjectSummary, SubjectRestart, SubjectStuck,
	}
	subjects := make([]string, 0, len(kinds))
	for _, kind := range kinds {
		subjects = append(subjects, Subject(deviceID, kind))
	}
	return subjects
}
