package facts

import "strings"

// isMalformedFTSError reports whether err is an FTS5 syntax / malformed-query
// error from SQLite. Such errors are surfaced from raw LLM-authored FTS5
// queries (recall); callers should treat them as "no results" rather than
// failing the whole operation.
//
// SQLite error strings vary slightly across versions but always contain
// "fts5" or "syntax error" for FTS5 query parse failures.
func isMalformedFTSError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(strings.ToLower(msg), "fts5") || strings.Contains(msg, "syntax error")
}
