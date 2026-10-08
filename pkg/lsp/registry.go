package lsp

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

// maxRestarts caps how many times Registry will re-spawn a crashed server per session.
const maxRestarts = 2

// maxExtraRoots bounds the number of additional project roots one Registry
// serves. Each extra root is a full language-server process (~400MB for gopls
// on this repo), so the pool must not grow with every path the model touches.
const maxExtraRoots = 2

// clientKey identifies one language-server process: a server kind rooted at a
// project directory. The same server name serving two trees is two processes.
type clientKey struct {
	spec string
	root string
}

// LiveServer is one live (server, root) pair.
type LiveServer struct {
	Spec   ServerSpec
	Root   string
	Client *Client
}

// Registry owns all live LSP clients, indexed by file extension and project root.
type Registry struct {
	rootDir string

	mu         sync.RWMutex
	specs      []ServerSpec
	extToSpec  map[string]ServerSpec
	live       map[clientKey]*Client
	restarts   map[clientKey]int           // (spec, root) -> crash-induced restart count (excludes initial spawn)
	sessions   map[clientKey]*spawnSession // (spec, root) -> in-progress spawn gate
	extraRoots []string                    // roots added beyond rootDir, in creation order
	closed     bool
	done       chan struct{}
}

// spawnSession serializes spawn attempts per (spec, root).
type spawnSession struct {
	done chan struct{} // closed when spawn attempt completes (success or failure)
}

func NewRegistry(rootDir string) *Registry {
	return &Registry{
		rootDir:   rootDir,
		extToSpec: make(map[string]ServerSpec),
		live:      make(map[clientKey]*Client),
		restarts:  make(map[clientKey]int),
		sessions:  make(map[clientKey]*spawnSession),
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
	sortSpecsByName(alive)
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
	sortSpecsByName(validated)
	r.specs = validated
	r.extToSpec = make(map[string]ServerSpec, len(validated))
	for _, s := range validated {
		for _, ext := range s.FileExts {
			r.extToSpec[ext] = s
		}
	}
	// Unconditional on purpose: the empty list is the record worth having — with
	// a guard here, "no server passed initialize" reads the same as "Start never
	// ran" when gbot.log is inspected after a restart.
	slog.Info("lsp:startup", "servers", r.serverNamesLocked())
	r.mu.Unlock()
}

// sortSpecsByName pins r.specs to name-ascending order. Discover appends results
// in goroutine-completion order, so without this the list reshuffles every process
// start — which shifts symbol#N numbering across restarts and invalidated the
// prompt prefix cache when the list was rendered into # Environment.
func sortSpecsByName(specs []ServerSpec) {
	slices.SortFunc(specs, func(a, b ServerSpec) int {
		return strings.Compare(a.Name, b.Name)
	})
}

// serverNamesLocked lists server names in r.specs order (caller holds mu).
func (r *Registry) serverNamesLocked() []string {
	names := make([]string, len(r.specs))
	for i, s := range r.specs {
		names[i] = s.Name
	}
	return names
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
	spec, ok := r.extToSpec[ext]
	r.mu.RUnlock()
	return spec, ok
}

// DefaultRoot reports the launch workspace.
func (r *Registry) DefaultRoot() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.rootDir
}

// RootFor reports which root will serve path, without spawning anything.
// The order is deliberate: a path inside the launch workspace stays there even
// when a nested module marker sits above it, so the single-workspace case can
// never be rerouted. Only a path that is outside it looks for its own project.
func (r *Registry) RootFor(path string) string {
	defaultRoot := r.DefaultRoot()
	p := cleanAbs(path)
	if pathWithin(p, defaultRoot) {
		return defaultRoot
	}
	// A marker is an ancestor of the path, so a path outside rootDir cannot
	// have its marker inside rootDir — rule 1 already covers that case.
	if mr := projectRootFor(p, filepath.Ext(p)); mr != "" {
		return mr
	}
	return defaultRoot
}

// ForFile returns a live LSP client for the file's extension, lazily spawning if needed.
func (r *Registry) ForFile(ctx context.Context, path string) (*Client, error) {
	return r.ForFileInRoot(ctx, path, r.RootFor(path))
}

// ForFileInRoot is ForFile with the root already decided. Use it when the
// caller resolved a symbol through a specific root and must keep using that
// server for the file the symbol lives in.
func (r *Registry) ForFileInRoot(ctx context.Context, path, root string) (*Client, error) {
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

	return r.clientFor(ctx, spec, root)
}

// ForSpec returns the client for a specific ServerSpec, spawning it if needed.
// This avoids the sentinel-path workaround (e.g. "/x.go") that ForFile requires
// when the caller already knows which server to talk to (workspace_symbol,
// capabilities, reload, request without a file).
func (r *Registry) ForSpec(ctx context.Context, spec ServerSpec) (*Client, error) {
	return r.ForSpecInRoot(ctx, spec, r.DefaultRoot())
}

// ForSpecInRoot returns the client for spec rooted at root, spawning if needed.
func (r *Registry) ForSpecInRoot(ctx context.Context, spec ServerSpec, root string) (*Client, error) {
	return r.clientFor(ctx, spec, root)
}

// InjectClient registers a pre-made client directly, bypassing spawn.
// The spec is needed to populate the extension→spec mapping for ForFile.
// Only used in tests — the client's Dead channel is monitored for eviction.
func (r *Registry) InjectClient(name string, spec ServerSpec, c *Client) {
	r.InjectClientInRoot(name, r.DefaultRoot(), spec, c)
}

// InjectClientInRoot registers a pre-made client for a specific root. Tests only.
// An injected foreign root joins extraRoots so LiveServers can report it, which
// also puts it in the eviction FIFO: a later on-demand spawn can evict it like
// any other extra root.
func (r *Registry) InjectClientInRoot(name, root string, spec ServerSpec, c *Client) {
	k := clientKey{name, root}

	r.mu.Lock()
	r.live[k] = c
	// Idempotent by name: a duplicated spec would make status print the server
	// twice and make resolveInWorkspace query the same client twice, which
	// duplicates matches and shifts symbol#N.
	if !slices.ContainsFunc(r.specs, func(s ServerSpec) bool { return s.Name == spec.Name }) {
		r.specs = append(r.specs, spec)
	}
	for _, ext := range spec.FileExts {
		r.extToSpec[ext] = spec
	}
	if root != r.rootDir && !slices.Contains(r.extraRoots, root) {
		r.extraRoots = append(r.extraRoots, root)
	}
	r.mu.Unlock()

	go func() {
		select {
		case <-c.Dead():
		case <-r.done:
		}
		r.mu.Lock()
		if cur, ok := r.live[k]; ok && cur == c {
			delete(r.live, k)
		}
		r.mu.Unlock()
	}()
}

// clientFor returns a live client, spawning under single-flight.
// Each spawn session is its own gate: after the spawn finishes (success or fail),
// the gate is removed, so a subsequent crash starts a fresh gate.
func (r *Registry) clientFor(ctx context.Context, spec ServerSpec, root string) (*Client, error) {
	k := clientKey{spec.Name, root}

	// Fast path: live and not dead.
	r.mu.RLock()
	if c, ok := r.live[k]; ok {
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
		if c, ok := r.live[k]; ok {
			select {
			case <-c.Dead():
			default:
				r.mu.Unlock()
				return c, nil
			}
		}
		// Is another goroutine already spawning?
		if sess, ok := r.sessions[k]; ok {
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
		r.sessions[k] = sess
		r.mu.Unlock()

		// Spawn outside the lock.
		c, err := r.spawnWithBudget(ctx, spec, root)
		close(sess.done)

		r.mu.Lock()
		delete(r.sessions, k)
		if err != nil {
			r.mu.Unlock()
			return nil, err
		}
		r.live[k] = c
		victims := r.evictOldestRootLocked(r.registerRootLocked(root))
		r.mu.Unlock()

		// Kill outside the write lock: Shutdown would deadlock waiting on the
		// process we just killed (same reason as KillAndEvict). A request that
		// was already in flight on a victim surfaces as ErrServerDead, and the
		// caller's next attempt respawns that root.
		for _, v := range victims {
			v.Kill()
		}

		go func() {
			select {
			case <-c.Dead():
				// Real crash or Shutdown — evict from live map.
				r.mu.Lock()
				if cur, ok := r.live[k]; ok && cur == c {
					delete(r.live, k)
					r.restarts[k]++
				}
				r.mu.Unlock()
			case <-r.done:
				// Registry shutting down — exit gracefully.
			}
		}()

		return c, nil
	}
}

// registerRootLocked records root as an extra root when it is new and is not
// the launch workspace. Returns true when the root was added. Caller holds r.mu.
func (r *Registry) registerRootLocked(root string) bool {
	if root == r.rootDir {
		return false
	}
	if slices.Contains(r.extraRoots, root) {
		return false
	}
	r.extraRoots = append(r.extraRoots, root)
	return true
}

// evictOldestRootLocked drops the oldest extra root when the pool is over
// budget, returning the clients that must be killed by the caller. Caller
// holds r.mu and must Kill the returned clients after unlocking.
//
// Eviction is creation-order rather than least-recently-used: with a cap of 2
// the difference is immaterial, and FIFO avoids taking the write lock on every
// cache hit. r.sessions is deliberately untouched — clientFor deletes the
// session before inserting into live, so a key present in live never has a
// session, while a different key at the same root that is mid-spawn must keep
// its own.
func (r *Registry) evictOldestRootLocked(rootWasNew bool) []*Client {
	if !rootWasNew || len(r.extraRoots) <= maxExtraRoots {
		return nil
	}
	victim := r.extraRoots[0]
	r.extraRoots = r.extraRoots[1:]
	var victims []*Client
	for k, c := range r.live {
		if k.root == victim {
			victims = append(victims, c)
			delete(r.live, k)
			delete(r.restarts, k)
		}
	}
	return victims
}

// spawnWithBudget enforces maxRestarts before calling spawnClient. The budget
// is per (spec, root): a server crashing in one project must not exhaust the
// restart allowance for the same server in another.
func (r *Registry) spawnWithBudget(ctx context.Context, spec ServerSpec, root string) (*Client, error) {
	r.mu.RLock()
	restarts := r.restarts[clientKey{spec.Name, root}]
	r.mu.RUnlock()
	if restarts > maxRestarts {
		return nil, fmt.Errorf("lsp %s: exceeded %d restarts", spec.Name, maxRestarts)
	}
	return spawnClient(ctx, spec, root)
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
	r.live = make(map[clientKey]*Client)
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

// LiveServers returns every live (spec, root) pair: default root first, then
// extra roots in creation order, each group sorted by spec name. Entries whose
// client reports IsAlive() == false are skipped, so a crashed-but-not-yet-
// evicted server is not reported as ready.
func (r *Registry) LiveServers() []LiveServer {
	r.mu.RLock()
	defer r.mu.RUnlock()

	byRoot := make(map[string][]LiveServer, len(r.extraRoots)+1)
	for k, c := range r.live {
		if !c.IsAlive() {
			continue
		}
		byRoot[k.root] = append(byRoot[k.root], LiveServer{
			Spec:   r.specForNameLocked(k.spec),
			Root:   k.root,
			Client: c,
		})
	}

	var out []LiveServer
	appendGroup := func(root string) {
		group := byRoot[root]
		if len(group) == 0 {
			return
		}
		slices.SortFunc(group, func(a, b LiveServer) int {
			return strings.Compare(a.Spec.Name, b.Spec.Name)
		})
		out = append(out, group...)
	}
	appendGroup(r.rootDir)
	for _, root := range r.extraRoots {
		appendGroup(root)
	}
	return out
}

// specForNameLocked recovers a ServerSpec from the name half of a clientKey.
// Falls back to a name-only spec when the pool holds a client that was never
// registered in specs, so callers still get a usable Name.
func (r *Registry) specForNameLocked(name string) ServerSpec {
	for _, s := range r.specs {
		if s.Name == name {
			return s
		}
	}
	return ServerSpec{Name: name}
}

// CheckWriteRoot returns an error when a write action on path would be served by
// the launch-workspace server even though path lies outside it.
func (r *Registry) CheckWriteRoot(path string) error {
	p := cleanAbs(path)
	root := r.DefaultRoot()
	if pathWithin(p, root) {
		return nil
	}
	if projectRootFor(p, filepath.Ext(p)) != "" {
		return nil
	}
	// "no marker of its own" would be false for a .py under a go.mod directory:
	// the marker is there, it just does not belong to that file's language.
	return fmt.Errorf("file %s is outside the workspace root %s and its directory has no project marker for its file type (go.mod, package.json or Cargo.toml), so no language server can be rooted at its directory; pass a file inside %s or a file whose directory has a marker for that extension",
		p, root, root)
}

// KillAndEvict kills the subprocess for `name` rooted at `root` (if any) and
// removes it from the live map so the next ForFile call respawns. Returns false
// if no live client exists. Used by reload as the kill fallback when neither
// rust-analyzer/reloadWorkspace nor workspace/didChangeConfiguration succeeds.
func (r *Registry) KillAndEvict(name, root string) bool {
	k := clientKey{name, root}
	r.mu.Lock()
	c, ok := r.live[k]
	if ok {
		delete(r.live, k)
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
