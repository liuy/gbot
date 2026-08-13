package recall

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/memory/short"
	"github.com/liuy/gbot/pkg/tool"
)

// openStore creates a short.Store in a temp dir for testing.
func openStore(t *testing.T) *short.Store {
	t.Helper()
	store, err := short.NewStore(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("short.NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func TestRecall_MissingQuery(t *testing.T) {
	fs := openStore(t)
	r := New(fs)
	_, err := r.Call(context.Background(), json.RawMessage(`{}`), nil)
	if err == nil {
		t.Fatal("expected error for missing query")
	}
	if !strings.Contains(err.Error(), "query is required") {
		t.Errorf("error = %q, want 'query is required'", err.Error())
	}
}

func TestRecall_HitsMessages(t *testing.T) {
	fs := openStore(t)
	sess, err := fs.CreateSession("/test", "test-model")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	msg := &short.TranscriptMessage{
		UUID:      "msg-blue-1",
		Type:      "user",
		Content:   `[{"type":"text","text":"alice mentioned blue today"}]`,
		CreatedAt: time.Date(2026, 7, 15, 10, 0, 0, 0, time.UTC),
	}
	if err := fs.AppendMessage(sess.SessionID, msg); err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}

	r := New(fs)
	result, err := r.Call(context.Background(), json.RawMessage(`{"query":"blue"}`), nil)
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	out, ok := result.Data.(*Output)
	if !ok {
		t.Fatalf("Data type = %T, want *Output", result.Data)
	}
	if len(out.Messages) != 1 {
		t.Fatalf("expected 1 message hit, got %d", len(out.Messages))
	}
	if out.Messages[0].Content != "alice mentioned blue today" {
		t.Errorf("msg content = %q", out.Messages[0].Content)
	}
	if out.Messages[0].Date != "2026-07-15 10:00" {
		t.Errorf("msg date = %q, want 2026-07-15 10:00", out.Messages[0].Date)
	}
}

func TestRecall_InvalidSince(t *testing.T) {
	fs := openStore(t)
	r := New(fs)
	_, err := r.Call(context.Background(), json.RawMessage(`{"query":"blue","since":"bad"}`), nil)
	if err == nil {
		t.Fatal("expected error for invalid since")
	}
	if !strings.Contains(err.Error(), "parse since") {
		t.Errorf("error = %q, want 'parse since'", err.Error())
	}
}

func TestRecall_MalformedQuery(t *testing.T) {
	fs := openStore(t)
	r := New(fs)
	result, err := r.Call(context.Background(), json.RawMessage(`{"query":"("}`), nil)
	if err != nil {
		t.Fatalf("malformed query should not error: %v", err)
	}
	out := result.Data.(*Output)
	if len(out.Messages) != 0 {
		t.Errorf("malformed query should return 0 messages, got %d", len(out.Messages))
	}
}

func TestRecall_EmptyResults(t *testing.T) {
	fs := openStore(t)
	r := New(fs)
	result, err := r.Call(context.Background(), json.RawMessage(`{"query":"nothinghere"}`), nil)
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	out := result.Data.(*Output)
	if len(out.Messages) != 0 {
		t.Errorf("expected 0 messages, got %d", len(out.Messages))
	}
	b, _ := json.Marshal(out)
	jsonStr := string(b)
	if strings.Contains(jsonStr, `"messages":null`) {
		t.Errorf("messages should be [], got null: %s", jsonStr)
	}
}

func TestRecall_LimitClamp(t *testing.T) {
	fs := openStore(t)
	r := New(fs)
	_, err := r.Call(context.Background(), json.RawMessage(`{"query":"test","limit":0}`), nil)
	if err != nil {
		t.Fatalf("limit=0: %v", err)
	}
	_, err = r.Call(context.Background(), json.RawMessage(`{"query":"test","limit":500}`), nil)
	if err != nil {
		t.Fatalf("limit=500: %v", err)
	}
}

func TestRecall_IsReadOnly(t *testing.T) {
	fs := openStore(t)
	r := New(fs)
	if !r.IsReadOnly(nil) {
		t.Error("recall should be read-only")
	}
	if !r.IsConcurrencySafe(nil) {
		t.Error("recall should be concurrency-safe")
	}
	if r.InterruptBehavior() != tool.InterruptCancel {
		t.Error("recall should use InterruptCancel")
	}
}

func TestRecall_RenderResult(t *testing.T) {
	r := New(nil)
	rendered := r.RenderResult(&Output{
		Messages: []msgHit{{Content: "test msg", Date: "2026-01-02"}},
	})
	if !strings.Contains(rendered, "[msg]") {
		t.Errorf("render should contain msg line: %s", rendered)
	}
	if !strings.Contains(rendered, "test msg") {
		t.Errorf("render should contain msg content: %s", rendered)
	}
	empty := r.RenderResult(&Output{Messages: []msgHit{}})
	if empty != "No matches found." {
		t.Errorf("empty render = %q, want 'No matches found.'", empty)
	}
}

func TestRecall_DecodeResult(t *testing.T) {
	r := New(nil)
	decoder, ok := r.(tool.ToolWithDecodeResult)
	if !ok {
		t.Fatal("recall tool should implement ToolWithDecodeResult")
	}
	innerJSON := `{"messages":[{"content":"hello","date":"2026-01-01"}]}`
	wire := tool.WrapSingleBlock(innerJSON)
	decoded, err := decoder.DecodeResult(wire)
	if err != nil {
		t.Fatalf("DecodeResult: %v", err)
	}
	out, ok := decoded.(*Output)
	if !ok {
		t.Fatalf("decoded type = %T, want *Output", decoded)
	}
	if len(out.Messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(out.Messages))
	}
	if out.Messages[0].Content != "hello" {
		t.Errorf("content = %q, want 'hello'", out.Messages[0].Content)
	}
}

func TestRecall_Since(t *testing.T) {
	fs := openStore(t)
	// REAL-TIME: parseSince computes cutoff from time.Now; test needs matching reference.
	now := time.Now()
	oldTime := now.Add(-100 * 24 * time.Hour)

	sess, err := fs.CreateSession("/test", "test-model")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	oldMsg := &short.TranscriptMessage{
		UUID:      "msg-old-since",
		Type:      "user",
		Content:   `[{"type":"text","text":"old blue message"}]`,
		CreatedAt: oldTime,
	}
	if err := fs.AppendMessage(sess.SessionID, oldMsg); err != nil {
		t.Fatalf("AppendMessage old: %v", err)
	}
	recentMsg := &short.TranscriptMessage{
		UUID:      "msg-recent-since",
		Type:      "user",
		Content:   `[{"type":"text","text":"recent blue message"}]`,
		CreatedAt: now,
	}
	if err := fs.AppendMessage(sess.SessionID, recentMsg); err != nil {
		t.Fatalf("AppendMessage recent: %v", err)
	}

	r := New(fs)
	// since="1m" (30 days): old data (100 days) excluded, recent included.
	result, err := r.Call(context.Background(),
		json.RawMessage(`{"query":"blue","since":"1m"}`), nil)
	if err != nil {
		t.Fatalf("Call since=1m: %v", err)
	}
	out := result.Data.(*Output)
	if len(out.Messages) != 1 {
		t.Errorf("since=1m: expected 1 recent message, got %d", len(out.Messages))
	} else if out.Messages[0].Content != "recent blue message" {
		t.Errorf("since=1m: msg content = %q, want 'recent blue message'", out.Messages[0].Content)
	}

	// since="1y" (365 days): both old and recent included.
	result, err = r.Call(context.Background(),
		json.RawMessage(`{"query":"blue","since":"1y"}`), nil)
	if err != nil {
		t.Fatalf("Call since=1y: %v", err)
	}
	out = result.Data.(*Output)
	if len(out.Messages) != 2 {
		t.Errorf("since=1y: expected 2 messages, got %d", len(out.Messages))
	}
}

func TestRecall_MessageSnippet(t *testing.T) {
	fs := openStore(t)
	sess, err := fs.CreateSession("/test", "test-model")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	longText := "This is a very long message about blue skies and green grass and many other colorful things in nature that goes well beyond fifty runes and keeps going"
	msg := &short.TranscriptMessage{
		UUID:      "msg-snippet-test",
		Type:      "user",
		Content:   `[{"type":"text","text":"` + longText + `"}]`,
		CreatedAt: time.Date(2026, 7, 15, 10, 0, 0, 0, time.UTC),
	}
	if err := fs.AppendMessage(sess.SessionID, msg); err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}

	r := New(fs)
	result, err := r.Call(context.Background(),
		json.RawMessage(`{"query":"blue"}`), nil)
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	out := result.Data.(*Output)
	if len(out.Messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(out.Messages))
	}
	snippet := out.Messages[0].Content
	if snippet == longText {
		t.Error("snippet should be truncated, not equal to full text")
	}
	if !strings.Contains(snippet, "blue") {
		t.Errorf("snippet should contain 'blue': %q", snippet)
	}
	if !strings.Contains(snippet, "...") {
		t.Errorf("snippet should contain ellipsis: %q", snippet)
	}
}

func TestParseSince(t *testing.T) {
	tests := []struct {
		input   string
		wantErr bool
		// dur is the expected approximate duration from now to the cutoff.
		dur time.Duration
	}{
		{"", false, 0}, // zero time = no filter
		{"1h", false, 1 * time.Hour},
		{"12h", false, 12 * time.Hour},
		{"7d", false, 7 * 24 * time.Hour},
		{"2w", false, 2 * 7 * 24 * time.Hour},
		{"3m", false, 3 * 30 * 24 * time.Hour},
		{"1y", false, 1 * 365 * 24 * time.Hour},
		{"bad", true, 0},
		{"7", true, 0},
		{"7x", true, 0},
		{"-1d", true, 0},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result, err := parseSince(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("parseSince(%q) expected error, got nil", tt.input)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseSince(%q) unexpected error: %v", tt.input, err)
			}
			if tt.input == "" {
				if !result.IsZero() {
					t.Errorf("parseSince(%q) = %v, want zero time", tt.input, result)
				}
				return
			}
			// REAL-TIME: parseSince calls time.Now internally.
			elapsed := time.Since(result)
			diff := elapsed - tt.dur
			if diff < 0 {
				diff = -diff
			}
			if diff > 5*time.Second {
				t.Errorf("parseSince(%q) elapsed = %v, want ~%v (diff %v)", tt.input, elapsed, tt.dur, diff)
			}
		})
	}
}

func TestMakeSnippet(t *testing.T) {
	tests := []struct {
		name     string
		text     string
		query    string
		maxRunes int
		want     string
	}{
		{
			name:     "short text returned as-is",
			text:     "hello world",
			query:    "hello",
			maxRunes: 50,
			want:     "hello world",
		},
		{
			name:     "empty text",
			text:     "",
			query:    "foo",
			maxRunes: 50,
			want:     "",
		},
		{
			name:     "term found centered",
			text:     "aaaaaaaaaa blue aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			query:    "blue",
			maxRunes: 10,
			want:     "...aa blue aa...",
		},
		{
			name:     "term not found takes from start",
			text:     "bbbbbbbbbb cccccccccc dddddddddd eeeeeeeeee ffffffffff",
			query:    "zzz",
			maxRunes: 10,
			want:     "bbbbbbbbbb...",
		},
		{
			name:     "CJK text rune-aware",
			text:     "这是一段很长的中文文本蓝色是关键词后面还有很多很多很多很多很多很多很多很多字",
			query:    "蓝色",
			maxRunes: 10,
			want:     "...中文文本蓝色是关键词...",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := makeSnippet(tt.text, tt.query, tt.maxRunes)
			if got != tt.want {
				t.Errorf("makeSnippet() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestMakeSnippet_TermAtStart(t *testing.T) {
	text := "blue then a lot of padding text that goes on and on and on far beyond the limit"
	got := makeSnippet(text, "blue", 10)
	if !strings.HasPrefix(got, "blue") {
		t.Errorf("snippet should start with 'blue': %q", got)
	}
	if strings.HasPrefix(got, "...") {
		t.Errorf("snippet should not have leading ellipsis when term is at start: %q", got)
	}
	if !strings.HasSuffix(got, "...") {
		t.Errorf("snippet should have trailing ellipsis: %q", got)
	}
}

func TestMakeSnippet_TermAtEnd(t *testing.T) {
	text := "a lot of padding text that goes on and on and on far beyond the limit then blue"
	got := makeSnippet(text, "blue", 10)
	if !strings.HasSuffix(got, "blue") {
		t.Errorf("snippet should end with 'blue': %q", got)
	}
	if !strings.HasPrefix(got, "...") {
		t.Errorf("snippet should have leading ellipsis when term is at end: %q", got)
	}
	// No trailing ellipsis because the window reaches the end of the text.
	if strings.HasSuffix(got, "...") {
		t.Errorf("snippet should not have trailing ellipsis when end == text length: %q", got)
	}
}

func TestFirstQueryTerm(t *testing.T) {
	tests := []struct {
		query string
		want  string
	}{
		{"blue", "blue"},
		{"alice AND blue", "alice"},
		{"NOT blue", "blue"},
		{"(blue)", "blue"},
		{`"blue sky"`, "blue"},
		{"蓝色", "蓝色"},
		{"", ""},
		{"AND OR NOT NEAR", ""},
	}
	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			got := firstQueryTerm(tt.query)
			if got != tt.want {
				t.Errorf("firstQueryTerm(%q) = %q, want %q", tt.query, got, tt.want)
			}
		})
	}
}
