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
	// Default to current working directory (dev mode)
	assetsDir = "assets"
	configDir = "."

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

// GetAssetsDir returns the absolute path to the assets directory
func GetAssetsDir() string {
	abs, err := filepath.Abs(assetsDir)
	if err != nil {
		return assetsDir
	}
	return abs
}

// GetConfigDir returns the absolute path to the directory for writable config/data files
func GetConfigDir() string {
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
