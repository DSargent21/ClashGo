package adb

import (
	"context"
	"errors"
	"testing"
)

func TestInputMethodsRejectAfterContextCancellation(t *testing.T) {
	client := NewClient(WithJitterTaps(false))
	ctx, cancel := context.WithCancel(context.Background())
	client.SetInputContext(ctx)
	cancel()

	checks := []struct {
		name string
		call func() error
	}{
		{"tap", func() error { return client.Tap(10, 20) }},
		{"tap async", func() error { return client.TapAsync(10, 20) }},
		{"tap fast", func() error { return client.TapFast(10, 20, 0) }},
		{"tap fast async", func() error { return client.TapFastAsync(10, 20, 0) }},
		{"tap dual", func() error { return client.TapDual(1, 2, 0, 3, 4, 0) }},
		{"tap triple", func() error { return client.TapTriple(1, 2, 0, 3, 4, 0, 5, 6, 0) }},
		{"swipe", func() error { return client.Swipe(1, 2, 3, 4, 100) }},
		{"text", func() error { return client.Text("text") }},
		{"key event", func() error { return client.KeyEvent(4) }},
	}

	for _, check := range checks {
		t.Run(check.name, func(t *testing.T) {
			if err := check.call(); !errors.Is(err, context.Canceled) {
				t.Fatalf("input error = %v, want context.Canceled", err)
			}
		})
	}
}

func TestInputContextCanBeCleared(t *testing.T) {
	client := NewClient()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	client.SetInputContext(ctx)
	if err := client.checkInputContext(); !errors.Is(err, context.Canceled) {
		t.Fatalf("bound input context error = %v, want context.Canceled", err)
	}

	client.SetInputContext(nil)
	if err := client.checkInputContext(); err != nil {
		t.Fatalf("cleared input context error = %v, want nil", err)
	}
}
