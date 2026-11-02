package scrapers

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const wikiSummaryJSON = `{"title":"Tea Party","description":"Political movement",` +
	`"extract":"The Tea Party movement is an American political movement."}`

const wikiContentHTML = `<body>` +
	`<section><h2>History</h2><p>A paragraph long enough to pass the filter.</p></section>` +
	`<section><h3>Legacy</h3><p>Another paragraph that is definitely longer than twenty characters.</p></section>` +
	`<section><h2>References</h2><p>This paragraph is hidden because the heading is skipped.</p></section>` +
	`</body>`

// recordingTransport captures the escaped path of each outgoing request,
// which the rewriting transports would otherwise decode away.
type recordingTransport struct {
	next http.RoundTripper
	urls *[]string
}

func (t recordingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	*t.urls = append(*t.urls, req.URL.EscapedPath())
	return t.next.RoundTrip(req)
}

// wikiSrv serves the REST summary and mobile-html endpoints, recording paths
// so the escaped title in the URL is pinned.
func wikiSrv(t *testing.T, summary, content string) (*http.Client, *[]string) {
	t.Helper()
	paths := new([]string)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/page/summary/"):
			if summary == "" {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, summary)
		case strings.Contains(r.URL.Path, "/page/mobile-html/"):
			if content == "" {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_, _ = io.WriteString(w, content)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return &http.Client{Transport: recordingTransport{
		next: &rewriteTransport{server: srv, host: "en.wikipedia.org"},
		urls: paths,
	}}, paths
}

func TestHandleWikipedia_FullArticle(t *testing.T) {
	client, paths := wikiSrv(t, wikiSummaryJSON, wikiContentHTML)

	result, err := HandleWikipedia(context.Background(), mustParseURL(t, "https://en.wikipedia.org/wiki/Tea_Party"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	want := "# Tea Party\n\n*Political movement*\n\n" +
		"The Tea Party movement is an American political movement.\n\n---\n\n" +
		"## History\n\nA paragraph long enough to pass the filter.\n\n" +
		"### Legacy\n\nAnother paragraph that is definitely longer than twenty characters.\n\n"
	if result.Content != want {
		t.Errorf("Content = %q, want %q", result.Content, want)
	}
	if result.ContentType != "text/markdown" {
		t.Errorf("ContentType = %q, want text/markdown", result.ContentType)
	}
	if result.Method != "wikipedia-api" {
		t.Errorf("Method = %q, want wikipedia-api", result.Method)
	}
	if len(result.Notes) != 1 || result.Notes[0] != "Fetched via Wikipedia API" {
		t.Errorf("Notes = %q", result.Notes)
	}
	wantPaths := []string{
		"/api/rest_v1/page/summary/Tea%20Party",
		"/api/rest_v1/page/mobile-html/Tea%20Party",
	}
	if strings.Join(*paths, ",") != strings.Join(wantPaths, ",") {
		t.Errorf("paths = %q, want %q", *paths, wantPaths)
	}
}

func TestHandleWikipedia_SummaryOnlyWhenContentMissing(t *testing.T) {
	client, _ := wikiSrv(t, wikiSummaryJSON, "")

	result, err := HandleWikipedia(context.Background(), mustParseURL(t, "https://en.wikipedia.org/wiki/Tea_Party"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	want := "# Tea Party\n\n*Political movement*\n\n" +
		"The Tea Party movement is an American political movement.\n\n---\n\n"
	if result.Content != want {
		t.Errorf("Content = %q, want %q", result.Content, want)
	}
	if len(result.Notes) != 1 || result.Notes[0] != "Fetched via Wikipedia API (summary only)" {
		t.Errorf("Notes = %q, want [Fetched via Wikipedia API (summary only)]", result.Notes)
	}
	if result.ContentType != "text/markdown" || result.Method != "wikipedia-api" {
		t.Errorf("ContentType/Method = %q/%q", result.ContentType, result.Method)
	}
}

func TestHandleWikipedia_SummaryWithoutDescription(t *testing.T) {
	client, _ := wikiSrv(t, `{"title":"T","description":"","extract":"Extract text."}`, "")
	result, err := HandleWikipedia(context.Background(), mustParseURL(t, "https://en.wikipedia.org/wiki/T"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	want := "# T\n\nExtract text.\n\n---\n\n"
	if result.Content != want {
		t.Errorf("Content = %q, want %q", result.Content, want)
	}
}

func TestHandleWikipedia_SummaryFetchFailure(t *testing.T) {
	client, _ := wikiSrv(t, "", "")
	result, err := HandleWikipedia(context.Background(), mustParseURL(t, "https://en.wikipedia.org/wiki/Tea_Party"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result != nil {
		t.Errorf("expected nil when the summary is unavailable, got %+v", result)
	}
}

func TestHandleWikipedia_SummaryNotJSON(t *testing.T) {
	client, _ := wikiSrv(t, `5`, "")
	result, err := HandleWikipedia(context.Background(), mustParseURL(t, "https://en.wikipedia.org/wiki/Tea_Party"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result != nil {
		t.Errorf("expected nil for non-object summary, got %+v", result)
	}
}

// A host with no language subdomain is not an article URL.
func TestHandleWikipedia_NoLanguageSubdomain(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, wikiSummaryJSON)
	}))
	defer srv.Close()

	client := &http.Client{Transport: &rewriteTransport{server: srv, host: ".wikipedia.org"}}
	result, err := HandleWikipedia(context.Background(), mustParseURL(t, "https://.wikipedia.org/wiki/Tea"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result != nil {
		t.Errorf("expected nil without a language subdomain, got %+v", result)
	}
}

func TestHandleWikipedia_HostAndPathVariants(t *testing.T) {
	client, _ := wikiSrv(t, wikiSummaryJSON, wikiContentHTML)
	for _, raw := range []string{
		"https://wikipedia.org/wiki/Tea",
		"https://en.wikipedia.org/page/Tea",
		"https://example.com/wiki/Tea",
	} {
		result, err := HandleWikipedia(context.Background(), mustParseURL(t, raw), client, nil)
		if err != nil {
			t.Errorf("%s: error = %v", raw, err)
		}
		if result != nil {
			t.Errorf("%s: expected nil, got %+v", raw, result)
		}
	}
}

func TestStripHTMLAndExtract_ParagraphLengthBoundary(t *testing.T) {
	html := `<section><p>12345678901234567890</p><p>123456789012345678901</p></section>`
	got := stripHTMLAndExtract([]byte(html))
	want := "123456789012345678901\n\n"
	if got != want {
		t.Errorf("stripHTMLAndExtract() = %q, want %q", got, want)
	}
}

// Whitespace around a paragraph counts toward neither its content nor its length.
func TestStripHTMLAndExtract_TrimsBeforeLengthCheck(t *testing.T) {
	html := `<section><p>          short para          </p></section>`
	got := stripHTMLAndExtract([]byte(html))
	if got != "" {
		t.Errorf("stripHTMLAndExtract() = %q, want empty", got)
	}
}

func TestStripHTMLAndExtract_SkipsReferenceHeadings(t *testing.T) {
	var b strings.Builder
	for _, h := range []string{"References", "External links", "See also", "Notes", "Further reading", "Sources"} {
		b.WriteString("<section><h2>" + h + "</h2><p>Hidden paragraph that is longer than twenty characters.</p></section>")
	}
	// Prefix matches are skipped as well as exact ones.
	b.WriteString(`<section><h2>External links further</h2><p>Hidden paragraph that is longer than twenty characters.</p></section>`)
	b.WriteString(`<section><h2>History</h2><p>Kept paragraph that is longer than twenty characters.</p></section>`)

	got := stripHTMLAndExtract([]byte(b.String()))
	want := "## History\n\nKept paragraph that is longer than twenty characters.\n\n"
	if got != want {
		t.Errorf("stripHTMLAndExtract() = %q, want %q", got, want)
	}
}

func TestStripHTMLAndExtract_H3HeadingGetsSubLevel(t *testing.T) {
	got := stripHTMLAndExtract([]byte(`<section><h3>Sub</h3><p>A paragraph long enough to pass the filter.</p></section>`))
	want := "### Sub\n\nA paragraph long enough to pass the filter.\n\n"
	if got != want {
		t.Errorf("stripHTMLAndExtract() = %q, want %q", got, want)
	}
}

func TestExtractTagContent(t *testing.T) {
	tests := []struct {
		name string
		html string
		tag  string
		want string
	}{
		{"plain", `<h2>Title</h2>`, "h2", "Title"},
		{"attributes", `<h2 class="x">Title</h2>`, "h2", "Title"},
		{"nested tags stripped", `<h2>A <a href="#x">link</a></h2>`, "h2", "A link"},
		{"empty element", `<h2></h2>`, "h2", ""},
		{"no close tag", `<h2>Title`, "h2", ""},
		{"missing tag", `<p>Title</p>`, "h2", ""},
		// An opening tag with no '>' before the close tag has no content start;
		// slicing past the close tag would be an out-of-range slice.
		{"open tag without close bracket", `<h2 foo</h2>`, "h2", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractTagContent(tt.html, tt.tag)
			if got != tt.want {
				t.Errorf("extractTagContent(%q, %q) = %q, want %q", tt.html, tt.tag, got, tt.want)
			}
		})
	}
}

func TestExtractAllTagContents(t *testing.T) {
	got := extractAllTagContents(`<p>one</p><p>two</p>`, "p")
	want := []string{"one", "two"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("extractAllTagContents() = %q, want %q", got, want)
	}

	// A stray close tag restarts the scan from just after it, so the same
	// paragraph is picked up twice rather than dropped.
	got = extractAllTagContents(`</p><p>hello world this is content</p>`, "p")
	want = []string{"hello world this is content", "hello world this is content"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("extractAllTagContents() = %q, want %q", got, want)
	}

	got = extractAllTagContents(`<p></p><p>real content here now</p>`, "p")
	if len(got) != 0 {
		t.Errorf("extractAllTagContents() = %q, want no entries", got)
	}
}

func TestExtractSections(t *testing.T) {
	got := extractSections(`<section><h2>A</h2></section><section><h2>B</h2></section>`)
	want := []string{"<section><h2>A</h2></section>", "<section><h2>B</h2></section>"}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("extractSections() = %q, want %q", got, want)
	}

	got = extractSections(`<section>unclosed`)
	if len(got) != 0 {
		t.Errorf("extractSections() = %q, want no sections", got)
	}

	got = extractSections(`<body><h2>Only</h2></body>`)
	want = []string{"Only"}
	if len(got) != 1 || got[0] != want[0] {
		t.Errorf("extractSections() = %q, want %q", got, want)
	}

	// A section tag that never opens properly still terminates cleanly.
	got = extractSections(`<section</section>x`)
	want = []string{"<section</section>"}
	if len(got) != 1 || got[0] != want[0] {
		t.Errorf("extractSections() = %q, want %q", got, want)
	}
}

func TestHandleWikipedia_SummaryAPIFailureDropsArticle(t *testing.T) {
	client, _ := wikiSrv(t, "", wikiContentHTML)
	result, err := HandleWikipedia(context.Background(), mustParseURL(t, "https://en.wikipedia.org/wiki/Tea_Party"), client, nil)
	if err != nil {
		t.Fatalf("HandleWikipedia() error = %v", err)
	}
	if result != nil {
		t.Errorf("result = %+v, want nil when the summary API fails", result)
	}
}
