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
}

type nopLogger struct{}

func (nopLogger) Debug() bool        { return false }
func (nopLogger) Debugf(string, ...any) {}
func (nopLogger) Info(string)         {}
func (nopLogger) Warn(string)         {}
func (nopLogger) Error(string)        {}

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
	LastCapture      time.Time
	AvgCaptureMs     float64
	ConsecutiveFails int
	CapturesTotal    uint64
	ErrorsTotal      uint64
	LastError        string
}

func (h *Health) RecordSuccess(d time.Duration) {
	h.LastCapture = time.Now()
	h.AvgCaptureMs = h.AvgCaptureMs*0.9 + d.Seconds()*1000*0.1
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