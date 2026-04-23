package mcp

import (
	"testing"
)

// ---------------------------------------------------------------------------
// NormalizeNameForMCP — Source: normalization.ts:17-23
// ---------------------------------------------------------------------------

func TestNormalizeNameForMCP(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		// Basic normalization — Source: normalization.ts:18
		{"basic", "my tool", "my_tool"},
		{"dot", "my.server", "my_server"},
		{"already_valid", "my_server", "my_server"},
		{"hyphen", "my-server", "my-server"},
		{"alphanumeric", "server123", "server123"},

		// CJK — non-[a-zA-Z0-9_-] replaced with _
		{"cjk", "测试工具", "____"},

		// Claude.ai prefix — collapse consecutive _, strip leading/trailing _
		// Source: normalization.ts:19-21
		{"claudeai_basic", "claude.ai My Server", "claude_ai_My_Server"},
		{"claudeai_double_space", "claude.ai  Server", "claude_ai_Server"},
		{"claudeai_with_dots", "claude.ai my.server", "claude_ai_my_server"},

		// NOT claude.ai prefix — no collapse
		{"not_claudeai_spaces", "my  server", "my__server"},
		{"not_claudeai_leading_dot", ".hidden", "_hidden"},

		// Edge cases
		{"empty", "", ""},
		{"single_char", "a", "a"},
		{"all_special", "!@#$%", "_____"},
		{"underscore_only", "_", "_"},
		{"leading_underscore", "_server", "_server"},
		{"trailing_underscore", "server_", "server_"},
		{"claudeai_leading_trailing", "claude.ai _server_", "claude_ai_server"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := NormalizeNameForMCP(tt.input)
			if got != tt.want {
				t.Errorf("NormalizeNameForMCP(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// McpInfoFromString — Source: mcpStringUtils.ts:19-32
// ---------------------------------------------------------------------------

func TestMcpInfoFromString(t *testing.T) {
	tests := []struct {
		name       string
		input      string
		wantServer string
		wantTool   string
		wantNil    bool
	}{
		// Source: mcpStringUtils.ts:19-31
		{"basic", "mcp__server__tool", "server", "tool", false},
		{"double_underscore_in_tool", "mcp__a__b__c", "a", "b__c", false},
		{"single_tool", "mcp__server__read", "server", "read", false},

		// Edge cases — should return nil
		{"empty", "", "", "", true},
		{"just_mcp", "mcp", "", "", true},
		{"mcp_double", "mcp__", "", "", true},
		{"mcp_server_only", "mcp__server", "server", "", false}, // no tool part → empty toolName, NOT nil
		{"mcp_server_trailing", "mcp__server__", "server", "", false},
		{"no_mcp_prefix", "server__tool", "", "", true},
		{"wrong_prefix", "mcpx__server__tool", "", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			info := McpInfoFromString(tt.input)
			if tt.wantNil {
				if info != nil {
					t.Errorf("McpInfoFromString(%q) = %+v, want nil", tt.input, info)
				}
				return
			}
			if info == nil {
				t.Fatalf("McpInfoFromString(%q) = nil, want non-nil", tt.input)
			}
			if info.ServerName != tt.wantServer {
				t.Errorf("ServerName = %q, want %q", info.ServerName, tt.wantServer)
			}
			if info.ToolName != tt.wantTool {
				t.Errorf("ToolName = %q, want %q", info.ToolName, tt.wantTool)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// BuildMcpToolName — Source: mcpStringUtils.ts:50-52
// ---------------------------------------------------------------------------

func TestBuildMcpToolName(t *testing.T) {
	tests := []struct {
		server string
		tool   string
		want   string
	}{
		{"my server", "read file", "mcp__my_server__read_file"},
		{"my-server", "read-file", "mcp__my-server__read-file"},
		{"a", "b", "mcp__a__b"},
		{"claude.ai Server", "tool", "mcp__claude_ai_Server__tool"},
	}

	for _, tt := range tests {
		got := BuildMcpToolName(tt.server, tt.tool)
		if got != tt.want {
			t.Errorf("BuildMcpToolName(%q, %q) = %q, want %q", tt.server, tt.tool, got, tt.want)
		}
	}
}

// ---------------------------------------------------------------------------
// BuildMcpToolName + McpInfoFromString round-trip
// ---------------------------------------------------------------------------

func TestBuildMcpToolName_RoundTrip(t *testing.T) {
	servers := []string{"my server", "test-server", "claude.ai Server", "a.b.c"}
	tools := []string{"read file", "tool-1", "do_something"}
	for _, srv := range servers {
		for _, tool := range tools {
			full := BuildMcpToolName(srv, tool)
			info := McpInfoFromString(full)
			if info == nil {
				t.Errorf("round-trip failed: BuildMcpToolName(%q,%q)=%q, McpInfoFromString returned nil", srv, tool, full)
				continue
			}
			// Server and tool names should match after normalization
			wantSrv := NormalizeNameForMCP(srv)
			wantTool := NormalizeNameForMCP(tool)
			if info.ServerName != wantSrv {
				t.Errorf("round-trip server: got %q, want %q (from %q)", info.ServerName, wantSrv, srv)
			}
			if info.ToolName != wantTool {
				t.Errorf("round-trip tool: got %q, want %q (from %q)", info.ToolName, wantTool, tool)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// GetMcpPrefix — Source: mcpStringUtils.ts:39-41
// ---------------------------------------------------------------------------

func TestGetMcpPrefix(t *testing.T) {
	got := GetMcpPrefix("my server")
	want := "mcp__my_server__"
	if got != want {
		t.Errorf("GetMcpPrefix(%q) = %q, want %q", "my server", got, want)
	}
}

// ---------------------------------------------------------------------------
// GetToolNameForPermissionCheck — Source: mcpStringUtils.ts:60-67
// ---------------------------------------------------------------------------

func TestGetToolNameForPermissionCheck(t *testing.T) {
	t.Run("with mcpInfo", func(t *testing.T) {
		info := &MCPToolInfo{ServerName: "my server", ToolName: "read"}
		got := GetToolNameForPermissionCheck("display", info)
		want := "mcp__my_server__read"
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("nil mcpInfo", func(t *testing.T) {
		got := GetToolNameForPermissionCheck("Write", nil)
		if got != "Write" {
			t.Errorf("got %q, want %q", got, "Write")
		}
	})

	t.Run("empty toolName in mcpInfo", func(t *testing.T) {
		info := &MCPToolInfo{ServerName: "srv", ToolName: ""}
		got := GetToolNameForPermissionCheck("display", info)
		if got != "display" {
			t.Errorf("got %q, want %q", got, "display")
		}
	})
}

// ---------------------------------------------------------------------------
// GetMcpDisplayName — Source: mcpStringUtils.ts:75-81
// ---------------------------------------------------------------------------

func TestGetMcpDisplayName(t *testing.T) {
	got := GetMcpDisplayName("mcp__my_server__read_file", "my server")
	want := "read_file"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// ---------------------------------------------------------------------------
// ExtractMcpToolDisplayName — Source: mcpStringUtils.ts:88-106
// ---------------------------------------------------------------------------

func TestExtractMcpToolDisplayName(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		want   string
	}{
		{"with_mcp_suffix_and_prefix", "github - Add comment to issue (MCP)", "Add comment to issue"},
		{"with_mcp_suffix_no_prefix", "read file (MCP)", "read file"},
		{"no_mcp_suffix_with_prefix", "github - Create PR", "Create PR"},
		{"plain_name", "read_file", "read_file"},
		{"mcp_suffix_with_spaces", "tool  (MCP)  ", "tool"},
		{"multiple_dashes", "server - tool - extra", "tool - extra"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ExtractMcpToolDisplayName(tt.input)
			if got != tt.want {
				t.Errorf("ExtractMcpToolDisplayName(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// IsMcpTool
// ---------------------------------------------------------------------------

func TestIsMcpTool(t *testing.T) {
	if !IsMcpTool("mcp__server__tool") {
		t.Error("expected true for mcp__server__tool")
	}
	if IsMcpTool("Read") {
		t.Error("expected false for Read")
	}
	if IsMcpTool("mcp_") {
		t.Error("expected false for mcp_")
	}
}
