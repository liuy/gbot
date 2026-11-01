package mcp

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"log/slog"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	mcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"golang.org/x/image/draw"
)

// ---------------------------------------------------------------------------
// fixtures
// ---------------------------------------------------------------------------

// mutateRawTools puts each named tool behind a JSON-RPC protocol error. The SDK
// only turns plain handler errors into isError results; a *jsonrpc.Error is the
// way to make Session.CallTool itself fail, which is the path CallMCPTool's
// auth and session-expiry classification runs on.
func mutateRawTools(t *testing.T, server *mcp.Server, failures map[string]*jsonrpc.Error) {
	t.Helper()
	for name, rpcErr := range failures {
		e := rpcErr
		server.AddTool(&mcp.Tool{Name: name, InputSchema: &map[string]any{"type": "object"}},
			func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				return nil, e
			})
	}
}

func mutateConnect(t *testing.T, server *mcp.Server, t2 mcp.Transport, serverName string) *ConnectedServer {
	t.Helper()
	client := mcp.NewClient(&mcp.Implementation{Name: "mutate-client", Version: "1.0"}, nil)
	session, err := client.Connect(context.Background(), t2, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return &ConnectedServer{
		Name:         serverName,
		Session:      session,
		Config:       ScopedMcpServerConfig{Config: &StdioConfig{Command: "test"}, Scope: ScopeUser},
		Capabilities: &mcp.ServerCapabilities{Tools: &mcp.ToolCapabilities{}},
	}
}

func mutateGIFBytes(t *testing.T, w, h int, c color.Color) []byte {
	t.Helper()
	src := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(src, src.Bounds(), &image.Uniform{C: c}, image.Point{}, draw.Src)
	var buf bytes.Buffer
	if err := gif.Encode(&buf, src, nil); err != nil {
		t.Fatalf("gif encode: %v", err)
	}
	return buf.Bytes()
}

func mutatePNGBytes(t *testing.T, w, h int, c color.Color) []byte {
	t.Helper()
	src := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(src, src.Bounds(), &image.Uniform{C: c}, image.Point{}, draw.Src)
	var buf bytes.Buffer
	if err := png.Encode(&buf, src); err != nil {
		t.Fatalf("png encode: %v", err)
	}
	return buf.Bytes()
}

// mutateNoisePNGBytes encodes a deterministic pseudo-random noise PNG; noise
// is JPEG-hostile, which lets tests place targetRawSize inside a chosen
// quality rung of the compression ladder.
func mutateNoisePNGBytes(t *testing.T, w, h int) []byte {
	t.Helper()
	src := image.NewRGBA(image.Rect(0, 0, w, h))
	rnd := rand.New(rand.NewPCG(42, 0))
	for y := range h {
		for x := range w {
			src.Set(x, y, color.NRGBA{R: uint8(rnd.IntN(256)), G: uint8(rnd.IntN(256)), B: uint8(rnd.IntN(256)), A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, src); err != nil {
		t.Fatalf("png encode: %v", err)
	}
	return buf.Bytes()
}

// mutateScaledJPEGBytes mirrors resizeImageWithLimits' scale-then-encode step so the
// expected bytes can be compared exactly (JPEG output is deterministic for a
// given image and quality).
func mutateScaledJPEGBytes(t *testing.T, data []byte, w, h, quality int) []byte {
	t.Helper()
	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.CatmullRom.Scale(dst, dst.Bounds(), src, src.Bounds(), draw.Over, nil)
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, dst, &jpeg.Options{Quality: quality}); err != nil {
		t.Fatalf("jpeg encode: %v", err)
	}
	return buf.Bytes()
}

func mutateImageShape(t *testing.T, data []byte) (format string, w, h int) {
	t.Helper()
	img, format, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("decode result: %v", err)
	}
	b := img.Bounds()
	return format, b.Dx(), b.Dy()
}

func mutateRegistryCount() int {
	n := 0
	progressRegistry.Range(func(_, _ any) bool {
		n++
		return true
	})
	return n
}

// ---------------------------------------------------------------------------
// progress plumbing
// ---------------------------------------------------------------------------

// The drain window is a deliberate constant, not a measured value: a ±1ms
// drift changes what a finished call can still report.
func TestProgressDrainGrace_IsTheDeclaredWindow(t *testing.T) {
	if progressDrainGrace != 50*time.Millisecond {
		t.Errorf("progressDrainGrace = %v, want 50ms", progressDrainGrace)
	}
}

// A missing token is the normal post-call state: return false silently.
// Falling through turns it into a registry-corruption error log plus a Delete.
func TestDispatchProgress_UnknownTokenIsNotRegistryCorruption(t *testing.T) {
	rec := &headerLogRecorder{}
	old := slog.Default()
	slog.SetDefault(slog.New(rec))
	t.Cleanup(func() { slog.SetDefault(old) })

	if dispatchProgress(fmt.Sprintf("mutate-missing-%d", os.Getpid()), MCPProgress{}) {
		t.Error("dispatchProgress for an unregistered token = true, want false")
	}
	recs := rec.logged()
	if len(recs) != 0 {
		t.Errorf("log records = %v, want none (a missing token is not a corrupt entry)", recs)
	}
}

// A registry entry that is not a callback is an internal invariant violation;
// the error log plus the Delete are the only evidence it happened.
func TestDispatchProgress_CorruptEntryIsLoggedAndDropped(t *testing.T) {
	rec := &headerLogRecorder{}
	old := slog.Default()
	slog.SetDefault(slog.New(rec))
	t.Cleanup(func() { slog.SetDefault(old) })

	token := fmt.Sprintf("mutate-corrupt-%d", os.Getpid())
	progressRegistry.Store(token, "not a callback")
	t.Cleanup(func() { progressRegistry.Delete(token) })

	if dispatchProgress(token, MCPProgress{}) {
		t.Error("dispatchProgress for a non-callable entry = true, want false")
	}
	recs := rec.logged()
	if len(recs) != 1 || recs[0].msg != "mcp: invalid progress callback type in registry" {
		t.Errorf("log records = %v, want exactly one invalid-callback-type record", recs)
	}
	if _, stillThere := progressRegistry.Load(token); stillThere {
		t.Error("corrupt entry must be deleted from the registry")
	}
}

// The token is random per call, so a leaked registration is only visible as
// a registry entry that outlives the call.
func TestCallMCPTool_UnregistersProgressTokenAfterReturn(t *testing.T) {
	server, t2 := setupInMemoryServer(t)
	server.AddTool(&mcp.Tool{Name: "ok", InputSchema: &map[string]any{"type": "object"}},
		func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "fine"}}}, nil
		})
	conn := mutateConnect(t, server, t2, "prog-server")

	before := mutateRegistryCount()
	if _, err := CallMCPTool(context.Background(), CallMCPToolParams{
		Server:     conn,
		ToolName:   "ok",
		OnProgress: func(MCPProgress) {},
	}); err != nil {
		t.Fatalf("CallMCPTool: %v", err)
	}
	if after := mutateRegistryCount(); after != before {
		t.Errorf("progress registry holds %d entries after the call, want %d (token must be unregistered)", after, before)
	}
}

// ---------------------------------------------------------------------------
// image limits and tool timeout constants
// ---------------------------------------------------------------------------

// These are the apiLimits.ts values verbatim; any drift changes which images
// get re-encoded.
func TestImageResizeLimits_MatchAPILimits(t *testing.T) {
	if imageMaxWidth != 2000 {
		t.Errorf("imageMaxWidth = %d, want 2000", imageMaxWidth)
	}
	if imageMaxHeight != 2000 {
		t.Errorf("imageMaxHeight = %d, want 2000", imageMaxHeight)
	}
	if imageTargetRawSize != 5*1024*1024*3/4 {
		t.Errorf("imageTargetRawSize = %d, want %d (5MB * 3/4)", imageTargetRawSize, 5*1024*1024*3/4)
	}
}

// MCP_TOOL_TIMEOUT only wins when it parses to a strictly positive number.
func TestGetToolTimeoutMs_RejectsNonPositiveValues(t *testing.T) {
	cases := []struct {
		env  string
		want int
	}{
		{"", DefaultToolCallTimeoutMs},
		{"0", DefaultToolCallTimeoutMs},
		{"-5", DefaultToolCallTimeoutMs},
		{"abc", DefaultToolCallTimeoutMs},
		{"1", 1},
		{"123", 123},
	}
	for _, tc := range cases {
		t.Run(fmt.Sprintf("env=%q", tc.env), func(t *testing.T) {
			t.Setenv("MCP_TOOL_TIMEOUT", tc.env)
			if got := GetToolTimeoutMs(); got != tc.want {
				t.Errorf("GetToolTimeoutMs(MCP_TOOL_TIMEOUT=%q) = %d, want %d", tc.env, got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// CallMCPTool error classification
// ---------------------------------------------------------------------------

// The call's _meta must reach the server.
func TestCallMCPTool_ForwardsMetaToServer(t *testing.T) {
	server, t2 := setupInMemoryServer(t)
	server.AddTool(&mcp.Tool{Name: "metatool", InputSchema: &map[string]any{"type": "object"}},
		func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			seen := ""
			if req.Params.Meta != nil {
				if s, ok := req.Params.Meta["prog"].(string); ok {
					seen = s
				}
			}
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "meta=" + seen}}}, nil
		})
	conn := mutateConnect(t, server, t2, "meta-server")

	result, err := CallMCPTool(context.Background(), CallMCPToolParams{
		Server:   conn,
		ToolName: "metatool",
		Meta:     map[string]any{"prog": "yes"},
	})
	if err != nil {
		t.Fatalf("CallMCPTool: %v", err)
	}
	tc, ok := result.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("content[0] = %T, want *mcp.TextContent", result.Content[0])
	}
	if tc.Text != "meta=yes" {
		t.Errorf("server saw %q, want %q (call Meta must reach the server)", tc.Text, "meta=yes")
	}
}

// Either auth keyword alone classifies the failure, and the auth error must
// carry the server name and the original message.
func TestCallMCPTool_AuthKeywordsProduceAuthError(t *testing.T) {
	cases := []struct {
		name    string
		message string
	}{
		{"status-only", "HTTP 401: token expired"},
		{"reason-only", "HTTP Unauthorized: token expired"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server, t2 := setupInMemoryServer(t)
			mutateRawTools(t, server, map[string]*jsonrpc.Error{
				"authfail": {Code: jsonrpc.CodeInternalError, Message: tc.message},
			})
			conn := mutateConnect(t, server, t2, "auth-server")

			_, err := CallMCPTool(context.Background(), CallMCPToolParams{
				Server:   conn,
				ToolName: "authfail",
			})
			if err == nil {
				t.Fatal("expected an auth error")
			}
			authErr, ok := errors.AsType[*McpAuthError](err)
			if !ok {
				t.Fatalf("error type = %T (%v), want *McpAuthError", err, err)
			}
			if authErr.ServerName != "auth-server" {
				t.Errorf("ServerName = %q, want %q", authErr.ServerName, "auth-server")
			}
			if !strings.Contains(authErr.Message, tc.message) {
				t.Errorf("Message = %q, want it to contain %q", authErr.Message, tc.message)
			}
		})
	}
}

// A -32001/404 failure must be attributed and keep its cause chain.
func TestCallMCPTool_SessionExpiredErrorWrapsCause(t *testing.T) {
	server, t2 := setupInMemoryServer(t)
	mutateRawTools(t, server, map[string]*jsonrpc.Error{
		"expired": {Code: -32001, Message: `HTTP 404 Not Found: {"code":-32001,"message":"Session not found"}`},
	})
	conn := mutateConnect(t, server, t2, "expired-server")

	_, err := CallMCPTool(context.Background(), CallMCPToolParams{Server: conn, ToolName: "expired"})
	if err == nil {
		t.Fatal("expected a session-expired error")
	}
	toolErr, ok := errors.AsType[*McpToolCallError](err)
	if !ok {
		t.Fatalf("error type = %T (%v), want *McpToolCallError", err, err)
	}
	if toolErr.ServerName != "expired-server" {
		t.Errorf("ServerName = %q, want %q", toolErr.ServerName, "expired-server")
	}
	if toolErr.ToolName != "expired" {
		t.Errorf("ToolName = %q, want %q", toolErr.ToolName, "expired")
	}
	if !strings.HasPrefix(toolErr.Err.Error(), "session expired: ") {
		t.Errorf("Err = %q, want it to start with %q", toolErr.Err.Error(), "session expired: ")
	}
	if errors.Unwrap(toolErr.Err) == nil {
		t.Errorf("Err = %q, want the original failure wrapped (%%w, not %%v)", toolErr.Err)
	}
}

// The generic failure path must still name the server, the tool, and carry
// the original error.
func TestCallMCPTool_GenericFailureFields(t *testing.T) {
	server, t2 := setupInMemoryServer(t)
	mutateRawTools(t, server, map[string]*jsonrpc.Error{
		"boom": {Code: jsonrpc.CodeInternalError, Message: "server exploded"},
	})
	conn := mutateConnect(t, server, t2, "generic-server")

	_, err := CallMCPTool(context.Background(), CallMCPToolParams{Server: conn, ToolName: "boom"})
	if err == nil {
		t.Fatal("expected an error")
	}
	toolErr, ok := errors.AsType[*McpToolCallError](err)
	if !ok {
		t.Fatalf("error type = %T (%v), want *McpToolCallError", err, err)
	}
	if toolErr.ServerName != "generic-server" {
		t.Errorf("ServerName = %q, want %q", toolErr.ServerName, "generic-server")
	}
	if toolErr.ToolName != "boom" {
		t.Errorf("ToolName = %q, want %q", toolErr.ToolName, "boom")
	}
	if !strings.Contains(toolErr.Err.Error(), "server exploded") {
		t.Errorf("Err = %q, want it to contain %q", toolErr.Err.Error(), "server exploded")
	}
}

// The structured payload must survive the call boundary.
func TestCallMCPTool_ForwardsStructuredContent(t *testing.T) {
	server, t2 := setupInMemoryServer(t)
	server.AddTool(&mcp.Tool{Name: "structured", InputSchema: &map[string]any{"type": "object"}},
		func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{
				Content:           []mcp.Content{&mcp.TextContent{Text: "see structured"}},
				StructuredContent: map[string]any{"k": "v"},
			}, nil
		})
	conn := mutateConnect(t, server, t2, "struct-server")

	result, err := CallMCPTool(context.Background(), CallMCPToolParams{Server: conn, ToolName: "structured"})
	if err != nil {
		t.Fatalf("CallMCPTool: %v", err)
	}
	got, ok := result.StructuredContent.(map[string]any)
	if !ok {
		t.Fatalf("StructuredContent = %T (%v), want map[string]any", result.StructuredContent, result.StructuredContent)
	}
	if got["k"] != "v" {
		t.Errorf("StructuredContent = %v, want k=v", got)
	}
}

// ---------------------------------------------------------------------------
// InferCompactSchema
// ---------------------------------------------------------------------------

// The signature caps itself at ten keys and says so.
func TestInferCompactSchema_KeyLimitIsTen(t *testing.T) {
	value := map[string]any{}
	for _, k := range []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j", "k"} {
		value[k] = 1.0
	}
	want := "{a: number, b: number, c: number, d: number, e: number, f: number, g: number, h: number, i: number, j: number, ...}"
	if got := InferCompactSchema(value); got != want {
		t.Errorf("InferCompactSchema(11 keys) = %q, want %q", got, want)
	}
}

// ---------------------------------------------------------------------------
// TransformResultContent
// ---------------------------------------------------------------------------

// An image inside the limits is returned byte-for-byte by the fast path, and
// sniffing that payload confirms the declared type.
func TestTransformResultContent_ImageKeepsMIMEWhenNotShrunk(t *testing.T) {
	data := mutatePNGBytes(t, 10, 10, color.NRGBA{R: 0xFF, A: 0xFF})
	got := TransformResultContent(&mcp.ImageContent{Data: data, MIMEType: "image/png"}, "srv")
	if len(got) != 1 {
		t.Fatalf("results = %d, want 1", len(got))
	}
	if got[0].MIMEType != "image/png" {
		t.Errorf("MIMEType = %q, want image/png (the bytes were not re-encoded)", got[0].MIMEType)
	}
	raw, err := base64.StdEncoding.DecodeString(got[0].RawData)
	if err != nil {
		t.Fatalf("RawData is not base64: %v", err)
	}
	if !bytes.Equal(raw, data) {
		t.Errorf("RawData = %d bytes, want the original %d bytes unchanged", len(raw), len(data))
	}
}

// When a gif source is re-encoded smaller the payload really is JPEG, and the
// declared type has to follow the bytes.
func TestTransformResultContent_ShrunkImageBecomesJPEG(t *testing.T) {
	// REAL-TIME: padding pushes the payload past imageTargetRawSize so the
	// resize path runs; the decoder ignores trailing bytes.
	data := append(mutateGIFBytes(t, 10, 10, color.NRGBA{R: 0xFF, A: 0xFF}), make([]byte, int(imageTargetRawSize)+1)...)
	got := TransformResultContent(&mcp.ImageContent{Data: data, MIMEType: "image/gif"}, "srv")
	if len(got) != 1 {
		t.Fatalf("results = %d, want 1", len(got))
	}
	if got[0].MIMEType != "image/jpeg" {
		t.Errorf("MIMEType = %q, want image/jpeg (the re-encode shrank the payload)", got[0].MIMEType)
	}
	raw, err := base64.StdEncoding.DecodeString(got[0].RawData)
	if err != nil {
		t.Fatalf("RawData is not base64: %v", err)
	}
	if len(raw) >= len(data) {
		t.Errorf("payload = %d bytes, want fewer than the original %d", len(raw), len(data))
	}
	if !bytes.HasPrefix(raw, []byte("\xff\xd8\xff")) {
		t.Errorf("payload magic = %x, want JPEG", raw[:min(3, len(raw))])
	}
}

// TS takes media_type from the resizer's own output (client.ts:2503-2519).
// A png source is re-encoded by png.Encode, so shrinking it must NOT relabel
// the payload as image/jpeg.
func TestTransformResultContent_ResizedPngKeepsPngMIME(t *testing.T) {
	// REAL-TIME: padding pushes the payload past imageTargetRawSize so the
	// resize path runs.
	data := append(mutatePNGBytes(t, 10, 10, color.NRGBA{R: 0xFF, A: 0xFF}), make([]byte, int(imageTargetRawSize)+1)...)
	got := TransformResultContent(&mcp.ImageContent{Data: data, MIMEType: "image/png"}, "srv")
	if len(got) != 1 {
		t.Fatalf("results = %d, want 1", len(got))
	}
	if got[0].MIMEType != "image/png" {
		t.Errorf("MIMEType = %q, want image/png (png source re-encodes to png)", got[0].MIMEType)
	}
	raw, err := base64.StdEncoding.DecodeString(got[0].RawData)
	if err != nil {
		t.Fatalf("RawData is not base64: %v", err)
	}
	if !bytes.HasPrefix(raw, []byte("\x89PNG")) {
		t.Errorf("payload magic = %x, want PNG (declared type must match encoded format)", raw[:min(4, len(raw))])
	}
}

// Extreme aspect ratios floor the preserved dimension to 0 and encoders
// reject zero dimensions: clamp to 1 on both axes.
func TestResizeImageWithLimits_DegenerateAspectRatioClampsTo1(t *testing.T) {
	cases := []struct {
		name         string
		w, h         int
		wantW, wantH int
	}{
		{"horizontal 4001x1", 4001, 1, 2000, 1},
		{"vertical 1x4001", 1, 4001, 1, 2000},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			data := mutatePNGBytes(t, tc.w, tc.h, color.NRGBA{R: 0xFF, A: 0xFF})
			got, err := resizeImageWithLimits(data, "image/png", 2000, 2000, 1<<20)
			if err != nil {
				t.Fatalf("resize: %v", err)
			}
			if len(got) == 0 {
				t.Fatal("resized payload empty: zero-dimension encode failure")
			}
			cfg, format, derr := image.DecodeConfig(bytes.NewReader(got))
			if derr != nil {
				t.Fatalf("payload not decodable: %v", derr)
			}
			if format != "png" {
				t.Errorf("format = %q, want png", format)
			}
			if cfg.Width != tc.wantW || cfg.Height != tc.wantH {
				t.Errorf("dimensions = %dx%d, want %dx%d", cfg.Width, cfg.Height, tc.wantW, tc.wantH)
			}
		})
	}
}

// TS walks a quality ladder [80,60,40,20] and returns the first rung that
// fits (imageResizer.ts:256-273). The payload must be byte-exact per rung,
// and target=1 must descend to the q20 floor.
func TestResizeImageWithLimits_QualityLadderReachesTarget(t *testing.T) {
	data := mutateNoisePNGBytes(t, 100, 100)
	src, _, derr := image.Decode(bytes.NewReader(data))
	if derr != nil {
		t.Fatalf("decode fixture: %v", derr)
	}
	encode := func(q int) []byte {
		var buf bytes.Buffer
		if err := jpeg.Encode(&buf, src, &jpeg.Options{Quality: q}); err != nil {
			t.Fatalf("q%d: %v", q, err)
		}
		return buf.Bytes()
	}
	q60, q40, q20 := encode(60), encode(40), encode(20)
	if len(q60) <= len(q40) || len(q40) <= len(q20) {
		t.Fatalf("ladder not monotonic: q60=%d q40=%d q20=%d", len(q60), len(q40), len(q20))
	}

	t.Run("stops at first fitting rung", func(t *testing.T) {
		target := int64((len(q60) + len(q40)) / 2)
		got, err := resizeImageWithLimits(data, "image/png", 2000, 2000, target)
		if err != nil {
			t.Fatalf("resize: %v", err)
		}
		if !bytes.Equal(got, q40) {
			t.Errorf("payload = %d bytes, want the q40 encoding (%d B) byte-exact", len(got), len(q40))
		}
	})
	t.Run("descends to q20 floor", func(t *testing.T) {
		got, err := resizeImageWithLimits(data, "image/png", 2000, 2000, 1)
		if err != nil {
			t.Fatalf("resize: %v", err)
		}
		if !bytes.Equal(got, q20) {
			t.Errorf("payload = %d bytes, want the q20 encoding (%d B) byte-exact", len(got), len(q20))
		}
	})
}

// The progress token must be random per call — a constant token would make
// concurrent progress callbacks share one registry key.
func TestCallMCPTool_ProgressTokensAreDistinct(t *testing.T) {
	var seen [][]string
	server, t2 := setupInMemoryServer(t)
	mcp.AddTool(server, &mcp.Tool{Name: "tok_tool", Description: "d"},
		func(ctx context.Context, req *mcp.CallToolRequest, input map[string]any) (*mcp.CallToolResult, any, error) {
			var batch []string
			progressRegistry.Range(func(k, v any) bool {
				if s, ok := k.(string); ok {
					batch = append(batch, s)
				}
				return true
			})
			seen = append(seen, batch)
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "ok"}}}, nil, nil
		})
	conn := mutateConnect(t, server, t2, "tok-server")
	for i := range 2 {
		_, err := CallMCPTool(context.Background(), CallMCPToolParams{
			Server:     conn,
			ToolName:   "tok_tool",
			Args:       map[string]any{},
			OnProgress: func(MCPProgress) {},
		})
		if err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	for i, batch := range seen {
		if len(batch) != 1 {
			t.Fatalf("call %d saw %d registry tokens, want exactly 1 (its own in-flight token)", i, len(batch))
		}
	}
	if seen[0][0] == seen[1][0] {
		t.Errorf("progress tokens identical (%s): rand.Read produced a constant token", seen[0][0])
	}
}

// The debug line is the only trace that a resize was attempted and failed,
// and it must not appear when the resize succeeded.
func TestTransformResultContent_ResizeFailureIsLogged(t *testing.T) {
	rec := &headerLogRecorder{}
	old := slog.Default()
	slog.SetDefault(slog.New(rec))
	t.Cleanup(func() { slog.SetDefault(old) })

	got := TransformResultContent(&mcp.ImageContent{Data: []byte("not an image"), MIMEType: "image/png"}, "srv")
	if len(got) != 1 {
		t.Fatalf("results = %d, want 1 (the original bytes must still be returned)", len(got))
	}
	recs := rec.logged()
	if len(recs) != 1 || recs[0].msg != "mcp: image resize failed, using original" {
		t.Errorf("log records = %v, want exactly one resize-failure record", recs)
	}

	// The negated guard logs on success instead; a within-limits image must be
	// silent.
	rec2 := &headerLogRecorder{}
	slog.SetDefault(slog.New(rec2))
	TransformResultContent(&mcp.ImageContent{Data: mutatePNGBytes(t, 4, 4, color.NRGBA{A: 0xFF}), MIMEType: "image/png"}, "srv")
	if recs2 := rec2.logged(); len(recs2) != 0 {
		t.Errorf("log records for a successful resize = %v, want none", recs2)
	}
}

// Audio is only useful if the bytes and their type travel.
func TestTransformResultContent_AudioCarriesRawDataAndMIME(t *testing.T) {
	data := []byte{0x01, 0x02, 0x03, 0xFE}
	got := TransformResultContent(&mcp.AudioContent{Data: data, MIMEType: "audio/wav"}, "srv")
	if len(got) != 1 {
		t.Fatalf("results = %d, want 1", len(got))
	}
	wantB64 := base64.StdEncoding.EncodeToString(data)
	if got[0].RawData != wantB64 {
		t.Errorf("RawData = %q, want %q", got[0].RawData, wantB64)
	}
	if got[0].MIMEType != "audio/wav" {
		t.Errorf("MIMEType = %q, want audio/wav", got[0].MIMEType)
	}
	if got[0].Content != "[Audio from srv: audio/wav, 4 bytes]" {
		t.Errorf("Content = %q, want %q", got[0].Content, "[Audio from srv: audio/wav, 4 bytes]")
	}
}

// The text-resource result must keep its Type.
func TestTransformResultContent_ResourceTextKeepsType(t *testing.T) {
	got := TransformResultContent(&mcp.EmbeddedResource{
		Resource: &mcp.ResourceContents{URI: "file://a.txt", Text: "hello"},
	}, "srv")
	if len(got) != 1 {
		t.Fatalf("results = %d, want 1", len(got))
	}
	if got[0].Type != MCPResultResource {
		t.Errorf("Type = %q, want %q", got[0].Type, MCPResultResource)
	}
	if got[0].Content != "[Resource from srv at file://a.txt]\nhello" {
		t.Errorf("Content = %q, want %q", got[0].Content, "[Resource from srv at file://a.txt]\nhello")
	}
}

// A one-byte blob is still a binary resource.
func TestTransformResultContent_SingleByteBlobIsABinaryResource(t *testing.T) {
	got := TransformResultContent(&mcp.EmbeddedResource{
		Resource: &mcp.ResourceContents{URI: "file://b.bin", Blob: []byte{0x01}, MIMEType: "application/octet-stream"},
	}, "srv")
	if len(got) != 1 {
		t.Fatalf("results = %d, want 1 (a 1-byte blob is not empty)", len(got))
	}
	if got[0].Type != MCPResultResource {
		t.Errorf("Type = %q, want %q", got[0].Type, MCPResultResource)
	}
}

// The binary-resource result must keep Type, RawData and MIMEType.
func TestTransformResultContent_BinaryResourceFields(t *testing.T) {
	blob := []byte{0xDE, 0xAD, 0xBE, 0xEF, 0x00}
	got := TransformResultContent(&mcp.EmbeddedResource{
		Resource: &mcp.ResourceContents{URI: "file://b.bin", Blob: blob, MIMEType: "application/pdf"},
	}, "srv")
	if len(got) != 1 {
		t.Fatalf("results = %d, want 1", len(got))
	}
	if got[0].Type != MCPResultResource {
		t.Errorf("Type = %q, want %q", got[0].Type, MCPResultResource)
	}
	wantB64 := base64.StdEncoding.EncodeToString(blob)
	if got[0].RawData != wantB64 {
		t.Errorf("RawData = %q, want %q", got[0].RawData, wantB64)
	}
	if got[0].MIMEType != "application/pdf" {
		t.Errorf("MIMEType = %q, want application/pdf", got[0].MIMEType)
	}
}

// The resource-link result must keep its Type.
func TestTransformResultContent_ResourceLinkKeepsType(t *testing.T) {
	got := TransformResultContent(&mcp.ResourceLink{Name: "doc", URI: "file://d", Description: "a doc"}, "srv")
	if len(got) != 1 {
		t.Fatalf("results = %d, want 1", len(got))
	}
	if got[0].Type != MCPResultText {
		t.Errorf("Type = %q, want %q", got[0].Type, MCPResultText)
	}
	if got[0].Content != "[Resource link: doc] file://d (a doc)" {
		t.Errorf("Content = %q, want %q", got[0].Content, "[Resource link: doc] file://d (a doc)")
	}
}

// ---------------------------------------------------------------------------
// TransformMCPResult
// ---------------------------------------------------------------------------

// The structured result must keep its Type.
func TestTransformMCPResult_StructuredContentKeepsType(t *testing.T) {
	got, schema := TransformMCPResult(&mcp.CallToolResult{
		StructuredContent: map[string]any{"n": 1.0},
	}, "tool", "srv")
	if len(got) != 1 {
		t.Fatalf("results = %d, want 1", len(got))
	}
	if got[0].Type != MCPResultText {
		t.Errorf("Type = %q, want %q", got[0].Type, MCPResultText)
	}
	if schema != "{n: number}" {
		t.Errorf("schema = %q, want %q", schema, "{n: number}")
	}
}

// Dropping the emptiness guard makes the schema step index all[0], which
// panics when every content block was dropped.
func TestTransformMCPResult_UnsupportedContentYieldsNoSchema(t *testing.T) {
	got, schema := TransformMCPResult(&mcp.CallToolResult{
		Content: []mcp.Content{&mcp.ToolUseContent{}},
	}, "tool", "srv")
	if len(got) != 0 {
		t.Errorf("results = %v, want none (ToolUseContent is not transformable)", got)
	}
	if schema != "" {
		t.Errorf("schema = %q, want empty", schema)
	}
}

// The schema describes the first transformed block, so a single-block result
// must still produce one.
func TestTransformMCPResult_SchemaFromFirstTransformedBlock(t *testing.T) {
	got, schema := TransformMCPResult(&mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: "hello"}},
	}, "tool", "srv")
	if len(got) != 1 {
		t.Fatalf("results = %d, want 1", len(got))
	}
	if schema != "string" {
		t.Errorf("schema = %q, want %q (signature of the first block)", schema, "string")
	}
}

// ---------------------------------------------------------------------------
// ProcessMCPResult
// ---------------------------------------------------------------------------

// The large-output threshold is the TS constant.
func TestMaxMCPContentChars_MatchesTSValue(t *testing.T) {
	if maxMCPContentChars != 50000 {
		t.Errorf("maxMCPContentChars = %d, want 50000", maxMCPContentChars)
	}
}

// An empty text block contributes nothing; joining it in adds a stray newline.
func TestProcessMCPResult_SkipsEmptyTextBlocks(t *testing.T) {
	got, err := ProcessMCPResult(&mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: ""}, &mcp.TextContent{Text: "hello"}},
	}, "tool", "srv")
	if err != nil {
		t.Fatalf("ProcessMCPResult: %v", err)
	}
	if got != "hello" {
		t.Errorf("content = %q, want %q (empty blocks must not add a separator)", got, "hello")
	}
}

// Output exactly at the threshold is not large output: it must come back
// untouched, with no file written.
func TestProcessMCPResult_AtLimitIsReturnedVerbatim(t *testing.T) {
	body := strings.Repeat("x", maxMCPContentChars)
	got, err := ProcessMCPResult(&mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: body}},
	}, "tool", "srv")
	if err != nil {
		t.Fatalf("ProcessMCPResult: %v", err)
	}
	if got != body {
		t.Errorf("content length = %d, want the %d-char body returned verbatim", len(got), len(body))
	}
}

// With large-output files disabled the caller still gets the truncated body.
func TestProcessMCPResult_LargeOutputGateDisabledTruncates(t *testing.T) {
	t.Setenv("ENABLE_MCP_LARGE_OUTPUT_FILES", "false")
	body := strings.Repeat("y", maxMCPContentChars+1)
	got, err := ProcessMCPResult(&mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: body}},
	}, "tool", "srv")
	if err != nil {
		t.Fatalf("ProcessMCPResult: %v", err)
	}
	want := body[:maxMCPContentChars] + "\n... [truncated]"
	if got != want {
		t.Errorf("content = %d bytes ending %q, want %d bytes ending %q",
			len(got), got[len(got)-16:], len(want), want[len(want)-16:])
	}
}

// Image payloads are never written to disk, so the oversized text has to come
// back truncated rather than vanish.
func TestProcessMCPResult_LargeOutputWithImageTruncates(t *testing.T) {
	body := strings.Repeat("w", maxMCPContentChars+1)
	got, err := ProcessMCPResult(&mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.TextContent{Text: body},
			&mcp.ImageContent{Data: mutatePNGBytes(t, 4, 4, color.NRGBA{A: 0xFF}), MIMEType: "image/png"},
		},
	}, "tool", "srv")
	if err != nil {
		t.Fatalf("ProcessMCPResult: %v", err)
	}
	want := body[:maxMCPContentChars] + "\n... [truncated]"
	if got != want {
		t.Errorf("content = %d bytes ending %q, want %d bytes ending %q",
			len(got), got[max(len(got)-16, 0):], len(want), want[len(want)-16:])
	}
}

// When the file write fails the caller must still get the truncated body, not
// a "persisted" pointer to a path that was never written.
func TestProcessMCPResult_PersistFailureFallsBackToTruncation(t *testing.T) {
	t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "mutate-missing-dir"))
	body := strings.Repeat("z", maxMCPContentChars+1)
	got, err := ProcessMCPResult(&mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: body}},
	}, "tool", "srv")
	if err != nil {
		t.Fatalf("ProcessMCPResult: %v", err)
	}
	want := body[:maxMCPContentChars] + "\n... [truncated]"
	if got != want {
		t.Errorf("content = %q..., want the truncated body (persist must fall back)", got[:40])
	}
}

// ---------------------------------------------------------------------------
// resizeImageWithLimits
// ---------------------------------------------------------------------------

// The decoder's own diagnosis has to stay reachable.
func TestResizeImage_DecodeFailureWrapsCause(t *testing.T) {
	_, err := resizeImage([]byte("definitely not an image"), "image/png")
	if err == nil {
		t.Fatal("expected a decode error")
	}
	if !strings.HasPrefix(err.Error(), "mcp: decode image: ") {
		t.Errorf("error = %q, want the decode-image prefix", err.Error())
	}
	if errors.Unwrap(err) == nil {
		t.Errorf("error = %q, want the decoder error wrapped (%%w, not %%v)", err)
	}
}

// The fast path must trigger at the exact boundary and the aspect-ratio
// scaling must produce these shapes.
func TestResizeImageWithLimits_FastPathAndScaling(t *testing.T) {
	red := color.NRGBA{R: 0xFF, A: 0xFF}

	t.Run("exactly at every limit keeps the original bytes", func(t *testing.T) {
		data := mutateGIFBytes(t, 10, 10, red)
		got, err := resizeImageWithLimits(data, "image/gif", 10, 10, int64(len(data)))
		if err != nil {
			t.Fatalf("resizeImageWithLimits: %v", err)
		}
		if !bytes.Equal(got, data) {
			t.Errorf("got %d re-encoded bytes, want the original %d bytes (fast path)", len(got), len(data))
		}
	})

	t.Run("size one byte over the limit re-encodes", func(t *testing.T) {
		data := mutateGIFBytes(t, 10, 10, red)
		got, err := resizeImageWithLimits(data, "image/gif", 10, 10, int64(len(data))-1)
		if err != nil {
			t.Fatalf("resizeImageWithLimits: %v", err)
		}
		format, w, h := mutateImageShape(t, got)
		if format != "jpeg" || w != 10 || h != 10 {
			t.Errorf("got %s %dx%d, want jpeg 10x10 (size alone must trigger the resize path)", format, w, h)
		}
	})

	t.Run("width over the limit scales both axes", func(t *testing.T) {
		data := mutateGIFBytes(t, 20, 5, red)
		got, err := resizeImageWithLimits(data, "image/gif", 10, 10, 1<<20)
		if err != nil {
			t.Fatalf("resizeImageWithLimits: %v", err)
		}
		if _, w, h := mutateImageShape(t, got); w != 10 || h != 2 {
			t.Errorf("got %dx%d, want 10x2", w, h)
		}
	})

	t.Run("height over the limit scales both axes", func(t *testing.T) {
		data := mutateGIFBytes(t, 5, 20, red)
		got, err := resizeImageWithLimits(data, "image/gif", 10, 10, 1<<20)
		if err != nil {
			t.Fatalf("resizeImageWithLimits: %v", err)
		}
		if _, w, h := mutateImageShape(t, got); w != 2 || h != 10 {
			t.Errorf("got %dx%d, want 2x10", w, h)
		}
	})

	t.Run("under every limit keeps the shape", func(t *testing.T) {
		data := mutateGIFBytes(t, 5, 5, red)
		got, err := resizeImageWithLimits(data, "image/gif", 10, 10, int64(len(data))-1)
		if err != nil {
			t.Fatalf("resizeImageWithLimits: %v", err)
		}
		if _, w, h := mutateImageShape(t, got); w != 5 || h != 5 {
			t.Errorf("got %dx%d, want 5x5 (neither axis is over the limit, so neither may scale)", w, h)
		}
	})
}

// Without the Scale call the destination buffer stays blank: the output is a
// correctly sized but empty image.
func TestResizeImageWithLimits_PreservesPixels(t *testing.T) {
	red := color.NRGBA{R: 0xFF, A: 0xFF}
	data := mutateGIFBytes(t, 20, 5, red)
	got, err := resizeImageWithLimits(data, "image/gif", 10, 10, 1<<20)
	if err != nil {
		t.Fatalf("resizeImageWithLimits: %v", err)
	}
	img, _, err := image.Decode(bytes.NewReader(got))
	if err != nil {
		t.Fatalf("decode result: %v", err)
	}
	r, g, b, _ := img.At(5, 1).RGBA()
	if r < 0xF000 || g > 0x1000 || b > 0x1000 {
		t.Errorf("center pixel = (%#x,%#x,%#x), want red (the scaled image must carry the source pixels)", r, g, b)
	}
}

// The first-pass JPEG quality is a fixed contract with the API.
func TestResizeImageWithLimits_FirstPassQualityIs80(t *testing.T) {
	red := color.NRGBA{R: 0xFF, A: 0xFF}
	data := mutateGIFBytes(t, 20, 5, red)
	got, err := resizeImageWithLimits(data, "image/gif", 10, 10, 1<<20)
	if err != nil {
		t.Fatalf("resizeImageWithLimits: %v", err)
	}
	want := mutateScaledJPEGBytes(t, data, 10, 2, 80)
	if !bytes.Equal(got, want) {
		t.Errorf("output = %d bytes, want the %d-byte quality-80 encoding", len(got), len(want))
	}
}

// The ladder must descend rung by rung and stop at the first fit.
func TestResizeImageWithLimits_SecondPassQualityIs60(t *testing.T) {
	red := color.NRGBA{R: 0xFF, A: 0xFF}
	data := mutateGIFBytes(t, 20, 5, red)
	q80 := mutateScaledJPEGBytes(t, data, 10, 2, 80)
	q60 := mutateScaledJPEGBytes(t, data, 10, 2, 60)

	t.Run("output exactly at the target keeps quality 80", func(t *testing.T) {
		got, err := resizeImageWithLimits(data, "image/gif", 10, 10, int64(len(q80)))
		if err != nil {
			t.Fatalf("resizeImageWithLimits: %v", err)
		}
		if !bytes.Equal(got, q80) {
			t.Errorf("output = %d bytes, want the %d-byte quality-80 encoding (len == target is not over it)", len(got), len(q80))
		}
	})

	t.Run("output over the target re-encodes at quality 60", func(t *testing.T) {
		got, err := resizeImageWithLimits(data, "image/gif", 10, 10, int64(len(q80))-1)
		if err != nil {
			t.Fatalf("resizeImageWithLimits: %v", err)
		}
		if !bytes.Equal(got, q60) {
			t.Errorf("output = %d bytes, want the %d-byte quality-60 re-encode", len(got), len(q60))
		}
	})
}

// ---------------------------------------------------------------------------
// truncateContent / persistToolResult
// ---------------------------------------------------------------------------

// Content exactly at the limit is not truncated.
func TestTruncateContent_AtLimitIsUnchanged(t *testing.T) {
	s := "abcdefghij"
	if got := truncateContent(s, len(s)); got != s {
		t.Errorf("truncateContent(len=%d, max=10) = %q, want %q", len(s), got, s)
	}
	if got := truncateContent(s, 4); got != "abcd\n... [truncated]" {
		t.Errorf("truncateContent(max=4) = %q, want %q", got, "abcd\n... [truncated]")
	}
}

// Persisted tool output can hold secrets: the file must be owner-only.
func TestPersistToolResult_WritesOwnerOnlyFile(t *testing.T) {
	oldUmask := syscall.Umask(0)
	defer syscall.Umask(oldUmask)
	t.Setenv("TMPDIR", t.TempDir())

	path, err := persistToolResult("payload", fmt.Sprintf("mutate-%d", os.Getpid()))
	if err != nil {
		t.Fatalf("persistToolResult: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0600 {
		t.Errorf("mode = %#o, want 0600", info.Mode().Perm())
	}
}

// The OS reason (ENOENT, EACCES) is what makes a persistence failure diagnosable.
func TestPersistToolResult_FailureWrapsCause(t *testing.T) {
	t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "mutate-missing-dir"))
	_, err := persistToolResult("payload", fmt.Sprintf("mutate-%d", os.Getpid()))
	if err == nil {
		t.Fatal("expected a persistence error")
	}
	if !strings.HasPrefix(err.Error(), "mcp: failed to persist tool result: ") {
		t.Errorf("error = %q, want the persist prefix", err.Error())
	}
	if errors.Unwrap(err) == nil {
		t.Errorf("error = %q, want the OS error wrapped (%%w, not %%v)", err)
	}
}

// ---------------------------------------------------------------------------
// CallMCPToolWithUrlElicitationRetry / isURLElicitationError
// ---------------------------------------------------------------------------

func mutateElicitationStub(t *testing.T, calls *int, onCall func(ctx context.Context)) error {
	t.Helper()
	old := _callMCPTool
	t.Cleanup(func() { _callMCPTool = old })
	underlying := &jsonrpc.Error{Code: mcp.CodeURLElicitationRequired, Message: "URL elicitation required"}
	_callMCPTool = func(ctx context.Context, p CallMCPToolParams) (*MCPToolCallResult, error) {
		*calls++
		if onCall != nil {
			onCall(ctx)
		}
		return nil, &McpToolCallError{ServerName: p.Server.Name, ToolName: p.ToolName, Err: underlying}
	}
	return underlying
}

// The budget is exactly one initial call plus three retries.
func TestCallMCPToolWithUrlElicitationRetry_BudgetIsThreeRetries(t *testing.T) {
	calls := 0
	underlying := mutateElicitationStub(t, &calls, nil)

	_, err := CallMCPToolWithUrlElicitationRetry(context.Background(), CallMCPToolParams{
		Server:   &ConnectedServer{Name: "elicit-server"},
		ToolName: "elicit",
	})
	if err == nil {
		t.Fatal("expected an error after the retry budget")
	}
	if calls != 4 {
		t.Errorf("call count = %d, want 4 (initial call + 3 retries)", calls)
	}
	if !strings.Contains(err.Error(), "max retries (3) exceeded") {
		t.Errorf("error = %q, want it to state the (3) retry budget", err.Error())
	}
	if !errors.Is(err, underlying) {
		t.Errorf("error = %q, want the elicitation error wrapped (%%w, not %%v)", err)
	}
}

// The caller's deadline has to reach the inner call.
func TestCallMCPToolWithUrlElicitationRetry_PassesContextThrough(t *testing.T) {
	calls := 0
	var seen []context.Context
	_ = mutateElicitationStub(t, &calls, func(ctx context.Context) { seen = append(seen, ctx) })

	_, err := CallMCPToolWithUrlElicitationRetry(context.Background(), CallMCPToolParams{
		Server:   &ConnectedServer{Name: "elicit-server"},
		ToolName: "elicit",
	})
	if err == nil {
		t.Fatal("expected an error")
	}
	if len(seen) != 4 {
		t.Fatalf("recorded contexts = %d, want 4", len(seen))
	}
	for i, ctx := range seen {
		if ctx == nil {
			t.Fatalf("attempt %d received a nil context", i)
		}
	}
}

// The retry log is the only record that retries happened, and its attempt
// numbering is 1-based.
func TestCallMCPToolWithUrlElicitationRetry_LogsEachAttempt(t *testing.T) {
	rec := &headerLogRecorder{}
	old := slog.Default()
	slog.SetDefault(slog.New(rec))
	t.Cleanup(func() { slog.SetDefault(old) })

	calls := 0
	_ = mutateElicitationStub(t, &calls, nil)
	if _, err := CallMCPToolWithUrlElicitationRetry(context.Background(), CallMCPToolParams{
		Server:   &ConnectedServer{Name: "elicit-server"},
		ToolName: "elicit",
	}); err == nil {
		t.Fatal("expected an error")
	}

	recs := rec.logged()
	if len(recs) != 3 {
		t.Fatalf("log records = %v, want one per retry (3)", recs)
	}
	wantAttempts := []string{"1", "2", "3"}
	for i, rec := range recs {
		if rec.msg != "mcp: URL elicitation required, retrying" {
			t.Errorf("record %d message = %q, want %q", i, rec.msg, "mcp: URL elicitation required, retrying")
		}
		if rec.attrs["attempt"] != wantAttempts[i] {
			t.Errorf("record %d attempt = %q, want %q", i, rec.attrs["attempt"], wantAttempts[i])
		}
		if rec.attrs["server"] != "elicit-server" || rec.attrs["tool"] != "elicit" {
			t.Errorf("record %d attrs = %v, want server=elicit-server tool=elicit", i, rec.attrs)
		}
	}
}

// The -32042 code lives on the inner JSON-RPC error: without the unwrap, every
// real elicitation failure is misclassified.
func TestIsURLElicitationError_UnwrapsToolCallError(t *testing.T) {
	wrapped := &McpToolCallError{
		ServerName: "srv",
		ToolName:   "t",
		Err:        &jsonrpc.Error{Code: mcp.CodeURLElicitationRequired, Message: "URL elicitation required"},
	}
	if !isURLElicitationError(wrapped) {
		t.Errorf("isURLElicitationError(%v) = false, want true", wrapped)
	}
	other := &McpToolCallError{
		ServerName: "srv",
		ToolName:   "t",
		Err:        &jsonrpc.Error{Code: jsonrpc.CodeInternalError, Message: "boom"},
	}
	if isURLElicitationError(other) {
		t.Errorf("isURLElicitationError(%v) = true, want false", other)
	}
}
