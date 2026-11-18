//go:build !cli

package main

// app_selftest.go — drive the real window for a real-runtime check.
//
// The packaged app is the only place Wails IPC exists: the browser build runs
// web/public/wails-stub.js instead, and a WKWebView has no display automation,
// so the first-run wizard and the account drawer can only be exercised — not
// simulated — from inside the window. With CLASHGO_SELFTEST_JS pointing at a
// script, that script runs once the DOM is ready and reports back through the
// app's own bindings (there is no IPC channel in the other direction).
//
// Inert unless the variable is set: nothing in the shipped UI depends on it.

import (
	"context"
	"os"
	"time"

	"github.com/rs/zerolog/log"
	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// The driver runs after the first paint (React commits, then the driver polls
// its own selectors) and is offered a few more times afterwards: OnDomReady is
// not guaranteed to fire again after the page itself reloads, and a driver that
// reloads to reach the first-run screen would otherwise be injected once and
// never run. Drivers guard against double execution themselves.
const (
	selfTestInjectDelay = 700 * time.Millisecond
	selfTestInjectTries = 6
	selfTestInjectGap   = 2 * time.Second
)

// runSelfTestIfRequested is wired to OnDomReady.
func (a *App) runSelfTestIfRequested(ctx context.Context) {
	path := os.Getenv("CLASHGO_SELFTEST_JS")
	if path == "" {
		return
	}
	script, err := os.ReadFile(path)
	if err != nil {
		log.Error().Err(err).Str("path", path).Msg("self-test: cannot read driver script")
		return
	}
	log.Info().Str("path", path).Msg("self-test: injecting driver into the real window")
	go func() {
		time.Sleep(selfTestInjectDelay)
		for attempt := 1; attempt <= selfTestInjectTries; attempt++ {
			runtime.WindowExecJS(ctx, string(script))
			time.Sleep(selfTestInjectGap)
		}
	}()
}
