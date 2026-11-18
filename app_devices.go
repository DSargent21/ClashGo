//go:build !cli

package main

// app_devices.go — emulator discovery for setup.
//
// Binding an account used to mean typing an ADB serial ("localhost:5556") from
// memory. Detection turns that into one click: scan the ports an emulator
// hands out, ask each one what it is, and report which account already owns it.
// Read-only on purpose (no `adb connect`, no server restart), so opening the
// add-account drawer can never disturb a running emulator or a running bot.

import (
	"net"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Ducky705/ClashGO/internal/adb"
	"github.com/Ducky705/ClashGO/internal/config"
	"github.com/rs/zerolog/log"
)

// DetectedDevice is one emulator instance an account can be bound to.
type DetectedDevice struct {
	Serial string `json:"serial"`
	Model  string `json:"model"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
	// BoundTo is the name of the account already using this serial ("" when
	// free). Accounts are one-per-serial, so the picker must not offer an
	// owned instance as if it were available.
	BoundTo string `json:"bound_to"`
	// Active marks the device of the active account.
	Active bool `json:"active"`
}

// Emulator instances take one ADB port each starting at 5555 (BlueStacks
// convention, also used by MuMu/LDPlayer). Scanning the span without touching
// the adb server keeps detection cheap and side-effect free.
const (
	emulatorPortFirst = 5555
	emulatorPortLast  = 5565

	// Per-port TCP probe and per-device ADB budget. Detection is a slow,
	// explicit action (opening the add-account drawer, checking the wizard),
	// never part of a poll.
	devicePortProbeTimeout = 80 * time.Millisecond
	deviceShellTimeout     = 1500 * time.Millisecond
)

// deviceProbe is the seam: tests replace it so device detection is covered
// without an emulator, an adb server or the adb binary.
var deviceProbe = probeDevices

// DetectDevices lists the emulator instances reachable right now, annotated
// with the account that already owns each serial.
func (a *App) DetectDevices() ([]DetectedDevice, error) {
	cfg := config.LoadOrDefault("config.json")
	devices, err := deviceProbe(cfg.Device.ADBHost, cfg.Device.ADBPort)
	if err != nil {
		return nil, err
	}

	st, err := a.ensureAccounts()
	if err != nil {
		// Without a registry there is nothing to annotate; the devices are
		// still useful for a first account.
		return devices, nil
	}
	activeID := st.ActiveID()
	for i := range devices {
		for _, acct := range st.List() {
			if canonicalSerial(acct.Device.DeviceID) != canonicalSerial(devices[i].Serial) {
				continue
			}
			devices[i].BoundTo = acct.Name
			devices[i].Active = acct.ID == activeID
			break
		}
	}
	return devices, nil
}

// probeDevices scans the emulator port span plus whatever the local adb server
// already knows about (USB devices, Android Studio AVDs, instances on a custom
// port) and returns only the serials that actually answer an ADB shell.
//
// A device that does not answer is dropped instead of listed: a stale
// "localhost:5555" ghost entry is exactly what makes a setup screen lie.
func probeDevices(host string, serverPort int) ([]DetectedDevice, error) {
	if strings.TrimSpace(host) == "" {
		host = "127.0.0.1"
	}
	if serverPort <= 0 {
		serverPort = 5037
	}

	var known []string
	if client := adb.NewClient(
		adb.WithHost(host),
		adb.WithPort(serverPort),
		adb.WithTimeout(deviceShellTimeout),
	); client != nil {
		if listed, err := client.Devices(); err == nil {
			known = listed
		} else {
			log.Debug().Err(err).Msg("device detection: adb server not reachable; port scan only")
		}
		client.Close()
	}
	serials := mergeDeviceSerials(listeningEmulatorPorts(host), known)

	seen := make(map[string]bool, len(serials))
	devices := make([]DetectedDevice, 0, len(serials))
	for _, serial := range serials {
		serial = strings.TrimSpace(serial)
		if serial == "" || seen[serial] {
			continue
		}
		seen[serial] = true
		device, ok := probeDevice(host, serverPort, serial)
		if !ok {
			continue
		}
		devices = append(devices, device)
	}

	// Stable order so the picker does not jump between openings: the
	// lowest serial (the main instance) leads.
	sort.Slice(devices, func(i, j int) bool { return devices[i].Serial < devices[j].Serial })
	return devices, nil
}

// listeningEmulatorPorts returns every emulator port that accepts a TCP
// connection. A closed port answers in milliseconds with a reset, so the whole
// span costs well under a second.
func listeningEmulatorPorts(host string) []int {
	var ports []int
	for port := emulatorPortFirst; port <= emulatorPortLast; port++ {
		conn, err := net.DialTimeout("tcp", net.JoinHostPort(host, strconv.Itoa(port)), devicePortProbeTimeout)
		if err != nil {
			continue
		}
		_ = conn.Close()
		ports = append(ports, port)
	}
	return ports
}

// mergeDeviceSerials builds the probe list: the scanned emulator ports first,
// then whatever else the adb server knows about.
//
// The adb server also registers an emulator's console endpoint as
// "emulator-<consolePort>", which is the same instance as localhost:<port+1>.
// Keeping both would offer one emulator twice in the picker — and let two
// accounts bind the same instance under different serials, defeating the
// one-account-per-device rule. The console duplicate is dropped whenever its
// TCP port was scanned.
func mergeDeviceSerials(listeningPorts []int, known []string) []string {
	listening := make(map[int]bool, len(listeningPorts))
	serials := make([]string, 0, len(listeningPorts)+len(known))
	seen := make(map[string]bool, len(listeningPorts)+len(known))
	for _, port := range listeningPorts {
		listening[port] = true
		serial := "localhost:" + strconv.Itoa(port)
		seen[serial] = true
		serials = append(serials, serial)
	}
	for _, serial := range known {
		serial = strings.TrimSpace(serial)
		if serial == "" || seen[serial] {
			continue
		}
		if consolePort, ok := emulatorConsolePort(serial); ok && listening[consolePort+1] {
			continue
		}
		seen[serial] = true
		serials = append(serials, serial)
	}
	return serials
}

// emulatorConsolePort parses an "emulator-NNNN" serial into its console port.
// The adb transport port for the same instance is that port + 1.
func emulatorConsolePort(serial string) (int, bool) {
	const prefix = "emulator-"
	if !strings.HasPrefix(serial, prefix) {
		return 0, false
	}
	port, err := strconv.Atoi(strings.TrimPrefix(serial, prefix))
	if err != nil || port <= 0 {
		return 0, false
	}
	return port, true
}

// canonicalSerial folds the adb console serial onto the TCP serial of the same
// instance, so an account bound as "emulator-5554" and a detected
// "localhost:5555" are recognised as one emulator.
func canonicalSerial(serial string) string {
	if consolePort, ok := emulatorConsolePort(strings.TrimSpace(serial)); ok {
		return "localhost:" + strconv.Itoa(consolePort+1)
	}
	return strings.TrimSpace(serial)
}

// probeDevice asks one serial for its model and screen size. ok=false means
// the port is open but not an Android device we can drive.
func probeDevice(host string, serverPort int, serial string) (DetectedDevice, bool) {
	t, err := adb.NewTransport(serial, host, serverPort, deviceShellTimeout)
	if err != nil {
		return DetectedDevice{}, false
	}
	defer t.Close()

	model, err := t.Shell("getprop ro.product.model")
	if err != nil {
		return DetectedDevice{}, false
	}
	device := DetectedDevice{Serial: serial, Model: strings.TrimSpace(model)}
	if device.Model == "" {
		device.Model = "Android device"
	}
	if width, height, err := t.ScreenSize(); err == nil {
		device.Width, device.Height = width, height
	}
	return device, true
}
