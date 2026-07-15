package lsp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ErrServerDead is returned when the LSP server process has exited.
var ErrServerDead = errors.New("lsp server is not running")

// execCommand and execLookPath are package-level vars so tests can replace them.
// execCommand intentionally does NOT take a context — see StartClient comment.
var execCommand = exec.Command
var execLookPath = exec.LookPath

// Client wraps a single LSP server subprocess speaking JSON-RPC 2.0 over stdio.
type Client struct {
	name   string
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.Reader

	mu       sync.Mutex
	pending  map[int64]chan *rpcResponse
	nextID   int64
	openURIs map[string]int // uri -> version, for didOpen/didChange dedup

	diagMu sync.RWMutex
	diags  map[string][]Diagnostic // uri -> latest diagnostics from server

	writeMu sync.Mutex

	teardownOnce sync.Once
	done         chan struct{}
	dead         chan struct{}
	readWG       sync.WaitGroup // tracks readLoop goroutine (for test cleanup)

	capabilities json.RawMessage
}

// NewTestClient creates a Client backed by a connection (net.Conn, pipe, etc.)
// instead of a subprocess. Used by tests that provide a fake LSP server.
// The caller is responsible for closing conn and waiting on readWG.
func NewTestClient(name string, conn io.ReadWriteCloser) *Client {
	c := &Client{
		name:     name,
		pending:  make(map[int64]chan *rpcResponse),
		openURIs: make(map[string]int),
		diags:    make(map[string][]Diagnostic),
		done:     make(chan struct{}),
		dead:     make(chan struct{}),
		stdin:    conn,
		stdout:   conn,
	}
	c.readWG.Go(func() { c.readLoop() })
	return c
}

type rpcResponse struct {
	result json.RawMessage
	err    error
}

type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int64  `json:"id"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type rpcResponseEnvelope struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *int64          `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *rpcError) Error() string { return fmt.Sprintf("lsp error %d: %s", e.Code, e.Message) }

type rpcNotification struct {
	JSONRPC string `json:"jsonrpc"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

// StartClient spawns the LSP server subprocess and starts the reader goroutine.
// Does NOT send initialize; caller calls Initialize() next.
// extraEnv allows passing additional environment variables (e.g., GBOT_FAKE_LSP for tests).
func StartClient(ctx context.Context, name, command string, args []string, cwd string, extraEnv ...string) (*Client, error) {
	// Use exec.Command (NOT exec.CommandContext) so the subprocess survives
	// the caller's ctx. The spawn ctx is only for the handshake; the process
	// should outlive it. Lifecycle is owned by the Client via Shutdown.
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("lsp %s: %w", name, err)
	}
	cmd := execCommand(command, args...)
	cmd.Dir = cwd
	cmd.Stderr = nil
	if len(extraEnv) > 0 {
		cmd.Env = append(os.Environ(), extraEnv...)
	}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("lsp %s: stdin pipe: %w", name, err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, fmt.Errorf("lsp %s: stdout pipe: %w", name, err)
	}

	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		return nil, fmt.Errorf("lsp %s: start %q: %w", name, command, err)
	}

	c := &Client{
		name:     name,
		cmd:      cmd,
		stdin:    stdin,
		stdout:   stdout,
		pending:  make(map[int64]chan *rpcResponse),
		openURIs: make(map[string]int),
		diags:    make(map[string][]Diagnostic),
		done:     make(chan struct{}),
		dead:     make(chan struct{}),
	}

	c.readWG.Go(func() {
		c.readLoop()
	})
	go c.waitLoop()

	return c, nil
}

// waitLoop calls cmd.Wait and triggers teardown on exit.
// For in-process clients (cmd == nil), readLoop's EOF drives teardown instead.
func (c *Client) waitLoop() {
	if c.cmd == nil {
		return
	}
	err := c.cmd.Wait()
	slog.Info("lsp:server_exited", "name", c.name, "err", err)
	c.teardown(err)
}

// readLoop parses Content-Length framed messages from stdout and dispatches them.
// Exits on stdout EOF or read error, which triggers teardown.
func (c *Client) readLoop() {
	r := bufio.NewReader(c.stdout)
	for {
		var contentLength int
		// Read headers until blank line.
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				c.teardown(err)
				return
			}
			line = strings.TrimRight(line, "\r\n")
			if line == "" {
				break
			}
			if strings.HasPrefix(line, "Content-Length:") {
				n, perr := strconv.Atoi(strings.TrimSpace(line[len("Content-Length:"):]))
				if perr != nil {
					slog.Warn("lsp:bad_content_length", "name", c.name, "line", line, "err", perr)
					continue
				}
				contentLength = n
			}
		}
		if contentLength <= 0 {
			continue
		}

		body := make([]byte, contentLength)
		if _, err := io.ReadFull(r, body); err != nil {
			slog.Warn("lsp:short_body", "name", c.name, "want", contentLength, "err", err)
			c.teardown(err)
			return
		}

		// Peek at the message shape: server→client request has both id and method.
		var probe struct {
			ID     *int64          `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal(body, &probe); err != nil {
			slog.Warn("lsp:bad_json", "name", c.name, "err", err)
			continue
		}

		// Server→client request: respond so it doesn't hang.
		if probe.ID != nil && probe.Method != "" {
			c.handleServerRequest(*probe.ID, probe.Method, probe.Params)
			continue
		}
		// Server→client notification: capture diagnostics, drop the rest.
		if probe.Method != "" && probe.ID == nil {
			if probe.Method == "textDocument/publishDiagnostics" {
				c.storeDiagnostics(probe.Params)
			} else {
				slog.Debug("lsp:notification_dropped", "name", c.name, "method", probe.Method)
			}
			continue
		}

		// Response to a previous request.
		if probe.ID == nil {
			continue
		}

		var env rpcResponseEnvelope
		if err := json.Unmarshal(body, &env); err != nil {
			slog.Warn("lsp:bad_response", "name", c.name, "err", err)
			continue
		}
		id := *env.ID

		c.mu.Lock()
		ch, ok := c.pending[id]
		if ok {
			delete(c.pending, id)
		}
		c.mu.Unlock()
		if !ok {
			continue
		}

		resp := &rpcResponse{result: env.Result}
		if env.Error != nil {
			resp.err = env.Error
		}
		select {
		case ch <- resp:
		default:
		}
	}
}

// handleServerRequest replies to server-initiated requests.
// gopls and other servers send workspace/configuration after init; not responding
// makes them hang or behave erratically. Reference: omp client.ts:343-407.
func (c *Client) handleServerRequest(id int64, method string, _ json.RawMessage) {
	var result any
	switch method {
	case "workspace/configuration":
		// Return empty config per requested section.
		result = []any{}
	case "workspace/workspaceFolders":
		result = []any{}
	case "window/workDoneProgress/create":
		result = nil
	case "workspace/applyEdit":
		// gbot owns the editor surface; server-pushed edits are rejected.
		result = map[string]bool{"applied": false}
	default:
		c.writeMu.Lock()
		_ = c.writeMessage(map[string]any{
			"jsonrpc": "2.0",
			"id":      id,
			"error":   map[string]any{"code": -32601, "message": "method not found: " + method},
		})
		c.writeMu.Unlock()
		return
	}

	c.writeMu.Lock()
	_ = c.writeMessage(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"result":  result,
	})
	c.writeMu.Unlock()
}

// teardown is idempotent: fails all pending requests, closes done+dead.
// Owns ALL channel close semantics — Request's defer must NOT close channels.
// (readErr parameter is logged at info level for diagnostics; plain EOF is suppressed.)
func (c *Client) teardown(readErr error) {
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		slog.Info("lsp:teardown", "name", c.name, "err", readErr.Error())
	}
	c.teardownOnce.Do(func() {
		close(c.done)

		c.mu.Lock()
		for id, ch := range c.pending {
			select {
			case ch <- &rpcResponse{err: ErrServerDead}:
			default:
			}
			delete(c.pending, id)
		}
		c.mu.Unlock()

		close(c.dead)

		if readErr != nil && !errors.Is(readErr, io.EOF) {
			slog.Warn("lsp:read_error", "name", c.name, "err", readErr)
		}
	})
}

func (c *Client) writeMessage(msg any) error {
	body, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("lsp %s: marshal: %w", c.name, err)
	}
	header := fmt.Sprintf("Content-Length: %d\r\n\r\n", len(body))
	if _, err := c.stdin.Write(append([]byte(header), body...)); err != nil {
		return fmt.Errorf("lsp %s: write: %w", c.name, err)
	}
	return nil
}

// Request sends a JSON-RPC request and waits for the response (or ctx / dead).
// teardown owns closing the response channel; this function never closes it
// (avoids double-close panic when teardown beats us to cleanup).
func (c *Client) Request(ctx context.Context, method string, params any) (json.RawMessage, error) {
	select {
	case <-c.dead:
		return nil, ErrServerDead
	default:
	}

	c.mu.Lock()
	c.nextID++
	id := c.nextID
	ch := make(chan *rpcResponse, 1)
	c.pending[id] = ch
	c.mu.Unlock()

	c.writeMu.Lock()
	err := c.writeMessage(rpcRequest{JSONRPC: "2.0", ID: id, Method: method, Params: params})
	c.writeMu.Unlock()
	if err != nil {
		// Pending slot still owned by us; remove it.
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, err
	}

	select {
	case resp, ok := <-ch:
		if !ok || resp == nil {
			return nil, ErrServerDead
		}
		return resp.result, resp.err
	case <-c.dead:
		return nil, ErrServerDead
	case <-ctx.Done():
		// Best-effort cancel; server may or may not honor it.
		_ = c.Notify(context.Background(), "$/cancelRequest", map[string]int64{"id": id})
		return nil, ctx.Err()
	}
}

// Notify sends a JSON-RPC notification (no response expected).
func (c *Client) Notify(ctx context.Context, method string, params any) error {
	select {
	case <-c.dead:
		return ErrServerDead
	default:
	}

	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return c.writeMessage(rpcNotification{JSONRPC: "2.0", Method: method, Params: params})
}

// EnsureFileOpen sends textDocument/didOpen for the file once (deduped by URI).
// Required before textDocument/* requests; LSP servers need file content buffered.
func (c *Client) EnsureFileOpen(ctx context.Context, uri, languageID, content string) error {
	c.mu.Lock()
	if _, ok := c.openURIs[uri]; ok {
		c.mu.Unlock()
		return nil
	}
	c.openURIs[uri] = 1
	c.mu.Unlock()

	return c.Notify(ctx, "textDocument/didOpen", map[string]any{
		"textDocument": TextDocumentItem{
			URI:        uri,
			LanguageID: languageID,
			Version:    1,
			Text:       content,
		},
	})
}

// DidChange notifies the server of new in-memory content for a file opened via EnsureFileOpen.
func (c *Client) DidChange(ctx context.Context, uri, content string) error {
	c.mu.Lock()
	v, ok := c.openURIs[uri]
	if !ok {
		c.mu.Unlock()
		return fmt.Errorf("lsp %s: DidChange before didOpen for %s", c.name, uri)
	}
	v++
	c.openURIs[uri] = v
	c.mu.Unlock()

	return c.Notify(ctx, "textDocument/didChange", map[string]any{
		"textDocument":   VersionedTextDocumentIdentifier{URI: uri, Version: v},
		"contentChanges": []map[string]string{{"text": content}},
	})
}

// NotifyFileChanged syncs the server's view of a file after an external edit.
// Reads fresh disk content and sends didOpen (if not yet open) or didChange
// (if already open), so the server's internal snapshot cache stays coherent.
func (c *Client) NotifyFileChanged(ctx context.Context, uri string, languageID string) error {
	path := URItoPath(uri)
	content, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("lsp %s: NotifyFileChanged read %s: %w", c.name, path, err)
	}
	c.mu.Lock()
	_, open := c.openURIs[uri]
	c.mu.Unlock()
	if !open {
		return c.EnsureFileOpen(ctx, uri, languageID, string(content))
	}
	return c.DidChange(ctx, uri, string(content))
}

// NotifyFilesChanged is a batch helper for NotifyFileChanged. It detects each
// file's language from its extension and calls NotifyFileChanged for each.
func (c *Client) NotifyFilesChanged(ctx context.Context, paths []string) {
	for _, path := range paths {
		uri := FileToURI(path)
		langID := DetectLanguage(path)
		if err := c.NotifyFileChanged(ctx, uri, langID); err != nil {
			slog.Warn("lsp: notify change failed", "file", path, "error", err)
		}
	}
}

// Initialize sends the LSP initialize handshake. Call once after StartClient.
func (c *Client) Initialize(ctx context.Context, rootURI string) error {
	params := map[string]any{
		"processId": os.Getpid(),
		"rootUri":   rootURI,
		"capabilities": map[string]any{
			"textDocument": map[string]any{
				"synchronization": map[string]any{"didSave": true},
				"hover":           map[string]any{"contentFormat": []string{"markdown", "plaintext"}},
				"definition":      map[string]any{"linkSupport": false},
				"implementation":  map[string]any{"linkSupport": false},
				"references":      map[string]any{},
				"documentSymbol":  map[string]any{"hierarchicalDocumentSymbolSupport": true},
				"rename":          map[string]any{"prepareSupport": true},
				"codeAction": map[string]any{
					"codeActionLiteralSupport": map[string]any{
						"codeActionKind": map[string]any{
							"valueSet": []string{"quickfix", "refactor", "source", "source.organizeImports", "source.fixAll"},
						},
					},
				},
			},
			"workspace": map[string]any{
				"symbol":    map[string]any{},
				"applyEdit": true,
				"workspaceEdit": map[string]any{
					"documentChanges":    true,
					"resourceOperations": []string{"create", "rename", "delete"},
				},
				"configuration":    true,
				"workspaceFolders": true,
			},
		},
	}

	result, err := c.Request(ctx, "initialize", params)
	if err != nil {
		return fmt.Errorf("lsp %s: initialize: %w", c.name, err)
	}
	c.mu.Lock()
	c.capabilities = result
	c.mu.Unlock()

	if err := c.Notify(ctx, "initialized", map[string]any{}); err != nil {
		return fmt.Errorf("lsp %s: initialized notify: %w", c.name, err)
	}
	return nil
}

// Shutdown sends the LSP shutdown+exit handshake. Idempotent via teardownOnce.
// Immediately closes stdin to unblock readLoop; waits briefly for graceful exit
// before SIGKILL (subprocess) or <-dead (in-process).
func (c *Client) Shutdown(ctx context.Context) {
	select {
	case <-c.dead:
		return
	default:
	}

	shutdownCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	_, _ = c.Request(shutdownCtx, "shutdown", nil)
	_ = c.Notify(context.Background(), "exit", nil)
	// Close stdin to unblock readLoop immediately. For subprocess this signals EOF;
	// for in-process (stdin == stdout net.Conn) this causes readLoop to get EOF.
	if c.stdin != nil {
		_ = c.stdin.Close()
	}

	select {
	case <-c.dead:
		return
	case <-time.After(200 * time.Millisecond):
	}

	if c.cmd != nil && c.cmd.Process != nil {
		_ = c.cmd.Process.Kill()
	}
	if c.cmd == nil {
		select {
		case <-c.dead:
		default:
		}
	}
	select {
	case <-c.dead:
	case <-ctx.Done():
	}
}

func (c *Client) Name() string { return c.name }

func (c *Client) Capabilities() json.RawMessage {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.capabilities
}

func (c *Client) Dead() <-chan struct{} { return c.dead }

// IsAlive reports whether the client subprocess (or in-process connection)
// is still responsive. False once teardown has fired.
func (c *Client) IsAlive() bool {
	select {
	case <-c.dead:
		return false
	default:
		return true
	}
}

// Kill forcibly terminates the subprocess (SIGKILL). No-op for in-process
// clients (cmd is nil). Used as the last-resort reload fallback when the
// server neither implements rust-analyzer/reloadWorkspace nor responds to
// workspace/didChangeConfiguration. The next ForFile call respawns.
func (c *Client) Kill() {
	if c.cmd != nil && c.cmd.Process != nil {
		_ = c.cmd.Process.Kill()
	}
}

// encodeMessage is exported for tests that want to inspect the framing layout.
func encodeMessage(msg any) ([]byte, error) {
	body, err := json.Marshal(msg)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	fmt.Fprintf(&buf, "Content-Length: %d\r\n\r\n", len(body))
	buf.Write(body)
	return buf.Bytes(), nil
}

// storeDiagnostics parses a publishDiagnostics notification and caches the
// latest diagnostics for each URI. Called from readLoop.
func (c *Client) storeDiagnostics(params json.RawMessage) {
	var notif struct {
		URI         string       `json:"uri"`
		Diagnostics []Diagnostic `json:"diagnostics"`
	}
	if err := json.Unmarshal(params, &notif); err != nil {
		slog.Debug("lsp:bad_diagnostics", "name", c.name, "err", err)
		return
	}
	c.InjectDiagnostics(notif.URI, notif.Diagnostics)
}

// InjectDiagnostics replaces the cached diagnostics for a URI. Exposed for
// test injection.
func (c *Client) InjectDiagnostics(uri string, diags []Diagnostic) {
	c.diagMu.Lock()
	defer c.diagMu.Unlock()
	if len(diags) == 0 {
		delete(c.diags, uri)
	} else {
		c.diags[uri] = diags
	}
}

// DiagnosticsFor returns the cached diagnostics for a URI, or nil if none.
// Returns a defensive copy so callers can't race with storeDiagnostics.
func (c *Client) DiagnosticsFor(uri string) []Diagnostic {
	c.diagMu.RLock()
	defer c.diagMu.RUnlock()
	s := c.diags[uri]
	if len(s) == 0 {
		return nil
	}
	out := make([]Diagnostic, len(s))
	copy(out, s)
	return out
}

// OpenURIs returns a snapshot of currently-open URIs (for didClose before rename).
func (c *Client) OpenURIs() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]string, 0, len(c.openURIs))
	for uri := range c.openURIs {
		out = append(out, uri)
	}
	return out
}

// IsFileOpen reports whether the server has been notified about uri via didOpen.
// Lets callers skip stat+read+didOpen for already-opened files.
func (c *Client) IsFileOpen(uri string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := c.openURIs[uri]
	return ok
}
