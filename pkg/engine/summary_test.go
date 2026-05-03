package engine

import (
	"encoding/json"
	"testing"

	"github.com/liuy/gbot/pkg/mcp"
)

func TestComputeSummary_MCPTool_SkipsLongDescription(t *testing.T) {
	// computeSummary should NOT use MCP tool's long Description as the summary.
	// MCP descriptions are paragraphs of text — useless in the TUI tool header.
	eng := New(&Params{})
	defer eng.Close()

	mcpDesc := "Converts web page content to well-formatted Markdown, preserving structural elements"
	eng.tools["mcp__fetch__get_markdown"] = NewMCPTool(mcp.DiscoveredTool{
		Name:         "mcp__fetch__get_markdown",
		OriginalName: "get_markdown",
		ServerName:   "fetch",
		Description:  mcpDesc,
	}, nil)

	input := json.RawMessage(`{"url":"https://example.com"}`)
	summary := eng.computeSummary("mcp__fetch__get_markdown", input)

	if summary == mcpDesc {
		t.Errorf("computeSummary returned full MCP description as summary:\n%q\nwant: empty or extracted param", summary)
	}
}

func TestComputeSummary_MCPTool_ExtractsURLParam(t *testing.T) {
	// For MCP tools with a "url" field, computeSummary should extract it.
	eng := New(&Params{})
	defer eng.Close()

	eng.tools["mcp__fetch__get_markdown"] = NewMCPTool(mcp.DiscoveredTool{
		Name:         "mcp__fetch__get_markdown",
		OriginalName: "get_markdown",
		ServerName:   "fetch",
		Description:  "some long description",
	}, nil)

	input := json.RawMessage(`{"url":"https://example.com/page"}`)
	summary := eng.computeSummary("mcp__fetch__get_markdown", input)

	if summary != "https://example.com/page" {
		t.Errorf("expected extracted URL, got: %q", summary)
	}
}

func TestComputeSummary_MCPTool_WithSearchHint_UsesHint(t *testing.T) {
	// SearchHint is a short curated summary — OK to use.
	eng := New(&Params{})
	defer eng.Close()

	eng.tools["mcp__fetch__get_markdown"] = NewMCPTool(mcp.DiscoveredTool{
		Name:         "mcp__fetch__get_markdown",
		OriginalName: "get_markdown",
		ServerName:   "fetch",
		Description:  "long description",
		SearchHint:   "fetch web pages",
	}, nil)

	input := json.RawMessage(`{"url":"https://example.com"}`)
	summary := eng.computeSummary("mcp__fetch__get_markdown", input)

	if summary != "fetch web pages" {
		t.Errorf("expected SearchHint, got: %q", summary)
	}
}

func TestComputeSummary_BuiltinTool_UsesExtractedParam(t *testing.T) {
	// Built-in tools still work — extractSummaryFromPartial extracts params.
	eng := New(&Params{})
	defer eng.Close()

	eng.tools["Bash"] = &stubTool{name: "Bash"}

	input := json.RawMessage(`{"command":"ls -la"}`)
	summary := eng.computeSummary("Bash", input)

	if summary != "ls -la" {
		t.Errorf("expected 'ls -la', got: %q", summary)
	}
}
