package scrapers

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// hnSrv routes /v0/... paths to canned bodies and records the paths asked for.
func hnSrv(t *testing.T, bodies map[string]string) (*http.Client, *[]string) {
	t.Helper()
	seen := new([]string)
	var seenMu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenMu.Lock()
		*seen = append(*seen, r.URL.Path)
		seenMu.Unlock()
		body, ok := bodies[r.URL.Path]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return &http.Client{Transport: &rewriteTransport{server: srv, host: "hacker-news.firebaseio.com"}}, seen
}

// REAL-TIME: wall-clock fixture; the code under test compares against the system clock.
func ago(d time.Duration) int64 { return time.Now().Add(-d).Unix() }

func TestHandleHackerNews_ItemPage(t *testing.T) {
	client, seen := hnSrv(t, map[string]string{
		"/v0/item/42.json": `{"id":42,"type":"story","by":"alice","time":` + itoa64(ago(5*time.Hour)) +
			`,"text":"Hello <i>world</i>","kids":[1,2],"url":"https://example.com/story","score":100,` +
			`"title":"Show GN: Thing","descendants":2}`,
		"/v0/item/1.json": `{"id":1,"type":"comment","by":"bob","time":` + itoa64(ago(4*time.Hour)) +
			`,"text":"First comment"}`,
		"/v0/item/2.json": `{"id":2,"type":"comment","by":"carol","time":` + itoa64(ago(3*time.Hour)) +
			`,"text":"Second comment"}`,
	})

	result, err := HandleHackerNews(context.Background(), mustParseURL(t, "https://news.ycombinator.com/item?id=42"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	want := "# [Show GN: Thing](https://example.com/story)\n\n" +
		"**Posted by:** alice | **Score:** 100 | **Time:** 5h ago\n" +
		"**Comments:** 2\n\n" +
		"Hello *world*\n\n" +
		"---\n\n## Comments\n\n" +
		"**bob** (4h ago)\n\nFirst comment\n\n---\n\n" +
		"**carol** (3h ago)\n\nSecond comment\n\n---\n\n"
	if result.Content != want {
		t.Errorf("Content = %q, want %q", result.Content, want)
	}
	if result.ContentType != "text/markdown" {
		t.Errorf("ContentType = %q", result.ContentType)
	}
	if result.Method != "hackernews-api" {
		t.Errorf("Method = %q", result.Method)
	}
	if len(result.Notes) != 1 || result.Notes[0] != "Fetched via Hacker News Firebase API" {
		t.Errorf("Notes = %q", result.Notes)
	}
	if len(*seen) != 3 || (*seen)[0] != "/v0/item/42.json" {
		t.Errorf("requests = %q", *seen)
	}
}

// A story without its own URL links back to HN; missing text and kids drop
// their sections entirely.
func TestHandleHackerNews_ItemWithoutURLOrText(t *testing.T) {
	client, _ := hnSrv(t, map[string]string{
		"/v0/item/43.json": `{"id":43,"type":"story","by":"dave","time":` + itoa64(ago(90*time.Second)) +
			`,"title":"Ask HN: Nothing","score":7,"descendants":0}`,
	})
	result, err := HandleHackerNews(context.Background(), mustParseURL(t, "https://news.ycombinator.com/item?id=43"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	want := "# [Ask HN: Nothing](https://news.ycombinator.com/item?id=43)\n\n" +
		"**Posted by:** dave | **Score:** 7 | **Time:** 1m ago\n" +
		"**Comments:** 0\n\n" +
		"---\n\n## Comments\n\n"
	if result.Content != want {
		t.Errorf("Content = %q, want %q", result.Content, want)
	}
}

func TestHandleHackerNews_SingleKid(t *testing.T) {
	client, _ := hnSrv(t, map[string]string{
		"/v0/item/44.json": `{"id":44,"type":"story","by":"eve","time":` + itoa64(ago(time.Hour)) +
			`,"title":"T","score":1,"kids":[9]}`,
		"/v0/item/9.json": `{"id":9,"type":"comment","by":"frank","time":` + itoa64(ago(time.Hour)) +
			`,"text":"Only comment"}`,
	})
	result, err := HandleHackerNews(context.Background(), mustParseURL(t, "https://news.ycombinator.com/item?id=44"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	if !strings.Contains(result.Content, "**frank** (1h ago)\n\nOnly comment\n\n---\n\n") {
		t.Errorf("missing single comment in %q", result.Content)
	}
}

// Deleted, dead, non-comment and unreachable kids are all dropped from the
// comment list without dropping the story.
func TestHandleHackerNews_KidFiltering(t *testing.T) {
	client, _ := hnSrv(t, map[string]string{
		"/v0/item/45.json": `{"id":45,"type":"story","by":"gina","time":` + itoa64(ago(2*time.Hour)) +
			`,"title":"T","score":1,"kids":[1,2,3,4,5]}`,
		"/v0/item/1.json": `{"id":1,"deleted":true,"type":"comment","by":"x","text":"gone"}`,
		"/v0/item/2.json": `{"id":2,"dead":true,"type":"comment","by":"x","text":"gone"}`,
		"/v0/item/3.json": `{"id":3,"type":"story","by":"x","text":"not a comment"}`,
		"/v0/item/4.json": `{"id":4,"type":"comment","by":"kept","time":` + itoa64(ago(time.Hour)) + `,"text":"kept"}`,
		// item 5 is absent from the map, so its fetch 404s.
	})
	result, err := HandleHackerNews(context.Background(), mustParseURL(t, "https://news.ycombinator.com/item?id=45"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	want := "# [T](https://news.ycombinator.com/item?id=45)\n\n" +
		"**Posted by:** gina | **Score:** 1 | **Time:** 2h ago\n" +
		"**Comments:** 0\n\n---\n\n## Comments\n\n" +
		"**kept** (1h ago)\n\nkept\n\n---\n\n"
	if result.Content != want {
		t.Errorf("Content = %q, want %q", result.Content, want)
	}
}

func TestHandleHackerNews_DeletedAndDeadItems(t *testing.T) {
	for _, flag := range []string{"deleted", "dead"} {
		t.Run(flag, func(t *testing.T) {
			client, _ := hnSrv(t, map[string]string{
				"/v0/item/46.json": `{"id":46,"` + flag + `":true,"type":"story","by":"a","title":"T"}`,
			})
			result, err := HandleHackerNews(context.Background(), mustParseURL(t, "https://news.ycombinator.com/item?id=46"), client, nil)
			if err != nil {
				t.Fatalf("error = %v", err)
			}
			if result == nil {
				t.Fatal("expected result, got nil")
			}
			if result.Content != "[deleted]" {
				t.Errorf("Content = %q, want [deleted]", result.Content)
			}
			if result.ContentType != "text/markdown" || result.Method != "hackernews-api" {
				t.Errorf("ContentType/Method = %q/%q", result.ContentType, result.Method)
			}
			if len(result.Notes) != 1 || result.Notes[0] != "Item is deleted or dead" {
				t.Errorf("Notes = %q", result.Notes)
			}
		})
	}
}

func TestHandleHackerNews_Listings(t *testing.T) {
	tests := []struct {
		path     string
		endpoint string
		label    string
	}{
		{"/", "topstories.json", "Top Stories"},
		{"", "topstories.json", "Top Stories"},
		{"/news", "topstories.json", "Top Stories"},
		{"/newest", "newstories.json", "New Stories"},
		{"/best", "beststories.json", "Best Stories"},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			client, seen := hnSrv(t, map[string]string{
				"/v0/" + tt.endpoint: `[7,8]`,
				"/v0/item/7.json":    `{"id":7,"type":"story","by":"a","time":` + itoa64(ago(2*time.Hour)) + `,"title":"Seven","url":"https://e/7","score":3,"descendants":1}`,
				"/v0/item/8.json":    `{"id":8,"type":"story","by":"b","time":` + itoa64(ago(3*time.Hour)) + `,"title":"Eight","score":4,"descendants":2}`,
			})
			result, err := HandleHackerNews(context.Background(), mustParseURL(t, "https://news.ycombinator.com"+tt.path), client, nil)
			if err != nil {
				t.Fatalf("error = %v", err)
			}
			if result == nil {
				t.Fatal("expected result, got nil")
			}
			want := "# Hacker News - " + tt.label + "\n\n" +
				"1. **Seven**\n   https://e/7\n   3 points by a | 2h ago | 1 comments\n   https://news.ycombinator.com/item?id=7\n\n" +
				"2. **Eight**\n   https://news.ycombinator.com/item?id=8\n   4 points by b | 3h ago | 2 comments\n   https://news.ycombinator.com/item?id=8\n\n"
			if result.Content != want {
				t.Errorf("Content = %q, want %q", result.Content, want)
			}
			if result.ContentType != "text/markdown" || result.Method != "hackernews-api" {
				t.Errorf("ContentType/Method = %q/%q", result.ContentType, result.Method)
			}
			if len(result.Notes) != 1 || result.Notes[0] != "Fetched via Hacker News Firebase API ("+tt.label+")" {
				t.Errorf("Notes = %q", result.Notes)
			}
			if (*seen)[0] != "/v0/"+tt.endpoint {
				t.Errorf("first request = %q, want /v0/%s", (*seen)[0], tt.endpoint)
			}
		})
	}
}

// The listing cap is 20 stories: 19 must survive whole, 21 must be cut.
func TestHandleHackerNews_ListingCap(t *testing.T) {
	for _, count := range []int{19, 20, 21} {
		t.Run(itoa64(int64(count)), func(t *testing.T) {
			ids := make([]string, count)
			bodies := map[string]string{}
			for i := range count {
				id := i + 1
				ids[i] = itoa64(int64(id))
				bodies["/v0/item/"+ids[i]+".json"] = `{"id":` + ids[i] + `,"type":"story","by":"a","time":` +
					itoa64(ago(time.Hour)) + `,"title":"S` + ids[i] + `","score":1}`
			}
			bodies["/v0/topstories.json"] = "[" + strings.Join(ids, ",") + "]"

			client, _ := hnSrv(t, bodies)
			result, err := HandleHackerNews(context.Background(), mustParseURL(t, "https://news.ycombinator.com/news"), client, nil)
			if err != nil {
				t.Fatalf("error = %v", err)
			}
			if result == nil {
				t.Fatal("expected result, got nil")
			}
			got := strings.Count(result.Content, " points by ")
			wantCount := min(count, 20)
			if got != wantCount {
				t.Errorf("rendered %d stories, want %d", got, wantCount)
			}
		})
	}
}

func TestHandleHackerNews_ListingSkipsBadItems(t *testing.T) {
	client, _ := hnSrv(t, map[string]string{
		"/v0/topstories.json": `[1,2,3,4,5]`,
		"/v0/item/1.json":     `{"id":1,"deleted":true,"title":"gone"}`,
		"/v0/item/2.json":     `{"id":2,"dead":true,"title":"gone"}`,
		// 3 is absent (fetch error), 4 is unparseable, 5 has no title.
		"/v0/item/4.json": `5`,
		"/v0/item/5.json": `{"id":5,"type":"story","by":"a","time":` + itoa64(ago(time.Hour)) + `,"score":1}`,
	})
	result, err := HandleHackerNews(context.Background(), mustParseURL(t, "https://news.ycombinator.com/news"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	want := "# Hacker News - Top Stories\n\n" +
		"5. **[deleted]**\n   https://news.ycombinator.com/item?id=5\n   1 points by a | 1h ago | 0 comments\n" +
		"   https://news.ycombinator.com/item?id=5\n\n"
	if result.Content != want {
		t.Errorf("Content = %q, want %q", result.Content, want)
	}
}

func TestHandleHackerNews_Routing(t *testing.T) {
	client, _ := hnSrv(t, map[string]string{})
	for _, raw := range []string{
		"https://news.ycombinator.com/item",
		"https://news.ycombinator.com/item?id=abc",
		"https://news.ycombinator.com/user?id=alice",
		"https://news.ycombinator.com/x",
		"https://example.com/",
	} {
		result, err := HandleHackerNews(context.Background(), mustParseURL(t, raw), client, nil)
		if err != nil {
			t.Errorf("%s: error = %v", raw, err)
		}
		if result != nil {
			t.Errorf("%s: expected nil, got %+v", raw, result)
		}
	}
}

func TestHandleHackerNews_ItemTransportError(t *testing.T) {
	sentinel := errors.New("hn unreachable")
	client := &http.Client{Transport: errTransport{err: sentinel}}
	result, err := HandleHackerNews(context.Background(), mustParseURL(t, "https://news.ycombinator.com/item?id=1"), client, nil)
	if result != nil {
		t.Errorf("expected nil result, got %+v", result)
	}
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.HasPrefix(err.Error(), "fetch HN item: ") {
		t.Errorf("error = %q, want prefix fetch HN item", err.Error())
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("error %v does not unwrap to the transport error", err)
	}
}

func TestHandleHackerNews_ItemNotJSON(t *testing.T) {
	client, _ := hnSrv(t, map[string]string{"/v0/item/1.json": `5`})
	result, err := HandleHackerNews(context.Background(), mustParseURL(t, "https://news.ycombinator.com/item?id=1"), client, nil)
	if result != nil {
		t.Errorf("expected nil result, got %+v", result)
	}
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.HasPrefix(err.Error(), "parse HN item: ") {
		t.Errorf("error = %q, want prefix parse HN item", err.Error())
	}
	if errors.Unwrap(err) == nil {
		t.Errorf("error %v does not wrap the JSON failure", err)
	}
}

func TestHandleHackerNews_ListingTransportError(t *testing.T) {
	sentinel := errors.New("hn listing down")
	client := &http.Client{Transport: errTransport{err: sentinel}}
	result, err := HandleHackerNews(context.Background(), mustParseURL(t, "https://news.ycombinator.com/newest"), client, nil)
	if result != nil {
		t.Errorf("expected nil result, got %+v", result)
	}
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.HasPrefix(err.Error(), "fetch HN listing: ") {
		t.Errorf("error = %q, want prefix fetch HN listing", err.Error())
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("error %v does not unwrap to the transport error", err)
	}
}

func TestHandleHackerNews_ListingNotJSON(t *testing.T) {
	client, _ := hnSrv(t, map[string]string{"/v0/beststories.json": `{"not":"a list"}`})
	result, err := HandleHackerNews(context.Background(), mustParseURL(t, "https://news.ycombinator.com/best"), client, nil)
	if result != nil {
		t.Errorf("expected nil result, got %+v", result)
	}
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.HasPrefix(err.Error(), "parse HN listing: ") {
		t.Errorf("error = %q, want prefix parse HN listing", err.Error())
	}
	if errors.Unwrap(err) == nil {
		t.Errorf("error %v does not wrap the JSON failure", err)
	}
}

// A query id on a path that is not /item must not be treated as an item page.
func TestHandleHackerNews_ItemIDOnOtherPath(t *testing.T) {
	client, _ := hnSrv(t, map[string]string{
		"/v0/item/42.json": `{"id":42,"type":"story","by":"a","title":"T","time":` + itoa64(ago(time.Hour)) + `}`,
	})
	for _, raw := range []string{"https://news.ycombinator.com/x?id=42", "https://news.ycombinator.com/user?id=42"} {
		result, err := HandleHackerNews(context.Background(), mustParseURL(t, raw), client, nil)
		if err != nil {
			t.Errorf("%s: error = %v", raw, err)
		}
		if result != nil {
			t.Errorf("%s: expected nil, got %+v", raw, result)
		}
	}
}

func TestFetchHNComment(t *testing.T) {
	client, _ := hnSrv(t, map[string]string{
		"/v0/item/1.json": `{"id":1,"type":"comment","by":"bob","time":` + itoa64(ago(2*time.Hour)) + `,"text":"Has <i>italic</i>"}`,
		"/v0/item/2.json": `{"id":2,"deleted":true,"type":"comment","by":"x","text":"gone"}`,
		"/v0/item/3.json": `{"id":3,"dead":true,"type":"comment","by":"x","text":"gone"}`,
		"/v0/item/4.json": `{"id":4,"type":"story","by":"x","text":"not a comment"}`,
		"/v0/item/5.json": `5`,
	})
	tests := []struct {
		name string
		id   int
		want string
		err  bool
	}{
		{"comment", 1, "**bob** (2h ago)\n\nHas *italic*", false},
		{"deleted", 2, "", false},
		{"dead", 3, "", false},
		{"not a comment", 4, "", false},
		{"not JSON object", 5, "", true},
		{"missing item", 6, "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			text, err := fetchHNComment(context.Background(), client, tt.id)
			if tt.err {
				if err == nil {
					t.Fatalf("expected error, got text %q", text)
				}
			} else if err != nil {
				t.Fatalf("error = %v", err)
			}
			if text != tt.want {
				t.Errorf("text = %q, want %q", text, tt.want)
			}
		})
	}
}

func TestDecodeHNText_AllTags(t *testing.T) {
	input := `Hello <p>World</p> <i>it</i> <pre><code>code()</code></pre> <code>x</code> <a href="https://x.com">link</a>`
	want := "Hello \n\nWorld *it* \n```\ncode()\n```\n `x` [link](https://x.com)"
	got := decodeHNText(input)
	if got != want {
		t.Errorf("decodeHNText() = %q, want %q", got, want)
	}
}

func TestDecodeHNText_CollapsesBlankRuns(t *testing.T) {
	got := decodeHNText("a<p></p><p>b</p>")
	want := "a\n\nb"
	if got != want {
		t.Errorf("decodeHNText() = %q, want %q", got, want)
	}
}

func TestReplaceAnchorTags_Forms(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"plain anchor", `<a href="u">t</a>`, "[t](u)"},
		{"surrounded", `x <a href="u">t</a> y`, "x [t](u) y"},
		{"two anchors", `<a href="u1">a</a> <a href="u2">b</a>`, "[a](u1) [b](u2)"},
		{"empty text", `<a href="u"></a>`, "[](u)"},
		// An empty href is still an href; only a missing one is malformed.
		{"empty href", `<a href="">text</a>`, "[text]()"},
		{"no href", `<a >t</a>`, ">t</a>"},
		{"unterminated href", `<a href="u`, `href="u`},
		{"no close tag", `<a href="u">t`, `href="u">t`},
		{"close before open gt", `<a href="u"x</a>>`, `<a href="u"x</a>>`},
		{"no anchor", "plain text", "plain text"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := replaceAnchorTags(tt.in)
			if got != tt.want {
				t.Errorf("replaceAnchorTags(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestFormatRelativeTime_AgeBuckets(t *testing.T) {
	tests := []struct {
		name string
		age  time.Duration
		want string
	}{
		{"half a minute", 30 * time.Second, "0m ago"},
		{"ninety seconds", 90 * time.Second, "1m ago"},
		{"half an hour", 30 * time.Minute, "30m ago"},
		{"fifty nine minutes", 59 * time.Minute, "59m ago"},
		{"five hours", 5 * time.Hour, "5h ago"},
		{"twenty three and a half hours", 23*time.Hour + 30*time.Minute, "23h ago"},
		{"twenty four and a half hours", 24*time.Hour + 30*time.Minute, "1d ago"},
		{"three days", 3 * 24 * time.Hour, "3d ago"},
		{"six and a half days", 6*24*time.Hour + 12*time.Hour, "6d ago"},
		{"six days twenty hours", 6*24*time.Hour + 20*time.Hour, "6d ago"},
		{"seven days one hour", 7*24*time.Hour + time.Hour, ""},
		{"future stamp", -5 * time.Minute, "0m ago"},
		{"seven and a half days", 7*24*time.Hour + 12*time.Hour, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// REAL-TIME: wall-clock fixture; the code under test compares against the system clock.
			ref := time.Now()
			got := formatRelativeTime(ref.Add(-tt.age).Unix())
			if tt.want == "" {
				want := ref.Add(-tt.age).Format("2006-01-02")
				if got != want {
					t.Errorf("formatRelativeTime(%v) = %q, want %q", tt.age, got, want)
				}
				return
			}
			if got != tt.want {
				t.Errorf("formatRelativeTime(%v) = %q, want %q", tt.age, got, tt.want)
			}
		})
	}
}

// A stamp at local midnight is the one instant where a nanosecond shift moves
// the rendered calendar day.
func TestFormatRelativeTime_MidnightStampUsesExactDay(t *testing.T) {
	// REAL-TIME: wall-clock fixture; the code under test compares against the system clock.
	day := time.Now().Add(-9 * 24 * time.Hour)
	midnight := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, time.Local).Unix()
	got := formatRelativeTime(midnight)
	want := time.Unix(midnight, 0).Format("2006-01-02")
	if got != want {
		t.Errorf("formatRelativeTime() = %q, want %q", got, want)
	}
}
