package scrapers

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

func TestHandleWeixin_NoMatch(t *testing.T) {
	result, _ := HandleWeixin(context.Background(), mustParseURL(t, "https://example.com/s/abc"), nil, nil)
	if result != nil {
		t.Errorf("expected nil for non-weixin host, got %+v", result)
	}
}

func TestHandleWeixin_NilJS(t *testing.T) {
	result, _ := HandleWeixin(context.Background(), mustParseURL(t, "https://mp.weixin.qq.com/s/abc"), nil, nil)
	if result != nil {
		t.Errorf("expected nil when JSFetcher is nil, got %+v", result)
	}
}

func TestHandleWeixin_Success(t *testing.T) {
	html := `<html>
		<head><title>Test Article</title></head>
		<body>
			<h1 id="activity-name">Test Article Title</h1>
			<div id="js_content">
				<p>This is the article content.</p>
				<p>It has multiple paragraphs.</p>
			</div>
		</body>
	</html>`

	js := func(ctx context.Context, u string) (string, error) {
		return html, nil
	}

	result, err := HandleWeixin(context.Background(), mustParseURL(t, "https://mp.weixin.qq.com/s/abc123"), nil, js)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	if !strings.Contains(result.Content, "Test Article Title") {
		t.Errorf("expected title, got: %q", result.Content)
	}
	if !strings.Contains(result.Content, "article content") {
		t.Errorf("expected content, got: %q", result.Content)
	}
}

func TestHandleWeixin_JSError(t *testing.T) {
	js := func(ctx context.Context, u string) (string, error) {
		return "", fmt.Errorf("chromedp failed")
	}
	_, err := HandleWeixin(context.Background(), mustParseURL(t, "https://mp.weixin.qq.com/s/abc"), nil, js)
	if err == nil {
		t.Fatal("expected error when JSFetcher fails")
	}
	if !strings.Contains(err.Error(), "chromedp failed") {
		t.Errorf("error should mention chromedp failure, got: %v", err)
	}
}

func TestHandleWeixin_NoTitleNoContent(t *testing.T) {
	html := `<html><body>no markers</body></html>`
	js := func(ctx context.Context, u string) (string, error) { return html, nil }
	result, err := HandleWeixin(context.Background(), mustParseURL(t, "https://mp.weixin.qq.com/s/abc"), nil, js)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result != nil {
		t.Errorf("expected nil for missing title, got %+v", result)
	}
}

func TestHandleWeixin_WithAllFields(t *testing.T) {
	html := `<html>
		<body>
			<h1 id="activity-name">Title</h1>
			<div id="js_name">Author</div>
			<span id="publish_time">2024-01-15</span>
			<div id="js_content">
				<p>First paragraph.</p>
				<p>Second paragraph with <a href="https://example.com">link</a>.</p>
			</div>
		</body>
	</html>`
	js := func(ctx context.Context, u string) (string, error) { return html, nil }
	result, err := HandleWeixin(context.Background(), mustParseURL(t, "https://mp.weixin.qq.com/s/abc"), nil, js)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	if !strings.Contains(result.Content, "Author") {
		t.Errorf("expected author, got: %q", result.Content)
	}
	if !strings.Contains(result.Content, "2024-01-15") {
		t.Errorf("expected date, got: %q", result.Content)
	}
	if !strings.Contains(result.Content, "First paragraph") {
		t.Errorf("expected first paragraph, got: %q", result.Content)
	}
}

func TestFindCloseTag_SimpleSelfClosing(t *testing.T) {
	html := `<div>X</div>`
	pos := findCloseTag(html, 5, "div")
	if pos != 12 {
		t.Errorf("findCloseTag(div) pos = %d, want 12", pos)
	}
}

func TestFindCloseTag_NoCommentEnd(t *testing.T) {
	html := `<div><!-- never ends`
	if got := findCloseTag(html, 0, "div"); got != -1 {
		t.Errorf("findCloseTag(unterminated comment) = %d, want -1", got)
	}
}

func TestFindCloseTag_NoClosingTag(t *testing.T) {
	html := `<div><p>unclosed`
	if got := findCloseTag(html, 0, "div"); got != -1 {
		t.Errorf("findCloseTag(no close) = %d, want -1", got)
	}
}

func TestFindCloseTag_NoGreaterThan(t *testing.T) {
	html := `<div broken`
	if got := findCloseTag(html, 0, "div"); got != -1 {
		t.Errorf("findCloseTag(broken) = %d, want -1", got)
	}
}

func TestFindCloseTag_PrefixButNotExactTag(t *testing.T) {
	html := `<div><data-foo>x</data-foo></div>`
	pos := findCloseTag(html, 5, "div")
	t.Logf("findCloseTag result: %d", pos)
}

func TestFindCloseTag_OpeningWithAttributes(t *testing.T) {
	html := `<div class="x">content</div>`
	pos := findCloseTag(html, 14, "div")
	t.Logf("findCloseTag result with attrs: %d", pos)
}

func TestExtractByIDText(t *testing.T) {
	html := `<div id="title">Hello</div><div id="other">World</div>`
	got := extractByIDText(html, "title")
	if got != "Hello" {
		t.Errorf("got %q, want Hello", got)
	}
	got = extractByIDText(html, "nonexistent")
	if got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

func TestExtractByIDInner(t *testing.T) {
	html := `<div id="content">main text</div>`
	got := extractByIDInner(html, "content")
	if !strings.Contains(got, "main text") {
		t.Errorf("got %q, want to contain 'main text'", got)
	}
	got = extractByIDInner(html, "missing")
	if got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

func TestExtractByIDInner_MultipleElements(t *testing.T) {
	html := `<div id="target">first</div><div id="target">second</div>`
	got := extractByIDInner(html, "target")
	if !strings.Contains(got, "first") {
		t.Errorf("expected 'first', got %q", got)
	}
}

func TestExtractByIDInner_NoTagStart(t *testing.T) {
	html := `prefix id="orphan"`
	got := extractByIDInner(html, "orphan")
	if got != "" {
		t.Errorf("expected empty for no preceding <, got %q", got)
	}
}

func TestExtractByIDInner_NoCloseGT(t *testing.T) {
	html := `<div id="broken"`
	got := extractByIDInner(html, "broken")
	if got != "" {
		t.Errorf("expected empty for no closing >, got %q", got)
	}
}

func TestExtractByIDInner_NoCloseTag(t *testing.T) {
	html := `<div id="unclosed"><p>never closed`
	got := extractByIDInner(html, "unclosed")
	if got != "" {
		t.Errorf("expected empty when no closing tag found, got %q", got)
	}
}

func TestExtractByIDInner_NonDivTag(t *testing.T) {
	html := `<span id="title">hello</span>`
	got := extractByIDInner(html, "title")
	if !strings.Contains(got, "hello") {
		t.Errorf("expected 'hello', got %q", got)
	}
}
