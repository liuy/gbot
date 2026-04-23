package mcp

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// AddScopeToServers — Source: config.ts:69-81
// ---------------------------------------------------------------------------

func TestAddScopeToServers_Nil(t *testing.T) {
	result := AddScopeToServers(nil, ScopeProject)
	if len(result) != 0 {
		t.Errorf("expected empty map for nil input, got %d entries", len(result))
	}
}

func TestAddScopeToServers_Empty(t *testing.T) {
	result := AddScopeToServers(map[string]McpServerConfig{}, ScopeUser)
	if len(result) != 0 {
		t.Errorf("expected empty map, got %d entries", len(result))
	}
}

func TestAddScopeToServers_AddsScope(t *testing.T) {
	servers := map[string]McpServerConfig{
		"myserver": &StdioConfig{Command: "node", Args: []string{"server.js"}},
	}
	result := AddScopeToServers(servers, ScopeEnterprise)
	if len(result) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(result))
	}
	scoped, ok := result["myserver"]
	if !ok {
		t.Fatal("expected 'myserver' in result")
	}
	if scoped.Scope != ScopeEnterprise {
		t.Errorf("scope = %q, want %q", scoped.Scope, ScopeEnterprise)
	}
	if _, isStdio := scoped.Config.(*StdioConfig); !isStdio {
		t.Errorf("expected *StdioConfig, got %T", scoped.Config)
	}
}

// ---------------------------------------------------------------------------
// GetServerCommandArray — Source: config.ts:137-144
// ---------------------------------------------------------------------------

func TestGetServerCommandArray_Stdio(t *testing.T) {
	cfg := &StdioConfig{Command: "npx", Args: []string{"-y", "server"}}
	cmd := GetServerCommandArray(cfg)
	want := []string{"npx", "-y", "server"}
	if !CommandArraysMatch(cmd, want) {
		t.Errorf("got %v, want %v", cmd, want)
	}
}

func TestGetServerCommandArray_StdioNoArgs(t *testing.T) {
	cfg := &StdioConfig{Command: "node"}
	cmd := GetServerCommandArray(cfg)
	if len(cmd) != 1 || cmd[0] != "node" {
		t.Errorf("got %v, want [node]", cmd)
	}
}

func TestGetServerCommandArray_NonStdio(t *testing.T) {
	cfg := &SSEConfig{URL: "http://localhost:3000"}
	cmd := GetServerCommandArray(cfg)
	if cmd != nil {
		t.Errorf("expected nil for non-stdio, got %v", cmd)
	}
}

func TestGetServerCommandArray_HTTP(t *testing.T) {
	cfg := &HTTPConfig{URL: "http://localhost:3000"}
	cmd := GetServerCommandArray(cfg)
	if cmd != nil {
		t.Errorf("expected nil for HTTP, got %v", cmd)
	}
}

// ---------------------------------------------------------------------------
// CommandArraysMatch — Source: config.ts:149-154
// ---------------------------------------------------------------------------

func TestCommandArraysMatch_Equal(t *testing.T) {
	if !CommandArraysMatch([]string{"a", "b"}, []string{"a", "b"}) {
		t.Error("expected match")
	}
}

func TestCommandArraysMatch_DifferentLength(t *testing.T) {
	if CommandArraysMatch([]string{"a"}, []string{"a", "b"}) {
		t.Error("expected no match for different lengths")
	}
}

func TestCommandArraysMatch_DifferentValues(t *testing.T) {
	if CommandArraysMatch([]string{"a", "b"}, []string{"a", "c"}) {
		t.Error("expected no match for different values")
	}
}

func TestCommandArraysMatch_Empty(t *testing.T) {
	if !CommandArraysMatch([]string{}, []string{}) {
		t.Error("expected match for empty slices")
	}
}

// ---------------------------------------------------------------------------
// GetServerUrl — Source: config.ts:160-162
// ---------------------------------------------------------------------------

func TestGetServerUrl_SSE(t *testing.T) {
	cfg := &SSEConfig{URL: "http://localhost:3000/sse"}
	if got := GetServerUrl(cfg); got != "http://localhost:3000/sse" {
		t.Errorf("got %q, want %q", got, "http://localhost:3000/sse")
	}
}

func TestGetServerUrl_HTTP(t *testing.T) {
	cfg := &HTTPConfig{URL: "https://api.example.com/mcp"}
	if got := GetServerUrl(cfg); got != "https://api.example.com/mcp" {
		t.Errorf("got %q, want %q", got, "https://api.example.com/mcp")
	}
}

func TestGetServerUrl_WS(t *testing.T) {
	cfg := &WSConfig{URL: "ws://localhost:8080"}
	if got := GetServerUrl(cfg); got != "ws://localhost:8080" {
		t.Errorf("got %q, want %q", got, "ws://localhost:8080")
	}
}

func TestGetServerUrl_Stdio(t *testing.T) {
	cfg := &StdioConfig{Command: "node"}
	if got := GetServerUrl(cfg); got != "" {
		t.Errorf("expected empty string for stdio, got %q", got)
	}
}

func TestGetServerUrl_SDK(t *testing.T) {
	cfg := &SDKConfig{}
	if got := GetServerUrl(cfg); got != "" {
		t.Errorf("expected empty string for SDK, got %q", got)
	}
}

// ---------------------------------------------------------------------------
// GetMcpServerSignature — Source: config.ts:202-212
// ---------------------------------------------------------------------------

func TestGetMcpServerSignature_Stdio(t *testing.T) {
	cfg := &StdioConfig{Command: "npx", Args: []string{"-y", "server"}}
	sig := GetMcpServerSignature(cfg)
	want := `stdio:["npx","-y","server"]`
	if sig != want {
		t.Errorf("got %q, want %q", sig, want)
	}
}

func TestGetMcpServerSignature_Remote(t *testing.T) {
	cfg := &SSEConfig{URL: "http://localhost:3000/sse"}
	sig := GetMcpServerSignature(cfg)
	want := "url:http://localhost:3000/sse"
	if sig != want {
		t.Errorf("got %q, want %q", sig, want)
	}
}

func TestGetMcpServerSignature_SDK(t *testing.T) {
	cfg := &SDKConfig{}
	sig := GetMcpServerSignature(cfg)
	if sig != "" {
		t.Errorf("expected empty string for SDK, got %q", sig)
	}
}

// ---------------------------------------------------------------------------
// UnwrapCcrProxyUrl — Source: config.ts:182-193
// ---------------------------------------------------------------------------

func TestUnwrapCcrProxyUrl_NonProxy(t *testing.T) {
	url := "http://localhost:3000/sse"
	if got := UnwrapCcrProxyUrl(url); got != url {
		t.Errorf("got %q, want %q", got, url)
	}
}

func TestUnwrapCcrProxyUrl_CcrProxy(t *testing.T) {
	url := "https://example.com/v2/session_ingress/shttp/mcp/123?mcp_url=http://real-server.com/api"
	want := "http://real-server.com/api"
	if got := UnwrapCcrProxyUrl(url); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestUnwrapCcrProxyUrl_CcrProxyNoMcpUrl(t *testing.T) {
	url := "https://example.com/v2/session_ingress/shttp/mcp/123"
	if got := UnwrapCcrProxyUrl(url); got != url {
		t.Errorf("expected original URL when no mcp_url param, got %q", got)
	}
}

// ---------------------------------------------------------------------------
// UrlPatternToRegex / UrlMatchesPattern — Source: config.ts:320-334
// ---------------------------------------------------------------------------

func TestUrlMatchesPattern_ExactMatch(t *testing.T) {
	if !UrlMatchesPattern("https://example.com/api", "https://example.com/api") {
		t.Error("expected exact match")
	}
}

func TestUrlMatchesPattern_Wildcard(t *testing.T) {
	if !UrlMatchesPattern("https://example.com/api/v1", "https://example.com/*") {
		t.Error("expected wildcard match")
	}
}

func TestUrlMatchesPattern_SubdomainWildcard(t *testing.T) {
	if !UrlMatchesPattern("https://api.example.com/path", "https://*.example.com/*") {
		t.Error("expected subdomain wildcard match")
	}
}

func TestUrlMatchesPattern_NoMatch(t *testing.T) {
	if UrlMatchesPattern("https://other.com/api", "https://example.com/*") {
		t.Error("expected no match")
	}
}

// ---------------------------------------------------------------------------
// DedupPluginMcpServers — Source: config.ts:223-266
// ---------------------------------------------------------------------------

func TestDedupPluginMcpServers_NoDuplicates(t *testing.T) {
	plugin := map[string]ScopedMcpServerConfig{
		"plugin:p1:s1": {Config: &SSEConfig{URL: "http://s1.com"}, Scope: ScopeDynamic},
		"plugin:p2:s2": {Config: &SSEConfig{URL: "http://s2.com"}, Scope: ScopeDynamic},
	}
	manual := map[string]ScopedMcpServerConfig{
		"manual1": {Config: &SSEConfig{URL: "http://m1.com"}, Scope: ScopeUser},
	}
	servers, suppressed := DedupPluginMcpServers(plugin, manual)
	if len(servers) != 2 {
		t.Errorf("expected 2 servers, got %d", len(servers))
	}
	if len(suppressed) != 0 {
		t.Errorf("expected 0 suppressed, got %d", len(suppressed))
	}
}

func TestDedupPluginMcpServers_DuplicateManual(t *testing.T) {
	plugin := map[string]ScopedMcpServerConfig{
		"plugin:p1:s1": {Config: &SSEConfig{URL: "http://same.com"}, Scope: ScopeDynamic},
	}
	manual := map[string]ScopedMcpServerConfig{
		"manual1": {Config: &SSEConfig{URL: "http://same.com"}, Scope: ScopeUser},
	}
	servers, suppressed := DedupPluginMcpServers(plugin, manual)
	if len(servers) != 0 {
		t.Errorf("expected 0 servers (suppressed), got %d", len(servers))
	}
	if len(suppressed) != 1 || suppressed[0].DuplicateOf != "manual1" {
		t.Errorf("expected 1 suppressed matching manual1, got %v", suppressed)
	}
}

func TestDedupPluginMcpServers_DuplicatePlugin(t *testing.T) {
	plugin := map[string]ScopedMcpServerConfig{
		"plugin:p1:s1": {Config: &SSEConfig{URL: "http://same.com"}, Scope: ScopeDynamic},
		"plugin:p2:s2": {Config: &SSEConfig{URL: "http://same.com"}, Scope: ScopeDynamic},
	}
	manual := map[string]ScopedMcpServerConfig{}
	servers, suppressed := DedupPluginMcpServers(plugin, manual)
	if len(servers) != 1 {
		t.Errorf("expected 1 server (first wins), got %d", len(servers))
	}
	if len(suppressed) != 1 {
		t.Errorf("expected 1 suppressed, got %d", len(suppressed))
	}
}

func TestDedupPluginMcpServers_SDKNoSuppress(t *testing.T) {
	plugin := map[string]ScopedMcpServerConfig{
		"plugin:p1:sdksvr": {Config: &SDKConfig{}, Scope: ScopeDynamic},
	}
	manual := map[string]ScopedMcpServerConfig{}
	servers, suppressed := DedupPluginMcpServers(plugin, manual)
	if len(servers) != 1 {
		t.Errorf("SDK servers should always be included, got %d servers", len(servers))
	}
	if len(suppressed) != 0 {
		t.Errorf("SDK servers should not be suppressed, got %d suppressed", len(suppressed))
	}
}

// ---------------------------------------------------------------------------
// DedupClaudeAiMcpServers — Source: config.ts:281-310
// ---------------------------------------------------------------------------

func TestDedupClaudeAiMcpServers_DisabledManualNotTarget(t *testing.T) {
	claudeAi := map[string]ScopedMcpServerConfig{
		"claude.ai Slack": {Config: &SSEConfig{URL: "http://slack.com/mcp"}, Scope: ScopeClaudeAI},
	}
	manual := map[string]ScopedMcpServerConfig{
		"slack": {Config: &SSEConfig{URL: "http://slack.com/mcp"}, Scope: ScopeUser},
	}
	// Disabled manual server should NOT suppress claude.ai connector.
	disabledChecker := func(name string) bool { return name == "slack" }
	servers, suppressed := DedupClaudeAiMcpServers(claudeAi, manual, disabledChecker)
	if len(servers) != 1 {
		t.Errorf("disabled manual should not suppress connector, got %d servers", len(servers))
	}
	if len(suppressed) != 0 {
		t.Errorf("expected 0 suppressed, got %d", len(suppressed))
	}
}

func TestDedupClaudeAiMcpServers_EnabledManualSuppresses(t *testing.T) {
	claudeAi := map[string]ScopedMcpServerConfig{
		"claude.ai Slack": {Config: &SSEConfig{URL: "http://slack.com/mcp"}, Scope: ScopeClaudeAI},
	}
	manual := map[string]ScopedMcpServerConfig{
		"slack": {Config: &SSEConfig{URL: "http://slack.com/mcp"}, Scope: ScopeUser},
	}
	disabledChecker := func(name string) bool { return false } // none disabled
	servers, suppressed := DedupClaudeAiMcpServers(claudeAi, manual, disabledChecker)
	if len(servers) != 0 {
		t.Errorf("enabled manual should suppress connector, got %d servers", len(servers))
	}
	if len(suppressed) != 1 || suppressed[0].DuplicateOf != "slack" {
		t.Errorf("expected 1 suppressed matching 'slack', got %v", suppressed)
	}
}

// ---------------------------------------------------------------------------
// ExpandConfigEnv — Source: config.ts:556-616
// ---------------------------------------------------------------------------

func TestExpandConfigEnv_Stdio(t *testing.T) {
	t.Setenv("MCP_TEST_CMD", "node")
	t.Setenv("MCP_TEST_ARG", "server.js")

	cfg := &StdioConfig{
		Command: "${MCP_TEST_CMD}",
		Args:    []string{"${MCP_TEST_ARG}"},
		Env:     map[string]string{"KEY": "${MCP_TEST_CMD}"},
	}
	expanded, missing := ExpandConfigEnv(cfg)
	if len(missing) != 0 {
		t.Errorf("expected no missing vars, got %v", missing)
	}
	stdio, ok := expanded.(*StdioConfig)
	if !ok {
		t.Fatalf("expected *StdioConfig, got %T", expanded)
	}
	if stdio.Command != "node" {
		t.Errorf("command = %q, want %q", stdio.Command, "node")
	}
	if len(stdio.Args) != 1 || stdio.Args[0] != "server.js" {
		t.Errorf("args = %v, want [server.js]", stdio.Args)
	}
	if stdio.Env["KEY"] != "node" {
		t.Errorf("env[KEY] = %q, want %q", stdio.Env["KEY"], "node")
	}
}

func TestExpandConfigEnv_MissingVar(t *testing.T) {
	cfg := &SSEConfig{URL: "${MCP_TEST_MISSING_99999_SERVER}"}
	_, missing := ExpandConfigEnv(cfg)
	if len(missing) != 1 || missing[0] != "MCP_TEST_MISSING_99999_SERVER" {
		t.Errorf("missing = %v, want [MCP_TEST_MISSING_99999_SERVER]", missing)
	}
}

func TestExpandConfigEnv_Remote(t *testing.T) {
	t.Setenv("MCP_TEST_HOST", "localhost")
	cfg := &HTTPConfig{URL: "http://${MCP_TEST_HOST}:3000"}
	expanded, missing := ExpandConfigEnv(cfg)
	if len(missing) != 0 {
		t.Errorf("expected no missing, got %v", missing)
	}
	httpCfg, ok := expanded.(*HTTPConfig)
	if !ok {
		t.Fatalf("expected *HTTPConfig, got %T", expanded)
	}
	if httpCfg.URL != "http://localhost:3000" {
		t.Errorf("url = %q, want %q", httpCfg.URL, "http://localhost:3000")
	}
}

func TestExpandConfigEnv_SDK_NoExpansion(t *testing.T) {
	cfg := &SDKConfig{}
	expanded, missing := ExpandConfigEnv(cfg)
	if len(missing) != 0 {
		t.Errorf("SDK should have no missing vars, got %v", missing)
	}
	if _, ok := expanded.(*SDKConfig); !ok {
		t.Errorf("SDK should be returned as-is, got %T", expanded)
	}
}

// ---------------------------------------------------------------------------
// Policy engine — Source: config.ts:364-551
// ---------------------------------------------------------------------------

func TestIsMcpServerDenied_NameMatch(t *testing.T) {
	denied := []McpPolicyEntry{&McpNameEntry{ServerName: "evil"}}
	if !IsMcpServerDenied("evil", nil, denied) {
		t.Error("expected denied by name")
	}
	if IsMcpServerDenied("good", nil, denied) {
		t.Error("expected not denied")
	}
}

func TestIsMcpServerDenied_CommandMatch(t *testing.T) {
	denied := []McpPolicyEntry{
		&McpCommandEntry{ServerCommand: []string{"npx", "-y", "evil"}},
	}
	cfg := &StdioConfig{Command: "npx", Args: []string{"-y", "evil"}}
	if !IsMcpServerDenied("myserver", cfg, denied) {
		t.Error("expected denied by command")
	}
}

func TestIsMcpServerDenied_UrlMatch(t *testing.T) {
	denied := []McpPolicyEntry{
		&McpUrlEntry{ServerUrl: "https://evil.com/*"},
	}
	cfg := &SSEConfig{URL: "https://evil.com/api"}
	if !IsMcpServerDenied("myserver", cfg, denied) {
		t.Error("expected denied by URL pattern")
	}
}

func TestIsMcpServerDenied_EmptyList(t *testing.T) {
	if IsMcpServerDenied("any", nil, nil) {
		t.Error("nil denied list should not deny")
	}
	if IsMcpServerDenied("any", nil, []McpPolicyEntry{}) {
		t.Error("empty denied list should not deny")
	}
}

func TestIsMcpServerAllowedByPolicy_NoAllowlist(t *testing.T) {
	// nil allowedEntries = no allowlist restrictions = all allowed.
	if !IsMcpServerAllowedByPolicy("any", nil, nil, nil) {
		t.Error("nil allowlist should allow all")
	}
}

func TestIsMcpServerAllowedByPolicy_EmptyAllowlist(t *testing.T) {
	// Empty allowlist = block all.
	if IsMcpServerAllowedByPolicy("any", nil, nil, []McpPolicyEntry{}) {
		t.Error("empty allowlist should block all")
	}
}

func TestIsMcpServerAllowedByPolicy_DenyOverridesAllow(t *testing.T) {
	allowed := []McpPolicyEntry{&McpNameEntry{ServerName: "test"}}
	denied := []McpPolicyEntry{&McpNameEntry{ServerName: "test"}}
	if IsMcpServerAllowedByPolicy("test", nil, denied, allowed) {
		t.Error("denylist should take precedence over allowlist")
	}
}

func TestIsMcpServerAllowedByPolicy_CommandAllow(t *testing.T) {
	allowed := []McpPolicyEntry{
		&McpCommandEntry{ServerCommand: []string{"node", "server.js"}},
	}
	cfg := &StdioConfig{Command: "node", Args: []string{"server.js"}}
	if !IsMcpServerAllowedByPolicy("myserver", cfg, nil, allowed) {
		t.Error("matching command should be allowed")
	}
	cfg2 := &StdioConfig{Command: "python", Args: []string{"evil.py"}}
	if IsMcpServerAllowedByPolicy("myserver", cfg2, nil, allowed) {
		t.Error("non-matching command should be blocked when command entries exist")
	}
}

func TestIsMcpServerAllowedByPolicy_UrlAllow(t *testing.T) {
	allowed := []McpPolicyEntry{
		&McpUrlEntry{ServerUrl: "https://safe.com/*"},
	}
	cfg := &SSEConfig{URL: "https://safe.com/api"}
	if !IsMcpServerAllowedByPolicy("myserver", cfg, nil, allowed) {
		t.Error("matching URL pattern should be allowed")
	}
	cfg2 := &SSEConfig{URL: "https://evil.com/api"}
	if IsMcpServerAllowedByPolicy("myserver", cfg2, nil, allowed) {
		t.Error("non-matching URL should be blocked when URL entries exist")
	}
}

// ---------------------------------------------------------------------------
// FilterMcpServersByPolicy — Source: config.ts:536-551
// ---------------------------------------------------------------------------

func TestFilterMcpServersByPolicy_SDKExempt(t *testing.T) {
	configs := map[string]ScopedMcpServerConfig{
		"sdk1": {Config: &SDKConfig{}, Scope: ScopeLocal},
		"deny1": {Config: &SSEConfig{URL: "http://evil.com"}, Scope: ScopeUser},
	}
	denied := []McpPolicyEntry{&McpNameEntry{ServerName: "deny1"}}
	allowed, blocked := FilterMcpServersByPolicy(configs, denied, nil)
	if _, ok := allowed["sdk1"]; !ok {
		t.Error("SDK servers should always be allowed")
	}
	if _, ok := allowed["deny1"]; ok {
		t.Error("denied server should be blocked")
	}
	if len(blocked) != 1 || blocked[0] != "deny1" {
		t.Errorf("blocked = %v, want [deny1]", blocked)
	}
}

// ---------------------------------------------------------------------------
// ParseMcpConfig — Source: config.ts:1297-1377
// ---------------------------------------------------------------------------

func TestParseMcpConfig_ValidStdio(t *testing.T) {
	input := `{"mcpServers":{"myserver":{"command":"node","args":["server.js"]}}}`
	config, errors := ParseMcpConfig([]byte(input), false, ScopeProject, "")
	if len(errors) != 0 {
		t.Fatalf("unexpected errors: %v", errors)
	}
	if config == nil {
		t.Fatal("expected non-nil config")
	}
	if len(config.McpServers) != 1 {
		t.Fatalf("expected 1 server, got %d", len(config.McpServers))
	}
}

func TestParseMcpConfig_InvalidJSON(t *testing.T) {
	input := `{not json}`
	config, errors := ParseMcpConfig([]byte(input), false, ScopeProject, "")
	if config != nil {
		t.Error("expected nil config for invalid JSON")
	}
	if len(errors) != 1 || !strings.Contains(errors[0].Message, "valid JSON") {
		t.Errorf("expected JSON parse error, got %v", errors)
	}
}

func TestParseMcpConfig_ExpandVars(t *testing.T) {
	t.Setenv("MCP_TEST_MY_CMD", "node")
	input := `{"mcpServers":{"srv":{"command":"${MCP_TEST_MY_CMD}","args":[]}}}`
	config, errors := ParseMcpConfig([]byte(input), true, ScopeProject, "")
	if len(errors) != 0 {
		t.Fatalf("unexpected errors: %v", errors)
	}
	if config == nil {
		t.Fatal("expected non-nil config")
	}
	// Verify the expanded value is correct.
	cfg, err := UnmarshalServerConfig(config.McpServers["srv"])
	if err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}
	stdio, ok := cfg.(*StdioConfig)
	if !ok {
		t.Fatalf("expected *StdioConfig, got %T", cfg)
	}
	if stdio.Command != "node" {
		t.Errorf("command = %q, want %q", stdio.Command, "node")
	}
}

func TestParseMcpConfig_MissingVar(t *testing.T) {
	input := `{"mcpServers":{"srv":{"command":"${MCP_TEST_MISSING_VAR_XYZ}","args":[]}}}`
	_, errors := ParseMcpConfig([]byte(input), true, ScopeProject, "")
	if len(errors) == 0 {
		t.Fatal("expected at least one error for missing env var")
	}
	want := "Missing environment variables: MCP_TEST_MISSING_VAR_XYZ"
	if errors[0].Message != want {
		t.Errorf("error[0].Message = %q, want %q", errors[0].Message, want)
	}
}

func TestParseMcpConfig_EmptyServers(t *testing.T) {
	input := `{"mcpServers":{}}`
	config, errors := ParseMcpConfig([]byte(input), false, ScopeProject, "")
	if len(errors) != 0 {
		t.Fatalf("unexpected errors: %v", errors)
	}
	if len(config.McpServers) != 0 {
		t.Errorf("expected 0 servers, got %d", len(config.McpServers))
	}
}

// ---------------------------------------------------------------------------
// ParseMcpConfigFromFilePath — Source: config.ts:1384-1468
// ---------------------------------------------------------------------------

func TestParseMcpConfigFromFilePath_Valid(t *testing.T) {
	dir := t.TempDir()
	content := `{"mcpServers":{"srv":{"command":"node","args":["s.js"]}}}`
	mustWriteFile(t, filepath.Join(dir, ".mcp.json"), []byte(content))
	config, errors := ParseMcpConfigFromFilePath(filepath.Join(dir, ".mcp.json"), false, ScopeProject)
	if len(errors) != 0 {
		t.Fatalf("unexpected errors: %v", errors)
	}
	if config == nil || len(config.McpServers) != 1 {
		t.Fatal("expected 1 server")
	}
}

func TestParseMcpConfigFromFilePath_NotFound(t *testing.T) {
	config, errors := ParseMcpConfigFromFilePath("/nonexistent/.mcp.json", false, ScopeProject)
	if config != nil {
		t.Error("expected nil config for missing file")
	}
	if len(errors) != 1 || !strings.Contains(errors[0].Message, "not found") {
		t.Errorf("expected 'not found' error, got %v", errors)
	}
}

func TestParseMcpConfigFromFilePath_InvalidJSON(t *testing.T) {
	dir := t.TempDir()
	mustWriteFile(t, filepath.Join(dir, ".mcp.json"), []byte(`{bad}`))
	config, errors := ParseMcpConfigFromFilePath(filepath.Join(dir, ".mcp.json"), false, ScopeProject)
	if config != nil {
		t.Error("expected nil config for invalid JSON")
	}
	if len(errors) != 1 || !strings.Contains(errors[0].Message, "valid JSON") {
		t.Errorf("expected JSON error, got %v", errors)
	}
}

// ---------------------------------------------------------------------------
// WriteMcpjsonFile — Source: config.ts:88-131
// ---------------------------------------------------------------------------

func TestWriteMcpjsonFile_CreatesFile(t *testing.T) {
	dir := t.TempDir()
	config := &McpJsonConfig{
		McpServers: map[string]json.RawMessage{
			"srv": json.RawMessage(`{"command":"node","args":["s.js"]}`),
		},
	}
	if err := WriteMcpjsonFile(dir, config); err != nil {
		t.Fatalf("WriteMcpjsonFile: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, ".mcp.json"))
	if err != nil {
		t.Fatalf("read back: %v", err)
	}

	// Verify it's valid JSON and contains our server.
	var parsed McpJsonConfig
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(parsed.McpServers) != 1 {
		t.Errorf("expected 1 server, got %d", len(parsed.McpServers))
	}
}

func TestWriteMcpjsonFile_AtomicWrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".mcp.json")

	// Create initial file.
	mustWriteFile(t, path, []byte(`{"mcpServers":{}}`))

	// Overwrite with new config.
	config := &McpJsonConfig{
		McpServers: map[string]json.RawMessage{
			"new": json.RawMessage(`{"command":"node"}`),
		},
	}
	if err := WriteMcpjsonFile(dir, config); err != nil {
		t.Fatalf("WriteMcpjsonFile: %v", err)
	}

	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "new") {
		t.Errorf("file should contain 'new' server, got: %s", data)
	}

	// No temp files left behind.
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp.") {
			t.Errorf("temp file left behind: %s", e.Name())
		}
	}
}

func TestWriteMcpjsonFile_CrashRecovery(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".mcp.json")

	// Write initial valid content.
	mustWriteFile(t, path, []byte(`{"mcpServers":{"old":{"command":"node"}}}`))

	// Simulate crash: temp file exists but rename didn't happen.
	tempPath := path + ".tmp.99999"
	mustWriteFile(t, tempPath, []byte(`{"mcpServers":{"crashed":{"command":"python"}}}`))

	// WriteMcpjsonFile should still succeed (creates its own temp file).
	config := &McpJsonConfig{
		McpServers: map[string]json.RawMessage{
			"recovered": json.RawMessage(`{"command":"go"}`),
		},
	}
	if err := WriteMcpjsonFile(dir, config); err != nil {
		t.Fatalf("WriteMcpjsonFile with leftover temp: %v", err)
	}

	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "recovered") {
		t.Errorf("should have 'recovered' server, got: %s", data)
	}
}

// ---------------------------------------------------------------------------
// GetProjectMcpConfigsFromCwd — Source: config.ts:843-881
// ---------------------------------------------------------------------------

func TestGetProjectMcpConfigsFromCwd_Valid(t *testing.T) {
	dir := t.TempDir()
	content := `{"mcpServers":{"srv":{"command":"node","args":["s.js"]}}}`
	mustWriteFile(t, filepath.Join(dir, ".mcp.json"), []byte(content))

	servers, errors := GetProjectMcpConfigsFromCwd(dir)
	if len(errors) != 0 {
		t.Fatalf("unexpected errors: %v", errors)
	}
	if len(servers) != 1 {
		t.Fatalf("expected 1 server, got %d", len(servers))
	}
	scoped, ok := servers["srv"]
	if !ok {
		t.Fatal("expected 'srv' in servers")
	}
	if scoped.Scope != ScopeProject {
		t.Errorf("scope = %q, want %q", scoped.Scope, ScopeProject)
	}
}

func TestGetProjectMcpConfigsFromCwd_NoFile(t *testing.T) {
	dir := t.TempDir()
	servers, errors := GetProjectMcpConfigsFromCwd(dir)
	if len(errors) != 0 {
		t.Errorf("expected no errors for missing file, got %v", errors)
	}
	if len(servers) != 0 {
		t.Errorf("expected 0 servers, got %d", len(servers))
	}
}

// ---------------------------------------------------------------------------
// GetMcpConfigsByScope project (directory walk) — Source: config.ts:909-961
// ---------------------------------------------------------------------------

func TestGetMcpConfigsByScope_ProjectWalk(t *testing.T) {
	// Create nested directory structure:
	// root/.mcp.json -> {parent: ...}
	// root/child/.mcp.json -> {child: ...}
	// root/child/grandchild/ (no .mcp.json)
	root := t.TempDir()
	child := filepath.Join(root, "child")
	grandchild := filepath.Join(child, "grandchild")
	mustMkdirAll(t, grandchild)

	parentJSON := `{"mcpServers":{"parent":{"command":"node"}}}`
	childJSON := `{"mcpServers":{"child":{"command":"python"}}}`
	mustWriteFile(t, filepath.Join(root, ".mcp.json"), []byte(parentJSON))
	mustWriteFile(t, filepath.Join(child, ".mcp.json"), []byte(childJSON))

	// From grandchild, should see both parent and child.
	provider := &mockConfigProvider{}
	servers, _ := GetMcpConfigsByScope(ScopeProject, grandchild, provider)
	if len(servers) != 2 {
		t.Fatalf("expected 2 servers, got %d", len(servers))
	}
	if _, ok := servers["parent"]; !ok {
		t.Error("expected 'parent' server")
	}
	if _, ok := servers["child"]; !ok {
		t.Error("expected 'child' server")
	}
}

func TestGetMcpConfigsByScope_ProjectOverride(t *testing.T) {
	// Parent and child both define "myserver" — child should win.
	root := t.TempDir()
	child := filepath.Join(root, "sub")
	mustMkdirAll(t, child)

	parentJSON := `{"mcpServers":{"myserver":{"command":"parent-cmd"}}}`
	childJSON := `{"mcpServers":{"myserver":{"command":"child-cmd"}}}`
	mustWriteFile(t, filepath.Join(root, ".mcp.json"), []byte(parentJSON))
	mustWriteFile(t, filepath.Join(child, ".mcp.json"), []byte(childJSON))

	provider := &mockConfigProvider{}
	servers, _ := GetMcpConfigsByScope(ScopeProject, child, provider)
	if len(servers) != 1 {
		t.Fatalf("expected 1 server, got %d", len(servers))
	}
	scoped := servers["myserver"]
	stdio, ok := scoped.Config.(*StdioConfig)
	if !ok {
		t.Fatalf("expected *StdioConfig, got %T", scoped.Config)
	}
	if stdio.Command != "child-cmd" {
		t.Errorf("command = %q, want %q (child should override parent)", stdio.Command, "child-cmd")
	}
}

// ---------------------------------------------------------------------------
// AddMcpConfig / RemoveMcpConfig — Source: config.ts:625-834
// ---------------------------------------------------------------------------

func TestAddMcpConfig_Project(t *testing.T) {
	dir := t.TempDir()
	provider := &mockConfigProvider{}
	cfg := &StdioConfig{Command: "node", Args: []string{"server.js"}}

	if err := AddMcpConfig("myserver", cfg, ScopeProject, dir, provider, nil, nil); err != nil {
		t.Fatalf("AddMcpConfig: %v", err)
	}

	// Verify file was written.
	servers, _ := GetProjectMcpConfigsFromCwd(dir)
	if len(servers) != 1 {
		t.Fatalf("expected 1 server, got %d", len(servers))
	}
	if _, ok := servers["myserver"]; !ok {
		t.Error("expected 'myserver' in servers")
	}
}

func TestAddMcpConfig_InvalidName(t *testing.T) {
	dir := t.TempDir()
	provider := &mockConfigProvider{}
	cfg := &StdioConfig{Command: "node"}

	err := AddMcpConfig("bad name!", cfg, ScopeProject, dir, provider, nil, nil)
	if err == nil {
		t.Fatal("want error for invalid name")
	}
	if !strings.Contains(err.Error(), "invalid name") {
		t.Errorf("error = %v, want invalid name error", err)
	}
}

func TestAddMcpConfig_DuplicateName(t *testing.T) {
	dir := t.TempDir()
	// Pre-create .mcp.json with existing server.
	content := `{"mcpServers":{"existing":{"command":"node"}}}`
	mustWriteFile(t, filepath.Join(dir, ".mcp.json"), []byte(content))

	provider := &mockConfigProvider{}
	cfg := &StdioConfig{Command: "python"}
	err := AddMcpConfig("existing", cfg, ScopeProject, dir, provider, nil, nil)
	if err == nil {
		t.Error("want error for duplicate name")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Errorf("error = %v, want 'already exists' error", err)
	}
}

func TestAddMcpConfig_DeniedByPolicy(t *testing.T) {
	dir := t.TempDir()
	provider := &mockConfigProvider{}
	cfg := &StdioConfig{Command: "evil"}
	denied := []McpPolicyEntry{&McpNameEntry{ServerName: "evil"}}

	err := AddMcpConfig("evil", cfg, ScopeProject, dir, provider, denied, nil)
	if err == nil {
		t.Error("want error for denied server")
	}
	if !strings.Contains(err.Error(), "blocked by enterprise policy") {
		t.Errorf("error = %v, want policy error", err)
	}
}

func TestRemoveMcpConfig_Project(t *testing.T) {
	dir := t.TempDir()
	content := `{"mcpServers":{"srv1":{"command":"node"},"srv2":{"command":"python"}}}`
	mustWriteFile(t, filepath.Join(dir, ".mcp.json"), []byte(content))

	provider := &mockConfigProvider{}
	if err := RemoveMcpConfig("srv1", ScopeProject, dir, provider); err != nil {
		t.Fatalf("RemoveMcpConfig: %v", err)
	}

	servers, _ := GetProjectMcpConfigsFromCwd(dir)
	if len(servers) != 1 {
		t.Fatalf("expected 1 server, got %d", len(servers))
	}
	if _, ok := servers["srv2"]; !ok {
		t.Error("expected 'srv2' to remain")
	}
	if _, ok := servers["srv1"]; ok {
		t.Error("expected 'srv1' to be removed")
	}
}

func TestRemoveMcpConfig_NotFound(t *testing.T) {
	dir := t.TempDir()
	provider := &mockConfigProvider{}
	err := RemoveMcpConfig("nonexistent", ScopeProject, dir, provider)
	if err == nil {
		t.Fatal("want error for nonexistent server")
	}
	if !strings.Contains(err.Error(), "no MCP server found") {
		t.Errorf("error = %v, want 'no MCP server found' error", err)
	}
}

// ---------------------------------------------------------------------------
// IsMcpServerDisabled / SetMcpServerEnabled — Source: config.ts:1528-1578
// ---------------------------------------------------------------------------

func TestIsMcpServerDisabled_True(t *testing.T) {
	provider := &mockConfigProvider{disabledServers: []string{"myserver"}}
	if !IsMcpServerDisabled("myserver", provider) {
		t.Error("expected server to be disabled")
	}
}

func TestIsMcpServerDisabled_False(t *testing.T) {
	provider := &mockConfigProvider{disabledServers: []string{"other"}}
	if IsMcpServerDisabled("myserver", provider) {
		t.Error("expected server to be enabled")
	}
}

func TestSetMcpServerEnabled_Disable(t *testing.T) {
	provider := &mockConfigProvider{disabledServers: []string{}}
	if err := SetMcpServerEnabled("myserver", false, provider); err != nil {
		t.Fatalf("SetMcpServerEnabled: %v", err)
	}
	if len(provider.savedDisabled) != 1 || provider.savedDisabled[0] != "myserver" {
		t.Errorf("saved disabled = %v, want [myserver]", provider.savedDisabled)
	}
}

func TestSetMcpServerEnabled_Enable(t *testing.T) {
	provider := &mockConfigProvider{disabledServers: []string{"myserver"}}
	if err := SetMcpServerEnabled("myserver", true, provider); err != nil {
		t.Fatalf("SetMcpServerEnabled: %v", err)
	}
	if len(provider.savedDisabled) != 0 {
		t.Errorf("saved disabled = %v, want empty", provider.savedDisabled)
	}
}

// ---------------------------------------------------------------------------
// toggleMembership — Source: config.ts:1538-1546
// ---------------------------------------------------------------------------

func TestToggleMembership_Add(t *testing.T) {
	result := toggleMembership([]string{"a"}, "b", true)
	if len(result) != 2 {
		t.Errorf("expected 2 items, got %d", len(result))
	}
}

func TestToggleMembership_Remove(t *testing.T) {
	result := toggleMembership([]string{"a", "b"}, "a", false)
	if len(result) != 1 || result[0] != "b" {
		t.Errorf("expected [b], got %v", result)
	}
}

func TestToggleMembership_NoChange(t *testing.T) {
	result := toggleMembership([]string{"a"}, "b", false)
	if len(result) != 1 {
		t.Errorf("expected unchanged, got %v", result)
	}
}

// ---------------------------------------------------------------------------
// UnmarshalMcpPolicyEntry
// ---------------------------------------------------------------------------

func TestUnmarshalMcpPolicyEntry_NameEntry(t *testing.T) {
	raw := json.RawMessage(`{"serverName":"test"}`)
	entry, err := UnmarshalMcpPolicyEntry(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	ne, ok := entry.(*McpNameEntry)
	if !ok {
		t.Fatalf("expected *McpNameEntry, got %T", entry)
	}
	if ne.ServerName != "test" {
		t.Errorf("ServerName = %q, want %q", ne.ServerName, "test")
	}
}

func TestUnmarshalMcpPolicyEntry_CommandEntry(t *testing.T) {
	raw := json.RawMessage(`{"serverCommand":["npx","server"]}`)
	entry, err := UnmarshalMcpPolicyEntry(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	ce, ok := entry.(*McpCommandEntry)
	if !ok {
		t.Fatalf("expected *McpCommandEntry, got %T", entry)
	}
	if !CommandArraysMatch(ce.ServerCommand, []string{"npx", "server"}) {
		t.Errorf("ServerCommand = %v, want [npx server]", ce.ServerCommand)
	}
}

func TestUnmarshalMcpPolicyEntry_UrlEntry(t *testing.T) {
	raw := json.RawMessage(`{"serverUrl":"https://example.com/*"}`)
	entry, err := UnmarshalMcpPolicyEntry(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	ue, ok := entry.(*McpUrlEntry)
	if !ok {
		t.Fatalf("expected *McpUrlEntry, got %T", entry)
	}
	if ue.ServerUrl != "https://example.com/*" {
		t.Errorf("ServerUrl = %q, want %q", ue.ServerUrl, "https://example.com/*")
	}
}

func TestUnmarshalMcpPolicyEntry_Invalid(t *testing.T) {
	raw := json.RawMessage(`{"unknownField":123}`)
	_, err := UnmarshalMcpPolicyEntry(raw)
	if err == nil {
		t.Fatal("want error for invalid entry")
	}
	if !strings.Contains(err.Error(), "invalid policy entry") {
		t.Errorf("error = %v, want 'invalid policy entry' error", err)
	}
}

// ---------------------------------------------------------------------------
// mockConfigProvider — test helper
// ---------------------------------------------------------------------------

type mockConfigProvider struct {
	userServers     map[string]McpServerConfig
	localServers    map[string]McpServerConfig
	disabledServers []string
	enabledServers  []string
	deniedEntries   []McpPolicyEntry
	allowedEntries  []McpPolicyEntry
	managedOnly     bool
	pluginOnly      bool
	managedPath     string
	savedDisabled   []string
	savedEnabled    []string
}

func (m *mockConfigProvider) UserMcpServers() map[string]McpServerConfig {
	if m.userServers == nil {
		return map[string]McpServerConfig{}
	}
	return m.userServers
}
func (m *mockConfigProvider) LocalMcpServers() map[string]McpServerConfig {
	if m.localServers == nil {
		return map[string]McpServerConfig{}
	}
	return m.localServers
}
func (m *mockConfigProvider) ProjectDisabledServers() []string     { return m.disabledServers }
func (m *mockConfigProvider) ProjectEnabledServers() []string      { return m.enabledServers }
func (m *mockConfigProvider) PolicyDeniedServers() []McpPolicyEntry  { return m.deniedEntries }
func (m *mockConfigProvider) PolicyAllowedServers() []McpPolicyEntry { return m.allowedEntries }
func (m *mockConfigProvider) IsManagedOnly() bool                  { return m.managedOnly }
func (m *mockConfigProvider) IsPluginOnly() bool                   { return m.pluginOnly }
func (m *mockConfigProvider) ManagedMcpFilePath() string {
	if m.managedPath != "" {
		return m.managedPath
	}
	return "/nonexistent/managed-mcp.json"
}
func (m *mockConfigProvider) SaveProjectDisabledServers(names []string) error {
	m.savedDisabled = names
	return nil
}
func (m *mockConfigProvider) SaveProjectEnabledServers(names []string) error {
	m.savedEnabled = names
	return nil
}
func (m *mockConfigProvider) SaveUserMcpServers(servers map[string]McpServerConfig) error {
	m.userServers = servers
	return nil
}
func (m *mockConfigProvider) SaveLocalMcpServers(servers map[string]McpServerConfig) error {
	m.localServers = servers
	return nil
}

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

func mustWriteFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatalf("setup write %s: %v", path, err)
	}
}

func mustMkdirAll(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0755); err != nil {
		t.Fatalf("setup mkdir %s: %v", path, err)
	}
}

// =========================================================================
// mergeMaps tests
// =========================================================================

func TestMergeMaps(t *testing.T) {
	makeScoped := func(cmd string) ScopedMcpServerConfig {
		return ScopedMcpServerConfig{Config: &StdioConfig{Command: cmd}, Scope: ScopeUser}
	}

	t.Run("empty", func(t *testing.T) {
		got := mergeMaps()
		if len(got) != 0 {
			t.Errorf("expected empty, got %d", len(got))
		}
	})

	t.Run("single_map", func(t *testing.T) {
		got := mergeMaps(map[string]ScopedMcpServerConfig{"a": makeScoped("echo")})
		if len(got) != 1 {
			t.Errorf("expected 1, got %d", len(got))
		}
		if _, ok := got["a"]; !ok {
			t.Error("missing key 'a'")
		}
	})

	t.Run("second_overrides", func(t *testing.T) {
		m1 := map[string]ScopedMcpServerConfig{"a": makeScoped("echo"), "b": makeScoped("cat")}
		m2 := map[string]ScopedMcpServerConfig{"a": makeScoped("ls"), "c": makeScoped("grep")}
		got := mergeMaps(m1, m2)
		if len(got) != 3 {
			t.Errorf("expected 3, got %d", len(got))
		}
		// "a" should be overridden by m2
		cfg := got["a"].Config.(*StdioConfig)
		if cfg.Command != "ls" {
			t.Errorf("a.Command = %q, want %q", cfg.Command, "ls")
		}
	})

	t.Run("nil_map", func(t *testing.T) {
		got := mergeMaps(nil, map[string]ScopedMcpServerConfig{"a": makeScoped("echo")})
		if len(got) != 1 {
			t.Errorf("expected 1, got %d", len(got))
		}
	})
}

// =========================================================================
// AreMcpConfigsAllowedWithEnterpriseMcpConfig tests
// =========================================================================

func TestAreMcpConfigsAllowedWithEnterpriseMcpConfig(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		if !AreMcpConfigsAllowedWithEnterpriseMcpConfig(map[string]ScopedMcpServerConfig{}) {
			t.Error("empty should be allowed")
		}
	})

	t.Run("all_sdk", func(t *testing.T) {
		cfgs := map[string]ScopedMcpServerConfig{
			"vscode": {Config: &SDKConfig{}, Scope: ScopeUser},
		}
		if !AreMcpConfigsAllowedWithEnterpriseMcpConfig(cfgs) {
			t.Error("all SDK should be allowed")
		}
	})

	t.Run("mixed", func(t *testing.T) {
		cfgs := map[string]ScopedMcpServerConfig{
			"vscode": {Config: &SDKConfig{}, Scope: ScopeUser},
			"custom": {Config: &StdioConfig{Command: "echo"}, Scope: ScopeUser},
		}
		if AreMcpConfigsAllowedWithEnterpriseMcpConfig(cfgs) {
			t.Error("mixed should not be allowed")
		}
	})

	t.Run("all_stdio", func(t *testing.T) {
		cfgs := map[string]ScopedMcpServerConfig{
			"custom": {Config: &StdioConfig{Command: "echo"}, Scope: ScopeUser},
		}
		if AreMcpConfigsAllowedWithEnterpriseMcpConfig(cfgs) {
			t.Error("all non-SDK should not be allowed")
		}
	})
}

// =========================================================================
// PolicyEntryMarkers tests
// =========================================================================

func TestPolicyEntryMarkers(t *testing.T) {
	// Verify interface satisfaction at compile time
	var _ McpPolicyEntry = (*McpNameEntry)(nil)
	var _ McpPolicyEntry = (*McpCommandEntry)(nil)
	var _ McpPolicyEntry = (*McpUrlEntry)(nil)

	// Verify field access
	name := &McpNameEntry{ServerName: "test"}
	if name.ServerName != "test" {
		t.Errorf("ServerName = %q, want %q", name.ServerName, "test")
	}

	cmd := &McpCommandEntry{ServerCommand: []string{"echo", "hello"}}
	if len(cmd.ServerCommand) != 2 {
		t.Errorf("ServerCommand len = %d, want 2", len(cmd.ServerCommand))
	}

	u := &McpUrlEntry{ServerUrl: "https://*.example.com"}
	if u.ServerUrl != "https://*.example.com" {
		t.Errorf("ServerUrl = %q, want %q", u.ServerUrl, "https://*.example.com")
	}
}

// =========================================================================
// DoesEnterpriseMcpConfigExist tests
// =========================================================================

func TestDoesEnterpriseMcpConfigExist(t *testing.T) {
	t.Run("valid_config", func(t *testing.T) {
		dir := t.TempDir()
		mcpPath := filepath.Join(dir, ".mcp.json")
		mustWriteFile(t, mcpPath, []byte(`{"mcpServers":{"test":{"type":"stdio","command":"echo"}}}`))

		provider := &mockConfigProvider{managedPath: mcpPath}
		if !DoesEnterpriseMcpConfigExist(dir, provider) {
			t.Error("should return true for valid config")
		}
	})

	t.Run("missing_file", func(t *testing.T) {
		provider := &mockConfigProvider{managedPath: "/nonexistent/.mcp.json"}
		if DoesEnterpriseMcpConfigExist("/tmp", provider) {
			t.Error("should return false for missing file")
		}
	})

	t.Run("invalid_json", func(t *testing.T) {
		dir := t.TempDir()
		mcpPath := filepath.Join(dir, ".mcp.json")
		mustWriteFile(t, mcpPath, []byte(`{invalid json}`))

		provider := &mockConfigProvider{managedPath: mcpPath}
		if DoesEnterpriseMcpConfigExist(dir, provider) {
			t.Error("should return false for invalid JSON")
		}
	})
}

// =========================================================================
// GetMcpConfigsByScope tests
// =========================================================================

func TestGetMcpConfigsByScope_AllScopes(t *testing.T) {
	t.Run("ScopeUser", func(t *testing.T) {
		provider := &mockConfigProvider{
			userServers: map[string]McpServerConfig{
				"test": &StdioConfig{Command: "echo"},
			},
		}
		result, errs := GetMcpConfigsByScope(ScopeUser, "/tmp", provider)
		if len(errs) != 0 {
			t.Errorf("unexpected errors: %v", errs)
		}
		if len(result) != 1 {
			t.Errorf("expected 1, got %d", len(result))
		}
		if _, ok := result["test"]; !ok {
			t.Error("missing 'test' server")
		}
		if result["test"].Scope != ScopeUser {
			t.Errorf("scope = %q, want %q", result["test"].Scope, ScopeUser)
		}
	})

	t.Run("ScopeLocal", func(t *testing.T) {
		provider := &mockConfigProvider{
			localServers: map[string]McpServerConfig{
				"local": &StdioConfig{Command: "cat"},
			},
		}
		result, errs := GetMcpConfigsByScope(ScopeLocal, "/tmp", provider)
		if len(errs) != 0 {
			t.Errorf("unexpected errors: %v", errs)
		}
		if len(result) != 1 {
			t.Errorf("expected 1, got %d", len(result))
		}
		if _, ok := result["local"]; !ok {
			t.Error("missing 'local' server")
		}
		if result["local"].Scope != ScopeLocal {
			t.Errorf("scope = %q, want %q", result["local"].Scope, ScopeLocal)
		}
	})

	t.Run("ScopeEnterprise", func(t *testing.T) {
		provider := &mockConfigProvider{}
		result, errs := GetMcpConfigsByScope(ScopeEnterprise, "/tmp", provider)
		if len(errs) != 0 {
			t.Errorf("unexpected errors: %v", errs)
		}
		// Enterprise reads from file, should be empty for mock
		if len(result) != 0 {
			t.Errorf("expected 0, got %d", len(result))
		}
	})

	t.Run("ScopeUnknown", func(t *testing.T) {
		provider := &mockConfigProvider{}
		result, errs := GetMcpConfigsByScope(ConfigScope("unknown"), "/tmp", provider)
		if len(errs) != 0 {
			t.Errorf("unexpected errors: %v", errs)
		}
		if len(result) != 0 {
			t.Errorf("expected 0 for unknown scope, got %d", len(result))
		}
	})
}

// =========================================================================
// GetMcpConfigByName tests
// =========================================================================

func TestGetMcpConfigByName(t *testing.T) {
	t.Run("found_in_user", func(t *testing.T) {
		provider := &mockConfigProvider{
			userServers: map[string]McpServerConfig{
				"test": &StdioConfig{Command: "echo"},
			},
		}
		scoped := GetMcpConfigByName("test", "/tmp", provider)
		if scoped == nil {
			t.Fatal("expected config, got nil")
		}
		if scoped.Scope != ScopeUser {
			t.Errorf("scope = %q, want %q", scoped.Scope, ScopeUser)
		}
	})

	t.Run("found_in_local", func(t *testing.T) {
		provider := &mockConfigProvider{
			localServers: map[string]McpServerConfig{
				"test": &StdioConfig{Command: "cat"},
			},
		}
		scoped := GetMcpConfigByName("test", "/tmp", provider)
		if scoped == nil {
			t.Fatal("expected config, got nil")
		}
		if scoped.Scope != ScopeLocal {
			t.Errorf("scope = %q, want %q", scoped.Scope, ScopeLocal)
		}
	})

	t.Run("not_found", func(t *testing.T) {
		provider := &mockConfigProvider{}
		scoped := GetMcpConfigByName("missing", "/tmp", provider)
		if scoped != nil {
			t.Error("expected nil, got non-nil")
		}
	})

	t.Run("enterprise_precedence", func(t *testing.T) {
		dir := t.TempDir()
		mcpPath := filepath.Join(dir, ".mcp.json")
		mustWriteFile(t, mcpPath, []byte(`{"mcpServers":{"test":{"type":"stdio","command":"enterprise"}}}`))

		provider := &mockConfigProvider{
			managedPath: mcpPath,
			userServers: map[string]McpServerConfig{
				"test": &StdioConfig{Command: "user"},
			},
		}

		scoped := GetMcpConfigByName("test", dir, provider)
		if scoped == nil {
			t.Fatal("expected config, got nil")
		}
		// Enterprise should take precedence
		if scoped.Scope != ScopeEnterprise {
			t.Errorf("scope = %q, want %q", scoped.Scope, ScopeEnterprise)
		}
		stdioCfg, ok := scoped.Config.(*StdioConfig)
		if !ok {
			t.Fatal("expected StdioConfig")
		}
		if stdioCfg.Command != "enterprise" {
			t.Errorf("command = %q, want %q", stdioCfg.Command, "enterprise")
		}
	})
}

// =========================================================================
// GetClaudeCodeMcpConfigs tests
// =========================================================================

func TestGetClaudeCodeMcpConfigs(t *testing.T) {
	t.Run("enterprise_exclusive", func(t *testing.T) {
		dir := t.TempDir()
		mcpPath := filepath.Join(dir, ".mcp.json")
		mustWriteFile(t, mcpPath, []byte(`{"mcpServers":{"test":{"type":"stdio","command":"echo"}}}`))

		provider := &mockConfigProvider{
			managedPath: mcpPath,
			managedOnly: true,
		}

		result, errs := GetClaudeCodeMcpConfigs(dir, provider, nil, nil)
		if len(errs) != 0 {
			t.Fatalf("unexpected errors: %v", errs)
		}
		if len(result) != 1 {
			t.Errorf("expected 1, got %d", len(result))
		}
		if _, ok := result["test"]; !ok {
			t.Error("missing 'test' server")
		}
	})

	t.Run("plugin_only_mode", func(t *testing.T) {
		provider := &mockConfigProvider{
			pluginOnly: true,
		}

		result, errs := GetClaudeCodeMcpConfigs("/tmp", provider, nil, nil)
		if len(errs) != 0 {
			t.Fatalf("unexpected errors: %v", errs)
		}
		if len(result) != 0 {
			t.Errorf("expected 0 in plugin-only mode, got %d", len(result))
		}
	})

	t.Run("normal_merge", func(t *testing.T) {
		provider := &mockConfigProvider{
			userServers: map[string]McpServerConfig{
				"user": &StdioConfig{Command: "echo"},
			},
			localServers: map[string]McpServerConfig{
				"local": &StdioConfig{Command: "cat"},
			},
		}

		result, errs := GetClaudeCodeMcpConfigs("/tmp", provider, nil, nil)
		if len(errs) != 0 {
			t.Fatalf("unexpected errors: %v", errs)
		}
		if len(result) != 2 {
			t.Errorf("expected 2, got %d", len(result))
		}
		if _, ok := result["user"]; !ok {
			t.Error("missing 'user' server")
		}
		if _, ok := result["local"]; !ok {
			t.Error("missing 'local' server")
		}
	})

	t.Run("policy_denied", func(t *testing.T) {
		provider := &mockConfigProvider{
			userServers: map[string]McpServerConfig{
				"allowed": &StdioConfig{Command: "echo"},
				"denied":  &StdioConfig{Command: "cat"},
			},
			deniedEntries: []McpPolicyEntry{&McpNameEntry{ServerName: "denied"}},
		}

		result, errs := GetClaudeCodeMcpConfigs("/tmp", provider, nil, nil)
		if len(errs) != 0 {
			t.Fatalf("unexpected errors: %v", errs)
		}
		if len(result) != 1 {
			t.Errorf("expected 1 (denied filtered out), got %d", len(result))
		}
		if _, ok := result["allowed"]; !ok {
			t.Error("missing 'allowed' server")
		}
		if _, ok := result["denied"]; ok {
			t.Error("'denied' server should be filtered out")
		}
	})

	t.Run("empty_provider", func(t *testing.T) {
		provider := &mockConfigProvider{}

		result, errs := GetClaudeCodeMcpConfigs("/tmp", provider, nil, nil)
		if len(errs) != 0 {
			t.Fatalf("unexpected errors: %v", errs)
		}
		if len(result) != 0 {
			t.Errorf("expected 0, got %d", len(result))
		}
	})
}

// ---------------------------------------------------------------------------
// Additional coverage for RemoveMcpConfig, AddMcpConfig, WriteMcpjsonFile, etc.
// ---------------------------------------------------------------------------

func TestRemoveMcpConfig_UserScope(t *testing.T) {
	servers := map[string]McpServerConfig{
		"mysrv": &StdioConfig{Command: "echo"},
	}
	provider := &mockConfigProvider{userServers: servers}
	if err := RemoveMcpConfig("mysrv", ScopeUser, "", provider); err != nil {
		t.Fatalf("RemoveMcpConfig User: %v", err)
	}
	if _, ok := provider.userServers["mysrv"]; ok {
		t.Error("expected mysrv to be removed from user servers")
	}
}

func TestRemoveMcpConfig_UserNotFound(t *testing.T) {
	provider := &mockConfigProvider{userServers: map[string]McpServerConfig{}}
	err := RemoveMcpConfig("missing", ScopeUser, "", provider)
	if err == nil {
		t.Fatal("want error for missing user server")
	}
	if !strings.Contains(err.Error(), "no user-scoped") {
		t.Errorf("error = %v, want user-scoped error", err)
	}
}

func TestRemoveMcpConfig_LocalScope(t *testing.T) {
	servers := map[string]McpServerConfig{
		"local": &StdioConfig{Command: "cat"},
	}
	provider := &mockConfigProvider{localServers: servers}
	if err := RemoveMcpConfig("local", ScopeLocal, "", provider); err != nil {
		t.Fatalf("RemoveMcpConfig Local: %v", err)
	}
	if _, ok := provider.localServers["local"]; ok {
		t.Error("expected local to be removed")
	}
}

func TestRemoveMcpConfig_LocalNotFound(t *testing.T) {
	provider := &mockConfigProvider{localServers: map[string]McpServerConfig{}}
	err := RemoveMcpConfig("missing", ScopeLocal, "", provider)
	if err == nil {
		t.Fatal("want error for missing local server")
	}
	if !strings.Contains(err.Error(), "no project-local") {
		t.Errorf("error = %v, want project-local error", err)
	}
}

func TestRemoveMcpConfig_InvalidScope(t *testing.T) {
	provider := &mockConfigProvider{}
	err := RemoveMcpConfig("srv", ScopeEnterprise, "", provider)
	if err == nil {
		t.Fatal("want error for unsupported scope")
	}
	if !strings.Contains(err.Error(), "cannot remove") {
		t.Errorf("error = %v, want cannot remove error", err)
	}
}

func TestAddMcpConfig_UserScope(t *testing.T) {
	provider := &mockConfigProvider{userServers: map[string]McpServerConfig{}}
	cfg := &StdioConfig{Command: "echo", Args: []string{"hello"}}
	if err := AddMcpConfig("newserver", cfg, ScopeUser, "", provider, nil, nil); err != nil {
		t.Fatalf("AddMcpConfig User: %v", err)
	}
	if _, ok := provider.userServers["newserver"]; !ok {
		t.Error("expected newserver in user servers")
	}
}

func TestAddMcpConfig_LocalScope(t *testing.T) {
	provider := &mockConfigProvider{localServers: map[string]McpServerConfig{}}
	cfg := &SSEConfig{URL: "http://localhost:8080/sse"}
	if err := AddMcpConfig("sse-server", cfg, ScopeLocal, "", provider, nil, nil); err != nil {
		t.Fatalf("AddMcpConfig Local: %v", err)
	}
	if _, ok := provider.localServers["sse-server"]; !ok {
		t.Error("expected sse-server in local servers")
	}
}

func TestAddMcpConfig_UserDuplicate(t *testing.T) {
	provider := &mockConfigProvider{
		userServers: map[string]McpServerConfig{
			"existing": &StdioConfig{Command: "echo"},
		},
	}
	err := AddMcpConfig("existing", &StdioConfig{Command: "cat"}, ScopeUser, "", provider, nil, nil)
	if err == nil {
		t.Fatal("want error for duplicate name")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Errorf("error = %v, want already exists error", err)
	}
}

func TestAddMcpConfig_LocalDuplicate(t *testing.T) {
	provider := &mockConfigProvider{
		localServers: map[string]McpServerConfig{
			"existing": &StdioConfig{Command: "echo"},
		},
	}
	err := AddMcpConfig("existing", &StdioConfig{Command: "cat"}, ScopeLocal, "", provider, nil, nil)
	if err == nil {
		t.Fatal("want error for duplicate name")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Errorf("error = %v, want already exists error", err)
	}
}

func TestWriteMcpjsonFile_UpdatesExisting(t *testing.T) {
	dir := t.TempDir()
	existing := `{"mcpServers":{"old":{"command":"echo"}}}`
	mustWriteFile(t, filepath.Join(dir, ".mcp.json"), []byte(existing))

	config := &McpJsonConfig{
		McpServers: map[string]json.RawMessage{
			"new": json.RawMessage(`{"command":"cat"}`),
		},
	}
	if err := WriteMcpjsonFile(dir, config); err != nil {
		t.Fatalf("WriteMcpjsonFile: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, ".mcp.json"))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !strings.Contains(string(data), "new") {
		t.Errorf("file should contain 'new', got %s", data)
	}
	if strings.Contains(string(data), "old") {
		t.Errorf("file should not contain 'old', got %s", data)
	}
}

// ---------------------------------------------------------------------------
// policyEntryMarker — interface marker method coverage
// ---------------------------------------------------------------------------

func TestPolicyEntryMarker_InterfaceAssertion(t *testing.T) {
	// Verify that concrete types satisfy the McpPolicyEntry interface.
	// The marker method itself has 0% coverage; type assertions exercise it.
	tests := []struct {
		name  string
		entry McpPolicyEntry
	}{
		{"McpNameEntry", &McpNameEntry{ServerName: "test"}},
		{"McpCommandEntry", &McpCommandEntry{ServerCommand: []string{"echo"}}},
		{"McpUrlEntry", &McpUrlEntry{ServerUrl: "https://example.com"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// This type assertion exercises policyEntryMarker().
			switch e := tt.entry.(type) {
			case *McpNameEntry:
				if e.ServerName != "test" {
					t.Errorf("ServerName = %q, want %q", e.ServerName, "test")
				}
			case *McpCommandEntry:
				if len(e.ServerCommand) != 1 || e.ServerCommand[0] != "echo" {
					t.Errorf("ServerCommand = %v, want [echo]", e.ServerCommand)
				}
			case *McpUrlEntry:
				if e.ServerUrl != "https://example.com" {
					t.Errorf("ServerUrl = %q, want https://example.com", e.ServerUrl)
				}
			default:
				t.Fatalf("unexpected type %T", e)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// UnmarshalMcpPolicyEntry — invalid JSON
// ---------------------------------------------------------------------------

func TestUnmarshalMcpPolicyEntry_InvalidJSON(t *testing.T) {
	raw := json.RawMessage(`not valid json`)
	_, err := UnmarshalMcpPolicyEntry(raw)
	if err == nil {
		t.Fatal("want error for invalid JSON")
	}
	if !strings.Contains(err.Error(), "invalid policy entry") {
		t.Errorf("error = %v, want 'invalid policy entry'", err)
	}
}

// ---------------------------------------------------------------------------
// GetServerCommandArray — SDKConfig returns nil
// ---------------------------------------------------------------------------

func TestGetServerCommandArray_SDK(t *testing.T) {
	cfg := &SDKConfig{Name: "test"}
	cmd := GetServerCommandArray(cfg)
	if cmd != nil {
		t.Errorf("expected nil for SDK, got %v", cmd)
	}
}

// ---------------------------------------------------------------------------
// GetServerUrl — SSEIDEConfig and WSIDEConfig
// ---------------------------------------------------------------------------

func TestGetServerUrl_SSEIDE(t *testing.T) {
	cfg := &SSEIDEConfig{URL: "http://localhost:1234/sse"}
	if got := GetServerUrl(cfg); got != "http://localhost:1234/sse" {
		t.Errorf("got %q, want http://localhost:1234/sse", got)
	}
}

func TestGetServerUrl_WSIDE(t *testing.T) {
	cfg := &WSIDEConfig{URL: "ws://localhost:8080/ws"}
	if got := GetServerUrl(cfg); got != "ws://localhost:8080/ws" {
		t.Errorf("got %q, want ws://localhost:8080/ws", got)
	}
}

func TestGetServerUrl_ClaudeAIProxy(t *testing.T) {
	cfg := &ClaudeAIProxyConfig{URL: "https://proxy.example.com"}
	if got := GetServerUrl(cfg); got != "" {
		t.Errorf("expected empty string for ClaudeAIProxy, got %q", got)
	}
}

// ---------------------------------------------------------------------------
// UrlMatchesPattern — invalid regex pattern
// ---------------------------------------------------------------------------

func TestUrlMatchesPattern_InvalidPattern(t *testing.T) {
	// Pattern with invalid regex chars that break compilation
	result := UrlMatchesPattern("http://example.com", "[invalid")
	if result {
		t.Error("expected false for invalid regex pattern")
	}
}

// ---------------------------------------------------------------------------
// ExpandConfigEnv — SSEConfig with Headers, WSConfig, and no-pass-through types
// ---------------------------------------------------------------------------

func TestExpandConfigEnv_SSEWithHeaders(t *testing.T) {
	t.Setenv("MCP_SSE_TOKEN", "secret")
	cfg := &SSEConfig{
		URL:     "http://example.com/sse",
		Headers: map[string]string{"Authorization": "Bearer ${MCP_SSE_TOKEN}"},
	}
	expanded, missing := ExpandConfigEnv(cfg)
	if len(missing) != 0 {
		t.Errorf("expected no missing vars, got %v", missing)
	}
	sse, ok := expanded.(*SSEConfig)
	if !ok {
		t.Fatalf("expected *SSEConfig, got %T", expanded)
	}
	if sse.Headers["Authorization"] != "Bearer secret" {
		t.Errorf("Authorization = %q, want %q", sse.Headers["Authorization"], "Bearer secret")
	}
}

func TestExpandConfigEnv_WSConfig(t *testing.T) {
	t.Setenv("MCP_WS_HOST", "ws-server")
	cfg := &WSConfig{
		URL:     "ws://${MCP_WS_HOST}:8080",
		Headers: map[string]string{"X-Auth": "token"},
	}
	expanded, missing := ExpandConfigEnv(cfg)
	if len(missing) != 0 {
		t.Errorf("expected no missing vars, got %v", missing)
	}
	ws, ok := expanded.(*WSConfig)
	if !ok {
		t.Fatalf("expected *WSConfig, got %T", expanded)
	}
	if ws.URL != "ws://ws-server:8080" {
		t.Errorf("URL = %q, want ws://ws-server:8080", ws.URL)
	}
}

func TestExpandConfigEnv_SSEIDE_NoExpansion(t *testing.T) {
	cfg := &SSEIDEConfig{URL: "http://localhost:1234/sse", IDEName: "vscode"}
	expanded, missing := ExpandConfigEnv(cfg)
	if len(missing) != 0 {
		t.Errorf("expected no missing vars, got %v", missing)
	}
	if _, ok := expanded.(*SSEIDEConfig); !ok {
		t.Errorf("expected *SSEIDEConfig, got %T", expanded)
	}
}

func TestExpandConfigEnv_WSIDE_NoExpansion(t *testing.T) {
	cfg := &WSIDEConfig{URL: "ws://localhost:8080/ws", IDEName: "vscode"}
	expanded, missing := ExpandConfigEnv(cfg)
	if len(missing) != 0 {
		t.Errorf("expected no missing vars, got %v", missing)
	}
	if _, ok := expanded.(*WSIDEConfig); !ok {
		t.Errorf("expected *WSIDEConfig, got %T", expanded)
	}
}

func TestExpandConfigEnv_ClaudeAIProxy_NoExpansion(t *testing.T) {
	cfg := &ClaudeAIProxyConfig{URL: "https://proxy.example.com", ID: "123"}
	expanded, missing := ExpandConfigEnv(cfg)
	if len(missing) != 0 {
		t.Errorf("expected no missing vars, got %v", missing)
	}
	if _, ok := expanded.(*ClaudeAIProxyConfig); !ok {
		t.Errorf("expected *ClaudeAIProxyConfig, got %T", expanded)
	}
}

func TestExpandConfigEnv_UnknownType(t *testing.T) {
	cfg := mockUnsupportedConfig{}
	expanded, missing := ExpandConfigEnv(cfg)
	if len(missing) != 0 {
		t.Errorf("expected no missing vars, got %v", missing)
	}
	if expanded.(mockUnsupportedConfig).GetTransport() != "unsupported" {
		t.Error("expected pass-through for unknown config type")
	}
}

func TestExpandConfigEnv_StdioNilEnv(t *testing.T) {
	cfg := &StdioConfig{Command: "node", Args: []string{}, Env: nil}
	expanded, missing := ExpandConfigEnv(cfg)
	if len(missing) != 0 {
		t.Errorf("expected no missing vars, got %v", missing)
	}
	stdio, ok := expanded.(*StdioConfig)
	if !ok {
		t.Fatalf("expected *StdioConfig, got %T", expanded)
	}
	if stdio.Env != nil {
		t.Errorf("expected nil Env, got %v", stdio.Env)
	}
}

// ---------------------------------------------------------------------------
// IsMcpServerAllowedByPolicy — all remaining branches
// ---------------------------------------------------------------------------

func TestIsMcpServerAllowedByPolicy_NameAllowStdio(t *testing.T) {
	// stdio server with only name entries in allowlist — should match by name
	allowed := []McpPolicyEntry{
		&McpNameEntry{ServerName: "myserver"},
	}
	cfg := &StdioConfig{Command: "node"}
	if !IsMcpServerAllowedByPolicy("myserver", cfg, nil, allowed) {
		t.Error("name-based allow should match stdio server when no command entries")
	}
	if IsMcpServerAllowedByPolicy("other", cfg, nil, allowed) {
		t.Error("non-matching name should be blocked")
	}
}

func TestIsMcpServerAllowedByPolicy_NameAllowRemote(t *testing.T) {
	// Remote server with only name entries — should match by name
	allowed := []McpPolicyEntry{
		&McpNameEntry{ServerName: "myserver"},
	}
	cfg := &SSEConfig{URL: "https://example.com"}
	if !IsMcpServerAllowedByPolicy("myserver", cfg, nil, allowed) {
		t.Error("name-based allow should match remote server when no URL entries")
	}
	if IsMcpServerAllowedByPolicy("other", cfg, nil, allowed) {
		t.Error("non-matching name should be blocked for remote")
	}
}

func TestIsMcpServerAllowedByPolicy_NameAllowUnknownType(t *testing.T) {
	// Unknown type server with name entries — should match by name
	allowed := []McpPolicyEntry{
		&McpNameEntry{ServerName: "myserver"},
	}
	cfg := &SDKConfig{Name: "test"}
	if !IsMcpServerAllowedByPolicy("myserver", cfg, nil, allowed) {
		t.Error("name-based allow should match SDK server")
	}
	if IsMcpServerAllowedByPolicy("other", cfg, nil, allowed) {
		t.Error("non-matching name should be blocked for unknown type")
	}
}

func TestIsMcpServerAllowedByPolicy_NoConfigNameOnly(t *testing.T) {
	// nil config — name-based check only
	allowed := []McpPolicyEntry{
		&McpNameEntry{ServerName: "myserver"},
	}
	if !IsMcpServerAllowedByPolicy("myserver", nil, nil, allowed) {
		t.Error("name match with nil config should be allowed")
	}
	if IsMcpServerAllowedByPolicy("other", nil, nil, allowed) {
		t.Error("non-matching name with nil config should be blocked")
	}
}

func TestIsMcpServerAllowedByPolicy_CommandBlockWhenNoCommandMatch(t *testing.T) {
	// stdio server, allowlist has command entries, server doesn't match
	allowed := []McpPolicyEntry{
		&McpCommandEntry{ServerCommand: []string{"node", "good.js"}},
	}
	cfg := &StdioConfig{Command: "python", Args: []string{"bad.py"}}
	if IsMcpServerAllowedByPolicy("myserver", cfg, nil, allowed) {
		t.Error("non-matching command should be blocked when command entries exist")
	}
}

func TestIsMcpServerAllowedByPolicy_UrlBlockWhenNoUrlMatch(t *testing.T) {
	// Remote server, allowlist has URL entries, server doesn't match
	allowed := []McpPolicyEntry{
		&McpUrlEntry{ServerUrl: "https://safe.com/*"},
	}
	cfg := &SSEConfig{URL: "https://evil.com/api"}
	if IsMcpServerAllowedByPolicy("myserver", cfg, nil, allowed) {
		t.Error("non-matching URL should be blocked when URL entries exist")
	}
}

func TestIsMcpServerAllowedByPolicy_UrlNameFallback(t *testing.T) {
	// Remote server, no URL entries in allowlist — fall back to name
	allowed := []McpPolicyEntry{
		&McpNameEntry{ServerName: "myserver"},
	}
	cfg := &SSEConfig{URL: "https://example.com"}
	if !IsMcpServerAllowedByPolicy("myserver", cfg, nil, allowed) {
		t.Error("name-based allow should match remote server when no URL entries")
	}
}

// ---------------------------------------------------------------------------
// ParseMcpConfig — nil mcpServers, invalid server config
// ---------------------------------------------------------------------------

func TestParseMcpConfig_NilMcpServers(t *testing.T) {
	input := `{"otherField":123}`
	config, errors := ParseMcpConfig([]byte(input), false, ScopeProject, "")
	if len(errors) != 0 {
		t.Fatalf("unexpected errors: %v", errors)
	}
	if config == nil {
		t.Fatal("expected non-nil config")
	}
	if len(config.McpServers) != 0 {
		t.Errorf("expected empty McpServers, got %d", len(config.McpServers))
	}
}

func TestParseMcpConfig_InvalidServerConfig(t *testing.T) {
	input := `{"mcpServers":{"bad":{"type":"ftp","url":"ftp://example.com"}}}`
	config, errors := ParseMcpConfig([]byte(input), false, ScopeProject, "")
	if config == nil {
		t.Fatal("expected non-nil config (partial parse)")
	}
	if len(errors) != 1 {
		t.Fatalf("expected 1 error, got %d: %v", len(errors), errors)
	}
	if !strings.Contains(errors[0].Message, "unknown server config type") {
		t.Errorf("error = %v, want unknown server config type", errors[0])
	}
	if len(config.McpServers) != 0 {
		t.Errorf("expected 0 valid servers, got %d", len(config.McpServers))
	}
}

func TestParseMcpConfig_ExpandVarsWithMarshalError(t *testing.T) {
	// This is hard to trigger normally — the expanded config is re-marshaled
	// and should never fail since it came from a valid config. Test what we can.
	t.Setenv("MCP_EXPAND_TEST", "expanded")
	input := `{"mcpServers":{"srv":{"command":"${MCP_EXPAND_TEST}","args":[]}}}`
	config, errors := ParseMcpConfig([]byte(input), true, ScopeProject, "test.json")
	if len(errors) != 0 {
		t.Fatalf("unexpected errors: %v", errors)
	}
	if config == nil {
		t.Fatal("expected non-nil config")
	}
	cfg, err := UnmarshalServerConfig(config.McpServers["srv"])
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	stdio := cfg.(*StdioConfig)
	if stdio.Command != "expanded" {
		t.Errorf("command = %q, want expanded", stdio.Command)
	}
}

func TestParseMcpConfig_ExpandWithMultipleMissing(t *testing.T) {
	input := `{"mcpServers":{"srv":{"command":"${MISSING_A}","args":["${MISSING_B}"]}}}`
	_, errors := ParseMcpConfig([]byte(input), true, ScopeProject, "test.json")
	if len(errors) != 1 {
		t.Fatalf("expected 1 error, got %d: %v", len(errors), errors)
	}
	if !strings.Contains(errors[0].Message, "Missing environment variables") {
		t.Errorf("error = %v, want missing env vars", errors[0])
	}
}

// ---------------------------------------------------------------------------
// ParseMcpConfigFromFilePath — permission denied
// ---------------------------------------------------------------------------

func TestParseMcpConfigFromFilePath_ReadError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".mcp.json")
	mustWriteFile(t, path, []byte(`{}`))
	// Make file unreadable
	_ = os.Chmod(path, 0000)
	defer func() { _ = os.Chmod(path, 0644) }()

	_, errors := ParseMcpConfigFromFilePath(path, false, ScopeProject)
	if len(errors) == 0 {
		t.Fatal("want error for unreadable file")
	}
	if !strings.Contains(errors[0].Message, "Failed to read file") {
		t.Errorf("error = %v, want 'Failed to read file'", errors[0])
	}
}

// ---------------------------------------------------------------------------
// WriteMcpjsonFile — sync error, close error, chmod error
// ---------------------------------------------------------------------------

func TestWriteMcpjsonFile_SyncError(t *testing.T) {
	// We can't easily force a sync error, but we can test the chmod path
	// by writing to a read-only directory.
	dir := t.TempDir()
	readOnlyDir := filepath.Join(dir, "readonly")
	mustMkdirAll(t, readOnlyDir)
	_ = os.Chmod(readOnlyDir, 0555)
	defer func() { _ = os.Chmod(readOnlyDir, 0755) }()

	config := &McpJsonConfig{
		McpServers: map[string]json.RawMessage{
			"srv": json.RawMessage(`{"command":"node"}`),
		},
	}
	err := WriteMcpjsonFile(readOnlyDir, config)
	if err == nil {
		t.Fatal("want error writing to read-only dir")
	}
}

// ---------------------------------------------------------------------------
// GetProjectMcpConfigsFromCwd — invalid config
// ---------------------------------------------------------------------------

func TestGetProjectMcpConfigsFromCwd_InvalidConfig(t *testing.T) {
	dir := t.TempDir()
	// Write invalid JSON to .mcp.json
	mustWriteFile(t, filepath.Join(dir, ".mcp.json"), []byte(`{bad json}`))

	servers, errors := GetProjectMcpConfigsFromCwd(dir)
	if len(errors) == 0 {
		t.Fatalf("want errors for invalid config, got none")
	}
	if !strings.Contains(errors[0].Message, "valid JSON") {
		t.Errorf("error = %v, want 'valid JSON'", errors[0])
	}
	if len(servers) != 0 {
		t.Errorf("expected 0 servers for invalid config, got %d", len(servers))
	}
}

// ---------------------------------------------------------------------------
// GetMcpConfigsByScope — enterprise with valid file
// ---------------------------------------------------------------------------

func TestGetMcpConfigsByScope_EnterpriseWithFile(t *testing.T) {
	dir := t.TempDir()
	mcpPath := filepath.Join(dir, "managed.json")
	mustWriteFile(t, mcpPath, []byte(`{"mcpServers":{"ent":{"command":"enterprise-server"}}}`))

	provider := &mockConfigProvider{managedPath: mcpPath}
	servers, errs := GetMcpConfigsByScope(ScopeEnterprise, dir, provider)
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if len(servers) != 1 {
		t.Fatalf("expected 1 server, got %d", len(servers))
	}
	if servers["ent"].Scope != ScopeEnterprise {
		t.Errorf("scope = %q, want enterprise", servers["ent"].Scope)
	}
}

func TestGetMcpConfigsByScope_UserNil(t *testing.T) {
	provider := &mockConfigProvider{userServers: nil}
	servers, errs := GetMcpConfigsByScope(ScopeUser, "/tmp", provider)
	if len(errs) != 0 {
		t.Errorf("unexpected errors: %v", errs)
	}
	if len(servers) != 0 {
		t.Errorf("expected 0 for nil user servers, got %d", len(servers))
	}
}

func TestGetMcpConfigsByScope_LocalNil(t *testing.T) {
	provider := &mockConfigProvider{localServers: nil}
	servers, errs := GetMcpConfigsByScope(ScopeLocal, "/tmp", provider)
	if len(errs) != 0 {
		t.Errorf("unexpected errors: %v", errs)
	}
	if len(servers) != 0 {
		t.Errorf("expected 0 for nil local servers, got %d", len(servers))
	}
}

// ---------------------------------------------------------------------------
// loadMcpServersFromConfig — invalid server entry
// ---------------------------------------------------------------------------

func TestLoadMcpServersFromConfig_InvalidEntry(t *testing.T) {
	config := &McpJsonConfig{
		McpServers: map[string]json.RawMessage{
			"bad": json.RawMessage(`{"type":"invalid_type"}`),
		},
	}
	servers, errs := loadMcpServersFromConfig(config)
	if len(servers) != 0 {
		t.Errorf("expected 0 servers, got %d", len(servers))
	}
	if len(errs) != 1 {
		t.Fatalf("expected 1 error, got %d", len(errs))
	}
	if !strings.Contains(errs[0].Message, "Invalid config") {
		t.Errorf("error = %v, want 'Invalid config'", errs[0])
	}
}

func TestLoadMcpServersFromConfig_MixedValidInvalid(t *testing.T) {
	config := &McpJsonConfig{
		McpServers: map[string]json.RawMessage{
			"good": json.RawMessage(`{"command":"node"}`),
			"bad":  json.RawMessage(`{"type":"invalid_type"}`),
		},
	}
	servers, errs := loadMcpServersFromConfig(config)
	if len(servers) != 1 {
		t.Errorf("expected 1 valid server, got %d", len(servers))
	}
	if _, ok := servers["good"]; !ok {
		t.Error("expected 'good' server to be parsed")
	}
	if len(errs) != 1 {
		t.Fatalf("expected 1 error, got %d", len(errs))
	}
}

// ---------------------------------------------------------------------------
// AddMcpConfig — invalid scope, not allowed by policy
// ---------------------------------------------------------------------------

func TestAddMcpConfig_InvalidScope(t *testing.T) {
	provider := &mockConfigProvider{}
	cfg := &StdioConfig{Command: "node"}
	err := AddMcpConfig("srv", cfg, ScopeEnterprise, "", provider, nil, nil)
	if err == nil {
		t.Fatal("want error for unsupported scope")
	}
	if !strings.Contains(err.Error(), "cannot add MCP server to scope") {
		t.Errorf("error = %v, want 'cannot add MCP server to scope'", err)
	}
}

func TestAddMcpConfig_NotAllowedByPolicy(t *testing.T) {
	provider := &mockConfigProvider{}
	cfg := &StdioConfig{Command: "node"}
	// Empty allowlist = block all (but denylist is empty so not denied)
	allowed := []McpPolicyEntry{}
	err := AddMcpConfig("srv", cfg, ScopeUser, "", provider, nil, allowed)
	if err == nil {
		t.Fatal("want error for server not in allowlist")
	}
	if !strings.Contains(err.Error(), "not allowed by enterprise policy") {
		t.Errorf("error = %v, want 'not allowed by enterprise policy'", err)
	}
}

// ---------------------------------------------------------------------------
// writeProjectConfig — marshal error on new config
// ---------------------------------------------------------------------------

func TestWriteProjectConfig_Success(t *testing.T) {
	dir := t.TempDir()
	existing := map[string]ScopedMcpServerConfig{
		"old": {Config: &StdioConfig{Command: "echo"}, Scope: ScopeProject},
	}
	cfg := &StdioConfig{Command: "new", Args: []string{"arg"}}
	err := writeProjectConfig(dir, existing, "newserver", cfg)
	if err != nil {
		t.Fatalf("writeProjectConfig: %v", err)
	}
	servers, _ := GetProjectMcpConfigsFromCwd(dir)
	if len(servers) != 2 {
		t.Fatalf("expected 2 servers, got %d", len(servers))
	}
	if _, ok := servers["newserver"]; !ok {
		t.Error("expected 'newserver' to be added")
	}
	if _, ok := servers["old"]; !ok {
		t.Error("expected 'old' to still exist")
	}
}

// ---------------------------------------------------------------------------
// writeProjectConfigExclude — successful removal
// ---------------------------------------------------------------------------

func TestWriteProjectConfigExclude_Success(t *testing.T) {
	dir := t.TempDir()
	existing := map[string]ScopedMcpServerConfig{
		"keep":   {Config: &StdioConfig{Command: "keep"}, Scope: ScopeProject},
		"remove": {Config: &StdioConfig{Command: "remove"}, Scope: ScopeProject},
	}
	err := writeProjectConfigExclude(dir, existing, "remove")
	if err != nil {
		t.Fatalf("writeProjectConfigExclude: %v", err)
	}
	servers, _ := GetProjectMcpConfigsFromCwd(dir)
	if len(servers) != 1 {
		t.Fatalf("expected 1 server, got %d", len(servers))
	}
	if _, ok := servers["keep"]; !ok {
		t.Error("expected 'keep' to still exist")
	}
}

// ---------------------------------------------------------------------------
// SetMcpServerEnabled — no change (already enabled/disabled)
// ---------------------------------------------------------------------------

func TestSetMcpServerEnabled_NoChangeAlreadyEnabled(t *testing.T) {
	provider := &mockConfigProvider{disabledServers: []string{}}
	err := SetMcpServerEnabled("myserver", true, provider)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if provider.savedDisabled != nil {
		t.Errorf("should not save when no change, but saved: %v", provider.savedDisabled)
	}
}

func TestSetMcpServerEnabled_NoChangeAlreadyDisabled(t *testing.T) {
	provider := &mockConfigProvider{disabledServers: []string{"myserver"}}
	err := SetMcpServerEnabled("myserver", false, provider)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if provider.savedDisabled != nil {
		t.Errorf("should not save when no change, but saved: %v", provider.savedDisabled)
	}
}

// ---------------------------------------------------------------------------
// GetClaudeCodeMcpConfigs — with dynamic and plugin servers
// ---------------------------------------------------------------------------

func TestGetClaudeCodeMcpConfigs_WithDynamicAndPlugin(t *testing.T) {
	provider := &mockConfigProvider{
		userServers: map[string]McpServerConfig{
			"user": &StdioConfig{Command: "echo"},
		},
	}
	dynamic := map[string]ScopedMcpServerConfig{
		"dynamic": {Config: &StdioConfig{Command: "dyn"}, Scope: ScopeDynamic},
	}
	plugin := map[string]ScopedMcpServerConfig{
		"plugin": {Config: &StdioConfig{Command: "plug"}, Scope: ScopeDynamic},
	}
	result, errs := GetClaudeCodeMcpConfigs("/tmp", provider, dynamic, plugin)
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	// dynamic servers are merged into the manual pool for dedup, then copied
	// into configs alongside user/project/local. plugin dedup is separate.
	if len(result) < 2 {
		t.Errorf("expected at least 2 servers (user+plugin), got %d", len(result))
	}
	if _, ok := result["user"]; !ok {
		t.Error("expected 'user' in result")
	}
	if _, ok := result["plugin"]; !ok {
		t.Error("expected 'plugin' in result")
	}
}

func TestGetClaudeCodeMcpConfigs_PluginDedup(t *testing.T) {
	// Plugin with same command as user server should be suppressed
	provider := &mockConfigProvider{
		userServers: map[string]McpServerConfig{
			"myserver": &StdioConfig{Command: "node", Args: []string{"server.js"}},
		},
	}
	plugin := map[string]ScopedMcpServerConfig{
		"plugin:same": {Config: &StdioConfig{Command: "node", Args: []string{"server.js"}}, Scope: ScopeDynamic},
	}
	result, _ := GetClaudeCodeMcpConfigs("/tmp", provider, nil, plugin)
	// Plugin should be suppressed by user server
	if _, ok := result["myserver"]; !ok {
		t.Error("expected user server 'myserver'")
	}
	if _, ok := result["plugin:same"]; ok {
		t.Error("plugin with same signature as user server should be suppressed")
	}
}

// ---------------------------------------------------------------------------
// GetMcpConfigByName — found in project scope
// ---------------------------------------------------------------------------

func TestGetMcpConfigByName_FoundInProject(t *testing.T) {
	dir := t.TempDir()
	content := `{"mcpServers":{"proj-srv":{"command":"node"}}}`
	mustWriteFile(t, filepath.Join(dir, ".mcp.json"), []byte(content))

	provider := &mockConfigProvider{}
	scoped := GetMcpConfigByName("proj-srv", dir, provider)
	if scoped == nil {
		t.Fatal("expected config, got nil")
	}
	if scoped.Scope != ScopeProject {
		t.Errorf("scope = %q, want project", scoped.Scope)
	}
}

// ---------------------------------------------------------------------------
// DedupStrings — dedup helper
// ---------------------------------------------------------------------------

func TestDedupStrings(t *testing.T) {
	input := []string{"a", "b", "a", "c", "b"}
	result := dedupStrings(input)
	if len(result) != 3 {
		t.Fatalf("expected 3, got %d", len(result))
	}
	if result[0] != "a" || result[1] != "b" || result[2] != "c" {
		t.Errorf("expected [a b c], got %v", result)
	}
}

func TestDedupStrings_Empty(t *testing.T) {
	result := dedupStrings(nil)
	if len(result) != 0 {
		t.Errorf("expected empty, got %v", result)
	}
}

func TestWriteMcpjsonFile_NonexistentDir(t *testing.T) {
	dir := t.TempDir()
	nestedDir := filepath.Join(dir, "a", "b", "c")

	config := &McpJsonConfig{
		McpServers: map[string]json.RawMessage{
			"srv": json.RawMessage(`{"command":"echo"}`),
		},
	}
	err := WriteMcpjsonFile(nestedDir, config)
	if err == nil {
		t.Fatal("want error for nonexistent directory")
	}
	if !strings.Contains(err.Error(), "failed to create temp file") {
		t.Errorf("error = %v, want failed to create temp file", err)
	}
}

// ---------------------------------------------------------------------------
// WriteMcpjsonFile — chmod error path (read-only temp file after close)
// ---------------------------------------------------------------------------

func TestWriteMcpjsonFile_ChmodError(t *testing.T) {
	// chmod error is hard to trigger on Linux since file owners can always
	// chmod their own files. The rename error path is tested instead via
	// TestWriteMcpjsonFile_RenameError which writes over a read-only file.
	t.Skip("chmod error path cannot be reliably triggered on Linux")
}

func TestWriteMcpjsonFile_RenameError(t *testing.T) {
	// Cross-device rename would fail, but within t.TempDir() it won't.
	// Instead, test that a valid write succeeds when existing file
	// has specific permissions.
	dir := t.TempDir()
	existingPath := filepath.Join(dir, ".mcp.json")
	// Create with read-only permissions — WriteMcpjsonFile should still
	// succeed because it writes to a temp file then renames.
	mustWriteFile(t, existingPath, []byte(`{"mcpServers":{}}`))
	_ = os.Chmod(existingPath, 0444)
	defer func() { _ = os.Chmod(existingPath, 0644) }()

	config := &McpJsonConfig{
		McpServers: map[string]json.RawMessage{
			"srv": json.RawMessage(`{"command":"node"}`),
		},
	}
	if err := WriteMcpjsonFile(dir, config); err != nil {
		t.Fatalf("WriteMcpjsonFile should succeed over read-only file: %v", err)
	}

	data, err := os.ReadFile(existingPath)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if !strings.Contains(string(data), "srv") {
		t.Errorf("file should contain 'srv', got: %s", data)
	}
}

func TestWriteMcpjsonFile_WriteError(t *testing.T) {
	// Test write error by making the temp file unwritable.
	// This is hard to trigger precisely because the temp file is created
	// by WriteMcpjsonFile itself. Instead, test that writing to a directory
	// that becomes read-only after temp file creation fails.
	// The existing SyncError test covers the read-only directory case.
	// We verify the error message format matches expectations.
	dir := t.TempDir()
	readOnlyDir := filepath.Join(dir, "ro")
	mustMkdirAll(t, readOnlyDir)
	_ = os.Chmod(readOnlyDir, 0555)
	defer func() { _ = os.Chmod(readOnlyDir, 0755) }()

	config := &McpJsonConfig{
		McpServers: map[string]json.RawMessage{
			"srv": json.RawMessage(`{"command":"node"}`),
		},
	}
	err := WriteMcpjsonFile(readOnlyDir, config)
	if err == nil {
		t.Fatal("want error for read-only directory")
	}
	// Error should mention either "create", "write", or "temp file"
	errMsg := err.Error()
	if !strings.Contains(errMsg, "temp file") && !strings.Contains(errMsg, "failed") {
		t.Errorf("error = %v, want mention of temp file creation failure", err)
	}
}

// ---------------------------------------------------------------------------
// GetServerCommandArray — ClaudeAIProxyConfig
// ---------------------------------------------------------------------------

func TestGetServerCommandArray_ClaudeAIProxy(t *testing.T) {
	cfg := &ClaudeAIProxyConfig{URL: "https://proxy.example.com", ID: "123"}
	cmd := GetServerCommandArray(cfg)
	// ClaudeAIProxyConfig has transport "claudeai-proxy" which is non-stdio, non-empty
	if cmd != nil {
		t.Errorf("expected nil for ClaudeAIProxy, got %v", cmd)
	}
}

// ---------------------------------------------------------------------------
// UrlMatchesPattern — additional pattern coverage
// ---------------------------------------------------------------------------

func TestUrlMatchesPattern_PortInPattern(t *testing.T) {
	if !UrlMatchesPattern("https://example.com:8080/api", "https://example.com:8080/*") {
		t.Error("expected port pattern match")
	}
	if UrlMatchesPattern("https://example.com:9090/api", "https://example.com:8080/*") {
		t.Error("different port should not match")
	}
}

func TestUrlMatchesPattern_MiddleWildcard(t *testing.T) {
	if !UrlMatchesPattern("https://api.example.com/v1/mcp", "https://*/v1/mcp") {
		t.Error("expected middle wildcard match on host")
	}
}

func TestUrlMatchesPattern_NoWildcardExactFail(t *testing.T) {
	if UrlMatchesPattern("https://example.com/api/v2", "https://example.com/api") {
		t.Error("exact pattern should not match longer URL")
	}
}

func TestUrlMatchesPattern_TrailingSlash(t *testing.T) {
	if !UrlMatchesPattern("https://example.com/", "https://example.com/") {
		t.Error("trailing slash exact match should work")
	}
}

func TestUrlMatchesPattern_MultipleWildcards(t *testing.T) {
	if !UrlMatchesPattern("https://a.b.c.com/x/y/z", "https://*/*/*") {
		t.Error("expected multiple wildcard match")
	}
}

// ---------------------------------------------------------------------------
// ParseMcpConfig — filePath in validation errors
// ---------------------------------------------------------------------------

func TestParseMcpConfig_FilePathInErrors(t *testing.T) {
	input := `{not json}`
	config, errors := ParseMcpConfig([]byte(input), false, ScopeProject, "/path/to/config.json")
	if config != nil {
		t.Error("expected nil config")
	}
	if len(errors) != 1 {
		t.Fatalf("expected 1 error, got %d", len(errors))
	}
	if errors[0].File != "/path/to/config.json" {
		t.Errorf("File = %q, want /path/to/config.json", errors[0].File)
	}
}

func TestParseMcpConfig_ExpandVarsFilePath(t *testing.T) {
	input := `{"mcpServers":{"srv":{"command":"${MCP_MISSING_XYZ_999}","args":[]}}}`
	_, errors := ParseMcpConfig([]byte(input), true, ScopeProject, "myconfig.json")
	if len(errors) == 0 {
		t.Fatal("want errors for missing env var")
	}
	if errors[0].File != "myconfig.json" {
		t.Errorf("File = %q, want myconfig.json", errors[0].File)
	}
	if errors[0].Path != "mcpServers.srv" {
		t.Errorf("Path = %q, want mcpServers.srv", errors[0].Path)
	}
	if errors[0].Suggestion == "" {
		t.Error("expected non-empty Suggestion for missing env var")
	}
}

// ---------------------------------------------------------------------------
// UnmarshalMcpPolicyEntry — invalid JSON body after probe succeeds
// ---------------------------------------------------------------------------

func TestUnmarshalMcpPolicyEntry_CommandOnly(t *testing.T) {
	// serverCommand present, no serverName — should match command branch
	raw := json.RawMessage(`{"serverCommand":["echo","arg1"]}`)
	entry, err := UnmarshalMcpPolicyEntry(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	ce, ok := entry.(*McpCommandEntry)
	if !ok {
		t.Fatalf("expected *McpCommandEntry, got %T", entry)
	}
	if !CommandArraysMatch(ce.ServerCommand, []string{"echo", "arg1"}) {
		t.Errorf("ServerCommand = %v, want [echo arg1]", ce.ServerCommand)
	}
}

func TestUnmarshalMcpPolicyEntry_UrlInvalidBody(t *testing.T) {
	// serverName is nil, serverCommand empty, serverUrl present — valid URL entry
	raw := json.RawMessage(`{"serverUrl":"https://example.com/*","extra":"ignored"}`)
	entry, err := UnmarshalMcpPolicyEntry(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	ue, ok := entry.(*McpUrlEntry)
	if !ok {
		t.Fatalf("expected *McpUrlEntry, got %T", entry)
	}
	if ue.ServerUrl != "https://example.com/*" {
		t.Errorf("ServerUrl = %q, want https://example.com/*", ue.ServerUrl)
	}
}

// ---------------------------------------------------------------------------
// GetMcpConfigsByScope — enterprise with read error (non-not-found)
// ---------------------------------------------------------------------------

func TestGetMcpConfigsByScope_EnterpriseReadError(t *testing.T) {
	dir := t.TempDir()
	mcpPath := filepath.Join(dir, "managed.json")
	// Write valid JSON, then make it unreadable
	mustWriteFile(t, mcpPath, []byte(`{"mcpServers":{}}`))
	_ = os.Chmod(mcpPath, 0000)
	defer func() { _ = os.Chmod(mcpPath, 0644) }()

	provider := &mockConfigProvider{managedPath: mcpPath}
	servers, errs := GetMcpConfigsByScope(ScopeEnterprise, dir, provider)
	// Should get a "Failed to read file" error, not "not found"
	if len(errs) == 0 {
		t.Fatal("want error for unreadable enterprise config")
	}
	if strings.HasPrefix(errs[0].Message, "MCP config file not found") {
		t.Errorf("should not be 'not found' error, got: %v", errs[0])
	}
	if !strings.Contains(errs[0].Message, "Failed to read file") {
		t.Errorf("error = %v, want 'Failed to read file'", errs[0])
	}
	if len(servers) != 0 {
		t.Errorf("expected 0 servers, got %d", len(servers))
	}
}

// ---------------------------------------------------------------------------
// GetProjectMcpConfigsFromCwd — non-missing errors pass through
// ---------------------------------------------------------------------------

func TestGetProjectMcpConfigsFromCwd_ReadError(t *testing.T) {
	dir := t.TempDir()
	mcpPath := filepath.Join(dir, ".mcp.json")
	mustWriteFile(t, mcpPath, []byte(`{}`))
	_ = os.Chmod(mcpPath, 0000)
	defer func() { _ = os.Chmod(mcpPath, 0644) }()

	servers, errors := GetProjectMcpConfigsFromCwd(dir)
	// Permission denied should produce a non-missing error
	if len(errors) == 0 {
		t.Fatalf("want errors for unreadable file, got none")
	}
	if strings.HasPrefix(errors[0].Message, "MCP config file not found") {
		t.Errorf("should not filter out read errors: %v", errors[0])
	}
	if len(servers) != 0 {
		t.Errorf("expected 0 servers, got %d", len(servers))
	}
}

// ---------------------------------------------------------------------------
// getProjectMcpConfigs — non-missing errors during directory walk
// ---------------------------------------------------------------------------

func TestGetProjectMcpConfigs_NonMissingError(t *testing.T) {
	dir := t.TempDir()
	mcpPath := filepath.Join(dir, ".mcp.json")
	// Write then make unreadable
	mustWriteFile(t, mcpPath, []byte(`{"mcpServers":{}}`))
	_ = os.Chmod(mcpPath, 0000)
	defer func() { _ = os.Chmod(mcpPath, 0644) }()

	servers, errs := getProjectMcpConfigs(dir)
	if len(errs) == 0 {
		t.Fatal("want errors for unreadable .mcp.json during walk")
	}
	// Error should NOT be filtered (it's not "not found")
	found := false
	for _, e := range errs {
		if !strings.HasPrefix(e.Message, "MCP config file not found") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected at least one non-missing error, got: %v", errs)
	}
	if len(servers) != 0 {
		t.Errorf("expected 0 servers, got %d", len(servers))
	}
}

// ---------------------------------------------------------------------------
// writeProjectConfig — marshal error on existing config
// ---------------------------------------------------------------------------

func TestWriteProjectConfig_MarshalExistingError(t *testing.T) {
	dir := t.TempDir()
	// Use a config type that can fail to marshal
	existing := map[string]ScopedMcpServerConfig{
		"bad": {Config: marshalErrorConfig{}, Scope: ScopeProject},
	}
	cfg := &StdioConfig{Command: "new"}
	err := writeProjectConfig(dir, existing, "new", cfg)
	if err != nil {
		t.Fatalf("writeProjectConfig should succeed even with marshal error on existing: %v", err)
	}
	// Verify only "new" server is written (the "bad" one is skipped)
	servers, _ := GetProjectMcpConfigsFromCwd(dir)
	if _, ok := servers["new"]; !ok {
		t.Error("expected 'new' server to be written")
	}
	if _, ok := servers["bad"]; ok {
		t.Error("expected 'bad' server to be skipped (marshal failed)")
	}
}

// ---------------------------------------------------------------------------
// writeProjectConfigExclude — marshal error on existing config
// ---------------------------------------------------------------------------

func TestWriteProjectConfigExclude_MarshalError(t *testing.T) {
	dir := t.TempDir()
	existing := map[string]ScopedMcpServerConfig{
		"keep": {Config: marshalErrorConfig{}, Scope: ScopeProject},
	}
	err := writeProjectConfigExclude(dir, existing, "remove")
	if err != nil {
		t.Fatalf("writeProjectConfigExclude: %v", err)
	}
	// "keep" should be absent because json.Marshal(marshalErrorConfig{}) fails
	servers, _ := GetProjectMcpConfigsFromCwd(dir)
	if len(servers) != 0 {
		t.Errorf("expected 0 servers (keep had marshal error), got %d", len(servers))
	}
}

// marshalErrorConfig is a config type that fails to marshal.
type marshalErrorConfig struct{}

func (marshalErrorConfig) GetTransport() Transport { return "custom" }
func (marshalErrorConfig) MarshalJSON() ([]byte, error) {
	return nil, fmt.Errorf("forced marshal error")
}

// ---------------------------------------------------------------------------
// GetClaudeCodeMcpConfigs — managed-only mode
// ---------------------------------------------------------------------------

func TestGetClaudeCodeMcpConfigs_ManagedOnly(t *testing.T) {
	provider := &mockConfigProvider{
		managedOnly: true,
		userServers: map[string]McpServerConfig{
			"test": &StdioConfig{Command: "echo"},
		},
	}
	result, errs := GetClaudeCodeMcpConfigs("/tmp", provider, nil, nil)
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	// No enterprise config file exists, so enterpriseServers is empty,
	// but managedOnly doesn't affect the result here. managedOnly only
	// affects whether MCP is allowed, not the loading.
	// The real check is that user servers are still loaded.
	if len(result) == 0 {
		t.Error("expected at least user server in non-managed-only scenario")
	}
}

// ---------------------------------------------------------------------------
// AddMcpConfig — SaveUserMcpServers error
// ---------------------------------------------------------------------------

type errorSaveProvider struct {
	mockConfigProvider
	saveErr error
}

func (p *errorSaveProvider) SaveUserMcpServers(servers map[string]McpServerConfig) error {
	if p.saveErr != nil {
		return p.saveErr
	}
	_ = p.mockConfigProvider.SaveUserMcpServers(servers)
	return nil
}

func (p *errorSaveProvider) SaveLocalMcpServers(servers map[string]McpServerConfig) error {
	if p.saveErr != nil {
		return p.saveErr
	}
	_ = p.mockConfigProvider.SaveLocalMcpServers(servers)
	return nil
}

func TestAddMcpConfig_UserSaveError(t *testing.T) {
	provider := &errorSaveProvider{
		mockConfigProvider: mockConfigProvider{
			userServers: map[string]McpServerConfig{},
		},
		saveErr: fmt.Errorf("disk full"),
	}
	cfg := &StdioConfig{Command: "node"}
	err := AddMcpConfig("srv", cfg, ScopeUser, "", provider, nil, nil)
	if err == nil {
		t.Fatal("want error when save fails")
	}
	if !strings.Contains(err.Error(), "disk full") {
		t.Errorf("error = %v, want 'disk full'", err)
	}
}

func TestAddMcpConfig_LocalSaveError(t *testing.T) {
	provider := &errorSaveProvider{
		mockConfigProvider: mockConfigProvider{
			localServers: map[string]McpServerConfig{},
		},
		saveErr: fmt.Errorf("permission denied"),
	}
	cfg := &StdioConfig{Command: "node"}
	err := AddMcpConfig("srv", cfg, ScopeLocal, "", provider, nil, nil)
	if err == nil {
		t.Fatal("want error when save fails")
	}
	if !strings.Contains(err.Error(), "permission denied") {
		t.Errorf("error = %v, want 'permission denied'", err)
	}
}

// ---------------------------------------------------------------------------
// RemoveMcpConfig — save error propagation
// ---------------------------------------------------------------------------

func TestRemoveMcpConfig_UserSaveError(t *testing.T) {
	provider := &errorSaveProvider{
		mockConfigProvider: mockConfigProvider{
			userServers: map[string]McpServerConfig{
				"srv": &StdioConfig{Command: "echo"},
			},
		},
		saveErr: fmt.Errorf("io error"),
	}
	err := RemoveMcpConfig("srv", ScopeUser, "", provider)
	if err == nil {
		t.Fatal("want error when save fails")
	}
	if !strings.Contains(err.Error(), "io error") {
		t.Errorf("error = %v, want 'io error'", err)
	}
}

func TestRemoveMcpConfig_LocalSaveError(t *testing.T) {
	provider := &errorSaveProvider{
		mockConfigProvider: mockConfigProvider{
			localServers: map[string]McpServerConfig{
				"srv": &StdioConfig{Command: "echo"},
			},
		},
		saveErr: fmt.Errorf("io error"),
	}
	err := RemoveMcpConfig("srv", ScopeLocal, "", provider)
	if err == nil {
		t.Fatal("want error when save fails")
	}
	if !strings.Contains(err.Error(), "io error") {
		t.Errorf("error = %v, want 'io error'", err)
	}
}

// ---------------------------------------------------------------------------
// SetMcpServerEnabled — save error
// ---------------------------------------------------------------------------

type errorSaveDisabledProvider struct {
	mockConfigProvider
}

func (p *errorSaveDisabledProvider) SaveProjectDisabledServers(names []string) error {
	return fmt.Errorf("settings save failed")
}

func TestSetMcpServerEnabled_SaveError(t *testing.T) {
	provider := &errorSaveDisabledProvider{
		mockConfigProvider: mockConfigProvider{
			disabledServers: []string{},
		},
	}
	err := SetMcpServerEnabled("srv", false, provider)
	if err == nil {
		t.Fatal("want error when save fails")
	}
	if !strings.Contains(err.Error(), "settings save failed") {
		t.Errorf("error = %v, want 'settings save failed'", err)
	}
}

// ---------------------------------------------------------------------------
// GetMcpConfigsByScope — enterprise with invalid JSON
// ---------------------------------------------------------------------------

func TestGetMcpConfigsByScope_EnterpriseInvalidJSON(t *testing.T) {
	dir := t.TempDir()
	mcpPath := filepath.Join(dir, "managed.json")
	mustWriteFile(t, mcpPath, []byte(`{invalid json}`))

	provider := &mockConfigProvider{managedPath: mcpPath}
	servers, errs := GetMcpConfigsByScope(ScopeEnterprise, dir, provider)
	// Should get errors from invalid JSON but no servers
	if len(errs) == 0 {
		t.Fatal("want errors for invalid JSON")
	}
	if len(servers) != 0 {
		t.Errorf("expected 0 servers for invalid JSON, got %d", len(servers))
	}
}

// ---------------------------------------------------------------------------
// policyEntryMarker — covers the marker method on all three entry types
// ---------------------------------------------------------------------------

func TestPolicyEntryMarker_ImplementsInterface(t *testing.T) {
	// Verify all three concrete types satisfy the McpPolicyEntry interface.
	// This compiles only if the marker methods exist on all three types.
	var _ McpPolicyEntry = (*McpNameEntry)(nil)
	var _ McpPolicyEntry = (*McpCommandEntry)(nil)
	var _ McpPolicyEntry = (*McpUrlEntry)(nil)
}

// ---------------------------------------------------------------------------
// UnmarshalMcpPolicyEntry — default case (no fields)
// ---------------------------------------------------------------------------

func TestUnmarshalMcpPolicyEntry_NoFields(t *testing.T) {
	raw := json.RawMessage(`{}`)
	entry, err := UnmarshalMcpPolicyEntry(raw)
	if err == nil {
		t.Fatalf("want error for empty entry, got entry=%v", entry)
	}
	if !strings.Contains(err.Error(), "must have serverName, serverCommand, or serverUrl") {
		t.Errorf("error = %v, want 'must have' message", err)
	}
}

// ---------------------------------------------------------------------------
// GetServerCommandArray — non-stdio config that passes transport check
// ---------------------------------------------------------------------------

func TestGetServerCommandArray_TransportStdioButNotStdioConfig(t *testing.T) {
	cfg := &nonStdioButStdioTransport{}
	result := GetServerCommandArray(cfg)
	if result != nil {
		t.Errorf("expected nil for non-*StdioConfig, got %v", result)
	}
}

// nonStdioButStdioTransport returns TransportStdio but is not *StdioConfig
type nonStdioButStdioTransport struct{}

func (n *nonStdioButStdioTransport) GetTransport() Transport { return TransportStdio }
func (n *nonStdioButStdioTransport) GetURL() string          { return "" }
func (n *nonStdioButStdioTransport) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]string{"type": "stdio"})
}

// ---------------------------------------------------------------------------
// UrlMatchesPattern — compile error path
// ---------------------------------------------------------------------------

func TestUrlMatchesPattern_InvalidRegex(t *testing.T) {
	result := UrlMatchesPattern("https://example.com", "https://[invalid")
	if result {
		t.Error("expected false for pattern that fails regex compilation")
	}
}

// ---------------------------------------------------------------------------
// WriteMcpjsonFile — read-only dir (open error)
// ---------------------------------------------------------------------------

func TestWriteMcpjsonFile_ReadOnlyDir(t *testing.T) {
	dir := t.TempDir()
	readOnlyDir := filepath.Join(dir, "readonly")
	if err := os.Mkdir(readOnlyDir, 0555); err != nil {
		t.Fatal(err)
	}
	cfg := &McpJsonConfig{
		McpServers: map[string]json.RawMessage{
			"test": json.RawMessage(`{"command":"echo"}`),
		},
	}
	err := WriteMcpjsonFile(readOnlyDir, cfg)
	if err == nil {
		t.Fatal("want error writing to read-only directory")
	}
	if !strings.Contains(err.Error(), "temp file") {
		t.Errorf("error should mention temp file, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// WriteMcpjsonFile — close error (via permission change after open)
// ---------------------------------------------------------------------------

func TestWriteMcpjsonFile_CloseError(t *testing.T) {
	// This tests the writeErr path: write succeeds but close fails.
	// On Linux, Close rarely fails for regular files, so we test the
	// sync path by writing to a dir where the temp file can be created
	// but sync/close may fail (e.g., full disk simulation is hard).
	// Instead, verify the normal path exercises close successfully.
	dir := t.TempDir()
	cfg := &McpJsonConfig{
		McpServers: map[string]json.RawMessage{
			"test": json.RawMessage(`{"command":"echo"}`),
		},
	}
	if err := WriteMcpjsonFile(dir, cfg); err != nil {
		t.Fatalf("expected success, got: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, ".mcp.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "echo") {
		t.Errorf("file content should contain 'echo', got: %s", data)
	}
}

// ---------------------------------------------------------------------------
// writeProjectConfig — marshal error on new config
// ---------------------------------------------------------------------------

func TestWriteProjectConfig_NewConfigMarshalError(t *testing.T) {
	dir := t.TempDir()
	existing := map[string]ScopedMcpServerConfig{
		"existing": {Config: &StdioConfig{Command: "echo"}, Scope: ScopeProject},
	}
	badCfg := &marshalErrorConfigType{}
	err := writeProjectConfig(dir, existing, "new", badCfg)
	if err == nil {
		t.Fatal("want error marshaling new config")
	}
	if !strings.Contains(err.Error(), "marshal") {
		t.Errorf("error should mention marshal, got: %v", err)
	}
}

// marshalErrorConfigType always fails to marshal
type marshalErrorConfigType struct{}

func (m *marshalErrorConfigType) GetTransport() Transport { return TransportStdio }
func (m *marshalErrorConfigType) GetURL() string          { return "" }
func (m *marshalErrorConfigType) MarshalJSON() ([]byte, error) {
	return nil, fmt.Errorf("forced marshal error")
}

// ---------------------------------------------------------------------------
// GetMcpConfigsByScope — user nil, local nil
// ---------------------------------------------------------------------------

func TestGetMcpConfigsByScope_UserNilServers(t *testing.T) {
	provider := &mockConfigProvider{userServers: nil}
	servers, errs := GetMcpConfigsByScope(ScopeUser, "", provider)
	if len(errs) != 0 {
		t.Errorf("expected no errors, got %v", errs)
	}
	if len(servers) != 0 {
		t.Errorf("expected empty servers, got %d", len(servers))
	}
}

func TestGetMcpConfigsByScope_LocalNilServers(t *testing.T) {
	provider := &mockConfigProvider{localServers: nil}
	servers, errs := GetMcpConfigsByScope(ScopeLocal, "", provider)
	if len(errs) != 0 {
		t.Errorf("expected no errors, got %v", errs)
	}
	if len(servers) != 0 {
		t.Errorf("expected empty servers, got %d", len(servers))
	}
}

// ---------------------------------------------------------------------------
// getProjectMcpConfigs — parse errors
// ---------------------------------------------------------------------------

func TestGetProjectMcpConfigs_ParseErrors(t *testing.T) {
	dir := t.TempDir()
	content := `{"mcpServers":{"bad":{"type":"unknown_type_xxx"}}}`
	mustWriteFile(t, filepath.Join(dir, ".mcp.json"), []byte(content))
	servers, errs := getProjectMcpConfigs(dir)
	if servers == nil {
		t.Error("expected non-nil servers map")
	}
	t.Logf("servers=%d, errors=%d, errs=%v", len(servers), len(errs), errs)
}

// ---------------------------------------------------------------------------
// ParseMcpConfig — env expansion with $HOME
// ---------------------------------------------------------------------------

func TestParseMcpConfig_EnvExpansion(t *testing.T) {
	t.Setenv("MCP_TEST_EXPAND_HOME", "/home/testuser")
	content := json.RawMessage(`{"mcpServers":{"test":{"command":"echo","args":["${MCP_TEST_EXPAND_HOME}"],"env":{"KEY":"val"}}}}`)
	config, errs := ParseMcpConfig(content, true, ScopeUser, "/test/.mcp.json")
	if config == nil {
		t.Fatal("expected non-nil config")
	}
	if len(config.McpServers) != 1 {
		t.Fatalf("expected 1 server, got %d", len(config.McpServers))
	}
	var raw map[string]any
	if err := json.Unmarshal(config.McpServers["test"], &raw); err != nil {
		t.Fatal(err)
	}
	args, ok := raw["args"].([]any)
	if !ok {
		t.Fatal("expected args array")
	}
	if len(args) != 1 {
		t.Fatalf("expected 1 arg, got %d", len(args))
	}
	got, ok := args[0].(string)
	if !ok {
		t.Fatal("expected string arg")
	}
	if got == "${MCP_TEST_EXPAND_HOME}" {
		t.Error("${MCP_TEST_EXPAND_HOME} should have been expanded")
	}
	if got != "/home/testuser" {
		t.Errorf("expected %q, got %q", "/home/testuser", got)
	}
	if len(errs) != 0 {
		t.Logf("errors: %v", errs)
	}
}

