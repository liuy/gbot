package mcp

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Compile-time interface checks — verified by compilation itself
// ---------------------------------------------------------------------------

func TestInterfaceChecks(t *testing.T) {
	// These assignments verify the compile-time checks at the bottom of types.go.
	// If types.go compiles, these interfaces are satisfied.
	var _ ServerConnection = (*ConnectedServer)(nil)
	var _ ServerConnection = (*FailedServer)(nil)
	var _ ServerConnection = (*NeedsAuthServer)(nil)
	var _ ServerConnection = (*PendingServer)(nil)
	var _ ServerConnection = (*DisabledServer)(nil)

	var _ McpServerConfig = (*StdioConfig)(nil)
	var _ McpServerConfig = (*SSEConfig)(nil)
	var _ McpServerConfig = (*SSEIDEConfig)(nil)
	var _ McpServerConfig = (*WSIDEConfig)(nil)
	var _ McpServerConfig = (*HTTPConfig)(nil)
	var _ McpServerConfig = (*WSConfig)(nil)
	var _ McpServerConfig = (*SDKConfig)(nil)
	var _ McpServerConfig = (*ClaudeAIProxyConfig)(nil)
}

// ---------------------------------------------------------------------------
// ConnType exhaustive type switch — Source: types.ts:221-226 (5 variants)
// ---------------------------------------------------------------------------

func TestConnTypeExhaustive(t *testing.T) {
	tests := []struct {
		conn ServerConnection
		want string
	}{
		{&ConnectedServer{}, "connected"},
		{&FailedServer{}, "failed"},
		{&NeedsAuthServer{}, "needs-auth"},
		{&PendingServer{}, "pending"},
		{&DisabledServer{}, "disabled"},
	}
	for _, tt := range tests {
		if got := tt.conn.ConnType(); got != tt.want {
			t.Errorf("%T.ConnType() = %q, want %q", tt.conn, got, tt.want)
		}
	}
}

// ---------------------------------------------------------------------------
// GetTransport — verify each config returns correct transport
// ---------------------------------------------------------------------------

func TestGetTransport(t *testing.T) {
	tests := []struct {
		cfg  McpServerConfig
		want Transport
	}{
		{&StdioConfig{}, TransportStdio},
		{&SSEConfig{}, TransportSSE},
		{&SSEIDEConfig{}, TransportSSEIDE},
		{&WSIDEConfig{}, TransportWSIDE},
		{&HTTPConfig{}, TransportHTTP},
		{&WSConfig{}, TransportWS},
		{&SDKConfig{}, TransportSDK},
		{&ClaudeAIProxyConfig{}, TransportClaudeAIProxy},
	}
	for _, tt := range tests {
		if got := tt.cfg.GetTransport(); got != tt.want {
			t.Errorf("%T.GetTransport() = %q, want %q", tt.cfg, got, tt.want)
		}
	}
}

// ---------------------------------------------------------------------------
// UnmarshalServerConfig — discriminated union deserialization
// Source: types.ts:124-135 McpServerConfigSchema
// ---------------------------------------------------------------------------

func TestUnmarshalServerConfig_Stdio(t *testing.T) {
	// Source: types.ts:30 — type is optional for stdio
	t.Run("explicit type", func(t *testing.T) {
		raw := json.RawMessage(`{"type":"stdio","command":"npx","args":["-y","server"]}`)
		cfg, err := UnmarshalServerConfig(raw)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		stdio, ok := cfg.(*StdioConfig)
		if !ok {
			t.Fatalf("expected *StdioConfig, got %T", cfg)
		}
		if stdio.Command != "npx" {
			t.Errorf("Command = %q, want %q", stdio.Command, "npx")
		}
		if len(stdio.Args) != 2 || stdio.Args[0] != "-y" || stdio.Args[1] != "server" {
			t.Errorf("Args = %v, want [-y server]", stdio.Args)
		}
	})

	t.Run("implicit type (no type field)", func(t *testing.T) {
		// Source: types.ts:30 — type: z.literal('stdio').optional()
		raw := json.RawMessage(`{"command":"node","args":["server.js"]}`)
		cfg, err := UnmarshalServerConfig(raw)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		stdio, ok := cfg.(*StdioConfig)
		if !ok {
			t.Fatalf("expected *StdioConfig, got %T", cfg)
		}
		if stdio.Command != "node" {
			t.Errorf("Command = %q, want %q", stdio.Command, "node")
		}
	})

	t.Run("with env", func(t *testing.T) {
		raw := json.RawMessage(`{"command":"npx","args":["server"],"env":{"KEY":"value"}}`)
		cfg, err := UnmarshalServerConfig(raw)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		stdio := cfg.(*StdioConfig)
		if stdio.Env["KEY"] != "value" {
			t.Errorf("Env[KEY] = %q, want %q", stdio.Env["KEY"], "value")
		}
	})

	t.Run("empty command rejected", func(t *testing.T) {
		raw := json.RawMessage(`{"command":""}`)
		_, err := UnmarshalServerConfig(raw)
		if err == nil {
			t.Fatal("want error for empty command")
		}
		if !strings.Contains(err.Error(), "command cannot be empty") {
			t.Errorf("error = %q, want command cannot be empty", err.Error())
		}
	})
}

func TestUnmarshalServerConfig_SSE(t *testing.T) {
	raw := json.RawMessage(`{"type":"sse","url":"http://localhost:8080/sse","headers":{"Authorization":"Bearer token"}}`)
	cfg, err := UnmarshalServerConfig(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	sse, ok := cfg.(*SSEConfig)
	if !ok {
		t.Fatalf("expected *SSEConfig, got %T", cfg)
	}
	if sse.URL != "http://localhost:8080/sse" {
		t.Errorf("URL = %q, want %q", sse.URL, "http://localhost:8080/sse")
	}
	if sse.Headers["Authorization"] != "Bearer token" {
		t.Errorf("Headers[Authorization] = %q, want %q", sse.Headers["Authorization"], "Bearer token")
	}
}

func TestUnmarshalServerConfig_HTTP(t *testing.T) {
	raw := json.RawMessage(`{"type":"http","url":"http://localhost:8080/mcp"}`)
	cfg, err := UnmarshalServerConfig(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	httpCfg, ok := cfg.(*HTTPConfig)
	if !ok {
		t.Fatalf("expected *HTTPConfig, got %T", cfg)
	}
	if httpCfg.URL != "http://localhost:8080/mcp" {
		t.Errorf("URL = %q, want %q", httpCfg.URL, "http://localhost:8080/mcp")
	}
}

func TestUnmarshalServerConfig_WS(t *testing.T) {
	raw := json.RawMessage(`{"type":"ws","url":"ws://localhost:8080/ws"}`)
	cfg, err := UnmarshalServerConfig(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	wsCfg, ok := cfg.(*WSConfig)
	if !ok {
		t.Fatalf("expected *WSConfig, got %T", cfg)
	}
	if wsCfg.URL != "ws://localhost:8080/ws" {
		t.Errorf("URL = %q, want %q", wsCfg.URL, "ws://localhost:8080/ws")
	}
}

func TestUnmarshalServerConfig_SSEIDE(t *testing.T) {
	raw := json.RawMessage(`{"type":"sse-ide","url":"http://localhost:1234","ideName":"vscode"}`)
	cfg, err := UnmarshalServerConfig(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	ide, ok := cfg.(*SSEIDEConfig)
	if !ok {
		t.Fatalf("expected *SSEIDEConfig, got %T", cfg)
	}
	if ide.IDEName != "vscode" {
		t.Errorf("IDEName = %q, want %q", ide.IDEName, "vscode")
	}
}

func TestUnmarshalServerConfig_WSIDE(t *testing.T) {
	raw := json.RawMessage(`{"type":"ws-ide","url":"ws://localhost:5678","ideName":"cursor","authToken":"tok"}`)
	cfg, err := UnmarshalServerConfig(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	ide, ok := cfg.(*WSIDEConfig)
	if !ok {
		t.Fatalf("expected *WSIDEConfig, got %T", cfg)
	}
	if ide.AuthToken != "tok" {
		t.Errorf("AuthToken = %q, want %q", ide.AuthToken, "tok")
	}
}

func TestUnmarshalServerConfig_SDK(t *testing.T) {
	raw := json.RawMessage(`{"type":"sdk","name":"my-plugin"}`)
	cfg, err := UnmarshalServerConfig(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	sdkCfg, ok := cfg.(*SDKConfig)
	if !ok {
		t.Fatalf("expected *SDKConfig, got %T", cfg)
	}
	if sdkCfg.Name != "my-plugin" {
		t.Errorf("Name = %q, want %q", sdkCfg.Name, "my-plugin")
	}
}

func TestUnmarshalServerConfig_ClaudeAIProxy(t *testing.T) {
	raw := json.RawMessage(`{"type":"claudeai-proxy","url":"https://example.com","id":"server-1"}`)
	cfg, err := UnmarshalServerConfig(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	proxy, ok := cfg.(*ClaudeAIProxyConfig)
	if !ok {
		t.Fatalf("expected *ClaudeAIProxyConfig, got %T", cfg)
	}
	if proxy.ID != "server-1" {
		t.Errorf("ID = %q, want %q", proxy.ID, "server-1")
	}
}

func TestUnmarshalServerConfig_Unknown(t *testing.T) {
	raw := json.RawMessage(`{"type":"ftp","url":"ftp://example.com"}`)
	_, err := UnmarshalServerConfig(raw)
	if err == nil {
		t.Fatal("want error for unknown type")
	}
	if !strings.Contains(err.Error(), "unknown server config type") {
		t.Errorf("error = %q, want unknown server config type", err.Error())
	}
}

func TestUnmarshalServerConfig_InvalidJSON(t *testing.T) {
	raw := json.RawMessage(`not json`)
	_, err := UnmarshalServerConfig(raw)
	if err == nil {
		t.Fatal("want error for invalid JSON")
	}
	if !strings.Contains(err.Error(), "invalid server config") {
		t.Errorf("error = %q, want invalid server config message", err.Error())
	}
}

func TestUnmarshalServerConfig_OAuth(t *testing.T) {
	raw := json.RawMessage(`{
		"type": "sse",
		"url": "http://localhost:8080/sse",
		"oauth": {
			"clientId": "my-client",
			"callbackPort": 9090,
			"authServerMetadataUrl": "https://auth.example.com/.well-known"
		}
	}`)
	cfg, err := UnmarshalServerConfig(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	sse := cfg.(*SSEConfig)
	if sse.OAuth == nil {
		t.Fatal("expected OAuth config")
	}
	if sse.OAuth.ClientID != "my-client" {
		t.Errorf("ClientID = %q, want %q", sse.OAuth.ClientID, "my-client")
	}
	if sse.OAuth.CallbackPort != 9090 {
		t.Errorf("CallbackPort = %d, want %d", sse.OAuth.CallbackPort, 9090)
	}
}

// ---------------------------------------------------------------------------
// LoadMcpJsonConfig — Source: types.ts:171-175
// ---------------------------------------------------------------------------

func TestLoadMcpJsonConfig(t *testing.T) {
	data := []byte(`{
		"mcpServers": {
			"my-server": {
				"type": "stdio",
				"command": "npx",
				"args": ["-y", "mcp-server"]
			},
			"remote": {
				"type": "sse",
				"url": "http://localhost:8080/sse"
			}
		}
	}`)

	configs, err := LoadMcpJsonConfig(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(configs) != 2 {
		t.Fatalf("expected 2 configs, got %d", len(configs))
	}

	stdio, ok := configs["my-server"].(*StdioConfig)
	if !ok {
		t.Fatalf("my-server: expected *StdioConfig, got %T", configs["my-server"])
	}
	if stdio.Command != "npx" {
		t.Errorf("my-server Command = %q, want %q", stdio.Command, "npx")
	}

	sse, ok := configs["remote"].(*SSEConfig)
	if !ok {
		t.Fatalf("remote: expected *SSEConfig, got %T", configs["remote"])
	}
	if sse.URL != "http://localhost:8080/sse" {
		t.Errorf("remote URL = %q, want %q", sse.URL, "http://localhost:8080/sse")
	}
}

func TestLoadMcpJsonConfig_Invalid(t *testing.T) {
	_, err := LoadMcpJsonConfig([]byte(`not json`))
	if err == nil {
		t.Fatal("want error for invalid JSON")
	}
}

func TestLoadMcpJsonConfig_BadServer(t *testing.T) {
	data := []byte(`{"mcpServers": {"bad": {"type":"ftp"}}}`)
	_, err := LoadMcpJsonConfig(data)
	if err == nil {
		t.Fatal("want error for unknown server type")
	}
	if !strings.Contains(err.Error(), "bad") {
		t.Errorf("error should mention server name 'bad', got: %v", err)
	}
}

func TestLoadMcpJsonConfig_Empty(t *testing.T) {
	data := []byte(`{"mcpServers": {}}`)
	configs, err := LoadMcpJsonConfig(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(configs) != 0 {
		t.Errorf("expected 0 configs, got %d", len(configs))
	}
}

// ---------------------------------------------------------------------------
// JSON round-trip for config structs
// ---------------------------------------------------------------------------

func TestStdioConfig_RoundTrip(t *testing.T) {
	original := StdioConfig{Command: "npx", Args: []string{"-y", "server"}, Env: map[string]string{"KEY": "val"}}
	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded StdioConfig
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded.Command != original.Command {
		t.Errorf("Command = %q, want %q", decoded.Command, original.Command)
	}
	if decoded.Env["KEY"] != "val" {
		t.Errorf("Env[KEY] = %q, want %q", decoded.Env["KEY"], "val")
	}
}

func TestSSEConfig_RoundTrip(t *testing.T) {
	original := SSEConfig{
		URL:           "http://localhost:8080",
		Headers:       map[string]string{"Auth": "token"},
		HeadersHelper: "helper-cmd",
		OAuth:         &OAuthConfig{ClientID: "client-1", CallbackPort: 9090},
	}
	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded SSEConfig
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded.URL != original.URL {
		t.Errorf("URL = %q, want %q", decoded.URL, original.URL)
	}
	if decoded.Headers["Auth"] != "token" {
		t.Errorf("Headers[Auth] = %q, want %q", decoded.Headers["Auth"], "token")
	}
	if decoded.OAuth == nil || decoded.OAuth.ClientID != "client-1" {
		t.Errorf("OAuth.ClientID mismatch")
	}
}

func TestHTTPConfig_RoundTrip(t *testing.T) {
	original := HTTPConfig{URL: "http://localhost:3000", Headers: map[string]string{"X-Key": "val"}}
	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded HTTPConfig
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded.URL != original.URL {
		t.Errorf("URL = %q, want %q", decoded.URL, original.URL)
	}
}

func TestWSConfig_RoundTrip(t *testing.T) {
	original := WSConfig{URL: "ws://localhost:8080", Headers: map[string]string{"Auth": "tok"}}
	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded WSConfig
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded.URL != original.URL {
		t.Errorf("URL = %q, want %q", decoded.URL, original.URL)
	}
}

// ---------------------------------------------------------------------------
// ScopedMcpServerConfig MarshalJSON
// ---------------------------------------------------------------------------

func TestScopedMcpServerConfig_MarshalJSON(t *testing.T) {
	s := ScopedMcpServerConfig{
		Config:       &StdioConfig{Command: "npx", Args: []string{"server"}},
		Scope:        ScopeUser,
		PluginSource: "slack@anthropic",
	}
	data, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	// Verify scope and pluginSource are present
	if !strings.Contains(string(data), `"scope":"user"`) {
		t.Errorf("missing scope in JSON: %s", data)
	}
	if !strings.Contains(string(data), `"pluginSource":"slack@anthropic"`) {
		t.Errorf("missing pluginSource in JSON: %s", data)
	}
	if !strings.Contains(string(data), `"command":"npx"`) {
		t.Errorf("missing command in JSON: %s", data)
	}
}

// ---------------------------------------------------------------------------
// Error types
// ---------------------------------------------------------------------------

func TestMcpAuthError(t *testing.T) {
	err := &McpAuthError{ServerName: "my-server", Message: "token expired"}
	msg := err.Error()
	if !strings.Contains(msg, "my-server") {
		t.Errorf("Error() should contain server name: %q", msg)
	}
	if !strings.Contains(msg, "token expired") {
		t.Errorf("Error() should contain message: %q", msg)
	}
}

func TestMcpToolCallError(t *testing.T) {
	inner := errors.New("connection refused")
	err := &McpToolCallError{ServerName: "srv", ToolName: "read", Err: inner}
	msg := err.Error()
	if !strings.Contains(msg, "srv") || !strings.Contains(msg, "read") {
		t.Errorf("Error() should contain server and tool name: %q", msg)
	}
	if !errors.Is(err, inner) {
		t.Error("Unwrap() should return inner error")
	}
}

// ---------------------------------------------------------------------------
// ConnectedServer.Close — sync.Once idempotent
// ---------------------------------------------------------------------------

func TestConnectedServer_Close_Idempotent(t *testing.T) {
	calls := 0
	s := &ConnectedServer{
		Cleanup: func() error {
			calls++
			return nil
		},
	}
	// Close twice — Cleanup should be called only once
	if err := s.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if calls != 1 {
		t.Errorf("Cleanup called %d times, want 1", calls)
	}
}

func TestConnectedServer_Close_Nil(t *testing.T) {
	s := &ConnectedServer{Cleanup: nil}
	// Should not panic even with nil Cleanup
	// Actually, calling nil func panics — let's handle this
	// Plan says: default no-op cleanup
	// So we need Close() to handle nil Cleanup
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("Close() panicked with nil Cleanup: %v", r)
		}
	}()
	_ = s.Close()
}

// ---------------------------------------------------------------------------
// Scope validation — ConfigScope constants
// ---------------------------------------------------------------------------

func TestConfigScope_Values(t *testing.T) {
	scopes := map[ConfigScope]bool{
		ScopeLocal:      true,
		ScopeUser:       true,
		ScopeProject:    true,
		ScopeDynamic:    true,
		ScopeEnterprise: true,
		ScopeClaudeAI:   true,
		ScopeManaged:    true,
	}
	if len(scopes) != 7 {
		t.Errorf("expected 7 distinct scopes, got %d", len(scopes))
	}
}

// ---------------------------------------------------------------------------
// Transport validation — Transport constants
// ---------------------------------------------------------------------------

func TestTransport_Values(t *testing.T) {
	transports := map[Transport]bool{
		TransportStdio:        true,
		TransportSSE:          true,
		TransportSSEIDE:       true,
		TransportHTTP:         true,
		TransportWS:           true,
		TransportWSIDE:        true,
		TransportSDK:          true,
		TransportClaudeAIProxy: true,
	}
	if len(transports) != 8 {
		t.Errorf("expected 8 distinct transports, got %d", len(transports))
	}
}

// ---------------------------------------------------------------------------
// ServerInfo
// ---------------------------------------------------------------------------

func TestServerInfo_RoundTrip(t *testing.T) {
	original := ServerInfo{Name: "test-server", Version: "1.0.0"}
	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded ServerInfo
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded.Name != "test-server" {
		t.Errorf("Name = %q, want %q", decoded.Name, "test-server")
	}
	if decoded.Version != "1.0.0" {
		t.Errorf("Version = %q, want %q", decoded.Version, "1.0.0")
	}
}

// ---------------------------------------------------------------------------
// CLI state types
// ---------------------------------------------------------------------------

func TestSerializedTool_RoundTrip(t *testing.T) {
	original := SerializedTool{
		Name:             "mcp__srv__tool",
		Description:      "A tool",
		IsMcp:            true,
		OriginalToolName: "tool",
	}
	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded SerializedTool
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded.Name != original.Name {
		t.Errorf("Name = %q, want %q", decoded.Name, original.Name)
	}
	if !decoded.IsMcp {
		t.Error("IsMcp should be true")
	}
}

func TestMCPCliState_RoundTrip(t *testing.T) {
	original := MCPCliState{
		Clients: []SerializedClient{
			{Name: "srv", Type: "connected"},
		},
		Tools: []SerializedTool{
			{Name: "mcp__srv__tool", Description: "tool"},
		},
	}
	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded MCPCliState
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(decoded.Clients) != 1 || decoded.Clients[0].Name != "srv" {
		t.Errorf("Clients mismatch: %+v", decoded.Clients)
	}
	if len(decoded.Tools) != 1 || decoded.Tools[0].Name != "mcp__srv__tool" {
		t.Errorf("Tools mismatch: %+v", decoded.Tools)
	}
}

// ---------------------------------------------------------------------------
// Constants
// ---------------------------------------------------------------------------

func TestConstants(t *testing.T) {
	if MaxReconnectAttempts != 5 {
		t.Errorf("MaxReconnectAttempts = %d, want 5", MaxReconnectAttempts)
	}
	if InitialBackoffMs != 1000 {
		t.Errorf("InitialBackoffMs = %d, want 1000", InitialBackoffMs)
	}
	if MaxBackoffMs != 30000 {
		t.Errorf("MaxBackoffMs = %d, want 30000", MaxBackoffMs)
	}
	if MaxErrorsBeforeReconnect != 3 {
		t.Errorf("MaxErrorsBeforeReconnect = %d, want 3", MaxErrorsBeforeReconnect)
	}
	if DefaultConnectionTimeoutMs != 30000 {
		t.Errorf("DefaultConnectionTimeoutMs = %d, want 30000", DefaultConnectionTimeoutMs)
	}
	if MaxMCPDescriptionLength != 2048 {
		t.Errorf("MaxMCPDescriptionLength = %d, want 2048", MaxMCPDescriptionLength)
	}
}

// ===========================================================================
// Additional coverage tests for MarshalJSON, UnmarshalServerConfig
// ===========================================================================

// ---------------------------------------------------------------------------
// MarshalJSON (80% → 90%+) — ConfigScope marshaling
// ---------------------------------------------------------------------------

func TestConfigScope_MarshalJSON(t *testing.T) {
	scopes := []ConfigScope{
		ScopeLocal, ScopeUser, ScopeProject, ScopeDynamic,
		ScopeEnterprise, ScopeClaudeAI, ScopeManaged,
	}
	for _, scope := range scopes {
		t.Run(string(scope), func(t *testing.T) {
			data, err := json.Marshal(scope)
			if err != nil {
				t.Fatalf("MarshalJSON: %v", err)
			}
			if string(data) != `"`+string(scope)+`"` {
				t.Errorf("MarshalJSON(%q) = %q, want %q", scope, string(data), `"`+string(scope)+`"`)
			}
		})
	}
}

func TestConfigScope_UnmarshalJSON(t *testing.T) {
	tests := []struct {
		json    string
		want    ConfigScope
		wantErr bool
	}{
		{`"user"`, ScopeUser, false},
		{`"project"`, ScopeProject, false},
		{`"local"`, ScopeLocal, false},
		{`"dynamic"`, ScopeDynamic, false},
		{`"enterprise"`, ScopeEnterprise, false},
		{`"claudeai"`, ScopeClaudeAI, false},
		{`"managed"`, ScopeManaged, false},
		// Note: ConfigScope is just a string type, so any string unmarshals successfully
		// Validation happens at the application layer, not during unmarshaling
		{`"anyvalue"`, "anyvalue", false},
		{`123`, "", true},  // Only non-JSON values error
	}
	for _, tt := range tests {
		t.Run(tt.json, func(t *testing.T) {
			var got ConfigScope
			err := json.Unmarshal([]byte(tt.json), &got)
			if tt.wantErr {
				if err == nil {
					t.Error("want error")
				}
			} else {
				if err != nil {
					t.Fatalf("UnmarshalJSON: %v", err)
				}
				if got != tt.want {
					t.Errorf("got %q, want %q", got, tt.want)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// UnmarshalServerConfig (81% → 90%+) — Source: types.go:~290-370
// ---------------------------------------------------------------------------

func TestUnmarshalServerConfig_AllValidTypes(t *testing.T) {
	tests := []struct {
		name     string
		json     string
		wantType McpServerConfig
	}{
		{
			"stdio",
			`{"type":"stdio","command":"echo"}`,
			&StdioConfig{Command: "echo"},
		},
		{
			"sse",
			`{"type":"sse","url":"https://example.com/sse"}`,
			&SSEConfig{URL: "https://example.com/sse"},
		},
		{
			"sse-ide",
			`{"type":"sse-ide","url":"http://127.0.0.1:1234/sse","ideName":"vscode"}`,
			&SSEIDEConfig{URL: "http://127.0.0.1:1234/sse", IDEName: "vscode"},
		},
		{
			"http",
			`{"type":"http","url":"https://example.com/mcp"}`,
			&HTTPConfig{URL: "https://example.com/mcp"},
		},
		{
			"ws",
			`{"type":"ws","url":"ws://example.com/ws"}`,
			&WSConfig{URL: "ws://example.com/ws"},
		},
		{
			"ws-ide",
			`{"type":"ws-ide","url":"ws://127.0.0.1:8080/ws","ideName":"vscode"}`,
			&WSIDEConfig{URL: "ws://127.0.0.1:8080/ws", IDEName: "vscode"},
		},
		{
			"sdk",
			`{"type":"sdk","name":"test-sdk"}`,
			&SDKConfig{Name: "test-sdk"},
		},
		{
			"claudeai-proxy",
			`{"type":"claudeai-proxy","url":"https://proxy.example.com","id":"123"}`,
			&ClaudeAIProxyConfig{URL: "https://proxy.example.com", ID: "123"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var raw json.RawMessage
			if err := json.Unmarshal([]byte(tt.json), &raw); err != nil {
				t.Fatalf("unmarshal raw: %v", err)
			}
			cfg, err := UnmarshalServerConfig(raw)
			if err != nil {
				t.Fatalf("UnmarshalServerConfig: %v", err)
			}
			if cfg.GetTransport() != tt.wantType.GetTransport() {
				t.Errorf("transport = %q, want %q", cfg.GetTransport(), tt.wantType.GetTransport())
			}
		})
	}
}

func TestUnmarshalServerConfig_InvalidType(t *testing.T) {
	raw := json.RawMessage(`{"type":"invalid","field":"value"}`)
	_, err := UnmarshalServerConfig(raw)
	if err == nil {
		t.Fatal("want error for invalid type")
	}
	if !strings.Contains(err.Error(), "unknown server config type") {
		t.Errorf("error should mention unknown type, got: %v", err)
	}
}

func TestUnmarshalServerConfig_EmptyJSON(t *testing.T) {
	raw := json.RawMessage(`{}`)
	_, err := UnmarshalServerConfig(raw)
	if err == nil {
		t.Fatal("want error for empty JSON")
	}
}

func TestUnmarshalServerConfig_MissingType(t *testing.T) {
	raw := json.RawMessage(`{"url":"https://example.com"}`)
	_, err := UnmarshalServerConfig(raw)
	if err == nil {
		t.Fatal("want error for missing type field")
	}
	// stdio type can be omitted, but other types require it
}

func TestUnmarshalServerConfig_TypeNull(t *testing.T) {
	raw := json.RawMessage(`{"type":null}`)
	_, err := UnmarshalServerConfig(raw)
	if err == nil {
		t.Fatal("want error for null type")
	}
}

func TestUnmarshalServerConfig_StdioNoType(t *testing.T) {
	// stdio type can be omitted (backwards compatibility per types.go:66)
	raw := json.RawMessage(`{"command":"echo","args":["test"]}`)
	cfg, err := UnmarshalServerConfig(raw)
	if err != nil {
		t.Fatalf("UnmarshalServerConfig: %v", err)
	}
	stdio, ok := cfg.(*StdioConfig)
	if !ok {
		t.Fatalf("expected *StdioConfig, got %T", cfg)
	}
	if stdio.Command != "echo" {
		t.Errorf("command = %q, want echo", stdio.Command)
	}
}

// ---------------------------------------------------------------------------
// MarshalJSON — empty pluginSource branch
// ---------------------------------------------------------------------------

func TestScopedMcpServerConfig_MarshalJSON_EmptyPluginSource(t *testing.T) {
	s := ScopedMcpServerConfig{
		Config:       &StdioConfig{Command: "node"},
		Scope:        ScopeProject,
		PluginSource: "",
	}
	data, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	str := string(data)
	if !strings.Contains(str, `"scope":"project"`) {
		t.Errorf("missing scope in JSON: %s", str)
	}
	if strings.Contains(str, "pluginSource") {
		t.Errorf("should not contain pluginSource when empty: %s", str)
	}
}

func TestScopedMcpServerConfig_MarshalJSON_WithPluginSource(t *testing.T) {
	s := ScopedMcpServerConfig{
		Config:       &StdioConfig{Command: "node"},
		Scope:        ScopeUser,
		PluginSource: "my-plugin",
	}
	data, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	str := string(data)
	if !strings.Contains(str, `"scope":"user"`) {
		t.Errorf("missing scope in JSON: %s", str)
	}
	if !strings.Contains(str, `"pluginSource":"my-plugin"`) {
		t.Errorf("missing pluginSource in JSON: %s", str)
	}
}

// ---------------------------------------------------------------------------
// UnmarshalServerConfig — invalid SSE, invalid HTTP, invalid WS, invalid SDK
// ---------------------------------------------------------------------------

func TestUnmarshalServerConfig_InvalidSSE(t *testing.T) {
	raw := json.RawMessage(`{"type":"sse","url":123}`)
	_, err := UnmarshalServerConfig(raw)
	if err == nil {
		t.Fatal("want error for invalid SSE config")
	}
	if !strings.Contains(err.Error(), "invalid sse config") {
		t.Errorf("error = %v, want 'invalid sse config'", err)
	}
}

func TestUnmarshalServerConfig_InvalidSSEIDE(t *testing.T) {
	raw := json.RawMessage(`{"type":"sse-ide","url":123}`)
	_, err := UnmarshalServerConfig(raw)
	if err == nil {
		t.Fatal("want error for invalid SSE-IDE config")
	}
	if !strings.Contains(err.Error(), "invalid sse-ide config") {
		t.Errorf("error = %v, want 'invalid sse-ide config'", err)
	}
}

func TestUnmarshalServerConfig_InvalidWSIDE(t *testing.T) {
	raw := json.RawMessage(`{"type":"ws-ide","url":123}`)
	_, err := UnmarshalServerConfig(raw)
	if err == nil {
		t.Fatal("want error for invalid WS-IDE config")
	}
	if !strings.Contains(err.Error(), "invalid ws-ide config") {
		t.Errorf("error = %v, want 'invalid ws-ide config'", err)
	}
}

func TestUnmarshalServerConfig_InvalidHTTP(t *testing.T) {
	raw := json.RawMessage(`{"type":"http","url":123}`)
	_, err := UnmarshalServerConfig(raw)
	if err == nil {
		t.Fatal("want error for invalid HTTP config")
	}
	if !strings.Contains(err.Error(), "invalid http config") {
		t.Errorf("error = %v, want 'invalid http config'", err)
	}
}

func TestUnmarshalServerConfig_InvalidWS(t *testing.T) {
	raw := json.RawMessage(`{"type":"ws","url":123}`)
	_, err := UnmarshalServerConfig(raw)
	if err == nil {
		t.Fatal("want error for invalid WS config")
	}
	if !strings.Contains(err.Error(), "invalid ws config") {
		t.Errorf("error = %v, want 'invalid ws config'", err)
	}
}

func TestUnmarshalServerConfig_InvalidSDK(t *testing.T) {
	raw := json.RawMessage(`{"type":"sdk","name":123}`)
	_, err := UnmarshalServerConfig(raw)
	if err == nil {
		t.Fatal("want error for invalid SDK config")
	}
	if !strings.Contains(err.Error(), "invalid sdk config") {
		t.Errorf("error = %v, want 'invalid sdk config'", err)
	}
}

func TestUnmarshalServerConfig_InvalidClaudeAIProxy(t *testing.T) {
	raw := json.RawMessage(`{"type":"claudeai-proxy","url":123}`)
	_, err := UnmarshalServerConfig(raw)
	if err == nil {
		t.Fatal("want error for invalid ClaudeAI proxy config")
	}
	if !strings.Contains(err.Error(), "invalid claudeai-proxy config") {
		t.Errorf("error = %v, want 'invalid claudeai-proxy config'", err)
	}
}

func TestUnmarshalServerConfig_InvalidStdio(t *testing.T) {
	raw := json.RawMessage(`{"type":"stdio","command":123}`)
	_, err := UnmarshalServerConfig(raw)
	if err == nil {
		t.Fatal("want error for invalid stdio config")
	}
	if !strings.Contains(err.Error(), "invalid stdio config") {
		t.Errorf("error = %v, want 'invalid stdio config'", err)
	}
}

func TestUnmarshalServerConfig_InvalidJSONInType(t *testing.T) {
	// Valid JSON but type field is an array
	raw := json.RawMessage(`{"type":[1,2,3]}`)
	_, err := UnmarshalServerConfig(raw)
	if err == nil {
		t.Fatal("want error for non-string type")
	}
}

// ---------------------------------------------------------------------------
// OAuthConfig — round trip
// ---------------------------------------------------------------------------

func TestOAuthConfig_RoundTrip(t *testing.T) {
	original := OAuthConfig{
		ClientID:              "client-123",
		CallbackPort:          9090,
		AuthServerMetadataURL: "https://auth.example.com/.well-known",
		XAA:                   true,
	}
	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded OAuthConfig
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded.ClientID != "client-123" {
		t.Errorf("ClientID = %q, want client-123", decoded.ClientID)
	}
	if decoded.CallbackPort != 9090 {
		t.Errorf("CallbackPort = %d, want 9090", decoded.CallbackPort)
	}
	if !decoded.XAA {
		t.Error("XAA should be true")
	}
}

// ---------------------------------------------------------------------------
// MarshalJSON — json.Unmarshal error path in ScopedMcpServerConfig
// ---------------------------------------------------------------------------

// typesUnmarshalFailConfig marshals to a non-object JSON value,
// causing the json.Unmarshal into map[string]json.RawMessage to fail.
type typesUnmarshalFailConfig struct{}

func (typesUnmarshalFailConfig) GetTransport() Transport { return "custom" }
func (typesUnmarshalFailConfig) MarshalJSON() ([]byte, error) {
	return []byte(`[1,2,3]`), nil
}

func TestScopedMcpServerConfig_MarshalJSON_UnmarshalError(t *testing.T) {
	s := ScopedMcpServerConfig{
		Config: typesUnmarshalFailConfig{},
		Scope:  ScopeUser,
	}
	_, err := json.Marshal(s)
	if err == nil {
		t.Fatal("want error for config that marshals to non-object JSON")
	}
}

// typesMarshalFailConfig fails to marshal entirely.
type typesMarshalFailConfig struct{}

func (typesMarshalFailConfig) GetTransport() Transport { return "custom" }
func (typesMarshalFailConfig) MarshalJSON() ([]byte, error) {
	return nil, fmt.Errorf("forced marshal failure")
}

func TestScopedMcpServerConfig_MarshalJSON_MarshalError(t *testing.T) {
	s := ScopedMcpServerConfig{
		Config: typesMarshalFailConfig{},
		Scope:  ScopeUser,
	}
	_, err := json.Marshal(s)
	if err == nil {
		t.Fatal("want error for config that fails to marshal")
	}
	if !strings.Contains(err.Error(), "forced marshal failure") {
		t.Errorf("error = %v, want 'forced marshal failure'", err)
	}
}
