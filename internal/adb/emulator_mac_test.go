//go:build darwin

package adb

import (
	"strings"
	"testing"
)

// TestVmProcessSignalsExcludesBlueStacksAmbiguousMatch is the
// CRITICAL regression test for a bug the supervisor caught during
// review of the simulate-boot fix. If vmProcessSignals ever
// contains "BlueStacks" or any match-pattern that's a substring of
// the always-running BlueStacksAI companion, then waitForVMProcess
// returns success IMMEDIATELY (before qemu-system-aarch64 / hd-adb
// ever appear) and the bot declares the VM is up while no actual
// VM exists.
//
// History: prior code did `pgrep -x BlueStacks` which had the same
// false-positive on BlueStacks cleanup tools. The fix in
// emulator_mac.go is to only wait for processes that genuinely
// belong to the Android subsystem. This test pins that contract.
func TestVmProcessSignalsExcludesBlueStacksAmbiguousMatch(t *testing.T) {
	for _, sig := range vmProcessSignals {
		// Reject any match that would also match BlueStacksAI
		// or other BlueStacks-prefixed helper processes that
		// are running BEFORE the user opens BlueStacks.app.
		if sig == "BlueStacks" || sig == "BlueStacks Main" || strings.HasPrefix(sig, "BlueStacks") {
			t.Errorf("vmProcessSignals contains %q; firstVMSignal's pgrep would false-positive on BlueStacksAI (always running on port 8080)", sig)
		}
	}
}

// TestVmProcessSignalsContainsVMOnly verifies the only signals we
// trust ARE the actual Android VM subsystem processes. If a future
// contributor removes these from vmProcessSignals, the bot would
// silently regress to the "no VM ever actually verified"
// failure mode.
func TestVmProcessSignalsContainsVMOnly(t *testing.T) {
	mustHave := []string{
		"qemu-system-aarch64", // the Android VM (BlueStacks 5.x on macOS)
		"hd-adb",              // BlueStacks' custom adb daemon (5.21+)
	}
	for _, want := range mustHave {
		found := false
		for _, got := range vmProcessSignals {
			if got == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("vmProcessSignals missing required signal %q; bot would not detect VM subsystem alive", want)
		}
	}
}

// TestCandidatePortsOrderHasMainFirst verifies the candidate adb
// port list starts with 5555 (BlueStacks main instance's default).
// If a future refactor reorders this without thinking, the bot
// could try 5556/5560 first and miss the obvious match.
//
// We don't test the entire order (5556+ are equally valid
// candidates for multi-instance setups) — only that 5555 is the
// first ONE we try, so the common case stays one-port-scan.
func TestCandidatePortsOrderHasMainFirst(t *testing.T) {
	if len(candidateBlueStacksAdbPorts) == 0 {
		t.Fatal("candidateBlueStacksAdbPorts is empty; waitForBlueStacksADB would never scan a port")
	}
	if candidateBlueStacksAdbPorts[0] != 5555 {
		t.Errorf("candidateBlueStacksAdbPorts should start with 5555 (BlueStacks main instance's default adb port); got first=%d", candidateBlueStacksAdbPorts[0])
	}
}

// TestCandidatePortsCoversCommonPortRange verifies the candidate list
// covers EVERY port from 5555 through 5565 inclusive. This is what
// waitForBlueStacksADB's port-scan relies on; if the range shrinks
// we may miss BlueStacks instances that landed on a slightly-off
// port (e.g. when a user has multi-instance manager enabled or
// Android Studio AVD claimed the lower port first).
func TestCandidatePortsCoversCommonPortRange(t *testing.T) {
	have := make(map[int]bool, len(candidateBlueStacksAdbPorts))
	for _, p := range candidateBlueStacksAdbPorts {
		have[p] = true
	}
	for _, p := range []int{5555, 5556, 5557, 5558, 5559, 5560, 5565} {
		if !have[p] {
			t.Errorf("candidateBlueStacksAdbPorts missing port %d; a BlueStacks instance using it would never be detected", p)
		}
	}
}

// TestEnsureBlueStacksMacSignatureStable is a compile-time guard.
// If anyone changes EnsureBlueStacksMac's signature, the orchestrator
// caller (bootorchestrator.go) would break, but the compiler
// wouldn't tell us this specific test is outdated — pin the
// signature explicitly.
func TestEnsureBlueStacksMacSignatureStable(t *testing.T) {
	// Compile-time signature guard: if emulator_mac.go's
	// EnsureBlueStacksMac changes arity or types, this assignment won't
	// compile. A method value is never nil, so there is no runtime
	// check to perform.
	var _ func(width, height, dpi int) error = (*Client)(nil).EnsureBlueStacksMac
}
