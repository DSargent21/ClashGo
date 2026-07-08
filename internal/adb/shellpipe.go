package adb

import (
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ShellPipe owns a dedicated persistent "shell:sh" adb connection (separate
// from the Transport used by CaptureToMat / one-shot shell commands). Keeping
// the pipe on its own connection prevents raw RGBA screencap output from
// corrupting shell stdout/stderr.
//
// Lifecycle: opened lazily on first interactive call, closed at end of attack
// cycle (or on client.Close()). A per-command done-channel inside the worker
// provides true socket-flush confirmation for the Send path; SendAsync is
// O(1) (no per-command ack).
//
// Cancellation: a stopCh is closed on Close() to unblock the worker
// goroutine. cmdCh is intentionally NEVER closed to avoid panic from any
// pre- or mid-flight Send calls; after Close, broken=true causes Send to
// return ErrShellPipeBroken and the worker drains the residual channel then
// exits via stopCh.
type ShellPipe struct {
	deviceID string
	host     string
	port     int
	timeout  time.Duration
	logger   Logger

	mu      sync.Mutex
	conn    net.Conn
	cmdCh   chan pipeCmd
	stopCh  chan struct{}
	started atomic.Bool
	closed  atomic.Bool
	broken  atomic.Bool

	// Capacity 100 = safe backpressure: a backlog larger than this means the
	// worker cannot drain fast enough (ADB hung, emulator stalled) and we'd
	// rather block than drop a CoC tap.
	cmdCap int
}

type pipeCmd struct {
	cmd  string
	done chan struct{}
}

// NewShellPipe constructs a ShellPipe; call Start before use.
func NewShellPipe(deviceID, host string, port int, timeout time.Duration, logger Logger) *ShellPipe {
	if logger == nil {
		logger = nopLogger{}
	}
	return &ShellPipe{
		deviceID: deviceID,
		host:     host,
		port:     port,
		timeout:  timeout,
		logger:   logger,
		cmdCh:    make(chan pipeCmd, 100),
		stopCh:   make(chan struct{}),
		cmdCap:   100,
	}
}

// Start opens the persistent "shell:sh" connection and spawns the worker
// goroutine + stdout drainer. Safe to call repeatedly (idempotent).
func (p *ShellPipe) Start() error {
	if p.started.Load() {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.started.Load() {
		return nil
	}

	addr := net.JoinHostPort(p.host, fmt.Sprintf("%d", p.port))
	conn, err := net.DialTimeout("tcp", addr, DialTimeout)
	if err != nil {
		return fmt.Errorf("dial adb: %w", err)
	}

	// 1. host:transport:<deviceID>
	if err := sendADBPacket(conn, "host:transport:"+p.deviceID, p.timeout); err != nil {
		conn.Close()
		return fmt.Errorf("transport: %w", err)
	}
	// 2. exec:sh
	if err := sendADBPacket(conn, "exec:sh", p.timeout); err != nil {
		conn.Close()
		return fmt.Errorf("exec:sh: %w", err)
	}

	p.conn = conn
	p.started.Store(true)

	go p.runWorker()
	go p.discardReader()
	p.logger.Info("adb persistent shell pipe started")
	return nil
}

// sendADBPacket mirrors transport.sendServiceLocked / readFailureLocked for
// the one-shot packets used during Start (we open "shell:sh" once and then
// write raw bytes thereafter).
func sendADBPacket(conn net.Conn, service string, timeout time.Duration) error {
	payload := fmt.Sprintf("%04x%s", len(service), service)
	conn.SetWriteDeadline(time.Now().Add(timeout))
	if _, err := conn.Write([]byte(payload)); err != nil {
		return fmt.Errorf("write service: %w", err)
	}
	conn.SetReadDeadline(time.Now().Add(timeout))
	status := make([]byte, 4)
	if _, err := io.ReadFull(conn, status); err != nil {
		return fmt.Errorf("read status: %w", err)
	}
	if string(status) == "OKAY" {
		return nil
	}
	if string(status) != "FAIL" {
		return fmt.Errorf("adb: status=%s", string(status))
	}
	conn.SetReadDeadline(time.Now().Add(timeout))
	lenBytes := make([]byte, 4)
	if _, err := io.ReadFull(conn, lenBytes); err != nil {
		return fmt.Errorf("read failure len: %w", err)
	}
	var msgLen uint32
	if _, err := fmt.Sscanf(string(lenBytes), "%04x", &msgLen); err != nil {
		return fmt.Errorf("parse failure len: %w", err)
	}
	if msgLen > 4096 {
		return fmt.Errorf("failure message too long: %d", msgLen)
	}
	if msgLen == 0 {
		return errors.New("adb: FAIL (no message)")
	}
	conn.SetReadDeadline(time.Now().Add(timeout))
	msg := make([]byte, msgLen)
	if _, err := io.ReadFull(conn, msg); err != nil {
		return fmt.Errorf("read failure msg: %w", err)
	}
	return fmt.Errorf("adb: %s", string(msg))
}

// Send blocks until the command has been written to the underlying socket
// (true socket-flush sync). Returns ErrShellPipeBroken if the pipe is broken
// OR has been Close()d; the caller must then fall back to legacy transport.Exec.
func (p *ShellPipe) Send(cmd string) error {
	if !p.started.Load() {
		if err := p.Start(); err != nil {
			return err
		}
	}
	if p.broken.Load() || p.closed.Load() {
		return ErrShellPipeBroken
	}
	if err := p.sendEnqueue(cmd, false); err != nil {
		return err
	}
	return nil
}

// SendAsync queues the command and returns immediately. Returns
// ErrShellPipeBroken if pipe is broken/closed, or ErrShellPipeBusy if the
// channel is full (the caller may then fall back to legacy transport).
func (p *ShellPipe) SendAsync(cmd string) error {
	if !p.started.Load() {
		if err := p.Start(); err != nil {
			return err
		}
	}
	if p.broken.Load() || p.closed.Load() {
		return ErrShellPipeBroken
	}
	if err := p.sendEnqueue(cmd, true); err != nil {
		return err
	}
	return nil
}

// sendEnqueue frames the command (newline-terminate), then either blocks
// until the worker has flushed to socket (sync) or returns immediately
// (async). Tracks the per-command done channel so Send can wait on it.
func (p *ShellPipe) sendEnqueue(cmd string, async bool) error {
	if !strings.HasSuffix(cmd, "\n") {
		cmd = cmd + "\n"
	}
	if async {
		select {
		case p.cmdCh <- pipeCmd{cmd: cmd, done: nil}:
			return nil
		default:
			return ErrShellPipeBusy
		}
	}
	done := make(chan struct{})
	select {
	case p.cmdCh <- pipeCmd{cmd: cmd, done: done}:
	case <-time.After(5 * time.Second):
		return errors.New("adb shell pipe enqueue timeout (channel full)")
	case <-p.stopCh:
		return ErrShellPipeBroken
	}
	select {
	case <-done:
		return nil
	case <-time.After(5 * time.Second):
		return errors.New("adb shell pipe write timeout")
	case <-p.stopCh:
		return ErrShellPipeBroken
	}
}

// runWorker consumes from cmdCh and writes serially to the persistent
// conn. Exits cleanly on stopCh close. Never closes cmdCh so post-Close
// pending senders see ErrShellPipeBroken via broken/closed checks rather
// than a closed-channel panic.
func (p *ShellPipe) runWorker() {
	for {
		select {
		case <-p.stopCh:
			p.drainOnExit()
			return
		case pc, ok := <-p.cmdCh:
			if !ok {
				p.drainOnExit()
				return
			}
			p.handlePipeCmd(pc)
		}
	}
}

// drainOnExit closes any per-cmd `done` channels for residual commands so
// blocked synchronous senders (already past the enqueue select) are
// released rather than hanging on their 5s timeout.
func (p *ShellPipe) drainOnExit() {
	for {
		select {
		case pc := <-p.cmdCh:
			if pc.done != nil {
				close(pc.done)
			}
		default:
			return
		}
	}
}

func (p *ShellPipe) handlePipeCmd(pc pipeCmd) {
	p.mu.Lock()
	conn := p.conn
	p.mu.Unlock()
	if conn == nil {
		p.broken.Store(true)
		if pc.done != nil {
			close(pc.done)
		}
		return
	}
	conn.SetWriteDeadline(time.Now().Add(p.timeout))
	if _, err := conn.Write([]byte(pc.cmd)); err != nil {
		p.broken.Store(true)
		p.logger.Warn(fmt.Sprintf("adb shell pipe write failed: %v (caller will fallback)", err))
		p.mu.Lock()
		if p.conn != nil {
			p.conn.Close()
			p.conn = nil
		}
		p.mu.Unlock()
		if pc.done != nil {
			close(pc.done)
		}
		return
	}
	if pc.done != nil {
		close(pc.done)
	}
}

// discardReader drains stdout/stderr to prevent backpressure-induced
// blocking on the socket. Exits on EOF / read error.
func (p *ShellPipe) discardReader() {
	p.mu.Lock()
	conn := p.conn
	p.mu.Unlock()
	if conn == nil {
		return
	}
	buf := make([]byte, 4096)
	for {
		_, err := conn.Read(buf)
		if err != nil {
			p.broken.Store(true)
			if err != io.EOF {
				p.logger.Warn(fmt.Sprintf("adb shell pipe reader stopped: %v", err))
			}
			return
		}
		_ = buf // discard; tap/keyevent commands produce no useful output
	}
}

// IsBroken reports whether the pipe has failed; the caller should use the
// legacy transport for subsequent commands.
func (p *ShellPipe) IsBroken() bool { return p.broken.Load() }

// IsStarted reports whether Start has succeeded.
func (p *ShellPipe) IsStarted() bool { return p.started.Load() }

// Close releases the underlying socket + worker goroutines. Idempotent.
// Sets broken=true and signals stopCh so the worker exits cleanly without
// closing cmdCh (avoiding closed-channel send panics).
func (p *ShellPipe) Close() error {
	if !p.started.Load() {
		return nil
	}
	if p.closed.Swap(true) {
		return nil
	}
	p.broken.Store(true)
	p.mu.Lock()
	if p.conn != nil {
		_ = p.conn.Close()
		p.conn = nil
	}
	p.mu.Unlock()
	// Wake the worker + any blocked synchronous senders via stopCh close.
	select {
	case <-p.stopCh:
		// already closed by a prior concurrent caller
	default:
		close(p.stopCh)
	}
	p.logger.Info("adb persistent shell pipe closed")
	return nil
}

var (
	ErrShellPipeBroken = errors.New("adb shell pipe is broken; caller must fallback to legacy transport")
	ErrShellPipeBusy   = errors.New("adb shell pipe channel is full (async fallback requested)")
)
