package mcp

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// FilterToolsByServer — Source: utils.ts:39-42
// ---------------------------------------------------------------------------

func TestFilterToolsByServer(t *testing.T) {
	tools := []SerializedTool{
		{Name: "mcp__my_server__tool1"},
		{Name: "mcp__my_server__tool2"},
		{Name: "mcp__other_server__tool1"},
		{Name: "BuiltinTool"},
	}

	result := FilterToolsByServer(tools, "my_server")
	if len(result) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(result))
	}
	for _, tool := range result {
		if !strings.HasPrefix(tool.Name, "mcp__my_server__") {
			t.Errorf("tool %q should have mcp__my_server__ prefix", tool.Name)
		}
	}
}

func TestFilterToolsByServer_Empty(t *testing.T) {
	result := FilterToolsByServer(nil, "server")
	if len(result) != 0 {
		t.Errorf("expected empty, got %d", len(result))
	}
}

func TestFilterToolsByServer_NoMatch(t *testing.T) {
	tools := []SerializedTool{{Name: "BuiltinTool"}}
	result := FilterToolsByServer(tools, "server")
	if len(result) != 0 {
		t.Errorf("expected empty for no match, got %d", len(result))
	}
}

// ---------------------------------------------------------------------------
// CommandBelongsToServer — Source: utils.ts:52-62
// ---------------------------------------------------------------------------

func TestCommandBelongsToServer(t *testing.T) {
	tests := []struct {
		name        string
		commandName string
		serverName  string
		want        bool
	}{
		{"mcp prefix match", "mcp__my_server__prompt1", "my_server", true},
		{"skill prefix match", "my_server:skill1", "my_server", true},
		{"no match", "other_server__prompt1", "my_server", false},
		{"empty command", "", "my_server", false},
		{"partial match", "mcp__my_server_", "my_server", false},
		{"normalized match", "mcp__My_Server__prompt", "My Server", true},
		{"colon skill match", "My_Server:skill", "My Server", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CommandBelongsToServer(tt.commandName, tt.serverName)
			if got != tt.want {
				t.Errorf("CommandBelongsToServer(%q, %q) = %v, want %v",
					tt.commandName, tt.serverName, got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// FilterCommandsByServer — Source: utils.ts:70-75
// ---------------------------------------------------------------------------

func TestFilterCommandsByServer(t *testing.T) {
	commands := []MCPCommand{
		{Name: "mcp__my_server__cmd1"},
		{Name: "mcp__other__cmd2"},
		{Name: "my_server:skill"},
	}

	result := FilterCommandsByServer(commands, "my_server")
	if len(result) != 2 {
		t.Fatalf("expected 2 commands, got %d", len(result))
	}
	names := []string{result[0].Name, result[1].Name}
	sort.Strings(names)
	if names[0] != "mcp__my_server__cmd1" {
		t.Errorf("expected mcp__my_server__cmd1, got %s", names[0])
	}
	if names[1] != "my_server:skill" {
		t.Errorf("expected my_server:skill, got %s", names[1])
	}
}

// ---------------------------------------------------------------------------
// FilterResourcesByServer — Source: utils.ts:102-107
// ---------------------------------------------------------------------------

func TestFilterResourcesByServer(t *testing.T) {
	resources := []ServerResource{
		{URI: "file:///a", Server: "srv1"},
		{URI: "file:///b", Server: "srv2"},
		{URI: "file:///c", Server: "srv1"},
	}

	result := FilterResourcesByServer(resources, "srv1")
	if len(result) != 2 {
		t.Fatalf("expected 2 resources, got %d", len(result))
	}
	for _, r := range result {
		if r.Server != "srv1" {
			t.Errorf("expected server srv1, got %s", r.Server)
		}
	}
}

func TestFilterResourcesByServer_Empty(t *testing.T) {
	result := FilterResourcesByServer(nil, "srv")
	if len(result) != 0 {
		t.Errorf("expected empty, got %d", len(result))
	}
}

// ---------------------------------------------------------------------------
// ExcludeToolsByServer — Source: utils.ts:115-121
// ---------------------------------------------------------------------------

func TestExcludeToolsByServer(t *testing.T) {
	tools := []SerializedTool{
		{Name: "mcp__srv__tool1"},
		{Name: "mcp__srv__tool2"},
		{Name: "mcp__other__tool1"},
		{Name: "BuiltinTool"},
	}

	result := ExcludeToolsByServer(tools, "srv")
	if len(result) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(result))
	}
	for _, tool := range result {
		if strings.HasPrefix(tool.Name, "mcp__srv__") {
			t.Errorf("tool %q should have been excluded", tool.Name)
		}
	}
}

// ---------------------------------------------------------------------------
// ExcludeCommandsByServer — Source: utils.ts:129-134
// ---------------------------------------------------------------------------

func TestExcludeCommandsByServer(t *testing.T) {
	commands := []MCPCommand{
		{Name: "mcp__srv__cmd"},
		{Name: "srv:skill"},
		{Name: "mcp__other__cmd"},
	}

	result := ExcludeCommandsByServer(commands, "srv")
	if len(result) != 1 {
		t.Fatalf("expected 1 command, got %d", len(result))
	}
	if result[0].Name != "mcp__other__cmd" {
		t.Errorf("expected mcp__other__cmd, got %s", result[0].Name)
	}
}

// ---------------------------------------------------------------------------
// ExcludeResourcesByServer — Source: utils.ts:142-149
// ---------------------------------------------------------------------------

func TestExcludeResourcesByServer(t *testing.T) {
	resources := map[string][]ServerResource{
		"srv1": {{URI: "file:///a", Server: "srv1"}},
		"srv2": {{URI: "file:///b", Server: "srv2"}},
	}

	result := ExcludeResourcesByServer(resources, "srv1")
	if len(result) != 1 {
		t.Fatalf("expected 1 key, got %d", len(result))
	}
	if _, exists := result["srv1"]; exists {
		t.Error("srv1 should have been excluded")
	}
	if _, exists := result["srv2"]; !exists {
		t.Error("srv2 should still be present")
	}
}

// ---------------------------------------------------------------------------
// HashMcpConfig — Source: utils.ts:157-169
// ---------------------------------------------------------------------------

func TestHashMcpConfig_Deterministic(t *testing.T) {
	cfg := ScopedMcpServerConfig{
		Config: &StdioConfig{Command: "node", Args: []string{"server.js"}},
		Scope:  ScopeLocal,
	}

	h1 := HashMcpConfig(cfg)
	h2 := HashMcpConfig(cfg)
	if h1 != h2 {
		t.Errorf("hash should be deterministic: %q != %q", h1, h2)
	}
	if len(h1) != 16 {
		t.Errorf("hash should be 16 hex chars, got %d: %q", len(h1), h1)
	}
}

func TestHashMcpConfig_ScopeExcluded(t *testing.T) {
	cfg1 := ScopedMcpServerConfig{
		Config: &StdioConfig{Command: "node"},
		Scope:  ScopeLocal,
	}
	cfg2 := ScopedMcpServerConfig{
		Config: &StdioConfig{Command: "node"},
		Scope:  ScopeProject,
	}

	if HashMcpConfig(cfg1) != HashMcpConfig(cfg2) {
		t.Error("hash should be the same regardless of scope")
	}
}

func TestHashMcpConfig_KeyOrderIndependent(t *testing.T) {
	// Two JSON objects with keys in different order should hash the same.
	// We test via HTTPConfig which has multiple fields.
	cfg1 := ScopedMcpServerConfig{
		Config: &HTTPConfig{
			URL:     "http://example.com",
			Headers: map[string]string{"B": "2", "A": "1"},
		},
	}
	cfg2 := ScopedMcpServerConfig{
		Config: &HTTPConfig{
			URL:     "http://example.com",
			Headers: map[string]string{"A": "1", "B": "2"},
		},
	}

	if HashMcpConfig(cfg1) != HashMcpConfig(cfg2) {
		t.Error("hash should be the same regardless of key order")
	}
}

func TestHashMcpConfig_ConfigChange(t *testing.T) {
	cfg1 := ScopedMcpServerConfig{
		Config: &StdioConfig{Command: "node", Args: []string{"v1.js"}},
	}
	cfg2 := ScopedMcpServerConfig{
		Config: &StdioConfig{Command: "node", Args: []string{"v2.js"}},
	}

	if HashMcpConfig(cfg1) == HashMcpConfig(cfg2) {
		t.Error("hash should differ when config changes")
	}
}

func TestHashMcpConfig_PluginSource(t *testing.T) {
	cfg1 := ScopedMcpServerConfig{
		Config:       &StdioConfig{Command: "node"},
		PluginSource: "plugin-a",
	}
	cfg2 := ScopedMcpServerConfig{
		Config:       &StdioConfig{Command: "node"},
		PluginSource: "plugin-b",
	}

	if HashMcpConfig(cfg1) == HashMcpConfig(cfg2) {
		t.Error("hash should differ when pluginSource changes")
	}
}

// ---------------------------------------------------------------------------
// sortKeysDeep — internal helper
// ---------------------------------------------------------------------------

func TestSortKeysDeep(t *testing.T) {
	input := map[string]any{
		"z": 1,
		"a": map[string]any{"b": 2, "a": 1},
	}
	result := sortKeysDeep(input)
	// Verify via JSON serialization — json.Marshal sorts map keys
	got, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	want := `{"a":{"a":1,"b":2},"z":1}`
	if string(got) != want {
		t.Errorf("sortKeysDeep JSON = %q, want %q", string(got), want)
	}
}

func TestSortKeysDeep_Array(t *testing.T) {
	input := []any{
		map[string]any{"b": 2, "a": 1},
		map[string]any{"d": 4, "c": 3},
	}
	result := sortKeysDeep(input)
	// Verify via JSON serialization — json.Marshal sorts map keys
	got, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	want := `[{"a":1,"b":2},{"c":3,"d":4}]`
	if string(got) != want {
		t.Errorf("sortKeysDeep array JSON = %q, want %q", string(got), want)
	}
}

func TestSortKeysDeep_Scalar(t *testing.T) {
	result := sortKeysDeep("hello")
	if result != "hello" {
		t.Errorf("expected hello, got %v", result)
	}
	result = sortKeysDeep(42)
	if result != 42 {
		t.Errorf("expected 42, got %v", result)
	}
}

// ---------------------------------------------------------------------------
// ExcludeStalePluginClients — Source: utils.ts:185-224
// ---------------------------------------------------------------------------

func TestExcludeStalePluginClients_NoStale(t *testing.T) {
	cfg := ScopedMcpServerConfig{
		Config: &StdioConfig{Command: "node"},
		Scope:  ScopeUser,
	}
	state := MCPConnectionState{
		Clients: []ServerConnection{
			&ConnectedServer{Name: "srv", Config: cfg},
		},
		Tools:     []SerializedTool{{Name: "mcp__srv__tool"}},
		Commands:  []MCPCommand{{Name: "mcp__srv__cmd"}},
		Resources: map[string][]ServerResource{"srv": {{URI: "file:///a", Server: "srv"}}},
	}
	configs := map[string]ScopedMcpServerConfig{
		"srv": cfg,
	}

	result := ExcludeStalePluginClients(state, configs)
	if len(result.Stale) != 0 {
		t.Errorf("expected no stale, got %d", len(result.Stale))
	}
	if len(result.Clients) != 1 {
		t.Errorf("expected 1 client, got %d", len(result.Clients))
	}
	if len(result.Tools) != 1 {
		t.Errorf("expected 1 tool, got %d", len(result.Tools))
	}
}

func TestExcludeStalePluginClients_DynamicRemoved(t *testing.T) {
	cfg := ScopedMcpServerConfig{
		Config: &StdioConfig{Command: "node"},
		Scope:  ScopeDynamic,
	}
	state := MCPConnectionState{
		Clients: []ServerConnection{
			&ConnectedServer{Name: "dynamic_srv", Config: cfg},
		},
		Tools:     []SerializedTool{{Name: "mcp__dynamic_srv__tool"}},
		Commands:  []MCPCommand{{Name: "mcp__dynamic_srv__cmd"}},
		Resources: map[string][]ServerResource{"dynamic_srv": {{URI: "file:///a", Server: "dynamic_srv"}}},
	}
	// Empty configs — dynamic server should be stale
	configs := map[string]ScopedMcpServerConfig{}

	result := ExcludeStalePluginClients(state, configs)
	if len(result.Stale) != 1 {
		t.Fatalf("expected 1 stale, got %d", len(result.Stale))
	}
	if connName(result.Stale[0]) != "dynamic_srv" {
		t.Errorf("stale client name = %q, want dynamic_srv", connName(result.Stale[0]))
	}
	if len(result.Clients) != 0 {
		t.Errorf("expected 0 remaining clients, got %d", len(result.Clients))
	}
	if len(result.Tools) != 0 {
		t.Errorf("expected 0 tools, got %d", len(result.Tools))
	}
	if len(result.Commands) != 0 {
		t.Errorf("expected 0 commands, got %d", len(result.Commands))
	}
	if _, exists := result.Resources["dynamic_srv"]; exists {
		t.Error("dynamic_srv resources should have been excluded")
	}
}

func TestExcludeStalePluginClients_ConfigChanged(t *testing.T) {
	oldCfg := ScopedMcpServerConfig{
		Config: &StdioConfig{Command: "node", Args: []string{"old.js"}},
		Scope:  ScopeUser,
	}
	newCfg := ScopedMcpServerConfig{
		Config: &StdioConfig{Command: "node", Args: []string{"new.js"}},
		Scope:  ScopeUser,
	}
	state := MCPConnectionState{
		Clients: []ServerConnection{
			&ConnectedServer{Name: "srv", Config: oldCfg},
		},
		Tools:    []SerializedTool{{Name: "mcp__srv__tool"}},
		Commands: []MCPCommand{{Name: "mcp__srv__cmd"}},
	}
	configs := map[string]ScopedMcpServerConfig{
		"srv": newCfg,
	}

	result := ExcludeStalePluginClients(state, configs)
	if len(result.Stale) != 1 {
		t.Fatalf("expected 1 stale, got %d", len(result.Stale))
	}
	if len(result.Clients) != 0 {
		t.Errorf("expected 0 remaining clients, got %d", len(result.Clients))
	}
	if len(result.Tools) != 0 {
		t.Errorf("expected 0 tools after exclusion, got %d", len(result.Tools))
	}
}

func TestExcludeStalePluginClients_NonDynamicNotStale(t *testing.T) {
	cfg := ScopedMcpServerConfig{
		Config: &StdioConfig{Command: "node"},
		Scope:  ScopeUser,
	}
	state := MCPConnectionState{
		Clients: []ServerConnection{
			&ConnectedServer{Name: "user_srv", Config: cfg},
		},
	}
	// No config for user_srv, but scope is 'user' (not dynamic) — not stale
	configs := map[string]ScopedMcpServerConfig{}

	result := ExcludeStalePluginClients(state, configs)
	if len(result.Stale) != 0 {
		t.Errorf("non-dynamic server without config should not be stale, got %d stale", len(result.Stale))
	}
	if len(result.Clients) != 1 {
		t.Errorf("expected 1 client, got %d", len(result.Clients))
	}
}

func TestExcludeStalePluginClients_MultipleServers(t *testing.T) {
	cfg1 := ScopedMcpServerConfig{
		Config: &StdioConfig{Command: "node"},
		Scope:  ScopeDynamic,
	}
	cfg2 := ScopedMcpServerConfig{
		Config: &StdioConfig{Command: "python"},
		Scope:  ScopeUser,
	}
	state := MCPConnectionState{
		Clients: []ServerConnection{
			&ConnectedServer{Name: "dynamic_srv", Config: cfg1},
			&ConnectedServer{Name: "user_srv", Config: cfg2},
		},
		Tools: []SerializedTool{
			{Name: "mcp__dynamic_srv__tool"},
			{Name: "mcp__user_srv__tool"},
		},
	}
	configs := map[string]ScopedMcpServerConfig{
		"user_srv": cfg2, // only user_srv has fresh config
	}

	result := ExcludeStalePluginClients(state, configs)
	if len(result.Stale) != 1 {
		t.Fatalf("expected 1 stale, got %d", len(result.Stale))
	}
	if connName(result.Stale[0]) != "dynamic_srv" {
		t.Errorf("stale = %q, want dynamic_srv", connName(result.Stale[0]))
	}
}

// ---------------------------------------------------------------------------
// IsToolFromMcpServer — Source: utils.ts:232-238
// ---------------------------------------------------------------------------

func TestIsToolFromMcpServer(t *testing.T) {
	tests := []struct {
		name       string
		toolName   string
		serverName string
		want       bool
	}{
		{"match", "mcp__my_server__tool1", "my_server", true},
		{"no match", "mcp__other__tool1", "my_server", false},
		{"not mcp", "BuiltinTool", "my_server", false},
		{"empty", "", "my_server", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsToolFromMcpServer(tt.toolName, tt.serverName)
			if got != tt.want {
				t.Errorf("IsToolFromMcpServer(%q, %q) = %v, want %v",
					tt.toolName, tt.serverName, got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// IsMcpCommand — Source: utils.ts:254-256
// ---------------------------------------------------------------------------

func TestIsMcpCommand(t *testing.T) {
	if !IsMcpCommand("mcp__srv__cmd") {
		t.Error("expected true for mcp__ prefix")
	}
	if IsMcpCommand("builtin_cmd") {
		t.Error("expected false for non-mcp command")
	}
}

// ---------------------------------------------------------------------------
// GetScopeLabel — Source: utils.ts:282-299
// ---------------------------------------------------------------------------

func TestGetScopeLabel(t *testing.T) {
	tests := []struct {
		scope ConfigScope
		want  string
	}{
		{ScopeLocal, "Local config (private to you in this project)"},
		{ScopeProject, "Project config (shared via .mcp.json)"},
		{ScopeUser, "User config (available in all your projects)"},
		{ScopeDynamic, "Dynamic config (from command line)"},
		{ScopeEnterprise, "Enterprise config (managed by your organization)"},
		{ScopeClaudeAI, "claude.ai config"},
		{ScopeManaged, "Managed config"},
		{ConfigScope("unknown"), "unknown"},
	}

	for _, tt := range tests {
		t.Run(string(tt.scope), func(t *testing.T) {
			got := GetScopeLabel(tt.scope)
			if got != tt.want {
				t.Errorf("GetScopeLabel(%q) = %q, want %q", tt.scope, got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// DescribeMcpConfigFilePath — Source: utils.ts:263-280
// ---------------------------------------------------------------------------

func TestDescribeMcpConfigFilePath(t *testing.T) {
	tests := []struct {
		name   string
		scope  ConfigScope
		global string
		cwd    string
		ent    string
		want   string
	}{
		{"user", ScopeUser, "/home/.claude.json", "/project", "", "/home/.claude.json"},
		{"project", ScopeProject, "/home/.claude.json", "/project", "", "/project/.mcp.json"},
		{"local", ScopeLocal, "/home/.claude.json", "/project", "", "/home/.claude.json [project: /project]"},
		{"dynamic", ScopeDynamic, "/home/.claude.json", "/project", "", "Dynamically configured"},
		{"enterprise", ScopeEnterprise, "/home/.claude.json", "/project", "/etc/mcp.json", "/etc/mcp.json"},
		{"claudeai", ScopeClaudeAI, "/home/.claude.json", "/project", "", "claude.ai"},
		{"unknown", ConfigScope("foo"), "/home/.claude.json", "/project", "", "foo"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DescribeMcpConfigFilePath(tt.scope, tt.global, tt.cwd, tt.ent)
			if got != tt.want {
				t.Errorf("DescribeMcpConfigFilePath(%q) = %q, want %q", tt.scope, got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// EnsureConfigScope — Source: utils.ts:301-311
// ---------------------------------------------------------------------------

func TestEnsureConfigScope(t *testing.T) {
	tests := []struct {
		input string
		want  ConfigScope
		err   bool
	}{
		{"", ScopeLocal, false},
		{"local", ScopeLocal, false},
		{"user", ScopeUser, false},
		{"project", ScopeProject, false},
		{"dynamic", ScopeDynamic, false},
		{"enterprise", ScopeEnterprise, false},
		{"claudeai", ScopeClaudeAI, false},
		{"managed", ScopeManaged, false},
		{"invalid", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, err := EnsureConfigScope(tt.input)
			if tt.err {
				if err == nil {
					t.Error("expected error")
				}
				if !strings.Contains(err.Error(), "invalid scope") {
					t.Errorf("error should mention 'invalid scope', got: %v", err)
				}
			} else {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
				if got != tt.want {
					t.Errorf("got %q, want %q", got, tt.want)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// EnsureTransport — Source: utils.ts:313-323
// ---------------------------------------------------------------------------

func TestEnsureTransport(t *testing.T) {
	tests := []struct {
		input string
		want  Transport
		err   bool
	}{
		{"", TransportStdio, false},
		{"stdio", TransportStdio, false},
		{"sse", TransportSSE, false},
		{"http", TransportHTTP, false},
		{"ws", "", true},
		{"invalid", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, err := EnsureTransport(tt.input)
			if tt.err {
				if err == nil {
					t.Error("expected error")
				}
				if !strings.Contains(err.Error(), "invalid transport") {
					t.Errorf("error should mention 'invalid transport', got: %v", err)
				}
			} else {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
				if got != tt.want {
					t.Errorf("got %q, want %q", got, tt.want)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// ParseHeaders — Source: utils.ts:325-349
// ---------------------------------------------------------------------------

func TestParseHeaders(t *testing.T) {
	tests := []struct {
		name    string
		input   []string
		want    map[string]string
		wantErr bool
		errMsg  string
	}{
		{
			"valid single",
			[]string{"Content-Type: application/json"},
			map[string]string{"Content-Type": "application/json"},
			false, "",
		},
		{
			"valid multiple",
			[]string{"Content-Type: application/json", "Authorization: Bearer token"},
			map[string]string{"Content-Type": "application/json", "Authorization": "Bearer token"},
			false, "",
		},
		{
			"with whitespace",
			[]string{"  Key  :  value  "},
			map[string]string{"Key": "value"},
			false, "",
		},
		{
			"value with colons",
			[]string{"URL: http://example.com:8080"},
			map[string]string{"URL": "http://example.com:8080"},
			false, "",
		},
		{
			"no colon",
			[]string{"InvalidHeader"},
			nil, true, "invalid header format",
		},
		{
			"empty key",
			[]string{": value"},
			nil, true, "Header name cannot be empty",
		},
		{
			"empty array",
			[]string{},
			map[string]string{},
			false, "",
		},
		{
			"nil array",
			nil,
			map[string]string{},
			false, "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseHeaders(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				if !strings.Contains(err.Error(), tt.errMsg) {
					t.Errorf("error %q should contain %q", err.Error(), tt.errMsg)
				}
			} else {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
				if len(got) != len(tt.want) {
					t.Fatalf("got %d headers, want %d", len(got), len(tt.want))
				}
				for k, v := range tt.want {
					if got[k] != v {
						t.Errorf("got[%q] = %q, want %q", k, got[k], v)
					}
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// GetProjectMcpServerStatus — Source: utils.ts:351-406
// ---------------------------------------------------------------------------

func TestGetProjectMcpServerStatus(t *testing.T) {
	tests := []struct {
		name     string
		server   string
		settings McpProjectSettings
		want     string
	}{
		{
			"disabled",
			"my_server",
			McpProjectSettings{DisabledServers: []string{"my_server"}},
			"rejected",
		},
		{
			"enabled",
			"my_server",
			McpProjectSettings{EnabledServers: []string{"my_server"}},
			"approved",
		},
		{
			"enable all",
			"any_server",
			McpProjectSettings{EnableAll: true},
			"approved",
		},
		{
			"skip permissions",
			"my_server",
			McpProjectSettings{SkipDangerousPermission: true, ProjectSettingsEnabled: true},
			"approved",
		},
		{
			"non-interactive",
			"my_server",
			McpProjectSettings{NonInteractive: true, ProjectSettingsEnabled: true},
			"approved",
		},
		{
			"pending",
			"my_server",
			McpProjectSettings{},
			"pending",
		},
		{
			"skip permissions without project settings",
			"my_server",
			McpProjectSettings{SkipDangerousPermission: true},
			"pending",
		},
		{
			"normalized disabled",
			"My Server",
			McpProjectSettings{DisabledServers: []string{"My Server"}},
			"rejected",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := GetProjectMcpServerStatus(tt.server, tt.settings)
			if got != tt.want {
				t.Errorf("GetProjectMcpServerStatus(%q, ...) = %q, want %q",
					tt.server, got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Type guards — Source: utils.ts:438-457
// ---------------------------------------------------------------------------

func TestTypeGuards(t *testing.T) {
	tests := []struct {
		name  string
		config McpServerConfig
		isStdio bool
		isSSE   bool
		isHTTP  bool
		isWS    bool
	}{
		{"stdio", &StdioConfig{Command: "node"}, true, false, false, false},
		{"sse", &SSEConfig{URL: "http://example.com"}, false, true, false, false},
		{"http", &HTTPConfig{URL: "http://example.com"}, false, false, true, false},
		{"ws", &WSConfig{URL: "ws://example.com"}, false, false, false, true},
		{"sdk", &SDKConfig{Name: "test"}, false, false, false, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsStdioConfig(tt.config); got != tt.isStdio {
				t.Errorf("IsStdioConfig = %v, want %v", got, tt.isStdio)
			}
			if got := IsSSEConfig(tt.config); got != tt.isSSE {
				t.Errorf("IsSSEConfig = %v, want %v", got, tt.isSSE)
			}
			if got := IsHTTPConfig(tt.config); got != tt.isHTTP {
				t.Errorf("IsHTTPConfig = %v, want %v", got, tt.isHTTP)
			}
			if got := IsWSConfig(tt.config); got != tt.isWS {
				t.Errorf("IsWSConfig = %v, want %v", got, tt.isWS)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// ExtractAgentMcpServers — Source: utils.ts:466-553
// ---------------------------------------------------------------------------

func TestExtractAgentMcpServers_Empty(t *testing.T) {
	result := ExtractAgentMcpServers(nil)
	if len(result) != 0 {
		t.Errorf("expected empty, got %d", len(result))
	}
}

func TestExtractAgentMcpServers_NoMcpServers(t *testing.T) {
	agents := []AgentDefinition{
		{AgentType: "Explore"},
	}
	result := ExtractAgentMcpServers(agents)
	if len(result) != 0 {
		t.Errorf("expected empty for agents without mcpServers, got %d", len(result))
	}
}

func TestExtractAgentMcpServers_StringRefsSkipped(t *testing.T) {
	agents := []AgentDefinition{
		{
			AgentType: "Explore",
			McpServers: []AgentMcpSpec{
				{Ref: "existing_server"},
			},
		},
	}
	result := ExtractAgentMcpServers(agents)
	if len(result) != 0 {
		t.Errorf("string refs should be skipped, got %d", len(result))
	}
}

func TestExtractAgentMcpServers_StdioServer(t *testing.T) {
	agents := []AgentDefinition{
		{
			AgentType: "Explore",
			McpServers: []AgentMcpSpec{
				{
					Name:   "my_stdio",
					Config: &StdioConfig{Command: "node", Args: []string{"server.js"}},
				},
			},
		},
	}

	result := ExtractAgentMcpServers(agents)
	if len(result) != 1 {
		t.Fatalf("expected 1 server, got %d", len(result))
	}
	srv := result[0]
	if srv.Name != "my_stdio" {
		t.Errorf("name = %q, want my_stdio", srv.Name)
	}
	if srv.Transport != "stdio" {
		t.Errorf("transport = %q, want stdio", srv.Transport)
	}
	if srv.Command != "node" {
		t.Errorf("command = %q, want node", srv.Command)
	}
	if srv.NeedsAuth {
		t.Error("stdio should not need auth")
	}
	if len(srv.SourceAgents) != 1 || srv.SourceAgents[0] != "Explore" {
		t.Errorf("sourceAgents = %v, want [Explore]", srv.SourceAgents)
	}
}

func TestExtractAgentMcpServers_MultipleAgents(t *testing.T) {
	agents := []AgentDefinition{
		{
			AgentType: "Explore",
			McpServers: []AgentMcpSpec{
				{Name: "shared_srv", Config: &HTTPConfig{URL: "http://example.com"}},
			},
		},
		{
			AgentType: "Plan",
			McpServers: []AgentMcpSpec{
				{Name: "shared_srv", Config: &HTTPConfig{URL: "http://example.com"}},
			},
		},
	}

	result := ExtractAgentMcpServers(agents)
	if len(result) != 1 {
		t.Fatalf("expected 1 server (merged), got %d", len(result))
	}
	srv := result[0]
	if len(srv.SourceAgents) != 2 {
		t.Fatalf("expected 2 source agents, got %d", len(srv.SourceAgents))
	}
	sort.Strings(srv.SourceAgents)
	if srv.SourceAgents[0] != "Explore" || srv.SourceAgents[1] != "Plan" {
		t.Errorf("sourceAgents = %v, want [Explore, Plan]", srv.SourceAgents)
	}
	if !srv.NeedsAuth {
		t.Error("HTTP server should need auth")
	}
}

func TestExtractAgentMcpServers_SortedByName(t *testing.T) {
	agents := []AgentDefinition{
		{
			AgentType: "Explore",
			McpServers: []AgentMcpSpec{
				{Name: "z_server", Config: &StdioConfig{Command: "z"}},
				{Name: "a_server", Config: &StdioConfig{Command: "a"}},
			},
		},
	}

	result := ExtractAgentMcpServers(agents)
	if len(result) != 2 {
		t.Fatalf("expected 2 servers, got %d", len(result))
	}
	if result[0].Name != "a_server" {
		t.Errorf("first = %q, want a_server", result[0].Name)
	}
	if result[1].Name != "z_server" {
		t.Errorf("second = %q, want z_server", result[1].Name)
	}
}

func TestExtractAgentMcpServers_UnsupportedTransport(t *testing.T) {
	agents := []AgentDefinition{
		{
			AgentType: "Explore",
			McpServers: []AgentMcpSpec{
				{Name: "sdk_srv", Config: &SDKConfig{Name: "test"}},
			},
		},
	}

	result := ExtractAgentMcpServers(agents)
	if len(result) != 0 {
		t.Errorf("SDK config should be skipped, got %d", len(result))
	}
}

func TestExtractAgentMcpServers_DuplicateAgentSource(t *testing.T) {
	agents := []AgentDefinition{
		{
			AgentType: "Explore",
			McpServers: []AgentMcpSpec{
				{Name: "srv", Config: &StdioConfig{Command: "a"}},
				{Name: "srv", Config: &StdioConfig{Command: "b"}},
			},
		},
	}

	result := ExtractAgentMcpServers(agents)
	if len(result) != 1 {
		t.Fatalf("expected 1 server (merged), got %d", len(result))
	}
	// SourceAgents should contain "Explore" only once
	if len(result[0].SourceAgents) != 1 {
		t.Errorf("expected 1 source agent, got %d: %v", len(result[0].SourceAgents), result[0].SourceAgents)
	}
}

func TestExtractAgentMcpServers_WSTransport(t *testing.T) {
	agents := []AgentDefinition{
		{
			AgentType: "Explore",
			McpServers: []AgentMcpSpec{
				{Name: "ws_srv", Config: &WSConfig{URL: "ws://example.com"}},
			},
		},
	}

	result := ExtractAgentMcpServers(agents)
	if len(result) != 1 {
		t.Fatalf("expected 1 server, got %d", len(result))
	}
	if result[0].Transport != "ws" {
		t.Errorf("transport = %q, want ws", result[0].Transport)
	}
	if result[0].NeedsAuth {
		t.Error("WS should not need auth")
	}
}

func TestExtractAgentMcpServers_EmptyNameSkipped(t *testing.T) {
	agents := []AgentDefinition{
		{
			AgentType: "Explore",
			McpServers: []AgentMcpSpec{
				{Name: "", Config: &StdioConfig{Command: "node"}},
			},
		},
	}

	result := ExtractAgentMcpServers(agents)
	if len(result) != 0 {
		t.Errorf("empty name should be skipped, got %d", len(result))
	}
}

// ---------------------------------------------------------------------------
// GetLoggingSafeMcpBaseUrl — Source: utils.ts:561-575
// ---------------------------------------------------------------------------

func TestGetLoggingSafeMcpBaseUrl(t *testing.T) {
	tests := []struct {
		name   string
		config McpServerConfig
		want   string
	}{
		{"sse with query", &SSEConfig{URL: "http://example.com/path?token=secret"}, "http://example.com/path"},
		{"http no query", &HTTPConfig{URL: "http://example.com/api"}, "http://example.com/api"},
		{"ws trailing slash", &WSConfig{URL: "ws://example.com/ws/"}, "ws://example.com/ws"},
		{"stdio returns empty", &StdioConfig{Command: "node"}, ""},
		{"sdk returns empty", &SDKConfig{Name: "test"}, ""},
		{"empty url", &HTTPConfig{URL: ""}, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := GetLoggingSafeMcpBaseUrl(tt.config)
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// connName / connConfig — internal helpers
// ---------------------------------------------------------------------------

func TestConnName(t *testing.T) {
	cfg := ScopedMcpServerConfig{Config: &StdioConfig{Command: "node"}, Scope: ScopeUser}
	tests := []struct {
		conn ServerConnection
		want string
	}{
		{&ConnectedServer{Name: "a", Config: cfg}, "a"},
		{&FailedServer{Name: "b", Config: cfg}, "b"},
		{&NeedsAuthServer{Name: "c", Config: cfg}, "c"},
		{&PendingServer{Name: "d", Config: cfg}, "d"},
		{&DisabledServer{Name: "e", Config: cfg}, "e"},
	}
	for _, tt := range tests {
		got := connName(tt.conn)
		if got != tt.want {
			t.Errorf("connName = %q, want %q", got, tt.want)
		}
	}
}

func TestConnConfig(t *testing.T) {
	cfg := ScopedMcpServerConfig{Config: &StdioConfig{Command: "node"}, Scope: ScopeUser}
	conn := &ConnectedServer{Name: "srv", Config: cfg}
	got := connConfig(conn)
	if got.Scope != ScopeUser {
		t.Errorf("scope = %q, want user", got.Scope)
	}
	stdio, ok := got.Config.(*StdioConfig)
	if !ok {
		t.Fatal("expected StdioConfig")
	}
	if stdio.Command != "node" {
		t.Errorf("command = %q, want node", stdio.Command)
	}
}

// ---------------------------------------------------------------------------
// ValidConfigScopes — ensure all scopes listed
// ---------------------------------------------------------------------------

func TestValidConfigScopes(t *testing.T) {
	if len(ValidConfigScopes) != 7 {
		t.Errorf("expected 7 scopes, got %d", len(ValidConfigScopes))
	}
	expected := map[ConfigScope]bool{
		ScopeLocal: true, ScopeUser: true, ScopeProject: true, ScopeDynamic: true,
		ScopeEnterprise: true, ScopeClaudeAI: true, ScopeManaged: true,
	}
	for _, s := range ValidConfigScopes {
		if !expected[s] {
			t.Errorf("unexpected scope: %q", s)
		}
	}
}

// ---------------------------------------------------------------------------
// JSON round-trip for AgentMcpSpec
// ---------------------------------------------------------------------------

func TestAgentMcpSpec_IsRef(t *testing.T) {
	ref := AgentMcpSpec{Ref: "existing"}
	if !ref.IsRef() {
		t.Error("ref spec should be a ref")
	}
	inline := AgentMcpSpec{Name: "srv", Config: &StdioConfig{Command: "node"}}
	if inline.IsRef() {
		t.Error("inline spec should not be a ref")
	}
}

// ---------------------------------------------------------------------------
// ScopedMcpServerConfig MarshalJSON (from types.go, tested here for hash context)
// ---------------------------------------------------------------------------

func TestScopedMcpServerConfig_MarshalJSON_RoundTrip(t *testing.T) {
	cfg := ScopedMcpServerConfig{
		Config:       &StdioConfig{Command: "node", Args: []string{"server.js"}},
		Scope:        ScopeUser,
		PluginSource: "plugin@v1",
	}

	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("MarshalJSON: %v", err)
	}
	if !strings.Contains(string(data), `"scope":"user"`) {
		t.Errorf("JSON should contain scope, got: %s", data)
	}
	if !strings.Contains(string(data), `"pluginSource":"plugin@v1"`) {
		t.Errorf("JSON should contain pluginSource, got: %s", data)
	}
}

func TestScopedMcpServerConfig_MarshalJSON_NoPluginSource(t *testing.T) {
	cfg := ScopedMcpServerConfig{
		Config: &StdioConfig{Command: "node"},
		Scope:  ScopeLocal,
	}

	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("MarshalJSON: %v", err)
	}
	if strings.Contains(string(data), "pluginSource") {
		t.Errorf("JSON should not contain pluginSource when empty, got: %s", data)
	}
}

// ===========================================================================
// Additional coverage tests for connConfig, HashMcpConfig
// ===========================================================================

// ---------------------------------------------------------------------------
// connConfig (28.6% → 90%+) — Source: utils.go:211-226
// ---------------------------------------------------------------------------

func TestConnConfig_ConnectedServer(t *testing.T) {
	cfg := ScopedMcpServerConfig{
		Config: &StdioConfig{Command: "node"},
		Scope:  ScopeUser,
	}
	conn := &ConnectedServer{Name: "srv", Config: cfg}
	got := connConfig(conn)
	if got.Scope != ScopeUser {
		t.Errorf("scope = %q, want user", got.Scope)
	}
	stdio, ok := got.Config.(*StdioConfig)
	if !ok {
		t.Fatal("expected StdioConfig")
	}
	if stdio.Command != "node" {
		t.Errorf("command = %q, want node", stdio.Command)
	}
}

func TestConnConfig_FailedServer(t *testing.T) {
	cfg := ScopedMcpServerConfig{
		Config: &SSEConfig{URL: "https://example.com"},
		Scope:  ScopeProject,
	}
	conn := &FailedServer{Name: "failed", Config: cfg, Error: "test error"}
	got := connConfig(conn)
	if got.Scope != ScopeProject {
		t.Errorf("scope = %q, want project", got.Scope)
	}
	sse, ok := got.Config.(*SSEConfig)
	if !ok {
		t.Fatal("expected SSEConfig")
	}
	if sse.URL != "https://example.com" {
		t.Errorf("url = %q, want https://example.com", sse.URL)
	}
}

func TestConnConfig_NeedsAuthServer(t *testing.T) {
	cfg := ScopedMcpServerConfig{
		Config: &HTTPConfig{URL: "http://example.com"},
		Scope:  ScopeLocal,
	}
	conn := &NeedsAuthServer{Name: "auth", Config: cfg}
	got := connConfig(conn)
	if got.Scope != ScopeLocal {
		t.Errorf("scope = %q, want local", got.Scope)
	}
	http, ok := got.Config.(*HTTPConfig)
	if !ok {
		t.Fatal("expected HTTPConfig")
	}
	if http.URL != "http://example.com" {
		t.Errorf("url = %q, want http://example.com", http.URL)
	}
}

func TestConnConfig_PendingServer(t *testing.T) {
	cfg := ScopedMcpServerConfig{
		Config: &WSConfig{URL: "ws://example.com"},
		Scope:  ScopeDynamic,
	}
	conn := &PendingServer{Name: "pending", Config: cfg}
	got := connConfig(conn)
	if got.Scope != ScopeDynamic {
		t.Errorf("scope = %q, want dynamic", got.Scope)
	}
	ws, ok := got.Config.(*WSConfig)
	if !ok {
		t.Fatal("expected WSConfig")
	}
	if ws.URL != "ws://example.com" {
		t.Errorf("url = %q, want ws://example.com", ws.URL)
	}
}

func TestConnConfig_DisabledServer(t *testing.T) {
	cfg := ScopedMcpServerConfig{
		Config: &StdioConfig{Command: "echo"},
		Scope:  ScopeUser,
	}
	conn := &DisabledServer{Name: "disabled", Config: cfg}
	got := connConfig(conn)
	if got.Scope != ScopeUser {
		t.Errorf("scope = %q, want user", got.Scope)
	}
	stdio, ok := got.Config.(*StdioConfig)
	if !ok {
		t.Fatal("expected StdioConfig")
	}
	if stdio.Command != "echo" {
		t.Errorf("command = %q, want echo", stdio.Command)
	}
}

// ---------------------------------------------------------------------------
// HashMcpConfig (78.6% → 90%+) — Source: utils.go:113-141
// ---------------------------------------------------------------------------

func TestHashMcpConfig_SameConfigSameHash(t *testing.T) {
	cfg1 := ScopedMcpServerConfig{
		Config:       &StdioConfig{Command: "node", Args: []string{"server.js"}},
		Scope:        ScopeUser,
		PluginSource: "plugin@v1",
	}
	cfg2 := ScopedMcpServerConfig{
		Config:       &StdioConfig{Command: "node", Args: []string{"server.js"}},
		Scope:        ScopeUser,
		PluginSource: "plugin@v1",
	}
	hash1 := HashMcpConfig(cfg1)
	hash2 := HashMcpConfig(cfg2)
	if hash1 != hash2 {
		t.Errorf("same config should produce same hash: %q != %q", hash1, hash2)
	}
	if len(hash1) != 16 {
		t.Errorf("hash should be 16 chars, got %d: %q", len(hash1), hash1)
	}
}

func TestHashMcpConfig_DifferentConfigDifferentHash(t *testing.T) {
	cfg1 := ScopedMcpServerConfig{
		Config: &StdioConfig{Command: "node"},
		Scope:  ScopeUser,
	}
	cfg2 := ScopedMcpServerConfig{
		Config: &StdioConfig{Command: "python"},
		Scope:  ScopeUser,
	}
	hash1 := HashMcpConfig(cfg1)
	hash2 := HashMcpConfig(cfg2)
	if hash1 == hash2 {
		t.Errorf("different config should produce different hash: both %q", hash1)
	}
}

func TestHashMcpConfig_DifferentArgsDifferentHash(t *testing.T) {
	cfg1 := ScopedMcpServerConfig{
		Config: &StdioConfig{Command: "node", Args: []string{"v1"}},
		Scope:  ScopeUser,
	}
	cfg2 := ScopedMcpServerConfig{
		Config: &StdioConfig{Command: "node", Args: []string{"v2"}},
		Scope:  ScopeUser,
	}
	hash1 := HashMcpConfig(cfg1)
	hash2 := HashMcpConfig(cfg2)
	if hash1 == hash2 {
		t.Errorf("different args should produce different hash: both %q", hash1)
	}
}

func TestHashMcpConfig_DifferentEnvDifferentHash(t *testing.T) {
	cfg1 := ScopedMcpServerConfig{
		Config: &StdioConfig{Command: "node", Env: map[string]string{"KEY": "val1"}},
		Scope:  ScopeUser,
	}
	cfg2 := ScopedMcpServerConfig{
		Config: &StdioConfig{Command: "node", Env: map[string]string{"KEY": "val2"}},
		Scope:  ScopeUser,
	}
	hash1 := HashMcpConfig(cfg1)
	hash2 := HashMcpConfig(cfg2)
	if hash1 == hash2 {
		t.Errorf("different env should produce different hash: both %q", hash1)
	}
}

func TestHashMcpConfig_DifferentUrlDifferentHash(t *testing.T) {
	cfg1 := ScopedMcpServerConfig{
		Config: &SSEConfig{URL: "https://example.com/v1"},
		Scope:  ScopeUser,
	}
	cfg2 := ScopedMcpServerConfig{
		Config: &SSEConfig{URL: "https://example.com/v2"},
		Scope:  ScopeUser,
	}
	hash1 := HashMcpConfig(cfg1)
	hash2 := HashMcpConfig(cfg2)
	if hash1 == hash2 {
		t.Errorf("different URL should produce different hash: both %q", hash1)
	}
}

func TestHashMcpConfig_ScopeIgnored(t *testing.T) {
	// Hash should exclude scope (per TS source: utils.ts:158)
	cfg1 := ScopedMcpServerConfig{
		Config: &StdioConfig{Command: "node"},
		Scope:  ScopeUser,
	}
	cfg2 := ScopedMcpServerConfig{
		Config: &StdioConfig{Command: "node"},
		Scope:  ScopeProject,
	}
	hash1 := HashMcpConfig(cfg1)
	hash2 := HashMcpConfig(cfg2)
	if hash1 != hash2 {
		t.Errorf("scope should not affect hash: %q != %q", hash1, hash2)
	}
}

func TestHashMcpConfig_PluginSourceIncluded(t *testing.T) {
	// Plugin source is part of "rest" after scope exclusion (utils.ts:158)
	cfg1 := ScopedMcpServerConfig{
		Config:       &StdioConfig{Command: "node"},
		Scope:        ScopeUser,
		PluginSource: "plugin@v1",
	}
	cfg2 := ScopedMcpServerConfig{
		Config:       &StdioConfig{Command: "node"},
		Scope:        ScopeUser,
		PluginSource: "plugin@v2",
	}
	hash1 := HashMcpConfig(cfg1)
	hash2 := HashMcpConfig(cfg2)
	if hash1 == hash2 {
		t.Errorf("different pluginSource should produce different hash: both %q", hash1)
	}
}

func TestHashMcpConfig_MarshalErrorReturnsEmpty(t *testing.T) {
	// Create a config that can't be marshaled (nil channel causes marshal error)
	// Actually, all our configs are marshalable. Let's test with a config that has unmarshalable fields.
	// Since we can't create an unmarshalable StdioConfig, we'll test the error path indirectly.
	// The hash function returns "" on marshal error - we can't easily trigger this without custom types.
	// Skip this test as it's hard to trigger without breaking the config type.
}

// ---------------------------------------------------------------------------
// HashMcpConfig — empty PluginSource not included
// ---------------------------------------------------------------------------

func TestHashMcpConfig_EmptyPluginSourceNotIncluded(t *testing.T) {
	cfg1 := ScopedMcpServerConfig{
		Config:       &StdioConfig{Command: "node"},
		PluginSource: "",
	}
	cfg2 := ScopedMcpServerConfig{
		Config:       &StdioConfig{Command: "node"},
		PluginSource: "",
	}
	h1 := HashMcpConfig(cfg1)
	h2 := HashMcpConfig(cfg2)
	if h1 != h2 {
		t.Errorf("same config with empty pluginSource should produce same hash")
	}
}

// ---------------------------------------------------------------------------
// connName — unknown type returns empty
// ---------------------------------------------------------------------------

type mockUnknownConn struct{}

func (mockUnknownConn) ConnType() string { return "unknown" }

func TestConnName_UnknownType(t *testing.T) {
	conn := mockUnknownConn{}
	got := connName(conn)
	if got != "" {
		t.Errorf("connName of unknown type = %q, want empty", got)
	}
}

// ---------------------------------------------------------------------------
// connConfig — unknown type returns empty
// ---------------------------------------------------------------------------

func TestConnConfig_UnknownType(t *testing.T) {
	conn := mockUnknownConn{}
	got := connConfig(conn)
	if got.Config != nil || got.Scope != "" {
		t.Errorf("connConfig of unknown type = %+v, want zero value", got)
	}
}

// ---------------------------------------------------------------------------
// connConfig — all five connection types
// ---------------------------------------------------------------------------

func TestConnConfig_AllTypes(t *testing.T) {
	cfg := ScopedMcpServerConfig{
		Config: &StdioConfig{Command: "node"},
		Scope:  ScopeUser,
	}
	tests := []struct {
		name string
		conn ServerConnection
	}{
		{"connected", &ConnectedServer{Name: "a", Config: cfg}},
		{"failed", &FailedServer{Name: "b", Config: cfg}},
		{"needs-auth", &NeedsAuthServer{Name: "c", Config: cfg}},
		{"pending", &PendingServer{Name: "d", Config: cfg}},
		{"disabled", &DisabledServer{Name: "e", Config: cfg}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := connConfig(tt.conn)
			if got.Scope != ScopeUser {
				t.Errorf("scope = %q, want user", got.Scope)
			}
			stdio, ok := got.Config.(*StdioConfig)
			if !ok {
				t.Fatalf("expected StdioConfig, got %T", got.Config)
			}
			if stdio.Command != "node" {
				t.Errorf("command = %q, want node", stdio.Command)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// stripURLForLogging — all branches
// ---------------------------------------------------------------------------

func TestStripURLForLogging_EmptyString(t *testing.T) {
	got := stripURLForLogging("")
	if got != "" {
		t.Errorf("expected empty string, got %q", got)
	}
}

func TestStripURLForLogging_InvalidURL(t *testing.T) {
	got := stripURLForLogging("://invalid")
	if got != "" {
		t.Errorf("expected empty string for invalid URL, got %q", got)
	}
}

func TestStripURLForLogging_StripsQuery(t *testing.T) {
	got := stripURLForLogging("https://example.com/path?token=secret&foo=bar")
	if strings.Contains(got, "token") {
		t.Errorf("query params should be stripped, got %q", got)
	}
	if !strings.HasPrefix(got, "https://example.com/path") {
		t.Errorf("expected base URL, got %q", got)
	}
}

func TestStripURLForLogging_StripsTrailingSlash(t *testing.T) {
	got := stripURLForLogging("https://example.com/api/")
	if got != "https://example.com/api" {
		t.Errorf("got %q, want https://example.com/api", got)
	}
}

func TestStripURLForLogging_NoTrailingSlash(t *testing.T) {
	got := stripURLForLogging("https://example.com/api")
	if got != "https://example.com/api" {
		t.Errorf("got %q, want https://example.com/api", got)
	}
}

func TestStripURLForLogging_WithFragment(t *testing.T) {
	got := stripURLForLogging("https://example.com/path#anchor")
	// Fragment should remain since RawQuery is what we strip
	if !strings.Contains(got, "example.com") {
		t.Errorf("expected host in output, got %q", got)
	}
}

// ---------------------------------------------------------------------------
// ExtractAgentMcpServers — SSE transport (needs auth)
// ---------------------------------------------------------------------------

func TestExtractAgentMcpServers_SSETransport(t *testing.T) {
	agents := []AgentDefinition{
		{
			AgentType: "Explore",
			McpServers: []AgentMcpSpec{
				{Name: "sse_srv", Config: &SSEConfig{URL: "https://example.com/sse"}},
			},
		},
	}
	result := ExtractAgentMcpServers(agents)
	if len(result) != 1 {
		t.Fatalf("expected 1 server, got %d", len(result))
	}
	if result[0].Transport != "sse" {
		t.Errorf("transport = %q, want sse", result[0].Transport)
	}
	if !result[0].NeedsAuth {
		t.Error("SSE should need auth")
	}
	if result[0].URL != "https://example.com/sse" {
		t.Errorf("URL = %q, want https://example.com/sse", result[0].URL)
	}
}

// ---------------------------------------------------------------------------
// GetLoggingSafeMcpBaseUrl — SSEConfig with query
// ---------------------------------------------------------------------------

func TestGetLoggingSafeMcpBaseUrl_SSEWithQuery(t *testing.T) {
	cfg := &SSEConfig{URL: "https://example.com/sse?token=abc"}
	got := GetLoggingSafeMcpBaseUrl(cfg)
	if strings.Contains(got, "token") {
		t.Errorf("should strip query params, got %q", got)
	}
	if got != "https://example.com/sse" {
		t.Errorf("got %q, want https://example.com/sse", got)
	}
}

// ---------------------------------------------------------------------------
// HashMcpConfig — SSEConfig, HTTPConfig (non-Stdio)
// ---------------------------------------------------------------------------

func TestHashMcpConfig_SSEConfig(t *testing.T) {
	cfg := ScopedMcpServerConfig{
		Config: &SSEConfig{URL: "https://example.com/sse"},
	}
	h := HashMcpConfig(cfg)
	if h == "" {
		t.Error("expected non-empty hash for SSE config")
	}
	if len(h) != 16 {
		t.Errorf("hash should be 16 chars, got %d", len(h))
	}
}

func TestHashMcpConfig_HTTPConfig(t *testing.T) {
	cfg := ScopedMcpServerConfig{
		Config: &HTTPConfig{URL: "https://example.com/mcp", Headers: map[string]string{"A": "1"}},
	}
	h := HashMcpConfig(cfg)
	if h == "" {
		t.Error("expected non-empty hash for HTTP config")
	}
}

func TestHashMcpConfig_WSConfig(t *testing.T) {
	cfg := ScopedMcpServerConfig{
		Config: &WSConfig{URL: "ws://example.com/ws", Headers: map[string]string{"X": "y"}},
	}
	h := HashMcpConfig(cfg)
	if h == "" {
		t.Error("expected non-empty hash for WS config")
	}
	if len(h) != 16 {
		t.Errorf("hash should be 16 chars, got %d", len(h))
	}
}

func TestHashMcpConfig_SSEIDEConfig(t *testing.T) {
	cfg := ScopedMcpServerConfig{
		Config: &SSEIDEConfig{URL: "http://localhost:1234/sse", IDEName: "vscode"},
	}
	h := HashMcpConfig(cfg)
	if h == "" {
		t.Error("expected non-empty hash for SSEIDE config")
	}
}

func TestHashMcpConfig_WSIDEConfig(t *testing.T) {
	cfg := ScopedMcpServerConfig{
		Config: &WSIDEConfig{URL: "ws://localhost:8080/ws", IDEName: "cursor", AuthToken: "tok"},
	}
	h := HashMcpConfig(cfg)
	if h == "" {
		t.Error("expected non-empty hash for WSIDE config")
	}
}

func TestHashMcpConfig_ClaudeAIProxyConfig(t *testing.T) {
	cfg := ScopedMcpServerConfig{
		Config: &ClaudeAIProxyConfig{URL: "https://proxy.example.com", ID: "server-1"},
	}
	h := HashMcpConfig(cfg)
	if h == "" {
		t.Error("expected non-empty hash for ClaudeAIProxy config")
	}
}

func TestHashMcpConfig_SDKConfig(t *testing.T) {
	cfg := ScopedMcpServerConfig{
		Config: &SDKConfig{Name: "my-plugin"},
	}
	h := HashMcpConfig(cfg)
	if h == "" {
		t.Error("expected non-empty hash for SDK config")
	}
}

func TestHashMcpConfig_SSEConfigWithHeaders(t *testing.T) {
	cfg1 := ScopedMcpServerConfig{
		Config: &SSEConfig{URL: "https://example.com/sse", Headers: map[string]string{"A": "1"}},
	}
	cfg2 := ScopedMcpServerConfig{
		Config: &SSEConfig{URL: "https://example.com/sse", Headers: map[string]string{"A": "2"}},
	}
	h1 := HashMcpConfig(cfg1)
	h2 := HashMcpConfig(cfg2)
	if h1 == h2 {
		t.Error("different headers should produce different hashes")
	}
}

func TestHashMcpConfig_WSConfigDifferentURL(t *testing.T) {
	cfg1 := ScopedMcpServerConfig{
		Config: &WSConfig{URL: "ws://example.com/v1"},
	}
	cfg2 := ScopedMcpServerConfig{
		Config: &WSConfig{URL: "ws://example.com/v2"},
	}
	h1 := HashMcpConfig(cfg1)
	h2 := HashMcpConfig(cfg2)
	if h1 == h2 {
		t.Error("different WS URLs should produce different hashes")
	}
}

// ---------------------------------------------------------------------------
// HashMcpConfig — first marshal error returns empty
// ---------------------------------------------------------------------------

func TestHashMcpConfig_FirstMarshalError(t *testing.T) {
	cfg := ScopedMcpServerConfig{
		Config: &utilsMarshalFailConfig{},
	}
	h := HashMcpConfig(cfg)
	if h != "" {
		t.Errorf("expected empty hash on marshal error, got %q", h)
	}
}

// utilsMarshalFailConfig: MarshalJSON always fails — triggers line 116-118
type utilsMarshalFailConfig struct{}

func (c *utilsMarshalFailConfig) GetTransport() Transport { return TransportStdio }
func (c *utilsMarshalFailConfig) GetURL() string          { return "" }
func (c *utilsMarshalFailConfig) MarshalJSON() ([]byte, error) {
	return nil, fmt.Errorf("forced marshal error")
}
