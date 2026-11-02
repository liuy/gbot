package scrapers

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

// gopkgSrv serves the Go proxy version endpoint and the pkg.go.dev page.
func gopkgSrv(t *testing.T, proxyJSON, pageHTML string) (*http.Client, *[]string) {
	t.Helper()
	paths := new([]string)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*paths = append(*paths, r.URL.Path)
		switch {
		case strings.HasSuffix(r.URL.Path, "/@latest"):
			if proxyJSON == "" {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_, _ = io.WriteString(w, proxyJSON)
		default:
			if pageHTML == "" {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_, _ = io.WriteString(w, pageHTML)
		}
	}))
	t.Cleanup(srv.Close)
	return &http.Client{Transport: &allowHostsTransport{server: srv, hosts: []string{"proxy.golang.org", "pkg.go.dev"}}}, paths
}

// gopkgHeader renders UnitHeader-* markers from "name|value" pairs.
func gopkgHeader(fields ...string) string {
	var b strings.Builder
	for _, f := range fields {
		name, value, ok := strings.Cut(f, "|")
		if !ok {
			continue
		}
		b.WriteString(`<span data-test-id="UnitHeader-` + name + `">` + value + `</span>`)
	}
	return b.String()
}

const gopkgReadme = `<div data-test-id="Unit-readmeContent"><h1>Doc Header</h1>` +
	`<p>Synopsis line.</p><p>More detail here.</p>` +
	`<a href="#Func">Func</a><a href="#Type">Type</a><a href="#lower">lower</a></div>`

func TestHandleGoPkg_FullOutput(t *testing.T) {
	client, paths := gopkgSrv(t, `{"Version":"v1.2.3","Time":"2024-01-15T00:00:00Z"}`,
		gopkgHeader("license|MIT", "imports|12", "importedby|345",
			"commitTime|Published: Feb 28, 2026 License: MIT")+gopkgReadme)

	result, err := HandleGoPkg(context.Background(), mustParseURL(t, "https://pkg.go.dev/github.com/x/y"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	want := "# github.com/x/y\n\n" +
		"**Version:** v1.2.3 · Published: 2024-01-15\n" +
		"**License:** MIT\n**Imports:** 12 · Imported by: 345\n**Exported:** Func, Type\n" +
		"\nDoc Header\n\n---\n\n## Documentation\n\nSynopsis line.\n\nMore detail here.\nFuncTypelower\n"
	if result.Content != want {
		t.Errorf("Content = %q, want %q", result.Content, want)
	}
	if result.ContentType != "text/markdown" {
		t.Errorf("ContentType = %q", result.ContentType)
	}
	if result.Method != "gopkg-scraper" {
		t.Errorf("Method = %q", result.Method)
	}
	if result.Notes != nil {
		t.Errorf("Notes = %q, want nil", result.Notes)
	}
	wantPaths := []string{"/github.com/x/y/@latest", "/github.com/x/y"}
	if strings.Join(*paths, ",") != strings.Join(wantPaths, ",") {
		t.Errorf("paths = %q, want %q", *paths, wantPaths)
	}
}

func TestHandleGoPkg_NoProxyNoPage(t *testing.T) {
	client, _ := gopkgSrv(t, "", "")
	result, err := HandleGoPkg(context.Background(), mustParseURL(t, "https://pkg.go.dev/mod"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	if result.Content != "# mod\n\n" {
		t.Errorf("Content = %q, want %q", result.Content, "# mod\n\n")
	}
}

// With no proxy timestamp the commitTime marker supplies the published date.
func TestHandleGoPkg_PublishedFromCommitTime(t *testing.T) {
	client, _ := gopkgSrv(t, `{"Version":"v1.2.3"}`,
		gopkgHeader("commitTime|Published: Feb 28, 2026 License: MIT"))
	result, err := HandleGoPkg(context.Background(), mustParseURL(t, "https://pkg.go.dev/mod"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	want := "# mod\n\n**Version:** v1.2.3 · Published: 28, 2026\n"
	if result.Content != want {
		t.Errorf("Content = %q, want %q", result.Content, want)
	}
}

func TestHandleGoPkg_CommitTimeVariants(t *testing.T) {
	tests := []struct {
		name string
		text string
		want string
	}{
		{"no Published prefix", "Feb 28, 2026 License: MIT", "# mod\n\n"},
		{"no License suffix", "Published: Feb 28, 2026", "# mod\n\n"},
		{"license at start", "License: MIT", "# mod\n\n"},
		{"empty text", "", "# mod\n\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client, _ := gopkgSrv(t, "", gopkgHeader("commitTime|"+tt.text))
			result, err := HandleGoPkg(context.Background(), mustParseURL(t, "https://pkg.go.dev/mod"), client, nil)
			if err != nil {
				t.Fatalf("error = %v", err)
			}
			if result == nil {
				t.Fatal("expected result, got nil")
			}
			if result.Content != tt.want {
				t.Errorf("Content = %q, want %q", result.Content, tt.want)
			}
		})
	}
}

// The proxy timestamp is kept only when it carries a full date.
func TestHandleGoPkg_ProxyTimeLengths(t *testing.T) {
	tests := []struct {
		time string
		want string
	}{
		{"2024-01-15T00:00:00Z", "2024-01-15"},
		{"2024-01-15", "2024-01-15"},
		{"2024-01-1", ""},
		{"", ""},
	}
	for _, tt := range tests {
		t.Run(tt.time, func(t *testing.T) {
			client, _ := gopkgSrv(t, `{"Version":"v1.0.0","Time":"`+tt.time+`"}`, "")
			result, err := HandleGoPkg(context.Background(), mustParseURL(t, "https://pkg.go.dev/mod"), client, nil)
			if err != nil {
				t.Fatalf("error = %v", err)
			}
			if result == nil {
				t.Fatal("expected result, got nil")
			}
			want := "# mod\n\n**Version:** v1.0.0"
			if tt.want != "" {
				want += " · Published: " + tt.want
			}
			want += "\n"
			if result.Content != want {
				t.Errorf("Content = %q, want %q", result.Content, want)
			}
		})
	}
}

func TestHandleGoPkg_ProxyBadJSON(t *testing.T) {
	client, _ := gopkgSrv(t, `not json`, "")
	result, err := HandleGoPkg(context.Background(), mustParseURL(t, "https://pkg.go.dev/mod"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	if result.Content != "# mod\n\n" {
		t.Errorf("Content = %q, want %q", result.Content, "# mod\n\n")
	}
}

func TestHandleGoPkg_HostAndPathGuards(t *testing.T) {
	client, _ := gopkgSrv(t, "", "")
	for _, raw := range []string{"https://example.com/mod", "https://pkg.go.dev/", "https://pkg.go.dev"} {
		result, err := HandleGoPkg(context.Background(), mustParseURL(t, raw), client, nil)
		if err != nil {
			t.Errorf("%s: error = %v", raw, err)
		}
		if result != nil {
			t.Errorf("%s: expected nil, got %+v", raw, result)
		}
	}
}

func TestExtractReadmeContent_MarkerAbsent(t *testing.T) {
	synopsis, readme, exports := extractReadmeContent("<p>nothing here</p>")
	if synopsis != "" || readme != "" || exports != nil {
		t.Errorf("got %q/%q/%q, want all empty", synopsis, readme, exports)
	}
}

func TestExtractReadmeContent_ShortTextWithoutParagraphBreak(t *testing.T) {
	synopsis, readme, exports := extractReadmeContent(`data-test-id="Unit-readmeContent">short text`)
	if synopsis != "short text" {
		t.Errorf("synopsis = %q, want %q", synopsis, "short text")
	}
	if readme != "" {
		t.Errorf("readme = %q, want empty", readme)
	}
	if exports != nil {
		t.Errorf("exports = %q, want nil", exports)
	}
}

// A run with no paragraph break splits at 200 characters.
func TestExtractReadmeContent_SynopsisCap(t *testing.T) {
	marker := `data-test-id="Unit-readmeContent">`
	for _, size := range []int{199, 200, 201} {
		text := strings.Repeat("a", size)
		synopsis, readme, _ := extractReadmeContent(marker + text)
		wantSynopsis := min(size, 200)
		if len(synopsis) != wantSynopsis {
			t.Errorf("size %d: synopsis len = %d, want %d", size, len(synopsis), wantSynopsis)
		}
		wantReadme := max(size-200, 0)
		if len(readme) != wantReadme {
			t.Errorf("size %d: readme len = %d, want %d", size, len(readme), wantReadme)
		}
	}
}

func TestExtractReadmeContent_ReadmeCap(t *testing.T) {
	marker := `data-test-id="Unit-readmeContent">`
	for _, size := range []int{1999, 2000, 2001} {
		text := "head\n\n" + strings.Repeat("b", size)
		_, readme, _ := extractReadmeContent(marker + text)
		want := size
		wantStr := strings.Repeat("b", size)
		if size > 2000 {
			want = 2003
			wantStr = strings.Repeat("b", 2000) + "..."
		}
		if len(readme) != want || readme != wantStr {
			t.Errorf("size %d: readme len = %d, want %d (tail %q)", size, len(readme), want, readme[max(0, len(readme)-6):])
		}
	}
}

// Exported names must start uppercase, deduplicate, and cap at 25.
func TestExtractReadmeContent_Exports(t *testing.T) {
	var links strings.Builder
	for i := range 26 {
		name := "F" + string(rune('a'+i%26)) + itoa64(int64(i))
		links.WriteString(`<a href="#` + name + `">` + name + `</a>`)
		links.WriteString(`<a href="#` + name + `">` + name + `</a>`)
	}
	links.WriteString(`<a href="#lower">lower</a><a href="#_underscore">_underscore</a>`)
	_, _, exports := extractReadmeContent(`data-test-id="Unit-readmeContent">` + links.String())
	if len(exports) != 25 {
		t.Fatalf("exports = %d entries, want 25 (%q)", len(exports), exports)
	}
	if exports[0] != "Fa0" {
		t.Errorf("first export = %q, want Fa0", exports[0])
	}
	seen := map[string]bool{}
	for _, e := range exports {
		if seen[e] {
			t.Errorf("duplicate export %q", e)
		}
		seen[e] = true
		if e[0] < 'A' || e[0] > 'Z' {
			t.Errorf("export %q does not start uppercase", e)
		}
	}
}

func TestExtractReadmeContent_AFirstExportKept(t *testing.T) {
	_, _, exports := extractReadmeContent(`data-test-id="Unit-readmeContent"><a href="#Alpha">Alpha</a><a href="#Zoo">Zoo</a>`)
	if len(exports) != 2 || exports[0] != "Alpha" || exports[1] != "Zoo" {
		t.Errorf("exports = %q, want [Alpha Zoo]", exports)
	}
}

func TestExtractReadmeContent_ContentCap(t *testing.T) {
	marker := `data-test-id="Unit-readmeContent">`
	// A link past the 80000 byte window is invisible to the export scan.
	const link = `<a href="#Tail">Tail</a>`
	for _, pad := range []int{79975, 79976, 79977} {
		_, _, exports := extractReadmeContent(marker + strings.Repeat("z", pad) + link)
		visible := pad+len(link) <= 80000
		if got := len(exports) == 1; got != visible {
			t.Errorf("pad %d: exports = %q, want link visible = %v", pad, exports, visible)
		}
	}
}

func TestStripHTMLTagsSimple(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"block elements", "<p>a</p><div>b</div><section>c</section><article>d</article><h1>e</h1><pre>f</pre>",
			"a\n\nb\n\nc\n\nd\n\ne\n\nf"},
		{"list and table", "<ul><li>i</li></ul><table><tr>r</tr></table><hr>", "i\n\nr"},
		{"br variants", "a<br>b<br/>c<br />d", "a\nb\nc\nd"},
		{"inline tags removed", "<b>x</b><i>y</i>", "xy"},
		{"entities", `&amp;&lt;&gt;&quot;&#39;&#x27;&#x2F;&nbsp;`, "&<>\"''/"},
		{"blank run collapse", "a\n\n\n\nb", "a\n\nb"},
		{"space collapse", "a   b\tc", "a b c"},
		{"trim", "  keep  ", "keep"},
		{"case insensitive block", "<P>a</P><BR/>b", "a\n\nb"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := stripHTMLTagsSimple(tt.in)
			if got != tt.want {
				t.Errorf("stripHTMLTagsSimple(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// The synopsis ends at the first blank line, even when that line starts at
// index 1 of the stripped text.
func TestExtractReadmeContent_SynopsisSplitAtFirstBlankLine(t *testing.T) {
	synopsis, readme, exports := extractReadmeContent(`<div data-test-id="Unit-readmeContent">a<br><br>b</div>`)
	if synopsis != "a" {
		t.Errorf("synopsis = %q, want %q", synopsis, "a")
	}
	if readme != "b" {
		t.Errorf("readme = %q, want %q", readme, "b")
	}
	if exports != nil {
		t.Errorf("exports = %q, want none", exports)
	}
}

// Exported identifiers start with an uppercase ASCII letter: 'A' itself counts,
// a name starting below 'A' does not, and an anchor whose text is only
// whitespace must not be indexed at all.
func TestExtractReadmeContent_ExportFilter(t *testing.T) {
	_, _, exports := extractReadmeContent(`<div data-test-id="Unit-readmeContent">` +
		`<a href="#Alpha">Alpha</a><a href="#2fa">2fa</a><a href="#blank"> </a></div>`)
	if !slices.Equal(exports, []string{"Alpha"}) {
		t.Errorf("exports = %q, want [Alpha]", exports)
	}
}

// The commitTime marker is a publication date only when it carries the
// "Published:" label; without the label an unrelated date would be lifted.
func TestHandleGoPkg_CommitTimeNeedsPublishedLabel(t *testing.T) {
	client, _ := gopkgSrv(t, `{"Version":"v1.2.3","Time":"x"}`,
		gopkgHeader("commitTime|Updated Feb 28, 2026 License: MIT"))
	result, err := HandleGoPkg(context.Background(), mustParseURL(t, "https://pkg.go.dev/mod"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	want := "# mod\n\n**Version:** v1.2.3\n"
	if result.Content != want {
		t.Errorf("Content = %q, want %q", result.Content, want)
	}
}

// The date scan stops at the first ASCII digit, so a leading 0 or 9 belongs to
// the published string; narrowing the digit window drops it.
func TestHandleGoPkg_PublishedDateLeadingZeroAndNine(t *testing.T) {
	tests := []struct {
		text string
		want string
	}{
		{"Published: Dec 01, 2026 License: MIT", "# mod\n\n**Version:** v1.2.3 · Published: 01, 2026\n"},
		{"Published: Sep 9, 2026 License: MIT", "# mod\n\n**Version:** v1.2.3 · Published: 9, 2026\n"},
	}
	for _, tt := range tests {
		client, _ := gopkgSrv(t, `{"Version":"v1.2.3","Time":"x"}`, gopkgHeader("commitTime|"+tt.text))
		result, err := HandleGoPkg(context.Background(), mustParseURL(t, "https://pkg.go.dev/mod"), client, nil)
		if err != nil {
			t.Fatalf("error = %v", err)
		}
		if result == nil {
			t.Fatal("expected result, got nil")
		}
		if result.Content != tt.want {
			t.Errorf("Content = %q, want %q", result.Content, tt.want)
		}
	}
}
