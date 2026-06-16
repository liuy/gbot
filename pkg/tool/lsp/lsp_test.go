package lsptool

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/lsp"
	"github.com/liuy/gbot/pkg/tool"
)

// ---------------------------------------------------------------------------
// Fake LSP server + test harness
// ---------------------------------------------------------------------------

type fakeHandler func(method string, params json.RawMessage) (any, bool)

// newFakeEnv builds a fake LSP server connected via net.Pipe.
// The handler factory is called AFTER the temp dir exists, so closures can
// reference dir when constructing URIs.
func newFakeEnv(t *testing.T, handlerFactory func(dir string) fakeHandler) (*lsp.Registry, string, func()) {
	t.Helper()

	dir := t.TempDir()

	clientConn, serverConn := net.Pipe()

	var handler fakeHandler
	if handlerFactory != nil {
		handler = handlerFactory(dir)
	}

	var wg sync.WaitGroup
	wg.Go(func() {
		serveFake(t, serverConn, handler)
	})

	c := lsp.NewTestClient("fakels", clientConn)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := c.Initialize(ctx, "file:///test"); err != nil {
		_ = clientConn.Close()
		_ = serverConn.Close()
		wg.Wait()
		t.Fatalf("Initialize: %v", err)
	}

	reg := lsp.NewRegistry("/test")
	reg.InjectClient("fakels", lsp.ServerSpec{
		Name:     "fakels",
		Language: "Fake",
		FileExts: []string{".go"},
		Command:  "fake-lsp",
	}, c)

	cleanup := func() {
		_ = clientConn.Close()
		_ = serverConn.Close()
		wg.Wait()
	}
	return reg, dir, cleanup
}

func serveFake(t *testing.T, conn net.Conn, handler fakeHandler) {
	t.Helper()
	r := bufio.NewReader(conn)
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		var contentLen int
		if _, err := fmt.Sscanf(line, "Content-Length: %d", &contentLen); err != nil {
			continue
		}
		_, _ = r.ReadString('\n')
		body := make([]byte, contentLen)
		if _, err := io.ReadFull(r, body); err != nil {
			return
		}

		var req struct {
			JSONRPC string          `json:"jsonrpc"`
			ID      int64           `json:"id"`
			Method  string          `json:"method"`
			Params  json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			continue
		}
		if req.JSONRPC == "" || req.ID == 0 {
			continue
		}

		var result any
		var handled bool
		switch req.Method {
		case "initialize":
			result = map[string]any{
				"capabilities": map[string]any{
					"referencesProvider":      true,
					"renameProvider":          map[string]bool{"prepareSupport": true},
					"hoverProvider":           true,
					"documentSymbolProvider":  true,
					"definitionProvider":      true,
					"implementationProvider":  true,
					"workspaceSymbolProvider": true,
					"codeActionProvider":      true,
				},
			}
			handled = true
		case "initialized":
			continue
		case "shutdown":
			result = nil
			handled = true
		case "exit":
			return
		}

		if !handled && handler != nil {
			result, handled = handler(req.Method, req.Params)
		}
		// When the test's handler does not answer documentSymbol, synthesize a
		// response by scanning the on-disk file for top-level `func NAME(`
		// declarations. This lets every integration test that passes File +
		// Symbol get automatic symbol→position resolution without each needing
		// its own documentSymbol handler.
		if !handled && req.Method == "textDocument/documentSymbol" {
			if syms, ok := defaultDocumentSymbols(req.Params); ok {
				result = syms
				handled = true
			}
		}
		if !handled {
			result = nil
		}

		resp := map[string]any{
			"jsonrpc": "2.0",
			"id":      req.ID,
			"result":  result,
		}
		respBody, _ := json.Marshal(resp)
		_, _ = fmt.Fprintf(conn, "Content-Length: %d\r\n\r\n", len(respBody))
		_, _ = conn.Write(respBody)
	}
}

func mustInput(t *testing.T, in Input) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// declPatterns maps a regex (anchored at line start, allowing leading
// whitespace) to the LSP SymbolKind assigned to matches. The first capture
// group is the declared name. This covers the Go top-level declaration forms
// used by the fake test files (func, var, type, const).
var declPatterns = []struct {
	re   *regexp.Regexp
	kind lsp.SymbolKind
}{
	{regexp.MustCompile(`^\s*func\s+(?:\([^)]*\)\s+)?([A-Za-z_][A-Za-z0-9_]*)\s*\(`), lsp.SymbolFunction},
	{regexp.MustCompile(`^\s*var\s+([A-Za-z_][A-Za-z0-9_]*)\b`), lsp.SymbolVariable},
	{regexp.MustCompile(`^\s*type\s+([A-Za-z_][A-Za-z0-9_]*)\b`), lsp.SymbolStruct},
	{regexp.MustCompile(`^\s*const\s+([A-Za-z_][A-Za-z0-9_]*)\b`), lsp.SymbolConstant},
}

// defaultDocumentSymbols reads the file referenced by a documentSymbol request
// and synthesizes a DocumentSymbol tree by scanning for common Go top-level
// declarations (func/var/type/const). Returns ok=false when the file cannot
// be read or the URI is malformed, so the caller falls back to a nil result.
func defaultDocumentSymbols(params json.RawMessage) ([]lsp.DocumentSymbol, bool) {
	var p struct {
		TextDocument struct {
			URI string `json:"uri"`
		} `json:"textDocument"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, false
	}
	path := lsp.URItoPath(p.TextDocument.URI)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	lines := strings.Split(string(data), "\n")
	var syms []lsp.DocumentSymbol
	for i, line := range lines {
		for _, pat := range declPatterns {
			m := pat.re.FindStringSubmatch(line)
			if m == nil {
				continue
			}
			name := m[1]
			col := max(strings.Index(line, name), 0)
			rng := lsp.Range{
				Start: lsp.Position{Line: i, Character: 0},
				End:   lsp.Position{Line: i, Character: len(line)},
			}
			sel := lsp.Range{
				Start: lsp.Position{Line: i, Character: col},
				End:   lsp.Position{Line: i, Character: col + len(name)},
			}
			syms = append(syms, lsp.DocumentSymbol{
				Name:           name,
				Kind:           pat.kind,
				Range:          rng,
				SelectionRange: sel,
			})
			break
		}
	}
	return syms, true
}

// ---------------------------------------------------------------------------
// Unit tests (no LSP server)
// ---------------------------------------------------------------------------

func TestNew(t *testing.T) {
	reg := lsp.NewRegistry(t.TempDir())
	reg.Scan([]lsp.ServerSpec{
		{Name: "gopls", Language: "Go", FileExts: []string{".go"}, Command: "gopls"},
	})
	tt := New(reg)
	if tt.Name() != "Lsp" {
		t.Errorf("Name() = %q, want %q", tt.Name(), "Lsp")
	}
	if !tt.IsReadOnly(mustInput(t, Input{Action: "definition"})) {
		t.Error("IsReadOnly(definition) = false, want true")
	}
	if tt.IsReadOnly(mustInput(t, Input{Action: "rename"})) {
		t.Error("IsReadOnly(rename) = true, want false")
	}
}

func TestExecute_UnknownAction(t *testing.T) {
	reg := lsp.NewRegistry(t.TempDir())
	_, err := New(reg).Call(context.Background(), mustInput(t, Input{Action: "foobar"}), basicCtx())
	if err == nil || !strings.Contains(err.Error(), "unknown LSP action") {
		t.Fatalf("expected unknown action error, got %v", err)
	}
}

func TestExecute_NoRegistry(t *testing.T) {
	_, err := New(nil).Call(context.Background(), mustInput(t, Input{Action: "status"}), basicCtx())
	if err == nil || !strings.Contains(err.Error(), "LSP not available") {
		t.Fatalf("expected LSP not available error, got %v", err)
	}
}

func TestExecute_NoSymbolForFileAction(t *testing.T) {
	reg := lsp.NewRegistry(t.TempDir())
	reg.Scan([]lsp.ServerSpec{{Name: "gopls", Language: "Go", FileExts: []string{".go"}, Command: "gopls"}})
	_, err := New(reg).Call(context.Background(), mustInput(t, Input{Action: "definition"}), basicCtx())
	if err == nil || !strings.Contains(err.Error(), "symbol parameter required") {
		t.Fatalf("expected symbol required error, got %v", err)
	}
}

func TestExecute_WorkspaceSymbol_NoQuery(t *testing.T) {
	reg := lsp.NewRegistry(t.TempDir())
	reg.Scan([]lsp.ServerSpec{{Name: "gopls", Language: "Go", FileExts: []string{".go"}, Command: "gopls"}})
	_, err := New(reg).Call(context.Background(), mustInput(t, Input{Action: "workspace_symbol"}), basicCtx())
	if err == nil || !strings.Contains(err.Error(), "query parameter required") {
		t.Fatalf("expected query required error, got %v", err)
	}
}

func TestExecute_Request_NoQuery(t *testing.T) {
	reg := lsp.NewRegistry(t.TempDir())
	reg.Scan([]lsp.ServerSpec{{Name: "gopls", Language: "Go", FileExts: []string{".go"}, Command: "gopls"}})
	_, err := New(reg).Call(context.Background(), mustInput(t, Input{Action: "request"}), basicCtx())
	if err == nil || !strings.Contains(err.Error(), "query parameter required") {
		t.Fatalf("expected query required error, got %v", err)
	}
}

func TestExecute_Rename_NoNewName(t *testing.T) {
	dir := t.TempDir()
	goFile := filepath.Join(dir, "main.go")
	if err := os.WriteFile(goFile, []byte("package main\nfunc foo() {}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	reg := lsp.NewRegistry(t.TempDir())
	reg.Scan([]lsp.ServerSpec{{Name: "gopls", Language: "Go", FileExts: []string{".go"}, Command: "gopls"}})
	_, err := New(reg).Call(context.Background(), mustInput(t, Input{
		Action: "rename", File: goFile, Symbol: "foo",
	}), basicCtxWithDir(t, dir))
	if err == nil || !strings.Contains(err.Error(), "new_name parameter required") {
		t.Fatalf("expected new_name required error, got %v", err)
	}
}

func TestExecute_Status_NoServers(t *testing.T) {
	reg := lsp.NewRegistry(t.TempDir())
	result, err := New(reg).Call(context.Background(), mustInput(t, Input{Action: "status"}), basicCtx())
	if err != nil {
		t.Fatal(err)
	}
	got := fmt.Sprintf("%v", result.Data)
	if !strings.Contains(got, "No language servers configured") {
		t.Errorf("status = %q, want 'No language servers configured'", got)
	}
}

func TestExecute_Status_WithServers(t *testing.T) {
	reg := lsp.NewRegistry(t.TempDir())
	reg.Scan([]lsp.ServerSpec{{Name: "gopls", Language: "Go", FileExts: []string{".go"}, Command: "gopls"}})
	result, err := New(reg).Call(context.Background(), mustInput(t, Input{Action: "status"}), basicCtx())
	if err != nil {
		t.Fatal(err)
	}
	got := fmt.Sprintf("%v", result.Data)
	if !strings.Contains(got, "gopls") || !strings.Contains(got, "not started") {
		t.Errorf("status = %q, want 'gopls — not started'", got)
	}
}

// ---------------------------------------------------------------------------
// Helper function tests
// ---------------------------------------------------------------------------

func TestResolvePath(t *testing.T) {
	if got := resolvePath("/abs/path", "/cwd"); got != "/abs/path" {
		t.Errorf("got %q", got)
	}
	if got := resolvePath("rel/path", "/cwd"); got != "/cwd/rel/path" {
		t.Errorf("got %q", got)
	}
}

func TestBoolPtrVal(t *testing.T) {
	trueVal := true
	falseVal := false
	if got := boolPtrVal(nil, true); got != true {
		t.Errorf("got %v", got)
	}
	if got := boolPtrVal(&trueVal, false); got != true {
		t.Errorf("got %v", got)
	}
	if got := boolPtrVal(&falseVal, true); got != false {
		t.Errorf("got %v", got)
	}
}

func TestFormatDocumentSymbol(t *testing.T) {
	sym := lsp.DocumentSymbol{
		Name:   "main",
		Kind:   lsp.SymbolFunction,
		Detail: "func main()",
		Children: []lsp.DocumentSymbol{
			{Name: "x", Kind: lsp.SymbolVariable},
		},
	}
	var b strings.Builder
	formatDocumentSymbol(&b, sym, 0)
	got := b.String()
	if !strings.Contains(got, "func") || !strings.Contains(got, "main") {
		t.Errorf("got %q", got)
	}
	if !strings.Contains(got, "var") || !strings.Contains(got, "x") {
		t.Errorf("missing child, got %q", got)
	}
}

func TestSymbolKindName(t *testing.T) {
	tests := []struct {
		kind lsp.SymbolKind
		want string
	}{
		{lsp.SymbolFile, "file"},
		{lsp.SymbolFunction, "func"},
		{lsp.SymbolClass, "class"},
		{lsp.SymbolInterface, "interface"},
		{lsp.SymbolStruct, "struct"},
		{lsp.SymbolMethod, "method"},
		{lsp.SymbolVariable, "var"},
		{lsp.SymbolKind(999), "symbol"},
	}
	for _, tc := range tests {
		if got := symbolKindName(tc.kind); got != tc.want {
			t.Errorf("symbolKindName(%d) = %q, want %q", tc.kind, got, tc.want)
		}
	}
}

func TestExtractHoverText(t *testing.T) {
	tests := []struct {
		input any
		want  string
	}{
		{input: "plain text", want: "plain text"},
		{input: map[string]any{"value": "markup"}, want: "markup"},
		{input: []any{"part1", "part2"}, want: "part1\n\npart2"},
		{input: 42, want: "42"},
	}
	for _, tc := range tests {
		if got := extractHoverText(tc.input); got != tc.want {
			t.Errorf("extractHoverText(%v) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestDecodeLocations(t *testing.T) {
	// null
	locs, err := decodeLocations(json.RawMessage("null"))
	if err != nil {
		t.Fatal(err)
	}
	if len(locs) != 0 {
		t.Errorf("got %d, want 0", len(locs))
	}
	// empty
	locs, err = decodeLocations(json.RawMessage(""))
	if err != nil {
		t.Fatal(err)
	}
	if len(locs) != 0 {
		t.Errorf("got %d", len(locs))
	}
	// single
	loc := lsp.Location{URI: "file:///main.go", Range: lsp.Range{Start: lsp.Position{Line: 10, Character: 5}, End: lsp.Position{Line: 10, Character: 20}}}
	raw, _ := json.Marshal(loc)
	locs, err = decodeLocations(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(locs) != 1 {
		t.Errorf("got %d", len(locs))
	}
	// array
	locsArr := []lsp.Location{loc, loc}
	raw, _ = json.Marshal(locsArr)
	locs, err = decodeLocations(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(locs) != 2 {
		t.Errorf("got %d", len(locs))
	}
	// invalid
	_, err = decodeLocations(json.RawMessage(`"garbage"`))
	if err == nil || !strings.Contains(err.Error(), "unexpected token") {
		t.Errorf("decodeLocations(garbage) err=%v, want 'unexpected token'", err)
	}
}

func TestDecodeLocations_LocationLink(t *testing.T) {
	raw := json.RawMessage(`[{"targetUri":"file:///link.go","targetSelectionRange":{"start":{"line":5,"character":2}}}]`)
	locs, err := decodeLocations(raw)
	if err != nil {
		t.Fatalf("decodeLocations: %v", err)
	}
	if len(locs) != 1 {
		t.Fatalf("expected 1 location, got %d", len(locs))
	}
	if locs[0].URI != "file:///link.go" {
		t.Errorf("URI = %q", locs[0].URI)
	}
	if locs[0].Range.Start.Line != 5 {
		t.Errorf("Line = %d", locs[0].Range.Start.Line)
	}
}

func TestDecodeLocations_BadArray(t *testing.T) {
	_, err := decodeLocations(json.RawMessage(`[{bad}]`))
	if err == nil {
		t.Fatal("should fail for bad array")
	}
	if !strings.Contains(err.Error(), "decodeLocations") {
		t.Errorf("wrong error: %v", err)
	}
}

func TestDecodeLocations_BadSingle(t *testing.T) {
	_, err := decodeLocations(json.RawMessage(`{bad}`))
	if err == nil {
		t.Fatal("should fail for bad object")
	}
	if !strings.Contains(err.Error(), "decodeLocations") {
		t.Errorf("wrong error: %v", err)
	}
}

func TestDecodeLocations_UnrecognizedObject(t *testing.T) {
	_, err := decodeLocations(json.RawMessage(`{"foo":"bar"}`))
	if err == nil {
		t.Fatal("should fail for unrecognized object")
	}
	if !strings.Contains(err.Error(), "unrecognized") {
		t.Errorf("wrong error: %v", err)
	}
}

func TestDecodeLocations_EmptyArray(t *testing.T) {
	locs, err := decodeLocations(json.RawMessage(`[]`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(locs) != 0 {
		t.Errorf("expected 0 locations, got %d", len(locs))
	}
}

func TestLocationFromMap_TargetRangeFallback(t *testing.T) {
	m := map[string]json.RawMessage{
		"targetUri":   json.RawMessage(`"file:///c.go"`),
		"targetRange": json.RawMessage(`{"start":{"line":3,"character":0}}`),
	}
	loc, ok := locationFromMap(m)
	if !ok {
		t.Fatal("expected ok")
	}
	if loc.URI != "file:///c.go" {
		t.Errorf("URI = %q", loc.URI)
	}
	if loc.Range.Start.Line != 3 {
		t.Errorf("Line = %d", loc.Range.Start.Line)
	}
}

func TestLocationFromMap_BadURI(t *testing.T) {
	m := map[string]json.RawMessage{
		"uri": json.RawMessage(`123`),
	}
	_, ok := locationFromMap(m)
	if ok {
		t.Fatal("expected !ok for non-string uri")
	}
}

func TestLocationFromMap_BadTargetURI(t *testing.T) {
	m := map[string]json.RawMessage{
		"targetUri": json.RawMessage(`123`),
	}
	_, ok := locationFromMap(m)
	if ok {
		t.Fatal("expected !ok for non-string targetUri")
	}
}

func TestExtractHoverText_EdgeCases(t *testing.T) {
	tests := []struct {
		name  string
		input any
		want  string
	}{
		{"map without value", map[string]any{"foo": "bar"}, "map[foo:bar]"},
		{"raw json string", json.RawMessage(`"hello"`), "hello"},
		{"raw json array", json.RawMessage(`["a","b"]`), "a\n\nb"},
		{"raw json bad", json.RawMessage(`not json`), "not json"},
		{"raw json empty", json.RawMessage(``), "[]"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := extractHoverText(tc.input); got != tc.want {
				t.Errorf("extractHoverText(%v) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestFormatJSON_Nested(t *testing.T) {
	got := formatJSON(json.RawMessage(`{"a":{"b":1}}`))
	if got == "" {
		t.Error("expected non-empty formatted JSON")
	}
}

func TestFormatJSON_MarshalError(t *testing.T) {
	got := formatJSON(json.RawMessage(`123`))
	if got != "123" {
		t.Errorf("got %q", got)
	}
}

func TestFormatJSON(t *testing.T) {
	tests := []struct {
		input json.RawMessage
		want  string
	}{
		{json.RawMessage(`"hello"`), "hello"},
		{json.RawMessage(`{"key":"val"}`), `"key"`},
		{json.RawMessage(`null`), "null"},
		{json.RawMessage(`[1,2]`), "["},
	}
	for _, tc := range tests {
		got := formatJSON(tc.input)
		if !strings.Contains(got, tc.want) {
			t.Errorf("formatJSON(%s) = %q, want contains %q", tc.input, got, tc.want)
		}
	}
}

func TestLSPPrompt(t *testing.T) {
	prompt := LSPPrompt()
	if !strings.Contains(prompt, "definition") || !strings.Contains(prompt, "NEVER") {
		t.Error("prompt missing key content")
	}
	if !strings.Contains(prompt, "request") {
		t.Error("prompt missing request")
	}
	if strings.Contains(prompt, "diagnostics") {
		t.Error("prompt mentions diagnostics")
	}
}

func TestReadonlyActions_Consistency(t *testing.T) {
	if readonlyActions["diagnostics"] {
		t.Error("should not include diagnostics")
	}
	if !readonlyActions["workspace_symbol"] {
		t.Error("should include workspace_symbol")
	}
	if readonlyActions["request"] {
		t.Error("request must NOT be readonly — it can invoke arbitrary mutating methods")
	}
}

// ---------------------------------------------------------------------------
// Integration tests (via fake LSP server over net.Pipe)
// ---------------------------------------------------------------------------

func TestIntegration_Definition(t *testing.T) {
	reg, dir, cleanup := newFakeEnv(t, func(d string) fakeHandler {
		return func(method string, _ json.RawMessage) (any, bool) {
			if method == "textDocument/definition" {
				return []lsp.Location{{
					URI:   "file://" + filepath.Join(d, "bar.go"),
					Range: lsp.Range{Start: lsp.Position{Line: 2, Character: 0}, End: lsp.Position{Line: 2, Character: 20}},
				}}, true
			}
			return nil, false
		}
	})
	defer cleanup()

	_ = os.WriteFile(filepath.Join(dir, "foo.go"), []byte("package main\nfunc foo() {}\n"), 0644)
	_ = os.WriteFile(filepath.Join(dir, "bar.go"), []byte("package main\nfunc bar() {}\n"), 0644)

	tt := New(reg)
	result, err := tt.Call(context.Background(), mustInput(t, Input{
		Action: "definition", File: filepath.Join(dir, "foo.go"), Symbol: "foo",
	}), &tool.ToolUseContext{WorkingDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	got := fmt.Sprintf("%v", result.Data)
	if !strings.Contains(got, "Found 1 definition") || !strings.Contains(got, "bar.go") {
		t.Errorf("got %q", got)
	}
}

func TestIntegration_TypeDefinition(t *testing.T) {
	reg, dir, cleanup := newFakeEnv(t, func(d string) fakeHandler {
		return func(method string, _ json.RawMessage) (any, bool) {
			if method == "textDocument/typeDefinition" {
				return []lsp.Location{{
					URI:   "file://" + filepath.Join(d, "types.go"),
					Range: lsp.Range{Start: lsp.Position{Line: 0, Character: 0}, End: lsp.Position{Line: 0, Character: 15}},
				}}, true
			}
			return nil, false
		}
	})
	defer cleanup()
	_ = os.WriteFile(filepath.Join(dir, "foo.go"), []byte("var x MyType\n"), 0644)

	tt := New(reg)
	result, err := tt.Call(context.Background(), mustInput(t, Input{
		Action: "type_definition", File: filepath.Join(dir, "foo.go"), Symbol: "x",
	}), &tool.ToolUseContext{WorkingDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprintf("%v", result.Data); !strings.Contains(got, "Found 1 type definition") {
		t.Errorf("got %q", got)
	}
}

func TestIntegration_Implementation(t *testing.T) {
	reg, dir, cleanup := newFakeEnv(t, func(d string) fakeHandler {
		return func(method string, _ json.RawMessage) (any, bool) {
			if method == "textDocument/implementation" {
				return []lsp.Location{{
					URI:   "file://" + filepath.Join(d, "impl.go"),
					Range: lsp.Range{Start: lsp.Position{Line: 0, Character: 0}, End: lsp.Position{Line: 0, Character: 10}},
				}}, true
			}
			return nil, false
		}
	})
	defer cleanup()
	_ = os.WriteFile(filepath.Join(dir, "foo.go"), []byte("type Foo interface{ Foo() }\n"), 0644)

	tt := New(reg)
	result, err := tt.Call(context.Background(), mustInput(t, Input{
		Action: "implementation", File: filepath.Join(dir, "foo.go"), Symbol: "Foo",
	}), &tool.ToolUseContext{WorkingDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprintf("%v", result.Data); !strings.Contains(got, "Found 1 implementation") {
		t.Errorf("got %q", got)
	}
}

// --- "no results" error paths for read-only navigation actions ---

func TestIntegration_Definition_NoResults(t *testing.T) {
	reg, dir, cleanup := newFakeEnv(t, func(d string) fakeHandler {
		return emptyHandler()
	})
	defer cleanup()

	_ = os.WriteFile(filepath.Join(dir, "test.go"), []byte("package main\nfunc foo() {}\n"), 0644)

	tt := New(reg)
	result, err := tt.Call(context.Background(), mustInput(t, Input{
		Action: "definition", Symbol: "foo", File: filepath.Join(dir, "test.go"),
	}), &tool.ToolUseContext{WorkingDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	got := fmt.Sprintf("%v", result.Data)
	if !strings.Contains(got, "No definition found") {
		t.Errorf("expected 'No definition found': %q", got)
	}
}

func TestIntegration_TypeDefinition_NoResults(t *testing.T) {
	reg, dir, cleanup := newFakeEnv(t, func(d string) fakeHandler {
		return emptyHandler()
	})
	defer cleanup()

	_ = os.WriteFile(filepath.Join(dir, "test.go"), []byte("package main\nfunc foo() {}\n"), 0644)

	tt := New(reg)
	result, err := tt.Call(context.Background(), mustInput(t, Input{
		Action: "type_definition", Symbol: "foo", File: filepath.Join(dir, "test.go"),
	}), &tool.ToolUseContext{WorkingDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	got := fmt.Sprintf("%v", result.Data)
	if !strings.Contains(got, "No type definition") {
		t.Errorf("expected 'No type definition': %q", got)
	}
}

func TestIntegration_Implementation_NoResults(t *testing.T) {
	reg, dir, cleanup := newFakeEnv(t, func(d string) fakeHandler {
		return emptyHandler()
	})
	defer cleanup()

	_ = os.WriteFile(filepath.Join(dir, "test.go"), []byte("package main\nfunc foo() {}\n"), 0644)

	tt := New(reg)
	result, err := tt.Call(context.Background(), mustInput(t, Input{
		Action: "implementation", Symbol: "foo", File: filepath.Join(dir, "test.go"),
	}), &tool.ToolUseContext{WorkingDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	got := fmt.Sprintf("%v", result.Data)
	if !strings.Contains(got, "No implementation") {
		t.Errorf("expected 'No implementation': %q", got)
	}
}

func TestIntegration_References_NoResults(t *testing.T) {
	reg, dir, cleanup := newFakeEnv(t, func(d string) fakeHandler {
		return emptyHandler()
	})
	defer cleanup()

	_ = os.WriteFile(filepath.Join(dir, "test.go"), []byte("package main\nfunc foo() {}\n"), 0644)

	tt := New(reg)
	result, err := tt.Call(context.Background(), mustInput(t, Input{
		Action: "references", Symbol: "foo", File: filepath.Join(dir, "test.go"),
	}), &tool.ToolUseContext{WorkingDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	got := fmt.Sprintf("%v", result.Data)
	if !strings.Contains(got, "No references found") {
		t.Errorf("expected 'No references found': %q", got)
	}
}

func TestIntegration_References(t *testing.T) {
	reg, dir, cleanup := newFakeEnv(t, func(d string) fakeHandler {
		return func(method string, _ json.RawMessage) (any, bool) {
			if method == "textDocument/references" {
				return []lsp.Location{
					{URI: "file://" + filepath.Join(d, "foo.go"), Range: lsp.Range{Start: lsp.Position{Line: 5, Character: 0}, End: lsp.Position{Line: 5, Character: 5}}},
					{URI: "file://" + filepath.Join(d, "bar.go"), Range: lsp.Range{Start: lsp.Position{Line: 3, Character: 2}, End: lsp.Position{Line: 3, Character: 7}}},
				}, true
			}
			return nil, false
		}
	})
	defer cleanup()
	_ = os.WriteFile(filepath.Join(dir, "foo.go"), []byte("package main\nfunc foo(){}\nfunc main(){\n\tfoo()\n}\n"), 0644)

	tt := New(reg)
	result, err := tt.Call(context.Background(), mustInput(t, Input{
		Action: "references", File: filepath.Join(dir, "foo.go"), Symbol: "foo",
	}), &tool.ToolUseContext{WorkingDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	got := fmt.Sprintf("%v", result.Data)
	if !strings.Contains(got, "Found 2 reference") || !strings.Contains(got, "bar.go") {
		t.Errorf("got %q", got)
	}
}

// Project-aware servers (gopls etc.) may return only the declaration on
// the first references call before indexing completes. omp retries up to
// REFERENCES_RETRY_COUNT times in that case. Verify the retry loop fires.
func TestIntegration_References_ProjectAwareRetry(t *testing.T) {
	callCount := 0
	var mu sync.Mutex
	uri := ""
	reg, dir, cleanup := newFakeEnv(t, func(d string) fakeHandler {
		uri = "file://" + filepath.Join(d, "foo.go")
		return func(method string, _ json.RawMessage) (any, bool) {
			if method == "textDocument/references" {
				mu.Lock()
				callCount++
				cc := callCount
				mu.Unlock()
				// First call returns only the declaration (matches queried pos).
				if cc == 1 {
					return []lsp.Location{{
						URI:   uri,
						Range: lsp.Range{Start: lsp.Position{Line: 1, Character: 5}, End: lsp.Position{Line: 1, Character: 8}},
					}}, true
				}
				// Second call returns real references.
				return []lsp.Location{
					{URI: uri, Range: lsp.Range{Start: lsp.Position{Line: 1, Character: 5}, End: lsp.Position{Line: 1, Character: 8}}},
					{URI: "file://" + filepath.Join(d, "bar.go"), Range: lsp.Range{Start: lsp.Position{Line: 3, Character: 2}, End: lsp.Position{Line: 3, Character: 7}}},
				}, true
			}
			return nil, false
		}
	})
	defer cleanup()
	_ = os.WriteFile(filepath.Join(dir, "foo.go"), []byte("package main\nfunc foo(){}\nfunc main(){\n\tfoo()\n}\n"), 0644)
	_ = os.WriteFile(filepath.Join(dir, "bar.go"), []byte("package main\nfunc bar(){\n\tfoo()\n}\n"), 0644)

	tt := New(reg)
	result, err := tt.Call(context.Background(), mustInput(t, Input{
		Action: "references", File: filepath.Join(dir, "foo.go"), Symbol: "foo",
	}), &tool.ToolUseContext{WorkingDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	cc := callCount
	mu.Unlock()
	if cc < 2 {
		t.Errorf("project-aware retry did not fire: callCount=%d, want ≥2", cc)
	}
	got := fmt.Sprintf("%v", result.Data)
	if !strings.Contains(got, "Found 2 reference") {
		t.Errorf("got %q", got)
	}
}

// When references exceed REFERENCE_CONTEXT_LIMIT, omp lists the first 50
// with context and the rest plain, with a separator line. Verify that.
func TestIntegration_References_TruncationReport(t *testing.T) {
	reg, dir, cleanup := newFakeEnv(t, func(d string) fakeHandler {
		return func(method string, _ json.RawMessage) (any, bool) {
			if method == "textDocument/references" {
				// Return 52 references — 2 over the 50-line context limit.
				locs := make([]lsp.Location, 52)
				for i := range locs {
					locs[i] = lsp.Location{
						URI:   "file://" + filepath.Join(d, "foo.go"),
						Range: lsp.Range{Start: lsp.Position{Line: i, Character: 0}, End: lsp.Position{Line: i, Character: 5}},
					}
				}
				return locs, true
			}
			return nil, false
		}
	})
	defer cleanup()
	_ = os.WriteFile(filepath.Join(dir, "foo.go"), []byte("package main\nfunc foo(){}\n"), 0644)

	tt := New(reg)
	result, err := tt.Call(context.Background(), mustInput(t, Input{
		Action: "references", File: filepath.Join(dir, "foo.go"), Symbol: "foo",
	}), &tool.ToolUseContext{WorkingDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	got := fmt.Sprintf("%v", result.Data)
	if !strings.Contains(got, "Found 52 reference") {
		t.Errorf("missing total count: %q", got)
	}
	if !strings.Contains(got, "2 additional reference(s) shown without context") {
		t.Errorf("missing separator notice: %q", got)
	}
}

func TestIntegration_Hover(t *testing.T) {
	reg, dir, cleanup := newFakeEnv(t, func(d string) fakeHandler {
		return func(method string, _ json.RawMessage) (any, bool) {
			if method == "textDocument/hover" {
				return map[string]any{
					"contents": map[string]any{
						"kind":  "markdown",
						"value": "```go\nfunc foo() string\n```",
					},
				}, true
			}
			return nil, false
		}
	})
	defer cleanup()
	_ = os.WriteFile(filepath.Join(dir, "foo.go"), []byte("package main\nfunc foo(){}\n"), 0644)

	tt := New(reg)
	result, err := tt.Call(context.Background(), mustInput(t, Input{
		Action: "hover", File: filepath.Join(dir, "foo.go"), Symbol: "foo",
	}), &tool.ToolUseContext{WorkingDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprintf("%v", result.Data); !strings.Contains(got, "func foo()") {
		t.Errorf("got %q", got)
	}
}

func TestIntegration_Hover_None(t *testing.T) {
	reg, dir, cleanup := newFakeEnv(t, nil)
	defer cleanup()
	_ = os.WriteFile(filepath.Join(dir, "foo.go"), []byte("package main\nfunc foo(){}\n"), 0644)

	tt := New(reg)
	result, err := tt.Call(context.Background(), mustInput(t, Input{
		Action: "hover", File: filepath.Join(dir, "foo.go"), Symbol: "foo",
	}), &tool.ToolUseContext{WorkingDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprintf("%v", result.Data); !strings.Contains(got, "No hover information") {
		t.Errorf("got %q", got)
	}
}

func TestIntegration_Symbols(t *testing.T) {
	reg, dir, cleanup := newFakeEnv(t, func(d string) fakeHandler {
		return func(method string, _ json.RawMessage) (any, bool) {
			if method == "textDocument/documentSymbol" {
				return []lsp.DocumentSymbol{{
					Name: "foo", Kind: lsp.SymbolFunction,
					Range:          lsp.Range{Start: lsp.Position{Line: 0, Character: 0}, End: lsp.Position{Line: 1, Character: 0}},
					SelectionRange: lsp.Range{Start: lsp.Position{Line: 0, Character: 0}, End: lsp.Position{Line: 1, Character: 0}},
				}}, true
			}
			return nil, false
		}
	})
	defer cleanup()
	_ = os.WriteFile(filepath.Join(dir, "foo.go"), []byte("package main\nfunc foo(){}\n"), 0644)

	tt := New(reg)
	result, err := tt.Call(context.Background(), mustInput(t, Input{
		Action: "symbols", File: filepath.Join(dir, "foo.go"),
	}), &tool.ToolUseContext{WorkingDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	got := fmt.Sprintf("%v", result.Data)
	if !strings.Contains(got, "foo") || !strings.Contains(got, "func") {
		t.Errorf("got %q", got)
	}
	if !strings.Contains(got, "@ line 1") {
		t.Errorf("missing line number: %q", got)
	}
}

// Some servers (e.g. clangd) return SymbolInformation[] instead of DocumentSymbol[].
// Verify the flat-format detection and rendering.
func TestIntegration_Symbols_SymbolInformation(t *testing.T) {
	reg, dir, cleanup := newFakeEnv(t, func(d string) fakeHandler {
		return func(method string, _ json.RawMessage) (any, bool) {
			if method == "textDocument/documentSymbol" {
				// No "selectionRange" key → detected as SymbolInformation path.
				return []lsp.SymbolInformation{{
					Name: "main", Kind: lsp.SymbolFunction,
					Location: lsp.Location{
						URI:   "file://" + filepath.Join(d, "foo.go"),
						Range: lsp.Range{Start: lsp.Position{Line: 4, Character: 0}, End: lsp.Position{Line: 4, Character: 10}},
					},
					ContainerName: "pkg",
				}}, true
			}
			return nil, false
		}
	})
	defer cleanup()
	_ = os.WriteFile(filepath.Join(dir, "foo.go"), []byte("package main\nfunc main(){}\n"), 0644)

	tt := New(reg)
	result, err := tt.Call(context.Background(), mustInput(t, Input{
		Action: "symbols", File: filepath.Join(dir, "foo.go"),
	}), &tool.ToolUseContext{WorkingDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	got := fmt.Sprintf("%v", result.Data)
	if !strings.Contains(got, "main") || !strings.Contains(got, "func") {
		t.Errorf("missing name/kind: %q", got)
	}
	if !strings.Contains(got, "@ line 5") {
		t.Errorf("missing/wrong line number: %q", got)
	}
	if !strings.Contains(got, "(pkg)") {
		t.Errorf("missing container name: %q", got)
	}
}

func TestIntegration_CodeActions(t *testing.T) {
	reg, dir, cleanup := newFakeEnv(t, func(d string) fakeHandler {
		return func(method string, _ json.RawMessage) (any, bool) {
			if method == "textDocument/codeAction" {
				return []map[string]any{
					{"title": "Organize imports", "kind": "source.organizeImports"},
				}, true
			}
			return nil, false
		}
	})
	defer cleanup()
	_ = os.WriteFile(filepath.Join(dir, "foo.go"), []byte("package main\nfunc foo(){}\n"), 0644)

	tt := New(reg)
	result, err := tt.Call(context.Background(), mustInput(t, Input{
		Action: "code_actions", File: filepath.Join(dir, "foo.go"), Symbol: "foo",
	}), &tool.ToolUseContext{WorkingDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprintf("%v", result.Data); !strings.Contains(got, "Organize imports") {
		t.Errorf("got %q", got)
	}
}

func TestIntegration_CodeActions_Empty(t *testing.T) {
	reg, dir, cleanup := newFakeEnv(t, func(d string) fakeHandler {
		return func(method string, _ json.RawMessage) (any, bool) {
			if method == "textDocument/codeAction" {
				return []map[string]any{}, true
			}
			return nil, false
		}
	})
	defer cleanup()
	_ = os.WriteFile(filepath.Join(dir, "foo.go"), []byte("package main\nfunc foo(){}\n"), 0644)

	tt := New(reg)
	result, err := tt.Call(context.Background(), mustInput(t, Input{
		Action: "code_actions", File: filepath.Join(dir, "foo.go"), Symbol: "foo",
	}), &tool.ToolUseContext{WorkingDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprintf("%v", result.Data); !strings.Contains(got, "No code actions") {
		t.Errorf("got %q", got)
	}
}

func TestIntegration_CodeActions_ApplyByIndex(t *testing.T) {
	reg, dir, cleanup := newFakeEnv(t, func(d string) fakeHandler {
		return func(method string, _ json.RawMessage) (any, bool) {
			switch method {
			case "textDocument/codeAction":
				return []map[string]any{
					{"title": "Add import", "kind": "quickfix"},
					{"title": "Organize imports", "kind": "source.organizeImports"},
				}, true
			case "codeAction/resolve":
				// Return the same action with an embedded edit.
				return map[string]any{
					"title": "Organize imports",
					"kind":  "source.organizeImports",
					"edit": map[string]any{
						"changes": map[string]any{
							"file://" + filepath.Join(d, "foo.go"): []map[string]any{
								{
									"range":   map[string]any{"start": map[string]any{"line": 0, "character": 0}, "end": map[string]any{"line": 0, "character": 12}},
									"newText": "import \"fmt\"\n",
								},
							},
						},
					},
				}, true
			}
			return nil, false
		}
	})
	defer cleanup()
	_ = os.WriteFile(filepath.Join(dir, "foo.go"), []byte("package main\nfunc foo(){}\n"), 0644)

	tt := New(reg)
	result, err := tt.Call(context.Background(), mustInput(t, Input{
		Action: "code_actions", File: filepath.Join(dir, "foo.go"), Symbol: "foo",
		Apply: new(true), Query: "1",
	}), &tool.ToolUseContext{WorkingDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	got := fmt.Sprintf("%v", result.Data)
	if !strings.Contains(got, "Applied") || !strings.Contains(got, "Organize imports") {
		t.Errorf("got %q", got)
	}
	if !strings.Contains(got, "Workspace edit:") {
		t.Errorf("missing workspace edit summary: %q", got)
	}
	// Verify file was actually modified.
	data, _ := os.ReadFile(filepath.Join(dir, "foo.go"))
	if !strings.Contains(string(data), "import") {
		t.Errorf("file not modified: %s", data)
	}
}

func TestIntegration_CodeActions_ApplyByTitle(t *testing.T) {
	reg, dir, cleanup := newFakeEnv(t, func(d string) fakeHandler {
		return func(method string, _ json.RawMessage) (any, bool) {
			switch method {
			case "textDocument/codeAction":
				return []map[string]any{
					{"title": "Add import", "kind": "quickfix"},
				}, true
			case "codeAction/resolve":
				return map[string]any{
					"title": "Add import",
					"kind":  "quickfix",
					"edit":  map[string]any{},
				}, true
			}
			return nil, false
		}
	})
	defer cleanup()
	_ = os.WriteFile(filepath.Join(dir, "foo.go"), []byte("package main\nfunc foo(){}\n"), 0644)

	tt := New(reg)
	result, err := tt.Call(context.Background(), mustInput(t, Input{
		Action: "code_actions", File: filepath.Join(dir, "foo.go"), Symbol: "foo",
		Apply: new(true), Query: "add",
	}), &tool.ToolUseContext{WorkingDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	got := fmt.Sprintf("%v", result.Data)
	if !strings.Contains(got, "Add import") {
		t.Errorf("got %q", got)
	}
}

func TestIntegration_CodeActions_ApplyNoQuery(t *testing.T) {
	reg, dir, cleanup := newFakeEnv(t, func(d string) fakeHandler {
		return func(method string, _ json.RawMessage) (any, bool) {
			if method == "textDocument/codeAction" {
				return []map[string]any{
					{"title": "X", "kind": "quickfix"},
				}, true
			}
			return nil, false
		}
	})
	defer cleanup()
	_ = os.WriteFile(filepath.Join(dir, "foo.go"), []byte("package main\nfunc foo(){}\n"), 0644)

	tt := New(reg)
	result, err := tt.Call(context.Background(), mustInput(t, Input{
		Action: "code_actions", File: filepath.Join(dir, "foo.go"), Symbol: "foo",
		Apply: new(true),
	}), &tool.ToolUseContext{WorkingDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprintf("%v", result.Data); !strings.Contains(got, "query parameter required") {
		t.Errorf("got %q", got)
	}
}

func TestIntegration_CodeActions_ListFormat(t *testing.T) {
	reg, dir, cleanup := newFakeEnv(t, func(d string) fakeHandler {
		return func(method string, _ json.RawMessage) (any, bool) {
			if method == "textDocument/codeAction" {
				return []map[string]any{
					{"title": "Add import", "kind": "quickfix", "isPreferred": true},
					{"title": "Format", "kind": "source.fixAll", "disabled": map[string]any{"reason": "not available"}},
				}, true
			}
			return nil, false
		}
	})
	defer cleanup()
	_ = os.WriteFile(filepath.Join(dir, "foo.go"), []byte("package main\nfunc foo(){}\n"), 0644)

	tt := New(reg)
	result, err := tt.Call(context.Background(), mustInput(t, Input{
		Action: "code_actions", File: filepath.Join(dir, "foo.go"), Symbol: "foo",
	}), &tool.ToolUseContext{WorkingDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	got := fmt.Sprintf("%v", result.Data)
	// Index, kind, preferred, disabled markers all present (mirrors omp formatCodeAction).
	if !strings.Contains(got, "0: [quickfix] Add import (preferred)") {
		t.Errorf("missing index/kind/preferred: %q", got)
	}
	if !strings.Contains(got, "1: [source.fixAll] Format (disabled: not available)") {
		t.Errorf("missing disabled marker: %q", got)
	}
}

func TestIntegration_Rename_NoEdits(t *testing.T) {
	reg, dir, cleanup := newFakeEnv(t, func(d string) fakeHandler {
		return func(method string, _ json.RawMessage) (any, bool) {
			if method == "textDocument/rename" {
				return nil, true
			}
			return nil, false
		}
	})
	defer cleanup()
	_ = os.WriteFile(filepath.Join(dir, "foo.go"), []byte("package main\nfunc foo(){}\n"), 0644)

	tt := New(reg)
	result, err := tt.Call(context.Background(), mustInput(t, Input{
		Action: "rename", File: filepath.Join(dir, "foo.go"), Symbol: "foo", NewName: "bar",
	}), &tool.ToolUseContext{WorkingDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprintf("%v", result.Data); !strings.Contains(got, "no edits") {
		t.Errorf("got %q", got)
	}
}

func TestIntegration_Rename_WithEdits(t *testing.T) {
	reg, dir, cleanup := newFakeEnv(t, func(d string) fakeHandler {
		return func(method string, _ json.RawMessage) (any, bool) {
			if method == "textDocument/rename" {
				uri := "file://" + filepath.Join(d, "foo.go")
				return lsp.WorkspaceEdit{
					Changes: map[string][]lsp.TextEdit{
						uri: {{Range: lsp.Range{Start: lsp.Position{Line: 1, Character: 5}, End: lsp.Position{Line: 1, Character: 8}}, NewText: "bar"}},
					},
				}, true
			}
			return nil, false
		}
	})
	defer cleanup()
	_ = os.WriteFile(filepath.Join(dir, "foo.go"), []byte("package main\nfunc foo(){}\n"), 0644)

	tt := New(reg)
	result, err := tt.Call(context.Background(), mustInput(t, Input{
		Action: "rename", File: filepath.Join(dir, "foo.go"), Symbol: "foo", NewName: "bar",
	}), &tool.ToolUseContext{WorkingDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	got := fmt.Sprintf("%v", result.Data)
	if !strings.Contains(got, "Applied rename") {
		t.Errorf("got %q", got)
	}
	// Verify file was actually written.
	data, err := os.ReadFile(filepath.Join(dir, "foo.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "func bar()") {
		t.Errorf("file not rewritten: %s", data)
	}
}

func TestIntegration_WorkspaceSymbol(t *testing.T) {
	reg, dir, cleanup := newFakeEnv(t, func(d string) fakeHandler {
		return func(method string, _ json.RawMessage) (any, bool) {
			if method == "workspace/symbol" {
				return []lsp.SymbolInformation{{
					Name: "MyFunc",
					Kind: lsp.SymbolFunction,
					Location: lsp.Location{
						URI:   "file://" + filepath.Join(d, "main.go"),
						Range: lsp.Range{Start: lsp.Position{Line: 5, Character: 0}, End: lsp.Position{Line: 5, Character: 20}},
					},
				}}, true
			}
			return nil, false
		}
	})
	defer cleanup()

	tt := New(reg)
	result, err := tt.Call(context.Background(), mustInput(t, Input{
		Action: "workspace_symbol", Query: "MyFunc",
	}), &tool.ToolUseContext{WorkingDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	got := fmt.Sprintf("%v", result.Data)
	if !strings.Contains(got, "Found 1 symbol") || !strings.Contains(got, "MyFunc") {
		t.Errorf("got %q", got)
	}
}

func TestIntegration_WorkspaceSymbol_Empty(t *testing.T) {
	reg, dir, cleanup := newFakeEnv(t, func(d string) fakeHandler {
		return func(method string, _ json.RawMessage) (any, bool) {
			if method == "workspace/symbol" {
				return []lsp.SymbolInformation{}, true
			}
			return nil, false
		}
	})
	defer cleanup()

	tt := New(reg)
	result, err := tt.Call(context.Background(), mustInput(t, Input{
		Action: "workspace_symbol", Query: "NONEXISTENT",
	}), &tool.ToolUseContext{WorkingDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprintf("%v", result.Data); !strings.Contains(got, "No symbols matching") {
		t.Errorf("got %q", got)
	}
}

func TestIntegration_Request_Custom(t *testing.T) {
	reg, dir, cleanup := newFakeEnv(t, func(d string) fakeHandler {
		return func(method string, _ json.RawMessage) (any, bool) {
			if method == "custom/test" {
				return map[string]any{"hello": "world"}, true
			}
			return nil, false
		}
	})
	defer cleanup()

	tt := New(reg)
	result, err := tt.Call(context.Background(), mustInput(t, Input{
		Action: "request", Query: "custom/test",
	}), &tool.ToolUseContext{WorkingDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprintf("%v", result.Data); !strings.Contains(got, "world") {
		t.Errorf("got %q", got)
	}
}

func TestIntegration_Request_WithPayload(t *testing.T) {
	reg, dir, cleanup := newFakeEnv(t, func(d string) fakeHandler {
		return func(method string, _ json.RawMessage) (any, bool) {
			if method == "test/payload" {
				return map[string]any{"status": "ok"}, true
			}
			return nil, false
		}
	})
	defer cleanup()
	_ = os.WriteFile(filepath.Join(dir, "foo.go"), []byte("package main\n"), 0644)

	tt := New(reg)
	result, err := tt.Call(context.Background(), mustInput(t, Input{
		Action: "request", Query: "test/payload", File: filepath.Join(dir, "foo.go"), Payload: `{"custom":true}`,
	}), &tool.ToolUseContext{WorkingDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprintf("%v", result.Data); !strings.Contains(got, "ok") {
		t.Errorf("got %q", got)
	}
}

// TestIntegration_Request_WithSymbol exercises the symbol-resolution branch of
// the request action: when Symbol is set the tool resolves it via
// documentSymbol and forwards Query to that position.
func TestIntegration_Request_WithSymbol(t *testing.T) {
	reg, dir, cleanup := newFakeEnv(t, func(d string) fakeHandler {
		return func(method string, _ json.RawMessage) (any, bool) {
			if method == "textDocument/hover" {
				return map[string]any{
					"contents": "func foo()",
				}, true
			}
			if method == "textDocument/documentSymbol" {
				return []map[string]any{{
					"name": "foo",
					"kind": 12,
					"range": map[string]any{
						"start": map[string]any{"line": 0, "character": 5},
						"end":   map[string]any{"line": 0, "character": 8},
					},
					"selectionRange": map[string]any{
						"start": map[string]any{"line": 0, "character": 5},
						"end":   map[string]any{"line": 0, "character": 8},
					},
				}}, true
			}
			return nil, false
		}
	})
	defer cleanup()

	_ = os.WriteFile(filepath.Join(dir, "foo.go"), []byte("package main\nfunc foo() {}\n"), 0644)

	tt := New(reg)
	result, err := tt.Call(context.Background(), mustInput(t, Input{
		Action: "request", Query: "textDocument/hover", File: filepath.Join(dir, "foo.go"), Symbol: "foo",
	}), &tool.ToolUseContext{WorkingDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	got := fmt.Sprintf("%v", result.Data)
	if !strings.Contains(got, "hover") || !strings.Contains(got, "foo") {
		t.Errorf("got %q", got)
	}
}

func TestIntegration_Capabilities(t *testing.T) {
	reg, dir, cleanup := newFakeEnv(t, nil)
	defer cleanup()

	tt := New(reg)
	result, err := tt.Call(context.Background(), mustInput(t, Input{
		Action: "capabilities",
	}), &tool.ToolUseContext{WorkingDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	got := fmt.Sprintf("%v", result.Data)
	if !strings.Contains(got, "referencesProvider") {
		t.Errorf("got %q", got)
	}
}

func TestIntegration_Reload(t *testing.T) {
	reg, dir, cleanup := newFakeEnv(t, nil)
	defer cleanup()
	_ = os.WriteFile(filepath.Join(dir, "foo.go"), []byte("package main\n"), 0644)

	tt := New(reg)
	_, err := tt.Call(context.Background(), mustInput(t, Input{
		Action: "reload", File: filepath.Join(dir, "foo.go"),
	}), &tool.ToolUseContext{WorkingDir: dir})
	if err != nil {
		t.Fatal(err)
	}
}

func TestIntegration_Status(t *testing.T) {
	reg, dir, cleanup := newFakeEnv(t, nil)
	defer cleanup()

	tt := New(reg)
	result, err := tt.Call(context.Background(), mustInput(t, Input{Action: "status"}), &tool.ToolUseContext{WorkingDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	got := fmt.Sprintf("%v", result.Data)
	// Injected fake client is live — should be reported as "ready", not "configured, not started".
	if !strings.Contains(got, "fakels") {
		t.Errorf("missing server name: %q", got)
	}
	if !strings.Contains(got, "fakels — ready") {
		t.Errorf("expected 'fakels — ready' since client is live: %q", got)
	}
}

// basicCtx returns a minimal ToolUseContext.
func basicCtx() *tool.ToolUseContext {
	return &tool.ToolUseContext{WorkingDir: "/tmp"}
}

func basicCtxWithDir(t *testing.T, dir string) *tool.ToolUseContext {
	t.Helper()
	return &tool.ToolUseContext{WorkingDir: dir}
}

// --- Description / IsReadOnly / RenderResult ---

func TestNew_Description(t *testing.T) {
	reg := lsp.NewRegistry(t.TempDir())
	tt := New(reg)
	desc, err := tt.Description(mustInput(t, Input{Action: "definition"}))
	if err != nil {
		t.Fatalf("Description: %v", err)
	}
	if desc != "Lsp definition" {
		t.Errorf("Description = %q", desc)
	}
}

func TestNew_Description_InvalidJSON(t *testing.T) {
	reg := lsp.NewRegistry(t.TempDir())
	tt := New(reg)
	desc, err := tt.Description(json.RawMessage(`{bad json`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if desc != "Query language server for code intelligence" {
		t.Errorf("Description fallback = %q", desc)
	}
}

func TestNew_IsReadOnly_AllActions(t *testing.T) {
	reg := lsp.NewRegistry(t.TempDir())
	tt := New(reg)
	readonly := map[string]bool{
		"definition": true, "type_definition": true, "implementation": true,
		"references": true, "hover": true, "symbols": true,
		"workspace_symbol": true, "capabilities": true,
		"callers": true, "callees": true, "source": true,
		"inspect": true, "impact": true,
		"status":       true,
		"code_actions": false, "reload": false,
		"rename": false, "rename_file": false, "request": false,
	}
	for action, wantRO := range readonly {
		got := tt.IsReadOnly(mustInput(t, Input{Action: action}))
		if got != wantRO {
			t.Errorf("IsReadOnly(%q) = %v, want %v", action, got, wantRO)
		}
	}
}

func TestNew_IsReadOnly_InvalidJSON(t *testing.T) {
	reg := lsp.NewRegistry(t.TempDir())
	tt := New(reg)
	if tt.IsReadOnly(json.RawMessage(`{bad json`)) {
		t.Error("expected IsReadOnly=false for invalid JSON")
	}
}

func TestNew_RenderResult(t *testing.T) {
	reg := lsp.NewRegistry(t.TempDir())
	tt := New(reg)
	got := tt.RenderResult("hello")
	if got != "hello" {
		t.Errorf("RenderResult(string) = %q", got)
	}
	got = tt.RenderResult(map[string]any{"a": 1})
	if got == "" {
		t.Error("expected non-empty JSON render")
	}
}

func TestNew_Constructor(t *testing.T) {
	reg := lsp.NewRegistry("/test")
	tt := New(reg)
	if tt == nil {
		t.Fatal("New returned nil")
	}
}

func TestComparePosition(t *testing.T) {
	a := lsp.Position{Line: 5, Character: 3}
	b := lsp.Position{Line: 5, Character: 3}
	if comparePosition(a, b) != 0 {
		t.Errorf("equal positions should return 0")
	}

	if comparePosition(lsp.Position{Line: 3}, lsp.Position{Line: 5}) >= 0 {
		t.Errorf("smaller line should be negative")
	}

	if comparePosition(lsp.Position{Line: 5, Character: 10}, lsp.Position{Line: 5, Character: 3}) <= 0 {
		t.Errorf("larger character should be positive")
	}
}
