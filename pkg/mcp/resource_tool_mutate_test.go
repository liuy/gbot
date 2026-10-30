package mcp

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"

	mcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// ---------------------------------------------------------------------------
// ListMcpResources:46 — the non-connected guard must return a non-nil empty
// slice, not fall through to FetchResourcesForServer(nil). TS
// ListMcpResourcesTool.ts:86 returns [] (an array), and callers marshal the
// result straight to JSON, where nil would render as null.
// ---------------------------------------------------------------------------

func TestMutateListResources_NonConnectedReturnsEmptySlice(t *testing.T) {
	reg := NewRegistry(NewClientManager(nil, false, ""), ChangeCallbacks{})
	reg.SetConnectionForTest("failed", &FailedServer{Name: "failed"})

	resources, err := ListMcpResources(context.Background(), reg, "failed")
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if resources == nil {
		t.Error("resources = nil, want non-nil empty slice")
	}
	if len(resources) != 0 {
		t.Errorf("len(resources) = %d, want 0", len(resources))
	}
}

// ---------------------------------------------------------------------------
// ListMcpResources:49,51 — error isolation is "log then return empty". A
// dropped guard or a dropped slog.Warn keeps the empty return but loses the
// only trace of the failed RPC.
// ---------------------------------------------------------------------------

func TestMutateListResources_FetchErrorIsLoggedAndIsolated(t *testing.T) {
	server, t2 := setupInMemoryServer(t)
	server.AddResource(&mcp.Resource{URI: "test://logme", Name: "logme"},
		func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
			return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{
				{URI: "test://logme", MIMEType: "text/plain", Text: "x"},
			}}, nil
		})

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1.0"}, nil)
	session, err := client.Connect(context.Background(), t2, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	defer session.Close()

	conn := &ConnectedServer{
		Name:         "log-srv",
		Session:      session,
		Config:       ScopedMcpServerConfig{Config: &StdioConfig{Command: "test"}, Scope: ScopeUser},
		Capabilities: &mcp.ServerCapabilities{Resources: &mcp.ResourceCapabilities{}},
	}
	reg := NewRegistry(NewClientManager(nil, false, ""), ChangeCallbacks{})
	reg.SetConnectionForTest("log-srv", conn)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	var resources []ServerResource
	var readErr error
	logs := withCapturedLogs(t, func() {
		resources, readErr = ListMcpResources(ctx, reg, "log-srv")
	})

	if readErr != nil {
		t.Fatalf("err = %v, want nil (error isolation)", readErr)
	}
	if resources == nil {
		t.Error("resources = nil, want non-nil empty slice")
	}
	if len(resources) != 0 {
		t.Errorf("len(resources) = %d, want 0", len(resources))
	}

	recs := logsFor(logs, "mcp: failed to fetch resources")
	if len(recs) != 1 {
		t.Fatalf("log records for %q = %d, want 1 (all: %v)", "mcp: failed to fetch resources", len(recs), logs)
	}
	if recs[0]["server"] != "log-srv" {
		t.Errorf("log server attr = %q, want %q", recs[0]["server"], "log-srv")
	}
	if !strings.Contains(recs[0]["error"], `resources/list for "log-srv"`) {
		t.Errorf("log error attr = %q, want it to name the failed resources/list call", recs[0]["error"])
	}
}

// ---------------------------------------------------------------------------
// ReadMcpResource:112,113 — the `result == nil` half of the empty guard is
// only reachable through a client middleware that answers resources/read with
// a nil result and nil error. Without the guard the nil deref panics.
// ---------------------------------------------------------------------------

func TestMutateReadResource_NilResultGuard(t *testing.T) {
	server, t2 := setupInMemoryServer(t)
	server.AddResource(&mcp.Resource{URI: "test://nilres", Name: "nilres"},
		func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
			return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{
				{URI: "test://nilres", MIMEType: "text/plain", Text: "x"},
			}}, nil
		})

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1.0"}, nil)
	client.AddSendingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			if method == "resources/read" {
				return (*mcp.ReadResourceResult)(nil), nil
			}
			return next(ctx, method, req)
		}
	})
	session, err := client.Connect(context.Background(), t2, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	defer session.Close()

	conn := &ConnectedServer{
		Name:         "nil-srv",
		Session:      session,
		Config:       ScopedMcpServerConfig{Config: &StdioConfig{Command: "test"}, Scope: ScopeUser},
		Capabilities: &mcp.ServerCapabilities{Resources: &mcp.ResourceCapabilities{}},
	}
	reg := NewRegistry(NewClientManager(nil, false, ""), ChangeCallbacks{})
	reg.SetConnectionForTest("nil-srv", conn)

	var contents []ResourceContent
	var readErr error
	func() {
		defer func() {
			if p := recover(); p != nil {
				t.Errorf("ReadMcpResource panicked on nil result: %v", p)
			}
		}()
		contents, readErr = ReadMcpResource(context.Background(), reg, "nil-srv", "test://nilres")
	}()
	if t.Failed() {
		return
	}

	if readErr != nil {
		t.Fatalf("err = %v, want nil", readErr)
	}
	if contents == nil {
		t.Error("contents = nil, want non-nil empty slice")
	}
	if len(contents) != 0 {
		t.Errorf("len(contents) = %d, want 0", len(contents))
	}
}

// ---------------------------------------------------------------------------
// ReadMcpResource:109 — the RPC error must stay wrapped so callers can
// errors.Is/As through the tool boundary.
// ---------------------------------------------------------------------------

func TestMutateReadResource_ReadErrorKeepsCause(t *testing.T) {
	server, t2 := setupInMemoryServer(t)
	server.AddResource(&mcp.Resource{URI: "test://wrap", Name: "wrap"},
		func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
			return &mcp.ReadResourceResult{}, nil
		})

	cause := errors.New("transport reset")
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1.0"}, nil)
	client.AddSendingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			if method == "resources/read" {
				return nil, fmt.Errorf("jsonrpc: %w", cause)
			}
			return next(ctx, method, req)
		}
	})
	session, err := client.Connect(context.Background(), t2, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	defer session.Close()

	conn := &ConnectedServer{
		Name:         "wrap-srv",
		Session:      session,
		Config:       ScopedMcpServerConfig{Config: &StdioConfig{Command: "test"}, Scope: ScopeUser},
		Capabilities: &mcp.ServerCapabilities{Resources: &mcp.ResourceCapabilities{}},
	}
	reg := NewRegistry(NewClientManager(nil, false, ""), ChangeCallbacks{})
	reg.SetConnectionForTest("wrap-srv", conn)

	_, err = ReadMcpResource(context.Background(), reg, "wrap-srv", "test://wrap")
	if err == nil {
		t.Fatal("err = nil, want the wrapped RPC error")
	}
	if !errors.Is(err, cause) {
		t.Errorf("errors.Is(%v, cause) = false, want the RPC cause to stay wrapped", err)
	}
	want := `mcp: read resource "test://wrap" from "wrap-srv": jsonrpc: transport reset`
	if err.Error() != want {
		t.Errorf("err = %q, want %q", err.Error(), want)
	}
}

// ---------------------------------------------------------------------------
// ReadMcpResource:127 — the blob branch triggers on any non-empty blob, so a
// single byte must still be persisted.
// ---------------------------------------------------------------------------

func TestMutateReadResource_SingleByteBlobPersisted(t *testing.T) {
	server, t2 := setupInMemoryServer(t)
	server.AddResource(&mcp.Resource{URI: "test://one", Name: "one", MIMEType: "image/png"},
		func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
			return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{
				{URI: "test://one", MIMEType: "image/png", Blob: []byte{0x00}},
			}}, nil
		})

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1.0"}, nil)
	session, err := client.Connect(context.Background(), t2, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	defer session.Close()

	conn := &ConnectedServer{
		Name:         "byte-srv",
		Session:      session,
		Config:       ScopedMcpServerConfig{Config: &StdioConfig{Command: "test"}, Scope: ScopeUser},
		Capabilities: &mcp.ServerCapabilities{Resources: &mcp.ResourceCapabilities{}},
	}
	reg := NewRegistry(NewClientManager(nil, false, ""), ChangeCallbacks{})
	reg.SetConnectionForTest("byte-srv", conn)

	contents, err := ReadMcpResource(context.Background(), reg, "byte-srv", "test://one")
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if len(contents) != 1 {
		t.Fatalf("len(contents) = %d, want 1", len(contents))
	}
	defer func() { _ = os.Remove(contents[0].BlobSavedTo) }()

	if contents[0].BlobSavedTo == "" {
		t.Fatal("BlobSavedTo = \"\", want the 1-byte blob persisted")
	}
	data, err := os.ReadFile(contents[0].BlobSavedTo)
	if err != nil {
		t.Fatalf("read persisted file: %v", err)
	}
	if !bytes.Equal(data, []byte{0x00}) {
		t.Errorf("persisted bytes = %v, want [0]", data)
	}
	if want := "[Resource from byte-srv at test://one] Binary content (image/png, 1B) saved to "; !strings.HasPrefix(contents[0].Text, want) {
		t.Errorf("Text = %q, want prefix %q", contents[0].Text, want)
	}
}

// ---------------------------------------------------------------------------
// ReadMcpResource:130 — the random suffix of the persist ID is exactly 6
// base36 chars (TS Math.random().toString(36).slice(2, 8)); the index segment
// is the content-block position.
// ---------------------------------------------------------------------------

func TestMutateReadResource_PersistIDShape(t *testing.T) {
	server, t2 := setupInMemoryServer(t)
	server.AddResource(&mcp.Resource{URI: "test://id", Name: "id", MIMEType: "application/pdf"},
		func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
			return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{
				{URI: "test://id", MIMEType: "application/pdf", Blob: []byte("pdf-bytes")},
			}}, nil
		})

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1.0"}, nil)
	session, err := client.Connect(context.Background(), t2, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	defer session.Close()

	conn := &ConnectedServer{
		Name:         "id-srv",
		Session:      session,
		Config:       ScopedMcpServerConfig{Config: &StdioConfig{Command: "test"}, Scope: ScopeUser},
		Capabilities: &mcp.ServerCapabilities{Resources: &mcp.ResourceCapabilities{}},
	}
	reg := NewRegistry(NewClientManager(nil, false, ""), ChangeCallbacks{})
	reg.SetConnectionForTest("id-srv", conn)

	contents, err := ReadMcpResource(context.Background(), reg, "id-srv", "test://id")
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if len(contents) != 1 {
		t.Fatalf("len(contents) = %d, want 1", len(contents))
	}
	defer func() { _ = os.Remove(contents[0].BlobSavedTo) }()

	name := strings.TrimSuffix(filepath.Base(contents[0].BlobSavedTo), ".pdf")
	parts := strings.Split(name, "-")
	if len(parts) != 5 {
		t.Fatalf("persist ID %q split into %d parts, want 5 (mcp-resource-<millis>-<index>-<rand6>)", name, len(parts))
	}
	if parts[0] != "mcp" || parts[1] != "resource" {
		t.Errorf("persist ID prefix = %q-%q, want mcp-resource", parts[0], parts[1])
	}
	if _, err := strconv.ParseInt(parts[2], 10, 64); err != nil {
		t.Errorf("persist ID millis segment %q is not a number", parts[2])
	}
	if parts[3] != "0" {
		t.Errorf("persist ID index segment = %q, want 0", parts[3])
	}
	if len(parts[4]) != 6 {
		t.Errorf("persist ID random segment %q has length %d, want 6", parts[4], len(parts[4]))
	}
	for _, r := range parts[4] {
		if !strings.ContainsRune(base36Chars, r) {
			t.Errorf("persist ID random segment %q has char %q outside base36", parts[4], r)
		}
	}
}

// ---------------------------------------------------------------------------
// persistBinaryContent:188 — persisted blobs hold raw server bytes and must be
// owner-only.
// ---------------------------------------------------------------------------

func TestMutatePersistBinaryContent_OwnerOnlyMode(t *testing.T) {
	// Private TMPDIR: persistBinaryContent reuses a fixed ID in the shared
	// temp dir, and os.WriteFile never chmods an existing file — a leftover
	// from a crashed run would pin 0600 regardless of the mutant.
	// Pinned umask: perm assertions are only exact when umask clears nothing.
	t.Setenv("TMPDIR", t.TempDir())
	old := syscall.Umask(0)
	defer syscall.Umask(old)
	fp, size, err := persistBinaryContent([]byte("secret-bytes"), "application/pdf", "mutate-mode")
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	defer func() { _ = os.Remove(fp) }()

	if size != len("secret-bytes") {
		t.Errorf("size = %d, want %d", size, len("secret-bytes"))
	}
	info, err := os.Stat(fp)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("mode = %O, want 0600", perm)
	}
}

// ---------------------------------------------------------------------------
// persistBinaryContent:189 — a failed write is logged before the error is
// returned; dropping the log loses the only record of the orphaned write.
// ---------------------------------------------------------------------------

func TestMutatePersistBinaryContent_WriteErrorIsLogged(t *testing.T) {
	var fp string
	var size int
	var err error
	logs := withCapturedLogs(t, func() {
		fp, size, err = persistBinaryContent([]byte("x"), "text/plain", "../../../nonexistent/dir/mutate")
	})

	if err == nil {
		t.Fatal("err = nil, want write failure")
	}
	if fp != "" {
		t.Errorf("filepath = %q, want empty string on failure", fp)
	}
	if size != 0 {
		t.Errorf("size = %d, want 0 on failure", size)
	}

	recs := logsFor(logs, "mcp: failed to persist binary content")
	if len(recs) != 1 {
		t.Fatalf("log records for %q = %d, want 1 (all: %v)", "mcp: failed to persist binary content", len(recs), logs)
	}
	if !strings.Contains(recs[0]["error"], "nonexistent/dir/mutate.txt") {
		t.Errorf("log error attr = %q, want it to name the unwritable path", recs[0]["error"])
	}
}

// ---------------------------------------------------------------------------
// extensionForMimeType:210 — the map keys are lowercase, so the lookup input
// must be lowercased before matching.
// ---------------------------------------------------------------------------

func TestMutateExtensionForMimeType_LowercasesLookup(t *testing.T) {
	for _, tt := range []struct{ mime, ext string }{
		{"APPLICATION/PDF", "pdf"},
		{"Text/HTML; Charset=UTF-8", "html"},
		{"Image/PNG", "png"},
	} {
		if got := extensionForMimeType(tt.mime); got != tt.ext {
			t.Errorf("extensionForMimeType(%q) = %q, want %q", tt.mime, got, tt.ext)
		}
	}
}
