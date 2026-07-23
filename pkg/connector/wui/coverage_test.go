package wui

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/tool"
)

// --- summaryMockTool: configurable Description for computeToolSummary tests ---

type summaryMockTool struct {
	decodeMockTool
	descStr string
	descErr error
}

func (m *summaryMockTool) Description(json.RawMessage) (string, error) {
	return m.descStr, m.descErr
}

// --- Tier 1: Pure functions ---

func TestFormatToolDisplayName(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"plain name", "Bash", "Bash"},
		{"mcp full", "mcp__server__toolname", "server - toolname (MCP)"},
		{"mcp with underscores in tool", "mcp__myserver__do_thing", "myserver - do_thing (MCP)"},
		{"mcp only two parts", "mcp__only_two_parts", "mcp__only_two_parts"},
		{"mcp prefix but single part", "mcp__", "mcp__"},
		{"empty string", "", ""},
		{"read tool", "Read", "Read"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := formatToolDisplayName(tt.input)
			if got != tt.want {
				t.Errorf("formatToolDisplayName(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestComputeToolSummary(t *testing.T) {
	t.Parallel()
	t.Run("tool not in map", func(t *testing.T) {
		t.Parallel()
		got := computeToolSummary("Missing", json.RawMessage(`{}`), map[string]tool.Tool{})
		if got != "" {
			t.Errorf("computeToolSummary(missing) = %q, want empty string", got)
		}
	})
	t.Run("nil tools map", func(t *testing.T) {
		t.Parallel()
		got := computeToolSummary("Bash", json.RawMessage(`{}`), nil)
		if got != "" {
			t.Errorf("computeToolSummary(nil map) = %q, want empty string", got)
		}
	})
	t.Run("description returns error", func(t *testing.T) {
		t.Parallel()
		tools := map[string]tool.Tool{
			"Bash": &summaryMockTool{
				descErr: errors.New("description failed"),
			},
		}
		got := computeToolSummary("Bash", json.RawMessage(`{}`), tools)
		if got != "" {
			t.Errorf("computeToolSummary(desc error) = %q, want empty string", got)
		}
	})
	t.Run("description returns value", func(t *testing.T) {
		t.Parallel()
		tools := map[string]tool.Tool{
			"Grep": &summaryMockTool{
				descStr: "Search file contents using regex",
			},
		}
		got := computeToolSummary("Grep", json.RawMessage(`{"pattern":"foo"}`), tools)
		want := "Search file contents using regex"
		if got != want {
			t.Errorf("computeToolSummary(Grep) = %q, want %q", got, want)
		}
	})
}

func TestReadPersistedFile(t *testing.T) {
	t.Parallel()
	t.Run("no marker returns nil", func(t *testing.T) {
		t.Parallel()
		got := readPersistedFile("<persisted-output>some stuff without marker")
		if got != nil {
			t.Errorf("readPersistedFile(no marker) = %v, want nil", got)
		}
	})
	t.Run("valid file path reads contents", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		filePath := filepath.Join(dir, "output.txt")
		content := `{"text":"file content here"}`
		if err := os.WriteFile(filePath, []byte(content), 0644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
		input := "<persisted-output>\nFull output saved to: " + filePath + "\nPreview (first 5 lines):\nline1"
		got := readPersistedFile(input)
		if got == nil {
			t.Fatal("readPersistedFile(valid path) returned nil, want non-nil")
		}
		if string(got) != content {
			t.Errorf("readPersistedFile = %q, want %q", string(got), content)
		}
	})
	t.Run("nonexistent path returns nil", func(t *testing.T) {
		t.Parallel()
		input := "<persisted-output>\nFull output saved to: /nonexistent/path/to/file.txt\nPreview:\nline1"
		got := readPersistedFile(input)
		if got != nil {
			t.Errorf("readPersistedFile(nonexistent) = %v, want nil", got)
		}
	})
	t.Run("path with trailing newline trims correctly", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		filePath := filepath.Join(dir, "data.json")
		content := `{"output":"trimmed"}`
		if err := os.WriteFile(filePath, []byte(content), 0644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
		input := "Full output saved to: " + filePath + "\nrest of text"
		got := readPersistedFile(input)
		if got == nil {
			t.Fatal("readPersistedFile(newline trim) returned nil")
		}
		if string(got) != content {
			t.Errorf("readPersistedFile = %q, want %q", string(got), content)
		}
	})
	t.Run("path without newline reads to end", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		filePath := filepath.Join(dir, "nonl.txt")
		content := "no newline content"
		if err := os.WriteFile(filePath, []byte(content), 0644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
		input := "Full output saved to: " + filePath
		got := readPersistedFile(input)
		if got == nil {
			t.Fatal("readPersistedFile(no newline) returned nil")
		}
		if string(got) != content {
			t.Errorf("readPersistedFile = %q, want %q", string(got), content)
		}
	})
}

func TestExtractPersistedPreview(t *testing.T) {
	t.Parallel()
	t.Run("no preview marker", func(t *testing.T) {
		t.Parallel()
		got := extractPersistedPreview("<persisted-output>no preview here")
		want := "<output saved to file>"
		if got != want {
			t.Errorf("extractPersistedPreview(no marker) = %q, want %q", got, want)
		}
	})
	t.Run("three lines returned joined", func(t *testing.T) {
		t.Parallel()
		input := "<persisted-output>\nFull output saved to: /tmp/x\nPreview (first 3 lines):\nline1\nline2\nline3"
		got := extractPersistedPreview(input)
		want := "line1\nline2\nline3"
		if got != want {
			t.Errorf("extractPersistedPreview(3 lines) = %q, want %q", got, want)
		}
	})
	t.Run("six lines truncated to five with ellipsis", func(t *testing.T) {
		t.Parallel()
		input := "<persisted-output>\nPreview (first 5 lines):\nL1\nL2\nL3\nL4\nL5\nL6"
		got := extractPersistedPreview(input)
		want := "L1\nL2\nL3\nL4\nL5\n..."
		if got != want {
			t.Errorf("extractPersistedPreview(6 lines) = %q, want %q", got, want)
		}
	})
	t.Run("missing separator returns fallback", func(t *testing.T) {
		t.Parallel()
		input := "<persisted-output>\nPreview (first 5 lines) something wrong"
		got := extractPersistedPreview(input)
		want := "<output saved to file>"
		if got != want {
			t.Errorf("extractPersistedPreview(missing sep) = %q, want %q", got, want)
		}
	})
	t.Run("empty content after separator returns fallback", func(t *testing.T) {
		t.Parallel()
		input := "<persisted-output>\nPreview (first 5 lines):\n"
		got := extractPersistedPreview(input)
		want := "<output saved to file>"
		if got != want {
			t.Errorf("extractPersistedPreview(empty content) = %q, want %q", got, want)
		}
	})
}

// --- Tier 2: Stateful functions ---

func TestRenderToolOutput_PersistedOutputValidFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	filePath := filepath.Join(dir, "result.json")
	fileContent := `{"text":"rendered from file"}`
	if err := os.WriteFile(filePath, []byte(fileContent), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	tools := map[string]tool.Tool{
		"Bash": &decodeMockTool{
			renderFn: func(data any) string {
				out, ok := data.(*stringResult)
				if !ok {
					return "FALLBACK"
				}
				return out.Text
			},
			decodeFn: func(raw json.RawMessage) (any, error) {
				var r stringResult
				if err := json.Unmarshal(raw, &r); err != nil {
					return nil, err
				}
				return &r, nil
			},
		},
	}
	input := "<persisted-output>\nFull output saved to: " + filePath + "\nPreview (first 5 lines):\nline1"
	textJSON, _ := json.Marshal(input)
	raw := json.RawMessage(`[{"type":"text","text":` + string(textJSON) + `}]`)
	got, elapsed := renderToolOutput("Bash", raw, tools)
	want := "rendered from file"
	if got != want {
		t.Errorf("renderToolOutput(valid file) = %q, want %q", got, want)
	}
	if elapsed != 0 {
		t.Errorf("elapsed = %d, want 0", elapsed)
	}
}

func TestRenderToolOutput_PersistedOutputInvalidFile(t *testing.T) {
	t.Parallel()
	input := "<persisted-output>\nFull output saved to: /nonexistent/path.txt\nPreview (first 3 lines):\nprev1\nprev2\nprev3"
	textJSON, _ := json.Marshal(input)
	raw := json.RawMessage(`[{"type":"text","text":` + string(textJSON) + `}]`)
	got, elapsed := renderToolOutput("Bash", raw, nil)
	want := "prev1\nprev2\nprev3"
	if got != want {
		t.Errorf("renderToolOutput(invalid file) = %q, want %q", got, want)
	}
	if elapsed != 0 {
		t.Errorf("elapsed = %d, want 0", elapsed)
	}
}

func TestRenderToolOutput_JSONOutputField(t *testing.T) {
	t.Parallel()
	inner := `{"output":"hello world"}`
	textJSON, _ := json.Marshal(inner)
	raw := json.RawMessage(`[{"type":"text","text":` + string(textJSON) + `}]`)
	got, elapsed := renderToolOutput("Bash", raw, nil)
	want := "hello world"
	if got != want {
		t.Errorf("renderToolOutput(JSON output) = %q, want %q", got, want)
	}
	if elapsed != 0 {
		t.Errorf("elapsed = %d, want 0", elapsed)
	}
}

func TestRenderToolOutput_BlocksArray(t *testing.T) {
	t.Parallel()
	raw := json.RawMessage(`[{"type":"text","text":"block A"},{"type":"text","text":"block B"}]`)
	got, elapsed := renderToolOutput("Bash", raw, nil)
	want := "block A\nblock B"
	if got != want {
		t.Errorf("renderToolOutput(blocks) = %q, want %q", got, want)
	}
	if elapsed != 0 {
		t.Errorf("elapsed = %d, want 0", elapsed)
	}
}

func TestRenderToolOutput_RawFallback(t *testing.T) {
	t.Parallel()
	raw := json.RawMessage(`not json at all {`)
	got, _ := renderToolOutput("Bash", raw, nil)
	want := "not json at all {"
	if got != want {
		t.Errorf("renderToolOutput(raw fallback) = %q, want %q", got, want)
	}
}

func TestRenderToolOutput_ToolSpentPrefix(t *testing.T) {
	t.Parallel()
	inner := "plain result text"
	wrapped := "[Tool spent 2.5s]" + inner
	textJSON, _ := json.Marshal(wrapped)
	raw := json.RawMessage(`[{"type":"text","text":` + string(textJSON) + `}]`)
	got, elapsed := renderToolOutput("Unknown", raw, nil)
	if got != inner {
		t.Errorf("renderToolOutput(spent prefix) = %q, want %q", got, inner)
	}
	wantNs := int64(2.5 * float64(time.Second))
	if elapsed != wantNs {
		t.Errorf("elapsed = %d, want %d", elapsed, wantNs)
	}
}

func TestRenderToolOutput_EmptyStringAfterSpent(t *testing.T) {
	t.Parallel()
	wrapped := "[Tool spent 1.0s]"
	textJSON, _ := json.Marshal(wrapped)
	raw := json.RawMessage(`[{"type":"text","text":` + string(textJSON) + `}]`)
	got, elapsed := renderToolOutput("Bash", raw, nil)
	if got != "" {
		t.Errorf("renderToolOutput(empty after spent) = %q, want empty", got)
	}
	wantNs := int64(1.0 * float64(time.Second))
	if elapsed != wantNs {
		t.Errorf("elapsed = %d, want %d", elapsed, wantNs)
	}
}

func TestRenderToolOutput_ErrorArrayForm(t *testing.T) {
	t.Parallel()
	raw := json.RawMessage(`[{"type":"text","text":"[Tool spent 1.5s]boom"}]`)
	got, elapsed := renderToolOutput("Bash", raw, nil)
	if got != "boom" {
		t.Errorf("got %q, want %q", got, "boom")
	}
	wantNs := int64(1.5 * float64(time.Second))
	if elapsed != wantNs {
		t.Errorf("elapsed = %d, want %d", elapsed, wantNs)
	}
}

// TestRenderToolOutput_NoStringBranch proves the legacy string-form branch is
// gone. The bare 7-byte JSON string literal "hello" (with surrounding quotes)
// fails the array parse and falls through to string(raw), returning the same
// 7 bytes back.
func TestRenderToolOutput_NoStringBranch(t *testing.T) {
	t.Parallel()
	raw := json.RawMessage(`"hello"`)
	got, _ := renderToolOutput("Bash", raw, nil)
	if got != `"hello"` {
		t.Errorf("got %q (%d bytes), want %q (7 bytes — string(raw) passthrough)", got, len(got), `"hello"`)
	}
}

func TestHandleEngineNew_NilCreateEngine(t *testing.T) {
	c := newTestConnector(t)
	c.createEngine = nil
	ws := dialAndStore(t, c)

	c.handleEngineNew("test-engine")

	msg := readWSMessage(t, ws)
	var env struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(msg, &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if env.Type != "error" {
		t.Errorf("type = %q, want \"error\"", env.Type)
	}
	if env.Message != "engine creation not configured" {
		t.Errorf("message = %q, want \"engine creation not configured\"", env.Message)
	}
}

func TestHandleEngineNew_CreateEngineError(t *testing.T) {
	c := newTestConnector(t)
	c.createEngine = func(name string) (string, error) {
		return "", errors.New("disk full")
	}
	ws := dialAndStore(t, c)

	c.handleEngineNew("test-engine")

	msg := readWSMessage(t, ws)
	var env struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(msg, &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if env.Type != "error" {
		t.Errorf("type = %q, want \"error\"", env.Type)
	}
	if env.Message != "disk full" {
		t.Errorf("message = %q, want \"disk full\"", env.Message)
	}
}

func TestHandleEngineNew_Success(t *testing.T) {
	c := newTestConnector(t)
	called := false
	c.createEngine = func(name string) (string, error) {
		called = true
		if name != "my-engine" {
			t.Errorf("createEngine called with name %q, want \"my-engine\"", name)
		}
		return "main", nil
	}
	ws := dialAndStore(t, c)

	c.handleEngineNew("my-engine")

	if !called {
		t.Fatal("createEngine was not called")
	}

	msg := readWSMessage(t, ws)
	var env struct {
		Type     string `json:"type"`
		ActiveID string `json:"activeID"`
	}
	if err := json.Unmarshal(msg, &env); err != nil {
		t.Fatalf("unmarshal engine_list: %v", err)
	}
	if env.Type != "engine_list" {
		t.Errorf("type = %q, want \"engine_list\"", env.Type)
	}
	if env.ActiveID != "main" {
		t.Errorf("activeID = %q, want \"main\"", env.ActiveID)
	}

	// switchEngine sends a metadata frame after engine_list.
	meta := readWSMessage(t, ws)
	var metaEnv struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(meta, &metaEnv); err != nil {
		t.Fatalf("unmarshal metadata: %v", err)
	}
	if metaEnv.Type != "metadata" {
		t.Errorf("second message type = %q, want \"metadata\"", metaEnv.Type)
	}
	if c.ActiveID() != "main" {
		t.Errorf("ActiveID after switch = %q, want \"main\"", c.ActiveID())
	}
}

func TestHandleSessionNew_EngineBusy(t *testing.T) {
	c := newTestConnector(t)
	c.mock().isBusyFn = func() bool { return true }
	ws := dialAndStore(t, c)

	c.handleSessionNew()

	msg := readWSMessage(t, ws)
	var env struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(msg, &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if env.Type != "error" {
		t.Errorf("type = %q, want \"error\"", env.Type)
	}
	wantSubstr := "Session is busy"
	if !contains(env.Message, wantSubstr) {
		t.Errorf("message = %q, want it to contain %q", env.Message, wantSubstr)
	}
}

func TestHandleSessionNew_NewSessionError(t *testing.T) {
	c := newTestConnector(t)
	c.mock().newSessionFn = func() (string, error) {
		return "", errors.New("store unavailable")
	}
	ws := dialAndStore(t, c)

	c.handleSessionNew()

	msg := readWSMessage(t, ws)
	var env struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(msg, &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if env.Type != "error" {
		t.Errorf("type = %q, want \"error\"", env.Type)
	}
	if env.Message != "store unavailable" {
		t.Errorf("message = %q, want \"store unavailable\"", env.Message)
	}
}

func TestHandleSessionNew_Success(t *testing.T) {
	c := newTestConnector(t)
	c.mock().newSessionFn = func() (string, error) {
		return "fresh-session", nil
	}
	ws := dialAndStore(t, c)

	c.handleSessionNew()

	// Success sends metadata then optionally session_list.
	msg := readWSMessage(t, ws)
	var env struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(msg, &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if env.Type != "metadata" {
		t.Errorf("type = %q, want \"metadata\"", env.Type)
	}
}

// contains is a local helper to avoid importing strings just for one call.
func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchString(s, substr)
}

func searchString(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
