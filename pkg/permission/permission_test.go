package permission

import (
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
)

func TestCheckerNoRules(t *testing.T) {
	c := NewChecker(nil)
	dec := c.Check("Bash", json.RawMessage(`{"command":"rm -rf /"}`))
	if dec.Action != ActionAllow {
		t.Errorf("got %v, want Allow", dec.Action)
	}
	if len(dec.ContentRules) != 0 {
		t.Errorf("got %d content rules, want 0", len(dec.ContentRules))
	}
}

func TestCheckerDenyBareToolName(t *testing.T) {
	c := NewChecker([]Rule{
		{Value: RuleValue{ToolName: "Bash", RuleContent: nil}, Action: ActionDeny, Source: "user"},
	})
	dec := c.Check("Bash", json.RawMessage(`{"command":"git status"}`))
	if dec.Action != ActionDeny {
		t.Errorf("got %v, want Deny", dec.Action)
	}
}

func TestCheckerDenyContentRulesPassthrough(t *testing.T) {
	content := "rm -rf *"
	c := NewChecker([]Rule{
		{Value: RuleValue{ToolName: "Bash", RuleContent: &content}, Action: ActionDeny, Source: "user"},
	})
	dec := c.Check("Bash", json.RawMessage(`{"command":"git status"}`))
	if dec.Action != ActionAllow {
		t.Errorf("got %v, want Allow (content rules passthrough)", dec.Action)
	}
	if len(dec.ContentRules) != 1 {
		t.Fatalf("got %d content rules, want 1", len(dec.ContentRules))
	}
	if *dec.ContentRules[0].Value.RuleContent != "rm -rf *" {
		t.Errorf("content rule: got %q, want %q", *dec.ContentRules[0].Value.RuleContent, "rm -rf *")
	}
}

func TestCheckerDenyBeforeAsk(t *testing.T) {
	c := NewChecker([]Rule{
		{Value: RuleValue{ToolName: "Bash", RuleContent: nil}, Action: ActionAsk, Source: "user"},
		{Value: RuleValue{ToolName: "Bash", RuleContent: nil}, Action: ActionDeny, Source: "project"},
	})
	dec := c.Check("Bash", json.RawMessage(`{"command":"ls"}`))
	if dec.Action != ActionDeny {
		t.Errorf("got %v, want Deny (deny always wins)", dec.Action)
	}
}

func TestCheckerAskBareToolName(t *testing.T) {
	c := NewChecker([]Rule{
		{Value: RuleValue{ToolName: "Write", RuleContent: nil}, Action: ActionAsk, Source: "user"},
	})
	dec := c.Check("Write", json.RawMessage(`{"file_path":"test.go"}`))
	if dec.Action != ActionAsk {
		t.Errorf("got %v, want Ask", dec.Action)
	}
}

func TestCheckerMCPWildcard(t *testing.T) {
	c := NewChecker([]Rule{
		{Value: RuleValue{ToolName: "mcp__server__*", RuleContent: nil}, Action: ActionDeny, Source: "user"},
	})
	dec := c.Check("mcp__server__delete", json.RawMessage(`{}`))
	if dec.Action != ActionDeny {
		t.Errorf("got %v, want Deny for MCP wildcard match", dec.Action)
	}
	dec = c.Check("mcp__other__tool", json.RawMessage(`{}`))
	if dec.Action != ActionAllow {
		t.Errorf("got %v, want Allow for non-matching MCP tool", dec.Action)
	}
}

func TestCheckerContentRulesForTool(t *testing.T) {
	denyContent := "rm -rf *"
	askContent := "git push *"
	c := NewChecker([]Rule{
		{Value: RuleValue{ToolName: "Bash", RuleContent: &denyContent}, Action: ActionDeny, Source: "user"},
		{Value: RuleValue{ToolName: "Bash", RuleContent: &askContent}, Action: ActionAsk, Source: "user"},
		{Value: RuleValue{ToolName: "Bash", RuleContent: nil}, Action: ActionDeny, Source: "project"},
	})
	rules := c.ContentRulesForTool("Bash")
	if len(rules) != 2 {
		t.Fatalf("got %d content rules, want 2", len(rules))
	}
}

func TestCheckerHasRules(t *testing.T) {
	c := NewChecker(nil)
	if c.HasRules() {
		t.Error("empty checker should not have rules")
	}
	c = NewChecker([]Rule{
		{Value: RuleValue{ToolName: "Bash"}, Action: ActionDeny},
	})
	if !c.HasRules() {
		t.Error("checker with rules should report HasRules")
	}
}

func TestCheckBashPermission(t *testing.T) {
	denyPattern := "rm -rf *"
	askPattern := "git push *"
	contentRules := []Rule{
		{Value: RuleValue{ToolName: "Bash", RuleContent: &denyPattern}, Action: ActionDeny, Source: "user"},
		{Value: RuleValue{ToolName: "Bash", RuleContent: &askPattern}, Action: ActionAsk, Source: "user"},
	}

	tests := []struct {
		name    string
		command string
		want    RuleAction
	}{
		{name: "deny matches", command: "rm -rf /", want: ActionDeny},
		{name: "ask matches", command: "git push origin main", want: ActionAsk},
		{name: "no match - allow", command: "git status", want: ActionAllow},
		{name: "deny via compound", command: "echo hi && rm -rf /tmp", want: ActionDeny},
		{name: "deny via env var bypass", command: "FOO=bar rm -rf /", want: ActionDeny},
		{name: "deny via wrapper bypass", command: "timeout 10 rm -rf /", want: ActionDeny},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			action, _, err := CheckBashPermission(tt.command, contentRules)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if action != tt.want {
				t.Errorf("got %v, want %v", action, tt.want)
			}
		})
	}
}

func TestCheckBashPermissionNoRules(t *testing.T) {
	action, _, err := CheckBashPermission("rm -rf /", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if action != ActionAllow {
		t.Errorf("got %v, want Allow with no rules", action)
	}
}

func TestCheckFilePermission(t *testing.T) {
	denyPattern := "*.json"
	contentRules := []Rule{
		{Value: RuleValue{ToolName: "Write", RuleContent: &denyPattern}, Action: ActionDeny, Source: "user"},
	}

	tests := []struct {
		name     string
		filePath string
		want     RuleAction
	}{
		{name: "deny matches", filePath: "settings.json", want: ActionDeny},
		{name: "no match - allow", filePath: "main.go", want: ActionAllow},
		{name: "dangerous file - ask", filePath: ".bashrc", want: ActionAsk},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			action, _, err := CheckFilePermission(tt.filePath, contentRules)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if action != tt.want {
				t.Errorf("got %v, want %v", action, tt.want)
			}
		})
	}
}

func TestMCPInfoFromString(t *testing.T) {
	tests := []struct {
		input   string
		want    *MCPInfo
		wantNil bool
	}{
		{input: "mcp__server__tool", want: &MCPInfo{Server: "server", Tool: "tool"}},
		{input: "mcp__myserver__delete_item", want: &MCPInfo{Server: "myserver", Tool: "delete_item"}},
		{input: "Bash", wantNil: true},
		{input: "mcp__server", wantNil: true},
		{input: "mcp__", wantNil: true},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := MCPInfoFromString(tt.input)
			if tt.wantNil {
				if got != nil {
					t.Error("expected nil, got non-nil")
				}
				return
			}
			if got == nil {
				t.Fatal("expected non-nil, got nil")
			}
			if got.Server != tt.want.Server || got.Tool != tt.want.Tool {
				t.Errorf("got {Server:%q, Tool:%q}, want {Server:%q, Tool:%q}",
					got.Server, got.Tool, tt.want.Server, tt.want.Tool)
			}
		})
	}
}

func TestExtractBashCommand(t *testing.T) {
	input := json.RawMessage(`{"command":"git status","timeout":5000}`)
	got := ExtractBashCommand(input)
	if got != "git status" {
		t.Errorf("got %q, want %q", got, "git status")
	}
}

func TestExtractFilePath(t *testing.T) {
	input := json.RawMessage(`{"file_path":"/tmp/test.go"}`)
	got := ExtractFilePath(input)
	if got != "/tmp/test.go" {
		t.Errorf("got %q, want %q", got, "/tmp/test.go")
	}
}

func TestSanitizeForLog(t *testing.T) {
	tests := []struct {
		input, want string
	}{
		{input: "hello\nworld", want: "hello\\nworld"},
		{input: "tab\there", want: "tab\\there"},
		{input: "cr\rhere", want: "cr\\rhere"},
		{input: "clean", want: "clean"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := sanitizeForLog(tt.input)
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestCheckerMCPBareServerDeny(t *testing.T) {
	c := NewChecker([]Rule{
		{Value: RuleValue{ToolName: "mcp__evil"}, Action: ActionDeny, Source: "user"},
	})
	dec := c.Check("mcp__evil__delete", json.RawMessage(`{}`))
	if dec.Action != ActionDeny {
		t.Errorf("got %v, want Deny for MCP bare server match", dec.Action)
	}
	if !strings.Contains(dec.Message, "server-level") {
		t.Errorf("message should mention server-level, got %q", dec.Message)
	}
	dec = c.Check("mcp__safe__tool", json.RawMessage(`{}`))
	if dec.Action != ActionAllow {
		t.Errorf("got %v, want Allow for different MCP server", dec.Action)
	}
}

func TestCheckerMCPBareServerAsk(t *testing.T) {
	c := NewChecker([]Rule{
		{Value: RuleValue{ToolName: "mcp__askserver"}, Action: ActionAsk, Source: "user"},
	})
	dec := c.Check("mcp__askserver__tool1", json.RawMessage(`{}`))
	if dec.Action != ActionAsk {
		t.Errorf("got %v, want Ask for MCP bare server ask", dec.Action)
	}
}

func TestCheckerWildcardAsk(t *testing.T) {
	c := NewChecker([]Rule{
		{Value: RuleValue{ToolName: "mcp__srv__*", RuleContent: nil}, Action: ActionAsk, Source: "user"},
	})
	dec := c.Check("mcp__srv__tool", json.RawMessage(`{}`))
	if dec.Action != ActionAsk {
		t.Errorf("got %v, want Ask for MCP wildcard ask", dec.Action)
	}
}

func TestCheckerWildcardContentRules(t *testing.T) {
	content := "delete *"
	c := NewChecker([]Rule{
		{Value: RuleValue{ToolName: "mcp__srv__*", RuleContent: &content}, Action: ActionDeny, Source: "user"},
	})
	rules := c.ContentRulesForTool("mcp__srv__tool")
	if len(rules) != 1 {
		t.Fatalf("got %d content rules for wildcard tool, want 1", len(rules))
	}
	if *rules[0].Value.RuleContent != "delete *" {
		t.Errorf("content rule: got %q, want %q", *rules[0].Value.RuleContent, "delete *")
	}
}

func TestNewCheckerAllowRulesIgnored(t *testing.T) {
	c := NewChecker([]Rule{
		{Value: RuleValue{ToolName: "Bash"}, Action: ActionAllow, Source: "user"},
	})
	dec := c.Check("Bash", json.RawMessage(`{}`))
	if dec.Action != ActionAllow {
		t.Errorf("got %v, want Allow since allow rules are not enforced", dec.Action)
	}
}

func TestMatchShellWithXargs(t *testing.T) {
	rule := ParseShellRule("rm *")
	tests := []struct {
		cmd  string
		want bool
	}{
		{cmd: "rm -rf /", want: true},
		{cmd: "xargs rm -rf /", want: true},
		{cmd: "xargs git status", want: false},
		{cmd: "git status", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.cmd, func(t *testing.T) {
			got := matchShellWithXargs(rule, tt.cmd)
			if got != tt.want {
				t.Errorf("matchShellWithXargs(%q) = %v, want %v", tt.cmd, got, tt.want)
			}
		})
	}
}

func TestCheckBashPermissionParseError(t *testing.T) {
	content := "rm *"
	contentRules := []Rule{
		{Value: RuleValue{ToolName: "Bash", RuleContent: &content}, Action: ActionDeny, Source: "user"},
	}
	action, _, err := CheckBashPermission("echo \xff", contentRules)
	if err == nil {
		t.Fatal("expected error for malformed shell input")
	}
	if !strings.Contains(err.Error(), "shell parse") {
		t.Errorf("error should mention shell parse, got: %v", err)
	}
	if action != ActionDeny {
		t.Errorf("got %v, want Deny on parse error (fail-secure)", action)
	}
}

func TestCheckFilePermissionNoRules(t *testing.T) {
	action, _, err := CheckFilePermission("test.go", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if action != ActionAllow {
		t.Errorf("got %v, want Allow with no rules", action)
	}
}

func TestCheckFilePermissionPathTraversal(t *testing.T) {
	content := "*.json"
	contentRules := []Rule{
		{Value: RuleValue{ToolName: "Write", RuleContent: &content}, Action: ActionDeny, Source: "user"},
	}
	action, _, err := CheckFilePermission("../../etc/passwd", contentRules)
	if err == nil {
		t.Fatal("expected error for path traversal")
	}
	if !strings.Contains(err.Error(), "..") {
		t.Errorf("error should mention path traversal, got: %v", err)
	}
	if action != ActionDeny {
		t.Errorf("got %v, want Deny for path traversal", action)
	}
}

func TestCheckFilePermissionMatchError(t *testing.T) {
	badPattern := "[invalid"
	contentRules := []Rule{
		{Value: RuleValue{ToolName: "Write", RuleContent: &badPattern}, Action: ActionDeny, Source: "user", ConfigRoot: "/tmp"},
	}
	action, _, err := CheckFilePermission("test.go", contentRules)
	if err == nil {
		t.Fatal("expected error for invalid pattern")
	}
	if !strings.Contains(err.Error(), "pattern") {
		t.Errorf("error should mention pattern, got: %v", err)
	}
	if action != ActionDeny {
		t.Errorf("got %v, want Deny on pattern match error (fail-secure)", action)
	}
}

func TestExtractBashCommandInvalidJSON(t *testing.T) {
	got := ExtractBashCommand(json.RawMessage(`{invalid`))
	if got != "" {
		t.Errorf("got %q, want empty string for invalid JSON", got)
	}
}

func TestExtractFilePathInvalidJSON(t *testing.T) {
	got := ExtractFilePath(json.RawMessage(`{invalid`))
	if got != "" {
		t.Errorf("got %q, want empty string for invalid JSON", got)
	}
}

func TestMatchToolWildcard(t *testing.T) {
	tests := []struct {
		pattern, toolName string
		want              bool
	}{
		{pattern: "*", toolName: "anything", want: true},
		{pattern: "Bash", toolName: "Bash", want: true},
		{pattern: "Bash", toolName: "Write", want: false},
		{pattern: "mcp__*", toolName: "mcp__server_tool", want: true},
		{pattern: "mcp__*", toolName: "Bash", want: false},
		{pattern: "mcp__srv__*", toolName: "mcp__srv__tool", want: true},
		{pattern: "mcp__srv__*", toolName: "mcp__other__tool", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.pattern+"/"+tt.toolName, func(t *testing.T) {
			got := matchToolWildcard(tt.pattern, tt.toolName)
			if got != tt.want {
				t.Errorf("matchToolWildcard(%q, %q) = %v, want %v", tt.pattern, tt.toolName, got, tt.want)
			}
		})
	}
}

func TestRegisterContentChecker(t *testing.T) {
	var called bool
	RegisterContentChecker("TestTool_content", func(input json.RawMessage, contentRules []Rule) RuleAction {
		called = true
		return ActionDeny
	})
	defer func() {
		delete(contentCheckers, "TestTool_content")
	}()

	action := CheckContent("TestTool_content", json.RawMessage(`{}`), nil)
	if !called {
		t.Error("expected content checker to be called")
	}
	if action != ActionDeny {
		t.Errorf("got %v, want Deny", action)
	}

	action = CheckContent("UnknownTool", json.RawMessage(`{}`), nil)
	if action != ActionAllow {
		t.Errorf("got %v, want Allow for unregistered tool", action)
	}
}

func TestCheckBashPermissionNilRuleContent(t *testing.T) {
	contentRules := []Rule{
		{Value: RuleValue{ToolName: "Bash", RuleContent: nil}, Action: ActionDeny, Source: "user"},
	}
	action, _, err := CheckBashPermission("rm -rf /", contentRules)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if action != ActionAllow {
		t.Errorf("got %v, want Allow — nil RuleContent rules are skipped", action)
	}
}

func TestCheckFilePermissionNilRuleContent(t *testing.T) {
	contentRules := []Rule{
		{Value: RuleValue{ToolName: "Write", RuleContent: nil}, Action: ActionDeny, Source: "user"},
	}
	action, _, err := CheckFilePermission("test.go", contentRules)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if action != ActionAllow {
		t.Errorf("got %v, want Allow — nil RuleContent rules are skipped", action)
	}
}

func TestContentRulesForToolWildcardWithContent(t *testing.T) {
	askContent := "sensitive *"
	c := NewChecker([]Rule{
		{Value: RuleValue{ToolName: "mcp__srv__*", RuleContent: &askContent}, Action: ActionAsk, Source: "user"},
	})
	rules := c.ContentRulesForTool("mcp__srv__tool")
	if len(rules) != 1 {
		t.Fatalf("got %d wildcard content rules, want 1", len(rules))
	}
	if rules[0].Action != ActionAsk {
		t.Errorf("got action %v, want Ask", rules[0].Action)
	}
}

func TestCheckBashPermissionBothStripped(t *testing.T) {
	content := "rm -rf *"
	contentRules := []Rule{
		{Value: RuleValue{ToolName: "Bash", RuleContent: &content}, Action: ActionDeny, Source: "user"},
	}
	action, _, err := CheckBashPermission("PATH=/evil timeout 10 rm -rf /", contentRules)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if action != ActionDeny {
		t.Errorf("got %v, want Deny for env+wrapper stripped command", action)
	}
}

func TestExtractContentPatternNoAskRules(t *testing.T) {
	// Register a mock content checker that returns deny
	RegisterContentChecker("TestTool_Deny", func(input json.RawMessage, contentRules []Rule) RuleAction {
		return ActionDeny
	})
	defer func() {
		delete(contentCheckers, "TestTool_Deny")
	}()

	denyContent := "rm -rf *"
	contentRules := []Rule{
		{Value: RuleValue{ToolName: "TestTool_Deny", RuleContent: &denyContent}, Action: ActionDeny, Source: "user"},
	}
	got := ExtractContentPattern("TestTool_Deny", json.RawMessage(`{}`), contentRules)
	if got != "" {
		t.Errorf("got %q, want empty string when CheckContent returns non-ask", got)
	}
}

func TestExtractContentPatternAskRuleMatches(t *testing.T) {
	// Register a mock content checker that returns ask
	RegisterContentChecker("TestTool_Ask", func(input json.RawMessage, contentRules []Rule) RuleAction {
		return ActionAsk
	})
	defer func() {
		delete(contentCheckers, "TestTool_Ask")
	}()

	askContent := "rm -rf *"
	contentRules := []Rule{
		{Value: RuleValue{ToolName: "TestTool_Ask", RuleContent: &askContent}, Action: ActionAsk, Source: "user"},
	}
	got := ExtractContentPattern("TestTool_Ask", json.RawMessage(`{}`), contentRules)
	want := "TestTool_Ask(rm -rf *)"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestExtractContentPatternAskRuleWithoutRuleContent(t *testing.T) {
	// Register a mock content checker that returns ask
	RegisterContentChecker("TestTool_BareAsk", func(input json.RawMessage, contentRules []Rule) RuleAction {
		return ActionAsk
	})
	defer func() {
		delete(contentCheckers, "TestTool_BareAsk")
	}()

	contentRules := []Rule{
		{Value: RuleValue{ToolName: "TestTool_BareAsk", RuleContent: nil}, Action: ActionAsk, Source: "user"},
	}
	got := ExtractContentPattern("TestTool_BareAsk", json.RawMessage(`{}`), contentRules)
	want := "TestTool_BareAsk"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestExtractContentPatternMultipleAskRules(t *testing.T) {
	// Register a mock content checker that returns ask
	RegisterContentChecker("TestTool_MultiAsk", func(input json.RawMessage, contentRules []Rule) RuleAction {
		return ActionAsk
	})
	defer func() {
		delete(contentCheckers, "TestTool_MultiAsk")
	}()

	askContent1 := "rm -rf *"
	askContent2 := "git push *"
	contentRules := []Rule{
		{Value: RuleValue{ToolName: "TestTool_MultiAsk", RuleContent: &askContent1}, Action: ActionAsk, Source: "user"},
		{Value: RuleValue{ToolName: "TestTool_MultiAsk", RuleContent: &askContent2}, Action: ActionAsk, Source: "user"},
	}
	got := ExtractContentPattern("TestTool_MultiAsk", json.RawMessage(`{}`), contentRules)
	want := "TestTool_MultiAsk(rm -rf *)"
	if got != want {
		t.Errorf("got %q, want %q (first match should be returned)", got, want)
	}
}

func TestExtractContentPatternCheckContentAskButNoAskRules(t *testing.T) {
	// Register a mock content checker that returns ask
	RegisterContentChecker("TestTool_AskNoRules", func(input json.RawMessage, contentRules []Rule) RuleAction {
		return ActionAsk
	})
	defer func() {
		delete(contentCheckers, "TestTool_AskNoRules")
	}()

	// Empty rules - CheckContent returns ask but no ask rules exist
	contentRules := []Rule{}
	got := ExtractContentPattern("TestTool_AskNoRules", json.RawMessage(`{}`), contentRules)
	if got != "" {
		t.Errorf("got %q, want empty string when CheckContent returns ask but no ask rules exist", got)
	}
}

func TestDefaultAllowDecisionValue(t *testing.T) {
	if DefaultAllow.Action != ActionAllow {
		t.Errorf("DefaultAllow.Action = %q, want allow", DefaultAllow.Action)
	}
	if DefaultAllow.Message != "" || DefaultAllow.Rule != nil || DefaultAllow.ContentRules != nil {
		t.Errorf("DefaultAllow = %+v, want only the action set", DefaultAllow)
	}
}

func TestCheckBashPermission_WrappersAndXargs(t *testing.T) {
	content := func(s string) *string { p := &s; return p }
	deny := Rule{Value: RuleValue{ToolName: "Bash", RuleContent: content("rm *")}, Action: ActionDeny, Source: "user"}
	ask := Rule{Value: RuleValue{ToolName: "Bash", RuleContent: content("git push*")}, Action: ActionAsk, Source: "user"}
	rules := []Rule{deny, ask}
	for _, cmd := range []string{"rm -rf /", "FOO=1 rm -rf /", "timeout 5 rm -rf /"} {
		action, rule, err := CheckBashPermission(cmd, rules)
		if err != nil {
			t.Errorf("CheckBashPermission(%q): %v", cmd, err)
			continue
		}
		if action != ActionDeny || rule == nil || rule.Value.RuleContent == nil || *rule.Value.RuleContent != "rm *" {
			t.Errorf("CheckBashPermission(%q) = (%q,%v), want deny by the rm rule", cmd, action, rule)
		}
	}
	action, rule, err := CheckBashPermission("git push origin main", rules)
	if err != nil || action != ActionAsk || rule == nil {
		t.Errorf("git push = (%q,%v,%v), want ask by a rule", action, rule, err)
	}
	action, rule, err = CheckBashPermission("ls -la", rules)
	if err != nil || action != ActionAllow || rule != nil {
		t.Errorf("ls = (%q,%v,%v), want allow with no rule", action, rule, err)
	}
	// xargs must not smuggle a denied command past the matcher.
	action, _, err = CheckBashPermission("echo rm | xargs rm", rules)
	if err != nil {
		t.Fatalf("xargs command: %v", err)
	}
	if action != ActionDeny {
		t.Errorf("xargs-wrapped deny command action = %q, want deny", action)
	}
	_, _, err = CheckBashPermission("if true; then", rules)
	if err == nil || !strings.HasPrefix(err.Error(), "shell parse error: ") {
		t.Errorf("unparsable command error = %v, want the shell-parse envelope", err)
	}
}

func TestCheckFilePermission_DangerousAndRules(t *testing.T) {
	content := func(s string) *string { p := &s; return p }
	rules := []Rule{{Value: RuleValue{ToolName: "Edit", RuleContent: content("src/**")}, Action: ActionAsk, Source: "user"}}
	action, rule, err := CheckFilePermission("src/a.go", rules)
	if err != nil || action != ActionAsk || rule == nil {
		t.Errorf("src/a.go = (%q,%v,%v), want ask by a rule", action, rule, err)
	}
	action, rule, err = CheckFilePermission("other/a.go", rules)
	if err != nil || action != ActionAllow || rule != nil {
		t.Errorf("other/a.go = (%q,%v,%v), want allow", action, rule, err)
	}
	action, rule, err = CheckFilePermission(".git/config", rules)
	if err != nil || action != ActionAsk || rule != nil {
		t.Errorf(".git/config = (%q,%v,%v), want ask with no rule (danger-file guard)", action, rule, err)
	}
}

func TestExtractHelpers(t *testing.T) {
	if got := ExtractBashCommand(json.RawMessage(`{"command":"ls -la"}`)); got != "ls -la" {
		t.Errorf("ExtractBashCommand = %q, want ls -la", got)
	}
	if got := ExtractBashCommand(json.RawMessage(`{"cmd":1}`)); got != "" {
		t.Errorf("ExtractBashCommand on a non-string = %q, want empty", got)
	}
	if got := ExtractBashCommand(json.RawMessage(`not json`)); got != "" {
		t.Errorf("ExtractBashCommand on bad JSON = %q, want empty", got)
	}
	if got := ExtractFilePath(json.RawMessage(`{"file_path":"/tmp/a.go"}`)); got != "/tmp/a.go" {
		t.Errorf("ExtractFilePath = %q", got)
	}
	if got := ExtractFilePath(json.RawMessage(`[1]`)); got != "" {
		t.Errorf("ExtractFilePath on an array = %q, want empty", got)
	}
	content := func(s string) *string { p := &s; return p }
	RegisterContentChecker("ProbeTool", func(_ json.RawMessage, rules []Rule) RuleAction {
		for _, r := range rules {
			if r.Action == ActionDeny {
				return ActionDeny
			}
		}
		for _, r := range rules {
			if r.Action == ActionAsk {
				return ActionAsk
			}
		}
		return ActionAllow
	})
	rules := []Rule{
		{Value: RuleValue{ToolName: "ProbeTool", RuleContent: content("rm *")}, Action: ActionDeny},
		{Value: RuleValue{ToolName: "ProbeTool", RuleContent: content("git *")}, Action: ActionAsk},
	}
	if got := ExtractContentPattern("ProbeTool", json.RawMessage(`{"command":"git push"}`), rules[1:]); got != "ProbeTool(git *)" {
		t.Errorf("ExtractContentPattern = %q, want the first ask rule rendered", got)
	}
	if got := ExtractContentPattern("ProbeTool", json.RawMessage(`{"command":"git push"}`), nil); got != "" {
		t.Errorf("ExtractContentPattern with no rules = %q, want empty", got)
	}
	if got := ExtractContentPattern("UnregisteredTool", json.RawMessage(`{"command":"git push"}`), rules); got != "" {
		t.Errorf("ExtractContentPattern for an unregistered tool = %q, want empty", got)
	}
	if got := CheckContent("ProbeTool", json.RawMessage(`{"command":"git push"}`), rules[:1]); got != ActionDeny {
		t.Errorf("CheckContent = %q, want deny", got)
	}
}

func TestMatchToolWildcard_Table(t *testing.T) {
	for _, tc := range []struct {
		pattern, tool string
		want          bool
	}{
		{"*", "Bash", true},
		{"Web", "Web", true},
		{"Web", "Grep", false},
		{"mcp__*", "mcp__github__create", true},
		{"mcp__*", "Bash", false},
		{"mcp__github__*", "mcp__github__x", true},
		{"*Edit", "NotebookEdit", false},
	} {
		if got := matchToolWildcard(tc.pattern, tc.tool); got != tc.want {
			t.Errorf("matchToolWildcard(%q,%q) = %v, want %v", tc.pattern, tc.tool, got, tc.want)
		}
	}
}

func TestMCPInfoFromString_Shape(t *testing.T) {
	info := MCPInfoFromString("mcp__github__create_issue")
	if info == nil || info.Server != "github" || info.Tool != "create_issue" {
		t.Fatalf("MCPInfoFromString = %+v, want github/create_issue", info)
	}
	if got := MCPInfoFromString("mcp__github"); got != nil {
		t.Errorf("MCPInfoFromString(mcp__github) = %+v, want nil", got)
	}
	if got := MCPInfoFromString("Bash"); got != nil {
		t.Errorf("MCPInfoFromString(Bash) = %+v, want nil", got)
	}
	if got := MCPInfoFromString("mcp____"); got == nil || got.Server != "" || got.Tool != "" {
		t.Errorf("MCPInfoFromString(mcp____) = %+v, want empty server and tool", got)
	}
}

func TestAuditLog_SanitizesAndCarriesRule(t *testing.T) {
	logs := capturePermLogs(t)
	content := func(s string) *string { p := &s; return p }
	rule := &Rule{Value: RuleValue{ToolName: "Bash", RuleContent: content("rm *")}, Action: ActionDeny, Source: "user"}
	auditLog("deny", "Bash", "rm -rf /\nsecond line", rule)
	recs := logs.find("permission decision")
	if len(recs) != 1 {
		t.Fatalf("decision logged %d times, want 1", len(recs))
	}
	r := recs[0]
	if r.level != slog.LevelWarn {
		t.Errorf("level = %v, want WARN", r.level)
	}
	if r.attrs["action"] != "deny" || r.attrs["tool"] != "Bash" {
		t.Errorf("attrs = %v, want action deny and tool Bash", r.attrs)
	}
	detail, _ := r.attrs["detail"].(string)
	if detail != `rm -rf /\nsecond line` {
		t.Errorf("detail = %q, want the newline escaped", detail)
	}
	if r.attrs["rule_source"] != "user" || r.attrs["rule_action"] != "deny" {
		t.Errorf("rule attrs = %v/%v, want user/deny", r.attrs["rule_source"], r.attrs["rule_action"])
	}
	auditLog("allow", "Gre\nen", "de\ntail", nil)
	recs = logs.find("permission decision")
	if len(recs) != 2 {
		t.Fatalf("decision logged %d times, want 2", len(recs))
	}
	if got, _ := recs[1].attrs["tool"].(string); got != `Gre\nen` {
		t.Errorf("tool attr = %q, want the sanitized tool name", got)
	}
	if _, ok := recs[1].attrs["rule_source"]; ok {
		t.Error("a rule-less decision logged rule provenance")
	}
}
