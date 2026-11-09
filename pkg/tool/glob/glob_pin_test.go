package glob

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/liuy/gbot/pkg/tool"
)

// Pin tests for the glob.go mutation sweep; assertions are exact so a single
// changed byte flips the test. Mutant line:mutator pairs are in the campaign
// ledger.

// fakeRg installs a stand-in ripgrep whose behavior is chosen by mode.
func pinRg(t *testing.T, script string) {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "rg")
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GBOT_RG_PATH", p)
}

func TestPinGlob_ToolMetadata(t *testing.T) {
	t.Parallel()
	tl := New()

	if tl.MaxResultSize() != 100000 {
		t.Errorf("MaxResultSize = %d", tl.MaxResultSize())
	}
	if !strings.Contains(tl.Prompt(), "glob") && !strings.Contains(tl.Prompt(), "file") {
		t.Errorf("Prompt = %.50q", tl.Prompt())
	}
	if sr := tl.(interface {
		IsSearchOrRead(json.RawMessage) tool.SearchReadKind
	}).IsSearchOrRead(nil); !sr.IsSearch {
		t.Errorf("IsSearchOrRead = %+v, want search", sr)
	}
}

func TestPinGlob_RenderSingleFile(t *testing.T) {
	t.Parallel()
	tl := New()
	// A single file renders as its bare name; the empty check is len == 0.
	got := tl.RenderResult(&Output{Files: []string{"only.txt"}, Count: 1})
	if got != "only.txt" {
		t.Errorf("RenderResult single = %q", got)
	}
	if got := tl.RenderResult(&Output{}); got != "" {
		t.Errorf("RenderResult empty = %q, want empty", got)
	}

	wb := tl.(tool.ToolWithWireBlocks)
	blocks := wb.FormatWireBlocks(&Output{})
	if len(blocks) != 1 || blocks[0].Text != "No files found" {
		t.Errorf("empty wire = %+v", blocks)
	}
}

func TestPinGlob_DecodeRoundTrip(t *testing.T) {
	t.Parallel()
	tl := New()
	dr := tl.(tool.ToolWithDecodeResult)
	wire, err := json.Marshal([]map[string]any{{"type": "text", "text": `{"filenames":["a"],"numFiles":1,"durationMs":5,"truncated":false}`}})
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := dr.DecodeResult(wire)
	if err != nil {
		t.Fatal(err)
	}
	o, ok := decoded.(*Output)
	if !ok || len(o.Files) != 1 || o.Files[0] != "a" || o.Count != 1 {
		t.Errorf("decode = %#v", decoded)
	}
}

func TestPinGlob_RgStartFailure(t *testing.T) {
	// No t.Parallel: t.Setenv.
	t.Setenv("GBOT_RG_PATH", filepath.Join(t.TempDir(), "definitely-not-rg"))
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := rgRaw(context.Background(), []string{"--files", dir})
	if err == nil {
		t.Fatal("want error when rg binary is missing")
	}
	var rge *rgError
	if !errors.As(err, &rge) {
		t.Fatalf("err = %v (%T), want *rgError", err, err)
	}
	// stderr is provably empty on the Start-failure path, so the wrapped error
	// is the only observable field; pin it so field-clear mutants die on an
	// assertion instead of a nil-deref panic in Error().
	if rge.wrapped == nil || !errors.Is(rge.wrapped, fs.ErrNotExist) {
		t.Errorf("wrapped = %v, want fs.ErrNotExist from missing rg binary", rge.wrapped)
	}
	if !strings.Contains(err.Error(), "ripgrep") {
		t.Errorf("err = %v, want ripgrep mention", err)
	}
}

func TestPinGlob_RgExitCodes(t *testing.T) {
	// No t.Parallel: t.Setenv (via pinRg).
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("needle\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Exit 1 (no matches) is success with empty output.
	pinRg(t, "#!/bin/sh\nexit 1\n")
	got, err := rgRaw(context.Background(), []string{"pattern", dir})
	if err != nil {
		t.Fatalf("exit-1 err = %v, want nil", err)
	}
	if len(got) != 0 {
		t.Errorf("exit-1 results = %v, want empty", got)
	}

	// Exit 2 (real error) surfaces as an error, not an empty success.
	pinRg(t, "#!/bin/sh\necho boom >&2\nexit 2\n")
	_, err = rgRaw(context.Background(), []string{"pattern", dir})
	if err == nil {
		t.Fatal("exit-2 err = nil, want error")
	}
	var rge *rgError
	if !errors.As(err, &rge) {
		t.Errorf("exit-2 err = %v (%T), want *rgError", err, err)
	}
	if !strings.Contains(rge.stderr, "boom") {
		t.Errorf("rgError stderr = %q, want captured stderr", rge.stderr)
	}
	if !strings.Contains(err.Error(), "ripgrep error") {
		t.Errorf("exit-2 err = %v, want ripgrep error mention", err)
	}

	// Exit 0 with output parses one path per line.
	pinRg(t, "#!/bin/sh\nprintf 'a.txt\\nb.txt\\n'\nexit 0\n")
	got, err = rgRaw(context.Background(), []string{"--files", dir})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != "a.txt" || got[1] != "b.txt" {
		t.Errorf("exit-0 results = %v", got)
	}
}
