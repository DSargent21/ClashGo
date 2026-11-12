//go:build !cli
// +build !cli

package main

import (
	"context"
	"embed"
	"os"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/mac"
)

//go:embed all:web/dist
var assets embed.FS

func main() {
	liveSmoke := liveSmokeRequested()
	if liveSmoke {
		// Fail closed: closing the window before the raid check completes is
		// not a pass. runLiveSmoke clears this only after verification.
		liveSmokeExitCode.Store(1)
	}

	// Create an instance of the app structure
	app := NewApp()
	app.liveSmoke = liveSmoke

	// Create application with options
	err := wails.Run(&options.App{
		Title:     "ClashGO Mission Control",
		Width:     1200,
		Height:    850,
		MinWidth:  1024,
		MinHeight: 768,
		AssetServer: &assetserver.Options{
			Assets: assets,
		},
		BackgroundColour: &options.RGBA{R: 9, G: 9, B: 11, A: 1},
		OnStartup: func(ctx context.Context) {
			app.startup(ctx)
			if liveSmoke {
				go app.runLiveSmoke(ctx)
			}
		},
		OnShutdown: app.shutdown,
		Bind: []interface{}{
			app,
		},
		Mac: &mac.Options{
			TitleBar: &mac.TitleBar{
				TitlebarAppearsTransparent: true,
				HideTitle:                  true,
				FullSizeContent:            true,
				UseToolbar:                 false,
				HideToolbarSeparator:       true,
			},
			Appearance:           mac.NSAppearanceNameDarkAqua,
			WebviewIsTransparent: true,
			WindowIsTranslucent:  true,
			About: &mac.AboutInfo{
				Title:   "ClashGO",
				Message: "© 2026 ClashGO Systems",
			},
		},
	})

	if err != nil {
		println("Error:", err.Error())
		liveSmokeExitCode.Store(1)
	}
	if liveSmoke {
		os.Exit(int(liveSmokeExitCode.Load()))
	}
}
