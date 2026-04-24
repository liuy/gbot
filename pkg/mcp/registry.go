// Package mcp implements the MCP (Model Context Protocol) client infrastructure.
// Source: src/services/mcp/ (22 files, ~12K lines TS)
//
// This file: useManageMCPConnections.ts (1142 lines) + client.ts batch/reconnect —
// server registry with lifecycle management, two-phase shutdown, and reconnect.
package mcp

import (
	"context"
	"fmt"
	"maps"
	"math/rand"
	"os"
	"strconv"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// Reconnection constants — Source: useManageMCPConnections.ts:16-18
// ---------------------------------------------------------------------------

const (
	// maxReconnectAttempts is the maximum number of automatic reconnection attempts.
	// Source: useManageMCPConnections.ts:16 — MAX_RECONNECT_ATTEMPTS
	maxReconnectAttempts = MaxReconnectAttempts

	// initialBackoff is the initial backoff duration for reconnection.
	// Source: useManageMCPConnections.ts:17 — INITIAL_BACKOFF_MS
	initialBackoff = time.Duration(InitialBackoffMs) * time.Millisecond

	// maxBackoff is the maximum backoff duration for reconnection.
	// Source: useManageMCPConnections.ts:18 — MAX_BACKOFF_MS
	maxBackoff = time.Duration(MaxBackoffMs) * time.Millisecond
)

var (
	// shutdownGracePeriod is the time to wait for graceful shutdown before forcing.
	shutdownGracePeriod = 5 * time.Second

	// reconnectMinBackoff is the minimum backoff for ScheduleReconnect (tests can override).
	reconnectMinBackoff = initialBackoff
)

// ---------------------------------------------------------------------------
// Batch size constants — Source: client.ts:552-561
// ---------------------------------------------------------------------------

const (
	// localBatchDefault is the default concurrency for local (stdio/sdk) servers.
	// Source: client.ts:552-554 — MCP_SERVER_CONNECTION_BATCH_SIZE
	localBatchDefault = 3

	// remoteBatchDefault is the default concurrency for remote (sse/http/ws) servers.
	// Source: client.ts:556-561 — MCP_REMOTE_SERVER_CONNECTION_BATCH_SIZE
	remoteBatchDefault = 20
)

func getLocalBatchSize() int {
	if v := os.Getenv("MCP_SERVER_CONNECTION_BATCH_SIZE"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return localBatchDefault
}

func getRemoteBatchSize() int {
	if v := os.Getenv("MCP_REMOTE_SERVER_CONNECTION_BATCH_SIZE"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return remoteBatchDefault
}

// ---------------------------------------------------------------------------
// Change callbacks — Source: useManageMCPConnections.ts:618-752
// ---------------------------------------------------------------------------

// ChangeCallbacks holds optional callbacks invoked when server data changes.
// Source: useManageMCPConnections.ts — updateServer / flushPendingUpdates
type ChangeCallbacks struct {
	// OnToolsChanged is called when a server's tool list changes (list_changed or reconnect).
	OnToolsChanged func(serverName string, tools []DiscoveredTool)
	// OnResourcesChanged is called when a server's resource list changes.
	OnResourcesChanged func(serverName string, resources []ServerResource)
	// OnCommandsChanged is called when a server's command list changes.
	OnCommandsChanged func(serverName string, commands []MCPCommand)
	// OnServerStatusChanged is called when a server's connection status changes.
	OnServerStatusChanged func(serverName string, conn ServerConnection)
}

// ---------------------------------------------------------------------------
// Registry — Source: useManageMCPConnections.ts (orchestration hook)
//
// The Registry manages the lifecycle of all MCP server connections:
//   - Connecting to all configured servers (ConnectAll)
//   - Disconnecting individual servers (Disconnect)
//   - Toggling servers on/off (ToggleServer)
//   - Reconnecting with exponential backoff (Reconnect)
//   - Two-phase shutdown (Close)
//   - Providing discovered tools/commands/resources (GetTools/GetCommands/GetResources)
//
// Source mapping:
//   - ConnectAll      → useManageMCPConnections.ts:858-1024 + client.ts:2226-2403
//   - Reconnect       → useManageMCPConnections.ts:1046-1070 + client.ts:2137-2214
//   - Disconnect      → useManageMCPConnections.ts:810 + client.ts:1648-1673
//   - ToggleServer    → useManageMCPConnections.ts:1074-1107
//   - Close           → useManageMCPConnections.ts:1027-1041 + client.ts:1404-1581
//   - list_changed    → useManageMCPConnections.ts:617-751
// ---------------------------------------------------------------------------

// Registry manages a set of MCP server connections.
type Registry struct {
	mu sync.RWMutex

	// Dependencies
	manager *ClientManager

	// Server state
	configs     map[string]ScopedMcpServerConfig
	connections map[string]ServerConnection
	disabled    map[string]bool

	// Discovery caches
	toolCache     *LRUCache[string, []DiscoveredTool]
	resourceCache *LRUCache[string, []ServerResource]
	commandCache  *LRUCache[string, []MCPCommand]

	// Aggregated discovery results
	tools     []DiscoveredTool
	resources []ServerResource
	commands  []MCPCommand

	// Reconnect timers — Source: useManageMCPConnections.ts:28 reconnectTimersRef
	reconnectTimers map[string]*time.Timer

	// Shutdown
	closeOnce sync.Once
	closed    bool
	ctx       context.Context
	cancel    context.CancelFunc

	// Callbacks
	callbacks ChangeCallbacks
}

// NewRegistry creates a new MCP server registry.
func NewRegistry(manager *ClientManager, callbacks ChangeCallbacks) *Registry {
	ctx, cancel := context.WithCancel(context.Background())
	return &Registry{
		manager:         manager,
		configs:         make(map[string]ScopedMcpServerConfig),
		connections:     make(map[string]ServerConnection),
		disabled:        make(map[string]bool),
		toolCache:       NewLRUCache[string, []DiscoveredTool](fetchCacheCapacity),
		resourceCache:   NewLRUCache[string, []ServerResource](fetchCacheCapacity),
		commandCache:    NewLRUCache[string, []MCPCommand](fetchCacheCapacity),
		reconnectTimers: make(map[string]*time.Timer),
		ctx:             ctx,
		cancel:          cancel,
		callbacks:       callbacks,
	}
}

// ---------------------------------------------------------------------------
// ConnectAll — Source: useManageMCPConnections.ts:858-1024 + client.ts:2226-2403
// ---------------------------------------------------------------------------

// ConnectAll connects to all configured servers that are not disabled.
// Source: useManageMCPConnections.ts:858-1024 — effect connects to all servers
// Source: client.ts:2388-2402 — processBatched with local/remote concurrency limits
//
// Returns a map of server name → connection result for each server.
func (r *Registry) ConnectAll(ctx context.Context, configs map[string]ScopedMcpServerConfig) map[string]ServerConnection {
	r.mu.Lock()
	// Update configs
	maps.Copy(r.configs, configs)
	// Remove stale configs
	for name := range r.configs {
		if _, ok := configs[name]; !ok {
			delete(r.configs, name)
			delete(r.connections, name)
		}
	}
	r.mu.Unlock()

	results := make(map[string]ServerConnection, len(configs))
	var resultsMu sync.Mutex

	// serverEntry holds a name+config pair for batch classification.
	type serverEntry struct {
		name string
		cfg  ScopedMcpServerConfig
	}

	// Classify into local/remote groups; handle disabled inline.
	var local, remote []serverEntry
	for name, cfg := range configs {
		r.mu.RLock()
		disabled := r.disabled[name]
		r.mu.RUnlock()

		if disabled {
			results[name] = &DisabledServer{Name: name, Config: cfg}
			r.mu.Lock()
			r.connections[name] = results[name]
			r.mu.Unlock()
			continue
		}

		if IsLocalServer(cfg) {
			local = append(local, serverEntry{name, cfg})
		} else {
			remote = append(remote, serverEntry{name, cfg})
		}
	}

	// processGroup connects a batch of servers with sliding-window concurrency.
	// Source: client.ts:2212-2224 — processBatched (pMap-style concurrency)
	processGroup := func(entries []serverEntry, batchSize int) {
		sem := make(chan struct{}, batchSize)
		var wg sync.WaitGroup
		for _, e := range entries {
			wg.Add(1)
			sem <- struct{}{} // acquire slot
			go func(name string, cfg ScopedMcpServerConfig) {
				defer wg.Done()
				defer func() { <-sem }() // release slot

				conn, err := r.manager.ConnectToServer(ctx, name, cfg)

				var result ServerConnection
				if err != nil {
					result = &FailedServer{Name: name, Config: cfg, Error: err.Error()}
				} else {
					result = conn
				}

				resultsMu.Lock()
				results[name] = result
				r.mu.Lock()
				r.connections[name] = result
				r.mu.Unlock()
				resultsMu.Unlock()
			}(e.name, e.cfg)
		}
		wg.Wait()
	}

	// Run local and remote groups concurrently.
	// Source: client.ts:2388-2402 — Promise.all([processBatched(local), processBatched(remote)])
	var groupWg sync.WaitGroup
	groupWg.Add(2)
	go func() { defer groupWg.Done(); processGroup(local, getLocalBatchSize()) }()
	go func() { defer groupWg.Done(); processGroup(remote, getRemoteBatchSize()) }()
	groupWg.Wait()

	// Batch discovery for all connected servers
	var connected []*ConnectedServer
	for _, conn := range results {
		if cs, ok := conn.(*ConnectedServer); ok {
			connected = append(connected, cs)
		}
	}

	if len(connected) > 0 {
		discoveries := BatchDiscovery(ctx, connected, r.toolCache, r.resourceCache, r.commandCache)
		r.mu.Lock()
		for _, d := range discoveries {
			r.tools = append(r.tools, d.Tools...)
			r.resources = append(r.resources, d.Resources...)
			r.commands = append(r.commands, d.Commands...)
		}
		r.mu.Unlock()
	}

	return results
}

// ---------------------------------------------------------------------------
// Reconnect — Source: useManageMCPConnections.ts:1046-1070 + client.ts:2137-2214
// ---------------------------------------------------------------------------

// Reconnect reconnects a specific server by name with exponential backoff.
// Source: useManageMCPConnections.ts:1046-1070 — reconnectMcpServer
//
// Clears the connection cache, reconnects, and re-fetches tools/resources/commands.
func (r *Registry) Reconnect(ctx context.Context, serverName string) (ServerConnection, error) {
	r.mu.RLock()
	cfg, ok := r.configs[serverName]
	if !ok {
		r.mu.RUnlock()
		return nil, fmt.Errorf("mcp: server %q not found in registry", serverName)
	}
	r.mu.RUnlock()

	// Source: client.ts:2145-2151 — clearServerCache
	r.manager.InvalidateCache(serverName, cfg)

	// Clear caches
	r.toolCache.Delete(serverName)
	r.resourceCache.Delete(serverName)
	r.commandCache.Delete(serverName)

	// Reconnect
	conn, err := r.manager.ConnectToServer(ctx, serverName, cfg)
	if err != nil {
		r.mu.Lock()
		r.connections[serverName] = &FailedServer{Name: serverName, Config: cfg, Error: err.Error()}
		r.mu.Unlock()
		return nil, fmt.Errorf("mcp: reconnect %q failed: %w", serverName, err)
	}

	r.mu.Lock()
	r.connections[serverName] = conn
	r.mu.Unlock()

	// Re-fetch discovery data
	if cs, ok := conn.(*ConnectedServer); ok {
		tools, _ := FetchToolsForServer(ctx, cs, r.toolCache)
		resources, _ := FetchResourcesForServer(ctx, cs, r.resourceCache)
		commands, _ := FetchCommandsForServer(ctx, cs, r.commandCache)

		r.mu.Lock()
		r.rebuildAggregatesLocked()
		r.mu.Unlock()

		// Notify callbacks
		if r.callbacks.OnToolsChanged != nil {
			r.callbacks.OnToolsChanged(serverName, tools)
		}
		if r.callbacks.OnResourcesChanged != nil {
			r.callbacks.OnResourcesChanged(serverName, resources)
		}
		if r.callbacks.OnCommandsChanged != nil {
			r.callbacks.OnCommandsChanged(serverName, commands)
		}
	}

	return conn, nil
}

// ---------------------------------------------------------------------------
// Disconnect — Source: useManageMCPConnections.ts:810 + client.ts:1648-1673
// ---------------------------------------------------------------------------

// Disconnect closes the connection to a specific server.
// Source: client.ts:1648-1673 — clearServerCache
func (r *Registry) Disconnect(serverName string) error {
	r.mu.Lock()
	conn, ok := r.connections[serverName]
	if !ok {
		r.mu.Unlock()
		return nil
	}

	// Cancel any pending reconnect timer
	if timer, ok := r.reconnectTimers[serverName]; ok {
		timer.Stop()
		delete(r.reconnectTimers, serverName)
	}

	// Remove from connections
	delete(r.connections, serverName)
	r.toolCache.Delete(serverName)
	r.resourceCache.Delete(serverName)
	r.commandCache.Delete(serverName)
	r.rebuildAggregatesLocked()
	r.mu.Unlock()

	// Close the connection outside the lock
	if cs, ok := conn.(*ConnectedServer); ok {
		r.manager.InvalidateCache(serverName, cs.Config)
		return cs.Close()
	}

	return nil
}

// ---------------------------------------------------------------------------
// ToggleServer — Source: useManageMCPConnections.ts:1074-1107
// ---------------------------------------------------------------------------

// ToggleServer enables or disables a server.
// Source: useManageMCPConnections.ts:1074-1107 — toggleMcpServer
//
// When disabling, disconnects the server. When enabling, reconnects it.
func (r *Registry) ToggleServer(ctx context.Context, serverName string) error {
	r.mu.Lock()
	_, disabled := r.disabled[serverName]
	_, hasConfig := r.configs[serverName]
	r.mu.Unlock()

	if !hasConfig {
		return fmt.Errorf("mcp: server %q not found in registry", serverName)
	}

	if disabled {
		// Currently disabled → enable
		r.mu.Lock()
		delete(r.disabled, serverName)
		r.mu.Unlock()
		// Reconnect
		_, err := r.Reconnect(ctx, serverName)
		return err
	}

	// Currently enabled → disable
	r.mu.Lock()
	r.disabled[serverName] = true
	r.mu.Unlock()
	return r.Disconnect(serverName)
}

// ---------------------------------------------------------------------------
// GetTools / GetCommands / GetResources
// ---------------------------------------------------------------------------

// GetTools returns all discovered tools across all servers.
func (r *Registry) GetTools() []DiscoveredTool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return append([]DiscoveredTool(nil), r.tools...)
}

// GetCommands returns all discovered commands across all servers.
func (r *Registry) GetCommands() []MCPCommand {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return append([]MCPCommand(nil), r.commands...)
}

// GetResources returns all discovered resources across all servers.
func (r *Registry) GetResources() []ServerResource {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return append([]ServerResource(nil), r.resources...)
}

// GetConnection returns the connection state for a server.
func (r *Registry) GetConnection(serverName string) (ServerConnection, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	conn, ok := r.connections[serverName]
	return conn, ok
}

// GetConfigs returns all registered server configs.
func (r *Registry) GetConfigs() map[string]ScopedMcpServerConfig {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make(map[string]ScopedMcpServerConfig, len(r.configs))
	maps.Copy(result, r.configs)
	return result
}

// ---------------------------------------------------------------------------
// Close — Two-phase shutdown
// Source: useManageMCPConnections.ts:1027-1041 + client.ts:1404-1581
//
// Phase 1: Notify all servers to close gracefully (call Close on each)
// Phase 2: After shutdownGracePeriod (5s), force kill any remaining
//
// Uses sync.Once to ensure Close is idempotent.
// ---------------------------------------------------------------------------

// Close performs a two-phase shutdown of all MCP server connections.
// Source: useManageMCPConnections.ts:1027-1041 — cleanup effect
//
// Phase 1: Sends close signals to all connected servers in parallel.
// Phase 2: Waits up to shutdownGracePeriod (5s) for graceful shutdown,
// then forces remaining processes to terminate.
// Safe to call multiple times (sync.Once).
func (r *Registry) Close() error {
	var err error
	r.closeOnce.Do(func() {
		err = r.closeInner()
	})
	return err
}

func (r *Registry) closeInner() error {
	// Cancel the registry context to stop reconnection attempts
	r.cancel()

	r.mu.Lock()
	// Cancel all reconnect timers
	for name, timer := range r.reconnectTimers {
		timer.Stop()
		delete(r.reconnectTimers, name)
	}

	// Collect connected servers
	var connected []*ConnectedServer
	for name, conn := range r.connections {
		if cs, ok := conn.(*ConnectedServer); ok {
			connected = append(connected, cs)
		}
		delete(r.connections, name)
	}
	r.closed = true
	r.mu.Unlock()

	if len(connected) == 0 {
		return nil
	}

	// Phase 1: Notify all servers to close gracefully in parallel
	// Source: client.ts:1404-1426 — cleanup function with process escalation
	done := make(chan struct{}, len(connected))
	for _, cs := range connected {
		go func(s *ConnectedServer) {
			closeCh := make(chan error, 1)
			go func() { closeCh <- s.Close() }()
			select {
			case <-closeCh:
			case <-time.After(5 * time.Second):
				// Server Close() timed out, continue shutdown
			}
			done <- struct{}{}
		}(cs)
	}

	// Phase 2: Wait for graceful shutdown with timeout
	// Source: useManageMCPConnections.ts:1027-1041 — cleanup on unmount
	remaining := len(connected)
	timer := time.NewTimer(shutdownGracePeriod)
	defer timer.Stop()

	for remaining > 0 {
		select {
		case <-done:
			remaining--
		case <-timer.C:
			// Graceful period elapsed — remaining servers are force-killed
			// by their cleanup escalation (SIGINT→SIGTERM→SIGKILL)
			// The Close() on each ConnectedServer already handles escalation.
			return fmt.Errorf("mcp: registry shutdown: %d server(s) did not close within %v", remaining, shutdownGracePeriod)
		}
	}

	return nil
}

// ---------------------------------------------------------------------------
// Auto-reconnect — Source: useManageMCPConnections.ts:28, 834-842
//
// ScheduleReconnect sets up an automatic reconnection attempt with exponential
// backoff. Called when a remote server's connection drops unexpectedly.
// ---------------------------------------------------------------------------

// ScheduleReconnect schedules an automatic reconnection attempt with exponential backoff.
// Source: useManageMCPConnections.ts:834-842 — reconnect with backoff
//
// Only reconnects remote servers (SSE/HTTP/WS). Stdio servers are not auto-reconnected.
// The attempt counter resets on successful connection.
func (r *Registry) ScheduleReconnect(serverName string, attempt int) {
	r.mu.RLock()
	cfg, ok := r.configs[serverName]
	if !ok || r.closed {
		r.mu.RUnlock()
		return
	}
	// Only auto-reconnect remote servers
	if IsLocalServer(cfg) {
		r.mu.RUnlock()
		return
	}
	r.mu.RUnlock()

	if attempt >= maxReconnectAttempts {
		return
	}

	// Exponential backoff with jitter
	// Source: useManageMCPConnections.ts:834 — Math.min(INITIAL * 2^attempt, MAX)
	backoff := min(reconnectMinBackoff*time.Duration(1<<uint(attempt)), maxBackoff)
	// Add jitter (±50%)
	jitter := time.Duration(rand.Int63n(int64(backoff) / 2))
	backoff = backoff/2 + jitter

	r.mu.Lock()
	// Cancel existing timer
	if timer, ok := r.reconnectTimers[serverName]; ok {
		timer.Stop()
	}
	r.reconnectTimers[serverName] = time.AfterFunc(backoff, func() {
		r.mu.Lock()
		delete(r.reconnectTimers, serverName)
		r.mu.Unlock()

		ctx, cancel := context.WithTimeout(r.ctx, getConnectionTimeout())
		defer cancel()

		conn, err := r.Reconnect(ctx, serverName)
		if err != nil {
			// Schedule next attempt
			r.ScheduleReconnect(serverName, attempt+1)
			return
		}

		// Notify status change
		if r.callbacks.OnServerStatusChanged != nil {
			r.callbacks.OnServerStatusChanged(serverName, conn)
		}
	})
	r.mu.Unlock()
}

// ---------------------------------------------------------------------------
// rebuildAggregatesLocked — rebuild aggregated tool/command/resource lists
// Must be called with r.mu held for writing.
// ---------------------------------------------------------------------------

func (r *Registry) rebuildAggregatesLocked() {
	// Fresh slices prevent accidental accumulation across rebuilds.
	r.tools = make([]DiscoveredTool, 0, cap(r.tools))
	r.resources = make([]ServerResource, 0, cap(r.resources))
	r.commands = make([]MCPCommand, 0, cap(r.commands))

	for name, conn := range r.connections {
		if _, ok := conn.(*ConnectedServer); ok {
			if tools, ok := r.toolCache.Get(name); ok {
				r.tools = append(r.tools, tools...)
			}
			if resources, ok := r.resourceCache.Get(name); ok {
				r.resources = append(r.resources, resources...)
			}
			if commands, ok := r.commandCache.Get(name); ok {
				r.commands = append(r.commands, commands...)
			}
		}
	}
}
