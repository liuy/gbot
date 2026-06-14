package lsp

import (
	"context"
	"log/slog"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// Note: time is still used by Discover's outer timeout.

// ServerSpec describes how to spawn a language server and which files it covers.
type ServerSpec struct {
	Name     string   // human-readable, e.g. "gopls"
	Command  string   // executable name; resolved via exec.LookPath
	Args     []string // args appended to Command
	FileExts []string // extensions this server owns, e.g. []string{".go"}
	Language string   // short label for Environment section, e.g. "Go"
	ExtraEnv []string // additional env var KEY=VALUE entries (test use only, e.g. GBOT_FAKE_LSP=1)
}

// DefaultServers is the static probe list, ordered by stability of binary names.
var DefaultServers = []ServerSpec{
	{Name: "gopls", Command: "gopls", Args: []string{"-rpc.trace"}, FileExts: []string{".go"}, Language: "Go"},
	{Name: "typescript-language-server", Command: "typescript-language-server", Args: []string{"--stdio"}, FileExts: []string{".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs"}, Language: "TypeScript, JavaScript"},
	{Name: "rust-analyzer", Command: "rust-analyzer", FileExts: []string{".rs"}, Language: "Rust"},
	{Name: "pyright-langserver", Command: "pyright-langserver", Args: []string{"--stdio"}, FileExts: []string{".py"}, Language: "Python"},
	{Name: "clangd", Command: "clangd", FileExts: []string{".c", ".h", ".cc", ".cpp", ".hpp", ".cxx", ".hxx"}, Language: "C, C++"},
}

// Discover probes every spec in parallel and returns specs that produced a working server.
// Each spec gets a 5s wall-clock budget; total wall-clock ≤ 5s regardless of spec count.
// A spec "works" if: (1) LookPath finds the binary, and (2) initialize handshake completes.
func Discover(ctx context.Context, specs []ServerSpec, rootDir string) []ServerSpec {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	var wg sync.WaitGroup
	type result struct {
		spec ServerSpec
		ok   bool
	}
	results := make(chan result, len(specs))

	for _, spec := range specs {
		wg.Add(1)
		go func(s ServerSpec) {
			defer wg.Done()
			results <- result{spec: s, ok: probeOne(ctx, s, rootDir)}
		}(spec)
	}
	wg.Wait()
	close(results)

	var alive []ServerSpec
	for r := range results {
		if r.ok {
			alive = append(alive, r.spec)
		}
	}
	return alive
}

// probeOne returns true if the binary is on PATH and initialize succeeds.
// Failure is logged at debug level; we don't treat "not installed" as an error.
// A server found on PATH that fails initialize is logged at Info — that's actionable.
func probeOne(ctx context.Context, spec ServerSpec, rootDir string) bool {
	path, err := execLookPath(spec.Command)
	if err != nil {
		return false
	}

	// Inherit deadline from parent ctx (Discover already capped to 5s).
	c, err := StartClient(ctx, spec.Name, path, spec.Args, rootDir, spec.ExtraEnv...)
	if err != nil {
		slog.Info("lsp:probe_start_failed", "name", spec.Name, "err", err)
		return false
	}

	rootURI := pathToURI(rootDir)
	if err := c.Initialize(ctx, rootURI); err != nil {
		slog.Info("lsp:probe_init_failed", "name", spec.Name, "err", err)
		c.Shutdown(context.Background())
		return false
	}

	c.Shutdown(context.Background())
	return true
}

// pathToURI converts a filesystem path to a file:// URI per RFC 8089.
func pathToURI(p string) string {
	abs, err := filepath.Abs(p)
	if err != nil {
		abs = p
	}
	if runtime.GOOS == "windows" {
		// Windows: file:///C:/path
		return "file:///" + strings.ReplaceAll(abs, "\\", "/")
	}
	return "file://" + abs
}
