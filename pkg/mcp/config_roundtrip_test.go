package mcp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// Regression: ExpandConfigEnv re-marshals each server config after validation.
// Config structs without a "type" field lost their discriminator in that
// round-trip, so non-stdio servers (http/sse/ws) were re-parsed as stdio and
// rejected with "stdio command cannot be empty".
func TestParseMcpConfigRoundTripsNonStdioTypes(t *testing.T) {
	dir := t.TempDir()
	mcpJson := `{
	  "mcpServers": {
	    "pywinauto": {"type": "http", "url": "http://127.0.0.1:10791/mcp", "headersHelper": "helper-cmd", "oauth": {"clientId": "abc"}},
	    "legacy-sse": {"type": "sse", "url": "http://127.0.0.1:2222/sse"},
	    "local-tools": {"command": "uvx", "args": ["some-server"]}
	  }
	}`
	if err := os.WriteFile(filepath.Join(dir, ".mcp.json"), []byte(mcpJson), 0o644); err != nil {
		t.Fatalf("write .mcp.json: %v", err)
	}

	config, errs := ParseMcpConfigFromFilePath(filepath.Join(dir, ".mcp.json"), true, ScopeProject)
	if config == nil {
		t.Fatalf("config nil, errors: %+v", errs)
	}
	if len(errs) != 0 {
		t.Fatalf("unexpected validation errors: %+v", errs)
	}
	servers, errs := loadMcpServersFromConfig(config)
	if len(errs) != 0 {
		t.Fatalf("unexpected load errors: %+v", errs)
	}

	httpCfg, ok := servers["pywinauto"].(*HTTPConfig)
	if !ok {
		t.Fatalf("pywinauto = %T, want *HTTPConfig (type discriminator lost in round-trip)", servers["pywinauto"])
	}
	if httpCfg.Type != TransportHTTP || httpCfg.URL != "http://127.0.0.1:10791/mcp" {
		t.Fatalf("pywinauto = %+v", httpCfg)
	}
	if httpCfg.HeadersHelper != "helper-cmd" {
		t.Fatalf("pywinauto HeadersHelper lost in expansion: %+v", httpCfg)
	}
	if httpCfg.OAuth == nil || httpCfg.OAuth.ClientID != "abc" {
		t.Fatalf("pywinauto OAuth lost in expansion: %+v", httpCfg)
	}

	sseCfg, ok := servers["legacy-sse"].(*SSEConfig)
	if !ok {
		t.Fatalf("legacy-sse = %T, want *SSEConfig", servers["legacy-sse"])
	}
	if sseCfg.Type != TransportSSE || sseCfg.URL != "http://127.0.0.1:2222/sse" {
		t.Fatalf("legacy-sse = %+v", sseCfg)
	}

	stdioCfg, ok := servers["local-tools"].(*StdioConfig)
	if !ok {
		t.Fatalf("local-tools = %T, want *StdioConfig", servers["local-tools"])
	}
	if len(stdioCfg.Args) != 1 || stdioCfg.Args[0] != "some-server" {
		t.Fatalf("local-tools = %+v", stdioCfg)
	}

	// The re-marshaled form must keep the discriminator for every non-stdio
	// type; stdio may lose it (it is the parse default) but must still
	// re-parse as StdioConfig.
	for name, raw := range config.McpServers {
		var probe struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(raw, &probe); err != nil {
			t.Fatalf("%s: re-marshaled config is not valid JSON: %v", name, err)
		}
		if probe.Type == "" {
			if _, err := UnmarshalServerConfig(raw); err != nil {
				t.Fatalf("%s: typeless re-marshal no longer parses as stdio: %v", name, err)
			}
		}
	}
}
