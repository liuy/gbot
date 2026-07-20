//go:build !windows

package bash

import (
	"context"
	"time"

	"github.com/aymanbagabas/go-pty"
)

// SigwinchPollInterval is the interval for checking terminal resize events.
const SigwinchPollInterval = 500 * time.Millisecond

// watchSigwinch monitors terminal resize and forwards to the PTY.
// Stops when the stop channel is closed.
//
// Source: ink.tsx:226 — process.on('SIGWINCH', handleResize)
func watchSigwinch(p ptyPty, stop <-chan struct{}) {
	ticker := time.NewTicker(SigwinchPollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			_ = setPTYWindowSize(p)
		}
	}
}

// setPTYWindowSize reads the controlling terminal size and applies it to the
// PTY via Resize. go-pty's Resize signature is Resize(width, height) where
// width=cols and height=rows — note the swap from GetTerminalSize's (rows, cols).
func setPTYWindowSize(p ptyPty) error {
	rows, cols, err := GetTerminalSize()
	if err != nil {
		return err
	}
	return p.Resize(cols, rows)
}

// closeSlaveAfterStart closes the parent's slave end of the Unix PTY so the
// master receives EOF when the child process exits. Without this, Drain would
// block forever on commands that exit naturally (the slave fd held by the
// parent keeps the master from seeing EOF even after the child closes its
// dups on exit).
//
// Note: pty.Pty.Close() will subsequently attempt to close the slave again,
// returning os.ErrClosed. PTYSession.Close() ignores that error — benign.
func closeSlaveAfterStart(p ptyPty) {
	if up, ok := p.(pty.UnixPty); ok {
		_ = up.Slave().Close()
	}
}

// waitAndCloseAfterExit returns nil on Unix. Unix's natural kernel-level EOF
// (delivered when closeSlaveAfterStart closed the slave) means Drain exits
// without any help — the Run loop calls Cmd.Wait() serially after Drain.
func waitAndCloseAfterExit(_ context.Context, _ *PTYSession) <-chan error {
	return nil
}
