package mcp

import (
	"encoding/json"
	"strings"
	"testing"

	gbotmcp "github.com/liuy/gbot/pkg/mcp"
)

// validateToolConventions checks that a tool follows standard conventions:
//   - Description_ with nil/empty input returns a short, single-line summary
//   - Prompt_ is non-empty and multi-line (detailed instructions)
func validateToolConventions(t *testing.T, name string, desc string, prompt string) {
	t.Helper()

	if desc == "" {
		t.Errorf("%s: Description_(nil) returned empty string", name)
	}
	if strings.Contains(desc, "\n") {
		t.Errorf("%s: Description_(nil) should be a single line, got multi-line:\n%s", name, desc)
	}
	if len(desc) > 80 {
		t.Errorf("%s: Description_(nil) should be ≤80 chars, got %d: %q", name, len(desc), desc)
	}
	if prompt == "" {
		t.Errorf("%s: Prompt_ should not be empty", name)
	}
	if !strings.Contains(prompt, "\n") {
		t.Errorf("%s: Prompt_ should be multi-line with detailed instructions", name)
	}
}

func TestToolConventions(t *testing.T) {
	reg := gbotmcp.NewRegistry(gbotmcp.NewClientManager(nil, false, ""), gbotmcp.ChangeCallbacks{})

	listTool := NewListMcpResources(reg)
	readTool := NewReadMcpResource(reg)

	t.Run("ListMcpResources", func(t *testing.T) {
		desc, err := listTool.Description(nil)
		if err != nil {
			t.Fatalf("Description_(nil) error: %v", err)
		}
		validateToolConventions(t, "ListMcpResources", desc, listTool.Prompt())
	})

	t.Run("ListMcpResources_with_input", func(t *testing.T) {
		desc, err := listTool.Description(json.RawMessage(`{"server":"everything"}`))
		if err != nil {
			t.Fatalf("Description_ error: %v", err)
		}
		if desc != "everything" {
			t.Errorf("Description_ with server input should return server name, got %q", desc)
		}
	})

	t.Run("ListMcpResources_empty_server", func(t *testing.T) {
		desc, err := listTool.Description(json.RawMessage(`{"server":""}`))
		if err != nil {
			t.Fatalf("Description_ error: %v", err)
		}
		if desc != "List available resources from configured MCP servers" {
			t.Errorf("Description_ with empty server should return generic description, got %q", desc)
		}
	})

	t.Run("ReadMcpResource_nil_input", func(t *testing.T) {
		desc, err := readTool.Description(nil)
		if err != nil {
			t.Fatalf("Description_(nil) error: %v", err)
		}
		validateToolConventions(t, "ReadMcpResource(nil)", desc, readTool.Prompt())
	})

	t.Run("ReadMcpResource_with_input", func(t *testing.T) {
		desc, err := readTool.Description(json.RawMessage(`{"server":"everything","uri":"test://res"}`))
		if err != nil {
			t.Fatalf("Description_ error: %v", err)
		}
		if desc != "everything test://res" {
			t.Errorf("Description_ with input should return server uri, got %q", desc)
		}
	})

	t.Run("ReadMcpResource_empty_input", func(t *testing.T) {
		desc, err := readTool.Description(json.RawMessage(`{}`))
		if err != nil {
			t.Fatalf("Description_({}) error: %v", err)
		}
		validateToolConventions(t, "ReadMcpResource({})", desc, readTool.Prompt())
	})
}
