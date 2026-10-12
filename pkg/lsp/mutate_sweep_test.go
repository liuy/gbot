package lsp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---- slog capture -------------------------------------------------------

type logRec struct {
	level slog.Level
	msg   string
	attrs map[string]any
}

type captureHandler struct {
	mu   sync.Mutex
	recs []logRec
}

func (h *captureHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *captureHandler) Handle(_ context.Context, r slog.Record) error {
	attrs := make(map[string]any, r.NumAttrs())
	r.Attrs(func(a slog.Attr) bool {
		attrs[a.Key] = a.Value.Any()
		return true
	})
	h.mu.Lock()
	h.recs = append(h.recs, logRec{level: r.Level, msg: r.Message, attrs: attrs})
	h.mu.Unlock()
	return nil
}

func (h *captureHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *captureHandler) WithGroup(string) slog.Handler      { return h }

func (h *captureHandler) find(msg string) []logRec {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []logRec
	for _, r := range h.recs {
		if r.msg == msg {
			out = append(out, r)
		}
	}
	return out
}

func captureLogs(t *testing.T) *captureHandler {
	t.Helper()
	h := &captureHandler{}
	prev := slog.Default()
	slog.SetDefault(slog.New(h))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return h
}

// ---- discover.go: the static probe table -------------------------------

// DefaultServers is what gbot probes on PATH at startup: a dropped field turns
// into a silent "server not found" or a wrong handshake flag.
func TestDefaultServers_Table(t *testing.T) {
	want := []ServerSpec{
		{Name: "gopls", Command: "gopls", Args: []string{"-rpc.trace"}, FileExts: []string{".go"}, Language: "Go"},
		{Name: "typescript-language-server", Command: "typescript-language-server", Args: []string{"--stdio"}, FileExts: []string{".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs"}, Language: "TypeScript, JavaScript"},
		{Name: "rust-analyzer", Command: "rust-analyzer", FileExts: []string{".rs"}, Language: "Rust"},
		{Name: "pyright-langserver", Command: "pyright-langserver", Args: []string{"--stdio"}, FileExts: []string{".py"}, Language: "Python"},
		{Name: "clangd", Command: "clangd", FileExts: []string{".c", ".h", ".cc", ".cpp", ".hpp", ".cxx", ".hxx"}, Language: "C, C++"},
	}
	if !reflect.DeepEqual(DefaultServers, want) {
		t.Errorf("DefaultServers =\n%+v\nwant\n%+v", DefaultServers, want)
	}
}

// ---- edits.go: uriToPath -----------------------------------------------

func TestUriToPath_DriveLetterAndAbs(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	tests := []struct {
		in   string
		want string
	}{
		{"file:///tmp/x/file.go", "/tmp/x/file.go"},
		{"file:///tmp/test%20space/file.go", "/tmp/test space/file.go"},
		{"file:///a/b.go", "/a/b.go"},
		// Windows file:///C:/path keeps its drive letter once the leading
		// slash is stripped; on Linux the stripped form is relative, so Abs
		// resolves it under the (pinned) working directory.
		{"file:///C:/path/x.go", filepath.Join(dir, "C:/path/x.go")},
		{"file://C:/path/x.go", filepath.Join(dir, "C:/path/x.go")},
		{"file:///C:", filepath.Join(dir, "C:")},
		{"file:///C", "/C"},
		{"relative/path.go", filepath.Join(dir, "relative/path.go")},
	}
	for _, tc := range tests {
		if got := uriToPath(tc.in); got != tc.want {
			t.Errorf("uriToPath(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// ---- edits.go: decodeLocations -----------------------------------------

func TestDecodeLocations_LeadingWhitespace(t *testing.T) {
	body := `[{"uri":"file:///repo/a.go","range":{"start":{"line":3,"character":4},"end":{"line":3,"character":9}}}]`
	for _, ws := range []string{"", " ", "\t", "\n", "\r", " \t\n\r", "\n\t  "} {
		locs, err := decodeLocations(json.RawMessage(ws + body))
		if err != nil {
			t.Errorf("decodeLocations(%q…): %v", ws, err)
			continue
		}
		if len(locs) != 1 {
			t.Errorf("decodeLocations(%q…): got %d locations, want 1", ws, len(locs))
			continue
		}
		want := Location{
			URI:   "file:///repo/a.go",
			Range: Range{Start: Position{Line: 3, Character: 4}, End: Position{Line: 3, Character: 9}},
		}
		if locs[0] != want {
			t.Errorf("decodeLocations(%q…) = %+v, want %+v", ws, locs[0], want)
		}
	}
}

func TestDecodeLocations_InternalWhitespaceAndMalformedSkip(t *testing.T) {
	raw := json.RawMessage(`[ {"bogus":true} , {"uri":"file:///repo/good.go","range":{"start":{"line":0,"character":0},"end":{"line":0,"character":1}}} ]`)
	locs, err := decodeLocations(raw)
	if err != nil {
		t.Fatalf("decodeLocations: %v", err)
	}
	if len(locs) != 1 {
		t.Fatalf("got %d locations, want 1 (malformed element must be skipped, not abort the array)", len(locs))
	}
	if locs[0].URI != "file:///repo/good.go" {
		t.Errorf("location = %+v, want file:///repo/good.go", locs[0])
	}
}

func TestDecodeLocations_EmptyAndNull(t *testing.T) {
	for _, raw := range []json.RawMessage{nil, json.RawMessage(""), json.RawMessage("null")} {
		locs, err := decodeLocations(raw)
		if err != nil {
			t.Errorf("decodeLocations(%q): err = %v, want nil", raw, err)
		}
		if locs != nil {
			t.Errorf("decodeLocations(%q) = %+v, want nil", raw, locs)
		}
	}

	// The null check is exact: a padded null is not a JSON-RPC null result,
	// it is an unexpected token.
	if _, err := decodeLocations(json.RawMessage("  null")); err == nil {
		t.Fatal("padded null: expected error")
	} else if want := `decodeLocations: unexpected token 'n' in   null`; err.Error() != want {
		t.Errorf("padded null error = %q, want %q", err.Error(), want)
	}
}

func TestDecodeLocations_Errors(t *testing.T) {
	if _, err := decodeLocations(json.RawMessage(" \t\n\r")); err == nil {
		t.Fatal("all-whitespace input: expected error")
	} else if want := fmt.Sprintf(`decodeLocations: unexpected token '\x00' in %s`, " \t\n\r"); err.Error() != want {
		t.Errorf("all-whitespace error = %q, want %q", err.Error(), want)
	}

	if _, err := decodeLocations(json.RawMessage(`{"uri":`)); err == nil {
		t.Fatal("truncated object: expected error")
	} else {
		if _, ok := errors.AsType[*json.SyntaxError](err); !ok {
			t.Errorf("single-object error does not wrap *json.SyntaxError: %v", err)
		}
		if !strings.HasPrefix(err.Error(), "decodeLocations single: ") {
			t.Errorf("single-object error = %q, want prefix %q", err.Error(), "decodeLocations single: ")
		}
	}

	if _, err := decodeLocations(json.RawMessage(`[{"uri":`)); err == nil {
		t.Fatal("truncated array: expected error")
	} else {
		if _, ok := errors.AsType[*json.SyntaxError](err); !ok {
			t.Errorf("array error does not wrap *json.SyntaxError: %v", err)
		}
		if !strings.HasPrefix(err.Error(), "decodeLocations array: ") {
			t.Errorf("array error = %q, want prefix %q", err.Error(), "decodeLocations array: ")
		}
	}

	if _, err := decodeLocations(json.RawMessage(`{"bogus":1}`)); err == nil {
		t.Fatal("unrecognized object: expected error")
	} else if want := `decodeLocations: unrecognized object: {"bogus":1}`; err.Error() != want {
		t.Errorf("unrecognized object error = %q, want %q", err.Error(), want)
	}
}

// ---- edits.go: RangesOverlap -------------------------------------------

func TestRangesOverlap_TruthTable(t *testing.T) {
	r := func(sl, sc, el, ec int) Range {
		return Range{Start: Position{Line: sl, Character: sc}, End: Position{Line: el, Character: ec}}
	}
	tests := []struct {
		name string
		a, b Range
		want bool
	}{
		{"touching a before b", r(0, 0, 0, 5), r(0, 5, 0, 9), false},
		{"touching b before a", r(0, 5, 0, 9), r(0, 0, 0, 5), false},
		{"disjoint a before b", r(0, 0, 0, 5), r(0, 9, 0, 12), false},
		{"disjoint b before a", r(0, 9, 0, 12), r(0, 0, 0, 5), false},
		{"overlap by one char", r(0, 0, 0, 5), r(0, 4, 0, 9), true},
		{"overlap by one char reversed", r(0, 4, 0, 9), r(0, 0, 0, 5), true},
		{"identical", r(1, 2, 3, 4), r(1, 2, 3, 4), true},
		{"b inside a", r(0, 0, 9, 0), r(4, 0, 5, 0), true},
		{"different lines", r(0, 0, 0, 9), r(1, 0, 1, 9), false},
	}
	for _, tc := range tests {
		if got := RangesOverlap(tc.a, tc.b); got != tc.want {
			t.Errorf("RangesOverlap(%s): got %v, want %v", tc.name, got, tc.want)
		}
	}
}

// ---- helpers.go: FormatWorkspaceEdit -----------------------------------

func TestFormatWorkspaceEdit_CountsAndResourceOps(t *testing.T) {
	dir := t.TempDir()
	uri := func(name string) string { return "file://" + filepath.Join(dir, name) }
	edit := func(line int, text string) map[string]any {
		return map[string]any{
			"range": map[string]any{
				"start": map[string]any{"line": line, "character": 0},
				"end":   map[string]any{"line": line, "character": 1},
			},
			"newText": text,
		}
	}

	we := &WorkspaceEdit{
		Changes: map[string][]TextEdit{
			uri("single.go"): {{NewText: "a"}},
			uri("double.go"): {{NewText: "a"}, {NewText: "b"}},
		},
		DocumentChanges: []map[string]any{
			{"textDocument": map[string]any{"uri": uri("dc1.go")}, "edits": []any{edit(0, "x")}},
			{"textDocument": map[string]any{"uri": uri("dc2.go")}, "edits": []any{edit(0, "x"), edit(1, "y")}},
			{"kind": "create", "uri": uri("fresh.go")},
			{"kind": "rename", "oldUri": uri("old.go"), "newUri": uri("new.go")},
			{"kind": "delete", "uri": uri("gone.go")},
		},
	}

	got := FormatWorkspaceEdit(we, dir)
	want := []string{
		"dc1.go: 1 edit",
		"dc2.go: 2 edits",
		"CREATE: fresh.go",
		"RENAME: old.go → new.go",
		"DELETE: gone.go",
		"double.go: 2 edits",
		"single.go: 1 edit",
	}
	sort.Strings(got)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("FormatWorkspaceEdit =\n%q\nwant\n%q", got, want)
	}

	if out := FormatWorkspaceEdit(nil, dir); out != nil {
		t.Errorf("FormatWorkspaceEdit(nil) = %q, want nil", out)
	}
}

// ---- helpers.go: ReadLocationContext -----------------------------------

func TestReadLocationContext_Window(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")
	var b strings.Builder
	for i := 1; i <= 10; i++ {
		fmt.Fprintf(&b, "line%d\n", i)
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}

	got := ReadLocationContext(path, 5, 2)
	want := []string{"     3: line3", "     4: line4", "     5: line5", "     6: line6", "     7: line7"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ReadLocationContext(_,5,2) = %q, want %q", got, want)
	}

	got = ReadLocationContext(path, 1, 2)
	want = []string{"     1: line1", "     2: line2", "     3: line3"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ReadLocationContext(_,1,2) = %q, want %q (window clamped at file start)", got, want)
	}

	if out := ReadLocationContext(filepath.Join(dir, "missing.txt"), 1, 1); out != nil {
		t.Errorf("ReadLocationContext(missing) = %q, want nil", out)
	}
}

// ---- helpers.go: FormatLocationsWithContext ----------------------------

func TestFormatLocationsWithContext_ContextWindow(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "multi.txt")
	var b strings.Builder
	for i := 1; i <= 10; i++ {
		fmt.Fprintf(&b, "line%d\n", i)
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	uri := "file://" + path

	locs := []Location{
		{URI: uri, Range: Range{Start: Position{Line: 4, Character: 0}}},
		{URI: uri, Range: Range{Start: Position{Line: 7, Character: 3}}},
		{URI: uri, Range: Range{Start: Position{Line: 0, Character: 0}}},
	}
	got := FormatLocationsWithContext(locs, dir, 1)
	want := []string{
		"multi.txt:5:1\n       4: line4\n       5: line5\n       6: line6",
		"multi.txt:8:4\n       7: line7\n       8: line8\n       9: line9",
		"multi.txt:1:1\n       1: line1\n       2: line2",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("FormatLocationsWithContext =\n%q\nwant\n%q", got, want)
	}

	// contextLines <= 0 must fall back to bare headers.
	got = FormatLocationsWithContext(locs, dir, 0)
	want = []string{"multi.txt:5:1", "multi.txt:8:4", "multi.txt:1:1"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("FormatLocationsWithContext(contextLines=0) =\n%q\nwant\n%q", got, want)
	}

	if out := FormatLocationsWithContext(nil, dir, 2); len(out) != 0 {
		t.Errorf("FormatLocationsWithContext(nil) = %q, want empty", out)
	}
}

func TestFormatLocationsWithContext_UnreadableAndSingleLine(t *testing.T) {
	dir := t.TempDir()
	oneLine := filepath.Join(dir, "one.txt")
	if err := os.WriteFile(oneLine, []byte("only line"), 0o644); err != nil {
		t.Fatal(err)
	}

	locs := []Location{
		{URI: "file://" + filepath.Join(dir, "missing.txt"), Range: Range{Start: Position{Line: 0, Character: 0}}},
		{URI: "file://" + oneLine, Range: Range{Start: Position{Line: 0, Character: 2}}},
	}
	got := FormatLocationsWithContext(locs, dir, 2)
	want := []string{
		"missing.txt:1:1",
		"one.txt:1:3\n       1: only line",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("FormatLocationsWithContext =\n%q\nwant\n%q", got, want)
	}
}

// ---- registry.go: Scan -------------------------------------------------

func TestScan_ExtMapAndInitLog(t *testing.T) {
	logs := captureLogs(t)
	prev := execLookPath
	t.Cleanup(func() { execLookPath = prev })
	execLookPath = func(name string) (string, error) {
		if name == "ghost" {
			return "", errors.New("not found")
		}
		return "/usr/bin/" + name, nil
	}

	dir := t.TempDir()

	// Nothing on PATH: no init log, no specs.
	empty := NewRegistry(dir)
	empty.Scan([]ServerSpec{{Name: "ghost", Command: "ghost", FileExts: []string{".g"}}})
	if n := empty.NumServers(); n != 0 {
		t.Errorf("NumServers after all-absent Scan = %d, want 0", n)
	}
	if recs := logs.find("lsp:registry_init"); len(recs) != 0 {
		t.Errorf("registry_init logged %d times for an empty scan, want 0", len(recs))
	}

	// Exactly one server: logged once, with its single name.
	single := NewRegistry(dir)
	single.Scan([]ServerSpec{
		{Name: "solo", Command: "solo", FileExts: []string{".s"}},
		{Name: "ghost", Command: "ghost", FileExts: []string{".g"}},
	})
	recs := logs.find("lsp:registry_init")
	if len(recs) != 1 {
		t.Fatalf("registry_init logged %d times after a one-server scan, want 1", len(recs))
	}
	if names, ok := recs[0].attrs["servers"].([]string); !ok || !reflect.DeepEqual(names, []string{"solo"}) {
		t.Errorf("registry_init servers = %#v, want [solo]", recs[0].attrs["servers"])
	}

	// Two servers: names are logged in sorted order, all of them.
	two := NewRegistry(dir)
	two.Scan([]ServerSpec{
		{Name: "zeta", Command: "zeta", FileExts: []string{".z"}, Language: "Z"},
		{Name: "alpha", Command: "alpha", FileExts: []string{".a", ".b"}, Language: "A"},
		{Name: "ghost", Command: "ghost", FileExts: []string{".g"}, Language: "G"},
	})
	if n := two.NumServers(); n != 2 {
		t.Fatalf("NumServers after two-server scan = %d, want 2", n)
	}
	recs = logs.find("lsp:registry_init")
	if len(recs) != 2 {
		t.Fatalf("registry_init logged %d times, want 2", len(recs))
	}
	if names, ok := recs[1].attrs["servers"].([]string); !ok || !reflect.DeepEqual(names, []string{"alpha", "zeta"}) {
		t.Errorf("registry_init servers = %#v, want [alpha zeta]", recs[1].attrs["servers"])
	}

	two.mu.RLock()
	a, b, ghost := two.extToSpec[".a"], two.extToSpec[".b"], two.extToSpec[".g"]
	two.mu.RUnlock()
	if a.Name != "alpha" || b.Name != "alpha" {
		t.Errorf("extToSpec[.a/.b] = %q/%q, want alpha/alpha", a.Name, b.Name)
	}
	if ghost.Name != "" {
		t.Errorf("extToSpec[.g] = %q, want unset for an absent binary", ghost.Name)
	}
}

// ---- registry.go: InjectClientInRoot -----------------------------------

func TestInjectClientInRoot_SpecsAndRootsAreIdempotent(t *testing.T) {
	dir := t.TempDir()
	r := NewRegistry(dir)
	c, _, cleanup := newInProcessServer(t)
	defer cleanup()

	first := ServerSpec{Name: "fake", Command: "fake", FileExts: []string{".go"}, Language: "Go"}
	second := ServerSpec{Name: "fake", Command: "other", FileExts: []string{".ts"}, Language: "Other"}
	root := filepath.Join(dir, "sub")

	r.InjectClientInRoot("fake", root, first, c)
	r.InjectClientInRoot("fake", root, second, c)

	if n := r.NumServers(); n != 1 {
		t.Errorf("NumServers after two injections of the same name = %d, want 1", n)
	}
	r.mu.RLock()
	specs := append([]ServerSpec(nil), r.specs...)
	ext := r.extToSpec[".go"]
	roots := append([]string(nil), r.extraRoots...)
	r.mu.RUnlock()

	if len(specs) != 1 || !reflect.DeepEqual(specs[0], first) {
		t.Errorf("specs = %+v, want exactly the first injected spec", specs)
	}
	if ext.Name != "fake" || ext.Language != "Go" {
		t.Errorf("extToSpec[.go] = %+v, want the first injected spec", ext)
	}
	if !reflect.DeepEqual(roots, []string{root}) {
		t.Errorf("extraRoots = %q, want [%s]", roots, root)
	}

	live := r.LiveServers()
	if len(live) != 1 || live[0].Root != root || live[0].Spec.Name != "fake" {
		t.Errorf("LiveServers = %+v, want exactly one fake@%s", live, root)
	}
}

// ---- client.go: server→client request replies --------------------------

func readReply(t *testing.T, conn net.Conn, rd *framingReader) map[string]json.RawMessage {
	t.Helper()
	if err := conn.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil { // REAL-TIME: net.Pipe read deadline, guards against a hung server
		t.Fatalf("SetReadDeadline: %v", err)
	}
	body, err := rd.read()
	if err != nil {
		t.Fatalf("no reply from client: %v", err)
	}
	var env map[string]json.RawMessage
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("reply is not a JSON object: %v (%s)", err, body)
	}
	return env
}

func TestHandleServerRequest_KnownMethods(t *testing.T) {
	_, conn, cleanup := rawPipeTest(t)
	defer cleanup()
	rd := newFramedReader(conn)

	tests := []struct {
		method     string
		wantResult string
	}{
		{"workspace/configuration", "[]"},
		{"workspace/workspaceFolders", "[]"},
		{"window/workDoneProgress/create", "null"},
		{"workspace/applyEdit", `{"applied":false}`},
	}
	for i, tc := range tests {
		id := int64(i + 1)
		rawWrite(t, conn, toFramed(fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":%q}`, id, tc.method)))
		env := readReply(t, conn, rd)

		if got := string(env["jsonrpc"]); got != `"2.0"` {
			t.Errorf("%s reply jsonrpc = %s, want \"2.0\"", tc.method, got)
		}
		if got := string(env["id"]); got != fmt.Sprint(id) {
			t.Errorf("%s reply id = %s, want %d", tc.method, got, id)
		}
		result, ok := env["result"]
		if !ok {
			t.Errorf("%s reply has no result field: %s", tc.method, env)
			continue
		}
		if string(result) != tc.wantResult {
			t.Errorf("%s reply result = %s, want %s", tc.method, result, tc.wantResult)
		}
	}
}

func TestHandleServerRequest_UnknownMethodError(t *testing.T) {
	_, conn, cleanup := rawPipeTest(t)
	defer cleanup()
	rd := newFramedReader(conn)

	rawWrite(t, conn, toFramed(`{"jsonrpc":"2.0","id":7,"method":"some/unknown/request"}`))
	env := readReply(t, conn, rd)

	if got := string(env["id"]); got != "7" {
		t.Errorf("error reply id = %s, want 7", got)
	}
	if _, ok := env["result"]; ok {
		t.Errorf("error reply must not carry a result field: %v", env)
	}
	errRaw, ok := env["error"]
	if !ok {
		t.Fatalf("error reply missing error field: %v", env)
	}
	var e map[string]json.RawMessage
	if err := json.Unmarshal(errRaw, &e); err != nil {
		t.Fatalf("error field is not an object: %v (%s)", err, errRaw)
	}
	if got := string(e["code"]); got != "-32601" {
		t.Errorf("error.code = %s, want -32601", got)
	}
	if got := string(e["message"]); got != `"method not found: some/unknown/request"` {
		t.Errorf("error.message = %s, want \"method not found: some/unknown/request\"", got)
	}
}

// ---- client.go: readLoop notification routing --------------------------

func TestReadLoop_StoresPublishDiagnostics(t *testing.T) {
	c, conn, cleanup := rawPipeTest(t)
	defer cleanup()
	rd := newFramedReader(conn)

	rawWrite(t, conn, toFramed(`{"jsonrpc":"2.0","method":"textDocument/publishDiagnostics","params":{"uri":"file:///x.go","diagnostics":[{"range":{"start":{"line":1,"character":2},"end":{"line":1,"character":3}},"severity":1,"message":"undefined: x"}]}}`))
	// readLoop dispatches sequentially on one goroutine and replies to a
	// server→client request inline, so the reply below can only arrive after
	// storeDiagnostics above has run.
	rawWrite(t, conn, toFramed(`{"jsonrpc":"2.0","id":1,"method":"window/workDoneProgress/create","params":{"token":"x"}}`))
	env := readReply(t, conn, rd)
	if got := string(env["id"]); got != "1" {
		t.Errorf("reply id = %s, want 1", got)
	}

	diags := c.DiagnosticsFor("file:///x.go")
	if len(diags) != 1 {
		t.Fatalf("DiagnosticsFor(file:///x.go) = %+v, want exactly one diagnostic", diags)
	}
	want := Diagnostic{
		Range:    Range{Start: Position{Line: 1, Character: 2}, End: Position{Line: 1, Character: 3}},
		Severity: 1,
		Message:  "undefined: x",
	}
	if diags[0] != want {
		t.Errorf("diagnostic = %+v, want %+v", diags[0], want)
	}
}

// ---- client.go: teardown -----------------------------------------------

func TestTeardown_FailsEveryPendingRequest(t *testing.T) {
	logs := captureLogs(t)
	c, _, cleanup := newInProcessServer(t)
	defer cleanup()

	ch1 := make(chan *rpcResponse, 1)
	ch2 := make(chan *rpcResponse, 1)
	c.mu.Lock()
	c.pending[11] = ch1
	c.pending[12] = ch2
	c.mu.Unlock()

	c.teardown(errors.New("read boom"))

	for i, ch := range []chan *rpcResponse{ch1, ch2} {
		select {
		case resp := <-ch:
			if resp == nil {
				t.Errorf("pending %d: got nil response, want ErrServerDead", i)
				continue
			}
			if !errors.Is(resp.err, ErrServerDead) {
				t.Errorf("pending %d err = %v, want ErrServerDead", i, resp.err)
			}
		default:
			t.Errorf("pending %d got no response after teardown", i)
		}
	}

	c.mu.Lock()
	n := len(c.pending)
	c.mu.Unlock()
	if n != 0 {
		t.Errorf("pending map still holds %d entries after teardown, want 0", n)
	}

	select {
	case <-c.Dead():
	default:
		t.Error("Dead channel not closed by teardown")
	}

	recs := logs.find("lsp:read_error")
	if len(recs) != 1 {
		t.Fatalf("lsp:read_error logged %d times, want 1", len(recs))
	}
	if recs[0].level != slog.LevelWarn {
		t.Errorf("lsp:read_error level = %v, want Warn", recs[0].level)
	}
	if errVal, ok := recs[0].attrs["err"].(error); !ok || errVal.Error() != "read boom" {
		t.Errorf("lsp:read_error err attr = %#v, want error \"read boom\"", recs[0].attrs["err"])
	}
	if got := recs[0].attrs["name"]; got != "fake" {
		t.Errorf("lsp:read_error name attr = %#v, want fake", got)
	}

	info := logs.find("lsp:teardown")
	if len(info) != 1 {
		t.Fatalf("lsp:teardown logged %d times, want 1", len(info))
	}
	if errVal, ok := info[0].attrs["err"].(string); !ok || errVal != "read boom" {
		t.Errorf("lsp:teardown err attr = %#v, want \"read boom\"", info[0].attrs["err"])
	}
}

func TestTeardown_SuppressesPlainEOF(t *testing.T) {
	logs := captureLogs(t)
	c, _, cleanup := newInProcessServer(t)
	defer cleanup()

	c.teardown(io.EOF)

	if recs := logs.find("lsp:read_error"); len(recs) != 0 {
		t.Errorf("lsp:read_error logged %d times for io.EOF, want 0: %+v", len(recs), recs)
	}
	if recs := logs.find("lsp:teardown"); len(recs) != 0 {
		t.Errorf("lsp:teardown logged %d times for io.EOF, want 0: %+v", len(recs), recs)
	}
	select {
	case <-c.Dead():
	default:
		t.Error("Dead channel not closed by teardown(io.EOF)")
	}
}

// ---- edits.go: HoverAt -------------------------------------------------

func TestHoverAt_NullResultAndRequestParams(t *testing.T) {
	var gotParams json.RawMessage
	c, _, cleanup := initClient(t, func(req rpcRequest) (any, bool) {
		if req.Method == "textDocument/hover" {
			gotParams, _ = json.Marshal(req.Params)
			return nil, true
		}
		return nil, false
	})
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	h, err := HoverAt(ctx, c, "file:///repo/x.go", Position{Line: 2, Character: 3})
	if err != nil {
		t.Fatalf("HoverAt: %v", err)
	}
	if h != nil {
		t.Errorf("HoverAt = %+v, want nil for a null result", h)
	}

	want := `{"position":{"character":3,"line":2},"textDocument":{"uri":"file:///repo/x.go"}}`
	if string(gotParams) != want {
		t.Errorf("hover params = %s, want %s", gotParams, want)
	}
}

func TestHoverAt_BadJSONWrapsSyntaxError(t *testing.T) {
	c, _, cleanup := initClient(t, func(req rpcRequest) (any, bool) {
		if req.Method == "textDocument/hover" {
			return "not a hover object", true
		}
		return nil, false
	})
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	h, err := HoverAt(ctx, c, "file:///repo/x.go", Position{})
	if err == nil {
		t.Fatalf("HoverAt = %+v, want a decode error", h)
	}
	if _, ok := errors.AsType[*json.UnmarshalTypeError](err); !ok {
		t.Errorf("hover error does not wrap *json.UnmarshalTypeError: %v", err)
	}
	if !strings.HasPrefix(err.Error(), "lsp hover: ") {
		t.Errorf("hover error = %q, want prefix %q", err.Error(), "lsp hover: ")
	}
}
