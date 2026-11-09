package permission

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestLoadConfigEmpty(t *testing.T) {
	userDir := t.TempDir()
	projectDir := t.TempDir()

	rules := LoadConfig(userDir, projectDir)
	if len(rules) != 0 {
		t.Fatalf("expected 0 rules with empty dirs, got %d", len(rules))
	}
}

func TestLoadConfigNoPermissionsKey(t *testing.T) {
	userDir := t.TempDir()
	projectDir := t.TempDir()

	// Write a settings.json without permissions key
	writeSettingsFile(t, userDir, map[string]any{"model": "pro"})

	rules := LoadConfig(userDir, projectDir)
	if len(rules) != 0 {
		t.Fatalf("expected 0 rules without permissions key, got %d", len(rules))
	}
}

func TestLoadConfigDenyRules(t *testing.T) {
	userDir := t.TempDir()
	projectDir := t.TempDir()

	writeSettingsFile(t, userDir, map[string]any{
		"permissions": map[string]any{
			"deny": []string{"Bash(rm -rf *)", "Write(.env)"},
		},
	})

	rules := LoadConfig(userDir, projectDir)
	if len(rules) != 2 {
		t.Fatalf("expected 2 rules, got %d", len(rules))
	}

	// First rule: Bash deny
	r0 := rules[0]
	if r0.Action != ActionDeny {
		t.Errorf("rule[0] action: got %v, want %v", r0.Action, ActionDeny)
	}
	if r0.Value.ToolName != "Bash" {
		t.Errorf("rule[0] tool: got %q, want %q", r0.Value.ToolName, "Bash")
	}
	if r0.Value.RuleContent == nil || *r0.Value.RuleContent != "rm -rf *" {
		t.Errorf("rule[0] content: got %v, want %q", r0.Value.RuleContent, "rm -rf *")
	}
	if r0.Source != "user" {
		t.Errorf("rule[0] source: got %q, want %q", r0.Source, "user")
	}
	if r0.ConfigRoot != userDir {
		t.Errorf("rule[0] configRoot: got %q, want %q", r0.ConfigRoot, userDir)
	}

	// Second rule: Write deny
	r1 := rules[1]
	if r1.Value.ToolName != "Write" {
		t.Errorf("rule[1] tool: got %q, want %q", r1.Value.ToolName, "Write")
	}
	if r1.Value.RuleContent == nil || *r1.Value.RuleContent != ".env" {
		t.Errorf("rule[1] content: got %v, want %q", r1.Value.RuleContent, ".env")
	}
}

func TestLoadConfigAskRules(t *testing.T) {
	userDir := t.TempDir()
	projectDir := t.TempDir()

	writeSettingsFile(t, userDir, map[string]any{
		"permissions": map[string]any{
			"ask": []string{"Bash(git push *)"},
		},
	})

	rules := LoadConfig(userDir, projectDir)
	if len(rules) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(rules))
	}
	if rules[0].Action != ActionAsk {
		t.Errorf("action: got %v, want %v", rules[0].Action, ActionAsk)
	}
}

func TestLoadConfigMultiScopeMerge(t *testing.T) {
	userDir := t.TempDir()
	projectDir := t.TempDir()

	// User settings: deny Bash(rm -rf *)
	writeSettingsFile(t, userDir, map[string]any{
		"permissions": map[string]any{
			"deny": []string{"Bash(rm -rf *)"},
		},
	})

	// Project settings: ask Bash(git push *)
	writeProjectSettings(t, projectDir, map[string]any{
		"permissions": map[string]any{
			"ask": []string{"Bash(git push *)"},
		},
	})

	rules := LoadConfig(userDir, projectDir)
	if len(rules) != 2 {
		t.Fatalf("expected 2 rules from multi-scope, got %d", len(rules))
	}

	// User deny first
	if rules[0].Action != ActionDeny {
		t.Errorf("rule[0] action: got %v, want deny", rules[0].Action)
	}
	if rules[0].Source != "user" {
		t.Errorf("rule[0] source: got %q, want %q", rules[0].Source, "user")
	}
	// Project ask second
	if rules[1].Action != ActionAsk {
		t.Errorf("rule[1] action: got %v, want ask", rules[1].Action)
	}
	if rules[1].Source != "project" {
		t.Errorf("rule[1] source: got %q, want %q", rules[1].Source, "project")
	}
}

func TestLoadConfigLocalScope(t *testing.T) {
	userDir := t.TempDir()
	projectDir := t.TempDir()

	// Local settings: deny Write(*.json)
	writeLocalSettings(t, projectDir, map[string]any{
		"permissions": map[string]any{
			"deny": []string{"Write(*.json)"},
		},
	})

	rules := LoadConfig(userDir, projectDir)
	if len(rules) != 1 {
		t.Fatalf("expected 1 rule from local scope, got %d", len(rules))
	}
	if rules[0].Source != "local" {
		t.Errorf("source: got %q, want %q", rules[0].Source, "local")
	}
	if rules[0].ConfigRoot != projectDir {
		t.Errorf("configRoot: got %q, want %q", rules[0].ConfigRoot, projectDir)
	}
}

func TestLoadConfigAllowRulesIgnored(t *testing.T) {
	userDir := t.TempDir()
	projectDir := t.TempDir()

	writeSettingsFile(t, userDir, map[string]any{
		"permissions": map[string]any{
			"allow": []string{"Bash(npm test)", "Read"},
			"deny":  []string{"Write(.env)"},
		},
	})

	rules := LoadConfig(userDir, projectDir)
	// Only deny rule should appear; allow rules are logged and skipped
	if len(rules) != 1 {
		t.Fatalf("expected 1 rule (allow ignored), got %d", len(rules))
	}
	if rules[0].Action != ActionDeny {
		t.Errorf("action: got %v, want deny", rules[0].Action)
	}
}

func TestLoadConfigBareToolName(t *testing.T) {
	userDir := t.TempDir()
	projectDir := t.TempDir()

	writeSettingsFile(t, userDir, map[string]any{
		"permissions": map[string]any{
			"deny": []string{"Bash"},
		},
	})

	rules := LoadConfig(userDir, projectDir)
	if len(rules) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(rules))
	}
	if rules[0].Value.ToolName != "Bash" {
		t.Errorf("tool: got %q, want %q", rules[0].Value.ToolName, "Bash")
	}
	if rules[0].Value.RuleContent != nil {
		t.Errorf("content: got %v, want nil (bare tool name)", rules[0].Value.RuleContent)
	}
}

func TestLoadConfigInvalidJSON(t *testing.T) {
	userDir := t.TempDir()
	projectDir := t.TempDir()

	// Write invalid JSON to user settings
	path := filepath.Join(userDir, "settings.json")
	if err := os.WriteFile(path, []byte("{invalid json"), 0644); err != nil {
		t.Fatal(err)
	}

	rules := LoadConfig(userDir, projectDir)
	if len(rules) != 0 {
		t.Fatalf("expected 0 rules with invalid JSON, got %d", len(rules))
	}
}

func TestLoadConfigAllThreeScopes(t *testing.T) {
	userDir := t.TempDir()
	projectDir := t.TempDir()

	// User: deny Bash(rm -rf *)
	writeSettingsFile(t, userDir, map[string]any{
		"permissions": map[string]any{
			"deny": []string{"Bash(rm -rf *)"},
		},
	})
	// Project: ask Bash(git push *)
	writeProjectSettings(t, projectDir, map[string]any{
		"permissions": map[string]any{
			"ask": []string{"Bash(git push *)"},
		},
	})
	// Local: deny Write(.env)
	writeLocalSettings(t, projectDir, map[string]any{
		"permissions": map[string]any{
			"deny": []string{"Write(.env)"},
		},
	})

	rules := LoadConfig(userDir, projectDir)
	if len(rules) != 3 {
		t.Fatalf("expected 3 rules, got %d", len(rules))
	}

	// Verify order and sources
	want := []struct {
		action RuleAction
		tool   string
		source string
	}{
		{ActionDeny, "Bash", "user"},
		{ActionAsk, "Bash", "project"},
		{ActionDeny, "Write", "local"},
	}
	for i, w := range want {
		if rules[i].Action != w.action {
			t.Errorf("rule[%d] action: got %v, want %v", i, rules[i].Action, w.action)
		}
		if rules[i].Value.ToolName != w.tool {
			t.Errorf("rule[%d] tool: got %q, want %q", i, rules[i].Value.ToolName, w.tool)
		}
		if rules[i].Source != w.source {
			t.Errorf("rule[%d] source: got %q, want %q", i, rules[i].Source, w.source)
		}
	}
}

func TestLoadConfigConfigRootForProjectAndLocal(t *testing.T) {
	userDir := t.TempDir()
	projectDir := t.TempDir()

	writeSettingsFile(t, userDir, map[string]any{
		"permissions": map[string]any{
			"deny": []string{"Write(.env)"},
		},
	})
	writeProjectSettings(t, projectDir, map[string]any{
		"permissions": map[string]any{
			"deny": []string{"Write(.env)"},
		},
	})
	writeLocalSettings(t, projectDir, map[string]any{
		"permissions": map[string]any{
			"deny": []string{"Write(.env)"},
		},
	})

	rules := LoadConfig(userDir, projectDir)
	if len(rules) != 3 {
		t.Fatalf("expected 3 rules, got %d", len(rules))
	}

	// User rule: ConfigRoot = userDir
	if rules[0].ConfigRoot != userDir {
		t.Errorf("rule[0] configRoot: got %q, want %q", rules[0].ConfigRoot, userDir)
	}
	// Project rule: ConfigRoot = projectDir
	if rules[1].ConfigRoot != projectDir {
		t.Errorf("rule[1] configRoot: got %q, want %q", rules[1].ConfigRoot, projectDir)
	}
	// Local rule: ConfigRoot = projectDir
	if rules[2].ConfigRoot != projectDir {
		t.Errorf("rule[2] configRoot: got %q, want %q", rules[2].ConfigRoot, projectDir)
	}
}

// --- helpers ---

func writeSettingsFile(t *testing.T, userDir string, data map[string]any) {
	t.Helper()
	dataJSON, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(userDir, "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, dataJSON, 0644); err != nil {
		t.Fatal(err)
	}
}

func writeProjectSettings(t *testing.T, projectDir string, data map[string]any) {
	t.Helper()
	dataJSON, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(projectDir, ".gbot")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(path, dataJSON, 0644); err != nil {
		t.Fatal(err)
	}
}

func writeLocalSettings(t *testing.T, projectDir string, data map[string]any) {
	t.Helper()
	dataJSON, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(projectDir, ".gbot")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "settings.local.json")
	if err := os.WriteFile(path, dataJSON, 0644); err != nil {
		t.Fatal(err)
	}
}

func TestLoadConfigInvalidDenyRuleSkipped(t *testing.T) {
	userDir := t.TempDir()
	projectDir := t.TempDir()

	writeSettingsFile(t, userDir, map[string]any{
		"permissions": map[string]any{
			"deny": []string{"Bash", "Write([invalid)"},
		},
	})

	rules := LoadConfig(userDir, projectDir)
	if len(rules) != 1 {
		t.Fatalf("expected 1 rule (invalid skipped), got %d", len(rules))
	}
	if rules[0].Value.ToolName != "Bash" {
		t.Errorf("expected Bash rule, got %q", rules[0].Value.ToolName)
	}
}

func TestLoadConfigInvalidAskRuleSkipped(t *testing.T) {
	userDir := t.TempDir()
	projectDir := t.TempDir()

	writeSettingsFile(t, userDir, map[string]any{
		"permissions": map[string]any{
			"ask": []string{"Write([invalid)", "Bash"},
		},
	})

	rules := LoadConfig(userDir, projectDir)
	if len(rules) != 1 {
		t.Fatalf("expected 1 rule (invalid skipped), got %d", len(rules))
	}
	if rules[0].Value.ToolName != "Bash" {
		t.Errorf("expected Bash rule, got %q", rules[0].Value.ToolName)
	}
}

// permLogHandler captures slog records for the permission package tests.
type permLogHandler struct {
	mu   sync.Mutex
	recs []permLogRec
}

type permLogRec struct {
	level slog.Level
	msg   string
	attrs map[string]any
}

func (h *permLogHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *permLogHandler) Handle(_ context.Context, r slog.Record) error {
	attrs := make(map[string]any, r.NumAttrs())
	r.Attrs(func(a slog.Attr) bool {
		attrs[a.Key] = a.Value.Any()
		return true
	})
	h.mu.Lock()
	h.recs = append(h.recs, permLogRec{level: r.Level, msg: r.Message, attrs: attrs})
	h.mu.Unlock()
	return nil
}

func (h *permLogHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *permLogHandler) WithGroup(string) slog.Handler      { return h }

func (h *permLogHandler) find(msg string) []permLogRec {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []permLogRec
	for _, r := range h.recs {
		if r.msg == msg {
			out = append(out, r)
		}
	}
	return out
}

func capturePermLogs(t *testing.T) *permLogHandler {
	t.Helper()
	h := &permLogHandler{}
	prev := slog.Default()
	slog.SetDefault(slog.New(h))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return h
}

func TestAppendRulesFromFile_SkipsInvalidAndLogsAllow(t *testing.T) {
	logs := capturePermLogs(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	body := `{"permissions":{"deny":["Bash(rm *)","Read([)"],"ask":["Web"],"allow":["Bash(ls)","Read(*)"]}}`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	rules := appendRulesFromFile(nil, path, sourceUser, dir)
	if len(rules) != 2 {
		t.Fatalf("rules = %d (%+v), want 2 (the traversal pattern must be skipped)", len(rules), rules)
	}
	if rules[0].Action != ActionDeny || rules[0].Value.ToolName != "Bash" {
		t.Errorf("rules[0] = %+v, want the Bash deny rule", rules[0])
	}
	if rules[1].Action != ActionAsk || rules[1].Value.ToolName != "Web" {
		t.Errorf("rules[1] = %+v, want the Web ask rule", rules[1])
	}
	if rules[0].ConfigRoot != dir || rules[0].Source != string(sourceUser) {
		t.Errorf("rule provenance = %+v, want source %q and root %q", rules[0], sourceUser, dir)
	}
	recs := logs.find("skipping invalid deny rule")
	if len(recs) != 1 {
		t.Fatalf("invalid deny logged %d times, want 1", len(recs))
	}
	if recs[0].attrs["rule"] != "Read([)" {
		t.Errorf("rule attr = %v, want the traversal rule", recs[0].attrs["rule"])
	}
	if got := logs.find("gbot defaults allow; allow rule ignored"); len(got) != 2 {
		t.Errorf("allow rules logged %d times, want 2", len(got))
	}
}

func TestAppendRulesFromFile_SkipsInvalidAskRule(t *testing.T) {
	logs := capturePermLogs(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	body := `{"permissions":{"ask":["Write([)"]}}`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	rules := appendRulesFromFile(nil, path, sourceProject, dir)
	if len(rules) != 0 {
		t.Fatalf("rules = %+v, want none (the malformed pattern must be skipped)", rules)
	}
	if got := logs.find("skipping invalid ask rule"); len(got) != 1 {
		t.Errorf("invalid ask logged %d times, want 1", len(got))
	}
}

func TestAppendRulesFromFile_BadJSONAndMissing(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(bad, []byte("{not json"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if got := appendRulesFromFile(nil, bad, sourceUser, dir); len(got) != 0 {
		t.Errorf("rules from malformed JSON = %d, want 0", len(got))
	}
	if got := appendRulesFromFile(nil, filepath.Join(dir, "missing.json"), sourceUser, dir); len(got) != 0 {
		t.Errorf("rules from a missing file = %d, want 0", len(got))
	}
}

func TestReadPermJSONFile_Errors(t *testing.T) {
	if err := readPermJSONFile(filepath.Join(t.TempDir(), "nope.json"), &struct{}{}); err == nil {
		t.Fatal("a missing settings file returned no error")
	} else if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("missing-file error = %v, want it to wrap os.ErrNotExist", err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(path, []byte("[]"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := readPermJSONFile(path, &struct{}{}); err == nil {
		t.Error("a JSON array did not fail unmarshalling into a struct")
	}
}
