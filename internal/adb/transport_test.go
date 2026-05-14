package adb

import (
	"bytes"
	"encoding/binary"
	"testing"
	"time"

	"os/exec"
)

func TestTransportConnect(t *testing.T) {
	t.Skip("requires real ADB server")

	transport, err := NewTransport("localhost:5555", "127.0.0.1", 5037, 30*time.Second)
	if err != nil {
		t.Fatalf("transport connect: %v", err)
	}
	defer transport.Close()

	if !transport.IsConnected() {
		t.Fatal("transport should be connected")
	}

	t.Log("Connected to ADB server successfully")
}

func TestTransportCapture(t *testing.T) {
	t.Skip("requires real ADB device")

	transport, err := NewTransport("localhost:5555", "127.0.0.1", 5037, 30*time.Second)
	if err != nil {
		t.Fatalf("transport connect: %v", err)
	}
	defer transport.Close()

	data, err := transport.ExecRaw("exec:exec-out screencap")
	if err != nil {
		t.Fatalf("screencap: %v", err)
	}

	if len(data) < 12 {
		t.Fatalf("screencap data too short: %d bytes", len(data))
	}

	width := binary.LittleEndian.Uint32(data[0:4])
	height := binary.LittleEndian.Uint32(data[4:8])
	t.Logf("Screen: %dx%d (%d bytes)", width, height, len(data))
}

func TestTransportTap(t *testing.T) {
	t.Skip("requires real ADB device")

	transport, err := NewTransport("localhost:5555", "127.0.0.1", 5037, 30*time.Second)
	if err != nil {
		t.Fatalf("transport connect: %v", err)
	}
	defer transport.Close()

	if err := transport.Tap(430, 566); err != nil {
		t.Fatalf("tap: %v", err)
	}

	t.Log("Tap executed successfully")
}

func TestTransportReconnect(t *testing.T) {
	t.Skip("requires real ADB server")

	transport, err := NewTransport("localhost:5555", "127.0.0.1", 5037, 30*time.Second)
	if err != nil {
		t.Fatalf("transport connect: %v", err)
	}
	defer transport.Close()

	if !transport.IsConnected() {
		t.Fatal("should be connected initially")
	}
}

func TestClientCaptureToMat(t *testing.T) {
	t.Skip("requires real ADB device")

	client := NewClient(
		WithHost("127.0.0.1"),
		WithPort(5037),
	)
	defer client.Close()

	if err := client.Connect(); err != nil {
		t.Fatalf("connect: %v", err)
	}

	mat, err := client.CaptureToMat()
	if err != nil {
		t.Fatalf("capture: %v", err)
	}
	defer mat.Close()

	if mat.Empty() {
		t.Fatal("mat is empty")
	}
	t.Logf("Mat: %dx%d", mat.Cols(), mat.Rows())
}

func TestClientReconnect(t *testing.T) {
	t.Skip("requires real ADB device")

	client := NewClient(
		WithHost("127.0.0.1"),
		WithPort(5037),
	)
	defer client.Close()

	if err := client.Connect(); err != nil {
		t.Fatalf("connect: %v", err)
	}

	if !client.IsConnected() {
		t.Fatal("should be connected")
	}

	if err := client.Reconnect(); err != nil {
		t.Fatalf("reconnect: %v", err)
	}

	if !client.IsConnected() {
		t.Fatal("should be reconnected")
	}
}

func captureProcessSpawn() ([]byte, error) {
	cmd := exec.Command("adb", "-s", "localhost:5555", "exec-out", "screencap")
	out := &bytes.Buffer{}
	cmd.Stdout = out
	if err := cmd.Run(); err != nil {
		return nil, err
	}

	data := out.Bytes()
	if len(data) < 12 {
		return nil, nil
	}

	width := binary.LittleEndian.Uint32(data[0:4])
	height := binary.LittleEndian.Uint32(data[4:8])
	expected := int(width*height*4) + 12
	if len(data) < expected {
		return data, nil
	}
	return data, nil
}

func TestProcessSpawnCapture(t *testing.T) {
	t.Skip("requires real ADB device")

	data, err := captureProcessSpawn()
	if err != nil {
		t.Fatalf("capture: %v", err)
	}
	if len(data) < 12 {
		t.Fatalf("short: %d", len(data))
	}
}

func BenchmarkTransportScreencap(b *testing.B) {
	b.Skip("requires real ADB device")

	transport, err := NewTransport("localhost:5555", "127.0.0.1", 5037, 30*time.Second)
	if err != nil {
		b.Fatalf("transport connect: %v", err)
	}
	defer transport.Close()

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		data, err := transport.ExecRaw("exec:exec-out screencap")
		if err != nil {
			b.Fatalf("screencap: %v", err)
		}
		if len(data) < 12 {
			b.Fatalf("short screencap: %d bytes", len(data))
		}
	}
}

func BenchmarkClientScreencap(b *testing.B) {
	b.Skip("requires real ADB device")

	client := NewClient(
		WithHost("127.0.0.1"),
		WithPort(5037),
	)
	defer client.Close()

	if err := client.Connect(); err != nil {
		b.Fatalf("connect: %v", err)
	}

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		mat, err := client.CaptureToMat()
		if err != nil {
			b.Fatalf("capture: %v", err)
		}
		if mat.Empty() {
			b.Fatal("empty mat")
		}
		mat.Close()
	}
}

func BenchmarkProcessSpawnScreencap(b *testing.B) {
	b.Skip("requires real ADB device")

	for i := 0; i < b.N; i++ {
		data, err := captureProcessSpawn()
		if err != nil {
			b.Fatalf("capture: %v", err)
		}
		if len(data) < 12 {
			b.Fatalf("short: %d", len(data))
		}
	}
}

func BenchmarkPersistentVsProcessSpawn(b *testing.B) {
	b.Skip("comparison test: persistent vs exec.Command")

	b.Run("persistent transport", func(b *testing.B) {
		transport, err := NewTransport("localhost:5555", "127.0.0.1", 5037, 30*time.Second)
		if err != nil {
			b.Fatalf("transport connect: %v", err)
		}
		defer transport.Close()

		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			_, err := transport.ExecRaw("exec:exec-out screencap")
			if err != nil {
				b.Fatalf("capture: %v", err)
			}
		}
	})

	b.Run("process spawn (old way)", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			data, err := captureProcessSpawn()
			if err != nil {
				b.Fatalf("capture: %v", err)
			}
			if len(data) < 12 {
				b.Fatalf("short screencap: %d bytes", len(data))
			}
		}
	})
}