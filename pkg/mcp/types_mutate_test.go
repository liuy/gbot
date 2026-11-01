package mcp

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// ScopedMcpServerConfig.MarshalJSON — the unmarshal-failure return.
// ---------------------------------------------------------------------------

func TestScopedMcpServerConfig_MarshalJSON_UnmarshalErrorNamesCause(t *testing.T) {
	s := ScopedMcpServerConfig{Config: typesUnmarshalFailConfig{}, Scope: ScopeUser}
	got, err := json.Marshal(s)
	if err == nil {
		t.Fatalf("want error for config that marshals to a non-object, got %s", got)
	}
	if !strings.Contains(err.Error(), "cannot unmarshal") {
		t.Errorf("error = %v, want it to carry the json unmarshal failure", err)
	}
	if _, ok := errors.AsType[*json.UnmarshalTypeError](err); !ok {
		t.Errorf("error = %v, want it to wrap a *json.UnmarshalTypeError", err)
	}
}

// ---------------------------------------------------------------------------
// Connection constants — each literal is a protocol contract.
// ---------------------------------------------------------------------------

func TestDefaultToolCallTimeoutMs_Value(t *testing.T) {
	if DefaultToolCallTimeoutMs != 100000000 {
		t.Errorf("DefaultToolCallTimeoutMs = %d, want 100000000", DefaultToolCallTimeoutMs)
	}
}

// ---------------------------------------------------------------------------
// UnmarshalServerConfig — every wrapper must keep the JSON cause reachable.
//
// The messages are byte-identical whether the verb is %w or %v, so only
// errors.As distinguishes a wrapped cause from a formatted one.
// ---------------------------------------------------------------------------

func TestUnmarshalServerConfig_ErrorsWrapJSONCause(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
		want string
	}{
		{"type-detect", `{"type":123}`, "invalid server config"},
		{"stdio", `{"command":"x","args":123}`, "invalid stdio config"},
		{"sse", `{"type":"sse","url":123}`, "invalid sse config"},
		{"sse-ide", `{"type":"sse-ide","url":123}`, "invalid sse-ide config"},
		{"ws-ide", `{"type":"ws-ide","url":123}`, "invalid ws-ide config"},
		{"http", `{"type":"http","url":123}`, "invalid http config"},
		{"ws", `{"type":"ws","url":123}`, "invalid ws config"},
		{"sdk", `{"type":"sdk","name":123}`, "invalid sdk config"},
		{"claudeai-proxy", `{"type":"claudeai-proxy","url":123}`, "invalid claudeai-proxy config"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := UnmarshalServerConfig(json.RawMessage(tc.raw))
			if err == nil {
				t.Fatalf("want error for %s, got config %+v", tc.raw, cfg)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %v, want it to contain %q", err, tc.want)
			}
			if _, ok := errors.AsType[*json.UnmarshalTypeError](err); !ok {
				t.Errorf("error = %v, want it to wrap the *json.UnmarshalTypeError from decoding", err)
			}
		})
	}
}

func TestUnmarshalServerConfig_SyntaxErrorIsWrapped(t *testing.T) {
	_, err := UnmarshalServerConfig(json.RawMessage(`{"type":`))
	if err == nil {
		t.Fatal("want error for truncated JSON")
	}
	if _, ok := errors.AsType[*json.SyntaxError](err); !ok {
		t.Errorf("error = %v, want it to wrap a *json.SyntaxError", err)
	}
}

// ---------------------------------------------------------------------------
// LoadMcpJsonConfig — both wrappers (.mcp.json level and per-server level).
// ---------------------------------------------------------------------------

func TestLoadMcpJsonConfig_TopLevelErrorWrapsCause(t *testing.T) {
	_, err := LoadMcpJsonConfig([]byte(`{"mcpServers":123}`))
	if err == nil {
		t.Fatal("want error for non-object mcpServers")
	}
	if !strings.Contains(err.Error(), "invalid .mcp.json") {
		t.Errorf("error = %v, want it to mention invalid .mcp.json", err)
	}
	if _, ok := errors.AsType[*json.UnmarshalTypeError](err); !ok {
		t.Errorf("error = %v, want it to wrap a *json.UnmarshalTypeError", err)
	}
}

func TestLoadMcpJsonConfig_ServerErrorWrapsCause(t *testing.T) {
	_, err := LoadMcpJsonConfig([]byte(`{"mcpServers":{"s1":{"type":"sse","url":123}}}`))
	if err == nil {
		t.Fatal("want error for a server config with a mistyped field")
	}
	if !strings.Contains(err.Error(), `server "s1"`) {
		t.Errorf("error = %v, want it to name the server", err)
	}
	if _, ok := errors.AsType[*json.UnmarshalTypeError](err); !ok {
		t.Errorf("error = %v, want it to unwrap through the per-server wrapper to the JSON cause", err)
	}
}
