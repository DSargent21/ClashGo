// Package bot — bootprofile.go
//
// BootProfile is the persistent, learned-timing store for boot
// durations. Each successful boot appends a sample; on timeout the
// recommended timeout is bumped (p95 * 1.5 capped at MaxRecommended).
// The orchestrator reads the recommended timeout before starting, so
// after one or two runs on a slow MacBook the bot stops timing out
// on a value that was hand-picked for fast hardware.
//
// The file lives at paths.ResolveConfig("boot_profile.json") and is
// written atomically (tmp + rename) so a crash mid-write doesn't
// corrupt the existing profile.
package bot

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// MaxBootProfileSamples is the rolling window of recent boots the
// profile keeps. 30 is enough to absorb a few bad days without
// forgetting that "this user usually boots in 12s."
const MaxBootProfileSamples = 30

// DefaultRecommendedTimeoutMs is the initial recommended boot timeout
// when no profile exists yet. 90s matches the prior hard-coded value
// in bot.go so behavior is unchanged on first launch.
const DefaultRecommendedTimeoutMs = 90_000

// MaxRecommendedTimeoutMs is the upper cap on the recommended
// timeout. Even on a very slow machine, 5 minutes is enough — past
// that, something is actually wrong and the user should investigate
// rather than have the bot silently retry for 10 minutes.
const MaxRecommendedTimeoutMs = 300_000

// MinRecommendedTimeoutMs is the lower bound. 30s is a reasonable
// floor: any device that boots faster than 30s on a clean run is
// already happy at the floor, and bumping below 30s just causes
// spurious timeouts on noisy hardware.
const MinRecommendedTimeoutMs = 30_000

// BootProfileSample is one observation. Outcome is "ok" or "timeout"
// — both are recorded so the recommended timeout adapts to BOTH
// typical-case speed AND the worst-case stretch the user has hit.
type BootProfileSample struct {
	StartedAt time.Time `json:"started_at"`
	Duration  int64     `json:"duration_ms"`
	Outcome   string    `json:"outcome"`
}

// BootProfile is the persistent learned-timing record for a single
// device. Currently the file is per-host (one profile, not per
// device), but the DeviceID field is kept on every sample so a future
// per-device split is a one-line change.
type BootProfile struct {
	mu sync.Mutex

	DeviceID      string              `json:"device_id,omitempty"`
	Samples       []BootProfileSample `json:"samples"`
	LastUpdated   time.Time           `json:"last_updated"`
	RecommendedMs int                 `json:"recommended_timeout_ms"`
}

// NewBootProfile returns a fresh, empty profile. Use LoadBootProfile
// to read an existing one from disk.
func NewBootProfile() *BootProfile {
	return &BootProfile{
		Samples:       make([]BootProfileSample, 0, MaxBootProfileSamples),
		RecommendedMs: DefaultRecommendedTimeoutMs,
		LastUpdated:   time.Now(),
	}
}

// LoadBootProfile reads path from disk. A missing file is not an
// error — the caller gets a default profile. Any other read or parse
// failure is returned wrapped.
func LoadBootProfile(path string) (*BootProfile, error) {
	blob, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return NewBootProfile(), nil
		}
		return nil, fmt.Errorf("read boot profile: %w", err)
	}
	var p BootProfile
	if err := json.Unmarshal(blob, &p); err != nil {
		// Corrupt file: rename it out of the way and return a fresh
		// profile. Better to lose a profile than to crash the bot
		// because someone edited the file manually.
		_ = os.Rename(path, path+".corrupt."+time.Now().Format("20060102150405"))
		return NewBootProfile(), nil
	}
	if p.Samples == nil {
		p.Samples = make([]BootProfileSample, 0, MaxBootProfileSamples)
	}
	if p.RecommendedMs == 0 {
		p.RecommendedMs = DefaultRecommendedTimeoutMs
	}
	return &p, nil
}

// Save writes the profile to path atomically (write to .tmp, fsync,
// rename). The mutex is held only over the in-memory mutation; the
// disk write is best-effort and any error is returned to the caller.
func (p *BootProfile) Save(path string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.LastUpdated = time.Now()
	blob, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal boot profile: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", filepath.Dir(path), err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, blob, 0o644); err != nil {
		return fmt.Errorf("write tmp profile: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename profile: %w", err)
	}
	return nil
}

// RecommendedTimeout returns the recommended boot timeout in
// milliseconds, clamped to [Min, Max]. The mutex is held only for the
// read; recomputation is cheap.
func (p *BootProfile) RecommendedTimeout() time.Duration {
	p.mu.Lock()
	defer p.mu.Unlock()
	return clampRecommendedMs(p.RecommendedMs)
}

// AddSample appends a sample and recomputes the recommended timeout
// in one atomic-with-respect-to-readers step. Trims the rolling
// window to MaxBootProfileSamples so the file stays small.
//
// Recommendation rule:
//   - If any sample is a "timeout", the recommended timeout is
//     bumped to max(p95(all) * 1.5, currentRecommended).
//   - Otherwise the recommended timeout is max(p95(successful) * 1.5,
//     MinRecommended).
//   - Always clamped to [Min, Max].
//
// 1.5x headroom covers a single noisy run without overcorrecting.
func (p *BootProfile) AddSample(s BootProfileSample) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.Samples = append(p.Samples, s)
	if len(p.Samples) > MaxBootProfileSamples {
		// Drop oldest, keeping the most-recent window.
		p.Samples = p.Samples[len(p.Samples)-MaxBootProfileSamples:]
	}

	successful := make([]int64, 0, len(p.Samples))
	anyTimeout := false
	for _, x := range p.Samples {
		if x.Outcome == "ok" && x.Duration > 0 {
			successful = append(successful, x.Duration)
		}
		if x.Outcome == "timeout" {
			anyTimeout = true
		}
	}

	var p95 int64
	if len(successful) > 0 {
		p95 = percentileMs(successful, 0.95)
	}

	switch {
	case anyTimeout:
		// Use the larger of p95(successful) and p95(all — incl timeouts
		// capped at the prior recommendation, so a single extreme
		// value doesn't dominate). 1.5x headroom.
		all := make([]int64, 0, len(p.Samples))
		for _, x := range p.Samples {
			if x.Duration > 0 {
				all = append(all, x.Duration)
			}
		}
		p95All := percentileMs(all, 0.95)
		base := p95
		if p95All > base {
			base = p95All
		}
		recommended := int64(float64(base) * 1.5)
		if int64(p.RecommendedMs) > recommended {
			recommended = int64(p.RecommendedMs)
		}
		p.RecommendedMs = int(clampRecommendedMs64(recommended))
	case len(successful) >= 3:
		// Enough successful samples to settle on p95 * 1.5.
		recommended := int64(float64(p95) * 1.5)
		p.RecommendedMs = int(clampRecommendedMs64(recommended))
	default:
		// Not enough data yet — keep the current recommendation.
	}
}

// p95 computes the requested percentile of ms using nearest-rank.
// Returns 0 for an empty input. Sorts a copy so we don't disturb the
// caller's slice.
func percentileMs(values []int64, p float64) int64 {
	if len(values) == 0 {
		return 0
	}
	cp := make([]int64, len(values))
	copy(cp, values)
	sort.Slice(cp, func(i, j int) bool { return cp[i] < cp[j] })
	// Nearest-rank: index = ceil(p * N) - 1, clamped to [0, N-1].
	rank := int(math.Ceil(p*float64(len(cp)))) - 1
	if rank < 0 {
		rank = 0
	}
	if rank >= len(cp) {
		rank = len(cp) - 1
	}
	return cp[rank]
}

func clampRecommendedMs(v int) time.Duration {
	return time.Duration(clampRecommendedMs64(int64(v))) * time.Millisecond
}

func clampRecommendedMs64(v int64) int64 {
	if v < MinRecommendedTimeoutMs {
		return MinRecommendedTimeoutMs
	}
	if v > MaxRecommendedTimeoutMs {
		return MaxRecommendedTimeoutMs
	}
	return v
}
