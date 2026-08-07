package short

import (
	"regexp"
	"strings"

	cjk "github.com/bitxeno/go-cjk-tokenizer"
)

// cjkAnalyzer is the shared analyzer instance. It is stateless and safe for
// concurrent use after construction.
var cjkAnalyzer = cjk.NewAnalyzer()

// ftsOperatorRe matches FTS5 query syntax that callers may rely on. When
// present, SegmentQuery returns the query verbatim so the user's boolean
// expression is preserved instead of being tokenized into inert terms.
var ftsOperatorRe = regexp.MustCompile(`(?i)\b(AND|OR|NOT|NEAR)\b|[()"]`)

// Segment tokenizes text using a CJK bigram tokenizer: CJK ideographs become
// 2-grams, Latin/numbers stay whole. The same function runs on both the index
// side (pre-segmenting content before FTS5 insert) and the query side
// (via SegmentQuery) so token boundaries always match.
//
// Replaces the previous gse-based segmenter: no 50MB dictionary load, no
// startup delay, and no dictionary-load timing to coordinate.
func (s *Store) Segment(text string) string {
	if text == "" {
		return ""
	}
	tokens := cjkAnalyzer.Analyze([]byte(text))
	parts := make([]string, 0, len(tokens))
	for _, tok := range tokens {
		parts = append(parts, string(tok.Term))
	}
	return strings.Join(parts, " ")
}

// SegmentQuery segments a query string for FTS5 MATCH. When the query contains
// FTS5 operators (AND/OR/NOT/NEAR, parentheses, quotes), it is returned
// verbatim so the caller's syntax is preserved. Otherwise, the query is
// segmented the same way as the index so CJK tokens match.
func (s *Store) SegmentQuery(query string) string {
	if ftsOperatorRe.MatchString(query) {
		return query
	}
	return s.Segment(query)
}
