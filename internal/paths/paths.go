// Package paths resolves absolute, writable locations for the bot's
// runtime files. Two distinct trees are exposed:
//
//   - GetAssetsDir / Resolve: read-only assets (templates, strategies).
//     In a packaged .app this is inside the bundle's Resources/ folder;
//     in dev mode it's the in-tree assets/ dir.
//   - GetConfigDir / ResolveConfig: writable state (boot_profile, stats,
//     attack_history, last_attack_report, attack_deploy_debug outputs,
//     logs/). The default lives outside the project root on every
//     platform so the wails dev filesystem watcher never sees state
//     writes — wails dev treats any project-tree file change as a Go
//     rebuild trigger and kills the active bot session to relaunch.
//
// Resolution order for GetConfigDir:
//
//  1. CLASHGO_CONFIG_DIR env var (tests, portable installs).
//  2. macOS .app-style bundle → ~/Library/Application Support/ClashGO
//     (covers both wails dev's build/bin/ClashGO.app and the production
//     .app, since both ship Resources/assets — see isPackagedApp).
//  3. non-mac dev           → $XDG_CONFIG_HOME/ClashGO/dev on linux
//                              %APPDATA%/ClashGO/dev on windows
//                              (os.UserConfigDir() picks the right base)
//  4. fallback              → os.TempDir()/ClashGO/dev
//
// Legacy compatibility: the first GetConfigDir() call runs a one-time
// migration that copies known state files from the project's cwd into
// the resolved directory. Copy-not-move — the legacy copy stays where
// it was so manual recovery is trivial and a failed migration cannot
// lose data. Re-runs are also harmless: sync.Once + os.OpenFile(O_EXCL)
// ensure destination files are never truncated or overwritten.
package paths

import (
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sync"
)

var (
	assetsDir   string
	migrateOnce sync.Once
)

// legacyStateFiles are the per-run state files that older bot versions
// (pre-this-fix) wrote into the project root (cwd). They are listed
// here verbatim so the first call to GetConfigDir can copy them into
// the new dev-mode config dir on disk. Migration is copy-not-move so
// the user's data never vanishes from the previous location.
//
// Must stay in sync with the writes in internal/bot/bot.go and app.go.
// Per-session directories like output/duke_picks/<ts>.ndjson are NOT
// listed — their filenames are dynamic and they live in a tree wails
// dev already has gitignored; leaving them in place is harmless.
var legacyStateFiles = []string{
	"config.json",
	"stats.json",
	"attack_history.json",
	"attack_runtime.json",
	"adb_last_known.json",
	"boot_profile.json",
	"last_attack_report.json",
	"last_battle_result.png",
	"attack_deploy_debug.png",
	"last_failure.png",
	"last_failure.json",
}

func init() {
	// Asset resolution: walk up to 5 levels looking for an `assets/`
	// folder. Useful when tests run from subdirectories of the project.
	assetsDir = "assets"
	for i := 0; i < 5; i++ {
		testPath := "assets"
		if i > 0 {
			dots := ""
			for j := 0; j < i; j++ {
				dots = filepath.Join(dots, "..")
			}
			testPath = filepath.Join(dots, "assets")
		}
		if info, err := os.Stat(testPath); err == nil && info.IsDir() {
			assetsDir = testPath
			break
		}
	}
}

// GetAssetsDir returns the absolute path to the assets directory,
// honoring CLASHGO_ASSETS_DIR at call time so tests and portable
// installs can override without rebuilding.
func GetAssetsDir() string {
	if override := os.Getenv("CLASHGO_ASSETS_DIR"); override != "" {
		return override
	}

	// macOS bundle layout: <bundle>/Contents/Resources/assets. Finder-
	// launched .app processes run with cwd=/ (or the user's home), so
	// the package-init walk-up in init() finds no in-tree assets dir
	// and the packaged app would otherwise resolve assets to /assets
	// — an empty dir that silently breaks strategy listing (the Config
	// page) and template loading (bot OCR). isPackagedApp() is the same
	// layout probe the config-dir resolver already uses; reuse it
	// rather than duplicating the MacOS-dir check here.
	if runtime.GOOS == "darwin" && isPackagedApp() {
		if execPath, err := os.Executable(); err == nil {
			// execPath is <bundle>/Contents/MacOS/ClashGO
			contentsDir := filepath.Dir(filepath.Dir(execPath))
			res := filepath.Join(contentsDir, "Resources", "assets")
			if info, err := os.Stat(res); err == nil && info.IsDir() {
				return res
			}
		}
	}

	abs, err := filepath.Abs(assetsDir)
	if err != nil {
		return assetsDir
	}
	return abs
}

// GetConfigDir returns the absolute path to the dir used for writable
// config and per-run state files. Reads CLASHGO_CONFIG_DIR on every
// call (via resolveConfigDir) so tests using t.Setenv still redirect,
// and runs the legacy migration at most once per process via sync.Once.
func GetConfigDir() string {
	d := resolveConfigDir()
	migrateOnce.Do(func() {
		migrateLegacyState(d)
	})
	abs, err := filepath.Abs(d)
	if err != nil {
		return d
	}
	return abs
}

// resolveConfigDir picks the base directory for writable state,
// honoring the OS-appropriate defaults and the production .app
// heuristic. Reads CLASHGO_CONFIG_DIR fresh each call so tests using
// t.Setenv still redirect. NEVER returns "." (the project root) —
// doing so would put state files in the wails dev watch tree and
// trigger spurious rebuilds that kill the active bot session.
func resolveConfigDir() string {
	if override := os.Getenv("CLASHGO_CONFIG_DIR"); override != "" {
		return ensureDir(override)
	}

	// Production-style macOS bundle layout: run from `*/MacOS/`
	// with a `Resources/assets` sibling. This covers the production
	// `.app` AND wails dev's `build/bin/ClashGO.app`, both of which
	// ship `Resources/assets`. Either way the destination is
	// `~/Library/Application Support/ClashGO`, which is outside the
	// project tree and never triggers wails dev's rebuild watcher.
	// (Note: this means a darwin dev session and a darwin production
	// install of the same user share one state dir on macOS. Users
	// who need strict separation can set CLASHGO_CONFIG_DIR.)
	if runtime.GOOS == "darwin" && isPackagedApp() {
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			return ensureDir(filepath.Join(home, "Library", "Application Support", "ClashGO"))
		}
	}

	// os.UserConfigDir handles mac/linux/windows correctly AND any
	// sandbox case where raw ~/Library does not work. We pin the
	// returned basename with our app name + "/dev" so concurrent
	// prod + dev installs of the same binary on a dev workstation
	// write into separate directories.
	base, err := os.UserConfigDir()
	if err != nil || base == "" {
		base = os.TempDir()
	}

	dir := filepath.Join(base, "ClashGO", "dev")
	return ensureDir(dir)
}

// isPackagedApp returns true when the currently-running executable
// lives inside a macOS-style bundle (X/MacOS/X + Resources/assets).
// This covers both the production .app (wails build → /Applications)
// and wails dev's build/bin/ClashGO.app/Contents/MacOS/ClashGO with
// Resources/assets next to it.
func isPackagedApp() bool {
	execPath, err := os.Executable()
	if err != nil {
		return false
	}
	dir := filepath.Dir(execPath)
	if filepath.Base(dir) != "MacOS" {
		return false
	}
	contentsDir := filepath.Dir(dir)
	if _, err := os.Stat(filepath.Join(contentsDir, "Resources", "assets")); err != nil {
		return false
	}
	return true
}

// ensureDir is os.MkdirAll + return. The MkdirAll error is
// intentionally swallowed: a process that fails to create its
// config dir should still start so that the FIRST write attempt
// surfaces a clearer EACCES / EROFS to the user, rather than
// failing at package init with a less-actionable stack trace.
func ensureDir(dir string) string {
	_ = os.MkdirAll(dir, 0o755)
	return dir
}

// migrateLegacyState best-effort-copies each known legacy state file
// from the process's cwd to the resolved config dir, only when the
// source exists and the destination does not. Copy-not-move is
// deliberate — losing a single file mid-migration (e.g. disk full,
// permission on dst) would be quiet and unrecoverable if we used
// os.Rename. The legacy files are .gitignore'd so leaving them in
// place is harmless if the user wants to inspect or remove them.
//
// Short-circuits if cwd and configDir resolve to the same absolute
// path so test fixtures and overrides don't self-trigger.
func migrateLegacyState(configDir string) {
	cwd, err := filepath.Abs(".")
	if err != nil || cwd == "" {
		return
	}
	absDst, err := filepath.Abs(configDir)
	if err != nil || absDst == "" {
		return
	}
	if cwd == absDst {
		// Same physical dir — nothing to migrate (covers the case
		// where CLASHGO_CONFIG_DIR is set to "." or tests run in cwd).
		return
	}

	for _, name := range legacyStateFiles {
		src := filepath.Join(cwd, name)
		srcInfo, err := os.Stat(src)
		if err != nil {
			continue
		}
		if !srcInfo.Mode().IsRegular() {
			continue
		}
		dst := filepath.Join(absDst, name)
		if _, err := os.Stat(dst); err == nil {
			// Destination already populated. If the user has been
			// actively writing through GetConfigDir() they have a
			// live file here we must not overwrite.
			continue
		}
		// Copy with explicit Close so any EIO on dst is caught
		// per-file rather than leaking a half-written copy.
		in, err := os.Open(src)
		if err != nil {
			continue
		}
		out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o644)
		if err != nil {
			_ = in.Close()
			continue
		}
		_, _ = io.Copy(out, in)
		_ = out.Close()
		_ = in.Close()
	}
}

// Resolve joins the assets dir with a subpath (e.g.
// Resolve("templates") → <assets>/templates). Read-only by intent;
// callers needing a writable location should use ResolveConfig.
func Resolve(subpath string) string {
	return filepath.Join(GetAssetsDir(), subpath)
}

// ResolveConfig joins the config dir with a subpath (e.g.
// ResolveConfig("stats.json") → <configDir>/stats.json). All
// per-run state reads/writes should funnel through this so the
// wails dev watcher never sees them.
func ResolveConfig(subpath string) string {
	return filepath.Join(GetConfigDir(), subpath)
}
