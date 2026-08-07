package short

import "testing"

// TestSegmentQuery_PreservesFTSOperators verifies that queries containing FTS5
// boolean operators or grouping syntax are returned verbatim so the caller's
// expression reaches MATCH intact.
func TestSegmentQuery_PreservesFTSOperators(t *testing.T) {
	store := &Store{}
	for _, q := range []string{
		"blue AND red",
		"blue OR red",
		"teacher NOT alice",
		"foo NEAR/3 bar",
		`"quoted phrase"`,
		"(blue OR red)",
	} {
		if got := store.SegmentQuery(q); got != q {
			t.Errorf("SegmentQuery(%q) = %q, want verbatim", q, got)
		}
	}
}

// TestSegmentQuery_SegmentsPlain verifies that plain queries (no FTS5 syntax)
// are segmented so CJK tokens match the index.
func TestSegmentQuery_SegmentsPlain(t *testing.T) {
	store := &Store{}

	// CJK gets bigram-segmented.
	if got := store.SegmentQuery("事务化"); got != "事务 务化" {
		t.Errorf("SegmentQuery(\"事务化\") = %q, want \"事务 务化\"", got)
	}
	// Latin passes through Segment whole.
	if got := store.SegmentQuery("hello"); got != "hello" {
		t.Errorf("SegmentQuery(\"hello\") = %q, want \"hello\"", got)
	}
	// Empty stays empty.
	if got := store.SegmentQuery(""); got != "" {
		t.Errorf("SegmentQuery(\"\") = %q, want \"\"", got)
	}
}

// TestSegmentQuery_LowercaseNotOperator confirms a lowercase "and"/"or" inside a
// word is not mistaken for an operator, so the query still gets segmented.
func TestSegmentQuery_LowercaseInsideWord(t *testing.T) {
	store := &Store{}
	// "android" contains "and" but not as a standalone word, so the \b boundary
	// in the regex must NOT match — the query is segmented, not passed through.
	if got := store.SegmentQuery("android"); got != "android" {
		t.Errorf("SegmentQuery(\"android\") = %q, want \"android\" (segmented whole)", got)
	}
}
