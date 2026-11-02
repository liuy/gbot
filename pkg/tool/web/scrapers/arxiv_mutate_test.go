package scrapers

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// arxivFeed serves an Atom entry and records the query the scraper built.
func arxivFeed(t *testing.T, xml string) (*http.Client, *string) {
	t.Helper()
	query := new(string)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*query = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/atom+xml")
		_, _ = io.WriteString(w, xml)
	}))
	t.Cleanup(srv.Close)
	return &http.Client{Transport: &rewriteTransport{server: srv, host: "export.arxiv.org"}}, query
}

func TestHandleArxiv_FullEntry(t *testing.T) {
	client, query := arxivFeed(t, `<feed><entry>`+
		`<id>http://arxiv.org/abs/2401.12345v1</id>`+
		`<title>Test Paper</title>`+
		`<summary>The abstract.</summary>`+
		`<author><name>Alice Smith</name></author>`+
		`<author><name>Bob Jones</name></author>`+
		`<published>2024-01-15T09:30:00Z</published>`+
		`<category term="cs.AI"/><category term="cs.LG"/>`+
		`</entry></feed>`)

	result, err := HandleArxiv(context.Background(), mustParseURL(t, "https://arxiv.org/abs/2401.12345"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	want := "# Test Paper\n\n" +
		"**Authors:** Alice Smith, Bob Jones\n" +
		"**Published:** 2024-01-15\n" +
		"**Categories:** cs.AI, cs.LG\n" +
		"**arXiv:** 2401.12345\n" +
		"\n---\n\n## Abstract\n\nThe abstract.\n"
	if result.Content != want {
		t.Errorf("Content = %q, want %q", result.Content, want)
	}
	if result.ContentType != "text/markdown" {
		t.Errorf("ContentType = %q, want text/markdown", result.ContentType)
	}
	if result.Method != "arxiv-api" {
		t.Errorf("Method = %q, want arxiv-api", result.Method)
	}
	if len(result.Notes) != 1 || result.Notes[0] != "Fetched via arXiv OAI-PMH API" {
		t.Errorf("Notes = %q, want [Fetched via arXiv OAI-PMH API]", result.Notes)
	}
	if *query != "id_list=2401.12345" {
		t.Errorf("query = %q, want id_list=2401.12345", *query)
	}
}

// A single-element list separates "more than one" from "at least one".
func TestHandleArxiv_SingleAuthorAndCategory(t *testing.T) {
	client, _ := arxivFeed(t, `<feed><entry><id>x</id><title>Solo</title><summary>Sum.</summary>`+
		`<author><name>Only Author</name></author><published>2024-02-01T00:00:00Z</published>`+
		`<category term="cs.CL"/></entry></feed>`)

	result, err := HandleArxiv(context.Background(), mustParseURL(t, "https://arxiv.org/abs/2402.00001"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	want := "# Solo\n\n**Authors:** Only Author\n**Published:** 2024-02-01\n**Categories:** cs.CL\n" +
		"**arXiv:** 2402.00001\n\n---\n\n## Abstract\n\nSum.\n"
	if result.Content != want {
		t.Errorf("Content = %q, want %q", result.Content, want)
	}
}

func TestHandleArxiv_NoAuthorsOrCategories(t *testing.T) {
	client, _ := arxivFeed(t, `<feed><entry><id>x</id><title>Bare</title><summary>Sum.</summary>`+
		`<published>2024-03-01T00:00:00Z</published></entry></feed>`)

	result, err := HandleArxiv(context.Background(), mustParseURL(t, "https://arxiv.org/abs/2403.00001"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	want := "# Bare\n\n**Published:** 2024-03-01\n**arXiv:** 2403.00001\n\n---\n\n## Abstract\n\nSum.\n"
	if result.Content != want {
		t.Errorf("Content = %q, want %q", result.Content, want)
	}
}

// A published stamp shorter than the date prefix must pass through untouched.
func TestHandleArxiv_ShortPublishedStamp(t *testing.T) {
	client, _ := arxivFeed(t, `<feed><entry><id>x</id><title>Short</title><summary>Sum.</summary>`+
		`<published>2024-01-1</published></entry></feed>`)

	result, err := HandleArxiv(context.Background(), mustParseURL(t, "https://arxiv.org/abs/2401.00002"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	want := "# Short\n\n**Published:** 2024-01-1\n**arXiv:** 2401.00002\n\n---\n\n## Abstract\n\nSum.\n"
	if result.Content != want {
		t.Errorf("Content = %q, want %q", result.Content, want)
	}
}

func TestHandleArxiv_PdfSuffixStrippedFromID(t *testing.T) {
	client, query := arxivFeed(t, `<feed><entry><id>x</id><title>P</title><summary>S.</summary>`+
		`<published>2024-04-01T00:00:00Z</published></entry></feed>`)

	result, err := HandleArxiv(context.Background(), mustParseURL(t, "https://arxiv.org/pdf/2404.00001.pdf"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	if *query != "id_list=2404.00001" {
		t.Errorf("query = %q, want id_list=2404.00001", *query)
	}
	want := "# P\n\n**Published:** 2024-04-01\n**arXiv:** 2404.00001\n\n---\n\n## Abstract\n\nS.\n"
	if result.Content != want {
		t.Errorf("Content = %q, want %q", result.Content, want)
	}
}

func TestHandleArxiv_EmptyPaperID(t *testing.T) {
	client, _ := arxivFeed(t, `<feed><entry><id>x</id><title>P</title></entry></feed>`)
	result, err := HandleArxiv(context.Background(), mustParseURL(t, "https://arxiv.org/abs/"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result != nil {
		t.Errorf("expected nil for empty id, got %+v", result)
	}
}

func TestHandleArxiv_NonArxivHostDoesNotFetch(t *testing.T) {
	client, _ := arxivFeed(t, `<feed><entry><id>x</id><title>P</title></entry></feed>`)
	result, err := HandleArxiv(context.Background(), mustParseURL(t, "https://example.com/abs/1234"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result != nil {
		t.Errorf("expected nil for non-arxiv host, got %+v", result)
	}
}

func TestHandleArxiv_FetchError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	client := &http.Client{Transport: &rewriteTransport{server: srv, host: "export.arxiv.org"}}
	result, err := HandleArxiv(context.Background(), mustParseURL(t, "https://arxiv.org/abs/2401.00003"), client, nil)
	if result != nil {
		t.Errorf("expected nil result, got %+v", result)
	}
	want := "HTTP 404 from https://export.arxiv.org/api/query?id_list=2401.00003"
	if err == nil {
		t.Fatalf("expected error, want %q", want)
	}
	if err.Error() != want {
		t.Errorf("error = %q, want %q", err.Error(), want)
	}
}

func TestHandleArxiv_MalformedXMLWrapsParseError(t *testing.T) {
	client, _ := arxivFeed(t, "not xml")
	result, err := HandleArxiv(context.Background(), mustParseURL(t, "https://arxiv.org/abs/2401.00004"), client, nil)
	if result != nil {
		t.Errorf("expected nil result, got %+v", result)
	}
	if err == nil {
		t.Fatal("expected error for malformed XML")
	}
	if err.Error() != "failed to parse arXiv API response: EOF" {
		t.Errorf("error = %q, want %q", err.Error(), "failed to parse arXiv API response: EOF")
	}
	if !errors.Is(err, io.EOF) {
		t.Errorf("error %v does not unwrap to io.EOF", err)
	}
}

func TestHandleArxiv_TransportErrorKeepsChain(t *testing.T) {
	sentinel := errors.New("arxiv unreachable")
	client := &http.Client{Transport: errTransport{err: sentinel}}
	result, err := HandleArxiv(context.Background(), mustParseURL(t, "https://arxiv.org/abs/2401.00005"), client, nil)
	if result != nil {
		t.Errorf("expected nil result, got %+v", result)
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("error = %v, want wrapped %v", err, sentinel)
	}
}
