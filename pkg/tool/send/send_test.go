package send

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/liuy/gbot/pkg/tool"
)

// fakeSender records SendFile calls for assertion.
type fakeSender struct {
	lastPath    string
	lastCaption string
	calls       int
	err         error
}

func (f *fakeSender) SendFile(ctx context.Context, filePath, caption string) error {
	f.calls++
	f.lastPath = filePath
	f.lastCaption = caption
	return f.err
}

func TestNew_CallForwardsFilePathAndCaption(t *testing.T) {
	t.Parallel()
	fs := &fakeSender{}
	tt := New(fs)

	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "img.png")
	if err := os.WriteFile(filePath, []byte("fake-png"), 0o644); err != nil {
		t.Fatalf("write temp file: %v", err)
	}

	input, _ := json.Marshal(Input{FilePath: filePath})
	result, err := tt.Call(context.Background(), input, &tool.ToolUseContext{})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if fs.calls != 1 {
		t.Fatalf("SendFile calls = %d, want 1", fs.calls)
	}
	if fs.lastPath != filePath {
		t.Errorf("SendFile path = %q, want %q", fs.lastPath, filePath)
	}
	if fs.lastCaption != "" {
		t.Errorf("SendFile caption = %q, want empty", fs.lastCaption)
	}
	if result == nil {
		t.Fatal("Call returned nil result")
	}
	m, ok := result.Data.(map[string]any)
	if !ok {
		t.Fatalf("result.Data type = %T, want map[string]any", result.Data)
	}
	if m["file_path"] != filePath {
		t.Errorf("result file_path = %q, want %q", m["file_path"], filePath)
	}
	if m["status"] != "sent" {
		t.Errorf("result status = %q, want 'sent'", m["status"])
	}
}

func TestNew_FileNotFound(t *testing.T) {
	t.Parallel()
	fs := &fakeSender{}
	tt := New(fs)

	input, _ := json.Marshal(Input{FilePath: "/nonexistent/path/file.png"})
	_, err := tt.Call(context.Background(), input, &tool.ToolUseContext{})
	if err == nil {
		t.Fatal("expected error for missing file, got nil")
	}
	if !strings.Contains(err.Error(), "file not found") {
		t.Errorf("error = %q, want 'file not found'", err.Error())
	}
	if fs.calls != 0 {
		t.Errorf("SendFile calls = %d, want 0 (should not call on missing file)", fs.calls)
	}
}

func TestNew_EmptyFilePath(t *testing.T) {
	t.Parallel()
	fs := &fakeSender{}
	tt := New(fs)

	input, _ := json.Marshal(Input{FilePath: ""})
	_, err := tt.Call(context.Background(), input, &tool.ToolUseContext{})
	if err == nil {
		t.Fatal("expected error for empty file_path, got nil")
	}
	if !strings.Contains(err.Error(), "required") {
		t.Errorf("error = %q, want 'required'", err.Error())
	}
}

func TestNew_SenderErrorPropagates(t *testing.T) {
	t.Parallel()
	fs := &fakeSender{err: context.DeadlineExceeded}
	tt := New(fs)

	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "doc.pdf")
	if err := os.WriteFile(filePath, []byte("fake-pdf"), 0o644); err != nil {
		t.Fatalf("write temp file: %v", err)
	}

	input, _ := json.Marshal(Input{FilePath: filePath})
	_, err := tt.Call(context.Background(), input, &tool.ToolUseContext{})
	if err == nil {
		t.Fatal("expected sender error to propagate, got nil")
	}
	if fs.calls != 1 {
		t.Errorf("SendFile calls = %d, want 1", fs.calls)
	}
}

func TestNew_ResultDataAndRender(t *testing.T) {
	t.Parallel()
	fs := &fakeSender{}
	tt := New(fs)

	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "vid.mp4")
	if err := os.WriteFile(filePath, []byte("fake-mp4"), 0o644); err != nil {
		t.Fatalf("write temp file: %v", err)
	}

	input, _ := json.Marshal(Input{FilePath: filePath})
	result, err := tt.Call(context.Background(), input, &tool.ToolUseContext{})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	m, ok := result.Data.(map[string]any)
	if !ok {
		t.Fatalf("result.Data type = %T, want map[string]any", result.Data)
	}
	if m["file_path"] != filePath {
		t.Errorf("result file_path = %v, want %q", m["file_path"], filePath)
	}
	if m["status"] != "sent" {
		t.Errorf("result status = %v, want 'sent'", m["status"])
	}

	rendered := tt.RenderResult(result.Data)
	if !strings.Contains(rendered, filePath) {
		t.Errorf("RenderResult = %q, want it to contain %q", rendered, filePath)
	}
}

func TestNew_NotReadOnly(t *testing.T) {
	t.Parallel()
	fs := &fakeSender{}
	tt := New(fs)
	input, _ := json.Marshal(Input{FilePath: "/tmp/x.png"})
	if tt.IsReadOnly(input) {
		t.Error("IsReadOnly = true, want false (Send has side effects)")
	}
}

func TestNew_Description(t *testing.T) {
	t.Parallel()
	fs := &fakeSender{}
	tt := New(fs)

	// Non-empty file_path → returns the path.
	input, _ := json.Marshal(Input{FilePath: "/some/path.png"})
	desc, err := tt.Description(input)
	if err != nil {
		t.Fatalf("Description: %v", err)
	}
	if desc != "/some/path.png" {
		t.Errorf("Description = %q, want /some/path.png", desc)
	}

	// Empty file_path → static description.
	emptyInput, _ := json.Marshal(Input{})
	desc2, err := tt.Description(emptyInput)
	if err != nil {
		t.Fatalf("Description empty: %v", err)
	}
	if desc2 != staticDescription {
		t.Errorf("Description empty = %q, want %q", desc2, staticDescription)
	}
}

func TestNew_InvalidJSON(t *testing.T) {
	t.Parallel()
	fs := &fakeSender{}
	tt := New(fs)

	// Description should fall back to static on invalid JSON.
	desc, err := tt.Description(json.RawMessage(`{bad json`))
	if err != nil {
		t.Fatalf("Description invalid JSON should not error: %v", err)
	}
	if desc != staticDescription {
		t.Errorf("Description invalid JSON = %q, want %q", desc, staticDescription)
	}

	// Call should error on invalid JSON.
	_, err = tt.Call(context.Background(), json.RawMessage(`{bad json`), &tool.ToolUseContext{})
	if err == nil {
		t.Fatal("expected error for invalid JSON input, got nil")
	}
	if !strings.Contains(err.Error(), "invalid input") {
		t.Errorf("error = %q, want it to mention 'invalid input'", err.Error())
	}
}

func TestNew_NameAndAliases(t *testing.T) {
	t.Parallel()
	fs := &fakeSender{}
	tt := New(fs)
	if tt.Name() != "Send" {
		t.Errorf("Name = %q, want 'Send'", tt.Name())
	}
	aliases := tt.Aliases()
	if len(aliases) != 1 || aliases[0] != "send" {
		t.Errorf("Aliases = %v, want [send]", aliases)
	}
}

func TestNew_RenderResult(t *testing.T) {
	t.Parallel()
	tt := New(&fakeSender{})

	got := tt.RenderResult(map[string]any{"file_path": "/tmp/x.png", "status": "sent"})
	if got != "Sent /tmp/x.png" {
		t.Errorf("RenderResult with path = %q, want 'Sent /tmp/x.png'", got)
	}

	got = tt.RenderResult(map[string]any{"status": "sent"})
	if got != "Sent" {
		t.Errorf("RenderResult without path = %q, want 'Sent'", got)
	}

	got = tt.RenderResult("not a map")
	if !strings.Contains(got, "not a map") {
		t.Errorf("RenderResult fallback = %q, want JSON fallback", got)
	}
}

func TestNew_RenderResult_JSONRawMessage(t *testing.T) {
	t.Parallel()
	tt := New(&fakeSender{})

	got := tt.RenderResult(json.RawMessage(`{"file_path":"/tmp/x.png","status":"sent"}`))
	if got != "Sent /tmp/x.png" {
		t.Errorf("RenderResult(RawMessage with path) = %q, want 'Sent /tmp/x.png'", got)
	}

	got = tt.RenderResult(json.RawMessage(`{"status":"sent"}`))
	if got != "Sent" {
		t.Errorf("RenderResult(RawMessage without path) = %q, want 'Sent'", got)
	}
}

func TestNew_Metadata(t *testing.T) {
	t.Parallel()
	tt := New(&fakeSender{})

	if tt.IsReadOnly(json.RawMessage(`{}`)) {
		t.Error("IsReadOnly = true, want false")
	}
	if tt.IsDestructive(json.RawMessage(`{}`)) {
		t.Error("IsDestructive = true, want false")
	}
	if !tt.IsConcurrencySafe(json.RawMessage(`{}`)) {
		t.Error("IsConcurrencySafe = false, want true")
	}
	schema := tt.InputSchema()
	if len(schema) == 0 {
		t.Error("InputSchema returned empty")
	}
	if !strings.Contains(string(schema), "file_path") {
		t.Errorf("InputSchema = %s, want it to contain 'file_path'", string(schema))
	}
}
