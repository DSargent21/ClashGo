package adb

import (
	"errors"
	"time"
)

var (
	ErrNotConnected    = errors.New("not connected to ADB server")
	ErrWriteTimeout    = errors.New("write timeout")
	ErrReadTimeout     = errors.New("read timeout")
	ErrServerFailure   = errors.New("ADB server failure")
	ErrTransportGone   = errors.New("transport lost")
	ErrInvalidResponse = errors.New("invalid ADB response")
)

const (
	DefaultHost    = "127.0.0.1"
	DefaultPort    = 5037
	DefaultTimeout = 30 * time.Second
	DialTimeout    = 5 * time.Second
)

type Logger interface {
	Debug() bool
	Debugf(format string, v ...any)
	Info(msg string)
	Warn(msg string)
	Error(msg string)
	WithFields(fields map[string]any) Logger
}

type nopLogger struct{}

func (nopLogger) Debug() bool                         { return false }
func (nopLogger) Debugf(string, ...any)               {}
func (nopLogger) Info(string)                         {}
func (nopLogger) Warn(string)                         {}
func (nopLogger) Error(string)                        {}
func (n nopLogger) WithFields(map[string]any) Logger { return n }

type Option func(*Client)

func WithHost(host string) Option {
	return func(c *Client) { c.host = host }
}

func WithPort(port int) Option {
	return func(c *Client) { c.port = port }
}

func WithLogger(l Logger) Option {
	return func(c *Client) { c.log = l }
}

func WithTimeout(d time.Duration) Option {
	return func(c *Client) { c.timeout = d }
}

func WithDeviceID(id string) Option {
	return func(c *Client) { c.DeviceID = id }
}

type Health struct {
	LastCapture      time.Time `json:"last_capture"`
	AvgCaptureMs     float64   `json:"avg_capture_ms"`
	ConsecutiveFails int       `json:"consecutive_fails"`
	CapturesTotal    uint64    `json:"captures_total"`
	ErrorsTotal      uint64    `json:"errors_total"`
	LastError        string    `json:"last_error"`
}

func (h *Health) RecordSuccess(d time.Duration) {
	h.LastCapture = time.Now()
	ms := d.Seconds() * 1000
	if h.AvgCaptureMs == 0 {
		h.AvgCaptureMs = ms
	} else {
		h.AvgCaptureMs = h.AvgCaptureMs*0.9 + ms*0.1
	}
	h.ConsecutiveFails = 0
}

func (h *Health) RecordFailure(err error) {
	h.ConsecutiveFails++
	if err != nil {
		h.LastError = err.Error()
	}
}

func (h *Health) IsHealthy() bool {
	return h.ConsecutiveFails < 3
}