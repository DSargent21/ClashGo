package adb

import (
	"context"
	"errors"
	"testing"
)

// The input guard is the seam long-running operations (the deployment guard,
// the battle-end wait) use to reject unsafe input without wrapping every tap
// call site. It composes with the owner context: ctx first, then the guard.

var errGuard = errors.New("input rejected by guard")

func guardBlockedCalls(client *Client) []struct {
	name string
	call func() error
} {
	return []struct {
		name string
		call func() error
	}{
		{"tap", func() error { return client.Tap(10, 20) }},
		{"tap async", func() error { return client.TapAsync(10, 20) }},
		{"tap fast", func() error { return client.TapFast(10, 20, 0) }},
		{"tap fast async", func() error { return client.TapFastAsync(10, 20, 0) }},
		{"swipe", func() error { return client.Swipe(1, 2, 3, 4, 100) }},
		{"text", func() error { return client.Text("text") }},
		{"key event", func() error { return client.KeyEvent(4) }},
	}
}

// Every input path must evaluate the guard before touching transport, so an
// erroring guard blocks all of them with the guard's own error.
func TestInputGuardBlocksEveryInputPath(t *testing.T) {
	client := NewClient(WithJitterTaps(false))
	client.SetInputGuard(func() error { return errGuard })
	defer client.SetInputGuard(nil)

	for _, check := range guardBlockedCalls(client) {
		t.Run(check.name, func(t *testing.T) {
			if err := check.call(); !errors.Is(err, errGuard) {
				t.Fatalf("error = %v, want errGuard", err)
			}
		})
	}
}

// A passing guard lets input through (checkInputContext returns nil), and
// clearing with nil stops evaluating it entirely.
func TestInputGuardEvaluatesAndClears(t *testing.T) {
	client := NewClient()
	calls := 0
	client.SetInputGuard(func() error { calls++; return nil })

	if err := client.checkInputContext(); err != nil {
		t.Fatalf("passing guard: checkInputContext() = %v, want nil", err)
	}
	if calls != 1 {
		t.Fatalf("guard evaluated %d times, want 1", calls)
	}

	client.SetInputGuard(nil)
	if err := client.checkInputContext(); err != nil {
		t.Fatalf("cleared guard: checkInputContext() = %v, want nil", err)
	}
	if calls != 1 {
		t.Fatalf("cleared guard still evaluated (%d calls)", calls)
	}
}

// The owner context outranks the guard: a canceled context must report
// context.Canceled even while a guard is installed.
func TestInputContextTakesPrecedenceOverGuard(t *testing.T) {
	client := NewClient()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	client.SetInputContext(ctx)
	client.SetInputGuard(func() error { return errGuard })
	defer func() {
		client.SetInputContext(nil)
		client.SetInputGuard(nil)
	}()

	if err := client.Tap(1, 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("Tap() = %v, want context.Canceled", err)
	}
}
