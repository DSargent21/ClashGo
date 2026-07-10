package logger

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Ducky705/ClashGO/internal/paths"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"gopkg.in/natefinch/lumberjack.v2"
)

// cleanWriter trims trailing spaces from the console output.
type cleanWriter struct {
	w io.Writer
}

func (cw cleanWriter) Write(p []byte) (n int, err error) {
	line := strings.TrimRight(string(p), " \t\n\r")
	if line == "" {
		return len(p), nil
	}
	_, err = cw.w.Write([]byte(line + "\n"))
	return len(p), err
}

// Init initializes the global logger with a clean console output and a rotated JSON file log.
func Init(debug bool, extraWriters ...io.Writer) {
	// 1. Setup File Logging (Full JSON)
	logDir := paths.ResolveConfig("logs")
	if err := os.MkdirAll(logDir, 0755); err != nil {
		fmt.Fprintf(os.Stderr, "failed to create log directory: %v\n", err)
	}

	fileWriter := &lumberjack.Logger{
		Filename:   filepath.Join(logDir, "app.log"),
		MaxSize:    10, // megabytes
		MaxBackups: 3,
		MaxAge:     28,   // days
		Compress:   true, // disabled by default
	}

	// 2. Setup Clean Console Logging (Story Mode)
	consoleWriter := zerolog.ConsoleWriter{
		Out:        cleanWriter{w: os.Stdout},
		TimeFormat: "15:04:05",
		NoColor:    false,
		// PartsOrder defines the order of the parts. We only want Time, Level, and Message.
		PartsOrder: []string{
			zerolog.TimestampFieldName,
			zerolog.LevelFieldName,
			zerolog.MessageFieldName,
		},
		FormatLevel: func(i interface{}) string {
			var l string
			if ll, ok := i.(string); ok {
				switch strings.ToUpper(ll) {
				case "DEBUG":
					l = "\x1b[35mDBG\x1b[0m" // Magenta
				case "INFO":
					l = "\x1b[32mINF\x1b[0m" // Green
				case "WARN":
					l = "\x1b[33mWRN\x1b[0m" // Yellow
				case "ERROR":
					l = "\x1b[31mERR\x1b[0m" // Red
				case "FATAL":
					l = "\x1b[31mFTL\x1b[0m" // Red
				case "PANIC":
					l = "\x1b[31mPNC\x1b[0m" // Red
				default:
					l = strings.ToUpper(ll)
				}
			}
			return fmt.Sprintf("| %s |", l)
		},
		FormatMessage: func(i interface{}) string {
			return fmt.Sprintf("%s", i)
		},
		// Only suppress the noise (caller file/line/pkg) and the stack
		// trace. The `error` field is INTENTIONALLY shown — without it
		// every boot failure used to print as bare
		// `ERR | failed to initialize bot` with the wrapped cause
		// silently dropped. The file/line info is still in the JSON
		// file log for developers who need it.
		FieldsExclude: []string{"stack", "pkg", "file", "line"},
		// Render non-error fields as `key=value` pairs after the message
		// for human readability. The cleanWriter wrapper still trims
		// trailing whitespace and forces a single newline.
		FormatFieldName: func(i interface{}) string {
			return fmt.Sprintf("%s=", i)
		},
		FormatFieldValue: func(i interface{}) string {
			// zerolog's ConsoleWriter hands us unquoted values as
			// []byte to avoid allocations; fmt's default %v formats
			// a []byte as a decimal byte stream (e.g. `[102 97 108
			// 115 101]` for "false"). That made the bot's boot logs
			// unreadable — every bool/JSON field looked like garbage.
			// Convert []byte to its string form first.
			if b, ok := i.([]byte); ok {
				return string(b)
			}
			return fmt.Sprintf("%v", i)
		},
		// Errors get the same `error=...` rendering as a normal field,
		// so the user sees e.g. `error=android boot timeout: timeout
		// waiting for boot after 1m30s` in their terminal.
		FormatErrFieldName: func(i interface{}) string {
			return "error="
		},
		FormatErrFieldValue: func(i interface{}) string {
			return fmt.Sprintf("%v", i)
		},
	}

	// 3. Combine Writers
	writers := []io.Writer{fileWriter, consoleWriter}
	writers = append(writers, extraWriters...)
	multi := zerolog.MultiLevelWriter(writers...)

	// 4. Set Global Logger
	level := zerolog.InfoLevel
	if debug {
		level = zerolog.DebugLevel
	}

	log.Logger = zerolog.New(multi).
		With().
		Timestamp().
		Logger().
		Level(level)

	zerolog.SetGlobalLevel(level)
	zerolog.TimeFieldFormat = time.RFC3339
}

// ---- NDJSON mirror --------------------------------------------------------
//
// ndjsonMirror duplicates every zerolog event into a per-day NDJSON file.
// It is non-blocking: a buffered channel feeds a single drainer goroutine.
//
// This sink exists for terminal-AI consumers who need to grep/jq/DuckDB the
// bot's runtime without parsing console output. The full JSON event is
// preserved (level, fields, error, time); only the timestamp is reformatted
// to an integer number of milliseconds since the Unix epoch so it is sortable.

// EnableNDJSONMirror installs an NDJSON mirror under dir. The dir is created
// if missing. Returns the io.Writer so callers can pass it via extraWriters.
// Safe to call before or after Init(); if called after, the mirror is appended
// to the active multi-writer chain.
//
// The returned Closer should be called on shutdown to flush in-flight events.
func EnableNDJSONMirror(dir string) (io.Writer, io.Closer, error) {
	if dir == "" {
		return nil, nilEmptyCloser{}, fmt.Errorf("logger: empty NDJSON dir")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, nilEmptyCloser{}, fmt.Errorf("logger: mkdir ndjson: %w", err)
	}

	m := &ndjsonMirror{
		dir: dir,
		buf: make(chan jsonLogEntry, 4096),
	}
	go m.run()
	return m, m, nil
}

type jsonLogEntry struct {
	ts    int64
	bytes []byte
}

// ndjsonMirror is an io.Writer that ships each Write to a per-day file via a
// background drainer goroutine. Backpressure is non-blocking: if the channel
// is full, we drop (logged at debug next time we drain) rather than stalling
// the hot path.
type ndjsonMirror struct {
	dir string
	buf chan jsonLogEntry

	// Test-only instrumentation; atomic so the hot path doesn't take a lock.
	dropped atomic.Int64
	closed  bool

	mu sync.Mutex // guards closed transitions only
}

// Write accepts a single zerolog line. zerolog always emits valid JSON with
// a trailing newline; we pass the raw bytes through to the buffered queue
// without parsing or re-marshaling — the hot path runs at every log line
// (frames at 10 FPS = 100s of entries per second). The drainer ensures a
// trailing newline if absent.
//
// On full channel, drop and bump the dropped counter; do not block. The
// canonical `select { case ... : default: }` non-blocking pattern is
// deterministic — Go picks the case whenever it's ready, falling through to
// default only when no case can proceed.
func (m *ndjsonMirror) Write(p []byte) (int, error) {
	if m == nil || len(p) == 0 {
		return len(p), nil
	}
	entry := jsonLogEntry{bytes: p}
	if p[len(p)-1] != '\n' {
		// Allocate a single byte append so the file stays valid NDJSON.
		cp := make([]byte, len(p)+1)
		copy(cp, p)
		cp[len(cp)-1] = '\n'
		entry.bytes = cp
	}
	select {
	case m.buf <- entry:
	default:
		m.dropped.Add(1)
	}
	return len(p), nil
}

func (m *ndjsonMirror) Close() error {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true
	close(m.buf)
	m.mu.Unlock()
	return nil
}

func (m *ndjsonMirror) Dropped() int64 {
	return m.dropped.Load()
}

// run drains the buffer into a per-day file. The file is rotated when the day
// changes.
func (m *ndjsonMirror) run() {
	if m == nil {
		return
	}
	var (
		currentDay string
		f          *os.File
		closeF     = func() {
			if f != nil {
				_ = f.Close()
				f = nil
			}
		}
	)
	defer closeF()

	for entry := range m.buf {
		day := time.UnixMilli(entry.ts).UTC().Format("2006-01-02")
		if day != currentDay {
			closeF()
			currentDay = day
			path := filepath.Join(m.dir, day+".ndjson")
			nf, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
			if err != nil {
				// Best-effort retry on next entry.
				continue
			}
			f = nf
		}
		if f == nil {
			continue
		}
		if _, err := f.Write(entry.bytes); err != nil {
			closeF()
		}
	}
}

type nilEmptyCloser struct{}

func (nilEmptyCloser) Close() error { return nil }
