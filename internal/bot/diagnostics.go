package bot

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Ducky705/ClashGO/internal/paths"
	"gocv.io/x/gocv"
)

// DiagnosticData holds the state of the bot at the time of failure.
type DiagnosticData struct {
	Timestamp time.Time              `json:"timestamp"`
	Reason    string                 `json:"reason"`
	State     string                 `json:"state"`
	Context   map[string]interface{} `json:"context,omitempty"`
}

// maxDiagnosticDumps bounds the diagnostic dumps kept in the config dir.
//
// These used to accumulate forever: 141 stale diag_* pairs (~200 MB) had built
// up in the runtime dir, one per failure since July, because nothing ever
// removed them. The newest few are all a post-mortem needs, and a long-running
// install must not grow without bound.
const maxDiagnosticDumps = 20

// DumpDiagnostics saves a screenshot and a JSON file containing the bot's state.
func (b *Bot) DumpDiagnostics(reason string, screen gocv.Mat, context map[string]interface{}) error {
	timestamp := time.Now().Format("20060102_150405")
	baseName := fmt.Sprintf("diag_%s", timestamp)

	// Save screenshot
	imgName := paths.ResolveConfig(baseName + ".png")
	if !screen.Empty() {
		if ok := gocv.IMWrite(imgName, screen); !ok {
			b.logger.Error().Str("file", imgName).Msg("failed to save diagnostic screenshot")
		} else {
			b.logger.Info().Str("file", imgName).Msg("saved diagnostic screenshot")
		}
	}

	// Save JSON data
	data := DiagnosticData{
		Timestamp: time.Now(),
		Reason:    reason,
		State:     "failed", // Could be more dynamic if Bot had a State field
		Context:   context,
	}

	jsonData, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal diagnostic data: %w", err)
	}

	jsonName := paths.ResolveConfig(baseName + ".json")
	if err := os.WriteFile(jsonName, jsonData, 0644); err != nil {
		b.logger.Error().Err(err).Str("file", jsonName).Msg("failed to save diagnostic json")
		return err
	}

	b.logger.Info().Str("file", jsonName).Msg("saved diagnostic data")

	// Also symlink or copy to "last_failure" for easy access
	_ = os.Remove(paths.ResolveConfig("last_failure.png"))
	_ = os.Remove(paths.ResolveConfig("last_failure.json"))

	// Copy files to last_failure (safer than symlinks on some systems/setups)
	if !screen.Empty() {
		_ = gocv.IMWrite(paths.ResolveConfig("last_failure.png"), screen)
	}
	_ = os.WriteFile(paths.ResolveConfig("last_failure.json"), jsonData, 0644)

	pruneDiagnosticDumps(b)

	return nil
}

// pruneDiagnosticDumps deletes all but the newest maxDiagnosticDumps diag_*
// screenshot/JSON pairs from the config dir. Best-effort by design: retention
// housekeeping must never turn a useful failure dump into an error.
func pruneDiagnosticDumps(b *Bot) {
	dir := paths.GetConfigDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), "diag_") {
			continue
		}
		names = append(names, e.Name())
	}
	if len(names) <= maxDiagnosticDumps*2 {
		return
	}
	// The timestamp in the name is fixed-width, so lexical order is age order
	// for each extension; sort newest first by the shared base name.
	base := make([]string, 0, len(names))
	seen := make(map[string]bool, len(names))
	for _, n := range names {
		key := strings.TrimSuffix(n, filepath.Ext(n))
		if !seen[key] {
			seen[key] = true
			base = append(base, key)
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(base)))
	removed := 0
	for _, key := range base[maxDiagnosticDumps:] {
		for _, ext := range []string{".png", ".json"} {
			if err := os.Remove(filepath.Join(dir, key+ext)); err == nil {
				removed++
			}
		}
	}
	if removed > 0 {
		b.logger.Info().Int("removed", removed).Int("kept", maxDiagnosticDumps).Msg("pruned old diagnostic dumps")
	}
}
