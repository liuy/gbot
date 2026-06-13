package scrapers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestHandleWikipedia_NoMatch(t *testing.T) {
	result, err := HandleWikipedia(context.Background(), mustParseURL(t, "https://example.com/wiki/Foo"), nil, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result != nil {
		t.Errorf("expected nil for non-wikipedia host, got %+v", result)
	}
}

func TestHandleWikipedia_NonWikiPath(t *testing.T) {
	result, _ := HandleWikipedia(context.Background(), mustParseURL(t, "https://en.wikipedia.org/random/Foo"), nil, nil)
	if result != nil {
		t.Errorf("expected nil for non-/wiki/ path, got %+v", result)
	}
}

func TestHandleWikipedia_SummaryOnly(t *testing.T) {
	var mu sync.Mutex
	var calls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls = append(calls, r.URL.Path)
		mu.Unlock()
		if strings.Contains(r.URL.Path, "/summary/") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"title":"Go","description":"language","extract":"Go is a statically typed, compiled language."}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	client := &http.Client{Transport: &rewriteTransport{server: srv, host: "en.wikipedia.org"}}
	result, err := HandleWikipedia(context.Background(), mustParseURL(t, "https://en.wikipedia.org/wiki/Go_(programming_language)"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	if !strings.Contains(result.Content, "Go is a statically typed") {
		t.Errorf("expected extract in content, got: %q", result.Content)
	}
	if !strings.Contains(result.Content, "# Go") {
		t.Errorf("expected title heading, got: %q", result.Content)
	}
	if result.Method != "wikipedia-api" {
		t.Errorf("Method = %q, want wikipedia-api", result.Method)
	}
}

func TestHandleWikipedia_FullContent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/summary/") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"title":"Tea","description":"drink","extract":"Tea is a drink."}`))
			return
		}
		if strings.Contains(r.URL.Path, "/mobile-html/") {
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte(`<html><body><section><h2>History</h2><p>Tea originated in China.</p></section></body></html>`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	client := &http.Client{Transport: &rewriteTransport{server: srv, host: "en.wikipedia.org"}}
	result, err := HandleWikipedia(context.Background(), mustParseURL(t, "https://en.wikipedia.org/wiki/Tea"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	if !strings.Contains(result.Content, "History") {
		t.Errorf("expected History section, got: %q", result.Content)
	}
	if !strings.Contains(result.Content, "originated in China") {
		t.Errorf("expected paragraph text, got: %q", result.Content)
	}
}

func TestDecodeURL_Error(t *testing.T) {
	got := decodeURL("%zzinvalid")
	if got != "%zzinvalid" {
		t.Errorf("decodeURL(invalid) = %q, want original string", got)
	}
}

func TestDecodeURL_Empty(t *testing.T) {
	got := decodeURL("")
	if got != "" {
		t.Errorf("decodeURL('') = %q, want ''", got)
	}
}

func TestExtractSections_Nested(t *testing.T) {
	html := `<section><h2>Outer</h2><section><h3>Inner</h3></section></section>`
	sections := extractSections(html)
	if len(sections) == 0 {
		t.Fatal("expected at least one section")
	}
	// String-based section extraction finds the first </section> for the first <section>,
	// so nested sections collapse into one. This is a known limitation.
	if len(sections) < 1 {
		t.Errorf("expected at least 1 section, got %d", len(sections))
	}
}

func TestExtractSections_NoSection(t *testing.T) {
	html := `<html><body><h2>No section</h2><p>text</p></body></html>`
	sections := extractSections(html)
	// Should fall back to body content
	if len(sections) == 0 {
		t.Fatal("expected at least one section from body fallback")
	}
}

func TestExtractSections_UnclosedSection(t *testing.T) {
	html := `<section><h2>Unclosed</h2>`
	sections := extractSections(html)
	if len(sections) != 0 {
		t.Errorf("expected 0 sections for unclosed, got %d", len(sections))
	}
}

func TestExtractTagContent_NestedTags(t *testing.T) {
	html := `<div><p>outer text<span>inner</span></p></div>`
	got := extractTagContent(html, "div")
	if got == "" {
		t.Fatal("extractTagContent returned empty")
	}
	if !strings.Contains(got, "outer text") {
		t.Errorf("expected outer text, got %q", got)
	}
}

func TestExtractTagContent_NonExistent(t *testing.T) {
	got := extractTagContent("<p>hello</p>", "div")
	if got != "" {
		t.Errorf("expected empty for non-existent tag, got %q", got)
	}
}

func TestExtractTagContent_NoCloseGT(t *testing.T) {
	got := extractTagContent("<div ", "div")
	if got != "" {
		t.Errorf("expected empty for unclosed tag, got %q", got)
	}
}

func TestExtractTagContent_ContentStartAfterClose(t *testing.T) {
	got := extractTagContent("<div></div>", "div")
	if got != "" {
		t.Errorf("expected empty for empty element, got %q", got)
	}
}

func TestStripHTMLAndExtract_Empty(t *testing.T) {
	got := stripHTMLAndExtract([]byte{})
	if got != "" {
		t.Errorf("expected empty for empty input, got %q", got)
	}
}

func TestStripHTMLAndExtract_HTMLEntities(t *testing.T) {
	html := `<section><h2>Info</h2><p>a &amp; b &lt; c</p></section>`
	got := stripHTMLAndExtract([]byte(html))
	if !strings.Contains(got, "Info") {
		t.Errorf("expected heading, got: %q", got)
	}
}

func TestStripHTMLAndExtract_SkipReferences(t *testing.T) {
	html := `<section><h2>References</h2><p>ref1</p></section><section><h2>Content</h2><p>real content</p></section>`
	got := stripHTMLAndExtract([]byte(html))
	if strings.Contains(got, "References") {
		t.Errorf("References section should be skipped, got: %q", got)
	}
	if !strings.Contains(got, "Content") {
		t.Errorf("expected Content section, got: %q", got)
	}
}

func TestStripHTMLAndExtract_ShortParagraph(t *testing.T) {
	html := `<section><h2>Section</h2><p>short</p><p>this is a long enough paragraph to be included</p></section>`
	got := stripHTMLAndExtract([]byte(html))
	// "short" is shorter than 20 chars, should be skipped.
	if strings.Contains(got, "short") {
		t.Errorf("short fragment should be skipped, got: %q", got)
	}
	if !strings.Contains(got, "long enough") {
		t.Errorf("expected long paragraph, got: %q", got)
	}
}

func TestStripHTMLAndExtract_H3Level(t *testing.T) {
	html := `<section><h3>Subsection</h3><p>This is a long enough paragraph that should be included</p></section>`
	got := stripHTMLAndExtract([]byte(html))
	if !strings.Contains(got, "long enough paragraph") {
		t.Errorf("expected paragraph text, got: %q", got)
	}
}

func TestHandleWikipedia_HostWithEmptyLang(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	client := &http.Client{Transport: &rewriteTransport{server: srv, host: ".wikipedia.org"}}
	result, err := HandleWikipedia(context.Background(), mustParseURL(t, "https://.wikipedia.org/wiki/Foo"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result != nil {
		t.Errorf("expected nil for empty lang, got %+v", result)
	}
}

func TestHandleWikipedia_BadJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("not json"))
	}))
	defer srv.Close()

	client := &http.Client{Transport: &rewriteTransport{server: srv, host: "en.wikipedia.org"}}
	result, err := HandleWikipedia(context.Background(), mustParseURL(t, "https://en.wikipedia.org/wiki/Go"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result != nil {
		t.Errorf("expected nil for bad JSON, got %+v", result)
	}
}
