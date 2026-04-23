// Package mcp implements the MCP (Model Context Protocol) client infrastructure.
// Source: src/services/mcp/ (22 files, ~12K lines TS)
//
// This file: transport factory + SSRF prevention.
// Source: client.ts:620-960 (transport creation), utils/hooks/ssrfGuard.ts (SSRF guard)
package mcp

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"time"

	mcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// ---------------------------------------------------------------------------
// SSRF Prevention — Source: utils/hooks/ssrfGuard.ts
//
// Blocks private, link-local, and other non-routable address ranges to prevent
// MCP servers from reaching cloud metadata endpoints (169.254.169.254) or
// internal infrastructure.
//
// Loopback (127.0.0.0/8, ::1) is intentionally ALLOWED — local dev policy
// servers are a primary MCP use case.
// ---------------------------------------------------------------------------

// isBlockedAddress returns true if the IP is in a range that MCP HTTP
// connections should not reach.
//
// Source: ssrfGuard.ts:42-53 isBlockedAddress
func isBlockedAddress(ip net.IP) bool {
	// Try IPv4 first (handles both native IPv4 and IPv4-mapped IPv6).
	// Go's To4() extracts the IPv4 portion from IPv4-mapped IPv6 addresses
	// like ::ffff:a.b.c.d, which prevents hex-form mapped address bypass
	// (e.g. ::ffff:a9fe:a9fe = 169.254.169.254).
	if v4 := ip.To4(); v4 != nil {
		return isBlockedV4(v4)
	}
	return isBlockedV6(ip)
}

// isBlockedV4 checks if an IPv4 address is in a blocked range.
// Source: ssrfGuard.ts:55-86 isBlockedV4
//
// Blocked:
//   - 0.0.0.0/8        "this" network
//   - 10.0.0.0/8       private (RFC 1918)
//   - 100.64.0.0/10    shared address space / CGNAT (RFC 6598)
//   - 169.254.0.0/16   link-local, cloud metadata
//   - 172.16.0.0/12    private (RFC 1918)
//   - 192.168.0.0/16   private (RFC 1918)
//
// Allowed: 127.0.0.0/8 (loopback — local dev MCP servers)
func isBlockedV4(ip net.IP) bool {
	// Source: ssrfGuard.ts:68 — loopback explicitly allowed
	if ip.IsLoopback() {
		return false
	}
	// Source: ssrfGuard.ts:71 — 0.0.0.0/8 ("this" network)
	// Go's IsUnspecified() only checks 0.0.0.0 exactly; TS blocks the whole /8.
	if ip[0] == 0 {
		return true
	}
	// Source: ssrfGuard.ts:73 — 10.0.0.0/8
	// Source: ssrfGuard.ts:77 — 172.16.0.0/12
	// Source: ssrfGuard.ts:83 — 192.168.0.0/16
	if ip.IsPrivate() {
		return true
	}
	// Source: ssrfGuard.ts:75 — 169.254.0.0/16, cloud metadata
	if ip.IsLinkLocalUnicast() {
		return true
	}
	// Source: ssrfGuard.ts:79-81 — 100.64.0.0/10 shared address space (CGNAT).
	// Not covered by Go's IsPrivate(). Some cloud providers use this range
	// for metadata endpoints (e.g. Alibaba Cloud at 100.100.100.200).
	if isSharedAddressSpaceV4(ip) {
		return true
	}
	return false
}

// isBlockedV6 checks if an IPv6 address is in a blocked range.
// Source: ssrfGuard.ts:88-125 isBlockedV6
//
// Blocked:
//   - ::                unspecified
//   - fc00::/7          unique local
//   - fe80::/10         link-local
//
// Allowed: ::1 (loopback)
// IPv4-mapped addresses are handled by isBlockedAddress via To4() before
// this function is called.
func isBlockedV6(ip net.IP) bool {
	// Source: ssrfGuard.ts:92 — ::1 loopback explicitly allowed
	if ip.IsLoopback() {
		return false
	}
	// Source: ssrfGuard.ts:95 — :: unspecified
	if ip.IsUnspecified() {
		return true
	}
	// Source: ssrfGuard.ts:107-109 — fc00::/7 unique local
	if ip.IsPrivate() {
		return true
	}
	// Source: ssrfGuard.ts:111-123 — fe80::/10 link-local
	if ip.IsLinkLocalUnicast() {
		return true
	}
	return false
}

// isSharedAddressSpaceV4 checks if ip is in 100.64.0.0/10 (RFC 6598 CGNAT).
// Source: ssrfGuard.ts:79-81
func isSharedAddressSpaceV4(ip net.IP) bool {
	return len(ip) == 4 && ip[0] == 100 && ip[1] >= 64 && ip[1] <= 127
}

// ssrfDialContext is a custom DialContext that validates resolved IPs before
// connecting, preventing DNS rebinding attacks.
//
// Source: ssrfGuard.ts:216-283 ssrfGuardedLookup
//
// IP literals in the hostname are validated directly without DNS.
// For hostnames, DNS resolves all addresses first, checks every one against
// the blocklist, and only then dials the first allowed address — no rebinding
// window between validation and connection.
func ssrfDialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("mcp: invalid address %q: %w", addr, err)
	}

	// Source: ssrfGuard.ts:231-243 — IP literal short-circuit
	if ip := net.ParseIP(host); ip != nil {
		if isBlockedAddress(ip) {
			return nil, ssrfError(host, ip.String())
		}
		dialer := net.Dialer{}
		return dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
	}

	// Source: ssrfGuard.ts:245-283 — DNS lookup + validate all results
	resolver := net.Resolver{}
	ips, err := resolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, fmt.Errorf("mcp: DNS lookup failed for %q: %w", host, err)
	}

	if len(ips) == 0 {
		return nil, fmt.Errorf("mcp: ENOTFOUND %s", host)
	}

	for _, ipAddr := range ips {
		if isBlockedAddress(ipAddr.IP) {
			return nil, ssrfError(host, ipAddr.IP.String())
		}
	}

	// Dial the first allowed address
	dialer := net.Dialer{}
	return dialer.DialContext(ctx, network, net.JoinHostPort(ips[0].IP.String(), port))
}

// ssrfError creates an error for blocked addresses.
// Source: ssrfGuard.ts:285-294 ssrfError
func ssrfError(hostname, address string) error {
	return fmt.Errorf(
		"mcp: blocked: %s resolves to %s (private/link-local address). Loopback (127.0.0.1, ::1) is allowed for local dev",
		hostname, address,
	)
}

// ssrfHTTPClient returns an *http.Client with SSRF protection via a custom
// DialContext that blocks private/link-local addresses at connection time.
// Source: client.ts:492-551 — wrapFetchWithTimeout adds per-request timeout.
// Context propagation covers tool call timeouts, but notifications and close
// requests lack a caller deadline. This Timeout field is a safety net.
func ssrfHTTPClient() *http.Client {
	return &http.Client{
		Timeout: 60 * time.Second,
		Transport: &http.Transport{
			DialContext: ssrfDialContext,
		},
	}
}

// ---------------------------------------------------------------------------
// URL Validation
// ---------------------------------------------------------------------------

// validateRemoteURL validates a URL for MCP remote connections.
// Source: client.ts transport creation — URL checked before creating transport.
//
// Checks:
//  1. URL is parseable
//  2. Scheme is http or https
//  3. If host is an IP literal, it's not in a blocked range
//
// DNS-level SSRF protection is applied at connection time via ssrfHTTPClient.
func validateRemoteURL(rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("mcp: invalid URL %q: %w", rawURL, err)
	}

	// Scheme allowlist — SSE/HTTP use http/https, WS uses ws/wss
	scheme := strings.ToLower(u.Scheme)
	switch scheme {
	case "http", "https", "ws", "wss":
		// ok
	default:
		return fmt.Errorf("mcp: URL scheme must be http, https, ws, or wss, got %q", u.Scheme)
	}

	if u.Host == "" {
		return fmt.Errorf("mcp: URL %q has no host", rawURL)
	}

	// Check IP literal in host
	host := u.Hostname()
	if ip := net.ParseIP(host); ip != nil {
		if isBlockedAddress(ip) {
			return ssrfError(host, host)
		}
	}

	return nil
}

// ---------------------------------------------------------------------------
// Trust Gate
// ---------------------------------------------------------------------------

// isTrustRequired returns true if the given scope requires workspace trust
// for stdio server execution.
//
// Project and local scope configs come from .mcp.json files in the project
// directory and must be explicitly trusted before spawning subprocesses.
// User/enterprise scope configs are always trusted — the user configured them.
func isTrustRequired(scope ConfigScope) bool {
	return scope == ScopeProject || scope == ScopeLocal
}

// ---------------------------------------------------------------------------
// Transport Factory — Source: client.ts:620-960 transport creation
//
// TransportFactory creates MCP SDK transports from MCP server configs.
// Each config type maps to the appropriate SDK transport with SSRF protection
// for remote URLs and trust gates for local subprocess execution.
// ---------------------------------------------------------------------------

// TransportFactory creates MCP SDK transports from MCP server configs.
type TransportFactory struct{}

// NewTransport creates an MCP transport for the given server config.
//
// Source: client.ts:620-960 — transport creation per config type.
//
// Parameters:
//   - name: server name (for error messages)
//   - cfg: server configuration (StdioConfig, SSEConfig, HTTPConfig, etc.)
//   - scope: configuration scope (affects trust requirements for stdio)
//   - trusted: whether the workspace is trusted (affects stdio gate)
func (TransportFactory) NewTransport(name string, cfg McpServerConfig, scope ConfigScope, trusted bool) (mcp.Transport, error) {
	switch c := cfg.(type) {
	case *StdioConfig:
		return newStdioTransport(name, c, scope, trusted)
	case *SSEConfig:
		return newSSETransport(name, c)
	case *SSEIDEConfig:
		return newSSEIDETransport(name, c)
	case *HTTPConfig:
		return newHTTPTransport(name, c)
	case *WSConfig:
		return newWSTransport(name, c)
	case *WSIDEConfig:
		return newWSIDETransport(name, c)
	case *SDKConfig:
		return nil, fmt.Errorf("mcp: SDK servers should be handled separately")
	case *ClaudeAIProxyConfig:
		return nil, fmt.Errorf("mcp: claude.ai proxy transport not yet implemented")
	default:
		return nil, fmt.Errorf("mcp: unsupported server config type for %q", name)
	}
}

// newStdioTransport creates a CommandTransport for stdio servers.
// Source: client.ts:944-958
//
// Trust gate: project/local scope requires workspace trust before spawning.
func newStdioTransport(name string, cfg *StdioConfig, scope ConfigScope, trusted bool) (mcp.Transport, error) {
	// Trust gate: project/local scope requires workspace trust
	if isTrustRequired(scope) && !trusted {
		return nil, fmt.Errorf(
			"mcp: server %q requires workspace trust for scope %q",
			name, scope,
		)
	}

	cmd := exec.Command(cfg.Command, cfg.Args...)

	// Source: client.ts:953-956 — env is subprocessEnv() merged with server env.
	// In Go, exec.Command inherits os.Environ by default. We only need to
	// add/override with server-specific vars.
	if len(cfg.Env) > 0 {
		cmd.Env = os.Environ()
		for k, v := range cfg.Env {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
	}

	return &mcp.CommandTransport{Command: cmd}, nil
}

// newSSETransport creates an SSEClientTransport for SSE servers.
// Source: client.ts:620-677
func newSSETransport(name string, cfg *SSEConfig) (mcp.Transport, error) {
	if err := validateRemoteURL(cfg.URL); err != nil {
		return nil, fmt.Errorf("mcp: server %q: %w", name, err)
	}

	return &mcp.SSEClientTransport{
		Endpoint:   cfg.URL,
		HTTPClient: ssrfHTTPClient(),
	}, nil
}

// newSSEIDETransport creates an SSEClientTransport for SSE-IDE servers.
// Source: client.ts:678-707
func newSSEIDETransport(name string, cfg *SSEIDEConfig) (mcp.Transport, error) {
	if err := validateRemoteURL(cfg.URL); err != nil {
		return nil, fmt.Errorf("mcp: server %q: %w", name, err)
	}

	return &mcp.SSEClientTransport{
		Endpoint:   cfg.URL,
		HTTPClient: ssrfHTTPClient(),
	}, nil
}

// newHTTPTransport creates a StreamableClientTransport for HTTP servers.
// Source: client.ts:784-865
func newHTTPTransport(name string, cfg *HTTPConfig) (mcp.Transport, error) {
	if err := validateRemoteURL(cfg.URL); err != nil {
		return nil, fmt.Errorf("mcp: server %q: %w", name, err)
	}

	return &mcp.StreamableClientTransport{
		Endpoint:   cfg.URL,
		HTTPClient: ssrfHTTPClient(),
	}, nil
}

