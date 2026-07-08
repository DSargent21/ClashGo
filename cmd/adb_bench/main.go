package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/Ducky705/ClashGO/internal/adb"
)

// adb_bench: tap-latency micro-benchmark.
//
// Two modes:
//   - LEGACY:        every tap goes through transport.Exec (current ADB path; each call spawns `app_process` JVM on device).
//   - PERSISTENT_PIPE: taps go through the new ShellPipe.Send (single persistent "shell:sh" connection; no JVM spin-up per tap).
//
// Selection: pass -pipe to enable the persistent pipe path. Default is legacy
// so A/B comparison is implicit.
//
// Usage:
//
//	adb_bench -device localhost:5555 -iters 50 -pipe
//
// Sends taps to a benign coordinate (5,5) so the device UI is unaffected.
// Reports mean, p50, p95, p99 latency over the iteration set in both modes so
// the gap is visible at a glance.
func main() {
	deviceID := flag.String("device", "localhost:5555", "ADB device id (e.g. emulator-5554 or localhost:5555)")
	iters := flag.Int("iters", 50, "number of taps per mode")
	usePipe := flag.Bool("pipe", false, "use persistent shell pipe (vs legacy per-call)")
	flag.Parse()

	if *iters < 1 {
		fmt.Fprintln(os.Stderr, "iters must be >= 1")
		os.Exit(1)
	}

	client := adb.NewClient(adb.WithDeviceID(*deviceID))

	if err := client.Connect(); err != nil {
		fmt.Fprintf(os.Stderr, "connect failed: %v\n", err)
		os.Exit(1)
	}
	defer client.Close()

	if *usePipe {
		client.EnablePersistentShell(true)
		defer client.ClosePersistentShell()
	}

	mode := "LEGACY (transport.Exec per call)"
	if *usePipe {
		mode = "PERSISTENT SHELL PIPE"
	}
	fmt.Printf("\n=== adb_bench: %s, device=%s, iters=%d ===\n\n", mode, *deviceID, *iters)

	durations := make([]time.Duration, 0, *iters)
	for i := 0; i < *iters; i++ {
		start := time.Now()
		if err := client.Tap(5, 5); err != nil {
			fmt.Fprintf(os.Stderr, "tap %d failed: %v\n", i, err)
			os.Exit(1)
		}
		durations = append(durations, time.Since(start))
	}

	var total time.Duration
	for _, d := range durations {
		total += d
	}
	mean := total / time.Duration(len(durations))
	min := durations[0]
	max := durations[0]
	for _, d := range durations {
		if d < min {
			min = d
		}
		if d > max {
			max = d
		}
	}

	// Approximate percentiles (linear scan suffices for small N).
	percentile := func(p float64) time.Duration {
		// Sort a copy
		tmp := make([]time.Duration, len(durations))
		copy(tmp, durations)
		// Simple insertion sort (N <= a few hundred)
		for i := 1; i < len(tmp); i++ {
			for j := i; j > 0 && tmp[j] < tmp[j-1]; j-- {
				tmp[j], tmp[j-1] = tmp[j-1], tmp[j]
			}
		}
		idx := int(p * float64(len(tmp)-1))
		return tmp[idx]
	}

	fmt.Printf("  min  : %s\n", min)
	fmt.Printf("  p50  : %s\n", percentile(0.50))
	fmt.Printf("  mean : %s\n", mean)
	fmt.Printf("  p95  : %s\n", percentile(0.95))
	fmt.Printf("  p99  : %s\n", percentile(0.99))
	fmt.Printf("  max  : %s\n", max)
	fmt.Printf("  total: %s\n\n", total)

	fmt.Println("Run both modes and compare:")
	fmt.Printf("  adb_bench -iters %d\n", *iters)
	fmt.Printf("  adb_bench -iters %d -pipe\n", *iters)
}
