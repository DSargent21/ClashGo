//go:build !cli
// +build !cli

package main

import (
	"embed"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/mac"
)

//go:embed all:web/dist
var assets embed.FS

func main() {
	// Create an instance of the app structure
	app := NewApp()

	// Create application with options
	err := wails.Run(&options.App{
		Title:             "Vanguard Mission Control",
		Width:             1200,
		Height:            850,
		MinWidth:          1024,
		MinHeight:         768,
		AssetServer: &assetserver.Options{
			Assets: assets,
		},
		BackgroundColour: &options.RGBA{R: 9, G: 9, B: 11, A: 1},
		OnStartup:        app.startup,
		OnShutdown:       app.shutdown,
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
				Title:   "Vanguard",
				Message: "© 2026 Vanguard Systems",
			},
		},
	})

	if err != nil {
		println("Error:", err.Error())
	}
}
