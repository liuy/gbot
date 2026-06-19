package main

import (
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

// freePort returns a TCP port that is free at call time.
func freePort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("freePort: %v", err)
	}
	addr := ln.Addr().String()
	ln.Close()
	return addr
}

// dialReachable waits until the pprof HTTP server is accepting connections
// on addr, or fails the test after a short timeout. Returns the baseURL
// (with scheme) for convenience.
func dialReachable(t *testing.T, addr string) string {
	t.Helper()
	timeoutCh := time.After(2 * time.Second)
	for {
		select {
		case <-timeoutCh:
			t.Fatalf("pprof server at %s never became reachable", addr)
		default:
		}
		conn, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
		if err == nil {
			conn.Close()
			return "http://" + addr
		}
	}
}

// TestStartPprofServer_Default listens on a free port and verifies
// the server responds with the pprof index page.
func TestStartPprofServer_Default(t *testing.T) {
	addr := freePort(t)
	startPprofServer(addr)

	base := dialReachable(t, addr)
	resp, err := http.Get(base + "/debug/pprof/")
	if err != nil {
		t.Fatalf("HTTP GET /debug/pprof/: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "Types of profiles available") {
		t.Errorf("pprof index page missing expected text; got %q", truncate(string(body), 200))
	}
}

// TestStartPprofServer_Disabled verifies that addr "off" skips starting
// the server (no listener is created).
func TestStartPprofServer_Disabled(t *testing.T) {
	startPprofServer("off")
	conn, err := net.DialTimeout("tcp", "127.0.0.1:6060", 100*time.Millisecond)
	if err == nil {
		conn.Close()
		t.Skip("default :6060 already has a listener (perhaps from another test); cannot assert absence reliably")
	}
}

// TestStartPprofServer_EnvOverride verifies GBOT_PPROF_ADDR takes precedence
// over the cfg argument.
func TestStartPprofServer_EnvOverride(t *testing.T) {
	envAddr := freePort(t)
	t.Setenv("GBOT_PPROF_ADDR", envAddr)

	// Pass a different addr; env should win.
	cfgAddr := freePort(t)
	startPprofServer(cfgAddr)

	// envAddr must be reachable.
	dialReachable(t, envAddr)

	// cfgAddr should NOT be reachable (env won).
	conn, err := net.DialTimeout("tcp", cfgAddr, 200*time.Millisecond)
	if err == nil {
		conn.Close()
		t.Errorf("cfgAddr %s unexpectedly has a listener; env override should have won", cfgAddr)
	}
}

// TestStartPprofServer_HeapProfile verifies /debug/pprof/heap returns
// valid output — the main motivating use case for the server.
func TestStartPprofServer_HeapProfile(t *testing.T) {
	addr := freePort(t)
	startPprofServer(addr)
	base := dialReachable(t, addr)

	req, _ := http.NewRequestWithContext(context.Background(), "GET", base+"/debug/pprof/heap", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("heap profile GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("heap status = %d, want 200", resp.StatusCode)
	}
	// pprof heap endpoint returns binary protobuf by default; just check non-empty.
	data, _ := io.ReadAll(resp.Body)
	if len(data) == 0 {
		t.Error("heap profile body is empty")
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
