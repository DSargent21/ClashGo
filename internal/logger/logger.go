package logger

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
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
