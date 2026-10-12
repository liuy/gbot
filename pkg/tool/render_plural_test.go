package tool

import "testing"

// Callers pass the plural label ("matches", "entries", "files", "lines",
// "occurrences") and expect real English back for n==1 — naive trailing-"s"
// stripping produces "matche"/"entrie".
func TestPluralWord_SingularForms(t *testing.T) {
	t.Parallel()
	cases := []struct {
		n    int
		word string
		want string
	}{
		{1, "matches", "match"},
		{1, "entries", "entry"},
		{1, "files", "file"},
		{1, "lines", "line"},
		{1, "occurrences", "occurrence"},
		{0, "matches", "matches"},
		{2, "matches", "matches"},
		{0, "entries", "entries"},
		{5, "entries", "entries"},
	}
	for _, c := range cases {
		if got := PluralWord(c.n, c.word); got != c.want {
			t.Errorf("PluralWord(%d, %q) = %q, want %q", c.n, c.word, got, c.want)
		}
	}
}
