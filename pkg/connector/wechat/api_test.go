package wechat

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestApiPost_Success(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			t.Errorf("Authorization = %q, want 'Bearer tok'", r.Header.Get("Authorization"))
		}
		_, _ = w.Write([]byte(`{"ret":0}`))
	}))
	defer srv.Close()

	raw, err := apiPost(context.Background(), srv.Client(), srv.URL, EPGetUpdates,
		map[string]any{"key": "val"}, "tok", 5*time.Second)
	if err != nil {
		t.Fatalf("apiPost: %v", err)
	}
	if !strings.Contains(string(raw), "ret") {
		t.Errorf("apiPost returned %q, want JSON with ret", string(raw))
	}
}

func TestApiPost_HTTP500(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	_, err := apiPost(context.Background(), srv.Client(), srv.URL, EPGetUpdates,
		map[string]any{}, "tok", 5*time.Second)
	if err == nil || !strings.Contains(err.Error(), "HTTP 500") {
		t.Fatalf("apiPost error = %v, want HTTP 500", err)
	}
}

func TestApiPost_MalformedURL(t *testing.T) {
	t.Parallel()
	_, err := apiPost(context.Background(), &http.Client{}, "http://[::1",
		EPGetUpdates, map[string]any{}, "tok", 5*time.Second)
	if err == nil {
		t.Fatal("apiPost malformed URL: expected error, got nil")
	}
}

func TestApiGet_Success(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("iLink-App-Id") == "" {
			t.Error("missing iLink-App-Id header")
		}
		_, _ = w.Write([]byte(`{"ret":0,"data":"ok"}`))
	}))
	defer srv.Close()

	raw, err := apiGet(context.Background(), srv.Client(), srv.URL, "endpoint", 5*time.Second)
	if err != nil {
		t.Fatalf("apiGet: %v", err)
	}
	if !strings.Contains(string(raw), "ok") {
		t.Errorf("apiGet = %q, want 'ok'", string(raw))
	}
}

func TestApiGet_HTTPError(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	_, err := apiGet(context.Background(), srv.Client(), srv.URL, "endpoint", 5*time.Second)
	if err == nil || !strings.Contains(err.Error(), "HTTP 404") {
		t.Fatalf("apiGet error = %v, want HTTP 404", err)
	}
}

func TestDecodeResponse_InvalidJSON(t *testing.T) {
	t.Parallel()
	var v map[string]any
	err := decodeResponse(json.RawMessage(`{bad`), &v)
	if err == nil || !strings.Contains(err.Error(), "invalid") {
		t.Fatalf("decodeResponse error = %v, want 'invalid'", err)
	}
}

func TestGetUpdates_Success(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"ret":0,"get_updates_buf":"next","msgs":[]}`))
	}))
	defer srv.Close()

	resp, err := GetUpdates(context.Background(), srv.Client(), srv.URL, "tok", "buf", 5*time.Second)
	if err != nil {
		t.Fatalf("GetUpdates: %v", err)
	}
	if resp.GetUpdatesBuf != "next" {
		t.Errorf("GetUpdatesBuf = %q, want 'next'", resp.GetUpdatesBuf)
	}
}

func TestGetUpdates_TimeoutReturnsOriginalBuf(t *testing.T) {
	t.Parallel()
	hang := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-hang
	}))
	defer srv.Close()
	defer close(hang)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	resp, err := GetUpdates(ctx, srv.Client(), srv.URL, "tok", "original-buf", 50*time.Millisecond)
	if err != nil {
		t.Fatalf("GetUpdates timeout: %v", err)
	}
	if resp.GetUpdatesBuf != "original-buf" {
		t.Errorf("timeout GetUpdatesBuf = %q, want 'original-buf'", resp.GetUpdatesBuf)
	}
}

// TestGetUpdates_BackgroundCtx_TimeoutFallback verifies that GetUpdates
// returns the fallback response when only the internal apiPost timeout fires
// (parent ctx is Background with no deadline). This is the real pollLoop
// scenario where ctx is long-lived and timeout is controlled by the timeout
// parameter.
func TestGetUpdates_BackgroundCtx_TimeoutFallback(t *testing.T) {
	t.Parallel()
	hang := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-hang
	}))
	defer srv.Close()
	defer close(hang)

	// No deadline on parent ctx — simulates pollLoop's long-lived context.
	resp, err := GetUpdates(context.Background(), srv.Client(), srv.URL, "tok", "orig", 50*time.Millisecond)
	if err != nil {
		t.Fatalf("GetUpdates background timeout: %v", err)
	}
	if resp.GetUpdatesBuf != "orig" {
		t.Errorf("timeout GetUpdatesBuf = %q, want 'orig'", resp.GetUpdatesBuf)
	}
}

func TestSendMessage_Success(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"ret":0,"errcode":0}`))
	}))
	defer srv.Close()

	err := SendMessage(context.Background(), srv.Client(), srv.URL, "tok", "bot@im", "user@im", "hi", "", "")
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
}

func TestSendMessage_EmptyText(t *testing.T) {
	t.Parallel()
	err := SendMessage(context.Background(), &http.Client{}, "", "", "", "", "  ", "", "")
	if err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("SendMessage empty: error = %v, want 'empty'", err)
	}
}

func TestSendMessage_SessionExpired(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"ret":-14,"errcode":0}`))
	}))
	defer srv.Close()

	err := SendMessage(context.Background(), srv.Client(), srv.URL, "tok", "bot@im", "user@im", "hi", "", "")
	if err != ErrSessionExpired {
		t.Errorf("SendMessage session expired: got %v, want ErrSessionExpired", err)
	}
}

func TestSendMessage_RateLimited(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"ret":-2,"errcode":0,"errmsg":"too many"}`))
	}))
	defer srv.Close()

	err := SendMessage(context.Background(), srv.Client(), srv.URL, "tok", "bot@im", "user@im", "hi", "", "")
	if err != ErrRateLimited {
		t.Errorf("SendMessage rate limited: got %v, want ErrRateLimited", err)
	}
}

func TestSendItemMessage_Success(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"ret":0,"errcode":0}`))
	}))
	defer srv.Close()

	item := Item{Type: ItemText, TextItem: &TextItem{Text: "test"}}
	err := SendItemMessage(context.Background(), srv.Client(), srv.URL, "tok", "bot@im", "user@im", item, "", "")
	if err != nil {
		t.Fatalf("SendItemMessage: %v", err)
	}
}

func TestSendItemMessage_GenericError(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"ret":42,"errcode":0,"errmsg":"bad"}`))
	}))
	defer srv.Close()

	item := Item{Type: ItemText, TextItem: &TextItem{Text: "test"}}
	err := SendItemMessage(context.Background(), srv.Client(), srv.URL, "tok", "bot@im", "user@im", item, "", "")
	if err == nil || !strings.Contains(err.Error(), "ret=42") {
		t.Fatalf("SendItemMessage: error = %v, want ret=42", err)
	}
}

func TestSendTyping_Success(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"ret":0}`))
	}))
	defer srv.Close()

	err := SendTyping(context.Background(), srv.Client(), srv.URL, "tok", "user@im", "ticket", 1)
	if err != nil {
		t.Fatalf("SendTyping: %v", err)
	}
}

func TestGetConfig_Success(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"ret":0,"typing_ticket":"tk123"}`))
	}))
	defer srv.Close()

	resp, err := GetConfig(context.Background(), srv.Client(), srv.URL, "tok", "user@im", "")
	if err != nil {
		t.Fatalf("GetConfig: %v", err)
	}
	if resp.TypingTicket != "tk123" {
		t.Errorf("TypingTicket = %q, want tk123", resp.TypingTicket)
	}
}
