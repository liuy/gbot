package mcp

import (
	"sort"
	"testing"
)

// ---------------------------------------------------------------------------
// ExpandEnvVarsInString — Source: envExpansion.ts:10-38
// ---------------------------------------------------------------------------

func TestExpandEnvVarsInString_ExistingVar(t *testing.T) {
	t.Setenv("MCP_TEST_HOME", "/home/user")

	expanded, missing := ExpandEnvVarsInString("${MCP_TEST_HOME}")
	if expanded != "/home/user" {
		t.Errorf("expanded = %q, want %q", expanded, "/home/user")
	}
	if len(missing) != 0 {
		t.Errorf("missing = %v, want empty", missing)
	}
}

func TestExpandEnvVarsInString_MissingWithDefault(t *testing.T) {
	// Source: envExpansion.ts test — "${MISSING:-fallback}" → ("fallback", [])
	expanded, missing := ExpandEnvVarsInString("${MCP_TEST_MISSING_12345:-fallback}")
	if expanded != "fallback" {
		t.Errorf("expanded = %q, want %q", expanded, "fallback")
	}
	if len(missing) != 0 {
		t.Errorf("missing = %v, want empty", missing)
	}
}

func TestExpandEnvVarsInString_MissingNoDefault(t *testing.T) {
	// Source: envExpansion.ts test — "${MISSING}" → ("${MISSING}", ["MISSING"])
	expanded, missing := ExpandEnvVarsInString("${MCP_TEST_MISSING_99999}")
	if expanded != "${MCP_TEST_MISSING_99999}" {
		t.Errorf("expanded = %q, want original", expanded)
	}
	if len(missing) != 1 || missing[0] != "MCP_TEST_MISSING_99999" {
		t.Errorf("missing = %v, want [MCP_TEST_MISSING_99999]", missing)
	}
}

func TestExpandEnvVarsInString_MultipleVars(t *testing.T) {
	t.Setenv("MCP_TEST_A", "hello")
	t.Setenv("MCP_TEST_B", "world")

	expanded, missing := ExpandEnvVarsInString("${MCP_TEST_A} ${MCP_TEST_B}")
	if expanded != "hello world" {
		t.Errorf("expanded = %q, want %q", expanded, "hello world")
	}
	if len(missing) != 0 {
		t.Errorf("missing = %v, want empty", missing)
	}
}

func TestExpandEnvVarsInString_Mixed(t *testing.T) {
	t.Setenv("MCP_TEST_EXISTS", "yes")

	expanded, missing := ExpandEnvVarsInString("${MCP_TEST_EXISTS} ${MCP_TEST_GONE:-no} ${MCP_TEST_GONE}")
	if expanded != "yes no ${MCP_TEST_GONE}" {
		t.Errorf("expanded = %q, want %q", expanded, "yes no ${MCP_TEST_GONE}")
	}
	if len(missing) != 1 || missing[0] != "MCP_TEST_GONE" {
		t.Errorf("missing = %v, want [MCP_TEST_GONE]", missing)
	}
}

func TestExpandEnvVarsInString_EmptyString(t *testing.T) {
	expanded, missing := ExpandEnvVarsInString("")
	if expanded != "" {
		t.Errorf("expanded = %q, want empty", expanded)
	}
	if len(missing) != 0 {
		t.Errorf("missing = %v, want empty", missing)
	}
}

func TestExpandEnvVarsInString_NoVars(t *testing.T) {
	expanded, missing := ExpandEnvVarsInString("plain text")
	if expanded != "plain text" {
		t.Errorf("expanded = %q, want %q", expanded, "plain text")
	}
	if len(missing) != 0 {
		t.Errorf("missing = %v, want empty", missing)
	}
}

func TestExpandEnvVarsInString_DefaultWithColon(t *testing.T) {
	// Edge case: default value contains colons
	expanded, missing := ExpandEnvVarsInString("${MCP_TEST_NOCOLON:-http://localhost:8080}")
	if expanded != "http://localhost:8080" {
		t.Errorf("expanded = %q, want %q", expanded, "http://localhost:8080")
	}
	if len(missing) != 0 {
		t.Errorf("missing = %v, want empty", missing)
	}
}

func TestExpandEnvVarsInString_EmptyEnvVar(t *testing.T) {
	// TS: process.env returns "" for empty string, which is !== undefined → use it
	t.Setenv("MCP_TEST_EMPTY", "")

	expanded, missing := ExpandEnvVarsInString("${MCP_TEST_EMPTY:-fallback}")
	if expanded != "" {
		t.Errorf("expanded = %q, want empty string (env exists but is empty)", expanded)
	}
	if len(missing) != 0 {
		t.Errorf("missing = %v, want empty", missing)
	}
}

func TestExpandEnvVarsInString_MultipleMissing(t *testing.T) {
	_, missing := ExpandEnvVarsInString("${MCP_TEST_MISS_A} ${MCP_TEST_MISS_B}")
	sort.Strings(missing)
	want := []string{"MCP_TEST_MISS_A", "MCP_TEST_MISS_B"}
	if len(missing) != len(want) {
		t.Fatalf("missing = %v, want %v", missing, want)
	}
	for i, w := range want {
		if missing[i] != w {
			t.Errorf("missing[%d] = %q, want %q", i, missing[i], w)
		}
	}
}
