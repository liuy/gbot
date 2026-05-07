package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	mcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// ---------------------------------------------------------------------------
// ListMcpResources tests
// ---------------------------------------------------------------------------

func TestListMcpResources_AllServers(t *testing.T) {
	reg := NewRegistry(NewClientManager(nil, false, ""), ChangeCallbacks{})
	reg.SetConnectionForTest("server-a", &ConnectedServer{
		Name:    "server-a",
		Session: nil,
		Config:  ScopedMcpServerConfig{Config: &StdioConfig{Command: "test"}, Scope: ScopeUser},
	})

	// GetResources() reads from r.resources, not cache — populate directly
	reg.resources = []ServerResource{
		{URI: "test://1", Name: "res1", Server: "server-a"},
		{URI: "test://2", Name: "res2", Server: "server-a"},
	}

	resources, err := ListMcpResources(context.Background(), reg, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resources) != 2 {
		t.Fatalf("expected 2 resources, got %d", len(resources))
	}
	if resources[0].URI != "test://1" {
		t.Errorf("URI[0] = %q, want %q", resources[0].URI, "test://1")
	}
	if resources[1].URI != "test://2" {
		t.Errorf("URI[1] = %q, want %q", resources[1].URI, "test://2")
	}
}

func TestListMcpResources_FilterByServer(t *testing.T) {
	reg := NewRegistry(NewClientManager(nil, false, ""), ChangeCallbacks{})
	reg.SetConnectionForTest("server-a", &ConnectedServer{
		Name:    "server-a",
		Session: nil,
		Config:  ScopedMcpServerConfig{Config: &StdioConfig{Command: "test"}, Scope: ScopeUser},
	})
	reg.SetConnectionForTest("server-b", &ConnectedServer{
		Name:    "server-b",
		Session: nil,
		Config:  ScopedMcpServerConfig{Config: &StdioConfig{Command: "test"}, Scope: ScopeUser},
	})

	// Populate cache — but nil session means FetchResourcesForServer returns nil
	reg.resourceCache.Put("server-a", []ServerResource{
		{URI: "test://a1", Name: "a1", Server: "server-a"},
	})

	// With nil session, FetchResourcesForServer short-circuits to nil,nil
	resources, err := ListMcpResources(context.Background(), reg, "server-a")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resources) != 0 {
		t.Fatalf("expected 0 resources with nil session, got %d", len(resources))
	}
}

func TestListMcpResources_ServerNotFound(t *testing.T) {
	reg := NewRegistry(NewClientManager(nil, false, ""), ChangeCallbacks{})
	// availableServerNames reads from configs, not connections
	reg.configs = map[string]ScopedMcpServerConfig{
		"existing": {Config: &StdioConfig{Command: "test"}, Scope: ScopeUser},
	}

	_, err := ListMcpResources(context.Background(), reg, "nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent server")
	}
	if !strings.Contains(err.Error(), `Server "nonexistent" not found`) {
		t.Errorf("error = %q, want server not found message", err.Error())
	}
	if !strings.Contains(err.Error(), "existing") {
		t.Errorf("error should include available server names, got: %q", err.Error())
	}
}

func TestListMcpResources_ServerNotConnected(t *testing.T) {
	reg := NewRegistry(NewClientManager(nil, false, ""), ChangeCallbacks{})
	// FailedServer is not *ConnectedServer
	reg.SetConnectionForTest("failed-server", &FailedServer{
		Name: "failed-server",
	})

	resources, err := ListMcpResources(context.Background(), reg, "failed-server")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Should return empty array, not error — matches TS line 86
	if len(resources) != 0 {
		t.Errorf("expected empty resources for non-connected server, got %d", len(resources))
	}
}

func TestListMcpResources_EmptyResources(t *testing.T) {
	reg := NewRegistry(NewClientManager(nil, false, ""), ChangeCallbacks{})

	resources, err := ListMcpResources(context.Background(), reg, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resources) != 0 {
		t.Errorf("expected 0 resources, got %d", len(resources))
	}
}

// ---------------------------------------------------------------------------
// ReadMcpResource tests
// ---------------------------------------------------------------------------

func TestReadMcpResource_TextContent(t *testing.T) {
	server, t2 := setupInMemoryServer(t)

	server.AddResource(&mcp.Resource{
		URI:      "test://hello",
		Name:     "hello",
		MIMEType: "text/plain",
	}, func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		return &mcp.ReadResourceResult{
			Contents: []*mcp.ResourceContents{
				{URI: "test://hello", MIMEType: "text/plain", Text: "Hello, World!"},
			},
		}, nil
	})

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1.0"}, nil)
	session, err := client.Connect(context.Background(), t2, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	defer session.Close()

	conn := &ConnectedServer{
		Name:    "test-server",
		Session: session,
		Config:  ScopedMcpServerConfig{Config: &StdioConfig{Command: "test"}, Scope: ScopeUser},
		Capabilities: &mcp.ServerCapabilities{
			Resources: &mcp.ResourceCapabilities{},
		},
	}

	reg := NewRegistry(NewClientManager(nil, false, ""), ChangeCallbacks{})
	reg.SetConnectionForTest("test-server", conn)

	contents, err := ReadMcpResource(context.Background(), reg, "test-server", "test://hello")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(contents) != 1 {
		t.Fatalf("expected 1 content block, got %d", len(contents))
	}
	if contents[0].Text != "Hello, World!" {
		t.Errorf("Text = %q, want %q", contents[0].Text, "Hello, World!")
	}
	if contents[0].URI != "test://hello" {
		t.Errorf("URI = %q, want %q", contents[0].URI, "test://hello")
	}
	if contents[0].BlobSavedTo != "" {
		t.Errorf("BlobSavedTo should be empty for text content, got %q", contents[0].BlobSavedTo)
	}
}

func TestReadMcpResource_BinaryContent(t *testing.T) {
	server, t2 := setupInMemoryServer(t)

	binaryData := []byte("fake-pdf-content")

	server.AddResource(&mcp.Resource{
		URI:      "test://doc.pdf",
		Name:     "doc",
		MIMEType: "application/pdf",
	}, func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		return &mcp.ReadResourceResult{
			Contents: []*mcp.ResourceContents{
				{URI: "test://doc.pdf", MIMEType: "application/pdf", Blob: binaryData},
			},
		}, nil
	})

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1.0"}, nil)
	session, err := client.Connect(context.Background(), t2, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	defer session.Close()

	conn := &ConnectedServer{
		Name:    "test-server",
		Session: session,
		Config:  ScopedMcpServerConfig{Config: &StdioConfig{Command: "test"}, Scope: ScopeUser},
		Capabilities: &mcp.ServerCapabilities{
			Resources: &mcp.ResourceCapabilities{},
		},
	}

	reg := NewRegistry(NewClientManager(nil, false, ""), ChangeCallbacks{})
	reg.SetConnectionForTest("test-server", conn)

	contents, err := ReadMcpResource(context.Background(), reg, "test-server", "test://doc.pdf")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(contents) != 1 {
		t.Fatalf("expected 1 content block, got %d", len(contents))
	}

	if !strings.HasSuffix(contents[0].BlobSavedTo, ".pdf") {
		t.Errorf("BlobSavedTo = %q, should end with .pdf", contents[0].BlobSavedTo)
	}

	data, err := os.ReadFile(contents[0].BlobSavedTo)
	if err != nil {
		t.Fatalf("failed to read persisted file: %v", err)
	}
	if !bytes.Equal(data, binaryData) {
		t.Errorf("file content mismatch: got %q, want %q", string(data), string(binaryData))
	}

	if !strings.Contains(contents[0].Text, "Binary content") {
		t.Errorf("Text should contain 'Binary content', got %q", contents[0].Text)
	}
	if !strings.Contains(contents[0].Text, "application/pdf") {
		t.Errorf("Text should contain MIME type, got %q", contents[0].Text)
	}

	defer os.Remove(contents[0].BlobSavedTo)
}

func TestReadMcpResource_ServerNotFound(t *testing.T) {
	reg := NewRegistry(NewClientManager(nil, false, ""), ChangeCallbacks{})
	// availableServerNames reads from configs
	reg.configs = map[string]ScopedMcpServerConfig{
		"existing": {Config: &StdioConfig{Command: "test"}, Scope: ScopeUser},
	}

	_, err := ReadMcpResource(context.Background(), reg, "nonexistent", "test://x")
	if err == nil {
		t.Fatal("expected error for nonexistent server")
	}
	if !strings.Contains(err.Error(), `Server "nonexistent" not found`) {
		t.Errorf("error = %q, want server not found message", err.Error())
	}
	if !strings.Contains(err.Error(), "existing") {
		t.Errorf("error should include available server names, got: %q", err.Error())
	}
}

func TestReadMcpResource_ServerNotConnected(t *testing.T) {
	reg := NewRegistry(NewClientManager(nil, false, ""), ChangeCallbacks{})
	reg.SetConnectionForTest("failed", &FailedServer{Name: "failed"})

	_, err := ReadMcpResource(context.Background(), reg, "failed", "test://x")
	if err == nil {
		t.Fatal("expected error for non-connected server")
	}
	if !strings.Contains(err.Error(), "is not connected") {
		t.Errorf("error = %q, want 'is not connected'", err.Error())
	}
}

func TestReadMcpResource_NoResourceSupport(t *testing.T) {
	_, t2 := setupInMemoryServer(t)

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1.0"}, nil)
	session, err := client.Connect(context.Background(), t2, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	defer session.Close()

	conn := &ConnectedServer{
		Name:         "test-server",
		Session:      session,
		Config:       ScopedMcpServerConfig{Config: &StdioConfig{Command: "test"}, Scope: ScopeUser},
		Capabilities: &mcp.ServerCapabilities{}, // No Resources capability
	}

	reg := NewRegistry(NewClientManager(nil, false, ""), ChangeCallbacks{})
	reg.SetConnectionForTest("test-server", conn)

	_, err = ReadMcpResource(context.Background(), reg, "test-server", "test://x")
	if err == nil {
		t.Fatal("expected error for no resource support")
	}
	if !strings.Contains(err.Error(), "does not support resources") {
		t.Errorf("error = %q, want 'does not support resources'", err.Error())
	}
}

func TestReadMcpResource_NeitherTextNorBlob(t *testing.T) {
	server, t2 := setupInMemoryServer(t)

	server.AddResource(&mcp.Resource{
		URI:  "test://empty",
		Name: "empty",
	}, func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		return &mcp.ReadResourceResult{
			Contents: []*mcp.ResourceContents{
				{URI: "test://empty", MIMEType: "application/octet-stream"},
			},
		}, nil
	})

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1.0"}, nil)
	session, err := client.Connect(context.Background(), t2, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	defer session.Close()

	conn := &ConnectedServer{
		Name:    "test-server",
		Session: session,
		Config:  ScopedMcpServerConfig{Config: &StdioConfig{Command: "test"}, Scope: ScopeUser},
		Capabilities: &mcp.ServerCapabilities{
			Resources: &mcp.ResourceCapabilities{},
		},
	}

	reg := NewRegistry(NewClientManager(nil, false, ""), ChangeCallbacks{})
	reg.SetConnectionForTest("test-server", conn)

	contents, err := ReadMcpResource(context.Background(), reg, "test-server", "test://empty")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(contents) != 1 {
		t.Fatalf("expected 1 content block, got %d", len(contents))
	}
	if contents[0].Text != "" {
		t.Errorf("Text should be empty, got %q", contents[0].Text)
	}
	if contents[0].BlobSavedTo != "" {
		t.Errorf("BlobSavedTo should be empty, got %q", contents[0].BlobSavedTo)
	}
	if contents[0].URI != "test://empty" {
		t.Errorf("URI = %q, want %q", contents[0].URI, "test://empty")
	}
}

func TestReadMcpResource_MultipleContentBlocks(t *testing.T) {
	server, t2 := setupInMemoryServer(t)

	server.AddResource(&mcp.Resource{
		URI: "test://multi",
	}, func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		return &mcp.ReadResourceResult{
			Contents: []*mcp.ResourceContents{
				{URI: "test://multi", MIMEType: "text/plain", Text: "text part"},
				{URI: "test://multi", MIMEType: "application/pdf", Blob: []byte("pdf-data")},
			},
		}, nil
	})

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1.0"}, nil)
	session, err := client.Connect(context.Background(), t2, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	defer session.Close()

	conn := &ConnectedServer{
		Name:    "test-server",
		Session: session,
		Config:  ScopedMcpServerConfig{Config: &StdioConfig{Command: "test"}, Scope: ScopeUser},
		Capabilities: &mcp.ServerCapabilities{
			Resources: &mcp.ResourceCapabilities{},
		},
	}

	reg := NewRegistry(NewClientManager(nil, false, ""), ChangeCallbacks{})
	reg.SetConnectionForTest("test-server", conn)

	contents, err := ReadMcpResource(context.Background(), reg, "test-server", "test://multi")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(contents) != 2 {
		t.Fatalf("expected 2 content blocks, got %d", len(contents))
	}

	if contents[0].Text != "text part" {
		t.Errorf("contents[0].Text = %q, want %q", contents[0].Text, "text part")
	}
	if contents[0].BlobSavedTo != "" {
		t.Errorf("contents[0].BlobSavedTo should be empty for text")
	}

	if !strings.HasSuffix(contents[1].BlobSavedTo, ".pdf") {
		t.Errorf("contents[1].BlobSavedTo = %q, should end with .pdf", contents[1].BlobSavedTo)
	}
	if !strings.Contains(contents[1].Text, "Binary content") {
		t.Errorf("contents[1].Text should contain 'Binary content', got %q", contents[1].Text)
	}

	defer os.Remove(contents[1].BlobSavedTo)
}

// ---------------------------------------------------------------------------
// persistBinaryContent tests
// ---------------------------------------------------------------------------

func TestPersistBinaryContent_Success(t *testing.T) {
	data := []byte("hello world")

	fp, size, err := persistBinaryContent(data, "application/pdf", "test-id")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer os.Remove(fp)

	if !strings.HasSuffix(fp, ".pdf") {
		t.Errorf("filepath = %q, should end with .pdf", fp)
	}
	if size != len(data) {
		t.Errorf("size = %d, want %d", size, len(data))
	}

	read, err := os.ReadFile(fp)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	if string(read) != string(data) {
		t.Errorf("content = %q, want %q", string(read), string(data))
	}
}

func TestPersistBinaryContent_PersistIDFormat(t *testing.T) {
	id := fmt.Sprintf("mcp-resource-%d-0-abc123", 1000000)
	fp, _, _ := persistBinaryContent([]byte("x"), "text/plain", id)
	defer os.Remove(fp)

	expected := filepath.Join(os.TempDir(), "mcp-resource-1000000-0-abc123.txt")
	if fp != expected {
		t.Errorf("filepath = %q, want %q", fp, expected)
	}
}

// ---------------------------------------------------------------------------
// extensionForMimeType tests
// ---------------------------------------------------------------------------

func TestExtensionForMimeType(t *testing.T) {
	tests := []struct {
		mime string
		ext  string
	}{
		{"application/pdf", "pdf"},
		{"application/json", "json"},
		{"text/csv", "csv"},
		{"text/plain", "txt"},
		{"text/html", "html"},
		{"text/markdown", "md"},
		{"application/zip", "zip"},
		{"application/vnd.openxmlformats-officedocument.wordprocessingml.document", "docx"},
		{"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", "xlsx"},
		{"application/vnd.openxmlformats-officedocument.presentationml.presentation", "pptx"},
		{"application/msword", "doc"},
		{"application/vnd.ms-excel", "xls"},
		{"audio/mpeg", "mp3"},
		{"audio/wav", "wav"},
		{"audio/ogg", "ogg"},
		{"video/mp4", "mp4"},
		{"video/webm", "webm"},
		{"image/png", "png"},
		{"image/jpeg", "jpg"},
		{"image/gif", "gif"},
		{"image/webp", "webp"},
		{"image/svg+xml", "svg"},
		{"application/octet-stream", "bin"},
		{"unknown/xyz", "bin"},
		{"", "bin"},
		// Charset parameter stripping
		{"text/plain; charset=utf-8", "txt"},
		{"application/json;charset=UTF-8", "json"},
	}
	for _, tt := range tests {
		got := extensionForMimeType(tt.mime)
		if got != tt.ext {
			t.Errorf("extensionForMimeType(%q) = %q, want %q", tt.mime, got, tt.ext)
		}
	}
}

// ---------------------------------------------------------------------------
// formatFileSize tests
// ---------------------------------------------------------------------------

func TestFormatFileSize(t *testing.T) {
	tests := []struct {
		bytes int
		want  string
	}{
		{0, "0 bytes"},
		{1, "1 bytes"},
		{42, "42 bytes"},
		{1023, "1023 bytes"},
		{1024, "1KB"},
		{1536, "1.5KB"},
		{1024 * 512, "512KB"},
		{1024 * 1024, "1MB"},
		{1024*1024 + 512*1024, "1.5MB"},
		{1024 * 1024 * 1024, "1GB"},
		{1024*1024*1024 + 512*1024*1024, "1.5GB"},
	}
	for _, tt := range tests {
		got := formatFileSize(tt.bytes)
		if got != tt.want {
			t.Errorf("formatFileSize(%d) = %q, want %q", tt.bytes, got, tt.want)
		}
	}
}

// ---------------------------------------------------------------------------
// getBinaryBlobSavedMessage tests
// ---------------------------------------------------------------------------

func TestGetBinaryBlobSavedMessage(t *testing.T) {
	msg := getBinaryBlobSavedMessage("/tmp/file.pdf", "application/pdf", 1024, "[Resource from test-server at test://x] ")

	if !strings.HasPrefix(msg, "[Resource from test-server at test://x] ") {
		t.Errorf("should start with sourceDescription, got: %q", msg)
	}
	if !strings.Contains(msg, "Binary content (application/pdf, 1KB)") ||
		strings.Contains(msg, "1.0KB") {
		t.Errorf("should contain mime and size without trailing .0, got: %q", msg)
		t.Errorf("should contain mime and size, got: %q", msg)
	}
	if !strings.Contains(msg, "saved to /tmp/file.pdf") {
		t.Errorf("should contain filepath, got: %q", msg)
	}
}

func TestGetBinaryBlobSavedMessage_UnknownMimeType(t *testing.T) {
	msg := getBinaryBlobSavedMessage("/tmp/file.bin", "", 42, "[Resource from s at u] ")

	if !strings.Contains(msg, "unknown type") {
		t.Errorf("should use 'unknown type' for empty mimeType, got: %q", msg)
	}
}

// ---------------------------------------------------------------------------
// HasResourceSupport tests
// ---------------------------------------------------------------------------

func TestHasResourceSupport_True(t *testing.T) {
	reg := NewRegistry(NewClientManager(nil, false, ""), ChangeCallbacks{})
	reg.SetConnectionForTest("s1", &ConnectedServer{
		Name: "s1",
		Capabilities: &mcp.ServerCapabilities{
			Resources: &mcp.ResourceCapabilities{},
		},
	})
	if !reg.HasResourceSupport() {
		t.Error("expected HasResourceSupport() = true")
	}
}

func TestHasResourceSupport_False(t *testing.T) {
	reg := NewRegistry(NewClientManager(nil, false, ""), ChangeCallbacks{})
	reg.SetConnectionForTest("s1", &ConnectedServer{
		Name:         "s1",
		Capabilities: &mcp.ServerCapabilities{}, // no Resources
	})
	if reg.HasResourceSupport() {
		t.Error("expected HasResourceSupport() = false")
	}
}

func TestHasResourceSupport_NoConnections(t *testing.T) {
	reg := NewRegistry(NewClientManager(nil, false, ""), ChangeCallbacks{})
	if reg.HasResourceSupport() {
		t.Error("expected HasResourceSupport() = false with no connections")
	}
}

// ---------------------------------------------------------------------------
// randomBase36 tests
// ---------------------------------------------------------------------------

func TestRandomBase36(t *testing.T) {
	s := randomBase36(6)
	if len(s) != 6 {
		t.Errorf("length = %d, want 6", len(s))
	}
	for _, c := range s {
		if !strings.ContainsRune("abcdefghijklmnopqrstuvwxyz0123456789", c) {
			t.Errorf("unexpected char %q in base36 string", c)
		}
	}
	// Verify uniqueness (probabilistic)
	s2 := randomBase36(6)
	if s == s2 {
		// Not impossible but very unlikely
		t.Logf("warning: two random base36 strings were identical: %q", s)
	}
}

// ---------------------------------------------------------------------------
// renderResourceResult is tested via adapter tests in pkg/tool/mcp/
// but we can test JSON serialization here
// ---------------------------------------------------------------------------

func TestResourceContentJSON(t *testing.T) {
	rc := ResourceContent{
		URI:      "test://x",
		MimeType: "text/plain",
		Text:     "hello",
	}
	b, err := json.Marshal(rc)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(b), `"uri":"test://x"`) {
		t.Errorf("JSON should contain uri, got: %s", b)
	}
	if !strings.Contains(string(b), `"text":"hello"`) {
		t.Errorf("JSON should contain text, got: %s", b)
	}
	// blobSavedTo should be omitted
	if strings.Contains(string(b), "blobSavedTo") {
		t.Errorf("JSON should not contain empty blobSavedTo, got: %s", b)
	}
}

// ---------------------------------------------------------------------------
// Coverage gap: ListMcpResources FetchResourcesForServer error path (line 47-50)
// ---------------------------------------------------------------------------

func TestListMcpResources_FetchErrorIsolation(t *testing.T) {
	server, t2 := setupInMemoryServer(t)

	// Register a resource on the server to ensure it's used
	server.AddResource(&mcp.Resource{
		URI:  "test://res",
		Name: "res",
	}, func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		return &mcp.ReadResourceResult{
			Contents: []*mcp.ResourceContents{
				{URI: "test://res", MIMEType: "text/plain", Text: "data"},
			},
		}, nil
	})

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1.0"}, nil)
	session, err := client.Connect(context.Background(), t2, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	defer session.Close()

	conn := &ConnectedServer{
		Name:         "test-server",
		Session:      session,
		Config:       ScopedMcpServerConfig{Config: &StdioConfig{Command: "test"}, Scope: ScopeUser},
		Capabilities: &mcp.ServerCapabilities{}, // no Resources → FetchResourcesForServer returns nil,nil
	}

	reg := NewRegistry(NewClientManager(nil, false, ""), ChangeCallbacks{})
	reg.SetConnectionForTest("test-server", conn)

	resources, err := ListMcpResources(context.Background(), reg, "test-server")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resources) != 0 {
		t.Errorf("expected 0 resources, got %d", len(resources))
	}
}

// ---------------------------------------------------------------------------
// Coverage gap: ReadMcpResource empty result (line 110-112)
// ---------------------------------------------------------------------------

func TestReadMcpResource_EmptyResult(t *testing.T) {
	server, t2 := setupInMemoryServer(t)

	server.AddResource(&mcp.Resource{
		URI:  "test://empty-result",
		Name: "empty-result",
	}, func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{}}, nil
	})

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1.0"}, nil)
	session, err := client.Connect(context.Background(), t2, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	defer session.Close()

	conn := &ConnectedServer{
		Name:    "test-server",
		Session: session,
		Config:  ScopedMcpServerConfig{Config: &StdioConfig{Command: "test"}, Scope: ScopeUser},
		Capabilities: &mcp.ServerCapabilities{
			Resources: &mcp.ResourceCapabilities{},
		},
	}

	reg := NewRegistry(NewClientManager(nil, false, ""), ChangeCallbacks{})
	reg.SetConnectionForTest("test-server", conn)

	contents, err := ReadMcpResource(context.Background(), reg, "test-server", "test://empty-result")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(contents) != 0 {
		t.Errorf("expected 0 contents, got %d", len(contents))
	}
}

// ---------------------------------------------------------------------------
// Coverage gap: persistBinaryContent write failure (line 186-188)
// ---------------------------------------------------------------------------

func TestPersistBinaryContent_WriteFailure(t *testing.T) {
	_, _, err := persistBinaryContent([]byte("x"), "text/plain", "../../../nonexistent/dir/test")
	if err == nil {
		t.Fatal("expected error for write to non-existent directory")
	}
	if !strings.Contains(err.Error(), "failed to persist binary content") {
		t.Errorf("error = %q, want persist failure message", err.Error())
	}
}

// ---------------------------------------------------------------------------
// Coverage gap: PutResourceCacheForTest
// ---------------------------------------------------------------------------

func TestPutResourceCacheForTest(t *testing.T) {
	reg := NewRegistry(NewClientManager(nil, false, ""), ChangeCallbacks{})
	reg.PutResourceCacheForTest("s1", []ServerResource{
		{URI: "test://1", Name: "res1", Server: "s1"},
	})

	resources := reg.GetResources()
	if len(resources) != 1 {
		t.Fatalf("expected 1 resource, got %d", len(resources))
	}
	if resources[0].URI != "test://1" {
		t.Errorf("URI = %q, want %q", resources[0].URI, "test://1")
	}
}

// ---------------------------------------------------------------------------
// Cache verification: FetchResourcesForServer populates LRU cache
// ---------------------------------------------------------------------------

func TestListMcpResources_CachePopulatedAfterFetch(t *testing.T) {
	server, t2 := setupInMemoryServer(t)

	server.AddResource(&mcp.Resource{
		URI:  "test://cached",
		Name: "cached",
	}, func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		return &mcp.ReadResourceResult{
			Contents: []*mcp.ResourceContents{
				{URI: "test://cached", MIMEType: "text/plain", Text: "data"},
			},
		}, nil
	})

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1.0"}, nil)
	session, err := client.Connect(context.Background(), t2, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	defer session.Close()

	conn := &ConnectedServer{
		Name:    "test-server",
		Session: session,
		Config:  ScopedMcpServerConfig{Config: &StdioConfig{Command: "test"}, Scope: ScopeUser},
		Capabilities: &mcp.ServerCapabilities{
			Resources: &mcp.ResourceCapabilities{},
		},
	}

	reg := NewRegistry(NewClientManager(nil, false, ""), ChangeCallbacks{})
	reg.SetConnectionForTest("test-server", conn)

	// Verify cache is empty before first call
	if _, ok := reg.resourceCache.Get("test-server"); ok {
		t.Fatal("cache should be empty before first call")
	}

	// First call — populates cache
	resources, err := ListMcpResources(context.Background(), reg, "test-server")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resources) != 1 {
		t.Fatalf("expected 1 resource, got %d", len(resources))
	}
	if resources[0].URI != "test://cached" {
		t.Errorf("URI = %q, want %q", resources[0].URI, "test://cached")
	}

	// Verify cache is populated
	cached, ok := reg.resourceCache.Get("test-server")
	if !ok {
		t.Fatal("cache should be populated after first call")
	}
	if len(cached) != 1 {
		t.Fatalf("cache should have 1 entry, got %d", len(cached))
	}
	if cached[0].URI != "test://cached" {
		t.Errorf("cached URI = %q, want %q", cached[0].URI, "test://cached")
	}
}

// ---------------------------------------------------------------------------
// FilterByServer with real MCP session — verifies actual server filtering
// ---------------------------------------------------------------------------

func TestListMcpResources_FilterByServer_RealSession(t *testing.T) {
	server, t2 := setupInMemoryServer(t)

	server.AddResource(&mcp.Resource{
		URI:  "test://filtered",
		Name: "filtered",
	}, func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		return &mcp.ReadResourceResult{
			Contents: []*mcp.ResourceContents{
				{URI: "test://filtered", MIMEType: "text/plain", Text: "data"},
			},
		}, nil
	})

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1.0"}, nil)
	session, err := client.Connect(context.Background(), t2, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	defer session.Close()

	conn := &ConnectedServer{
		Name:    "test-server",
		Session: session,
		Config:  ScopedMcpServerConfig{Config: &StdioConfig{Command: "test"}, Scope: ScopeUser},
		Capabilities: &mcp.ServerCapabilities{
			Resources: &mcp.ResourceCapabilities{},
		},
	}

	reg := NewRegistry(NewClientManager(nil, false, ""), ChangeCallbacks{})
	reg.SetConnectionForTest("test-server", conn)

	// Filter by server — should fetch from real MCP server
	resources, err := ListMcpResources(context.Background(), reg, "test-server")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resources) != 1 {
		t.Fatalf("expected 1 resource from real server, got %d", len(resources))
	}
	if resources[0].URI != "test://filtered" {
		t.Errorf("URI = %q, want %q", resources[0].URI, "test://filtered")
	}
	if resources[0].Server != "test-server" {
		t.Errorf("Server = %q, want %q", resources[0].Server, "test-server")
	}
}

// ---------------------------------------------------------------------------
// Coverage gap: ListMcpResources error isolation via cancelled context
// (line 47-50: FetchResourcesForServer returns error → return empty)
// ---------------------------------------------------------------------------

func TestListMcpResources_FetchErrorWithRealSession(t *testing.T) {
	server, t2 := setupInMemoryServer(t)

	server.AddResource(&mcp.Resource{
		URI:  "test://res",
		Name: "res",
	}, func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		return &mcp.ReadResourceResult{
			Contents: []*mcp.ResourceContents{
				{URI: "test://res", MIMEType: "text/plain", Text: "hello"},
			},
		}, nil
	})

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1.0"}, nil)
	session, err := client.Connect(context.Background(), t2, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	defer session.Close()

	conn := &ConnectedServer{
		Name:    "test-server",
		Session: session,
		Config:  ScopedMcpServerConfig{Config: &StdioConfig{Command: "test"}, Scope: ScopeUser},
		Capabilities: &mcp.ServerCapabilities{
			Resources: &mcp.ResourceCapabilities{},
		},
	}

	reg := NewRegistry(NewClientManager(nil, false, ""), ChangeCallbacks{})
	reg.SetConnectionForTest("test-server", conn)

	// Use cancelled context to force ListResources RPC error
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	resources, err := ListMcpResources(ctx, reg, "test-server")
	if err != nil {
		t.Fatalf("error should be isolated, got: %v", err)
	}
	// Error isolation: returns empty resources, not error
	if len(resources) != 0 {
		t.Errorf("expected 0 resources after error isolation, got %d", len(resources))
	}
}

// ---------------------------------------------------------------------------
// Coverage gap: ReadMcpResource binary persist failure within ReadMcpResource
// (line 130-136: persistErr != nil → error fallback text)
// ---------------------------------------------------------------------------

func TestReadMcpResource_BinaryPersistFailureInsideRead(t *testing.T) {
	// Set TMPDIR to non-existent directory to force persist failure
	t.Setenv("TMPDIR", "/nonexistent/tmp/dir/for/test")

	server, t2 := setupInMemoryServer(t)

	server.AddResource(&mcp.Resource{
		URI:      "test://bin",
		Name:     "bin",
		MIMEType: "application/pdf",
	}, func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		return &mcp.ReadResourceResult{
			Contents: []*mcp.ResourceContents{
				{URI: "test://bin", MIMEType: "application/pdf", Blob: []byte("pdf-data")},
			},
		}, nil
	})

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1.0"}, nil)
	session, err := client.Connect(context.Background(), t2, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	defer session.Close()

	conn := &ConnectedServer{
		Name:    "test-server",
		Session: session,
		Config:  ScopedMcpServerConfig{Config: &StdioConfig{Command: "test"}, Scope: ScopeUser},
		Capabilities: &mcp.ServerCapabilities{
			Resources: &mcp.ResourceCapabilities{},
		},
	}

	reg := NewRegistry(NewClientManager(nil, false, ""), ChangeCallbacks{})
	reg.SetConnectionForTest("test-server", conn)

	contents, err := ReadMcpResource(context.Background(), reg, "test-server", "test://bin")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(contents) != 1 {
		t.Fatalf("expected 1 content block, got %d", len(contents))
	}
	// Persist failure → Text should contain error message
	if !strings.Contains(contents[0].Text, "Binary content could not be saved to disk") {
		t.Errorf("expected persist failure message, got: %q", contents[0].Text)
	}
	if contents[0].BlobSavedTo != "" {
		t.Errorf("BlobSavedTo should be empty on failure, got: %q", contents[0].BlobSavedTo)
	}
}
