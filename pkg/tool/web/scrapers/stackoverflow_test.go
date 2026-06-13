package scrapers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandleStackOverflow_NoMatch(t *testing.T) {
	result, _ := HandleStackOverflow(context.Background(), mustParseURL(t, "https://github.com/questions/123"), nil, nil)
	if result != nil {
		t.Errorf("expected nil for non-SE host, got %+v", result)
	}
}

func TestHandleStackOverflow_NonQuestionPath(t *testing.T) {
	result, _ := HandleStackOverflow(context.Background(), mustParseURL(t, "https://stackoverflow.com/users/123"), nil, nil)
	if result != nil {
		t.Errorf("expected nil for non-/questions/ path, got %+v", result)
	}
}

func TestHandleStackOverflow_NonNumericID(t *testing.T) {
	result, _ := HandleStackOverflow(context.Background(), mustParseURL(t, "https://stackoverflow.com/questions/abc/foo"), nil, nil)
	if result != nil {
		t.Errorf("expected nil for non-numeric ID, got %+v", result)
	}
}

func TestHandleStackOverflow_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/questions/123/answers") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"items":[{"body":"<p>Use map[int]int</p>","score":5,"is_accepted":true,"owner":{"display_name":"alice"},"creation_date":1700000000}]}`))
			return
		}
		if strings.Contains(r.URL.Path, "/questions/123") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"items":[{"title":"How to use maps","body":"<p>I need help</p>","score":3,"owner":{"display_name":"bob"},"creation_date":1699000000,"tags":["go","map"],"answer_count":1,"is_answered":true}]}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	client := &http.Client{Transport: &rewriteTransport{server: srv, host: "api.stackexchange.com"}}
	result, err := HandleStackOverflow(context.Background(), mustParseURL(t, "https://stackoverflow.com/questions/123/how-to-use-maps"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	if !strings.Contains(result.Content, "How to use maps") {
		t.Errorf("expected title, got: %q", result.Content)
	}
	if !strings.Contains(result.Content, "Use map[int]int") {
		t.Errorf("expected answer body, got: %q", result.Content)
	}
	if !strings.Contains(result.Content, "(Accepted)") {
		t.Errorf("expected (Accepted) marker, got: %q", result.Content)
	}
	if result.Method != "stackexchange" {
		t.Errorf("Method = %q, want stackexchange", result.Method)
	}
}

func TestHandleStackOverflow_StackExchangeSubdomain(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[{"title":"Question","body":"<p>body</p>","score":1,"owner":{"display_name":"x"},"creation_date":1700000000,"tags":[],"answer_count":0,"is_answered":false}],"items":[{"body":"<p>ans</p>","score":0,"is_accepted":false,"owner":{"display_name":"y"},"creation_date":1700000001}]}`))
	}))
	defer srv.Close()

	client := &http.Client{Transport: &rewriteTransport{server: srv, host: "api.stackexchange.com"}}
	result, err := HandleStackOverflow(context.Background(), mustParseURL(t, "https://unix.stackexchange.com/questions/456/q"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	if !strings.Contains(result.Content, "Question") {
		t.Errorf("expected content, got: %q", result.Content)
	}
}

func TestHandleStackOverflow_EmptyItems(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[]}`))
	}))
	defer srv.Close()

	client := &http.Client{Transport: &rewriteTransport{server: srv, host: "api.stackexchange.com"}}
	result, err := HandleStackOverflow(context.Background(), mustParseURL(t, "https://stackoverflow.com/questions/999/empty"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result != nil {
		t.Errorf("expected nil for empty items, got %+v", result)
	}
}

func TestHandleStackOverflow_WWWPrefix(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[{"title":"WWW","body":"<p>b</p>","score":1,"owner":{"display_name":"u"},"creation_date":1700000000,"tags":[],"answer_count":0,"is_answered":false}]}`))
	}))
	defer srv.Close()

	client := &http.Client{Transport: &rewriteTransport{server: srv, host: "api.stackexchange.com"}}
	result, err := HandleStackOverflow(context.Background(), mustParseURL(t, "https://www.stackoverflow.com/questions/1/title"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	if !strings.Contains(result.Content, "WWW") {
		t.Errorf("expected title, got: %q", result.Content)
	}
}

func TestHandleStackOverflow_NestedSubdomain(t *testing.T) {
	result, _ := HandleStackOverflow(context.Background(), mustParseURL(t, "https://foo.bar.stackexchange.com/questions/1/q"), nil, nil)
	if result != nil {
		t.Errorf("expected nil for nested subdomain, got %+v", result)
	}
}

func TestHandleStackOverflow_EmptyQuestionID(t *testing.T) {
	result, _ := HandleStackOverflow(context.Background(), mustParseURL(t, "https://stackoverflow.com/questions//title"), nil, nil)
	if result != nil {
		t.Errorf("expected nil for empty question ID, got %+v", result)
	}
}

func TestHandleStackOverflow_DeletedUser(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "/answers") {
			_, _ = w.Write([]byte(`{"items":[]}`))
			return
		}
		_, _ = w.Write([]byte(`{"items":[{"title":"Deleted User Q","body":"<p>q</p>","score":5,"creation_date":1700000000,"tags":["go"],"answer_count":0,"is_answered":false}]}`))
	}))
	defer srv.Close()

	client := &http.Client{Transport: &rewriteTransport{server: srv, host: "api.stackexchange.com"}}
	result, err := HandleStackOverflow(context.Background(), mustParseURL(t, "https://stackoverflow.com/questions/5/q"), client, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	if !strings.Contains(result.Content, "anonymous") {
		t.Errorf("expected anonymous user, got: %q", result.Content)
	}
}

func TestHandleStackOverflow_AnswersFetchError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/answers") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[{"title":"No Answers","body":"<p>b</p>","score":1,"owner":{"display_name":"u"},"creation_date":1700000000,"tags":[],"answer_count":0,"is_answered":false}]}`))
	}))
	defer srv.Close()

	client := &http.Client{Transport: &rewriteTransport{server: srv, host: "api.stackexchange.com"}}
	_, err := HandleStackOverflow(context.Background(), mustParseURL(t, "https://stackoverflow.com/questions/10/no-answers"), client, nil)
	if err == nil {
		t.Fatal("expected error for 404 on answers")
	}
	if !strings.Contains(err.Error(), "fetch answers") {
		t.Errorf("error should mention fetch answers, got: %v", err)
	}
}
