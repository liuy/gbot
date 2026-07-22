//go:build !windows

package main

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/liuy/gbot/pkg/app"
)

func printGUIMessage(wsPort string) {
	fmt.Fprintf(os.Stderr, "Wails GUI is Windows-only.\n")
	fmt.Fprintf(os.Stderr, "WUI server running at http://localhost:%s/\n", wsPort)
	fmt.Fprintf(os.Stderr, "Open the URL in a browser, or press Ctrl+C to exit.\n")
}

func runGUI(inst *app.Instance, wsPort string) {
	printGUIMessage(wsPort)
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh
}
