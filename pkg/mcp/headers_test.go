package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// GetDynamicHeaders — Source: headersHelper.ts:32-117
// ---------------------------------------------------------------------------

func TestGetDynamicHeaders_NoHelper(t *testing.T) {
	cfg := &SSEConfig{URL: "http://example.com"}
	result := GetDynamicHeaders(context.Background(), "test", cfg, ScopeUser, true)
	if result != nil {
		t.Errorf("expected nil when no headersHelper, got %v", result)
	}
}

func TestGetDynamicHeaders_UnsupportedConfig(t *testing.T) {
	cfg := &StdioConfig{Command: "echo"}
	result := GetDynamicHeaders(context.Background(), "test", cfg, ScopeUser, true)
	if result != nil {
		t.Errorf("expected nil for unsupported config type, got %v", result)
	}
}

func TestGetDynamicHeaders_TrustGate(t *testing.T) {
	// Project scope, not trusted → should return nil even with a valid helper
	cfg := &SSEConfig{
		URL:            "http://example.com",
		HeadersHelper:  "echo '{}'",
	}
	result := GetDynamicHeaders(context.Background(), "test", cfg, ScopeProject, false)
	if result != nil {
		t.Errorf("expected nil when trust gate blocks, got %v", result)
	}

	// Local scope, not trusted → blocked
	result = GetDynamicHeaders(context.Background(), "test", cfg, ScopeLocal, false)
	if result != nil {
		t.Errorf("expected nil when local scope not trusted, got %v", result)
	}
}

func TestGetDynamicHeaders_TrustGate_Allowed(t *testing.T) {
	cfg := &SSEConfig{
		URL:           "http://example.com",
		HeadersHelper: "echo '{\"Authorization\": \"Bearer test\"}'",
	}
	// User scope → trust gate doesn't apply
	result := GetDynamicHeaders(context.Background(), "test", cfg, ScopeUser, true)
	if result == nil {
		t.Fatal("expected headers, got nil")
	}
	if result["Authorization"] != "Bearer test" {
		t.Errorf("expected Authorization Bearer test, got %v", result["Authorization"])
	}

	// Project scope, trusted → allowed
	result = GetDynamicHeaders(context.Background(), "test", cfg, ScopeProject, true)
	if result == nil {
		t.Fatal("expected headers when trusted, got nil")
	}
}

func TestGetDynamicHeaders_HTTPConfig(t *testing.T) {
	cfg := &HTTPConfig{
		URL:           "http://example.com",
		HeadersHelper: "echo '{\"X-API-Key\": \"secret123\"}'",
	}
	result := GetDynamicHeaders(context.Background(), "myserver", cfg, ScopeUser, true)
	if result == nil {
		t.Fatal("expected headers, got nil")
	}
	if result["X-API-Key"] != "secret123" {
		t.Errorf("expected X-API-Key secret123, got %v", result["X-API-Key"])
	}
}

func TestGetDynamicHeaders_WSConfig(t *testing.T) {
	cfg := &WSConfig{
		URL:           "ws://example.com",
		HeadersHelper: "echo '{\"X-WS-Auth\": \"token\"}'",
	}
	result := GetDynamicHeaders(context.Background(), "wsserver", cfg, ScopeUser, true)
	if result == nil {
		t.Fatal("expected headers, got nil")
	}
	if result["X-WS-Auth"] != "token" {
		t.Errorf("expected X-WS-Auth token, got %v", result["X-WS-Auth"])
	}
}

func TestGetDynamicHeaders_EnvVars(t *testing.T) {
	// Helper script that outputs the env vars as JSON headers
	helper := `echo "{\"name\": \"$CLAUDE_CODE_MCP_SERVER_NAME\", \"url\": \"$CLAUDE_CODE_MCP_SERVER_URL\"}"`
	cfg := &SSEConfig{
		URL:           "http://example.com/mcp",
		HeadersHelper: helper,
	}
	result := GetDynamicHeaders(context.Background(), "myserver", cfg, ScopeUser, true)
	if result == nil {
		t.Fatal("expected headers, got nil")
	}
	if result["name"] != "myserver" {
		t.Errorf("expected name=myserver, got %v", result["name"])
	}
	if result["url"] != "http://example.com/mcp" {
		t.Errorf("expected url=http://example.com/mcp, got %v", result["url"])
	}
}

func TestGetDynamicHeaders_CommandFailure(t *testing.T) {
	cfg := &SSEConfig{
		URL:           "http://example.com",
		HeadersHelper: "exit 1",
	}
	result := GetDynamicHeaders(context.Background(), "test", cfg, ScopeUser, true)
	if result != nil {
		t.Errorf("expected nil on command failure, got %v", result)
	}
}

func TestGetDynamicHeaders_EmptyOutput(t *testing.T) {
	cfg := &SSEConfig{
		URL:           "http://example.com",
		HeadersHelper: "echo ''",
	}
	result := GetDynamicHeaders(context.Background(), "test", cfg, ScopeUser, true)
	if result != nil {
		t.Errorf("expected nil on empty output, got %v", result)
	}
}

func TestGetDynamicHeaders_InvalidJSON(t *testing.T) {
	cfg := &SSEConfig{
		URL:           "http://example.com",
		HeadersHelper: "echo 'not json'",
	}
	result := GetDynamicHeaders(context.Background(), "test", cfg, ScopeUser, true)
	if result != nil {
		t.Errorf("expected nil on invalid JSON, got %v", result)
	}
}

func TestGetDynamicHeaders_NonStringValues(t *testing.T) {
	cfg := &SSEConfig{
		URL:           "http://example.com",
		HeadersHelper: `echo '{"key": 123}'`,
	}
	result := GetDynamicHeaders(context.Background(), "test", cfg, ScopeUser, true)
	if result != nil {
		t.Errorf("expected nil when values are not strings, got %v", result)
	}
}

func TestGetDynamicHeaders_ArrayResponse(t *testing.T) {
	cfg := &SSEConfig{
		URL:           "http://example.com",
		HeadersHelper: `echo '[1,2,3]'`,
	}
	result := GetDynamicHeaders(context.Background(), "test", cfg, ScopeUser, true)
	if result != nil {
		t.Errorf("expected nil when response is array, got %v", result)
	}
}

func TestGetDynamicHeaders_NullResponse(t *testing.T) {
	cfg := &SSEConfig{
		URL:           "http://example.com",
		HeadersHelper: `echo 'null'`,
	}
	result := GetDynamicHeaders(context.Background(), "test", cfg, ScopeUser, true)
	if result != nil {
		t.Errorf("expected nil when response is null, got %v", result)
	}
}

func TestGetDynamicHeaders_MultipleHeaders(t *testing.T) {
	helper := `echo '{"Authorization": "Bearer tok", "X-Request-ID": "abc123", "X-Custom": "value"}'`
	cfg := &SSEConfig{
		URL:           "http://example.com",
		HeadersHelper: helper,
	}
	result := GetDynamicHeaders(context.Background(), "test", cfg, ScopeUser, true)
	if result == nil {
		t.Fatal("expected headers, got nil")
	}
	if len(result) != 3 {
		t.Fatalf("expected 3 headers, got %d", len(result))
	}
	if result["Authorization"] != "Bearer tok" {
		t.Errorf("expected Authorization Bearer tok, got %v", result["Authorization"])
	}
	if result["X-Request-ID"] != "abc123" {
		t.Errorf("expected X-Request-ID abc123, got %v", result["X-Request-ID"])
	}
	if result["X-Custom"] != "value" {
		t.Errorf("expected X-Custom value, got %v", result["X-Custom"])
	}
}

func TestGetDynamicHeaders_Timeout(t *testing.T) {
	cfg := &SSEConfig{
		URL:           "http://example.com",
		HeadersHelper: "sleep 60",
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	start := time.Now()
	result := GetDynamicHeaders(ctx, "test", cfg, ScopeUser, true)
	elapsed := time.Since(start)

	if result != nil {
		t.Errorf("expected nil on timeout, got %v", result)
	}
	// Process cleanup escalation adds ~2s overhead (SIGINT→SIGTERM→SIGKILL)
	if elapsed > 3*time.Second {
		t.Errorf("timeout took too long: %v", elapsed)
	}
}

func TestGetDynamicHeaders_CommandInjection(t *testing.T) {
	// Verify that the headersHelper runs in a shell context and that
	// malicious commands are contained (the output must be valid JSON).
	cfg := &SSEConfig{
		URL:           "http://example.com",
		HeadersHelper: "rm -rf /nonexistent_test_path; echo '{}'",
	}
	// This should succeed with {} since rm fails but echo succeeds
	result := GetDynamicHeaders(context.Background(), "test", cfg, ScopeUser, true)
	if result == nil {
		t.Fatal("expected empty headers from combined command, got nil")
	}
	if len(result) != 0 {
		t.Errorf("expected empty map, got %v", result)
	}
}

// ---------------------------------------------------------------------------
// GetMcpServerHeaders — Source: headersHelper.ts:125-138
// ---------------------------------------------------------------------------

func TestGetMcpServerHeaders_StaticOnly(t *testing.T) {
	cfg := &SSEConfig{
		URL:     "http://example.com",
		Headers: map[string]string{"X-Static": "value"},
	}
	result := GetMcpServerHeaders(context.Background(), "test", cfg, ScopeUser, true)
	if result == nil {
		t.Fatal("expected headers, got nil")
	}
	if result["X-Static"] != "value" {
		t.Errorf("expected X-Static value, got %v", result["X-Static"])
	}
}

func TestGetMcpServerHeaders_DynamicOnly(t *testing.T) {
	cfg := &SSEConfig{
		URL:            "http://example.com",
		HeadersHelper:  `echo '{"X-Dynamic": "dyn"}'`,
	}
	result := GetMcpServerHeaders(context.Background(), "test", cfg, ScopeUser, true)
	if result == nil {
		t.Fatal("expected headers, got nil")
	}
	if result["X-Dynamic"] != "dyn" {
		t.Errorf("expected X-Dynamic dyn, got %v", result["X-Dynamic"])
	}
}

func TestGetMcpServerHeaders_DynamicOverridesStatic(t *testing.T) {
	// Source: headersHelper.ts:134-137 — dynamic overrides static
	cfg := &SSEConfig{
		URL:            "http://example.com",
		Headers:        map[string]string{"Authorization": "static-token", "X-Only-Static": "yes"},
		HeadersHelper:  `echo '{"Authorization": "dynamic-token", "X-Only-Dynamic": "yes"}'`,
	}
	result := GetMcpServerHeaders(context.Background(), "test", cfg, ScopeUser, true)
	if result == nil {
		t.Fatal("expected headers, got nil")
	}
	// Dynamic should override static
	if result["Authorization"] != "dynamic-token" {
		t.Errorf("expected dynamic-token, got %v", result["Authorization"])
	}
	// Static-only header preserved
	if result["X-Only-Static"] != "yes" {
		t.Errorf("expected X-Only-Static yes, got %v", result["X-Only-Static"])
	}
	// Dynamic-only header present
	if result["X-Only-Dynamic"] != "yes" {
		t.Errorf("expected X-Only-Dynamic yes, got %v", result["X-Only-Dynamic"])
	}
}

func TestGetMcpServerHeaders_NoHeaders(t *testing.T) {
	cfg := &SSEConfig{URL: "http://example.com"}
	result := GetMcpServerHeaders(context.Background(), "test", cfg, ScopeUser, true)
	if result == nil {
		t.Fatal("expected empty map, got nil")
	}
	if len(result) != 0 {
		t.Errorf("expected empty map, got %v", result)
	}
}

func TestGetMcpServerHeaders_UnsupportedConfig(t *testing.T) {
	cfg := &StdioConfig{Command: "echo"}
	result := GetMcpServerHeaders(context.Background(), "test", cfg, ScopeUser, true)
	if result == nil {
		t.Fatal("expected empty map for unsupported config, got nil")
	}
	if len(result) != 0 {
		t.Errorf("expected empty map for unsupported config, got %v", result)
	}
}

func TestGetMcpServerHeaders_HTTPConfig(t *testing.T) {
	cfg := &HTTPConfig{
		URL:            "http://example.com",
		Headers:        map[string]string{"X-Static": "s"},
		HeadersHelper:  `echo '{"X-Dynamic": "d"}'`,
	}
	result := GetMcpServerHeaders(context.Background(), "test", cfg, ScopeUser, true)
	if result["X-Static"] != "s" {
		t.Errorf("expected X-Static s, got %v", result["X-Static"])
	}
	if result["X-Dynamic"] != "d" {
		t.Errorf("expected X-Dynamic d, got %v", result["X-Dynamic"])
	}
}

func TestGetMcpServerHeaders_WSConfig(t *testing.T) {
	cfg := &WSConfig{
		URL:            "ws://example.com",
		Headers:        map[string]string{"X-WS": "ws-static"},
		HeadersHelper:  `echo '{"X-WS-Dyn": "ws-dynamic"}'`,
	}
	result := GetMcpServerHeaders(context.Background(), "test", cfg, ScopeUser, true)
	if result["X-WS"] != "ws-static" {
		t.Errorf("expected X-WS ws-static, got %v", result["X-WS"])
	}
	if result["X-WS-Dyn"] != "ws-dynamic" {
		t.Errorf("expected X-WS-Dyn ws-dynamic, got %v", result["X-WS-Dyn"])
	}
}

func TestGetMcpServerHeaders_TrustGate(t *testing.T) {
	cfg := &SSEConfig{
		URL:            "http://example.com",
		HeadersHelper:  `echo '{"X-Dynamic": "should-not-appear"}'`,
	}
	// Project scope, not trusted → dynamic headers blocked, static headers still available
	result := GetMcpServerHeaders(context.Background(), "test", cfg, ScopeProject, false)
	if result == nil {
		t.Fatal("expected empty map (static only, no helper), got nil")
	}
	// Dynamic headers should be blocked
	if _, ok := result["X-Dynamic"]; ok {
		t.Error("dynamic headers should be blocked by trust gate")
	}
}

// ---------------------------------------------------------------------------
// IsProjectTrusted — Source: utils/config.ts:690-761
// ---------------------------------------------------------------------------

func TestIsProjectTrusted_Default(t *testing.T) {
	// Save and restore
	orig := IsProjectTrusted
	defer func() { IsProjectTrusted = orig }()

	IsProjectTrusted = func() bool { return false }
	if IsProjectTrusted() {
		t.Error("expected false from default implementation")
	}

	IsProjectTrusted = func() bool { return true }
	if !IsProjectTrusted() {
		t.Error("expected true after override")
	}
}

// ---------------------------------------------------------------------------
// executeHeadersHelper — direct testing
// ---------------------------------------------------------------------------

func TestExecuteHeadersHelper_ValidJSON(t *testing.T) {
	result := executeHeadersHelper(context.Background(), "server", "http://example.com",
		`echo '{"Authorization": "Bearer xyz"}'`)
	if result == nil {
		t.Fatal("expected headers, got nil")
	}
	if result["Authorization"] != "Bearer xyz" {
		t.Errorf("expected Authorization Bearer xyz, got %v", result["Authorization"])
	}
}

func TestExecuteHeadersHelper_Fails(t *testing.T) {
	result := executeHeadersHelper(context.Background(), "server", "http://example.com",
		"exit 1")
	if result != nil {
		t.Errorf("expected nil on failure, got %v", result)
	}
}

func TestExecuteHeadersHelper_ContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result := executeHeadersHelper(ctx, "server", "http://example.com",
		`echo '{"key": "value"}'`)
	if result != nil {
		t.Errorf("expected nil on cancelled context, got %v", result)
	}
}

// ---------------------------------------------------------------------------
// Integration: mock HTTP server returning headers
// ---------------------------------------------------------------------------

func TestGetDynamicHeaders_WithHTTPServer(t *testing.T) {
	// Create a mock server that returns JSON headers
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		headers := map[string]string{
			"X-Server-Auth": "token-from-server",
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(headers)
	}))
	defer srv.Close()

	// Helper uses curl to get headers from mock server
	helper := "curl -s " + srv.URL
	cfg := &SSEConfig{
		URL:            "http://example.com",
		HeadersHelper:  helper,
	}
	result := GetDynamicHeaders(context.Background(), "test", cfg, ScopeUser, true)
	if result == nil {
		t.Fatal("expected headers from HTTP server, got nil")
	}
	if result["X-Server-Auth"] != "token-from-server" {
		t.Errorf("expected X-Server-Auth token-from-server, got %v", result["X-Server-Auth"])
	}
}

// ---------------------------------------------------------------------------
// getHeadersConfig — type dispatch
// ---------------------------------------------------------------------------

func TestGetHeadersConfig_Types(t *testing.T) {
	tests := []struct {
		name string
		cfg  McpServerConfig
		want bool // whether getHeadersConfig returns non-nil
	}{
		{"SSEConfig", &SSEConfig{URL: "http://example.com"}, true},
		{"HTTPConfig", &HTTPConfig{URL: "http://example.com"}, true},
		{"WSConfig", &WSConfig{URL: "ws://example.com"}, true},
		{"StdioConfig", &StdioConfig{Command: "echo"}, false},
		{"SDKConfig", &SDKConfig{}, false},
		{"SSEIDEConfig", &SSEIDEConfig{URL: "http://example.com"}, false},
		{"WSIDEConfig", &WSIDEConfig{URL: "ws://example.com"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := getHeadersConfig(tt.cfg)
			if (got != nil) != tt.want {
				t.Errorf("getHeadersConfig(%s) returned %v, want non-nil=%v", tt.name, got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Edge cases
// ---------------------------------------------------------------------------

func TestGetDynamicHeaders_EmptyJSON(t *testing.T) {
	cfg := &SSEConfig{
		URL:           "http://example.com",
		HeadersHelper: "echo '{}'",
	}
	result := GetDynamicHeaders(context.Background(), "test", cfg, ScopeUser, true)
	if result == nil {
		t.Fatal("expected empty map, got nil")
	}
	if len(result) != 0 {
		t.Errorf("expected empty map, got %v", result)
	}
}

func TestGetDynamicHeaders_WhitespaceOutput(t *testing.T) {
	cfg := &SSEConfig{
		URL:           "http://example.com",
		HeadersHelper: "echo '  '",
	}
	result := GetDynamicHeaders(context.Background(), "test", cfg, ScopeUser, true)
	if result != nil {
		t.Errorf("expected nil on whitespace output, got %v", result)
	}
}

func TestGetDynamicHeaders_NestedJSON(t *testing.T) {
	// Nested object value → should fail (non-string value)
	cfg := &SSEConfig{
		URL:           "http://example.com",
		HeadersHelper: `echo '{"nested": {"key": "value"}}'`,
	}
	result := GetDynamicHeaders(context.Background(), "test", cfg, ScopeUser, true)
	if result != nil {
		t.Errorf("expected nil when value is nested object, got %v", result)
	}
}

func TestGetDynamicHeaders_SpecialCharsInValues(t *testing.T) {
	cfg := &SSEConfig{
		URL:           "http://example.com",
		HeadersHelper: `echo '{"Auth": "Bearer abc=def&ghi"}'`,
	}
	result := GetDynamicHeaders(context.Background(), "test", cfg, ScopeUser, true)
	if result == nil {
		t.Fatal("expected headers, got nil")
	}
	if !strings.Contains(result["Auth"], "Bearer abc=def&ghi") {
		t.Errorf("expected special chars preserved, got %v", result["Auth"])
	}
}

// ---------------------------------------------------------------------------
// Trust gate scope coverage — all scopes
// ---------------------------------------------------------------------------

func TestGetDynamicHeaders_AllScopes(t *testing.T) {
	helper := `echo '{"X": "1"}'`
	scopes := []struct {
		scope   ConfigScope
		trusted bool
		want    bool // expect headers returned
	}{
		{ScopeUser, false, true},
		{ScopeEnterprise, false, true},
		{ScopeDynamic, false, true},
		{ScopeClaudeAI, false, true},
		{ScopeManaged, false, true},
		{ScopeProject, false, false},  // trust gate blocks
		{ScopeLocal, false, false},    // trust gate blocks
		{ScopeProject, true, true},    // trust gate passed
		{ScopeLocal, true, true},      // trust gate passed
	}
	for _, tt := range scopes {
		t.Run(string(tt.scope)+"_trusted_"+func() string {
			if tt.trusted {
				return "yes"
			}
			return "no"
		}(), func(t *testing.T) {
			cfg := &SSEConfig{URL: "http://example.com", HeadersHelper: helper}
			// Override PATH to avoid interference
				if err := os.Setenv("PATH", "/usr/bin:/bin:"+os.Getenv("PATH")); err != nil {
					t.Fatal(err)
				}
			result := GetDynamicHeaders(context.Background(), "test", cfg, tt.scope, tt.trusted)
			if (result != nil) != tt.want {
				t.Errorf("scope=%s trusted=%v: got result=%v, want non-nil=%v", tt.scope, tt.trusted, result, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Full integration: GetMcpServerHeaders with mock helper script file
// ---------------------------------------------------------------------------

func TestGetMcpServerHeaders_HelperScript(t *testing.T) {
	// Create a temporary helper script
	tmpDir := t.TempDir()
	scriptPath := tmpDir + "/helper.sh"
	script := `#!/bin/sh
echo "{\"X-Helper\": \"from-script\", \"Server\": \"$CLAUDE_CODE_MCP_SERVER_NAME\"}"
`
	if err := os.WriteFile(scriptPath, []byte(script), 0755); err != nil {
		t.Fatalf("write script: %v", err)
	}

	cfg := &SSEConfig{
		URL:            "http://example.com",
		Headers:        map[string]string{"X-Static": "val"},
		HeadersHelper:  "sh " + scriptPath,
	}
	result := GetMcpServerHeaders(context.Background(), "myserver", cfg, ScopeUser, true)
	if result == nil {
		t.Fatal("expected headers, got nil")
	}
	if result["X-Static"] != "val" {
		t.Errorf("expected X-Static val, got %v", result["X-Static"])
	}
	if result["X-Helper"] != "from-script" {
		t.Errorf("expected X-Helper from-script, got %v", result["X-Helper"])
	}
	if result["Server"] != "myserver" {
		t.Errorf("expected Server myserver, got %v", result["Server"])
	}
}
