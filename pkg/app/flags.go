package app

import (
	"os"
	"runtime"
	"strings"
)

func ParseFlags(args []string) Options {
	var opts Options
	opts.WSPort = "8765"
	// Android has no terminal; force the headless path so the WUI HTTP+WS server
	// mounts and the Java WebView has something to load.
	if runtime.GOOS == "android" {
		opts.DaemonMode = true
		opts.NoTUI = true
	}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-d", "--daemon":
			// Global isolated daemon: no TUI, chdir to ~/.gbot/daemon.
			opts.DaemonMode = true
			opts.NoTUI = true
		case "-v", "--verbose":
			opts.Verbose = true
		case "-p", "--port":
			// Any explicit port flag = serve wui, no TUI. Bare -p keeps
			// the default port — the presence is the intent signal, so
			// `-p 8765` counts the same as `-p 1234`.
			opts.NoTUI = true
			// Consume the next token as the port ONLY if it is not itself
			// a flag: `-p -v` must stay verbose-and-default-port, and
			// `-p restart` (subcommand typo) must not become a listen
			// address of ":restart".
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				opts.WSPort = args[i+1]
				i++
			}
		}
	}
	if !opts.Verbose && os.Getenv("GBOT_VERBOSE") != "" {
		opts.Verbose = true
	}
	return opts
}
