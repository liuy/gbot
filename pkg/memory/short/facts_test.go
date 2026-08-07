package short

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestAddFact_Insert(t *testing.T) {
	store := openTestStore(t)

	id, inserted, err := store.AddFact("alice likes blue")
	if err != nil {
		t.Fatalf("AddFact: %v", err)
	}
	if !inserted {
		t.Error("first AddFact should report inserted=true")
	}
	if id <= 0 {
		t.Errorf("fact_id should be > 0, got %d", id)
	}
	hits, err := store.SearchFacts("blue", 10)
	if err != nil {
		t.Fatalf("SearchFacts: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("expected 1 hit, got %d", len(hits))
	}
	if hits[0].ID != id {
		t.Errorf("hit id = %d, want %d", hits[0].ID, id)
	}
	if hits[0].Content != "alice likes blue" {
		t.Errorf("content = %q, want original", hits[0].Content)
	}
}

func TestAddFact_Duplicate(t *testing.T) {
	store := openTestStore(t)

	id1, inserted1, err := store.AddFact("dup fact")
	if err != nil {
		t.Fatalf("first AddFact: %v", err)
	}
	id2, inserted2, err := store.AddFact("dup fact")
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
	if err := store.DB().QueryRow(`SELECT COUNT(*) FROM facts_fts_map WHERE fact_id = ?`, id1).Scan(&mapCount); err != nil {
		t.Fatalf("count map rows: %v", err)
	}
	if mapCount != 1 {
		t.Errorf("facts_fts_map should have exactly 1 row for this fact, got %d", mapCount)
	}
}

func TestAddFact_Empty(t *testing.T) {
	store := openTestStore(t)

	for _, in := range []string{"", "   ", "\t\n"} {
		if _, _, err := store.AddFact(in); err == nil {
			t.Errorf("AddFact(%q) should error", in)
		}
	}
}

func TestDeleteFact_Cascade(t *testing.T) {
	store := openTestStore(t)

	id, _, err := store.AddFact("to be deleted")
	if err != nil {
		t.Fatalf("AddFact: %v", err)
	}
	if err := store.DeleteFact(id); err != nil {
		t.Fatalf("DeleteFact: %v", err)
	}
	hits, err := store.SearchFacts("deleted", 10)
	if err != nil {
		t.Fatalf("SearchFacts after delete: %v", err)
	}
	if len(hits) != 0 {
		t.Errorf("expected 0 hits after delete, got %d", len(hits))
	}
	// facts_fts_map row must be cleared so the orphaned FTS row can't JOIN back.
	var mapCount int
	if err := store.DB().QueryRow(`SELECT COUNT(*) FROM facts_fts_map WHERE fact_id = ?`, id).Scan(&mapCount); err != nil {
		t.Fatalf("count map: %v", err)
	}
	if mapCount != 0 {
		t.Errorf("map should be empty after delete, got %d rows", mapCount)
	}
}

func TestDeleteFact_NonExistent(t *testing.T) {
	store := openTestStore(t)

	// Deleting an unknown id must not error.
	if err := store.DeleteFact(99999); err != nil {
		t.Errorf("DeleteFact(unknown) returned error: %v", err)
	}
}

func TestUpdateFact_Normal(t *testing.T) {
	store := openTestStore(t)

	id1, _, err := store.AddFact("alice likes blue")
	if err != nil {
		t.Fatalf("AddFact: %v", err)
	}

	newID, inserted, err := store.UpdateFact(id1, "alice likes red")
	if err != nil {
		t.Fatalf("UpdateFact: %v", err)
	}
	if !inserted {
		t.Error("UpdateFact with new content should report inserted=true")
	}
	if newID == id1 {
		t.Error("UpdateFact should produce a new id, not reuse the old one")
	}

	hitsBlue, err := store.SearchFacts("blue", 10)
	if err != nil {
		t.Fatalf("SearchFacts(blue): %v", err)
	}
	if len(hitsBlue) != 0 {
		t.Errorf("old content should be gone, got %d hits", len(hitsBlue))
	}

	hitsRed, err := store.SearchFacts("red", 10)
	if err != nil {
		t.Fatalf("SearchFacts(red): %v", err)
	}
	if len(hitsRed) != 1 {
		t.Fatalf("expected 1 hit for new content, got %d", len(hitsRed))
	}
	if hitsRed[0].ID != newID {
		t.Errorf("hit id = %d, want %d", hitsRed[0].ID, newID)
	}
}

func TestUpdateFact_Duplicate(t *testing.T) {
	store := openTestStore(t)

	idExisting, _, err := store.AddFact("already here")
	if err != nil {
		t.Fatalf("AddFact: %v", err)
	}
	idToUpdate, _, err := store.AddFact("to be replaced")
	if err != nil {
		t.Fatalf("AddFact: %v", err)
	}

	retID, inserted, err := store.UpdateFact(idToUpdate, "already here")
	if err != nil {
		t.Fatalf("UpdateFact: %v", err)
	}
	if inserted {
		t.Error("UpdateFact to duplicate content should report inserted=false")
	}
	if retID != idExisting {
		t.Errorf("should return existing id %d, got %d", idExisting, retID)
	}

	hits, err := store.SearchFacts("already", 10)
	if err != nil {
		t.Fatalf("SearchFacts: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("expected 1 hit (the existing fact), got %d", len(hits))
	}
	if hits[0].ID != idExisting {
		t.Errorf("hit id = %d, want existing id %d", hits[0].ID, idExisting)
	}
}

func TestUpdateFact_NonExistent(t *testing.T) {
	store := openTestStore(t)

	newID, inserted, err := store.UpdateFact(99999, "brand new fact")
	if err != nil {
		t.Fatalf("UpdateFact: %v", err)
	}
	if !inserted {
		t.Error("UpdateFact on non-existent id should degrade to insert (inserted=true)")
	}
	if newID <= 0 {
		t.Errorf("new id should be > 0, got %d", newID)
	}

	hits, err := store.SearchFacts("brand", 10)
	if err != nil {
		t.Fatalf("SearchFacts: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("expected 1 hit, got %d", len(hits))
	}
	if hits[0].ID != newID {
		t.Errorf("hit id = %d, want %d", hits[0].ID, newID)
	}
}

func TestUpdateFact_Empty(t *testing.T) {
	store := openTestStore(t)

	for _, in := range []string{"", "   ", "\t\n"} {
		if _, _, err := store.UpdateFact(1, in); err == nil {
			t.Errorf("UpdateFact(%q) should error", in)
		}
	}
}

func TestSearchFacts_AndOrNot(t *testing.T) {
	store := openTestStore(t)

	for _, c := range []string{
		"alice likes blue",
		"alice likes red",
		"bob is teacher",
	} {
		if _, _, err := store.AddFact(c); err != nil {
			t.Fatalf("AddFact(%q): %v", c, err)
		}
	}

	tests := []struct {
		name     string
		query    string
		wantHits int
	}{
		{"AND hit", "blue AND alice", 1},
		{"AND miss", "blue AND red", 0},
		{"OR both colors", "alice AND (blue OR red)", 2},
		{"NOT teacher", "teacher NOT alice", 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hits, err := store.SearchFacts(tt.query, 10)
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
	store := openTestStore(t)

	for _, q := range []string{"(", "*", "AND"} {
		hits, err := store.SearchFacts(q, 10)
		if err != nil {
			t.Errorf("SearchFacts(%q) should not error on malformed query, got: %v", q, err)
		}
		if hits != nil {
			t.Errorf("SearchFacts(%q) should return nil, got %d hits", q, len(hits))
		}
	}
}

func TestSearchFacts_Empty(t *testing.T) {
	store := openTestStore(t)

	for _, q := range []string{"", "   ", "\t"} {
		hits, err := store.SearchFacts(q, 10)
		if err != nil {
			t.Fatalf("SearchFacts(%q): %v", q, err)
		}
		if hits != nil {
			t.Errorf("SearchFacts(%q) should return nil, got %d hits", q, len(hits))
		}
	}
}

func TestSearchFacts_LimitClamp(t *testing.T) {
	store := openTestStore(t)

	// Insert 3 facts sharing the "apple" token.
	for i := range 3 {
		if _, _, err := store.AddFact("apple variant " + string(rune('a'+i))); err != nil {
			t.Fatalf("AddFact: %v", err)
		}
	}
	// limit <= 0 → default 50 → returns all 3
	hits, err := store.SearchFacts("apple", 0)
	if err != nil {
		t.Fatalf("SearchFacts: %v", err)
	}
	if len(hits) != 3 {
		t.Errorf("default limit: got %d hits, want 3", len(hits))
	}
	// limit > 200 → clamped to 200 (still returns all 3 here)
	hits, err = store.SearchFacts("apple", 500)
	if err != nil {
		t.Fatalf("SearchFacts: %v", err)
	}
	if len(hits) != 3 {
		t.Errorf("clamped limit: got %d hits, want 3", len(hits))
	}
	// limit = 1
	hits, err = store.SearchFacts("apple", 1)
	if err != nil {
		t.Fatalf("SearchFacts: %v", err)
	}
	if len(hits) != 1 {
		t.Errorf("limit=1: got %d hits, want 1", len(hits))
	}
}

func TestSearchFacts_PersistenceAcrossInstances(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")

	s1, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore s1: %v", err)
	}
	if _, _, err := s1.AddFact("persist me"); err != nil {
		t.Fatalf("AddFact: %v", err)
	}

	s2, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore s2: %v", err)
	}
	defer s2.Close()
	if err := s1.Close(); err != nil {
		t.Fatalf("close s1: %v", err)
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

func TestAddFact_SegmentsContent(t *testing.T) {
	store := openTestStore(t)

	id, _, err := store.AddFact("hello world test")
	if err != nil {
		t.Fatalf("AddFact: %v", err)
	}

	// facts_fts is a contentless FTS5 table (content='') so segmented_content
	// can't be SELECTed directly. Instead verify the FTS map has a row for
	// this fact (proves the insert path ran) and the content is searchable
	// via MATCH (proves Segment output was indexed).
	var mapCount int
	err = store.DB().QueryRow("SELECT COUNT(*) FROM facts_fts_map WHERE fact_id = ?", id).Scan(&mapCount)
	if err != nil {
		t.Fatalf("count facts_fts_map: %v", err)
	}
	if mapCount != 1 {
		t.Fatalf("facts_fts_map should have 1 row for fact_id %d, got %d", id, mapCount)
	}

	var ftsMatch int
	err = store.DB().QueryRow("SELECT COUNT(*) FROM facts_fts WHERE segmented_content MATCH ?", "hello").Scan(&ftsMatch)
	if err != nil {
		t.Fatalf("MATCH query on facts_fts: %v", err)
	}
	if ftsMatch != 1 {
		t.Errorf("facts_fts MATCH 'hello' should return 1 row, got %d", ftsMatch)
	}

	// CJK content is bigram-indexed: "事务化验证" -> "事务 务化 化验 验证".
	// Searching the "验证" bigram must hit the indexed row.
	id2, _, err := store.AddFact("事务化验证")
	if err != nil {
		t.Fatalf("AddFact CJK: %v", err)
	}
	var cjkMatch int
	err = store.DB().QueryRow("SELECT COUNT(*) FROM facts_fts WHERE segmented_content MATCH ?", "验证").Scan(&cjkMatch)
	if err != nil {
		t.Fatalf("MATCH CJK query on facts_fts: %v", err)
	}
	if cjkMatch != 1 {
		t.Errorf("facts_fts MATCH '验证' should return 1 row for fact_id %d, got %d", id2, cjkMatch)
	}
}

// TestSearchFacts_CJKQuery verifies that SearchFacts segments the query the
// same way as the index, so CJK terms hit. Without SegmentQuery on the query
// side, "事务" would fail to match bigram-indexed "事务化验证".
func TestSearchFacts_CJKQuery(t *testing.T) {
	store := openTestStore(t)

	if _, _, err := store.AddFact("事务化验证"); err != nil {
		t.Fatalf("AddFact: %v", err)
	}

	tests := []struct {
		name     string
		query    string
		wantHits int
	}{
		// bigram query "事务" matches indexed bigram of "事务化验证"
		{"single bigram", "事务", 1},
		// "验证" is the trailing bigram
		{"trailing bigram", "验证", 1},
		// "事务化" segments to "事务 务化" — both present, implicit AND -> hit
		{"multi bigram AND", "事务化", 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hits, err := store.SearchFacts(tt.query, 10)
			if err != nil {
				t.Fatalf("SearchFacts(%q): %v", tt.query, err)
			}
			if len(hits) != tt.wantHits {
				t.Errorf("query %q: got %d hits, want %d", tt.query, len(hits), tt.wantHits)
			}
		})
	}
}
