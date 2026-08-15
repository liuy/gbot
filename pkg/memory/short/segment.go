package short

import (
	"strings"

	cjk "github.com/bitxeno/go-cjk-tokenizer"
)

// cjkAnalyzer is the shared analyzer instance. It is stateless and safe for
// concurrent use after construction.
var cjkAnalyzer = cjk.NewAnalyzer()

// ftsStopword holds FTS5 boolean keywords that are dropped from queries
// instead of searched as literal terms. The schema is keyword-oriented, but
// the LLM may still emit operators from older habits — "blue AND red" must
// degrade to "blue OR red", not zero out on messages lacking a literal "AND".
var ftsStopword = map[string]bool{
	"AND": true, "OR": true, "NOT": true, "NEAR": true,
}

// segmentTokens returns the analyzer tokens for text: CJK ideographs become
// 2-grams, Latin/numbers stay whole.
func segmentTokens(text string) []string {
	tokens := cjkAnalyzer.Analyze([]byte(text))
	parts := make([]string, 0, len(tokens))
	for _, tok := range tokens {
		parts = append(parts, string(tok.Term))
	}
	return parts
}

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
	return strings.Join(segmentTokens(text), " ")
}

// SegmentQuery converts a raw query into an FTS5 MATCH expression following
// an OR+phrase pipeline: split on whitespace into words, tokenize each word
// (same analyzer as the index), join a word's tokens into a quoted phrase so
// CJK bigrams stay adjacent within the word, then OR the phrases together.
// bm25 ranking (ORDER BY f.rank) scores the whole expression, so documents
// matching more words rank first — recall over precision, which is what an
// LLM-facing search tool needs: a missed hit is unrecoverable, a weak hit
// gets filtered by reading the snippet.
//
// A word whose only tokens are FTS5 operator keywords contributes nothing;
// a query made entirely of them produces an empty MATCH, which degrades to
// empty results at the call site.
func (s *Store) SegmentQuery(query string) string {
	var phrases []string
	for word := range strings.FieldsSeq(query) {
		var kept []string
		for _, tok := range segmentTokens(word) {
			if ftsStopword[strings.ToUpper(tok)] {
				continue
			}
			kept = append(kept, tok)
		}
		if len(kept) == 0 {
			continue
		}
		if len(kept) == 1 {
			phrases = append(phrases, kept[0])
			continue
		}
		phrases = append(phrases, `"`+strings.Join(kept, " ")+`"`)
	}
	return strings.Join(phrases, " OR ")
}
