package scrapers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandleArxiv_NoMatch(t *testing.T) {
	result, _ := HandleArxiv(context.Background(), mustParseURL(t, "https://example.com/abs/1234"), nil, nil)
	if result != nil {
		t.Errorf("expected nil for non-arxiv host, got %+v", result)
	}
}

func TestHandleArxiv_NonAbsPath(t *testing.T) {
	result, _ := HandleArxiv(context.Background(), mustParseURL(t, "https://arxiv.org/list/cs/1234"), nil, nil)
	if result != nil {
		t.Errorf("expected nil for non-abs/pdf path, got %+v", result)
	}
}

func TestHandleArxiv_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/atom+xml")
		_, _ = w.Write([]byte(`<?xml version="1.0"?>
<feed>
  <entry>
    <id>http://arxiv.org/abs/2401.12345v1</id>
    <title>Test Paper Title</title>
    <summary>This is the abstract of the test paper.</summary>
    <author><name>Alice Smith</name></author>
    <author><name>Bob Jones</name></author>
    <published>2024-01-15T00:00:00Z</published>
    <category term="cs.AI"/>
    <category term="cs.LG"/>
  </entry>
</feed>`))
	}))
	defer srv.Close()

	client := &http.Client{Transport: &rewriteTransport{server: srv, host: "export.arxiv.org"}}
	result, err := HandleArxiv(context.Background(), mustParseURL(t, "https://arxiv.org/abs/2401.12345"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	if !strings.Contains(result.Content, "Test Paper Title") {
		t.Errorf("expected title, got: %q", result.Content)
	}
	if !strings.Contains(result.Content, "Alice Smith") {
		t.Errorf("expected author, got: %q", result.Content)
	}
	if !strings.Contains(result.Content, "2024-01-15") {
		t.Errorf("expected date, got: %q", result.Content)
	}
	if !strings.Contains(result.Content, "cs.AI") {
		t.Errorf("expected category, got: %q", result.Content)
	}
	if !strings.Contains(result.Content, "This is the abstract") {
		t.Errorf("expected abstract, got: %q", result.Content)
	}
	if result.Method != "arxiv-api" {
		t.Errorf("Method = %q, want arxiv-api", result.Method)
	}
}

func TestHandleArxiv_PdfPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/atom+xml")
		_, _ = w.Write([]byte(`<feed><entry><id>x</id><title>From PDF Path</title><summary>abs</summary><author><name>Y</name></author></entry></feed>`))
	}))
	defer srv.Close()

	client := &http.Client{Transport: &rewriteTransport{server: srv, host: "export.arxiv.org"}}
	result, err := HandleArxiv(context.Background(), mustParseURL(t, "https://arxiv.org/pdf/2401.12345.pdf"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	if !strings.Contains(result.Content, "From PDF Path") {
		t.Errorf("expected title, got: %q", result.Content)
	}
}

func TestHandleArxiv_EmptyFeed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<feed></feed>`))
	}))
	defer srv.Close()

	client := &http.Client{Transport: &rewriteTransport{server: srv, host: "export.arxiv.org"}}
	result, err := HandleArxiv(context.Background(), mustParseURL(t, "https://arxiv.org/abs/9999.99999"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result != nil {
		t.Errorf("expected nil for empty feed, got %+v", result)
	}
}

func TestHandleArxiv_BadXML(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("not xml"))
	}))
	defer srv.Close()

	client := &http.Client{Transport: &rewriteTransport{server: srv, host: "export.arxiv.org"}}
	_, err := HandleArxiv(context.Background(), mustParseURL(t, "https://arxiv.org/abs/1234"), client, nil)
	if err == nil {
		t.Fatal("expected error for bad XML")
	}
	if !strings.Contains(err.Error(), "parse arXiv") {
		t.Fatalf("error should mention parse failure, got: %v", err)
	}
}
