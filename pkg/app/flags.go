package app

import (
	"os"
	"runtime"
)

func ParseFlags(args []string) Options {
	var opts Options
	opts.WSPort = "8765"
	// Android has no terminal; force daemon mode so the WUI HTTP+WS server
	// mounts and the Java WebView has something to load.
	if runtime.GOOS == "android" {
		opts.DaemonMode = true
	}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-d", "--daemon":
			opts.DaemonMode = true
		case "-v", "--verbose":
			opts.Verbose = true
		case "-p", "--port":
			if i+1 < len(args) {
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
