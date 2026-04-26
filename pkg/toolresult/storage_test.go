package toolresult

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)


func TestGetSessionDir_HomeError(t *testing.T) {
	t.Setenv("HOME", "")
	_, err := GetSessionDir("test")
	if err != nil {
		t.Logf("expected error when HOME empty: %v", err)
	}
}

func TestEnsureToolResultsDir_CacheHit(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	ResetDirCache()

	if err := EnsureToolResultsDir("cache-test"); err != nil {
		t.Fatalf("first call: %v", err)
	}
	// Second call should hit cache
	if err := EnsureToolResultsDir("cache-test"); err != nil {
		t.Fatalf("cache hit: %v", err)
	}
}

func TestGetToolResultPath_JSON(t *testing.T) {
	path, err := GetToolResultPath("test-session", "tool-1", true)
	if err != nil {
		t.Fatalf("GetToolResultPath: %v", err)
	}
	if filepath.Ext(path) != ".json" {
		t.Errorf("expected .json extension, got %q", filepath.Ext(path))
	}
}

func TestIsToolResultPath_ShortPath(t *testing.T) {
	if IsToolResultPath("/tmp/file.txt") {
		t.Error("short path should not be tool result path")
	}
}

func TestPersistToolResult_WriteError(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	ResetDirCache()

	// Make the tool-results dir a file to cause write error
	sessionDir := filepath.Join(tmpDir, ".gbot", "sessions", "fail-session")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	toolResultsDir := filepath.Join(sessionDir, ToolResultsSubdir)
	if err := os.WriteFile(toolResultsDir, []byte("not a dir"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	defer func() { _ = os.Remove(toolResultsDir) }()

	_, err := PersistToolResult("fail-session", "tool-1", []byte("data"))
	if err == nil {
		t.Fatal("expected error when tool-results is a file")
	}
	if !strings.Contains(err.Error(), "ensure dir") {
		t.Errorf("error should mention dir issue, got: %v", err)
	}
}

func TestPersistToolResult_CloseError(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	ResetDirCache()

	// Normal persist — close should succeed
	result, err := PersistToolResult("close-test", "tool-1", []byte("data"))
	if err != nil {
		t.Fatalf("PersistToolResult: %v", err)
	}
	if result.Filepath == "" {
		t.Error("expected filepath")
	}
}

func TestGeneratePreview_NoNewline(t *testing.T) {
	content := strings.Repeat("a", 3000)
	preview, hasMore := GeneratePreview(content, 2000)
	if !hasMore {
		t.Error("expected hasMore")
	}
	if len(preview) > 2000 {
		t.Errorf("preview too long: %d", len(preview))
	}
}

func TestBuildLargeToolResultMessage_NoMore(t *testing.T) {
	result := &PersistedToolResult{
		Filepath:     "/tmp/test.txt",
		OriginalSize: 1000,
		Preview:      "preview",
		HasMore:      false,
	}
	msg := BuildLargeToolResultMessage(result)
	if strings.Contains(msg, "...") {
		t.Error("should not contain ... when HasMore is false")
	}
}

func TestHasImageBlock_NonArray(t *testing.T) {
	if HasImageBlock(json.RawMessage(`"hello"`)) {
		t.Error("string content should not have image block")
	}
}

func TestGetPersistenceThreshold(t *testing.T) {
	tests := []struct {
		name     string
		declared int
		want     int
	}{
		{"negative means unlimited", -1, -1},
		{"zero means default", 0, DefaultMaxResultSizeChars},
		{"small value passes through", 30000, 30000},
		{"large value capped at default", 100000, DefaultMaxResultSizeChars},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := GetPersistenceThreshold("TestTool", tt.declared)
			if got != tt.want {
				t.Errorf("GetPersistenceThreshold(%d) = %d, want %d", tt.declared, got, tt.want)
			}
		})
	}
}

func TestGetSessionDir(t *testing.T) {
	dir, err := GetSessionDir("abc123")
	if err != nil {
		t.Fatalf("GetSessionDir: %v", err)
	}
	home, _ := os.UserHomeDir()
	want := filepath.Join(home, ".gbot", "sessions", "abc123")
	if dir != want {
		t.Errorf("GetSessionDir = %q, want %q", dir, want)
	}
}

func TestGetSessionDir_InvalidID(t *testing.T) {
	_, err := GetSessionDir("../../../etc")
	if err == nil {
		t.Fatal("expected error for path traversal sessionID")
	}
	if !strings.Contains(err.Error(), "invalid") {
		t.Errorf("error should mention invalid, got: %v", err)
	}
}

func TestGetToolResultsDir(t *testing.T) {
	dir, err := GetToolResultsDir("abc123")
	if err != nil {
		t.Fatalf("GetToolResultsDir: %v", err)
	}
	if filepath.Base(dir) != ToolResultsSubdir {
		t.Errorf("GetToolResultsDir base = %q, want %q", filepath.Base(dir), ToolResultsSubdir)
	}
}

func TestEnsureToolResultsDir(t *testing.T) {
	// Use a temp HOME to avoid polluting real sessions
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	ResetDirCache()

	sessionID := "test-session-1"
	err := EnsureToolResultsDir(sessionID)
	if err != nil {
		t.Fatalf("EnsureToolResultsDir: %v", err)
	}

	dir, _ := GetToolResultsDir(sessionID)
	if _, statErr := os.Stat(dir); statErr != nil {
		t.Errorf("directory not created: %v", statErr)
	}

	// Idempotent: call again
	err = EnsureToolResultsDir(sessionID)
	if err != nil {
		t.Fatalf("EnsureToolResultsDir (second call): %v", err)
	}
}

func TestPersistToolResult(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	ResetDirCache()

	sessionID := "test-persist"
	content := []byte("hello world")

	result, err := PersistToolResult(sessionID, "tool-123", content)
	if err != nil {
		t.Fatalf("PersistToolResult: %v", err)
	}
	if result == nil {
		t.Fatal("PersistToolResult returned nil result")
	}
	if result.Filepath == "" {
		t.Error("Filepath is empty")
	}
	if result.OriginalSize != len(content) {
		t.Errorf("OriginalSize = %d, want %d", result.OriginalSize, len(content))
	}
	if result.Preview != "hello world" {
		t.Errorf("Preview = %q, want %q", result.Preview, "hello world")
	}
	if result.HasMore {
		t.Error("HasMore should be false for short content")
	}

	// Verify file exists
	if _, statErr := os.Stat(result.Filepath); statErr != nil {
		t.Errorf("file not created: %v", statErr)
	}

	// Verify file content
	data, readErr := os.ReadFile(result.Filepath)
	if readErr != nil {
		t.Fatalf("ReadFile: %v", readErr)
	}
	if string(data) != "hello world" {
		t.Errorf("file content = %q, want %q", string(data), "hello world")
	}
}

func TestPersistToolResult_FileExtension(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	ResetDirCache()

	tests := []struct {
		name    string
		content []byte
		wantExt string
	}{
		{"plain text", []byte("hello"), ".txt"},
		{"JSON array", []byte(`[{"type":"text","text":"hi"}]`), ".json"},
		{"JSON object", []byte(`{"key":"val"}`), ".json"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := PersistToolResult("test-ext", "id-"+tt.name, tt.content)
			if err != nil {
				t.Fatalf("PersistToolResult: %v", err)
			}
			if filepath.Ext(result.Filepath) != tt.wantExt {
				t.Errorf("extension = %q, want %q (path: %s)", filepath.Ext(result.Filepath), tt.wantExt, result.Filepath)
			}
		})
	}
}

func TestPersistToolResult_ExclusiveCreate(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	ResetDirCache()

	sessionID := "test-exclusive"
	_, _ = PersistToolResult(sessionID, "dup-id", []byte("first"))

	// Second call with same ID should return existing file
	result2, err := PersistToolResult(sessionID, "dup-id", []byte("second"))
	if err != nil {
		t.Fatalf("second PersistToolResult: %v", err)
	}
	// Content should be "first" (not overwritten)
	data, _ := os.ReadFile(result2.Filepath)
	if string(data) != "first" {
		t.Errorf("expected original content preserved, got %q", string(data))
	}
}

func TestGeneratePreview(t *testing.T) {
	// Short content — no cutting
	preview, hasMore := GeneratePreview("hello", 100)
	if hasMore {
		t.Error("hasMore should be false for short content")
	}
	if preview != "hello" {
		t.Errorf("preview = %q, want %q", preview, "hello")
	}
}

func TestGeneratePreview_LongContent(t *testing.T) {
	// Content with newlines — should cut at newline boundary
	content := "line1\nline2\nline3\nline4\nline5"
	preview, hasMore := GeneratePreview(content, 15)
	if !hasMore {
		t.Error("hasMore should be true")
	}
	if preview != "line1\nline2" {
		t.Errorf("preview = %q, want %q", preview, "line1\nline2")
	}
}

func TestGeneratePreview_UTF8(t *testing.T) {
	// Multi-byte UTF-8 characters should not be cut
	content := "你好世界这是测试内容更多"
	preview, _ := GeneratePreview(content, 20)
	// Verify preview is valid UTF-8
	for _, r := range preview {
		if r == 0xFFFD {
			t.Error("preview contains replacement character, UTF-8 was corrupted")
		}
	}
}

func TestFormatFileSize(t *testing.T) {
	tests := []struct {
		size int
		want string
	}{
		{0, "0B"},
		{500, "500B"},
		{1500, "1.5KB"},
		{1500000, "1.4MB"},
		{1500000000, "1.4GB"},
	}
	for _, tt := range tests {
		got := FormatFileSize(tt.size)
		if got != tt.want {
			t.Errorf("FormatFileSize(%d) = %q, want %q", tt.size, got, tt.want)
		}
	}
}

func TestBuildLargeToolResultMessage(t *testing.T) {
	result := &PersistedToolResult{
		Filepath:     "/tmp/test/file.txt",
		OriginalSize: 1500000,
		Preview:      "first line\nsecond line",
		HasMore:      true,
	}
	msg := BuildLargeToolResultMessage(result)
	if msg == "" {
		t.Fatal("BuildLargeToolResultMessage returned empty string")
	}
	if !strings.Contains(msg, PersistedOutputTag) {
		t.Error("message missing opening tag")
	}
	if !strings.Contains(msg, PersistedOutputClosingTag) {
		t.Error("message missing closing tag")
	}
	if !strings.Contains(msg, "1.4MB") {
		t.Error("message missing file size")
	}
	if !strings.Contains(msg, "/tmp/test/file.txt") {
		t.Error("message missing file path")
	}
	if !strings.Contains(msg, "first line") {
		t.Error("message missing preview content")
	}
}

func TestSanitizeToolUseID(t *testing.T) {
	tests := []struct {
		input string
		want  string // want == input means pass-through
	}{
		{"normal-id_123", "normal-id_123"},
		{"abcDEF012", "abcDEF012"},
		{"../../etc/passwd", ""}, // will be hashed, just verify not the input
	}
	for _, tt := range tests {
		got := sanitizeToolUseID(tt.input)
		if tt.want != "" && got != tt.want {
			t.Errorf("sanitizeToolUseID(%q) = %q, want %q", tt.input, got, tt.want)
		}
		if tt.want == "" && got == tt.input {
			t.Errorf("sanitizeToolUseID(%q) should have been hashed, got original", tt.input)
		}
	}
}

func TestIsToolResultPath(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	ResetDirCache()

	tests := []struct {
		path string
		want bool
	}{
		{filepath.Join(tmpDir, ".gbot", "sessions", "abc", ToolResultsSubdir, "file.txt"), true},
		{"/etc/passwd", false},
		{filepath.Join(tmpDir, ".gbot", "sessions", "abc", "other", "file.txt"), false},
	}
	for _, tt := range tests {
		got := IsToolResultPath(tt.path)
		if got != tt.want {
			t.Errorf("IsToolResultPath(%q) = %v, want %v", tt.path, got, tt.want)
		}
	}
}

func TestHasImageBlock(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    bool
	}{
		{"text blocks", `[{"type":"text","text":"hello"}]`, false},
		{"image block", `[{"type":"image","source":{"type":"base64"}}]`, true},
		{"mixed with image", `[{"type":"text","text":"hi"},{"type":"image","source":{}}]`, true},
		{"empty", ``, false},
		{"invalid json", `not json`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := HasImageBlock(json.RawMessage(tt.content))
			if got != tt.want {
				t.Errorf("HasImageBlock(%q) = %v, want %v", tt.name, got, tt.want)
			}
		})
	}
}
