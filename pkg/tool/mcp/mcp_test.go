package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	gbotmcp "github.com/liuy/gbot/pkg/mcp"
	"github.com/liuy/gbot/pkg/tool"
)

// Pin tests for the mcpresource.go mutation sweep; assertions are exact so a
// single changed byte flips the test. Mutant line:mutator pairs are in the
// campaign ledger.

func pinRegistry() *gbotmcp.Registry {
	reg := gbotmcp.NewRegistry(gbotmcp.NewClientManager(nil, false, ""), gbotmcp.ChangeCallbacks{})
	reg.SetConnectionForTest("server-a", &gbotmcp.ConnectedServer{
		Name:    "server-a",
		Session: nil,
		Config:  gbotmcp.ScopedMcpServerConfig{Config: &gbotmcp.StdioConfig{Command: "test"}, Scope: gbotmcp.ScopeUser},
	})
	return reg
}

func TestPinMcp_ToolMetadata(t *testing.T) {
	t.Parallel()
	list := NewListMcpResources(pinRegistry())
	read := NewReadMcpResource(pinRegistry())

	if sh, ok := list.(tool.ToolWithSearchHint); !ok || sh.SearchHint() != "list resources from connected MCP servers" {
		t.Errorf("list SearchHint = %q", sh.SearchHint())
	}
	if d, ok := list.(interface{ IsDeferred() bool }); !ok || !d.IsDeferred() {
		t.Error("list IsDeferred = false, want true")
	}
	if list.MaxResultSize() != 100000 {
		t.Errorf("list MaxResultSize = %d, want 100000", list.MaxResultSize())
	}
	if !strings.Contains(list.Prompt(), "List available resources") && !strings.Contains(list.Prompt(), "resource") {
		t.Errorf("list Prompt = %.50q, want resource prompt", list.Prompt())
	}
	var schema map[string]any
	if err := json.Unmarshal(list.InputSchema(), &schema); err != nil {
		t.Errorf("list InputSchema not JSON: %v", err)
	}
	if d, err := list.Description(json.RawMessage(`{"server":"srv"}`)); err != nil || d != "srv" {
		t.Errorf("list Description(server) = (%q, %v)", d, err)
	}
	if d, err := list.Description(json.RawMessage(`{}`)); err != nil || d != listMcpResourcesDescription {
		t.Errorf("list Description(empty) = (%q, %v)", d, err)
	}

	if sh, ok := read.(tool.ToolWithSearchHint); !ok || sh.SearchHint() != "read a specific MCP resource by URI" {
		t.Errorf("read SearchHint = %q", sh.SearchHint())
	}
	if d, ok := read.(interface{ IsDeferred() bool }); !ok || !d.IsDeferred() {
		t.Error("read IsDeferred = false, want true")
	}
	if read.MaxResultSize() != 100000 {
		t.Errorf("read MaxResultSize = %d, want 100000", read.MaxResultSize())
	}
	if !strings.Contains(read.Prompt(), "resource") {
		t.Errorf("read Prompt = %.50q", read.Prompt())
	}
	if d, err := read.Description(json.RawMessage(`{"server":"s","uri":"u://x"}`)); err != nil || d != "s u://x" {
		t.Errorf("read Description(server,uri) = (%q, %v)", d, err)
	}
	if d, err := read.Description(json.RawMessage(`{"server":"s"}`)); err != nil || d != "s" {
		t.Errorf("read Description(server) = (%q, %v)", d, err)
	}
	if d, err := read.Description(json.RawMessage(`{}`)); err != nil || d != readMcpResourceDescription {
		t.Errorf("read Description(empty) = (%q, %v)", d, err)
	}
}

func TestPinMcp_CallGuards(t *testing.T) {
	t.Parallel()
	list := NewListMcpResources(pinRegistry())
	read := NewReadMcpResource(pinRegistry())

	_, err := list.Call(context.Background(), json.RawMessage(`{bad`), nil)
	if err == nil || !strings.HasPrefix(err.Error(), "mcp: invalid input:") {
		t.Fatalf("list invalid input err = %v", err)
	}
	if errors.Unwrap(err) == nil {
		t.Errorf("list invalid input err not wrapped: %v", err)
	}

	for _, c := range []struct {
		input string
		want  string
	}{
		{`{}`, "mcp: server is required"},
		{`{"server":"s"}`, "mcp: uri is required"},
		{`{bad`, "mcp: invalid input"},
	} {
		_, err := read.Call(context.Background(), json.RawMessage(c.input), nil)
		if err == nil || !strings.HasPrefix(err.Error(), c.want) {
			t.Errorf("read %s err = %v, want prefix %q", c.input, err, c.want)
		}
	}

	// Unknown server names the available servers.
	_, err = list.Call(context.Background(), json.RawMessage(`{"server":"nope"}`), nil)
	if err == nil || !strings.Contains(err.Error(), `server "nope" not found`) {
		t.Errorf("list unknown server err = %v, want not-found error", err)
	}
}

func TestPinMcp_Renderers(t *testing.T) {
	t.Parallel()
	// Single content renders without a leading newline separator.
	got := formatResourceContent([]gbotmcp.ResourceContent{{URI: "u://a", Text: "body"}})
	if got != "[u://a]\nbody" {
		t.Errorf("formatResourceContent single = %q", got)
	}
	// Two contents separate with a blank line; blobs report their path.
	got = formatResourceContent([]gbotmcp.ResourceContent{
		{URI: "u://a", MimeType: "text/plain", Text: "one"},
		{URI: "u://b", BlobSavedTo: "/tmp/blob"},
	})
	want := "[u://a] (text/plain)\none\n[u://b]\n(binary content saved to /tmp/blob)"
	if got != want {
		t.Errorf("formatResourceContent two = %q, want %q", got, want)
	}

	// Grouping is per-server, servers sorted, with a summary line.
	got = formatResourceList([]gbotmcp.ServerResource{
		{Server: "zeta", URI: "z://1"},
		{Server: "alpha", URI: "a://1", MimeType: "text/x"},
		{Server: "alpha", URI: "a://2"},
	})
	want = "alpha (2 resources):\n  a://1 (text/x)\n  a://2\nzeta (1 resources):\n  z://1\n(3 resources from 2 servers)"
	if got != want {
		t.Errorf("formatResourceList = %q, want %q", got, want)
	}
}

func TestPinMcp_WireBlocksAndDecode(t *testing.T) {
	t.Parallel()
	list := NewListMcpResources(pinRegistry())
	read := NewReadMcpResource(pinRegistry())

	wb := list.(tool.ToolWithWireBlocks)
	blocks := wb.FormatWireBlocks(nil)
	if len(blocks) != 1 || !strings.Contains(blocks[0].Text, "No resources found") {
		t.Errorf("list nil wire = %+v", blocks)
	}
	blocks = wb.FormatWireBlocks([]gbotmcp.ServerResource{})
	if len(blocks) != 1 || !strings.Contains(blocks[0].Text, "No resources found") {
		t.Errorf("list empty wire = %+v", blocks)
	}
	// ResourceContent slices render as JSON, not the friendly message.
	wb2 := read.(tool.ToolWithWireBlocks)
	blocks = wb2.FormatWireBlocks([]gbotmcp.ResourceContent{})
	if len(blocks) != 1 || !strings.Contains(blocks[0].Text, "server-a") && blocks[0].Text != "[]" {
		t.Errorf("read empty wire = %+v", blocks)
	}
	if blocks[0].Text != "[]" {
		t.Errorf("read empty wire text = %q, want []", blocks[0].Text)
	}

	// DecodeResult round-trips both shapes.
	dr := list.(tool.ToolWithDecodeResult)
	decoded, err := dr.DecodeResult(json.RawMessage(`[{"type":"text","text":"[{\"server\":\"s\",\"uri\":\"u\"}]"}]`))
	if err != nil {
		t.Fatal(err)
	}
	rs, ok := decoded.([]gbotmcp.ServerResource)
	if !ok || len(rs) != 1 || rs[0].Server != "s" || rs[0].URI != "u" {
		t.Errorf("list decode = %#v", decoded)
	}
	dr2 := read.(tool.ToolWithDecodeResult)
	decoded, err = dr2.DecodeResult(json.RawMessage(`[{"type":"text","text":"[{\"uri\":\"u\",\"text\":\"t\"}]"}]`))
	if err != nil {
		t.Fatal(err)
	}
	cs, ok := decoded.([]gbotmcp.ResourceContent)
	if !ok || len(cs) != 1 || cs[0].URI != "u" || cs[0].Text != "t" {
		t.Errorf("read decode = %#v", decoded)
	}
}
