package repl

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// Empty input yields the long API description; real input yields the snippet.
func TestREPL_Description(t *testing.T) {
	t.Parallel()
	tr := New()
	for _, in := range []json.RawMessage{nil, []byte("null"), []byte("{}")} {
		got, err := tr.Description(in)
		if err != nil {
			t.Fatalf("Description(%v): %v", in, err)
		}
		if got != replDescription {
			t.Errorf("Description(%q) = %q, want the long repl description", in, got)
		}
	}
	got, err := tr.Description(json.RawMessage(`{"code":"let x = 1"}`))
	if err != nil || got != "let x = 1" {
		t.Errorf("Description(code) = %q, %v, want the code snippet", got, err)
	}
	got, err = tr.Description(json.RawMessage(`{"code":""}`))
	if err != nil || got != "Execute JavaScript code" {
		t.Errorf("Description(empty code) = %q, %v, want the generic label", got, err)
	}
	got, err = tr.Description(json.RawMessage(`{`))
	if err != nil || got != "Execute JavaScript code" {
		t.Errorf("Description(bad json) = %q, %v, want the generic label", got, err)
	}
}

// Identifiers keep letters (including z/Z boundaries), digits (not leading),
// _ and $; everything else becomes an underscore.
func TestNormalizeIdentifier_Boundaries(t *testing.T) {
	t.Parallel()
	cases := []struct{ in, want string }{
		{"toolz", "toolz"},
		{"ToolZ", "ToolZ"},
		{"z9", "z9"},
		{"$foo", "$foo"},
		{"ünicode", "_nicode"},
		{"a.b", "a_b"},
	}
	for _, c := range cases {
		if got := normalizeIdentifier(c.in); got != c.want {
			t.Errorf("normalizeIdentifier(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// console.log output is buffered per Execute and flushed as the result.
func TestSession_ExecuteConsole(t *testing.T) {
	t.Parallel()
	s, err := NewSession()
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer s.Close()

	out, evidence, err := s.Execute(context.Background(), "console.log('a', 1, true);", "/tmp", nil, 5000, nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if out != "a 1 true\n" {
		t.Errorf("console output = %q, want %q", out, "a 1 true\n")
	}
	if len(evidence) != 0 {
		t.Errorf("evidence = %v, want none", evidence)
	}

	// A second Execute starts from an empty buffer.
	out, _, err = s.Execute(context.Background(), "console.log('second');", "/tmp", nil, 5000, nil)
	if err != nil {
		t.Fatalf("second Execute: %v", err)
	}
	if out != "second\n" {
		t.Errorf("second output = %q, want second", out)
	}

	// image() collects evidence paths.
	out, evidence, err = s.Execute(context.Background(), "image('/tmp/x.png'); console.log('done');", "/tmp", nil, 5000, nil)
	if err != nil {
		t.Fatalf("image Execute: %v", err)
	}
	if len(evidence) != 1 || evidence[0] != "/tmp/x.png" {
		t.Errorf("evidence = %v, want [/tmp/x.png]", evidence)
	}
	if out != "done\n" {
		t.Errorf("output with image = %q, want done", out)
	}

	// image() without a path throws inside the script.
	out, _, err = s.Execute(context.Background(), "try { image(''); console.log('missed') } catch (e) { console.log('caught:' + e) }", "/tmp", nil, 5000, nil)
	if err != nil {
		t.Fatalf("image('') Execute: %v", err)
	}
	if out != "caught:image: path required\n" {
		t.Errorf("image('') output = %q, want the caught path-required error", out)
	}
}

// Async errors surface in the buffered output rather than being swallowed.
func TestSession_ExecuteAsyncError(t *testing.T) {
	t.Parallel()
	s, err := NewSession()
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer s.Close()
	out, _, err := s.Execute(context.Background(),
		"Promise.reject(new Error('boom')); console.log('sync');", "/tmp", nil, int64(2*time.Second/time.Millisecond), nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "sync") {
		t.Errorf("output = %q, want the sync log to survive", out)
	}
	if !strings.Contains(out, "Unhandled promise rejection") {
		t.Errorf("output = %q, want the unhandled rejection line", out)
	}
}
