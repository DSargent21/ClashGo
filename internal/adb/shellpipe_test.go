package adb

import (
	"io"
	"net"
	"testing"
	"time"
)

func TestShellPipeHandlePipeCmdAcknowledgesSuccessfulWrite(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	p := NewShellPipe("", "", 0, time.Second, nil)
	p.conn = client

	const command = "input tap 10 20\n"
	done := make(chan error, 1)
	go p.handlePipeCmd(pipeCmd{cmd: command, done: done})

	got := make([]byte, len(command))
	if _, err := io.ReadFull(server, got); err != nil {
		t.Fatalf("read command: %v", err)
	}
	if string(got) != command {
		t.Fatalf("command = %q, want %q", got, command)
	}
	if err := <-done; err != nil {
		t.Fatalf("write acknowledgement = %v, want nil", err)
	}
}

func TestShellPipeHandlePipeCmdPropagatesWriteFailure(t *testing.T) {
	client, server := net.Pipe()
	_ = server.Close()
	defer client.Close()

	p := NewShellPipe("", "", 0, time.Second, nil)
	p.conn = client
	done := make(chan error, 1)
	p.handlePipeCmd(pipeCmd{cmd: "input tap 10 20\n", done: done})

	if err := <-done; err == nil {
		t.Fatal("write acknowledgement = nil after peer closed; caller would silently drop tap")
	}
	if !p.IsBroken() {
		t.Fatal("pipe not marked broken after write failure")
	}
}

func TestShellPipeDrainOnExitFailsPendingSends(t *testing.T) {
	p := NewShellPipe("", "", 0, time.Second, nil)
	done := make(chan error, 1)
	p.cmdCh <- pipeCmd{cmd: "input tap 10 20\n", done: done}

	p.drainOnExit()
	if err := <-done; err == nil {
		t.Fatal("pending send acknowledged success even though worker discarded command")
	}
}
