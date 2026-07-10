package paths

import (
	"os"
	"path/filepath"
	"runtime"
)

var (
	assetsDir string
	configDir string
)

func init() {
	// Default to current working directory (dev mode).
	// Note: CLASHGO_CONFIG_DIR / CLASHGO_ASSETS_DIR overrides are
	// read at call time inside GetConfigDir / GetAssetsDir so tests
	// using t.Setenv() actually redirect.
	assetsDir = "assets"
	configDir = "."

	// Search upward for the "assets" folder (resolves path issues when running tests in subdirectories)
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

	if runtime.GOOS == "darwin" {
		// Check if we are running inside a .app bundle
		execPath, err := os.Executable()
		if err == nil {
			// execPath is typically ClashGO.app/Contents/MacOS/ClashGO
			// Resources are in ClashGO.app/Contents/Resources
			// We look for Assets inside Resources

			// Potential paths:
			// 1. ProjectRoot/build/bin/ClashGO.app/Contents/MacOS/ClashGO
			// 2. /Applications/ClashGO.app/Contents/MacOS/ClashGO

			dir := filepath.Dir(execPath) // ClashGO.app/Contents/MacOS
			if filepath.Base(dir) == "MacOS" {
				contentsDir := filepath.Dir(dir) // ClashGO.app/Contents
				resourcesAssets := filepath.Join(contentsDir, "Resources", "assets")

				if _, err := os.Stat(resourcesAssets); err == nil {
					assetsDir = resourcesAssets

					// For writable config, use Application Support
					home, _ := os.UserHomeDir()
					configDir = filepath.Join(home, "Library", "Application Support", "ClashGO")
					_ = os.MkdirAll(configDir, 0755)
				}
			}
		}
	}
}

// GetAssetsDir returns the absolute path to the assets directory.
// CLASHGO_ASSETS_DIR overrides resolution at call time for tests +
// portable installs.
func GetAssetsDir() string {
	if override := os.Getenv("CLASHGO_ASSETS_DIR"); override != "" {
		return override
	}
	abs, err := filepath.Abs(assetsDir)
	if err != nil {
		return assetsDir
	}
	return abs
}

// GetConfigDir returns the absolute path to the directory for writable
// config/data files. CLASHGO_CONFIG_DIR overrides resolution at call
// time — useful for tests (via t.Setenv) and for users who want a
// portable install (state next to the binary, not ~/Library/Application
// Support). Reading at call time matters because Go's package init()
// runs once before any test's t.Setenv would land.
func GetConfigDir() string {
	if override := os.Getenv("CLASHGO_CONFIG_DIR"); override != "" {
		return override
	}
	abs, err := filepath.Abs(configDir)
	if err != nil {
		return configDir
	}
	return abs
}

// Resolve joins the assets directory with the given subpath
func Resolve(subpath string) string {
	return filepath.Join(GetAssetsDir(), subpath)
}

// ResolveConfig joins the config directory with the given subpath
func ResolveConfig(subpath string) string {
	return filepath.Join(GetConfigDir(), subpath)
}
