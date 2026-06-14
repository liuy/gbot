package lsp

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"sync"
	"time"
)

// maxRestarts caps how many times Registry will re-spawn a crashed server per session.
const maxRestarts = 2

// ErrNoServerForFile is returned by ForFile when no known server handles the extension.
var ErrNoServerForFile = errors.New("no lsp server for file extension")

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
	}
}

// Start probes specs via Discover and records the working ones.
// Should be called once at startup; subsequent calls clear previous state.
func (r *Registry) Start(ctx context.Context, specs []ServerSpec) {
	alive := Discover(ctx, specs, r.rootDir)

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
		slog.Info("lsp:registry_started", "servers", names)
	}
}

func (r *Registry) Snapshot() []ServerSpec {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]ServerSpec, len(r.specs))
	copy(out, r.specs)
	return out
}

// ForFile returns a live LSP client for the file's extension, lazily spawning if needed.
func (r *Registry) ForFile(ctx context.Context, path string) (*Client, error) {
	ext := filepath.Ext(path)
	if ext == "" {
		return nil, ErrNoServerForFile
	}

	r.mu.RLock()
	spec, ok := r.extToSpec[ext]
	r.mu.RUnlock()
	if !ok {
		return nil, ErrNoServerForFile
	}

	return r.clientFor(ctx, spec)
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

		// Monitor for crashes: evict on Dead.
		go func() {
			<-c.Dead()
			r.mu.Lock()
			if cur, ok := r.live[spec.Name]; ok && cur == c {
				delete(r.live, spec.Name)
				// Increment crash counter only if this was a real crash (not Shutdown).
				r.restarts[spec.Name]++
			}
			r.mu.Unlock()
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

func (r *Registry) HasExtension(ext string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.extToSpec[ext]
	return ok
}
