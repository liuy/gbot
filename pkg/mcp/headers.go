// Package mcp implements the MCP (Model Context Protocol) client infrastructure.
// Source: src/services/mcp/ (22 files, ~12K lines TS)
//
// This file: headersHelper.ts (139 lines) — dynamic headers + trust gate.
package mcp

import (
	"context"
	"encoding/json"
	"log/slog"
	"maps"
	"os"
	"os/exec"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// Constants — Source: headersHelper.ts:61 (timeout: 10000)
// ---------------------------------------------------------------------------

// headerHelperTimeout is the maximum time to wait for a headersHelper script.
// Source: headersHelper.ts:61 — timeout: 10000
const headerHelperTimeout = 10 * time.Second

// ---------------------------------------------------------------------------
// IsProjectTrusted — Source: utils/config.ts:690-761 checkHasTrustDialogAccepted
// ---------------------------------------------------------------------------

// IsProjectTrusted reports whether the current workspace is trusted.
// The default implementation always returns false; the application should
// override this with its actual trust-checking logic.
//
// Source: headersHelper.ts:48 — checkHasTrustDialogAccepted()
// Source: utils/config.ts:690-761 — checkHasTrustDialogAccepted
var IsProjectTrusted = func() bool { return false }

// ---------------------------------------------------------------------------
// GetDynamicHeaders — Source: headersHelper.ts:32-117 getMcpHeadersFromHelper
// ---------------------------------------------------------------------------

// headersConfig is the subset of config fields needed for header resolution.
// SSEConfig, HTTPConfig, and WSConfig all share these fields.
type headersConfig interface {
	getHeadersHelper() string
	getURL() string
	getHeaders() map[string]string
}

// getHeadersConfig extracts the headers-related fields from an McpServerConfig.
// Returns nil if the config type doesn't support headers (e.g. StdioConfig).
func getHeadersConfig(cfg McpServerConfig) headersConfig {
	switch c := cfg.(type) {
	case *SSEConfig:
		return (*sseHeaders)(c)
	case *HTTPConfig:
		return (*httpHeaders)(c)
	case *WSConfig:
		return (*wsHeaders)(c)
	default:
		return nil
	}
}

// Thin wrapper types to implement headersConfig without changing the main structs.
type sseHeaders SSEConfig
type httpHeaders HTTPConfig
type wsHeaders WSConfig

func (c *sseHeaders) getHeadersHelper() string      { return c.HeadersHelper }
func (c *sseHeaders) getURL() string                 { return c.URL }
func (c *sseHeaders) getHeaders() map[string]string  { return c.Headers }

func (c *httpHeaders) getHeadersHelper() string      { return c.HeadersHelper }
func (c *httpHeaders) getURL() string                 { return c.URL }
func (c *httpHeaders) getHeaders() map[string]string  { return c.Headers }

func (c *wsHeaders) getHeadersHelper() string        { return c.HeadersHelper }
func (c *wsHeaders) getURL() string                   { return c.URL }
func (c *wsHeaders) getHeaders() map[string]string    { return c.Headers }

// GetDynamicHeaders executes the headersHelper script from the config to
// retrieve dynamic HTTP headers for an MCP server connection.
//
// Source: headersHelper.ts:32-117 — getMcpHeadersFromHelper
//
// Returns nil if no headersHelper is configured or if execution fails.
// Errors are logged but never returned — the connection should not be blocked
// by a headers helper failure.
func GetDynamicHeaders(ctx context.Context, serverName string, cfg McpServerConfig, scope ConfigScope, trusted bool) map[string]string {
	hc := getHeadersConfig(cfg)
	if hc == nil {
		return nil
	}

	helper := hc.getHeadersHelper()
	if helper == "" {
		return nil
	}

	// Source: headersHelper.ts:40-57 — trust gate for project/local scope
	// Skip trust check in non-interactive mode (handled by caller setting trusted=true).
	if isTrustRequired(scope) && !trusted {
		return nil
	}

	return executeHeadersHelper(ctx, serverName, hc.getURL(), helper)
}

// executeHeadersHelper runs the headersHelper command and parses its JSON output.
// Source: headersHelper.ts:59-117
func executeHeadersHelper(ctx context.Context, serverName, serverURL, helper string) map[string]string {
	// Security model: headersHelper is sourced from user MCP config files.
	// Only trusted configs are loaded (workspace trust gate in config.go),
	// so shell execution is acceptable here — matching TS shell:true behavior.
	// Source: headersHelper.ts:61-71 — execFileNoThrowWithCwd with shell:true, timeout:10000
	cmdCtx, cancel := context.WithTimeout(ctx, headerHelperTimeout)
	defer cancel()

	// Source: headersHelper.ts:62 — shell: true
	cmd := exec.CommandContext(cmdCtx, "sh", "-c", helper)
	// Ensure child processes are cleaned up on timeout.
	cmd.WaitDelay = 2 * time.Second

	// Source: headersHelper.ts:66-70 — env vars for server context
	cmd.Env = append(os.Environ(),
		"CLAUDE_CODE_MCP_SERVER_NAME="+serverName,
		"CLAUDE_CODE_MCP_SERVER_URL="+serverURL,
	)

	output, err := cmd.Output()
	if err != nil {
		slog.Warn("mcp: headers helper failed", "server", serverName, "error", err)
		return nil
	}

	// Source: headersHelper.ts:77 — trim stdout
	result := strings.TrimSpace(string(output))
	if result == "" {
		return nil
	}

	// Source: headersHelper.ts:79-88 — jsonParse + type validation
	var rawHeaders map[string]any
	if err := json.Unmarshal([]byte(result), &rawHeaders); err != nil {
		slog.Warn("mcp: headers helper returned invalid JSON", "server", serverName, "error", err)
		return nil
	}
	// Source: headersHelper.ts:80-84 — typeof headers !== 'object' || headers === null
	if rawHeaders == nil {
		return nil
	}

	// Source: headersHelper.ts:90-97 — validate all values are strings
	headers := make(map[string]string, len(rawHeaders))
	for key, value := range rawHeaders {
		s, ok := value.(string)
		if !ok {
			return nil
		}
		headers[key] = s
	}

	return headers
}

// ---------------------------------------------------------------------------
// GetMcpServerHeaders — Source: headersHelper.ts:125-138 getMcpServerHeaders
// ---------------------------------------------------------------------------

// GetMcpServerHeaders combines static headers from the config with dynamic
// headers from the headersHelper script. Dynamic headers override static ones
// on conflict.
//
// Source: headersHelper.ts:125-138 — getMcpServerHeaders
func GetMcpServerHeaders(ctx context.Context, serverName string, cfg McpServerConfig, scope ConfigScope, trusted bool) map[string]string {
	// Source: headersHelper.ts:129 — static headers from config
	var staticHeaders map[string]string
	hc := getHeadersConfig(cfg)
	if hc != nil {
		staticHeaders = hc.getHeaders()
	}
	if staticHeaders == nil {
		staticHeaders = make(map[string]string)
	}

	// Source: headersHelper.ts:130-131 — dynamic headers from helper
	dynamicHeaders := GetDynamicHeaders(ctx, serverName, cfg, scope, trusted)
	if dynamicHeaders == nil {
		dynamicHeaders = make(map[string]string)
	}

	// Source: headersHelper.ts:134-137 — spread static then dynamic (dynamic overrides)
	combined := make(map[string]string, len(staticHeaders)+len(dynamicHeaders))
	maps.Copy(combined, staticHeaders)
	maps.Copy(combined, dynamicHeaders)

	return combined
}
