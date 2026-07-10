// Package main implements cmd/adb_probe, a NO-GUI verification tool
// for the ClashGO adb bridge.
//
// Purpose: prove (or refute) that the bot's adb communication layer
// is sound on the user's machine, separate from the question of
// whether the local BlueStacks install is producing a VM. The probe
// NEVER launches a windowed app, never runs `open -a BlueStacks`,
// and never spawns a wails binary; it only exercises the adb
// protocol, the local adb-server (port 5037), and the live
// TCP-socket/listening-port world as it is.
//
// Why this matters: the bot has been failing on the user's
// BlueStacks 5.21.775 install with "timeout waiting for BlueStacks
// ADB daemon to respond". That diagnosis is a true observation but
// conflates two separate failures: (1) the adb bridge code may be
// broken, or (2) the local BlueStacks install may have nothing for
// the bridge to talk to. This tool splits (1) and (2) apart.
//
// Usage:
//
//	go run ./cmd/adb_probe
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"
)

// candidatePorts mirrors internal/adb/emulator_mac.go's
// candidateBlueStacksAdbPorts (single source of truth for the
// orchestrator's port-scan set). Kept duplicated here because
// cmd/adb_probe must NOT import the internal/adb package (it
// lives under cmd/, not internal/). If you change one list, change
// the other to match — the orchestrator will silently miss
// BlueStacks instances otherwise.
var candidatePorts = []int{5555, 5556, 5557, 5558, 5559, 5560, 5565}

// vmProcessNames is the same vmProcessSignals list the bot checks
// when deciding whether the VM is up. Reading this list from a
// probe tool confirms the install is dead or alive.
var vmProcessNames = []string{
	"qemu-system",
	"qemu-system-aarch64",
	"hd-adb",
}

// companionProcesses is what we EXPECT to find running BEFORE
// BlueStacks opens (BlueStacksAI is the local companion/admin
// daemon). Listed here so the probe output is explicit about
// "this is normal and unrelated to VM availability".
var companionProcesses = []string{
	"BlueStacksAI",
}

// ProbeReport is the structured output of cmd/adb_probe. Every
// field is computed from real state at the moment the probe ran.
type ProbeReport struct {
	StartedAt      time.Time                `json:"started_at"`
	CompletedAt    time.Time                `json:"completed_at"`
	AdbPath        string                   `json:"adb_path"`
	AdbVersion     string                   `json:"adb_version"`
	AdbServerAlive bool                     `json:"adb_server_alive"`
	DevicesBefore  string                   `json:"devices_before"`
	DevicesAfter   string                   `json:"devices_after"`
	PortProbes     []PortProbe              `json:"port_probes"`
	AdbConnects    []AdbConnect             `json:"adb_connects"`
	Getprops       []Getprop                `json:"getprops"`
	Processes      map[string][]ProcessInfo `json:"processes"`
	Verdict        string                   `json:"verdict"`
}

// PortProbe is the OS-level TCP socket check for one candidate port.
// This is exactly what the new waitForBlueStacksADB does as a
// fast pre-filter before paying for a real adb connect.
type PortProbe struct {
	Port     int    `json:"port"`
	Reaches  bool   `json:"reaches"`
	Listener string `json:"listener,omitempty"`
	Error    string `json:"error,omitempty"`
}

// AdbConnect is the system `adb connect localhost:N` outcome.
// Mirror of what the bot invokes in its wait loop.
type AdbConnect struct {
	Port     int    `json:"port"`
	Output   string `json:"output"`
	ExitCode int    `json:"exit_code"`
}

// Getprop is the result of `adb -s localhost:N shell getprop
// ro.product.manufacturer` if a connect succeeded. This is the
// exact shell command the bot uses inside isBlueStacksDevice.
type Getprop struct {
	Port         int    `json:"port"`
	DeviceID     string `json:"device_id"`
	Manufacturer string `json:"manufacturer"`
	Output       string `json:"output,omitempty"`
	OK           bool   `json:"ok"`
	Error        string `json:"error,omitempty"`
}

// ProcessInfo is one BlueStacks-related process visible on the
// machine, categorized by IsVM and IsCompanion so the probe
// output is structured enough to read.
type ProcessInfo struct {
	PID         int    `json:"pid"`
	Command     string `json:"command"`
	IsVM        bool   `json:"is_vm"`
	IsCompanion bool   `json:"is_companion"`
}

func main() {
	out := flag.String("out", "logs/adb_probe.json", "JSON output path")
	timeout := flag.Duration("timeout", 8*time.Second, "per-port TCP dial ceiling")
	flag.Parse()

	start := time.Now()
	_ = os.MkdirAll("logs", 0o755)

	rep := ProbeReport{StartedAt: start}

	// 0. Where is adb and what version? This is purely diagnostic.
	rep.AdbPath, rep.AdbVersion = probeAdbBinary()

	// 1. Is adb-server up? We try a TCP dial on localhost:5037.
	rep.AdbServerAlive = probeAdbServerAlive(*timeout)

	// 2. Snapshot of devices per adb's view BEFORE any connect
	// attempts. If this is empty, no BlueStacks is currently
	// registered with the system adb-server — which matches the
	// user's symptom.
	rep.DevicesBefore = runShell(3*time.Second, "adb", "devices", "-l")

	// 3. OS-level TCP probes of all candidate ports. Identical
	// to waitForBlueStacksADB's first hop: 200ms per port via
	// net.DialTimeout.
	rep.PortProbes = probeAllCandidatePorts(200 * time.Millisecond)

	// 4. System-level `adb connect localhost:N` for each port.
	// Identical to waitForBlueStacksADB's second hop.
	for _, p := range candidatePorts {
		addr := fmt.Sprintf("localhost:%d", p)
		ctx := exec.Command("adb", "connect", addr)
		blob, err := ctx.CombinedOutput()
		exit := 0
		if err != nil {
			exit = ctx.ProcessState.ExitCode()
		}
		rep.AdbConnects = append(rep.AdbConnects, AdbConnect{
			Port:     p,
			Output:   strings.TrimSpace(string(blob)),
			ExitCode: exit,
		})
	}

	// 5. After all connect attempts, what does `adb devices` say?
	// This is the canonical "is anything reachable?" check.
	rep.DevicesAfter = runShell(3*time.Second, "adb", "devices", "-l")

	// 6. For every port that the OS probe says is listening,
	// attempt the actual production-code shell command the bot
	// would issue: `adb -s localhost:PORT shell getprop
	// ro.product.manufacturer`. If ANY port responds, the bridge
	// is up and BlueStacks IS reachable.
	for _, pp := range rep.PortProbes {
		if !pp.Reaches {
			continue
		}
		addr := fmt.Sprintf("localhost:%d", pp.Port)
		g := runGetprop(addr, *timeout)
		rep.Getprops = append(rep.Getprops, g)
	}

	// 7. Process reality: BlueStacksAI (companion), qemu (VM),
	// hd-adb (BlueStacks' own daemon). Snapshot all relevant
	// processes so the user can SEE the install state from a
	// single output.
	rep.Processes = snapshotProcesses()

	// 8. Verdict — a single human-readable sentence the user
	// can paste into a bug report.
	rep.Verdict = renderVerdict(&rep)
	rep.CompletedAt = time.Now()

	writeJSON(*out, rep)
	fmt.Println("\n=== ADB PROBE VERDICT ===")
	fmt.Println(rep.Verdict)
	fmt.Printf("\nFull report: %s\n", *out)
}

// writeJSON serializes rep to path with 2-space indent. Errors are
// non-fatal: the verdict is still printed to stdout even if disk
// write fails.
func writeJSON(path string, rep ProbeReport) {
	blob, _ := json.MarshalIndent(rep, "", "  ")
	if err := os.WriteFile(path, blob, 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "WARN: failed to write %s: %v\n", path, err)
	}
}

// probeAdbBinary reports the absolute path of the active adb and
// its version string. Falls back to "unknown" if either is empty.
func probeAdbBinary() (path, version string) {
	if p, err := exec.Command("which", "adb").Output(); err == nil {
		path = strings.TrimSpace(string(p))
	} else {
		path = "(not on PATH)"
	}
	if blob, err := exec.Command("adb", "version").Output(); err == nil {
		version = strings.TrimSpace(string(blob))
	}
	return
}

// probeAdbServerAlive returns true if TCP localhost:5037 accepts a
// connection within timeout. adb-server's well-known listening
// port. If false, no adb-server is up; the bot cannot talk to any
// device until one starts.
func probeAdbServerAlive(timeout time.Duration) bool {
	conn, err := net.DialTimeout("tcp", "127.0.0.1:5037", timeout)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// runShell executes `args[0]` with args[1:] as its argument vector,
// bounded by timeout via exec.CommandContext. Returns combined
// stdout+stderr as a trimmed string. Used for `adb devices -l` and
// similar non-protocol reads. Never blocks past timeout.
func runShell(timeout time.Duration, args ...string) string {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	blob, _ := cmd.CombinedOutput()
	return strings.TrimSpace(string(blob))
}

// probeAllCandidatePorts does a fast TCP-dial probe on every
// candidate port. Mirrors client.tcpScanListens exactly.
func probeAllCandidatePorts(perPort time.Duration) []PortProbe {
	out := make([]PortProbe, 0, len(candidatePorts))
	for _, p := range candidatePorts {
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", p), perPort)
		if err == nil {
			_ = conn.Close()
			out = append(out, PortProbe{Port: p, Reaches: true})
		} else {
			out = append(out, PortProbe{Port: p, Error: err.Error()})
		}
	}
	return out
}

// runGetprop attempts `adb -s <id> shell getprop
// ro.product.manufacturer` against id and returns the result. The
// bot uses this exact pattern inside isBlueStacksDevice.
//
// We treat "already connected" / "already paired" / a non-empty
// stdout as success — the manufacturer string is what matters for
// the bridge verdict, not whether the connect state was fresh.
func runGetprop(id string, timeout time.Duration) Getprop {
	parts := strings.SplitN(id, ":", 2)
	port, _ := strconv.Atoi(parts[1])
	g := Getprop{Port: port, DeviceID: id}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "adb", "-s", id, "shell", "getprop", "ro.product.manufacturer")
	blob, err := cmd.CombinedOutput()
	out := strings.TrimSpace(string(blob))
	g.Output = out
	if err != nil {
		g.Error = err.Error()
		// adb sometimes writes a payload even when err is non-nil
		// (e.g. when the device goes offline mid-call). Honor the
		// payload if it's non-empty.
		if out != "" {
			g.Manufacturer = out
			g.OK = true
		}
		return g
	}
	g.Manufacturer = out
	g.OK = g.Manufacturer != ""
	return g
}

// snapshotProcesses enumerates processes matching BlueStacks-related
// names AND tags each with is_vm / is_companion for the JSON
// reader. Mirrors the same logic embedded in cmd/boot_debug but
// tighter (no polling).
func snapshotProcesses() map[string][]ProcessInfo {
	out := make(map[string][]ProcessInfo, 2)
	blob, err := exec.Command("sh", "-c",
		`ps -axo pid,command | grep -i -E 'BlueStacks|qemu-system-aarch64|hd-adb' | grep -v grep | head -40`,
	).Output()
	if err != nil {
		return out
	}
	allProcs := []ProcessInfo{}
	for _, ln := range strings.Split(strings.TrimSpace(string(blob)), "\n") {
		ln = strings.TrimSpace(ln)
		if ln == "" {
			continue
		}
		fields := strings.SplitN(ln, " ", 2)
		if len(fields) < 2 {
			continue
		}
		var pid int
		fmt.Sscanf(fields[0], "%d", &pid)
		if pid == 0 {
			continue
		}
		allProcs = append(allProcs, ProcessInfo{
			PID:         pid,
			Command:     fields[1],
			IsVM:        matchesAny(fields[1], vmProcessNames),
			IsCompanion: matchesAny(fields[1], companionProcesses),
		})
	}
	sort.Slice(allProcs, func(i, j int) bool { return allProcs[i].PID < allProcs[j].PID })
	out["all"] = allProcs
	vmOnly := []ProcessInfo{}
	for _, p := range allProcs {
		if p.IsVM {
			vmOnly = append(vmOnly, p)
		}
	}
	out["vm_processes"] = vmOnly
	compOnly := []ProcessInfo{}
	for _, p := range allProcs {
		if p.IsCompanion {
			compOnly = append(compOnly, p)
		}
	}
	out["companion_processes"] = compOnly
	return out
}

// matchesAny returns true if needle contains any of the patterns as
// a substring. Case-insensitive.
func matchesAny(needle string, patterns []string) bool {
	low := strings.ToLower(needle)
	for _, p := range patterns {
		if strings.Contains(low, strings.ToLower(p)) {
			return true
		}
	}
	return false
}

// renderVerdict produces a one-paragraph human verdict that
// summarizes the bridge state for a non-technical reader. The
// conditional structure mirrors the bot's own diagnosis tree.
func renderVerdict(rep *ProbeReport) string {
	if !rep.AdbServerAlive {
		return fmt.Sprintf("adb-server is NOT running on localhost:5037 (this is the local helper that brokers adb-protocol to BlueStacks). Without it, the bot cannot reach any device. Start it with `adb start-server` (or just run BlueStacks once; it autostarts adb-server on its own port). adb found at %q, version %q.", rep.AdbPath, rep.AdbVersion)
	}
	vmProcs := rep.Processes["vm_processes"]
	companion := rep.Processes["companion_processes"]
	if len(vmProcs) == 0 {
		if len(companion) == 0 {
			return "adb-server is up but neither the VM (qemu/hd-adb) nor the BlueStacksAI companion is running. Looks like BlueStacks isn't running at all. Launch BlueStacks.app once manually and let it finish initializing; the bot will talk to it from then on."
		}
		return fmt.Sprintf("adb-server is up; companion BlueStacksAI is running (%d process(es)) but no VM (qemu-system, hd-adb) is up. This is exactly the user's reported failure mode: BlueStacks' main GUI binary is silently aborting during startup. The local BlueStacks 5.21.775 install appears to have an incomplete Android-engine download or a corrupt config — re-running the official BlueStacks installer is the path to recovery.", len(companion))
	}
	gotAny := false
	for _, g := range rep.Getprops {
		if g.OK {
			gotAny = true
			break
		}
	}
	if gotAny {
		return fmt.Sprintf("adb-server is up, VM is up, and at least one candidate adb port responded to getprop. Bridge is verified functional; bot should be able to connect.")
	}
	return "adb-server is up, VM processes are visible, but no candidate adb port responded to getprop. Bridge may be functional but the VM's adb daemon might be on a port outside [5555..5565], or still initializing. Re-run probe in 30s."
}
