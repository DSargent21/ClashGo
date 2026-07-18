package bot

import (
	"time"

	"golang.org/x/sys/unix"
)

// CPUTime returns the total CPU time consumed by this process since it
// started, as an absolute duration. It is the sum of user + system time
// reported by the kernel (getrusage RUSAGE_SELF).
//
// Unlike a percentage, this number is device-independent: it means the same
// thing on an M1, an M3 Max, or any other machine. A busy-loop burning one
// core for 10 seconds reports ~10s here regardless of how many cores the
// host has. To compare efficiency across devices, measure CPUTime() delta
// over a fixed wall-clock window (see CPUUsage).
func CPUTime() time.Duration {
	var ru unix.Rusage
	if err := unix.Getrusage(unix.RUSAGE_SELF, &ru); err != nil {
		return 0
	}
	// ru.Utime / ru.Stime are unix.Timeval (Sec + Usec).
	user := time.Duration(ru.Utime.Sec)*time.Second + time.Duration(ru.Utime.Usec)*time.Microsecond
	sys := time.Duration(ru.Stime.Sec)*time.Second + time.Duration(ru.Stime.Usec)*time.Microsecond
	return user + sys
}

// cpuSample is a single (wall, cpu) observation used to derive a usage rate.
type cpuSample struct {
	wall time.Time
	cpu  time.Duration
}

// cpuSampler computes CPU usage as a fraction of one core (0..N) over a
// sliding window. It is the device-independent primitive behind any "% CPU"
// display: divide by the host's logical core count only when you want a
// 0..100% number for a specific machine.
type cpuSampler struct {
	last cpuSample
}

func newCPUSampler() *cpuSampler {
	return &cpuSampler{last: cpuSample{wall: time.Now(), cpu: CPUTime()}}
}

// Usage returns CPU time consumed divided by wall time elapsed since the
// previous call. The result is a core-fraction (1.0 == one full core busy
// for the whole interval). Call it on a steady cadence (e.g. each stats
// poll) for a stable estimate.
func (s *cpuSampler) Usage() float64 {
	now := time.Now()
	cpu := CPUTime()
	wall := now.Sub(s.last.wall)
	if wall <= 0 {
		return 0
	}
	frac := float64(cpu-s.last.cpu) / float64(wall)
	s.last = cpuSample{wall: now, cpu: cpu}
	if frac < 0 {
		return 0
	}
	return frac
}
