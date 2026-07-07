package bus

import "time"

const (
	DefaultNATSURL     = "nats://127.0.0.1:4222"
	DefaultStreamName  = "CLASHGO_EVENTS"
	DefaultSubjectRoot = "clashgo.events"
)

type SubjectKind string

const (
	SubjectState       SubjectKind = "state"
	SubjectLoot        SubjectKind = "loot"
	SubjectAction      SubjectKind = "action"
	SubjectDiagnostic  SubjectKind = "diagnostic"
	SubjectEnriched    SubjectKind = "enriched"
	SubjectCommand     SubjectKind = "command"
	SubjectCommandAck  SubjectKind = "command_ack"
	SubjectHealth      SubjectKind = "health"
	SubjectFrame       SubjectKind = "frame"
	SubjectScreen      SubjectKind = "screen"
	SubjectTap         SubjectKind = "tap"
	SubjectSequence    SubjectKind = "sequence"
	SubjectClassifier  SubjectKind = "classifier"
	SubjectDeploy      SubjectKind = "deploy"
	SubjectSummary     SubjectKind = "summary"
	SubjectRestart     SubjectKind = "restart"
	SubjectStuck       SubjectKind = "stuck"
)

func Subject(deviceID string, kind SubjectKind) string {
	if deviceID == "" {
		deviceID = "default"
	}
	return DefaultSubjectRoot + "." + string(kind) + "." + deviceID
}

type Options struct {
	URL             string
	DeviceID        string
	StreamName      string
	ConnectTimeout  time.Duration
	Retention       time.Duration
	LogWarn         func(string, ...interface{})
	LogInfo         func(string, ...interface{})
	NoopOnFail      bool
	AsyncBufferSize int
}

func DefaultOptions() Options {
	return Options{
		URL:             DefaultNATSURL,
		DeviceID:        "default",
		StreamName:      DefaultStreamName,
		ConnectTimeout:  2 * time.Second,
		Retention:       24 * time.Hour,
		NoopOnFail:      true,
		AsyncBufferSize: 8192,
	}
}
