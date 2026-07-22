package main

import (
	"fmt"
	"os"

	"github.com/liuy/gbot/pkg/app"
)

func main() {
	opts := app.ParseFlags(os.Args[1:])

	inst, err := app.Start(opts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	defer inst.Cleanup()

	if opts.DaemonMode {
		runGUI(inst, opts.WSPort)
	} else {
		if err := inst.RunTUI(); err != nil {
			inst.Cleanup()
			fmt.Fprintf(os.Stderr, "Error running TUI: %v\n", err)
			os.Exit(1)
		}
	}
}
