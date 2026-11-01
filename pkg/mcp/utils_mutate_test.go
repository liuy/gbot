package mcp

import (
	"crypto/sha256"
	"fmt"
	"reflect"
	"slices"
	"testing"
)

func mutateHash16(payload string) string {
	sum := sha256.Sum256([]byte(payload))
	return fmt.Sprintf("%x", sum)[:16]
}

// The pluginSource key is part of config identity only when the plugin
// actually names itself, so an empty PluginSource must leave the hashed
// payload untouched.
func TestHashMcpConfig_EmptyPluginSourceNotInjected(t *testing.T) {
	got := HashMcpConfig(ScopedMcpServerConfig{Config: &StdioConfig{Command: "srv"}})
	want := mutateHash16(`{"command":"srv"}`)
	if got != want {
		t.Errorf("HashMcpConfig(PluginSource=\"\") = %q, want %q (payload %q)", got, want, `{"command":"srv"}`)
	}
}

// Positive control: a non-empty PluginSource is part of the payload,
// sorted alongside the config keys.
func TestHashMcpConfig_NonEmptyPluginSourceInjected(t *testing.T) {
	got := HashMcpConfig(ScopedMcpServerConfig{
		Config:       &StdioConfig{Command: "srv"},
		PluginSource: "slack@anthropic",
	})
	want := mutateHash16(`{"command":"srv","pluginSource":"slack@anthropic"}`)
	if got != want {
		t.Errorf("HashMcpConfig(PluginSource=slack) = %q, want %q (payload %q)", got, want,
			`{"command":"srv","pluginSource":"slack@anthropic"}`)
	}
}

// A nil config marshals to JSON "null", which is not an object, so there is
// no map to attach pluginSource to.
func TestHashMcpConfig_NilConfigGetsNoPluginSourceKey(t *testing.T) {
	got := HashMcpConfig(ScopedMcpServerConfig{PluginSource: "slack@anthropic"})
	want := mutateHash16("null")
	if got != want {
		t.Errorf("HashMcpConfig(nil config) = %q, want %q (payload %q)", got, want, "null")
	}
}

// The no-stale fast path is content-identical to the fall-through; the only
// observable difference is that it hands back the caller's own Clients slice
// rather than a freshly built one (utils.ts:205-207 returns the input untouched).
func TestExcludeStalePluginClients_NoStaleReturnsInputClientsSlice(t *testing.T) {
	cfg := ScopedMcpServerConfig{Config: &StdioConfig{Command: "keep"}, Scope: ScopeUser}
	state := MCPConnectionState{
		Clients:   []ServerConnection{&ConnectedServer{Name: "a", Config: cfg}},
		Tools:     []SerializedTool{{Name: "mcp__a__t1"}},
		Commands:  []MCPCommand{{Name: "mcp__a__c1"}},
		Resources: map[string][]ServerResource{"a": {{URI: "res://a"}}},
	}
	res := ExcludeStalePluginClients(state, map[string]ScopedMcpServerConfig{"a": cfg})

	if res.Stale != nil {
		t.Errorf("Stale = %v, want nil", res.Stale)
	}
	if len(res.Clients) != 1 || res.Clients[0] != state.Clients[0] {
		t.Fatalf("Clients = %v, want the single input connection", res.Clients)
	}
	if &res.Clients[0] != &state.Clients[0] {
		t.Error("no-stale path must return the caller's Clients slice, not a rebuilt copy")
	}
}

// Commands/Resources must survive the no-stale return.
func TestExcludeStalePluginClients_NoStaleKeepsToolsCommandsResources(t *testing.T) {
	cfg := ScopedMcpServerConfig{Config: &StdioConfig{Command: "keep"}, Scope: ScopeUser}
	state := MCPConnectionState{
		Clients:   []ServerConnection{&ConnectedServer{Name: "a", Config: cfg}},
		Tools:     []SerializedTool{{Name: "mcp__a__t1"}, {Name: "mcp__a__t2"}},
		Commands:  []MCPCommand{{Name: "mcp__a__c1"}},
		Resources: map[string][]ServerResource{"a": {{URI: "res://a"}}},
	}
	res := ExcludeStalePluginClients(state, map[string]ScopedMcpServerConfig{"a": cfg})

	if !reflect.DeepEqual(res.Tools, state.Tools) {
		t.Errorf("Tools = %v, want %v", res.Tools, state.Tools)
	}
	if !reflect.DeepEqual(res.Commands, state.Commands) {
		t.Errorf("Commands = %v, want %v", res.Commands, state.Commands)
	}
	if !reflect.DeepEqual(res.Resources, state.Resources) {
		t.Errorf("Resources = %v, want %v", res.Resources, state.Resources)
	}
}

// The stale path must keep scanning past a client with no fresh config, keep
// filtering surviving clients, and carry all four result fields.
func TestExcludeStalePluginClients_ExcludesOnlyTheStaleClient(t *testing.T) {
	unchanged := ScopedMcpServerConfig{Config: &StdioConfig{Command: "keep"}, Scope: ScopeUser}
	state := MCPConnectionState{
		Clients: []ServerConnection{
			// No fresh config, scope is not dynamic → kept, and the loop must
			// keep scanning the clients behind it.
			&ConnectedServer{Name: "a", Config: ScopedMcpServerConfig{Config: &StdioConfig{Command: "gone"}, Scope: ScopeUser}},
			// Fresh config differs → stale.
			&FailedServer{Name: "b", Config: ScopedMcpServerConfig{Config: &StdioConfig{Command: "old"}, Scope: ScopeUser}},
			// Fresh config identical → kept.
			&ConnectedServer{Name: "c", Config: unchanged},
		},
		Tools:    []SerializedTool{{Name: "mcp__a__t1"}, {Name: "mcp__b__t2"}, {Name: "mcp__c__t3"}},
		Commands: []MCPCommand{{Name: "mcp__a__c1"}, {Name: "mcp__b__c2"}, {Name: "mcp__c__c3"}},
		Resources: map[string][]ServerResource{
			"a": {{URI: "res://a"}},
			"b": {{URI: "res://b"}},
			"c": {{URI: "res://c"}},
		},
	}
	configs := map[string]ScopedMcpServerConfig{
		"b": {Config: &StdioConfig{Command: "new"}, Scope: ScopeUser},
		"c": unchanged,
	}

	res := ExcludeStalePluginClients(state, configs)

	if len(res.Stale) != 1 || connName(res.Stale[0]) != "b" {
		t.Fatalf("Stale = %v, want exactly [b]", mutateConnNames(res.Stale))
	}
	if got := mutateConnNames(res.Clients); !slices.Equal(got, []string{"a", "c"}) {
		t.Errorf("Clients = %v, want [a c]", got)
	}
	gotTools := make([]string, 0, len(res.Tools))
	for _, tool := range res.Tools {
		gotTools = append(gotTools, tool.Name)
	}
	if !slices.Equal(gotTools, []string{"mcp__a__t1", "mcp__c__t3"}) {
		t.Errorf("Tools = %v, want [mcp__a__t1 mcp__c__t3]", gotTools)
	}
	gotCommands := make([]string, 0, len(res.Commands))
	for _, cmd := range res.Commands {
		gotCommands = append(gotCommands, cmd.Name)
	}
	if !slices.Equal(gotCommands, []string{"mcp__a__c1", "mcp__c__c3"}) {
		t.Errorf("Commands = %v, want [mcp__a__c1 mcp__c__c3]", gotCommands)
	}
	if _, ok := res.Resources["b"]; ok {
		t.Errorf("Resources still carries the stale server: %v", res.Resources)
	}
	if len(res.Resources) != 2 {
		t.Errorf("Resources = %v, want exactly the a and c entries", res.Resources)
	}
}

func mutateConnNames(conns []ServerConnection) []string {
	names := make([]string, 0, len(conns))
	for _, c := range conns {
		names = append(names, connName(c))
	}
	return names
}

// The invalid-scope message is the caller's only guide to a legal value, so
// it must enumerate all seven scopes in declaration order.
func TestEnsureConfigScope_InvalidListsEveryScope(t *testing.T) {
	_, err := EnsureConfigScope("bogus")
	if err == nil {
		t.Fatal("expected error for invalid scope")
	}
	want := "invalid scope: bogus. Must be one of: local, user, project, dynamic, enterprise, claudeai, managed"
	if err.Error() != want {
		t.Errorf("error = %q, want %q", err.Error(), want)
	}
}

// Both auto-approve modes need the bypass flag AND the project-settings gate
// open; either half alone must leave the server pending.
func TestGetProjectMcpServerStatus_AutoApproveNeedsBothFlags(t *testing.T) {
	cases := []struct {
		name     string
		settings McpProjectSettings
		want     string
	}{
		{"bypass-only", McpProjectSettings{SkipDangerousPermission: true}, "pending"},
		{"project-settings-only", McpProjectSettings{ProjectSettingsEnabled: true}, "pending"},
		{"non-interactive-only", McpProjectSettings{NonInteractive: true}, "pending"},
		{"bypass+project-settings", McpProjectSettings{SkipDangerousPermission: true, ProjectSettingsEnabled: true}, "approved"},
		{"non-interactive+project-settings", McpProjectSettings{NonInteractive: true, ProjectSettingsEnabled: true}, "approved"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := GetProjectMcpServerStatus("srv", tc.settings); got != tc.want {
				t.Errorf("GetProjectMcpServerStatus(%+v) = %q, want %q", tc.settings, got, tc.want)
			}
		})
	}
}

// Agents without mcpServers, string references and empty names must be
// skipped without aborting their loops.
func TestExtractAgentMcpServers_SkipsRefsAndEmptyNames(t *testing.T) {
	agents := []AgentDefinition{
		{AgentType: "empty"},
		{AgentType: "mixed", McpServers: []AgentMcpSpec{
			{Ref: "existing"},
			{Ref: "also-a-ref", Name: "refname", Config: &StdioConfig{Command: "must-not-appear"}},
			{Name: ""},
			{Name: "kept", Config: &StdioConfig{Command: "srv"}},
		}},
	}

	got := ExtractAgentMcpServers(agents)

	want := []AgentMcpServerInfo{{
		Name:         "kept",
		SourceAgents: []string{"mixed"},
		Transport:    "stdio",
		Command:      "srv",
	}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ExtractAgentMcpServers = %+v, want %+v", got, want)
	}
}

// Every transport branch must carry the server name and its source agents,
// and an unsupported config type must be dropped.
func TestExtractAgentMcpServers_TransportFields(t *testing.T) {
	agents := []AgentDefinition{{AgentType: "a1", McpServers: []AgentMcpSpec{
		{Name: "ss", Config: &SSEConfig{URL: "https://sse.example/mcp"}},
		{Name: "ht", Config: &HTTPConfig{URL: "https://http.example/mcp"}},
		{Name: "ws", Config: &WSConfig{URL: "wss://ws.example/mcp"}},
		{Name: "st", Config: &StdioConfig{Command: "cmd"}},
		{Name: "un", Config: &SDKConfig{Name: "sdk"}},
	}}}

	got := ExtractAgentMcpServers(agents)

	want := []AgentMcpServerInfo{
		{Name: "ht", SourceAgents: []string{"a1"}, Transport: "http", URL: "https://http.example/mcp", NeedsAuth: true},
		{Name: "ss", SourceAgents: []string{"a1"}, Transport: "sse", URL: "https://sse.example/mcp", NeedsAuth: true},
		{Name: "st", SourceAgents: []string{"a1"}, Transport: "stdio", Command: "cmd"},
		{Name: "ws", SourceAgents: []string{"a1"}, Transport: "ws", URL: "wss://ws.example/mcp"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ExtractAgentMcpServers = %+v, want %+v", got, want)
	}
}

// Without the trailing sort.Slice the result is map iteration order. A 6-key
// map lives in one bucket, so each range yields one of 6 rotations of a fixed
// slot order — a single pass is a ~1/6 escape. Ten independent passes (range
// start is re-randomized per iteration) drive the escape probability to (1/6)^10.
func TestExtractAgentMcpServers_SortIsDeterministicWithManyServers(t *testing.T) {
	insertion := []string{"zeta", "mike", "alpha", "yankee", "delta", "kilo"}
	specs := make([]AgentMcpSpec, 0, len(insertion))
	for _, n := range insertion {
		specs = append(specs, AgentMcpSpec{Name: n, Config: &StdioConfig{Command: n}})
	}
	defs := []AgentDefinition{{AgentType: "a", McpServers: specs}}
	want := []string{"alpha", "delta", "kilo", "mike", "yankee", "zeta"}
	for round := range 10 {
		got := ExtractAgentMcpServers(defs)
		gotNames := make([]string, 0, len(got))
		for _, info := range got {
			gotNames = append(gotNames, info.Name)
		}
		if !slices.Equal(gotNames, want) {
			t.Fatalf("round %d: ExtractAgentMcpServers order = %v, want %v", round, gotNames, want)
		}
	}
}
