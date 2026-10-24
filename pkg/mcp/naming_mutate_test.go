package mcp

// Mutation-escape kill tests for naming.go. Each test pins a behavior that a
// specific escaped mutant breaks; assertions are exact on purpose.

import (
	"testing"
)

// 120 numbers/incrementer: GetMcpDisplayName strips the mcp__server__ prefix
// exactly once (Replace count 1). A tool name that itself contains the prefix
// substring must keep its inner occurrence.
func TestGetMcpDisplayName_ReplacesOnlyFirstPrefix(t *testing.T) {
	t.Parallel()
	// fullName = "mcp__my__" + "mcp__my__tool": the second occurrence is part
	// of the display name and must survive.
	got := GetMcpDisplayName("mcp__my__mcp__my__tool", "my")
	if got != "mcp__my__tool" {
		t.Fatalf("GetMcpDisplayName must strip only the first prefix, got %q, want %q", got, "mcp__my__tool")
	}
}

// 133 statement/remove: the TrimSpace after (MCP)-suffix removal is
// unconditional (mcpStringUtils.ts:98) — it must also trim whitespace on
// names that never had a (MCP) suffix, where the suffix regex does not match
// and leaves the whitespace in place.
func TestExtractMcpToolDisplayName_TrimsWithoutSuffix(t *testing.T) {
	t.Parallel()
	cases := []struct {
		input string
		want  string
	}{
		{"  read file  ", "read file"},
		{"\tgithub - Create PR\n", "Create PR"},
	}
	for _, c := range cases {
		if got := ExtractMcpToolDisplayName(c.input); got != c.want {
			t.Errorf("ExtractMcpToolDisplayName(%q) = %q, want %q", c.input, got, c.want)
		}
	}
}
