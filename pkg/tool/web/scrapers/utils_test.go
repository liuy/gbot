package scrapers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFormatNumber(t *testing.T) {
	tests := []struct {
		input int
		want  string
	}{
		{0, "0"},
		{1, "1"},
		{999, "999"},
		{1000, "1,000"},
		{12345, "12,345"},
		{1234567, "1,234,567"},
		{-1234, "-1,234"},
	}
	for _, tt := range tests {
		got := formatNumber(tt.input)
		if got != tt.want {
			t.Errorf("formatNumber(%d) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestFetchBytes_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("hello"))
	}))
	defer srv.Close()

	client := &http.Client{Transport: &rewriteTransport{server: srv, host: "hacker-news.firebaseio.com"}}
	data, err := fetchBytes(context.Background(), client, "https://hacker-news.firebaseio.com/v0/item/1.json")
	if err != nil {
		t.Fatalf("fetchBytes() error = %v", err)
	}
	if string(data) != "hello" {
		t.Errorf("data = %q, want hello", string(data))
	}
}

func TestFetchBytes_404(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	client := &http.Client{Transport: &rewriteTransport{server: srv, host: "example.com"}}
	_, err := fetchBytes(context.Background(), client, "https://example.com/missing")
	if err == nil {
		t.Fatal("expected error for 404")
	}
	if !strings.Contains(err.Error(), "404") {
		t.Errorf("error should mention 404, got: %v", err)
	}
}

func TestFetchJSON_InvalidJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("not json"))
	}))
	defer srv.Close()

	client := &http.Client{Transport: &rewriteTransport{server: srv, host: "example.com"}}
	_, err := fetchJSON(context.Background(), client, "https://example.com/data")
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
	if !strings.Contains(err.Error(), "invalid JSON") {
		t.Errorf("error should mention invalid JSON, got: %v", err)
	}
}

func TestFetchJSON_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"key":"value"}`))
	}))
	defer srv.Close()

	client := &http.Client{Transport: &rewriteTransport{server: srv, host: "example.com"}}
	raw, err := fetchJSON(context.Background(), client, "https://example.com/data")
	if err != nil {
		t.Fatalf("fetchJSON() error = %v", err)
	}
	if !strings.Contains(string(raw), "value") {
		t.Errorf("raw = %q, want value", string(raw))
	}
}

func TestFetchBytes_NilClient(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()
	_, _ = fetchBytes(context.Background(), nil, "https://hacker-news.firebaseio.com/v0/item/1.json")
}

func TestFetchBytes_BadURL(t *testing.T) {
	_, err := fetchBytes(context.Background(), http.DefaultClient, "://bad-url")
	if err == nil {
		t.Fatal("expected error for bad URL")
	}
	if !strings.Contains(err.Error(), "missing protocol scheme") {
		t.Errorf("error should mention bad URL, got: %v", err)
	}
}

func TestFetchBytes_ClientDoError(t *testing.T) {
	_, err := fetchBytes(context.Background(), http.DefaultClient, "http://127.0.0.1:1")
	if err == nil {
		t.Fatal("expected error for unreachable")
	}
	if !strings.Contains(err.Error(), "refused") {
		t.Errorf("fetchBytes err = %q, want 'refused'", err.Error())
	}
}

func TestIsBinary(t *testing.T) {
	if isBinary([]byte("plain text")) {
		t.Error("plain text should not be binary")
	}
	if !isBinary([]byte{0x00, 0x01, 0x02}) {
		t.Error("data with null byte should be binary")
	}
	if isBinary(nil) {
		t.Error("empty data should not be binary")
	}
}

func TestStripLogTimestamp(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"2024-01-15T00:00:00.0000000Z hello world", "hello world"},
		{"short line", "short line"},
		{"not a timestamp just text", "not a timestamp just text"},
	}
	for _, tt := range tests {
		got := stripLogTimestamp(tt.input)
		if got != tt.want {
			t.Errorf("stripLogTimestamp(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestDecodeURL(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"foo_bar", "foo bar"},
		{"hello%20world", "hello world"},
		{"%E4%B8%AD%E6%96%87", "中文"},
	}
	for _, tt := range tests {
		got := decodeURL(tt.input)
		if got != tt.want {
			t.Errorf("decodeURL(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestStripHTMLTags(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"<p>hello</p>", "hello"},
		{`<a href="x">link</a>`, "link"},
		{"a &amp; b", "a & b"},
		{"&lt;tag&gt;", "<tag>"},
		{`&quot;quoted&quot;`, `"quoted"`},
		{"it&#39;s", "it's"},
		{"a&nbsp;b", "a b"},
		{"<p>a</p>\n\n<p>b</p>", "a b"},
		{"", ""},
	}
	for _, tt := range tests {
		got := stripHTMLTags(tt.input)
		if got != tt.want {
			t.Errorf("stripHTMLTags(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestStripTags(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"<p>hello</p>", "hello"},
		{"plain", "plain"},
		{`<a href="x">link</a>`, "link"},
		{"", ""},
		{"<p>nested <b>bold</b></p>", "nested bold"},
	}
	for _, tt := range tests {
		got := stripTags(tt.input)
		if got != tt.want {
			t.Errorf("stripTags(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}
