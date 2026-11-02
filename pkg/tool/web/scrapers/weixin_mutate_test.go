package scrapers

import (
	"context"
	"errors"
	"testing"
)

type weixinCtxKey struct{}

const weixinPage = `<html><body>` +
	`<h1 id="activity-name">Article Title</h1>` +
	`<div id="js_name">Official Account</div>` +
	`<div id="publish_time">2024-01-15</div>` +
	`<div id="js_content"><p>First paragraph</p><p>Second <b>bold</b></p></div>` +
	`</body></html>`

func TestHandleWeixin_FullArticle(t *testing.T) {
	var gotURL string
	var gotCtx context.Context
	js := func(ctx context.Context, u string) (string, error) {
		gotCtx, gotURL = ctx, u
		return weixinPage, nil
	}

	result, err := HandleWeixin(context.WithValue(context.Background(), weixinCtxKey{}, "marker"),
		mustParseURL(t, "https://mp.weixin.qq.com/s/abc"), nil, js)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	want := "# Article Title\n\n**Author:** Official Account\n**Published:** 2024-01-15\n" +
		"**Source:** https://mp.weixin.qq.com/s/abc\n\n---\n\nFirst paragraph\n\nSecond bold\n"
	if result.Content != want {
		t.Errorf("Content = %q, want %q", result.Content, want)
	}
	if result.ContentType != "text/markdown" {
		t.Errorf("ContentType = %q", result.ContentType)
	}
	if result.Method != "weixin-js" {
		t.Errorf("Method = %q", result.Method)
	}
	if len(result.Notes) != 1 || result.Notes[0] != "Fetched via headless Chrome (JS rendering + stealth)" {
		t.Errorf("Notes = %q", result.Notes)
	}
	if gotURL != "https://mp.weixin.qq.com/s/abc" {
		t.Errorf("js fetched %q", gotURL)
	}
	if gotCtx == nil || gotCtx.Value(weixinCtxKey{}) != "marker" {
		t.Errorf("js lost the caller context (ctx=%v)", gotCtx)
	}
}

func TestHandleWeixin_WithoutAuthorOrPublish(t *testing.T) {
	js := func(ctx context.Context, u string) (string, error) {
		return `<div id="activity-name">Bare</div><div id="js_content"><p>Body</p></div>\n`, nil
	}
	result, err := HandleWeixin(context.Background(), mustParseURL(t, "https://mp.weixin.qq.com/s/x"), nil, js)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	want := "# Bare\n\n**Source:** https://mp.weixin.qq.com/s/x\n\n---\n\nBody\n"
	if result.Content != want {
		t.Errorf("Content = %q, want %q", result.Content, want)
	}
}

func TestHandleWeixin_Guards(t *testing.T) {
	tests := []struct {
		name string
		url  string
		js   JSFetcher
	}{
		{"no js fetcher", "https://mp.weixin.qq.com/s/x", nil},
		{"other host", "https://example.com/s/x", func(context.Context, string) (string, error) { return weixinPage, nil }},
		{"missing title", "https://mp.weixin.qq.com/s/x", func(context.Context, string) (string, error) {
			return `<div id="js_content"><p>Body</p></div>\n`, nil
		}},
		{"empty title", "https://mp.weixin.qq.com/s/x", func(context.Context, string) (string, error) {
			return `<div id="activity-name">   </div><div id="js_content"><p>Body</p></div>\n`, nil
		}},
		{"missing body", "https://mp.weixin.qq.com/s/x", func(context.Context, string) (string, error) {
			return `<div id="activity-name">T</div>`, nil
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := HandleWeixin(context.Background(), mustParseURL(t, tt.url), nil, tt.js)
			if err != nil {
				t.Errorf("error = %v", err)
			}
			if result != nil {
				t.Errorf("expected nil, got %+v", result)
			}
		})
	}
}

func TestHandleWeixin_FetchErrorKeepsChain(t *testing.T) {
	sentinel := errors.New("chrome crashed")
	js := func(ctx context.Context, u string) (string, error) { return "", sentinel }
	result, err := HandleWeixin(context.Background(), mustParseURL(t, "https://mp.weixin.qq.com/s/x"), nil, js)
	if result != nil {
		t.Errorf("expected nil result, got %+v", result)
	}
	if err == nil {
		t.Fatal("expected error")
	}
	if err.Error() != "weixin fetch: chrome crashed" {
		t.Errorf("error = %q, want %q", err.Error(), "weixin fetch: chrome crashed")
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("error %v does not unwrap to the fetch error", err)
	}
}

func TestWeixinToMarkdown(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"paragraph break", "a<p>b</p>", "a\n\nb"},
		{"inline tags removed", "<b>x</b>", "x"},
		{"br variants", "one<br>two<br/>three<br />four", "one\n\ntwo\n\nthree\n\nfour"},
		{"block elements", "<h2>H</h2><section>S</section><pre>P</pre><ul><li>L</li></ul><blockquote>Q</blockquote><hr>",
			"H\n\nS\n\nP\n\nL\n\nQ"},
		{"entities", `&amp;&lt;&gt;&quot;&#39;&nbsp;&ldquo;&rdquo;&lsquo;&rsquo;&mdash;&ndash;&hellip;`,
			"&<>\"' “”‘’—–…"},
		{"ampersand decoded first", "&amp;lt;", "<"},
		{"img is a block tag", `<p>text</p><img src="https://x/i.png"><p>more</p>`, "text\n\nmore"},
		{"img with data-src", `<p>text</p><img data-src="/local.png"><p>more</p>`, "text\n\nmore"},
		{"trailing spaces before newline", "x   \ny", "x\ny"},
		{"blank run collapse", "a\n\n\n\nb", "a\n\nb"},
		{"surrounding whitespace", "   keep   ", "keep"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := weixinToMarkdown(tt.in)
			if got != tt.want {
				t.Errorf("weixinToMarkdown(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestExtractByIDInner_Nesting(t *testing.T) {
	tests := []struct {
		name string
		html string
		id   string
		want string
	}{
		{"div", `<div id="a">inner<b>x</b></div>Z`, "a", "inner<b>x</b></div>"},
		{"span", `<span id="a">x</span>Z`, "a", "x</span>"},
		{"attributes before id", `<div class="c" id="a">x</div>Z`, "a", "x</div>"},
		{"nested same tag", `<div id="a">outer<div>inner</div></div>Z`, "a", "outer<div>inner</div></div>"},
		{"comment with tag", `<div id="a">x<!-- <div>c</div> --></div>Z`, "a", "x<!-- <div>c</div> --></div>"},
		{"self closing tag", `<div id="a">x<br/>y</div>Z`, "a", "x<br/>y</div>"},
		{"prefix tag not counted", `<div id="a">x<divider>n</divider></div>Z`, "a", "x<divider>n</divider></div>"},
		{"unclosed", `<div id="a">unclosed`, "a", ""},
		{"wrong close tag", `<div id="a">x</divx>Z`, "a", ""},
		{"id absent", `<div id="b">x</div>Z`, "a", ""},
		{"no opening bracket", `id="a">x</div>Z`, "a", ""},
		// A close tag that ends the document is still a close tag: the scan must
		// be allowed to start at len(html)-len(closeMarker), not stop short of it.
		{"close tag at end after inner tags", `<div id="a">x<b>y</b></div>`, "a", "x<b>y</b></div>"},
		{"close tag ends the document", `<div id="a"></div>`, "a", "</div>"},
		{"tag name without space", `<divid="a">x</divid>Z`, "a", ""},
		{"tag name with leading space", `< div id="a">x</div>Z`, "a", ""},
		{"marker but no close bracket", `<div id="a" unclosed`, "a", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractByIDInner(tt.html, tt.id)
			if got != tt.want {
				t.Errorf("extractByIDInner(%q, %q) = %q, want %q", tt.html, tt.id, got, tt.want)
			}
		})
	}
}

func TestExtractByIDText_StripsTags(t *testing.T) {
	got := extractByIDText(`<div id="a">  <b>bold</b> text  </div>`, "a")
	if got != "bold  text" {
		t.Errorf("extractByIDText() = %q, want %q", got, "bold  text")
	}
	got = extractByIDText(`<div id="b">x</div>`, "a")
	if got != "" {
		t.Errorf("extractByIDText() = %q, want empty", got)
	}
}

func TestFindCloseTag(t *testing.T) {
	html := `<div>x</div>Z`
	if got := findCloseTag(html, 5, "div"); got != 12 {
		t.Errorf("findCloseTag() = %d, want 12", got)
	}
	if got := findCloseTag(`<div>x`, 5, "div"); got != -1 {
		t.Errorf("findCloseTag() = %d, want -1", got)
	}
	if got := findCloseTag(`text without tags`, 0, "div"); got != -1 {
		t.Errorf("findCloseTag() = %d, want -1", got)
	}
	if got := findCloseTag(`<div>x<!-- unterminated`, 5, "div"); got != -1 {
		t.Errorf("findCloseTag() = %d, want -1", got)
	}
	if got := findCloseTag(`<div>x<div>y</div></div>Z`, 5, "div"); got != 24 {
		t.Errorf("findCloseTag() = %d, want 24", got)
	}
	// The closing tag may start at exactly len(html)-len(closeMarker); a scan that
	// stops one byte short reports a well-formed document as unclosed.
	if got := findCloseTag(`<div>x<b>y</b></div>`, 5, "div"); got != 20 {
		t.Errorf("findCloseTag() = %d, want 20", got)
	}
	if got := findCloseTag(`<div></div>`, 5, "div"); got != 11 {
		t.Errorf("findCloseTag() = %d, want 11", got)
	}
	if got := findCloseTag(`<div>x<a`, 5, "div"); got != -1 {
		t.Errorf("findCloseTag() = %d, want -1", got)
	}
	if got := findCloseTag(`<div>x<div>y</div><div>z</div></div>Z`, 5, "div"); got != 36 {
		t.Errorf("findCloseTag() = %d, want 36", got)
	}
	if got := findCloseTag(`<div>x<div/>y</div>Z`, 5, "div"); got != 19 {
		t.Errorf("findCloseTag() = %d, want 19", got)
	}
	// The 4-byte comment peek needs its own bounds check: a lone '<' at the end
	// of the document has no four bytes to read.
	if got := findCloseTag(`<div>aaaaaaaaaaaaaaaaaaaa<`, 5, "div"); got != -1 {
		t.Errorf("findCloseTag() = %d, want -1", got)
	}
	// An unterminated comment swallows the rest of the document. Resuming the
	// scan inside it would match the close tag the comment hides.
	if got := findCloseTag(`<div>x<!--</div>`, 5, "div"); got != -1 {
		t.Errorf("findCloseTag() = %d, want -1", got)
	}
	// `<div >` opens a div: the tag-name boundary is the byte right after the
	// name, and missing it leaves the matching close tag unmatched.
	if got := findCloseTag(`<div>x<div >y</div>`, 5, "div"); got != -1 {
		t.Errorf("findCloseTag() = %d, want -1", got)
	}
}

func TestStripXMLTags(t *testing.T) {
	got := stripXMLTags(`  a<b>x</b>c  `)
	if got != "a x c" {
		t.Errorf("stripXMLTags() = %q, want %q", got, "a x c")
	}
	got = stripXMLTags("plain")
	if got != "plain" {
		t.Errorf("stripXMLTags() = %q, want plain", got)
	}
}
