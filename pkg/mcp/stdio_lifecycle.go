// Package mcp implements the MCP (Model Context Protocol) client infrastructure.
//
// This file: gbot's own stdio subprocess transport implementing the omp close
// sequence. Source: oh-my-pi packages/coding-agent/src/mcp/transports/stdio.ts
// (StdioTransport/close/terminateStdioProcess/signalStdioProcess/waitForProcessExit).
//
// It replaces the SDK's CommandTransport, which cannot express that sequence:
// CommandTransport hides the stdin write end inside its pipeRWC and its Close
// signals only the direct child — wrapper commands (npx → npm → node) leave
// grandchildren behind. Owning the pipes here lets stdin close first (the
// MCP-spec shutdown contract) and the teardown target the whole process group
// on every path that closes a connection, including the SDK's internal
// close-on-handshake-failure.
package mcp

import (
	"context"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	mcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// Grace windows. Source: omp stdio.ts TERM_GRACE_MS / KILL_GRACE_MS.
const (
	stdioTermGrace = 1000 * time.Millisecond // observe a cooperative exit after SIGTERM
	stdioKillGrace = 500 * time.Millisecond  // observe SIGKILL taking effect before giving up
)

type procSignal int

const (
	procSigTerm procSignal = iota
	procSigKill
)

// stdioProcTransport owns the stdio subprocess and its teardown. It
// implements the SDK Transport interface so client.Connect drives it, and
// every connection Close — including the SDK's internal ones — funnels into
// the same teardown. Source: omp stdio.ts StdioTransport (#process).
type stdioProcTransport struct {
	name     string
	cmd      *exec.Cmd
	detached bool

	mu   sync.Mutex
	proc *stdioProc // non-nil from Connect until teardown grabs it (omp #process)
}

// stdioProc is the started process plus the pipe ends the parent still holds.
// The parent-facing copies of the child's ends (stdin read, stdout write) are
// dropped in Connect right after Start — keeping stdout's write end would
// keep the pipe open forever, so a dead server would never surface as stdout
// EOF to the framing layer.
type stdioProc struct {
	name     string
	cmd      *exec.Cmd
	stdinW   io.WriteCloser // parent write end; closing it delivers stdin EOF
	stdout   io.ReadCloser  // parent read end
	detached bool
	exited   chan error // cap 1, fed once by the cmd.Wait goroutine (omp proc.exited)
}

func newStdioProcTransport(name string, cmd *exec.Cmd, detached bool) *stdioProcTransport {
	return &stdioProcTransport{name: name, cmd: cmd, detached: detached}
}

// Connect implements mcp.Transport: create the pipes, start the command, and
// wrap the SDK's IOTransport for ndjson framing (no reimplementation of the
// JSON-RPC wire format).
func (t *stdioProcTransport) Connect(ctx context.Context) (mcp.Connection, error) {
	stdinR, stdinW, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		_ = stdinR.Close()
		_ = stdinW.Close()
		return nil, err
	}
	t.cmd.Stdin, t.cmd.Stdout = stdinR, stdoutW
	if err := t.cmd.Start(); err != nil {
		_ = stdinR.Close()
		_ = stdinW.Close()
		_ = stdoutR.Close()
		_ = stdoutW.Close()
		return nil, err
	}
	// Drop the parent's copies of the child-facing ends (see stdioProc doc).
	_ = stdinR.Close()
	_ = stdoutW.Close()

	p := &stdioProc{
		name:     t.name,
		cmd:      t.cmd,
		stdinW:   stdinW,
		stdout:   stdoutR,
		detached: t.detached,
		exited:   make(chan error, 1),
	}
	go func() { p.exited <- t.cmd.Wait() }()

	// Guarded by the mutex for the concurrent-Teardown race (a Teardown
	// grabbing between Start and this store must not miss the process).
	// Double Connect never reaches here: cmd.Start rejects a reused command
	// with "exec: already started" — the SDK's one-Connect contract is
	// enforced by the exec package itself.
	t.mu.Lock()
	t.proc = p
	t.mu.Unlock()

	framed, err := (&mcp.IOTransport{Reader: p.stdout, Writer: p.stdinW}).Connect(ctx)
	if err != nil {
		return nil, err
	}
	return &stdioProcConn{t: t, framed: framed}, nil
}

// stdioProcConn adapts the framed connection so its Close runs the transport
// teardown. Source: omp stdio.ts StdioTransport implements MCPTransport; the
// framing itself is the SDK's IOTransport.
type stdioProcConn struct {
	t      *stdioProcTransport
	framed mcp.Connection
}

func (c *stdioProcConn) Read(ctx context.Context) (jsonrpc.Message, error) {
	return c.framed.Read(ctx)
}

func (c *stdioProcConn) Write(ctx context.Context, msg jsonrpc.Message) error {
	return c.framed.Write(ctx, msg)
}

func (c *stdioProcConn) SessionID() string { return "" }

func (c *stdioProcConn) Close() error {
	c.t.Teardown()
	// framed.Close signals the ioConn's closed channel so its read goroutine
	// exits even when no jsonrpc2 reader loop is draining it. Errors are
	// expected — teardown already closed both pipes.
	_ = c.framed.Close()
	return nil
}

// Teardown runs the omp close sequence with production graces. Idempotent and
// safe for concurrent callers.
func (t *stdioProcTransport) Teardown() {
	t.teardownGraces(stdioTermGrace, stdioKillGrace)
}

// teardownGraces — omp stdio.ts close() #process handling: the process handle
// is grabbed and nilled in one critical section BEFORE any awaiting, so
// repeat and concurrent callers skip straight past instead of blocking on the
// first caller's grace windows or double-signaling.
func (t *stdioProcTransport) teardownGraces(termGrace, killGrace time.Duration) {
	t.mu.Lock()
	p := t.proc
	t.proc = nil
	t.mu.Unlock()
	if p == nil {
		// Connect can fail before storing (e.g. nonexistent command: the
		// cmd.Start error path closes fds and returns without a proc), and the
		// SDK then surfaces a transport error with no connection Close —
		// connectInner still calls Teardown. Nothing to do.
		return
	}
	p.teardown(termGrace, killGrace)
}

// teardown is the omp five-step close. Source: stdio.ts close():916-956,
// terminateStdioProcess():519-539.
//
//	(1) cooperative stdin EOF — errors ignored (already closed/dead)
//	(2) SIGTERM, group-targeted when detached
//	(3) race leader exit vs TERM grace; a non-detached leader exit ends it
//	(4) group SIGKILL sweep — fired ALWAYS when detached, because a grandchild
//	    can outlive the leader and ignore SIGTERM
//	(5) KILL grace wait only when the leader itself hadn't exited yet
func (p *stdioProc) teardown(termGrace, killGrace time.Duration) {
	pid := p.cmd.Process.Pid

	// (1) Source: stdio.ts:939-943 — cooperative EOF first; a well-behaved
	// server exits on stdin close per the MCP shutdown contract.
	if err := p.stdinW.Close(); err != nil {
		slog.Debug("mcp: stdio stdin already closed", "server", p.name, "pid", pid, "error", err)
	}

	// (2)
	p.signalStdio(procSigTerm)

	// (3) Source: stdio.ts:526 — exit of any kind counts as exited.
	exitedOnTerm := waitForProcessExit(p.exited, termGrace)

	if exitedOnTerm && !p.detached {
		// Source: stdio.ts:532 — a non-detached transport has no process
		// group beyond the leader itself; once it exits, nothing is left.
		slog.Info("mcp: stdio server exited after SIGTERM", "server", p.name, "pid", pid)
		p.closeTailFDs()
		return
	}

	// (4) Source: stdio.ts:533 — a detached leader's exit does NOT prove the
	// group is empty; the sweep always fires in detached mode.
	if exitedOnTerm {
		slog.Info("mcp: stdio server leader exited on SIGTERM, sweeping group", "server", p.name, "pid", pid)
	}
	p.signalStdio(procSigKill)

	// (5) Source: stdio.ts:538 — after a leader exit the KILL above is a
	// fire-and-forget sweep for surviving members; only block on the grace
	// window when the leader itself is still being escalated against.
	if !exitedOnTerm {
		if !waitForProcessExit(p.exited, killGrace) {
			slog.Error("mcp: stdio server still alive after SIGKILL", "server", p.name, "pid", pid)
		}
	}
	p.closeTailFDs()
}

// closeTailFDs drops the parent's remaining pipe ends. Errors are ignored —
// double closes are expected once the teardown ran.
func (p *stdioProc) closeTailFDs() {
	_ = p.stdinW.Close()
	_ = p.stdout.Close()
}

// waitForProcessExit races the leader's exit against a timer. Any settle of
// the exited channel counts — there is nothing left to wait on either way.
// Source: stdio.ts:440-454 waitForProcessExit.
func waitForProcessExit(exited <-chan error, timeout time.Duration) bool {
	select {
	case <-exited:
		return true
	case <-time.After(timeout):
		return false
	}
}
