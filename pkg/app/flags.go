package app

import "os"

func ParseFlags(args []string) Options {
	var opts Options
	opts.WSPort = "8765"
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
