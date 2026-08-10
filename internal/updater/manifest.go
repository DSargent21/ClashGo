package updater

import (
	"time"
)

// Manifest is the published `latest.json` schema that ships in each
// GitHub release as a release asset. The updater prefers this over the
// raw GitHub releases API because it carries the SHA256 we need to
// verify downloads.
//
// Schema is intentionally simple and discriminated only by `platform` —
// the Makefile's release target emits one entry per supported OS so the
// same JSON file works for future Windows/Linux clients.
//
// Example:
//
//	{
//	  "version": "0.3.0-beta",
//	  "release_date": "2026-07-08T12:00:00Z",
//	  "notes": "Bug fixes + new auto-attack presets.",
//	  "min_supported": "0.1.0",
//	  "platforms": {
//	    "darwin": {
//	      "asset_name": "ClashGO-v0.3.0-beta-macOS.zip",
//	      "asset_url": "https://github.com/.../ClashGO-v0.3.0-beta-macOS.zip",
//	      "size": 12345678,
//	      "sha256": "abc123..."
//	    }
//	  }
//	}
type Manifest struct {
	Version      string                  `json:"version"`
	ReleaseDate  time.Time               `json:"release_date"`
	Notes        string                  `json:"notes"`
	MinSupported string                  `json:"min_supported,omitempty"`
	Platforms    map[string]PlatformSpec `json:"platforms"`
}

// PlatformSpec is per-OS metadata: where to download from, what the
// expected SHA256 is, and how big the file should be (used as a sanity
// check before opening the file).
type PlatformSpec struct {
	AssetName string `json:"asset_name"`
	AssetURL  string `json:"asset_url"`
	Size      int64  `json:"size"`
	SHA256    string `json:"sha256"`
}

// githubRelease is the subset of the GitHub releases API we read when
// `latest.json` is not present (transition period or older releases).
// We use stdlib net/http rather than a third-party GitHub client to keep
// the binary lean.
type githubRelease struct {
	TagName     string    `json:"tag_name"`
	Name        string    `json:"name"`
	Body        string    `json:"body"`
	PublishedAt time.Time `json:"published_at"`
	HTMLURL     string    `json:"html_url"`
	Assets      []struct {
		Name               string `json:"name"`
		BrowserDownloadURL string `json:"browser_download_url"`
		Size               int64  `json:"size"`
	} `json:"assets"`
}

// platformKey maps Go's runtime.GOOS to the manifest's "platforms"
// discriminator. Today only darwin is shipped; windows/linux slots can
// be added with no schema change.
func platformKey(goos string) string {
	switch goos {
	case "darwin":
		return "darwin"
	case "windows":
		return "windows"
	case "linux":
		return "linux"
	default:
		return goos
	}
}

// pickReleaseAsset for the current OS when falling back to the GitHub
// API. Returns (asset, ok). Requires the asset to be a ClashGO asset
// (prefix "ClashGO-") AND match the per-OS suffix.
//
// An unknown OS (assetSuffix returns "") short-circuits to no-match —
// otherwise the empty suffix would match anything (Go string slice
// `s[len(s):]` is the empty string for any s, and hasSuffix of an
// empty string always returns true).
func pickReleaseAsset(rel githubRelease, goos string) (name, url string, size int64, ok bool) {
	want := assetSuffix(goos)
	if want == "" {
		return "", "", 0, false
	}
	for _, a := range rel.Assets {
		if !hasPrefix(a.Name, "ClashGO-") {
			continue
		}
		if !hasSuffix(a.Name, want) {
			continue
		}
		return a.Name, a.BrowserDownloadURL, a.Size, true
	}
	return "", "", 0, false
}

// assetSuffix matches the suffix produced by `make release` for each
// OS. Returns "" for unknown platforms — the picker will then decline
// to match anything, which is the safe behaviour (we don't want the
// picker to silently hand back a macOS zip when asked for a plan9
// binary).
//
// Keep in sync with Makefile.
func assetSuffix(goos string) string {
	switch goos {
	case "darwin":
		return "-macOS.zip"
	case "windows":
		return "-windows.zip"
	case "linux":
		return "-linux.tar.gz"
	default:
		return ""
	}
}

func hasPrefix(s, p string) bool {
	return len(s) >= len(p) && s[:len(p)] == p
}

func hasSuffix(s, suf string) bool {

	if suf == "" {
		return false
	}
	return len(s) >= len(suf) && s[len(s)-len(suf):] == suf
}
