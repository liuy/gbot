package permission

import (
	"encoding/json"
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
