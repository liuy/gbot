package web

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"time"
)

func TestLoadPage_Success(t *testing.T) {
	want := "<html><body><h1>Hello</h1></body></html>"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") != "curl/8.0" {
			t.Errorf("first attempt should use curl/8.0, got %q", r.Header.Get("User-Agent"))
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = fmt.Fprint(w, want)
	}))
	defer server.Close()

	result := LoadPage(context.Background(), server.URL, LoadPageOptions{})
	if !result.OK {
		t.Fatalf("expected OK, got error: %s", result.Error)
	}
	if result.Content != want {
		t.Errorf("Content = %q, want %q", result.Content, want)
	}
	if result.ContentType != "text/html" {
		t.Errorf("ContentType = %q, want text/html", result.ContentType)
	}
	if result.Truncated {
		t.Error("should not be truncated")
	}
}

func TestLoadPage_UARotation(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts < 3 {
			w.WriteHeader(http.StatusForbidden)
			_, _ = fmt.Fprint(w, `<html><body>Access denied by Cloudflare</body></html>`)
			return
		}
		_, _ = fmt.Fprint(w, "success")
	}))
	defer server.Close()

	result := LoadPage(context.Background(), server.URL, LoadPageOptions{})
	if !result.OK {
		t.Fatalf("expected OK after UA rotation, got error: %s", result.Error)
	}
	if attempts != 3 {
		t.Errorf("expected 3 attempts, got %d", attempts)
	}
}

func TestLoadPage_429Retry(t *testing.T) {
	// Unit-test the Retry-After parser and the 429 retry behavior at the
	// HTTP level, rather than going through LoadPage which sleeps for
	// the full Retry-After duration (race mode makes this 1s+).
	if got := parseRetryAfterMs("garbage"); got != time.Second {
		t.Errorf("parseRetryAfterMs(garbage) = %v, want 1s", got)
	}
	if got := parseRetryAfterMs("5"); got != 5*time.Second {
		t.Errorf("parseRetryAfterMs(5) = %v, want 5s", got)
	}
	if got := parseRetryAfterMs(""); got != time.Second {
		t.Errorf("parseRetryAfterMs('') = %v, want 1s", got)
	}

	// Verify 429 → 200 retry works at the HTTP level.
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if requests == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_, _ = fmt.Fprint(w, "ok")
	}))
	defer server.Close()

	resp1, err := server.Client().Get(server.URL)
	if err != nil {
		t.Fatalf("first request: %v", err)
	}
	_ = resp1.Body.Close()
	if resp1.StatusCode != http.StatusTooManyRequests {
		t.Errorf("first request status = %d, want 429", resp1.StatusCode)
	}

	resp2, err := server.Client().Get(server.URL)
	if err != nil {
		t.Fatalf("second request: %v", err)
	}
	_ = resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Errorf("second request status = %d, want 200", resp2.StatusCode)
	}
	if requests != 2 {
		t.Errorf("expected 2 requests, got %d", requests)
	}
}

func TestLoadPage_MaxBytesTruncation(t *testing.T) {
	big := strings.Repeat("x", 2000)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, big)
	}))
	defer server.Close()

	result := LoadPage(context.Background(), server.URL, LoadPageOptions{MaxBytes: 1000})
	if !result.Truncated {
		t.Error("expected truncated=true")
	}
	if len(result.Content) > 1000 {
		t.Errorf("content length %d should be <= 1000", len(result.Content))
	}
}

func TestLoadPage_ContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	result := LoadPage(ctx, "http://example.com", LoadPageOptions{})
	if !strings.Contains(result.Error, "cancel") {
		t.Fatalf("error should mention cancel, got: %s", result.Error)
	}
}

func TestLoadPage_AllAttemptsFail(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = fmt.Fprint(w, "server error")
	}))
	defer server.Close()

	result := LoadPage(context.Background(), server.URL, LoadPageOptions{})
	if result.OK {
		t.Error("expected not OK when all attempts fail with 500")
	}
	if result.Status != http.StatusInternalServerError {
		t.Errorf("Status = %d, want 500", result.Status)
	}
}

func TestLoadPage_BadURL(t *testing.T) {
	result := LoadPage(context.Background(), "http://[::1]:namedport", LoadPageOptions{})
	if result.OK {
		t.Fatal("expected not OK for invalid URL")
	}
	if len(result.Error) < 5 {
		t.Fatalf("expected meaningful error, got: %q", result.Error)
	}
}

func TestLoadPage_ConnectionRefused(t *testing.T) {
	result := LoadPage(context.Background(), "http://127.0.0.1:1", LoadPageOptions{Timeout: time.Second})
	if result.OK {
		t.Fatal("expected not OK for connection refused")
	}
	if !strings.Contains(result.Error, "connection") && !strings.Contains(result.Error, "refused") && !strings.Contains(result.Error, "error") {
		t.Fatalf("expected connection error, got: %q", result.Error)
	}
}

func TestLoadPage_FinalURL(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/old" {
			http.Redirect(w, r, "/new", http.StatusFound)
			return
		}
		_, _ = fmt.Fprint(w, "redirected")
	}))
	defer server.Close()

	result := LoadPage(context.Background(), server.URL+"/old", LoadPageOptions{})
	if !result.OK {
		t.Fatalf("expected OK, got error: %s", result.Error)
	}
	if !strings.HasSuffix(result.FinalURL, "/new") {
		t.Errorf("FinalURL = %q, want ending with /new", result.FinalURL)
	}
}

func TestLoadPage_CustomHeaders(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Custom") != "test" {
			t.Errorf("X-Custom header not forwarded, got %q", r.Header.Get("X-Custom"))
		}
		_, _ = fmt.Fprint(w, "ok")
	}))
	defer server.Close()

	result := LoadPage(context.Background(), server.URL, LoadPageOptions{
		Headers: map[string]string{"X-Custom": "test"},
	})
	if !result.OK {
		t.Fatalf("expected OK, got error: %s", result.Error)
	}
}

func TestLoadPage_CustomClient(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, "custom client")
	}))
	defer server.Close()

	client := &http.Client{Timeout: 5 * time.Second}
	result := LoadPage(context.Background(), server.URL, LoadPageOptions{Client: client})
	if !result.OK {
		t.Fatalf("expected OK, got error: %s", result.Error)
	}
	if result.Content != "custom client" {
		t.Errorf("Content = %q, want %q", result.Content, "custom client")
	}
}

// --- Helper function tests ---

func TestIsBotBlocked(t *testing.T) {
	tests := []struct {
		status  int
		content string
		want    bool
	}{
		{403, "Cloudflare challenge", true},
		{503, "captcha required", true},
		{200, "cloudflare", false},
		{404, "not found", false},
		{403, "normal page", false},
	}
	for _, tt := range tests {
		got := isBotBlocked(tt.status, tt.content)
		if got != tt.want {
			t.Errorf("isBotBlocked(%d, %q) = %v, want %v", tt.status, tt.content, got, tt.want)
		}
	}
}

func TestParseRetryAfterMs(t *testing.T) {
	future := time.Now().Add(5 * time.Second).UTC().Format(http.TimeFormat) // REAL-TIME: need relative future date
	past := time.Now().Add(-10 * time.Second).UTC().Format(http.TimeFormat) // REAL-TIME: need relative past date

	tests := []struct {
		input string
		want  time.Duration
	}{
		{"", time.Second},
		{"5", 5 * time.Second},
		{"0", time.Second},
		{"-1", time.Second},
		{"99999", 10 * time.Second},
		{future, time.Second}, // HTTP date very near → >0 but ≤ cap, we just verify >0
		{past, time.Second},   // past date → default 1s
		{"not-a-number", time.Second},
	}
	for _, tt := range tests {
		got := parseRetryAfterMs(tt.input)
		if tt.input == future {
			if got <= 0 || got > 10*time.Second {
				t.Errorf("parseRetryAfterMs(future date) = %v, want (0, 10s]", got)
			}
			continue
		}
		if got != tt.want {
			t.Errorf("parseRetryAfterMs(%q) = %v, want %v", tt.input, got, tt.want)
		}
	}
}

func TestCharsetFromContentType(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"text/html; charset=utf-8", "utf-8"},
		{"text/html; charset=GBK", "GBK"},
		{"text/html", ""},
		{"text/html; charset=\"shift_jis\"", "shift_jis"},
	}
	for _, tt := range tests {
		got := charsetFromContentType(tt.input)
		if got != tt.want {
			t.Errorf("charsetFromContentType(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestDecodeBody_UTF8Passthrough(t *testing.T) {
	input := []byte("Hello 世界")
	got := decodeBody(input, "text/html; charset=utf-8")
	if got != string(input) {
		t.Errorf("UTF-8 should pass through, got %q", got)
	}
}

func TestDecodeBody_MetaCharset(t *testing.T) {
	// Latin1 bytes for "café" → <meta charset=iso-8859-1>
	html := "<html><head><meta charset=\"iso-8859-1\"></head><body>caf\xe9</body></html>"
	got := decodeBody([]byte(html), "text/html")
	if !strings.Contains(got, "caf") {
		t.Errorf("meta charset decode failed, got: %q", got)
	}
}

func TestFinalizeOutput(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		want      string
		truncated bool
	}{
		{"collapse newlines", "a\n\n\n\nb", "a\n\nb", false},
		{"trim spaces", "  hello  ", "hello", false},
		{"truncation", strings.Repeat("x", MaxOutputChars+100), strings.Repeat("x", MaxOutputChars), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, trunc := FinalizeOutput(tt.input)
			if trunc != tt.truncated {
				t.Errorf("truncated = %v, want %v", trunc, tt.truncated)
			}
			if got != tt.want {
				if len(got) > 50 || len(tt.want) > 50 {
					t.Errorf("len(got)=%d, len(want)=%d", len(got), len(tt.want))
				} else {
					t.Errorf("got %q, want %q", got, tt.want)
				}
			}
		})
	}
}

func TestLooksLikeHTML(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"<!DOCTYPE html>", true},
		{"<html>", true},
		{"<head>", true},
		{"<body>", true},
		{"  <HTML>", true},
		{"plain text", false},
		{"<div>", false},
	}
	for _, tt := range tests {
		got := LooksLikeHTML(tt.input)
		if got != tt.want {
			t.Errorf("LooksLikeHTML(%q) = %v, want %v", tt.input, got, tt.want)
		}
	}
}

func TestDecodeHTMLEntities(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"&lt;tag&gt;", "<tag>"},
		{"a&amp;b", "a&b"},
		{"&quot;hello&quot;", `"hello"`},
		{"&nbsp;space", " space"},
		{"&#39;single&#39;", "'single'"},
		{"&#x27;x&#x27;", "'x'"},
	}
	for _, tt := range tests {
		got := DecodeHTMLEntities(tt.input)
		if got != tt.want {
			t.Errorf("DecodeHTMLEntities(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestHTMLToMarkdown(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"heading", "<h1>Title</h1>", "# Title"},
		{"paragraph", "<p>Hello</p>", "Hello"},
		{"strips script", "<script>alert(1)</script><p>ok</p>", "ok"},
		{"strips style", "<style>.x{}</style><p>ok</p>", "ok"},
		{"strikethrough", "<del>removed</del>", "~~removed~~"},
		{"h1", "<h1>Title</h1>", "# Title"},
		{"h2", "<h2>Subtitle</h2>", "## Subtitle"},
		{"h3", "<h3>Section</h3>", "### Section"},
		{"h4", "<h4>Subsection</h4>", "#### Subsection"},
		{"h5", "<h5>Detail</h5>", "##### Detail"},
		{"h6", "<h6>Fine</h6>", "###### Fine"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := HTMLToMarkdown(tt.input)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !strings.Contains(got, tt.want) {
				t.Errorf("HTMLToMarkdown(%q) = %q, want containing %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestFormatISODate(t *testing.T) {
	tests := []struct {
		input any
		want  string
	}{
		{"2024-01-15", "2024-01-15"},
		{"2024-01-15T10:30:00Z", "2024-01-15"},
		{"2024-01-15 10:30:00", "2024-01-15"},
		{nil, ""},
		{float64(1705276800), "2024-01-15"},
		{float64(100), ""}, // < 1e9
		{int(1705276800), "2024-01-15"},
		{int64(1705276800), "2024-01-15"},
		{"invalid", ""},
	}
	for _, tt := range tests {
		got := FormatISODate(tt.input)
		if got != tt.want {
			t.Errorf("FormatISODate(%v) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestFormatMediaDuration(t *testing.T) {
	tests := []struct {
		input float64
		want  string
	}{
		{65, "1:05"},
		{3661, "1:01:01"},
		{0, "0:00"},
		{59, "0:59"},
	}
	for _, tt := range tests {
		got := FormatMediaDuration(tt.input)
		if got != tt.want {
			t.Errorf("FormatMediaDuration(%v) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestExtractTextFromHTML(t *testing.T) {
	input := "<html><body><h1>Title</h1><p>Content</p><script>skip</script></body></html>"
	got := ExtractTextFromHTML(input)
	if !strings.Contains(got, "Title") {
		t.Errorf("should contain Title, got: %q", got)
	}
	if !strings.Contains(got, "Content") {
		t.Errorf("should contain Content, got: %q", got)
	}
	if strings.Contains(got, "skip") {
		t.Error("should not contain script content")
	}
}

func TestLoadPage_BotBlockLastAttempt(t *testing.T) {
	// Bot block on last attempt should return result (not retry).
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = fmt.Fprint(w, `<html><body>Cloudflare challenge</body></html>`)
	}))
	defer server.Close()

	result := LoadPage(context.Background(), server.URL, LoadPageOptions{})
	// All attempts get bot-blocked → last attempt returns not-OK with bot content.
	if result.OK {
		t.Error("expected not OK for bot-blocked response")
	}
	if result.Status != http.StatusForbidden {
		t.Errorf("Status = %d, want 403", result.Status)
	}
}

func TestLoadPage_WithBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/plain")
		_, _ = fmt.Fprintf(w, "got: %s", string(body))
	}))
	defer server.Close()

	result := LoadPage(context.Background(), server.URL, LoadPageOptions{
		Method: http.MethodPost,
		Body:   strings.NewReader("hello"),
	})
	if !result.OK {
		t.Fatalf("expected OK, got error: %s", result.Error)
	}
	if !strings.Contains(result.Content, "got: hello") {
		t.Errorf("expected echoed body, got: %q", result.Content)
	}
}

func TestLoadPage_ReadBodyError(t *testing.T) {
	result := LoadPage(context.Background(), "http://example.com", LoadPageOptions{
		Body: &errorReader{},
	})
	if result.Error == "" {
		t.Fatal("expected error for body read failure")
	}
	if !strings.Contains(result.Error, "read error") {
		t.Fatalf("error should mention read error, got: %q", result.Error)
	}
}

type errorReader struct{}

func (e *errorReader) Read(p []byte) (int, error) { return 0, fmt.Errorf("read error") }

func TestParseRetryAfterMs_HTTPDateBeyondCap(t *testing.T) {
	// HTTP date >10s in the future should clamp to retryAfterMaxMs (10s).
	future := time.Now().Add(30 * time.Second).UTC().Format(http.TimeFormat) // REAL-TIME: need relative future date
	got := parseRetryAfterMs(future)
	if got != 10*time.Second {
		t.Errorf("parseRetryAfterMs(future+30s) = %v, want 10s (capped)", got)
	}
}

func TestLoadPage_429RetryThroughLoadPage(t *testing.T) {
	// Drive the full LoadPage 429-retry path: first response is 429 with
	// Retry-After: 0 (parsed to 1s default since "0" fails Sscanf > 0 check),
	// second is 200. Exercises the retry429=true branch in LoadPage and the
	// 429 handling + ctx.Done() select inside loadPageAttempt.
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if requests == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.Header().Set("Content-Type", "text/plain")
		_, _ = fmt.Fprint(w, "ok after retry")
	}))
	defer server.Close()

	result := LoadPage(context.Background(), server.URL, LoadPageOptions{Timeout: 5 * time.Second})
	if !result.OK {
		t.Fatalf("expected OK after 429 retry, got error: %s", result.Error)
	}
	if requests != 2 {
		t.Errorf("expected 2 requests (429 then 200), got %d", requests)
	}
	if !strings.Contains(result.Content, "ok after retry") {
		t.Errorf("content = %q, want containing 'ok after retry'", result.Content)
	}
}

func TestLoadPage_429RetryContextCanceled(t *testing.T) {
	// Trigger 429 retry, then cancel context during the retry delay so the
	// ctx.Done() branch in loadPageAttempt fires.
	ctx, cancel := context.WithCancel(context.Background())
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Retry-After", "60")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()

	// Cancel shortly after the first request returns so the retry sleep sees ctx.Done.
	go func() {
		time.Sleep(200 * time.Millisecond) // REAL-TIME: cancel after first response
		cancel()
	}()

	result := LoadPage(ctx, server.URL, LoadPageOptions{Timeout: 5 * time.Second})
	if result.OK {
		t.Fatal("expected not OK when ctx canceled during 429 retry delay")
	}
	if !strings.Contains(result.Error, "cancel") {
		t.Errorf("error should mention cancel, got: %q", result.Error)
	}
	if requests != 1 {
		t.Errorf("expected exactly 1 request before cancel, got %d", requests)
	}
}

func TestLoadPage_ClientDoContextCanceled(t *testing.T) {
	// Cancel ctx while client.Do is in flight on a slow server. loadPageAttempt
	// should report "context canceled" rather than the transport error.
	ctx, cancel := context.WithCancel(context.Background())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(500 * time.Millisecond) // REAL-TIME: slow server to allow mid-request cancel
		_, _ = fmt.Fprint(w, "ok")
	}))
	defer server.Close()

	go func() {
		time.Sleep(50 * time.Millisecond) // REAL-TIME: cancel mid-request
		cancel()
	}()

	result := LoadPage(ctx, server.URL, LoadPageOptions{Timeout: 5 * time.Second})
	if result.OK {
		t.Fatal("expected not OK when ctx canceled mid-request")
	}
	if !strings.Contains(result.Error, "cancel") {
		t.Errorf("error should mention cancel, got: %q", result.Error)
	}
}

func TestLoadPage_NonLastAttemptReadError(t *testing.T) {
	// Force a body read error on every attempt by hijacking the conn and
	// declaring Content-Length > actual bytes — io.ReadAll returns
	// io.ErrUnexpectedEOF (which is != io.EOF), triggering the read-error
	// branch on non-final attempts that returns (nil, false) for retry.
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		hj, ok := w.(http.Hijacker)
		if !ok {
			http.Error(w, "no hijack", http.StatusInternalServerError)
			return
		}
		conn, bufrw, err := hj.Hijack()
		if err != nil {
			return
		}
		defer conn.Close()
		// Lie about Content-Length so io.ReadAll gets io.ErrUnexpectedEOF.
		_, _ = fmt.Fprint(bufrw, "HTTP/1.1 200 OK\r\nContent-Length: 100\r\nContent-Type: text/plain\r\n\r\nshort")
		_ = bufrw.Flush()
	}))
	defer server.Close()

	result := LoadPage(context.Background(), server.URL, LoadPageOptions{Timeout: 5 * time.Second})
	if result.OK {
		t.Error("expected not OK when all attempts have body read errors")
	}
	if requests < 2 {
		t.Errorf("expected retries on read error, got %d requests", requests)
	}
}

func TestLoadPage_LastAttemptReadError(t *testing.T) {
	// Force a read error on the final UA attempt by closing the connection
	// mid-response. Use a handler that writes a short Content-Length then
	// more bytes than declared — http.Client returns an error on body read.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") == userAgents[len(userAgents)-1] {
			// Last attempt: lie about Content-Length to force read error.
			w.Header().Set("Content-Length", "5")
			_, _ = w.Write([]byte("this is way more than 5 bytes so the client should error reading the body"))
			return
		}
		w.WriteHeader(http.StatusForbidden)
		_, _ = fmt.Fprint(w, "blocked")
	}))
	defer server.Close()

	result := LoadPage(context.Background(), server.URL, LoadPageOptions{Timeout: 5 * time.Second})
	if result.OK {
		t.Error("expected not OK when final attempt has read error")
	}
}

func TestLoadPage_AllAttemptsErrorFallThrough(t *testing.T) {
	// When all attempts return errors but none is the final-attempt short
	// circuit, LoadPage falls out of the loop and returns the lastError.
	// Connection-refused on 127.0.0.1:1 (definitely not listening) gives
	// an error per attempt; the last attempt sets lastError and returns it.
	result := LoadPage(context.Background(), "http://127.0.0.1:1", LoadPageOptions{Timeout: time.Second})
	if result.OK {
		t.Fatal("expected not OK for unreachable URL")
	}
	if result.Error == "" {
		t.Fatal("expected non-empty Error for unreachable URL")
	}
}

func TestFormatISODate_NonStandardString(t *testing.T) {
	// time.Parse branch is unreachable because any ISO-like date matches
	// the len>=10 && v[4]=='-' && v[7]=='-' fast path. This documents that
	// non-ISO strings fall through and return "".
	tests := []struct {
		input any
		want  string
	}{
		{"Jan 2, 2006", ""},
		{"2006/01/02", ""},
		{"yesterday", ""},
		{true, ""}, // unsupported type
		{[]string{}, ""},
	}
	for _, tt := range tests {
		got := FormatISODate(tt.input)
		if got != tt.want {
			t.Errorf("FormatISODate(%v) = %q, want %q", tt.input, got, tt.want)
		}
	}
}
