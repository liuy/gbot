//go:build !linux

package computer

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/liuy/gbot/pkg/mcp"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// serverName is the constant MCP server name the CuaBackend registers its
// cua-driver stdio connection under. Mirror of cua_backend.py's hardcoded
// "cua-driver" reference used throughout the lifecycle.
const serverName = "cua-driver"

// telemetryEnv is the env var cua-driver reads to gate its anonymous usage
// telemetry (PostHog). Setting it to "0" disables telemetry. Translate of
// cua_backend.py:92 `_CUA_TELEMETRY_ENV_VAR`.
const telemetryEnv = "CUA_DRIVER_RS_TELEMETRY_ENABLED"

// fallbackMCPArgs is the default cua-driver stdio MCP subcommand, used when
// `cua-driver manifest` discovery fails or returns nothing usable.
var fallbackMCPArgs = []string{"mcp"}

// timeAfter/timeDuration package vars removed (R1): the wait action and its
// sleep indirection are dropped from all platforms.

// cuaDriverInstallHint returns the actionable install hint shown when the
// cua-driver binary cannot be found. Translate of
// cua_backend.py:286-303 `cua_driver_install_hint`.
func cuaDriverInstallHint() string {
	var installer string
	if runtime.GOOS == "windows" {
		installer = "  irm https://raw.githubusercontent.com/trycua/cua/main/" +
			"libs/cua-driver/scripts/install.ps1 | iex"
	} else {
		installer = "  /bin/bash -c \"$(curl -fsSL " +
			"https://raw.githubusercontent.com/trycua/cua/main/" +
			"libs/cua-driver/scripts/install.sh)\""
	}
	return "cua-driver is not installed. Install with one of:\n" +
		"  hermes computer-use install\n" +
		"Or run the upstream installer directly:\n" +
		installer + "\n"
}

// resolveDriverCmd returns the cua-driver command path, honoring the
// `GBOT_CUA_DRIVER_CMD` env override (the gbot equivalent of Hermes'
// `HERMES_CUA_DRIVER_CMD`). Falls back to `exec.LookPath("cua-driver")`.
// Returns the actionable install hint as the error text on miss.
func resolveDriverCmd() (string, error) {
	if override := os.Getenv("GBOT_CUA_DRIVER_CMD"); override != "" {
		return override, nil
	}
	path, err := exec.LookPath("cua-driver")
	if err != nil {
		return "", errors.New(cuaDriverInstallHint())
	}
	return path, nil
}

// resolveMCPInvocation asks cua-driver itself which subcommand spawns the
// MCP stdio server, instead of hardcoding ["mcp"]. Translate of
// cua_backend.py:159-208 `_resolve_mcp_invocation`.
//
// Runs `cua-driver manifest` (trycua/cua#1961), parses mcp_invocation.{command,args},
// falls back to (driverCmd, ["mcp"]) for older drivers that don't expose
// manifest or any indeterminate failure — the wrapper must not refuse to
// start just because discovery failed.
func resolveMCPInvocation(driverCmd string) (string, []string) {
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()

	proc := exec.CommandContext(ctx, driverCmd, "manifest")
	proc.Stdin = nil
	out, err := proc.Output()
	if err != nil {
		return driverCmd, fallbackMCPArgs
	}
	out = bytes.TrimSpace(out)
	if len(out) == 0 {
		return driverCmd, fallbackMCPArgs
	}

	var manifest struct {
		MCPInvocation struct {
			Command string   `json:"command"`
			Args    []string `json:"args"`
		} `json:"mcp_invocation"`
	}
	if err := json.Unmarshal(out, &manifest); err != nil {
		return driverCmd, fallbackMCPArgs
	}
	inv := manifest.MCPInvocation
	if len(inv.Args) == 0 {
		return driverCmd, fallbackMCPArgs
	}
	if inv.Command == "" {
		// Driver knows the args but didn't surface its own path —
		// keep our resolved driverCmd; the args are still authoritative.
		return driverCmd, inv.Args
	}
	return inv.Command, inv.Args
}

// childEnv builds the environment for the cua-driver child process.
// Translate of cua_backend.py:99-114 `cua_driver_child_env`: start from the
// parent env, inject telemetryEnv=0 by default (mirrors Hermes'
// `_cua_telemetry_disabled` failing safe toward telemetry OFF), and merge in
// DISPLAY for Linux (set by detectDisplay in ensureStarted). The plan's
// hardening section leaves config-driven opt-in for a future task, so the
// default here is always-disable.
func childEnv(extra map[string]string) map[string]string {
	env := map[string]string{}
	for _, kv := range os.Environ() {
		if eq := strings.IndexByte(kv, '='); eq > 0 {
			env[kv[:eq]] = kv[eq+1:]
		}
	}
	env[telemetryEnv] = "0"
	maps.Copy(env, extra)
	return env
}

// mapToEnvSlice renders an env map as the "key=value" slice exec.Cmd.Env
// expects. Keys are sorted for deterministic output (helps reproducible test
// fixtures; order has no runtime effect).
func mapToEnvSlice(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, k+"="+m[k])
	}
	return out
}

// CuaBackend owns a single long-lived cua-driver stdio MCP session, lazily
// connected on first call and kept alive for the process lifetime. mac/windows
// only — linux uses X11Backend. Translate of cua_backend.py:339-470
// `_CuaDriverSession` + `CuaDriverBackend.__init__`/`start`/`stop`,
// re-implemented against gbot's existing MCP client stack (pkg/mcp/) instead
// of the Python `mcp` SDK.
type CuaBackend struct {
	mu sync.Mutex

	cmd       string // resolved via resolveDriverCmd once at first start
	mgr       *mcp.ClientManager
	cfg       mcp.ScopedMcpServerConfig
	conn      *mcp.ConnectedServer
	sessionID string // minted once per CuaBackend instance
	started   bool
	winCache  map[int]int // window_id → pid, refreshed by list/snapshot
	// snapTokens/snapshotID removed (R3): element-based click dropped, so the
	// per-snapshot token cache is no longer needed.
}

// NewCuaBackend constructs a CuaBackend with a freshly-minted session id.
// Translate of CuaDriverBackend.__init__'s `self._session_id = f"hermes-{uuid.uuid4().hex[:12]}"`.
func NewCuaBackend() *CuaBackend {
	return &CuaBackend{
		sessionID: "gbot-" + randomHex(12),
		winCache:  map[int]int{},
	}
}

// randomHex returns n hex characters of crypto-strong randomness. Used for
// the session id suffix so concurrent gbot runs get distinct cua-driver
// agent-cursor colors (trycua/cua#1961 session semantics).
func randomHex(n int) string {
	b := make([]byte, (n+1)/2)
	if _, err := rand.Read(b); err != nil {
		// rand.Read only fails when the system entropy source is unavailable,
		// which is fatal for the process anyway. Fall back to a low-quality
		// id rather than panic so a flaky entropy source can't wedge the tool.
		return fmt.Sprintf("%x", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)[:n]
}

// ensureStarted lazily opens the cua-driver stdio MCP session on first call.
// Translate of cua_backend.py:426-470 `_CuaDriverSession.start` +
// `CuaDriverBackend.start`: build the ClientManager + ScopedMcpServerConfig
// (trusted=true, ScopeUser avoids the stdio trust gate at transport.go:301),
// connect, and call `start_session` so cua-driver owns this run's
// agent-cursor + per-session state. A failed start clears state so the next
// call retries cleanly (Hermes `_get_backend` semantics).
func (b *CuaBackend) ensureStarted(ctx context.Context) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.started {
		return nil
	}

	cmd, err := resolveDriverCmd()
	if err != nil {
		return err
	}
	b.cmd = cmd

	// Resolve DISPLAY for Linux before spawning so the child sees it.
	display, _ := detectDisplay()
	slog.Info("computer: ensureStarted", "display", display, "os_DISPLAY", os.Getenv("DISPLAY"))
	extra := map[string]string{}
	if display != "" {
		extra["DISPLAY"] = display
	}

	mcpCmd, mcpArgs := resolveMCPInvocation(cmd)
	b.mgr = mcp.NewClientManager(mcp.TransportFactory{}, true, "") // trusted=true — mirrors registry.go:468
	b.cfg = mcp.ScopedMcpServerConfig{
		Config: &mcp.StdioConfig{Command: mcpCmd, Args: mcpArgs, Env: childEnv(extra)},
		Scope:  mcp.ScopeUser, // avoids the stdio trust gate at transport.go:301
	}

	if err := b.connectLocked(ctx); err != nil {
		// Clear partial state so a retry rebuilds from scratch.
		b.conn = nil
		b.mgr = nil
		return err
	}
	b.started = true

	// Declare this run's session identity to cua-driver. From the cua-driver
	// server instructions: "start_session(session) once at the start of a run
	// → declares THIS run's identity. Pass that same session on every action
	// below. It owns your agent cursor (a distinct color per id)." Failure
	// is non-fatal — cua-driver's tools accept anonymous calls.
	if _, err := b.callLocked(ctx, "start_session", map[string]any{"session": b.sessionID}); err != nil {
		slog.Debug("computer: cua-driver start_session failed (continuing anonymous)", "err", err)
	}
	return nil
}

// connectLocked opens the MCP session. Caller must hold b.mu.
func (b *CuaBackend) connectLocked(ctx context.Context) error {
	result, err := b.mgr.ConnectToServer(ctx, serverName, b.cfg)
	if err != nil {
		return fmt.Errorf("computer: connect cua-driver: %w", err)
	}
	conn, ok := result.(*mcp.ConnectedServer)
	if !ok {
		return fmt.Errorf("computer: cua-driver connection state: %s", result.ConnType())
	}
	b.conn = conn
	return nil
}

// call invokes a cua-driver MCP tool by name. On a closed-session error it
// invalidates the cached dead connection (so ConnectToServer actually
// reconnects — without this the cached entry is returned, see client.go:152),
// reconnects, and retries exactly once. Translate of
// cua_backend.py:536-548 `call_tool` (the reconnect-once path).
// Callers that already hold b.mu use callLocked.
func (b *CuaBackend) call(ctx context.Context, toolName string, args map[string]any) (*mcp.MCPToolCallResult, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.callLocked(ctx, toolName, args)
}

func (b *CuaBackend) callLocked(ctx context.Context, toolName string, args map[string]any) (*mcp.MCPToolCallResult, error) {
	res, err := mcp.CallMCPTool(ctx, mcp.CallMCPToolParams{
		Server:   b.conn,
		ToolName: toolName,
		Args:     args,
	})
	if err == nil {
		return res, nil
	}
	if !isClosedSessionError(err) {
		return nil, err
	}
	slog.Warn("computer: cua-driver MCP session closed; reconnecting once", "tool", toolName)
	// Mirror registry.go:283 — drop the cached dead entry before reconnect.
	b.mgr.InvalidateCache(serverName, b.cfg)
	b.conn = nil
	if err := b.connectLocked(ctx); err != nil {
		return nil, fmt.Errorf("computer: reconnect cua-driver: %w", err)
	}
	return mcp.CallMCPTool(ctx, mcp.CallMCPToolParams{
		Server:   b.conn,
		ToolName: toolName,
		Args:     args,
	})
}

// isClosedSessionError reports whether err is the kind of MCP/stdio failure
// that's recoverable by reconnecting. Translate of cua_backend.py:509-518
// `_is_closed_session_error`. gbot's MCP client surfaces transport tears as
// generic errors with substring signatures; match those rather than the
// Python anyio exception class names.
func isClosedSessionError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	for _, sig := range []string{
		"closed", "broken pipe", "EOF", "no such file", "session not found",
		"connection reset",
	} {
		if strings.Contains(msg, sig) {
			return true
		}
	}
	return false
}

// Stop tears the cua-driver session down (best-effort end_session), then
// drops the MCP connection. Translate of cua_backend.py:472-486
// `CuaDriverBackend.stop`.
func (b *CuaBackend) Stop() {
	b.mu.Lock()
	conn := b.conn
	started := b.started
	b.mu.Unlock()
	if !started || conn == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := b.call(ctx, "end_session", map[string]any{"session": b.sessionID}); err != nil {
		slog.Debug("computer: cua-driver end_session failed (continuing teardown)", "err", err)
	}
	if err := conn.Close(); err != nil {
		slog.Debug("computer: cua-driver connection close failed", "err", err)
	}
	b.mu.Lock()
	b.started = false
	b.conn = nil
	b.mu.Unlock()
}

// resolvePID returns the process pid for a window_id. It reads the winCache
// first (warmed by list/snapshot); on a miss it falls back to a list_windows
// round-trip, repopulating the cache for all returned windows. This is the
// slow path taken only when the cache is cold or stale.
func (b *CuaBackend) resolvePID(ctx context.Context, windowID int) (int, error) {
	b.mu.Lock()
	if pid, ok := b.winCache[windowID]; ok {
		b.mu.Unlock()
		return pid, nil
	}
	b.mu.Unlock()

	out, err := b.call(ctx, "list_windows", map[string]any{
		"on_screen_only": false, // off-screen windows resolve too
		"session":        b.sessionID,
	})
	if err != nil {
		return 0, fmt.Errorf("list_windows: %w", err)
	}
	windows := parseWindows(extractResult(out))
	b.mu.Lock()
	if b.winCache == nil {
		b.winCache = map[int]int{}
	}
	for _, w := range windows {
		b.winCache[w.WindowID] = w.PID
	}
	pid, ok := b.winCache[windowID]
	b.mu.Unlock()
	if !ok {
		return 0, fmt.Errorf("window_id %d not found; call list to refresh", windowID)
	}
	return pid, nil
}

// extractResult converts an *mcp.MCPToolCallResult into the plain map shape
// the rest of the Backend consumes. This is a re-implementation against
// gbot types, NOT a literal port of cua_backend.py:565-608
// `_extract_tool_result`. gbot's CallMCPTool returns typed Content values
// (*mcp.TextContent, *mcp.ImageContent) plus StructuredContent and Meta, so
// we type-switch over Content (concat text, surface image fields) and merge
// StructuredContent — no raw dict walking, no isError re-parse (CallMCPTool
// already turns isError results into McpToolCallError).
//
// Output keys mirror _extract_tool_result so downstream code reads the same
// field names:
//
//	"data"             — string text, or parsed JSON when text starts with { or [
//	"images"           — []string base64 image data (one per image part)
//	"image_mime_types" — []string parallel to images ("" when part had no mimeType)
//	"structuredContent" — map[string]any when the SDK surfaced one
func extractResult(res *mcp.MCPToolCallResult) map[string]any {
	if res == nil {
		return map[string]any{}
	}
	out := map[string]any{
		"images":           []string{},
		"image_mime_types": []string{},
	}
	var textChunks []string
	for _, part := range res.Content {
		switch c := part.(type) {
		case *mcpsdk.TextContent:
			if c.Text != "" {
				textChunks = append(textChunks, c.Text)
			}
		case *mcpsdk.ImageContent:
			images, _ := out["images"].([]string)
			mimes, _ := out["image_mime_types"].([]string)
			// c.Data is raw bytes; the wire format is base64, so re-encode.
			images = append(images, encodeBase64(c.Data))
			mimes = append(mimes, c.MIMEType)
			out["images"] = images
			out["image_mime_types"] = mimes
		}
	}
	if len(textChunks) > 0 {
		joined := strings.Join(textChunks, "\n")
		trimmed := strings.TrimSpace(joined)
		if strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") {
			var parsed any
			if json.Unmarshal([]byte(trimmed), &parsed) == nil {
				out["data"] = parsed
			} else {
				out["data"] = joined
			}
		} else {
			out["data"] = joined
		}
	}
	if res.StructuredContent != nil {
		if sc, ok := res.StructuredContent.(map[string]any); ok {
			out["structuredContent"] = sc
		} else {
			// Non-map structured payload (rare) — round-trip through JSON to
			// normalize into a map so downstream field access is uniform.
			raw, err := json.Marshal(res.StructuredContent)
			if err == nil {
				var m map[string]any
				if json.Unmarshal(raw, &m) == nil {
					out["structuredContent"] = m
				}
			}
		}
	}
	return out
}

// encodeBase64 returns the base64 (StdEncoding) form of b. Wraps std lib so
// the image path has a single encoding call site.
func encodeBase64(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	const table = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	out := make([]byte, 0, ((len(b)+2)/3)*4)
	for i := 0; i < len(b); i += 3 {
		end := min(i+3, len(b))
		chunk := make([]byte, 3)
		copy(chunk, b[i:end])
		c0 := chunk[0] >> 2
		c1 := ((chunk[0] & 0x03) << 4) | (chunk[1] >> 4)
		c2 := ((chunk[1] & 0x0f) << 2) | (chunk[2] >> 6)
		c3 := chunk[2] & 0x3f
		out = append(out, table[c0], table[c1])
		switch end - i {
		case 1:
			out = append(out, '=', '=')
		case 2:
			out = append(out, table[c2], '=')
		default:
			out = append(out, table[c2], table[c3])
		}
	}
	return string(out)
}
