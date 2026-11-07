package web

// Each test pins an exact observable output so the corresponding mutant
// cannot survive.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestFKIsBotBlockedEachKeyword(t *testing.T) {
	// Each keyword alone must trip the bot-block heuristic; killing mutants
	// that drop individual || arms requires content with exactly one marker.
	tests := []struct {
		status  int
		content string
		want    bool
	}{
		{403, "cloudflare", true},
		{503, "captcha", true},
		{503, "challenge", true},
		{403, "blocked", true},
		{403, "access denied", true},
		{503, "bot detection", true},
		{403, "unrelated page", false},
	}
	for _, tt := range tests {
		if got := isBotBlocked(tt.status, tt.content); got != tt.want {
			t.Errorf("isBotBlocked(%d, %q) = %v, want %v", tt.status, tt.content, got, tt.want)
		}
	}
}

func TestFKParseRetryAfterMsBoundaries(t *testing.T) {
	// A date truncated into the current second lands in (-1s, 0]: the d<=0
	// default must win, not the raw negative delta.
	truncNow := time.Now().Truncate(time.Second).UTC().Format(http.TimeFormat) // REAL-TIME: needs a date within the current second
	if got := parseRetryAfterMs(truncNow); got != time.Second {
		t.Errorf("parseRetryAfterMs(truncated now) = %v, want 1s", got)
	}
	// HTTP dates are second-granular, so +5s parses to d in [4s, 5s]; the
	// cap must not fire there.
	future := time.Now().Add(5 * time.Second).UTC().Format(http.TimeFormat) // REAL-TIME: needs a relative future date
	got := parseRetryAfterMs(future)
	if got < 4*time.Second || got > 5*time.Second {
		t.Errorf("parseRetryAfterMs(+5s date) = %v, want [4s, 5s]", got)
	}
}

func TestFKDecodeBodyHeaderCharset(t *testing.T) {
	if got := decodeBody([]byte("caf\xe9"), "text/html; charset=iso-8859-1"); got != "café" {
		t.Errorf("decodeBody iso-8859-1 = %q, want %q", got, "café")
	}
	// Header label utf-8 with an invalid byte must pass through raw, not be
	// re-decoded into U+FFFD.
	if got := decodeBody([]byte("caf\xe9"), "text/html; charset=utf-8"); got != "caf\xe9" {
		t.Errorf("decodeBody utf-8 raw = %q, want %q", got, "caf\xe9")
	}
	// Alias "utf8" gets the same raw passthrough treatment.
	if got := decodeBody([]byte("caf\xe9"), "text/html; charset=utf8"); got != "caf\xe9" {
		t.Errorf("decodeBody utf8 raw = %q, want %q", got, "caf\xe9")
	}
	// A header label must win over a conflicting meta tag.
	if got := decodeBody([]byte("<meta charset=\"gbk\">caf\xe9"), "text/html; charset=utf-8"); got != "<meta charset=\"gbk\">caf\xe9" {
		t.Errorf("decodeBody header-over-meta = %q, want raw passthrough", got)
	}
}

func TestFKDecodeBodyMetaCharsetExact(t *testing.T) {
	in := "<meta charset=\"iso-8859-1\">caf\xe9"
	if got := decodeBody([]byte(in), "text/html"); got != "<meta charset=\"iso-8859-1\">café" {
		t.Errorf("decodeBody meta sniff = %q, want decoded é", got)
	}
}

func TestFKDecodeBodyMetaSniffWindow(t *testing.T) {
	// The sniff window cut must land inside the charset value, not after the
	// tag: the regex needs no closing '>', so positions are value-relative.
	// iso-8859-1 value occupies tag bytes [15, 25).
	isoTag := `<meta charset="iso-8859-1">`
	// Value ending exactly at 2048: fully inside the window, must decode.
	data := []byte(strings.Repeat("a", 2048-25) + isoTag + "\xe9")
	if got := decodeBody(data, "text/html"); !strings.HasSuffix(got, "é") {
		t.Errorf("value ending at 2048: got %q, want decoded é suffix", got[len(got)-20:])
	}
	// Tag fully past the window: no decode, raw passthrough.
	data = []byte(strings.Repeat("a", 2100) + isoTag + "\xe9")
	if got := decodeBody(data, "text/html"); got != string(data) {
		t.Errorf("meta past 2048: got %q, want raw", got[:40])
	}
	// gbk value occupies tag bytes [15, 18); ending exactly at 2049 leaves
	// "gb" in the 2048 window (invalid label → raw) but "gbk" in 2049.
	gbkTag := `<meta charset="gbk">`
	data = []byte(strings.Repeat("a", 2049-18) + gbkTag + "\xe9")
	if got := decodeBody(data, "text/html"); got != string(data) {
		t.Errorf("value ending at 2049: got %q, want raw", got[:40])
	}
}

func TestFKLoadPageDefaultDeadline(t *testing.T) {
	// The 20s default timeout is observable through the request context
	// deadline without waiting for a slow server.
	var deadline time.Time
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		d, ok := r.Context().Deadline()
		if !ok {
			t.Error("request context has no deadline")
		}
		deadline = d
		return cannedResponse(r, 200, "ok"), nil
	})}
	start := time.Now() // REAL-TIME: measuring the default deadline window
	res := LoadPage(context.Background(), "http://example.com/", LoadPageOptions{Client: client})
	if !res.OK {
		t.Fatalf("expected OK, got error: %s", res.Error)
	}
	slop := deadline.Sub(start)
	if slop < 19*time.Second+500*time.Millisecond || slop > 20*time.Second+500*time.Millisecond {
		t.Errorf("default deadline = %v, want ~20s", slop)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func cannedResponse(r *http.Request, status int, body string) *http.Response {
	return &http.Response{
		Status:     fmt.Sprintf("%d canned", status),
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     http.Header{"Content-Type": []string{"text/plain"}},
		Request:    r,
	}
}

func TestFKLoadPageRequestHeaders(t *testing.T) {
	var gotAccept, gotLang, gotEnc string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAccept = r.Header.Get("Accept")
		gotLang = r.Header.Get("Accept-Language")
		gotEnc = r.Header.Get("Accept-Encoding")
		_, _ = fmt.Fprint(w, "ok")
	}))
	defer server.Close()

	res := LoadPage(context.Background(), server.URL, LoadPageOptions{})
	if !res.OK {
		t.Fatalf("expected OK, got error: %s", res.Error)
	}
	if want := "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8"; gotAccept != want {
		t.Errorf("Accept = %q, want %q", gotAccept, want)
	}
	if want := "en-US,en;q=0.5"; gotLang != want {
		t.Errorf("Accept-Language = %q, want %q", gotLang, want)
	}
	if want := "identity"; gotEnc != want {
		t.Errorf("Accept-Encoding = %q, want %q", gotEnc, want)
	}
}

func TestFKLoadPageReadBodyErrorFields(t *testing.T) {
	res := LoadPage(context.Background(), "http://example.com/", LoadPageOptions{Body: &errorReader{}})
	if !strings.Contains(res.Error, "read error") {
		t.Fatalf("error = %q, want containing 'read error'", res.Error)
	}
	if res.FinalURL != "http://example.com/" {
		t.Errorf("FinalURL = %q, want requested URL", res.FinalURL)
	}
	if res.Content != "" {
		t.Errorf("Content = %q, want empty", res.Content)
	}
}

func TestFKLoadPageCanceledPreCheck(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	// A canceled context must short-circuit before URL parsing: the error is
	// "context canceled", not a URL parse failure.
	res := LoadPage(ctx, "http://[::1]:namedport", LoadPageOptions{})
	if res.Error != "context canceled" {
		t.Errorf("error = %q, want %q", res.Error, "context canceled")
	}
	if res.FinalURL != "http://[::1]:namedport" {
		t.Errorf("FinalURL = %q, want requested URL", res.FinalURL)
	}

	res = LoadPage(ctx, "http://example.com/x", LoadPageOptions{})
	if res.Error != "context canceled" {
		t.Errorf("error = %q, want %q", res.Error, "context canceled")
	}
	if res.FinalURL != "http://example.com/x" {
		t.Errorf("FinalURL = %q, want requested URL", res.FinalURL)
	}
}

func TestFKLoadPageDoErrorCanceledExact(t *testing.T) {
	// The Do-error + canceled-context path returns the literal
	// "context canceled", never the wrapped transport error.
	ctx, cancel := context.WithCancel(context.Background())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(500 * time.Millisecond) // REAL-TIME: cancel fires mid-request
		_, _ = fmt.Fprint(w, "ok")
	}))
	defer server.Close()

	go func() {
		time.Sleep(50 * time.Millisecond) // REAL-TIME: cancel mid-flight
		cancel()
	}()

	res := LoadPage(ctx, server.URL, LoadPageOptions{Timeout: 5 * time.Second})
	if res.OK {
		t.Fatal("expected not OK when canceled mid-request")
	}
	if res.Error != "context canceled" {
		t.Errorf("error = %q, want %q", res.Error, "context canceled")
	}
	if res.FinalURL != server.URL {
		t.Errorf("FinalURL = %q, want %q", res.FinalURL, server.URL)
	}
}

func TestFKLoadPageDoErrorTripCount(t *testing.T) {
	boom := errors.New("boom")
	trips := 0
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		trips++
		return nil, boom
	})}
	res := LoadPage(context.Background(), "http://example.com/x", LoadPageOptions{Client: client})
	if res.OK {
		t.Fatal("expected not OK for transport errors")
	}
	// Literal 3, not len(userAgents): mutating the array length must not
	// move the assertion with it.
	if trips != 3 {
		t.Errorf("trips = %d, want 3 (one per UA attempt)", trips)
	}
	if !strings.Contains(res.Error, "boom") {
		t.Errorf("error = %q, want containing 'boom'", res.Error)
	}
	if res.FinalURL != "http://example.com/x" {
		t.Errorf("FinalURL = %q, want requested URL", res.FinalURL)
	}
}

func TestFKLoadPage429RetrySameAttempt(t *testing.T) {
	// The 429 retry repeats the SAME attempt (same User-Agent); a ctx
	// deadline bounds mutants that would retry forever.
	ctx, cancel := context.WithTimeout(context.Background(), 2500*time.Millisecond)
	defer cancel()

	var uas []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		uas = append(uas, r.Header.Get("User-Agent"))
		w.Header().Set("Retry-After", "1")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()

	res := LoadPage(ctx, server.URL, LoadPageOptions{Timeout: 5 * time.Second})
	if res.OK {
		t.Fatal("expected not OK when server always 429s")
	}
	if res.Status != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429 (error: %s)", res.Status, res.Error)
	}
	if len(uas) != 2 {
		t.Errorf("requests = %d, want exactly 2 (429 then final 429)", len(uas))
	}
	for i, ua := range uas {
		if ua != userAgents[0] {
			t.Errorf("request %d User-Agent = %q, want %q (retry must not burn a UA)", i, ua, userAgents[0])
		}
	}
}

func TestFKLoadPageNon429NoRetry(t *testing.T) {
	// Only 429 enters the retry path; a 500 must come back in one request.
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = fmt.Fprint(w, "boom")
	}))
	defer server.Close()

	res := LoadPage(context.Background(), server.URL, LoadPageOptions{})
	if res.OK {
		t.Fatal("expected not OK for 500")
	}
	if res.Status != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", res.Status)
	}
	if requests != 1 {
		t.Errorf("requests = %d, want 1", requests)
	}
}

func TestFKLoadPageReadErrorFields(t *testing.T) {
	// Content-Length lie forces io.ErrUnexpectedEOF on every attempt; the
	// final attempt's result must carry the content type, final URL and the
	// read error verbatim.
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
		_, _ = fmt.Fprint(bufrw, "HTTP/1.1 200 OK\r\nContent-Length: 100\r\nContent-Type: text/plain\r\n\r\nshort")
		_ = bufrw.Flush()
	}))
	defer server.Close()

	res := LoadPage(context.Background(), server.URL, LoadPageOptions{Timeout: 5 * time.Second})
	if res.OK {
		t.Error("expected not OK when all attempts hit read errors")
	}
	if requests != 3 {
		t.Errorf("requests = %d, want 3", requests)
	}
	if !strings.Contains(res.Error, "unexpected EOF") {
		t.Errorf("error = %q, want containing 'unexpected EOF'", res.Error)
	}
	if res.ContentType != "text/plain" {
		t.Errorf("ContentType = %q, want text/plain", res.ContentType)
	}
	if res.FinalURL != server.URL {
		t.Errorf("FinalURL = %q, want %q", res.FinalURL, server.URL)
	}
}

func TestFKLoadPageExactMaxBytesNotTruncated(t *testing.T) {
	payload := strings.Repeat("x", 1000)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, payload)
	}))
	defer server.Close()

	res := LoadPage(context.Background(), server.URL, LoadPageOptions{MaxBytes: 1000})
	if !res.OK {
		t.Fatalf("expected OK, got error: %s", res.Error)
	}
	if res.Truncated {
		t.Error("exactly-at-limit body must not be flagged truncated")
	}
	if res.Content != payload {
		t.Errorf("content length = %d, want %d", len(res.Content), len(payload))
	}
}

func TestFKLoadPageStatusOKBoundaries(t *testing.T) {
	// OK is [200, 300): pin 199, 299, 300 and 304 through a canned transport.
	tests := []struct {
		status int
		wantOK bool
	}{
		{199, false},
		{299, true},
		{300, false},
		{304, false},
	}
	for _, tt := range tests {
		client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			return cannedResponse(r, tt.status, "body"), nil
		})}
		res := LoadPage(context.Background(), "http://example.com/", LoadPageOptions{Client: client})
		if res.OK != tt.wantOK {
			t.Errorf("status %d: OK = %v, want %v", tt.status, res.OK, tt.wantOK)
		}
		if res.Status != tt.status {
			t.Errorf("status %d: reported %d", tt.status, res.Status)
		}
	}
}

func TestFKLoadPageBadURLFields(t *testing.T) {
	res := LoadPage(context.Background(), "http://[::1]:namedport", LoadPageOptions{})
	if res.OK {
		t.Fatal("expected not OK for invalid URL")
	}
	if res.FinalURL != "http://[::1]:namedport" {
		t.Errorf("FinalURL = %q, want requested URL", res.FinalURL)
	}
}

func TestFKLoadPageContentTypeNormalization(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Uppercase media type with whitespace before the parameter must be
		// normalized to a bare lowercase media type.
		w.Header().Set("Content-Type", "TEXT/HTML ; charset=utf-8")
		_, _ = fmt.Fprint(w, "ok")
	}))
	defer server.Close()

	res := LoadPage(context.Background(), server.URL, LoadPageOptions{})
	if !res.OK {
		t.Fatalf("expected OK, got error: %s", res.Error)
	}
	if res.ContentType != "text/html" {
		t.Errorf("ContentType = %q, want text/html", res.ContentType)
	}
}

func TestFKFinalizeOutputExactBoundary(t *testing.T) {
	in := strings.Repeat("x", MaxOutputChars)
	got, truncated := FinalizeOutput(in)
	if truncated {
		t.Error("input exactly at MaxOutputChars must not be truncated")
	}
	if got != in {
		t.Errorf("content len = %d, want %d", len(got), len(in))
	}
}

func TestFKHTMLToMarkdownHeadingExact(t *testing.T) {
	// The custom heading renderer writes a bare prefix line ahead of the
	// commonmark rendering, so each level produces an exact doubled form.
	tests := []struct{ in, want string }{
		{"<h1>T1</h1>", "# \n\n# T1"},
		{"<h2>T2</h2>", "## \n\n## T2"},
		{"<h3>T3</h3>", "### \n\n### T3"},
		{"<h4>T4</h4>", "#### \n\n#### T4"},
		{"<h5>T5</h5>", "##### \n\n##### T5"},
		{"<h6>T6</h6>", "###### \n\n###### T6"},
		{"<h1>a</h1><p>b</p>", "# \n\n# a\n\nb"},
	}
	for _, tt := range tests {
		got, err := HTMLToMarkdown(tt.in)
		if err != nil {
			t.Fatalf("HTMLToMarkdown(%q) error: %v", tt.in, err)
		}
		if got != tt.want {
			t.Errorf("HTMLToMarkdown(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestFKHTMLToMarkdownDeepNestingError(t *testing.T) {
	// x/net/html panics once the open-element stack passes 512 nodes and its
	// Parse recovers that panic into a returned error, so plain string input
	// CAN drive ConvertString to fail — the wrap must fire exactly once.
	deep := strings.Repeat("<div>", 600) + "payload" + strings.Repeat("</div>", 600)
	md, err := HTMLToMarkdown(deep)
	if err == nil {
		t.Fatalf("HTMLToMarkdown(600-deep) err = nil, want open-stack error (got md len %d)", len(md))
	}
	const want = "html→markdown: html: open stack of elements exceeds 512 nodes"
	if err.Error() != want {
		t.Errorf("HTMLToMarkdown(600-deep) err = %q, want %q", err.Error(), want)
	}
	if errors.Unwrap(err) == nil {
		t.Error("HTMLToMarkdown(600-deep) err does not wrap its cause; want %w wrap")
	}
	if md != "" {
		t.Errorf("HTMLToMarkdown(600-deep) md = %d chars, want empty on error", len(md))
	}
}

func TestFKExtractTextDeepNestingRaw(t *testing.T) {
	// Same >512-depth input through ExtractTextFromHTML: the parse-error
	// guard must pass the raw string through untouched.
	deep := strings.Repeat("<div>", 600) + "payload" + strings.Repeat("</div>", 600)
	if got := ExtractTextFromHTML(deep); got != deep {
		t.Errorf("ExtractTextFromHTML(600-deep) = %d chars (%q…), want raw passthrough of %d chars",
			len(got), clip(got, 40), len(deep))
	}
}

func clip(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

func TestFKFormatISODateFastPath(t *testing.T) {
	tests := []struct {
		input any
		want  string
	}{
		// 10+ chars with dashes at 4 and 7 returns the raw 10-byte prefix,
		// no calendar validation. "abcd-ef-gh" is exactly 10 bytes, pinning
		// the >= boundary.
		{"abcd-ef-gh", "abcd-ef-gh"},
		{"abcd-ef-ghi", "abcd-ef-gh"},
		// v[4] or v[7] missing the dash falls to the layout loop, which
		// cannot parse these, so "".
		{"abcde67-90", ""},
		{"abcd-ef8ghi", ""},
		{"abcd-ef-g", ""},
	}
	for _, tt := range tests {
		if got := FormatISODate(tt.input); got != tt.want {
			t.Errorf("FormatISODate(%v) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestFKFormatISODateUnixNanos(t *testing.T) {
	// Local midnight flips to the previous day when the nanosecond argument
	// is nudged to -1, and midnight-1s flips forward with +1.
	mid := time.Date(2024, 1, 15, 0, 0, 0, 0, time.Local).Unix()
	tests := []struct {
		input any
		want  string
	}{
		{mid, "2024-01-15"},
		{mid - 1, "2024-01-14"},
		{int(mid), "2024-01-15"},
		{int(mid - 1), "2024-01-14"},
		{float64(mid), "2024-01-15"},
		{float64(mid - 1), "2024-01-14"},
	}
	for _, tt := range tests {
		if got := FormatISODate(tt.input); got != tt.want {
			t.Errorf("FormatISODate(%v) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestFKFormatISODateEpochBoundary(t *testing.T) {
	// float64 seconds below 1e9 are "not a unix timestamp" and return "".
	tests := []struct {
		input any
		want  string
	}{
		{float64(1e9), ""},
		{float64(1000000001), time.Unix(1000000001, 0).Format("2006-01-02")},
	}
	for _, tt := range tests {
		if got := FormatISODate(tt.input); got != tt.want {
			t.Errorf("FormatISODate(%v) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestFKFormatMediaDurationBoundaries(t *testing.T) {
	tests := []struct {
		input float64
		want  string
	}{
		{3600, "1:00:00"},
		{3661, "1:01:01"},
		{7199, "1:59:59"},
		{7200, "2:00:00"},
		{7260, "2:01:00"},
		{61, "1:01"},
		{62, "1:02"},
	}
	for _, tt := range tests {
		if got := FormatMediaDuration(tt.input); got != tt.want {
			t.Errorf("FormatMediaDuration(%v) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestFKBuildResultFallbacks(t *testing.T) {
	res := BuildResult("hello", FetchResultOptions{
		URL:       "http://a/f",
		Method:    "POST",
		FetchedAt: "2024-01-15T00:00:00Z",
	})
	if res.FinalURL != "http://a/f" {
		t.Errorf("FinalURL = %q, want URL fallback", res.FinalURL)
	}
	if res.ContentType != "text/markdown" {
		t.Errorf("ContentType = %q, want text/markdown default", res.ContentType)
	}
	if res.Notes == nil || len(res.Notes) != 0 {
		t.Errorf("Notes = %v, want non-nil empty slice", res.Notes)
	}
	if res.Method != "POST" {
		t.Errorf("Method = %q, want POST", res.Method)
	}
	if res.FetchedAt != "2024-01-15T00:00:00Z" {
		t.Errorf("FetchedAt = %q, want passthrough", res.FetchedAt)
	}
	if res.Content != "hello" || res.Truncated {
		t.Errorf("Content = %q truncated = %v, want hello/false", res.Content, res.Truncated)
	}

	res = BuildResult("x", FetchResultOptions{
		URL:         "http://a/f",
		FinalURL:    "http://b/f",
		ContentType: "application/pdf",
		Notes:       []string{"n"},
	})
	if res.FinalURL != "http://b/f" || res.ContentType != "application/pdf" || len(res.Notes) != 1 || res.Notes[0] != "n" {
		t.Errorf("explicit fields not preserved: %+v", res)
	}
}

func TestFKExtractTextStyleSkipped(t *testing.T) {
	got := ExtractTextFromHTML("<html><body><p>visible</p><style>body{color:red}</style></body></html>")
	if got != "visible" {
		t.Errorf("ExtractTextFromHTML = %q, want %q (style content must be skipped)", got, "visible")
	}
}

func TestFKDecodeBodySniffLenBoundary(t *testing.T) {
	// Total body length exactly 2049 with the gbk value ending at byte 2049:
	// the 2048 cut sees only "gb" (invalid label → raw), any wider window
	// would complete "gbk" and decode the leading \xe9 away.
	data := []byte("\xe9" + strings.Repeat("a", 2030) + `<meta charset="gbk`)
	if len(data) != 2049 {
		t.Fatalf("probe length = %d, want 2049", len(data))
	}
	if got := decodeBody(data, "text/html"); got != string(data) {
		t.Errorf("decodeBody len-2049 = %q…, want raw", got[:40])
	}
}

func TestFKLoadPageDefaultMaxBytes(t *testing.T) {
	// Literal 50*1024*1024, not the MaxBytes constant: the default limit is
	// pinned at exactly 50 MiB, so at-limit bodies are whole and one byte
	// over is truncated.
	atLimit := bytes.Repeat([]byte("x"), 50*1024*1024)
	run := func(body []byte) LoadPageResult {
		client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			return &http.Response{
				Status:     "200 canned",
				StatusCode: 200,
				Body:       io.NopCloser(bytes.NewReader(body)),
				Header:     http.Header{"Content-Type": []string{"text/plain"}},
				Request:    r,
			}, nil
		})}
		return LoadPage(context.Background(), "http://example.com/", LoadPageOptions{Client: client})
	}

	res := run(atLimit)
	if !res.OK {
		t.Fatalf("at-limit: expected OK, got error: %s", res.Error)
	}
	if res.Truncated {
		t.Error("body exactly at 50 MiB must not be truncated")
	}
	if len(res.Content) != 50*1024*1024 {
		t.Errorf("at-limit content len = %d, want %d", len(res.Content), 50*1024*1024)
	}

	res = run(append(append([]byte{}, atLimit...), 'x'))
	if !res.Truncated {
		t.Error("body one byte over 50 MiB must be truncated")
	}
	if len(res.Content) != 50*1024*1024 {
		t.Errorf("over-limit content len = %d, want %d", len(res.Content), 50*1024*1024)
	}
}

func TestFKFinalizeOutputLiteralBoundary(t *testing.T) {
	// Literal 500000, not MaxOutputChars, so the pin survives mutation of
	// the constant itself.
	got, truncated := FinalizeOutput(strings.Repeat("x", 500000))
	if truncated {
		t.Error("input exactly 500000 chars must not be truncated")
	}
	if len(got) != 500000 {
		t.Errorf("content len = %d, want 500000", len(got))
	}
	got, truncated = FinalizeOutput(strings.Repeat("x", 500001))
	if !truncated {
		t.Error("input 500001 chars must be truncated")
	}
	if len(got) != 500000 {
		t.Errorf("content len = %d, want 500000", len(got))
	}
}

func TestFKLoadPage429CancelDuringDelay(t *testing.T) {
	// Canceling inside the Retry-After sleep must surface the canned
	// "context canceled" result with the requested URL.
	ctx, cancel := context.WithCancel(context.Background())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "5")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()

	go func() {
		time.Sleep(300 * time.Millisecond) // REAL-TIME: cancel during delay
		cancel()
	}()

	res := LoadPage(ctx, server.URL, LoadPageOptions{Timeout: 5 * time.Second})
	if res.OK {
		t.Fatal("expected not OK when canceled during 429 delay")
	}
	if res.Error != "context canceled" {
		t.Errorf("error = %q, want %q", res.Error, "context canceled")
	}
	if res.FinalURL != server.URL {
		t.Errorf("FinalURL = %q, want %q", res.FinalURL, server.URL)
	}
}

func TestFKLoadPage429CancelHonoredPromptly(t *testing.T) {
	// Removing the ctx.Done case from the retry select leaves cancellation
	// ignored for the whole (capped 10s) delay; the cancel must win instead.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "60") // caps at 10s
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()

	go func() {
		time.Sleep(200 * time.Millisecond) // REAL-TIME: cancel during delay
		cancel()
	}()

	done := make(chan LoadPageResult, 1)
	go func() { done <- LoadPage(ctx, server.URL, LoadPageOptions{Timeout: 5 * time.Second}) }()

	select {
	case res := <-done:
		if res.Error != "context canceled" {
			t.Errorf("error = %q, want %q", res.Error, "context canceled")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("cancel during 429 delay was not honored within 3s")
	}
}
