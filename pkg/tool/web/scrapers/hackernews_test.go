package scrapers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestHandleHackerNews_NoMatch(t *testing.T) {
	result, _ := HandleHackerNews(context.Background(), mustParseURL(t, "https://example.com/item?id=1"), nil, nil)
	if result != nil {
		t.Errorf("expected nil for non-HN host, got %+v", result)
	}
}

func TestHandleHackerNews_ItemByID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v0/item/1.json" {
			_, _ = w.Write([]byte(`{"id":1,"type":"story","by":"alice","time":1700000000,"title":"Test Story","url":"https://example.com","score":42,"descendants":3,"kids":[]}`))
			return
		}
		_, _ = w.Write([]byte(`null`))
	}))
	defer srv.Close()

	client := &http.Client{Transport: &rewriteTransport{server: srv, host: "hacker-news.firebaseio.com"}}
	result, err := HandleHackerNews(context.Background(), mustParseURL(t, "https://news.ycombinator.com/item?id=1"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	if !strings.Contains(result.Content, "Test Story") {
		t.Errorf("expected title, got: %q", result.Content)
	}
	if !strings.Contains(result.Content, "alice") {
		t.Errorf("expected by-line, got: %q", result.Content)
	}
}

func TestHandleHackerNews_ItemWithComments(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v0/item/1.json":
			_, _ = w.Write([]byte(`{"id":1,"type":"story","by":"alice","time":1700000000,"title":"Story","url":"","score":10,"descendants":2,"kids":[2,3]}`))
		case "/v0/item/2.json":
			_, _ = w.Write([]byte(`{"id":2,"type":"comment","by":"bob","time":1700001000,"text":"<p>nice</p>"}`))
		case "/v0/item/3.json":
			_, _ = w.Write([]byte(`{"id":3,"type":"comment","by":"carol","time":1700002000,"text":"<p>cool</p>"}`))
		default:
			_, _ = w.Write([]byte(`null`))
		}
	}))
	defer srv.Close()

	client := &http.Client{Transport: &rewriteTransport{server: srv, host: "hacker-news.firebaseio.com"}}
	result, err := HandleHackerNews(context.Background(), mustParseURL(t, "https://news.ycombinator.com/item?id=1"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	if !strings.Contains(result.Content, "Comments") {
		t.Errorf("expected Comments section, got: %q", result.Content)
	}
}

func TestHandleHackerNews_ItemDeleted(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":1,"deleted":true}`))
	}))
	defer srv.Close()

	client := &http.Client{Transport: &rewriteTransport{server: srv, host: "hacker-news.firebaseio.com"}}
	result, err := HandleHackerNews(context.Background(), mustParseURL(t, "https://news.ycombinator.com/item?id=1"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	if !strings.Contains(result.Content, "[deleted]") {
		t.Errorf("expected [deleted] marker, got: %q", result.Content)
	}
}

func TestHandleHackerNews_TopStories(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v0/topstories.json" {
			_, _ = w.Write([]byte(`[1,2]`))
			return
		}
		if r.URL.Path == "/v0/item/1.json" {
			_, _ = w.Write([]byte(`{"id":1,"type":"story","by":"alice","time":1700000000,"title":"First Story","url":"https://example.com/a","score":50,"descendants":10}`))
			return
		}
		if r.URL.Path == "/v0/item/2.json" {
			_, _ = w.Write([]byte(`{"id":2,"type":"story","by":"bob","time":1700001000,"title":"Second Story","url":"https://example.com/b","score":30,"descendants":5}`))
			return
		}
		_, _ = w.Write([]byte(`null`))
	}))
	defer srv.Close()

	client := &http.Client{Transport: &rewriteTransport{server: srv, host: "hacker-news.firebaseio.com"}}
	result, err := HandleHackerNews(context.Background(), mustParseURL(t, "https://news.ycombinator.com/"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	if !strings.Contains(result.Content, "First Story") {
		t.Errorf("expected first story, got: %q", result.Content)
	}
	if !strings.Contains(result.Content, "Second Story") {
		t.Errorf("expected second story, got: %q", result.Content)
	}
}

func TestHandleHackerNews_BadItemID(t *testing.T) {
	result, _ := HandleHackerNews(context.Background(), mustParseURL(t, "https://news.ycombinator.com/item?id=abc"), nil, nil)
	if result != nil {
		t.Errorf("expected nil for non-numeric ID, got %+v", result)
	}
}

func TestHandleHackerNews_ItemNewest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[1]`))
	}))
	defer srv.Close()

	client := &http.Client{Transport: &rewriteTransport{server: srv, host: "hacker-news.firebaseio.com"}}
	result, err := HandleHackerNews(context.Background(), mustParseURL(t, "https://news.ycombinator.com/newest"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	if !strings.Contains(result.Content, "New Stories") {
		t.Errorf("expected New Stories label, got: %q", result.Content)
	}
}

func TestHandleHackerNews_Best(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[1]`))
	}))
	defer srv.Close()

	client := &http.Client{Transport: &rewriteTransport{server: srv, host: "hacker-news.firebaseio.com"}}
	result, err := HandleHackerNews(context.Background(), mustParseURL(t, "https://news.ycombinator.com/best"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	if !strings.Contains(result.Content, "Best Stories") {
		t.Errorf("expected Best Stories label, got: %q", result.Content)
	}
}

func TestHandleHackerNews_ItemFetchError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	client := &http.Client{Transport: &rewriteTransport{server: srv, host: "hacker-news.firebaseio.com"}}
	result, err := HandleHackerNews(context.Background(), mustParseURL(t, "https://news.ycombinator.com/item?id=1"), client, nil)
	if err == nil {
		t.Fatal("expected error for 500")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("err = %q, want 500", err.Error())
	}
	if result != nil {
		t.Errorf("expected nil result on error, got %+v", result)
	}
}

func TestHandleHackerNews_ListingItemFetchError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/topstories.json") {
			_, _ = w.Write([]byte(`[1,2,3]`))
			return
		}
		if strings.HasSuffix(r.URL.Path, "/item/1.json") {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/item/2.json") {
			_, _ = w.Write([]byte(`not-json`))
			return
		}
		if strings.HasSuffix(r.URL.Path, "/item/3.json") {
			_, _ = w.Write([]byte(`{"id":3,"title":"Third","type":"story","by":"carol","time":1700000000,"score":10,"descendants":0}`))
			return
		}
	}))
	defer srv.Close()

	client := &http.Client{Transport: &rewriteTransport{server: srv, host: "hacker-news.firebaseio.com"}}
	result, err := HandleHackerNews(context.Background(), mustParseURL(t, "https://news.ycombinator.com/news"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	if !strings.Contains(result.Content, "Third") {
		t.Errorf("expected third item, got: %q", result.Content)
	}
}

func TestHandleHackerNews_ListingDeletedItem(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/topstories.json") {
			_, _ = w.Write([]byte(`[1,2]`))
			return
		}
		if strings.HasSuffix(r.URL.Path, "/item/1.json") {
			_, _ = w.Write([]byte(`{"id":1,"deleted":true}`))
			return
		}
		if strings.HasSuffix(r.URL.Path, "/item/2.json") {
			_, _ = w.Write([]byte(`{"id":2,"title":"Valid Story","type":"story","by":"alice","time":1700000000,"score":10,"descendants":0}`))
			return
		}
	}))
	defer srv.Close()

	client := &http.Client{Transport: &rewriteTransport{server: srv, host: "hacker-news.firebaseio.com"}}
	result, err := HandleHackerNews(context.Background(), mustParseURL(t, "https://news.ycombinator.com/"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	if strings.Contains(result.Content, "1.") {
		t.Errorf("deleted item should be skipped, got: %q", result.Content)
	}
}

func TestHandleHackerNews_ListingEmptyTitle(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/topstories.json") {
			_, _ = w.Write([]byte(`[1]`))
			return
		}
		if strings.HasSuffix(r.URL.Path, "/item/1.json") {
			_, _ = w.Write([]byte(`{"id":1,"type":"story","by":"alice","time":1700000000,"score":10,"descendants":0}`))
			return
		}
	}))
	defer srv.Close()

	client := &http.Client{Transport: &rewriteTransport{server: srv, host: "hacker-news.firebaseio.com"}}
	result, err := HandleHackerNews(context.Background(), mustParseURL(t, "https://news.ycombinator.com/"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	if !strings.Contains(result.Content, "[deleted]") {
		t.Errorf("expected [deleted] for empty title, got: %q", result.Content)
	}
}

func TestHandleHackerNews_BadIDFormat(t *testing.T) {
	result, _ := HandleHackerNews(context.Background(), mustParseURL(t, "https://news.ycombinator.com/item?id="), nil, nil)
	if result != nil {
		t.Errorf("expected nil for empty id, got %+v", result)
	}
}

func TestHandleHackerNews_NoIDQuery(t *testing.T) {
	result, _ := HandleHackerNews(context.Background(), mustParseURL(t, "https://news.ycombinator.com/item"), nil, nil)
	if result != nil {
		t.Errorf("expected nil for missing id query, got %+v", result)
	}
}

func TestFormatRelativeTime(t *testing.T) {
	now := time.Now() // REAL-TIME:
	tests := []struct {
		delta time.Duration
		want  string
	}{
		{-30 * time.Minute, "30m ago"},
		{-2 * time.Hour, "2h ago"},
	}
	for _, tt := range tests {
		ts := now.Add(tt.delta).Unix()
		got := formatRelativeTime(ts)
		if got != tt.want {
			t.Errorf("formatRelativeTime(%v) = %q, want %q", tt.delta, got, tt.want)
		}
	}
	oldTS := now.Add(-365 * 24 * time.Hour).Unix()
	got := formatRelativeTime(oldTS)
	if !strings.Contains(got, "-") {
		t.Errorf("expected date format for old timestamp, got: %q", got)
	}
}

func TestFormatRelativeTime_VeryRecent(t *testing.T) {
	now := time.Now() // REAL-TIME:
	got := formatRelativeTime(now.Add(-1 * time.Second).Unix())
	if !strings.HasSuffix(got, "m ago") {
		t.Errorf("expected 'Xm ago', got %q", got)
	}
}

func TestFormatRelativeTime_MultiDay(t *testing.T) {
	now := time.Now() // REAL-TIME:
	got := formatRelativeTime(now.Add(-72 * time.Hour).Unix())
	if !strings.Contains(got, "d ago") {
		t.Errorf("expected 'Xd ago', got %q", got)
	}
}

func TestDecodeHNText(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"<p>hello</p>", "hello"},
		{"<p>para 1</p><p>para 2</p>", "para 1\n\npara 2"},
		{"<i>italic</i>", "*italic*"},
		{"plain", "plain"},
		{"", ""},
		{"<pre><code>x := 1</code></pre>", "```\nx := 1\n```"},
	}
	for _, tt := range tests {
		got := decodeHNText(tt.input)
		if got != tt.want {
			t.Errorf("decodeHNText(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestReplaceAnchorTags(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{`<a href="https://example.com">link</a>`, "[link](https://example.com)"},
		{"no anchors here", "no anchors here"},
		{`<a href="https://a.com">a</a> and <a href="https://b.com">b</a>`, "[a](https://a.com) and [b](https://b.com)"},
		{`<a href="x">y</a> trailing`, "[y](x) trailing"},
	}
	for _, tt := range tests {
		got := replaceAnchorTags(tt.input)
		if got != tt.want {
			t.Errorf("replaceAnchorTags(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestReplaceAnchorTags_NoHref(t *testing.T) {
	got := replaceAnchorTags(`<a >no href</a>`)
	if got != ">no href</a>" {
		t.Errorf("expected >no href</a>, got %q", got)
	}
}

func TestReplaceAnchorTags_NoClosingQuote(t *testing.T) {
	got := replaceAnchorTags(`<a href="https://x.com>text</a>`)
	if got != `href="https://x.com>text</a>` {
		t.Errorf("expected href=... for unclosed quote, got %q", got)
	}
}

func TestReplaceAnchorTags_NoClosingTag(t *testing.T) {
	got := replaceAnchorTags(`<a href="url">text`)
	if got != `href="url">text` {
		t.Errorf("expected href=... for no closing tag, got %q", got)
	}
}

func TestReplaceAnchorTags_NoCloseGT(t *testing.T) {
	got := replaceAnchorTags(`<a `)
	if got != "" {
		t.Errorf("expected empty, got %q", got)
	}
}

func TestFetchHNComment_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	client := &http.Client{Transport: &rewriteTransport{server: srv, host: "hacker-news.firebaseio.com"}}
	_, err := fetchHNComment(context.Background(), client, 1)
	if err == nil {
		t.Fatal("expected error for HTTP 500")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("error should mention 500, got: %v", err)
	}
}

func TestFetchHNComment_Deleted(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":1,"deleted":true}`))
	}))
	defer srv.Close()

	client := &http.Client{Transport: &rewriteTransport{server: srv, host: "hacker-news.firebaseio.com"}}
	text, err := fetchHNComment(context.Background(), client, 1)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if text != "" {
		t.Errorf("expected empty string for deleted comment, got %q", text)
	}
}

func TestFetchHNComment_NotCommentType(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":1,"type":"story","by":"alice","time":1700000000,"text":"hello"}`))
	}))
	defer srv.Close()

	client := &http.Client{Transport: &rewriteTransport{server: srv, host: "hacker-news.firebaseio.com"}}
	text, err := fetchHNComment(context.Background(), client, 1)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if text != "" {
		t.Errorf("expected empty for non-comment type, got %q", text)
	}
}

func TestFetchHNComment_NoText(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":1,"type":"comment","by":"alice","time":1700000000,"text":""}`))
	}))
	defer srv.Close()

	client := &http.Client{Transport: &rewriteTransport{server: srv, host: "hacker-news.firebaseio.com"}}
	text, err := fetchHNComment(context.Background(), client, 1)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if !strings.Contains(text, "alice") {
		t.Errorf("expected author in output, got %q", text)
	}
}

func TestHandleHackerNews_ListingFetchError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	client := &http.Client{Transport: &rewriteTransport{server: srv, host: "hacker-news.firebaseio.com"}}
	_, err := HandleHackerNews(context.Background(), mustParseURL(t, "https://news.ycombinator.com/"), client, nil)
	if err == nil {
		t.Fatal("expected error for 500")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("error should mention 500, got: %v", err)
	}
}

func TestHandleHackerNews_ListingBadJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`not json`))
	}))
	defer srv.Close()

	client := &http.Client{Transport: &rewriteTransport{server: srv, host: "hacker-news.firebaseio.com"}}
	_, err := HandleHackerNews(context.Background(), mustParseURL(t, "https://news.ycombinator.com/"), client, nil)
	if err == nil {
		t.Fatal("expected error for bad JSON")
	}
	if !strings.Contains(err.Error(), "fetch HN listing") {
		t.Errorf("error should mention fetch error, got: %v", err)
	}
}

func TestFetchHNCommentsParallel_EmptyKids(t *testing.T) {
	comments := fetchHNCommentsParallel(context.Background(), nil, nil)
	if len(comments) != 0 {
		t.Errorf("expected empty comments, got %d", len(comments))
	}
}

func TestHandleHackerNews_ItemWithSelfURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/item/1.json") {
			_, _ = w.Write([]byte(`{"id":1,"type":"story","by":"alice","time":1700000000,"title":"Self Post","url":"","score":10,"descendants":0,"kids":[]}`))
			return
		}
	}))
	defer srv.Close()

	client := &http.Client{Transport: &rewriteTransport{server: srv, host: "hacker-news.firebaseio.com"}}
	result, err := HandleHackerNews(context.Background(), mustParseURL(t, "https://news.ycombinator.com/item?id=1"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	// Item without URL should link to HN itself.
	if !strings.Contains(result.Content, "news.ycombinator.com/item?id=1") {
		t.Errorf("expected HN self-link, got: %q", result.Content)
	}
}

func TestHandleHackerNews_ItemBadJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`not json`))
	}))
	defer srv.Close()

	client := &http.Client{Transport: &rewriteTransport{server: srv, host: "hacker-news.firebaseio.com"}}
	_, err := HandleHackerNews(context.Background(), mustParseURL(t, "https://news.ycombinator.com/item?id=1"), client, nil)
	if err == nil {
		t.Fatal("expected error for bad JSON")
	}
	if !strings.Contains(err.Error(), "fetch HN item") {
		t.Errorf("error should mention fetch error, got: %v", err)
	}
}

func TestHandleHackerNews_ListingItemSelfURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/topstories.json") {
			_, _ = w.Write([]byte(`[1]`))
			return
		}
		if strings.HasSuffix(r.URL.Path, "/item/1.json") {
			_, _ = w.Write([]byte(`{"id":1,"title":"Self Post","type":"story","by":"alice","time":1700000000,"score":10,"url":"","descendants":0}`))
			return
		}
	}))
	defer srv.Close()

	client := &http.Client{Transport: &rewriteTransport{server: srv, host: "hacker-news.firebaseio.com"}}
	result, err := HandleHackerNews(context.Background(), mustParseURL(t, "https://news.ycombinator.com/"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	if !strings.Contains(result.Content, "news.ycombinator.com/item?id=1") {
		t.Errorf("expected HN self-link for item without URL, got: %q", result.Content)
	}
}
