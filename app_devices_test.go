package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/Ducky705/ClashGO/internal/accounts"
)

// TestDetectDevicesMergesConsoleSerials covers the duplicate-emulator bug the
// live probe exposed: the adb server lists "emulator-5554" for the same
// instance that answers on localhost:5555. Offering both would let two accounts
// bind one emulator under different serials.
func TestDetectDevicesMergesConsoleSerials(t *testing.T) {
	got := mergeDeviceSerials(
		[]int{5555, 5557},
		[]string{"emulator-5554", "emulator-5556", "emulator-5558", "localhost:5557", "", "USB123"},
	)
	want := []string{"localhost:5555", "localhost:5557", "emulator-5558", "USB123"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("mergeDeviceSerials = %v, want %v", got, want)
	}

	if port, ok := emulatorConsolePort("emulator-5554"); !ok || port != 5554 {
		t.Fatalf("emulatorConsolePort(emulator-5554) = %d,%v", port, ok)
	}
	if _, ok := emulatorConsolePort("localhost:5555"); ok {
		t.Fatal("emulatorConsolePort accepted a TCP serial")
	}
	if _, ok := emulatorConsolePort("emulator-abc"); ok {
		t.Fatal("emulatorConsolePort accepted a non-numeric console serial")
	}

	// An account bound the legacy way must still read as owning the instance
	// detected under its TCP serial.
	if canonicalSerial("emulator-5554") != "localhost:5555" {
		t.Fatalf("canonicalSerial(emulator-5554) = %q, want localhost:5555", canonicalSerial("emulator-5554"))
	}
	if canonicalSerial("localhost:5556") != "localhost:5556" {
		t.Fatalf("canonicalSerial changed a TCP serial: %q", canonicalSerial("localhost:5556"))
	}
}

// TestUI_DeviceDetectionBindsFreeSerials covers the add-account device picker:
// detection must read the configured adb host, carry the account that already
// owns each serial, and never claim a free device is taken (or the reverse).
// The device layer is seamed, so no emulator or adb server is touched.
func TestUI_DeviceDetectionBindsFreeSerials(t *testing.T) {
	t.Setenv("CLASHGO_CONFIG_DIR", t.TempDir())
	a := NewApp()

	cfg := a.GetConfig()
	cfg.Device.ADBHost = "127.0.0.9"
	cfg.Device.ADBPort = 5099
	if err := writeConfigFile(cfg); err != nil {
		t.Fatalf("write config: %v", err)
	}

	oldProbe := deviceProbe
	t.Cleanup(func() { deviceProbe = oldProbe })

	var gotHost string
	var gotPort int
	deviceProbe = func(host string, port int) ([]DetectedDevice, error) {
		gotHost, gotPort = host, port
		return []DetectedDevice{
			{Serial: "localhost:5556", Model: "BlueStacks 5", Width: 1024, Height: 768},
			{Serial: "localhost:5555", Model: "BlueStacks 5", Width: 1024, Height: 768},
		}, nil
	}

	devices, err := a.DetectDevices()
	if err != nil {
		t.Fatalf("DetectDevices: %v", err)
	}
	if gotHost != "127.0.0.9" || gotPort != 5099 {
		t.Fatalf("probe called with %s:%d, want the configured 127.0.0.9:5099", gotHost, gotPort)
	}
	if len(devices) != 2 {
		t.Fatalf("devices = %+v, want both detected instances", devices)
	}

	// The bootstrapped account owns the configured serial; the other port is
	// free and therefore the one the picker may offer.
	active := ""
	for _, device := range devices {
		if device.Serial == "localhost:5555" {
			continue
		}
		if device.BoundTo != "" {
			t.Fatalf("localhost:5556 reported bound to %q before any account used it", device.BoundTo)
		}
	}
	for _, device := range devices {
		if device.Serial == "localhost:5555" && device.Active {
			active = device.BoundTo
		}
	}

	state, err := a.GetAccounts()
	if err != nil {
		t.Fatalf("GetAccounts: %v", err)
	}
	wantBound := ""
	for _, acct := range state.Accounts {
		if acct.Active {
			wantBound = acct.Name
		}
	}
	if wantBound == "" {
		t.Fatal("no active account; the bootstrap profile is missing")
	}
	if active != wantBound {
		t.Fatalf("active device reported bound to %q, want the active account %q", active, wantBound)
	}

	// Binding a second account to the free serial flips it to occupied, which
	// is what stops the picker offering it twice.
	if _, err := a.CreateAccount(accounts.Input{
		Name:    "Village 2",
		Device:  accounts.Device{DeviceID: "localhost:5556"},
		Enabled: true,
	}); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	devices, err = a.DetectDevices()
	if err != nil {
		t.Fatalf("DetectDevices after create: %v", err)
	}
	for _, device := range devices {
		if device.Serial == "localhost:5556" && device.BoundTo != "Village 2" {
			t.Fatalf("localhost:5556 reported bound_to=%q, want Village 2", device.BoundTo)
		}
	}

	// A scan that cannot reach the adb server must surface as an error the
	// drawer can show, not a silent empty list.
	deviceProbe = func(string, int) ([]DetectedDevice, error) {
		return nil, errors.New("adb unreachable")
	}
	if _, err := a.DetectDevices(); err == nil || !strings.Contains(err.Error(), "adb unreachable") {
		t.Fatalf("DetectDevices swallowed the probe error: %v", err)
	}
}
