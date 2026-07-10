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
