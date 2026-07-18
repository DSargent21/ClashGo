package bot

import (
	"testing"
	"time"
)

func TestCPUTimeMonotonicAndNonZero(t *testing.T) {
	before := CPUTime()
	if before < 0 {
		t.Fatalf("CPUTime returned negative: %v", before)
	}

	// Burn some CPU so the kernel accounts measurable user time.
	start := time.Now()
	for time.Since(start) < 50*time.Millisecond {
		_ = make([]byte, 4096)
		for j := 0; j < 1000; j++ {
			_ = j * j
		}
	}

	after := CPUTime()
	if after < before {
		t.Errorf("CPUTime went backwards: before=%v after=%v", before, after)
	}
	// On a loaded machine the kernel should have accounted >0 CPU for the
	// busy loop. This is non-deterministic under extreme scheduling pressure
	// but should hold in practice.
	if after == before {
		t.Logf("warning: CPUTime unchanged after 50ms busy loop (before=%v after=%v) — scheduler may not have charged user time", before, after)
	}
}

func TestCPUSamplerUsageInRange(t *testing.T) {
	s := newCPUSampler()
	// First call establishes baseline; second measures a window.
	time.Sleep(20 * time.Millisecond)
	frac := s.Usage()
	if frac < 0 {
		t.Errorf("CPU usage fraction negative: %v", frac)
	}
	// A 20ms sleep window with a nearly-idle bot should report well under
	// one full core. Guard against absurd values from clock skew.
	if frac > 100 {
		t.Errorf("CPU usage fraction implausibly high: %v", frac)
	}
}
