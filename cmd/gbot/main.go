// Package main is the CLI entrypoint for gbot.
//
// Source reference: main.tsx
// Bootstraps config, LLM provider, tools, engine, and launches the TUI.
package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/liuy/gbot/pkg/app"
	"github.com/liuy/gbot/pkg/config"
	"github.com/liuy/gbot/pkg/connector/wechat"
	"github.com/liuy/gbot/pkg/project"
)

// Android's time.initLocal() is a stub that hardcodes time.Local to UTC and
// ignores $TZ (golang/go#20455). When the host passes an explicit TZ (the
// Android app does, from TimeZone.getDefault()), set time.Local from it. On
// other platforms the standard initLocal already handles this and TZ is
// typically unset, so this is a no-op there.
func init() {
	if tz := os.Getenv("TZ"); tz != "" && tz != "UTC" {
		if loc, err := time.LoadLocation(tz); err == nil {
			time.Local = loc
		}
	}
}

func main() {
	opts := app.ParseFlags(os.Args[1:])

	// WeChat login subcommand: `gbot wechat login` or `gbot -d wechat login`.
	// Checked before app.Start() so the PID guard does not create a PID
	// file for this one-shot command.
	loginIdx := slices.IndexFunc(os.Args[1:], func(a string) bool { return a == "wechat" })
	loginOk := loginIdx >= 0 && loginIdx+1 < len(os.Args[1:]) && os.Args[1:][loginIdx+1] == "login"
	if loginOk {
		home, err := os.UserHomeDir()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		workingDir, _ := os.Getwd()
		projectDir := project.Dir(workingDir)
		if opts.DaemonMode {
			// No MkdirAll here: wechat.SaveState creates the daemon dir on demand.
			daemonDir := filepath.Join(home, ".gbot", "daemon")
			projectDir = project.Dir(daemonDir)
		}

		cfg, err := config.Load()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error loading config: %v\n", err)
			os.Exit(1)
		}
		client := cfg.ProxyHTTPClient()
		accountID, err := wechat.Login(context.Background(), client, projectDir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "WeChat login failed: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("WeChat login successful. Account: %s\n", accountID)
		os.Exit(0)
	}

	inst, err := app.Start(opts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	defer inst.Cleanup()

	if opts.DaemonMode {
		// WS server is already started inside app.Start(). runDaemon waits
		// for a signal (non-Windows) or shows a wails window (Windows).
		runDaemon(inst, opts.WSPort)
		return
	}

	if err := inst.RunTUI(); err != nil {
		inst.Cleanup()
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}
