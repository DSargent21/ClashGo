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
//  1. CLASHGO_CONFIG_DIR env var (tests, portable installs, two installs
//     side by side — the only supported way to run two trees).
//  2. os.UserConfigDir()/ClashGO on every platform: on macOS that is
//     ~/Library/Application Support/ClashGO, on linux $XDG_CONFIG_HOME/ClashGO,
//     on windows %APPDATA%/ClashGO.
//  3. fallback → os.TempDir()/ClashGO
//
// ONE TREE PER USER, for both binaries the project ships. This used to split by
// binary — a bundled .app (which is what `wails dev` runs: build/bin/ClashGO.app)
// got <base>/ClashGO while the unbundled CLI got <base>/ClashGO/dev — and that
// split quietly cost a live session its geometry: the display scale is a machine
// property, tuned with the CLI probe tools (cmd/resprobe, cmd/screendump
// -anchors) and pinned in config.json, so with the trees split the pin lived in
// the CLI's file while every app run read a different one, fell back to the
// geometry-derived scale measured 1.9% off at 1280x720, lost the troop-count
// label row to that drift, and turned deploy verification into blind top-up
// batches. A device setting cannot live in a per-binary tree.
//
// GetAssetsDir follows the same one-tree rule as GetConfigDir: a wails-dev build
// reads the source assets/ tree it was built from rather than the stale copy the
// app bundle happens to carry, so `make pick-coords` and the running bot always
// agree about where the user's pins live. DescribeAssets reports which tree won
// and which one was ignored, so a run can log the pin file it actually obeyed.
//
// Legacy compatibility: the first GetConfigDir() call runs a one-time migration
// that copies known state files into the resolved directory from every tree in
// legacyStateTrees (today: the old per-binary "dev" tree, and the project's cwd
// as before). Copy-not-move — the legacy copy stays where it was so manual
// recovery is trivial and a failed migration cannot lose data. Re-runs are also
// harmless: sync.Once + os.OpenFile(O_EXCL) ensure destination files are never
// truncated or overwritten.
package paths

import (
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
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

// assetsProvenance records HOW the last GetAssetsDir call resolved, so a run can
// state which asset tree — and therefore which precision_config.json pin file —
// it obeyed, and can name the copy it ignored. See DescribeAssets.
type assetsProvenance struct {
	dir        string
	source     string
	ignoredDir string
}

var lastAssets assetsProvenance

// AssetsProvenance is what DescribeAssets reports.
type AssetsProvenance struct {
	// Dir is the asset tree in use.
	Dir string
	// Source says which rule picked Dir, in words.
	Source string
	// IgnoredDir is a second asset tree that was found but NOT used (today:
	// the app bundle's own Resources/assets when a wails-dev tree outranks
	// it). Empty when only one tree exists. When it is set, Dir and
	// IgnoredDir are two different pin files and the run should say so.
	IgnoredDir string
}

// DescribeAssets reports how GetAssetsDir resolved. Call it after any
// GetAssetsDir call in the same process (it is set on every resolution).
func DescribeAssets() AssetsProvenance {
	return AssetsProvenance{Dir: lastAssets.dir, Source: lastAssets.source, IgnoredDir: lastAssets.ignoredDir}
}

// GetAssetsDir returns the absolute path to the assets directory,
// honoring CLASHGO_ASSETS_DIR at call time so tests and portable
// installs can override without rebuilding.
//
// ONE ASSET TREE, for the same reason GetConfigDir unified the state trees. The
// app bundle carries a COPY of assets/, taken when the bundle was last built, so
// a packaged app that reads its own copy attacks with whatever geometry was
// pinned at that moment while cmd/pick_coords writes assets/precision_config.json
// in the project tree and reports success. `wails dev` runs
// build/bin/ClashGO.app, so the developer loop is exactly the case that hits it:
// live 2026-09-27 the app spent a night attacking with pins from a bundle built
// 2026-08-10 (its TopRight deploy line resolved to (1086,263)->(699,130) — the
// bundle's edges scaled — while the pins the user had just drawn describe
// (816,130)->(1086,251)), which put troops on refused ground, the siege and every
// hero on one tile, and the spells on lines that were not the pinned ones.
//
// A wails-dev build output (an executable under .../build/bin) therefore uses the
// source tree it was built from. An installed .app (anywhere else) still uses its
// bundle, which is the only tree it ships with.
func GetAssetsDir() string {
	if override := os.Getenv("CLASHGO_ASSETS_DIR"); override != "" {
		lastAssets = assetsProvenance{dir: override, source: "CLASHGO_ASSETS_DIR"}
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
			_, bundledOK := dirExists(res)
			src, haveSrc := sourceAssetsTree(execPath)

			if haveSrc && underBuildBin(filepath.Dir(execPath)) {
				ignored := ""
				if bundledOK {
					ignored = res
				}
				lastAssets = assetsProvenance{
					dir:        src,
					source:     "source tree of this wails-dev build (the bundle's copy of assets/ is a build artifact)",
					ignoredDir: ignored,
				}
				return src
			}

			if bundledOK {
				lastAssets = assetsProvenance{dir: res, source: "app bundle Contents/Resources/assets", ignoredDir: src}
				return res
			}
		}
	}

	abs, err := filepath.Abs(assetsDir)
	if err != nil {
		lastAssets = assetsProvenance{dir: assetsDir, source: "project assets/ (cwd walk-up)"}
		return assetsDir
	}
	lastAssets = assetsProvenance{dir: abs, source: "project assets/ (cwd walk-up)"}
	return abs
}

// sourceAssetsTree walks up from the running executable looking for a project
// asset tree: a directory holding assets/strategies, the marker that separates a
// source checkout from an installed bundle. Returns the assets dir and whether it
// was found. The walk stops at the filesystem root so a stray /assets can never
// be picked up.
func sourceAssetsTree(execPath string) (string, bool) {
	dir := filepath.Dir(execPath)
	for {
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
		cand := filepath.Join(dir, "assets")
		if _, ok := dirExists(filepath.Join(cand, "strategies")); ok {
			return cand, true
		}
	}
}

// underBuildBin reports whether dir lies inside a .../build/bin output tree,
// which is what `wails dev` and `make` build into. That layout is the marker for
// "this process is a development build of the project, not an installed app".
func underBuildBin(dir string) bool {
	parts := strings.Split(filepath.ToSlash(dir), "/")
	for i := 0; i+1 < len(parts); i++ {
		if parts[i] == "build" && parts[i+1] == "bin" {
			return true
		}
	}
	return false
}

// FileAge reports how long ago path was last modified, and whether it could be
// read at all. Deploy logging uses it to say exactly which pin file a run obeyed
// and how stale it is — a run that names its geometry's mtime can no longer be
// silently attacking with last month's pins.
func FileAge(path string) (time.Duration, bool) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, false
	}
	return time.Since(info.ModTime()), true
}

// dirExists is os.Stat + IsDir, the shape every probe above wants.
func dirExists(path string) (os.FileInfo, bool) {
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		return nil, false
	}
	return info, true
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

// resolveConfigDir picks the base directory for writable state. The same
// answer for both binaries (see the package doc): <os.UserConfigDir()>/ClashGO,
// which on macOS is the path the packaged app has always used, so unifying the
// trees moves no existing app state.
//
// Reads CLASHGO_CONFIG_DIR fresh each call so tests using t.Setenv still
// redirect. NEVER returns "." (the project root) — doing so would put state files
// in the wails dev watch tree and trigger spurious rebuilds that kill the active
// bot session.
//
// os.UserConfigDir handles mac/linux/windows correctly AND any sandbox case
// where raw ~/Library does not work.
func resolveConfigDir() string {
	if override := os.Getenv("CLASHGO_CONFIG_DIR"); override != "" {
		return ensureDir(override)
	}

	base, err := os.UserConfigDir()
	if err != nil || base == "" {
		base = os.TempDir()
	}

	return ensureDir(filepath.Join(base, "ClashGO"))
}

// legacyStateTrees returns the additional trees older versions wrote state into,
// most-recently-used first.
//
//  1. <configDir>/dev — the per-binary tree the unbundled CLI used while the
//     trees were split. This is the one that matters: it held the config.json
//     whose device block carries the measured display scale, and a pin the app
//     cannot see is precisely the bug the unification fixes.
//  2. "." — the cwd, where pre-config-dir versions wrote state files directly
//     into the project tree.
//
// Order is significant because migration copies at most once per file: the first
// tree that can supply a name wins, and the dev tree is the newer of the two.
func legacyStateTrees(configDir string) []string {
	trees := make([]string, 0, 2)
	if configDir != "" {
		trees = append(trees, filepath.Join(configDir, "dev"))
	}
	return append(trees, ".")
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

// migrateLegacyState best-effort-copies each known legacy state file from every
// tree in legacyStateTrees into the resolved config dir, only when the source
// exists and the destination does not. Copy-not-move is deliberate — losing a
// single file mid-migration (e.g. disk full, permission on dst) would be quiet
// and unrecoverable if we used os.Rename. The legacy files are .gitignore'd (and
// the dev tree is left in place) so nothing vanishes for a user who wants to
// inspect or remove it.
func migrateLegacyState(configDir string) {
	if configDir == "" {
		return
	}
	absDst, err := filepath.Abs(configDir)
	if err != nil || absDst == "" {
		return
	}

	for _, tree := range legacyStateTrees(configDir) {
		migrateStateFromTree(absDst, tree)
	}
}

// migrateStateFromTree copies the known state files from one legacy tree into
// absDst. Short-circuits when the source resolves to the destination itself, so
// test fixtures and CLASHGO_CONFIG_DIR="." overrides don't self-trigger.
func migrateStateFromTree(absDst, tree string) {
	srcDir, err := filepath.Abs(tree)
	if err != nil || srcDir == "" || srcDir == absDst {
		// Same physical dir — nothing to migrate. Covers
		// CLASHGO_CONFIG_DIR="." and tests running in cwd.
		return
	}

	for _, name := range legacyStateFiles {
		src := filepath.Join(srcDir, name)
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
