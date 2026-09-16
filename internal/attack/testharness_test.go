package attack

import (
	"encoding/json"
	"testing"

	"github.com/Ducky705/ClashGO/internal/adb"
	"github.com/Ducky705/ClashGO/internal/config"
)

// newClosedTestClient builds an adb client with the transport closed so
// every capture/tap fails fast — the tap hook still fires (used by the
// spell deployer tests) and capture-based loops short-circuit. No device
// needed.
func newClosedTestClient(t testing.TB) *adb.Client {
	t.Helper()
	client := adb.NewClient(
		adb.WithJitterTaps(false),
		adb.WithJitterDelays(false),
		adb.WithTimeout(1),
	)
	client.Close()
	t.Cleanup(func() { _ = client.Close() })
	return client
}

// minimalAttackConfig returns an AttackConfig safe for tests: no stall
// timer (so waits rely purely on ctx/threshold paths) and no live-OCR
// side effects.
func minimalAttackConfig() *config.AttackConfig {
	return &config.AttackConfig{
		StallTimerSeconds: 0,
	}
}

// jsonUnmarshalStall decodes stall_config.json. Exported field tags live
// on attack.StallConfig; this wrapper keeps the test import-free.
func jsonUnmarshalStall(data []byte, dst *StallConfig) error {
	return json.Unmarshal(data, dst)
}
