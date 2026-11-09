package api

import (
	"io/fs"

	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"

	"github.com/BikaSolution/bikasend/pkg/sdk/bikasend"
)

func Options(svc bikasend.Service, assets fs.FS) *options.App {
	app := &App{svc: svc}
	return &options.App{
		Title:            "Bikasend",
		Width:            1280,
		Height:           800,
		MinWidth:         800,
		MinHeight:        500,
		AssetServer:      &assetserver.Options{Assets: assets},
		OnStartup:        app.startup,
		Bind:             []any{app},
		BackgroundColour: &options.RGBA{R: 30, G: 30, B: 30, A: 255},
	}
}
