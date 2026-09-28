package paths

import (
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
)

// resetForTest restores package-level state to a clean baseline so
// each test gets its own fresh migration cycle. Without this the
// sync.Once-guarded migration would lock in the first test's state
// for every subsequent test in the file.
func resetForTest(t *testing.T) {
	t.Helper()
	t.Setenv("CLASHGO_CONFIG_DIR", "")
	t.Setenv("CLASHGO_ASSETS_DIR", "")
	migrateOnce = sync.Once{}
}

// writeInCwd writes a regular file in the cwd with cleanup so tests
// don't leak files into the project tree.
func writeInCwd(t *testing.T, name string, data []byte) {
	t.Helper()
	p := filepath.Join(".", name)
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatalf("write %s: %v", p, err)
	}
	t.Cleanup(func() { _ = os.Remove(p) })
}

// TestGetConfigDirNotProjectRoot is the regression test for the
// "wails dev rebuilds mid bot session" bug. The fix's contract is:
// whatever GetConfigDir returns, it must NOT equal the cwd's
// absolute path. If it does, wails dev's filesystem watcher can see
// our state-file writes and spuriously trigger a rebuild + restart
// that kills the active bot.
func TestGetConfigDirNotProjectRoot(t *testing.T) {
	resetForTest(t)
	if runtime.GOOS == "darwin" && isPackagedApp() {
		t.Skip("running inside a packaged .app; production ~/Library path is correct as-is")
	}
	cwd, _ := os.Getwd()
	cwdAbs, _ := filepath.Abs(cwd)
	got := GetConfigDir()
	if got == "." {
		t.Fatalf("GetConfigDir returned '.'; this triggers wails dev rebuilds — bug regressed")
	}
	gotAbs, _ := filepath.Abs(got)
	if gotAbs == cwdAbs {
		t.Fatalf("GetConfigDir resolved to cwd (%s); should be outside project root to keep wails dev watcher quiet", got)
	}
	info, err := os.Stat(got)
	if err != nil {
		t.Fatalf("expected GetConfigDir to exist on disk after resolve: %v", err)
	}
	if !info.IsDir() {
		t.Fatalf("GetConfigDir is not a directory: %s", got)
	}
}

// setTempUserConfigBase points os.UserConfigDir() at a throwaway directory so a
// test can exercise the real resolution order without touching the user's own
// config tree. os.UserConfigDir() reads $HOME on macOS and $XDG_CONFIG_HOME on
// linux, so both are set.
func setTempUserConfigBase(t *testing.T) (base string, home string) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "xdg"))
	base, err := os.UserConfigDir()
	if err != nil {
		t.Fatalf("UserConfigDir: %v", err)
	}
	return base, home
}

// TestConfigDirIsOneTreeForEveryBinary is the regression test for the split that
// cost a live session its display scale. The bundled app (wails dev runs one) and
// the unbundled CLI used to resolve to different directories, so a pin written by
// the CLI probe tools was invisible to the app: every app run fell back to the
// geometry-derived scale (1.9% off at 1280x720), lost the troop-count label row,
// and deployed on blind top-up batches. Config resolution must not depend on the
// bundle layout, and must not end in a per-binary "dev" directory.
func TestConfigDirIsOneTreeForEveryBinary(t *testing.T) {
	resetForTest(t)
	base, _ := setTempUserConfigBase(t)

	got := resolveConfigDir()
	want := filepath.Join(base, "ClashGO")
	if filepath.Clean(got) != filepath.Clean(want) {
		t.Fatalf("resolveConfigDir() = %s, want %s for every binary layout", got, want)
	}
	if filepath.Base(got) == "dev" {
		t.Fatalf("resolveConfigDir() = %s: a per-binary tree again; a pinned device setting in one tree is invisible to the other", got)
	}

	// The escape hatch for genuinely separate installs still has to win.
	other := t.TempDir()
	t.Setenv("CLASHGO_CONFIG_DIR", other)
	if got := resolveConfigDir(); filepath.Clean(got) != filepath.Clean(other) {
		t.Fatalf("CLASHGO_CONFIG_DIR ignored: got %s, want %s", got, other)
	}
}

// TestMigrateFromLegacyDevTreeAdoptsThePin covers the state that actually
// existed on disk: the measured display scale pinned in the old per-binary tree's
// config.json. Adoption is copy-not-move, so the old tree stays readable and a
// user who wants to inspect or remove it can.
func TestMigrateFromLegacyDevTreeAdoptsThePin(t *testing.T) {
	resetForTest(t)
	base, _ := setTempUserConfigBase(t)

	configDir := filepath.Join(base, "ClashGO")
	legacyDir := filepath.Join(configDir, "dev")
	if err := os.MkdirAll(legacyDir, 0o755); err != nil {
		t.Fatal(err)
	}
	const pin = `{"device":{"display_scale":1.325,"width":1280,"height":720}}`
	if err := os.WriteFile(filepath.Join(legacyDir, "config.json"), []byte(pin), 0o644); err != nil {
		t.Fatal(err)
	}
	// A second state file proves the whole tree is adopted, not just config.json.
	if err := os.WriteFile(filepath.Join(legacyDir, "attack_history.json"), []byte(`[]`), 0o644); err != nil {
		t.Fatal(err)
	}

	if got := GetConfigDir(); filepath.Clean(got) != filepath.Clean(configDir) {
		t.Fatalf("GetConfigDir() = %s, want the unified tree %s", got, configDir)
	}

	for _, name := range []string{"config.json", "attack_history.json"} {
		data, err := os.ReadFile(filepath.Join(configDir, name))
		if err != nil {
			t.Fatalf("%s was not adopted from the legacy dev tree: %v", name, err)
		}
		if name == "config.json" && string(data) != pin {
			t.Errorf("adopted config.json = %s, want the pinned one %s", data, pin)
		}
		if _, err := os.Stat(filepath.Join(legacyDir, name)); err != nil {
			t.Errorf("legacy copy of %s is gone; migration must be copy-not-move: %v", name, err)
		}
	}
}

// TestMigrateDoesNotOverwriteUnifiedTree: adoption is only for files the unified
// tree does not have. A live file there belongs to the running app and must never
// be replaced by an older tree's copy.
func TestMigrateDoesNotOverwriteUnifiedTree(t *testing.T) {
	resetForTest(t)
	base, _ := setTempUserConfigBase(t)

	configDir := filepath.Join(base, "ClashGO")
	legacyDir := filepath.Join(configDir, "dev")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(legacyDir, 0o755); err != nil {
		t.Fatal(err)
	}
	const live = `{"device":{"display_scale":1.4}}`
	const stale = `{"device":{"display_scale":1.1}}`
	if err := os.WriteFile(filepath.Join(configDir, "config.json"), []byte(live), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacyDir, "config.json"), []byte(stale), 0o644); err != nil {
		t.Fatal(err)
	}

	GetConfigDir()

	data, err := os.ReadFile(filepath.Join(configDir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != live {
		t.Fatalf("unified config.json was overwritten by the legacy tree: got %s, want %s", data, live)
	}
}

// TestGetConfigDirOverride verifies CLASHGO_CONFIG_DIR wins over
// every other resolution rule. This is the escape hatch tests and
// portable installs depend on; a regression would silently send
// them to ~/Library on macOS.
func TestGetConfigDirOverride(t *testing.T) {
	resetForTest(t)
	tmp := t.TempDir()
	t.Setenv("CLASHGO_CONFIG_DIR", tmp)
	got := GetConfigDir()
	if filepath.Clean(got) != filepath.Clean(tmp) {
		t.Fatalf("CLASHGO_CONFIG_DIR override not honored; got %s, want %s", got, tmp)
	}
}

// TestMigrateLegacyStateCopiesMissing files from cwd → configDir
// when src exists and dst does not. Verifies the bytes round-trip
// and the migration runs exactly once.
func TestMigrateLegacyStateCopiesMissing(t *testing.T) {
	resetForTest(t)
	tmp := t.TempDir()
	t.Setenv("CLASHGO_CONFIG_DIR", tmp)

	const payload = `{"version":"0.1.0","samples":7}`
	writeInCwd(t, "boot_profile.json", []byte(payload))

	GetConfigDir() // first call copies boot_profile.json → tmp
	GetConfigDir() // second call must NOT re-copy (already present)

	dst := filepath.Join(tmp, "boot_profile.json")
	data, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("migrated file not found at %s: %v", dst, err)
	}
	if string(data) != payload {
		t.Fatalf("migrated content mismatch: got %q, want %q", data, payload)
	}

	// Idempotence: mutating the source after migration must not
	// change the destination. Proves the migration is once-only
	// per process (any path) and that subsequent calls truly
	// short-circuit on sync.Once.
	_ = os.WriteFile(filepath.Join(".", "boot_profile.json"), []byte(`{"tampered":true}`), 0o644)
	defer os.Remove(filepath.Join(".", "boot_profile.json"))
	GetConfigDir()
	data, _ = os.ReadFile(dst)
	if string(data) == `{"tampered":true}` {
		t.Fatalf("destination got overwritten on a later GetConfigDir call; migrateOnce was not idempotent")
	}
}

// TestMigrateLegacyStatePreservesExistingDest verifies the
// migration is copy-only — if the user already has a live state
// file at the destination path we must NOT overwrite it, because
// that file is being actively written to by the running bot.
func TestMigrateLegacyStatePreservesExistingDest(t *testing.T) {
	resetForTest(t)
	tmp := t.TempDir()
	t.Setenv("CLASHGO_CONFIG_DIR", tmp)

	const original = `{"dans":"dest-already"}`
	const legacy = `{"dans":"older-src"}`
	writeInCwd(t, "boot_profile.json", []byte(legacy))
	if err := os.WriteFile(filepath.Join(tmp, "boot_profile.json"), []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	GetConfigDir()

	data, err := os.ReadFile(filepath.Join(tmp, "boot_profile.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != original {
		t.Fatalf("destination was overwritten by migration; got %q, want %q (kept)", data, original)
	}
}

// TestMigrateLegacyStateShortCircuitsSameDir protects against the
// trivial-but-easy-to-regress case where CLASHGO_CONFIG_DIR is set
// to "." or empty and cwd resolution ends up equal to configDir.
// In that scenario we must NOT attempt an io.Copy that would
// truncate a file the live bot is actively writing to.
func TestMigrateLegacyStateShortCircuitsSameDir(t *testing.T) {
	resetForTest(t)
	cwd, _ := filepath.Abs(".")
	t.Setenv("CLASHGO_CONFIG_DIR", cwd)

	const payload = "live!"
	writeInCwd(t, "stats.json", []byte(payload))

	GetConfigDir()

	// If migration short-circuited, the file in cwd is still live
	// (we wrote `live!`). If migration mistakenly tried to copy
	// cwd=="." into cwd (same path), open+create+truncate could
	// have happened.
	data, _ := os.ReadFile(filepath.Join(cwd, "stats.json"))
	if string(data) != payload {
		t.Fatalf("file mutated by same-dir migration; got %q, want %q", data, payload)
	}
}

// TestResolveAndResolveConfigAreAbsolute guards against a future
// regression where someone forgets filepath.Abs at the end of
// GetConfigDir. Relative config paths are the exact thing that
// made the bug happen in the first place.
func TestResolveAndResolveConfigAreAbsolute(t *testing.T) {
	resetForTest(t)
	if runtime.GOOS == "darwin" && isPackagedApp() {
		t.Skip("running inside packaged .app")
	}
	if !filepath.IsAbs(Resolve("strategies/auto_edrag_rush.yaml")) {
		t.Errorf("Resolve returned a relative path")
	}
	// Pick any of the well-known state files; all should be absolute.
	if !filepath.IsAbs(ResolveConfig("boot_profile.json")) {
		t.Errorf("ResolveConfig returned a relative path")
	}
}
