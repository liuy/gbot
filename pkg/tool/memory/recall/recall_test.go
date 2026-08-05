package recall

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/memory/facts"
	"github.com/liuy/gbot/pkg/memory/short"
	"github.com/liuy/gbot/pkg/tool"
	_ "modernc.org/sqlite"
)

// openTestDB opens a fresh SQLite database in a temp dir.
func openTestDB(t *testing.T) (*sql.DB, func()) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	for _, p := range []string{"PRAGMA journal_mode=WAL", "PRAGMA foreign_keys=ON"} {
		if _, err := db.Exec(p); err != nil {
			_ = db.Close()
			t.Fatalf("pragma %q: %v", p, err)
		}
	}
	return db, func() { _ = db.Close() }
}

// fakeMessages is a controllable MessageSearcher for recall tests.
type fakeMessages struct {
	results []*short.SearchResult
	err     error
}

func (f *fakeMessages) SearchMessages(query string, opts *short.SearchOptions) ([]*short.SearchResult, error) {
	return f.results, f.err
}

func identitySegmenter(s string) string { return s }

func TestRecall_MissingQuery(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	fs, err := facts.NewStore(db, identitySegmenter)
	if err != nil {
		t.Fatalf("facts.NewStore: %v", err)
	}
	r := New(fs, &fakeMessages{})
	_, err = r.Call(context.Background(), json.RawMessage(`{}`), nil)
	if err == nil {
		t.Fatal("expected error for missing query")
	}
	if !strings.Contains(err.Error(), "query is required") {
		t.Errorf("error = %q, want 'query is required'", err.Error())
	}
}

func TestRecall_HitsBothSources(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	fs, err := facts.NewStore(db, identitySegmenter)
	if err != nil {
		t.Fatalf("facts.NewStore: %v", err)
	}
	if _, _, err := fs.AddFact("张三 喜欢 蓝色"); err != nil {
		t.Fatalf("AddFact: %v", err)
	}

	msgs := &fakeMessages{
		results: []*short.SearchResult{
			{
				TranscriptMessage: &short.TranscriptMessage{
					Content:   `[{"type":"text","text":"张三 mentioned 蓝色 today"}]`,
					CreatedAt: time.Date(2026, 7, 15, 10, 0, 0, 0, time.UTC),
				},
			},
		},
	}

	r := New(fs, msgs)
	result, err := r.Call(context.Background(), json.RawMessage(`{"query":"蓝色"}`), nil)
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	out, ok := result.Data.(*Output)
	if !ok {
		t.Fatalf("Data type = %T, want *Output", result.Data)
	}
	if len(out.Facts) != 1 {
		t.Errorf("expected 1 fact hit, got %d", len(out.Facts))
	} else {
		if out.Facts[0].Content != "张三 喜欢 蓝色" {
			t.Errorf("fact content = %q", out.Facts[0].Content)
		}
		if out.Facts[0].Date == "" {
			t.Error("fact date should not be empty")
		}
		if out.Facts[0].FactID <= 0 {
			t.Errorf("fact_id should be > 0, got %d", out.Facts[0].FactID)
		}
	}
	if len(out.Messages) != 1 {
		t.Errorf("expected 1 message hit, got %d", len(out.Messages))
	} else {
		if out.Messages[0].Content != "张三 mentioned 蓝色 today" {
			t.Errorf("msg content = %q", out.Messages[0].Content)
		}
		if out.Messages[0].Date != "2026-07-15" {
			t.Errorf("msg date = %q, want 2026-07-15", out.Messages[0].Date)
		}
	}
}

func TestRecall_MalformedQuery(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	fs, err := facts.NewStore(db, identitySegmenter)
	if err != nil {
		t.Fatalf("facts.NewStore: %v", err)
	}
	if _, _, err := fs.AddFact("apple banana"); err != nil {
		t.Fatalf("AddFact: %v", err)
	}

	r := New(fs, &fakeMessages{})
	result, err := r.Call(context.Background(), json.RawMessage(`{"query":"("}`), nil)
	if err != nil {
		t.Fatalf("malformed query should not error: %v", err)
	}
	out := result.Data.(*Output)
	if len(out.Facts) != 0 {
		t.Errorf("malformed query should return 0 facts, got %d", len(out.Facts))
	}
	if len(out.Messages) != 0 {
		t.Errorf("malformed query should return 0 messages, got %d", len(out.Messages))
	}
}

func TestRecall_EmptyResults(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	fs, err := facts.NewStore(db, identitySegmenter)
	if err != nil {
		t.Fatalf("facts.NewStore: %v", err)
	}

	r := New(fs, &fakeMessages{})
	result, err := r.Call(context.Background(), json.RawMessage(`{"query":"nothinghere"}`), nil)
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	out := result.Data.(*Output)
	// JSON serialization must produce `[]` not `null` for empty slices.
	if len(out.Facts) != 0 {
		t.Errorf("expected 0 facts, got %d", len(out.Facts))
	}
	if len(out.Messages) != 0 {
		t.Errorf("expected 0 messages, got %d", len(out.Messages))
	}
	// Verify JSON shape: empty arrays, not null.
	b, _ := json.Marshal(out)
	jsonStr := string(b)
	if strings.Contains(jsonStr, `"facts":null`) {
		t.Errorf("facts should be [], got null: %s", jsonStr)
	}
	if strings.Contains(jsonStr, `"messages":null`) {
		t.Errorf("messages should be [], got null: %s", jsonStr)
	}
}

func TestRecall_LimitClamp(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	fs, err := facts.NewStore(db, identitySegmenter)
	if err != nil {
		t.Fatalf("facts.NewStore: %v", err)
	}

	r := New(fs, &fakeMessages{})

	// limit=0 → default 50
	_, err = r.Call(context.Background(), json.RawMessage(`{"query":"test","limit":0}`), nil)
	if err != nil {
		t.Fatalf("limit=0: %v", err)
	}

	// limit=500 → clamped to 200, should not error
	_, err = r.Call(context.Background(), json.RawMessage(`{"query":"test","limit":500}`), nil)
	if err != nil {
		t.Fatalf("limit=500: %v", err)
	}
}

func TestRecall_IsReadOnly(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	fs, err := facts.NewStore(db, identitySegmenter)
	if err != nil {
		t.Fatalf("facts.NewStore: %v", err)
	}
	r := New(fs, &fakeMessages{})
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
	r := New(nil, nil)
	rendered := r.RenderResult(&Output{
		Facts:    []factHit{{FactID: 5, Content: "test fact", Date: "2026-01-01"}},
		Messages: []msgHit{{Content: "test msg", Date: "2026-01-02"}},
	})
	if !strings.Contains(rendered, "[fact 5]") {
		t.Errorf("render should contain fact line: %s", rendered)
	}
	if !strings.Contains(rendered, "test fact") {
		t.Errorf("render should contain fact content: %s", rendered)
	}
	if !strings.Contains(rendered, "[msg]") {
		t.Errorf("render should contain msg line: %s", rendered)
	}

	// Empty output
	empty := r.RenderResult(&Output{Facts: []factHit{}, Messages: []msgHit{}})
	if empty != "No matches found." {
		t.Errorf("empty render = %q, want 'No matches found.'", empty)
	}
}

func TestRecall_DecodeResult(t *testing.T) {
	r := New(nil, nil)
	decoder, ok := r.(tool.ToolWithDecodeResult)
	if !ok {
		t.Fatal("recall tool should implement ToolWithDecodeResult")
	}
	// DecodeResult expects the wire format: [{type:"text",text:"<json>"}]
	innerJSON := `{"facts":[{"fact_id":3,"content":"hello","date":"2026-01-01"}],"messages":[]}`
	wire := tool.WrapSingleBlock(innerJSON)
	decoded, err := decoder.DecodeResult(wire)
	if err != nil {
		t.Fatalf("DecodeResult: %v", err)
	}
	out, ok := decoded.(*Output)
	if !ok {
		t.Fatalf("decoded type = %T, want *Output", decoded)
	}
	if len(out.Facts) != 1 {
		t.Fatalf("expected 1 fact, got %d", len(out.Facts))
	}
	if out.Facts[0].FactID != 3 {
		t.Errorf("fact_id = %d, want 3", out.Facts[0].FactID)
	}
	if out.Facts[0].Content != "hello" {
		t.Errorf("content = %q, want 'hello'", out.Facts[0].Content)
	}
}
