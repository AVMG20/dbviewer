package main

import (
	"embed"
	"log"

	"dbviewer/internal/services"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

//go:embed all:frontend/dist
var assets embed.FS

func main() {
	core, err := services.NewCore()
	if err != nil {
		log.Fatalf("dbviewer: %v", err)
	}

	app := application.New(application.Options{
		Name:        "DB Viewer",
		Description: "Database viewer for PostgreSQL, MySQL and SQLite",
		Services: []application.Service{
			application.NewService(services.NewProjectService(core)),
			application.NewService(services.NewConnectionService(core)),
			application.NewService(services.NewSchemaService(core)),
			application.NewService(services.NewDataService(core)),
			application.NewService(services.NewQueryService(core)),
			application.NewService(services.NewAIService(core)),
		},
		Assets: application.AssetOptions{
			Handler: application.AssetFileServerFS(assets),
		},
		Mac: application.MacOptions{
			ApplicationShouldTerminateAfterLastWindowClosed: true,
		},
		OnShutdown: core.Close,
	})
	app.Menu.Set(appMenu(app))

	// Hidden-inset title bar: the traffic lights float over our own title bar
	// (drawn in App.vue, 38px high, draggable via `--wails-draggable: drag`).
	titleBar := application.MacTitleBarHiddenInset
	titleBar.ToolbarStyle = application.MacToolbarStyleUnifiedCompact
	opts := application.WebviewWindowOptions{
		Name:             "main",
		Title:            "DB Viewer",
		Width:            defaultWidth,
		Height:           defaultHeight,
		MinWidth:         minWidth,
		MinHeight:        minHeight,
		BackgroundColour: application.NewRGB(0x1c, 0x1c, 0x1f),
		URL:              "/",
		Mac: application.MacWindow{
			TitleBar:                titleBar,
			InvisibleTitleBarHeight: 0,
			Backdrop:                application.MacBackdropNormal,
		},
	}
	applyWindowOptions(core.Store, &opts) // restore the last size and position
	win := app.Window.NewWithOptions(opts)
	app.Event.OnApplicationEvent(events.Common.ApplicationStarted, func(*application.ApplicationEvent) {
		ensureOnScreen(app, win)
		trackWindowState(core.Store, win)
	})

	if err := app.Run(); err != nil {
		log.Fatal(err)
	}
}
