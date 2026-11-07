package web

import (
	"strings"
	"testing"

	"github.com/liuy/gbot/pkg/tool/web/providers"
)

func TestFK2FormatForLLMGates(t *testing.T) {
	// Answer with zero sources must not emit a Sources header.
	resp := &providers.SearchResponse{Provider: "test", Answer: "A."}
	if got := formatForLLM(resp, nil); strings.Contains(got, "## Sources") {
		t.Errorf("zero sources: got %q, want no Sources header", got)
	}

	// A computed age wins over the published-date fallback.
	resp = &providers.SearchResponse{Provider: "test", Sources: []providers.SearchSource{{
		Title:         "T",
		URL:           "https://example.com",
		AgeSeconds:    86400 * 2,
		PublishedDate: "2024-01-15",
	}}}
	if got := formatForLLM(resp, nil); !strings.Contains(got, "(2d ago)") || strings.Contains(got, "2024-01-15") {
		t.Errorf("age fallback: got %q, want (2d ago)", got)
	}

	// Snippet at exactly 240 bytes stays whole; 241 truncates to 239 + ….
	mk := func(n int) string { return strings.Repeat("x", n) }
	resp = &providers.SearchResponse{Provider: "test", Sources: []providers.SearchSource{{
		Title: "T", URL: "https://example.com", Snippet: mk(240),
	}}}
	if got := formatForLLM(resp, nil); strings.Contains(got, "…") || !strings.Contains(got, mk(240)) {
		t.Errorf("240-byte snippet: got %q, want untruncated", got)
	}
	resp = &providers.SearchResponse{Provider: "test", Sources: []providers.SearchSource{{
		Title: "T", URL: "https://example.com", Snippet: mk(241),
	}}}
	got := formatForLLM(resp, nil)
	if !strings.Contains(got, mk(239)+"…") {
		t.Errorf("241-byte snippet: want 239 bytes + ellipsis")
	}
	if strings.Contains(got, mk(240)) {
		t.Errorf("241-byte snippet: truncation kept 240 bytes")
	}
}

func TestFK2FormatAgeBoundaries(t *testing.T) {
	tests := []struct {
		seconds int64
		want    string
	}{
		{1, "just now"},           // 1s still "just now" (0/1 boundary)
		{172799, "1d ago"},        // just under 2 days
		{86400 * 29, "29d ago"},   // last day of the days bucket
		{86400 * 30, "1mo ago"},   // first day of the months bucket
		{86400 * 59, "1mo ago"},   // 59/30 = 1 month
		{86400 * 61, "2mo ago"},   // 61/30 = 2 months
		{86400 * 364, "12mo ago"}, // last day of the months bucket
		{86400 * 365, "1y ago"},   // first day of the years bucket
		{86400 * 729, "1y ago"},   // 729/365 = 1 year
		{86400 * 730, "2y ago"},   // 730/365 = 2 years
	}
	for _, tt := range tests {
		if got := formatAge(tt.seconds); got != tt.want {
			t.Errorf("formatAge(%d) = %q, want %q", tt.seconds, got, tt.want)
		}
	}
}

func TestFK2TruncateRunesBoundaries(t *testing.T) {
	// Exact-length input returns early without indexing s[len].
	if got := truncateRunes("hello", 5); got != "hello" {
		t.Errorf("truncateRunes(exact) = %q, want hello", got)
	}
	// A mid-rune cut at byte 1 of a 3-byte rune backs off to zero.
	if got := truncateRunes("你", 1); got != "" {
		t.Errorf("truncateRunes(single CJK, 1) = %q, want empty", got)
	}
	// Mid-rune boundary inside a longer string backs off one full rune.
	if got := truncateRunes("你好世界", 5); len(got) != 3 {
		t.Errorf("truncateRunes(CJK, 5) len = %d, want 3", len(got))
	}
	// A walk that reaches byte 0 must stop there, even when byte 0 is a
	// continuation byte (invalid UTF-8 is still just bytes here).
	if got := truncateRunes("\x80\x80\x80", 2); got != "" {
		t.Errorf("truncateRunes(invalid utf8, 2) = %q, want empty", got)
	}
}
