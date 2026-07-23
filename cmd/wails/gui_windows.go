//go:build windows

package main

import (
	"fmt"
	"log/slog"

	"github.com/liuy/gbot/pkg/app"
	"github.com/wailsapp/wails/v3/pkg/application"
)

func runGUI(inst *app.Instance, wsPort string) {
	wailsApp := application.New(application.Options{
		Name: "GBot",
	})
	w := wailsApp.Window.NewWithOptions(application.WebviewWindowOptions{
		Title:  "",
		Width:  1200,
		Height: 800,
		URL:    fmt.Sprintf("http://localhost:%s/", wsPort),
	})
	w.Show()
	if err := wailsApp.Run(); err != nil {
		slog.Error("wails: run failed", "error", err)
	}
}
