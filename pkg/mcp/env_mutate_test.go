package mcp

// Mutation-escape kill tests for env.go (mutate batch m2).
// Each test pins the behavior of one escaped mutant; verdicts and evidence
// live in /home/yliu/mutate-mcp2/ledger-mcp2.txt.

import (
	"os"
	"testing"
)

// env.go:26 numbers/incrementer (SplitN limit 2 → 3): the default-value split is
// capped at two parts, so a default may itself contain ":-". With limit 3 the
// content splits into three parts, len(parts) == 2 no longer holds, and the
// variable is reported missing instead of expanding to its default.
func TestExpandEnvVarsInString_DefaultKeepsSeparator(t *testing.T) {
	const name = "GBOT_MCP2_UNSET_WITH_DEFAULT"
	if err := os.Unsetenv(name); err != nil {
		t.Fatalf("Unsetenv(%s): %v", name, err)
	}

	expanded, missing := ExpandEnvVarsInString("${" + name + ":-a:-b}")
	if expanded != "a:-b" {
		t.Errorf("expanded = %q, want %q", expanded, "a:-b")
	}
	if len(missing) != 0 {
		t.Errorf("missingVars = %v, want none", missing)
	}
}
