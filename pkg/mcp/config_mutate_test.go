package mcp

// Mutation kill tests for config.go — each test pins a mutant that escaped
// the census run (see /home/yliu/mutate-config/escapes-config.txt). Grouped
// by the function under test; comment above each test names the mutants
// (line:mutator) it is responsible for killing.

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
)

// ---------------------------------------------------------------------------
// UnmarshalMcpPolicyEntry
// ---------------------------------------------------------------------------

// 87:numbers/incrementer — len(ServerCommand) > 1 must still admit single-
// element command entries (the 0→1 mutant sends them to the default error).
func TestUnmarshalMcpPolicyEntry_SingleCommandElement(t *testing.T) {
	entry, err := UnmarshalMcpPolicyEntry(json.RawMessage(`{"serverCommand":["only"]}`))
	if err != nil {
		t.Fatalf("single-element serverCommand must parse: %v", err)
	}
	ce, ok := entry.(*McpCommandEntry)
	if !ok {
		t.Fatalf("entry type = %T, want *McpCommandEntry", entry)
	}
	if !CommandArraysMatch(ce.ServerCommand, []string{"only"}) {
		t.Errorf("ServerCommand = %v, want [only]", ce.ServerCommand)
	}
}

// 76:expression/error-guard, 77:branch/if, 77:expression/errorf-wrap — a
// probe-level unmarshal failure must surface the "invalid policy entry: <cause>"
// error (wrapping the json error), not the generic "must have ..." default
// error, and the chain must stay unwrappable for errors.As.
func TestUnmarshalMcpPolicyEntry_ProbeErrorWraps(t *testing.T) {
	_, err := UnmarshalMcpPolicyEntry(json.RawMessage(`not valid json`))
	if err == nil {
		t.Fatal("want error for syntactically invalid JSON")
	}
	if strings.Contains(err.Error(), "must have") {
		t.Errorf("err = %v, want probe-level error, not default-branch error", err)
	}
	if _, ok := errors.AsType[*json.SyntaxError](err); !ok {
		t.Errorf("err = %v, want wrapped *json.SyntaxError (verifies %%w wrapping)", err)
	}
}

// Branch dispatch collapses onto the probe struct's shape — these edges pin
// that shape so widening probe types (e.g. *string → string) trips something.
func TestUnmarshalMcpPolicyEntry_ProbeShapeEdges(t *testing.T) {
	if _, err := UnmarshalMcpPolicyEntry(json.RawMessage(`{"serverName":null}`)); err == nil {
		t.Error("explicit null serverName: want default-branch error")
	}
	if _, err := UnmarshalMcpPolicyEntry(json.RawMessage(`{"serverCommand":[]}`)); err == nil {
		t.Error("empty serverCommand: want default-branch error")
	}
	entry, err := UnmarshalMcpPolicyEntry(json.RawMessage(`{"serverName":""}`))
	if err != nil {
		t.Fatalf("empty-string serverName: %v", err)
	}
	ne, ok := entry.(*McpNameEntry)
	if !ok || ne.ServerName != "" {
		t.Errorf("empty-string serverName = %T %+v, want *McpNameEntry with ServerName \"\"", entry, entry)
	}
	entry, err = UnmarshalMcpPolicyEntry(json.RawMessage(`{"serverName":"first","serverName":"last"}`))
	if err != nil {
		t.Fatalf("duplicate keys: %v", err)
	}
	if ne, ok := entry.(*McpNameEntry); !ok || ne.ServerName != "last" {
		t.Errorf("duplicate keys = %T %+v, want last-wins *McpNameEntry{\"last\"}", entry, entry)
	}
}

// ---------------------------------------------------------------------------
// UnwrapCcrProxyUrl
// ---------------------------------------------------------------------------

// 240:conditional/bool-literal, 248:branch/if — a non-CCR URL must be
// returned unchanged even when it carries an mcp_url query param.
func TestUnwrapCcrProxyUrl_NonProxyWithMcpUrlParam(t *testing.T) {
	url := "http://localhost:3000/sse?mcp_url=http://evil.example.com/x"
	if got := UnwrapCcrProxyUrl(url); got != url {
		t.Errorf("got %q, want original URL (non-proxy must not unwrap)", got)
	}
}

// 254:expression/remove — an empty mcp_url value must be skipped and the
// search continues to the next mcp_url param.
func TestUnwrapCcrProxyUrl_EmptyMcpUrlSkipped(t *testing.T) {
	url := "https://example.com/v2/session_ingress/shttp/mcp/123?mcp_url=&mcp_url=https://real.com/api"
	if got := UnwrapCcrProxyUrl(url); got != "https://real.com/api" {
		t.Errorf("got %q, want %q", got, "https://real.com/api")
	}
}

// 254:expression/logical — non-mcp_url params must never be mistaken for
// the mcp_url value (ok||… mutant returns the preceding pair verbatim).
func TestUnwrapCcrProxyUrl_OtherParamsIgnored(t *testing.T) {
	url := "https://example.com/v2/session_ingress/shttp/mcp/123?foo=bar&mcp_url=https://real.com/api"
	if got := UnwrapCcrProxyUrl(url); got != "https://real.com/api" {
		t.Errorf("got %q, want %q", got, "https://real.com/api")
	}
}

// ---------------------------------------------------------------------------
// DedupPluginMcpServers
// ---------------------------------------------------------------------------

// 334:branch/if, 335:loop/break — every SDK-signature plugin server is kept;
// clearing the include body lets the second SDK fall into the seen-signature
// suppress path, and continue→break drops it entirely.
func TestDedupPluginMcpServers_TwoSDKPluginsBothKept(t *testing.T) {
	plugin := map[string]ScopedMcpServerConfig{
		"sdk1": {Config: &SDKConfig{}, Scope: ScopeDynamic},
		"sdk2": {Config: &SDKConfig{}, Scope: ScopeDynamic},
	}
	servers, suppressed := DedupPluginMcpServers(plugin, nil)
	if len(suppressed) != 0 {
		t.Errorf("suppressed = %v, want none", suppressed)
	}
	if len(servers) != 2 {
		t.Errorf("servers = %d entries, want both SDK plugins", len(servers))
	}
}

// 340:composite/field-clear, 343:loop/break — two plugin servers duplicating
// the same manual server are both suppressed with their names recorded;
// Name field-clearing blanks the record and continue→break stops after one.
func TestDedupPluginMcpServers_TwoManualDupsDetails(t *testing.T) {
	manual := map[string]ScopedMcpServerConfig{
		"m": {Config: &StdioConfig{Command: "node"}, Scope: ScopeUser},
	}
	plugin := map[string]ScopedMcpServerConfig{
		"p1": {Config: &StdioConfig{Command: "node"}, Scope: ScopeDynamic},
		"p2": {Config: &StdioConfig{Command: "node"}, Scope: ScopeDynamic},
	}
	servers, suppressed := DedupPluginMcpServers(plugin, manual)
	if len(servers) != 0 {
		t.Errorf("servers = %d, want 0", len(servers))
	}
	if len(suppressed) != 2 {
		t.Fatalf("suppressed = %d entries, want both duplicates", len(suppressed))
	}
	for _, s := range suppressed {
		if s.Name == "" {
			t.Errorf("suppressed entry has empty Name: %+v", s)
		}
		if s.DuplicateOf != "m" {
			t.Errorf("DuplicateOf = %q, want %q", s.DuplicateOf, "m")
		}
	}
}

// 348:composite/field-clear, 351:loop/break — three plugins sharing one
// signature yield one kept + two suppressed, each suppression recording
// both names; continue→break after the first suppression loses the third.
func TestDedupPluginMcpServers_ThreeSameSignaturePlugins(t *testing.T) {
	plugin := map[string]ScopedMcpServerConfig{
		"q1": {Config: &SSEConfig{URL: "http://same.example.com"}, Scope: ScopeDynamic},
		"q2": {Config: &SSEConfig{URL: "http://same.example.com"}, Scope: ScopeDynamic},
		"q3": {Config: &SSEConfig{URL: "http://same.example.com"}, Scope: ScopeDynamic},
	}
	servers, suppressed := DedupPluginMcpServers(plugin, nil)
	if len(servers) != 1 {
		t.Errorf("servers = %d, want 1 (first-loaded wins)", len(servers))
	}
	if len(suppressed) != 2 {
		t.Fatalf("suppressed = %d entries, want both later duplicates", len(suppressed))
	}
	for _, s := range suppressed {
		if s.Name == "" {
			t.Errorf("suppressed entry has empty Name: %+v", s)
		}
		if s.DuplicateOf == "" {
			t.Errorf("suppressed entry has empty DuplicateOf: %+v", s)
		}
	}
}

// ---------------------------------------------------------------------------
// DedupClaudeAiMcpServers
// ---------------------------------------------------------------------------

// 388:composite/field-clear, 391:loop/break — every claude.ai connector
// duplicating an enabled manual server is suppressed with both names recorded;
// continue→break after the first loses the second, clearing Name empties the
// record.
func TestDedupClaudeAiMcpServers_TwoDupsBothSuppressed(t *testing.T) {
	manual := map[string]ScopedMcpServerConfig{
		"m": {Config: &SSEConfig{URL: "http://x.example.com"}, Scope: ScopeUser},
	}
	claude := map[string]ScopedMcpServerConfig{
		"c1": {Config: &SSEConfig{URL: "http://x.example.com"}, Scope: ScopeClaudeAI},
		"c2": {Config: &SSEConfig{URL: "http://x.example.com"}, Scope: ScopeClaudeAI},
	}
	servers, suppressed := DedupClaudeAiMcpServers(claude, manual, func(string) bool { return false })
	if len(servers) != 0 {
		t.Errorf("servers = %d, want 0", len(servers))
	}
	if len(suppressed) != 2 {
		t.Fatalf("suppressed = %d entries, want both connectors", len(suppressed))
	}
	names := make([]string, 0, len(suppressed))
	for _, s := range suppressed {
		names = append(names, s.Name)
		if s.DuplicateOf != "m" {
			t.Errorf("DuplicateOf = %q, want %q", s.DuplicateOf, "m")
		}
	}
	slices.Sort(names)
	if !slices.Equal(names, []string{"c1", "c2"}) {
		t.Errorf("suppressed names = %v, want [c1 c2]", names)
	}
}

// 373:loop/break — the manual scan must consult the disabled checker once per
// manual entry. A break stops at the first disabled entry, which hides the
// enabled dedup targets sitting later in the (randomly ordered) map; the call
// count makes that order-independent.
func TestDedupClaudeAiMcpServers_DisabledCheckerScansEveryManual(t *testing.T) {
	manual := map[string]ScopedMcpServerConfig{
		"d1": {Config: &SSEConfig{URL: "http://d1.example.com"}, Scope: ScopeUser},
		"d2": {Config: &SSEConfig{URL: "http://d2.example.com"}, Scope: ScopeUser},
		"d3": {Config: &SSEConfig{URL: "http://d3.example.com"}, Scope: ScopeUser},
	}
	calls := 0
	checker := func(string) bool {
		calls++
		return true
	}
	claude := map[string]ScopedMcpServerConfig{
		"c1": {Config: &SSEConfig{URL: "http://c1.example.com"}, Scope: ScopeClaudeAI},
	}
	servers, suppressed := DedupClaudeAiMcpServers(claude, manual, checker)
	if calls != 3 {
		t.Errorf("disabledChecker calls = %d, want one per manual entry (3)", calls)
	}
	if len(suppressed) != 0 {
		t.Errorf("suppressed = %v, want none (every manual entry is disabled)", suppressed)
	}
	if len(servers) != 1 {
		t.Errorf("servers = %d, want the single connector kept", len(servers))
	}
}

// ---------------------------------------------------------------------------
// ExpandConfigEnv
// ---------------------------------------------------------------------------

// 442:composite/field-clear, 444:composite/field-clear — SSE pass-through
// fields (OAuth, HeadersHelper) must survive expansion.
func TestExpandConfigEnv_SSEPassThrough(t *testing.T) {
	t.Setenv("CFG_MUT_SSE_TOK", "secret")
	oauth := &OAuthConfig{}
	cfg := &SSEConfig{
		Type:          TransportSSE,
		URL:           "http://x.example.com/${CFG_MUT_SSE_TOK}",
		Headers:       map[string]string{"h": "${CFG_MUT_SSE_TOK}"},
		HeadersHelper: "helper-cmd",
		Mode:          ModeCode,
		OAuth:         oauth,
	}
	expanded, missing := ExpandConfigEnv(cfg)
	if len(missing) != 0 {
		t.Fatalf("missing = %v, want none", missing)
	}
	sse, ok := expanded.(*SSEConfig)
	if !ok {
		t.Fatalf("expanded type = %T, want *SSEConfig", expanded)
	}
	if sse.OAuth != oauth {
		t.Error("OAuth pointer must pass through unexpanded")
	}
	if sse.HeadersHelper != "helper-cmd" {
		t.Errorf("HeadersHelper = %q, want %q", sse.HeadersHelper, "helper-cmd")
	}
	if sse.Headers["h"] != "secret" {
		t.Errorf("Headers[h] = %q, want expanded %q", sse.Headers["h"], "secret")
	}
}

// 455:statement/return — HTTP expansion must return a fully populated
// *HTTPConfig and the collected missing-var list (the mutant nils the
// missingVars return value).
func TestExpandConfigEnv_HTTPPassThrough(t *testing.T) {
	t.Setenv("CFG_MUT_HTTP_TOK", "v")
	oauth := &OAuthConfig{}
	cfg := &HTTPConfig{
		Type:          TransportHTTP,
		URL:           "http://h.example.com/${CFG_MUT_HTTP_TOK}/${CFG_MUT_HTTP_MISSING}",
		Headers:       map[string]string{"auth": "tok"},
		HeadersHelper: "hh",
		Mode:          ModeDeferred,
		OAuth:         oauth,
	}
	expanded, missing := ExpandConfigEnv(cfg)
	if len(missing) != 1 || missing[0] != "CFG_MUT_HTTP_MISSING" {
		t.Fatalf("missing = %v, want [CFG_MUT_HTTP_MISSING]", missing)
	}
	h, ok := expanded.(*HTTPConfig)
	if !ok {
		t.Fatalf("expanded type = %T, want *HTTPConfig", expanded)
	}
	if h.URL != "http://h.example.com/v/${CFG_MUT_HTTP_MISSING}" {
		t.Errorf("URL = %q, want expanded prefix with literal missing var", h.URL)
	}
	if h.Type != TransportHTTP {
		t.Errorf("Type = %q, want %q", h.Type, TransportHTTP)
	}
	if h.OAuth != oauth {
		t.Error("OAuth pointer must pass through")
	}
	if h.HeadersHelper != "hh" {
		t.Errorf("HeadersHelper = %q, want %q", h.HeadersHelper, "hh")
	}
	if h.Mode != ModeDeferred {
		t.Errorf("Mode = %q, want %q", h.Mode, ModeDeferred)
	}
}

// 459:composite/field-clear, 461:composite/field-clear, 462:composite/
// field-clear, 464:statement/return — WS pass-through fields must survive
// and the missing-var list must be reported.
func TestExpandConfigEnv_WSPassThrough(t *testing.T) {
	t.Setenv("CFG_MUT_WS_TOK", "w")
	cfg := &WSConfig{
		Type:          TransportWS,
		URL:           "ws://w.example.com/${CFG_MUT_WS_TOK}/${CFG_MUT_WS_MISSING}",
		Headers:       map[string]string{"h": "static"},
		HeadersHelper: "wh",
		Mode:          ModeCode,
	}
	expanded, missing := ExpandConfigEnv(cfg)
	if len(missing) != 1 || missing[0] != "CFG_MUT_WS_MISSING" {
		t.Fatalf("missing = %v, want [CFG_MUT_WS_MISSING]", missing)
	}
	w, ok := expanded.(*WSConfig)
	if !ok {
		t.Fatalf("expanded type = %T, want *WSConfig", expanded)
	}
	if w.Type != TransportWS {
		t.Errorf("Type = %q, want %q", w.Type, TransportWS)
	}
	if w.Headers["h"] != "static" {
		t.Errorf("Headers[h] = %v, want preserved", w.Headers["h"])
	}
	if w.HeadersHelper != "wh" {
		t.Errorf("HeadersHelper = %q, want %q", w.HeadersHelper, "wh")
	}
	if w.Mode != ModeCode {
		t.Errorf("Mode = %q, want %q", w.Mode, ModeCode)
	}
	if w.URL != "ws://w.example.com/w/${CFG_MUT_WS_MISSING}" {
		t.Errorf("URL = %q, want unexpanded missing var intact", w.URL)
	}
}

// ---------------------------------------------------------------------------
// Policy engine
// ---------------------------------------------------------------------------

// 522:expression/logical — a mixed denylist must not panic or mis-evaluate:
// name entries that fail the type assertion must be skipped (ok && …), not
// short-circuit into dereferencing a nil *McpUrlEntry (ok || … panics).
func TestIsMcpServerDenied_MixedEntriesNoFalseDeny(t *testing.T) {
	denied := []McpPolicyEntry{
		&McpNameEntry{ServerName: "other"},
		&McpUrlEntry{ServerUrl: "https://deny.example.com/*"},
	}
	cfg := &SSEConfig{URL: "https://safe.example.com/api"}
	if IsMcpServerDenied("myserver", cfg, denied) {
		t.Error("non-matching name and URL must not deny")
	}
}

// 638:branch/if — SDK servers stay exempt from policy filtering even when
// explicitly denied by name.
func TestFilterMcpServersByPolicy_SDKDeniedByName(t *testing.T) {
	configs := map[string]ScopedMcpServerConfig{
		"sdk1": {Config: &SDKConfig{}, Scope: ScopeLocal},
	}
	denied := []McpPolicyEntry{&McpNameEntry{ServerName: "sdk1"}}
	allowed, blocked := FilterMcpServersByPolicy(configs, denied, nil)
	if _, ok := allowed["sdk1"]; !ok {
		t.Error("SDK server must stay exempt from denial")
	}
	if len(blocked) != 0 {
		t.Errorf("blocked = %v, want empty", blocked)
	}
}

// ---------------------------------------------------------------------------
// ParseMcpConfig / loadMcpServersFromConfig
// ---------------------------------------------------------------------------

// 686:composite/field-clear — schema-validation errors keep the file path.
func TestParseMcpConfig_InvalidServerKeepsFilePath(t *testing.T) {
	input := `{"mcpServers":{"bad":{"type":"ftp","url":"ftp://example.com"}}}`
	_, errs := ParseMcpConfig([]byte(input), false, ScopeProject, "my.json")
	if len(errs) != 1 {
		t.Fatalf("errs = %v, want 1", errs)
	}
	if errs[0].File != "my.json" {
		t.Errorf("File = %q, want %q", errs[0].File, "my.json")
	}
}

// 690:loop/break — every invalid server is reported (continue→break after
// the first would report only one).
func TestParseMcpConfig_TwoInvalidServers(t *testing.T) {
	input := `{"mcpServers":{"b1":{"type":"ftp"},"b2":{"type":"ftp2"}}}`
	config, errs := ParseMcpConfig([]byte(input), false, ScopeProject, "")
	if config == nil {
		t.Fatal("want non-nil partial config")
	}
	if len(errs) != 2 {
		t.Errorf("errs = %d (%v), want one per invalid server", len(errs), errs)
	}
}

// 959:loop/break — loadMcpServersFromConfig reports every invalid entry.
func TestLoadMcpServersFromConfig_TwoInvalidEntries(t *testing.T) {
	config := &McpJsonConfig{
		McpServers: map[string]json.RawMessage{
			"bad1": json.RawMessage(`{"type":"invalid_one"}`),
			"bad2": json.RawMessage(`{"type":"invalid_two"}`),
		},
	}
	servers, errs := loadMcpServersFromConfig(config)
	if len(servers) != 0 {
		t.Errorf("servers = %d, want 0", len(servers))
	}
	if len(errs) != 2 {
		t.Errorf("errs = %d (%v), want one per invalid entry", len(errs), errs)
	}
}

// ---------------------------------------------------------------------------
// WriteMcpjsonFile
// ---------------------------------------------------------------------------

// 759:numbers/incrementer, 759:numbers/decrementer — a fresh .mcp.json is
// created with the documented 0644 default permissions.
func TestWriteMcpjsonFile_DefaultPerm(t *testing.T) {
	dir := t.TempDir()
	if err := WriteMcpjsonFile(dir, &McpJsonConfig{McpServers: map[string]json.RawMessage{
		"srv": json.RawMessage(`{"command":"node"}`),
	}}); err != nil {
		t.Fatalf("WriteMcpjsonFile: %v", err)
	}
	info, err := os.Stat(filepath.Join(dir, ".mcp.json"))
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0644 {
		t.Errorf("perm = %o, want 644", perm)
	}
}

// 761:branch/if, 761:statement/remove — an existing file's permissions are
// preserved through the temp-file + rename rewrite.
func TestWriteMcpjsonFile_PreservesPerm(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".mcp.json")
	mustWriteFile(t, path, []byte(`{"mcpServers":{}}`))
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(path, 0644) }()
	if err := WriteMcpjsonFile(dir, &McpJsonConfig{McpServers: map[string]json.RawMessage{
		"srv": json.RawMessage(`{"command":"node"}`),
	}}); err != nil {
		t.Fatalf("WriteMcpjsonFile: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0600 {
		t.Errorf("perm = %o, want 600 preserved", perm)
	}
}

// 774:expression/errorf-wrap — the create-temp error wraps the underlying
// *os.PathError so callers can inspect it via errors.As.
func TestWriteMcpjsonFile_CreateTempErrWrapped(t *testing.T) {
	err := WriteMcpjsonFile(filepath.Join(t.TempDir(), "no", "such", "dir"), &McpJsonConfig{})
	if err == nil {
		t.Fatal("want error for nonexistent dir")
	}
	if _, ok := errors.AsType[*os.PathError](err); !ok {
		t.Errorf("err = %v, want wrapped *os.PathError", err)
	}
}

// 802:expression/error-guard, 804:statement/return, 804:expression/
// errorf-wrap — when rename fails (.mcp.json is a directory), the error is
// returned (not swallowed into success) and wraps the *os.LinkError.
func TestWriteMcpjsonFile_RenameTargetIsDir(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, ".mcp.json")
	if err := os.Mkdir(target, 0755); err != nil {
		t.Fatal(err)
	}
	err := WriteMcpjsonFile(dir, &McpJsonConfig{McpServers: map[string]json.RawMessage{
		"srv": json.RawMessage(`{"command":"node"}`),
	}})
	if err == nil {
		t.Fatal("want error when rename target is a directory")
	}
	if _, ok := errors.AsType[*os.LinkError](err); !ok {
		t.Errorf("err = %v, want wrapped *os.LinkError", err)
	}
	// No temp file may survive the failed rename.
	entries, rdErr := os.ReadDir(dir)
	if rdErr != nil {
		t.Fatalf("ReadDir(%s): %v", dir, rdErr)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp.") {
			t.Errorf("temp file left behind: %s", e.Name())
		}
	}
}

// ---------------------------------------------------------------------------
// Scope loaders
// ---------------------------------------------------------------------------

// 766:expression/error-guard, 767:branch/if, 767:statement/return,
// 767:expression/errorf-wrap — an invalid RawMessage makes the config-level
// json.MarshalIndent fail; the failure must surface wrapped, and no file may
// be produced for a config that could not be marshaled.
func TestWriteMcpjsonFile_MarshalError(t *testing.T) {
	dir := t.TempDir()
	err := WriteMcpjsonFile(dir, &McpJsonConfig{McpServers: map[string]json.RawMessage{
		"bad": json.RawMessage(`{not json`),
	}})
	if err == nil {
		t.Fatal("want failure for a config whose RawMessage is not JSON")
	}
	if !strings.Contains(err.Error(), "failed to marshal MCP config") {
		t.Errorf("err = %v, want the marshal step reported", err)
	}
	if errors.Unwrap(err) == nil {
		t.Error("err must wrap the underlying json encoder error")
	}
	if _, statErr := os.Stat(filepath.Join(dir, ".mcp.json")); statErr == nil {
		t.Error("no .mcp.json may exist when marshaling fails")
	}
}

// 780:statement/return, 780:expression/errorf-wrap, 783:expression/error-guard,
// 784:branch/if, 784:statement/return — the temp path is pre-created as a
// symlink to /dev/full, so OpenFile succeeds and only f.Write fails (ENOSPC).
// That isolates the write guard: every later step must be skipped and the
// *os.PathError chain must stay intact.
func TestWriteMcpjsonFile_DevFullWriteError(t *testing.T) {
	if _, statErr := os.Stat("/dev/full"); statErr != nil {
		t.Skipf("/dev/full unavailable: %v", statErr)
	}
	dir := t.TempDir()
	tempPath := filepath.Join(dir, fmt.Sprintf(".mcp.json.tmp.%d", os.Getpid()))
	if err := os.Symlink("/dev/full", tempPath); err != nil {
		t.Fatalf("symlink %s: %v", tempPath, err)
	}
	t.Cleanup(func() { _ = os.Remove(tempPath) })

	err := WriteMcpjsonFile(dir, &McpJsonConfig{McpServers: map[string]json.RawMessage{
		"srv": json.RawMessage(`{"command":"node"}`),
	}})
	if err == nil {
		t.Fatal("want write failure against /dev/full")
	}
	if !strings.Contains(err.Error(), "failed to write temp file") {
		t.Errorf("err = %v, want the write step reported", err)
	}
	if _, ok := errors.AsType[*os.PathError](err); !ok {
		t.Errorf("err = %v, want wrapped *os.PathError", err)
	}
	if !errors.Is(err, syscall.ENOSPC) {
		t.Errorf("err = %v, want ENOSPC from /dev/full", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, ".mcp.json")); statErr == nil {
		t.Error("no .mcp.json may exist after a failed write")
	}
}

// 787:expression/error-guard, 788:branch/if, 788:statement/return — the temp
// path is a symlink to /dev/null: the write is accepted there, but fsync on a
// character device fails with EINVAL. That is the only in-process way to make
// Sync fail; it also shows why the chmod guard below has no reachable failure
// path — every target that refuses chmod (a char device, a FIFO) is refused
// earlier, at Sync.
func TestWriteMcpjsonFile_DevNullSyncError(t *testing.T) {
	if _, statErr := os.Stat("/dev/null"); statErr != nil {
		t.Skipf("/dev/null unavailable: %v", statErr)
	}
	dir := t.TempDir()
	tempPath := filepath.Join(dir, fmt.Sprintf(".mcp.json.tmp.%d", os.Getpid()))
	if err := os.Symlink("/dev/null", tempPath); err != nil {
		t.Fatalf("symlink %s: %v", tempPath, err)
	}
	t.Cleanup(func() { _ = os.Remove(tempPath) })

	err := WriteMcpjsonFile(dir, &McpJsonConfig{McpServers: map[string]json.RawMessage{
		"srv": json.RawMessage(`{"command":"node"}`),
	}})
	if err == nil {
		t.Fatal("want sync failure on a character device")
	}
	if !strings.Contains(err.Error(), "failed to sync temp file") {
		t.Errorf("err = %v, want the sync step reported", err)
	}
	if pathErr, ok := errors.AsType[*os.PathError](err); !ok {
		t.Errorf("err = %v, want wrapped *os.PathError", err)
	} else if pathErr.Op != "sync" {
		t.Errorf("PathError.Op = %q, want sync", pathErr.Op)
	}
	if !errors.Is(err, syscall.EINVAL) {
		t.Errorf("err = %v, want EINVAL from fsync on /dev/null", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, ".mcp.json")); statErr == nil {
		t.Error("a failed sync must stop before the rename")
	}
}

// 820:conditional/bool-literal — GetProjectMcpConfigsFromCwd expands env
// vars, so a server referencing a missing variable surfaces a validation
// error (expandVars=false mutant reports none).
func TestGetProjectMcpConfigsFromCwd_MissingEnvErrorReported(t *testing.T) {
	dir := t.TempDir()
	mustWriteFile(t, filepath.Join(dir, ".mcp.json"),
		[]byte(`{"mcpServers":{"s":{"command":"node","args":["${CFG_MUT_MISSING_VAR}"]}}}`))
	servers, errs := GetProjectMcpConfigsFromCwd(dir)
	if len(errs) != 1 {
		t.Fatalf("errs = %v, want the missing-env validation error", errs)
	}
	if !strings.Contains(errs[0].Message, "Missing environment variables") {
		t.Errorf("errs[0].Message = %q, want missing-env report", errs[0].Message)
	}
	if _, ok := servers["s"]; !ok {
		t.Error("missing-env server must stay in the parsed view")
	}
}

// 911:conditional/bool-literal — the project directory walk also expands env
// vars (same observable as above, through getProjectMcpConfigs).
func TestGetProjectMcpConfigs_MissingEnvErrorReported(t *testing.T) {
	dir := t.TempDir()
	mustWriteFile(t, filepath.Join(dir, ".mcp.json"),
		[]byte(`{"mcpServers":{"s":{"command":"node","args":["${CFG_MUT_MISSING_VAR_2}"]}}}`))
	_, errs := getProjectMcpConfigs(dir)
	if len(errs) != 1 {
		t.Fatalf("errs = %v, want the missing-env validation error", errs)
	}
	if !strings.Contains(errs[0].Message, "Missing environment variables") {
		t.Errorf("errs[0].Message = %q, want missing-env report", errs[0].Message)
	}
}

// 882:statement/return (errs instance), 885:branch/case — scope loaders
// return a non-nil empty map on the empty paths, and enterprise schema
// errors flow out alongside parsed servers.
func TestGetMcpConfigsByScope_EmptyPathsReturnNonNilMap(t *testing.T) {
	provider := &mockConfigProvider{} // managed path does not exist
	result, errs := GetMcpConfigsByScope(ScopeEnterprise, t.TempDir(), provider)
	if len(errs) != 0 {
		t.Errorf("enterprise errs = %v, want none (not-found filtered)", errs)
	}
	if result == nil {
		t.Error("missing enterprise config must return non-nil empty map")
	}
	result, errs = GetMcpConfigsByScope(ConfigScope("unknown"), "/tmp", provider)
	if len(errs) != 0 {
		t.Errorf("unknown-scope errs = %v, want none", errs)
	}
	if result == nil {
		t.Error("unknown scope must return non-nil empty map")
	}
}

// 882:statement/return (errs instance) — an enterprise file with an invalid
// server yields both the parsed good server and the validation error.
func TestGetMcpConfigsByScope_EnterpriseSchemaErrReported(t *testing.T) {
	mdir := t.TempDir()
	mustWriteFile(t, filepath.Join(mdir, "managed.json"),
		[]byte(`{"mcpServers":{"good":{"command":"echo"},"bad":{"type":"ftp"}}}`))
	provider := &mockConfigProvider{managedPath: filepath.Join(mdir, "managed.json")}
	servers, errs := GetMcpConfigsByScope(ScopeEnterprise, t.TempDir(), provider)
	if _, ok := servers["good"]; !ok {
		t.Error("good enterprise server missing")
	}
	if len(errs) != 1 {
		t.Fatalf("errs = %v, want the schema-validation error", errs)
	}
	if !strings.Contains(errs[0].Message, "unknown server config type") {
		t.Errorf("errs[0].Message = %q, want unknown-type report", errs[0].Message)
	}
}

// 838:conditional/negated, 838:numbers/incrementer, 839:branch/if,
// 839:statement/remove — a server that only turns invalid *after* env
// expansion (command expands to the empty string, which the schema rejects on
// re-parse) is reported by loadMcpServersFromConfig; the re-append guard must
// not skip a non-empty parseErrs slice.
func TestGetProjectMcpConfigsFromCwd_PostExpansionInvalid(t *testing.T) {
	t.Setenv("CFG_MUT_EMPTY_CMD", "")
	dir := t.TempDir()
	mustWriteFile(t, filepath.Join(dir, ".mcp.json"),
		[]byte(`{"mcpServers":{"s":{"command":"${CFG_MUT_EMPTY_CMD}"}}}`))
	servers, errs := GetProjectMcpConfigsFromCwd(dir)
	if len(errs) != 1 {
		t.Fatalf("errs = %v, want the post-expansion schema error", errs)
	}
	if !strings.Contains(errs[0].Message, "Invalid config") {
		t.Errorf("errs[0].Message = %q, want the Invalid config report", errs[0].Message)
	}
	if errs[0].Path != "mcpServers.s" {
		t.Errorf("Path = %q, want mcpServers.s", errs[0].Path)
	}
	if len(servers) != 0 {
		t.Errorf("servers = %v, want the invalid server dropped", servers)
	}
}

// 926:statement/remove — the same server seen through the directory walk: the
// loadMcpServersFromConfig errors must be re-appended to allErrors.
func TestGetProjectMcpConfigs_PostExpansionInvalid(t *testing.T) {
	t.Setenv("CFG_MUT_EMPTY_CMD_WALK", "")
	dir := t.TempDir()
	mustWriteFile(t, filepath.Join(dir, ".mcp.json"),
		[]byte(`{"mcpServers":{"s":{"command":"${CFG_MUT_EMPTY_CMD_WALK}"}}}`))
	servers, errs := getProjectMcpConfigs(dir)
	if len(errs) != 1 {
		t.Fatalf("errs = %v, want the post-expansion schema error", errs)
	}
	if !strings.Contains(errs[0].Message, "Invalid config") {
		t.Errorf("errs[0].Message = %q, want the Invalid config report", errs[0].Message)
	}
	if len(servers) != 0 {
		t.Errorf("servers = %v, want the invalid server dropped", servers)
	}
}

// ---------------------------------------------------------------------------
// writeProjectConfig / writeProjectConfigExclude
// ---------------------------------------------------------------------------

// 1083:branch/if — an existing server whose marshal fails is skipped from
// the written file (field-clear mutant writes a null entry for it).
func TestWriteProjectConfig_MarshalErrSkipsInFileContent(t *testing.T) {
	dir := t.TempDir()
	existing := map[string]ScopedMcpServerConfig{
		"bad": {Config: marshalErrorConfig{}, Scope: ScopeProject},
	}
	if err := writeProjectConfig(dir, existing, "new", &StdioConfig{Command: "echo"}); err != nil {
		t.Fatalf("writeProjectConfig: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, ".mcp.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "bad") {
		t.Errorf("marshal-failing server must be skipped from file, got: %s", data)
	}
	if !strings.Contains(string(data), "new") {
		t.Errorf("new server missing from file: %s", data)
	}
}

// 1105:branch/if — same invariant for writeProjectConfigExclude.
func TestWriteProjectConfigExclude_MarshalErrSkipsInFileContent(t *testing.T) {
	dir := t.TempDir()
	existing := map[string]ScopedMcpServerConfig{
		"bad": {Config: marshalErrorConfig{}, Scope: ScopeProject},
	}
	if err := writeProjectConfigExclude(dir, existing, "remove"); err != nil {
		t.Fatalf("writeProjectConfigExclude: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, ".mcp.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "bad") {
		t.Errorf("marshal-failing server must be skipped from file, got: %s", data)
	}
}

// decodeWrittenServers reads the mcpServers map back out of a written file.
func decodeWrittenServers(data []byte) (map[string]json.RawMessage, error) {
	var parsed McpJsonConfig
	if err := json.Unmarshal(data, &parsed); err != nil {
		return nil, err
	}
	return parsed.McpServers, nil
}

// countingMarshalConfig records every marshal attempt, which makes a scan that
// stops early visible no matter how the map happens to iterate.
type countingMarshalConfig struct {
	calls *int
	bad   bool
}

func (c countingMarshalConfig) GetTransport() Transport { return "counting" }

func (c countingMarshalConfig) MarshalJSON() ([]byte, error) {
	*c.calls++
	if c.bad {
		return nil, errors.New("cfg mutate: forced marshal failure")
	}
	return []byte(`{"command":"ok"}`), nil
}

// 1083:loop/break — every existing server must be marshalled. A break on the
// first failure silently drops the entries that come after it in the map.
func TestWriteProjectConfig_MarshalsEveryExisting(t *testing.T) {
	dir := t.TempDir()
	calls := 0
	existing := map[string]ScopedMcpServerConfig{
		"bad1": {Config: countingMarshalConfig{calls: &calls, bad: true}, Scope: ScopeProject},
		"bad2": {Config: countingMarshalConfig{calls: &calls, bad: true}, Scope: ScopeProject},
		"good": {Config: countingMarshalConfig{calls: &calls}, Scope: ScopeProject},
	}
	if err := writeProjectConfig(dir, existing, "new", &StdioConfig{Command: "echo"}); err != nil {
		t.Fatalf("writeProjectConfig: %v", err)
	}
	if calls != 3 {
		t.Errorf("marshal attempts = %d, want one per existing entry (3)", calls)
	}
	data, err := os.ReadFile(filepath.Join(dir, ".mcp.json"))
	if err != nil {
		t.Fatal(err)
	}
	written, decodeErr := decodeWrittenServers(data)
	if decodeErr != nil {
		t.Fatalf("written file is not valid JSON: %v (%s)", decodeErr, data)
	}
	if _, ok := written["good"]; !ok {
		t.Errorf("'good' dropped by a sibling's marshal failure; file = %s", data)
	}
	if len(written) != 2 {
		t.Errorf("file has %d entries (%s), want good+new", len(written), data)
	}
}

// 1105:loop/break — same invariant for the exclude path: the excluded name is
// skipped, every remaining entry is still marshalled.
func TestWriteProjectConfigExclude_MarshalsEveryExisting(t *testing.T) {
	dir := t.TempDir()
	calls := 0
	existing := map[string]ScopedMcpServerConfig{
		"bad1":     {Config: countingMarshalConfig{calls: &calls, bad: true}, Scope: ScopeProject},
		"bad2":     {Config: countingMarshalConfig{calls: &calls, bad: true}, Scope: ScopeProject},
		"good":     {Config: countingMarshalConfig{calls: &calls}, Scope: ScopeProject},
		"excluded": {Config: countingMarshalConfig{calls: &calls, bad: true}, Scope: ScopeProject},
	}
	if err := writeProjectConfigExclude(dir, existing, "excluded"); err != nil {
		t.Fatalf("writeProjectConfigExclude: %v", err)
	}
	if calls != 3 {
		t.Errorf("marshal attempts = %d, want one per non-excluded entry (3)", calls)
	}
	data, err := os.ReadFile(filepath.Join(dir, ".mcp.json"))
	if err != nil {
		t.Fatal(err)
	}
	written, decodeErr := decodeWrittenServers(data)
	if decodeErr != nil {
		t.Fatalf("written file is not valid JSON: %v (%s)", decodeErr, data)
	}
	if len(written) != 1 {
		t.Errorf("file has %d entries (%s), want only 'good'", len(written), data)
	}
	if _, ok := written["good"]; !ok {
		t.Errorf("'good' dropped by a sibling's marshal failure; file = %s", data)
	}
}

// 1089:expression/errorf-wrap — the new-config marshal error is wrapped so
// errors.Unwrap can recover it.
func TestWriteProjectConfig_NewConfigMarshalErrWrapped(t *testing.T) {
	err := writeProjectConfig(t.TempDir(), nil, "bad", marshalErrorConfig{})
	if err == nil {
		t.Fatal("want error for unmarshalable new config")
	}
	if !strings.Contains(err.Error(), "failed to marshal new config") {
		t.Errorf("err = %v, want marshal-failure message", err)
	}
	if errors.Unwrap(err) == nil {
		t.Error("err must wrap the underlying marshal error")
	}
}

// ---------------------------------------------------------------------------
// GetClaudeCodeMcpConfigs
// ---------------------------------------------------------------------------

// 1187:numbers/incrementer, 1189:branch/if — with an
// isolated enterprise file (not on the project walk path), enterprise takes
// exclusive control: its server is the only result, despite user servers.
func TestGetClaudeCodeMcpConfigs_EnterpriseExclusiveIsolated(t *testing.T) {
	cwd := t.TempDir() // no .mcp.json anywhere on its walk path
	mdir := t.TempDir()
	mustWriteFile(t, filepath.Join(mdir, "managed.json"),
		[]byte(`{"mcpServers":{"ent":{"command":"echo"}}}`))
	provider := &mockConfigProvider{
		managedPath:  filepath.Join(mdir, "managed.json"),
		userServers:  map[string]McpServerConfig{"u": &StdioConfig{Command: "user"}},
		localServers: map[string]McpServerConfig{"u": &StdioConfig{Command: "local"}},
	}
	result, errs := GetClaudeCodeMcpConfigs(cwd, provider, nil, nil)
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if len(result) != 1 {
		t.Errorf("result = %d entries (%v), want enterprise-exclusive", len(result), result)
	}
	scoped, ok := result["ent"]
	if !ok {
		t.Fatal("enterprise server 'ent' missing from result")
	}
	if scoped.Scope != ScopeEnterprise {
		t.Errorf("ent scope = %q, want enterprise", scoped.Scope)
	}
}

// 1195:branch/if, 1195:statement/remove, 1195:conditional/bool-literal —
// plugin-only mode locks out user-scope servers.
func TestGetClaudeCodeMcpConfigs_PluginOnlyLocksUser(t *testing.T) {
	provider := &mockConfigProvider{
		pluginOnly:  true,
		userServers: map[string]McpServerConfig{"u": &StdioConfig{Command: "user"}},
	}
	result, errs := GetClaudeCodeMcpConfigs(t.TempDir(), provider, nil, nil)
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if len(result) != 0 {
		t.Errorf("result = %d entries (%v), want plugin-only lockdown", len(result), result)
	}
}

// 1211:branch/else, 1211:statement/remove, 1247:statement/remove — project-scope
// servers are loaded in normal (unlocked) mode and merged into the final result.
func TestGetClaudeCodeMcpConfigs_ProjectScopeLoaded(t *testing.T) {
	cwd := t.TempDir()
	mustWriteFile(t, filepath.Join(cwd, ".mcp.json"),
		[]byte(`{"mcpServers":{"proj":{"command":"node"}}}`))
	result, errs := GetClaudeCodeMcpConfigs(cwd, &mockConfigProvider{}, nil, nil)
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	scoped, ok := result["proj"]
	if !ok {
		t.Fatal("project server 'proj' missing from result")
	}
	if scoped.Scope != ScopeProject {
		t.Errorf("proj scope = %q, want project", scoped.Scope)
	}
}

// 1225:expression/remove — a disabled manual server is not a dedup target:
// its plugin twin survives.
func TestGetClaudeCodeMcpConfigs_DisabledManualNotDedupTarget(t *testing.T) {
	provider := &mockConfigProvider{
		userServers:     map[string]McpServerConfig{"m": &StdioConfig{Command: "echo"}},
		disabledServers: []string{"m"},
	}
	plugin := map[string]ScopedMcpServerConfig{
		"plugin:p:s": {Config: &StdioConfig{Command: "echo"}, Scope: ScopeDynamic},
	}
	result, errs := GetClaudeCodeMcpConfigs(t.TempDir(), provider, nil, plugin)
	if len(errs) != 0 {
		t.Fatalf("GetClaudeCodeMcpConfigs: unexpected errors %v", errs)
	}
	if _, ok := result["plugin:p:s"]; !ok {
		t.Error("disabled manual must not suppress its plugin twin")
	}
}

// 1226:expression/remove — a policy-denied manual server is not a dedup
// target either, and it is filtered from the final result.
func TestGetClaudeCodeMcpConfigs_DeniedManualNotDedupTarget(t *testing.T) {
	provider := &mockConfigProvider{
		userServers:   map[string]McpServerConfig{"m": &StdioConfig{Command: "echo"}},
		deniedEntries: []McpPolicyEntry{&McpNameEntry{ServerName: "m"}},
	}
	plugin := map[string]ScopedMcpServerConfig{
		"plugin:p:s": {Config: &StdioConfig{Command: "echo"}, Scope: ScopeDynamic},
	}
	result, errs := GetClaudeCodeMcpConfigs(t.TempDir(), provider, nil, plugin)
	if len(errs) != 0 {
		t.Fatalf("GetClaudeCodeMcpConfigs: unexpected errors %v", errs)
	}
	if _, ok := result["plugin:p:s"]; !ok {
		t.Error("denied manual must not suppress its plugin twin")
	}
	if _, ok := result["m"]; ok {
		t.Error("denied manual must be filtered from the final result")
	}
}

// 1234:expression/remove, 1236:loop/break, 1236:branch/if — disabled plugins
// are filtered while enabled plugins survive (two plugins make the
// continue→break mutant drop the enabled one regardless of map order).
func TestGetClaudeCodeMcpConfigs_DisabledPluginFiltered(t *testing.T) {
	provider := &mockConfigProvider{disabledServers: []string{"plugin:p:off"}}
	plugin := map[string]ScopedMcpServerConfig{
		"plugin:p:off": {Config: &SSEConfig{URL: "http://off.example.com"}, Scope: ScopeDynamic},
		"plugin:p:on":  {Config: &SSEConfig{URL: "http://on.example.com"}, Scope: ScopeDynamic},
	}
	result, errs := GetClaudeCodeMcpConfigs(t.TempDir(), provider, nil, plugin)
	if len(errs) != 0 {
		t.Fatalf("GetClaudeCodeMcpConfigs: unexpected errors %v", errs)
	}
	if _, ok := result["plugin:p:off"]; ok {
		t.Error("disabled plugin must be filtered")
	}
	if _, ok := result["plugin:p:on"]; !ok {
		t.Error("enabled plugin must survive")
	}
}

// 1235:expression/remove — dropping the policy arm of the plugin prefilter
// lets a denied plugin into the dedup pool, where it can shadow (suppress)
// an enabled same-signature twin that should survive.
func TestGetClaudeCodeMcpConfigs_DeniedPluginNoShadow(t *testing.T) {
	provider := &mockConfigProvider{
		deniedEntries: []McpPolicyEntry{&McpNameEntry{ServerName: "plugin:p:deny"}},
	}
	plugin := map[string]ScopedMcpServerConfig{
		"plugin:p:deny": {Config: &SSEConfig{URL: "http://shared.example.com"}, Scope: ScopeDynamic},
		"plugin:p:ok":   {Config: &SSEConfig{URL: "http://shared.example.com"}, Scope: ScopeDynamic},
	}
	result, errs := GetClaudeCodeMcpConfigs(t.TempDir(), provider, nil, plugin)
	if len(errs) != 0 {
		t.Fatalf("GetClaudeCodeMcpConfigs: unexpected errors %v", errs)
	}
	if _, ok := result["plugin:p:ok"]; !ok {
		t.Error("enabled plugin twin must not be shadowed by the denied duplicate")
	}
	if _, ok := result["plugin:p:deny"]; ok {
		t.Error("denied plugin must not appear in the final result")
	}
}

// 1248:statement/remove (killed at census; kept as regression guard) —
// local-scope servers are merged into the final
// result (maps.Copy(configs, localServers) noop mutant drops them).
func TestGetClaudeCodeMcpConfigs_LocalScopeMerged(t *testing.T) {
	provider := &mockConfigProvider{
		localServers: map[string]McpServerConfig{"loc": &StdioConfig{Command: "cat"}},
	}
	result, errs := GetClaudeCodeMcpConfigs(t.TempDir(), provider, nil, nil)
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	scoped, ok := result["loc"]
	if !ok {
		t.Fatal("local server 'loc' missing from merged result")
	}
	if scoped.Scope != ScopeLocal {
		t.Errorf("loc scope = %q, want local", scoped.Scope)
	}
}

// countingDisabledProvider counts disabled-list lookups so a plugin scan that
// stops early is observable regardless of map iteration order.
type countingDisabledProvider struct {
	mockConfigProvider
	calls *int
}

func (p *countingDisabledProvider) ProjectDisabledServers() []string {
	*p.calls++
	return p.disabledServers
}

// 1236:branch/if, 1236:loop/break — the plugin prefilter must evaluate every
// plugin server: an emptied guard body admits disabled servers, and a break
// stops the scan at the first one, silently dropping everything after it.
func TestGetClaudeCodeMcpConfigs_PluginPrefilterScansEveryPlugin(t *testing.T) {
	calls := 0
	provider := &countingDisabledProvider{
		disabledServers: []string{"plugin:p:off1", "plugin:p:off2"},
		calls:           &calls,
	}
	plugin := map[string]ScopedMcpServerConfig{
		"plugin:p:off1": {Config: &SSEConfig{URL: "http://o1.example.com"}, Scope: ScopeDynamic},
		"plugin:p:off2": {Config: &SSEConfig{URL: "http://o2.example.com"}, Scope: ScopeDynamic},
		"plugin:p:on":   {Config: &SSEConfig{URL: "http://on.example.com"}, Scope: ScopeDynamic},
	}
	result, errs := GetClaudeCodeMcpConfigs(t.TempDir(), provider, nil, plugin)
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if calls != 3 {
		t.Errorf("disabled-list lookups = %d, want one per plugin (3)", calls)
	}
	if _, ok := result["plugin:p:on"]; !ok {
		t.Error("enabled plugin must survive the prefilter")
	}
	for _, name := range []string{"plugin:p:off1", "plugin:p:off2"} {
		if _, ok := result[name]; ok {
			t.Errorf("disabled plugin %s leaked into the result", name)
		}
	}
}
