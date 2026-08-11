// Command release_manifest emits a `latest.json` file for the just-built
// release zip. Called by Makefile's release target after the zip is
// produced so the in-app updater can verify downloads.
//
// Usage:
//
//	go run ./cmd/release_manifest \
//	    -version 0.3.1 \
//	    -zip build/bin/ClashGO-v0.3.1-macOS.zip \
//	    -min-supported 0.1.0 \
//	    -notes-file /dev/stdin \
//	    -out build/bin/latest.json \
//	    -repo Ducky705/ClashGO
//
// The output is consumed by internal/updater.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Ducky705/ClashGO/internal/updater"
)

type minimalManifest struct {
	Version      string                          `json:"version"`
	ReleaseDate  time.Time                       `json:"release_date"`
	Notes        string                          `json:"notes"`
	MinSupported string                          `json:"min_supported,omitempty"`
	Platforms    map[string]updater.PlatformSpec `json:"platforms"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "release_manifest:", err)
		os.Exit(1)
	}
}

func run() error {
	version := flag.String("version", "", "release version (without v prefix)")
	zipPath := flag.String("zip", "", "path to the just-built zip")
	minSupported := flag.String("min-supported", "", "minimum supported version (optional)")
	notesFile := flag.String("notes-file", "", "path to release notes (optional; reads stdin if blank)")
	outPath := flag.String("out", "latest.json", "output file path")
	repo := flag.String("repo", "", "owner/name used to build asset URLs (e.g. Ducky705/ClashGO)")
	platformOS := flag.String("os", "", "platform tag for platforms map (e.g. darwin, windows); auto-detects if blank")
	flag.Parse()

	if *version == "" || *zipPath == "" || *repo == "" {
		return fmt.Errorf("required: -version, -zip, -repo")
	}

	stat, err := os.Stat(*zipPath)
	if err != nil {
		return fmt.Errorf("stat zip: %w", err)
	}
	size := stat.Size()

	f, err := os.Open(*zipPath)
	if err != nil {
		return fmt.Errorf("open zip: %w", err)
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return fmt.Errorf("hash zip: %w", err)
	}
	sha := hex.EncodeToString(h.Sum(nil))

	notes := ""
	if *notesFile != "" {
		b, err := os.ReadFile(*notesFile)
		if err != nil {
			return fmt.Errorf("read notes: %w", err)
		}
		notes = string(b)
	}

	if *platformOS == "" {
		*platformOS = detectOSFromName(*zipPath)
	}
	if *platformOS == "" {
		return fmt.Errorf("could not detect OS from zip path; pass -os")
	}

	assetName := filepath.Base(*zipPath)
	manifest := minimalManifest{
		Version:      *version,
		ReleaseDate:  time.Now().UTC(),
		Notes:        notes,
		MinSupported: *minSupported,
		Platforms: map[string]updater.PlatformSpec{
			*platformOS: {
				AssetName: assetName,
				AssetURL:  fmt.Sprintf("https://github.com/%s/releases/download/v%s/%s", *repo, *version, assetName),
				Size:      size,
				SHA256:    sha,
			},
		},
	}

	b, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	if err := os.WriteFile(*outPath, b, 0o644); err != nil {
		return fmt.Errorf("write out: %w", err)
	}

	// Also feed it through the canonical updater.Manifest type to
	// catch schema drift.
	roundTrip := updater.Manifest{}
	if err := json.Unmarshal(b, &roundTrip); err != nil {
		return fmt.Errorf("round-trip schema check failed: %w", err)
	}
	if roundTrip.Version != manifest.Version || len(roundTrip.Platforms) == 0 {
		return fmt.Errorf("schema round-trip mismatch")
	}

	fmt.Printf("wrote %s (size=%d sha256=%s)\n", *outPath, size, sha)
	return nil
}

func detectOSFromName(name string) string {
	switch {
	case strings.Contains(name, "macOS") || strings.Contains(name, "darwin"):
		return "darwin"
	case strings.Contains(name, "windows") || strings.Contains(name, "win"):
		return "windows"
	case strings.Contains(name, "linux"):
		return "linux"
	}
	return ""
}
