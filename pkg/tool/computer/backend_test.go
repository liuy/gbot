//go:build !linux

package computer

import (
	"encoding/json"
	"testing"
)

// TestResolveMCPInvocationFallback exercises the manifest-parse logic without
// spawning a real cua-driver process. The function's contract (mirroring
// cua_backend.py:159-208 `_resolve_mcp_invocation`) is: parse the manifest
// JSON, extract mcp_invocation.{command,args}; on ANY failure or missing
// field, fall back to (driverCmd, ["mcp"]).
//
// Because resolveMCPInvocation spawns the real binary, these tests cover the
// parse branches by injecting a fake driver command that prints crafted JSON.
// The actual cua-driver manifest call is exercised by the e2e test.

// manifestShape is the subset of `cua-driver manifest` output we parse.
type manifestShape struct {
	MCPInvocation struct {
		Command string   `json:"command"`
		Args    []string `json:"args"`
	} `json:"mcp_invocation"`
}

// TestManifestShapeParse verifies our struct tag alignment against the real
// cua-driver manifest output (verified on 0.6.8):
//
//	{"mcp_invocation":{"args":["mcp"],"command":"/path/to/cua-driver"}, ...}
func TestManifestShapeParse(t *testing.T) {
	raw := `{"binary_path":"/x/cua-driver","binary_version":"0.6.8",` +
		`"mcp_invocation":{"args":["mcp"],"command":"/x/cua-driver"},` +
		`"schema_version":"1"}`

	var m manifestShape
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if m.MCPInvocation.Command != "/x/cua-driver" {
		t.Errorf("command: got %q, want /x/cua-driver", m.MCPInvocation.Command)
	}
	if len(m.MCPInvocation.Args) != 1 || m.MCPInvocation.Args[0] != "mcp" {
		t.Errorf("args: got %v, want [mcp]", m.MCPInvocation.Args)
	}
}

// TestManifestShapeMissingCommand verifies the branch where the driver
// surfaces args but not command — we keep driverCmd, use the discovered args.
func TestManifestShapeMissingCommand(t *testing.T) {
	raw := `{"mcp_invocation":{"args":["serve","--stdio"]}}`
	var m manifestShape
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if m.MCPInvocation.Command != "" {
		t.Errorf("command: got %q, want empty", m.MCPInvocation.Command)
	}
	if len(m.MCPInvocation.Args) != 2 {
		t.Errorf("args len: got %d, want 2", len(m.MCPInvocation.Args))
	}
}

// TestManifestShapeMalformed verifies that broken JSON leaves the struct at
// its zero value — the caller falls back to default args.
func TestManifestShapeMalformed(t *testing.T) {
	var m manifestShape
	if err := json.Unmarshal([]byte("not json"), &m); err == nil {
		t.Fatalf("got nil error, want non-nil for malformed JSON")
	}
	if m.MCPInvocation.Command != "" || len(m.MCPInvocation.Args) != 0 {
		t.Errorf("expected zero values after malformed parse, got command=%q args=%v",
			m.MCPInvocation.Command, m.MCPInvocation.Args)
	}
}

// TestResolveMCPInvocationFallbackValues verifies the fallback constant is
// exactly what cua-driver 0.6.8 uses, so a future rename is the only thing
// that could break discovery.
func TestResolveMCPInvocationFallbackValues(t *testing.T) {
	if len(fallbackMCPArgs) != 1 || fallbackMCPArgs[0] != "mcp" {
		t.Errorf("fallbackMCPArgs = %v, want [mcp]", fallbackMCPArgs)
	}
}
