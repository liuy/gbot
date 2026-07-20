//go:build windows

package bash

import (
	"context"
)

// watchSigwinch is a no-op on Windows. ConPTY size changes are driven by
// explicit Resize calls; Windows has no SIGWINCH equivalent.
func watchSigwinch(_ ptyPty, _ <-chan struct{}) {}

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

// closeSlaveAfterStart is a no-op on Windows. ConPTY uses pipes (inPipe/outPipe)
// rather than a slave/master fd pair, so there's no slave to close.
func closeSlaveAfterStart(_ ptyPty) {}

// waitAndCloseAfterExit works around ConPTY's lack of automatic EOF on child
// exit (microsoft/terminal#4564). It runs Cmd.Wait() in a goroutine; once Wait
// returns, it closes the PTY — which closes the ConPTY output pipe and causes
// Drain's blocking Read to return an error, exiting the drain loop.
//
// This mirrors wezterm/portable-pty's pattern of dropping the slave and writer
// handles after process exit (see wezterm issue #463). go-pty's conPty doesn't
// expose slave/writer separately, so Close() (which calls ClosePseudoConsole +
// closes both pipes) is the equivalent operation.
//
// Race: closing the PTY could race with Drain's Read, potentially truncating
// the last few bytes of output buffered in the pipe. Acceptable trade-off —
// empirically verified to deliver complete output for typical commands.
func waitAndCloseAfterExit(ctx context.Context, s *PTYSession) <-chan error {
	ch := make(chan error, 1)
	go func() {
		err := s.Cmd.Wait()
		s.Close()
		ch <- err
	}()
	return ch
}
