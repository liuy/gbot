// Package mcp implements the MCP (Model Context Protocol) client infrastructure.
// Source: src/services/mcp/ (22 files, ~12K lines TS)
//
// This file: connection management + memoization.
// Source: client.ts:1-1641 (connectToServer, memoize, auth cache, process cleanup)
package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	mcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// ---------------------------------------------------------------------------
// TransportProvider — interface for transport creation (testable)
// Source: client.ts:595 — memoize wraps the entire connectToServer function
// ---------------------------------------------------------------------------

// TransportProvider creates MCP transports. TransportFactory satisfies this interface.
type TransportProvider interface {
	NewTransport(name string, cfg McpServerConfig, scope ConfigScope, trusted bool) (mcp.Transport, error)
}

// Compile-time check that TransportFactory satisfies TransportProvider.
var _ TransportProvider = TransportFactory{}

// ---------------------------------------------------------------------------
// Cache key — Source: client.ts:581-586 getServerCacheKey
// ---------------------------------------------------------------------------

// GetServerCacheKey generates a stable cache key for a server connection.
// Source: client.ts:581-586 — `${name}-${jsonStringify(serverRef)}`
func GetServerCacheKey(name string, serverRef ScopedMcpServerConfig) string {
	b, _ := json.Marshal(serverRef)
	return name + "-" + string(b)
}

// ---------------------------------------------------------------------------
// cacheEntry — single-flight memoization
// Source: client.ts:595 — lodash memoize + getServerCacheKey resolver
//
// Prevents thundering herd: concurrent callers for the same key share one
// connection attempt via the done channel. defer+recover ensures the channel
// is always closed, even on panic.
// ---------------------------------------------------------------------------

type cacheEntry struct {
	done   chan struct{} // closed when connection attempt completes
	result ServerConnection
	err    error
}

// ---------------------------------------------------------------------------
// ClientManager — per-server MCP client instances
// Source: client.ts:595-1641 — module-level connectToServer (memoized)
//
// Manages connection lifecycle with:
//   - Single-flight memoization (cacheEntry)
//   - Auth cache (15-min TTL, skips reconnection for recently auth-failed servers)
//   - Process cleanup escalation for stdio servers (SIGINT→SIGTERM→SIGKILL)
// ---------------------------------------------------------------------------

// ClientManager manages per-server MCP client connections with memoization.
type ClientManager struct {
	mu       sync.Mutex
	cache    map[string]*cacheEntry
	provider TransportProvider
	trusted  bool
	auth     authCacheStore
}

// NewClientManager creates a new connection manager.
// configDir enables file-backed auth cache persistence when non-empty.
// Source: client.ts:261-263 — getClaudeConfigHomeDir()/mcp-needs-auth-cache.json
func NewClientManager(provider TransportProvider, trusted bool, configDir string) *ClientManager {
	cm := &ClientManager{
		cache:    make(map[string]*cacheEntry),
		provider: provider,
		trusted:  trusted,
	}
	if configDir != "" {
		cm.auth.filePath = filepath.Join(configDir, "mcp-needs-auth-cache.json")
		cm.auth.loadFromFile()
	}
	return cm
}

// ConnectToServer connects to an MCP server with memoized single-flight.
// Source: client.ts:595-1641 — connectToServer (memoized via lodash)
//
// If a connection is already in progress for the same key, callers block on
// the done channel and receive the same result — no duplicate connections.
// On failure, the cache entry is removed so callers can retry.
func (cm *ClientManager) ConnectToServer(ctx context.Context, name string, serverRef ScopedMcpServerConfig) (result ServerConnection, err error) {
	key := GetServerCacheKey(name, serverRef)

	cm.mu.Lock()
	if entry, ok := cm.cache[key]; ok {
		cm.mu.Unlock()
		<-entry.done // wait for in-flight connection
		if entry.err != nil {
			return nil, entry.err
		}
		return entry.result, nil
	}

	entry := &cacheEntry{done: make(chan struct{})}
	cm.cache[key] = entry
	cm.mu.Unlock()

	// defer+recover prevents thundering herd on panic — the done channel
	// is always closed, so all waiters unblock regardless of outcome.
	// Named return values let us set the error on panic before returning.
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("mcp: panic connecting to %q: %v", name, r)
			cm.mu.Lock()
			delete(cm.cache, key)
			cm.mu.Unlock()
			entry.err = err
		}
		close(entry.done)
	}()

	result, err = cm.connectInner(ctx, name, serverRef)
	if err != nil {
		// Remove from cache on failure so retry is possible
		cm.mu.Lock()
		delete(cm.cache, key)
		cm.mu.Unlock()
		entry.err = err
		return nil, err
	}

	entry.result = result
	return result, nil
}

// connectInner performs the actual connection attempt.
// Source: client.ts:608-1641 — body of the memoized connectToServer
func (cm *ClientManager) connectInner(ctx context.Context, name string, serverRef ScopedMcpServerConfig) (ServerConnection, error) {
	// Source: client.ts:280-287 — check auth cache, skip connection if recently failed auth
	if cm.auth.isCached(name) {
		return &NeedsAuthServer{Name: name, Config: serverRef}, nil
	}

	// Create transport via provider (includes trust gate for stdio)
	transport, err := cm.provider.NewTransport(name, serverRef.Config, serverRef.Scope, cm.trusted)
	if err != nil {
		return &FailedServer{Name: name, Config: serverRef, Error: err.Error()}, nil
	}

	// Source: client.ts:985-1002 — create SDK Client
	client := mcp.NewClient(
		&mcp.Implementation{
			Name:    "gbot",
			Title:   "gbot",
			Version: "0.1.0",
		},
		nil, // default capabilities
	)

	// Source: client.ts:1020-1077 — connect with timeout
	timeout := getConnectionTimeout()
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		return &FailedServer{Name: name, Config: serverRef, Error: err.Error()}, nil
	}

	// Source: client.ts:1157-1171 — extract connection info
	initResult := session.InitializeResult()

	var capabilities *mcp.ServerCapabilities
	var instructions string
	var serverInfo *ServerInfo

	if initResult != nil {
		capabilities = initResult.Capabilities
		instructions = initResult.Instructions
		if initResult.ServerInfo != nil {
			serverInfo = &ServerInfo{
				Name:    initResult.ServerInfo.Name,
				Version: initResult.ServerInfo.Version,
			}
		}
	}

	// Source: client.ts:1161-1171 — truncate instructions if too long
	if len(instructions) > MaxMCPDescriptionLength {
		instructions = instructions[:MaxMCPDescriptionLength] + "… [truncated]"
	}

	// Source: client.ts:1404-1581 — cleanup function with process escalation for stdio
	var cmd *exec.Cmd
	if serverRef.Config.GetTransport() == TransportStdio {
		if ct, ok := transport.(*mcp.CommandTransport); ok {
			cmd = ct.Command
		}
	}

	cleanup := func() error {
		// Source: client.ts:1426-1557 — process cleanup escalation for stdio
		if cmd != nil && cmd.Process != nil {
			processCleanupEscalation(cmd.Process)
		}
		return session.Close()
	}

	return &ConnectedServer{
		Name:         name,
		Config:       serverRef,
		Session:      session,
		Capabilities: capabilities,
		ServerInfo:   serverInfo,
		Instructions: instructions,
		Cleanup:      cleanup,
	}, nil
}

// InvalidateCache removes the cached connection for a server.
// Source: client.ts:1648-1673 — clearServerCache
func (cm *ClientManager) InvalidateCache(name string, serverRef ScopedMcpServerConfig) {
	key := GetServerCacheKey(name, serverRef)
	cm.mu.Lock()
	defer cm.mu.Unlock()
	delete(cm.cache, key)
}

// ClearAllCache removes all cached connections.
func (cm *ClientManager) ClearAllCache() {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	cm.cache = make(map[string]*cacheEntry)
}

// SetAuthCached marks a server as needing auth (cached for 15 minutes).
// Source: client.ts:293-309 — setMcpAuthCacheEntry
func (cm *ClientManager) SetAuthCached(serverName string) {
	cm.auth.set(serverName)
}

// EnsureConnected returns a connected server, reconnecting if needed.
// Source: client.ts:1688-1704 — ensureConnectedClient
func (cm *ClientManager) EnsureConnected(ctx context.Context, conn *ConnectedServer) (*ConnectedServer, error) {
	// Source: client.ts:1692-1694 — SDK servers don't go through connectToServer
	if conn.Config.Config.GetTransport() == TransportSDK {
		return conn, nil
	}

	result, err := cm.ConnectToServer(ctx, conn.Name, conn.Config)
	if err != nil {
		return nil, fmt.Errorf("mcp: server %q is not connected: %w", conn.Name, err)
	}

	connected, ok := result.(*ConnectedServer)
	if !ok {
		return nil, fmt.Errorf("mcp: server %q is not connected (state: %s)", conn.Name, result.ConnType())
	}
	return connected, nil
}

// ---------------------------------------------------------------------------
// Session expiry detection — Source: client.ts:193-206
// ---------------------------------------------------------------------------

// IsMcpSessionExpiredError detects MCP "Session not found" errors.
// Source: client.ts:193-206 — isMcpSessionExpiredError
//
// MCP servers return HTTP 404 with JSON-RPC error code -32001 when a session
// ID is no longer valid. We check both signals to avoid false positives from
// generic 404s (wrong URL, server gone).
func IsMcpSessionExpiredError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	// Source: client.ts:196 — HTTP status 404 check
	has404 := strings.Contains(msg, "404") || strings.Contains(msg, "Not Found")
	// Source: client.ts:200-205 — JSON-RPC error code -32001
	hasSessionCode := strings.Contains(msg, `"code":-32001`) || strings.Contains(msg, `"code": -32001`)
	return has404 && hasSessionCode
}

// ---------------------------------------------------------------------------
// Config comparison — Source: client.ts:1710-1722 areMcpConfigsEqual
// ---------------------------------------------------------------------------

// AreMcpConfigsEqual compares two server configs by serialization, excluding scope.
// Source: client.ts:1710-1722 — compares by JSON string, excluding scope metadata
func AreMcpConfigsEqual(a, b ScopedMcpServerConfig) bool {
	// Source: client.ts:1715 — quick type check first
	if a.Config.GetTransport() != b.Config.GetTransport() {
		return false
	}
	// Source: client.ts:1717-1721 — compare by serialization, excluding scope
	aCopy := ScopedMcpServerConfig{Config: a.Config, PluginSource: a.PluginSource}
	bCopy := ScopedMcpServerConfig{Config: b.Config, PluginSource: b.PluginSource}
	aBytes, _ := json.Marshal(aCopy)
	bBytes, _ := json.Marshal(bCopy)
	return string(aBytes) == string(bBytes)
}

// ---------------------------------------------------------------------------
// Auth cache — Source: client.ts:257-316
//
// In-memory cache tracking servers that recently needed authentication.
// Prevents repeated connection attempts to servers that require auth.
// TTL: 15 minutes (Source: client.ts:257 MCP_AUTH_CACHE_TTL_MS).
// ---------------------------------------------------------------------------

const authCacheTTL = 15 * time.Minute

type authCacheEntry struct {
	timestamp time.Time
}

// authCacheJSONEntry is the on-disk format matching TS: { timestamp: number }
type authCacheJSONEntry struct {
	Timestamp int64 `json:"timestamp"`
}

type authCacheStore struct {
	mu       sync.Mutex
	entries  map[string]authCacheEntry
	filePath string // if set, persists to this file
}

func (s *authCacheStore) isCached(serverID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.entries == nil {
		return false
	}
	entry, ok := s.entries[serverID]
	if !ok {
		return false
	}
	return time.Since(entry.timestamp) < authCacheTTL
}

func (s *authCacheStore) set(serverID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.entries == nil {
		s.entries = make(map[string]authCacheEntry)
	}
	s.entries[serverID] = authCacheEntry{timestamp: time.Now()}
	if s.filePath != "" {
		s.writeToFileLocked()
	}
}

func (s *authCacheStore) clear(serverID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.entries != nil {
		delete(s.entries, serverID)
	}
	if s.filePath != "" {
		s.writeToFileLocked()
	}
}

// clearAll removes all entries and deletes the cache file.
// Source: client.ts:311-316 — clearMcpAuthCache (exported, deletes file)
func (s *authCacheStore) clearAll() {
	s.mu.Lock()
	s.entries = nil
	path := s.filePath
	s.mu.Unlock()
	if path != "" {
		_ = os.Remove(path)
	}
}

// loadFromFile reads the cache from disk. Called once at construction.
// Source: client.ts:265-278 — getMcpAuthCache (lazy load)
func (s *authCacheStore) loadFromFile() {
	if s.filePath == "" {
		return
	}
	data, err := os.ReadFile(s.filePath)
	if err != nil {
		return // file does not exist — start empty
	}
	var raw map[string]authCacheJSONEntry
	if json.Unmarshal(data, &raw) != nil {
		return // corrupt file — start empty
	}
	s.entries = make(map[string]authCacheEntry, len(raw))
	for k, v := range raw {
		s.entries[k] = authCacheEntry{
			timestamp: time.UnixMilli(v.Timestamp),
		}
	}
}

// writeToFileLocked persists the cache to disk.
// Must be called with s.mu held.
// Source: client.ts:289-309 — setMcpAuthCacheEntry (serialized write)
func (s *authCacheStore) writeToFileLocked() {
	raw := make(map[string]authCacheJSONEntry, len(s.entries))
	for k, v := range s.entries {
		raw[k] = authCacheJSONEntry{Timestamp: v.timestamp.UnixMilli()}
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return
	}
	_ = os.MkdirAll(filepath.Dir(s.filePath), 0700)
	_ = os.WriteFile(s.filePath, data, 0600)
}

// ---------------------------------------------------------------------------
// Connection timeout — Source: client.ts:456-458 getConnectionTimeoutMs
// ---------------------------------------------------------------------------

// getConnectionTimeout returns the connection timeout from MCP_TIMEOUT env or default.
// Source: client.ts:456-458 — parseInt(process.env.MCP_TIMEOUT) || 30000
func getConnectionTimeout() time.Duration {
	if v := os.Getenv("MCP_TIMEOUT"); v != "" {
		if ms, err := strconv.Atoi(v); err == nil && ms > 0 {
			return time.Duration(ms) * time.Millisecond
		}
	}
	return time.Duration(DefaultConnectionTimeoutMs) * time.Millisecond
}

// ---------------------------------------------------------------------------
// Process cleanup escalation — Source: client.ts:1426-1557
//
// For stdio servers: after closing the transport, ensure the child process
// terminates. Some MCP servers (especially Docker containers) ignore the
// SDK's close signal, so we escalate:
//   1. SIGINT (Ctrl+C — graceful)
//   2. SIGTERM (after 100ms — force graceful)
//   3. SIGKILL (after 400ms — uncatchable)
//
// Total max: ~500ms. Source: client.ts:1445 — "rapid escalation to keep CLI responsive"
// ---------------------------------------------------------------------------

// ProcessCleanupEscalation ensures a process terminates via signal escalation.
// Exported for testing. Source: client.ts:1428-1557
func ProcessCleanupEscalation(proc *os.Process) {
	if proc == nil {
		return
	}
	pid := proc.Pid
	if !processExists(pid) {
		return
	}

	// Step 1: SIGINT — Source: client.ts:1438-1439
	_ = syscall.Kill(pid, syscall.SIGINT)
	if waitProcessGone(pid, 100*time.Millisecond) {
		return
	}

	// Step 2: SIGTERM — Source: client.ts:1492-1493
	_ = syscall.Kill(pid, syscall.SIGTERM)
	if waitProcessGone(pid, 400*time.Millisecond) {
		return
	}

	// Step 3: SIGKILL — Source: client.ts:1523-1524
	_ = syscall.Kill(pid, syscall.SIGKILL)
}

// processCleanupEscalation is the unexported alias used by cleanup functions.
func processCleanupEscalation(proc *os.Process) {
	ProcessCleanupEscalation(proc)
}

// processExists checks if a process with the given PID is still running.
// Uses syscall.Kill(pid, 0) which checks existence without sending a signal.
// Source: client.ts:1453 — process.kill(pid, 0)
func processExists(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}

// waitProcessGone polls until the process exits or timeout elapses.
// Returns true if the process is gone within the timeout.
func waitProcessGone(pid int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if !processExists(pid) {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return !processExists(pid)
}

// ---------------------------------------------------------------------------
// Compile-time checks
// ---------------------------------------------------------------------------

var _ TransportProvider = TransportFactory{}
