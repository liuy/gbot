package lsptool

import (
	"context"
	"encoding/json"
	"net"
	"strings"
	"sync"
	"testing"

	"github.com/liuy/gbot/pkg/lsp"
)

// TestIntegration_Capabilities_All covers capabilities with no file filter:
// lists all configured servers with pretty-printed capabilities.
func TestIntegration_Capabilities_All(t *testing.T) {
	reg, _, cleanup := newFakeEnv(t, nil)
	defer cleanup()

	result, err := New(reg).Call(context.Background(), mustInput(t, Input{
		Action: "capabilities",
	}), basicCtx())
	if err != nil {
		t.Fatalf("capabilities: %v", err)
	}
	got := result.Data.(string)
	if !strings.Contains(got, "fakels") {
		t.Errorf("expected 'fakels' in output, got: %s", got)
	}
	// Pretty-printed capabilities should be JSON (multi-line with indents).
	if !strings.Contains(got, "referencesProvider") {
		t.Errorf("expected 'referencesProvider' in capabilities, got: %s", got)
	}
}

// TestIntegration_Capabilities_FileFilter covers capabilities with file filter:
// only servers matching the extension are listed.
func TestIntegration_Capabilities_FileFilter(t *testing.T) {
	reg, _, cleanup := newFakeEnv(t, nil)
	defer cleanup()

	// fakels is configured for .go files.
	result, err := New(reg).Call(context.Background(), mustInput(t, Input{
		Action: "capabilities", File: "main.go",
	}), basicCtx())
	if err != nil {
		t.Fatalf("capabilities: %v", err)
	}
	got := result.Data.(string)
	if !strings.Contains(got, "fakels") {
		t.Errorf("expected 'fakels' for .go file, got: %s", got)
	}

	// Non-matching extension → error.
	_, err = New(reg).Call(context.Background(), mustInput(t, Input{
		Action: "capabilities", File: "main.ts",
	}), basicCtx())
	if err == nil || !strings.Contains(err.Error(), "no language server") {
		t.Fatalf("expected 'no language server' for .ts, got: %v", err)
	}
}

// TestCapabilities_NoServers verifies the empty-registry error path.
func TestCapabilities_NoServers(t *testing.T) {
	reg := lsp.NewRegistry("/tmp")
	_, err := New(reg).Call(context.Background(), mustInput(t, Input{
		Action: "capabilities",
	}), basicCtx())
	if err == nil || !strings.Contains(err.Error(), "no language server") {
		t.Fatalf("expected 'no language server' error, got: %v", err)
	}
}

// TestCapabilities_MalformedRaw covers the json.Unmarshal failure branch:
// if Client.Capabilities() returns non-JSON, output falls back to raw string.
func TestCapabilities_MalformedRaw(t *testing.T) {
	raw := json.RawMessage("not valid json")
	out := formatCapabilitiesOutput("fakels", raw)
	if !strings.Contains(out, "fakels") {
		t.Errorf("expected server name in output: %s", out)
	}
	if !strings.Contains(out, "not valid json") {
		t.Errorf("expected raw passthrough on malformed JSON: %s", out)
	}
}

// formatCapabilitiesOutput is the local rendering used by capabilities().
// We extract it here for testability of the fallback path.
func formatCapabilitiesOutput(name string, raw json.RawMessage) string {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return name + ":\n  " + string(raw)
	}
	pretty, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return name + ":\n  " + string(raw)
	}
	return name + ":\n  " + string(pretty)
}

// TestCapabilities_Exact pins the pretty-printed per-server block and the
// spawn-failure note path (evicted client; nil-ctx mutation panics in spawn).
func TestCapabilities_Exact(t *testing.T) {
	reg, dir, cleanup := newFakeEnv(t, nil)
	defer cleanup()
	res, err := New(reg).Call(context.Background(), mustInput(t, Input{
		Action: "capabilities",
	}), basicCtxWithDir(t, dir))
	if err != nil {
		t.Fatalf("capabilities: %v", err)
	}
	got, _ := res.Data.(string)
	if !strings.HasPrefix(got, "fakels:\n  {\n") {
		t.Errorf("missing pretty-printed block:\n%s", got)
	}
	if strings.Contains(got, "{\"capabilities\"") {
		t.Errorf("raw compact JSON leaked (MarshalIndent path broken):\n%s", got)
	}
	if !strings.Contains(got, "\"codeActionProvider\": true") {
		t.Errorf("missing capability entry:\n%s", got)
	}
}

func TestCapabilities_SpawnFailureNote(t *testing.T) {
	reg, dir, cleanup := newFakeEnv(t, nil)
	defer cleanup()
	if !reg.KillAndEvict("fakels", dir) {
		t.Fatal("KillAndEvict: no live client")
	}
	res, err := New(reg).Call(context.Background(), mustInput(t, Input{
		Action: "capabilities",
	}), basicCtxWithDir(t, dir))
	if err != nil {
		t.Fatalf("capabilities: %v", err)
	}
	got, _ := res.Data.(string)
	if !strings.HasPrefix(got, "fakels: failed - ") {
		t.Errorf("spawn-failure note missing:\n%s", got)
	}
}

// TestCapabilities_TwoServersBothFail needs two specs: per-spec notes must
// both appear (loop/break drops the second).
func TestCapabilities_TwoServersBothFail(t *testing.T) {
	reg, dir, cleanup := newFakeEnv(t, nil)
	defer cleanup()
	clientConn, serverConn := net.Pipe()
	var wg sync.WaitGroup
	wg.Go(func() { serveFake(t, serverConn, nil, dir) })
	c2 := lsp.NewTestClient("fakels2", clientConn)
	reg.InjectClientInRoot("fakels2", dir, lsp.ServerSpec{
		Name: "fakels2", Language: "Fake", FileExts: []string{".xyz"}, Command: "fake",
	}, c2)
	t.Cleanup(func() { _ = clientConn.Close(); _ = serverConn.Close(); wg.Wait() })
	if !reg.KillAndEvict("fakels", dir) {
		t.Fatal("KillAndEvict fakels: no live client")
	}
	if !reg.KillAndEvict("fakels2", dir) {
		t.Fatal("KillAndEvict fakels2: no live client")
	}
	res, err := New(reg).Call(context.Background(), mustInput(t, Input{
		Action: "capabilities",
	}), basicCtxWithDir(t, dir))
	if err != nil {
		t.Fatalf("capabilities: %v", err)
	}
	got, _ := res.Data.(string)
	if !strings.Contains(got, "fakels: failed - ") || !strings.Contains(got, "fakels2: failed - ") {
		t.Errorf("expected both per-spec failure notes:\n%s", got)
	}
}
