package short

import "testing"

// TestSegmentQuery_WordPhraseOrJoin verifies the OR+phrase pipeline: each
// whitespace-separated word becomes a phrase of its analyzer tokens (CJK
// bigrams inside one word must stay adjacent), and words are joined with OR
// so bm25 ranking — not implicit AND — separates good hits from noise.
func TestSegmentQuery_WordPhraseOrJoin(t *testing.T) {
	store := &Store{}
	tests := []struct {
		name  string
		query string
		want  string
	}{
		// 3-char CJK word: two bigrams form one phrase.
		{"cjk word phrase", "事务化", `"事务 务化"`},
		// 2-char CJK word: single bigram, no quotes needed.
		{"cjk single bigram", "蓝色", "蓝色"},
		// Two CJK words: one phrase each, joined with OR.
		{"two cjk words", "向量 数据库", `向量 OR "数据 据库"`},
		// Latin words pass through whole.
		{"latin words", "alice blue", "alice OR blue"},
		{"latin single", "hello", "hello"},
		// Mixed: per-word phrases, OR between words.
		{"mixed", "数据库 migration", `"数据 据库" OR migration`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := store.SegmentQuery(tt.query); got != tt.want {
				t.Errorf("SegmentQuery(%q) = %q, want %q", tt.query, got, tt.want)
			}
		})
	}
}

// TestSegmentQuery_StripsOperators verifies that FTS5 operator keywords are
// stripped as stopwords instead of being searched as literal terms or passed
// through as syntax. The schema is keyword-oriented, but the LLM may still
// emit operators from older habits — they must not break the pipeline.
func TestSegmentQuery_StripsOperators(t *testing.T) {
	store := &Store{}
	tests := []struct {
		name  string
		query string
		want  string
	}{
		{"AND", "blue AND red", "blue OR red"},
		{"lowercase and", "blue and red", "blue OR red"},
		{"OR", "blue OR red", "blue OR red"},
		{"NOT", "teacher NOT alice", "teacher OR alice"},
		{"NEAR", "foo NEAR bar", "foo OR bar"},
		// NEAR/3 is one word tokenizing to [near 3]; near is stripped, 3 stays.
		{"NEAR/N", "foo NEAR/3 bar", "foo OR 3 OR bar"},
		// Operators only: nothing searchable remains.
		{"all operators", "AND OR NOT NEAR", ""},
		{"empty", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := store.SegmentQuery(tt.query); got != tt.want {
				t.Errorf("SegmentQuery(%q) = %q, want %q", tt.query, got, tt.want)
			}
		})
	}
}

// TestSegmentQuery_LowercaseInsideWord confirms a lowercase "and" inside a
// word is not mistaken for an operator — only exact token equality is
// stripped, so "android" stays a single searchable term.
func TestSegmentQuery_LowercaseInsideWord(t *testing.T) {
	store := &Store{}
	if got := store.SegmentQuery("android"); got != "android" {
		t.Errorf("SegmentQuery(\"android\") = %q, want \"android\"", got)
	}
}
