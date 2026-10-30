package mcp

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"

	mcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// Zoned IP literals are the only way to drive ssrfDialContext's post-lookup
// validation loop without a DNS server: net.ParseIP rejects the zone suffix,
// so the code takes the resolver path, and the resolver short-circuits the
// literal instead of querying. The mapped form keeps the dial on IPv4
// loopback, so no ::1 route is required.
const (
	zonedLoopback  = "::ffff:127.0.0.1%lo"
	zonedLinkLocal = "fe80::1%eth0"
)

func stdioCommand(t *testing.T, cfg *StdioConfig) *exec.Cmd {
	t.Helper()
	tr, err := newStdioTransport("named-server", cfg, ScopeUser, true)
	if err != nil {
		t.Fatalf("newStdioTransport: %v", err)
	}
	ct, ok := tr.(*mcp.CommandTransport)
	if !ok {
		t.Fatalf("transport = %T, want *mcp.CommandTransport", tr)
	}
	if ct.Command == nil {
		t.Fatal("CommandTransport.Command is nil")
	}
	return ct.Command
}

func envHas(env []string, want string) bool {
	return slices.Contains(env, want)
}

// The SplitHostPort failure must stay inspectable, not just readable: callers
// classify address-shape problems by unwrapping to *net.AddrError.
func TestSsrfDialContext_InvalidAddressKeepsAddrError(t *testing.T) {
	_, err := ssrfDialContext(context.Background(), "tcp", "example.com")
	var addrErr *net.AddrError
	if !errors.As(err, &addrErr) {
		t.Fatalf("error %v (%T) does not wrap *net.AddrError", err, err)
	}
	if addrErr.Addr != "example.com" {
		t.Errorf("AddrError.Addr = %q, want %q", addrErr.Addr, "example.com")
	}
	if addrErr.Err != "missing port in address" {
		t.Errorf("AddrError.Err = %q, want %q", addrErr.Err, "missing port in address")
	}
	want := `mcp: invalid address "example.com": address example.com: missing port in address`
	if err.Error() != want {
		t.Errorf("error = %q, want %q", err.Error(), want)
	}
}

// A resolver failure must be reported as a DNS failure, not as ENOTFOUND from
// the empty-answer guard further down, and must keep the *net.DNSError.
func TestSsrfDialContext_DNSFailureKeepsDNSError(t *testing.T) {
	const host = "gbot-ssrf-probe.invalid"
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := ssrfDialContext(ctx, "tcp", host+":80")
	var dnsErr *net.DNSError
	if !errors.As(err, &dnsErr) {
		t.Fatalf("error %v (%T) does not wrap *net.DNSError", err, err)
	}
	if dnsErr.Name != host {
		t.Errorf("DNSError.Name = %q, want %q", dnsErr.Name, host)
	}
	want := fmt.Sprintf("mcp: DNS lookup failed for %q: ", host)
	if !strings.HasPrefix(err.Error(), want) {
		t.Errorf("error = %q, want prefix %q", err.Error(), want)
	}
}

// The blocked-address message is the caller's only handle on a rejected
// endpoint, so both halves of it — the hostname as dialed and the normalized
// IP that tripped the blocklist — must be exact.
func TestSsrfDialContext_IPLiteralBlockedMessages(t *testing.T) {
	const suffix = " (private/link-local address). Loopback (127.0.0.1, ::1) is allowed for local dev"
	cases := []struct {
		addr string
		want string
	}{
		{"10.0.0.1:80", "mcp: blocked: 10.0.0.1 resolves to 10.0.0.1" + suffix},
		{"169.254.169.254:80", "mcp: blocked: 169.254.169.254 resolves to 169.254.169.254" + suffix},
		{"[fc00::1]:80", "mcp: blocked: fc00::1 resolves to fc00::1" + suffix},
		// The mapped hex form is dialed as written and reported normalized.
		{"[::ffff:a9fe:a9fe]:80", "mcp: blocked: ::ffff:a9fe:a9fe resolves to 169.254.169.254" + suffix},
	}
	for _, tc := range cases {
		t.Run(tc.addr, func(t *testing.T) {
			conn, err := ssrfDialContext(context.Background(), "tcp", tc.addr)
			if conn != nil {
				_ = conn.Close()
				t.Fatalf("conn returned for %s, want nil", tc.addr)
			}
			if err == nil || err.Error() != tc.want {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
		})
	}
}

// A zoned link-local literal reaches the post-lookup loop, so the loop's block
// decision — not the dial's own failure — is what must reject it.
func TestSsrfDialContext_ZonedLinkLocalLiteralBlocked(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := ssrfDialContext(ctx, "tcp", "["+zonedLinkLocal+"]:80")
	if conn != nil {
		_ = conn.Close()
		t.Fatalf("conn returned for blocked %s, want nil", zonedLinkLocal)
	}
	want := "mcp: blocked: " + zonedLinkLocal + " resolves to fe80::1 (private/link-local address). Loopback (127.0.0.1, ::1) is allowed for local dev"
	if err == nil || err.Error() != want {
		t.Fatalf("error = %v, want %q", err, want)
	}
}

// The resolver path must accept an answer, validate it, and dial it. Every
// early bail in that chain (negated guards, off-by-one on the answer count)
// turns this successful dial into an error.
func TestSsrfDialContext_ZonedLoopbackLiteralDials(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := ssrfDialContext(ctx, "tcp", fmt.Sprintf("[%s]:%d", zonedLoopback, port))
	if err != nil {
		t.Fatalf("ssrfDialContext([%s]:%d) = %v, want a successful dial", zonedLoopback, port, err)
	}
	if conn == nil {
		t.Fatal("conn = nil with nil error, want a live connection")
	}
	defer conn.Close()
	if _, werr := conn.Write([]byte("ping")); werr != nil {
		t.Fatalf("write on dialed conn: %v", werr)
	}
}

// The overflow guard is a growth bound, not a line-length policy: exactly the
// cap stays buffered, one over it flushes, and a newline past the cap still
// truncates at the cap.
func TestStderrLineLoggerOverflowBounds(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{"exactly-cap-stays-buffered", strings.Repeat("a", 4096), nil},
		{"one-over-cap-flushes-truncated", strings.Repeat("a", 4097), []string{strings.Repeat("a", 4096) + "…"}},
		{"newline-past-cap-truncates-line", strings.Repeat("a", 5000) + "\n", []string{strings.Repeat("a", 4096)}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			recs := withCapturedLogs(t, func() {
				l := &stderrLineLogger{server: "srv"}
				n, err := l.Write([]byte(tc.in))
				if err != nil || n != len(tc.in) {
					t.Fatalf("Write(%d bytes) = (%d, %v), want (%d, nil)", len(tc.in), n, err, len(tc.in))
				}
			})
			got := logsFor(recs, "mcp: server stderr")
			if len(got) != len(tc.want) {
				t.Fatalf("logged %d records, want %d: %v", len(got), len(tc.want), got)
			}
			for i, rec := range got {
				if rec["line"] != tc.want[i] {
					t.Errorf("line len = %d, want %d (content %q)", len(rec["line"]), len(tc.want[i]), first20(rec["line"]))
				}
				if rec["server"] != "srv" {
					t.Errorf("server attr = %q, want %q", rec["server"], "srv")
				}
			}
		})
	}
}

func first20(s string) string {
	if len(s) > 20 {
		return s[:20]
	}
	return s
}

// An empty env map must leave Command.Env nil (child inherits), and a single
// override must keep the whole parent snapshot alongside it.
func TestNewStdioTransportEnvSemantics(t *testing.T) {
	t.Run("empty-env-inherits", func(t *testing.T) {
		cmd := stdioCommand(t, &StdioConfig{Command: "true"})
		if cmd.Env != nil {
			t.Fatalf("Command.Env has %d entries, want nil so the child inherits the parent environment", len(cmd.Env))
		}
	})
	t.Run("single-override-keeps-parent", func(t *testing.T) {
		cmd := stdioCommand(t, &StdioConfig{Command: "true", Env: map[string]string{"GBOT_MCP_ENV_PROBE": "on"}})
		want := len(os.Environ()) + 1
		if len(cmd.Env) != want {
			t.Fatalf("Command.Env has %d entries, want %d (parent snapshot + 1 override)", len(cmd.Env), want)
		}
		if !envHas(cmd.Env, "GBOT_MCP_ENV_PROBE=on") {
			t.Error("Command.Env is missing the GBOT_MCP_ENV_PROBE=on override")
		}
	})
}

// Wiring the logger is not enough: the server name is what makes the crash
// line attributable when several servers are spawning.
func TestNewStdioTransportStderrLoggerCarriesServerName(t *testing.T) {
	cmd := stdioCommand(t, &StdioConfig{Command: "sh", Args: []string{"-c", "echo crash-reason >&2"}})
	recs := withCapturedLogs(t, func() {
		if err := cmd.Run(); err != nil {
			t.Fatalf("run: %v", err)
		}
	})
	got := logsFor(recs, "mcp: server stderr")
	if len(got) != 1 {
		t.Fatalf("logged %d records, want 1: %v", len(got), got)
	}
	if got[0]["server"] != "named-server" {
		t.Errorf("server attr = %q, want %q", got[0]["server"], "named-server")
	}
	if got[0]["line"] != "crash-reason" {
		t.Errorf("line attr = %q, want %q", got[0]["line"], "crash-reason")
	}
}

// Each conjunct of the CGNAT range check has to fail on its own: a 4-byte
// address whose second octet is in range but whose first is not, and the
// 16-byte form of an in-range address.
func TestIsSharedAddressSpaceV4Conjuncts(t *testing.T) {
	cases := []struct {
		ip       string
		ipv4Form bool
		want     bool
	}{
		{"10.64.0.1", true, false},
		{"100.63.255.255", true, false},
		{"100.128.0.0", true, false},
		// Load-bearing for the len(ip)==4 conjunct mutants: ipv4Form hands a
		// 16-byte form whose ip[0..1] still spell 100.64, so only the length
		// guard keeps it false — the CGNAT semantics are pinned by the rows above.
		{"100.64.0.0", false, false},
		// Leading bytes spell 100.64 while the address is a real IPv6 one:
		// only the length guard keeps it out of the CGNAT range.
		{"6440::1", false, false},
		{"100.100.100.200", true, true},
	}
	for _, tc := range cases {
		t.Run(fmt.Sprintf("%s/ipv4form=%v", tc.ip, tc.ipv4Form), func(t *testing.T) {
			ip := net.ParseIP(tc.ip)
			if ip == nil {
				t.Fatalf("failed to parse %q", tc.ip)
			}
			if tc.ipv4Form {
				ip = ip.To4()
				if ip == nil {
					t.Fatalf("%q has no IPv4 form", tc.ip)
				}
			}
			if got := isSharedAddressSpaceV4(ip); got != tc.want {
				t.Errorf("isSharedAddressSpaceV4(%s, %d bytes) = %v, want %v", tc.ip, len(ip), got, tc.want)
			}
		})
	}
}

// The "this network" block is the first octet only: a zero in any later octet
// must not drag a public address into 0.0.0.0/8.
func TestIsBlockedAddressZeroNetworkUsesFirstOctet(t *testing.T) {
	allowed := []string{"8.0.0.1", "1.0.0.1", "100.0.0.1"}
	for _, ip := range allowed {
		parsed := net.ParseIP(ip)
		if parsed == nil {
			t.Fatalf("failed to parse %q", ip)
		}
		if isBlockedAddress(parsed) {
			t.Errorf("isBlockedAddress(%s) = true, want false", ip)
		}
	}
	blocked := []string{"0.0.0.0", "0.1.2.3"}
	for _, ip := range blocked {
		parsed := net.ParseIP(ip)
		if parsed == nil {
			t.Fatalf("failed to parse %q", ip)
		}
		if !isBlockedAddress(parsed) {
			t.Errorf("isBlockedAddress(%s) = false, want true", ip)
		}
	}
}

// url.Parse failures must stay unwrappable all the way out of the transport
// factories, so callers can tell a malformed URL from a blocked one.
func TestTransportErrorsKeepURLErrorWrap(t *testing.T) {
	const badURL = "http://[::1"
	inner := validateRemoteURL(badURL)
	var parsedErr *url.Error
	if !errors.As(inner, &parsedErr) {
		t.Fatalf("validateRemoteURL(%q) = %v (%T), want a wrapped *url.Error", badURL, inner, inner)
	}
	if parsedErr.URL != badURL {
		t.Errorf("url.Error.URL = %q, want %q", parsedErr.URL, badURL)
	}

	servers := []struct {
		name string
		run  func() error
	}{
		{"sse-srv", func() error { _, err := newSSETransport("sse-srv", &SSEConfig{URL: badURL}); return err }},
		{"ide-srv", func() error {
			_, err := newSSEIDETransport("ide-srv", &SSEIDEConfig{URL: badURL, IDEName: "vscode"})
			return err
		}},
		{"http-srv", func() error { _, err := newHTTPTransport("http-srv", &HTTPConfig{URL: badURL}); return err }},
	}
	for _, srv := range servers {
		t.Run(srv.name, func(t *testing.T) {
			err := srv.run()
			if _, ok := errors.AsType[*url.Error](err); !ok {
				t.Fatalf("error %v (%T) does not wrap the *url.Error from url.Parse", err, err)
			}
			want := fmt.Sprintf("mcp: server %q: %v", srv.name, inner)
			if err.Error() != want {
				t.Errorf("error = %q, want %q", err.Error(), want)
			}
		})
	}
}
