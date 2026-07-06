package lsp

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// maxRestarts caps how many times Registry will re-spawn a crashed server per session.
const maxRestarts = 2

// Registry owns all live LSP clients, indexed by file extension.
type Registry struct {
	rootDir string

	mu        sync.RWMutex
	specs     []ServerSpec
	extToSpec map[string]ServerSpec
	live      map[string]*Client
	restarts  map[string]int           // spec.Name -> crash-induced restart count (excludes initial spawn)
	sessions  map[string]*spawnSession // spec.Name -> in-progress spawn gate
	closed    bool
	done      chan struct{}
}

// spawnSession serializes spawn attempts per spec.
type spawnSession struct {
	done chan struct{} // closed when spawn attempt completes (success or failure)
}

func NewRegistry(rootDir string) *Registry {
	return &Registry{
		rootDir:   rootDir,
		extToSpec: make(map[string]ServerSpec),
		live:      make(map[string]*Client),
		restarts:  make(map[string]int),
		sessions:  make(map[string]*spawnSession),
		done:      make(chan struct{}),
	}
}

// Scan populates the registry from PATH-only checks (no spawn).
// Returns immediately; used at startup so RuntimeInfo can list available
// servers right away. Call Start afterwards (or in a goroutine) to spawn+validate.
func (r *Registry) Scan(specs []ServerSpec) {
	alive := ScanServers(specs)

	r.mu.Lock()
	defer r.mu.Unlock()
	r.specs = alive
	r.extToSpec = make(map[string]ServerSpec, len(alive))
	for _, s := range alive {
		for _, ext := range s.FileExts {
			r.extToSpec[ext] = s
		}
	}
	if len(alive) > 0 {
		names := make([]string, len(alive))
		for i, s := range alive {
			names[i] = s.Name
		}
		slog.Info("lsp:registry_init", "servers", names)
	}
}

// Start spawns+validates each server via initialize handshake.
// Call after InitFromPATH; results replace the PATH-only spec list.
func (r *Registry) Start(ctx context.Context, specs []ServerSpec) {
	validated := Discover(ctx, specs, r.rootDir)

	r.mu.Lock()
	r.specs = validated
	r.extToSpec = make(map[string]ServerSpec, len(validated))
	for _, s := range validated {
		for _, ext := range s.FileExts {
			r.extToSpec[ext] = s
		}
	}
	if len(validated) > 0 {
		slog.Info("lsp:startup", "servers", r.lspStringLocked())
	}
	r.mu.Unlock()
}

// lspStringLocked builds LSPString without taking the lock (caller holds mu).
func (r *Registry) lspStringLocked() string {
	if len(r.specs) == 0 {
		return ""
	}
	var b strings.Builder
	for i, s := range r.specs {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(s.Name)
		b.WriteString(" (")
		b.WriteString(s.Language)
		b.WriteString(")")
	}
	return b.String()
}

func (r *Registry) Snapshot() []ServerSpec {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]ServerSpec, len(r.specs))
	copy(out, r.specs)
	return out
}

// SpecForFile returns the configured ServerSpec for the file's extension.
// Returns ok=false when no server is configured for this file type.
func (r *Registry) SpecForFile(path string) (ServerSpec, bool) {
	ext := filepath.Ext(path)
	if ext == "" {
		return ServerSpec{}, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	spec, ok := r.extToSpec[ext]
	return spec, ok
}

// ForFile returns a live LSP client for the file's extension, lazily spawning if needed.
func (r *Registry) ForFile(ctx context.Context, path string) (*Client, error) {
	ext := filepath.Ext(path)
	if ext == "" {
		return nil, fmt.Errorf("lsp needs a file path with extension (e.g. .go), got: %s", path)
	}

	r.mu.RLock()
	spec, ok := r.extToSpec[ext]
	r.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("no lsp server for file extension %q (path: %s)", ext, path)
	}

	return r.clientFor(ctx, spec)
}

// ForSpec returns the client for a specific ServerSpec, spawning it if needed.
// This avoids the sentinel-path workaround (e.g. "/x.go") that ForFile requires
// when the caller already knows which server to talk to (workspace_symbol,
// capabilities, reload, request without a file).
func (r *Registry) ForSpec(ctx context.Context, spec ServerSpec) (*Client, error) {
	return r.clientFor(ctx, spec)
}

// InjectClient registers a pre-made client directly, bypassing spawn.
// The spec is needed to populate the extension→spec mapping for ForFile.
// Only used in tests — the client's Dead channel is monitored for eviction.
func (r *Registry) InjectClient(name string, spec ServerSpec, c *Client) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.live[name] = c
	r.specs = append(r.specs, spec)
	for _, ext := range spec.FileExts {
		r.extToSpec[ext] = spec
	}

	go func() {
		select {
		case <-c.Dead():
		case <-r.done:
		}
		r.mu.Lock()
		if cur, ok := r.live[name]; ok && cur == c {
			delete(r.live, name)
		}
		r.mu.Unlock()
	}()
}

// clientFor returns a live client, spawning under single-flight.
// Each spawn session is its own gate: after the spawn finishes (success or fail),
// the gate is removed, so a subsequent crash starts a fresh gate.
func (r *Registry) clientFor(ctx context.Context, spec ServerSpec) (*Client, error) {
	// Fast path: live and not dead.
	r.mu.RLock()
	if c, ok := r.live[spec.Name]; ok {
		select {
		case <-c.Dead():
		default:
			r.mu.RUnlock()
			return c, nil
		}
	}
	r.mu.RUnlock()

	// Slow path: spawn under single-flight.
	for {
		r.mu.Lock()
		if r.closed {
			r.mu.Unlock()
			return nil, errors.New("lsp registry closed")
		}
		// Re-check live after acquiring write lock.
		if c, ok := r.live[spec.Name]; ok {
			select {
			case <-c.Dead():
			default:
				r.mu.Unlock()
				return c, nil
			}
		}
		// Is another goroutine already spawning?
		if sess, ok := r.sessions[spec.Name]; ok {
			r.mu.Unlock()
			select {
			case <-sess.done:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			// Loop: re-check live map. If spawn failed, we'll get our own session.
			continue
		}
		// We won the race — create our own session.
		sess := &spawnSession{done: make(chan struct{})}
		r.sessions[spec.Name] = sess
		r.mu.Unlock()

		// Spawn outside the lock.
		c, err := r.spawnWithBudget(ctx, spec)
		close(sess.done)

		r.mu.Lock()
		delete(r.sessions, spec.Name)
		if err != nil {
			r.mu.Unlock()
			return nil, err
		}
		r.live[spec.Name] = c
		r.mu.Unlock()

		go func() {
			select {
			case <-c.Dead():
				// Real crash or Shutdown — evict from live map.
				r.mu.Lock()
				if cur, ok := r.live[spec.Name]; ok && cur == c {
					delete(r.live, spec.Name)
					r.restarts[spec.Name]++
				}
				r.mu.Unlock()
			case <-r.done:
				// Registry shutting down — exit gracefully.
			}
		}()

		return c, nil
	}
}

// spawnWithBudget enforces maxRestarts before calling spawnClient.
func (r *Registry) spawnWithBudget(ctx context.Context, spec ServerSpec) (*Client, error) {
	r.mu.RLock()
	restarts := r.restarts[spec.Name]
	r.mu.RUnlock()
	if restarts > maxRestarts {
		return nil, fmt.Errorf("lsp %s: exceeded %d restarts", spec.Name, maxRestarts)
	}
	return spawnClient(ctx, spec, r.rootDir)
}

func spawnClient(ctx context.Context, spec ServerSpec, rootDir string) (*Client, error) {
	path, err := execLookPath(spec.Command)
	if err != nil {
		return nil, fmt.Errorf("lsp %s: lookpath: %w", spec.Name, err)
	}

	startCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	c, err := StartClient(startCtx, spec.Name, path, spec.Args, rootDir, spec.ExtraEnv...)
	if err != nil {
		return nil, err
	}

	if err := c.Initialize(startCtx, pathToURI(rootDir)); err != nil {
		c.Shutdown(context.Background())
		return nil, err
	}
	return c, nil
}

// Shutdown tears down all live clients in parallel. Idempotent.
func (r *Registry) Shutdown(ctx context.Context) {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return
	}
	r.closed = true
	close(r.done)
	clients := make([]*Client, 0, len(r.live))
	for _, c := range r.live {
		clients = append(clients, c)
	}
	r.live = make(map[string]*Client)
	r.mu.Unlock()

	var wg sync.WaitGroup
	for _, c := range clients {
		wg.Add(1)
		go func(c *Client) {
			defer wg.Done()
			shutdownCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			c.Shutdown(shutdownCtx)
		}(c)
	}
	wg.Wait()
}

// NumServers returns the count of discovered LSP servers without allocation.
func (r *Registry) NumServers() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.specs)
}

// StartedClient reports whether a server with the given name has been spawned
// (live in the registry) and is still responsive. Mirrors omp's
// startedByConfigName check (index.ts:1366-1375).
func (r *Registry) StartedClient(name string) (*Client, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	c, ok := r.live[name]
	if !ok || !c.IsAlive() {
		return nil, false
	}
	return c, true
}

// KillAndEvict kills the subprocess for `name` (if any) and removes it from
// the live map so the next ForFile call respawns. Returns false if no live
// client exists. Used by reload as the kill fallback when neither
// rust-analyzer/reloadWorkspace nor workspace/didChangeConfiguration succeeds.
func (r *Registry) KillAndEvict(name string) bool {
	r.mu.Lock()
	c, ok := r.live[name]
	if ok {
		delete(r.live, name)
	}
	r.mu.Unlock()
	if !ok {
		return false
	}
	c.Kill()
	// Shutdown would deadlock here (it waits on the same process we just killed).
	// waitLoop goroutine started in StartClient reaps the zombie.
	return true
}

func (r *Registry) HasExtension(ext string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.extToSpec[ext]
	return ok
}

// LSPString returns a compact server listing for the # Environment section.
// Includes language labels so the model can match lsp:true to the right file type.
// Uses comma (not pipe) to avoid confusion with the outer Runtime separator.
func (r *Registry) LSPString() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if len(r.specs) == 0 {
		return ""
	}
	var b strings.Builder
	for i, s := range r.specs {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(s.Name)
		b.WriteString(" (")
		b.WriteString(s.Language)
		b.WriteString(")")
	}
	return b.String()
}
