//go:build !darwin

// Package adb — emulator_stub.go
//
// Non-darwin build. Mirrors the method signatures declared in
// emulator_mac.go so the package compiles on Linux/Windows but no
// BlueStacks-specific logic ever executes on those platforms.
// All BlueStacks auto-config entry points return an error here.
package adb

import (
	"context"
	"errors"
	"time"
)

// EnsureBlueStacksMac is a stub for non-macOS platforms.
func (c *Client) EnsureBlueStacksMac(width, height, dpi int) error {
	return errors.New("BlueStacks auto-config only supported on macOS")
}

// EnsureBlueStacksMacCtx is a stub for non-macOS platforms. Mirrors
// the emulator_mac.go signature so the boot orchestrator compiles.
func (c *Client) EnsureBlueStacksMacCtx(_ context.Context, width, height, dpi int) error {
	return errors.New("BlueStacks auto-config only supported on macOS")
}

// isBlueStacksDevice is a permissive stub for non-darwin platforms.
// We can't run `pgrep` to check for a BlueStacks process on
// Linux/Windows in any meaningful way, and `getprop ro.product.manufacturer`
// may not distinguish BlueStacks from a generic Android emulator on
// those platforms. The user is responsible for pointing DeviceID
// at the correct emulator.
func (c *Client) isBlueStacksDevice(id string) bool {
	return true
}

// waitForVMProcess is a stub for non-darwin platforms — mirrors
// the signature used by emulator_mac.go so the package compiles.
func (c *Client) waitForVMProcess(_ context.Context, _ time.Duration) error {
	return errors.New("BlueStacks auto-config only supported on macOS")
}

// waitForBlueStacksADB is a stub for non-darwin platforms. Mirror
// of the darwin waitForBlueStacksADB signature.
func (c *Client) waitForBlueStacksADB(_ context.Context, _ time.Duration) error {
	return errors.New("BlueStacks auto-config only supported on macOS")
}

// launchBlueStacks is a stub for non-darwin platforms.
func (c *Client) launchBlueStacks(_ bool, _ int, _ int, _ int) error {
	return errors.New("BlueStacks auto-config only supported on macOS")
}

// writeResolutionDefaultsIfPlistExists is a no-op stub for non-darwin.
func (c *Client) writeResolutionDefaultsIfPlistExists(_, _, _ int) {}

// firstVMSignal is a stub that returns "" on non-darwin platforms.
func (c *Client) firstVMSignal() string { return "" }

// tcpScanListens is a stub that returns an empty slice on non-darwin.
func (c *Client) tcpScanListens(_ []int, _ time.Duration) []int { return nil }
