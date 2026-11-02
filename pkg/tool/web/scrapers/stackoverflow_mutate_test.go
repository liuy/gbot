package scrapers

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Local midnight and the last nanosecond before it are the only instants
// where a one-nanosecond clock shift changes the rendered date.
func localMidnight(y int, m time.Month, d int) int64 {
	return time.Date(y, m, d, 0, 0, 0, 0, time.Local).Unix()
}

func localEndOfDay(y int, m time.Month, d int) int64 {
	return time.Date(y, m, d+1, 0, 0, 0, -1, time.Local).Unix()
}

func localNoon(y int, m time.Month, d int) int64 {
	return time.Date(y, m, d, 12, 0, 0, 0, time.Local).Unix()
}

type seFixture struct {
	question      string
	answers       string
	answersStatus int // non-zero makes only the answers endpoint fail
}

// seSrv serves the two Stack Exchange API endpoints and records the query
// strings, which carry the site parameter.
func seSrv(t *testing.T, fx seFixture) (*http.Client, *[]string) {
	t.Helper()
	queries := new([]string)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*queries = append(*queries, r.URL.Path+"?"+r.URL.RawQuery)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/answers"):
			if fx.answersStatus != 0 {
				w.WriteHeader(fx.answersStatus)
				return
			}
			_, _ = io.WriteString(w, fx.answers)
		case strings.Contains(r.URL.Path, "/questions/"):
			_, _ = io.WriteString(w, fx.question)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return &http.Client{Transport: &redirectTransport{server: srv}}, queries
}

const seQuestionFull = `{"items":[{"title":"How to X?","body":"<p>Body &amp; <b>bold</b></p>","score":42,
	"owner":{"display_name":"alice"},"creation_date":QCREATED,"tags":["go","http"],
	"answer_count":3,"is_answered":true}]}`

const seAnswersFull = `{"items":[
	{"body":"First answer","score":7,"is_accepted":true,"owner":{"display_name":"bob"},"creation_date":A1},
	{"body":"Second answer","score":6,"owner":null,"creation_date":A2},
	{"body":"Third answer","score":5,"owner":{"display_name":""},"creation_date":ANOON},
	{"body":"Fourth answer","score":4,"owner":{"display_name":"dave"},"creation_date":ANOON},
	{"body":"Fifth answer","score":3,"owner":{"display_name":"erin"},"creation_date":ANOON},
	{"body":"Sixth answer must not appear","score":2,"owner":{"display_name":"frank"},"creation_date":ANOON}]}`

func seFill(s string) string {
	s = strings.ReplaceAll(s, "QCREATED", strconv.FormatInt(localMidnight(2024, time.January, 1), 10))
	s = strings.ReplaceAll(s, "A1", strconv.FormatInt(localMidnight(2024, time.January, 3), 10))
	s = strings.ReplaceAll(s, "A2", strconv.FormatInt(localEndOfDay(2024, time.January, 3), 10))
	s = strings.ReplaceAll(s, "ANOON", strconv.FormatInt(localNoon(2024, time.January, 5), 10))
	return s
}

func TestHandleStackOverflow_FullOutput(t *testing.T) {
	client, queries := seSrv(t, seFixture{
		question: seFill(seQuestionFull),
		answers:  seFill(seAnswersFull),
	})

	result, err := HandleStackOverflow(context.Background(), mustParseURL(t, "https://stackoverflow.com/questions/12345/how-to"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	want := "# How to X?\n\n**Score:** 42 · **Answers:** 3 (Yes)\n**Tags:** go, http\n" +
		"**Asked by:** alice · 2024-01-01\n\n---\n\n## Question\n\nBody & bold\n\n---\n\n## Answers\n\n" +
		"### Score: 7 (Accepted) · by bob · 2024-01-03\n\nFirst answer\n\n---\n\n" +
		"### Score: 6 · by anonymous · 2024-01-03\n\nSecond answer\n\n---\n\n" +
		"### Score: 5 · by anonymous · 2024-01-05\n\nThird answer\n\n---\n\n" +
		"### Score: 4 · by dave · 2024-01-05\n\nFourth answer\n\n---\n\n" +
		"### Score: 3 · by erin · 2024-01-05\n\nFifth answer\n\n---\n\n"
	if result.Content != want {
		t.Errorf("Content = %q, want %q", result.Content, want)
	}
	if result.ContentType != "text/markdown" {
		t.Errorf("ContentType = %q, want text/markdown", result.ContentType)
	}
	if result.Method != "stackexchange" {
		t.Errorf("Method = %q, want stackexchange", result.Method)
	}
	if len(result.Notes) != 1 || result.Notes[0] != "Fetched via Stack Exchange API (site: stackoverflow)" {
		t.Errorf("Notes = %q", result.Notes)
	}
	wantQuery := "order=desc&sort=votes&site=stackoverflow&filter=withbody"
	if len(*queries) != 2 {
		t.Fatalf("requests = %q, want question then answers", *queries)
	}
	for _, q := range *queries {
		if !strings.HasSuffix(q, "?"+wantQuery) {
			t.Errorf("query = %q, want suffix %q", q, wantQuery)
		}
	}
}

func TestHandleStackOverflow_MinimalQuestion(t *testing.T) {
	client, _ := seSrv(t, seFixture{
		question: `{"items":[{"title":"Bare","body":"","score":0,"owner":null,"creation_date":` +
			strconv.FormatInt(localNoon(2024, time.January, 5), 10) + `,"tags":[],"answer_count":0,"is_answered":false}]}`,
		answers: `{"items":[]}`,
	})

	result, err := HandleStackOverflow(context.Background(), mustParseURL(t, "https://stackoverflow.com/questions/1"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	want := "# Bare\n\n**Score:** 0 · **Answers:** 0 (No)\n**Asked by:** anonymous · 2024-01-05\n\n" +
		"---\n\n## Question\n\n\n\n---\n\n## Answers\n\n"
	if result.Content != want {
		t.Errorf("Content = %q, want %q", result.Content, want)
	}
}

func TestHandleStackOverflow_SingleTag(t *testing.T) {
	client, _ := seSrv(t, seFixture{
		question: `{"items":[{"title":"T","body":"b","score":1,"owner":{"display_name":"a"},"creation_date":` +
			strconv.FormatInt(localNoon(2024, time.January, 5), 10) + `,"tags":["go"],"answer_count":0,"is_answered":true}]}`,
		answers: `{"items":[]}`,
	})
	result, err := HandleStackOverflow(context.Background(), mustParseURL(t, "https://stackoverflow.com/questions/1"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	if !strings.Contains(result.Content, "**Tags:** go\n") {
		t.Errorf("missing single-tag line in %q", result.Content)
	}
	if strings.Contains(result.Content, "**Tags:** go,") {
		t.Errorf("unexpected tag separator in %q", result.Content)
	}
}

// Every mapped host must reach the API with its own site parameter.
func TestHandleStackOverflow_SiteMapping(t *testing.T) {
	question := `{"items":[{"title":"T","body":"b","score":1,"creation_date":0,"tags":[],"answer_count":0}]}`
	for host, site := range map[string]string{
		"stackoverflow.com":      "stackoverflow",
		"superuser.com":          "superuser",
		"serverfault.com":        "serverfault",
		"askubuntu.com":          "askubuntu",
		"mathoverflow.net":       "mathoverflow",
		"stackapps.com":          "stackapps",
		"www.stackoverflow.com":  "stackoverflow",
		"unix.stackexchange.com": "unix",
	} {
		t.Run(host, func(t *testing.T) {
			client, queries := seSrv(t, seFixture{question: question, answers: `{"items":[]}`})
			result, err := HandleStackOverflow(context.Background(), mustParseURL(t, "https://"+host+"/questions/7"), client, nil)
			if err != nil {
				t.Fatalf("error = %v", err)
			}
			if result == nil {
				t.Fatal("expected result, got nil")
			}
			if len(*queries) != 2 {
				t.Fatalf("requests = %q", *queries)
			}
			want := "order=desc&sort=votes&site=" + site + "&filter=withbody"
			if !strings.HasSuffix((*queries)[0], "?"+want) {
				t.Errorf("query = %q, want suffix %q", (*queries)[0], want)
			}
			if len(result.Notes) != 1 || result.Notes[0] != "Fetched via Stack Exchange API (site: "+site+")" {
				t.Errorf("Notes = %q", result.Notes)
			}
		})
	}
}

func TestHandleStackOverflow_QuestionTimestampBoundaries(t *testing.T) {
	tests := []struct {
		name    string
		created int64
		want    string
	}{
		{"local_midnight", localMidnight(2024, time.January, 1), "2024-01-01"},
		{"last_nanosecond_of_day", localEndOfDay(2024, time.January, 1), "2024-01-01"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client, _ := seSrv(t, seFixture{
				question: `{"items":[{"title":"T","body":"b","score":1,"creation_date":` +
					strconv.FormatInt(tt.created, 10) + `,"tags":[],"answer_count":0}]}`,
				answers: `{"items":[]}`,
			})
			result, err := HandleStackOverflow(context.Background(), mustParseURL(t, "https://stackoverflow.com/questions/1"), client, nil)
			if err != nil {
				t.Fatalf("error = %v", err)
			}
			if result == nil {
				t.Fatal("expected result, got nil")
			}
			wantLine := "**Asked by:** anonymous · " + tt.want + "\n"
			if !strings.Contains(result.Content, wantLine) {
				t.Errorf("missing %q in %q", wantLine, result.Content)
			}
		})
	}
}

// A deleted user keeps the owner object but empties display_name.
func TestHandleStackOverflow_QuestionOwnerWithoutDisplayName(t *testing.T) {
	client, _ := seSrv(t, seFixture{
		question: `{"items":[{"title":"T","body":"b","score":1,"owner":{"display_name":""},"creation_date":` +
			strconv.FormatInt(localNoon(2024, time.January, 5), 10) + `,"tags":[],"answer_count":0}]}`,
		answers: `{"items":[]}`,
	})
	result, err := HandleStackOverflow(context.Background(), mustParseURL(t, "https://stackoverflow.com/questions/1"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	want := "# T\n\n**Score:** 1 · **Answers:** 0 (No)\n**Asked by:** anonymous · 2024-01-05\n\n" +
		"---\n\n## Question\n\nb\n\n---\n\n## Answers\n\n"
	if result.Content != want {
		t.Errorf("Content = %q, want %q", result.Content, want)
	}
}

func TestHandleStackOverflow_RejectedHostsAndPaths(t *testing.T) {
	question := `{"items":[{"title":"T","body":"b","score":1,"creation_date":0}]}`
	for _, raw := range []string{
		"https://example.com/questions/7",
		"https://stackexchange.com/questions/7",
		"https://foo.bar.stackexchange.com/questions/7",
		"https://.stackexchange.com/questions/7",
		"https://stackoverflow.com/questions",
		"https://stackoverflow.com/questions/",
		"https://stackoverflow.com/questions/abc",
		"https://stackoverflow.com/topics/go",
	} {
		client, _ := seSrv(t, seFixture{question: question, answers: `{"items":[]}`})
		result, err := HandleStackOverflow(context.Background(), mustParseURL(t, raw), client, nil)
		if err != nil {
			t.Errorf("%s: error = %v", raw, err)
		}
		if result != nil {
			t.Errorf("%s: expected nil, got %+v", raw, result)
		}
	}
}

func TestHandleStackOverflow_NoItemsReturnsNil(t *testing.T) {
	client, _ := seSrv(t, seFixture{question: `{"items":[]}`, answers: `{"items":[]}`})
	result, err := HandleStackOverflow(context.Background(), mustParseURL(t, "https://stackoverflow.com/questions/7"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result != nil {
		t.Errorf("expected nil for empty items, got %+v", result)
	}
}

func TestHandleStackOverflow_QuestionFetchError(t *testing.T) {
	sentinel := errors.New("se unreachable")
	client := &http.Client{Transport: errTransport{err: sentinel}}
	result, err := HandleStackOverflow(context.Background(), mustParseURL(t, "https://stackoverflow.com/questions/7"), client, nil)
	if result != nil {
		t.Errorf("expected nil result, got %+v", result)
	}
	if err == nil {
		t.Fatal("expected error")
	}
	if err.Error() != `fetch question: Get "https://api.stackexchange.com/2.3/questions/7?order=desc&sort=votes&site=stackoverflow&filter=withbody": se unreachable` {
		t.Errorf("error = %q", err.Error())
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("error %v does not unwrap to the transport error", err)
	}
}

func TestHandleStackOverflow_QuestionParseError(t *testing.T) {
	client, _ := seSrv(t, seFixture{question: `5`, answers: `{"items":[]}`})
	_, err := HandleStackOverflow(context.Background(), mustParseURL(t, "https://stackoverflow.com/questions/7"), client, nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.HasPrefix(err.Error(), "parse question: ") {
		t.Errorf("error = %q, want prefix %q", err.Error(), "parse question: ")
	}
	if _, ok := errors.AsType[*json.UnmarshalTypeError](err); !ok {
		t.Errorf("error %v does not unwrap to *json.UnmarshalTypeError", err)
	}
}

// The answers endpoint is fetched after the question, so its failure is a
// distinct error from the question path.
func TestHandleStackOverflow_AnswersFetchErrorPropagates(t *testing.T) {
	client, _ := seSrv(t, seFixture{
		question:      `{"items":[{"title":"T","body":"b","score":1,"creation_date":0}]}`,
		answers:       ``,
		answersStatus: http.StatusServiceUnavailable,
	})
	result, err := HandleStackOverflow(context.Background(), mustParseURL(t, "https://stackoverflow.com/questions/7"), client, nil)
	if result != nil {
		t.Errorf("expected nil result, got %+v", result)
	}
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.HasPrefix(err.Error(), `fetch answers: HTTP 503 from https://api.stackexchange.com/2.3/questions/7/answers`) {
		t.Errorf("error = %q, want prefix fetch answers HTTP 503", err.Error())
	}
	if errors.Unwrap(err) == nil {
		t.Errorf("error %v does not wrap the underlying fetch failure", err)
	}
}

func TestHandleStackOverflow_AnswersParseError(t *testing.T) {
	client, _ := seSrv(t, seFixture{
		question: `{"items":[{"title":"T","body":"b","score":1,"creation_date":0}]}`,
		answers:  `5`,
	})
	_, err := HandleStackOverflow(context.Background(), mustParseURL(t, "https://stackoverflow.com/questions/7"), client, nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.HasPrefix(err.Error(), "parse answers: ") {
		t.Errorf("error = %q, want prefix %q", err.Error(), "parse answers: ")
	}
	if _, ok := errors.AsType[*json.UnmarshalTypeError](err); !ok {
		t.Errorf("error %v does not unwrap to *json.UnmarshalTypeError", err)
	}
}
