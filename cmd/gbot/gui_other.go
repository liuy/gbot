//go:build !windows

package main

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/liuy/gbot/pkg/app"
)

func printDaemonBanner(wsPort string) {
	fmt.Fprintf(os.Stderr, "WUI server running at http://localhost:%s/\n", wsPort)
	fmt.Fprintf(os.Stderr, "Open the URL in a browser, or press Ctrl+C to exit.\n")
}

func runDaemon(_ *app.Instance, wsPort string) {
	printDaemonBanner(wsPort)
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh
}
