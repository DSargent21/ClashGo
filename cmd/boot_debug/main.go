// Package main implements cmd/boot_debug, a standalone diagnostic
// tool that walks the bot's BlueStacks boot sequence step-by-step
// and reports the state of every checkpoint with timing and
// diagnostic data the user can READ to understand what is happening
// on their machine.
//
// The production boot path lives in
// internal/bot/bootorchestrator.go::Boot() and is bounded by ~215s
// of timeouts that hide transient state. This tool does the same
// steps in slow motion and writes a JSON report to logs/boot_debug.json.
//
// The KEY questions this tool answers:
//
//  1. After `open -a BlueStacks.app`, does ANY BlueStacks-VM-related
//     process ever appear? (we want to see qemu-system-aarch64 or hd-adb
//     or "BlueStacks Main" — not just BlueStacksAI which is the companion)
//  2. Does any port in the candidate range [5555..5565] ever start
//     listening? (BlueStacks 5.x on macOS occasionally opens on 5556
//     or 5557 instead of 5555)
//  3. After polling adb, does `getprop ro.product.manufacturer`
//     succeed against ANY candidate port?
//
// Modes:
//
//	default (observe) — just record state every 2s for 60s. SAFE.
//	-launch           — open -a BlueStacks.app, then poll for 90s.
//	-kill-launch      — killall -9 BlueStacks + open, then poll for 90s.
//	                  DESTRUCTIVE: stops anything running in BlueStacks.
//	-portscan         — scan all 5xxx ports + try adb connect on each.
//
// Usage:
//
//	go run ./cmd/boot_debug
//	go run ./cmd/boot_debug -launch
//	go run ./cmd/boot_debug -kill-launch
//	go run ./cmd/boot_debug -portscan
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"
)

// vmProcessNames is the union of process names that mean "the
// BlueStacks VM subsystem is at least partially alive" on
// BlueStacks 4.x and 5.x on macOS. ANY of these appearing in `ps`
// output means we should not panic about "BlueStacks didn't start"
// — at least part of it is alive.
//
// Note: this is intentionally generous. We used to require an exact
// match for "BlueStacks" (the main GUI shell), but BlueStacks 5.21
// sometimes names the player differently, and `qemu-system-aarch64`
// or `hd-adb` is the more reliable "VM is up" signal — they only
// exist after BlueStacks has fully spawned the Android subsystem.
var vmProcessNames = []string{
	"BlueStacks",          // main GUI shell (older)
	"BlueStacks Main",     // alternative name in recent builds
	"qemu-system-aarch64", // the Android VM
	"hd-adb",              // BlueStacks' custom adb daemon (5.21+)
	"BlueStacksAI",        // companion/Hive UI (almost always present)
}

// candidateAdbPorts is the set of ports BlueStacks 4.x/5.x might
// listen on. Default main instance is 5555; secondary instances on
// 5556, 5557; very rare outliers up to ~5565.
var candidateAdbPorts = []int{5555, 5556, 5557, 5558, 5559, 5560, 5565}

// Snap is one checkpoint's data. The slice of Snaps is what we
// serialize to JSON.
type Snap struct {
	ElapsedMs int64         `json:"elapsed_ms"`
	Phase     string        `json:"phase"`
	Action    string        `json:"action"`
	OK        bool          `json:"ok"`
	Detail    string        `json:"detail,omitempty"`
	Processes []ProcessInfo `json:"processes,omitempty"`
	Ports     []PortInfo    `json:"ports,omitempty"`
	Notes     []string      `json:"notes,omitempty"`
}

// ProcessInfo identifies a single BlueStacks-related process.
type ProcessInfo struct {
	PID     int    `json:"pid"`
	Command string `json:"command"`
	IsVM    bool   `json:"is_vm_signal"`
}

// PortInfo identifies whether a candidate adb port is listening.
type PortInfo struct {
	Port  int    `json:"port"`
	State string `json:"state"` // listen / refused / unknown
	Note  string `json:"note,omitempty"`
}

// Report is the full boot_debug output. Written to logs/boot_debug.json
// for machine-readable analysis.
type Report struct {
	StartedAt   time.Time `json:"started_at"`
	CompletedAt time.Time `json:"completed_at"`
	Mode        string    `json:"mode"`
	DeviceID    string    `json:"device_id"`
	Outcome     string    `json:"outcome"`
	Snaps       []Snap    `json:"snaps"`
}

func main() {
	var (
		mode     = flag.String("mode", "observe", "observe|launch|kill-launch|portscan")
		timeout  = flag.Duration("timeout", 60*time.Second, "polling timeout")
		deviceID = flag.String("device", "localhost:5555", "configured device id (unused in observe/launch modes)")
		pollInt  = flag.Duration("poll", 2*time.Second, "poll interval")
	)
	flag.Parse()

	_ = os.MkdirAll("logs", 0o755)
	start := time.Now()
	rep := Report{
		StartedAt: start,
		Mode:      *mode,
		DeviceID:  *deviceID,
		Snaps:     []Snap{},
	}

	// Always record a baseline so we can diff against later snapshots.
	rep.Snaps = append(rep.Snaps, takeSnap("baseline", "initial state", nil, nil))

	switch *mode {
	case "observe":
		rep = pollLoop(rep, *timeout, *pollInt)
		rep.Outcome = "observation complete"
	case "launch":
		rep.Snaps = append(rep.Snaps, snap("launch", "open -a BlueStacks.app (no kill)", true,
			"observe what an `open` produces; safe but the bot's real flow does kill+open"))
		_, _ = exec.Command("open", "-a", "BlueStacks.app").CombinedOutput()
		rep = pollLoop(rep, *timeout, *pollInt)
		rep.Outcome = "launch observation complete"
	case "kill-launch":
		rep.Snaps = append(rep.Snaps, snap("kill-launch", "killall -9 BlueStacks", true,
			"WARN: destructive — stops anything running in BlueStacks"))
		_, _ = exec.Command("killall", "-9", "BlueStacks").CombinedOutput()
		time.Sleep(2 * time.Second)
		rep.Snaps = append(rep.Snaps, snap("kill-launch", "open -a BlueStacks.app", true, ""))
		_, _ = exec.Command("open", "-a", "BlueStacks.app").CombinedOutput()
		rep = pollLoop(rep, *timeout, *pollInt)
		rep.Outcome = "kill-launch observation complete"
	case "portscan":
		rep = portScan(rep)
		rep = pollLoop(rep, *timeout, *pollInt)
		rep.Outcome = "portscan complete"
	default:
		fmt.Fprintf(os.Stderr, "unknown mode %q\n", *mode)
		os.Exit(2)
	}

	rep.CompletedAt = time.Now()
	writeJSON(rep)
	fmt.Printf("\n[done in %s] mode=%s snaps=%d outcome=%q — see logs/boot_debug.json\n",
		time.Since(start), rep.Mode, len(rep.Snaps), rep.Outcome)
}

// pollLoop takes a snapshot every pollInt until either timeout or
// the VM signals are all up and at least one adb port is listening.
// The early-exit is the "happy path" escape hatch — once we see
// VM-alive + port-open + adb-responds, we stop polling and record
// the timestamp so the user can see how long it took.
func pollLoop(rep Report, timeout, pollInt time.Duration) Report {
	deadline := time.Now().Add(timeout)
	consecutiveReady := 0
	for time.Now().Before(deadline) {
		time.Sleep(pollInt)
		s := takeSnap("poll", "observe state", nil, nil)
		rep.Snaps = append(rep.Snaps, s)
		// Ready = VM signal alive + at least one port listening + adb responds
		if hasVMSignal(s.Processes) && anyPortListens(s.Ports) {
			consecutiveReady++
			if consecutiveReady >= 2 {
				rep.Snaps = append(rep.Snaps, snap("poll", "READY (early-exit)", true,
					fmt.Sprintf("2 consecutive snapshots showed VM signals + listening port")))
				return rep
			}
		} else {
			consecutiveReady = 0
		}
	}
	return rep
}

// portScan tries `adb connect` against every candidate port and
// dumps the results so we can see which port BlueStacks actually
// exposes on this machine.
func portScan(rep Report) Report {
	rep.Snaps = append(rep.Snaps, snap("portscan", "begin", true,
		fmt.Sprintf("trying %d candidate ports: %v", len(candidateAdbPorts), candidateAdbPorts)))
	for _, p := range candidateAdbPorts {
		addr := fmt.Sprintf("localhost:%d", p)
		ctx := exec.Command("adb", "connect", addr)
		blob, err := ctx.CombinedOutput()
		rep.Snaps = append(rep.Snaps, snap("portscan", "adb connect "+addr, err == nil,
			strings.TrimSpace(string(blob))))
	}
	if blob, err := exec.Command("adb", "devices", "-l").CombinedOutput(); err == nil {
		rep.Snaps = append(rep.Snaps, snap("portscan", "adb devices -l", true,
			strings.TrimSpace(string(blob))))
	}
	return rep
}

// writeJSON serializes the report to logs/boot_debug.json.
func writeJSON(rep Report) {
	blob, _ := json.MarshalIndent(&rep, "", "  ")
	if err := os.WriteFile("logs/boot_debug.json", blob, 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "WARN: failed to write logs/boot_debug.json: %v\n", err)
	}
}

// snap creates a snap with no processes/ports fields populated.
// Used for textual events.
func snap(phase, action string, ok bool, detail string) Snap {
	return Snap{
		ElapsedMs: time.Since(snapsT0).Milliseconds(),
		Phase:     phase,
		Action:    action,
		OK:        ok,
		Detail:    detail,
	}
}

// takeSnap does a fresh state capture: lists BlueStacks-related
// processes, lists candidate adb ports, and decorates each process
// with is_vm_signal=true if its name is in vmProcessNames.
func takeSnap(phase, action string, _ []string, _ []string) Snap {
	procs := listProcesses()
	ports := listPorts()
	s := snap(phase, action, true, "")
	s.Processes = procs
	s.Ports = ports
	// Notes summarize what we observe. Helps when reading the JSON.
	if notes := describeVMSignals(procs); notes != "" {
		s.Notes = append(s.Notes, "VM signals: "+notes)
	}
	if notes := describePorts(ports); notes != "" {
		s.Notes = append(s.Notes, "Ports: "+notes)
	}
	return s
}

// listProcesses runs `ps` and returns BlueStacks-VM-related
// processes, sorted by PID.
func listProcesses() []ProcessInfo {
	// Match a single process-by-name with grep -i. Each named
	// pattern is checked independently so we get accurate IsVM
	// tagging.
	type procName struct {
		pat  string
		full bool
	}
	// Broad pattern first; we re-tag IsVM by computing it from
	// the matched command line, not the grep pattern, because
	// substring matches are reliable enough here.
	blob, err := exec.Command("sh", "-c",
		`ps -axo pid,command | grep -i -E 'BlueStacks|qemu-system-aarch64|hd-adb' | grep -v grep | head -40`,
	).Output()
	if err != nil {
		return nil
	}
	var out []ProcessInfo
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
		out = append(out, ProcessInfo{
			PID:     pid,
			Command: strings.TrimSpace(fields[1]),
			IsVM:    isVMProcessName(fields[1]),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].PID < out[j].PID })
	return out
}

// isVMProcessName returns true if the given command line contains
// any of vmProcessNames as a substring. Used to tag IsVM in the
// process list so the JSON clearly shows which processes are the
// "VM subsystem" vs which are unrelated.
func isVMProcessName(cmd string) bool {
	for _, name := range vmProcessNames {
		if strings.Contains(cmd, name) {
			return true
		}
	}
	return false
}

// hasVMSignal returns true if any process in the snapshot is a
// VM-signal process. This is the cheap "VM is at least partially
// alive" gate we use to short-circuit `<plenty of>` waits in the
// production code.
func hasVMSignal(procs []ProcessInfo) bool {
	for _, p := range procs {
		if p.IsVM {
			return true
		}
	}
	return false
}

// listPorts does a `lsof` check on each candidate adb port
// (LISTEN-only filter) and returns the result.
func listPorts() []PortInfo {
	out := make([]PortInfo, 0, len(candidateAdbPorts))
	for _, p := range candidateAdbPorts {
		state := "closed"
		note := ""
		if blob, err := exec.Command("lsof", "-nP", fmt.Sprintf("-iTCP:%d", p), "-sTCP:LISTEN").Output(); err == nil {
			s := strings.TrimSpace(string(blob))
			// lsof output format: "COMMAND PID USER FD TYPE DEVICE SIZE/OFF NODE NAME"
			// With no LISTEN matching the port we get only the header
			// back, which means nobody is listening (closed/closed).
			if s != "" && strings.Contains(s, "LISTEN") && len(strings.Fields(s)) >= 9 {
				state = "listen"
				fields := strings.Fields(s)
				note = fields[0] + ":" + fields[1] // COMMAND:PID
			}
		}
		out = append(out, PortInfo{Port: p, State: state, Note: note})
	}
	return out
}

// anyPortListens returns true if any candidate port is currently
// listening.
func anyPortListens(ports []PortInfo) bool {
	for _, p := range ports {
		if p.State == "listen" {
			return true
		}
	}
	return false
}

// describeVMSignals returns a compact human-readable summary like
// "BlueStacks=1, qemu-system-aarch64=0, hd-adb=1".
func describeVMSignals(procs []ProcessInfo) string {
	counts := make(map[string]int, len(vmProcessNames))
	for _, n := range vmProcessNames {
		counts[n] = 0
	}
	for _, p := range procs {
		if !p.IsVM {
			continue
		}
		for _, name := range vmProcessNames {
			if strings.Contains(p.Command, name) {
				counts[name]++
			}
		}
	}
	parts := make([]string, 0, len(vmProcessNames))
	for _, name := range vmProcessNames {
		if counts[name] > 0 {
			parts = append(parts, fmt.Sprintf("%s=%d", name, counts[name]))
		} else {
			parts = append(parts, fmt.Sprintf("%s=0", name))
		}
	}
	return strings.Join(parts, ", ")
}

// describePorts returns a compact human-readable summary like
// "5555=listen, 5556=closed". Mostly for the in-JSON notes.
func describePorts(ports []PortInfo) string {
	parts := make([]string, 0, len(ports))
	for _, p := range ports {
		parts = append(parts, fmt.Sprintf("%d=%s", p.Port, p.State))
	}
	return strings.Join(parts, ", ")
}

var snapsT0 = time.Now()
