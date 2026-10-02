package mcp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
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

// Regression: AddMcpConfig on a project dir whose .mcp.json has validation
// errors must refuse to write — the old behavior read an empty view and
// rewrote the file, wiping every existing server.
func TestAddMcpConfigRefusesBrokenFile(t *testing.T) {
	dir := t.TempDir()
	broken := `{"mcpServers":{"broken":{}}}` // stdio without command = validation error
	path := filepath.Join(dir, ".mcp.json")
	if err := os.WriteFile(path, []byte(broken), 0o644); err != nil {
		t.Fatalf("write broken .mcp.json: %v", err)
	}

	err := AddMcpConfig("new-srv", &StdioConfig{Command: "foo"}, ScopeProject, dir, nil, nil, nil)
	if err == nil {
		t.Fatal("AddMcpConfig succeeded on a broken .mcp.json — existing servers would be wiped")
	}
	if want := "invalid server configs"; !strings.Contains(err.Error(), want) {
		t.Fatalf("error %q does not mention %q", err, want)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(after) != broken {
		t.Fatalf(".mcp.json was modified:\n%s", after)
	}
}

// Missing-env-var errors keep their servers, so they must not block Add.
func TestMissingEnvVarDoesNotBlockAdd(t *testing.T) {
	dir := t.TempDir()
	mcpJson := `{"mcpServers":{"gh":{"type":"http","url":"https://api.github.com/mcp","headers":{"Authorization":"Bearer ${UNSET_VAR}"}}}}`
	path := filepath.Join(dir, ".mcp.json")
	if err := os.WriteFile(path, []byte(mcpJson), 0o644); err != nil {
		t.Fatalf("write .mcp.json: %v", err)
	}

	if err := AddMcpConfig("extra", &StdioConfig{Command: "foo"}, ScopeProject, dir, nil, nil, nil); err != nil {
		t.Fatalf("missing env var blocked Add: %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	for _, want := range []string{`"gh"`, `"extra"`, "UNSET_VAR"} {
		if !strings.Contains(string(after), want) {
			t.Fatalf(".mcp.json missing %s after add:\n%s", want, after)
		}
	}
}

// Mirror of the Add guard: Remove must also refuse on a broken file.
func TestRemoveMcpConfigRefusesBrokenFile(t *testing.T) {
	dir := t.TempDir()
	broken := `{"mcpServers":{"broken":{}}}`
	path := filepath.Join(dir, ".mcp.json")
	if err := os.WriteFile(path, []byte(broken), 0o644); err != nil {
		t.Fatalf("write broken .mcp.json: %v", err)
	}

	err := RemoveMcpConfig("broken", ScopeProject, dir, nil)
	if err == nil {
		t.Fatal("RemoveMcpConfig succeeded on a broken .mcp.json")
	}
	if want := "invalid server configs"; !strings.Contains(err.Error(), want) {
		t.Fatalf("error %q does not mention %q", err, want)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(after) != broken {
		t.Fatalf(".mcp.json was modified:\n%s", after)
	}
}

// A file with one valid and one schema-invalid server must surface the
// server-level validation error instead of silently dropping the server.
func TestGetProjectMcpConfigsReturnsServerLevelErrors(t *testing.T) {
	dir := t.TempDir()
	mcpJson := `{"mcpServers":{"good":{"command":"echo","args":["hi"]},"bad":{}}}`
	path := filepath.Join(dir, ".mcp.json")
	if err := os.WriteFile(path, []byte(mcpJson), 0o644); err != nil {
		t.Fatalf("write .mcp.json: %v", err)
	}
	servers, errs := GetProjectMcpConfigsFromCwd(dir)
	if len(servers) != 1 {
		t.Fatalf("servers = %d, want 1 (good only)", len(servers))
	}
	if _, ok := servers["good"]; !ok {
		t.Fatalf("good server missing: %+v", servers)
	}
	if len(errs) != 1 {
		t.Fatalf("errs = %d, want 1 server-level error", len(errs))
	}
	if errs[0].Path != "mcpServers.bad" {
		t.Fatalf("errs[0].Path = %q, want mcpServers.bad", errs[0].Path)
	}
}
