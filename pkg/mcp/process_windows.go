//go:build windows

// Package mcp implements the MCP (Model Context Protocol) client infrastructure.
package mcp

import (
	"os/exec"
)

// SetProcessGroup is a Unix-only concept (process groups). On Windows it is a
// no-op, so the stdio teardown terminates only the top-level process —
// descendants that outlive a crashed parent are not reaped here. The
// equivalent mechanism would be job objects; deliberately out of scope
// (matches omp's win32 branch).
func SetProcessGroup(*exec.Cmd) {}

// processGroupApplied reports whether SetProcessGroup actually puts children
// in a dedicated group on this platform. Windows: false — signalStdio then
// signals the direct child only.
func processGroupApplied() bool { return false }

// signalStdio — omp stdio.ts win32 branch: Windows has no signal concept, so
// both TERM and KILL collapse to TerminateProcess (Process.Kill). The
// stdin-EOF step and the exited-channel waits are platform-shared and live in
// stdio_lifecycle.go.
func (p *stdioProc) signalStdio(procSignal) {
	_ = p.cmd.Process.Kill()
}
