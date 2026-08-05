package facts

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

// openTestDB opens a fresh SQLite database in a temp dir and returns (db, cleanup).
func openTestDB(t *testing.T) (*sql.DB, func()) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	// Match production pragmas so WAL/foreign_keys behavior is exercised.
	for _, p := range []string{"PRAGMA journal_mode=WAL", "PRAGMA foreign_keys=ON"} {
		if _, err := db.Exec(p); err != nil {
			_ = db.Close()
			t.Fatalf("pragma %q: %v", p, err)
		}
	}
	return db, func() {
		if err := db.Close(); err != nil {
			t.Fatalf("close db: %v", err)
		}
	}
}

// identitySegmenter is a no-op segmenter used by tests that pre-segment
// content manually (so they don't depend on gse dictionary load timing).
func identitySegmenter(s string) string { return s }

func TestNewStore_NilDeps(t *testing.T) {
	if _, err := NewStore(nil, identitySegmenter); err == nil {
		t.Error("NewStore(nil db, _) should error")
	}
	db, cleanup := openTestDB(t)
	defer cleanup()
	if _, err := NewStore(db, nil); err == nil {
		t.Error("NewStore(_, nil segmenter) should error")
	}
}

func TestInitSchema_Idempotent(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	s1, err := NewStore(db, identitySegmenter)
	if err != nil {
		t.Fatalf("first NewStore: %v", err)
	}
	// A second NewStore on the same db must not fail (CREATE TABLE IF NOT EXISTS).
	s2, err := NewStore(db, identitySegmenter)
	if err != nil {
		t.Fatalf("second NewStore: %v", err)
	}
	if s1 == nil || s2 == nil {
		t.Fatal("stores must not be nil")
	}
}

func TestAddFact_Insert(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	s, err := NewStore(db, identitySegmenter)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	id, inserted, err := s.AddFact("张三 喜欢 蓝色")
	if err != nil {
		t.Fatalf("AddFact: %v", err)
	}
	if !inserted {
		t.Error("first AddFact should report inserted=true")
	}
	if id <= 0 {
		t.Errorf("fact_id should be > 0, got %d", id)
	}
	hits, err := s.SearchFacts("蓝色", 10)
	if err != nil {
		t.Fatalf("SearchFacts: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("expected 1 hit, got %d", len(hits))
	}
	if hits[0].ID != id {
		t.Errorf("hit id = %d, want %d", hits[0].ID, id)
	}
	if hits[0].Content != "张三 喜欢 蓝色" {
		t.Errorf("content = %q, want original", hits[0].Content)
	}
}

func TestAddFact_Duplicate(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	s, err := NewStore(db, identitySegmenter)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	id1, inserted1, err := s.AddFact("dup fact")
	if err != nil {
		t.Fatalf("first AddFact: %v", err)
	}
	id2, inserted2, err := s.AddFact("dup fact")
	if err != nil {
		t.Fatalf("second AddFact: %v", err)
	}
	if inserted2 {
		t.Error("duplicate AddFact should report inserted=false")
	}
	if id1 != id2 {
		t.Errorf("duplicate should return same id: got %d and %d", id1, id2)
	}
	if !inserted1 {
		t.Error("first insert should report inserted=true")
	}
	// Verify no duplicate FTS map row was added on the second call.
	var mapCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM facts_fts_map WHERE fact_id = ?`, id1).Scan(&mapCount); err != nil {
		t.Fatalf("count map rows: %v", err)
	}
	if mapCount != 1 {
		t.Errorf("facts_fts_map should have exactly 1 row for this fact, got %d", mapCount)
	}
}

func TestAddFact_Empty(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	s, err := NewStore(db, identitySegmenter)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	for _, in := range []string{"", "   ", "\t\n"} {
		if _, _, err := s.AddFact(in); err == nil {
			t.Errorf("AddFact(%q) should error", in)
		}
	}
}

func TestAddFact_SegmentsBeforeInsert(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	called := false
	seg := func(s string) string {
		called = true
		return s
	}
	s, err := NewStore(db, seg)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	id, _, err := s.AddFact("hello world")
	if err != nil {
		t.Fatalf("AddFact: %v", err)
	}
	if !called {
		t.Error("segmenter was not invoked during AddFact")
	}
	// Verify the map row exists, linking fact_id to the FTS index.
	var mapCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM facts_fts_map WHERE fact_id = ?`, id).Scan(&mapCount); err != nil {
		t.Fatalf("count map: %v", err)
	}
	if mapCount != 1 {
		t.Errorf("expected 1 map row, got %d", mapCount)
	}
}

func TestDeleteFact_Cascade(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	s, err := NewStore(db, identitySegmenter)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	id, _, err := s.AddFact("to be deleted")
	if err != nil {
		t.Fatalf("AddFact: %v", err)
	}
	if err := s.DeleteFact(id); err != nil {
		t.Fatalf("DeleteFact: %v", err)
	}
	hits, err := s.SearchFacts("deleted", 10)
	if err != nil {
		t.Fatalf("SearchFacts after delete: %v", err)
	}
	if len(hits) != 0 {
		t.Errorf("expected 0 hits after delete, got %d", len(hits))
	}
	// facts_fts_map row must be cleared so the orphaned FTS row can't JOIN back.
	var mapCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM facts_fts_map WHERE fact_id = ?`, id).Scan(&mapCount); err != nil {
		t.Fatalf("count map: %v", err)
	}
	if mapCount != 0 {
		t.Errorf("map should be empty after delete, got %d rows", mapCount)
	}
}

func TestDeleteFact_NonExistent(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	s, err := NewStore(db, identitySegmenter)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	// Deleting an unknown id must not error.
	if err := s.DeleteFact(99999); err != nil {
		t.Errorf("DeleteFact(unknown) returned error: %v", err)
	}
}

func TestSearchFacts_AndOrNot(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	s, err := NewStore(db, identitySegmenter)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	// Pre-segmented content (identity segmenter): each Chinese token separated
	// by spaces so unicode61 can split on whitespace.
	for _, c := range []string{
		"张三 喜欢 蓝色",
		"张三 喜欢 红色",
		"王丽君 是 教师",
	} {
		if _, _, err := s.AddFact(c); err != nil {
			t.Fatalf("AddFact(%q): %v", c, err)
		}
	}

	tests := []struct {
		name     string
		query    string
		wantHits int
	}{
		{"AND hit", "蓝色 AND 张三", 1},
		{"AND miss", "蓝色 AND 红色", 0},
		{"OR both colors", "张三 AND (蓝色 OR 红色)", 2},
		{"NOT teacher", "教师 NOT 张三", 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hits, err := s.SearchFacts(tt.query, 10)
			if err != nil {
				t.Fatalf("SearchFacts(%q): %v", tt.query, err)
			}
			if len(hits) != tt.wantHits {
				t.Errorf("query %q: got %d hits, want %d", tt.query, len(hits), tt.wantHits)
				for _, h := range hits {
					t.Logf("  hit: id=%d content=%q", h.ID, h.Content)
				}
			}
		})
	}
}

func TestSearchFacts_Malformed(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	s, err := NewStore(db, identitySegmenter)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	for _, q := range []string{"(", "*", "AND"} {
		hits, err := s.SearchFacts(q, 10)
		if err != nil {
			t.Errorf("SearchFacts(%q) should not error on malformed query, got: %v", q, err)
		}
		if hits != nil {
			t.Errorf("SearchFacts(%q) should return nil, got %d hits", q, len(hits))
		}
	}
}

func TestSearchFacts_Empty(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	s, err := NewStore(db, identitySegmenter)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	for _, q := range []string{"", "   ", "\t"} {
		hits, err := s.SearchFacts(q, 10)
		if err != nil {
			t.Fatalf("SearchFacts(%q): %v", q, err)
		}
		if hits != nil {
			t.Errorf("SearchFacts(%q) should return nil, got %d", q, len(hits))
		}
	}
}

func TestSearchFacts_LimitClamp(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	s, err := NewStore(db, identitySegmenter)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	// Insert 3 facts sharing the "apple" token.
	for i := range 3 {
		if _, _, err := s.AddFact("apple variant " + string(rune('a'+i))); err != nil {
			t.Fatalf("AddFact: %v", err)
		}
	}
	// limit <= 0 → default 50 → returns all 3
	hits, err := s.SearchFacts("apple", 0)
	if err != nil {
		t.Fatalf("SearchFacts: %v", err)
	}
	if len(hits) != 3 {
		t.Errorf("default limit: got %d hits, want 3", len(hits))
	}
	// limit > 200 → clamped to 200 (still returns all 3 here)
	hits, err = s.SearchFacts("apple", 500)
	if err != nil {
		t.Fatalf("SearchFacts: %v", err)
	}
	if len(hits) != 3 {
		t.Errorf("clamped limit: got %d hits, want 3", len(hits))
	}
	// limit = 1
	hits, err = s.SearchFacts("apple", 1)
	if err != nil {
		t.Fatalf("SearchFacts: %v", err)
	}
	if len(hits) != 1 {
		t.Errorf("limit=1: got %d hits, want 1", len(hits))
	}
}

// TestSearchFacts_PersistenceAcrossInstances verifies that data written by one
// Store is visible to a fresh Store opened on the same db (no in-memory state).
func TestSearchFacts_PersistenceAcrossInstances(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	s1, err := NewStore(db, identitySegmenter)
	if err != nil {
		t.Fatalf("NewStore s1: %v", err)
	}
	if _, _, err := s1.AddFact("persist me"); err != nil {
		t.Fatalf("AddFact: %v", err)
	}
	s2, err := NewStore(db, identitySegmenter)
	if err != nil {
		t.Fatalf("NewStore s2: %v", err)
	}
	hits, err := s2.SearchFacts("persist", 10)
	if err != nil {
		t.Fatalf("SearchFacts s2: %v", err)
	}
	if len(hits) != 1 {
		t.Errorf("s2 should see persisted fact: got %d hits", len(hits))
	}
	if !strings.Contains(hits[0].Content, "persist") {
		t.Errorf("unexpected content: %q", hits[0].Content)
	}
}
