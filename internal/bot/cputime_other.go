//go:build !darwin && !linux

package bot

import "time"

// CPUTime is unsupported on this platform; report zero. The macOS/Linux
// implementation reads kernel rusage via golang.org/x/sys/unix.
func CPUTime() time.Duration { return 0 }

type cpuSampler struct{}

func newCPUSampler() *cpuSampler { return &cpuSampler{} }

func (s *cpuSampler) Usage() float64 { return 0 }
