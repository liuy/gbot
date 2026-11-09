//go:build !windows

// Package mcp implements the MCP (Model Context Protocol) client infrastructure.
package mcp

import (
	"log/slog"
	"os"
	"os/exec"
	"syscall"
)

// SetProcessGroup puts a spawned MCP server in its own process group
// (pgid == child pid). Group ownership is what lets the teardown's group
// signals reap the whole tree — including descendants that outlived their
// parents (e.g. a browser child surviving a crashed server) — via
// kill(-pgid, sig). Without it, signals reach only the top-level wrapper
// process.
//
// Setpgid (not Setsid) is deliberate: the two load-bearing properties both
// hold — group kill reaches the whole tree, and the child leaves the daemon's
// foreground process group so terminal-generated signals never reach MCP
// servers (all three stdio ends are pipes; session-leader SIGHUP semantics
// are irrelevant). omp's Bun detached:true (setsid) would add a behavior
// change with no additional guarantee here. darwin stays grouped too — gbot's
// shipped behavior; omp deliberately does not detach on darwin (documented
// deviation: group kill is safe there).
func SetProcessGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

// processGroupApplied reports whether SetProcessGroup actually puts children
// in a dedicated group on this platform. It seeds the transport's `detached`
// flag (omp resolveStdioSpawnCommand's detached matrix): only a transport
// that actually spawned into its own group may group-target its signals.
func processGroupApplied() bool { return true }

// signalStdio — omp stdio.ts signalStdioProcess:477-497. Group first when
// detached; ESRCH from the group signal means the group is already gone — a
// success, no fallback; any other group-signal failure (e.g. EPERM) falls
// back to signaling the direct child. Non-detached signals the direct child
// only: a negative-pid signal outside a dedicated group could hit an
// unrelated process group.
func (p *stdioProc) signalStdio(sig procSignal) {
	s := syscall.SIGTERM
	if sig == procSigKill {
		s = syscall.SIGKILL
	}
	pid := p.cmd.Process.Pid
	if p.detached {
		if err := syscall.Kill(-pid, s); err == nil {
			slog.Info("mcp: signaled stdio server process group", "server", p.name, "pgid", pid, "signal", s.String())
			return
		} else if err == syscall.ESRCH {
			slog.Info("mcp: stdio server process group already gone", "server", p.name, "pgid", pid, "signal", s.String())
			return
		}
		// Other group-signal failure: fall through to the direct child.
	}
	// Already-gone errors are ignored — teardown must stay no-op-safe against
	// an exited target across repeat calls (omp close() idempotency).
	_ = syscall.Kill(pid, s)
}

// isZombie reads the process state on Linux; on systems without /proc it
// reports false (the caller then treats the process as live — safe side).
func isZombie(pid int) bool {
	b, err := os.ReadFile("/proc/" + itoa(pid) + "/stat")
	if err != nil {
		return false
	}
	// state follows the "(comm)" field; comm can itself contain parens/spaces,
	// so anchor on the last ')'.
	for i := len(b) - 2; i >= 0; i-- {
		if b[i] == ')' {
			return i+2 < len(b) && b[i+2] == 'Z'
		}
	}
	return false
}

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	return string(buf[i:])
}
