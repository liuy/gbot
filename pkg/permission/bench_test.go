package permission

import (
	"encoding/json"
	"testing"
)

func BenchmarkCheckNoRules(b *testing.B) {
	c := NewChecker(nil)
	input := json.RawMessage(`{"command":"git status"}`)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		c.Check("Bash", input)
	}
}

func BenchmarkCheckWithDenyRules(b *testing.B) {
	content := "rm -rf *"
	c := NewChecker([]Rule{
		{Value: RuleValue{ToolName: "Bash", RuleContent: nil}, Action: ActionDeny, Source: "user"},
		{Value: RuleValue{ToolName: "Bash", RuleContent: &content}, Action: ActionDeny, Source: "user"},
		{Value: RuleValue{ToolName: "Write", RuleContent: nil}, Action: ActionAsk, Source: "user"},
	})
	input := json.RawMessage(`{"command":"git status"}`)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		c.Check("Bash", input)
	}
}

func BenchmarkCheckDenyMatch(b *testing.B) {
	c := NewChecker([]Rule{
		{Value: RuleValue{ToolName: "Bash", RuleContent: nil}, Action: ActionDeny, Source: "user"},
	})
	input := json.RawMessage(`{"command":"git status"}`)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		c.Check("Bash", input)
	}
}

func BenchmarkCheckBashPermission(b *testing.B) {
	denyPattern := "rm -rf *"
	askPattern := "git push *"
	rules := []Rule{
		{Value: RuleValue{ToolName: "Bash", RuleContent: &denyPattern}, Action: ActionDeny, Source: "user"},
		{Value: RuleValue{ToolName: "Bash", RuleContent: &askPattern}, Action: ActionAsk, Source: "user"},
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _, _ = CheckBashPermission("git status", rules)
	}
}

func BenchmarkParseShellCommand(b *testing.B) {
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = ParseShellCommand("git add . && npm run build || echo failed")
	}
}
