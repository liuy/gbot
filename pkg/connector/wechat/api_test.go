package wechat

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
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

// --- apiPost error paths ---

func TestApiPost_MarshalError(t *testing.T) {
	t.Parallel()
	// channels cannot be marshaled to JSON.
	_, err := apiPost(context.Background(), &http.Client{}, "http://x", EPGetUpdates,
		make(chan int), "tok", 5*time.Second)
	if err == nil || !strings.Contains(err.Error(), "marshal") {
		t.Fatalf("apiPost marshal error: %v, want 'marshal'", err)
	}
}

func TestApiPost_ReadBodyError(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Fatal("server does not support Hijacker")
			return
		}
		conn, _, _ := hj.Hijack()
		_ = conn.Close()
	}))
	defer srv.Close()

	_, err := apiPost(context.Background(), srv.Client(), srv.URL, EPGetUpdates,
		map[string]any{"key": "val"}, "tok", 5*time.Second)
	if err == nil {
		t.Fatal("apiPost read error: expected error, got nil")
	}
}

func TestApiPost_HTTPError_LongBody(t *testing.T) {
	t.Parallel()
	longBody := strings.Repeat("x", 300)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(longBody))
	}))
	defer srv.Close()

	_, err := apiPost(context.Background(), srv.Client(), srv.URL, EPGetUpdates,
		map[string]any{}, "tok", 5*time.Second)
	if err == nil || !strings.Contains(err.Error(), "HTTP 500") {
		t.Fatalf("apiPost HTTP error: %v, want HTTP 500", err)
	}
}

// --- apiGet error paths ---

func TestApiGet_RequestCreationError(t *testing.T) {
	t.Parallel()
	_, err := apiGet(context.Background(), &http.Client{}, "http://[::1", "endpoint", 5*time.Second)
	if err == nil || !strings.Contains(err.Error(), "request") {
		t.Fatalf("apiGet request error: %v, want 'request'", err)
	}
}

func TestApiGet_ReadBodyError(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Fatal("server does not support Hijacker")
			return
		}
		conn, _, _ := hj.Hijack()
		_ = conn.Close()
	}))
	defer srv.Close()

	_, err := apiGet(context.Background(), srv.Client(), srv.URL, "endpoint", 5*time.Second)
	if err == nil {
		t.Fatal("apiGet read error: expected error, got nil")
	}
}

func TestApiGet_HTTPError_LongBody(t *testing.T) {
	t.Parallel()
	longBody := strings.Repeat("x", 300)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(longBody))
	}))
	defer srv.Close()

	_, err := apiGet(context.Background(), srv.Client(), srv.URL, "endpoint", 5*time.Second)
	if err == nil || !strings.Contains(err.Error(), "HTTP 404") {
		t.Fatalf("apiGet HTTP error: %v, want HTTP 404", err)
	}
}

// --- GetUpdates decode error ---

func TestGetUpdates_DecodeError(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{not json`))
	}))
	defer srv.Close()

	_, err := GetUpdates(context.Background(), srv.Client(), srv.URL, "tok", "buf", 5*time.Second)
	if err == nil || !strings.Contains(err.Error(), "decode") {
		t.Fatalf("GetUpdates decode error: %v, want 'decode'", err)
	}
}

// --- SendMessage error paths ---

func TestSendMessage_DecodeError(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{bad json`))
	}))
	defer srv.Close()

	err := SendMessage(context.Background(), srv.Client(), srv.URL, "tok", "bot@im", "user@im", "hi", "", "")
	if err == nil || !strings.Contains(err.Error(), "decode") {
		t.Fatalf("SendMessage decode error: %v, want 'decode'", err)
	}
}

func TestSendMessage_APIError(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	err := SendMessage(context.Background(), srv.Client(), srv.URL, "tok", "bot@im", "user@im", "hi", "", "")
	if err == nil || !strings.Contains(err.Error(), "HTTP 500") {
		t.Fatalf("SendMessage API error: %v, want HTTP 500", err)
	}
}

func TestSendMessage_GenericResponseError(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"ret":-1,"errcode":-1,"errmsg":"something bad"}`))
	}))
	defer srv.Close()

	err := SendMessage(context.Background(), srv.Client(), srv.URL, "tok", "bot@im", "user@im", "hi", "", "")
	if err == nil || !strings.Contains(err.Error(), "ret=-1") {
		t.Fatalf("SendMessage generic error: %v, want 'ret=-1'", err)
	}
}

// --- SendItemMessage error paths ---

func TestSendItemMessage_APIError(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	item := Item{Type: ItemText, TextItem: &TextItem{Text: "test"}}
	err := SendItemMessage(context.Background(), srv.Client(), srv.URL, "tok", "bot@im", "user@im", item, "", "")
	if err == nil || !strings.Contains(err.Error(), "HTTP 500") {
		t.Fatalf("SendItemMessage API error: %v, want HTTP 500", err)
	}
}

func TestSendItemMessage_DecodeError(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{bad json`))
	}))
	defer srv.Close()

	item := Item{Type: ItemText, TextItem: &TextItem{Text: "test"}}
	err := SendItemMessage(context.Background(), srv.Client(), srv.URL, "tok", "bot@im", "user@im", item, "", "")
	if err == nil || !strings.Contains(err.Error(), "decode") {
		t.Fatalf("SendItemMessage decode error: %v, want 'decode'", err)
	}
}

func TestSendItemMessage_SessionExpired(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"ret":-14,"errcode":0}`))
	}))
	defer srv.Close()

	item := Item{Type: ItemText, TextItem: &TextItem{Text: "test"}}
	err := SendItemMessage(context.Background(), srv.Client(), srv.URL, "tok", "bot@im", "user@im", item, "", "")
	if err != ErrSessionExpired {
		t.Fatalf("SendItemMessage session expired: got %v, want ErrSessionExpired", err)
	}
}

func TestSendItemMessage_RateLimited(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"ret":-2,"errcode":0,"errmsg":"too many"}`))
	}))
	defer srv.Close()

	item := Item{Type: ItemText, TextItem: &TextItem{Text: "test"}}
	err := SendItemMessage(context.Background(), srv.Client(), srv.URL, "tok", "bot@im", "user@im", item, "", "")
	if err != ErrRateLimited {
		t.Fatalf("SendItemMessage rate limited: got %v, want ErrRateLimited", err)
	}
}

// --- GetConfig error paths ---

func TestGetConfig_APIError(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	_, err := GetConfig(context.Background(), srv.Client(), srv.URL, "tok", "user@im", "")
	if err == nil || !strings.Contains(err.Error(), "HTTP 500") {
		t.Fatalf("GetConfig API error: %v, want HTTP 500", err)
	}
}

func TestGetConfig_DecodeError(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{bad`))
	}))
	defer srv.Close()

	_, err := GetConfig(context.Background(), srv.Client(), srv.URL, "tok", "user@im", "")
	if err == nil || !strings.Contains(err.Error(), "decode") {
		t.Fatalf("GetConfig decode error: %v, want 'decode'", err)
	}
}

// --- GetBotQR error paths ---

func TestGetBotQR_APIError(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	// GetBotQR uses ILINKBaseURL; redirect via custom transport.
	client := &http.Client{Transport: &loginTestRoundTripper{handler: func(w http.ResponseWriter, r *http.Request) {
		srv.Config.Handler.ServeHTTP(w, r)
	}}}
	_, err := GetBotQR(context.Background(), client, "3")
	if err == nil || !strings.Contains(err.Error(), "HTTP 500") {
		t.Fatalf("GetBotQR API error: %v, want HTTP 500", err)
	}
}

func TestGetBotQR_DecodeError(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{bad`))
	}))
	defer srv.Close()

	client := &http.Client{Transport: &loginTestRoundTripper{handler: func(w http.ResponseWriter, r *http.Request) {
		srv.Config.Handler.ServeHTTP(w, r)
	}}}
	_, err := GetBotQR(context.Background(), client, "3")
	if err == nil || !strings.Contains(err.Error(), "decode") {
		t.Fatalf("GetBotQR decode error: %v, want 'decode'", err)
	}
}

// --- GetQRStatus error paths ---

func TestGetQRStatus_APIError(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	client := &http.Client{Transport: &loginTestRoundTripper{handler: func(w http.ResponseWriter, r *http.Request) {
		srv.Config.Handler.ServeHTTP(w, r)
	}}}
	_, err := GetQRStatus(context.Background(), client, srv.URL, "qr123")
	if err == nil || !strings.Contains(err.Error(), "HTTP 500") {
		t.Fatalf("GetQRStatus API error: %v, want HTTP 500", err)
	}
}

func TestGetQRStatus_DecodeError(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{bad`))
	}))
	defer srv.Close()

	client := &http.Client{Transport: &loginTestRoundTripper{handler: func(w http.ResponseWriter, r *http.Request) {
		srv.Config.Handler.ServeHTTP(w, r)
	}}}
	_, err := GetQRStatus(context.Background(), client, srv.URL, "qr123")
	if err == nil || !strings.Contains(err.Error(), "decode") {
		t.Fatalf("GetQRStatus decode error: %v, want 'decode'", err)
	}
}

// --- GetUpdates non-timeout error ---

func TestGetUpdates_APIError(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	_, err := GetUpdates(context.Background(), srv.Client(), srv.URL, "tok", "buf", 5*time.Second)
	if err == nil || !strings.Contains(err.Error(), "HTTP 500") {
		t.Fatalf("GetUpdates API error: %v, want HTTP 500", err)
	}
}

// --- SendTyping error path ---

func TestSendTyping_APIError(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	err := SendTyping(context.Background(), srv.Client(), srv.URL, "tok", "user@im", "ticket", 1)
	if err == nil || !strings.Contains(err.Error(), "HTTP 500") {
		t.Fatalf("SendTyping API error: %v, want HTTP 500", err)
	}
}

// pinAPIStub is a stub transport that records the request (headers, body,
// context deadline) and replies with a canned response.
type pinAPIStub struct {
	status  int
	body    string
	readErr error
	reqs    []*pinCapturedRequest
}

type pinCapturedRequest struct {
	method   string
	url      string
	header   http.Header
	body     []byte
	deadline time.Time
}

func (tr *pinAPIStub) RoundTrip(r *http.Request) (*http.Response, error) {
	if err := r.Context().Err(); err != nil {
		return nil, &url.Error{Op: "Get", URL: r.URL.String(), Err: err}
	}
	var body []byte
	if r.Body != nil {
		body, _ = io.ReadAll(r.Body)
	}
	dl, _ := r.Context().Deadline()
	tr.reqs = append(tr.reqs, &pinCapturedRequest{
		method:   r.Method,
		url:      r.URL.String(),
		header:   r.Header.Clone(),
		body:     body,
		deadline: dl,
	})
	resp := &http.Response{
		StatusCode: tr.status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       http.NoBody,
	}
	if tr.status == 0 {
		resp.StatusCode = http.StatusOK
	}
	switch {
	case tr.readErr != nil:
		resp.Body = io.NopCloser(pinErrReader{err: tr.readErr})
	default:
		resp.Body = io.NopCloser(strings.NewReader(tr.body))
	}
	return resp, nil
}

type pinErrReader struct{ err error }

func (r pinErrReader) Read([]byte) (int, error) { return 0, r.err }

func pinClient(tr *pinAPIStub) *http.Client { return &http.Client{Transport: tr} }

func (c *pinCapturedRequest) jsonBody(t *testing.T) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(c.body, &m); err != nil {
		t.Fatalf("request body is not JSON: %v (%s)", err, c.body)
	}
	return m
}

// TestPinApiPostHeaders pins the iLink header envelope: every entry in
// buildHeaders is a protocol requirement, and Authorization only appears when
// a token exists.
func TestPinApiPostHeaders(t *testing.T) {
	tr := &pinAPIStub{body: `{"ret":0,"errcode":0}`}
	if err := SendMessage(context.Background(), pinClient(tr), "https://api.example", "tok-1",
		"bot@im.bot", "user-1", "hello", "ctx-1", "client-1"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if len(tr.reqs) != 1 {
		t.Fatalf("requests = %d, want 1", len(tr.reqs))
	}
	req := tr.reqs[0]
	if req.method != http.MethodPost {
		t.Errorf("method = %q, want POST", req.method)
	}
	if req.url != "https://api.example/"+EPSendMessage {
		t.Errorf("url = %q, want %q", req.url, "https://api.example/"+EPSendMessage)
	}
	for name, want := range map[string]string{
		"Content-Type":            "application/json",
		"AuthorizationType":       "ilink_bot_token",
		"Authorization":           "Bearer tok-1",
		"iLink-App-Id":            ILINKAppID,
		"iLink-App-ClientVersion": strconv.Itoa(AppClientVer),
	} {
		if got := req.header.Get(name); got != want {
			t.Errorf("header %s = %q, want %q", name, got, want)
		}
	}
	if got := req.header.Get("Content-Length"); got != strconv.Itoa(len(req.body)) {
		t.Errorf("Content-Length = %q, want %d (the marshalled body length)", got, len(req.body))
	}
	uin := req.header.Get("X-WECHAT-UIN")
	if uin == "" {
		t.Fatal("X-WECHAT-UIN is empty")
	}
	decoded, err := base64.StdEncoding.DecodeString(uin)
	if err != nil {
		t.Fatalf("X-WECHAT-UIN is not base64: %v", err)
	}
	if _, err := strconv.ParseUint(string(decoded), 10, 32); err != nil {
		t.Errorf("X-WECHAT-UIN decodes to %q, want a uint32 decimal string", decoded)
	}
}

// The QR endpoints carry the app headers but no Authorization.
func TestPinApiGetHeaders(t *testing.T) {
	tr := &pinAPIStub{body: `{"qrcode":"QR","qrcode_img_content":"img"}`}
	if _, err := GetBotQR(context.Background(), pinClient(tr), "3"); err != nil {
		t.Fatalf("GetBotQR: %v", err)
	}
	req := tr.reqs[0]
	if req.method != http.MethodGet {
		t.Errorf("method = %q, want GET", req.method)
	}
	if !strings.HasSuffix(req.url, EPGetBotQR+"?bot_type=3") {
		t.Errorf("url = %q, want it to end with %q", req.url, EPGetBotQR+"?bot_type=3")
	}
	if got := req.header.Get("iLink-App-Id"); got != ILINKAppID {
		t.Errorf("iLink-App-Id = %q, want %q", got, ILINKAppID)
	}
	if got := req.header.Get("iLink-App-ClientVersion"); got != strconv.Itoa(AppClientVer) {
		t.Errorf("iLink-App-ClientVersion = %q, want %d", got, AppClientVer)
	}
	if got := req.header.Get("Authorization"); got != "" {
		t.Errorf("Authorization = %q, want it absent for a tokenless GET", got)
	}
}

// The QR request timeout is QRTimeoutMs; a collapsed duration expression makes
// every QR call expire instantly.
func TestPinQRLongTimeoutApplied(t *testing.T) {
	tr := &pinAPIStub{body: `{"qrcode":"QR","qrcode_img_content":"img"}`}
	if _, err := GetBotQR(context.Background(), pinClient(tr), "3"); err != nil {
		t.Fatalf("GetBotQR: %v", err)
	}
	dl := tr.reqs[0].deadline
	if dl.IsZero() {
		t.Fatal("QR request has no context deadline, want QRTimeoutMs")
	}
	if got := time.Until(dl); got < 34*time.Second || got > 35*time.Second {
		t.Errorf("QR deadline in %v, want ~35s", got)
	}
}

// Marshal, transport and body-read failures must keep their cause wrapped.
func TestPinApiPostErrorWraps(t *testing.T) {
	t.Run("marshal", func(t *testing.T) {
		// A payload that cannot marshal exercises the marshal wrap directly.
		_, err := apiPost(context.Background(), pinClient(&pinAPIStub{body: `{}`}),
			"https://api.example", "ep", map[string]any{"ch": make(chan int)}, "tok", time.Second)
		if err == nil {
			t.Fatal("apiPost(unmarshalable payload) = nil error, want failure")
		}
		if !strings.Contains(err.Error(), "ep marshal:") {
			t.Errorf("error = %q, want the marshal prefix", err)
		}
		if _, ok := errors.AsType[*json.UnsupportedTypeError](err); !ok {
			t.Errorf("error = %q, want it to wrap *json.UnsupportedTypeError", err)
		}
	})
	t.Run("request", func(t *testing.T) {
		_, err := apiPost(context.Background(), pinClient(&pinAPIStub{body: `{}`}),
			"http://[::1", "ep", map[string]any{}, "tok", time.Second)
		if err == nil {
			t.Fatal("apiPost(bad url) = nil error, want failure")
		}
		if !strings.Contains(err.Error(), "ep request:") {
			t.Errorf("error = %q, want the request prefix", err)
		}
		if _, ok := errors.AsType[*url.Error](err); !ok {
			t.Errorf("error = %q, want it to wrap the *url.Error from URL parsing", err)
		}
	})
	t.Run("read", func(t *testing.T) {
		pin := errors.New("body read boom")
		_, err := apiPost(context.Background(), pinClient(&pinAPIStub{readErr: pin}),
			"https://api.example", "ep", map[string]any{}, "tok", time.Second)
		if err == nil {
			t.Fatal("apiPost(failing body) = nil error, want failure")
		}
		if !strings.Contains(err.Error(), "ep read:") {
			t.Errorf("error = %q, want the read prefix", err)
		}
		if !errors.Is(err, pin) {
			t.Errorf("error = %q, want it to wrap the read error", err)
		}
	})
	t.Run("get-request", func(t *testing.T) {
		_, err := apiGet(context.Background(), pinClient(&pinAPIStub{body: `{}`}),
			"http://[::1", "ep", time.Second)
		if err == nil {
			t.Fatal("apiGet(bad url) = nil error, want failure")
		}
		if _, ok := errors.AsType[*url.Error](err); !ok {
			t.Errorf("apiGet error = %q, want it to wrap the *url.Error", err)
		}
	})
	t.Run("get-read", func(t *testing.T) {
		pin := errors.New("get body read boom")
		_, err := apiGet(context.Background(), pinClient(&pinAPIStub{readErr: pin}),
			"https://api.example", "ep", time.Second)
		if err == nil {
			t.Fatal("apiGet(failing body) = nil error, want failure")
		}
		if !errors.Is(err, pin) {
			t.Errorf("apiGet error = %q, want it to wrap the read error", err)
		}
	})
	t.Run("get-transport", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := apiGet(ctx, pinClient(&pinAPIStub{body: `{}`}), "https://api.example", "ep", time.Second)
		if err == nil {
			t.Fatal("apiGet(cancelled ctx) = nil error, want failure")
		}
		if !errors.Is(err, context.Canceled) {
			t.Errorf("apiGet error = %q, want it to wrap context.Canceled", err)
		}
	})
}

// A non-200 response body is quoted in the error, truncated at 200 bytes.
func TestPinApiPostPreviewTruncation(t *testing.T) {
	long := strings.Repeat("z", 201)
	tr := &pinAPIStub{status: http.StatusInternalServerError, body: long}
	_, err := apiPost(context.Background(), pinClient(tr), "https://api.example", "ep", map[string]any{}, "tok", time.Second)
	if err == nil {
		t.Fatal("apiPost(500) = nil error, want failure")
	}
	if !strings.Contains(err.Error(), strings.Repeat("z", 200)) {
		t.Errorf("error = %q, want the first 200 bytes of the body", err)
	}
	if strings.Contains(err.Error(), long) {
		t.Errorf("error = %q, want the body truncated to 200 bytes", err)
	}

	trGet := &pinAPIStub{status: http.StatusBadGateway, body: long}
	_, err = apiGet(context.Background(), pinClient(trGet), "https://api.example", "ep", time.Second)
	if err == nil {
		t.Fatal("apiGet(502) = nil error, want failure")
	}
	if !strings.Contains(err.Error(), strings.Repeat("z", 200)) || strings.Contains(err.Error(), long) {
		t.Errorf("apiGet error = %q, want the GET body truncated to 200 bytes", err)
	}
}

// The getupdates envelope carries the sync buffer and base_info; a timed-out
// long poll must come back as an empty, non-error result with Ret 0.
func TestPinGetUpdatesEnvelope(t *testing.T) {
	tr := &pinAPIStub{body: `{"ret":0,"msgs":[],"get_updates_buf":"buf-2"}`}
	resp, err := GetUpdates(context.Background(), pinClient(tr), "https://api.example", "tok", "buf-1", 5*time.Second)
	if err != nil {
		t.Fatalf("GetUpdates: %v", err)
	}
	if resp.GetUpdatesBuf != "buf-2" {
		t.Errorf("GetUpdatesBuf = %q, want buf-2", resp.GetUpdatesBuf)
	}
	body := tr.reqs[0].jsonBody(t)
	if body["get_updates_buf"] != "buf-1" {
		t.Errorf("request get_updates_buf = %v, want buf-1", body["get_updates_buf"])
	}
	info, ok := body["base_info"].(map[string]any)
	if !ok {
		t.Fatalf("request base_info = %v, want an object", body["base_info"])
	}
	if info["channel_version"] != ChannelVersion {
		t.Errorf("base_info.channel_version = %v, want %q", info["channel_version"], ChannelVersion)
	}

	// A deadline-exceeded long poll degrades to an empty result, not an error.
	deadlineTr := &pinDeadlineTransport{}
	resp, err = GetUpdates(context.Background(), &http.Client{Transport: deadlineTr},
		"https://api.example", "tok", "keep-me", 10*time.Millisecond)
	if err != nil {
		t.Fatalf("GetUpdates(timeout) = %v, want the empty fallback", err)
	}
	if resp.Ret != 0 {
		t.Errorf("fallback Ret = %d, want 0", resp.Ret)
	}
	if resp.GetUpdatesBuf != "keep-me" {
		t.Errorf("fallback GetUpdatesBuf = %q, want the original sync buffer", resp.GetUpdatesBuf)
	}
	if resp.Msgs != nil {
		t.Errorf("fallback Msgs = %v, want nil", resp.Msgs)
	}
}

// pinDeadlineTransport blocks until the request context expires.
type pinDeadlineTransport struct{}

func (pinDeadlineTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	<-r.Context().Done()
	return nil, &url.Error{Op: "Get", URL: r.URL.String(), Err: context.DeadlineExceeded}
}

// The sendmessage envelope must carry every outbound field the server expects,
// and an empty context token must stay out of the payload.
func TestPinSendMessageEnvelope(t *testing.T) {
	cases := []struct {
		name         string
		contextToken string
		clientID     string
	}{
		{"with token, explicit client", "ctx-9", "client-9"},
		{"no token, generated client", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tr := &pinAPIStub{body: `{"ret":0,"errcode":0}`}
			if err := SendMessage(context.Background(), pinClient(tr), "https://api.example", "tok",
				"bot@im.bot", "user-1", "the text", tc.contextToken, tc.clientID); err != nil {
				t.Fatalf("SendMessage: %v", err)
			}
			body := tr.reqs[0].jsonBody(t)
			info, ok := body["base_info"].(map[string]any)
			if !ok || info["channel_version"] != ChannelVersion {
				t.Errorf("base_info = %v, want channel_version %q", body["base_info"], ChannelVersion)
			}
			msg, ok := body["msg"].(map[string]any)
			if !ok {
				t.Fatalf("msg = %v, want an object", body["msg"])
			}
			if msg["from_user_id"] != "bot@im.bot" || msg["to_user_id"] != "user-1" {
				t.Errorf("msg ids = %v/%v, want bot@im.bot/user-1", msg["from_user_id"], msg["to_user_id"])
			}
			if msg["message_type"] != float64(MsgTypeBot) || msg["message_state"] != float64(MsgStateFinish) {
				t.Errorf("msg type/state = %v/%v, want %d/%d", msg["message_type"], msg["message_state"],
					MsgTypeBot, MsgStateFinish)
			}
			clientID, _ := msg["client_id"].(string)
			if tc.clientID == "" {
				if !strings.HasPrefix(clientID, "gbot-wechat-") {
					t.Errorf("generated client_id = %q, want the gbot-wechat- prefix", clientID)
				}
			} else if clientID != tc.clientID {
				t.Errorf("client_id = %q, want %q", clientID, tc.clientID)
			}
			items, ok := msg["item_list"].([]any)
			if !ok || len(items) != 1 {
				t.Fatalf("item_list = %v, want one item", msg["item_list"])
			}
			item, _ := items[0].(map[string]any)
			if item["type"] != float64(ItemText) {
				t.Errorf("item type = %v, want %d", item["type"], ItemText)
			}
			textItem, ok := item["text_item"].(map[string]any)
			if !ok || textItem["text"] != "the text" {
				t.Errorf("text_item = %v, want the sent text", item["text_item"])
			}
			if got, exists := msg["context_token"]; tc.contextToken == "" {
				if exists {
					t.Errorf("context_token = %v, want it absent", got)
				}
			} else if got != tc.contextToken {
				t.Errorf("context_token = %v, want %q", got, tc.contextToken)
			}
		})
	}
}

// SendItemMessage keeps the same envelope but carries the caller's item.
func TestPinSendItemMessageEnvelope(t *testing.T) {
	tr := &pinAPIStub{body: `{"ret":0,"errcode":0}`}
	item := Item{Type: ItemImage, ImageItem: &MediaItemHolder{Media: &MediaRef{FullURL: "https://cdn/x", AesKey: "k"}}}
	if err := SendItemMessage(context.Background(), pinClient(tr), "https://api.example", "tok",
		"bot", "user-1", item, "ctx-3", ""); err != nil {
		t.Fatalf("SendItemMessage: %v", err)
	}
	body := tr.reqs[0].jsonBody(t)
	info, ok := body["base_info"].(map[string]any)
	if !ok || info["channel_version"] != ChannelVersion {
		t.Errorf("base_info = %v, want channel_version %q", body["base_info"], ChannelVersion)
	}
	msg, _ := body["msg"].(map[string]any)
	if msg["from_user_id"] != "bot" || msg["to_user_id"] != "user-1" {
		t.Errorf("msg ids = %v/%v, want bot/user-1", msg["from_user_id"], msg["to_user_id"])
	}
	if msg["context_token"] != "ctx-3" {
		t.Errorf("context_token = %v, want ctx-3", msg["context_token"])
	}
	if msg["message_type"] != float64(MsgTypeBot) || msg["message_state"] != float64(MsgStateFinish) {
		t.Errorf("msg type/state = %v/%v, want %d/%d", msg["message_type"], msg["message_state"], MsgTypeBot, MsgStateFinish)
	}
	clientID, _ := msg["client_id"].(string)
	if !strings.HasPrefix(clientID, "gbot-wechat-") {
		t.Errorf("generated client_id = %q, want the gbot-wechat- prefix", clientID)
	}
	items, ok := msg["item_list"].([]any)
	if !ok || len(items) != 1 {
		t.Fatalf("item_list = %v, want the single image item", msg["item_list"])
	}
	got, _ := items[0].(map[string]any)
	if got["type"] != float64(ItemImage) {
		t.Errorf("item type = %v, want %d", got["type"], ItemImage)
	}
	if _, ok := got["image_item"]; !ok {
		t.Errorf("item = %v, want the image_item payload preserved", got)
	}
}

// The typing and config payloads must name the user, ticket and status.
func TestPinTypingAndConfigPayloads(t *testing.T) {
	tr := &pinAPIStub{body: `{}`}
	if err := SendTyping(context.Background(), pinClient(tr), "https://api.example", "tok",
		"user-7", "ticket-7", TypingStop); err != nil {
		t.Fatalf("SendTyping: %v", err)
	}
	body := tr.reqs[0].jsonBody(t)
	if body["ilink_user_id"] != "user-7" || body["typing_ticket"] != "ticket-7" || body["status"] != float64(TypingStop) {
		t.Errorf("sendtyping payload = %v, want user-7/ticket-7/status %d", body, TypingStop)
	}
	if _, ok := body["base_info"]; !ok {
		t.Errorf("sendtyping payload = %v, want base_info", body)
	}

	cfg := &pinAPIStub{body: `{"typing_ticket":"t"}`}
	if _, err := GetConfig(context.Background(), pinClient(cfg), "https://api.example", "tok",
		"user-8", "ctx-8"); err != nil {
		t.Fatalf("GetConfig: %v", err)
	}
	cb := cfg.reqs[0].jsonBody(t)
	if cb["ilink_user_id"] != "user-8" {
		t.Errorf("getconfig ilink_user_id = %v, want user-8", cb["ilink_user_id"])
	}
	if cb["context_token"] != "ctx-8" {
		t.Errorf("getconfig context_token = %v, want ctx-8", cb["context_token"])
	}
	info, ok := cb["base_info"].(map[string]any)
	if !ok || info["channel_version"] != ChannelVersion {
		t.Errorf("getconfig base_info = %v, want channel_version %q", cb["base_info"], ChannelVersion)
	}

	cfg2 := &pinAPIStub{body: `{"typing_ticket":"t"}`}
	if _, err := GetConfig(context.Background(), pinClient(cfg2), "https://api.example", "tok",
		"user-9", ""); err != nil {
		t.Fatalf("GetConfig(no token): %v", err)
	}
	if got, exists := cfg2.reqs[0].jsonBody(t)["context_token"]; exists {
		t.Errorf("getconfig context_token = %v, want it absent for an empty token", got)
	}
}

// Decode failures on every endpoint must stay inspectable through the wrap.
func TestPinApiDecodeWraps(t *testing.T) {
	cases := []struct {
		name string
		call func(client *http.Client) error
	}{
		{"getupdates", func(c *http.Client) error {
			_, err := GetUpdates(context.Background(), c, "https://api.example", "tok", "", time.Second)
			return err
		}},
		{"sendmessage", func(c *http.Client) error {
			return SendMessage(context.Background(), c, "https://api.example", "tok", "b", "u", "hi", "", "cid")
		}},
		{"senditem", func(c *http.Client) error {
			return SendItemMessage(context.Background(), c, "https://api.example", "tok", "b", "u", Item{Type: ItemText}, "", "cid")
		}},
		{"getconfig", func(c *http.Client) error {
			_, err := GetConfig(context.Background(), c, "https://api.example", "tok", "u", "")
			return err
		}},
		{"getbotqr", func(c *http.Client) error { _, err := GetBotQR(context.Background(), c, "3"); return err }},
		{"getqrstatus", func(c *http.Client) error {
			_, err := GetQRStatus(context.Background(), c, "https://api.example", "QR")
			return err
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client := pinClient(&pinAPIStub{body: `{"ret":`})
			err := tc.call(client)
			if err == nil {
				t.Fatal("decode of truncated JSON = nil error, want failure")
			}
			if !strings.Contains(err.Error(), "decode:") {
				t.Errorf("error = %q, want the decode prefix", err)
			}
			if _, ok := errors.AsType[*json.SyntaxError](err); !ok {
				t.Errorf("error = %q, want it to wrap *json.SyntaxError", err)
			}
		})
	}
}
